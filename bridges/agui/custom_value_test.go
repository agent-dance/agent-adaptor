package agui_test

import (
	"reflect"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/bridges/agui"
)

// AG-UI 1.0 requires CUSTOM.value. @ag-ui/client 1.x (bundled by CopilotKit
// 1.83) rejects the whole stream when a payload-free notice omits it.
func TestEventTranslatorCustomAlwaysHasValue(t *testing.T) {
	tr := agui.NewEventTranslator()
	_ = tr.Translate(adaptor.RunStarted{RunID: "r1", ThreadID: "t1"})
	events := tr.Translate(adaptor.Notice{Kind: adaptor.NoticeTranscriptItem})
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	frame := frameMap(t, events[0])
	if frame["type"] != "CUSTOM" {
		t.Fatalf("type = %v, want CUSTOM", frame["type"])
	}
	value, ok := frame["value"]
	if !ok {
		t.Fatalf("CUSTOM frame has no value: %v", frame)
	}
	if !reflect.DeepEqual(value, map[string]any{}) {
		t.Fatalf("value = %#v, want empty object", value)
	}
}
