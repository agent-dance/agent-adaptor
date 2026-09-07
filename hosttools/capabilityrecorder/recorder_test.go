package capabilityrecorder_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/capability"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/hosttools/capabilityrecorder"
	"github.com/agent-dance/agent-adaptor/memory"
	"github.com/agent-dance/agent-adaptor/todo"
)

type fixtureDriver struct {
	run         func(context.Context, driver.Request, driver.EventSink) (driver.Response, error)
	unavailable bool
}

func (d *fixtureDriver) Descriptor() driver.Descriptor {
	support := driver.ObservationSupport{Skills: true, MCP: true, Subagents: true, Todos: true}
	if d.unavailable {
		support = driver.ObservationSupport{}
	}
	return driver.Descriptor{Type: "recorder-fixture", Sessions: driver.SessionCapability{SupportsResume: true},
		Observation: driver.ObservationCapabilities{Batch: support, Streaming: support},
		RunPolicyCaps: driver.RunPolicyCapabilities{
			Permission: driver.HumanDecisionSupport{Ask: true, AutoApprove: true, AutoReject: true},
			PlanReview: driver.HumanDecisionSupport{Ask: true, AutoApprove: true, AutoReject: true},
			Question:   driver.QuestionSupport{Ask: true, AutoReject: true},
		},
	}
}
func (*fixtureDriver) ValidateConfig(any) error                  { return nil }
func (*fixtureDriver) SessionConfigFingerprint() (string, error) { return "recorder-fixture-v1", nil }
func (*fixtureDriver) SessionCodec() driver.SessionCodec         { return fixtureCodec{} }
func (*fixtureDriver) StreamCapability() driver.StreamCapability {
	return driver.StreamCapability{Native: true, HITL: true, TokenLevel: true}
}
func (d *fixtureDriver) Run(ctx context.Context, r driver.Request, s driver.EventSink) (driver.Response, error) {
	return d.run(ctx, r, s)
}

type fixtureCodec struct{}

func (fixtureCodec) Name() string { return "recorder-fixture/v1" }
func (fixtureCodec) ToParams(s *driver.SessionState) driver.SessionParams {
	if s == nil {
		return driver.SessionParams{}
	}
	return driver.SessionParams{ResumeID: s.ResumeID, DisplayID: s.DisplayID, Values: maps.Clone(s.Data)}
}
func (fixtureCodec) FromParams(p driver.SessionParams) *driver.SessionState {
	if p.ResumeID == "" && p.DisplayID == "" && len(p.Values) == 0 {
		return nil
	}
	return &driver.SessionState{ResumeID: p.ResumeID, DisplayID: p.DisplayID, Data: maps.Clone(p.Values)}
}
func (fixtureCodec) GuardFingerprint(p driver.SessionParams) string { return p.ResumeID }

type hookStore struct {
	capabilityrecorder.Store
	append func(context.Context, capabilityrecorder.Record) error
	query  func(context.Context, capabilityrecorder.Query) (capabilityrecorder.Page, error)
	closed atomic.Int32
}

func (s *hookStore) Append(ctx context.Context, r capabilityrecorder.Record) error {
	if s.append != nil {
		return s.append(ctx, r)
	}
	return s.Store.Append(ctx, r)
}
func (s *hookStore) Query(ctx context.Context, q capabilityrecorder.Query) (capabilityrecorder.Page, error) {
	if s.query != nil {
		return s.query(ctx, q)
	}
	return s.Store.Query(ctx, q)
}
func (s *hookStore) Close() error { s.closed.Add(1); return nil }

