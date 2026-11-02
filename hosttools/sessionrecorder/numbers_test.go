package sessionrecorder_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"github.com/agent-dance/agent-adaptor/v2/hosttools/sessionrecorder"
)

func TestExistingJSONLNumbersDecodeLosslessly(t *testing.T) {
	// Handwritten persisted envelope uses the existing format, not the current
	// encoder. Both ordinary old files and arbitrarily large valid numbers load.
	dir := t.TempDir()
	old := `{"host_seq":1,"recorded_at":"2026-07-01T12:00:00Z","kind":"tool.result","meta":{"sequence":7},"event":{"ID":"tc","Result":{"zero":0,"exact":9007199254740993,"large":1e1000,"exitCode":1.00000000000000000001}}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "old.jsonl"), []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	b, err := sessionrecorder.NewJSONLEventBackend(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	rows, err := b.Load(context.Background(), "old")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].HostSeq != 1 || rows[0].Event.Meta().Sequence != 7 {
		t.Fatal(rows)
	}
	ev := rows[0].Event.(adaptor.ToolResult)
	for key, want := range map[string]string{"zero": "0", "exact": "9007199254740993", "large": "1e1000", "exitCode": "1.00000000000000000001"} {
		if got, ok := ev.Result[key].(json.Number); !ok || got.String() != want {
			t.Fatalf("%s: %T %v", key, ev.Result[key], ev.Result[key])
		}
	}
}

func TestTypedEnvelopeOpaqueNumbers(t *testing.T) {
	payload := map[string]any{"exact": json.Number("9007199254740993"), "large": json.Number("1e1000")}
	for _, event := range []adaptor.Event{
		adaptor.Notice{Kind: "host.data", Data: payload},
		adaptor.ToolCall{ID: "tc", Args: payload, Result: payload},
		adaptor.ToolResult{ID: "tc", Result: payload},
		adaptor.SubagentUpdate{Member: "helper", Data: payload},
		&adaptor.ApprovalRequest{ID: "q", Kind: adaptor.ApprovalQuestion, Details: payload},
	} {
		t.Run(fmt.Sprintf("%T", event), func(t *testing.T) {
			raw, err := json.Marshal(sessionrecorder.EventRecord{Event: event})
			if err != nil {
				t.Fatal(err)
			}
			var decoded sessionrecorder.EventRecord
			if err := json.Unmarshal(raw, &decoded); err != nil {
				t.Fatal(err)
			}
			reencoded, err := json.Marshal(decoded)
			if err != nil {
				t.Fatal(err)
			}
			if string(raw) != string(reencoded) {
				t.Fatalf("opaque payload changed: %s -> %s", raw, reencoded)
			}
		})
	}
}
