package a2a

import (
	"encoding/json"
	"fmt"
	"math"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/hosttools/sessionrecorder"
)

func TestNumericStatusConsumers(t *testing.T) {
	for _, tc := range []struct {
		value any
		want  int
	}{
		{int(2), 2}, {uint(2), 2}, {int64(2), 2}, {float64(2), 2},
		{json.Number("2"), 2}, {json.Number("2.0"), 2}, {json.Number("2e0"), 2},
		{json.Number("2.00000000000000000001"), 0}, {float64(2.5), 0},
		{json.Number("-2"), 0}, {int64(-2), 0}, {math.NaN(), 0},
		{json.Number("18446744073709551616"), 0},
	} {
		t.Run(fmt.Sprintf("%T/%v", tc.value, tc.value), func(t *testing.T) {
			for name, read := range map[string]func(any) int{
				"intValue": intValue,
				"approval artifact attempt": func(v any) int {
					return hitlNoticeArtifact("hitl.resolved", adaptor.Notice{Data: map[string]any{"attempt": v}}, ExposurePolicy{})["retry_attempt"].(int)
				},
				"resolved approval attempt": func(v any) int {
					return decodeApprovalResolved(AdapterStreamEventV1{HITL: map[string]any{"retry_attempt": v}}).(adaptor.Notice).Data["attempt"].(int)
				},
				"approval attempt": func(v any) int {
					e, err := decodeApprovalRequest(AdapterStreamEventV1{HITL: map[string]any{"retry_attempt": v}})
					if err != nil {
						t.Fatal(err)
					}
					return e.(*adaptor.ApprovalRequest).Attempt
				},
				"drop count": func(v any) int {
					return decodeDropped(map[string]any{"dropped_count": v}).(adaptor.Dropped).Count
				},
				"drop by kind": func(v any) int {
					return decodeDropped(map[string]any{"by_kind": map[string]any{"text.content": v}}).(adaptor.Dropped).ByKind["text.content"]
				},
			} {
				if got := read(tc.value); got != tc.want {
					t.Errorf("%s = %d, want %d", name, got, tc.want)
				}
			}
			if got := uint64Value(tc.value); got != uint64(tc.want) {
				t.Errorf("uint64Value = %d, want %d", got, tc.want)
			}
		})
	}
	for _, value := range []any{json.Number("9007199254740993.0"), json.Number("18446744073709551615"), uint64(math.MaxUint64)} {
		want := uint64(math.MaxUint64)
		if value == json.Number("9007199254740993.0") {
			want = 9007199254740993
		}
		drop := decodeDropped(map[string]any{"first_sequence": value, "last_sequence": value}).(adaptor.Dropped)
		if uint64Value(value) != want || drop.FirstSequence != want || drop.LastSequence != want {
			t.Fatalf("sequence %v lost precision: %+v", value, drop)
		}
	}
	for _, value := range []any{uint64(math.MaxUint64), json.Number("9223372036854775808")} {
		if got := intValue(value); got != 0 {
			t.Fatalf("counter %v overflowed int: %d", value, got)
		}
	}
}

func TestNumericLatencyConsumers(t *testing.T) {
	for _, tc := range []struct {
		value any
		valid bool
	}{
		{int64(1234), true}, {float64(1234), true},
		{json.Number("1234"), true}, {json.Number("1234.0"), true}, {json.Number("1.234e3"), true},
		{json.Number("1234.0000000000000001"), false}, {float64(1234.5), false},
		{json.Number("9223372036854775807"), false}, // ms -> ns overflow
	} {
		t.Run(fmt.Sprintf("%T/%v", tc.value, tc.value), func(t *testing.T) {
			decoded := decodeApprovalResolved(AdapterStreamEventV1{HITL: map[string]any{"latency_ms": tc.value}}).(adaptor.Notice)
			latency, ok := decoded.Data["latency"]
			if ok != tc.valid || ok && latency != 1234*time.Millisecond {
				t.Fatalf("decoded latency = %v, %v", latency, ok)
			}
		})
	}
	for _, tc := range []struct {
		value any
		valid bool
	}{
		{1234 * time.Millisecond, true}, {int64(1234000000), true}, {float64(1234000000), true},
		{json.Number("1234000000"), true}, {json.Number("1234000000.0"), true},
		{json.Number("1.234e9"), true}, {json.Number("1234000000.00000001"), false},
		{json.Number("9223372036854775808"), false},
	} {
		artifact := hitlNoticeArtifact("hitl.resolved", adaptor.Notice{Data: map[string]any{"latency": tc.value}}, ExposurePolicy{})
		latency, ok := artifact["latency_ms"]
		if ok != tc.valid || ok && latency != int64(1234) {
			t.Errorf("projected latency for %T %v = %v, %v", tc.value, tc.value, latency, ok)
		}
	}
}