func newRecorder(t *testing.T, s capabilityrecorder.Store) *capabilityrecorder.Recorder {
	t.Helper()
	r, err := capabilityrecorder.New(capabilityrecorder.Config{Store: s})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func fact(phase capability.Phase) capability.Invocation {
	v := record(capabilityrecorder.Scope{RunID: "unused"}, 1).Invocation
	v.Phase = phase
	if phase == capability.Started {
		v.Duration = nil
	}
	return v
}
func emitFact(s driver.EventSink, phase capability.Phase) error {
	v := fact(phase)
	return s.EmitStream(driver.StreamPayload{Kind: driver.StreamCapabilityInvocation, Capability: &v})
}
func response() driver.Response {
	return driver.Response{
		Output: `{"answer":42}`, Summary: "observed summary", Model: "model", Provider: "provider", Usage: &driver.Usage{InputTokens: 2}, Metadata: map[string]string{"meta": "result-secret"},
		RawStreams:      &driver.RawStreams{Stdout: "raw-secret", Stderr: "stderr-secret", Terminal: &driver.TerminalPayload{Event: "result", JSON: json.RawMessage(`{"secret":"terminal-secret"}`)}},
		Transcript:      []driver.TranscriptItem{{Kind: driver.TranscriptAssistant, Text: "transcript-secret"}},
		RuntimeServices: []driver.RuntimeServiceReport{{ID: "service", Metadata: map[string]string{"url": "https://private.invalid"}}},
		Checkpoint:      &driver.Checkpoint{Valid: true, State: &driver.SessionState{ResumeID: "healthy", DisplayID: "healthy"}},
	}
}
func assertResponse(t *testing.T, res *adaptor.Result) {
	t.Helper()
	want := response()
	if res == nil {
		t.Fatal("missing Result")
	}
	if res.Text != want.Output || res.Summary != want.Summary || res.Provider != want.Provider || res.Model != want.Model || !reflect.DeepEqual(res.Usage, want.Usage) || !reflect.DeepEqual(res.Metadata, want.Metadata) || !reflect.DeepEqual(res.Raw(), *want.RawStreams) || !reflect.DeepEqual(res.Transcript(), want.Transcript) || !reflect.DeepEqual(res.Services(), want.RuntimeServices) {
		t.Fatalf("Result changed: %+v", res)
	}
	var decoded struct{ Answer int }
	if err := res.Decode(&decoded); err != nil || decoded.Answer != 42 {
		t.Fatal(decoded, err)
	}
}
func waitValue[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("timed out")
		var zero T
		return zero
	}
}

func TestRecorderLiveQueryBeforeUserDrain(t *testing.T) {
	for _, mode := range []string{"run", "stream", "blocking-stream"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			backing := capabilityrecorder.NewMemoryStore()
			accepted := make(chan capabilityrecorder.Record, 2)
			s := &hookStore{Store: backing}
			s.append = func(ctx context.Context, r capabilityrecorder.Record) error {
				if err := backing.Append(ctx, r); err != nil {
					return err
				}
				accepted <- r
				return nil
			}
			r := newRecorder(t, s)
			release := make(chan struct{})
			defer close(release)
			d := &fixtureDriver{run: func(ctx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
				if !req.Observation.CapabilityInvocations || req.Observation.Todos {
					t.Error("wrong demand", req.Observation)
				}
				if err := emitFact(sink, capability.Started); err != nil {
					return driver.Response{}, err
				}
				select {
				case <-release:
				case <-ctx.Done():
					return driver.Response{}, ctx.Err()
				}
				return response(), emitFact(sink, capability.Completed)
			}}
			opts := []adaptor.Option{r.Option(), adaptor.WithEventBuffer(1), adaptor.WithIdentity(adaptor.Identity{ID: " who ", Tenant: "租户", Profile: "p", Name: "name-secret"})}
			if mode == "blocking-stream" {
				opts = append(opts, adaptor.WithBlockingEvents())
			}
			a := adaptor.New(d, opts...)
			defer a.Close(ctx)
			type outcome struct {
				r *adaptor.Result
				e error
			}
			done := make(chan outcome, 1)
			var st adaptor.Stream
			if mode == "run" {
				go func() { result, err := a.Run(ctx, "prompt-secret"); done <- outcome{result, err} }()
			} else {
				st = a.Stream(ctx, "prompt-secret")
			}
			first := waitValue(t, accepted)
			page, err := r.Query(ctx, capabilityrecorder.Query{Scope: first.Scope})
			if err != nil || len(page.Records) != 1 || page.Records[0].Invocation.Phase != capability.Started || page.NextSequence != 0 {
				t.Fatal(page, err)
			}
			if first.Scope != (capabilityrecorder.Scope{IdentityID: " who ", Tenant: "租户", Profile: "p", RunID: first.Scope.RunID}) {
				t.Fatal(first.Scope)
			}
			select {
			case <-done:
				t.Fatal("run ended before live query")
			default:
			}
			if st != nil {
				go func() {
					for range st.Events() {
					}
					result, err := st.Result()
					done <- outcome{result, err}
				}()
			}
			// Permit completion without closing twice in failure paths.
			release <- struct{}{}
			out := waitValue(t, done)
			if out.e != nil {
				t.Fatal(out.e)
			}
			assertResponse(t, out.r)
			page, err = r.Query(ctx, capabilityrecorder.Query{Scope: first.Scope, AfterSequence: first.Sequence})
			if err != nil || len(page.Records) != 1 || page.Records[0].Invocation.Phase != capability.Completed || *page.Records[0].Invocation.Duration != 0 {
				t.Fatal(page, err)
			}
		})
	}
}

