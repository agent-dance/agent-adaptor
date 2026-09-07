package adaptor_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/capability"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/memory"
	"github.com/agent-dance/agent-adaptor/threadstore"
	"github.com/agent-dance/agent-adaptor/todo"
)

func alignmentFact(id string) capability.Invocation {
	return capability.Invocation{InvocationID: id, Ref: capability.Ref{Kind: capability.MCP, Key: "knowledge", Operation: "search"}, Phase: capability.Started, Source: capability.Provider, Evidence: capability.ProviderProtocol, OccurredAt: time.Now().UTC()}
}
func alignmentTodo() todo.Snapshot {
	return todo.Snapshot{Items: []todo.Item{{ID: "id", Content: "work", Status: todo.Pending}}, Source: todo.PlanUpdate, Revision: 1, OccurredAt: time.Now().UTC()}
}
func observerProvider(att adaptor.RunAttachment) *fakeProvider {
	return &fakeProvider{log: &callLog{}, attachment: att}
}

func TestAlignmentObserverSafeOrderedProjection(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			var mu sync.Mutex
			var observed []adaptor.Event
			var infos []adaptor.RunEventInfo
			first := observerProvider(adaptor.RunAttachment{Observer: func(_ context.Context, info adaptor.RunEventInfo, ev adaptor.Event) error {
				switch v := ev.(type) {
				case adaptor.CapabilityInvocation:
					if v.Invocation.Duration != nil {
						*v.Invocation.Duration = 999
					}
				case adaptor.TodoUpdated:
					v.Snapshot.Items[0].Content = "mutated"
				default:
					t.Errorf("unsafe observed %T", ev)
				}
				return nil
			}})
			second := observerProvider(adaptor.RunAttachment{Observer: func(_ context.Context, info adaptor.RunEventInfo, ev adaptor.Event) error {
				mu.Lock()
				observed = append(observed, ev)
				infos = append(infos, info)
				mu.Unlock()
				return nil
			}})
			d := alignmentFakeDriver()
			d.runFunc = func(ctx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
				_ = sink.Emit(driver.RunEvent{Type: driver.RunEventChunk, Bytes: []byte("raw-secret")})
				_ = sink.EmitStream(driver.StreamPayload{Kind: driver.StreamToolCallStart, ToolCallID: "tool", Name: "tool", Args: map[string]any{"secret": "arg"}})
				v := alignmentFact("one")
				if e := sink.EmitStream(driver.StreamPayload{Kind: driver.StreamCapabilityInvocation, Capability: &v}); e != nil {
					return driver.Response{}, e
				}
				zero := time.Duration(0)
				v.Phase = capability.Completed
				v.Duration = &zero
				if e := sink.EmitStream(driver.StreamPayload{Kind: driver.StreamCapabilityInvocation, Capability: &v}); e != nil {
					return driver.Response{}, e
				}
				zero = 4
				snapshot := alignmentTodo()
				if e := sink.EmitStream(driver.StreamPayload{Kind: driver.StreamTodoUpdated, Todo: &snapshot}); e != nil {
					return driver.Response{}, e
				}
				snapshot.Items[0].Content = "producer mutated"
				_ = sink.EmitStream(driver.StreamPayload{Kind: driver.StreamToolCallEnd, ToolCallID: "tool"})
				return alignmentPartialResponse(), nil
			}
			a := adaptor.New(d, adaptor.WithRunServices(first, second), adaptor.WithIdentity(adaptor.Identity{ID: "who", Tenant: "tenant"}))
			res, e := alignmentCall(t, a, context.Background(), stream, alignmentSchema())
			if e != nil {
				t.Fatal(e)
			}
			alignmentAssertLayers(t, res, alignmentPartialResponse())
			if len(observed) != 3 || observed[0].Meta().Sequence >= observed[1].Meta().Sequence || observed[1].Meta().Sequence >= observed[2].Meta().Sequence {
				t.Fatal(observed)
			}
			if *observed[1].(adaptor.CapabilityInvocation).Invocation.Duration != 0 || observed[2].(adaptor.TodoUpdated).Snapshot.Items[0].Content != "work" {
				t.Fatal("observer sharing mutable values")
			}
			for _, info := range infos {
				if info.Identity.ID != "who" || info.Identity.Tenant != "tenant" || info.DriverType != "fake" || info.RunID != res.RunID {
					t.Fatal(info)
				}
			}
		})
	}
}

