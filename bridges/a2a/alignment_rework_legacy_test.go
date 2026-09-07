package a2a

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestAlignmentReworkLegacyCompatibility(t *testing.T) {
	cases := []struct{ name, raw string }{
		{"safe_control", `{"schema":"adapter.stream.v1","event":{"kind":"tool_call.start","sequence":7,"timestamp":"2026-09-07T00:00:00Z","tool_call_id":"t","args":{"counter":12}}}`},
		{"args_large_integer", `{"schema":"adapter.stream.v1","event":{"kind":"tool_call.start","sequence":7,"timestamp":"2026-09-07T00:00:00Z","tool_call_id":"t","args":{"counter":9007199254740992}}}`},
		{"raw_large_integer", `{"schema":"adapter.stream.v1","event":{"kind":"stream.dropped","sequence":7,"timestamp":"2026-09-07T00:00:00Z","raw":{"details":{"counter":9007199254740992},"dropped_count":1}}}`},
		{"zero_flat_time", `{"schema":"adapter.stream.v1","event":{"kind":"text.content","delta":"old text","timestamp":"0001-01-01T00:00:00Z"}}`},
		{"zero_meta_time", `{"schema":"adapter.stream.v1","event":{"kind":"text.content","delta":"old text","meta":{"time":"0001-01-01T00:00:00Z"}}}`},
	}
	for _, tc := range cases {
		for _, mode := range []string{"raw", "map"} {
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				var input any = json.RawMessage(tc.raw)
				if mode == "map" {
					if err := json.Unmarshal([]byte(tc.raw), &input); err != nil {
						t.Fatal(err)
					}
				}
				event, matched, err := DecodeAdapterEventV1(input)
				if err != nil || !matched || event == nil {
					t.Fatalf("legacy rejected: matched=%v err=%v event=%#v input=%s", matched, err, event, tc.raw)
				}
				t.Logf("accepted %T meta=%#v", event, event.Meta())
			})
		}
	}
}

func TestAlignmentReworkOldDecoderNewKind(t *testing.T) {
	raw := json.RawMessage(`{"schema":"adapter.stream.v1","event":{"kind":"capability.invocation"}}`)
	event, matched, err := DecodeAdapterEventV1(raw)
	if !matched || err == nil || event != nil {
		t.Fatalf("unsupported newkind matched=%v err=%v event=%#v", matched, err, event)
	}
}

func TestAlignmentReworkLegacyPayloadWithNewCoordinates(t *testing.T) {
	cases := []struct{ name, raw string }{
		{"parent_and_large_args", `{"schema":"adapter.stream.v1","event":{"kind":"tool_call.start","sequence":7,"tool_call_id":"tool","scope_id":"child","parent_tool_call_id":"parent","timestamp":"0001-01-01T00:00:00Z","args":{"counter":9007199254740992}}}`},
		{"parent_and_old_source_time", `{"schema":"adapter.stream.v1","event":{"kind":"tool_call.start","sequence":7,"tool_call_id":"tool","scope_id":"child","meta":{"time":"0001-01-01T00:00:00Z","source":{"timestamp":"0001-01-01T00:00:00Z"}},"args":{"counter":9007199254740992}}}`},
		{"source_and_large_diagnostics", `{"schema":"adapter.stream.v1","event":{"kind":"stream.dropped","sequence":7,"meta":{"source":{"scope_id":"upstream","timestamp":"2026-09-07T00:00:00Z"}},"raw":{"dropped_count":1,"details":{"counter":9007199254740992}}}}`},
		{"parent_and_legacy_duplicate_body", `{"schema":"adapter.stream.v1","event":{"kind":"tool_call.start","tool_call_id":"tool","scope_id":"child","args":{"old":1,"old":2}}}`},
		{"parent_and_legacy_surrogate_body", `{"schema":"adapter.stream.v1","event":{"kind":"tool_call.start","tool_call_id":"tool","scope_id":"child","args":{"old":"\ud800"}}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var input map[string]any
			if err := json.Unmarshal([]byte(tc.raw), &input); err != nil {
				t.Fatal(err)
			}
			rawEvent, rawMatched, rawErr := DecodeAdapterEventV1(json.RawMessage(tc.raw))
			mapEvent, mapMatched, mapErr := DecodeAdapterEventV1(input)
			if !rawMatched || !mapMatched || rawErr != nil || mapErr != nil || !reflect.DeepEqual(rawEvent, mapEvent) {
				t.Fatalf("raw/map mismatch: raw=%#v %v %v; map=%#v %v %v", rawEvent, rawMatched, rawErr, mapEvent, mapMatched, mapErr)
			}
		})
	}
}

func TestAlignmentReworkAddedFieldsStayStrict(t *testing.T) {
	seed := `{"schema":"adapter.stream.v1","event":{"kind":"tool_call.start","tool_call_id":"tool","scope_id":"child","meta":{"source":{"scope_id":"upstream","timestamp":"2026-09-07T00:00:00Z"}},"args":{"counter":9007199254740992}}}`
	for name, raw := range map[string]string{
		"oversized_scope":              strings.Replace(seed, `"scope_id":"child"`, `"scope_id":"`+strings.Repeat("s", 2049)+`"`, 1),
		"null_scope":                   strings.Replace(seed, `"scope_id":"child"`, `"scope_id":null`, 1),
		"case_folded_event_null_scope": strings.Replace(strings.Replace(seed, `"event":`, `"Event":`, 1), `"scope_id":"child"`, `"scope_id":null`, 1),
		"control_scope":                strings.Replace(seed, `"scope_id":"child"`, `"scope_id":"child\u0000"`, 1),
		"new_source_zero_time":         strings.Replace(seed, `2026-09-07T00:00:00Z`, `0001-01-01T00:00:00Z`, 1),
		"new_source_unknown_field":     strings.Replace(seed, `"scope_id":"upstream"`, `"scope_id":"upstream","args":"secret"`, 1),
		"new_source_unsafe_sequence":   strings.Replace(seed, `"scope_id":"upstream"`, `"scope_id":"upstream","sequence":9007199254740992`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			var input map[string]any
			if err := json.Unmarshal([]byte(raw), &input); err != nil {
				t.Fatal(err)
			}
			for _, value := range []any{json.RawMessage(raw), input} {
				event, matched, err := DecodeAdapterEventV1(value)
				if !matched || err == nil || event != nil {
					t.Fatalf("invalid extension accepted %T: %v %v %#v", value, matched, err, event)
				}
			}
		})
	}
	for name, raw := range map[string]string{
		"raw_scope_surrogate": strings.Replace(seed, `"scope_id":"child"`, `"scope_id":"\ud800"`, 1),
		"raw_scope_duplicate": strings.Replace(seed, `"scope_id":"child"`, `"scope_id":"child","scope_id":"other"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			event, matched, err := DecodeAdapterEventV1(json.RawMessage(raw))
			if !matched || err == nil || event != nil {
				t.Fatalf("invalid raw extension: %v %v %#v", matched, err, event)
			}
		})
	}
	var input map[string]any
	if err := json.Unmarshal([]byte(seed), &input); err != nil {
		t.Fatal(err)
	}
	input["event"].(map[string]any)["scope_id"] = string([]byte{0xff})
	if event, matched, err := DecodeAdapterEventV1(input); !matched || err == nil || event != nil {
		t.Fatalf("original invalid UTF-8 repaired: %v %v %#v", matched, err, event)
	}
}
