package codex

import (
	"strings"
	"testing"

	"github.com/agent-dance/agent-adaptor/driver"
)

// FuzzCodexBatchParser keeps the official Codex JSONL parser total over
// arbitrary stdout bytes and locks the process-outcome checkpoint boundary.
func FuzzCodexBatchParser(f *testing.F) {
	f.Add([]byte("{\"type\":\"thread.started\",\"thread_id\":\"seed-codex\"}\n{\"type\":\"turn.completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}\n"), 1)
	f.Add([]byte("{\"type\":\"thread.started\",\"thread_id\":\"seed-failed\"}\n{\"type\":\"turn.failed\",\"error\":{\"message\":\"failed\"}}\n"), -1)
	f.Add([]byte("{broken\n{\"type\":\"thread.started\",\"thread_id\":\"seed-malformed\"}\n{\"type\":\"turn.completed\"}\n"), 2)
	f.Add([]byte(nil), 255)

	f.Fuzz(func(t *testing.T, stdout []byte, exitCode int) {
		if exitCode == 0 {
			exitCode = 1
		}
		parsed := snapshotCodexStdout(string(stdout))
		assertCodexFuzzCheckpointShape(t, parsed.checkpointForOutcome(0, "", false, nil))
		if checkpoint := parsed.checkpointForOutcome(exitCode, "", false, nil); checkpoint != nil {
			t.Fatalf("non-zero exit %d produced checkpoint %#v", exitCode, checkpoint)
		}
		if structured := parsed.nativeStructuredOutputForOutcome(exitCode, "", false, nil); structured != nil {
			t.Fatalf("non-zero exit %d produced native structured output %#v", exitCode, structured)
		}
		poisoned := snapshotCodexStdout("{broken\n" + string(stdout))
		if checkpoint := poisoned.checkpointForOutcome(0, "", false, nil); checkpoint != nil {
			t.Fatalf("malformed protocol produced checkpoint %#v", checkpoint)
		}
	})
}

func assertCodexFuzzCheckpointShape(t *testing.T, checkpoint *driver.Checkpoint) {
	t.Helper()
	if checkpoint == nil {
		return
	}
	if !checkpoint.Valid || checkpoint.State == nil || strings.TrimSpace(checkpoint.State.ResumeID) == "" {
		t.Fatalf("parser produced malformed checkpoint %#v", checkpoint)
	}
}
