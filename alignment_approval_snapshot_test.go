package adaptor_test

// R013 regression derived from C01's independent fixed-G02 public fixture.
// Descriptions are copied; response authority remains run-owned and shared.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
)

type asDriver struct {
	run func(context.Context, driver.Request, driver.EventSink) (driver.Response, error)
}

func (asDriver) Descriptor() driver.Descriptor {
	return driver.Descriptor{Type: "independent-approval-snapshot", RunPolicyCaps: driver.RunPolicyCapabilities{Permission: driver.HumanDecisionSupport{Ask: true, AutoApprove: true, AutoReject: true}, PlanReview: driver.HumanDecisionSupport{Ask: true, AutoApprove: true, AutoReject: true}, Question: driver.QuestionSupport{Ask: true, AutoReject: true}}}
}
func (asDriver) ValidateConfig(any) error { return nil }
func (d asDriver) Run(c context.Context, r driver.Request, s driver.EventSink) (driver.Response, error) {
	return d.run(c, r, s)
}
func asPolicy() adaptor.SharedOption {
	return adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAsk, PlanReview: adaptor.ApprovalAsk, Question: adaptor.QuestionAsk, OnReject: adaptor.FallbackContinue}})
}

var asKinds = []driver.HumanDecisionKind{driver.HumanDecisionPermission, driver.HumanDecisionPlanReview, driver.HumanDecisionQuestion}

func asResolve(c context.Context, q *adaptor.ApprovalRequest) error {
	if q.Kind == adaptor.ApprovalQuestion {
		return q.Answer(c, "yes")
	}
	return q.Approve(c)
}
func asWait[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(3 * time.Second):
		t.Fatal("approval review timeout")
		var v T
		return v
	}
}
func asPayload() map[string]any {
	return map[string]any{"top": "original", "nested": map[string]any{"value": "original"}, "slice": []any{map[string]any{"value": "original"}}, "typed": map[string][]string{"value": {"original"}}, "array": [1]map[string]string{{"value": "original"}}, "bytes": []byte("original")}
}
func asMutate(m map[string]any) {
	m["top"] = "changed"
	m["nested"].(map[string]any)["value"] = "changed"
	m["slice"].([]any)[0].(map[string]any)["value"] = "changed"
	m["typed"].(map[string][]string)["value"][0] = "changed"
	m["array"].([1]map[string]string)[0]["value"] = "changed"
	m["bytes"].([]byte)[0] = 'X'
}
func asAssertOriginal(t *testing.T, name string, m map[string]any, checkTop bool) {
	t.Helper()
	if checkTop && m["top"] != "original" {
		t.Errorf("%s top map aliases copied request", name)
	}
	if m["nested"].(map[string]any)["value"] != "original" {
		t.Errorf("%s nested map aliases copied request", name)
	}
	if m["slice"].([]any)[0].(map[string]any)["value"] != "original" {
		t.Errorf("%s nested slice aliases copied request", name)
	}
	if m["typed"].(map[string][]string)["value"][0] != "original" {
		t.Errorf("%s typed map/slice aliases copied request", name)
	}
	if m["array"].([1]map[string]string)[0]["value"] != "original" {
		t.Errorf("%s array-contained map aliases copied request", name)
	}
	if string(m["bytes"].([]byte)) != "original" {
		t.Errorf("%s byte slice aliases copied request", name)
	}
}
func asRequest(kind driver.HumanDecisionKind, payload map[string]any) driver.DecisionRequest {
	return driver.DecisionRequest{Kind: kind, Prompt: "public pending request", Payload: payload, Choices: []driver.DecisionChoice{{Key: "yes", Label: "Original", Description: "Original description"}}}
}

