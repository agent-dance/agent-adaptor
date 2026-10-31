package sse

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/bridges/agui"
	schema "github.com/santhosh-tekuri/jsonschema/v6"
)

type conformanceStream struct{ *terminalTestStream }

func (s *conformanceStream) RunID() string { return "sdk-run" }

type conformanceRunner struct {
	calls  int
	prompt string
}

func (r *conformanceRunner) Run(context.Context, string, ...adaptor.CallOption) (*adaptor.Result, error) {
	panic("handler must Stream")
}

func (r *conformanceRunner) Stream(_ context.Context, p string, _ ...adaptor.CallOption) adaptor.Stream {
	r.calls++
	r.prompt = p
	ch := make(chan adaptor.Event, 60)
	emit := func(e adaptor.Event) {
		ch <- adaptor.WithEventMeta(e, adaptor.EventMeta{RunID: "sdk-run", ThreadKey: "bridge-key"})
	}
	emit(adaptor.RunStarted{RunID: "provider-run", ThreadID: "provider-thread"})
	emit(adaptor.TextDelta{MessageID: "assistant-1", Text: "Running thirteen commands."})
	for i := 1; i <= 13; i++ {
		id := fmt.Sprintf("tool-%02d", i)
		emit(adaptor.ToolCall{ID: id, Name: "command", Phase: adaptor.PhaseStart, Args: map[string]any{"command": fmt.Sprintf("command %d", i)}})
		m := map[string]any{"output": "ok", "exitCode": float64(0)}
		if i == 13 {
			m = map[string]any{"output": "command failed", "exitCode": json.Number("1"), "error": "exit status 1"}
		}
		emit(adaptor.ToolCall{ID: id, Phase: adaptor.PhaseEnd, Result: m})
	}
	emit(adaptor.RunFinished{RunID: "provider-run", ThreadID: "provider-thread"})
	close(ch)
	return &conformanceStream{&terminalTestStream{events: ch, result: &adaptor.Result{Text: "Running thirteen commands."}}}
}

func TestAGUIHandlerRejectsBeforeStream(t *testing.T) {
	for _, tc := range []struct {
		body, code string
		status     int
	}{
		{`{`, "agui.invalid_json", 400},
		{`{"protocolVersion":"1.0","messages":[]}`, "agui.identity_required", 400},
		{`{"protocolVersion":"2.0","messages":[]}`, "agui.unsupported_version", 400},
		{`{"messages":[{"id":"u","role":"user","content":"old"},{"id":"a","role":"assistant","content":"answer"}]}`, "agui.trailing_user_required", 400},
		{`{"messages":[{"id":"t","role":"tool","toolCallId":"call","content":"result"}]}`, "agui.trailing_user_required", 400},
		{`{"messages":[{"id":"u","role":"user","content":"go"}],"resume":[{"interruptId":"i","status":"resolved"}]}`, "agui.resume_unsupported", 400},
		{`{"messages":[{"id":"u","role":"user","content":[{"type":"image","source":{"type":"url","value":"x"}}]}]}`, "agui.no_executable_text", 400},
		{`{"messages":[{"id":"old","role":"user","content":"old"},{"id":"u","role":"user","content":"new"}]}`, "agui.history_unrecoverable", 409},
	} {
		t.Run(tc.code, func(t *testing.T) {
			runner := &conformanceRunner{}
			w := httptest.NewRecorder()
			Handler(runner, Options{}).ServeHTTP(w, httptest.NewRequest("POST", "/", strings.NewReader(tc.body)))
			if w.Code != tc.status || runner.calls != 0 || strings.Contains(w.Body.String(), "data:") || !strings.Contains(w.Body.String(), tc.code) {
				t.Fatalf("status=%d calls=%d body=%s", w.Code, runner.calls, w.Body)
			}
			if tc.code == "agui.history_unrecoverable" {
				for _, remedy := range []string{"ThreadStore", "InputOptions.HistoryFallback"} {
					if !strings.Contains(w.Body.String(), remedy) {
						t.Fatalf("missing actionable remedy %s: %s", remedy, w.Body)
					}
				}
			}
		})
	}
}

