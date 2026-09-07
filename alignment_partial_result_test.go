package adaptor_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
	"github.com/agent-dance/agent-adaptor/memory"
	"github.com/agent-dance/agent-adaptor/threadstore"
)

type alignmentTransportError struct{ message string }

func (e *alignmentTransportError) Error() string { return e.message }

func alignmentPartialResponse() driver.Response {
	return driver.Response{
		Output: "partial assistant", Summary: "partial summary", Model: "observed-model", Provider: "observed-provider",
		Usage:    &driver.Usage{InputTokens: 2, OutputTokens: 3, CachedInputTokens: 1, EstimatedCostMilli: 4},
		Metadata: map[string]string{"observed": "metadata"},
		RawStreams: &driver.RawStreams{Stdout: "stdout bytes", Stderr: "stderr bytes", Terminal: &driver.TerminalPayload{
			Event: "result", JSON: json.RawMessage(`{"official":"partial"}`),
		}},
		Transcript:       []driver.TranscriptItem{{Kind: driver.TranscriptAssistant, Text: "partial assistant", Data: map[string]any{"seen": true}}},
		RuntimeServices:  []driver.RuntimeServiceReport{{ID: "observed-service", Metadata: map[string]string{"source": "driver"}}},
		StructuredOutput: &driver.StructuredOutput{Valid: true, RawJSON: json.RawMessage(`{"answer":42}`)},
		Checkpoint:       &driver.Checkpoint{Valid: true, State: &driver.SessionState{ResumeID: "unhealthy-must-not-persist"}},
	}
}

func alignmentCall(t *testing.T, runner adaptor.Runner, ctx context.Context, streaming bool, opts ...adaptor.CallOption) (*adaptor.Result, error) {
	t.Helper()
	if !streaming {
		return runner.Run(ctx, "work", opts...)
	}
	st := runner.Stream(ctx, "work", opts...)
	if st.RunID() == "" {
		t.Fatal("Stream RunID unavailable at return")
	}
	for range st.Events() {
	}
	res, err := st.Result()
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			again, againErr := st.Result()
			if again != res || againErr != err {
				t.Errorf("concurrent Result changed after Events closed: %p/%v != %p/%v", again, againErr, res, err)
			}
		})
	}
	wg.Wait()
	return res, err
}

func alignmentRequirePartial(t *testing.T, res *adaptor.Result, err error, reason adaptor.FailureReason, cause error) *adaptor.RunError {
	t.Helper()
	if res != nil || err == nil {
		t.Fatalf("failure outcome = %v, %v; want nil, error", res, err)
	}
	var re *adaptor.RunError
	if !errors.As(err, &re) || re.Result == nil || re.Reason != reason {
		t.Fatalf("error = %T %v, carrier = %#v; want %s with Result", err, err, re, reason)
	}
	if cause != nil && !errors.Is(err, cause) {
		t.Fatalf("error %v lost cause %v", err, cause)
	}
	return re
}

func alignmentAssertLayers(t *testing.T, got *adaptor.Result, want driver.Response) {
	t.Helper()
	if got.RunID == "" || got.Text != want.Output || got.Summary != want.Summary || got.Model != want.Model || got.Provider != want.Provider ||
		!reflect.DeepEqual(got.Usage, want.Usage) || !reflect.DeepEqual(got.Metadata, want.Metadata) ||
		!reflect.DeepEqual(got.Raw(), *want.RawStreams) || !reflect.DeepEqual(got.Transcript(), want.Transcript) || !reflect.DeepEqual(got.Services(), want.RuntimeServices) {
		t.Fatalf("partial layers differ: result=%#v raw=%#v transcript=%#v services=%#v; want=%#v", got, got.Raw(), got.Transcript(), got.Services(), want)
	}
	var decoded struct{ Answer int }
	if err := got.Decode(&decoded); err != nil || decoded.Answer != 42 {
		t.Fatalf("validated partial Decode = %#v, %v", decoded, err)
	}
}

