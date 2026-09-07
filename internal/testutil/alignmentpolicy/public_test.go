package alignmentpolicy_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	a2aproto "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/errordetails"

	adaptor "github.com/agent-dance/agent-adaptor"
	bridge "github.com/agent-dance/agent-adaptor/bridges/a2a"
	client "github.com/agent-dance/agent-adaptor/clients/a2a"
	"github.com/agent-dance/agent-adaptor/driver"
	delegation "github.com/agent-dance/agent-adaptor/hosttools/a2adelegation"
	recorder "github.com/agent-dance/agent-adaptor/hosttools/sessionrecorder"
	qa "github.com/agent-dance/agent-adaptor/internal/testutil/alignmentpolicy"
)

func testContext(t *testing.T) context.Context {
	ctx, c := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(c)
	return ctx
}

type service struct {
	f func(context.Context) (adaptor.RunAttachment, error)
}

func (s service) AttachRun(ctx context.Context, _ string) (adaptor.RunAttachment, error) {
	return s.f(ctx)
}
func (service) DetachRun(context.Context, string) error { return nil }

type fixedStream struct {
	id     string
	es     chan adaptor.Event
	r      *adaptor.Result
	err    error
	once   sync.Once
	cancel func()
}

func (s *fixedStream) Events() <-chan adaptor.Event     { return s.es }
func (s *fixedStream) RunID() string                    { return s.id }
func (s *fixedStream) Result() (*adaptor.Result, error) { return s.r, s.err }
func (s *fixedStream) Cancel() {
	s.once.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
	})
}

type fixedRunner struct {
	calls      atomic.Int32
	makeStream func(context.Context) adaptor.Stream
}

func (r *fixedRunner) Run(context.Context, string, ...adaptor.CallOption) (*adaptor.Result, error) {
	panic("R016 must use exactly one Stream, never Runner.Run")
}
func (r *fixedRunner) Stream(ctx context.Context, _ string, _ ...adaptor.CallOption) adaptor.Stream {
	r.calls.Add(1)
	return r.makeStream(ctx)
}
func closedStream(id string, es []adaptor.Event, r *adaptor.Result, err error) adaptor.Stream {
	ch := make(chan adaptor.Event, len(es))
	for _, e := range es {
		ch <- e
	}
	close(ch)
	return &fixedStream{id: id, es: ch, r: r, err: err}
}
func hint(metaID, bodyID string, failed bool, reason adaptor.FailureReason) adaptor.Event {
	return adaptor.WithEventMeta(adaptor.RunFinished{RunID: bodyID, Failed: failed, Reason: reason}, adaptor.EventMeta{RunID: metaID, Sequence: 2, Time: time.Date(2026, 9, 7, 1, 2, 3, 0, time.UTC)})
}
func request() client.SendRequest {
	return client.SendRequest{Message: client.Message{ID: "t22-message", Role: "user", Parts: []client.Part{{Kind: client.PartText, Text: "private test input"}}}}
}
func remote(t *testing.T, r adaptor.Runner) *client.Client {
	t.Helper()
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	b := bridge.NewServer(r, bridge.ServerOptions{AgentCard: bridge.AgentCard{Name: "T22 QA", Version: "1", URL: server.URL + "/a2a"}})
	mux.Handle("/a2a", b.Handler())
	mux.Handle("/.well-known/agent-card.json", b.AgentCardHandler())
	return client.New(client.Options{AgentCardURL: server.URL, HTTPClient: server.Client()})
}
func remoteTask(t *testing.T, c *client.Client, stream bool) client.Task {
	t.Helper()
	ctx := testContext(t)
	if !stream {
		task, err := c.Send(ctx, request())
		if err != nil {
			t.Fatal(err)
		}
		return task
	}
	s, err := c.SendStream(ctx, request())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var result client.Task
	for {
		e, err := s.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if e.Task != nil {
			result = *e.Task
		}
		if e.Status != nil {
			result.Status = *e.Status
		}
		if e.TaskID != "" {
			result.ID = e.TaskID
		}
	}
	return result
}
func control(task client.Task) (string, any) {
	if task.Status.Message != nil {
		for _, part := range task.Status.Message.Parts {
			if raw, ok := part.Metadata["agentadaptor.failure"].(map[string]any); ok {
				code, _ := raw["code"].(string)
				return code, raw["limit_ms"]
			}
		}
	}
	return "", nil
}
func assertTask(t *testing.T, task client.Task, code string, limit bool) {
	t.Helper()
	wantState := client.TaskStateFailed
	if code == "cancelled" {
		wantState = client.TaskStateCanceled
	}
	if code == "success" {
		wantState = client.TaskStateCompleted
		code = ""
	}
	got, value := control(task)
	if task.Status.State != wantState || got != code || (value != nil) != limit {
		t.Fatalf("state=%s control=%s/%v want=%s/%s/limit=%v", task.Status.State, got, value, wantState, code, limit)
	}
	encoded, _ := json.Marshal(task)
	if strings.Contains(string(encoded), "SECRET_PAYLOAD") {
		t.Fatal("private diagnostic leaked on safe wire")
	}
}

