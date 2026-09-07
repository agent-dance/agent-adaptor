package a2a

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	a2aproto "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/capability"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/todo"
)

type alignmentDemandDriver struct{ requests []driver.Request }

func (*alignmentDemandDriver) Descriptor() driver.Descriptor {
	return driver.Descriptor{Type: "alignment-a2a-observation", Observation: driver.ObservationCapabilities{Streaming: driver.ObservationSupport{Skills: true, MCP: true, Subagents: true, Todos: true}}}
}
func (*alignmentDemandDriver) StreamCapability() driver.StreamCapability {
	return driver.StreamCapability{Native: true}
}
func (*alignmentDemandDriver) ValidateConfig(any) error { return nil }
func (d *alignmentDemandDriver) Run(ctx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
	d.requests = append(d.requests, req)
	at := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	if req.Observation.CapabilityInvocations {
		sink.EmitStream(driver.StreamPayload{Kind: driver.StreamCapabilityInvocation, Capability: &capability.Invocation{InvocationID: "call", Ref: capability.Ref{Kind: capability.MCP, Key: "knowledge", Operation: "search"}, Phase: capability.Started, Evidence: capability.ProviderProtocol, Source: capability.Provider, OccurredAt: at}})
	}
	if req.Observation.Todos {
		sink.EmitStream(driver.StreamPayload{Kind: driver.StreamTodoUpdated, Todo: &todo.Snapshot{Items: []todo.Item{}, Source: todo.PlanUpdate, Revision: 1, OccurredAt: at}})
	}
	return driver.Response{Output: "done", Summary: "safe summary"}, nil
}
func alignmentExecutor(runner adaptor.Runner, exposure ExposurePolicy) *executor {
	return &executor{runner: runner, session: Stateless(), prompt: defaultPrompt, exposure: exposure, active: map[a2aproto.TaskID]adaptor.Stream{}, pending: map[a2aproto.TaskID]context.CancelFunc{}}
}
func alignmentExecContext() *a2asrv.ExecutorContext {
	return &a2asrv.ExecutorContext{TaskID: "task", Message: a2aproto.NewMessage(a2aproto.MessageRoleUser, a2aproto.NewTextPart("hello"))}
}

func TestAlignmentNoStoreObservationDemand(t *testing.T) {
	for _, exposure := range []ExposurePolicy{{}, {IncludeToolCalls: true}, {IncludeCapabilityInvocations: true}, {IncludeTodos: true}, {IncludeCapabilityInvocations: true, IncludeTodos: true}} {
		t.Run(fmt.Sprintf("cap=%v/todo=%v/tool=%v", exposure.IncludeCapabilityInvocations, exposure.IncludeTodos, exposure.IncludeToolCalls), func(t *testing.T) {
			fake := &alignmentDemandDriver{}
			agent := adaptor.New(fake)
			defer agent.Close(context.Background())
			opts := observationOptions(exposure)
			if !exposure.IncludeCapabilityInvocations && !exposure.IncludeTodos && len(opts) != 0 {
				t.Fatal("opt-out installed attachment")
			}
			if _, err := agent.Run(context.Background(), "run", opts...); err != nil {
				t.Fatal(err)
			}
			stream := agent.Stream(context.Background(), "stream", opts...)
			for range stream.Events() {
			}
			if _, err := stream.Result(); err != nil {
				t.Fatal(err)
			}
			exec := alignmentExecutor(agent, exposure)
			capCount, todoCount := 0, 0
			for event, err := range exec.Execute(context.Background(), alignmentExecContext()) {
				if err != nil {
					t.Fatal(err)
				}
				status, ok := event.(*a2aproto.TaskStatusUpdateEvent)
				if !ok || status.Status.Message == nil {
					continue
				}
				for _, part := range status.Status.Message.Parts {
					data, ok := part.Content.(a2aproto.Data)
					if !ok {
						continue
					}
					value, m, err := DecodeAdapterEventV1(data.Value)
					if err != nil {
						t.Fatal(err)
					}
					if !m {
						continue
					}
					switch value.(type) {
					case adaptor.CapabilityInvocation:
						capCount++
					case adaptor.TodoUpdated:
						todoCount++
					}
				}
			}
			if capCount != boolInt(exposure.IncludeCapabilityInvocations) || todoCount != boolInt(exposure.IncludeTodos) {
				t.Fatalf("relay counts %d/%d", capCount, todoCount)
			}
			if len(fake.requests) != 3 {
				t.Fatalf("dispatch count=%d", len(fake.requests))
			}
			for _, req := range fake.requests {
				if req.Observation.CapabilityInvocations != exposure.IncludeCapabilityInvocations || req.Observation.Todos != exposure.IncludeTodos {
					t.Fatalf("demand=%#v", req.Observation)
				}
				if req.Streaming != fake.requests[0].Streaming {
					t.Fatal("Run/Stream/executor selected different transport")
				}
			}
		})
	}
	attachment, err := (observationDemandProvider{demand: adaptor.ObservationDemand{Todos: true}}).AttachRun(context.Background(), "run")
	if err != nil || attachment.Observer != nil || attachment.Events != nil || attachment.BindEvents != nil || len(attachment.Services) != 0 {
		t.Fatalf("demand creates resources: %#v %v", attachment, err)
	}
}
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

