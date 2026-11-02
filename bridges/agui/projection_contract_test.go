package agui

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	events "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
	adaptor "github.com/agent-dance/agent-adaptor/v2"
)

func TestEmptyMessageLifecyclesAreSuppressed(t *testing.T) {
	for _, reasoning := range []bool{false, true} {
		for _, end := range []bool{false, true} {
			name := "text"
			if reasoning {
				name = "reasoning"
			}
			t.Run(name+map[bool]string{false: "/close", true: "/end"}[end], func(t *testing.T) {
				tr := NewEventTranslator()
				out := tr.Translate(adaptor.RunStarted{})
				event := func(phase adaptor.Phase, text string) adaptor.Event {
					if reasoning {
						return adaptor.Thinking{MessageID: "empty", Phase: phase, Text: text}
					}
					return adaptor.TextDelta{MessageID: "empty", Phase: phase, Text: text}
				}
				for _, e := range []adaptor.Event{event(adaptor.PhaseStart, ""), event(adaptor.PhaseStart, ""), event(adaptor.PhaseContent, "")} {
					if got := tr.Translate(e); len(got) != 0 {
						t.Fatalf("empty opener/content: %v", got)
					}
				}
				if end {
					if got := tr.Translate(event(adaptor.PhaseEnd, "")); len(got) != 0 {
						t.Fatalf("empty end: %v", got)
					}
				}
				out = append(out, tr.CloseRun(nil)...)
				if len(out) != 2 {
					t.Fatalf("empty lifecycle projected: %v", out)
				}
				assertWireEvents(t, out)
			})
		}
	}
}

func TestMessageOpenersWaitForContent(t *testing.T) {
	for _, reasoning := range []bool{false, true} {
		tr := NewEventTranslator()
		out := tr.Translate(adaptor.RunStarted{})
		var start, content, end adaptor.Event
		if reasoning {
			start = adaptor.Thinking{MessageID: "m", Phase: adaptor.PhaseStart}
			content = adaptor.Thinking{MessageID: "m", Text: "thinking"}
			end = adaptor.Thinking{MessageID: "m", Phase: adaptor.PhaseEnd}
		} else {
			start = adaptor.TextDelta{MessageID: "m", Phase: adaptor.PhaseStart, Role: adaptor.RoleUser}
			content = adaptor.TextDelta{MessageID: "m", Text: "user text"}
			end = adaptor.TextDelta{MessageID: "m", Phase: adaptor.PhaseEnd}
		}
		if got := tr.Translate(start); len(got) != 0 {
			t.Fatal(got)
		}
		got := tr.Translate(content)
		if len(got) != 2 {
			t.Fatalf("want opener + content, got %v", got)
		}
		if !reasoning && (got[0].(*events.TextMessageStartEvent).Role == nil || *got[0].(*events.TextMessageStartEvent).Role != "user") {
			t.Fatal("deferred role lost")
		}
		out = append(out, got...)
		out = append(out, tr.Translate(end)...)
		out = append(out, tr.CloseRun(nil)...)
		if len(out) != 5 {
			t.Fatal(out)
		}
		assertWireEvents(t, out)
	}
	tr := NewEventTranslator()
	tr.Translate(adaptor.RunStarted{})
	tr.Translate(adaptor.TextDelta{MessageID: "tool-only", Phase: adaptor.PhaseStart})
	tool := tr.Translate(adaptor.ToolCall{ID: "tool", Name: "shell", Phase: adaptor.PhaseStart})[0].(*events.ToolCallStartEvent)
	if tool.ParentMessageID == nil || *tool.ParentMessageID != "tool-only" {
		t.Fatal("declared tool-only assistant identity lost")
	}
}