func TestT22R016HintQualification(t *testing.T) {
	active := &adaptor.ActiveExecutionTimeoutError{Limit: 100 * time.Millisecond}
	bare := errors.Join(active, context.Canceled)
	cases := []struct {
		name, id string
		events   []adaptor.Event
		err      error
		code     string
		limit    bool
	}{
		{"different-body", "r", []adaptor.Event{hint("r", "provider-other", true, adaptor.ReasonCancelled)}, bare, "cancelled", false},
		{"empty-body", "r", []adaptor.Event{hint("r", "", true, adaptor.ReasonCancelled)}, bare, "cancelled", false},
		{"missing-meta", "r", []adaptor.Event{hint("", "r", true, adaptor.ReasonCancelled)}, bare, "active_execution_timeout", true},
		{"foreign-meta", "r", []adaptor.Event{hint("other", "r", true, adaptor.ReasonCancelled)}, bare, "active_execution_timeout", true},
		{"empty-stream", "", []adaptor.Event{hint("r", "r", true, adaptor.ReasonCancelled)}, bare, "active_execution_timeout", true},
		{"no-terminal", "r", nil, bare, "active_execution_timeout", true},
		{"not-failed", "r", []adaptor.Event{hint("r", "", false, adaptor.ReasonCancelled)}, bare, "active_execution_timeout", true},
		{"unknown-reason", "r", []adaptor.Event{hint("r", "", true, "private_reason")}, bare, "active_execution_timeout", true},
		{"empty-reason", "r", []adaptor.Event{hint("r", "", true, "")}, bare, "active_execution_timeout", true},
		{"duplicate", "r", []adaptor.Event{hint("r", "", true, adaptor.ReasonCancelled), hint("r", "", true, adaptor.ReasonCancelled)}, bare, "active_execution_timeout", true},
		{"conflict", "r", []adaptor.Event{hint("r", "", true, adaptor.ReasonCancelled), hint("r", "", true, adaptor.ReasonApprovalDenied)}, bare, "active_execution_timeout", true},
		{"not-last", "r", []adaptor.Event{hint("r", "", true, adaptor.ReasonCancelled), adaptor.TextDelta{Text: "tail"}}, bare, "active_execution_timeout", true},
		{"error-only", "r", []adaptor.Event{hint("r", "", true, adaptor.ReasonActiveExecutionTimeout)}, nil, "success", false},
		{"carrier-priority", "r", []adaptor.Event{hint("r", "", true, adaptor.ReasonActiveExecutionTimeout)}, &adaptor.RunError{Reason: adaptor.ReasonApprovalDenied, Result: &adaptor.Result{Text: "partial"}, Cause: errors.Join(context.Canceled, errors.New("SECRET_PAYLOAD"))}, "approval_denied", false},
		{"unknown-carrier", "r", []adaptor.Event{hint("r", "", true, adaptor.ReasonActiveExecutionTimeout)}, &adaptor.RunError{Reason: "private_reason", Result: &adaptor.Result{}, Cause: bare}, "", false},
		{"empty-carrier", "r", []adaptor.Event{hint("r", "", true, adaptor.ReasonActiveExecutionTimeout)}, &adaptor.RunError{Result: &adaptor.Result{}, Cause: bare}, "", false},
	}
	for _, tc := range cases {
		for _, stream := range []bool{false, true} {
			for _, local := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/stream=%v/local=%v", tc.name, stream, local), func(t *testing.T) {
					r := &fixedRunner{makeStream: func(context.Context) adaptor.Stream {
						var result *adaptor.Result
						if tc.err == nil {
							result = &adaptor.Result{Text: "success"}
						}
						return closedStream(tc.id, tc.events, result, tc.err)
					}}
					if local {
						team, err := delegation.NewService(delegation.Config{Agents: []delegation.AgentRef{delegation.Local("member", r, delegation.Policy{})}})
						if err != nil {
							t.Fatal(err)
						}
						defer team.Close()
						out, err := team.Delegate(testContext(t), delegation.DelegationRequest{RunID: "leader", Agent: "member", Prompt: "qualification", Stream: stream})
						if tc.code == "success" {
							if err != nil || out.Error != nil {
								t.Fatalf("success became failure: %v", err)
							}
						} else {
							var de *delegation.DelegationError
							want := tc.code
							if want == "" {
								want = "remote_failed"
							}
							if !errors.As(err, &de) || de.Code != want || (de.Metadata["limit_ms"] != nil) != tc.limit {
								t.Fatalf("local code/limit = %#v, %v; want %s/%v", out, err, want, tc.limit)
							}
							if !errors.Is(err, tc.err) {
								t.Fatalf("local lost original error: %v", err)
							}
						}
					} else {
						task := remoteTask(t, remote(t, r), stream)
						assertTask(t, task, tc.code, tc.limit)
					}
					if r.calls.Load() != 1 {
						t.Fatal("bridge re-executed Runner")
					}
					var carrier *adaptor.RunError
					if errors.As(tc.err, &carrier) && carrier != nil {
						if carrier.Cause == nil || carrier.Result == nil {
							t.Fatal("original carrier mutated")
						}
					}
				})
			}
		}
	}
}

type traceRunner struct {
	localFirst  bool
	calls       atomic.Int32
	driverCalls atomic.Int32
	mu          sync.Mutex
	events      []adaptor.Event
	result      *adaptor.Result
	err         error
	foreign     *adaptor.ActiveExecutionTimeoutError
}