func TestAlignmentPartialResultDriverError(t *testing.T) {
	cause := &alignmentTransportError{message: "transport stopped after output"}
	var previous *adaptor.Result
	for _, streaming := range []bool{false, true} {
		name := "Run"
		if streaming {
			name = "Stream"
		}
		t.Run(name, func(t *testing.T) {
			fake := newFakeDriver()
			fake.runFunc = func(context.Context, driver.Request, driver.EventSink) (driver.Response, error) {
				return alignmentPartialResponse(), cause
			}
			res, err := alignmentCall(t, adaptor.New(fake), context.Background(), streaming)
			re := alignmentRequirePartial(t, res, err, "infrastructure_error", cause)
			var typed *alignmentTransportError
			if !errors.As(err, &typed) || typed != cause {
				t.Fatalf("typed transport cause lost: %v", err)
			}
			alignmentAssertLayers(t, re.Result, alignmentPartialResponse())
			comparable := *re.Result
			comparable.RunID = ""
			if previous != nil && !reflect.DeepEqual(previous, &comparable) {
				t.Fatalf("Run and Stream partial Results differ: %#v != %#v", previous, comparable)
			}
			previous = &comparable
			if fake.runCount() != 1 {
				t.Fatalf("delivered prompt replayed %d times", fake.runCount())
			}
		})
	}
}

func alignmentFakeDriver() *fakeDriver {
	fake := newFakeDriver()
	desc := fake.Descriptor()
	desc.StructuredOutput = driver.StructuredOutputCapability{JSONSchemaNative: true, WorksWithRun: true, WorksWithStreaming: true, WorksWithHITL: true}
	fake.descriptor = &desc
	return fake
}

func alignmentSchema() adaptor.CallOption {
	return adaptor.WithSchemaJSON([]byte(`{"type":"object","properties":{"answer":{"type":"integer"}},"required":["answer"]}`))
}

func alignmentSeed(t *testing.T, runner adaptor.Runner, store threadstore.Store) *threadstore.Record {
	t.Helper()
	if _, err := runner.Run(context.Background(), "seed", alignmentSchema()); err != nil {
		t.Fatal(err)
	}
	record, err := store.Resolve(context.Background(), threadstore.Query{Key: "alignment-thread"})
	if err != nil || record == nil {
		t.Fatalf("seed record = %#v, %v", record, err)
	}
	return record
}

