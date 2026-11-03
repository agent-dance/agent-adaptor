package agui_test

import (
	"encoding/json"
	"strings"
	"testing"

	events "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"github.com/agent-dance/agent-adaptor/v2/bridges/agui"
	"github.com/agent-dance/agent-adaptor/v2/hosttools/sessionrecorder"
)

func TestRecordedToolResultPreservesExitFailure(t *testing.T) {
	original := sessionrecorder.EventRecord{
		HostSeq: 1,
		Event: adaptor.ToolResult{
			ID: "command",
			Result: map[string]any{
				"output": "command failed", "exitCode": 1,
			},
		},
	}
	// EventRecord's public JSON methods are the same typed-envelope codec
	// used by durable backends; replay restores opaque numbers as json.Number.
	wire, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var replayed sessionrecorder.EventRecord
	if err := json.Unmarshal(wire, &replayed); err != nil {
		t.Fatal(err)
	}
	result, ok := replayed.Event.(adaptor.ToolResult)
	if !ok {
		t.Fatalf("replayed event = %T", replayed.Event)
	}
	if _, ok := result.Result["exitCode"].(json.Number); !ok {
		t.Fatalf("round trip did not exercise replayed numbers: %T", result.Result["exitCode"])
	}

	translator := agui.NewEventTranslator()
	translator.Translate(adaptor.RunStarted{RunID: "run"})
	projected := translator.Translate(replayed.Event)
	if len(projected) != 1 {
		t.Fatalf("projected events = %v", projected)
	}
	tool := projected[0].(*events.ToolCallResultEvent)
	metadata := tool.Metadata["agentAdaptor"].(map[string]any)
	if !strings.HasSuffix(tool.Content, "[exit=1]") || metadata["status"] != "failed" {
		t.Fatalf("replayed failure lost: content=%q metadata=%v", tool.Content, metadata)
	}
}
