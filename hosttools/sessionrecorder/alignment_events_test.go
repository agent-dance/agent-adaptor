package sessionrecorder

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/capability"
	"github.com/agent-dance/agent-adaptor/todo"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestAlignmentRecordObservationsRoundTrip(t *testing.T) {
	at := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	zero := time.Duration(0)
	meta := adaptor.EventMeta{RunID: "r", Sequence: 9, Time: at, Source: &adaptor.EventSourceMeta{ScopeID: "scope", ToolCallID: "tool", InvocationID: "i", DelegationID: "d", Upstream: &adaptor.EventSourceMeta{RunID: "origin"}}}
	for _, ev := range []adaptor.Event{
		adaptor.CapabilityInvocation{Invocation: capability.Invocation{InvocationID: "i", Ref: capability.Ref{Kind: capability.MCP, Key: "knowledge", Operation: "search"}, Phase: capability.Completed, Source: capability.Provider, Evidence: capability.ProviderProtocol, OccurredAt: at, Duration: &zero}},
		adaptor.TodoUpdated{Snapshot: todo.Snapshot{Items: []todo.Item{}, Source: todo.PlanUpdate, Revision: 2, OccurredAt: at}},
		adaptor.ToolCall{ID: "x", ScopeID: "scope", ParentScopeID: "p", ParentToolCallID: "parent", Args: map[string]any{"nested": map[string]any{"value": "original"}}},
	} {
		t.Run(reflect.TypeOf(ev).Name(), func(t *testing.T) {
			ev = adaptor.WithEventMeta(ev, meta)
			rec := EventRecord{HostSeq: 1, RecordedAt: at, Event: ev}
			data, err := json.Marshal(rec)
			if err != nil {
				t.Fatal(err)
			}
			var got EventRecord
			if err = json.Unmarshal(data, &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, rec) {
				t.Fatalf("roundtrip differs: got=%#v meta=%#v want=%#v", got, got.Event.Meta(), rec)
			}
		})
	}
}

func TestAlignmentRecorderOwnsSnapshots(t *testing.T) {
	ctx := context.Background()
	backend := NewMemoryEventBackend()
	r := NewEventRecorder(backend)
	defer r.Close()
	ev := adaptor.ToolCall{ID: "x", Args: map[string]any{"nested": map[string]any{"value": "original"}}}
	got, err := r.Record(ctx, "session", ev)
	if err != nil {
		t.Fatal(err)
	}
	ev.Args["nested"].(map[string]any)["value"] = "input changed"
	got.Event.(adaptor.ToolCall).Args["nested"].(map[string]any)["value"] = "return changed"
	records, err := r.Since(ctx, "session", 0)
	if err != nil {
		t.Fatal(err)
	}
	if records[0].Event.(adaptor.ToolCall).Args["nested"].(map[string]any)["value"] != "original" {
		t.Fatal("recorder history aliases caller")
	}
	records[0].Event.(adaptor.ToolCall).Args["nested"].(map[string]any)["value"] = "query changed"
	again, _ := r.Since(ctx, "session", 0)
	if again[0].Event.(adaptor.ToolCall).Args["nested"].(map[string]any)["value"] != "original" {
		t.Fatal("query aliases recorder")
	}
	disk, _ := backend.Load(ctx, "session")
	if disk[0].Event.(adaptor.ToolCall).Args["nested"].(map[string]any)["value"] != "original" {
		t.Fatal("backend aliases recorder")
	}
}