func TestAGUIHandlerGolden(t *testing.T) {
	runner := &conformanceRunner{}
	w := httptest.NewRecorder()
	forwarded := ""
	opts := Options{OnAGUIRequest: func(_ *http.Request, in *agui.RunAgentInput) error {
		forwarded = string(in.ForwardedProps)
		return nil
	}}
	bodyInput := `{
 "protocolVersion":"1.1", "threadId":"public-thread", "runId":"public-run",
 "parentRunId":"public-parent",
 "messages":[{"id":"user-1","role":"user","content":[
  {"type":"text","text":"run commands"},
  {"type":"image","source":{"type":"url","value":"x"}}
 ]}],
 "tools":[{"name":"frontend","description":"unsupported"}],
 "state":{"ignored":true},
 "context":[{"description":"test","value":"thirteen"}],
 "forwardedProps":{ "untouched": true }, "extension":42
}`
	Handler(runner, opts).ServeHTTP(w, httptest.NewRequest("POST", "/", strings.NewReader(bodyInput)))
	if w.Code != 200 || runner.calls != 1 || forwarded != `{ "untouched": true }` || !strings.Contains(runner.prompt, "agent-adaptor-context") {
		t.Fatalf("%d %s prompt=%s forwarded=%s", w.Code, w.Body, runner.prompt, forwarded)
	}
	body := w.Body.String()
	path := "../agui/testdata/handler-agui-1.0.sse"
	// Upstream constructors attach observation time. Normalize it for a stable
	// fixture; all other wire bytes remain exactly those written by Handler.
	var normalized strings.Builder
	var projected []map[string]any
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "data: ") {
			var ev map[string]any
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
				t.Fatal(err)
			}
			delete(ev, "timestamp")
			projected = append(projected, ev)
			raw, _ := json.Marshal(ev)
			line = "data: " + string(raw)
		}
		if strings.HasPrefix(line, "id:") {
			continue
		}
		normalized.WriteString(line + "\n")
	}
	body = normalized.String()
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != body {
		t.Fatal("SSE fixture changed; review and regenerate with UPDATE_GOLDEN=1")
	}
	first, last := projected[0], projected[len(projected)-1]
	if first["threadId"] != "public-thread" || first["runId"] != "public-run" ||
		first["parentRunId"] != "public-parent" || first["protocolVersion"] != "1.0" ||
		last["threadId"] != first["threadId"] || last["runId"] != first["runId"] {
		t.Fatalf("boundaries %v %v", first, last)
	}
	warnings, results := 0, 0
	// Validate every frame against the same frozen schema used by clients.
	raw, err := os.ReadFile("../agui/testdata/ag-ui-schema-1.0.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc any
	_ = json.Unmarshal(raw, &doc)
	c := schema.NewCompiler()
	if err = c.AddResource("fixture.json", doc); err != nil {
		t.Fatal(err)
	}
	compiled, err := c.Compile("fixture.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range projected {
		if err := compiled.Validate(ev); err != nil {
			t.Fatal(err)
		}
		if ev["type"] == "CUSTOM" && ev["name"] == "agentAdaptor.input.warning" {
			warnings++
		}
		if ev["type"] == "TOOL_CALL_RESULT" {
			results++
			if ev["toolCallId"] == "tool-13" {
				m := ev["metadata"].(map[string]any)["agentAdaptor"].(map[string]any)
				if m["status"] != "failed" || m["exitCode"] != float64(1) {
					t.Fatal(m)
				}
			}
		}
	}
	if results != 13 || warnings != 5 {
		t.Fatalf("results=%d warnings=%d", results, warnings)
	}
}

func TestAGUIHandlerHistoryHooks(t *testing.T) {
	body := `{"threadId":"t","runId":"r","messages":[{"id":"o","role":"assistant","content":"old"},{"id":"u","role":"user","content":"new"}]}`
	for _, fallback := range []bool{false, true} {
		r := &conformanceRunner{}
		w := httptest.NewRecorder()
		opts := Options{AGUIInput: agui.InputOptions{HistoryFallback: fallback}}
		if !fallback {
			opts.VerifyHistory = func(_ context.Context, _ *agui.RunAgentInput, _ adaptor.Runner, key string) (string, bool, error) {
				return key, true, nil
			}
		}
		Handler(r, opts).ServeHTTP(w, httptest.NewRequest("POST", "/", strings.NewReader(body)))
		if w.Code != 200 {
			t.Fatal(w.Body)
		}
		if fallback != strings.Contains(r.prompt, "agent-adaptor-history") {
			t.Fatal(r.prompt)
		}
	}
	r := &conformanceRunner{}
	w := httptest.NewRecorder()
	Handler(r, Options{VerifyHistory: func(context.Context, *agui.RunAgentInput, adaptor.Runner, string) (string, bool, error) {
		return "", false, &agui.InputError{Code: "agui.history_diverged", Message: "edited history", Status: 409}
	}}).ServeHTTP(w, httptest.NewRequest("POST", "/", strings.NewReader(body)))
	if w.Code != 409 || r.calls != 0 {
		t.Fatalf("%d %d", w.Code, r.calls)
	}
}

func TestAGUIHandlerGeneratesLegacyWireIdentityWithoutThreadBinding(t *testing.T) {
	runner := &conformanceRunner{}
	body := `{"messages":[{"id":"user","role":"user","content":"go"}]}`
	request, err := prepareAGUIRequest(
		httptest.NewRequest("POST", "/", strings.NewReader(body)), runner, Options{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if request.threadKey != "" || request.target != runner {
		t.Fatal("generated wire identity created a Thread binding")
	}

	var previousRun, previousThread string
	for range 2 {
		response := httptest.NewRecorder()
		Handler(runner, Options{}).ServeHTTP(response,
			httptest.NewRequest("POST", "/", strings.NewReader(body)),
		)
		var boundaries []map[string]any
		for _, line := range strings.Split(response.Body.String(), "\n") {
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var event map[string]any
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
				t.Fatal(err)
			}
			if event["type"] == "RUN_STARTED" || event["type"] == "RUN_FINISHED" {
				boundaries = append(boundaries, event)
			}
		}
		if response.Code != 200 || len(boundaries) != 2 {
			t.Fatalf("status=%d boundaries=%v", response.Code, boundaries)
		}
		start, finish := boundaries[0], boundaries[1]
		runID, _ := start["runId"].(string)
		threadID, _ := start["threadId"].(string)
		if runID == "" || threadID == "" || finish["runId"] != runID || finish["threadId"] != threadID {
			t.Fatalf("legacy identities do not agree: %v", boundaries)
		}
		if runID == previousRun || threadID == previousThread || threadID == "agent-adaptor-unknown-thread" {
			t.Fatalf("legacy IDs were not generated: %v", start)
		}
		previousRun, previousThread = runID, threadID
	}
}
