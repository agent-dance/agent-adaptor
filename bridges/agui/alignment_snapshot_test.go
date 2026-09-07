package agui_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"sync"
	"testing"
	"time"

	aguievents "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/bridges/agui"
)

type snapshotNumbers []uint64
type snapshotMap map[string]snapshotNumbers

func snapshotData() map[string]any {
	return map[string]any{
		"nested": []any{map[string]any{"value": "original"}},
		"typed":  snapshotMap{"values": {math.MaxUint64, 0}},
		"array":  [1]map[string][]string{{"values": {"original"}}},
		"raw":    json.RawMessage(`{"ok":true}`), "bytes": []byte{1, 2},
		"number": json.Number("18446744073709551615"),
		"nil":    nil, "nil_map": map[string]any(nil), "nil_slice": []string(nil),
		"empty_map": map[string]any{}, "empty_slice": []string{},
		"time": time.Date(2026, 9, 7, 1, 2, 3, 4, time.UTC), "duration": time.Second,
	}
}

func snapshotUpdate(kind string, phase adaptor.SubagentEventKind, extra map[string]any) adaptor.Event {
	data := map[string]any{"kind": kind, "delegation_id": "child", "parent_tool_call_id": "parent/tool", "remote_tool_call_id": "tool", "tool_name": "search"}
	for k, v := range extra {
		data[k] = v
	}
	return adaptor.WithEventMeta(adaptor.SubagentUpdate{Agent: "worker", Kind: phase, Data: data}, adaptor.EventMeta{RunID: "run", Time: time.Date(2026, 9, 7, 1, 2, 3, 4, time.UTC)})
}

type snapshotSaved struct {
	event aguievents.Event
	json  []byte
}

func snapshotRemember(t *testing.T, saved *[]snapshotSaved, events []aguievents.Event) {
	t.Helper()
	for _, event := range events {
		raw, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		*saved = append(*saved, snapshotSaved{event, raw})
	}
}

func snapshotUnchanged(t *testing.T, saved []snapshotSaved) {
	t.Helper()
	for i, entry := range saved {
		raw, err := json.Marshal(entry.event)
		if err != nil || !bytes.Equal(raw, entry.json) {
			t.Errorf("published event %d changed: before=%s after=%s err=%v", i, entry.json, raw, err)
		}
	}
}

func snapshotPatch(t *testing.T, events []aguievents.Event, path string) any {
	t.Helper()
	for _, ev := range events {
		if v, ok := patchValue(ev, path); ok {
			return v
		}
	}
	t.Fatalf("missing %s patch", path)
	return nil
}

// Inspect exported wire fields on the public returned value. The bridge's
// concrete content structs are private; tests do not reach into tracker state.
func snapshotToolField(t *testing.T, calls any, index int, field string) any {
	t.Helper()
	v := reflect.ValueOf(calls)
	if v.Kind() != reflect.Slice || v.Len() <= index {
		t.Fatalf("invalid toolCalls: %#v", calls)
	}
	return v.Index(index).FieldByName(field).Interface()
}

func TestAlignmentSnapshotTranslatorRetainsPublishedValues(t *testing.T) {
	for _, finish := range []string{"completed", "cancelled"} {
		t.Run(finish, func(t *testing.T) {
			tr := agui.NewEventTranslator()
			var saved []snapshotSaved
			snapshotRemember(t, &saved, tr.Translate(adaptor.RunStarted{RunID: "run"}))
			updates := []adaptor.Event{
				snapshotUpdate("subagent.started", adaptor.SubagentStarted, nil),
				snapshotUpdate("subagent.tool_call.start", adaptor.SubagentDelta, map[string]any{"args": snapshotData()}),
				snapshotUpdate("subagent.tool_call.args", adaptor.SubagentDelta, map[string]any{"args": snapshotData()}),
				snapshotUpdate("subagent.tool_call.result", adaptor.SubagentDelta, map[string]any{"result": snapshotData()}),
				snapshotUpdate("subagent.tool_call.end", adaptor.SubagentDelta, nil),
			}
			if finish == "completed" {
				updates = append(updates, snapshotUpdate("subagent.finished", adaptor.SubagentFinished, map[string]any{"result": snapshotData(), "error": snapshotData()}))
			}
			for _, update := range updates {
				out := tr.Translate(update)
				snapshotUnchanged(t, saved)
				snapshotRemember(t, &saved, out)
			}
			var err error
			if finish == "cancelled" {
				err = context.Canceled
			}
			snapshotRemember(t, &saved, tr.CloseResult(&adaptor.Result{}, err))
			snapshotUnchanged(t, saved)
			if out := tr.CloseResult(nil, err); len(out) != 0 {
				t.Fatal("duplicate terminal")
			}
			snapshotUnchanged(t, saved)
			all := make([]aguievents.Event, len(saved))
			for i, entry := range saved {
				all[i] = entry.event
			}
			assertVerified(t, all)
			initial := all[1].(*aguievents.ActivitySnapshotEvent)
			tools := reflect.ValueOf(initial.Content).FieldByName("ToolCalls")
			if tools.IsNil() || tools.Len() != 0 {
				t.Fatal("initial toolCalls must remain an explicit empty list")
			}
			if got := reflect.ValueOf(initial.Content).FieldByName("ParentToolCallID").String(); got != "parent/tool" {
				t.Fatalf("parent identity = %q", got)
			}
		})
	}
}