// Expected red on G02: isolate the constructor from WithEventMeta and broker
// copies by observing the original Driver payload after the callback returns.
func TestAlignmentApprovalSnapshotConstructorIndependent(t *testing.T) {
	for _, kind := range asKinds {
		t.Run(string(kind), func(t *testing.T) {
			payload := asPayload()
			choices := []driver.DecisionChoice{{Key: "yes", Label: "Original"}}
			d := asDriver{run: func(c context.Context, _ driver.Request, s driver.EventSink) (driver.Response, error) {
				req := asRequest(kind, payload)
				req.Choices = choices
				_, e := s.(driver.DecisionCapableSink).RequestDecision(c, req)
				asAssertOriginal(t, "driver input", payload, true)
				if choices[0].Label != "Original" {
					t.Error("constructor Choices alias driver input")
				}
				return driver.Response{Output: "done"}, e
			}}
			a := adaptor.New(d, asPolicy(), adaptor.OnApproval(func(c context.Context, q *adaptor.ApprovalRequest) error {
				asMutate(q.Details)
				q.Choices[0].Label = "handler label"
				return asResolve(c, q)
			}))
			defer a.Close(context.Background())
			if _, e := a.Run(context.Background(), "work"); e != nil {
				t.Fatal(e)
			}
		})
	}
}

// Expected red on G02: WithEventMeta must copy descriptions while keeping the
// live responder shared. Mutation is sequential to prove aliasing without a race.
func TestAlignmentApprovalSnapshotWithEventMetaIndependent(t *testing.T) {
	for _, kind := range asKinds {
		for _, route := range []string{"callback", "stream"} {
			t.Run(string(kind)+"/"+route, func(t *testing.T) {
				var original *adaptor.ApprovalRequest
				mutate := func(q *adaptor.ApprovalRequest) {
					original = q
					first := adaptor.WithEventMeta(q, adaptor.EventMeta{RunID: "replay-one", Sequence: 10}).(*adaptor.ApprovalRequest)
					other := adaptor.WithEventMeta(q, adaptor.EventMeta{RunID: "replay-two", Sequence: 20}).(*adaptor.ApprovalRequest)
					first.Choices[0].Label = "copy label"
					first.Choices[0].Key = "altered-key"
					asMutate(first.Details)
					for name, value := range map[string]*adaptor.ApprovalRequest{"live": q, "other consumer": other} {
						asAssertOriginal(t, name, value.Details, true)
						if value.Choices[0].Key != "yes" || value.Choices[0].Label != "Original" {
							t.Errorf("%s Choices aliases WithEventMeta copy: %+v", name, value.Choices)
						}
					}
					if q.Meta().RunID == "replay-one" || other.Meta().Sequence != 20 {
						t.Error("meta envelope changed across copies")
					}
				}
				d := asDriver{run: func(c context.Context, _ driver.Request, s driver.EventSink) (driver.Response, error) {
					reply, e := s.(driver.DecisionCapableSink).RequestDecision(c, asRequest(kind, asPayload()))
					if e == nil && kind == driver.HumanDecisionQuestion && reply.Choice != "yes" {
						t.Errorf("copy mutation changed live Answer classification: Choice=%q Text=%q", reply.Choice, reply.Text)
					}
					return driver.Response{Output: "done"}, e
				}}
				opts := []adaptor.Option{asPolicy()}
				if route == "callback" {
					opts = append(opts, adaptor.OnApproval(func(c context.Context, q *adaptor.ApprovalRequest) error { mutate(q); return asResolve(c, q) }))
				}
				a := adaptor.New(d, opts...)
				defer a.Close(context.Background())
				if route == "callback" {
					if _, e := a.Run(context.Background(), "work"); e != nil {
						t.Fatal(e)
					}
				} else {
					st := a.Stream(context.Background(), "work")
					for ev := range st.Events() {
						if q, ok := ev.(*adaptor.ApprovalRequest); ok {
							mutate(q)
							if e := asResolve(context.Background(), q); e != nil {
								t.Error(e)
							}
						}
					}
					if _, e := st.Result(); e != nil {
						t.Fatal(e)
					}
				}
				if original == nil {
					t.Fatal("no live request observed")
				}
			})
		}
	}
}