func (r *traceRunner) Run(context.Context, string, ...adaptor.CallOption) (*adaptor.Result, error) {
	panic("only Stream authorized")
}
func (r *traceRunner) Stream(ctx context.Context, p string, opts ...adaptor.CallOption) adaptor.Stream {
	r.calls.Add(1)
	parent, cancel := context.WithCancelCause(ctx)
	foreign := &adaptor.ActiveExecutionTimeoutError{Limit: 777 * time.Second}
	d := qa.NewDriver()
	a := adaptor.New(d, adaptor.WithPolicy(adaptor.Policy{ActiveExecutionTimeout: 100 * time.Millisecond}), adaptor.WithRunServices(service{func(ctx context.Context) (adaptor.RunAttachment, error) {
		if r.localFirst {
			<-ctx.Done()
		}
		cancel(foreign)
		return adaptor.RunAttachment{}, errors.Join(ctx.Err(), context.Cause(ctx), foreign)
	}}))
	base := a.Stream(parent, p, opts...)
	out := make(chan adaptor.Event, 32)
	go func() {
		for e := range base.Events() {
			r.mu.Lock()
			r.events = append(r.events, e)
			r.mu.Unlock()
			out <- e
		}
		result, err := base.Result()
		r.mu.Lock()
		r.result = result
		r.err = err
		r.foreign = foreign
		r.driverCalls.Store(d.Calls.Load())
		r.mu.Unlock()
		cancel(nil)
		close(out)
	}()
	return &tracedStream{Stream: base, events: out}
}

type tracedStream struct {
	adaptor.Stream
	events <-chan adaptor.Event
}

func (s *tracedStream) Events() <-chan adaptor.Event { return s.events }
func (r *traceRunner) check(t *testing.T) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	var re *adaptor.RunError
	if r.calls.Load() != 1 || r.driverCalls.Load() != 0 || r.result != nil || errors.As(r.err, &re) || !errors.Is(r.err, context.Canceled) || !errors.Is(r.err, adaptor.ErrActiveExecutionTimeout) || !errors.Is(r.err, r.foreign) {
		t.Fatalf("pre-driver boundary result=%#v err=%v calls=%d/%d", r.result, r.err, r.calls.Load(), r.driverCalls.Load())
	}
	want := adaptor.ReasonCancelled
	if r.localFirst {
		want = adaptor.ReasonActiveExecutionTimeout
	}
	n := 0
	for i, e := range r.events {
		if f, ok := e.(adaptor.RunFinished); ok {
			n++
			if !f.Failed || f.Reason != want || i != len(r.events)-1 || f.Meta().RunID == "" {
				t.Fatalf("pre-driver terminal %#v", f)
			}
		}
	}
	if n != 1 {
		t.Fatalf("terminal count=%d", n)
	}
}
func TestT22R016RealCoreSources(t *testing.T) {
	for _, localFirst := range []bool{false, true} {
		for _, local := range []bool{false, true} {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("activeFirst=%v/local=%v/stream=%v", localFirst, local, stream), func(t *testing.T) {
					r := &traceRunner{localFirst: localFirst}
					code := "cancelled"
					if localFirst {
						code = "active_execution_timeout"
					}
					if local {
						team, err := delegation.NewService(delegation.Config{Agents: []delegation.AgentRef{delegation.Local("member", r, delegation.Policy{})}})
						if err != nil {
							t.Fatal(err)
						}
						defer team.Close()
						out, err := team.Delegate(testContext(t), delegation.DelegationRequest{RunID: "leader", Agent: "member", Prompt: "source", Stream: stream})
						var de *delegation.DelegationError
						if !errors.As(err, &de) || de.Code != code || out.Error == nil {
							t.Fatalf("local outcome=%#v err=%v", out, err)
						}
						if (de.Metadata["limit_ms"] != nil) != localFirst {
							t.Fatalf("limit source=%#v", de.Metadata)
						}
					} else {
						task := remoteTask(t, remote(t, r), stream)
						assertTask(t, task, code, localFirst)
						if localFirst {
							_, limit := control(task)
							if fmt.Sprint(limit) != "100" {
								t.Fatalf("wrong primary limit=%v", limit)
							}
						}
					}
					r.check(t)
				})
			}
		}
	}
}