func TestRecorderFailureIsPerRunAndDoesNotChangeHITLOrCheckpoint(t *testing.T) {
	for _, mode := range []string{"error", "panic", "timeout", "timeout-nil", "late"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			backing := capabilityrecorder.NewMemoryStore()
			s := &hookStore{Store: backing}
			var fail atomic.Bool
			fail.Store(true)
			var attempts atomic.Int32
			late := make(chan struct{})
			finished := make(chan struct{}, 1)
			s.append = func(ctx context.Context, r capabilityrecorder.Record) error {
				attempts.Add(1)
				if !fail.Load() {
					return backing.Append(ctx, r)
				}
				switch mode {
				case "error":
					return errors.New("error-secret")
				case "panic":
					panic("panic-secret")
				case "timeout":
					<-ctx.Done()
					return ctx.Err()
				case "timeout-nil":
					<-ctx.Done()
					return nil
				default:
					<-late
					defer func() { finished <- struct{}{} }()
					return backing.Append(ctx, r)
				}
			}
			r := newRecorder(t, s)
			var approvals atomic.Int32
			d := &fixtureDriver{run: func(ctx context.Context, _ driver.Request, sink driver.EventSink) (driver.Response, error) {
				if err := emitFact(sink, capability.Started); err != nil {
					return driver.Response{}, err
				}
				if err := emitFact(sink, capability.Completed); err != nil {
					return driver.Response{}, err
				}
				reply, err := sink.(driver.DecisionCapableSink).RequestDecision(ctx, driver.DecisionRequest{Kind: driver.HumanDecisionPermission, Prompt: "approval-secret"})
				if err != nil {
					return driver.Response{}, err
				}
				if reply.Result != driver.DecisionApproved {
					return driver.Response{}, errors.New("approval changed")
				}
				return response(), nil
			}}
			a := adaptor.New(d, r.Option(), adaptor.WithThreadStore(memory.NewStore()), adaptor.OnApproval(func(ctx context.Context, a *adaptor.ApprovalRequest) error { approvals.Add(1); return a.Approve(ctx) }))
			defer a.Close(ctx)
			th := a.Thread("thread-secret")
			st := th.Stream(ctx, "prompt-secret")
			events := []adaptor.Event{}
			notices := []adaptor.Notice{}
			for ev := range st.Events() {
				events = append(events, ev)
				if n, ok := ev.(adaptor.Notice); ok && n.Data["code"] == "observation_disabled" {
					notices = append(notices, n)
				}
			}
			res, err := st.Result()
			if err != nil {
				t.Fatal(err)
			}
			assertResponse(t, res)
			if attempts.Load() != 1 || approvals.Load() != 1 || len(notices) != 1 {
				t.Fatal(attempts.Load(), approvals.Load(), notices)
			}
			wantReason := mode
			if mode == "late" || mode == "timeout-nil" {
				wantReason = "timeout"
			}
			n := notices[0]
			if n.Text != "" || len(n.Data) != 3 || n.Data["reason"] != wantReason || n.Data["observer_index"] != 0 {
				t.Fatal(n)
			}
			encoded, _ := json.Marshal(events)
			if strings.Contains(string(encoded), "error-secret") || strings.Contains(string(encoded), "panic-secret") {
				t.Fatal("store diagnostics leaked")
			}
			cp, err := th.Checkpoint(ctx)
			if err != nil || cp == nil || !cp.Valid || cp.State.ResumeID != "healthy" {
				t.Fatal(cp, err)
			}
			if mode == "late" {
				close(late)
				waitValue(t, finished)
				page, err := r.Query(ctx, capabilityrecorder.Query{Scope: capabilityrecorder.Scope{RunID: res.RunID}})
				if err != nil || len(page.Records) != 0 {
					t.Fatal("late cancelled commit", page, err)
				}
			}
			fail.Store(false)
			second, err := th.Run(ctx, "second")
			if err != nil {
				t.Fatal(err)
			}
			assertResponse(t, second)
			page, err := r.Query(ctx, capabilityrecorder.Query{Scope: capabilityrecorder.Scope{RunID: second.RunID}})
			if err != nil || len(page.Records) != 2 || attempts.Load() != 3 || approvals.Load() != 2 {
				t.Fatal(page, err, attempts.Load())
			}
		})
	}
}

