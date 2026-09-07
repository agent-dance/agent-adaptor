package a2a

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
)

func reworkBadRaw(t *testing.T, raw []byte) {
	t.Helper()
	event, matched, err := DecodeAdapterEventV1(json.RawMessage(raw))
	if !matched || err == nil || event != nil {
		t.Fatalf("invalid raw accepted/unrecognized: matched=%v err=%v event=%#v raw=%q", matched, err, event, raw)
	}
}
func TestAlignmentReworkStrictRaw(t *testing.T) {
	cases := map[string]string{
		"duplicate-schema":      strings.Replace(reworkCapabilitySeed, `"schema":"adapter.stream.v1"`, `"schema":"adapter.stream.v1","schema":"adapter.stream.v1"`, 1),
		"duplicate-key":         strings.Replace(reworkCapabilitySeed, `"key":"catalog"`, `"key":"catalog","k\u0065y":"other"`, 1),
		"trailing-object":       reworkCapabilitySeed + ` {}`,
		"trailing-scalar":       reworkCapabilitySeed + ` 7`,
		"trailing-garbage":      reworkCapabilitySeed + ` x`,
		"unknown-kind":          strings.Replace(reworkCapabilitySeed, `"capability.invocation"`, `"future.kind"`, 1),
		"unknown-inner":         strings.Replace(reworkCapabilitySeed, `"key":"catalog"`, `"key":"catalog","secret":"private"`, 1),
		"unknown-meta":          strings.Replace(reworkCapabilitySeed, `"run_id":"r"`, `"run_id":"r","secret":"private"`, 1),
		"unknown-envelope":      strings.Replace(reworkCapabilitySeed, `"event":`, `"secret":"private","event":`, 1),
		"null-meta":             strings.Replace(reworkCapabilitySeed, `{"run_id":"r","sequence":7,"time":"2026-09-07T00:00:00Z"}`, `null`, 1),
		"null-duration":         strings.Replace(reworkCapabilitySeed, `"key":"catalog"`, `"key":"catalog","duration_ns":null`, 1),
		"started-zero-duration": strings.Replace(reworkCapabilitySeed, `"key":"catalog"`, `"key":"catalog","duration_ns":0`, 1),
		"bad-source-evidence":   strings.Replace(reworkCapabilitySeed, `"source":"provider"`, `"source":"host"`, 1),
		"bad-surrogate-high":    strings.Replace(reworkCapabilitySeed, `catalog`, `\ud800`, 1),
		"bad-surrogate-low":     strings.Replace(reworkCapabilitySeed, `catalog`, `\udfff`, 1),
		"invalid-utf8":          strings.Replace(reworkCapabilitySeed, `catalog`, string([]byte{0xff}), 1),
		"unsafe-sequence":       strings.Replace(reworkCapabilitySeed, `"sequence":7`, `"sequence":9007199254740992`, 1),
		"fraction-sequence":     strings.Replace(reworkCapabilitySeed, `"sequence":7`, `"sequence":1.5`, 1),
		"zero-sequence":         strings.Replace(reworkCapabilitySeed, `"sequence":7`, `"sequence":0`, 1),
		"flat-zero":             strings.Replace(reworkCapabilitySeed, `"kind":"capability.invocation"`, `"kind":"capability.invocation","sequence":0`, 1),
		"flat-mismatch":         strings.Replace(reworkCapabilitySeed, `"kind":"capability.invocation"`, `"kind":"capability.invocation","sequence":8`, 1),
		"time-zero":             strings.ReplaceAll(reworkCapabilitySeed, `2026-09-07T00:00:00Z`, `0001-01-01T00:00:00Z`),
		"time-yearzero":         strings.ReplaceAll(reworkCapabilitySeed, `2026-09-07T00:00:00Z`, `0000-09-07T00:00:00Z`),
		"time-bad-date":         strings.ReplaceAll(reworkCapabilitySeed, `2026-09-07T00:00:00Z`, `2026-02-30T00:00:00Z`),
		"parent-without-id":     strings.Replace(reworkCapabilitySeed, `"key":"catalog"`, `"key":"catalog","parent_scope_id":"p"`, 1),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) { reworkBadRaw(t, []byte(raw)) })
	}
	for name, raw := range map[string]string{"unicode": strings.Replace(reworkCapabilitySeed, "catalog", `目录\ud83d\ude00`, 1), "space": reworkCapabilitySeed + " \n\t", "max-sequence": strings.Replace(reworkCapabilitySeed, `"sequence":7`, `"sequence":9007199254740991`, 1)} {
		t.Run(name, func(t *testing.T) {
			if e, m, err := DecodeAdapterEventV1(json.RawMessage(raw)); err != nil || !m || e == nil {
				t.Fatalf("valid raw: %v %v", m, err)
			}
		})
	}
	t.Run("foreign-schema", func(t *testing.T) {
		e, m, err := DecodeAdapterEventV1(json.RawMessage(`{"schema":"different","event":{"kind":"future"}}`))
		if m || err != nil || e != nil {
			t.Fatalf("foreign schema claimed: %v %v", m, err)
		}
	})
}