func TestT22RecorderDescriptiveReplay(t *testing.T) {
	d := qa.NewDriver()
	var live *adaptor.ApprovalRequest
	records := recorder.NewEventRecorder(recorder.NewMemoryEventBackend())
	defer records.Close()
	d.RunFunc = func(ctx context.Context, _ driver.Request, s driver.EventSink) (driver.Response, error) {
		_, err := s.(driver.DecisionCapableSink).RequestDecision(ctx, driver.DecisionRequest{Kind: driver.HumanDecisionQuestion, Choices: []driver.DecisionChoice{{Key: "yes"}}, Payload: map[string]any{"nested": []any{map[string]any{"value": "before"}}}})
		return qa.Response(), err
	}
	a := adaptor.New(d, adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalPolicy{Question: adaptor.QuestionAsk}}))
	s := a.Stream(testContext(t), "record")
	for e := range s.Events() {
		if req, ok := e.(*adaptor.ApprovalRequest); ok {
			live = req
			if _, err := records.Record(context.Background(), "history", req); err != nil {
				t.Fatal(err)
			}
			req.Choices[0].Key = "after"
			req.Details["nested"].([]any)[0].(map[string]any)["value"] = "after"
			if err := req.Answer(context.Background(), "yes"); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := s.Result(); err != nil {
		t.Fatal(err)
	}
	rs, err := records.Since(context.Background(), "history", 0)
	if err != nil || len(rs) != 1 {
		t.Fatalf("record=%#v %v", rs, err)
	}
	b, err := json.Marshal(rs[0])
	if err != nil {
		t.Fatal(err)
	}
	var replay recorder.EventRecord
	if err = json.Unmarshal(b, &replay); err != nil {
		t.Fatal(err)
	}
	r := replay.Event.(*adaptor.ApprovalRequest)
	if r.Choices[0].Key != "yes" || r.Details["nested"].([]any)[0].(map[string]any)["value"] != "before" {
		t.Fatal("record shared live mutable description")
	}
	if !errors.Is(r.Answer(context.Background(), "yes"), adaptor.ErrApprovalUnavailable) || !errors.Is(live.Answer(context.Background(), "yes"), adaptor.ErrApprovalResolved) {
		t.Fatal("replay answer right differs from live exactly-once state")
	}
}

type budgetClient struct {
	mode                  string
	cards, sends, cancels atomic.Int32
	cancelID              string
	detached              bool
}

func (c *budgetClient) AgentCard(ctx context.Context) (client.AgentCard, error) {
	c.cards.Add(1)
	if c.mode == "initialize" {
		<-ctx.Done()
		return client.AgentCard{}, ctx.Err()
	}
	return client.AgentCard{Capabilities: client.Capabilities{Streaming: false}}, nil
}
func (c *budgetClient) Send(ctx context.Context, r client.SendRequest) (client.Task, error) {
	c.sends.Add(1)
	if c.mode == "wait" {
		<-ctx.Done()
		return client.Task{}, ctx.Err()
	}
	return client.Task{ID: "known", ContextID: "ctx", Status: client.TaskStatus{State: client.TaskStateCompleted}}, nil
}
func (c *budgetClient) SendStream(context.Context, client.SendRequest) (delegation.A2AStream, error) {
	return nil, errors.New("stream not requested")
}
func (c *budgetClient) GetTask(ctx context.Context, _ client.GetTaskRequest) (client.Task, error) {
	<-ctx.Done()
	return client.Task{}, ctx.Err()
}
func (c *budgetClient) CancelTask(ctx context.Context, r client.CancelTaskRequest) (client.Task, error) {
	c.cancels.Add(1)
	c.cancelID = r.TaskID
	deadline, ok := ctx.Deadline()
	c.detached = ctx.Err() == nil && ok && time.Until(deadline) > 0 && time.Until(deadline) <= 5*time.Second
	return client.Task{}, errors.New("SECRET_PAYLOAD cancel diagnostic")
}
func TestT22DelegationOwnBudgetAndKnownCancellation(t *testing.T) {
	for _, mode := range []string{"initialize", "before", "wall-clock", "negative", "continuation"} {
		t.Run(mode, func(t *testing.T) {
			fc := &budgetClient{mode: "initialize"}
			policy := delegation.Policy{MaxActiveExecutionTimeout: 30 * time.Millisecond}
			if mode == "wall-clock" {
				fc.mode = "wait"
				policy.MaxActiveExecutionTimeout = time.Second
			}
			if mode == "negative" || mode == "continuation" {
				fc.mode = "success"
				policy.MaxActiveExecutionTimeout = time.Second
			}
			var before atomic.Int32
			reg, err := delegation.NewRegistry(delegation.RemoteAgentSpec{Key: "remote", AgentCardURL: "http://127.0.0.1/unused", Policy: policy})
			if err != nil {
				t.Fatal(err)
			}
			d := delegation.NewDelegator(reg, nil)
			d.NewClient = func(delegation.RemoteAgentSpec) delegation.A2AClient { return fc }
			d.LifecycleHook = delegation.DelegationLifecycleHookFuncs{BeforeFunc: func(ctx context.Context, _ delegation.BeforeDelegation) error {
				before.Add(1)
				if mode == "before" {
					<-ctx.Done()
					return ctx.Err()
				}
				return nil
			}, AfterFunc: func(context.Context, delegation.AfterDelegation) error { return nil }}
			req := delegation.DelegationRequest{Agent: "remote", RunID: "leader", Prompt: "budget", ActiveExecutionTimeout: time.Second, Message: &client.Message{ID: "follow-up", TaskID: "known", Role: "user", Parts: []client.Part{{Kind: client.PartText, Text: "continue"}}}}
			if mode == "negative" {
				req.ActiveExecutionTimeout = -1
			}
			if mode == "wall-clock" {
				req.Timeout = 20 * time.Millisecond
			}
			result, err := d.Delegate(testContext(t), req)
			if mode == "continuation" {
				if err != nil {
					t.Fatal(err)
				}
				second, err := d.Delegate(testContext(t), req)
				if err != nil || result.DelegationID == second.DelegationID || fc.sends.Load() != 2 {
					t.Fatalf("continuation not independent: %v", err)
				}
				return
			}
			var de *delegation.DelegationError
			if !errors.As(err, &de) {
				t.Fatalf("missing delegation error: %v", err)
			}
			want := "active_execution_timeout"
			if mode == "wall-clock" {
				want = "deadline_exceeded"
			}
			if mode == "negative" {
				want = "invalid_policy"
			}
			if de.Code != want {
				t.Fatalf("code=%s want=%s", de.Code, want)
			}
			if mode == "negative" {
				if before.Load() != 0 || fc.cards.Load() != 0 || fc.cancels.Load() != 0 {
					t.Fatal("negative policy performed lifecycle/IO")
				}
				return
			}
			if fc.cancels.Load() != 1 || fc.cancelID != "known" || !fc.detached {
				t.Fatalf("known-task cleanup=%d %q detached=%v", fc.cancels.Load(), fc.cancelID, fc.detached)
			}
			if result.Metadata["remote_cancel"] != "failed" {
				t.Fatalf("cleanup diagnostic absent: %#v", result.Metadata)
			}
			if de.Retryable {
				t.Fatal("budget/deadline error gained retry")
			}
			if want == "active_execution_timeout" {
				var b *adaptor.ActiveExecutionTimeoutError
				if !errors.As(err, &b) || b.Limit != 30*time.Millisecond {
					t.Fatalf("clamped limit=%#v err=%v", b, err)
				}
			}
			b, _ := json.Marshal(result)
			if strings.Contains(string(b), "SECRET_PAYLOAD") {
				t.Fatal("cleanup error leaked")
			}
		})
	}
}

// Events reveals the tail only when a second read starts after cancellation.
// The first select has already evaluated Events while the context is healthy;
// therefore the explicit CancelTask case must pass through the drain path.
type drainStream struct {
	ctx                                            context.Context
	translate                                      bool
	es                                             chan adaptor.Event
	started, cancelSeen, tailPublished, resultDone chan struct{}
	first, cancelled, released, resultFinished     sync.Once
	reads                                          atomic.Int32
	resultReads                                    atomic.Int32
	earlyResult                                    atomic.Bool
	err                                            error
}

func (s *drainStream) RunID() string { return "drain-run" }
func (s *drainStream) Events() <-chan adaptor.Event {
	s.reads.Add(1)
	s.first.Do(func() { close(s.started) })
	ready := s.ctx.Err() != nil
	if s.translate {
		select {
		case <-s.cancelSeen:
			ready = true
		default:
		}
	}
	if ready {
		s.released.Do(func() {
			s.es <- hint("drain-run", "provider-other", true, adaptor.ReasonApprovalDenied)
			close(s.es)
			close(s.tailPublished)
		})
	}
	return s.es
}
func (s *drainStream) Cancel() { s.cancelled.Do(func() { close(s.cancelSeen) }) }
func (s *drainStream) Result() (*adaptor.Result, error) {
	s.resultReads.Add(1)
	defer s.resultFinished.Do(func() { close(s.resultDone) })
	// tailPublished proves producer closure, not consumer progress. The tail
	// is buffered deliberately, so Result must also observe an empty channel.
	select {
	case <-s.tailPublished:
		if len(s.es) == 0 {
			if s.err != nil {
				return nil, s.err
			}
			return nil, context.Canceled
		}
	default:
	}
	// Record a test-observable violation: public CancelTask ACK/EOF can hide
	// the returned error, so integrations must inspect this flag directly.
	s.earlyResult.Store(true)
	return nil, errors.New("Result read before full drain")
}
func (s *drainStream) awaitResult(t *testing.T, ctx context.Context) {
	t.Helper()
	select {
	case <-s.resultDone:
	case <-ctx.Done():
		t.Fatal("Result call did not complete after drain")
	}
	if s.earlyResult.Load() {
		t.Fatal("consumer called Result before consuming the closed event channel")
	}
}

func TestT22R016CancellationAndTranslationDrain(t *testing.T) {
	for _, translate := range []bool{false, true} {
		t.Run(fmt.Sprintf("translation=%v", translate), func(t *testing.T) {
			ctx := testContext(t)
			holder := make(chan *drainStream, 1)
			r := &fixedRunner{makeStream: func(runCtx context.Context) adaptor.Stream {
				s := &drainStream{ctx: runCtx, translate: translate, es: make(chan adaptor.Event, 2), started: make(chan struct{}), cancelSeen: make(chan struct{}), tailPublished: make(chan struct{}), resultDone: make(chan struct{})}
				if translate {
					s.es <- adaptor.WithEventMeta(adaptor.TextDelta{Text: "text", Phase: adaptor.PhaseContent, Role: adaptor.RoleAssistant}, adaptor.EventMeta{RunID: "drain-run", Sequence: 1 << 54})
				}
				holder <- s
				return s
			}}
			c := remote(t, r)
			stream, err := c.SendStream(ctx, request())
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			var task client.Task
			var terminalErr error
			var s *drainStream
			cancelled := false
			for {
				e, err := stream.Recv()
				t.Logf("drain event: %#v err=%T %v", e, err, err)
				if e.Status != nil {
					b, _ := json.Marshal(e.Status)
					t.Logf("drain status: %s", b)
				}
				if err != nil {
					terminalErr = err
					break
				}
				if e.Task != nil {
					task = *e.Task
				}
				if e.TaskID != "" {
					task.ID = e.TaskID
				}
				if e.Status != nil {
					task.Status = *e.Status
				}
				if translate {
					if err := translationObservedTaskError(task); err != nil {
						t.Fatal(err)
					}
				}
				if !translate && !cancelled && task.ID != "" {
					select {
					case s = <-holder:
					case <-ctx.Done():
						t.Fatal("Stream not started")
					}
					select {
					case <-s.started:
					case <-ctx.Done():
						t.Fatal("no initial event read")
					}
					if s.ctx.Err() != nil {
						t.Fatal("premature cancellation")
					}
					if _, err := c.CancelTask(ctx, client.CancelTaskRequest{TaskID: task.ID}); err != nil {
						t.Fatal(err)
					}
					cancelled = true
				}
			}
			if s == nil {
				select {
				case s = <-holder:
				case <-ctx.Done():
					t.Fatal("missing stream")
				}
			}
			s.awaitResult(t, ctx)
			if s.reads.Load() < 2 || s.resultReads.Load() != 1 || r.calls.Load() != 1 {
				t.Fatalf("read/result/execute=%d/%d/%d", s.reads.Load(), s.resultReads.Load(), r.calls.Load())
			}
			if translate {
				// The pinned protocol SDK can return its failed Task or preserve
				// the producer error. Both remain independent infrastructure
				// paths; a RunFinished hint cannot manufacture their outcome.
				if err := translationOutcomeError(task, terminalErr); err != nil {
					t.Fatalf("translation outcome=%#v %T %v: %v", task, terminalErr, terminalErr, err)
				}
			} else {
				if !errors.Is(terminalErr, io.EOF) {
					t.Fatal(terminalErr)
				}
				// CancelTask's existing public acknowledgement closes its HTTP
				// subscription before the executor's separate drain classification.
				if task.Status.State != client.TaskStateCanceled {
					t.Fatalf("cancel acknowledgement=%#v", task)
				}
				if code, limit := control(task); code != "" || limit != nil {
					t.Fatalf("cancel acknowledgement gained control=%s/%v", code, limit)
				}
			}
		})
	}
}

// This is a handwritten assertion for the intentionally malformed Sequence
// fixture. It never classifies a production outcome or generates an expected
// code by calling a production classification helper.
func translationObservedTaskError(task client.Task) error {
	if task.Status.Message != nil {
		for _, part := range task.Status.Message.Parts {
			if _, present := part.Metadata["agentadaptor.failure"]; present {
				return errors.New("translation synthesized a failure control envelope")
			}
		}
	}
	switch task.Status.State {
	case client.TaskStateUnspecified, client.TaskStateSubmitted, client.TaskStateWorking, client.TaskStateFailed:
		return nil
	default:
		return errors.New("translation error accompanied a conflicting task state")
	}
}
func translationOutcomeError(task client.Task, terminalErr error) error {
	if err := translationObservedTaskError(task); err != nil {
		return err
	}
	if terminalErr == io.EOF && task.Status.State == client.TaskStateFailed {
		return nil
	}
	recovery, ok := terminalErr.(*client.StreamRecoveryError)
	if !ok || recovery == nil || recovery.Cause == nil || recovery.TaskID != task.ID {
		return errors.New("missing exact recovery wrapper or matching observed TaskID")
	}
	var upstream *a2aproto.Error
	if !errors.As(recovery.Cause, &upstream) || upstream == nil || upstream.Err != a2aproto.ErrInternalError || upstream.Message != "encode adapter stream status: invalid_payload" || recovery.Cause.Error() != upstream.Message || len(upstream.Details) != 0 {
		return errors.New("missing exact pinned upstream internal translation error")
	}
	if len(upstream.TypedDetails) > 1 {
		return errors.New("unexpected translation error detail count")
	}
	for _, detail := range upstream.TypedDetails {
		if detail == nil || detail.TypeURL != "type.googleapis.com/google.rpc.ErrorInfo" || detail.Value["reason"] != "INTERNAL_ERROR" || detail.Value["domain"] != "a2a-protocol.org" || len(detail.Value) != 3 {
			return errors.New("unexpected translation ErrorInfo")
		}
		metadata, ok := detail.Value["metadata"].(map[string]string)
		if !ok || len(metadata) != 1 {
			return errors.New("custom translation metadata")
		}
		if _, err := time.Parse(time.RFC3339, metadata["timestamp"]); err != nil {
			return errors.New("invalid standard translation timestamp")
		}
	}
	return nil
}

func TestT22TranslationOutcomeControls(t *testing.T) {
	failed := client.Task{Status: client.TaskStatus{State: client.TaskStateFailed}}
	upstream := func() *a2aproto.Error {
		return &a2aproto.Error{Err: a2aproto.ErrInternalError, Message: "encode adapter stream status: invalid_payload"}
	}
	exact := &client.StreamRecoveryError{Cause: upstream()}
	detailError := upstream()
	detailError.Details = map[string]any{"limit_ms": 100}
	timed := upstream()
	timed.TypedDetails = []*errordetails.Typed{{TypeURL: "type.googleapis.com/google.rpc.ErrorInfo", Value: map[string]any{"reason": "INTERNAL_ERROR", "domain": "a2a-protocol.org", "metadata": map[string]string{"timestamp": "2026-09-08T00:00:00Z"}}}}
	custom := upstream()
	custom.TypedDetails = []*errordetails.Typed{{TypeURL: "type.googleapis.com/google.rpc.ErrorInfo", Value: map[string]any{"reason": "INTERNAL_ERROR", "domain": "a2a-protocol.org", "metadata": map[string]string{"timestamp": "2026-09-08T00:00:00Z", "limit_ms": "100"}}}}
	withControl := func(code string, limit any) client.Task {
		return client.Task{Status: client.TaskStatus{State: client.TaskStateFailed, Message: &client.Message{Parts: []client.Part{{Kind: client.PartText, Metadata: map[string]any{"agentadaptor.failure": map[string]any{"code": code, "limit_ms": limit}}}}}}}
	}
	cases := []struct {
		name string
		task client.Task
		err  error
		ok   bool
	}{
		{"failed-eof", failed, io.EOF, true},
		{"typed-producer-error", client.Task{}, exact, true},
		{"typed-producer-existing-failed", failed, exact, true},
		{"typed-standard-timestamp", client.Task{}, &client.StreamRecoveryError{Cause: timed}, true},
		{"typed-observed-id", client.Task{ID: "observed", Status: client.TaskStatus{State: client.TaskStateWorking}}, &client.StreamRecoveryError{TaskID: "observed", Cause: upstream()}, true},
		{"typed-false-id", client.Task{}, &client.StreamRecoveryError{TaskID: "foreign", Cause: upstream()}, false},
		{"typed-ordinary-same-cause", client.Task{}, &client.StreamRecoveryError{Cause: errors.New("encode adapter stream status: invalid_payload")}, false},
		{"typed-wrong-upstream-code", client.Task{}, &client.StreamRecoveryError{Cause: &a2aproto.Error{Err: a2aproto.ErrInvalidParams, Message: "encode adapter stream status: invalid_payload"}}, false},
		{"typed-upstream-details", client.Task{}, &client.StreamRecoveryError{Cause: detailError}, false},
		{"typed-custom-errorinfo", client.Task{}, &client.StreamRecoveryError{Cause: custom}, false},
		{"wrapped-recovery", client.Task{}, fmt.Errorf("unexpected wrapper: %w", exact), false},
		{"empty-eof", client.Task{}, io.EOF, false},
		{"nil-error", client.Task{}, nil, false},
		{"failed-nil-error", failed, nil, false},
		{"ordinary-same-message", client.Task{}, errors.New("encode adapter stream status: invalid_payload"), false},
		{"deadline", client.Task{}, context.DeadlineExceeded, false},
		{"cancel", client.Task{}, context.Canceled, false},
		{"typed-nil-cause", client.Task{}, &client.StreamRecoveryError{}, false},
		{"typed-wrong-source", client.Task{}, &client.StreamRecoveryError{Cause: errors.New("unrelated invalid_payload")}, false},
		{"typed-eof-cause", client.Task{}, &client.StreamRecoveryError{Cause: io.EOF}, false},
		{"typed-deadline-cause", client.Task{}, &client.StreamRecoveryError{Cause: context.DeadlineExceeded}, false},
		{"typed-plus-cancel", client.Task{}, errors.Join(exact, context.Canceled), false},
		{"wrapped-eof", failed, fmt.Errorf("unrelated failure: %w", io.EOF), false},
		{"failed-fake-code", withControl("approval_denied", nil), io.EOF, false},
		{"typed-fake-code", withControl("active_execution_timeout", nil), exact, false},
		{"failed-fake-limit", withControl("", float64(100)), io.EOF, false},
		{"typed-fake-limit", withControl("", float64(100)), exact, false},
		{"empty-control", withControl("", nil), io.EOF, false},
		{"malformed-control", client.Task{Status: client.TaskStatus{State: client.TaskStateFailed, Message: &client.Message{Parts: []client.Part{{Metadata: map[string]any{"agentadaptor.failure": 7}}}}}}, exact, false},
	}
	for _, state := range []client.TaskState{client.TaskStateCompleted, client.TaskStateCanceled, client.TaskStateRejected, client.TaskStateInputRequired, client.TaskStateAuthRequired, "unknown"} {
		task := client.Task{Status: client.TaskStatus{State: state}}
		cases = append(cases, struct {
			name string
			task client.Task
			err  error
			ok   bool
		}{"eof-state-" + string(state), task, io.EOF, false}, struct {
			name string
			task client.Task
			err  error
			ok   bool
		}{"typed-state-" + string(state), task, exact, false})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := translationOutcomeError(tc.task, tc.err)
			if (err == nil) != tc.ok {
				t.Fatalf("oracle accepted=%v want=%v outcome=%#v %T %v: %v", err == nil, tc.ok, tc.task, tc.err, tc.err, err)
			}
		})
	}
}