func TestRecorderSharedStoreAndConcurrentIdentities(t *testing.T) {
	ctx := context.Background()
	s := &hookStore{Store: capabilityrecorder.NewMemoryStore()}
	r := newRecorder(t, s)
	d := &fixtureDriver{run: func(_ context.Context, _ driver.Request, s driver.EventSink) (driver.Response, error) {
		if err := emitFact(s, capability.Started); err != nil {
			return driver.Response{}, err
		}
		return response(), emitFact(s, capability.Completed)
	}}
	a := adaptor.New(d, r.Option())
	b := adaptor.New(d)
	defer b.Close(ctx)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			identity := adaptor.Identity{ID: "same", Tenant: fmt.Sprint(i % 3), Profile: fmt.Sprint(i / 3)}
			res, err := a.Run(ctx, "work", adaptor.WithIdentity(identity))
			if err != nil {
				t.Error(err)
				return
			}
			scope := capabilityrecorder.Scope{IdentityID: identity.ID, Tenant: identity.Tenant, Profile: identity.Profile, RunID: res.RunID}
			page, err := r.Query(ctx, capabilityrecorder.Query{Scope: scope})
			if err != nil || len(page.Records) != 2 {
				t.Error(page, err)
			}
			scope.Profile += " "
			empty, err := r.Query(ctx, capabilityrecorder.Query{Scope: scope})
			if err != nil || len(empty.Records) != 0 {
				t.Error(empty, err)
			}
		}(i)
	}
	wg.Wait()
	if err := a.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if s.closed.Load() != 0 {
		t.Fatal("Agent closed shared store")
	}
	res, err := b.Run(ctx, "work", r.Option())
	if err != nil {
		t.Fatal(err)
	}
	page, err := r.Query(ctx, capabilityrecorder.Query{Scope: capabilityrecorder.Scope{RunID: res.RunID}})
	if err != nil || len(page.Records) != 2 {
		t.Fatal(page, err)
	}
	if err := s.Close(); err != nil || s.closed.Load() != 1 {
		t.Fatal(err)
	}
}