func TestAlignmentJSONLDurableNewEventsAndByteBoundaries(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	backend, err := NewJSONLEventBackend(dir)
	if err != nil {
		t.Fatal(err)
	}
	r := NewEventRecorder(backend)
	content := strings.Repeat("灵", 1365) + "a" // Exactly 4096 UTF-8 bytes.
	snapshot := todo.Snapshot{Items: make([]todo.Item, 20), Source: todo.PlanUpdate, Revision: 1, OccurredAt: at, ScopeID: "child", ParentScopeID: "parent", ParentToolCallID: "p"}
	for i := range snapshot.Items {
		snapshot.Items[i] = todo.Item{ID: fmt.Sprint(i), Content: content, Status: todo.Pending, SyntheticID: i%2 == 0}
	}
	meta := adaptor.EventMeta{RunID: "r", Sequence: math.MaxUint64, Time: at, Source: &adaptor.EventSourceMeta{ScopeID: "s", ToolCallID: "t", InvocationID: "i", DelegationID: "d", Upstream: &adaptor.EventSourceMeta{RunID: "up"}}}
	events := []adaptor.Event{adaptor.WithEventMeta(adaptor.TodoUpdated{Snapshot: snapshot}, meta), adaptor.WithEventMeta(adaptor.TodoUpdated{Snapshot: todo.Snapshot{Items: []todo.Item{}, Source: todo.PlanUpdate, Revision: 2, OccurredAt: at}}, meta)}
	zero := time.Duration(0)
	events = append(events, adaptor.WithEventMeta(adaptor.CapabilityInvocation{Invocation: capability.Invocation{InvocationID: "i", Ref: capability.Ref{Kind: capability.MCP, Key: "k", Operation: "o"}, Phase: capability.Completed, Source: capability.Provider, Evidence: capability.ProviderProtocol, OccurredAt: at, Duration: &zero}}, meta))
	for _, ev := range events {
		if _, err := r.Record(ctx, "s", ev); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "s.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(bytes.Split(data, []byte{'\n'})[0]) <= 65536 {
		t.Fatal("fixture does not exceed A2A bound")
	}
	reopened, err := NewJSONLEventBackend(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	records, err := reopened.Load(ctx, "s")
	if err != nil {
		t.Fatal(err)
	}
	for i, record := range records {
		if !reflect.DeepEqual(record.Event, events[i]) {
			t.Fatalf("event %d lost bytes, parent, source, duration or clear", i)
		}
	}
	// EventRecord is a uint64 format, independent of A2A's safe-number limit.
	encoded, err := json.Marshal(EventRecord{HostSeq: math.MaxUint64, RecordedAt: at, Event: events[0]})
	if err != nil {
		t.Fatal(err)
	}
	var record EventRecord
	if err = json.Unmarshal(encoded, &record); err != nil || record.HostSeq != math.MaxUint64 {
		t.Fatalf("uint64 roundtrip: %d %v", record.HostSeq, err)
	}
	invalid := snapshot
	invalid.Items = append([]todo.Item(nil), snapshot.Items...)
	invalid.Items[0].Content += "b"
	if _, err := json.Marshal(EventRecord{Event: adaptor.TodoUpdated{Snapshot: invalid}}); err == nil {
		t.Fatal("accepted 4097-byte content")
	}
}

func TestAlignmentJSONLRejectsMalformedObservations(t *testing.T) {
	at := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	ev := adaptor.WithEventMeta(adaptor.TodoUpdated{Snapshot: todo.Snapshot{Items: []todo.Item{{ID: "id", Content: "value", Status: todo.Pending}}, Source: todo.PlanUpdate, Revision: 1, OccurredAt: at}}, adaptor.EventMeta{RunID: "r", Sequence: 1, Time: at})
	encoded, err := json.Marshal(EventRecord{HostSeq: 1, RecordedAt: at, Event: ev})
	if err != nil {
		t.Fatal(err)
	}
	good := string(encoded)
	mutations := map[string]string{
		"unknown-kind":       strings.Replace(good, `"kind":"todo.updated"`, `"kind":"future"`, 1),
		"unknown-envelope":   strings.Replace(good, `"host_seq":1`, `"unknown":1,"host_seq":1`, 1),
		"unknown-payload":    strings.Replace(good, `"Snapshot":{`, `"unknown":true,"Snapshot":{`, 1),
		"unknown-item":       strings.Replace(good, `"ID":"id"`, `"Unknown":true,"ID":"id"`, 1),
		"duplicate-key":      strings.Replace(good, `"Revision":1`, `"Revision":1,"Revision":2`, 1),
		"null-items":         strings.Replace(good, `[{"ID":"id","Content":"value","Status":"pending","SyntheticID":false}]`, `null`, 1),
		"null-synthetic":     strings.Replace(good, `"SyntheticID":false`, `"SyntheticID":null`, 1),
		"missing-synthetic":  strings.Replace(good, `,"SyntheticID":false`, "", 1),
		"unknown-status":     strings.Replace(good, `"Status":"pending"`, `"Status":"unknown"`, 1),
		"invalid-utf8":       strings.Replace(good, "value", string([]byte{0xff}), 1),
		"unpaired-surrogate": strings.Replace(good, "value", `\ud800`, 1),
		"trailing":           good + ` {}`,
	}
	for name, data := range mutations {
		t.Run(name, func(t *testing.T) {
			if data == good {
				t.Fatal("mutation did not apply")
			}
			_, err := readJSONLEventRecords(context.Background(), strings.NewReader(data+"\n"), "fixture")
			if !errors.Is(err, ErrJSONLEventLogCorrupt) {
				t.Fatalf("malformed log accepted: %v", err)
			}
		})
	}
}

func TestAlignmentRecordSourceDepthAndObservationValidation(t *testing.T) {
	at := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	source := &adaptor.EventSourceMeta{RunID: "origin"}
	for i := 1; i < 8; i++ {
		source = &adaptor.EventSourceMeta{Upstream: source, InvocationID: fmt.Sprint(i)}
	}
	ev := adaptor.WithEventMeta(adaptor.Notice{Kind: adaptor.NoticeRuntime}, adaptor.EventMeta{Source: source})
	if _, err := json.Marshal(EventRecord{Event: ev}); err != nil {
		t.Fatalf("eight source nodes: %v", err)
	}
	source = &adaptor.EventSourceMeta{Upstream: source}
	ev = adaptor.WithEventMeta(ev, adaptor.EventMeta{Source: source})
	if _, err := json.Marshal(EventRecord{Event: ev}); err == nil {
		t.Fatal("accepted ninth source")
	}
	cyc := &adaptor.EventSourceMeta{}
	cyc.Upstream = cyc
	ev = adaptor.WithEventMeta(ev, adaptor.EventMeta{Source: cyc})
	if _, err := json.Marshal(EventRecord{Event: ev}); err == nil {
		t.Fatal("accepted source cycle")
	}
	invocation := capability.Invocation{InvocationID: "i", Ref: capability.Ref{Kind: capability.MCP, Key: strings.Repeat("灵", 170) + "ab", Operation: "search"}, Phase: capability.Started, Source: capability.Provider, Evidence: capability.ProviderProtocol, OccurredAt: at}
	if _, err := json.Marshal(EventRecord{Event: adaptor.CapabilityInvocation{Invocation: invocation}}); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"key-bytes", "source-evidence", "duration", "parent", "phase"} {
		t.Run(mode, func(t *testing.T) {
			v := invocation
			switch mode {
			case "key-bytes":
				v.Ref.Key += "a"
			case "source-evidence":
				v.Source = capability.Host
			case "duration":
				zero := time.Duration(0)
				v.Duration = &zero
			case "parent":
				v.ParentScopeID = "p"
			case "phase":
				v.Phase = "unknown"
			}
			if _, err := json.Marshal(EventRecord{Event: adaptor.CapabilityInvocation{Invocation: v}}); err == nil {
				t.Fatalf("accepted invalid %s", mode)
			}
		})
	}
}