func TestAlignmentObserverFailuresDoNotChangeCheckpointOrApproval(t *testing.T) {
	for _, mode := range []string{"error", "panic", "timeout", "timeout-nil", "reentrant"} {
		t.Run(mode, func(t *testing.T) {
			var publish adaptor.RunEventPublisher
			var calls atomic.Int32
			late := make(chan struct{})
			defer close(late)
			failing := observerProvider(adaptor.RunAttachment{BindEvents: func(p adaptor.RunEventPublisher) error { publish = p; return nil }, Observer: func(ctx context.Context, _ adaptor.RunEventInfo, e adaptor.Event) error {
				calls.Add(1)
				switch mode {
				case "error":
					return errors.New("secret-url-token")
				case "panic":
					panic("secret-url-token")
				case "timeout-nil":
					<-ctx.Done()
					return nil
				case "timeout":
					<-late
					return errors.New("late-secret")
				default:
					err := publish(ctx, adaptor.Notice{})
					if err == nil || err.Error() != "adaptor: invalid run event" {
						t.Errorf("reentrant publication=%v", err)
					}
					return err
				}
			}})
			var good atomic.Int32
			healthy := observerProvider(adaptor.RunAttachment{Observer: func(context.Context, adaptor.RunEventInfo, adaptor.Event) error { good.Add(1); return nil }})
			store := memory.NewStore()
			d := newFakeDriver()
			d.runFunc = func(ctx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
				for _, id := range []string{"a", "b"} {
					v := alignmentFact(id)
					if e := sink.EmitStream(driver.StreamPayload{Kind: driver.StreamCapabilityInvocation, Capability: &v}); e != nil {
						return driver.Response{}, e
					}
				}
				reply, err := sink.(driver.DecisionCapableSink).RequestDecision(ctx, driver.DecisionRequest{Kind: driver.HumanDecisionPermission, Prompt: "permission"})
				if err != nil {
					return driver.Response{}, err
				}
				if reply.Result != driver.DecisionApproved {
					return driver.Response{}, errors.New("approval changed")
				}
				return alignmentPartialResponse(), nil
			}
			a := adaptor.New(d, adaptor.WithThreadStore(store), adaptor.WithRunServices(failing, healthy), adaptor.OnApproval(func(ctx context.Context, r *adaptor.ApprovalRequest) error { return r.Approve(ctx) }))
			st := a.Thread("observer").Stream(context.Background(), "work")
			var notices []adaptor.Notice
			var events []adaptor.Event
			for ev := range st.Events() {
				events = append(events, ev)
				if n, ok := ev.(adaptor.Notice); ok && n.Data["code"] == "observation_disabled" {
					notices = append(notices, n)
				}
			}
			res, e := st.Result()
			if e != nil || res == nil {
				t.Fatal(e)
			}
			if calls.Load() != 1 || good.Load() != 2 || len(notices) != 1 {
				t.Fatal(calls.Load(), good.Load(), len(notices))
			}
			n := notices[0]
			reason := mode
			if reason == "reentrant" {
				reason = "error"
			}
			if reason == "timeout-nil" {
				reason = "timeout"
			}
			if n.Text != "" || len(n.Data) != 3 || n.Data["reason"] != reason || n.Data["observer_index"] != 0 {
				t.Fatal(n)
			}
			raw, _ := json.Marshal(events)
			if strings.Contains(string(raw), "secret-url-token") {
				t.Fatal("callback error leaked")
			}
			cp, e := a.Thread("observer").Checkpoint(context.Background())
			if e != nil || cp == nil {
				t.Fatal("observer failure polluted checkpoint", e)
			}
			if err := publish(context.Background(), adaptor.Notice{}); !errors.Is(err, context.Canceled) {
				t.Fatal("publisher not revoked", err)
			}
		})
	}
}

