package e2e_test

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
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/memory"
	"github.com/agent-dance/agent-adaptor/threadstore"
)

// Local watchdogs do not rely on the SDK honoring cancellation during a regression.
const alignmentWaitLimit = 8 * time.Second

func alignmentDrain(t *testing.T, s adaptor.Stream) (*adaptor.Result, error, []adaptor.Event) {
	t.Helper()
	return alignmentCollect(t, s, nil)
}
func alignmentCollect(t *testing.T, s adaptor.Stream, visit func(adaptor.Event)) (*adaptor.Result, error, []adaptor.Event) {
	t.Helper()
	timer := time.NewTimer(alignmentWaitLimit)
	defer timer.Stop()
	var events []adaptor.Event
	for {
		select {
		case event, open := <-s.Events():
			if !open {
				goto result
			}
			events = append(events, event)
			if visit != nil {
				visit(event)
			}
		case <-timer.C:
			go s.Cancel()
			t.Fatal("local watchdog: Events did not close")
		}
	}
result:
	type outcome struct {
		result *adaptor.Result
		err    error
	}
	done := make(chan outcome, 1)
	go func() { r, e := s.Result(); done <- outcome{r, e} }()
	select {
	case out := <-done:
		return out.result, out.err, events
	case <-timer.C:
		go s.Cancel()
		t.Fatal("local watchdog: Result did not return after Events closed")
		return nil, nil, events
	}
}
func alignmentClose(t *testing.T, a *adaptor.Agent, parent context.Context) error {
	t.Helper()
	return alignmentCloseContext(a, parent)
}
func alignmentCloseContext(a *adaptor.Agent, parent context.Context) error {
	ctx, cancel := context.WithTimeout(parent, 4*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Close(ctx) }()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		return errors.New("local watchdog: Agent.Close did not return")
	}
}
func alignmentCleanupAgent(t *testing.T, a *adaptor.Agent) {
	t.Helper()
	if err := alignmentClose(t, a, context.Background()); err != nil {
		t.Errorf("cleanup Close: %v", err)
	}
}
func alignmentEnvelope(t *testing.T, events []adaptor.Event, id string, reason adaptor.FailureReason) {
	t.Helper()
	starts, ends := 0, 0
	var seq uint64
	for i, e := range events {
		m := e.Meta()
		if m.RunID != id || m.Sequence <= seq || m.Time.IsZero() {
			t.Errorf("invalid event envelope %d: %+v", i, m)
		}
		seq = m.Sequence
		switch v := e.(type) {
		case adaptor.RunStarted:
			starts++
		case adaptor.RunFinished:
			ends++
			if i != len(events)-1 || v.Failed != (reason != "") || v.Reason != reason {
				t.Errorf("terminal=%+v at %d/%d want reason=%s", v, i, len(events), reason)
			}
		}
	}
	if starts != 1 || ends != 1 {
		t.Errorf("lifecycle start=%d end=%d", starts, ends)
	}
}
func alignmentCarried(t *testing.T, r *adaptor.Result, err error) *adaptor.RunError {
	t.Helper()
	var re *adaptor.RunError
	if r != nil || !errors.As(err, &re) || re == nil || re.Result == nil {
		t.Fatalf("failure outcome result=%v err=%v", r, err)
	}
	return re
}

func TestAlignmentLifecycleResultOnly(t *testing.T) {
	for _, prompt := range []string{"result-only", "message-stop", "provider-failure", "invalid-schema"} {
		for _, streaming := range []bool{false, true} {
			t.Run(prompt+map[bool]string{false: "/Run", true: "/Stream"}[streaming], func(t *testing.T) {
				f := newAlignmentFixture(t, "claude")
				a := f.agent(t)
				ctx, c := alignmentContext(t)
				defer c()
				opts := []adaptor.CallOption{adaptor.WithSpawn(), adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAutoApprove, PlanReview: adaptor.ApprovalAsk}})}
				if prompt == "invalid-schema" || prompt == "result-only" {
					opts = append(opts, adaptor.WithSchemaJSON([]byte(alignmentSchema)))
				}
				var r *adaptor.Result
				var err error
				var events []adaptor.Event
				var s adaptor.Stream
				if streaming {
					s = a.Stream(ctx, prompt, opts...)
					r, err, events = alignmentDrain(t, s)
				} else {
					r, err = a.Run(ctx, prompt, opts...)
				}
				reason := adaptor.FailureReason("")
				if prompt == "provider-failure" || prompt == "invalid-schema" {
					re := alignmentCarried(t, r, err)
					r = re.Result
					reason = re.Reason
				} else if err != nil {
					t.Fatal(err)
				}
				if r.Text != "answer-text" {
					t.Errorf("Text=%q", r.Text)
				}
				raw := r.Raw()
				if raw.Terminal == nil || raw.Terminal.Event != "result" || !strings.HasSuffix(raw.Stdout, "\n \t\n") || !strings.Contains(raw.Stderr, "t20-eof-tail") {
					t.Errorf("missing terminal/EOF tail: %+v", raw)
				}
				if len(r.Transcript()) == 0 {
					t.Error("empty official transcript")
				}
				if prompt == "result-only" {
					var v struct{ Value string }
					if e := r.Decode(&v); e != nil || v.Value != "ok" {
						t.Errorf("Decode=%+v %v", v, e)
					}
				}
				if streaming {
					alignmentEnvelope(t, events, s.RunID(), reason)
					again, againErr, _ := alignmentDrain(t, s)
					if (again == nil) != (err != nil) || againErr != err {
						t.Error("Result not stable")
					}
				}
				if logs := f.wait(t, "eof", 1); len(logs) != 1 {
					t.Errorf("EOF count=%d", len(logs))
				}
				if len(f.wait(t, "prompt", 1)) != 1 {
					t.Error("prompt replay")
				}
			})
		}
	}
}

