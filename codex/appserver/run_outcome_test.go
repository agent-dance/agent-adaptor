package appserver

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/agent-dance/agent-adaptor/v2/driver"
)

func TestFinalAppServerErrorInvalidatesCheckpointAndRetainsAudit(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "native"}[native], func(t *testing.T) {
			waitErr := errors.New("exec: canceling Cmd: TerminateProcess: Access is denied.")
			exitCode, signal, timedOut, fatal := processOutcome(waitErr, nil)
			if fatal != waitErr {
				t.Fatalf("wait diagnostic changed: %v", fatal)
			}
			// A clean process status and official successful terminal can precede
			// an infrastructure failure while reaping or draining the process.
			state := newRunState("wait-failure", &recordingSink{})
			state.setThread("thread-1")
			state.turnID = "turn-1"
			state.finalAgentText = `{"answer":42}`
			state.onNotification(NotifyTurnCompleted, json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-1","status":"completed","usage":{"inputTokens":2,"outputTokens":1}}}`))
			result := state.snapshot(Options{Model: "model"}, "thread-1", "raw stdout", "raw stderr", exitCode, signal, timedOut)
			if result.Checkpoint == nil || !result.Checkpoint.Valid {
				t.Fatal("fixture did not produce a healthy pre-finalization checkpoint")
			}
			before := result
			var schema *driver.OutputSchema
			if native {
				schema = &driver.OutputSchema{Format: driver.OutputFormatJSONSchema, SchemaJSON: []byte(`{"type":"object","properties":{"answer":{"type":"integer"}},"required":["answer"],"additionalProperties":false}`)}
			}
			result = finalizeAppServerResult(schema, result, fatal)
			if result.Checkpoint != nil {
				t.Fatalf("infrastructure error retained checkpoint: %+v", result.Checkpoint)
			}
			if result.Output != before.Output || result.Model != before.Model || result.Provider != before.Provider || !reflect.DeepEqual(result.Usage, before.Usage) || !reflect.DeepEqual(result.RawStreams, before.RawStreams) || !reflect.DeepEqual(result.Transcript, before.Transcript) {
				t.Fatalf("infrastructure error lost audit data: %+v", result)
			}
			if native && (result.StructuredOutput == nil || !result.StructuredOutput.Valid || string(result.StructuredOutput.RawJSON) != result.Output) {
				t.Fatalf("validated business output lost: %+v", result.StructuredOutput)
			}
		})
	}
}