func TestRecorderClosedProjectionAndUnavailableObservation(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		t.Run(fmt.Sprint(unavailable), func(t *testing.T) {
			ctx := context.Background()
			r := newRecorder(t, capabilityrecorder.NewMemoryStore())
			d := &fixtureDriver{unavailable: unavailable, run: func(_ context.Context, _ driver.Request, s driver.EventSink) (driver.Response, error) {
				if unavailable {
					return response(), nil
				}
				_ = s.Emit(driver.RunEvent{Type: driver.RunEventChunk, Bytes: []byte("raw-secret")})
				_ = s.EmitStream(driver.StreamPayload{Kind: driver.StreamToolCallStart, ToolCallID: "tool", Name: "tool", Args: map[string]any{"url": "https://private.invalid", "header": "header-secret", "env": "env-secret"}})
				_ = s.EmitStream(driver.StreamPayload{Kind: driver.StreamToolCallEnd, ToolCallID: "tool"})
				tasks := todo.Snapshot{Items: []todo.Item{{ID: "task", Content: "todo-secret", Status: todo.Pending}}, Source: todo.PlanUpdate, Revision: 1, OccurredAt: time.Now().UTC()}
				_ = s.EmitStream(driver.StreamPayload{Kind: driver.StreamTodoUpdated, Todo: &tasks})
				return response(), emitFact(s, capability.Completed)
			}}
			a := adaptor.New(d, r.Option(), adaptor.WithThreadStore(memory.NewStore()), adaptor.WithIdentity(adaptor.Identity{Name: "name-secret"}))
			defer a.Close(ctx)
			st := a.Thread("thread-secret").Stream(ctx, "prompt-secret")
			var notices int
			for e := range st.Events() {
				if n, ok := e.(adaptor.Notice); ok && n.Data["code"] == "observation_unavailable" {
					notices++
				}
			}
			res, err := st.Result()
			if err != nil {
				t.Fatal(err)
			}
			page, err := r.Query(ctx, capabilityrecorder.Query{Scope: capabilityrecorder.Scope{RunID: res.RunID}})
			if err != nil {
				t.Fatal(err)
			}
			want := 1
			if unavailable {
				want = 0
				if notices != 1 {
					t.Fatal("missing unavailable notice")
				}
			}
			if len(page.Records) != want {
				t.Fatal(page)
			}
			raw, _ := json.Marshal(page)
			for _, secret := range []string{"raw-secret", "header-secret", "env-secret", "todo-secret", "name-secret", "thread-secret", "prompt-secret", "result-secret", "https://private.invalid", "transcript-secret", "terminal-secret"} {
				if strings.Contains(string(raw), secret) {
					t.Fatal("secret projected", secret)
				}
			}
		})
	}
}