func TestAlignmentReworkSchemaDuplicateOrdering(t *testing.T) {
	raw := strings.Replace(reworkCapabilitySeed, `"schema":"adapter.stream.v1"`, `"schema":"foreign","schema":"adapter.stream.v1"`, 1)
	reworkBadRaw(t, []byte(raw))
}

func TestAlignmentReworkStrictStructured(t *testing.T) {
	for _, mode := range []string{"bad-key-utf8", "bad-value-utf8", "function", "nan", "infinity", "large-integer", "fraction"} {
		t.Run(mode, func(t *testing.T) {
			var input map[string]any
			if err := json.Unmarshal([]byte(reworkCapabilitySeed), &input); err != nil {
				t.Fatal(err)
			}
			event := input["event"].(map[string]any)
			fact := event["capability"].(map[string]any)
			switch mode {
			case "bad-key-utf8":
				fact[string([]byte{0xff})] = "x"
			case "bad-value-utf8":
				fact["key"] = string([]byte{0xff})
			case "function":
				fact["key"] = func() {}
			case "nan":
				event["meta"].(map[string]any)["sequence"] = math.NaN()
			case "infinity":
				event["meta"].(map[string]any)["sequence"] = math.Inf(1)
			case "large-integer":
				event["meta"].(map[string]any)["sequence"] = uint64(9007199254740992)
			case "fraction":
				event["meta"].(map[string]any)["sequence"] = 1.5
			}
			e, m, err := DecodeAdapterEventV1(input)
			if !m || err == nil || e != nil {
				t.Fatalf("invalid map: %v %v %#v", m, err, e)
			}
		})
	}
}