type alignmentFixedRunner struct{ stream *alignmentFixedStream }

func (r alignmentFixedRunner) Run(context.Context, string, ...adaptor.CallOption) (*adaptor.Result, error) {
	return r.stream.Result()
}
func (r alignmentFixedRunner) Stream(context.Context, string, ...adaptor.CallOption) adaptor.Stream {
	return r.stream
}

type alignmentFixedStream struct {
	events     chan adaptor.Event
	result     *adaptor.Result
	err        error
	cancelled  bool
	readResult bool
}

func (s *alignmentFixedStream) Events() <-chan adaptor.Event { return s.events }
func (s *alignmentFixedStream) Result() (*adaptor.Result, error) {
	s.readResult = true
	return s.result, s.err
}
func (s *alignmentFixedStream) RunID() string { return "run" }
func (s *alignmentFixedStream) Cancel()       { s.cancelled = true }

func TestAlignmentExecutorReportsUnencodableMetaWithPartialResult(t *testing.T) {
	for _, partialError := range []bool{false, true} {
		t.Run(fmt.Sprint(partialError), func(t *testing.T) {
			for _, broken := range []string{"opaque_key", "source_cycle", "source_depth", "unsafe_sequence"} {
				t.Run(broken, func(t *testing.T) {
					event := alignmentEvent(t, "capability-start")
					meta := event.Meta()
					switch broken {
					case "opaque_key":
						meta.ThreadKey = strings.Repeat("opaque/", 10000)
					case "source_cycle":
						source := &adaptor.EventSourceMeta{RunID: "upstream"}
						source.Upstream = source
						meta.Source = source
					case "source_depth":
						tail := &meta.Source
						for i := 0; i < 9; i++ {
							*tail = &adaptor.EventSourceMeta{RunID: "upstream"}
							tail = &(*tail).Upstream
						}
					case "unsafe_sequence":
						meta.Sequence = 9007199254740992
					}
					result := resultWithRawForTest(t, adaptor.RawStreams{Stdout: "partial-output"})
					stream := &alignmentFixedStream{events: make(chan adaptor.Event, 2), result: result}
					if partialError {
						stream.result = nil
						stream.err = &adaptor.RunError{Reason: adaptor.ReasonAgentError, Message: "provider private-error", Result: result}
					}
					stream.events <- adaptor.WithEventMeta(event, meta)
					stream.events <- adaptor.WithEventMeta(adaptor.RunFinished{}, adaptor.EventMeta{RunID: "run", Sequence: 8})
					close(stream.events)
					exec := alignmentExecutor(alignmentFixedRunner{stream}, ExposurePolicy{IncludeCapabilityInvocations: true, Diagnostics: DiagnosticsPolicy{IncludeMetadata: true, IncludeRawStreams: true}})
					var seenErr error
					seenArtifact := false
					for event, err := range exec.Execute(context.Background(), alignmentExecContext()) {
						if err != nil {
							seenErr = err
							continue
						}
						if artifact, ok := event.(*a2aproto.TaskArtifactUpdateEvent); ok {
							encoded, _ := json.Marshal(artifact)
							seenArtifact = strings.Contains(string(encoded), "partial-output")
						}
					}
					if seenErr == nil || !seenArtifact || !stream.readResult || !stream.cancelled || len(stream.events) != 0 {
						t.Fatalf("error=%v artifact=%v result=%v cancelled=%v pending=%d", seenErr, seenArtifact, stream.readResult, stream.cancelled, len(stream.events))
					}
					if strings.Contains(seenErr.Error(), "opaque/") || strings.Contains(seenErr.Error(), "private-error") || strings.Contains(seenErr.Error(), "upstream") {
						t.Fatalf("unsafe error=%v", seenErr)
					}
				})
			}
		})
	}
}

