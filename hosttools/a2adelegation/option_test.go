package a2adelegation_test

// P4.7 end-to-end: delegation.Service.Option() on the main agent path.
//
// The difference from service_s9_test.go is the whole point of this file. There
// the host wired the sidecar by hand (the driver called EnsureSidecar itself)
// and the events arrived through subagentstream.Merge. Here the host writes one
// thing —
//
//	leader := adaptor.New(drv, team.Option())
//
// — and everything else is the SDK's: the sidecar is started before the driver
// launches, published to it as a typed MCP server with the bearer token in
// SecretEnv, the delegation events land on the leader's own event channel, and
// the sidecar is gone by the time Result() returns. The fake leader driver
// therefore takes its endpoint strictly from driver.Request, exactly as a real
// CLI driver reading its mcp_servers config would; if the SDK failed to publish
// it, the driver cannot delegate at all.
//
// No sleeps: every hand-off is a channel or an HTTP round trip, so the file is
// -count=5 stable.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/hosttools/a2adelegation"
	adaptor "github.com/agent-dance/agent-adaptor"
)

// requestDrivenLeader is the fake leader driver.Driver for the Option() path.
// It never touches the Service: the sidecar URL comes from req.MCP (the typed
// MCP server the SDK assembled) and the bearer token from req.Runtime.SecretEnv
// (the subprocess-only secret channel), which is exactly the information a real
// driver has.
type requestDrivenLeader struct {
	calls  []leaderCall
	output string

	mu       sync.Mutex
	requests []driver.Request
	preCall  func() // rendezvous hook, invoked before the first delegation
}

func (d *requestDrivenLeader) Descriptor() driver.Descriptor {
	return driver.Descriptor{
		Type:        "fake-leader",
		DisplayName: "fake leader",
		MCP:         driver.MCPCapability{Supported: true, Stdio: true, HTTP: true, SSE: true},
	}
}

func (d *requestDrivenLeader) ValidateConfig(any) error { return nil }

func (d *requestDrivenLeader) Run(ctx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
	d.mu.Lock()
	d.requests = append(d.requests, req)
	d.mu.Unlock()

	sc, err := sidecarFromRequest(req)
	if err != nil {
		return driver.Response{}, err
	}
	_ = sink.EmitStream(driver.StreamPayload{Kind: driver.StreamTextStart, RunID: req.RunID, MessageID: "leader-m1"})
	for i, call := range d.calls {
		if i == 0 && d.preCall != nil {
			d.preCall()
		}
		result, err := callDelegateTool(ctx, sc, call.agent, call.objective)
		if err != nil {
			return driver.Response{}, err
		}
		if result.Status != "completed" {
			return driver.Response{Failure: &driver.RunFailure{Message: fmt.Sprintf("delegation to %s ended %s", call.agent, result.Status)}}, nil
		}
		_ = sink.EmitStream(driver.StreamPayload{Kind: driver.StreamTextContent, RunID: req.RunID, MessageID: "leader-m1", Delta: call.agent + " done. "})
	}
	_ = sink.EmitStream(driver.StreamPayload{Kind: driver.StreamTextEnd, RunID: req.RunID, MessageID: "leader-m1"})
	return driver.Response{Output: d.output}, nil
}

func (d *requestDrivenLeader) lastRequest(t *testing.T) driver.Request {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.requests) == 0 {
		t.Fatal("driver was never invoked")
	}
	return d.requests[len(d.requests)-1]
}

// sidecarFromRequest reconstructs the connection details from the driver
// request alone. It is the assertion that matters most in this file: every
// field a driver needs to reach the delegation endpoint must be present in
// driver.Request without the driver knowing the a2adelegation package.
func sidecarFromRequest(req driver.Request) (a2adelegation.Sidecar, error) {
	var server driver.MCPServerSpec
	found := false
	for _, candidate := range req.MCP.Servers {
		if candidate.Key == a2adelegation.ServiceKey {
			server, found = candidate, true
		}
	}
	if !found {
		return a2adelegation.Sidecar{}, fmt.Errorf("no %q MCP server in the request (servers %+v)", a2adelegation.ServiceKey, req.MCP.Servers)
	}
	if server.Transport != driver.MCPTransportHTTP {
		return a2adelegation.Sidecar{}, fmt.Errorf("sidecar transport = %q, want http", server.Transport)
	}
	token := ""
	for _, binding := range req.Runtime.SecretEnv {
		if binding.Name == server.BearerTokenEnvVar {
			token = binding.Value
		}
	}
	if token == "" {
		return a2adelegation.Sidecar{}, fmt.Errorf("no %q binding in Runtime.SecretEnv", server.BearerTokenEnvVar)
	}
	return a2adelegation.Sidecar{RunID: req.RunID, URL: server.URL, BearerToken: token}, nil
}

