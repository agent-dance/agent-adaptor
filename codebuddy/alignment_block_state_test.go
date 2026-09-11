package codebuddy

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/agent-dance/agent-adaptor/capability"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/testutil"
)

func TestAlignmentCodeBuddyInterleavedToolBlocksAndReuse(t *testing.T) {
	rec := &testutil.EventRecorder{}
	p := newParser(rec)
	p.configureObservations(context.Background(), alignmentObservationRequest())
	p.enableStreaming(p.runID)
	feed := func(parent string, event map[string]any) {
		t.Helper()
		raw, err := json.Marshal(map[string]any{"type": "stream_event", "parent_tool_use_id": parent, "event": event})
		if err != nil {
			t.Fatal(err)
		}
		alignmentFeed(t, p, string(raw))
	}
	start := func(index int, id, name, parent string, input map[string]any) {
		feed(parent, map[string]any{"type": "content_block_start", "index": index, "content_block": map[string]any{"type": "tool_use", "id": id, "name": name, "input": input}})
	}
	delta := func(index int, parent, text string) {
		feed(parent, map[string]any{"type": "content_block_delta", "index": index, "delta": map[string]any{"type": "input_json_delta", "partial_json": text}})
	}
	stop := func(index int) { feed("", map[string]any{"type": "content_block_stop", "index": index}) }
	// Missing index still projects the original delta at index zero, without
	// inventing a capability. A later start must discard that orphan buffer.
	feed("", map[string]any{"type": "content_block_delta", "delta": map[string]any{"type": "input_json_delta", "partial_json": "orphan"}})
	start(0, "first", "Skill", "", map[string]any{})
	delta(0, "", `{"skill":`)
	start(1, "other", "Task", "", map[string]any{"subagent_type": "planner"})
	stop(1)
	delta(0, "", `" review "}`)
	stop(0)
	start(0, "blocked", "Skill", "foreign", map[string]any{})
	delta(0, "foreign", `{"skill":" review "}`)
	stop(0)
	start(0, "fresh", "Skill", "", map[string]any{"command": " review "})
	stop(0)
	for _, id := range []string{"first", "other", "fresh"} {
		alignmentFeed(t, p, alignmentResult(id, `"ok"`, false, ""))
	}
	facts := alignmentCapabilities(rec)
	wantIDs := []string{"other", "first", "fresh", "first", "other", "fresh"}
	if len(facts) != len(wantIDs) {
		t.Fatalf("lost, invented or blocked capability: %+v", facts)
	}
	for i, id := range wantIDs {
		phase := capability.Started
		if i >= 3 {
			phase = capability.Completed
		}
		if facts[i].InvocationID != id || facts[i].Phase != phase {
			t.Fatalf("interleaved capability %d: %+v", i, facts[i])
		}
	}
	var args []string
	for _, event := range rec.StreamSnapshot() {
		if event.Kind == driver.StreamToolCallArgs {
			args = append(args, event.ToolCallID+":"+event.Delta)
		}
	}
	if !reflect.DeepEqual(args, []string{"idx-0:orphan", `first:{"skill":`, `first:" review "}`, `blocked:{"skill":" review "}`}) {
		t.Fatalf("raw argument events changed: %q", args)
	}
}

func TestAlignmentCodeBuddyLateArgsAfterRetryTerminal(t *testing.T) {
	rec := &testutil.EventRecorder{}
	p := newParser(rec)
	p.configureObservations(context.Background(), alignmentObservationRequest())
	p.enableStreaming(p.runID)
	alignmentFeed(t, p, `{"type":"system","subtype":"api_retry","error_status":500,"will_retry":false}`)
	before := len(rec.StreamSnapshot())
	// The reader may drain a delta after the retry handler closed public
	// lifecycles. It has no accepted tool identity and cannot reopen the stream.
	alignmentFeed(t, p, `{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{}"}}}`, `{"type":"stream_event","event":{"type":"content_block_stop","index":0}}`)
	if len(rec.StreamSnapshot()) != before || len(alignmentCapabilities(rec)) != 0 {
		t.Fatal("late unbound arguments reopened a closed lifecycle")
	}
}