func TestAlignmentTranslatorSafeLossKeepsMetaAndCompletes(t *testing.T) {
	ev := alignmentEvent(t, "todo-clear").(adaptor.TodoUpdated)
	ev.Snapshot.Items = []todo.Item{{ID: "x", Content: "secret-payload", Status: todo.Status("unknown")}}
	translator := newStreamTranslator(testTaskInfo{}, ExposurePolicy{IncludeTodos: true})
	out := translator.Translate(ev)
	if translator.err != nil || len(out) != 1 {
		t.Fatalf("drop not observable: %v", translator.err)
	}
	data := dataValue(t, out[0])
	decoded, _, err := DecodeAdapterEventV1(data)
	if err != nil {
		t.Fatal(err)
	}
	drop := decoded.(adaptor.Dropped)
	if drop.Count != 1 || drop.Reason != "invalid_payload" || drop.Meta().Sequence != ev.Meta().Sequence {
		t.Fatalf("bad drop=%#v", drop)
	}
	encoded, _ := json.Marshal(data)
	if strings.Contains(string(encoded), "secret-payload") {
		t.Fatal("invalid field leaked")
	}
	_, err = encodeAdapterStreamDrop(AdapterStreamEventV1{Kind: "unknown"}, errAdapterKind)
	if err != nil {
		t.Fatal(err)
	}
}

type alignmentCancelStream struct {
	events    chan adaptor.Event
	cancelled chan struct{}
	once      sync.Once
	result    *adaptor.Result
}

func (s *alignmentCancelStream) Events() <-chan adaptor.Event     { return s.events }
func (s *alignmentCancelStream) Result() (*adaptor.Result, error) { return s.result, nil }
func (s *alignmentCancelStream) RunID() string                    { return "run" }
func (s *alignmentCancelStream) Cancel()                          { s.once.Do(func() { close(s.cancelled) }) }

type alignmentCancelRunner struct{ stream *alignmentCancelStream }

func (r alignmentCancelRunner) Run(context.Context, string, ...adaptor.CallOption) (*adaptor.Result, error) {
	return r.stream.Result()
}
func (r alignmentCancelRunner) Stream(context.Context, string, ...adaptor.CallOption) adaptor.Stream {
	return r.stream
}

func TestAlignmentEncodingFailureCancelsAndDrainsLiveStream(t *testing.T) {
	event := alignmentEvent(t, "capability-start")
	meta := event.Meta()
	meta.ThreadKey = strings.Repeat("x", 65537)
	stream := &alignmentCancelStream{events: make(chan adaptor.Event), cancelled: make(chan struct{}), result: &adaptor.Result{Summary: "partial"}}
	producerDone := make(chan struct{})
	go func() {
		defer close(producerDone)
		defer close(stream.events)
		stream.events <- adaptor.WithEventMeta(event, meta)
		<-stream.cancelled
		stream.events <- adaptor.WithEventMeta(adaptor.RunFinished{}, adaptor.EventMeta{RunID: "run", Sequence: 8})
	}()
	exec := alignmentExecutor(alignmentCancelRunner{stream}, ExposurePolicy{IncludeCapabilityInvocations: true})
	done := make(chan error, 1)
	go func() {
		var failure error
		for _, err := range exec.Execute(context.Background(), alignmentExecContext()) {
			if err != nil {
				failure = err
			}
		}
		done <- failure
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("missing encoding failure")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("encoding error deadlocked stream cancellation/drain")
	}
	select {
	case <-producerDone:
	case <-time.After(time.Second):
		t.Fatal("producer was not drained")
	}
}
