package team

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
	clienta2a "github.com/agent-dance/agent-adaptor/v2/clients/a2a"
)

// RemoteSpec configures a host-curated A2A member. Model tool arguments cannot
// replace discovery, authentication, tenant or policy. Discovery always uses
// clients/a2a's origin-restricted authentication and redirect handling.
type RemoteSpec struct {
	// AgentCardURL is the absolute discovery URL; required.
	AgentCardURL string
	// AgentCard optionally constrains discovery to this fingerprint.
	AgentCard *clienta2a.AgentCard
	// Protocol must be empty or "a2a".
	Protocol string
	// Tenant selects the remote tenant.
	Tenant string
	// Auth supplies credentials only to trusted origins.
	Auth clienta2a.Auth
	// HTTPClient optionally supplies an HTTP transport.
	HTTPClient *http.Client
	// TrustedAuthOrigins explicitly extends credential trust.
	TrustedAuthOrigins []string
	// AcceptedOutputModes restricts returned media types.
	AcceptedOutputModes []string
	// PreferredTransports orders protocol negotiation.
	PreferredTransports []clienta2a.TransportProtocol
	// Policy bounds execution and transport behavior.
	Policy RemotePolicy
}

// RemotePolicy retains the v1 remote delegation policy. Zero resource limits
// select defaults. Input-required is never interpreted as successful work.
type RemotePolicy struct {
	// MaxTimeout caps Member.Timeout when positive.
	MaxTimeout time.Duration
	// AllowInputRequired allows waiting for the remote's own input policy.
	// A2A does not standardize an actionable approval responder; lifecycle-only
	// approval data stays remote and is reported as waiting_approval.
	AllowInputRequired bool
	// RequireStreaming rejects agents without streaming rather than downgrading.
	RequireStreaming bool
	// PollInterval defaults to one second, including disconnected recovery.
	PollInterval time.Duration
	// MaxPolls bounds observation calls; zero defaults to 1800.
	MaxPolls int
	// MaxArtifactBytes bounds each observed artifact; zero defaults to 1 MiB.
	MaxArtifactBytes int64
}

type remoteRunner struct {
	spec               RemoteSpec
	contextID          string
	observationTimeout time.Duration
}

