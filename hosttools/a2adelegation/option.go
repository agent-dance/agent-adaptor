package a2adelegation

// Option() is the one-liner that turns a delegation Service into a leader
// agent's team (design doc §9.7):
//
//	team, err := delegation.NewService(delegation.Config{Agents: specs, ToolTimeout: t})
//	defer team.Close()
//	leader := adaptor.New(claude.Driver(cfg), team.Option())
//
//	stream := leader.Stream(ctx, objective)
//	for ev := range stream.Events() {
//	    switch e := ev.(type) {
//	    case adaptor.TextDelta:      // the leader's own output
//	    case adaptor.SubagentUpdate: // team progress, same channel
//	    }
//	}
//	review, ok := team.Result(stream.RunID(), "review")
//
// One option covers all three halves of what the hand-written host runtime
// used to do: declaring the per-run MCP sidecar as a runtime service, keeping
// its lifecycle bound to the run, and folding delegation events into the
// leader's single event stream. No adaptor.WithTeam() exists and none is
// wanted — the root package does not know the word "team" (§9.8). Option
// implements the root package's generic RunServiceProvider extension point, so
// this is the ecosystem-extension path any host tool can take.

import (
	"context"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/mcp"
	adaptor "github.com/agent-dance/agent-adaptor"
)

const (
	// ServiceKey is the runtime-service ID/name and MCP server key under
	// which the per-run delegation sidecar is published to the driver. A
	// host WithMCP declaration using the same key collides on purpose:
	// two different servers cannot share one key, and the run fails before
	// launch rather than silently picking one.
	ServiceKey = "delegate-a2a"

	// BearerTokenEnvVar names the environment variable through which the
	// sidecar's per-run bearer token reaches the driver process. The token
	// travels as ServiceRef.SecretEnv, which the SDK injects into driver
	// env only — it never enters runtime service reports, request metadata,
	// or the serialized runtime-services payload.
	BearerTokenEnvVar = "AGENT_ADAPTOR_DELEGATION_TOKEN"
)

// Option returns the adaptor option that binds this Service to every run of
// the agent it is passed to. It is a dual-scope option: in adaptor.New it is
// the agent's team for every run, in Run/Stream it equips a single
// invocation. Passing the same Service's option in both places attaches it
// once, not twice.
//
// The Service must outlive the runs it equips; Close it when the host is done
// (recorded results stay readable afterwards).
func (s *Service) Option() adaptor.SharedOption {
	return runServiceOption{provider: s}
}

// runServiceOption is the delegation package's own option type. It exists to
// demonstrate the intended shape of an ecosystem option: adaptor.Option is an
// interface, so a package outside the SDK can issue one without the root
// package's vocabulary growing a single entry.
type runServiceOption struct {
	provider adaptor.RunServiceProvider
}

func (o runServiceOption) ApplyNew(settings *adaptor.AgentSettings) {
	settings.AddRunServiceProvider(o.provider)
}

func (o runServiceOption) ApplyRun(settings *adaptor.RunSettings) {
	settings.AddRunServiceProvider(o.provider)
}

var _ adaptor.RunServiceProvider = (*Service)(nil)

// AttachRun starts (or reuses) this run's MCP sidecar and publishes it as a
// runtime service carrying a typed MCP declaration, plus the delegation event
// source for the run. It is the RunServiceProvider half of Option().
//
// A failure here is a pre-launch failure: the leader driver never starts,
// which is the correct outcome — a leader whose delegate_to_agent endpoint
// does not exist would silently run as a solo agent.
func (s *Service) AttachRun(_ context.Context, runID string) (adaptor.RunAttachment, error) {
	sidecar, err := s.EnsureSidecar(runID)
	if err != nil {
		return adaptor.RunAttachment{}, err
	}
	return adaptor.RunAttachment{
		Services: []adaptor.ServiceRef{sidecarServiceRef(sidecar)},
		Events:   s.runEvents,
	}, nil
}

// DetachRun shuts the run's sidecar down, stops its observer, and clears the
// run's bus state — ReleaseRun, reached through the SDK's teardown instead of
// a host-written defer. Recorded results survive, so team.Result(runID, key)
// still answers after the run ends.
func (s *Service) DetachRun(_ context.Context, runID string) error {
	return s.ReleaseRun(runID)
}