func TestAlignmentSnapshotConsumerOwnsNestedValues(t *testing.T) {
	tr := agui.NewEventTranslator()
	tr.Translate(adaptor.RunStarted{RunID: "run"})
	tr.Translate(snapshotUpdate("subagent.started", adaptor.SubagentStarted, nil))
	input := snapshotData()
	start := tr.Translate(snapshotUpdate("subagent.tool_call.start", adaptor.SubagentDelta, map[string]any{"args": input}))
	calls := snapshotPatch(t, start, "/toolCalls")
	args := snapshotToolField(t, calls, 0, "Args").(map[string]any)
	if !reflect.DeepEqual(args, input) {
		t.Fatal("Args changed concrete types or values")
	}
	args["nested"].([]any)[0].(map[string]any)["value"] = "consumer"
	args["typed"].(snapshotMap)["values"][0] = 1
	args["array"].([1]map[string][]string)[0]["values"][0] = "consumer"
	args["raw"].(json.RawMessage)[2] = 'X'
	args["bytes"].([]byte)[0] = 9
	if !reflect.DeepEqual(input, snapshotData()) {
		t.Fatal("consumer mutated caller input")
	}
	result := tr.Translate(snapshotUpdate("subagent.tool_call.result", adaptor.SubagentDelta, map[string]any{"result": input}))
	calls = snapshotPatch(t, result, "/toolCalls")
	if !reflect.DeepEqual(snapshotToolField(t, calls, 0, "Args"), snapshotData()) {
		t.Fatal("consumer mutated translator Args")
	}
	r := snapshotToolField(t, calls, 0, "Result").(map[string]any)
	r["nested"].([]any)[0].(map[string]any)["value"] = "consumer-result"
	end := tr.Translate(snapshotUpdate("subagent.tool_call.end", adaptor.SubagentDelta, nil))
	if !reflect.DeepEqual(snapshotToolField(t, snapshotPatch(t, end, "/toolCalls"), 0, "Result"), snapshotData()) {
		t.Fatal("consumer mutated translator Result")
	}
	finished := tr.Translate(snapshotUpdate("subagent.finished", adaptor.SubagentFinished, map[string]any{"result": input, "error": input}))
	finalResult := snapshotPatch(t, finished, "/result").(map[string]any)
	finalError := snapshotPatch(t, finished, "/error").(map[string]any)
	if !reflect.DeepEqual(finalResult, snapshotData()) || !reflect.DeepEqual(finalError, snapshotData()) {
		t.Fatal("terminal Result/Error changed concrete types or values")
	}
	finalResult["nested"].([]any)[0].(map[string]any)["value"] = "consumer-final"
	if !reflect.DeepEqual(finalError, snapshotData()) || !reflect.DeepEqual(input, snapshotData()) {
		t.Fatal("terminal Result/Error/caller share mutable containers")
	}
}

func TestAlignmentSnapshotTranslatorConcurrentMarshal(t *testing.T) {
	tr := agui.NewEventTranslator()
	tr.Translate(adaptor.RunStarted{RunID: "run"})
	out := tr.Translate(snapshotUpdate("subagent.tool_call.start", adaptor.SubagentDelta, map[string]any{"args": snapshotData()}))
	var saved []snapshotSaved
	snapshotRemember(t, &saved, out)
	readDone := make(chan error, 1)
	go func() {
		for i := 0; i < 400; i++ {
			if _, err := json.Marshal(out); err != nil {
				readDone <- err
				return
			}
		}
		readDone <- nil
	}()
	for i := 0; i < 100; i++ {
		tr.Translate(snapshotUpdate("subagent.tool_call.args", adaptor.SubagentDelta, map[string]any{"args": snapshotData()}))
		tr.Translate(snapshotUpdate("subagent.tool_call.result", adaptor.SubagentDelta, map[string]any{"result": snapshotData()}))
		tr.Translate(snapshotUpdate("subagent.tool_call.end", adaptor.SubagentDelta, nil))
	}
	tr.CloseResult(&adaptor.Result{}, context.Canceled)
	if err := <-readDone; err != nil {
		t.Fatal(err)
	}
	snapshotUnchanged(t, saved)
}