type recoveryClient struct {
	budgetClient
	t          *testing.T
	stale      bool
	initial    context.Context
	recoveries atomic.Int32
}

func (c *recoveryClient) AgentCard(ctx context.Context) (client.AgentCard, error) {
	c.initial = ctx
	return client.AgentCard{Capabilities: client.Capabilities{Streaming: true}}, nil
}
func (c *recoveryClient) SendStream(ctx context.Context, _ client.SendRequest) (delegation.A2AStream, error) {
	if ctx != c.initial {
		c.t.Error("transport allocated a different budget context")
	}
	return &recoveryStream{stale: c.stale}, nil
}
func (c *recoveryClient) GetTask(ctx context.Context, _ client.GetTaskRequest) (client.Task, error) {
	c.recoveries.Add(1)
	if ctx != c.initial {
		c.t.Error("recovery allocated a different budget context")
	}
	if c.stale {
		return client.Task{ID: "known", Status: client.TaskStatus{State: client.TaskStateInputRequired}}, nil
	}
	<-ctx.Done()
	return client.Task{}, ctx.Err()
}

type recoveryStream struct {
	n     int
	stale bool
}

func (s *recoveryStream) Close() error { return nil }
func (s *recoveryStream) Recv() (client.Event, error) {
	s.n++
	if s.n == 1 {
		return client.Event{Task: &client.Task{ID: "known", Status: client.TaskStatus{State: client.TaskStateInputRequired}}}, nil
	}
	if s.stale {
		return client.Event{}, io.EOF
	}
	return client.Event{}, errors.New("offline connection interrupted")
}
func TestT22DelegationRecoveryKeepsBudgetAndRejectsStaleReplay(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(fmt.Sprintf("stale=%v", stale), func(t *testing.T) {
			fc := &recoveryClient{t: t, stale: stale}
			reg, err := delegation.NewRegistry(delegation.RemoteAgentSpec{Key: "remote", AgentCardURL: "http://127.0.0.1/unused"})
			if err != nil {
				t.Fatal(err)
			}
			d := delegation.NewDelegator(reg, nil)
			d.NewClient = func(delegation.RemoteAgentSpec) delegation.A2AClient { return fc }
			out, err := d.Delegate(testContext(t), delegation.DelegationRequest{Agent: "remote", RunID: "leader", Prompt: "continue", Stream: true, ActiveExecutionTimeout: 50 * time.Millisecond, Message: &client.Message{ID: "followup", TaskID: "known", Role: "user", Parts: []client.Part{{Kind: client.PartText, Text: "continue"}}}})
			var de *delegation.DelegationError
			want := "active_execution_timeout"
			if stale {
				want = "stream_interrupted"
			}
			if !errors.As(err, &de) || de.Code != want || out.Error == nil {
				t.Fatalf("recovery outcome=%#v %v", out, err)
			}
			if !stale && fc.recoveries.Load() != 1 {
				t.Fatalf("recovery calls=%d", fc.recoveries.Load())
			}
			if fc.cancels.Load() != 1 || !fc.detached {
				t.Fatalf("known-task cleanup=%d detached=%v", fc.cancels.Load(), fc.detached)
			}
			if stale && errors.Is(err, adaptor.ErrActiveExecutionTimeout) {
				t.Fatal("old input-required replay invented active timeout")
			}
		})
	}
}

