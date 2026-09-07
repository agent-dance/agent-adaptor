package a2adelegation

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/capability"
	clienta2a "github.com/agent-dance/agent-adaptor/clients/a2a"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/todo"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAlignmentRelayDecodesNewFacts(t *testing.T) {
	for _, raw := range []string{
		`{"schema":"adapter.stream.v1","event":{"kind":"capability.invocation","meta":{"run_id":"remote","sequence":7,"time":"2026-09-07T00:00:00Z"},"capability":{"invocation_id":"call","kind":"mcp","key":"catalog","operation":"search","phase":"started","evidence":"provider_protocol","source":"provider","occurred_at":"2026-09-07T00:00:00Z"}}}`,
		`{"schema":"adapter.stream.v1","event":{"kind":"todo.updated","meta":{"run_id":"remote","sequence":8,"time":"2026-09-07T00:00:00Z"},"todo":{"items":[],"source":"plan_update","revision":1,"occurred_at":"2026-09-07T00:00:00Z"}}}`,
	} {
		events, matched, err := (adapterStreamStatusDecoder{}).DecodeStatusPart(json.RawMessage(raw))
		if err != nil || !matched || len(events) != 1 {
			t.Fatalf("decoded %d facts, matched=%v err=%v", len(events), matched, err)
		}
	}
}

type alignmentRunner struct {
	events []adaptor.Event
	result *adaptor.Result
	err    error
}

func (r alignmentRunner) Run(context.Context, string, ...adaptor.CallOption) (*adaptor.Result, error) {
	return r.result, r.err
}
func (r alignmentRunner) Stream(context.Context, string, ...adaptor.CallOption) adaptor.Stream {
	ch := make(chan adaptor.Event, len(r.events))
	for _, ev := range r.events {
		ch <- adaptor.WithEventMeta(ev, ev.Meta())
	}
	close(ch)
	return &alignmentFactStream{ch: ch, result: r.result, err: r.err}
}

type alignmentFactStream struct {
	ch     <-chan adaptor.Event
	result *adaptor.Result
	err    error
}

func (s *alignmentFactStream) Events() <-chan adaptor.Event     { return s.ch }
func (s *alignmentFactStream) RunID() string                    { return "remote-run" }
func (s *alignmentFactStream) Result() (*adaptor.Result, error) { return s.result, s.err }
func (s *alignmentFactStream) Cancel()                          {}

type alignmentDriver struct {
	run func(context.Context, driver.Request, driver.EventSink) (driver.Response, error)
}

func (d alignmentDriver) Descriptor() driver.Descriptor {
	return driver.Descriptor{Type: "alignment-delegation", MCP: driver.MCPCapability{Supported: true, HTTP: true}, RunPolicyCaps: driver.RunPolicyCapabilities{Permission: driver.HumanDecisionSupport{Ask: true, AutoApprove: true, AutoReject: true, Retry: true}}}
}
func (d alignmentDriver) ValidateConfig(any) error { return nil }
func (d alignmentDriver) Run(ctx context.Context, r driver.Request, s driver.EventSink) (driver.Response, error) {
	return d.run(ctx, r, s)
}

type alignmentObserver struct{ observe adaptor.RunEventObserver }

