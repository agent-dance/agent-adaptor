package claude

import (
	"testing"

	"github.com/agent-dance/agent-adaptor/driver"
)

func TestAlignmentClaudeInterleavedBlockStopKeepsOtherScope(t *testing.T) {
	p, sink := alignmentAdoptionParser(driver.Request{RunID: "interleaved"})
	alignmentFeed(p, alignmentTool("p", "Agent", "", map[string]any{})+alignmentTool("q", "Agent", "", map[string]any{}))
	feed := func(parent string, event map[string]any) {
		t.Helper()
		alignmentFeed(p, alignmentJSON(map[string]any{"type": "stream_event", "parent_tool_use_id": parent, "event": event}))
	}
	start := map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "tool_use", "id": "same", "name": "Read", "input": map[string]any{}}}
	feed("p", start)
	feed("q", start)
	// Stopping one scope must leave the same index and call ID in q open.
	feed("p", map[string]any{"type": "content_block_stop", "index": 0})
	feed("p", start) // A replay must not accept new arguments or end q.
	feed("p", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": `{"path":"replayed"}`}})
	feed("p", map[string]any{"type": "content_block_stop", "index": 0})
	feed("q", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": `{"path":"kept"}`}})
	feed("q", map[string]any{"type": "content_block_stop", "index": 0})
	alignmentFeed(p, alignmentToolResult("same", "p", false, nil)+alignmentToolResult("same", "q", false, nil))

	args := alignmentKinds(sink, driver.StreamToolCallArgs)
	if len(args) != 1 || args[0].ParentToolCallID != "q" || args[0].Delta != `{"path":"kept"}` {
		t.Fatalf("replayed or cross-scope arguments: %+v", args)
	}
	ends := alignmentKinds(sink, driver.StreamToolCallEnd)
	if len(ends) != 4 || ends[2].ParentToolCallID != "p" || ends[3].ParentToolCallID != "q" || ends[2].ScopeID == ends[3].ScopeID {
		t.Fatalf("scope lost or duplicate end: %+v", ends)
	}
	results := alignmentKinds(sink, driver.StreamToolCallResult)
	if len(results) != 2 || results[0].ScopeID != ends[2].ScopeID || results[1].ScopeID != ends[3].ScopeID {
		t.Fatalf("result scope changed after block stop: %+v", results)
	}
}
