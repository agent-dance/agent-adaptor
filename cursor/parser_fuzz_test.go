package cursor

import (
	"strings"
	"testing"

	"github.com/agent-dance/agent-adaptor/driver"
)

// FuzzCursorBatchParser keeps Cursor's official stream-json parser total over
// arbitrary stdout bytes and locks the process-outcome checkpoint boundary.
func FuzzCursorBatchParser(f *testing.F) {
	f.Add([]byte("{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"seed-cursor\"}\n{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":\"ok\",\"session_id\":\"seed-cursor\"}\n"), 1)
	f.Add([]byte("{\"type\":\"result\",\"subtype\":\"error\",\"is_error\":true,\"session_id\":\"seed-failed\"}\n"), -1)
	f.Add([]byte("{broken\n{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"session_id\":\"seed-malformed\"}\n"), 2)
	f.Add([]byte(nil), 255)

	f.Fuzz(func(t *testing.T, stdout []byte, exitCode int) {
		if exitCode == 0 {
			exitCode = 1
		}
		parsed := snapshotCursorStdout(string(stdout))
		assertCursorFuzzCheckpointShape(t, parsed.checkpointForOutcome(0, "", false, nil))
		if checkpoint := parsed.checkpointForOutcome(exitCode, "", false, nil); checkpoint != nil {
			t.Fatalf("non-zero exit %d produced checkpoint %#v", exitCode, checkpoint)
		}
		poisoned := snapshotCursorStdout("{broken\n" + string(stdout))
		if checkpoint := poisoned.checkpointForOutcome(0, "", false, nil); checkpoint != nil {
			t.Fatalf("malformed protocol produced checkpoint %#v", checkpoint)
		}
	})
}

func assertCursorFuzzCheckpointShape(t *testing.T, checkpoint *driver.Checkpoint) {
	t.Helper()
	if checkpoint == nil {
		return
	}
	if !checkpoint.Valid || checkpoint.State == nil || strings.TrimSpace(checkpoint.State.ResumeID) == "" {
		t.Fatalf("parser produced malformed checkpoint %#v", checkpoint)
	}
}