func (p alignmentObserver) AttachRun(context.Context, string) (adaptor.RunAttachment, error) {
	return adaptor.RunAttachment{Observer: p.observe}, nil
}
func (alignmentObserver) DetachRun(context.Context, string) error { return nil }
func alignmentFact(phase capability.Phase, id, scope string, seq uint64) adaptor.Event {
	v := capability.Invocation{InvocationID: id, Ref: capability.Ref{Kind: capability.MCP, Key: "目录", Operation: "search"}, Phase: phase, Source: capability.Provider, Evidence: capability.ProviderProtocol, ScopeID: scope, OccurredAt: time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)}
	if phase != capability.Started {
		d := time.Duration(0)
		v.Duration = &d
	}
	return adaptor.WithEventMeta(adaptor.CapabilityInvocation{Invocation: v}, adaptor.EventMeta{RunID: "remote-run", Sequence: seq, Time: v.OccurredAt, ThreadKey: "thread/中文", TurnID: "turn", Source: &adaptor.EventSourceMeta{RunID: "provider", Sequence: 99}})
}
func TestAlignmentRelayBoundPublisherBeforeBus(t *testing.T) {
	facts := []adaptor.Event{}
	for i := 0; i < 40; i++ {
		facts = append(facts, alignmentFact(capability.Started, fmt.Sprint(i), "scope", uint64(i*2+1)), alignmentFact(capability.Completed, fmt.Sprint(i), "scope", uint64(i*2+2)))
	}
	member := alignmentRunner{events: facts, result: &adaptor.Result{Text: "member", Summary: "member summary"}}
	team, err := NewService(Config{Agents: []AgentRef{Local("member", member, Policy{})}, NewID: func() string { return "D-A" }})
	if err != nil {
		t.Fatal(err)
	}
	defer team.Close()
	var observed []adaptor.Event
	var old adaptor.RunEventPublisher
	observer := alignmentObserver{observe: func(_ context.Context, info adaptor.RunEventInfo, ev adaptor.Event) error {
		// A synchronous core observer is an acceptance barrier before the lossy bus.
		v := ev.(adaptor.CapabilityInvocation).Invocation
		team.bus.mu.Lock()
		for _, busEvent := range team.bus.replay[info.RunID] {
			if busEvent.Capability != nil && busEvent.Capability.InvocationID == v.InvocationID && busEvent.Capability.Phase == v.Phase {
				t.Error("bus preceded observer")
			}
		}
		team.bus.mu.Unlock()
		observed = append(observed, ev)
		return nil
	}}
	drv := alignmentDriver{run: func(ctx context.Context, req driver.Request, _ driver.EventSink) (driver.Response, error) {
		team.mu.Lock()
		old = team.publishers[req.RunID]
		team.mu.Unlock()
		if !team.Bus().RunEventsBound(req.RunID) {
			t.Error("binding proof missing")
		}
		busyCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		_ = team.Bus().SubscribeRun(busyCtx, req.RunID)
		_, err := team.Delegate(ctx, DelegationRequest{RunID: req.RunID, Agent: "member", ScopeID: "caller-scope", ParentScopeID: "actual-parent-scope", ParentToolCallID: "parent"})
		return driver.Response{Output: "leader"}, err
	}}
	leader := adaptor.New(drv, team.Option(), adaptor.WithRunServices(observer))
	defer leader.Close(context.Background())
	original := leader.Stream(context.Background(), "go")
	stream := original
	var visible []adaptor.Event
	for ev := range stream.Events() {
		visible = append(visible, ev)
	}
	result, err := stream.Result()
	if err != nil || result.Text != "leader" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if len(observed) != 82 {
		t.Fatalf("observed %d facts", len(observed))
	}
	if !team.Bus().RunEventsBound(stream.RunID()) || team.Bus().RunEventsBound(" "+stream.RunID()) {
		t.Fatal("proof not exact/historical")
	}
	if err := old(context.Background(), adaptor.CapabilityInvocation{Invocation: observed[0].(adaptor.CapabilityInvocation).Invocation}); !errors.Is(err, context.Canceled) {
		t.Fatalf("late publisher: %v", err)
	}
	count := 0
	var last uint64
	for _, ev := range visible {
		if ev.Meta().Sequence <= last || ev.Meta().RunID != stream.RunID() {
			t.Fatal("parent sequence authority")
		}
		last = ev.Meta().Sequence
		if v, ok := ev.(adaptor.CapabilityInvocation); ok {
			count++
			if v.Invocation.Source == capability.Relay {
				if ev.Meta().Source.RunID != "remote-run" || ev.Meta().Source.Upstream.Sequence != 99 || v.Invocation.ParentScopeID != "actual-parent-scope" {
					t.Fatalf("source/parent=%+v %+v", ev.Meta(), v.Invocation)
				}
			}
		}
	}
	if count != 82 {
		t.Fatalf("visible facts=%d", count)
	}
	if _, ok := visible[len(visible)-1].(adaptor.RunFinished); !ok {
		t.Fatal("terminal not last")
	}
}
func TestAlignmentRelayBindingAndCopies(t *testing.T) {
	team, err := NewService(Config{Agents: []AgentRef{Local("member", alignmentRunner{}, Policy{})}})
	if err != nil {
		t.Fatal(err)
	}
	defer team.Close()
	runID := " run/中文 "
	a, err := team.AttachRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if a.Events != nil || a.BindEvents == nil || team.Bus().RunEventsBound(runID) {
		t.Fatal("premature/fake binding")
	}
	team.Bus().Publish(DelegationEvent{RunID: runID, Kind: DelegationStatus})
	if team.Bus().RunEventsBound(runID) {
		t.Fatal("Publish forged binding")
	}
	if a.BindEvents(nil) == nil || team.Bus().RunEventsBound(runID) {
		t.Fatal("failed Bind forged proof")
	}
	if err := a.BindEvents(func(context.Context, adaptor.Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := team.DetachRun(context.Background(), runID); err != nil {
		t.Fatal(err)
	}
	if !team.Bus().RunEventsBound(runID) || team.Bus().RunEventsBound(strings.TrimSpace(runID)) {
		t.Fatal("proof revoked/normalized")
	}
	ev, _ := delegationEventFromAdapterEvent(alignmentFact(capability.Completed, "x", "scope", 7))
	copy := cloneDelegationEvent(ev)
	*copy.Capability.Duration = time.Hour
	copy.Source.Upstream.Sequence = 123
	if *ev.Capability.Duration != 0 || ev.Source.Upstream.Sequence != 99 {
		t.Fatal("clone shared duration/source")
	}
}
func TestAlignmentRelayDomainsReplayAndLoss(t *testing.T) {
	for _, delegation := range []string{"D/A", "D:B"} {
		base := DelegationEvent{RunID: "leader", DelegationID: delegation, ScopeID: "caller", ParentScopeID: "parent scope", ParentToolCallID: "parent"}
		m := newEventMapper(base)
		for _, scope := range []string{"", "scope/中文\\\""} {
			raw, _ := delegationEventFromAdapterEvent(alignmentFact(capability.Started, "call", scope, 7))
			mapped := m.completeStatusDelegationEvent("task", "context", "message", "adapter.stream.v1", raw)
			want := alignmentTuple("invocation", delegation, "remote-run", scope, "call")
			if mapped.Capability.InvocationID != want || mapped.ScopeID != alignmentTuple("scope", delegation, "remote-run", scope) || mapped.Capability.ParentScopeID != "parent scope" {
				t.Fatalf("tuple %+v", mapped)
			}
		}
	}
	m := newEventMapper(DelegationEvent{RunID: "leader", DelegationID: "D"})
	frame, _ := encodeLocalFact(alignmentFact(capability.Started, "call", "scope", 7))
	message := clienta2a.Message{ID: "message", Parts: []clienta2a.Part{{Kind: clienta2a.PartData, Data: map[string]any{"schema": "adapter.stream.v1", "event": frame}}}}
	if out := m.statusPartEvents("task", "ctx", message); len(out) != 1 || out[0].Capability == nil {
		t.Fatal("first fact missing")
	}
	message.ID = "recovered"
	if out := m.statusPartEvents("task", "ctx", message); len(out) != 0 {
		t.Fatal("replay duplicated")
	}
	frame["capability"].(map[string]any)["key"] = "other"
	if out := m.statusPartEvents("task", "ctx", message); len(out) != 1 || out[0].Kind != DelegationStreamDropped || out[0].Raw["reason"] != "invalid_payload" {
		t.Fatalf("conflict=%+v", out)
	}
	for _, depth := range []int{0, 8} {
		ev, _ := delegationEventFromAdapterEvent(alignmentFact(capability.Started, strings.Repeat("x", 2048), "scope", 10))
		if depth == 8 {
			ev.Capability.InvocationID = "call"
			ev.Source.Upstream = nil
			tail := ev.Source
			for i := 1; i < 9; i++ {
				tail.Upstream = &adaptor.EventSourceMeta{RunID: "up"}
				tail = tail.Upstream
			}
		}
		got := mapRelay(ev, DelegationEvent{DelegationID: "D"})
		if got.Kind != DelegationStreamDropped || got.Capability != nil || got.Source != nil {
			t.Fatal("unrepresentable relay accepted")
		}
	}
}
func alignmentTuple(parts ...string) string {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(parts)
	return "aa1:" + base64.RawURLEncoding.EncodeToString(bytes.TrimSuffix(out.Bytes(), []byte("\n")))
}

func TestAlignmentObserverTimeoutStaysWithinOneRun(t *testing.T) {
	team, err := NewService(Config{Agents: []AgentRef{Local("member", alignmentRunner{result: &adaptor.Result{Text: "done"}}, Policy{})}})
	if err != nil {
		t.Fatal(err)
	}
	defer team.Close()
	release := make(chan struct{})
	late := make(chan struct{})
	var mu sync.Mutex
	first := ""
	counts := map[string]int{}
	obs := alignmentObserver{observe: func(_ context.Context, info adaptor.RunEventInfo, _ adaptor.Event) error {
		mu.Lock()
		if first == "" {
			first = info.RunID
		}
		counts[info.RunID]++
		blocked := info.RunID == first
		mu.Unlock()
		if blocked {
			<-release
			close(late)
			return errors.New("late secret")
		}
		return nil
	}}
	leader := adaptor.New(alignmentDriver{run: func(ctx context.Context, req driver.Request, _ driver.EventSink) (driver.Response, error) {
		_, err := team.Delegate(ctx, DelegationRequest{RunID: req.RunID, Agent: "member"})
		return driver.Response{Output: "done"}, err
	}}, team.Option(), adaptor.WithRunServices(obs))
	defer leader.Close(context.Background())
	for i := 0; i < 2; i++ {
		stream := leader.Stream(context.Background(), "go")
		notices := 0
		for ev := range stream.Events() {
			if n, ok := ev.(adaptor.Notice); ok && n.Data["code"] == "observation_disabled" {
				notices++
				if n.Data["reason"] != "timeout" {
					t.Fatal("wrong safe reason")
				}
			}
		}
		result, err := stream.Result()
		if err != nil || result.Text != "done" {
			t.Fatalf("observer changed outcome: %v", err)
		}
		mu.Lock()
		count := counts[stream.RunID()]
		mu.Unlock()
		if i == 0 && (count != 1 || notices != 1) {
			t.Fatalf("first callbacks=%d notices=%d", count, notices)
		}
		if i == 1 && (count != 2 || notices != 0) {
			t.Fatalf("second callbacks=%d notices=%d", count, notices)
		}
	}
	close(release)
	select {
	case <-late:
	case <-time.After(time.Second):
		t.Fatal("late callback did not finish")
	}
}
func TestAlignmentRelayToolParentTodoClearAndNestedSource(t *testing.T) {
	at := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	meta := adaptor.EventMeta{RunID: "member", Sequence: 11, Time: at, Source: &adaptor.EventSourceMeta{RunID: "upstream", Sequence: 99, ScopeID: "original-scope", ToolCallID: "original-tool", InvocationID: "original-call", DelegationID: "upstream-D"}}
	for _, scope := range []string{"A", "B"} {
		tool := adaptor.WithEventMeta(adaptor.ToolCall{ID: "x", ScopeID: scope, ParentScopeID: "different-parent-scope", ParentToolCallID: "p", Name: "Explore", Phase: adaptor.PhaseStart, Args: map[string]any{"x": 1}}, meta)
		decoded, _ := delegationEventFromAdapterEvent(tool)
		mapped := mapRelay(decoded, DelegationEvent{DelegationID: "D"})
		if mapped.RemoteToolCallID != alignmentTuple("tool", "D", "member", scope, "x") || mapped.ParentToolCallID != alignmentTuple("tool", "D", "member", "different-parent-scope", "p") || mapped.ParentScopeID != alignmentTuple("scope", "D", "member", "different-parent-scope") || mapped.Capability != nil {
			t.Fatalf("wrong tool parent=%+v", mapped)
		}
	}
	for _, items := range [][]todo.Item{{{ID: "synthetic:known", Content: "plan", Status: todo.Pending, SyntheticID: true}}, {}} {
		snapshot := todo.Snapshot{Items: items, Source: todo.PlanUpdate, ScopeID: "A", Revision: 2, OccurredAt: at}
		ev := adaptor.WithEventMeta(adaptor.TodoUpdated{Snapshot: snapshot}, meta)
		frame, _ := encodeLocalFact(ev)
		decoded, _, err := (adapterStreamStatusDecoder{}).DecodeStatusPart(map[string]any{"schema": "adapter.stream.v1", "event": frame})
		if err != nil || len(decoded) != 1 {
			t.Fatal(err)
		}
		first := mapRelay(decoded[0], DelegationEvent{DelegationID: "D1"})
		if first.Todo.Items == nil || len(first.Todo.Items) != len(items) || first.Todo.Revision != 2 {
			t.Fatal("snapshot/clear lost")
		}
		core := adaptor.WithEventMeta(coreDelegationEvent(first), adaptor.EventMeta{RunID: "middle", Sequence: 20, Time: at, Source: first.Source})
		next, _ := delegationEventFromAdapterEvent(core)
		second := mapRelay(next, DelegationEvent{DelegationID: "D2"})
		if second.Source.RunID != "middle" || second.Source.Sequence != 20 || second.Source.Upstream.RunID != "member" || second.Source.Upstream.Sequence != 11 || second.Source.Upstream.Upstream.DelegationID != "upstream-D" {
			t.Fatalf("nested chain=%+v", second.Source)
		}
		copy := cloneDelegationEvent(second)
		if len(items) > 0 {
			copy.Todo.Items[0].Content = "mutated"
			if second.Todo.Items[0].Content != "plan" {
				t.Fatal("todo copy shared")
			}
		}
	}
}
func TestAlignmentBeforeAndAfterControlOuterLifecycle(t *testing.T) {
	for _, mode := range []string{"reject", "cancel_after_before", "after_failure", "after_secondary"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ioCalls := 0
			primary := &DelegationError{Code: "approval_denied", Message: "denied", Cause: adaptor.ErrApprovalDenied}
			c := &alignmentClient{card: func(context.Context) (clienta2a.AgentCard, error) { ioCalls++; return clienta2a.AgentCard{}, nil }, send: func(context.Context, clienta2a.SendRequest) (clienta2a.Task, error) {
				if mode == "after_secondary" {
					return clienta2a.Task{ID: "task", Status: incomingFailure(map[string]any{"code": "approval_denied"}, clienta2a.TaskStateFailed)}, nil
				}
				return clienta2a.Task{ID: "task", Status: clienta2a.TaskStatus{State: clienta2a.TaskStateCompleted}}, nil
			}}
			d := alignmentDelegator(t, DelegationPolicy{}, c)
			afterErr := errors.New("after failure")
			d.LifecycleHook = DelegationLifecycleHookFuncs{BeforeFunc: func(context.Context, BeforeDelegation) error {
				if mode == "reject" {
					return primary
				}
				if mode == "cancel_after_before" {
					cancel()
				}
				return nil
			}, AfterFunc: func(context.Context, AfterDelegation) error { return afterErr }}
			out, err := d.Delegate(ctx, DelegationRequest{RunID: "leader", Agent: "member"})
			if err == nil {
				t.Fatal("failure lost")
			}
			facts := []capability.Invocation{}
			for _, ev := range drainAvailableBus(t, d.Bus, "leader") {
				if ev.Capability != nil {
					facts = append(facts, *ev.Capability)
				}
			}
			if mode == "reject" || mode == "cancel_after_before" {
				if len(facts) != 0 || ioCalls != 0 {
					t.Fatal("Before emitted facts or IO")
				}
				return
			}
			if len(facts) != 2 || facts[0].Phase != capability.Started || facts[1].Phase != capability.Failed || facts[0].InvocationID != facts[1].InvocationID {
				t.Fatalf("outer lifecycle=%+v", facts)
			}
			if mode == "after_failure" && out.Error.Code != "workflow_after_failed" {
				t.Fatal("After did not affect final")
			}
			if mode == "after_secondary" && (out.Error.Code != "approval_denied" || !errors.Is(err, afterErr)) {
				t.Fatal("After overwrote primary or dropped cause")
			}
		})
	}
}