func TestAlignmentReworkWireLengths(t *testing.T) {
	for _, field := range []struct {
		name  string
		limit int
	}{{"key", 512}, {"operation", 256}, {"invocation_id", 2048}} {
		for _, n := range []int{0, field.limit, field.limit + 1} {
			t.Run(fmt.Sprintf("%s/%d", field.name, n), func(t *testing.T) {
				e := reworkWire(t)
				switch field.name {
				case "key":
					e.Capability.Key = strings.Repeat("a", n)
				case "operation":
					e.Capability.Operation = strings.Repeat("a", n)
				case "invocation_id":
					e.Capability.InvocationID = strings.Repeat("a", n)
				}
				_, err := encodeAdapterStreamEvent(e)
				if (err == nil) != (n > 0 && n <= field.limit) {
					t.Fatalf("length %d err=%v", n, err)
				}
			})
		}
	}
	for _, bad := range []string{"\x00", "\n", "\t", "\x7f", "\u0085"} {
		t.Run(fmt.Sprintf("control/%x", bad), func(t *testing.T) {
			e := reworkWire(t)
			e.Capability.Key = "x" + bad
			if _, err := encodeAdapterStreamEvent(e); err == nil {
				t.Fatal("control accepted")
			}
		})
	}
	for _, size := range []int{65536, 65537} {
		t.Run(fmt.Sprintf("envelope/%d", size), func(t *testing.T) {
			e := reworkWire(t)
			e.Kind = "todo.updated"
			e.Capability = nil
			e.Todo = &AdapterTodoSnapshotV1{Source: "plan_update", Revision: 2, OccurredAt: e.Meta.Time, Items: []AdapterTodoItemV1{}}
			for i := 0; i < 16; i++ {
				e.Todo.Items = append(e.Todo.Items, AdapterTodoItemV1{ID: fmt.Sprint(i), Content: strings.Repeat("x", 4096), Status: "pending"})
			}
			raw, err := json.Marshal(AdapterStreamEnvelopeV1{Schema: AdapterStreamSchemaV1, Event: e})
			if err != nil {
				t.Fatal(err)
			}
			e.Todo.Items[0].Content = strings.Repeat("x", 4096-(len(raw)-size))
			raw, err = json.Marshal(AdapterStreamEnvelopeV1{Schema: AdapterStreamSchemaV1, Event: e})
			if err != nil {
				t.Fatal(err)
			}
			if len(raw) != size {
				t.Fatalf("size=%d", len(raw))
			}
			got, m, decodeErr := DecodeAdapterEventV1(json.RawMessage(raw))
			_, encodeErr := encodeAdapterStreamEvent(e)
			if size == 65536 {
				if decodeErr != nil || encodeErr != nil || !m || got == nil {
					t.Fatalf("limit rejected: decode=%v encode=%v matched=%v", decodeErr, encodeErr, m)
				}
			} else {
				if decodeErr == nil || encodeErr == nil || !m || got != nil {
					t.Fatalf("overflow accepted: decode=%v encode=%v matched=%v", decodeErr, encodeErr, m)
				}
			}
		})
	}
	t.Run("raw-padding-counted", func(t *testing.T) {
		raw := append([]byte(reworkCapabilitySeed), bytes.Repeat([]byte{' '}, 65536-len(reworkCapabilitySeed))...)
		if _, _, err := DecodeAdapterEventV1(json.RawMessage(raw)); err != nil {
			t.Fatal(err)
		}
		reworkBadRaw(t, append(raw, ' '))
	})
}

func TestAlignmentReworkSourceDepthAndAuthority(t *testing.T) {
	for _, depth := range []int{1, 8, 9} {
		t.Run(fmt.Sprint(depth), func(t *testing.T) {
			e := reworkWire(t)
			tail := &e.Meta.Source
			for i := 0; i < depth; i++ {
				*tail = &AdapterEventSourceMetaV1{RunID: fmt.Sprint(i), Sequence: 99}
				tail = &(*tail).Upstream
			}
			raw, err := json.Marshal(AdapterStreamEnvelopeV1{Schema: AdapterStreamSchemaV1, Event: e})
			if err != nil {
				t.Fatal(err)
			}
			got, m, decodeErr := DecodeAdapterEventV1(json.RawMessage(raw))
			_, encodeErr := encodeAdapterStreamEvent(e)
			if depth > 8 {
				if decodeErr == nil || encodeErr == nil || !m || got != nil {
					t.Fatal("depth9 accepted")
				}
				return
			}
			if decodeErr != nil || encodeErr != nil || !m {
				t.Fatalf("depth%d %v %v", depth, decodeErr, encodeErr)
			}
			if got.Meta().Sequence != 7 || got.Meta().Source.Sequence != 99 {
				t.Fatal("source replaced authoritative coordinates")
			}
		})
	}
	t.Run("cycle", func(t *testing.T) {
		e := reworkWire(t)
		e.Meta.Source = &AdapterEventSourceMetaV1{RunID: "s"}
		e.Meta.Source.Upstream = e.Meta.Source
		if _, err := encodeAdapterStreamEvent(e); err == nil {
			t.Fatal("cycle accepted")
		}
	})
}