func TestAlignmentObserverPublisherAuthorizationAndBinding(t *testing.T) {
	var pub adaptor.RunEventPublisher
	var observed atomic.Int32
	var order []int
	p1 := observerProvider(adaptor.RunAttachment{Observer: func(context.Context, adaptor.RunEventInfo, adaptor.Event) error { observed.Add(1); return nil }, BindEvents: func(p adaptor.RunEventPublisher) error {
		order = append(order, 1)
		pub = p
		v := alignmentFact("host")
		v.Source = capability.Host
		v.Evidence = capability.HostLifecycle
		return p(context.Background(), adaptor.CapabilityInvocation{Invocation: v})
	}})
	p2 := observerProvider(adaptor.RunAttachment{BindEvents: func(p adaptor.RunEventPublisher) error {
		order = append(order, 2)
		if observed.Load() != 1 {
			t.Fatal("observer not installed before binding")
		}
		for _, ev := range []adaptor.Event{(*adaptor.ApprovalRequest)(nil), (*adaptor.CapabilityInvocation)(nil), (*adaptor.TodoUpdated)(nil), (*adaptor.TextDelta)(nil), (*adaptor.Thinking)(nil), (*adaptor.ToolCall)(nil), (*adaptor.ToolResult)(nil), (*adaptor.ProcessInfo)(nil), (*adaptor.Notice)(nil), (*adaptor.Dropped)(nil), (*adaptor.SubagentUpdate)(nil), (*adaptor.RunStarted)(nil), (*adaptor.RunFinished)(nil), adaptor.RunStarted{}, adaptor.RunFinished{}, &adaptor.ApprovalRequest{}, adaptor.CapabilityInvocation{Invocation: alignmentFact("forged")}, adaptor.TodoUpdated{Snapshot: todo.Snapshot{}}} {
			if e := p(context.Background(), ev); e == nil || e.Error() != "adaptor: invalid run event" {
				t.Errorf("accepted forged %T: %v", ev, e)
			}
		}
		return nil
	}})
	if _, e := adaptor.New(newFakeDriver(), adaptor.WithRunServices(p1, p2)).Run(context.Background(), "work"); e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(order, []int{1, 2}) {
		t.Fatal(order)
	}
	if e := pub(context.Background(), adaptor.Notice{}); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	log := &callLog{}
	cause := errors.New("bind fail")
	first := observerProvider(adaptor.RunAttachment{})
	first.log = log
	first.name = "first"
	second := observerProvider(adaptor.RunAttachment{BindEvents: func(adaptor.RunEventPublisher) error { return cause }})
	second.log = log
	second.name = "second"
	d := newFakeDriver()
	_, e := adaptor.New(d, adaptor.WithRunServices(first, second)).Run(context.Background(), "work")
	if !errors.Is(e, cause) || d.runCount() != 0 {
		t.Fatal(e)
	}
	got := log.snapshot()
	if len(got) != 4 || !strings.HasPrefix(got[2], "detach:second") || !strings.HasPrefix(got[3], "detach:first") {
		t.Fatal(got)
	}
}

func TestAlignmentObserverCancelCountsAcceptedSnapshotBeforeTerminal(t *testing.T) {
	for _, blocking := range []bool{false, true} {
		t.Run(fmt.Sprint(blocking), func(t *testing.T) {
			accepted := make(chan struct{})
			p := observerProvider(adaptor.RunAttachment{Observer: func(ctx context.Context, _ adaptor.RunEventInfo, _ adaptor.Event) error {
				// SDK cancels this child context after consuming the successful
				// callback result. Signal then, not inside the callback before
				// return, so the test cancels only after observation completes.
				go func() { <-ctx.Done(); close(accepted) }()
				return nil
			}})
			d := newFakeDriver()
			d.runFunc = func(ctx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
				if !blocking {
					_ = sink.EmitStream(driver.StreamPayload{Kind: driver.StreamTextContent, Delta: "lost before the snapshot"})
				}
				snapshot := alignmentTodo()
				_ = sink.EmitStream(driver.StreamPayload{Kind: driver.StreamTodoUpdated, Todo: &snapshot})
				return alignmentPartialResponse(), ctx.Err()
			}
			opts := []adaptor.Option{adaptor.WithRunServices(p), adaptor.WithEventBuffer(1)}
			if blocking {
				opts = append(opts, adaptor.WithBlockingEvents())
			}
			st := adaptor.New(d, opts...).Stream(context.Background(), "work")
			select {
			case <-accepted:
			case <-time.After(time.Second):
				t.Fatal("observer blocked behind user buffer")
			}
			st.Cancel()
			done := make(chan error, 1)
			go func() { _, e := st.Result(); done <- e }()
			select {
			case e := <-done:
				if !errors.Is(e, context.Canceled) {
					t.Fatal(e)
				}
			case <-time.After(time.Second):
				t.Fatal("cancel waited for consumer")
			}
			var events []adaptor.Event
			for ev := range st.Events() {
				events = append(events, ev)
			}
			if len(events) != 3 {
				t.Fatalf("want started/dropped/terminal, got %v", events)
			}
			lost := 1
			if !blocking {
				lost += 2 // the prior delta and its assigned but undelivered summary
			}
			loss, ok := events[1].(adaptor.Dropped)
			if !ok || loss.Count != lost || loss.ByKind["todo.updated"] != 1 || loss.FirstSequence != 2 || loss.LastSequence != uint64(lost+1) {
				t.Fatal(loss)
			}
			if !blocking && (loss.ByKind["dropped"] != 1 || loss.ByKind["text.content"] != 1) {
				t.Fatal("lost assigned summary not counted", loss)
			}
			terminal, ok := events[2].(adaptor.RunFinished)
			if !ok || !terminal.Failed || terminal.Reason != adaptor.ReasonCancelled || terminal.Meta().Sequence != uint64(lost+3) {
				t.Fatal(terminal)
			}
		})
	}
}

