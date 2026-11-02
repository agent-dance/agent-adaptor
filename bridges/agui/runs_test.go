package agui_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	events "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"github.com/agent-dance/agent-adaptor/v2/bridges/agui"
)

type routeThread struct {
	adaptor.ThreadRunner
	count  atomic.Int32
	stream *routeStream
}

func (*routeThread) Key() string { return agui.ThreadKey("thread") }
func (thread *routeThread) Send(context.Context, string, ...adaptor.CallOption) adaptor.Stream {
	thread.count.Add(1)
	return thread.stream
}

type routeStream struct {
	adaptor.Stream
	mu       sync.Mutex
	messages []adaptor.MessageContext
	contexts []context.Context
	err      error
	id       string
	sendHook func(context.Context) error
}

func (s *routeStream) RunID() string {
	if s.id != "" {
		return s.id
	}
	return "original-run"
}
func (s *routeStream) Send(ctx context.Context, text string, opts ...adaptor.MessageOption) error {
	if s.sendHook != nil {
		return s.sendHook(ctx)
	}
	calls := make([]adaptor.CallOption, 0, len(opts))
	for _, o := range opts {
		calls = append(calls, o)
	}
	attrs := adaptor.MessageAttributes(calls...)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, adaptor.MessageContext{ID: attrs.ID, From: attrs.From, Timing: attrs.Timing, Text: text})
	s.contexts = append(s.contexts, ctx)
	return s.err
}

func TestRunRegistryCancellationUnblocksAdmission(t *testing.T) {
	entered := make(chan struct{})
	thread := &routeThread{stream: &routeStream{sendHook: func(ctx context.Context) error {
		close(entered)
		<-ctx.Done()
		return ctx.Err()
	}}}
	var registry agui.RunRegistry
	_, _, _ = registry.Send(t.Context(), thread, input("initial"))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, supplement, err := registry.Send(ctx, thread, input("supplement"))
		if !supplement {
			t.Error("input did not route to the active execution")
		}
		done <- err
	}()
	<-entered
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("admission error = %v, want cancellation", err)
	}
	if registry.Active(thread.Key()) != thread.stream || thread.count.Load() != 1 {
		t.Fatal("admission cancellation replaced the active execution")
	}
}
func input(id string) *agui.RunAgentInput {
	return &agui.RunAgentInput{ThreadID: "thread", Messages: []agui.Message{{ID: id, Role: "user", Content: []byte(`"guidance"`)}}}
}

func TestRunRegistryRoutesConcurrentInputs(t *testing.T) {
	var registry agui.RunRegistry
	thread := &routeThread{stream: &routeStream{}}
	stream, supplement, err := registry.Send(t.Context(), thread, input("initial"))
	if err != nil || supplement || stream != thread.stream {
		t.Fatal(stream, supplement, err)
	}
	const count = 16
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			got, supplement, err := registry.Send(ctx, thread, input("supplement"))
			if err != nil || !supplement || got != stream {
				t.Error(got, supplement, err)
			}
		}()
	}
	wg.Wait()
	if thread.count.Load() != 1 || registry.Active(thread.Key()) != stream {
		t.Fatal("opened another execution")
	}
	thread.stream.mu.Lock()
	defer thread.stream.mu.Unlock()
	if len(thread.stream.messages) != count {
		t.Fatal(thread.stream.messages)
	}
	for i, m := range thread.stream.messages {
		if m.ID != "supplement" || m.Text != "guidance" || m.From.Kind != adaptor.AuthorHuman || m.Timing != adaptor.Soon || !errors.Is(thread.stream.contexts[i].Err(), context.Canceled) {
			t.Fatal(m, thread.stream.contexts[i].Err())
		}
	}
	registry.Forget(thread.Key(), &routeStream{id: "stale-run"})
	if registry.Active(thread.Key()) != stream {
		t.Fatal("stale Forget removed current stream")
	}
	registry.Forget(thread.Key(), stream)
	if registry.Active(thread.Key()) != nil {
		t.Fatal("Forget retained stream")
	}
}