func TestAlignmentLifecycleResidentSchemaPrewarm(t *testing.T) {
	for _, provider := range []string{"claude", "codebuddy"} {
		t.Run(provider, func(t *testing.T) {
			f := newAlignmentFixture(t, provider)
			store := memory.NewStore()
			a := f.agent(t, adaptor.WithThreadStore(store))
			ctx, c := alignmentContext(t)
			defer c()
			th := a.Thread("opaque:/中\x00key")
			auto := adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAutoApprove, PlanReview: adaptor.ApprovalAutoApprove}})
			if _, e := th.Run(ctx, "warm", auto); e != nil {
				t.Fatal(e)
			}
			if _, e := th.Run(ctx, "warm-again", auto); e != nil {
				t.Fatal(e)
			}
			oldRecord, _ := store.Resolve(ctx, threadstore.Query{Key: th.Key()})
			before, e := th.Checkpoint(ctx)
			if e != nil {
				t.Fatal(e)
			}
			if len(f.wait(t, "start", 1)) != 1 {
				t.Fatal("resident not reused")
			}
			r, e := a.Thread(th.Key(), adaptor.ResumeOnly()).Run(ctx, "schema", auto, adaptor.WithSchemaJSON([]byte(alignmentSchema)))
			if e != nil {
				t.Fatal(e)
			}
			var value struct{ Value string }
			if e = r.Decode(&value); e != nil || value.Value != "ok" {
				t.Fatalf("decode %+v %v", value, e)
			}
			f.wait(t, "start", 3)
			if _, e := a.Thread(th.Key(), adaptor.ResumeOnly()).Run(ctx, "after", auto); e != nil {
				t.Fatal(e)
			}
			after, e := th.Checkpoint(ctx)
			if e != nil {
				t.Fatal(e)
			}
			newRecord, _ := store.Resolve(ctx, threadstore.Query{Key: th.Key()})
			if oldRecord.ID != newRecord.ID || oldRecord.Key != newRecord.Key {
				t.Errorf("transport rebound active record: before=%+v after=%+v", oldRecord, newRecord)
			}
			if !reflect.DeepEqual(before, after) {
				t.Errorf("checkpoint rebound %v -> %v", before, after)
			}
			starts := f.wait(t, "start", 3)
			if len(starts) != 3 {
				t.Errorf("starts=%d want resident+batch+prewarm", len(starts))
			}
			prompts := f.wait(t, "prompt", 4)
			if len(prompts) != 4 {
				t.Errorf("prompt count=%d", len(prompts))
			}
			if prompts[0].PID != prompts[1].PID || prompts[2].PID == prompts[1].PID || prompts[3].PID != starts[2].PID {
				t.Errorf("bad process handoff: %+v", prompts)
			}
			// Actual exit record must precede each replacement start in the ledger.
			for _, l := range f.logs(t) {
				if l.Kind == "start" && len(l.Overlap) != 0 {
					t.Errorf("overlapping live writers: %+v", l)
				}
			}
			if _, e := th.Run(ctx, "spawn", auto, adaptor.WithSpawn()); e != nil {
				t.Fatal(e)
			}
			if _, e := th.Run(ctx, "after-spawn", auto); e != nil {
				t.Fatal(e)
			}
			ps := f.wait(t, "prompt", 6)
			if len(ps) != 6 || ps[4].PID == ps[5].PID {
				t.Errorf("WithSpawn promoted current writer/replayed: %+v", ps)
			}
		})
	}
}

func TestAlignmentLifecyclePartialProviders(t *testing.T) {
	for _, provider := range []string{"claude", "codebuddy", "codex"} {
		for _, method := range []string{"Run", "Stream"} {
			t.Run(provider+"/"+method, func(t *testing.T) {
				f := newAlignmentFixture(t, provider)
				store := memory.NewStore()
				a := f.agent(t, adaptor.WithThreadStore(store))
				ctx, c := alignmentContext(t)
				defer c()
				th := a.Thread("partial-key")
				if rr, e := th.Run(ctx, "healthy"); e != nil {
					var re *adaptor.RunError
					if errors.As(e, &re) {
						t.Fatalf("healthy error=%v raw=%+v result=%+v", e, re.Result.Raw(), re.Result)
					}
					t.Fatalf("healthy result=%v err=%v", rr, e)
				}
				before, e := store.Resolve(ctx, threadstore.Query{Key: "partial-key"})
				if e != nil {
					t.Fatal(e)
				}
				runctx, cancel := context.WithCancel(ctx)
				var r *adaptor.Result
				var err error
				var events []adaptor.Event
				var id string
				done := make(chan struct{})
				go func() {
					defer close(done)
					if method == "Run" {
						r, err = th.Run(runctx, "partial")
					} else {
						s := th.Stream(runctx, "partial")
						id = s.RunID()
						for event := range s.Events() {
							events = append(events, event)
						}
						r, err = s.Result()
					}
				}()
				f.wait(t, "barrier", 1)
				cancel()
				select {
				case <-done:
				case <-ctx.Done():
					t.Fatal("cancel stuck")
				}
				re := alignmentCarried(t, r, err)
				if re.Reason != adaptor.ReasonCancelled || !errors.Is(err, context.Canceled) {
					t.Errorf("reason/cause: %v", err)
				}
				out := re.Result
				wantText := "partial-text"
				if provider == "claude" {
					wantText = ""
				}
				if out.Text != wantText || !strings.Contains(out.Raw().Stdout, "partial-text") || !strings.Contains(out.Raw().Stderr, "t20-stderr") || len(out.Transcript()) == 0 {
					t.Errorf("partial fields missing: %+v raw=%+v transcript=%+v", out, out.Raw(), out.Transcript())
				}
				if out.Usage == nil || out.Usage.InputTokens != 7 || out.Usage.OutputTokens != 3 {
					t.Errorf("partial usage=%+v", out.Usage)
				}
				after, e := store.Resolve(ctx, threadstore.Query{Key: "partial-key"})
				if e != nil || !reflect.DeepEqual(before, after) {
					t.Errorf("unhealthy checkpoint mutated: before=%+v after=%+v err=%v", before, after, e)
				}
				if len(f.wait(t, "prompt", 2)) != 2 {
					t.Error("delivered prompt replayed")
				}
				if method == "Stream" {
					alignmentEnvelope(t, events, id, re.Reason)
				}
			})
		}
	}
}