// Control expected green on G02 and after R013: all copies retain exactly one
// response right, including denial with the explicit Continue policy.
func TestAlignmentApprovalSnapshotResponderCopiesExactlyOnce(t *testing.T) {
	for _, kind := range asKinds {
		for _, action := range []string{"positive", "deny"} {
			t.Run(string(kind)+"/"+action, func(t *testing.T) {
				var settled atomic.Int32
				replies := make(chan driver.DecisionResponse, 1)
				d := asDriver{run: func(c context.Context, _ driver.Request, s driver.EventSink) (driver.Response, error) {
					reply, e := s.(driver.DecisionCapableSink).RequestDecision(c, asRequest(kind, asPayload()))
					replies <- reply
					return driver.Response{Output: "done"}, e
				}}
				a := adaptor.New(d, asPolicy())
				defer a.Close(context.Background())
				st := a.Stream(context.Background(), "work")
				seen := 0
				for ev := range st.Events() {
					q, ok := ev.(*adaptor.ApprovalRequest)
					if !ok {
						continue
					}
					seen++
					if kind == driver.HumanDecisionQuestion {
						if e := q.Approve(context.Background()); !errors.Is(e, adaptor.ErrApprovalKindMismatch) {
							t.Error(e)
						}
					} else {
						if e := q.Answer(context.Background(), "yes"); !errors.Is(e, adaptor.ErrApprovalKindMismatch) {
							t.Error(e)
						}
					}
					var wg sync.WaitGroup
					for i := range 24 {
						copy := adaptor.WithEventMeta(q, adaptor.EventMeta{RunID: fmt.Sprint(i)}).(*adaptor.ApprovalRequest)
						wg.Add(1)
						go func() {
							defer wg.Done()
							var e error
							if action == "deny" {
								e = copy.Deny(context.Background(), "declined")
							} else {
								e = asResolve(context.Background(), copy)
							}
							if e == nil {
								settled.Add(1)
							} else if !errors.Is(e, adaptor.ErrApprovalResolved) {
								t.Error(e)
							}
						}()
					}
					wg.Wait()
				}
				if _, e := st.Result(); e != nil {
					t.Fatal(e)
				}
				reply := asWait(t, replies)
				if seen != 1 || settled.Load() != 1 {
					t.Fatal("response rights multiplied", seen, settled.Load())
				}
				want := driver.DecisionApproved
				if action == "deny" {
					want = driver.DecisionRejected
				} else if kind == driver.HumanDecisionQuestion {
					want = driver.DecisionAnswered
					if reply.Choice != "yes" {
						t.Error("original choice lost", reply)
					}
				}
				if reply.Result != want {
					t.Fatal(reply)
				}
			})
		}
	}
}

func TestAlignmentApprovalSnapshotResponderZeroAndExpired(t *testing.T) {
	for _, q := range []*adaptor.ApprovalRequest{nil, {}, {Kind: adaptor.ApprovalQuestion, Choices: []adaptor.Choice{{Key: "yes"}}}} {
		for _, call := range []func() error{func() error { return q.Approve(context.Background()) }, func() error { return q.Deny(context.Background(), "no") }, func() error { return q.Answer(context.Background(), "yes") }} {
			done := make(chan error, 1)
			go func() { done <- call() }()
			if e := asWait(t, done); !errors.Is(e, adaptor.ErrApprovalUnavailable) {
				t.Fatal(e)
			}
		}
	}
	var nilRequest *adaptor.ApprovalRequest
	if adaptor.WithEventMeta(nilRequest, adaptor.EventMeta{}) != nil {
		t.Fatal("typed nil clone is not nil")
	}
	for _, kind := range asKinds {
		t.Run(string(kind), func(t *testing.T) {
			d := asDriver{run: func(c context.Context, _ driver.Request, s driver.EventSink) (driver.Response, error) {
				_, e := s.(driver.DecisionCapableSink).RequestDecision(c, asRequest(kind, asPayload()))
				return driver.Response{Output: "partial"}, e
			}}
			a := adaptor.New(d, asPolicy())
			defer a.Close(context.Background())
			st := a.Stream(context.Background(), "work")
			var copy *adaptor.ApprovalRequest
			for ev := range st.Events() {
				if q, ok := ev.(*adaptor.ApprovalRequest); ok {
					copy = adaptor.WithEventMeta(q, q.Meta()).(*adaptor.ApprovalRequest)
					st.Cancel()
				}
			}
			_, e := st.Result()
			if !errors.Is(e, context.Canceled) {
				t.Fatal(e)
			}
			if copy == nil {
				t.Fatal("no pending request")
			}
			if e := asResolve(context.Background(), copy); !errors.Is(e, adaptor.ErrApprovalExpired) || !errors.Is(e, adaptor.ErrApprovalResolved) {
				t.Fatal("late response not expired", e)
			}
		})
	}
}