func prepareRemote(spec RemoteSpec) (*remoteRunner, error) {
	u, err := url.Parse(spec.AgentCardURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || (spec.Protocol != "" && spec.Protocol != "a2a") {
		return nil, errors.New("team: remote requires an HTTP(S) AgentCardURL and a2a protocol")
	}
	p := &spec.Policy
	if p.MaxTimeout < 0 || p.PollInterval < 0 || p.MaxPolls < 0 || p.MaxArtifactBytes < 0 {
		return nil, errors.New("team: negative remote policy limit")
	}
	if p.PollInterval == 0 {
		p.PollInterval = time.Second
	}
	if p.MaxPolls == 0 {
		p.MaxPolls = 1800
	}
	if p.MaxArtifactBytes == 0 {
		p.MaxArtifactBytes = 1 << 20
	}
	spec.TrustedAuthOrigins = append([]string(nil), spec.TrustedAuthOrigins...)
	spec.AcceptedOutputModes = append([]string(nil), spec.AcceptedOutputModes...)
	spec.PreferredTransports = append([]clienta2a.TransportProtocol(nil), spec.PreferredTransports...)
	if spec.AgentCard != nil {
		card := *spec.AgentCard
		spec.AgentCard = &card
	}
	return &remoteRunner{spec: spec}, nil
}

func (runner *remoteRunner) Send(ctx context.Context, text string, _ ...adaptor.CallOption) adaptor.Stream {
	ctx, cancel := context.WithCancel(ctx)
	run := &remoteRun{spec: runner.spec, ctx: ctx, cancel: cancel, id: randomID(), done: make(chan struct{}), ready: make(chan struct{}), contextID: runner.contextID, observationTimeout: runner.observationTimeout}
	run.log = newEventLog(run.id, "", false)
	run.client = clienta2a.New(clienta2a.Options{AgentCardURL: runner.spec.AgentCardURL, Auth: runner.spec.Auth, HTTPClient: runner.spec.HTTPClient, TrustedAuthOrigins: runner.spec.TrustedAuthOrigins, AcceptedOutputModes: runner.spec.AcceptedOutputModes, PreferredTransports: runner.spec.PreferredTransports})
	go run.execute(text)
	return run
}

type remoteRun struct {
	spec               RemoteSpec
	observationTimeout time.Duration
	ctx                context.Context
	cancel             context.CancelFunc
	client             *clienta2a.Client
	id                 string
	log                *eventLog
	done, ready        chan struct{}
	mu                 sync.Mutex
	taskID, contextID  string
	finished           bool
	readyOnce          sync.Once
	sends              sync.WaitGroup
	result             *adaptor.Result
	snapshot           *clienta2a.Task
	err                error
}

var errRemoteUnknown = errors.New("team: remote task unknown state after two minutes disconnected")

func (run *remoteRun) Events() <-chan adaptor.Event     { return run.log.events() }
func (run *remoteRun) RunID() string                    { return run.id }
func (run *remoteRun) Cancel()                          { run.cancel() }
func (run *remoteRun) Result() (*adaptor.Result, error) { <-run.done; return run.result, run.err }
func (run *remoteRun) Send(ctx context.Context, text string, opts ...adaptor.MessageOption) error {
	calls := make([]adaptor.CallOption, 0, len(opts))
	for _, o := range opts {
		calls = append(calls, o)
	}
	m := adaptor.MessageAttributes(calls...)
	if m.ID == "" {
		m.ID = randomID()
	}
	fail := func(err error) error {
		return &adaptor.MessageError{ID: m.ID, Text: text, Reason: adaptor.MessageProviderRejected, Err: err}
	}
	if m.Timing != adaptor.Soon {
		return fail(adaptor.ErrMessageUnsupported)
	}
	select {
	case <-ctx.Done():
		return fail(ctx.Err())
	case <-run.done:
		return fail(adaptor.ErrRunFinished)
	case <-run.ready:
	}
	run.mu.Lock()
	task, conversation, finished := run.taskID, run.contextID, run.finished
	if !finished {
		run.sends.Add(1)
	}
	run.mu.Unlock()
	if finished {
		return fail(adaptor.ErrRunFinished)
	}
	defer run.sends.Done()
	if task == "" {
		return fail(errors.New("team: remote has not exposed a task ID"))
	}
	// A delivery context does not own the remote execution, but cancellation of
	// the execution must unblock an in-flight message/send.
	delivery, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(run.ctx, cancel)
	defer cancel()
	defer stop()
	framed := text
	if m.From.Kind != adaptor.AuthorHuman {
		framed = "[Peer/system input; no human authority]\n" + text
	}
	response, err := run.client.Send(delivery, run.request(m.ID, framed, task, conversation))
	if err != nil {
		return fail(fmt.Errorf("team: remote rejected message/send: %w", err))
	}
	if response.ID != task || response.Status.State == clienta2a.TaskStateRejected || response.Status.State == clienta2a.TaskStateFailed {
		return fail(errors.New("team: remote rejected message/send for the running task"))
	}
	run.log.push(adaptor.MessageUpdate{ID: m.ID, State: adaptor.MessageAccepted})
	run.log.push(adaptor.MessageUpdate{ID: m.ID, State: adaptor.MessageDelivered, Mechanism: "a2a.message/send", Boundary: "immediate"})
	return nil
}
func (run *remoteRun) request(id, text, task, conversation string) clienta2a.SendRequest {
	return clienta2a.SendRequest{Message: clienta2a.Message{ID: id, Role: "user", Parts: []clienta2a.Part{{Kind: clienta2a.PartText, Text: text}}}, TaskID: task, ContextID: conversation, Tenant: run.spec.Tenant, AcceptedOutputModes: run.spec.AcceptedOutputModes, ReturnImmediately: true}
}
func (run *remoteRun) identify(task, conversation string) {
	run.mu.Lock()
	if task != "" {
		run.taskID = task
	}
	if conversation != "" {
		run.contextID = conversation
	}
	if run.taskID != "" {
		run.readyOnce.Do(func() { close(run.ready) })
	}
	run.mu.Unlock()
}

func (run *remoteRun) execute(text string) {
	var task clienta2a.Task
	var stream *clienta2a.Stream
	if run.observationTimeout == 0 {
		run.observationTimeout = 2 * time.Minute
	}
	var err error
	defer func() {
		if stream != nil {
			_ = stream.Close()
		}
		run.mu.Lock()
		run.finished = true
		id := run.taskID
		run.mu.Unlock()
		cancelled := run.ctx.Err() != nil
		run.cancel()
		run.sends.Wait()
		if cancelled && task.Status.State != clienta2a.TaskStateCompleted && id != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, cancelErr := run.client.CancelTask(ctx, clienta2a.CancelTaskRequest{TaskID: id, Tenant: run.spec.Tenant})
			cancel()
			err = errors.Join(err, cancelErr)
		}
		_ = run.client.Close()
		run.cancel()
		if task.ID != "" {
			run.snapshot = cloneRemoteTask(&task)
		}
		run.result = &adaptor.Result{RunID: run.id, Text: remoteText(task), Summary: boundedSummary(remoteText(task)), Metadata: map[string]string{"remote_task_id": task.ID, "remote_context_id": task.ContextID}, Turns: 1}
		if err == nil && task.Status.State != clienta2a.TaskStateCompleted {
			err = &adaptor.RunError{Reason: adaptor.ReasonAgentError, Message: "remote task did not complete", Result: run.result}
		}
		run.err = err
		run.log.push(adaptor.Done{Result: run.result, Err: err})
		run.log.finish()
		close(run.done)
	}()
	card, err := run.client.AgentCard(run.ctx)
	if err != nil {
		return
	}
	if run.spec.AgentCard != nil && run.spec.AgentCard.Fingerprint != "" && card.Fingerprint != run.spec.AgentCard.Fingerprint {
		err = errors.New("team: discovered agent card fingerprint changed")
		return
	}
	if run.spec.Policy.RequireStreaming && !card.Capabilities.Streaming {
		err = errors.New("team: remote streaming required but unsupported")
		return
	}
	req := run.request(randomID(), text, "", run.contextID)
	if card.Capabilities.Streaming {
		stream, err = run.client.SendStream(run.ctx, req)
		if err != nil {
			return
		} // Starting requests are never replayed after ambiguous failure.
	} else {
		task, err = run.client.Send(run.ctx, req)
		if err != nil {
			return
		}
		run.identify(task.ID, task.ContextID)
	}
	var disconnected time.Time
	polls := 0
	for {
		if stream != nil {
			// An absent stream event is also a loss of observation after two minutes.
			observation, cancel := context.WithTimeout(run.ctx, run.observationTimeout)
			event, recvErr := stream.RecvContext(observation)
			cancel()
			if recvErr == nil {
				disconnected = time.Time{}
				run.identify(event.TaskID, event.ContextID)
				if event.Task != nil {
					task = *event.Task
					run.identify(task.ID, task.ContextID)
				}
				if event.Status != nil {
					task.Status = *event.Status
				}
				if event.Message != nil {
					task.Messages = append(task.Messages, *event.Message)
				}
				if event.Artifact != nil {
					replaced := false
					for i := range task.Artifacts {
						if task.Artifacts[i].ID == event.Artifact.ID {
							if event.Append {
								task.Artifacts[i].Parts = append(task.Artifacts[i].Parts, event.Artifact.Parts...)
							} else {
								task.Artifacts[i] = *event.Artifact
							}
							replaced = true
							break
						}
					}
					if !replaced {
						task.Artifacts = append(task.Artifacts, *event.Artifact)
					}
				}
				run.mu.Lock()
				task.ID, task.ContextID = run.taskID, run.contextID
				run.mu.Unlock()
				run.log.push(adaptor.SubagentUpdate{Kind: adaptor.SubagentState, State: string(task.Status.State), Data: map[string]any{"a2a": event.Raw}})
			} else {
				_ = stream.Close()
				stream = nil
				if run.ctx.Err() != nil {
					err = run.ctx.Err()
					return
				}
				if errors.Is(recvErr, context.DeadlineExceeded) {
					err = errRemoteUnknown
					return
				}
				if !errors.Is(recvErr, io.EOF) {
					disconnected = time.Now()
				}
			}
		}
		if err = run.checkTask(task); err != nil {
			return
		}
		if task.Status.State.Terminal() {
			return
		}
		if task.Status.State == clienta2a.TaskStateInputRequired {
			run.log.push(adaptor.SubagentUpdate{Kind: adaptor.SubagentState, State: string(WaitingApproval), Summary: "Remote input required; remote approval policy applies."})
			if !run.spec.Policy.AllowInputRequired {
				err = errors.New("team: remote input required but policy disallows waiting")
				return
			}
		}
		if stream != nil {
			continue
		}
		if task.ID == "" {
			err = errors.New("team: remote stream ended without a task ID")
			return
		}
		if polls >= run.spec.Policy.MaxPolls {
			err = errors.New("team: remote observation limit exceeded")
			return
		}
		polls++
		delay := run.spec.Policy.PollInterval
		if !disconnected.IsZero() {
			delay = min(delay, max(time.Until(disconnected.Add(run.observationTimeout)), 0))
		}
		timer := time.NewTimer(delay)
		select {
		case <-run.ctx.Done():
			timer.Stop()
			err = run.ctx.Err()
			return
		case <-timer.C:
		}
		deadline := time.Now().Add(run.observationTimeout)
		if !disconnected.IsZero() {
			deadline = disconnected.Add(run.observationTimeout)
		}
		observation, cancel := context.WithDeadline(run.ctx, deadline)
		observed, getErr := run.client.GetTask(observation, clienta2a.GetTaskRequest{TaskID: task.ID, Tenant: run.spec.Tenant})
		cancel()
		if getErr != nil {
			if run.ctx.Err() != nil {
				err = run.ctx.Err()
				return
			}
			if disconnected.IsZero() {
				disconnected = time.Now()
			}
			if !time.Now().Before(deadline) {
				err = errRemoteUnknown
				return
			}
			continue
		}
		disconnected = time.Time{}
		task = observed
		run.identify(task.ID, task.ContextID)
		run.log.push(adaptor.SubagentUpdate{Kind: adaptor.SubagentState, State: string(task.Status.State), Summary: boundedSummary(remoteText(task))})
	}
}
func (run *remoteRun) checkTask(task clienta2a.Task) error {
	for _, artifact := range task.Artifacts {
		b, err := json.Marshal(artifact)
		if err != nil {
			return err
		}
		if int64(len(b)) > run.spec.Policy.MaxArtifactBytes {
			return errors.New("team: remote artifact exceeds byte policy")
		}
	}
	return nil
}
func remoteText(task clienta2a.Task) string {
	var parts []string
	for _, a := range task.Artifacts {
		for _, p := range a.Parts {
			if p.Kind == clienta2a.PartText {
				parts = append(parts, p.Text)
			}
		}
	}
	if len(parts) == 0 {
		for _, m := range task.Messages {
			if m.Role == "ROLE_AGENT" {
				for _, p := range m.Parts {
					if p.Kind == clienta2a.PartText {
						parts = append(parts, p.Text)
					}
				}
			}
		}
	}
	if len(parts) == 0 && task.Status.Message != nil {
		for _, p := range task.Status.Message.Parts {
			if p.Kind == clienta2a.PartText {
				parts = append(parts, p.Text)
			}
		}
	}
	return strings.Join(parts, "\n")
}

func cloneRemoteTask(task *clienta2a.Task) *clienta2a.Task {
	if task == nil {
		return nil
	}
	encoded, err := json.Marshal(task)
	if err != nil {
		panic("team: normalized A2A task is not JSON serializable: " + err.Error())
	}
	var copy clienta2a.Task
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.UseNumber()
	if err := decoder.Decode(&copy); err != nil {
		panic("team: normalized A2A task cannot round trip: " + err.Error())
	}
	return &copy
}