func TestAlignmentPartialResultOutcomeMatrix(t *testing.T) {
	for _, stateful := range []bool{false, true} {
		for _, streaming := range []bool{false, true} {
			for _, kind := range []string{"cancel", "deadline", "transport", "nonzero", "protocol", "approval-denied", "approval-timeout", "handler-error", "handler-panic", "approved-then-cancel"} {
				name := kind
				if stateful {
					name += "/Thread"
				} else {
					name += "/Agent"
				}
				if streaming {
					name += "/Stream"
				} else {
					name += "/Run"
				}
				t.Run(name, func(t *testing.T) {
					store := memory.NewStore()
					fake := alignmentFakeDriver()
					ctx, cancel := context.WithCancelCause(context.Background())
					defer cancel(nil)
					transport := &alignmentTransportError{message: "observed cause"}
					var cause error = transport
					reason := adaptor.ReasonInfrastructure
					var callback adaptor.ApprovalHandler
					policy := adaptor.Policy{}
					switch kind {
					case "cancel", "approved-then-cancel":
						reason = adaptor.ReasonCancelled
					case "deadline":
						reason, cause = adaptor.ReasonDeadlineExceeded, context.DeadlineExceeded
					case "nonzero", "protocol", "handler-panic":
						reason = adaptor.ReasonAgentError
					case "approval-denied":
						reason = adaptor.ReasonApprovalDenied
					case "approval-timeout":
						reason = adaptor.ReasonApprovalTimeout
					}
					if kind == "nonzero" || kind == "handler-panic" {
						cause = nil
					}
					switch kind {
					case "approval-denied":
						callback = adaptor.DenyAll("denied")
					case "approval-timeout":
						policy.Approvals.Timeout = time.Millisecond
						callback = func(ctx context.Context, _ *adaptor.ApprovalRequest) error { <-ctx.Done(); return ctx.Err() }
					case "handler-error":
						callback = func(context.Context, *adaptor.ApprovalRequest) error { return transport }
					case "handler-panic":
						callback = func(context.Context, *adaptor.ApprovalRequest) error { panic("fixture panic") }
					case "approved-then-cancel":
						callback = adaptor.ApproveAll()
					}
					var seed *threadstore.Record
					fake.runFunc = func(runCtx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
						resp := alignmentPartialResponse()
						if req.Prompt == "seed" {
							resp.Checkpoint.State.ResumeID = "healthy"
							return resp, nil
						}
						switch kind {
						case "cancel":
							cancel(transport)
							return resp, runCtx.Err()
						case "deadline":
							<-runCtx.Done()
							return resp, runCtx.Err()
						case "nonzero":
							resp.ExitCode = 7
							return resp, nil
						case "protocol":
							resp.Failure = &driver.RunFailure{Code: driver.FailureAgentError, Message: "malformed formal protocol"}
							return resp, transport
						case "approval-denied", "approval-timeout", "handler-error", "handler-panic", "approved-then-cancel":
							_, decisionErr := sink.(driver.DecisionCapableSink).RequestDecision(runCtx, driver.DecisionRequest{Kind: driver.HumanDecisionPermission, Prompt: "permit?"})
							if kind == "approved-then-cancel" && decisionErr != nil {
								t.Errorf("approval failed: %v", decisionErr)
							}
							if kind == "handler-error" {
								return resp, decisionErr
							}
							cancel(transport)
							return resp, errors.Join(decisionErr, runCtx.Err(), transport)
						default:
							return resp, transport
						}
					}
					agent := adaptor.New(fake, adaptor.WithThreadStore(store), adaptor.WithPolicy(policy), adaptor.OnApproval(callback))
					var runner adaptor.Runner = agent
					if stateful {
						runner = agent.Thread("alignment-thread")
						seed = alignmentSeed(t, runner, store)
					}
					if kind == "deadline" {
						deadlineCtx, stop := context.WithTimeout(ctx, 100*time.Millisecond)
						defer stop()
						ctx = deadlineCtx
					}
					res, err := alignmentCall(t, runner, ctx, streaming, alignmentSchema())
					re := alignmentRequirePartial(t, res, err, reason, cause)
					alignmentAssertLayers(t, re.Result, alignmentPartialResponse())
					if kind == "cancel" || kind == "approved-then-cancel" || kind == "approval-denied" || kind == "approval-timeout" {
						if !errors.Is(err, context.Canceled) {
							t.Fatalf("lost cancellation secondary cause: %v", err)
						}
					}
					if stateful {
						after, e := store.Resolve(context.Background(), threadstore.Query{Key: "alignment-thread"})
						if e != nil || !reflect.DeepEqual(seed, after) {
							t.Fatalf("failed run changed healthy store: before=%#v after=%#v err=%v", seed, after, e)
						}
					}
					wantRuns := 1
					if stateful {
						wantRuns++
					}
					if fake.runCount() != wantRuns {
						t.Fatalf("prompt replayed: runs=%d want=%d", fake.runCount(), wantRuns)
					}
				})
			}
		}
	}
}

type alignmentFaultStore struct {
	threadstore.Store
	active    atomic.Bool
	finalized atomic.Int32
	kind      string
	cause     error
	entered   chan struct{}
}