func TestAlignmentLifecycleApprovalSchema(t *testing.T) {
	for _, kind := range []string{"question", "plan", "permission"} {
		for _, outcome := range []string{"allow", "deny", "timeout"} {
			t.Run(kind+"/"+outcome, func(t *testing.T) {
				f := newAlignmentFixture(t, "claude")
				a := f.agent(t)
				ctx, c := alignmentContext(t)
				defer c()
				policy := adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAutoApprove, PlanReview: adaptor.ApprovalAutoApprove, Question: adaptor.QuestionAutoDeny, Timeout: 100 * time.Millisecond}
				switch kind {
				case "question":
					policy.Question = adaptor.QuestionAsk
				case "plan":
					policy.PlanReview = adaptor.ApprovalAsk
				case "permission":
					policy.Permission = adaptor.ApprovalAsk
				}
				s := a.Thread("approval").Stream(ctx, "ask-"+kind, adaptor.WithSpawn(), adaptor.WithPolicy(adaptor.Policy{Approvals: policy}), adaptor.WithSchemaJSON([]byte(alignmentSchema)))
				requests := 0
				r, e, events := alignmentCollect(t, s, func(event adaptor.Event) {
					if req, ok := event.(*adaptor.ApprovalRequest); ok {
						requests++
						wantKind := map[string]adaptor.ApprovalKind{"question": adaptor.ApprovalQuestion, "plan": adaptor.ApprovalPlanReview, "permission": adaptor.ApprovalPermission}[kind]
						if req.Kind != wantKind {
							t.Errorf("Kind=%s want=%s", req.Kind, wantKind)
						}
						var e error
						switch outcome {
						case "allow":
							if kind == "question" {
								e = req.Answer(ctx, "docs")
							} else {
								e = req.Approve(ctx)
							}
						case "deny":
							e = req.Deny(ctx, "declined")
						}
						if e != nil {
							t.Errorf("respond: %v", e)
						}
					}
				})
				reason := adaptor.FailureReason("")
				if outcome == "allow" {
					if e != nil {
						t.Fatal(e)
					}
					var v struct{ Value string }
					if err := r.Decode(&v); err != nil || v.Value != "ok" {
						t.Errorf("decoded %+v err %v", v, err)
					}
					if r.Raw().Terminal == nil {
						t.Error("missing same-run terminal")
					}
					if len(f.wait(t, "answer", 1)) != 1 {
						t.Error("answer replay")
					}
				} else {
					re := alignmentCarried(t, r, e)
					reason = re.Reason
					want := adaptor.ReasonApprovalDenied
					if outcome == "timeout" {
						want = adaptor.ReasonApprovalTimeout
					}
					if reason != want {
						t.Errorf("reason=%s want %s", reason, want)
					}
				}
				if requests != 1 {
					t.Errorf("requests=%d", requests)
				}
				alignmentEnvelope(t, events, s.RunID(), reason)
				p := f.wait(t, "prompt", 1)[0]
				if p.Native != (kind != "permission") {
					t.Errorf("native=%v kind=%s", p.Native, kind)
				}
			})
		}
	}
}

// A hand-written SPI producer supplies rich audit values, while the public
// observer sees core lifecycle only after cleanup decides the final outcome.
type alignmentAuditDriver struct {
	calls atomic.Int32
	err   error
}

func (d *alignmentAuditDriver) Descriptor() driver.Descriptor {
	return driver.Descriptor{Type: "t20-audit", Workspace: driver.WorkspaceCapability{Supported: true}, Sessions: driver.SessionCapability{SupportsResume: true}, Runtime: driver.RuntimeCapability{ReportsServices: true}, StructuredOutput: driver.StructuredOutputCapability{JSONSchemaNative: true, WorksWithRun: true, WorksWithStreaming: true, WorksWithHITL: true}}
}
func (d *alignmentAuditDriver) ValidateConfig(any) error { return nil }
func (d *alignmentAuditDriver) StreamCapability() driver.StreamCapability {
	return driver.StreamCapability{Native: true}
}
func (d *alignmentAuditDriver) SessionConfigFingerprint() (string, error) {
	return "t20-config-v1", nil
}
func (d *alignmentAuditDriver) SessionCodec() driver.SessionCodec { return alignmentCodec{} }
func (d *alignmentAuditDriver) Run(ctx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
	d.calls.Add(1)
	_ = sink.EmitStream(driver.StreamPayload{Kind: driver.StreamRunStarted})
	_ = sink.EmitStream(driver.StreamPayload{Kind: driver.StreamRunFinished})
	return driver.Response{StructuredOutput: func() *driver.StructuredOutput {
		if req.OutputSchema == nil {
			return nil
		}
		return &driver.StructuredOutput{Format: driver.OutputFormatJSONSchema, Source: driver.StructuredOutputSourceNative, RawJSON: json.RawMessage(`{"value":"ok"}`), Valid: true}
	}(), Output: "audit-text", Summary: "audit-summary", Model: "audit-model", Provider: "audit-provider", Metadata: map[string]string{"safe": "observed"}, Usage: &driver.Usage{InputTokens: 17, OutputTokens: 9}, RawStreams: &driver.RawStreams{Stdout: "audit-stdout\n", Stderr: "audit-stderr\n", Terminal: &driver.TerminalPayload{Event: "result", JSON: json.RawMessage(`{"success":true}`)}}, Transcript: []driver.TranscriptItem{
		{Kind: driver.TranscriptAssistant, ScopeID: "scope-a", ParentScopeID: "parent-a", ParentToolCallID: "parent-tool", Text: "audit-text", Model: "audit-model", SessionID: "audit-session", Metadata: map[string]string{"origin": "protocol"}, Data: map[string]any{"segments": []any{"one", "two"}}},
		{Kind: driver.TranscriptResult, Text: "audit-summary", Subtype: "success", Usage: &driver.Usage{InputTokens: 17, OutputTokens: 9}, CostUSD: func() *float64 { v := 0.125; return &v }(), Metadata: map[string]string{"terminal": "observed"}},
	}, RuntimeServices: []driver.RuntimeServiceReport{{ID: "observed-service", Name: "fixture service", URL: "http://127.0.0.1:7654", Status: driver.RuntimeServiceRunning, Lifecycle: driver.RuntimeLifecycleEphemeral, ReuseKey: "service-reuse", Command: "fixture-command", CWD: "fixture-workspace", Port: 7654, OwnerAgentID: "audit-owner", Health: driver.RuntimeHealthHealthy, Metadata: map[string]string{"probe": "passed"}}}, Checkpoint: &driver.Checkpoint{Valid: true, State: &driver.SessionState{ResumeID: "audit-session", Data: map[string]string{"audit-turn": req.Prompt}}}}, d.err
}

