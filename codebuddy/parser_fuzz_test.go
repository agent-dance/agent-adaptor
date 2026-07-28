package codebuddy

import (
	"strings"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
)

// FuzzCodeBuddyBatchParser keeps CodeBuddy's official stream-json parser
// total over arbitrary stdout bytes and locks the process-outcome checkpoint
// boundary.
func FuzzCodeBuddyBatchParser(f *testing.F) {
	f.Add([]byte("{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"seed-codebuddy\"}\n{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":\"ok\",\"session_id\":\"seed-codebuddy\"}\n"), 1)
	f.Add([]byte("{\"type\":\"result\",\"subtype\":\"error_during_execution\",\"session_id\":\"seed-failed\"}\n"), -1)
	f.Add([]byte("{broken\n{\"type\":\"result\",\"subtype\":\"success\",\"session_id\":\"seed-malformed\"}\n"), 2)
	f.Add([]byte(nil), 255)

	f.Fuzz(func(t *testing.T, stdout []byte, exitCode int) {
		if exitCode == 0 {
			exitCode = 1
		}
		parsed := newParser(nil)
		_ = parsed.onChunk("stdout", stdout, time.Unix(0, 0).UTC())
		parsed.finalize()
		assertCodeBuddyFuzzCheckpointShape(t, parsed.checkpointForOutcome(0, "", false, nil))
		if checkpoint := parsed.checkpointForOutcome(exitCode, "", false, nil); checkpoint != nil {
			t.Fatalf("non-zero exit %d produced checkpoint %#v", exitCode, checkpoint)
		}
		poisoned := newParser(nil)
		_ = poisoned.onChunk("stdout", append([]byte("{broken\n"), stdout...), time.Unix(0, 0).UTC())
		poisoned.finalize()
		if checkpoint := poisoned.checkpointForOutcome(0, "", false, nil); checkpoint != nil {
			t.Fatalf("malformed protocol produced checkpoint %#v", checkpoint)
		}
	})
}

func assertCodeBuddyFuzzCheckpointShape(t *testing.T, checkpoint *driver.Checkpoint) {
	t.Helper()
	if checkpoint == nil {
		return
	}
	if !checkpoint.Valid || checkpoint.State == nil || strings.TrimSpace(checkpoint.State.ResumeID) == "" {
		t.Fatalf("parser produced malformed checkpoint %#v", checkpoint)
	}
}