func TestRecorderRequiresStoreAndRejectsUnsafeQueryPages(t *testing.T) {
	var nilStore *hookStore
	for _, s := range []capabilityrecorder.Store{nil, nilStore} {
		if _, err := capabilityrecorder.New(capabilityrecorder.Config{Store: s}); !errors.Is(err, capabilityrecorder.ErrStoreRequired) {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	scope := capabilityrecorder.Scope{RunID: "r"}
	good := record(scope, 2)
	var calls int
	s := &hookStore{query: func(context.Context, capabilityrecorder.Query) (capabilityrecorder.Page, error) {
		calls++
		return capabilityrecorder.Page{Records: []capabilityrecorder.Record{good}}, nil
	}}
	r := newRecorder(t, s)
	for _, q := range []capabilityrecorder.Query{{}, {Scope: scope, Limit: -1}, {Scope: scope, Limit: 1001}} {
		if _, err := r.Query(ctx, q); !errors.Is(err, capabilityrecorder.ErrInvalidQuery) {
			t.Fatal(err)
		}
	}
	if calls != 0 {
		t.Fatal("unsafe query reached store")
	}
	page, err := r.Query(ctx, capabilityrecorder.Query{Scope: scope})
	if err != nil {
		t.Fatal(err)
	}
	*page.Records[0].Invocation.Duration = 99
	if *good.Invocation.Duration != 0 {
		t.Fatal("store result shared")
	}
	for _, mode := range []string{"scope", "cursor", "order", "duplicate", "limit", "next", "empty-next", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			p := capabilityrecorder.Page{Records: []capabilityrecorder.Record{record(scope, 2)}}
			q := capabilityrecorder.Query{Scope: scope, Limit: 1}
			switch mode {
			case "scope":
				p.Records[0].Scope.Tenant = "other"
			case "cursor":
				q.AfterSequence = 2
			case "order":
				q.Limit = 2
				p.Records = append(p.Records, record(scope, 1))
			case "duplicate":
				q.Limit = 2
				p.Records = append(p.Records, record(scope, 2))
			case "limit":
				p.Records = append(p.Records, record(scope, 3))
			case "next":
				p.NextSequence = 3
			case "empty-next":
				p.Records = nil
				p.NextSequence = 2
			case "invalid":
				p.Records[0].Invocation.ErrorCode = "error-secret"
			}
			s.query = func(context.Context, capabilityrecorder.Query) (capabilityrecorder.Page, error) { return p, nil }
			got, err := r.Query(ctx, q)
			if err == nil || len(got.Records) != 0 || strings.Contains(err.Error(), "error-secret") {
				t.Fatal(got, err)
			}
		})
	}
	cause := errors.New("host store offline")
	s.query = func(context.Context, capabilityrecorder.Query) (capabilityrecorder.Page, error) {
		return capabilityrecorder.Page{}, cause
	}
	if _, err := r.Query(ctx, capabilityrecorder.Query{Scope: scope}); !errors.Is(err, cause) {
		t.Fatal(err)
	}
}

func TestRecorderCancellationKeepsAcceptedFactsAndObservesCleanup(t *testing.T) {
	for _, blocking := range []bool{false, true} {
		t.Run(fmt.Sprint(blocking), func(t *testing.T) {
			ctx := context.Background()
			backing := capabilityrecorder.NewMemoryStore()
			s := &hookStore{Store: backing}
			accepted := make(chan struct{})
			cleanup := make(chan time.Duration, 1)
			s.append = func(ctx context.Context, r capabilityrecorder.Record) error {
				if r.Invocation.Phase == capability.Cancelled {
					if ctx.Err() != nil {
						t.Error("cleanup inherited cancellation", ctx.Err())
					}
					deadline, ok := ctx.Deadline()
					if !ok {
						t.Error("unbounded cleanup")
					}
					cleanup <- time.Until(deadline)
				}
				if err := backing.Append(ctx, r); err != nil {
					return err
				}
				if r.Invocation.Phase == capability.Started {
					go func() { <-ctx.Done(); close(accepted) }()
				}
				return nil
			}
			r := newRecorder(t, s)
			d := &fixtureDriver{run: func(ctx context.Context, _ driver.Request, s driver.EventSink) (driver.Response, error) {
				_ = emitFact(s, capability.Started)
				<-ctx.Done()
				v := fact(capability.Cancelled)
				v.ErrorCode = capability.RunCancelled
				_ = s.EmitStream(driver.StreamPayload{Kind: driver.StreamCapabilityInvocation, Capability: &v})
				return response(), ctx.Err()
			}}
			opts := []adaptor.Option{r.Option(), adaptor.WithEventBuffer(1)}
			if blocking {
				opts = append(opts, adaptor.WithBlockingEvents())
			}
			a := adaptor.New(d, opts...)
			defer a.Close(ctx)
			st := a.Stream(ctx, "work")
			waitValue(t, accepted)
			st.Cancel()
			done := make(chan error, 1)
			go func() { _, err := st.Result(); done <- err }()
			err := waitValue(t, done)
			var runErr *adaptor.RunError
			if !errors.As(err, &runErr) || !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			assertResponse(t, runErr.Result)
			bound := waitValue(t, cleanup)
			if bound <= 0 || bound > 100*time.Millisecond {
				t.Fatal("wrong cleanup bound", bound)
			}
			page, err := r.Query(ctx, capabilityrecorder.Query{Scope: capabilityrecorder.Scope{RunID: st.RunID()}})
			if err != nil || len(page.Records) != 2 || page.Records[1].Invocation.Phase != capability.Cancelled {
				t.Fatal(page, err)
			}
			var lost int
			var terminals int
			for ev := range st.Events() {
				switch e := ev.(type) {
				case adaptor.Dropped:
					lost += e.ByKind["capability.invocation"]
				case adaptor.RunFinished:
					terminals++
				case adaptor.Notice:
					if e.Data["code"] == "observation_disabled" {
						t.Error("healthy observer disabled", e)
					}
				}
			}
			if lost != 2 || terminals != 1 {
				t.Fatal("accepted but undelivered facts not accounted for", lost, terminals)
			}
		})
	}
}