type alignmentCodec struct{}

func (alignmentCodec) Name() string { return "t20-codec-v1" }
func (alignmentCodec) ToParams(s *driver.SessionState) driver.SessionParams {
	if s == nil {
		return driver.SessionParams{}
	}
	return driver.SessionParams{ResumeID: s.ResumeID, DisplayID: s.DisplayID, Values: s.Data}
}
func (alignmentCodec) FromParams(p driver.SessionParams) *driver.SessionState {
	if p.ResumeID == "" {
		return nil
	}
	return &driver.SessionState{ResumeID: p.ResumeID, DisplayID: p.DisplayID, Data: p.Values}
}
func (alignmentCodec) GuardFingerprint(driver.SessionParams) string { return "t20-guard" }

type alignmentCleanup struct {
	entered, release         chan struct{}
	err                      error
	enteredOnce, releaseOnce sync.Once
}

func (w *alignmentCleanup) unblock() {
	if w.release != nil {
		w.releaseOnce.Do(func() { close(w.release) })
	}
}

func (w *alignmentCleanup) Resolve(context.Context, adaptor.WorkspaceRequest) (adaptor.WorkspaceLease, error) {
	return adaptor.WorkspaceLease{CWD: ".", Fingerprint: "t20-workspace"}, nil
}
func (w *alignmentCleanup) Release(ctx context.Context, _ adaptor.WorkspaceLease, _ adaptor.WorkspaceReleaseMode) error {
	if w.entered != nil {
		w.enteredOnce.Do(func() { close(w.entered) })
		select {
		case <-w.release:
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(alignmentWaitLimit):
			return errors.New("local watchdog: cleanup release barrier")
		}
	}
	return w.err
}

type alignmentSource struct{}

func (alignmentSource) AttachRun(context.Context, string) (adaptor.RunAttachment, error) {
	return adaptor.RunAttachment{Events: func(context.Context, string) <-chan adaptor.Event {
		ch := make(chan adaptor.Event)
		close(ch)
		return ch
	}}, nil
}
func (alignmentSource) DetachRun(context.Context, string) error { return nil }

type alignmentLeaseFailure struct {
	*memory.Store
	fail    atomic.Bool
	commits atomic.Int32
}

func (s *alignmentLeaseFailure) Finalize(ctx context.Context, r threadstore.FinalizeRequest) error {
	if s.fail.Load() {
		return &threadstore.LeaseLostError{Target: r.Key}
	}
	if err := s.Store.Finalize(ctx, r); err != nil {
		return err
	}
	s.commits.Add(1)
	return nil
}