func (s *alignmentFaultStore) Finalize(ctx context.Context, req threadstore.FinalizeRequest) error {
	if s.active.Load() && s.kind == "finalize" {
		return s.cause
	}
	if err := s.Store.Finalize(ctx, req); err != nil {
		return err
	}
	s.finalized.Add(1)
	return nil
}
func (s *alignmentFaultStore) RenewLease(ctx context.Context, lease threadstore.Lease, ttl time.Duration) error {
	if s.active.Load() && s.kind == "renew" {
		select {
		case <-s.entered:
			return s.cause
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return s.Store.RenewLease(ctx, lease, ttl)
}
func (s *alignmentFaultStore) ReleaseLease(ctx context.Context, lease threadstore.Lease) error {
	err := s.Store.ReleaseLease(ctx, lease)
	if s.active.Load() && s.kind == "release" {
		return errors.Join(err, s.cause)
	}
	return err
}
func (s *alignmentFaultStore) AcquireLease(ctx context.Context, target, owner string, ttl time.Duration) (threadstore.Lease, error) {
	if s.active.Load() && s.kind == "fresh" {
		return threadstore.Lease{}, s.cause
	}
	return s.Store.AcquireLease(ctx, target, owner, ttl)
}

func TestAlignmentPartialResultThreadCoordinationAndCleanup(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, kind := range []string{"renew", "finalize", "missing-checkpoint", "release", "teardown", "failure-and-cleanup", "fresh"} {
			name := kind + "/Run"
			if streaming {
				name = kind + "/Stream"
			}
			t.Run(name, func(t *testing.T) {
				shortLeases(t, time.Second, 10*time.Millisecond)
				cause := &alignmentTransportError{message: kind + " failed"}
				store := &alignmentFaultStore{Store: memory.NewStore(), kind: kind, cause: cause, entered: make(chan struct{})}
				fake := alignmentFakeDriver()
				provider := &fakeProvider{name: "cleanup", log: &callLog{}, detach: func(ctx context.Context, _ string) error {
					if store.active.Load() && (kind == "teardown" || kind == "failure-and-cleanup") {
						if ctx.Err() != nil {
							t.Errorf("cleanup context already cancelled: %v", ctx.Err())
						}
						if _, ok := ctx.Deadline(); !ok {
							t.Error("cleanup context has no bound")
						}
						return cause
					}
					return nil
				}}
				reject := &engine.ResumeRejectedError{Reason: "checkpoint rejected before prompt"}
				fake.runFunc = func(ctx context.Context, req driver.Request, _ driver.EventSink) (driver.Response, error) {
					resp := alignmentPartialResponse()
					if req.Prompt == "seed" {
						resp.Checkpoint.State.ResumeID = "healthy"
						return resp, nil
					}
					switch kind {
					case "renew":
						close(store.entered)
						<-ctx.Done()
						return resp, ctx.Err()
					case "missing-checkpoint":
						resp.Checkpoint = nil
					case "failure-and-cleanup":
						resp.Failure = &driver.RunFailure{Code: driver.FailureReject, Message: "denied"}
						return resp, context.Canceled
					case "fresh":
						store.active.Store(true)
						return resp, reject
					}
					return resp, nil
				}
				agent := adaptor.New(fake, adaptor.WithThreadStore(store), adaptor.WithRunServices(provider))
				thread := agent.Thread("alignment-thread")
				seed := alignmentSeed(t, thread, store)
				if kind != "fresh" {
					store.active.Store(true)
				}
				res, err := alignmentCall(t, thread, context.Background(), streaming, alignmentSchema())
				reason := adaptor.ReasonInfrastructure
				var wantCause error = cause
				if kind == "missing-checkpoint" {
					wantCause = adaptor.ErrThreadCheckpointMissing
				}
				if kind == "failure-and-cleanup" {
					reason = adaptor.ReasonApprovalDenied
				}
				re := alignmentRequirePartial(t, res, err, reason, wantCause)
				alignmentAssertLayers(t, re.Result, alignmentPartialResponse())
				if kind == "fresh" && !errors.Is(err, reject) {
					t.Fatalf("lost resume rejection cause: %v", err)
				}
				if kind == "renew" && (!errors.Is(err, adaptor.ErrThreadLeaseLost) || !errors.Is(err, context.Canceled)) {
					t.Fatalf("lease cause/cancellation lost: %v", err)
				}
				if kind == "failure-and-cleanup" && !errors.Is(err, context.Canceled) {
					t.Fatalf("cleanup lost earlier cause: %v", err)
				}
				after, resolveErr := store.Resolve(context.Background(), threadstore.Query{Key: "alignment-thread"})
				if resolveErr != nil {
					t.Fatal(resolveErr)
				}
				if kind == "release" || kind == "teardown" {
					if after.State.ResumeID != "unhealthy-must-not-persist" || store.finalized.Load() != 2 {
						t.Fatalf("cleanup rolled back/repeated healthy commit: %#v count=%d", after, store.finalized.Load())
					}
				} else if !reflect.DeepEqual(seed, after) || store.finalized.Load() != 1 {
					t.Fatalf("unhealthy run modified store: before=%#v after=%#v saves=%d", seed, after, store.finalized.Load())
				}
				if fake.runCount() != 2 {
					t.Fatalf("prompt replayed: %d calls", fake.runCount())
				}
			})
		}
	}
}

func TestAlignmentPartialResultPrelaunchBoundary(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, stage := range []string{"workspace", "runtime", "thread-acquire", "closed", "cancelled"} {
			name := stage + "/Run"
			if streaming {
				name = stage + "/Stream"
			}
			t.Run(name, func(t *testing.T) {
				fake := newFakeDriver()
				cause := error(&alignmentTransportError{message: "preparation failed"})
				var opts []adaptor.Option
				switch stage {
				case "workspace":
					opts = append(opts, adaptor.WithWorkspaceManager(&fakeWorkspaceManager{err: cause, log: &callLog{}}), adaptor.WithWorkspace(t.TempDir()))
				case "runtime":
					opts = append(opts, adaptor.WithRunServices(&fakeProvider{name: "failed-attach", log: &callLog{}, attachErr: cause}))
				case "thread-acquire":
					opts = append(opts, adaptor.WithThreadStore(&acquireErrorStore{Store: memory.NewStore(), err: cause}))
				case "closed":
					cause = adaptor.ErrAgentClosed
				case "cancelled":
					cause = context.Canceled
					opts = append(opts, adaptor.WithWorkspaceManager(&fakeWorkspaceManager{err: cause, log: &callLog{}}), adaptor.WithWorkspace(t.TempDir()))
				}
				agent := adaptor.New(fake, opts...)
				var runner adaptor.Runner = agent
				if stage == "thread-acquire" {
					runner = agent.Thread("alignment-thread")
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if stage == "closed" {
					if err := agent.Close(ctx); err != nil {
						t.Fatal(err)
					}
				}
				if stage == "cancelled" {
					cancel()
				}
				res, err := alignmentCall(t, runner, ctx, streaming)
				var re *adaptor.RunError
				if res != nil || !errors.Is(err, cause) || errors.As(err, &re) || fake.runCount() != 0 {
					t.Fatalf("prelaunch boundary: result=%#v error=%v carrier=%#v calls=%d", res, err, re, fake.runCount())
				}
			})
		}
	}
}

func TestAlignmentPartialResultFirstCancellationKeepsThreadFresh(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		name := "Run"
		if streaming {
			name = "Stream"
		}
		t.Run(name, func(t *testing.T) {
			store := memory.NewStore()
			fake := alignmentFakeDriver()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			fake.runFunc = func(ctx context.Context, req driver.Request, _ driver.EventSink) (driver.Response, error) {
				if req.Session.State != nil {
					t.Errorf("first cancelled checkpoint contaminated next run: %#v", req.Session.State)
				}
				if req.Prompt == "seed" {
					return alignmentPartialResponse(), nil
				}
				cancel()
				return alignmentPartialResponse(), ctx.Err()
			}
			thread := adaptor.New(fake, adaptor.WithThreadStore(store)).Thread("alignment-thread")
			res, err := alignmentCall(t, thread, ctx, streaming, alignmentSchema())
			alignmentRequirePartial(t, res, err, adaptor.ReasonCancelled, context.Canceled)
			if _, err := thread.Checkpoint(context.Background()); !errors.Is(err, adaptor.ErrThreadNotFound) {
				t.Fatalf("cancelled first checkpoint: %v", err)
			}
			alignmentSeed(t, thread, store)
			if fake.runCount() != 2 {
				t.Fatalf("cancelled prompt replayed: %d", fake.runCount())
			}
		})
	}
}