func TestAlignmentObserverConcurrentRunsAndPublishers(t *testing.T) {
	var mu sync.Mutex
	seen := map[string][]uint64{}
	active := map[string]bool{}
	p := observerProvider(adaptor.RunAttachment{Observer: func(_ context.Context, info adaptor.RunEventInfo, ev adaptor.Event) error {
		mu.Lock()
		defer mu.Unlock()
		if active[info.RunID] {
			t.Error("overlapping observer")
		}
		active[info.RunID] = true
		seen[info.RunID] = append(seen[info.RunID], ev.Meta().Sequence)
		active[info.RunID] = false
		return nil
	}})
	d := newFakeDriver()
	d.runFunc = func(ctx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
		var wg sync.WaitGroup
		for i := range 20 {
			wg.Go(func() {
				v := alignmentFact(fmt.Sprint(i))
				if e := sink.EmitStream(driver.StreamPayload{Kind: driver.StreamCapabilityInvocation, Capability: &v}); e != nil {
					t.Error(e)
				}
			})
		}
		wg.Wait()
		return driver.Response{Output: "ok"}, nil
	}
	a := adaptor.New(d, adaptor.WithRunServices(p))
	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() {
			if _, e := a.Run(context.Background(), "work"); e != nil {
				t.Error(e)
			}
		})
	}
	wg.Wait()
	if len(seen) != 5 {
		t.Fatal(len(seen))
	}
	for _, seq := range seen {
		if len(seq) != 20 {
			t.Fatal(seq)
		}
		for i := 1; i < len(seq); i++ {
			if seq[i] <= seq[i-1] {
				t.Fatal(seq)
			}
		}
	}
}