func TestAlignmentLifecycleFinalAuthority(t *testing.T) {
	for _, source := range []bool{false, true} {
		for _, failure := range []string{"cleanup", "lease"} {
			t.Run(failure+map[bool]string{false: "/driver-only", true: "/source"}[source], func(t *testing.T) {
				cause := errors.New("t20-cleanup")
				d := &alignmentAuditDriver{}
				store := &alignmentLeaseFailure{Store: memory.NewStore()}
				w := &alignmentCleanup{}
				opts := []adaptor.Option{adaptor.WithThreadStore(store), adaptor.WithWorkspaceManager(w), adaptor.WithWorkspaceSpec(adaptor.SharedWorkspace{})}
				if source {
					opts = append(opts, adaptor.WithRunServices(alignmentSource{}))
				}
				a := adaptor.New(d, opts...)
				defer alignmentCleanupAgent(t, a)
				ctx, c := alignmentContext(t)
				defer c()
				th := a.Thread("authority")
				if rr, e := th.Run(ctx, "healthy"); e != nil {
					var re *adaptor.RunError
					if errors.As(e, &re) {
						t.Fatalf("healthy error=%v raw=%+v result=%+v", e, re.Result.Raw(), re.Result)
					}
					t.Fatalf("healthy result=%v err=%v", rr, e)
				}
				before, _ := store.Resolve(ctx, threadstore.Query{Key: "authority"})
				if before == nil || before.State == nil || before.State.Data["audit-turn"] != "healthy" || store.commits.Load() != 1 {
					t.Fatal("first healthy checkpoint did not commit")
				}
				if failure == "lease" {
					store.fail.Store(true)
				} else {
					w.err = cause
				}
				w.entered = make(chan struct{})
				w.release = make(chan struct{})
				defer w.unblock()
				s := th.Stream(ctx, "second")
				var mu sync.Mutex
				var events []adaptor.Event
				done := make(chan struct{})
				var r *adaptor.Result
				var err error
				go func() {
					defer close(done)
					for e := range s.Events() {
						mu.Lock()
						events = append(events, e)
						mu.Unlock()
					}
					r, err = s.Result()
				}()
				select {
				case <-w.entered:
				case <-time.After(alignmentWaitLimit):
					go s.Cancel()
					t.Fatal("local watchdog: cleanup not entered")
				}
				mu.Lock()
				for _, e := range events {
					if _, ok := e.(adaptor.RunFinished); ok {
						t.Error("provider terminal published before cleanup")
					}
				}
				mu.Unlock()
				w.unblock()
				select {
				case <-done:
				case <-time.After(alignmentWaitLimit):
					go s.Cancel()
					t.Fatal("local watchdog: final result did not return")
				}
				re := alignmentCarried(t, r, err)
				if re.Reason != adaptor.ReasonInfrastructure {
					t.Errorf("reason=%s", re.Reason)
				}
				if failure == "cleanup" && !errors.Is(err, cause) {
					t.Error("cleanup cause lost")
				}
				if failure == "lease" && !errors.Is(err, threadstore.ErrLeaseLost) {
					t.Error("lease cause lost")
				}
				alignmentAuditFields(t, re.Result)
				alignmentEnvelope(t, events, s.RunID(), re.Reason)
				after, _ := store.Resolve(ctx, threadstore.Query{Key: "authority"})
				if failure == "lease" && (!reflect.DeepEqual(before, after) || store.commits.Load() != 1) {
					t.Error("failed lease altered healthy record")
				}
				// Two fast commits can share a Windows wall-clock timestamp. The
				// successful Finalize count and actual second-turn state prove the
				// committed checkpoint survived cleanup failure without rollback.
				if failure == "cleanup" && (after == nil || after.ID != before.ID || after.State == nil || after.State.Data["audit-turn"] != "second" || store.commits.Load() != 2) {
					t.Error("healthy committed state was rolled back")
				}
			})
		}
	}
}
func alignmentAuditFields(t *testing.T, r *adaptor.Result) {
	t.Helper()
	cost := 0.125
	// Independent expected values use public Result vocabulary and are never derived
	// from the fixture Response, actual Result, or the production conversion helper.
	wantTranscript := []adaptor.TranscriptItem{
		{Kind: "assistant", ScopeID: "scope-a", ParentScopeID: "parent-a", ParentToolCallID: "parent-tool", Text: "audit-text", Model: "audit-model", SessionID: "audit-session", Metadata: map[string]string{"origin": "protocol"}, Data: map[string]any{"segments": []any{"one", "two"}}},
		{Kind: "result", Text: "audit-summary", Subtype: "success", Usage: &adaptor.Usage{InputTokens: 17, OutputTokens: 9}, CostUSD: &cost, Metadata: map[string]string{"terminal": "observed"}},
	}
	wantServices := []adaptor.ServiceReport{{ID: "observed-service", Name: "fixture service", URL: "http://127.0.0.1:7654", Status: "running", Lifecycle: "ephemeral", ReuseKey: "service-reuse", Command: "fixture-command", CWD: "fixture-workspace", Port: 7654, OwnerAgentID: "audit-owner", Health: "healthy", Metadata: map[string]string{"probe": "passed"}}}
	wantRaw := adaptor.RawStreams{Stdout: "audit-stdout\n", Stderr: "audit-stderr\n", Terminal: &adaptor.TerminalPayload{Event: "result", JSON: json.RawMessage(`{"success":true}`)}}
	if r.Text != "audit-text" || r.Summary != "audit-summary" || r.Model != "audit-model" || r.Provider != "audit-provider" || r.Usage == nil || *r.Usage != (adaptor.Usage{InputTokens: 17, OutputTokens: 9}) || !reflect.DeepEqual(r.Metadata, map[string]string{"safe": "observed"}) || !reflect.DeepEqual(r.Raw(), wantRaw) || !reflect.DeepEqual(r.Transcript(), wantTranscript) || !reflect.DeepEqual(r.Services(), wantServices) {
		t.Errorf("audit fields lost: %+v raw=%+v tr=%+v services=%+v", r, r.Raw(), r.Transcript(), r.Services())
	}
}
func TestAlignmentLifecycleRunStreamAuditEquivalence(t *testing.T) {
	cause := errors.New("transport")
	d := &alignmentAuditDriver{err: cause}
	a := adaptor.New(d)
	defer alignmentCleanupAgent(t, a)
	ctx, c := alignmentContext(t)
	defer c()
	r, e := a.Run(ctx, "x")
	run := alignmentCarried(t, r, e)
	s := a.Stream(ctx, "x")
	r, e, ev := alignmentDrain(t, s)
	stream := alignmentCarried(t, r, e)
	alignmentAuditFields(t, run.Result)
	alignmentAuditFields(t, stream.Result)
	if !errors.Is(e, cause) {
		t.Error("cause lost")
	}
	x, y := *run.Result, *stream.Result
	x.RunID = ""
	y.RunID = ""
	if !reflect.DeepEqual(x, y) {
		t.Error("Run/Stream result fields differ")
	}
	alignmentEnvelope(t, ev, s.RunID(), stream.Reason)
}
func TestAlignmentLifecycleStaticRejection(t *testing.T) {
	d := &alignmentAuditDriver{}
	a := adaptor.New(d)
	defer alignmentCleanupAgent(t, a)
	ctx, c := alignmentContext(t)
	defer c()
	s := a.Stream(ctx, "x", adaptor.WithPolicy(adaptor.Policy{ActiveExecutionTimeout: -1}))
	r, e, ev := alignmentDrain(t, s)
	var re *adaptor.RunError
	if r != nil || !errors.Is(e, adaptor.ErrInvalidPolicy) || errors.As(e, &re) || len(ev) != 0 || d.calls.Load() != 0 {
		t.Errorf("static rejection outcome r=%v err=%v events=%d calls=%d", r, e, len(ev), d.calls.Load())
	}
}

func TestAlignmentLifecycleUnhealthyCheckpoint(t *testing.T) {
	for _, provider := range []string{"claude", "codebuddy"} {
		for _, prompt := range []string{"nonzero", "terminal-nonzero", "malformed", "missing"} {
			t.Run(provider+"/"+prompt, func(t *testing.T) {
				f := newAlignmentFixture(t, provider)
				store := memory.NewStore()
				a := f.agent(t, adaptor.WithThreadStore(store))
				ctx, c := alignmentContext(t)
				defer c()
				th := a.Thread("unchanged")
				if _, e := th.Run(ctx, "healthy"); e != nil {
					t.Fatal(e)
				}
				before, _ := store.Resolve(ctx, threadstore.Query{Key: "unchanged"})
				s := th.Stream(ctx, prompt, adaptor.WithSpawn())
				r, e, ev := alignmentDrain(t, s)
				re := alignmentCarried(t, r, e)
				if re.Reason != adaptor.ReasonAgentError {
					t.Errorf("abnormal reason=%s", re.Reason)
				}
				after, _ := store.Resolve(ctx, threadstore.Query{Key: "unchanged"})
				if !reflect.DeepEqual(before, after) {
					t.Error("abnormal process contaminated healthy record")
				}
				if len(f.wait(t, "prompt", 2)) != 2 {
					t.Error("delivered prompt was replayed")
				}
				raw := re.Result.Raw()
				if raw.Stdout == "" || !strings.Contains(raw.Stderr, "t20-stderr") || len(re.Result.Transcript()) == 0 {
					t.Error("abnormal response lost audit")
				}
				if prompt == "terminal-nonzero" && (raw.Terminal == nil || re.Result.Text != "answer-text") {
					t.Errorf("terminal on nonzero lost: %+v", re.Result)
				}
				alignmentEnvelope(t, ev, s.RunID(), re.Reason)
			})
		}
	}
}