func TestAlignmentLocalInvalidStringAndPublisherFailure(t *testing.T) {
	bad := alignmentFact(capability.Started, "call", "scope", 7).(adaptor.CapabilityInvocation)
	bad.Invocation.Ref.Key = string([]byte{0xff})
	frame, _ := encodeLocalFact(bad)
	m := newEventMapper(DelegationEvent{RunID: "leader", DelegationID: "D"})
	events := m.statusPartEvents("task", "ctx", clienta2a.Message{ID: "bad", Parts: []clienta2a.Part{{Kind: clienta2a.PartData, Data: map[string]any{"schema": "adapter.stream.v1", "event": frame}}}})
	if len(events) != 1 || events[0].Kind != DelegationStreamDropped || events[0].Capability != nil {
		t.Fatalf("malformed UTF-8 repaired: %+v", events)
	}
	for _, terminalOnly := range []bool{false, true} {
		sentinel := errors.New("publisher rejected")
		primary := errors.New("primary")
		calls := 0
		d := alignmentDelegator(t, DelegationPolicy{}, &alignmentClient{})
		d.publishRun = func(_ context.Context, ev DelegationEvent) error {
			if !terminalOnly || isTerminal(ev.Kind) {
				return sentinel
			}
			return nil
		}
		d.LifecycleHook = DelegationLifecycleHookFuncs{AfterFunc: func(context.Context, AfterDelegation) error { calls++; return primary }}
		out, err := d.Delegate(context.Background(), DelegationRequest{RunID: "leader", Agent: "member"})
		if !errors.Is(err, sentinel) || out.Error == nil {
			t.Fatalf("publication disappeared: %+v %v", out, err)
		}
		if terminalOnly && (!errors.Is(err, primary) || out.Error.Code != "workflow_after_failed" || calls != 1) {
			t.Fatalf("primary lost: %+v %v", out, err)
		}
	}
}