func TestAlignmentJSONLWriteFailureNeverEntersHistory(t *testing.T) {
	fake := &fakeJSONLEventFile{writeN: 2}
	backend := newFakeJSONLEventBackend(t, fake)
	r := NewEventRecorder(backend)
	defer r.Close()
	ctx := context.Background()
	at := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	ev := adaptor.TodoUpdated{Snapshot: todo.Snapshot{Items: []todo.Item{}, Source: todo.PlanUpdate, Revision: 1, OccurredAt: at}}
	if _, err := r.Record(ctx, "s", ev); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("write error=%v", err)
	}
	history, err := r.Since(ctx, "s", 0)
	if err != nil || len(history) != 0 {
		t.Fatalf("failure fell back to history: %v %v", history, err)
	}
	fake.writeN = -1
	rec, err := r.Record(ctx, "s", ev)
	if err != nil || rec.HostSeq != 1 {
		t.Fatalf("retry consumed failed cursor: %v %v", rec, err)
	}
}

func TestAlignmentRecordedApprovalsAreOnlyDescriptions(t *testing.T) {
	ctx := context.Background()
	for _, kind := range []adaptor.ApprovalKind{adaptor.ApprovalPermission, adaptor.ApprovalPlanReview, adaptor.ApprovalQuestion} {
		t.Run(string(kind), func(t *testing.T) {
			backend, err := NewJSONLEventBackend(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			r := NewEventRecorder(backend)
			defer r.Close()
			ev := &adaptor.ApprovalRequest{ID: "q", Kind: kind, Title: "question", Details: map[string]any{"nested": map[string]any{"text": "original"}}}
			record, err := r.Record(ctx, "s", ev)
			if err != nil {
				t.Fatal(err)
			}
			records, err := backend.Load(ctx, "s")
			if err != nil {
				t.Fatal(err)
			}
			for _, req := range []*adaptor.ApprovalRequest{record.Event.(*adaptor.ApprovalRequest), records[0].Event.(*adaptor.ApprovalRequest)} {
				for _, err := range []error{req.Approve(ctx), req.Deny(ctx, "reason"), req.Answer(ctx, "answer")} {
					if !errors.Is(err, adaptor.ErrApprovalUnavailable) {
						t.Fatalf("replay gained responder: %v", err)
					}
				}
			}
		})
	}
}