func TestT22LocalCancellationDrainsPartialCarrier(t *testing.T) {
	parent, cancel := context.WithCancel(testContext(t))
	defer cancel()
	marker := errors.New("local partial cause")
	carrier := &adaptor.RunError{Reason: adaptor.ReasonApprovalDenied, Result: &adaptor.Result{Text: "local partial text", Summary: "local partial summary"}, Cause: errors.Join(context.Canceled, marker)}
	holder := make(chan *drainStream, 1)
	r := &fixedRunner{makeStream: func(ctx context.Context) adaptor.Stream {
		s := &drainStream{ctx: ctx, es: make(chan adaptor.Event, 2), started: make(chan struct{}), cancelSeen: make(chan struct{}), tailPublished: make(chan struct{}), resultDone: make(chan struct{}), err: carrier}
		holder <- s
		return s
	}}
	team, err := delegation.NewService(delegation.Config{Agents: []delegation.AgentRef{delegation.Local("member", r, delegation.Policy{})}})
	if err != nil {
		t.Fatal(err)
	}
	defer team.Close()
	type outcome struct {
		r delegation.DelegationResult
		e error
	}
	finished := make(chan outcome, 1)
	go func() {
		r, e := team.Delegate(parent, delegation.DelegationRequest{RunID: "leader", Agent: "member", Prompt: "cancel local", Stream: true})
		finished <- outcome{r, e}
	}()
	var s *drainStream
	select {
	case s = <-holder:
	case <-parent.Done():
		t.Fatal("local Stream not started")
	}
	select {
	case <-s.started:
	case <-parent.Done():
		t.Fatal("local event reader not started")
	}
	cancel()
	var out outcome
	select {
	case out = <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("local cancel deadlocked")
	}
	var de *delegation.DelegationError
	var original *adaptor.RunError
	if !errors.As(out.e, &de) || de.Code != "cancelled" || de.Metadata["limit_ms"] != nil || !errors.Is(out.e, marker) || !errors.As(out.e, &original) || original != carrier {
		t.Fatalf("local primary/carrier=%#v %v", out.r, out.e)
	}
	if original.Result.Text != "local partial text" || original.Result.Summary != "local partial summary" {
		t.Fatal("partial carrier changed")
	}
	b, _ := json.Marshal(out.r)
	if !strings.Contains(string(b), "local partial text") {
		t.Fatalf("local partial projection missing: %s", b)
	}
	s.awaitResult(t, testContext(t))
	if s.reads.Load() < 2 || s.resultReads.Load() != 1 || r.calls.Load() != 1 {
		t.Fatalf("local read/result/execute=%d/%d/%d", s.reads.Load(), s.resultReads.Load(), r.calls.Load())
	}
}