func TestAlignmentPartialResultSafeFallbackAudit(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, outcome := range []string{"success", "failure", "rejected-again", "cancel-before-fallback", "cancel-after-prepare"} {
			name := outcome + "/Run"
			if streaming {
				name = outcome + "/Stream"
			}
			t.Run(name, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				base := memory.NewStore()
				var store threadstore.Store = base
				if outcome == "cancel-after-prepare" {
					store = &alignmentCancelFreshStore{Store: base, cancel: cancel}
				}
				fake := alignmentFakeDriver()
				rejected := &engine.ResumeRejectedError{Reason: "provider rejected resume before delivery"}
				cause := &alignmentTransportError{message: "fresh attempt stopped"}
				var runID string
				fake.runFunc = func(_ context.Context, req driver.Request, _ driver.EventSink) (driver.Response, error) {
					resp := alignmentPartialResponse()
					if req.Prompt == "seed" {
						resp.Checkpoint.State.ResumeID = "healthy"
						return resp, nil
					}
					if req.Session.State != nil {
						runID = req.RunID
						if outcome == "cancel-before-fallback" {
							cancel()
						}
						if cancelStore, ok := store.(*alignmentCancelFreshStore); ok {
							cancelStore.cancelNext = true
						}
						return resp, rejected
					}
					if req.RunID != runID {
						t.Errorf("fallback changed RunID: %s -> %s", runID, req.RunID)
					}
					resp.RawStreams.Stdout, resp.RawStreams.Stderr = "fresh stdout", "fresh stderr"
					resp.RawStreams.Terminal = &driver.TerminalPayload{Event: "fresh-result", JSON: json.RawMessage(`{"official":"fresh"}`)}
					switch outcome {
					case "failure":
						return resp, cause
					case "rejected-again":
						return resp, rejected
					default:
						return resp, nil
					}
				}
				thread := adaptor.New(fake, adaptor.WithThreadStore(store)).Thread("alignment-thread")
				seed := alignmentSeed(t, thread, store)
				res, err := alignmentCall(t, thread, ctx, streaming, alignmentSchema())
				wantCalls := 3
				if outcome == "cancel-before-fallback" || outcome == "cancel-after-prepare" {
					wantCalls = 2
				}
				if outcome == "success" {
					if err != nil || res == nil {
						t.Fatalf("safe fallback failed: %v", err)
					}
				} else {
					reason := adaptor.ReasonInfrastructure
					if wantCalls == 2 {
						reason = adaptor.ReasonCancelled
					}
					re := alignmentRequirePartial(t, res, err, reason, rejected)
					res = re.Result
					if outcome == "failure" && !errors.Is(err, cause) {
						t.Fatalf("lost fallback cause: %v", err)
					}
					if wantCalls == 2 && !errors.Is(err, context.Canceled) {
						t.Fatalf("lost cancel cause: %v", err)
					}
					after, e := base.Resolve(context.Background(), threadstore.Query{Key: "alignment-thread"})
					if e != nil || !reflect.DeepEqual(seed, after) {
						t.Fatalf("failed fallback changed healthy record: %#v / %#v / %v", seed, after, e)
					}
				}
				if fake.runCount() != wantCalls {
					t.Fatalf("fallback replay count=%d want=%d", fake.runCount(), wantCalls)
				}
				if wantCalls == 3 {
					if res.Raw().Stdout != "stdout bytesfresh stdout" || res.Raw().Stderr != "stderr bytesfresh stderr" || res.Raw().Terminal.Event != "fresh-result" ||
						len(res.Transcript()) != 2 || res.Usage.InputTokens != 4 || res.Usage.OutputTokens != 6 || len(res.Services()) != 1 {
						t.Fatalf("fallback lost observations: raw=%#v transcript=%#v usage=%#v services=%#v", res.Raw(), res.Transcript(), res.Usage, res.Services())
					}
				} else {
					alignmentAssertLayers(t, res, alignmentPartialResponse())
				}
			})
		}
	}
}