func TestAlignmentObserverTransportCandidatesAndRealApproval(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, tc := range []struct {
			name                                           string
			rich, schema, precise, demand, ask, zeroPolicy bool
			wantStreaming                                  bool
			wantSource                                     driver.StructuredOutputSource
		}{
			{name: "zero-demand-retains-rich", rich: true, schema: true, wantStreaming: true, wantSource: driver.StructuredOutputSourceNative},
			{name: "demand-selects-batch", rich: true, schema: true, demand: true, wantSource: driver.StructuredOutputSourceNative},
			{name: "no-schema-ask-preserved", rich: true, demand: true, ask: true, wantStreaming: true},
			{name: "initial-batch-ask-remains-legal", demand: true, ask: true},
			{name: "precise-inherited-ask-preserved", rich: true, schema: true, precise: true, demand: true, ask: true, wantStreaming: true, wantSource: driver.StructuredOutputSourcePromptValidate},
			{name: "precise-default-ask-preserved", rich: true, schema: true, precise: true, demand: true, zeroPolicy: true, wantStreaming: true, wantSource: driver.StructuredOutputSourcePromptValidate},
			{name: "nil-legacy-default-batch-allowed", rich: true, schema: true, demand: true, zeroPolicy: true, wantSource: driver.StructuredOutputSourceNative},
		} {
			t.Run(fmt.Sprintf("%s/%v", tc.name, stream), func(t *testing.T) {
				d := newFakeDriver()
				if tc.rich {
					d.streamCaps = driver.StreamCapability{Native: true, HITL: true}
				}
				desc := d.Descriptor()
				desc.Observation.Batch = driver.ObservationSupport{Skills: true, MCP: true, Subagents: true, Todos: true}
				desc.Observation.Streaming = driver.ObservationSupport{MCP: true}
				desc.StructuredOutput = driver.StructuredOutputCapability{JSONSchemaNative: true, JSONSchemaPromptValidate: true, WorksWithRun: true, WorksWithStreaming: true, WorksWithHITL: true}
				if tc.precise {
					desc.StructuredOutput = alignmentHITLCaps(t)
				}
				d.descriptor = &desc
				var replies atomic.Int32
				d.runFunc = func(ctx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
					if req.Streaming != tc.wantStreaming || req.StructuredOutputSource != tc.wantSource || req.Observation.CapabilityInvocations != tc.demand {
						t.Errorf("resolved transport/source/demand=%v/%v/%+v", req.Streaming, req.StructuredOutputSource, req.Observation)
					}
					if tc.ask {
						reply, e := sink.(driver.DecisionCapableSink).RequestDecision(ctx, driver.DecisionRequest{Kind: driver.HumanDecisionPermission, Prompt: "permission"})
						if e != nil {
							return driver.Response{}, e
						}
						if reply.Result != driver.DecisionApproved {
							return driver.Response{}, errors.New("permission reply lost")
						}
						replies.Add(1)
					}
					response := driver.Response{Output: `{"answer":42}`}
					if req.StructuredOutputSource == driver.StructuredOutputSourceNative {
						response.StructuredOutput = &driver.StructuredOutput{RawJSON: json.RawMessage(response.Output)}
					}
					return response, nil
				}
				att := adaptor.RunAttachment{}
				if tc.demand {
					att.Observation = adaptor.ObservationDemand{CapabilityInvocations: true, Todos: true}
				}
				p := observerProvider(att)
				policy := adaptor.Policy{Approvals: adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAutoApprove, PlanReview: adaptor.ApprovalAutoApprove}}
				if tc.ask {
					policy.Approvals = adaptor.ApprovalPolicy{Question: adaptor.QuestionAsk}
				}
				if tc.zeroPolicy {
					policy = adaptor.Policy{}
				}
				a := adaptor.New(d, adaptor.WithRunServices(p), adaptor.WithPolicy(policy), adaptor.OnApproval(func(ctx context.Context, r *adaptor.ApprovalRequest) error { return r.Approve(ctx) }))
				opts := []adaptor.CallOption{}
				if tc.schema {
					opts = append(opts, alignmentSchema())
				}
				if _, e := alignmentCall(t, a, context.Background(), stream, opts...); e != nil {
					t.Fatal(e)
				}
				if tc.ask && replies.Load() != 1 {
					t.Fatal("no real Ask reply")
				}
				if d.runCount() != 1 {
					t.Fatal("duplicate dispatch")
				}
			})
		}
	}
}

func TestAlignmentObserverUnavailableAndPreflight(t *testing.T) {
	for _, schema := range []string{"invalid", "unsupported", "absent"} {
		t.Run(schema, func(t *testing.T) {
			d := newFakeDriver()
			p := observerProvider(adaptor.RunAttachment{Observation: adaptor.ObservationDemand{CapabilityInvocations: true, Todos: true}})
			a := adaptor.New(d, adaptor.WithRunServices(p))
			opts := []adaptor.CallOption{}
			if schema == "invalid" {
				opts = append(opts, adaptor.WithSchemaJSON([]byte(`{"type":"invalid"}`)))
			}
			if schema == "unsupported" {
				opts = append(opts, alignmentSchema())
			}
			st := a.Stream(context.Background(), "work", opts...)
			var unavailable []string
			var notices, eventCount int
			for ev := range st.Events() {
				eventCount++
				if n, ok := ev.(adaptor.Notice); ok && n.Data["code"] == "observation_unavailable" {
					unavailable = n.Data["capabilities"].([]string)
					notices++
				}
			}
			_, err := st.Result()
			if schema != "absent" {
				if err == nil || len(p.log.snapshot()) != 0 || d.runCount() != 0 || eventCount != 0 {
					t.Fatal("static failure acquired resources", err)
				}
				return
			}
			if err != nil || notices != 1 || !reflect.DeepEqual(unavailable, []string{"skill", "mcp", "subagent", "todo"}) {
				t.Fatal(unavailable, notices, err)
			}
		})
	}
}

