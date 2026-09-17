package appserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/agent-dance/agent-adaptor/adaptertest"
	"github.com/agent-dance/agent-adaptor/capability"
	"github.com/agent-dance/agent-adaptor/driver"
)

func TestAlignmentMCPResultErrorPresence(t *testing.T) {
	for _, tc := range []struct {
		name, status, errorField string
		wantError                bool
	}{
		{"completed-absent", "completed", "", false},
		{"completed-null", "completed", `,"error":null`, false},
		{"completed-whitespace-null", "completed", ",\"error\": \n null \t", false},
		{"completed-formal-error", "completed", `,"error":{"message":"tool rejected"}`, true},
		{"failed-absent", "failed", "", true},
		{"failed-null", "failed", `,"error":null`, true},
		{"failed-formal-error", "failed", `,"error":{"message":"tool rejected"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const resultJSON = `{"content":[{"type":"text","text":"tool output"}]}`
			itemJSON := json.RawMessage(fmt.Sprintf(`{"id":"tool","type":"mcpToolCall","server":"mcp","tool":"read","status":%q,"result":%s%s}`, tc.status, resultJSON, tc.errorField))
			item, err := DecodeThreadItem(itemJSON)
			if err != nil || !bytes.Equal(item.Raw, itemJSON) {
				t.Fatalf("formal item Raw changed: %v", err)
			}
			sink := &recordingSink{}
			opts := Options{ResolvedMCP: []driver.MCPServerSpec{{Key: "mcp"}}}
			s := newRunState("run", sink, opts)
			s.setThread("thread")
			s.setTurn("turn")
			s.onNotification(NotifyItemStarted, json.RawMessage(`{"threadId":"thread","turnId":"turn","item":{"id":"tool","type":"mcpToolCall","server":"mcp","tool":"read","status":"inProgress","arguments":{}}}`))
			s.onNotification(NotifyItemCompleted, json.RawMessage(`{"threadId":"thread","turnId":"turn","item":`+string(itemJSON)+`}`))
			terminal := json.RawMessage(`{"threadId":"thread","turn":{"id":"turn","status":"completed"}}`)
			s.onNotification(NotifyTurnCompleted, terminal)
			response := s.snapshot(opts, "thread", string(itemJSON), "stderr", 0, "", false)
			if s.protocolError() != nil || response.Failure != nil || response.Checkpoint == nil || !response.Checkpoint.Valid || response.RawStreams.Stdout != string(itemJSON) || response.RawStreams.Stderr != "stderr" || !bytes.Equal(response.RawStreams.Terminal.JSON, terminal) {
				t.Fatalf("tool audit changed parent outcome or Raw: err=%v response=%+v", s.protocolError(), response)
			}
			var toolResults []driver.TranscriptItem
			for _, entry := range response.Transcript {
				if entry.Kind == driver.TranscriptToolResult {
					toolResults = append(toolResults, entry)
				}
			}
			if len(toolResults) != 1 || toolResults[0].ToolUseID != "tool" || toolResults[0].IsError != tc.wantError {
				t.Fatalf("tool result error presence: want IsError=%v, got %+v", tc.wantError, toolResults)
			}
			wantData := map[string]any{"result": decodeJSONValue(json.RawMessage(resultJSON)), "error": decodeJSONValue(item.McpToolCall.Error)}
			if !reflect.DeepEqual(toolResults[0].Data, wantData) {
				t.Fatalf("formal result/error data changed: got %#v want %#v", toolResults[0].Data, wantData)
			}
			facts, _ := alignmentFacts(sink)
			wantPhase := capability.Completed
			if tc.wantError {
				wantPhase = capability.Failed
			}
			if len(facts) != 2 || facts[0].Phase != capability.Started || facts[1].Phase != wantPhase || facts[1].Ref.Key != "mcp" || facts[1].Source != capability.Provider || facts[1].Evidence != capability.ProviderProtocol {
				t.Fatalf("capability and transcript disagree: %+v", facts)
			}
			assertNoViolations(t, adaptertest.VerifyTranscriptMirror(sink.events, response.Transcript))
		})
	}
}