func TestRecordedNoticeNumericA2ARoundTrip(t *testing.T) {
	for _, kind := range []string{adaptor.NoticeApprovalRequested, adaptor.NoticeApprovalResolved} {
		t.Run(kind, func(t *testing.T) {
			raw, err := json.Marshal(sessionrecorder.EventRecord{Event: adaptor.Notice{Kind: kind, Data: map[string]any{
				"request_id": "q", "kind": "permission", "attempt": 2, "latency": 1234 * time.Millisecond,
			}}})
			if err != nil {
				t.Fatal(err)
			}
			var record sessionrecorder.EventRecord
			if err := json.Unmarshal(raw, &record); err != nil {
				t.Fatal(err)
			}
			translator := newStreamTranslator(testTaskInfo{}, ExposurePolicy{IncludeHITL: true})
			projected := translator.Translate(record.Event)
			if len(projected) != 1 {
				t.Fatal(projected)
			}
			decoded, matched, err := DecodeAdapterEventV1(dataValue(t, projected[0]))
			if err != nil || !matched {
				t.Fatalf("decode matched=%v err=%v", matched, err)
			}
			if kind == adaptor.NoticeApprovalRequested {
				if got := decoded.(*adaptor.ApprovalRequest).Attempt; got != 2 {
					t.Fatal(got)
				}
			} else {
				n := decoded.(adaptor.Notice)
				if n.Data["attempt"] != 2 || n.Data["latency"] != 1234*time.Millisecond {
					t.Fatal(n.Data)
				}
			}
		})
	}
}

func TestAdapterStreamDecodeKeepsExactNumbers(t *testing.T) {
	raw := json.RawMessage(`{"schema":"adapter.stream.v1","event":{"kind":"stream.dropped","raw":{"dropped_count":2.0,"by_kind":{"text.content":2e0},"first_sequence":9007199254740993,"last_sequence":18446744073709551615}}}`)
	event, matched, err := DecodeAdapterEventV1(raw)
	if err != nil || !matched {
		t.Fatalf("decode matched=%v err=%v", matched, err)
	}
	drop := event.(adaptor.Dropped)
	if drop.Count != 2 || drop.ByKind["text.content"] != 2 || drop.FirstSequence != 9007199254740993 || drop.LastSequence != math.MaxUint64 {
		t.Fatal(drop)
	}
	raw = json.RawMessage(`{"schema":"adapter.stream.v1","event":{"kind":"tool_call.result","result":{"exact":9007199254740993,"huge":1e1000}}}`)
	event, matched, err = DecodeAdapterEventV1(raw)
	if err != nil || !matched {
		t.Fatalf("decode matched=%v err=%v", matched, err)
	}
	result := event.(adaptor.ToolResult).Result
	if result["exact"] != json.Number("9007199254740993") || result["huge"] != json.Number("1e1000") {
		t.Fatal(result)
	}
}

func TestNumberNormalizationAndMetadataConsumers(t *testing.T) {
	for _, value := range []json.Number{"2", "2.0", "2e0"} {
		if got, ok := normalizeDecodedJSONNumbers(value); !ok || got != int64(2) {
			t.Errorf("normalize %s = %T %v, %v", value, got, got, ok)
		}
	}
	for _, value := range []json.Number{"9007199254740993", "9007199254740993.0", "9.007199254740993e15", "1e1000"} {
		if got, ok := normalizeDecodedJSONNumbers(value); ok {
			t.Errorf("normalized unsafe %s = %v", value, got)
		}
	}
	for _, value := range []json.Number{"9007199254740993", "1e1000", "2.0000000000000001"} {
		if err := validateMetadata(map[string]any{"nested": []any{value}}); err != nil {
			t.Errorf("metadata rejected exact JSON number %s: %v", value, err)
		}
	}
	for _, value := range []json.Number{"", "NaN", "true"} {
		if err := validateMetadata(map[string]any{"n": value}); err == nil {
			t.Errorf("metadata accepted invalid JSON number %q", value)
		}
	}
}