func TestAlignmentObserverPublisherContextBoundsWait(t *testing.T) {
	accepted := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	var pub adaptor.RunEventPublisher
	p := observerProvider(adaptor.RunAttachment{BindEvents: func(p adaptor.RunEventPublisher) error { pub = p; return nil }, Observer: func(ctx context.Context, _ adaptor.RunEventInfo, _ adaptor.Event) error {
		close(accepted)
		<-release
		return nil
	}})
	d := newFakeDriver()
	d.runFunc = func(ctx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
		v := alignmentFact("blocking")
		_ = sink.EmitStream(driver.StreamPayload{Kind: driver.StreamCapabilityInvocation, Capability: &v})
		return driver.Response{Output: "done"}, nil
	}
	st := adaptor.New(d, adaptor.WithRunServices(p)).Stream(context.Background(), "work")
	<-accepted
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := pub(ctx, adaptor.Notice{Kind: adaptor.NoticeRuntime})
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 80*time.Millisecond {
		t.Fatal("publisher ctx did not bound mutex wait", err)
	}
	for range st.Events() {
	}
	if _, err := st.Result(); err != nil {
		t.Fatal(err)
	}
}

func TestAlignmentObserverPendingDropPrecedesFactWithoutDelayingObservation(t *testing.T) {
	for _, kind := range []string{"capability", "todo"} {
		t.Run(kind, func(t *testing.T) {
			accepted := make(chan adaptor.Event, 1)
			p := observerProvider(adaptor.RunAttachment{Observer: func(_ context.Context, _ adaptor.RunEventInfo, ev adaptor.Event) error { accepted <- ev; return nil }})
			d := newFakeDriver()
			d.runFunc = func(_ context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
				_ = sink.EmitStream(driver.StreamPayload{Kind: driver.StreamTextContent, Delta: "dropped"})
				if kind == "capability" {
					v := alignmentFact("one")
					_ = sink.EmitStream(driver.StreamPayload{Kind: driver.StreamCapabilityInvocation, Capability: &v})
				} else {
					v := alignmentTodo()
					_ = sink.EmitStream(driver.StreamPayload{Kind: driver.StreamTodoUpdated, Todo: &v})
				}
				return driver.Response{Output: "done"}, nil
			}
			st := adaptor.New(d, adaptor.WithEventBuffer(1), adaptor.WithRunServices(p)).Stream(context.Background(), "work")
			defer st.Cancel()
			select {
			case fact := <-accepted:
				if fact.Meta().Sequence != 4 {
					t.Fatal(fact.Meta())
				}
			case <-time.After(time.Second):
				t.Fatal("prior Dropped publication blocked observer")
			}
			events, _, err := collect(st)
			if err != nil {
				t.Fatal(err)
			}
			if len(events) != 4 {
				t.Fatal(events)
			}
			loss, ok := events[1].(adaptor.Dropped)
			if !ok || loss.Count != 1 || loss.FirstSequence != 2 || loss.LastSequence != 2 {
				t.Fatal(events)
			}
			for i, seq := range []uint64{1, 3, 4, 5} {
				if events[i].Meta().Sequence != seq {
					t.Fatal(events)
				}
			}
		})
	}
}

func TestAlignmentObserverDirectPublisherRevocationUnblocksCleanup(t *testing.T) {
	accepted := make(chan struct{})
	published := make(chan error, 1)
	var detached atomic.Bool
	p := observerProvider(adaptor.RunAttachment{Observer: func(context.Context, adaptor.RunEventInfo, adaptor.Event) error { close(accepted); return nil }, BindEvents: func(pub adaptor.RunEventPublisher) error {
		go func() { published <- pub(context.Background(), adaptor.TodoUpdated{Snapshot: alignmentTodo()}) }()
		return nil
	}})
	p.detach = func(context.Context, string) error { detached.Store(true); return nil }
	d := newFakeDriver()
	d.runFunc = func(context.Context, driver.Request, driver.EventSink) (driver.Response, error) {
		<-accepted
		return driver.Response{Output: "done"}, nil
	}
	st := adaptor.New(d, adaptor.WithEventBuffer(1), adaptor.WithRunServices(p)).Stream(context.Background(), "work")
	defer st.Cancel()
	done := make(chan error, 1)
	go func() { _, err := st.Result(); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("direct publisher held cleanup gate")
	}
	if !detached.Load() {
		t.Fatal("Detach was not reached")
	}
	if err := <-published; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	events, _, err := collect(st)
	if err != nil || len(events) != 3 {
		t.Fatal(events, err)
	}
	drop, ok := events[1].(adaptor.Dropped)
	if !ok || drop.Count != 1 || drop.ByKind["todo.updated"] != 1 {
		t.Fatal(events)
	}
	if last, ok := events[2].(adaptor.RunFinished); !ok || last.Failed {
		t.Fatal(events)
	}
}