func TestAlignmentSnapshotTranslatorOwnsInputs(t *testing.T) {
	tr := agui.NewEventTranslator()
	tr.Translate(adaptor.RunStarted{RunID: "run"})
	args := snapshotData()
	start := adaptor.SubagentUpdate{Agent: "worker", Kind: adaptor.SubagentDelta, Data: map[string]any{
		"kind": "subagent.tool_call.start", "delegation_id": "child", "remote_tool_call_id": "tool", "args": args,
	}}
	var saved []snapshotSaved
	snapshotRemember(t, &saved, tr.Translate(start))
	args["nested"].([]any)[0].(map[string]any)["value"] = "caller"
	args["typed"].(snapshotMap)["values"][0] = 1
	snapshotUnchanged(t, saved)
	end := tr.Translate(snapshotUpdate("subagent.tool_call.end", adaptor.SubagentDelta, nil))
	if !reflect.DeepEqual(snapshotToolField(t, snapshotPatch(t, end, "/toolCalls"), 0, "Args"), snapshotData()) {
		t.Fatal("caller mutated translator input snapshot")
	}
	data := snapshotData()
	finished := adaptor.SubagentUpdate{Agent: "worker", Kind: adaptor.SubagentFinished, Data: map[string]any{
		"kind": "subagent.finished", "delegation_id": "child", "result": data, "error": data,
	}}
	snapshotRemember(t, &saved, tr.Translate(finished))
	data["nested"].([]any)[0].(map[string]any)["value"] = "caller"
	data["bytes"].([]byte)[0] = 9
	snapshotUnchanged(t, saved)
	tr.CloseResult(&adaptor.Result{}, nil)
	snapshotUnchanged(t, saved)
}

func TestAlignmentSnapshotTypedNilAndEmpty(t *testing.T) {
	values := []any{nil, map[string]any(nil), []string(nil), map[string]any{}, []string{}, snapshotMap{"n": {math.MaxUint64}}, [1]map[string][]int{{"n": {1}}}, json.RawMessage(`null`), []byte{}, json.Number("18446744073709551615"), time.Second}
	for i, value := range values {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			tr := agui.NewEventTranslator()
			tr.Translate(adaptor.RunStarted{RunID: "run"})
			tr.Translate(snapshotUpdate("subagent.tool_call.start", adaptor.SubagentDelta, map[string]any{"args": value}))
			tool := tr.Translate(snapshotUpdate("subagent.tool_call.result", adaptor.SubagentDelta, map[string]any{"result": value}))
			calls := snapshotPatch(t, tool, "/toolCalls")
			if got := snapshotToolField(t, calls, 0, "Result"); !reflect.DeepEqual(got, value) {
				t.Fatalf("tool Result: %#v (%T), want %#v (%T)", got, got, value, value)
			}
			finished := tr.Translate(snapshotUpdate("subagent.finished", adaptor.SubagentFinished, map[string]any{"result": value, "error": value}))
			for _, path := range []string{"/result", "/error"} {
				if got := snapshotPatch(t, finished, path); !reflect.DeepEqual(got, value) {
					t.Fatalf("%s: %#v (%T), want %#v (%T)", path, got, got, value, value)
				}
			}
		})
	}
}

type snapshotStream struct{ events chan adaptor.Event }

func (s *snapshotStream) Events() <-chan adaptor.Event     { return s.events }
func (s *snapshotStream) Result() (*adaptor.Result, error) { return &adaptor.Result{Text: "done"}, nil }
func (*snapshotStream) RunID() string                      { return "run" }
func (*snapshotStream) Cancel()                            {}

func TestAlignmentSnapshotEventsConcurrentMarshal(t *testing.T) {
	s := &snapshotStream{events: make(chan adaptor.Event, 3)}
	s.events <- adaptor.RunStarted{RunID: "run"}
	s.events <- snapshotUpdate("subagent.started", adaptor.SubagentStarted, nil)
	s.events <- snapshotUpdate("subagent.tool_call.start", adaptor.SubagentDelta, map[string]any{"args": snapshotData()})
	output := agui.Events(s)
	var saved []snapshotSaved
	var first aguievents.Event
	for event := range output {
		snapshotRemember(t, &saved, []aguievents.Event{event})
		if _, ok := patchValue(event, "/toolCalls"); ok {
			first = event
			break
		}
	}
	if first == nil {
		t.Fatal("missing initial tool delta")
	}
	// Only the initial receipt is synchronized. Normal consumers marshal
	// already-delivered objects while the producer translates later updates.
	var wg sync.WaitGroup
	serialization := make(chan error, 1)
	wg.Go(func() {
		for i := 0; i < 400; i++ {
			if _, err := json.Marshal(first); err != nil {
				serialization <- err
				return
			}
		}
	})
	wg.Go(func() {
		defer close(s.events)
		for i := 0; i < 100; i++ {
			s.events <- snapshotUpdate("subagent.tool_call.args", adaptor.SubagentDelta, map[string]any{"args": snapshotData()})
			s.events <- snapshotUpdate("subagent.tool_call.result", adaptor.SubagentDelta, map[string]any{"result": snapshotData()})
			s.events <- snapshotUpdate("subagent.tool_call.end", adaptor.SubagentDelta, nil)
		}
		s.events <- snapshotUpdate("subagent.finished", adaptor.SubagentFinished, map[string]any{"result": snapshotData(), "error": snapshotData()})
	})
	for event := range output {
		snapshotRemember(t, &saved, []aguievents.Event{event})
	}
	wg.Wait()
	select {
	case err := <-serialization:
		t.Fatal(err)
	default:
	}
	snapshotUnchanged(t, saved)
}