func TestRecorderCancelDuringAppendBoundsLateCallback(t *testing.T) {
	ctx := context.Background()
	backing := capabilityrecorder.NewMemoryStore()
	entered := make(chan struct{})
	late := make(chan struct{})
	finished := make(chan error, 1)
	var calls atomic.Int32
	s := &hookStore{Store: backing, append: func(ctx context.Context, r capabilityrecorder.Record) error {
		calls.Add(1)
		close(entered)
		<-late
		err := backing.Append(ctx, r)
		finished <- err
		return err
	}}
	r := newRecorder(t, s)
	d := &fixtureDriver{run: func(ctx context.Context, _ driver.Request, sink driver.EventSink) (driver.Response, error) {
		_ = emitFact(sink, capability.Started)
		_ = emitFact(sink, capability.Cancelled)
		return response(), ctx.Err()
	}}
	a := adaptor.New(d, r.Option())
	defer a.Close(ctx)
	st := a.Stream(ctx, "work")
	waitValue(t, entered)
	st.Cancel()
	done := make(chan error, 1)
	go func() { _, err := st.Result(); done <- err }()
	if err := waitValue(t, done); !errors.Is(err, context.Canceled) {
		close(late)
		t.Fatal(err)
	}
	close(late)
	if err := waitValue(t, finished); !errors.Is(err, context.Canceled) {
		t.Fatal("late Store commit was not fenced by context", err)
	}
	var notices int
	for ev := range st.Events() {
		if n, ok := ev.(adaptor.Notice); ok && n.Data["code"] == "observation_disabled" {
			notices++
			if n.Data["reason"] != "timeout" {
				t.Fatal(n)
			}
		}
	}
	page, err := r.Query(ctx, capabilityrecorder.Query{Scope: capabilityrecorder.Scope{RunID: st.RunID()}})
	if err != nil || len(page.Records) != 0 || calls.Load() != 1 || notices != 1 {
		t.Fatal(page, err, calls.Load(), notices)
	}
}

func TestNoRecorderDoesNotRequestObservations(t *testing.T) {
	d := &fixtureDriver{run: func(_ context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
		if req.Observation.CapabilityInvocations || req.Observation.Todos {
			t.Error("implicit observation demand")
		}
		return response(), emitFact(sink, capability.Completed)
	}}
	a := adaptor.New(d)
	defer a.Close(context.Background())
	st := a.Stream(context.Background(), "work")
	var facts int
	for ev := range st.Events() {
		if _, ok := ev.(adaptor.CapabilityInvocation); ok {
			facts++
		}
	}
	res, err := st.Result()
	if err != nil || facts != 1 {
		t.Fatal(res, err, facts)
	}
	assertResponse(t, res)
}