func newTeamOfThree(t *testing.T, observe func(a2adelegation.Event)) *a2adelegation.Service {
	t.Helper()
	planner := adaptor.New(&scriptedRoleDriver{kind: "fake-plan", deltas: []string{"drafting ", "the plan"}, final: "PLAN_READY"})
	implementer := adaptor.New(&scriptedRoleDriver{kind: "fake-impl", deltas: []string{"implementing"}, final: "IMPL_DONE"})
	reviewer := adaptor.New(&scriptedRoleDriver{kind: "fake-review", deltas: []string{"reviewing"}, final: "TEAM_REVIEW_APPROVED"})

	team, err := a2adelegation.NewService(a2adelegation.Config{
		Agents: []a2adelegation.AgentRef{
			a2adelegation.Local("plan", planner, a2adelegation.Policy{MaxTimeout: time.Minute}),
			a2adelegation.Local("impl", implementer, a2adelegation.Policy{MaxTimeout: time.Minute}),
			a2adelegation.Local("review", reviewer, a2adelegation.Policy{MaxTimeout: time.Minute}),
		},
		ToolTimeout: time.Minute,
		Observe:     observe,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = team.Close() })
	return team
}

// TestServiceOptionEndToEnd is the §9.7 headline: adaptor.New(drv,
// team.Option()) and nothing else.
func TestServiceOptionEndToEnd(t *testing.T) {
	team := newTeamOfThree(t, nil)
	drv := &requestDrivenLeader{
		calls: []leaderCall{
			{agent: "plan", objective: "draft the plan"},
			{agent: "impl", objective: "implement the plan"},
			{agent: "review", objective: "review the implementation"},
		},
		output: "team workflow complete",
	}
	leader := adaptor.New(drv, team.Option())

	stream := leader.Stream(context.Background(), "coordinate the team")
	runID := stream.RunID()

	var leaderText strings.Builder
	perAgent := map[string][]adaptor.SubagentUpdate{}
	var agentOrder []string
	for ev := range stream.Events() {
		switch e := ev.(type) {
		case adaptor.TextDelta:
			leaderText.WriteString(e.Text)
		case adaptor.SubagentUpdate:
			if len(perAgent[e.Agent]) == 0 {
				agentOrder = append(agentOrder, e.Agent)
			}
			perAgent[e.Agent] = append(perAgent[e.Agent], e)
		}
	}

	res, err := stream.Result()
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	if res.Text != "team workflow complete" {
		t.Errorf("Result.Text = %q, want the leader's output", res.Text)
	}
	if got := leaderText.String(); got != "plan done. impl done. review done. " {
		t.Errorf("leader text = %q, want all three delegations to have succeeded", got)
	}

	// 1. Team progress arrived on the leader's own channel — no Merge, no
	// second stream, no host-side plumbing.
	if want := []string{"plan", "impl", "review"}; fmt.Sprint(agentOrder) != fmt.Sprint(want) {
		t.Fatalf("subagent order = %v, want %v", agentOrder, want)
	}
	for _, agent := range agentOrder {
		updates := perAgent[agent]
		if len(updates) < 3 {
			t.Fatalf("agent %q: %d updates, want at least started/delta/finished", agent, len(updates))
		}
		if updates[0].Kind != adaptor.SubagentStarted {
			t.Errorf("agent %q: first kind = %q, want started", agent, updates[0].Kind)
		}
		// 2. The terminal update is not clipped by teardown: the run's
		// event channel closed only after the sources were drained.
		last := updates[len(updates)-1]
		if last.Kind != adaptor.SubagentFinished {
			t.Errorf("agent %q: last kind = %q, want the terminal update to survive teardown", agent, last.Kind)
		}
		if got := last.Data["kind"]; got != string(a2adelegation.DelegationFinished) {
			t.Errorf("agent %q: last Data[kind] = %v, want %q", agent, got, a2adelegation.DelegationFinished)
		}
	}

	// 3. Results are recorded per run and outlive the run.
	review, ok := team.Result(runID, "review")
	if !ok {
		t.Fatalf("team.Result(%q, review): not recorded", runID)
	}
	if !review.HasLine("TEAM_REVIEW_APPROVED") {
		t.Errorf("review.HasLine(TEAM_REVIEW_APPROVED) = false, result %+v", review)
	}
	if got := len(team.Results(runID)); got != 3 {
		t.Errorf("team.Results(%q) = %d entries, want 3", runID, got)
	}

	// 4. The published declaration is typed, not stringly metadata.
	req := drv.lastRequest(t)
	var server driver.MCPServerSpec
	for _, candidate := range req.MCP.Servers {
		if candidate.Key == a2adelegation.ServiceKey {
			server = candidate
		}
	}
	if server.Transport != driver.MCPTransportHTTP || !strings.HasPrefix(server.URL, "http://127.0.0.1:") {
		t.Errorf("sidecar server = %+v, want a loopback Streamable-HTTP endpoint", server)
	}
	if server.BearerTokenEnvVar != a2adelegation.BearerTokenEnvVar {
		t.Errorf("BearerTokenEnvVar = %q, want %q", server.BearerTokenEnvVar, a2adelegation.BearerTokenEnvVar)
	}
	if !server.Required || !strings.Contains(server.RequiredReason, a2adelegation.DelegateToolName) {
		t.Errorf("Required=%v reason=%q, want a required server naming the delegation tool", server.Required, server.RequiredReason)
	}
	if len(req.Runtime.Ensured) != 1 || req.Runtime.Ensured[0].ID != a2adelegation.ServiceKey {
		t.Fatalf("Runtime.Ensured = %+v, want the sidecar runtime service", req.Runtime.Ensured)
	}
	ref := req.Runtime.Ensured[0]
	if ref.Lifecycle != driver.RuntimeLifecycleEphemeral {
		t.Errorf("ref Lifecycle = %q, want ephemeral (per-run sidecar)", ref.Lifecycle)
	}
	if ref.Metadata["delegation.tool"] != a2adelegation.DelegateToolName {
		t.Errorf("ref metadata = %v, want the delegation tool name", ref.Metadata)
	}
	if ref.Metadata["delegation.tool_timeout"] != time.Minute.String() {
		t.Errorf("delegation.tool_timeout = %q, want %q", ref.Metadata["delegation.tool_timeout"], time.Minute)
	}
	// The token reached the driver through SecretEnv and nowhere else.
	if len(ref.SecretEnv) != 0 {
		t.Errorf("ref.SecretEnv = %+v, want secrets carried only by the payload-level slice", ref.SecretEnv)
	}
	var token string
	for _, binding := range req.Runtime.SecretEnv {
		if binding.Name == a2adelegation.BearerTokenEnvVar {
			token = binding.Value
		}
	}
	if token == "" {
		t.Fatal("no bearer token in Runtime.SecretEnv")
	}
	for _, report := range res.Services() {
		if strings.Contains(fmt.Sprint(report), token) {
			t.Error("the bearer token leaked into a public service report")
		}
	}

	// 5. Result() returning means the sidecar is already released: the
	// endpoint no longer answers.
	if err := probeSidecar(server.URL, token); err == nil {
		t.Error("the sidecar still answers after Result(); DetachRun must have torn it down")
	}
}

