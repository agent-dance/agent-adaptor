package a2a

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor"
)

const reworkCapabilitySeed = `{"schema":"adapter.stream.v1","event":{"kind":"capability.invocation","meta":{"run_id":"r","sequence":7,"time":"2026-09-07T00:00:00Z"},"capability":{"invocation_id":"call","kind":"mcp","key":"catalog","operation":"search","phase":"started","evidence":"provider_protocol","source":"provider","occurred_at":"2026-09-07T00:00:00Z"}}}`

func reworkWire(t *testing.T) AdapterStreamEventV1 {
	t.Helper()
	var e AdapterStreamEnvelopeV1
	if err := json.Unmarshal([]byte(reworkCapabilitySeed), &e); err != nil {
		t.Fatal(err)
	}
	return e.Event
}

func TestAlignmentReworkCapabilitySelfParent(t *testing.T) {
	for _, scope := range []string{"", "scope-A"} {
		e := reworkWire(t)
		e.Capability.ScopeID, e.Capability.ParentScopeID = scope, scope
		e.Capability.ParentToolCallID = e.Capability.InvocationID
		t.Run("encode/"+scope, func(t *testing.T) {
			value, err := encodeAdapterStreamEvent(e)
			if err == nil {
				raw, _ := json.Marshal(value)
				t.Fatalf("self-parent accepted for encoding: %s", raw)
			}
		})
		t.Run("decode/"+scope, func(t *testing.T) {
			raw, err := json.Marshal(AdapterStreamEnvelopeV1{Schema: AdapterStreamSchemaV1, Event: e})
			if err != nil {
				t.Fatal(err)
			}
			got, matched, err := DecodeAdapterEventV1(json.RawMessage(raw))
			if !matched || err == nil || got != nil {
				t.Fatalf("self-parent decoded: matched=%v err=%v event=%#v", matched, err, got)
			}
		})
	}
	t.Run("different-scope-legal", func(t *testing.T) {
		e := reworkWire(t)
		e.Capability.ScopeID = "child"
		e.Capability.ParentScopeID = "parent"
		e.Capability.ParentToolCallID = e.Capability.InvocationID
		if _, err := encodeAdapterStreamEvent(e); err != nil {
			t.Fatal(err)
		}
	})
}

func TestAlignmentReworkOpaqueThreadKey(t *testing.T) {
	for _, n := range []int{2048, 2049, 8192} {
		e := reworkWire(t)
		e.Meta.ThreadKey = strings.Repeat("k", n)
		raw, err := json.Marshal(AdapterStreamEnvelopeV1{Schema: AdapterStreamSchemaV1, Event: e})
		if err != nil {
			t.Fatal(err)
		}
		if len(raw) > 65536 {
			t.Fatalf("fixture exceeds envelope limit: %d", len(raw))
		}
		t.Run("encode/"+strings.Repeat("x", n/2048), func(t *testing.T) {
			value, err := encodeAdapterStreamEvent(e)
			if err != nil {
				t.Fatalf("legal opaque ThreadKey bytes=%d envelope=%d rejected: %v", n, len(raw), err)
			}
			decoded, matched, err := DecodeAdapterEventV1(value)
			if err != nil || !matched || decoded.Meta().ThreadKey != e.Meta.ThreadKey {
				t.Fatalf("key not preserved: matched=%v err=%v", matched, err)
			}
		})
		t.Run("decode/"+strings.Repeat("x", n/2048), func(t *testing.T) {
			decoded, matched, err := DecodeAdapterEventV1(json.RawMessage(raw))
			if err != nil || !matched || decoded == nil {
				t.Fatalf("legal opaque ThreadKey bytes=%d envelope=%d rejected: matched=%v err=%v", n, len(raw), matched, err)
			}
			if decoded.Meta().ThreadKey != e.Meta.ThreadKey {
				t.Fatal("key changed")
			}
		})
		t.Run("translator/"+strings.Repeat("x", n/2048), func(t *testing.T) {
			meta := adaptor.EventMeta{RunID: e.Meta.RunID, ThreadKey: e.Meta.ThreadKey, Sequence: e.Meta.Sequence}
			decoded, _, _ := DecodeAdapterEventV1(json.RawMessage(reworkCapabilitySeed))
			meta.Time = decoded.Meta().Time
			translator := newStreamTranslator(testTaskInfo{}, ExposurePolicy{IncludeCapabilityInvocations: true})
			out := translator.Translate(adaptor.WithEventMeta(decoded, meta))
			if translator.err != nil || len(out) != 1 {
				t.Fatalf("valid key bytes=%d became transport failure: outputs=%d err=%v", n, len(out), translator.err)
			}
		})
	}
}

