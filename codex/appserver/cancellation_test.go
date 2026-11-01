package appserver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/agent-dance/agent-adaptor/driver"
)

func TestSnapshotCancellationPreventsCheckpointAtAnyExit(t *testing.T) {
	for _, tc := range []struct {
		name  string
		exit  int
		cause error
		want  driver.FailureCode
	}{
		{"healthy", 0, nil, ""},
		{"failed exit", 7, nil, driver.FailureAgentError},
		{"external kill", -1, nil, driver.FailureAgentError},
		{"cancelled kill", -1, context.Canceled, driver.FailureCancelled},
		{"cancel races completed turn", 0, context.Canceled, driver.FailureCancelled},
		{"deadline", -1, context.DeadlineExceeded, driver.FailureCancelled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := newRunState("sdk-run", &recordingSink{})
			state.setThread("t")
			state.setTurn("turn")
			state.onNotification(NotifyTurnCompleted, json.RawMessage(`{"threadId":"t","turn":{"id":"turn","status":"completed"}}`))
			result := state.snapshot(Options{}, "t", "raw stdout", "raw stderr", tc.exit, "", tc.cause == context.DeadlineExceeded, tc.cause)
			if tc.want == "" {
				if result.Failure != nil || result.Checkpoint == nil || !result.Checkpoint.Valid {
					t.Fatalf("healthy result=%+v", result)
				}
			} else {
				if result.Failure == nil || result.Failure.Code != tc.want || result.Checkpoint != nil {
					t.Fatalf("classified result=%+v", result)
				}
			}
			if result.RawStreams.Stdout != "raw stdout" || result.RawStreams.Stderr != "raw stderr" || result.RawStreams.Terminal == nil {
				t.Fatal("raw protocol evidence lost")
			}
		})
	}
}