// probeSidecar reports whether the endpoint is still reachable.
func probeSidecar(url, token string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

// TestServiceOptionOnRunScope: the same option at the call site equips one
// invocation, and passing it in both scopes attaches once (a second attach
// would collide on the MCP key and fail the run).
func TestServiceOptionOnRunScope(t *testing.T) {
	team := newTeamOfThree(t, nil)
	drv := &requestDrivenLeader{calls: []leaderCall{{agent: "plan", objective: "draft"}}, output: "ok"}

	// Call scope only.
	bare := adaptor.New(drv)
	if _, err := bare.Run(context.Background(), "go", team.Option()); err != nil {
		t.Fatalf("Run with a call-scoped team option: %v", err)
	}

	// Both scopes: idempotent, not doubled.
	both := adaptor.New(drv, team.Option())
	res, err := both.Run(context.Background(), "go", team.Option())
	if err != nil {
		t.Fatalf("Run with the option in both scopes: %v", err)
	}
	req := drv.lastRequest(t)
	sidecars := 0
	for _, server := range req.MCP.Servers {
		if server.Key == a2adelegation.ServiceKey {
			sidecars++
		}
	}
	if sidecars != 1 {
		t.Fatalf("%d sidecar MCP servers, want exactly 1: the same Service must attach once", sidecars)
	}
	if len(req.Runtime.Ensured) != 1 {
		t.Fatalf("Runtime.Ensured = %+v, want a single attachment", req.Runtime.Ensured)
	}
	if _, ok := team.Result(res.RunID, "plan"); !ok {
		t.Errorf("team.Result(%q, plan): not recorded", res.RunID)
	}

	// Agents that never received the option are untouched by it.
	plain := adaptor.New(&requestDrivenLeader{output: "solo"})
	if _, err := plain.Run(context.Background(), "go"); err == nil {
		t.Error("a leader without the team option must not find a delegation endpoint")
	}
}

// TestServiceOptionAttachFailureIsPreLaunch: a Service that can no longer mint
// sidecars fails the run before the driver starts, rather than letting a leader
// silently run solo.
func TestServiceOptionAttachFailureIsPreLaunch(t *testing.T) {
	team := newTeamOfThree(t, nil)
	if err := team.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	drv := &requestDrivenLeader{calls: []leaderCall{{agent: "plan", objective: "draft"}}, output: "ok"}
	leader := adaptor.New(drv, team.Option())

	res, err := leader.Run(context.Background(), "go")
	if err == nil {
		t.Fatal("a closed Service must fail the run at attach time")
	}
	if res != nil {
		t.Errorf("Result = %+v, want nil", res)
	}
	var runErr *adaptor.RunError
	if errors.As(err, &runErr) {
		t.Errorf("err = %v, want a pre-launch infrastructure error, not a business *RunError", err)
	}
	drv.mu.Lock()
	invoked := len(drv.requests)
	drv.mu.Unlock()
	if invoked != 0 {
		t.Errorf("driver invoked %d time(s), want 0", invoked)
	}
}

// TestServiceOptionEventsInterleaveWithLeaderOutput pins the ordering property
// the injection point exists for: subagent updates are folded into the leader's
// channel as they happen, not batched at the end.
func TestServiceOptionEventsInterleaveWithLeaderOutput(t *testing.T) {
	team := newTeamOfThree(t, nil)
	started := make(chan struct{})
	drv := &requestDrivenLeader{
		calls: []leaderCall{
			{agent: "plan", objective: "draft"},
			{agent: "review", objective: "review"},
		},
		output:  "done",
		preCall: func() { close(started) },
	}
	leader := adaptor.New(drv, team.Option())

	stream := leader.Stream(context.Background(), "go")
	<-started

	var order []string
	for ev := range stream.Events() {
		switch e := ev.(type) {
		case adaptor.TextDelta:
			if e.Text != "" {
				order = append(order, "leader:"+strings.TrimSpace(e.Text))
			}
		case adaptor.SubagentUpdate:
			if e.Kind != adaptor.SubagentDelta {
				order = append(order, string(e.Kind)+":"+e.Agent)
			}
		}
	}
	if _, err := stream.Result(); err != nil {
		t.Fatalf("Result: %v", err)
	}

	// The leader emits "plan done." only after the plan delegation returned,
	// and the plan delegation's terminal event is published before the tool
	// call returns — so the sequence below is a property of the pipeline, not
	// a scheduling coincidence.
	want := []string{
		"started:plan",
		"finished:plan",
		"leader:plan done.",
		"started:review",
		"finished:review",
		"leader:review done.",
	}
	if fmt.Sprint(order) != fmt.Sprint(want) {
		t.Fatalf("event order =\n  %v\nwant\n  %v", order, want)
	}
}