func TestAlignmentLifecycleBootFailureBoundary(t *testing.T) {
	for _, provider := range []string{"claude", "codebuddy", "codex"} {
		t.Run(provider, func(t *testing.T) {
			f := newAlignmentFixture(t, provider)
			f.common.Env = append(f.common.Env, driver.EnvBinding{Name: "AA_T20_FAIL_BOOT", Value: "1"})
			a := f.agent(t)
			ctx, c := alignmentContext(t)
			defer c()
			r, e := a.Thread("boot").Run(ctx, "deliver-once")
			if provider == "claude" {
				// The child consumes one actual byte of the first prompt before failing.
				// Delivery has begun; an automatic replacement must not replay it.
				re := alignmentCarried(t, r, e)
				if re.Reason != adaptor.ReasonAgentError {
					t.Errorf("boot error=%v", e)
				}
				if len(f.wait(t, "start", 1)) != 1 {
					t.Error("partially delivered Claude prompt replayed")
				}
				if len(f.wait(t, "partial-input", 1)) != 1 {
					t.Error("fixture did not prove partial prompt delivery")
				}
				for _, entry := range f.logs(t) {
					if entry.Kind == "prompt" {
						t.Error("unexpected full prompt after partial delivery failure")
					}
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			expected := 2
			if provider == "codex" {
				expected = 3
			} // Failed handshake, one-shot fallback, then Codex's empty prewarm.
			starts := f.wait(t, "start", expected)
			if len(starts) != expected {
				t.Errorf("boot/fallback/prewarm starts=%d want=%d", len(starts), expected)
			}
			if len(f.wait(t, "prompt", 1)) != 1 {
				t.Error("safe fallback duplicated prompt")
			}
		})
	}
}

func TestAlignmentLifecycleDeadlinePreservesAudit(t *testing.T) {
	for _, provider := range []string{"claude", "codebuddy", "codex"} {
		t.Run(provider, func(t *testing.T) {
			f := newAlignmentFixture(t, provider)
			a := f.agent(t)
			ctx, c := alignmentContext(t)
			defer c()
			th := a.Thread("deadline")
			if _, e := th.Run(ctx, "warm"); e != nil {
				t.Fatal(e)
			}
			s := th.Stream(ctx, "partial", adaptor.WithTimeout(250*time.Millisecond))
			f.wait(t, "barrier", 1)
			r, e, events := alignmentDrain(t, s)
			re := alignmentCarried(t, r, e)
			if re.Reason != adaptor.ReasonDeadlineExceeded || !errors.Is(e, context.DeadlineExceeded) {
				t.Errorf("deadline identity=%v", e)
			}
			if !strings.Contains(re.Result.Raw().Stdout, "partial-text") || len(re.Result.Transcript()) == 0 {
				t.Error("deadline lost partial audit")
			}
			alignmentEnvelope(t, events, s.RunID(), re.Reason)
		})
	}
}

type alignmentCloseDriver struct {
	alignmentAuditDriver
	permit chan struct{}
}

func (d *alignmentCloseDriver) Descriptor() driver.Descriptor {
	out := d.alignmentAuditDriver.Descriptor()
	out.Process.Persistent = true
	return out
}
func (d *alignmentCloseDriver) CloseProcesses(ctx context.Context) error {
	select {
	case <-d.permit:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func TestAlignmentLifecycleCloseDeadlineRetry(t *testing.T) {
	d := &alignmentCloseDriver{permit: make(chan struct{})}
	a := adaptor.New(d, adaptor.WithThreadStore(memory.NewStore()))
	var permitOnce sync.Once
	release := func() { permitOnce.Do(func() { close(d.permit) }) }
	defer func() { release(); alignmentCleanupAgent(t, a) }()
	ctx, c := alignmentContext(t)
	defer c()
	if _, e := a.Thread("close").Run(ctx, "healthy"); e != nil {
		t.Fatal(e)
	}
	short, stop := context.WithTimeout(ctx, 20*time.Millisecond)
	e := alignmentClose(t, a, short)
	stop()
	if !errors.Is(e, context.DeadlineExceeded) {
		t.Errorf("Close bound error=%v", e)
	}
	for _, r := range []adaptor.Runner{a, a.Thread("close")} {
		s := r.Stream(ctx, "closed")
		result, err, events := alignmentDrain(t, s)
		if result != nil || !errors.Is(err, adaptor.ErrAgentClosed) || len(events) != 0 {
			t.Errorf("Close reopened admission result=%v err=%v events=%d", result, err, len(events))
		}
	}
	release()
	if e = alignmentClose(t, a, ctx); e != nil {
		t.Fatal(e)
	}
	if e = alignmentClose(t, a, ctx); e != nil {
		t.Fatal(e)
	}
	if d.calls.Load() != 1 {
		t.Errorf("new Driver call after Close: %d", d.calls.Load())
	}
}

func TestAlignmentLifecycleStructuredAuditEquivalence(t *testing.T) {
	d := &alignmentAuditDriver{}
	a := adaptor.New(d)
	ctx, c := alignmentContext(t)
	defer c()
	defer alignmentCleanupAgent(t, a)
	r, e := a.Run(ctx, "schema", adaptor.WithSchemaJSON([]byte(alignmentSchema)))
	if e != nil {
		t.Fatal(e)
	}
	s := a.Stream(ctx, "schema", adaptor.WithSchemaJSON([]byte(alignmentSchema)))
	other, e, ev := alignmentDrain(t, s)
	if e != nil {
		t.Fatal(e)
	}
	for _, result := range []*adaptor.Result{r, other} {
		alignmentAuditFields(t, result)
		var value struct{ Value string }
		if e := result.Decode(&value); e != nil || value.Value != "ok" {
			t.Errorf("structured audit: %+v %v", value, e)
		}
	}
	x, y := *r, *other
	x.RunID = ""
	y.RunID = ""
	if !reflect.DeepEqual(x, y) {
		t.Error("Run/Stream structured/raw/transcript/services differ")
	}
	alignmentEnvelope(t, ev, s.RunID(), "")
}

func TestAlignmentLifecycleRealCompatibilityGuards(t *testing.T) {
	for _, provider := range []string{"claude", "codebuddy", "codex"} {
		t.Run(provider, func(t *testing.T) {
			f := newAlignmentFixture(t, provider)
			store := memory.NewStore()
			a := f.agent(t, adaptor.WithThreadStore(store))
			ctx, c := alignmentContext(t)
			defer c()
			if _, e := a.Thread("guard").Run(ctx, "healthy"); e != nil {
				t.Fatal(e)
			}
			old, _ := store.Resolve(ctx, threadstore.Query{Key: "guard"})
			for _, change := range []struct {
				name   string
				option adaptor.CallOption
			}{{"model", adaptor.WithModel("different-model")}, {"workspace", adaptor.WithWorkspace(t.TempDir())}, {"append", adaptor.WithAppendSystemPrompt("different native context")}} {
				t.Run(change.name, func(t *testing.T) {
					if _, e := a.Thread("guard", adaptor.ResumeOnly()).Run(ctx, "rejected", change.option); !errors.Is(e, adaptor.ErrThreadIncompatible) {
						t.Errorf("changed %s allowed resume: %v", change.name, e)
					}
					current, _ := store.Resolve(ctx, threadstore.Query{Key: "guard"})
					if !reflect.DeepEqual(old, current) {
						t.Error("incompatible resume altered active record")
					}
				})
			}
			if len(f.wait(t, "prompt", 1)) != 1 {
				t.Error("incompatible run reached provider")
			}
		})
	}
}

type alignmentAdmissionProbe struct {
	calls atomic.Int32
	err   error
}

func (p *alignmentAdmissionProbe) AttachRun(context.Context, string) (adaptor.RunAttachment, error) {
	p.calls.Add(1)
	return adaptor.RunAttachment{}, p.err
}
func (p *alignmentAdmissionProbe) DetachRun(context.Context, string) error { return nil }
func TestAlignmentLifecycleAdmissionBoundary(t *testing.T) {
	cause := errors.New("prepare fixture")
	for _, static := range []bool{false, true} {
		t.Run(map[bool]string{false: "admitted-resource-error", true: "static-schema-error"}[static], func(t *testing.T) {
			d := &alignmentAuditDriver{}
			p := &alignmentAdmissionProbe{err: cause}
			a := adaptor.New(d, adaptor.WithRunServices(p))
			ctx, c := alignmentContext(t)
			defer c()
			defer alignmentCleanupAgent(t, a)
			var opts []adaptor.CallOption
			if static {
				opts = append(opts, adaptor.WithSchemaJSON([]byte(`{"type":`)))
			}
			s := a.Stream(ctx, "x", opts...)
			r, e, ev := alignmentDrain(t, s)
			var re *adaptor.RunError
			if r != nil || e == nil || errors.As(e, &re) || d.calls.Load() != 0 {
				t.Errorf("pre-Driver shape result=%v err=%v calls=%d", r, e, d.calls.Load())
			}
			if static {
				if p.calls.Load() != 0 || len(ev) != 0 {
					t.Error("static failure acquired resources or emitted lifecycle")
				}
			} else {
				if p.calls.Load() != 1 || !errors.Is(e, cause) {
					t.Error("admitted resource failure lost cause")
				}
				alignmentEnvelope(t, ev, s.RunID(), adaptor.ReasonInfrastructure)
			}
		})
	}
}

func TestAlignmentLifecycleTerminalCancellationRace(t *testing.T) {
	for i := 0; i < 5; i++ {
		t.Run(fmt.Sprintf("race-%d", i), func(t *testing.T) {
			f := newAlignmentFixture(t, "claude")
			a := f.agent(t)
			ctx, c := alignmentContext(t)
			defer c()
			s := a.Stream(ctx, "terminal-race", adaptor.WithSpawn(), adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalPolicy{PlanReview: adaptor.ApprovalAsk}}))
			f.wait(t, "terminal-sent", 1)
			s.Cancel()
			s.Cancel()
			r, e, ev := alignmentDrain(t, s)
			reason := adaptor.FailureReason("")
			if e != nil {
				re := alignmentCarried(t, r, e)
				if re.Reason != adaptor.ReasonCancelled {
					t.Errorf("terminal race reason=%s", re.Reason)
				}
				r = re.Result
				reason = re.Reason
			}
			if r.Raw().Terminal == nil || len(r.Transcript()) == 0 {
				t.Error("terminal race lost observed protocol")
			}
			alignmentEnvelope(t, ev, s.RunID(), reason)
			if len(f.wait(t, "prompt", 1)) != 1 {
				t.Error("terminal cancellation replayed prompt")
			}
		})
	}
}

func TestAlignmentLifecyclePermissionWithoutSchema(t *testing.T) {
	for _, method := range []string{"Run", "Stream"} {
		t.Run(method, func(t *testing.T) {
			f := newAlignmentFixture(t, "claude")
			a := f.agent(t)
			ctx, cancel := alignmentContext(t)
			defer cancel()
			var requests atomic.Int32
			opts := []adaptor.CallOption{adaptor.WithSpawn(), adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAsk, PlanReview: adaptor.ApprovalAutoApprove}}), adaptor.OnApproval(func(ctx context.Context, req *adaptor.ApprovalRequest) error {
				requests.Add(1)
				if req.Kind != adaptor.ApprovalPermission || req.ToolCallID != "t20-tool" {
					return errors.New("unexpected Permission request identity")
				}
				return req.Approve(ctx)
			})}
			var r *adaptor.Result
			var err error
			if method == "Run" {
				r, err = a.Run(ctx, "ask-permission", opts...)
			} else {
				s := a.Stream(ctx, "ask-permission", opts...)
				var events []adaptor.Event
				r, err, events = alignmentDrain(t, s)
				alignmentEnvelope(t, events, s.RunID(), "")
			}
			if err != nil {
				t.Fatal(err)
			}
			if requests.Load() != 1 || r.Raw().Terminal == nil || r.Raw().Terminal.Event != "result" {
				t.Fatal("ordinary Permission roundtrip lost")
			}
			if len(f.wait(t, "answer", 1)) != 1 || f.wait(t, "prompt", 1)[0].Native {
				t.Fatal("unexpected answer count/native schema without schema")
			}
		})
	}
}

