package subagentstream_test

import (
	"context"
	"errors"
	"fmt"
	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/bridges/subagentstream"
	"github.com/agent-dance/agent-adaptor/hosttools/a2adelegation"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

type reviewerStream struct {
	events   chan adaptor.Event
	result   *adaptor.Result
	err      error
	complete atomic.Bool
}

func (s *reviewerStream) Events() <-chan adaptor.Event { return s.events }
func (s *reviewerStream) Result() (*adaptor.Result, error) {
	s.complete.Store(true)
	return s.result, s.err
}
func (s *reviewerStream) RunID() string { return "opaque / run\nID" }
func (s *reviewerStream) Cancel()       {}

type reviewerBus struct {
	parent    *reviewerStream
	bound     bool
	queried   atomic.Int32
	premature atomic.Bool
}

func (b *reviewerBus) SubscribeRun(context.Context, string) <-chan a2adelegation.DelegationEvent {
	panic("Merge must not subscribe to the bus")
}
func (b *reviewerBus) RunEventsBound(id string) bool {
	b.queried.Add(1)
	if !b.parent.complete.Load() || id != b.parent.RunID() {
		b.premature.Store(true)
	}
	return b.bound
}

type reviewerOuterError struct {
	parent error
	marker error
}

func (e *reviewerOuterError) Error() string        { return "outer diagnostic: " + e.parent.Error() }
func (e *reviewerOuterError) Unwrap() error        { return e.parent }
func (e *reviewerOuterError) Is(target error) bool { return target == e.marker }

func TestReviewerMergeParentCauseChain(t *testing.T) {
	for _, shape := range []string{"direct", "join-sibling", "typed-wrapper", "fmt-wrapper"} {
		t.Run(shape, func(t *testing.T) {
			marker := errors.New("outer cleanup diagnostic")
			partial := &adaptor.Result{RunID: "opaque / run\nID", Text: "partial", Summary: "audit", Metadata: map[string]string{"checkpoint": "unchanged"}}
			original := &adaptor.RunError{Reason: adaptor.ReasonApprovalDenied, Message: "primary denial", Details: map[string]any{"nested": map[string]any{"preserve": "original"}}, Result: partial, Cause: errors.Join(context.Canceled, context.DeadlineExceeded)}
			var parent error = original
			switch shape {
			case "join-sibling":
				parent = errors.Join(marker, original)
			case "typed-wrapper":
				parent = &reviewerOuterError{parent: original, marker: marker}
			case "fmt-wrapper":
				parent = fmt.Errorf("cleanup: %w: %w", marker, original)
			}
			s := &reviewerStream{events: make(chan adaptor.Event), err: parent}
			close(s.events)
			b := &reviewerBus{parent: s}
			m := subagentstream.Merge(context.Background(), s, b)
			for range m.Events() {
			}
			result, err := m.Result()
			var got *adaptor.RunError
			if result != nil || !errors.As(err, &got) {
				t.Fatalf("result=%#v err=%v", result, err)
			}
			if got == original || got.Reason != original.Reason || got.Message != original.Message || got.Result != partial || !reflect.DeepEqual(got.Details, original.Details) {
				t.Fatalf("primary or partial changed: %+v", got)
			}
			got.Details["nested"].(map[string]any)["preserve"] = "changed"
			if original.Details["nested"].(map[string]any)["preserve"] != "original" || errors.Is(original, subagentstream.ErrEventInjectionUnsupported) {
				t.Fatal("mutated parent")
			}
			for _, cause := range []error{parent, original, context.Canceled, context.DeadlineExceeded, adaptor.ErrApprovalDenied, subagentstream.ErrEventInjectionUnsupported} {
				if !errors.Is(err, cause) {
					t.Errorf("lost cause %v", cause)
				}
			}
			if shape == "typed-wrapper" {
				var outer *reviewerOuterError
				if !errors.As(err, &outer) || outer != parent {
					t.Fatal("lost typed outer wrapper")
				}
			}
			if shape != "direct" && !errors.Is(err, marker) {
				t.Errorf("lost outer cause marker: parent errors.Is=%v, merged errors.Is=%v, merged=%v", errors.Is(parent, marker), errors.Is(err, marker), err)
			}
			if b.queried.Load() != 1 || b.premature.Load() {
				t.Fatal("binding proof queried outside completed parent")
			}
			again, againErr := m.Result()
			if again != result || againErr != err {
				t.Fatal("Result not cached")
			}
		})
	}
}

func TestReviewerMergeBareDeadline(t *testing.T) {
	for _, shape := range []string{"bare", "wrapped", "join"} {
		t.Run(shape, func(t *testing.T) {
			marker := errors.New("deadline diagnostic")
			var cause error = context.DeadlineExceeded
			switch shape {
			case "wrapped":
				cause = fmt.Errorf("parent deadline: %w", cause)
			case "join":
				cause = errors.Join(marker, cause)
			}
			s := &reviewerStream{events: make(chan adaptor.Event), result: &adaptor.Result{Text: "partial before deadline"}, err: cause}
			close(s.events)
			m := subagentstream.Merge(context.Background(), s, &reviewerBus{parent: s})
			for range m.Events() {
			}
			_, err := m.Result()
			var got *adaptor.RunError
			if !errors.As(err, &got) {
				t.Fatal(err)
			}
			if got.Reason != adaptor.ReasonDeadlineExceeded {
				t.Errorf("parent deadline became primary %q; cause retains deadline=%v", got.Reason, errors.Is(err, context.DeadlineExceeded))
			}
			if got.Result != s.result || !errors.Is(err, cause) || !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, subagentstream.ErrEventInjectionUnsupported) {
				t.Fatal("lost partial/cause")
			}
			if shape == "join" && !errors.Is(err, marker) {
				t.Fatal("lost joined deadline diagnostic")
			}
		})
	}
}

func TestReviewerMergeBoundTransparent(t *testing.T) {
	s := &reviewerStream{events: make(chan adaptor.Event, 2), result: &adaptor.Result{Text: "complete"}, err: errors.New("parent failure")}
	original := adaptor.WithEventMeta(adaptor.Notice{Kind: adaptor.NoticeRuntime, Data: map[string]any{"nested": []any{"payload"}}}, adaptor.EventMeta{RunID: s.RunID(), ThreadKey: "host / opaque", Sequence: 73, Time: time.Date(2026, 9, 7, 1, 2, 3, 4, time.UTC), TurnID: "turn", Source: &adaptor.EventSourceMeta{RunID: "source", Sequence: 999, ScopeID: "scope", Upstream: &adaptor.EventSourceMeta{RunID: "origin"}}})
	terminal := adaptor.WithEventMeta(adaptor.RunFinished{RunID: s.RunID(), Failed: true, Reason: adaptor.ReasonInfrastructure}, adaptor.EventMeta{Sequence: 100})
	s.events <- original
	s.events <- terminal
	close(s.events)
	b := &reviewerBus{parent: s, bound: true}
	m := subagentstream.Merge(context.Background(), s, b)
	var out []adaptor.Event
	for ev := range m.Events() {
		out = append(out, ev)
	}
	if !reflect.DeepEqual(out, []adaptor.Event{original, terminal}) {
		t.Fatalf("stream mutated: %#v", out)
	}
	r, err := m.Result()
	if r != s.result || err != s.err || b.queried.Load() != 1 || b.premature.Load() {
		t.Fatal("result/error/binding proof changed")
	}
}
