package aguisession

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	events "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
)

// jsonEvent retains schema fields absent from some upstream Go event types.
// It also lets the reducer table exercise defensive collision/patch behavior
// directly, without the official client's independent sequence verifier.
type jsonEvent struct {
	events.Event
	raw json.RawMessage
}

func (e jsonEvent) ToJSON() ([]byte, error) { return e.raw, nil }

func TestFoldClientReducerTable(t *testing.T) {
	raw, err := os.ReadFile("testdata/client-fold.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name     string
		Initial  []Message
		Events   []json.RawMessage
		Expected []Message
	}
	if err := decodeOpaque(raw, &cases); err != nil {
		t.Fatal(err)
	}
	// Expectations were derived by reading @ag-ui/client 1.0.3 src/apply/default.ts
	// in index.mjs.map, line by line, and @ag-ui/core mergeMetadata. The Node-free
	// test and verify-client.mjs use the same literal expectations independently.
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			ms := tc.Initial
			for _, raw := range tc.Events {
				ms = Fold(ms, jsonEvent{raw: raw})
			}
			if !reflect.DeepEqual(ms, tc.Expected) {
				got, _ := json.Marshal(ms)
				want, _ := json.Marshal(tc.Expected)
				t.Fatalf("reducer mismatch\n got %s\nwant %s", got, want)
			}
			values := make([]any, len(ms))
			for i, m := range ms {
				values[i] = m
			}
			validate(t, "Message", values)
		})
	}
}

func TestFoldHistoryToolErrorCorrection(t *testing.T) {
	ms := []Message{}
	for _, raw := range []string{
		`{"type":"TOOL_CALL_RESULT","toolCallId":"t","messageId":"out","content":"bad","metadata":{"agentAdaptor":{"status":"failed","error":"specific"}}}`,
		`{"type":"TOOL_CALL_RESULT","toolCallId":"t","messageId":"out","content":"ok","metadata":{"agentAdaptor":{"status":"succeeded"}}}`,
	} {
		ms = Fold(ms, jsonEvent{raw: json.RawMessage(raw)})
	}
	if _, ok := ms[0]["error"]; ok {
		t.Fatal("corrected success retained error", ms)
	}
}