func TestRunRegistryReturnsFinishingRunRejection(t *testing.T) {
	rejection := &adaptor.MessageError{ID: "supplement", Text: "guidance", Err: adaptor.ErrRunFinished}
	thread := &routeThread{stream: &routeStream{err: rejection}}
	var registry agui.RunRegistry
	_, _, _ = registry.Send(t.Context(), thread, input("initial"))
	_, supplement, err := registry.Send(t.Context(), thread, input("supplement"))
	if !supplement || !errors.Is(err, adaptor.ErrRunFinished) || thread.count.Load() != 1 {
		t.Fatal(supplement, err)
	}
	registry.Forget(thread.Key(), thread.stream)
	_, supplement, err = registry.Send(t.Context(), thread, input("next"))
	if supplement || err != nil || thread.count.Load() != 2 {
		t.Fatal(supplement, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := registry.Send(ctx, thread, input("cancelled")); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestRunRegistrySendActiveNeverStartsAnExecution(t *testing.T) {
	var registry agui.RunRegistry
	thread := &routeThread{stream: &routeStream{}}
	if _, err := registry.SendActive(t.Context(), thread.Key(), input("inactive")); !errors.Is(err, adaptor.ErrRunFinished) {
		t.Fatalf("inactive SendActive = %v", err)
	}
	if thread.count.Load() != 0 {
		t.Fatal("SendActive started an execution")
	}
	stream, _, err := registry.Send(t.Context(), thread, input("initial"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	got, err := registry.SendActive(ctx, thread.Key(), input("active"))
	cancel()
	if err != nil || got != stream || thread.count.Load() != 1 {
		t.Fatal(got, err, thread.count.Load())
	}
	if !errors.Is(thread.stream.contexts[0].Err(), context.Canceled) {
		t.Fatal("SendActive detached the unclaimed input from its sender context")
	}
	registry.Forget(thread.Key(), stream)
	if _, err := registry.SendActive(t.Context(), thread.Key(), input("finished")); !errors.Is(err, adaptor.ErrRunFinished) {
		t.Fatalf("finished SendActive = %v", err)
	}
	if thread.count.Load() != 1 {
		t.Fatal("SendActive started a replacement execution")
	}
}

func TestRunRegistryPreservesSenderCancellationAtAdmissionBoundary(t *testing.T) {
	for _, route := range []string{"Send", "SendActive"} {
		for _, cancelAt := range []string{"before return", "after return"} {
			t.Run(route+"/"+cancelAt, func(t *testing.T) {
				var registry agui.RunRegistry
				thread := &routeThread{stream: &routeStream{}}
				if _, _, err := registry.Send(t.Context(), thread, input("initial")); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				var delivery context.Context
				thread.stream.sendHook = func(received context.Context) error {
					// The stream has admitted the input, but has not claimed it.
					delivery = received
					if cancelAt == "before return" {
						cancel()
					}
					return nil
				}
				var err error
				if route == "SendActive" {
					_, err = registry.SendActive(ctx, thread.Key(), input("supplement"))
				} else {
					_, _, err = registry.Send(ctx, thread, input("supplement"))
				}
				if err != nil {
					t.Fatalf("admitted input returned %v", err)
				}
				if cancelAt == "after return" {
					cancel()
				}
				if delivery != ctx || !errors.Is(delivery.Err(), context.Canceled) {
					t.Fatalf("delivery context = %v, want the cancelled original sender context", delivery)
				}
			})
		}
	}
}

func TestMessageTurnAndDoneProjection(t *testing.T) {
	translator := agui.NewEventTranslator()
	translator.Translate(adaptor.RunStarted{RunID: "run"})
	for _, state := range []adaptor.MessageState{adaptor.MessageAccepted, adaptor.MessageDelivered, adaptor.MessageFailed} {
		out := translator.Translate(adaptor.MessageUpdate{ID: "m", State: state, Reason: adaptor.MessageProviderRejected, Text: "original"})
		if len(out) != 1 {
			t.Fatal(out)
		}
		custom, ok := out[0].(*events.CustomEvent)
		if !ok || custom.Name != "message.update" {
			t.Fatalf("event=%#v", out[0])
		}
		value := custom.Value.(map[string]any)
		if value["id"] != "m" || value["state"] != state || value["text"] != "original" {
			t.Fatal(value)
		}
	}
	out := translator.Translate(adaptor.TurnEnd{Turn: 2, Result: &adaptor.Result{Text: "turn answer"}, MessageIDs: []string{"m"}})
	if len(out) != 1 || out[0].(*events.CustomEvent).Name != "turn.end" {
		t.Fatal(out)
	}
	// TurnEnd never ends an AG-UI run. Done owns the only terminal.
	terminal := translator.Translate(adaptor.Done{Result: &adaptor.Result{Text: "answer", Turns: 2}})
	if len(terminal) == 0 || terminal[len(terminal)-1].Type() != events.EventTypeRunFinished {
		t.Fatal(terminal)
	}
	if len(translator.CloseResult(nil, nil)) != 0 {
		t.Fatal("duplicate terminal")
	}
}

func TestCustomEventsAlwaysCarryValue(t *testing.T) {
	// Operational notices are opt-in; an exact passthrough kind keeps its CUSTOM form.
	translator := agui.NewEventTranslator(agui.WithNoticePassthrough("empty"))
	translator.Translate(adaptor.RunStarted{RunID: "run"})
	out := translator.Translate(adaptor.Notice{Kind: "empty"})
	if len(out) != 1 {
		t.Fatalf("events = %v", out)
	}
	raw, err := json.Marshal(out[0])
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["value"]) != "{}" {
		t.Fatalf("CUSTOM.value = %s, want {}: %s", fields["value"], raw)
	}
}

func TestTurnProjectionOmitsProviderMetadataAndDoneKeepsFailure(t *testing.T) {
	translator := agui.NewEventTranslator()
	translator.Translate(adaptor.RunStarted{RunID: "run"})
	result := &adaptor.Result{Text: "partial answer", Metadata: map[string]string{"token": "private-token"}, Usage: &adaptor.Usage{InputTokens: 5}}
	out := translator.Translate(adaptor.TurnEnd{Turn: 1, Result: result})
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "private-token") || !strings.Contains(string(raw), "partial answer") {
		t.Fatalf("unsafe or incomplete turn projection: %s", raw)
	}
	projected := out[0].(*events.CustomEvent).Value.(map[string]any)["result"].(*adaptor.Result)
	projected.Usage.InputTokens = 99
	if result.Usage.InputTokens != 5 {
		t.Fatal("projection shares mutable usage with the original Result")
	}
	runErr := &adaptor.RunError{Reason: adaptor.ReasonCancelled, Message: "cancelled", Result: result}
	terminal := translator.Translate(adaptor.Done{Err: errors.Join(runErr, context.Canceled)})
	last, ok := terminal[len(terminal)-1].(*events.RunErrorEvent)
	if !ok || last.Code == nil || *last.Code != "run.cancelled" {
		t.Fatalf("terminal = %#v, want run.cancelled", terminal)
	}
}

func TestDoneBeforeRunStartedUsesStreamIdentity(t *testing.T) {
	channel := make(chan adaptor.Event, 1)
	channel <- adaptor.Done{Err: context.Canceled}
	close(channel)
	stream := &cancellationStream{events: channel, cancelled: make(chan struct{})}
	var translated []events.Event
	for event := range agui.Events(stream) {
		translated = append(translated, event)
	}
	assertVerified(t, translated)
	started, ok := translated[0].(*agui.RunStartedEvent)
	if !ok || started.RunID() != stream.RunID() {
		t.Fatalf("start = %#v, want Stream.RunID %q", translated[0], stream.RunID())
	}
}