// sidecarServiceRef projects one live sidecar onto the runtime-service shape
// the driver consumes. Transport, URL, auth, and requiredness use the typed
// MCP contract; no stringly metadata parsing participates in execution.
func sidecarServiceRef(sidecar Sidecar) adaptor.ServiceRef {
	server := mcp.HTTP(ServiceKey, sidecar.URL,
		mcp.WithBearerTokenEnv(BearerTokenEnvVar),
		mcp.Required("a2a subagent delegation ("+DelegateToolName+")"),
	)
	metadata := map[string]string{
		"delegation.tool":   DelegateToolName,
		"delegation.run_id": sidecar.RunID,
	}
	if sidecar.ToolTimeout > 0 {
		// Surfaced as metadata rather than a typed field: the SDK has no
		// per-tool timeout knob, and the Service already enforces this
		// budget itself (Config.ToolTimeout becomes the delegation
		// policy's MaxTimeout). Drivers that can align their own tool
		// timeout read it here; nothing depends on them doing so.
		metadata["delegation.tool_timeout"] = sidecar.ToolTimeout.String()
	}
	return adaptor.ServiceRef{
		ID:        ServiceKey,
		Name:      ServiceKey,
		URL:       sidecar.URL,
		Status:    driver.RuntimeServiceRunning,
		Lifecycle: driver.RuntimeLifecycleEphemeral,
		Health:    driver.RuntimeHealthHealthy,
		MCP:       &server,
		Metadata:  metadata,
		SecretEnv: []driver.EnvBinding{{Name: BearerTokenEnvVar, Value: sidecar.BearerToken}},
	}
}

// runEvents is the attachment's Event source: every delegation event
// published for the run is projected onto adaptor.SubagentUpdate and delivered
// on a channel the SDK folds into the leader's own event stream.
//
// Cancellation contract (the SDK drains this channel to closure before it
// closes the run's event channel): when ctx ends, whatever the bus has already
// delivered is flushed before the channel closes. That is what keeps a
// terminal SubagentUpdate from being clipped by teardown — delegation
// terminals are published while the delegate_to_agent tool call is still in
// flight, so they are in the subscription buffer by the time the leader's
// driver returns.
func (s *Service) runEvents(ctx context.Context, runID string) <-chan adaptor.Event {
	out := make(chan adaptor.Event, subscriberBuffer)
	source := s.Bus().SubscribeRun(ctx, runID)
	go func() {
		defer close(out)
		for {
			select {
			case ev, ok := <-source:
				if !ok {
					return
				}
				out <- SubagentEvent(ev)
			case <-ctx.Done():
				// Flush, then close. Sends stay blocking on purpose:
				// the SDK's pump only stops when out closes, so it
				// cannot deadlock, and a non-blocking send here would
				// drop exactly the terminal events that matter.
				for {
					select {
					case ev, ok := <-source:
						if !ok {
							return
						}
						out <- SubagentEvent(ev)
					default:
						return
					}
				}
			}
		}
	}()
	return out
}

// SubagentEvent projects one DelegationEvent onto the next-gen event
// vocabulary. The 19 DelegationEventKinds collapse onto the three
// SubagentUpdate kinds (started / delta / finished); the full detail —
// original kind, status, remote identifiers, tool payloads, errors — is
// preserved in Data so nothing is lost in the projection.
//
// It lives here rather than in the bridge package because both consumers need
// it and only one direction of import is legal: bridges/subagentstream imports
// this package (and forwards to this function), while this package cannot
// import the bridge.
func SubagentEvent(ev DelegationEvent) adaptor.SubagentUpdate {
	update := adaptor.SubagentUpdate{
		Agent: ev.AgentKey,
		Kind:  adaptor.SubagentDelta,
		Delta: ev.Delta,
	}
	switch {
	case ev.Kind == DelegationStarted:
		update.Kind = adaptor.SubagentStarted
	case isTerminal(ev.Kind):
		update.Kind = adaptor.SubagentFinished
	}

	data := map[string]any{
		"kind":                string(ev.Kind),
		"status":              ev.Status,
		"agent_name":          ev.AgentName,
		"delegation_id":       ev.DelegationID,
		"parent_tool_call_id": ev.ParentToolCallID,
		"remote_protocol":     ev.Protocol,
		"remote_task_id":      ev.RemoteTaskID,
		"remote_context_id":   ev.RemoteContextID,
		"remote_message_id":   ev.RemoteMessageID,
		"remote_artifact_id":  ev.RemoteArtifactID,
		"remote_tool_call_id": ev.RemoteToolCallID,
		"tool_name":           ev.ToolName,
		"name":                ev.Name,
		"role":                ev.Role,
		"text":                ev.Text,
		"args":                ev.Args,
		"result":              ev.Result,
	}
	if ev.Sequence != 0 {
		data["sequence"] = ev.Sequence
	}
	if ev.Artifact != nil {
		data["artifact"] = ev.Artifact
	}
	if ev.Error != nil {
		data["error"] = ev.Error
	}
	if !ev.Time.IsZero() {
		data["time"] = ev.Time
	}
	for key, val := range data {
		if val == "" || val == nil {
			delete(data, key)
		}
	}
	update.Data = data
	return update
}
