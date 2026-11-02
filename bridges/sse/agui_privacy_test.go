package sse

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"github.com/agent-dance/agent-adaptor/v2/bridges/agui"
)

type noticePrivacyRunner struct{ conformanceRunner }

func (*noticePrivacyRunner) Send(context.Context, string, ...adaptor.CallOption) adaptor.Stream {
	source := []adaptor.Event{
		adaptor.RunStarted{},
		adaptor.Notice{Kind: adaptor.NoticeInvocation, Text: "private-command-path"},
		adaptor.Notice{Kind: adaptor.NoticeRuntime, Text: "private-runtime"},
		adaptor.Notice{Kind: adaptor.NoticeTranscriptItem},
		adaptor.Notice{Kind: "codex.event", Text: "private-provider-payload"},
		adaptor.Notice{Kind: "claude.event", Text: "private-claude-payload"},
		adaptor.Notice{Kind: adaptor.NoticeLifecycle, Text: "private-skill-path"},
		adaptor.Notice{Kind: adaptor.NoticeLifecycle, Data: map[string]any{"warning": "human_decision_retry_unsupported"}},
		adaptor.Notice{Kind: adaptor.NoticeStep, Text: "step", Data: map[string]any{"phase": "started"}},
		adaptor.Notice{Kind: adaptor.NoticeStep, Text: "step", Data: map[string]any{"phase": "finished"}},
		adaptor.Dropped{Count: 1},
		adaptor.SubagentUpdate{Member: "helper", Kind: adaptor.SubagentStarted, Data: map[string]any{"delegation_id": "child"}},
		adaptor.Thinking{MessageID: "empty", Phase: adaptor.PhaseStart}, adaptor.Thinking{MessageID: "empty", Phase: adaptor.PhaseEnd},
		adaptor.TextDelta{MessageID: "empty-text", Phase: adaptor.PhaseStart}, adaptor.TextDelta{MessageID: "empty-text", Phase: adaptor.PhaseEnd},
		adaptor.TextDelta{MessageID: "a", Text: "visible"},
	}
	ch := make(chan adaptor.Event, len(source))
	for _, ev := range source {
		ch <- ev
	}
	close(ch)
	return &conformanceStream{&terminalTestStream{events: ch}}
}
func TestAGUIHandlerFiltersOperationalNoticesByDefault(t *testing.T) {
	for _, optIn := range []bool{false, true} {
		opts := Options{}
		if optIn {
			opts.AGUIEvents = []agui.EventTranslatorOption{agui.WithNoticePassthrough(adaptor.NoticeInvocation, "codex.event")}
		}
		w := httptest.NewRecorder()
		Handler(&noticePrivacyRunner{}, opts).ServeHTTP(w, httptest.NewRequest("POST", "/", strings.NewReader(`{"protocolVersion":"1.1","threadId":"t","runId":"r","messages":[{"id":"u","role":"user","content":"go"}]}`)))
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body)
		}
		body := w.Body.String()
		for _, marker := range []string{"private-command-path", "private-provider-payload"} {
			if strings.Contains(body, marker) != optIn {
				t.Fatalf("opt-in %t: %s", optIn, body)
			}
		}
		for _, marker := range []string{"private-runtime", "private-claude-payload", "private-skill-path", "transcript.item", "empty-text", `"messageId":"empty"`} {
			if strings.Contains(body, marker) {
				t.Fatalf("unapproved data leaked: %s", marker)
			}
		}
		for _, marker := range []string{"human_decision_retry_unsupported", "agentAdaptor.input.warning", "stream.dropped", "STEP_STARTED", "STEP_FINISHED", "ACTIVITY_SNAPSHOT", "RUN_FINISHED"} {
			if !strings.Contains(body, marker) {
				t.Fatalf("protocol-relevant event lost: %s", marker)
			}
		}
	}
}
