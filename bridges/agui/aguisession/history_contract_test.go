package aguisession

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/bridges/agui"
)

func TestReasoningIsOptionalInHistoryVerification(t *testing.T) {
	s := New(fakeRunner{func(context.Context, string) adaptor.Stream {
		return script(nil, nil,
			adaptor.Thinking{MessageID: "empty", Phase: adaptor.PhaseStart}, adaptor.Thinking{MessageID: "empty", Phase: adaptor.PhaseEnd},
			adaptor.Thinking{MessageID: "reason", Text: "optional reasoning"},
			adaptor.TextDelta{MessageID: "answer", Text: "answer"},
			adaptor.ToolCall{ID: "call", Name: "shell", Phase: adaptor.PhaseStart},
			adaptor.ToolCall{ID: "call", Name: "shell", Phase: adaptor.PhaseEnd, Result: map[string]any{"output": "ok", "exitCode": 0}},
		)
	}}, Options{Recorder: recorder(t), Input: agui.InputOptions{HistoryFallback: true}})
	if w := request(s, "t", "first", []Message{user("u1", "first")}); w.Code != 200 {
		t.Fatal(w.Body)
	}
	history := checkedMessages(t, s, ThreadKey("t"))
	for _, m := range history {
		if m["role"] == "reasoning" && m["content"] == "" {
			t.Fatal("empty reasoning reached history")
		}
	}
	raw, err := json.Marshal(history)
	if err != nil {
		t.Fatal(err)
	}
	var prior []agui.Message
	if err = json.Unmarshal(raw, &prior); err != nil {
		t.Fatal(err)
	}
	strip := func(ms []agui.Message) []agui.Message {
		out := []agui.Message{}
		for _, m := range ms {
			if m.Role != "reasoning" {
				out = append(out, m)
			}
		}
		return out
	}
	cases := []struct {
		name     string
		edit     func([]agui.Message) []agui.Message
		diverged bool
	}{
		{"present", func(ms []agui.Message) []agui.Message { return ms }, false},
		{"absent", strip, false},
		{"empty", func(ms []agui.Message) []agui.Message {
			return append(strip(ms), agui.Message{ID: "empty-from-client", Role: "reasoning", Content: json.RawMessage(`""`)})
		}, false},
		{"different optional ids", func(ms []agui.Message) []agui.Message {
			for i := range ms {
				if ms[i].Role == "reasoning" {
					ms[i].ID = "client-reasoning"
					ms[i].Content = json.RawMessage(`"optional"`)
				}
			}
			return ms
		}, false},
		{"required assistant disguised as reasoning", func(ms []agui.Message) []agui.Message {
			for i := range ms {
				if ms[i].Role == "assistant" {
					ms[i].Role = "reasoning"
				}
			}
			return ms
		}, true},
		{"reordered", func(ms []agui.Message) []agui.Message { ms = strip(ms); ms[0], ms[1] = ms[1], ms[0]; return ms }, true},
	}
	for _, role := range []string{"user", "assistant", "tool"} {
		cases = append(cases, struct {
			name     string
			edit     func([]agui.Message) []agui.Message
			diverged bool
		}{role + " id changed", func(ms []agui.Message) []agui.Message {
			for i := range ms {
				if ms[i].Role == role {
					ms[i].ID += "-edited"
				}
			}
			return ms
		}, true})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := tc.edit(append([]agui.Message{}, prior...))
			input = append(input, agui.Message{ID: "u2", Role: "user", Content: json.RawMessage(`"next"`)})
			_, _, err := s.VerifyHistory(context.Background(), &agui.RunAgentInput{Messages: input}, fakeRunner{}, ThreadKey("t"))
			if tc.diverged {
				if err == nil || !strings.Contains(err.Error(), "client message IDs/roles") {
					t.Fatalf("required identity mutation accepted: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSessionNoticePrivacyAndEmptyEventArrays(t *testing.T) {
	for _, optIn := range []bool{false, true} {
		options := Options{Recorder: recorder(t)}
		if optIn {
			options.Events = []agui.EventTranslatorOption{agui.WithNoticePassthrough(adaptor.NoticeInvocation, "codex.event")}
		}
		s := New(fakeRunner{func(context.Context, string) adaptor.Stream {
			return script(nil, nil,
				adaptor.Notice{Kind: adaptor.NoticeInvocation, Data: map[string]any{"command": "private-cli"}},
				adaptor.Notice{Kind: "codex.event", Data: map[string]any{"thread": "private-provider-thread"}},
				adaptor.Notice{Kind: adaptor.NoticeTranscriptItem},
				adaptor.Thinking{MessageID: "empty", Phase: adaptor.PhaseStart}, adaptor.Thinking{MessageID: "empty", Phase: adaptor.PhaseEnd},
				adaptor.TextDelta{MessageID: "empty-text", Phase: adaptor.PhaseStart}, adaptor.TextDelta{MessageID: "empty-text", Phase: adaptor.PhaseEnd},
				adaptor.TextDelta{MessageID: "a", Text: "public answer"},
			)
		}}, options)
		w := request(s, "t", "run", []Message{user("u", "go")})
		if w.Code != 200 {
			t.Fatal(w.Body)
		}
		if strings.Contains(w.Body.String(), "private-cli") != optIn || strings.Contains(w.Body.String(), "private-provider-thread") != optIn {
			t.Fatalf("live notice policy: %s", w.Body)
		}
		key := ThreadKey("t")
		records, err := s.Events(context.Background(), key, 0)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(records)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), "private-cli") != optIn || strings.Contains(string(encoded), "private-provider-thread") != optIn {
			t.Fatal("history notice policy differs from live")
		}
		if strings.Contains(string(encoded), `"events":null`) {
			t.Fatal("null event array")
		}
		stored, err := options.Recorder.Since(context.Background(), RecorderKey(key), 0)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, r := range stored {
			if n, ok := r.Event.(adaptor.Notice); ok && n.Kind == adaptor.NoticeInvocation {
				found = true
			}
		}
		if !found {
			t.Fatal("privacy filtering discarded the trusted raw audit record")
		}
		w = httptest.NewRecorder()
		s.EventsHandler().ServeHTTP(w, httptest.NewRequest("GET", "/?thread_id=t", nil))
		if w.Code != 200 || strings.Contains(w.Body.String(), `"events":null`) || !strings.Contains(w.Body.String(), `"events":[]`) {
			t.Fatalf("events HTTP shape: %d %s", w.Code, w.Body)
		}
		for _, m := range checkedMessages(t, s, key) {
			if m["role"] == "reasoning" || m["role"] == "assistant" && m["content"] == "" {
				t.Fatal("empty source lifecycle materialized")
			}
		}
		live := wireEvents(t, request(s, "other", "run-2", []Message{user("u2", "go")}))
		for _, e := range live {
			metadata, _ := e["metadata"].(map[string]any)
			ns, _ := metadata["agentAdaptor"].(map[string]any)
			if value, exists := ns["sdkRunId"]; exists && value == "" {
				t.Fatal("empty SDK identity on session wire")
			}
		}
		last := live[len(live)-1]
		ns := last["metadata"].(map[string]any)["agentAdaptor"].(map[string]any)
		if ns["sdkRunId"] != "sdk" {
			t.Fatal("terminal lost observed SDK identity")
		}
	}
	raw, err := json.Marshal(ProjectedRecord{HostSeq: 1})
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"host_seq":1,"events":[]}` {
		t.Fatal(string(raw))
	}
}