type alignmentCancelFreshStore struct {
	threadstore.Store
	cancel     context.CancelFunc
	cancelNext bool
}

func (s *alignmentCancelFreshStore) AcquireLease(ctx context.Context, target, owner string, ttl time.Duration) (threadstore.Lease, error) {
	lease, err := s.Store.AcquireLease(ctx, target, owner, ttl)
	if s.cancelNext {
		s.cancel()
	}
	return lease, err
}

func TestAlignmentPartialResultEnsuredServicesAndCleanupCauses(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		name := "Run"
		if streaming {
			name = "Stream"
		}
		t.Run(name, func(t *testing.T) {
			transport := &alignmentTransportError{message: "transport"}
			cleanup := errors.New("service release failed")
			manager := &fakeServiceManager{log: &callLog{}, ensure: func(context.Context, adaptor.ServiceRequest) ([]adaptor.ServiceRef, error) {
				return []adaptor.ServiceRef{{ID: "ensured", Name: "actual", Status: driver.RuntimeServiceRunning}}, nil
			}, release: func(context.Context, string) error { return cleanup }}
			fake := newFakeDriver()
			fake.runFunc = func(context.Context, driver.Request, driver.EventSink) (driver.Response, error) {
				return alignmentPartialResponse(), transport
			}
			agent := adaptor.New(fake, adaptor.WithServiceManager(manager), adaptor.WithServices(adaptor.ServiceSpec{ID: "declared"}))
			res, err := alignmentCall(t, agent, context.Background(), streaming)
			re := alignmentRequirePartial(t, res, err, adaptor.ReasonInfrastructure, transport)
			if !errors.Is(err, cleanup) || len(re.Result.Services()) != 2 {
				t.Fatalf("lost service observation/cleanup: %v %#v", err, re.Result.Services())
			}
			for _, report := range re.Result.Services() {
				if report.ID == "declared" {
					t.Fatal("declaration falsely reported as observed")
				}
			}
		})
	}
}

