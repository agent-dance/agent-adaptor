package appserver

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/agent-dance/agent-adaptor/v2/driver"
)

func TestMCPTranscriptNullableError(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		status    string
		errorJSON string
		wantError bool
	}{
		{name: "omitted", status: "completed"},
		{name: "explicit null", status: "completed", errorJSON: `,"error":null`},
		{name: "whitespace null", status: "completed", errorJSON: ",\"error\": \n null \t"},
		{name: "provider error", status: "completed", errorJSON: `,"error":{"message":"tool unavailable"}`, wantError: true},
		{name: "failed without error", status: "failed", wantError: true},
		{name: "failed with null", status: "failed", errorJSON: `,"error":null`, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := &recordingSink{}
			state := newRunState("run-mcp-error", sink)
			state.setThread("thread-mcp")
			state.setTurn("turn-mcp")
			state.startPublic()
			item := fmt.Sprintf(`{"threadId":"thread-mcp","turnId":"turn-mcp","item":{"id":"mcp-1","type":"mcpToolCall","server":"workflow","tool":"inspect","status":%q,"arguments":{},"result":{"content":[],"structuredContent":{"stage":"review"}}%s}}`, tc.status, tc.errorJSON)
			state.onNotification(NotifyItemStarted, json.RawMessage(item))
			state.onNotification(NotifyItemCompleted, json.RawMessage(item))
			state.onNotification(NotifyTurnCompleted, json.RawMessage(`{"threadId":"thread-mcp","turn":{"id":"turn-mcp","status":"completed"}}`))
			result := state.snapshot(Options{}, "thread-mcp", item, "", 0, "", false)
			state.finishPublicResult(result, nil)
			var toolResults []driver.TranscriptItem
			for _, entry := range result.Transcript {
				if entry.Kind == driver.TranscriptToolResult {
					toolResults = append(toolResults, entry)
				}
			}
			if len(toolResults) != 1 || toolResults[0].ToolUseID != "mcp-1" || toolResults[0].IsError != tc.wantError {
				t.Fatalf("tool results = %#v; want IsError=%v", toolResults, tc.wantError)
			}
			if toolResults[0].Data["result"] == nil {
				t.Fatalf("MCP structured result lost: %#v", toolResults[0].Data)
			}
			if result.RawStreams == nil || result.RawStreams.Stdout != item {
				t.Fatal("original MCP wire payload lost")
			}
			resultEvents := 0
			for _, event := range sink.streams {
				if event.Kind != driver.StreamToolCallResult {
					continue
				}
				resultEvents++
				_, hasError := event.Result["error"]
				if hasError != (tc.name == "provider error") || event.Result["result"] == nil {
					t.Fatalf("MCP result event = %#v", event.Result)
				}
			}
			if resultEvents != 1 {
				t.Fatalf("MCP result events = %d, want 1", resultEvents)
			}
			verifyAppserverContracts(t, sink, result)
		})
	}
}
