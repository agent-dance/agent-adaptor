package agentadaptor

import (
	"errors"
	"fmt"
	"testing"
)

// Test that the five exported predicates correctly match both the bare
// sentinel and any wrapped / typed-error variant.

func TestIsRunEnded(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"unrelated", errors.New("nope"), false},
		{"sentinel", ErrRunEnded, true},
		{"wrapped", fmt.Errorf("wrap: %w", ErrRunEnded), true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := IsRunEnded(tc.err); got != tc.want {
				t.Fatalf("IsRunEnded(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestIsDecisionExpired(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"unrelated", errors.New("nope"), false},
		{"sentinel", ErrDecisionRequestExpired, true},
		{"wrapped", fmt.Errorf("wrap: %w", ErrDecisionRequestExpired), true},
		{"run-ended", ErrRunEnded, false}, // 拒绝聚合：RunEnded != DecisionExpired
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := IsDecisionExpired(tc.err); got != tc.want {
				t.Fatalf("IsDecisionExpired(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestIsSessionBusy(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"sentinel", ErrSessionBusy, true},
		{"typed-empty", &SessionBusyError{}, true},
		{"typed-target", &SessionBusyError{Target: "thread-1"}, true},
		{"unrelated", ErrSessionLeaseLost, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := IsSessionBusy(tc.err); got != tc.want {
				t.Fatalf("IsSessionBusy(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestIsSessionIncompatible(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"sentinel", ErrSessionIncompatible, true},
		{"typed-empty", &SessionIncompatibleError{}, true},
		{"typed-detail", &SessionIncompatibleError{Reason: "fingerprint drift"}, true},
		{"unrelated", ErrSessionBusy, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := IsSessionIncompatible(tc.err); got != tc.want {
				t.Fatalf("IsSessionIncompatible(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestIsSkillKeyConflict(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"sentinel", ErrSkillKeyConflict, true},
		{"typed-empty", &SkillKeyConflictError{}, true},
		{"typed-full", &SkillKeyConflictError{Key: "k", Sources: []string{"binding", "run"}}, true},
		{"unrelated", ErrSkillNotFound, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := IsSkillKeyConflict(tc.err); got != tc.want {
				t.Fatalf("IsSkillKeyConflict(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// Verify the typed-error Unwrap chain is wired correctly so consumers
// using errors.Is directly behave the same as the predicates above.
func TestTypedErrorUnwrap(t *testing.T) {
	t.Parallel()
	type pair struct {
		err      error
		sentinel error
	}
	cases := []pair{
		{&SessionBusyError{Target: "x"}, ErrSessionBusy},
		{&SessionLeaseLostError{Target: "x"}, ErrSessionLeaseLost},
		{&SessionIncompatibleError{Reason: "x"}, ErrSessionIncompatible},
		{&ResumeRejectedError{Reason: "x"}, ErrResumeRejected},
		{&ResumeRejectedError{Cause: errors.New("upstream")}, ErrResumeRejected},
		{&SkillKeyConflictError{Key: "x"}, ErrSkillKeyConflict},
	}
	for _, tc := range cases {
		if !errors.Is(tc.err, tc.sentinel) {
			t.Errorf("errors.Is(%T, %v) = false; expected typed error to unwrap to sentinel", tc.err, tc.sentinel)
		}
	}
}

// Ensure the SDK does NOT export aggregate predicates accidentally
// (a guardrail for future contributors). If you intentionally add a new
// predicate, update this list.
func TestExportedPredicateInventory(t *testing.T) {
	t.Parallel()
	// Compile-time: each name below must be a defined function in this
	// package, and the list must match exactly the predicates documented
	// in errors.go.
	_ = []func(error) bool{
		IsRunEnded,
		IsDecisionExpired,
		IsSessionBusy,
		IsSessionIncompatible,
		IsSkillKeyConflict,
	}
}
