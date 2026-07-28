package claude

import (
	"strings"
	"testing"

	"github.com/agent-dance/agent-adaptor/driver"
)

// FuzzClaudeBatchParser keeps the official stream-json parser total over
// arbitrary stdout bytes. The successful result seed is intentionally paired
// with a non-zero process outcome: provider success text must never override
// the process-level checkpoint safety gate.
func FuzzClaudeBatchParser(f *testing.F) {
	f.Add([]byte("{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"seed-claude\"}\n{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"session_id\":\"seed-claude\",\"result\":\"done\"}\n"), 1)
	f.Add([]byte("{\"type\":\"result\",\"subtype\":\"error_during_execution\",\"session_id\":\"seed-failed\"}\n"), -1)
	f.Add([]byte("{broken\n{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"session_id\":\"seed-malformed\",\"result\":\"done\"}\n"), 2)
	f.Add([]byte("{\"event\":\"result\",\"subtype\":\"success\",\"sessionId\":\"seed-alias\",\"result\":\"done\"}\n"), 3)
	f.Add([]byte(nil), 255)

	f.Fuzz(func(t *testing.T, stdout []byte, exitCode int) {
		if exitCode == 0 {
			exitCode = 1
		}
		parsed := snapshotClaudeStdout(string(stdout))
		checkpoint := parsed.checkpointForOutcome(0, "", false, nil)
		assertClaudeFuzzCheckpointShape(t, checkpoint)
		if checkpoint != nil && (!parsed.terminalSeen || !parsed.terminalSuccess || parsed.protocolMalformed || checkpoint.State.ResumeID != parsed.terminalSessionID) {
			t.Fatalf("checkpoint was not sourced from one clean formal terminal: parser=%+v checkpoint=%#v", parsed, checkpoint)
		}
		if checkpoint := parsed.checkpointForOutcome(exitCode, "", false, nil); checkpoint != nil {
			t.Fatalf("non-zero exit %d produced checkpoint %#v", exitCode, checkpoint)
		}
		poisoned := snapshotClaudeStdout("{broken\n" + string(stdout))
		if checkpoint := poisoned.checkpointForOutcome(0, "", false, nil); checkpoint != nil {
			t.Fatalf("malformed protocol produced checkpoint %#v", checkpoint)
		}
	})
}

func assertClaudeFuzzCheckpointShape(t *testing.T, checkpoint *driver.Checkpoint) {
	t.Helper()
	if checkpoint == nil {
		return
	}
	if !checkpoint.Valid || checkpoint.State == nil || strings.TrimSpace(checkpoint.State.ResumeID) == "" {
		t.Fatalf("parser produced malformed checkpoint %#v", checkpoint)
	}
}