func TestAlignmentReworkOffsetTimePublicValue(t *testing.T) {
	for _, kind := range []string{"capability.invocation", "todo.updated"} {
		t.Run(kind, func(t *testing.T) {
			e := reworkWire(t)
			e.Meta.Time = "2026-09-07T08:00:00+08:00"
			e.Capability.OccurredAt = e.Meta.Time
			if kind == "todo.updated" {
				e.Kind = kind
				e.Capability = nil
				e.Todo = &AdapterTodoSnapshotV1{Source: "plan_update", Revision: 1, OccurredAt: e.Meta.Time, Items: []AdapterTodoItemV1{}}
			}
			raw, _ := json.Marshal(AdapterStreamEnvelopeV1{Schema: AdapterStreamSchemaV1, Event: e})
			got, m, err := DecodeAdapterEventV1(json.RawMessage(raw))
			if err != nil || !m {
				t.Fatalf("valid offset rejected %v", err)
			}
			var at time.Time
			switch v := got.(type) {
			case adaptor.CapabilityInvocation:
				at = v.Invocation.OccurredAt
			case adaptor.TodoUpdated:
				at = v.Snapshot.OccurredAt
			}
			if !at.Equal(time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)) {
				t.Fatal("instant changed")
			}
			_, offset := at.Zone()
			if offset != 0 {
				t.Fatalf("valid wire decoded to invalid non-UTC public %s OccurredAt=%s (offset=%d)", kind, at.Format(time.RFC3339Nano), offset)
			}
		})
	}
}

func TestAlignmentReworkTodoClearAndDeepCopies(t *testing.T) {
	e := reworkWire(t)
	e.Kind = "todo.updated"
	e.Capability = nil
	e.Meta.Source = &AdapterEventSourceMetaV1{RunID: "s", Upstream: &AdapterEventSourceMetaV1{RunID: "up"}}
	e.Todo = &AdapterTodoSnapshotV1{Source: "plan_update", Revision: 1, OccurredAt: e.Meta.Time, Items: []AdapterTodoItemV1{{ID: "x", Content: "中文😀\n\t保留", Status: "pending"}}}
	data, err := encodeAdapterStreamEvent(e)
	if err != nil {
		t.Fatal(err)
	}
	e.Todo.Items[0].Content = "mutated"
	e.Meta.Source.Upstream.RunID = "mutated"
	first, _, err := DecodeAdapterEventV1(data)
	if err != nil {
		t.Fatal(err)
	}
	got := first.(adaptor.TodoUpdated)
	if got.Snapshot.Items[0].Content != "中文😀\n\t保留" || got.Meta().Source.Upstream.RunID != "up" {
		t.Fatal("encode aliases input")
	}
	got.Snapshot.Items[0].Content = "another mutation"
	meta := got.Meta()
	meta.Source.Upstream.RunID = "another mutation"
	second, _, err := DecodeAdapterEventV1(data)
	if err != nil {
		t.Fatal(err)
	}
	if second.(adaptor.TodoUpdated).Snapshot.Items[0].Content != "中文😀\n\t保留" || second.Meta().Source.Upstream.RunID != "up" {
		t.Fatal("decode aliases another recipient")
	}
	e.Todo.Items = []AdapterTodoItemV1{}
	data, err = encodeAdapterStreamEvent(e)
	if err != nil {
		t.Fatal(err)
	}
	clear, _, err := DecodeAdapterEventV1(data)
	if err != nil {
		t.Fatal(err)
	}
	if items := clear.(adaptor.TodoUpdated).Snapshot.Items; items == nil || len(items) != 0 {
		t.Fatalf("clear lost: %#v", items)
	}
	for _, items := range [][]AdapterTodoItemV1{nil, {{ID: "x", Content: "x", Status: "pending"}, {ID: "x", Content: "y", Status: "pending"}}, {{ID: "x", Content: "bad\x00", Status: "pending"}}, {{ID: "x", Content: "x", Status: "future"}}} {
		e.Todo.Items = items
		if _, err := encodeAdapterStreamEvent(e); err == nil {
			t.Fatalf("invalid todo accepted %#v", items)
		}
	}
	duration := int64(0)
	cap := reworkWire(t)
	cap.Capability.Phase = "completed"
	cap.Capability.DurationNS = &duration
	data, err = encodeAdapterStreamEvent(cap)
	if err != nil {
		t.Fatal(err)
	}
	duration = 12
	a, _, _ := DecodeAdapterEventV1(data)
	b, _, _ := DecodeAdapterEventV1(data)
	da := a.(adaptor.CapabilityInvocation).Invocation.Duration
	db := b.(adaptor.CapabilityInvocation).Invocation.Duration
	if da == nil || db == nil || *da != 0 || *db != 0 {
		t.Fatal("zero duration lost")
	}
	*da = time.Second
	if *db != 0 {
		t.Fatal("duration aliases recipient")
	}
}