func TestAlignmentReworkLongEnvelopeCoordinatesAndSafeDrop(t *testing.T) {
	for _, field := range []string{"run_id", "turn_id", "thread_key", "source_run_id", "source_thread_id", "source_turn_id"} {
		t.Run(field, func(t *testing.T) {
			event := reworkWire(t)
			value := "  " + strings.Repeat("目录/", 1200) + "  "
			switch field {
			case "run_id":
				event.Meta.RunID = value
			case "turn_id":
				event.Meta.TurnID = value
			case "thread_key":
				event.Meta.ThreadKey = value
				event.ThreadID = value
			case "source_run_id":
				event.Meta.Source = &AdapterEventSourceMetaV1{RunID: value, ScopeID: "upstream"}
			case "source_thread_id":
				event.Meta.Source = &AdapterEventSourceMetaV1{ThreadID: value, ScopeID: "upstream"}
			case "source_turn_id":
				event.Meta.Source = &AdapterEventSourceMetaV1{TurnID: value, ScopeID: "upstream"}
			}
			data, err := encodeAdapterStreamEvent(event)
			if err != nil {
				t.Fatal(err)
			}
			decoded, matched, err := DecodeAdapterEventV1(data)
			if !matched || err != nil {
				t.Fatalf("valid coordinate decode=%v,%v", matched, err)
			}
			meta := decoded.Meta()
			var got string
			switch field {
			case "run_id":
				got = meta.RunID
			case "turn_id":
				got = meta.TurnID
			case "thread_key":
				got = meta.ThreadKey
			case "source_run_id":
				got = meta.Source.RunID
			case "source_thread_id":
				got = meta.Source.ThreadID
			case "source_turn_id":
				got = meta.Source.TurnID
			}
			if got != value {
				t.Fatal("opaque coordinate changed")
			}
			event.Capability.Key = ""
			drop, err := encodeAdapterStreamDrop(event, errAdapterPayload)
			if err != nil {
				t.Fatalf("safe metadata wrongly became infrastructure error: %v", err)
			}
			marker, matched, err := DecodeAdapterEventV1(drop)
			if !matched || err != nil || marker.(adaptor.Dropped).Reason != "invalid_payload" {
				t.Fatalf("drop=%#v %v", marker, err)
			}
			if !reflect.DeepEqual(marker.Meta(), meta) {
				t.Fatalf("safe drop changed metadata: got=%#v want=%#v", marker.Meta(), meta)
			}
		})
	}
	for _, field := range []string{"scope_id", "tool_call_id", "invocation_id", "delegation_id"} {
		t.Run("source_limit/"+field, func(t *testing.T) {
			event := reworkWire(t)
			source := &AdapterEventSourceMetaV1{}
			value := strings.Repeat("x", 2049)
			switch field {
			case "scope_id":
				source.ScopeID = value
			case "tool_call_id":
				source.ToolCallID = value
			case "invocation_id":
				source.InvocationID = value
			case "delegation_id":
				source.DelegationID = value
			}
			event.Meta.Source = source
			if _, err := encodeAdapterStreamEvent(event); err == nil {
				t.Fatal("explicit source ID limit relaxed")
			}
		})
	}
}

func TestAlignmentReworkOpaqueThreadKeyEscaping(t *testing.T) {
	keys := []string{" 业务\n\t:key ", "业务\\n\\t:key", "业务\x00:key"}
	seen := map[string]bool{}
	for _, key := range keys {
		t.Run(fmt.Sprintf("%q", key), func(t *testing.T) {
			event := reworkWire(t)
			event.Meta.ThreadKey = key
			event.ThreadID = key
			encoded, err := encodeAdapterStreamEvent(event)
			if err != nil {
				t.Fatal(err)
			}
			wire, _ := json.Marshal(encoded)
			if seen[string(wire)] {
				t.Fatal("distinct opaque keys collided")
			}
			seen[string(wire)] = true
			for _, input := range []any{encoded, json.RawMessage(wire)} {
				decoded, matched, err := DecodeAdapterEventV1(input)
				if err != nil || !matched || decoded.Meta().ThreadKey != key {
					t.Fatalf("opaque key changed: %#v %v %v", decoded, matched, err)
				}
			}
			flat := encoded["event"].(map[string]any)["thread_id"]
			meta := encoded["event"].(map[string]any)["meta"].(map[string]any)["thread_key"]
			if flat != key || meta != key {
				t.Fatalf("flat/meta mismatch: %q %q", flat, meta)
			}
			event.Capability.Key = ""
			drop, err := encodeAdapterStreamDrop(event, errAdapterPayload)
			if err != nil {
				t.Fatal(err)
			}
			decoded, _, err := DecodeAdapterEventV1(drop)
			if err != nil || decoded.Meta().ThreadKey != key {
				t.Fatal("safe drop rewrote opaque key")
			}
		})
	}
}