// This fixture control must distinguish a published buffered tail from its
// actual consumption. It intentionally exercises Result without draining in
// the negative case; no production consumer or HTTP acknowledgement is used.
func TestT22DrainOracleConsumption(t *testing.T) {
	for _, consume := range []bool{false, true} {
		t.Run(fmt.Sprintf("consume=%v", consume), func(t *testing.T) {
			ctx, cancel := context.WithCancel(testContext(t))
			defer cancel()
			s := &drainStream{ctx: ctx, es: make(chan adaptor.Event, 2), started: make(chan struct{}), cancelSeen: make(chan struct{}), tailPublished: make(chan struct{}), resultDone: make(chan struct{})}
			s.Events()
			cancel()
			tail := s.Events()
			if consume {
				for range tail {
				}
			}
			// Closing the producer and draining the tail must not pretend that
			// the consumer has already entered/completed Result.
			select {
			case <-s.tailPublished:
			default:
				t.Fatal("tail was not published")
			}
			select {
			case <-s.resultDone:
				t.Fatal("producer publication closed the Result barrier")
			default:
			}
			_, err := s.Result()
			select {
			case <-s.resultDone:
			default:
				t.Fatal("Result completion barrier remained open")
			}
			if s.earlyResult.Load() == consume {
				t.Fatalf("direct early-read flag=%v consume=%v", s.earlyResult.Load(), consume)
			}
			t.Logf("consume=%v buffered=%d Events=%d Result=%d early=%v err=%v", consume, len(s.es), s.reads.Load(), s.resultReads.Load(), s.earlyResult.Load(), err)
			if !consume && errors.Is(err, context.Canceled) {
				t.Fatal("drain oracle accepted Result with an unread buffered tail")
			}
			if consume && !errors.Is(err, context.Canceled) {
				t.Fatalf("fully drained Result rejected: %v", err)
			}
		})
	}
}