type alignmentLegacyHITLDriver struct {
	works, policyAsk, requestDecision bool
	calls                             atomic.Int32
	sources                           chan driver.StructuredOutputSource
}

func (d *alignmentLegacyHITLDriver) Descriptor() driver.Descriptor {
	return driver.Descriptor{Type: "t20-legacy-hitl", RunPolicyCaps: driver.RunPolicyCapabilities{Permission: driver.HumanDecisionSupport{Ask: d.policyAsk}}, StructuredOutput: driver.StructuredOutputCapability{JSONSchemaNative: true, JSONSchemaPromptValidate: true, WorksWithRun: true, WorksWithStreaming: true, WorksWithHITL: d.works}}
}
func (*alignmentLegacyHITLDriver) ValidateConfig(any) error { return nil }
func (*alignmentLegacyHITLDriver) StreamCapability() driver.StreamCapability {
	return driver.StreamCapability{Native: true, HITL: true}
}
func (d *alignmentLegacyHITLDriver) Run(ctx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
	d.calls.Add(1)
	d.sources <- req.StructuredOutputSource
	if err := sink.EmitStream(driver.StreamPayload{Kind: driver.StreamRunStarted}); err != nil {
		return driver.Response{}, err
	}
	if d.requestDecision {
		decisions, ok := sink.(driver.DecisionCapableSink)
		if !ok {
			return driver.Response{}, errors.New("decision sink missing")
		}
		reply, err := decisions.RequestDecision(ctx, driver.DecisionRequest{RequestID: "legacy-permission", Kind: driver.HumanDecisionPermission, Source: "t20", ToolCallID: "legacy-tool", Prompt: "Allow fixture read?"})
		if err != nil {
			return driver.Response{}, err
		}
		if reply.RequestID != "legacy-permission" || reply.Result != driver.DecisionApproved {
			return driver.Response{}, errors.New("legacy decision reply mismatch")
		}
	}
	if err := sink.EmitStream(driver.StreamPayload{Kind: driver.StreamRunFinished}); err != nil {
		return driver.Response{}, err
	}
	response := driver.Response{Output: `{"value":"ok"}`}
	if req.OutputSchema != nil {
		response.StructuredOutput = &driver.StructuredOutput{Format: driver.OutputFormatJSONSchema, Source: req.StructuredOutputSource, RawJSON: json.RawMessage(`{"value":"ok"}`), Valid: true}
	}
	return response, nil
}
func TestAlignmentLifecycleLegacyHITLMatrix(t *testing.T) {
	cases := []struct {
		name                               string
		works, explicit, policyAsk, schema bool
		wantErr                            error
		source                             driver.StructuredOutputSource
	}{
		{"explicit-legacy-true", true, true, true, true, nil, driver.StructuredOutputSourceNative},
		{"explicit-legacy-false", false, true, true, true, driver.ErrStructuredOutputUnsupported, ""},
		{"unset-legacy-false", false, false, true, true, nil, driver.StructuredOutputSourceNative},
		{"unset-legacy-true", true, false, true, true, nil, driver.StructuredOutputSourceNative},
		{"ordinary-policy-first", true, true, false, true, driver.ErrHumanDecisionModeUnsupported, ""},
		{"no-schema-legacy-false", false, true, true, false, nil, ""},
	}
	for _, tc := range cases {
		for _, method := range []string{"Run", "Stream"} {
			t.Run(tc.name+"/"+method, func(t *testing.T) {
				d := &alignmentLegacyHITLDriver{works: tc.works, policyAsk: tc.policyAsk, requestDecision: tc.explicit, sources: make(chan driver.StructuredOutputSource, 1)}
				resource := &alignmentAdmissionProbe{}
				a := adaptor.New(d, adaptor.WithRunServices(resource))
				defer alignmentCleanupAgent(t, a)
				ctx, cancel := alignmentContext(t)
				defer cancel()
				var answered atomic.Int32
				opts := []adaptor.CallOption{adaptor.OnApproval(func(ctx context.Context, req *adaptor.ApprovalRequest) error {
					answered.Add(1)
					if req.Kind != adaptor.ApprovalPermission || req.ToolCallID != "legacy-tool" {
						return errors.New("legacy public approval mismatch")
					}
					return req.Approve(ctx)
				})}
				if tc.explicit {
					opts = append(opts, adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAsk}}))
				}
				if tc.schema {
					opts = append(opts, adaptor.WithSchemaJSON([]byte(alignmentSchema)))
				}
				var r *adaptor.Result
				var err error
				var events []adaptor.Event
				var id string
				if method == "Run" {
					r, err = a.Run(ctx, "legacy", opts...)
				} else {
					s := a.Stream(ctx, "legacy", opts...)
					id = s.RunID()
					r, err, events = alignmentDrain(t, s)
				}
				if tc.wantErr != nil {
					if r != nil || !errors.Is(err, tc.wantErr) || d.calls.Load() != 0 || resource.calls.Load() != 0 || len(events) != 0 || answered.Load() != 0 {
						t.Fatalf("static rejection result=%v err=%v driver=%d resources=%d approvals=%d events=%d", r, err, d.calls.Load(), resource.calls.Load(), answered.Load(), len(events))
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if d.calls.Load() != 1 || resource.calls.Load() != 1 {
					t.Fatalf("invocation count driver=%d resource=%d", d.calls.Load(), resource.calls.Load())
				}
				select {
				case actual := <-d.sources:
					if actual != tc.source {
						t.Errorf("resolved source=%s want=%s", actual, tc.source)
					}
				default:
					t.Fatal("Driver source missing")
				}
				wantAnswers := int32(0)
				if tc.explicit {
					wantAnswers = 1
				}
				if answered.Load() != wantAnswers {
					t.Errorf("approvals=%d want=%d", answered.Load(), wantAnswers)
				}
				if tc.schema {
					var value struct{ Value string }
					if err := r.Decode(&value); err != nil || value.Value != "ok" {
						t.Fatal("legacy schema output lost")
					}
				}
				if method == "Stream" {
					alignmentEnvelope(t, events, id, "")
				}
			})
		}
	}
}
