package agentadaptor_test

import (
	"context"
	"testing"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/memory"
)

// TestStartHumanDecisionFailureWithoutCheckpointDoesNotSurfaceCheckpointError
// guards the documented Run/Start symmetry for the "HITL rejected + no
// new checkpoint" scenario. Run() is already covered by
// TestHumanDecisionFailureWithoutCheckpointDoesNotPersistSession; this is
// the Start() mirror. Both paths must return a structured RunFailure with
// err=nil, never surface ErrSessionCheckpointMissing up to the host.
func TestStartHumanDecisionFailureWithoutCheckpointDoesNotSurfaceCheckpointError(t *testing.T) {
	store := memory.NewSessionStore()
	driver := &fakeDriver{}
	sdk := newSDK(store, fakeBinding("default", driver), nil)

	first, err := sdk.Run(context.Background(), "hello", agentadaptor.WithSessionKey("company", "issue-1"))
	if err != nil {
		t.Fatalf("seed run: %v", err)
	}
	if first.Session == nil || first.Session.ID == "" {
		t.Fatalf("seed session missing: %#v", first.Session)
	}

	driver.humanRejectNoCP = true
	handle, err := sdk.Start(context.Background(), "reject", agentadaptor.WithSessionKey("company", "issue-1"))
	if err != nil {
		t.Fatalf("sdk.Start: %v", err)
	}

	// Drain the stream channel in the background so the runner never
	// blocks on Emit. We only assert on the Wait() result here.
	if ch := handle.StreamEvents(); ch != nil {
		go func() {
			for range ch {
			}
		}()
	}
	if ch := handle.Events(); ch != nil {
		go func() {
			for range ch {
			}
		}()
	}

	waitCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result, err := handle.Wait(waitCtx)
	if err != nil {
		t.Fatalf("Start+HITL reject must not surface checkpoint error, got %v", err)
	}
	if result.Failure == nil || !result.Failure.IsHumanDecision() || !result.Failure.IsRejected() {
		t.Fatalf("expected structured human decision rejection, got %+v", result.Failure)
	}
	if result.Session != nil {
		t.Fatalf("failed run without checkpoint must not persist a new session, got %#v", result.Session)
	}
}