func TestAlignmentPartialResultRunErrorCauseContract(t *testing.T) {
	cause := &alignmentTransportError{message: "retained"}
	for _, tc := range []struct {
		reason   adaptor.FailureReason
		sentinel error
	}{
		{adaptor.ReasonApprovalDenied, adaptor.ErrApprovalDenied}, {adaptor.ReasonApprovalTimeout, adaptor.ErrApprovalTimeout},
		{adaptor.ReasonAgentError, adaptor.ErrAgentFailed}, {adaptor.ReasonCancelled, adaptor.ErrRunCancelled},
		{adaptor.ReasonPolicyViolation, adaptor.ErrPolicyViolation}, {adaptor.ReasonDeadlineExceeded, context.DeadlineExceeded},
		{adaptor.ReasonInfrastructure, nil}, {"extension", nil}, {"", nil},
	} {
		t.Run(string(tc.reason), func(t *testing.T) {
			err := &adaptor.RunError{Reason: tc.reason, Cause: errors.Join(cause, context.Canceled)}
			var typed *alignmentTransportError
			if !errors.Is(err, cause) || !errors.As(err, &typed) || typed != cause || !errors.Is(err, context.Canceled) || (tc.sentinel != nil && !errors.Is(err, tc.sentinel)) {
				t.Fatalf("cause chain = %v", err)
			}
		})
	}
	var nilError *adaptor.RunError
	if nilError.Unwrap() != nil || nilError.Error() == "" || (&adaptor.RunError{}).Unwrap() != nil {
		t.Fatal("nil/zero error contract broken")
	}
}

func TestAlignmentPartialResultDoesNotValidateInterruptedSchema(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, data := range []string{"missing", "invalid"} {
			name := data + "/Run"
			if streaming {
				name = data + "/Stream"
			}
			t.Run(name, func(t *testing.T) {
				fake := alignmentFakeDriver()
				cause := &alignmentTransportError{message: "interrupted"}
				fake.runFunc = func(context.Context, driver.Request, driver.EventSink) (driver.Response, error) {
					resp := alignmentPartialResponse()
					resp.Output = `{"answer":"wrong type"}`
					resp.StructuredOutput = nil
					if data == "invalid" {
						resp.StructuredOutput = &driver.StructuredOutput{Valid: false, RawJSON: []byte(resp.Output), ValidationErrors: []string{"provider data is incomplete"}}
					}
					return resp, cause
				}
				res, err := alignmentCall(t, adaptor.New(fake), context.Background(), streaming, alignmentSchema())
				re := alignmentRequirePartial(t, res, err, adaptor.ReasonInfrastructure, cause)
				var decoded map[string]any
				if e := re.Result.Decode(&decoded); e == nil || errors.Is(err, adaptor.ErrPolicyViolation) {
					t.Fatalf("interruption validated schema or decoded unchecked Text: decode=%v error=%v", e, err)
				}
			})
		}
	}
}

func TestAlignmentPartialResultMergedTerminalReasonMatchesCarrier(t *testing.T) {
	cause := &alignmentTransportError{message: "provider transport interrupted"}
	fake := newFakeDriver()
	fake.runFunc = func(_ context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
		if err := sink.EmitStream(driver.StreamPayload{Kind: driver.StreamRunStarted, RunID: req.RunID}); err != nil {
			return driver.Response{}, err
		}
		if err := sink.EmitStream(driver.StreamPayload{Kind: driver.StreamRunFinished, RunID: req.RunID}); err != nil {
			return driver.Response{}, err
		}
		return alignmentPartialResponse(), cause
	}
	provider := &fakeProvider{name: "merged-events", log: &callLog{}, attachment: adaptor.RunAttachment{Events: func(context.Context, string) <-chan adaptor.Event {
		events := make(chan adaptor.Event)
		close(events)
		return events
	}}}
	st := adaptor.New(fake, adaptor.WithRunServices(provider)).Stream(context.Background(), "work")
	var terminal *adaptor.RunFinished
	count := 0
	for ev := range st.Events() {
		if finished, ok := ev.(adaptor.RunFinished); ok {
			terminal = &finished
			count++
		}
	}
	res, err := st.Result()
	re := alignmentRequirePartial(t, res, err, adaptor.ReasonInfrastructure, cause)
	if count != 1 || terminal == nil || !terminal.Failed || terminal.Reason != re.Reason || terminal.Message != re.Message {
		t.Fatalf("terminal diverged from carrier: %#v / %#v", terminal, re)
	}
}