func TestNoticeProjectionRequiresExplicitPassthrough(t *testing.T) {
	operational := []adaptor.Notice{
		{Kind: adaptor.NoticeInvocation, Text: "private invocation", Data: map[string]any{"command": "/private/cli", "cwd": "/private/repo", "env_keys": []string{"PRIVATE_KEY"}}},
		{Kind: adaptor.NoticeRuntime, Text: "private service"},
		{Kind: adaptor.NoticeStep, Text: "private debug", Data: map[string]any{"phase": "debug"}},
		{Kind: adaptor.NoticeTranscriptItem},
		{Kind: adaptor.NoticeLifecycle, Text: "created skill in /private/profile"},
		{Kind: adaptor.NoticeLifecycle, Data: map[string]any{"warning": "unknown_private_warning"}},
		{Kind: "codex.event", Data: map[string]any{"thread_id": "private-provider-id"}},
		{Kind: "claude.message"}, {Kind: "cursor.event"}, {Kind: "codebuddy.event"}, {Kind: "host.card"}, {},
	}
	tr := NewEventTranslator()
	out := tr.Translate(adaptor.RunStarted{})
	for _, n := range operational {
		if got := tr.Translate(n); len(got) != 0 {
			t.Fatalf("operational notice leaked: %q", n.Kind)
		}
	}
	for _, n := range []adaptor.Notice{
		{Kind: adaptor.NoticeLifecycle, Data: map[string]any{"warning": "human_decision_retry_unsupported"}},
		{Kind: "agentAdaptor.input.warning", Data: map[string]any{"code": "input.warning"}},
		{Kind: "stream.dropped", Data: map[string]any{"dropped_count": 1}},
	} {
		got := tr.Translate(n)
		if len(got) != 1 || got[0].Type() != events.EventTypeCustom {
			t.Fatalf("protocol warning lost: %q", n.Kind)
		}
		out = append(out, got...)
	}
	out = append(out, tr.CloseRun(nil)...)
	assertWireEvents(t, out)
	kinds := []string{adaptor.NoticeInvocation}
	option := WithNoticePassthrough(kinds...)
	kinds[0] = "codex.event"
	for i := 0; i < 2; i++ {
		tr = NewEventTranslator(option, WithNoticePassthrough("host.card"), WithProjectionHook(func(e events.Event) []events.Event {
			c := e.(*events.CustomEvent)
			c.Value = map[string]any{"redacted": true}
			return []events.Event{c}
		}))
		tr.Translate(adaptor.RunStarted{})
		for _, name := range []string{adaptor.NoticeInvocation, "host.card"} {
			got := tr.Translate(adaptor.Notice{Kind: name, Text: "private"})
			if len(got) != 1 {
				t.Fatal("explicit opt-in lost")
			}
			raw, _ := got[0].ToJSON()
			if strings.Contains(string(raw), "private") || !strings.Contains(string(raw), "redacted") {
				t.Fatal(string(raw))
			}
		}
		if got := tr.Translate(adaptor.Notice{Kind: "codex.event"}); len(got) != 0 {
			t.Fatal("option captured caller slice")
		}
	}
	tr = NewEventTranslator(WithNoticePassthrough(), WithNoticePassthrough("claude.*"))
	tr.Translate(adaptor.RunStarted{})
	if got := tr.Translate(adaptor.Notice{Kind: "claude.message"}); len(got) != 0 {
		t.Fatal("passthrough is not exact")
	}
}

func TestBoundaryMetadataOmitsUnknownIdentities(t *testing.T) {
	tr := NewEventTranslator(WithRunIdentity("public-thread", "public-run", ""))
	started := tr.Translate(adaptor.RunStarted{})[0]
	if started.GetBaseEvent().Metadata != nil {
		t.Fatalf("unknown identity metadata: %v", started.GetBaseEvent().Metadata)
	}
	tr.Translate(adaptor.WithEventMeta(adaptor.Notice{Kind: adaptor.NoticeInvocation}, adaptor.EventMeta{RunID: "sdk", ThreadKey: "key"}))
	terminal := tr.CloseRun(nil)[0]
	meta := terminal.GetBaseEvent().Metadata["agentAdaptor"]
	if !reflect.DeepEqual(meta, map[string]any{"sdkRunId": "sdk", "threadKey": "key"}) {
		t.Fatal(meta)
	}
	raw, err := json.Marshal([]events.Event{started, terminal})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"sdkRunId":""`) || strings.Contains(string(raw), `"providerThreadId":""`) {
		t.Fatal(string(raw))
	}
}
