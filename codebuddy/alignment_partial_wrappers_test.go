package codebuddy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/capability"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/testutil"
	"github.com/agent-dance/agent-adaptor/mcp"
)

// Original frames and assertions: T21 QA b2f7b9293906b9feca27806b61fb0ebabc89eb13.
// Only the provider harness is localized to this package's real fake process.
func alignmentT21Context(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}
func alignmentT21JSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}
func alignmentT21Tool(id, name, parent string, input any) string {
	return alignmentT21JSON(map[string]any{"type": "assistant", "parent_tool_use_id": parent, "message": map[string]any{"content": []any{map[string]any{"type": "tool_use", "id": id, "name": name, "input": input}}}}) + "\n"
}
func alignmentT21ToolResult(id, parent string, failed bool, result any) string {
	return alignmentT21JSON(map[string]any{"type": "user", "parent_tool_use_id": parent, "tool_use_result": result, "message": map[string]any{"content": []any{map[string]any{"type": "tool_result", "tool_use_id": id, "content": "tool reply", "is_error": failed}}}}) + "\n"
}

const alignmentT21Terminal = `{"type":"result","subtype":"success","session_id":"t21-session","is_error":false,"result":"final answer","usage":{"input_tokens":0,"output_tokens":3}}` + "\n"

func alignmentT21Facts(events []adaptor.Event) ([]adaptor.CapabilityInvocation, []adaptor.TodoUpdated) {
	var caps []adaptor.CapabilityInvocation
	var todos []adaptor.TodoUpdated
	for _, e := range events {
		switch v := e.(type) {
		case adaptor.CapabilityInvocation:
			caps = append(caps, v)
		case adaptor.TodoUpdated:
			todos = append(todos, v)
		}
	}
	return caps, todos
}
func alignmentT21Lifecycle(t *testing.T, events []adaptor.Event, err error) {
	t.Helper()
	starts, ends := 0, 0
	var last uint64
	for i, e := range events {
		m := e.Meta()
		if m.RunID == "" || m.Sequence <= last || m.Time.IsZero() {
			t.Fatalf("invalid authoritative meta: %+v", m)
		}
		last = m.Sequence
		switch v := e.(type) {
		case adaptor.RunStarted:
			starts++
		case adaptor.RunFinished:
			ends++
			if i != len(events)-1 {
				t.Error("terminal not last")
			}
			if v.Failed != (err != nil) {
				t.Errorf("terminal failure=%v error=%v", v.Failed, err)
			}
		}
	}
	if starts != 1 || ends != 1 {
		t.Fatalf("core lifecycle %d/%d", starts, ends)
	}
}
func alignmentT21Provider(t *testing.T, provider, frames string) *adaptor.Agent {
	t.Helper()
	path := filepath.Join(t.TempDir(), "protocol")
	if err := os.WriteFile(path, []byte(frames), 0600); err != nil {
		t.Fatal(err)
	}
	observer := &alignmentObservationService{events: map[string][]adaptor.Event{}}
	fx := newPersistentCodeBuddyFixtureWithOptions(t, []driver.EnvBinding{{Name: "ALIGNMENT_CODEBUDDY_NATIVE_EXEC", Value: "1"}, {Name: "ALIGNMENT_CODEBUDDY_PROTOCOL", Value: path}}, adaptor.WithMCP(mcp.Stdio("知识_库", "fixture-unused"), mcp.Stdio("a.b", "fixture-unused"), mcp.Stdio("a_b", "fixture-unused")), adaptor.WithRunServices(observer))
	t.Cleanup(fx.close)
	return fx.agent
}
func alignmentT21Drain(s adaptor.Stream) ([]adaptor.Event, *adaptor.Result, error) {
	var events []adaptor.Event
	for e := range s.Events() {
		events = append(events, e)
	}
	r, err := s.Result()
	return events, r, err
}
func TestAlignmentCodeBuddyProtocolPartialWrappers(t *testing.T) {
	for _, provider := range []string{"codebuddy"} {
		for _, abnormal := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/abnormal=%v", provider, abnormal), func(t *testing.T) {
				stream := func(event any) string {
					return alignmentT21JSON(map[string]any{"type": "stream_event", "parent_tool_use_id": "", "event": event}) + "\n"
				}
				frames := `{"type":"system","subtype":"init","session_id":"t21-session"}` + "\n"
				frames += stream(map[string]any{"type": "message_start", "message": map[string]any{"id": "message-1", "role": "assistant", "usage": map[string]any{"input_tokens": 1, "output_tokens": 0}}})
				frames += stream(map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "tool_use", "id": "partial-id", "name": "mcp__知识_库__search", "input": map[string]any{}}})
				frames += stream(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": `{"query":`}})
				frames += stream(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": `"PRIVATE"}`}})
				frames += stream(map[string]any{"type": "content_block_stop", "index": 0})
				frames += alignmentT21Tool("partial-id", "mcp__知识_库__search", "", map[string]any{"query": "PRIVATE"})
				if abnormal {
					frames += `{"type":"error","message":"T21_FORMAL_FAILURE"}` + "\n"
				} else {
					frames += alignmentT21ToolResult("partial-id", "", false, nil) + alignmentT21ToolResult("partial-id", "", false, nil) + alignmentT21Terminal
				}
				a := alignmentT21Provider(t, provider, frames)
				events, r, err := alignmentT21Drain(a.Stream(alignmentT21Context(t), "partial wrappers"))
				caps, _ := alignmentT21Facts(events)
				if len(caps) != 2 {
					t.Fatalf("partial/wrapper capability count=%d", len(caps))
				}
				if caps[0].Invocation.Phase != capability.Started {
					t.Fatal("missing start")
				}
				want := capability.Completed
				if abnormal {
					want = capability.Interrupted
				}
				if caps[1].Invocation.Phase != want {
					t.Fatalf("terminal phase=%s want=%s", caps[1].Invocation.Phase, want)
				}
				starts, ends, results := 0, 0, 0
				var deltas strings.Builder
				var args map[string]any
				for _, event := range events {
					switch v := event.(type) {
					case adaptor.ToolCall:
						if v.ID != "partial-id" {
							continue
						}
						if v.Phase == adaptor.PhaseStart {
							starts++
						}
						if v.Phase == adaptor.PhaseEnd {
							ends++
						}
						if v.Args != nil {
							args = v.Args
						}
						deltas.WriteString(v.ArgsDelta)
					case adaptor.ToolResult:
						if v.ID == "partial-id" {
							results++
						}
					}
				}
				if starts != 1 || ends != 1 {
					t.Fatalf("partial lifecycle %d/%d", starts, ends)
				}
				if args != nil || deltas.String() != `{"query":"PRIVATE"}` {
					t.Fatalf("Args and delta changed: args=%v deltas=%q", args, deltas.String())
				}
				if abnormal {
					var re *adaptor.RunError
					if r != nil || !errors.As(err, &re) || re.Result.Raw().Terminal == nil || results != 0 {
						t.Fatal("formal failure lost partial audit or invented result")
					}
				} else if err != nil || r == nil || results != 1 {
					t.Fatalf("wrapper success=%v results=%d", err, results)
				}

				audit := r
				if abnormal {
					var re *adaptor.RunError
					if errors.As(err, &re) {
						audit = re.Result
					}
				}
				if audit == nil || audit.Raw().Stdout != frames {
					t.Fatal("replay or formal error raw bytes lost")
				}
				transcriptResults := 0
				for _, item := range audit.Transcript() {
					if item.Kind == driver.TranscriptToolResult && item.ToolUseID == "partial-id" {
						transcriptResults++
					}
				}
				if !abnormal && transcriptResults != 2 {
					t.Fatal("dedupe removed original Transcript audit")
				}
				alignmentT21Lifecycle(t, events, err)
			})
		}
	}
}

// Dedupe only identical official results, never another parent or changed result.
func TestAlignmentCodeBuddyToolResultReplayScopeAndPayload(t *testing.T) {
	rec := &testutil.EventRecorder{}
	p := newParser(rec)
	p.configureObservations(context.Background(), alignmentObservationRequest())
	p.enableStreaming("replay-run")
	frames := []string{
		alignmentT21ToolResult("same", "foreign", false, nil),
		alignmentT21ToolResult("same", "", false, nil),
		alignmentT21ToolResult("same", "", false, nil),
		alignmentT21ToolResult("same", "", true, nil),
		alignmentT21ToolResult("other", "", false, nil),
	}
	alignmentFeed(t, p, frames...)
	count := 0
	for _, e := range rec.StreamSnapshot() {
		if e.Kind == driver.StreamToolCallResult {
			count++
		}
	}
	if count != 4 {
		t.Fatalf("distinct parent/payload/ID collapsed: count=%d", count)
	}
}