func TestAlignmentObserverCompatibleTransportKeepsThread(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, reason := range []string{"schema", "observation"} {
			for _, resumeOnly := range []bool{false, true} {
				t.Run(fmt.Sprintf("%v/%s/resume=%v", stream, reason, resumeOnly), func(t *testing.T) {
					d := &configuredSessionFake{sessionFake: newSessionFake("transport"), configFingerprint: "stable-config"}
					d.streamCaps = driver.StreamCapability{Native: true}
					desc := d.Descriptor()
					desc.StructuredOutput = driver.StructuredOutputCapability{JSONSchemaNative: true, JSONSchemaPromptValidate: true, WorksWithRun: true}
					desc.Observation.Batch = driver.ObservationSupport{Todos: true}
					d.descriptor = &desc
					run := d.runFunc
					d.runFunc = func(ctx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
						response, err := run(ctx, req, sink)
						response.Output = `{"answer":42}`
						if req.OutputSchema != nil {
							response.StructuredOutput = &driver.StructuredOutput{RawJSON: json.RawMessage(response.Output)}
						}
						return response, err
					}
					store := memory.NewStore()
					p := observerProvider(adaptor.RunAttachment{})
					a := adaptor.New(d, adaptor.WithThreadStore(store), adaptor.WithRunServices(p))
					t.Cleanup(func() { _ = a.Close(context.Background()) })
					engineID, resumeID := "", ""
					for i, prompt := range []string{"warm", "temporary", "after"} {
						th := a.Thread("transport")
						if i > 0 && resumeOnly {
							th = a.Thread("transport", adaptor.ResumeOnly())
						}
						var opts []adaptor.CallOption
						p.attachment.Observation = adaptor.ObservationDemand{}
						if i == 1 {
							if reason == "schema" {
								opts = append(opts, alignmentSchema())
							} else {
								p.attachment.Observation.Todos = true
							}
						}
						var result *adaptor.Result
						var err error
						if stream {
							s := th.Stream(context.Background(), prompt, opts...)
							for range s.Events() {
							}
							result, err = s.Result()
						} else {
							result, err = th.Run(context.Background(), prompt, opts...)
						}
						if err != nil {
							t.Fatalf("compatible %s turn: %v", prompt, err)
						}
						if i == 1 && reason == "schema" {
							var decoded struct {
								Answer int `json:"answer"`
							}
							if err := result.Decode(&decoded); err != nil || decoded.Answer != 42 {
								t.Fatal("schema result", err)
							}
						}
						req := d.request(t, i)
						if req.Prompt != prompt || req.Streaming != (i != 1) {
							t.Fatalf("wrong dispatch: %q rich=%v", req.Prompt, req.Streaming)
						}
						if req.Session == nil {
							t.Fatal("no session")
						}
						if i == 0 {
							engineID = req.Session.EngineSessionID
						} else if req.Session.EngineSessionID != engineID || req.Session.PreviousID != "" || req.Session.State == nil || req.Session.State.ResumeID != resumeID {
							t.Fatal("compatible transport rebound the active record", req.Session)
						}
						record, err := store.Resolve(context.Background(), threadstore.Query{ID: engineID})
						if err != nil || record == nil || record.Status != threadstore.StatusActive || record.State == nil {
							t.Fatal("healthy active record lost", err)
						}
						if i == 0 {
							resumeID = record.State.ResumeID
						} else if record.State.ResumeID != resumeID {
							t.Fatal("resume state replaced")
						}
					}
					if d.runCount() != 3 {
						t.Fatal("prompt replayed", d.runCount())
					}
					// A real construction/codec environment change is still a durable guard.
					d.configFingerprint = "changed-config"
					if _, err := a.Thread("transport", adaptor.ResumeOnly()).Run(context.Background(), "must-not-dispatch"); !errors.Is(err, adaptor.ErrThreadIncompatible) {
						t.Fatal("configuration drift accepted", err)
					}
					if d.runCount() != 3 {
						t.Fatal("incompatible prompt dispatched")
					}
				})
			}
		}
	}
}
