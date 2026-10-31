package agui

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func decodeInputForTest(t *testing.T, body string) (*RunAgentInput, error) {
	t.Helper()
	return DecodeHTTPRequest(httptest.NewRequest("POST", "/", strings.NewReader(body)))
}

func TestValidation(t *testing.T) {
	for _, tc := range []struct{ name, body, code string }{
		{"syntax", "{", "agui.invalid_json"}, {"null", "null", "agui.invalid_input"},
		{"missing messages", `{}`, "agui.invalid_input"}, {"null messages", `{"messages":null}`, "agui.invalid_input"},
		{"missing identity", `{"protocolVersion":"1.0","messages":[]}`, "agui.identity_required"},
		{"bad version", `{"protocolVersion":"1","messages":[]}`, "agui.unsupported_version"},
		{"unsupported major", `{"protocolVersion":"2.0","messages":[]}`, "agui.unsupported_version"},
		{"wrong known type", `{"messages":[],"threadId":4}`, "agui.invalid_input"},
		{"missing message id", `{"messages":[{"role":"user","content":"x"}]}`, "agui.invalid_input"},
		{"null forwarded", `{"messages":[],"forwardedProps":null}`, "agui.invalid_input"},
		{"bad tool", `{"messages":[],"tools":[{"name":"x"}]}`, "agui.invalid_input"},
		{"bad context", `{"messages":[],"context":[{"description":3,"value":"x"}]}`, "agui.invalid_input"},
		{"bad resume", `{"messages":[],"resume":[{"interruptId":"i","status":"other"}]}`, "agui.invalid_input"},
		{"bad content", `{"messages":[{"id":"m","role":"user","content":4}]}`, "agui.invalid_input"},
		{"bad text part", `{"messages":[{"id":"m","role":"user","content":[{"type":"text","text":4}]}]}`, "agui.invalid_input"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := decodeInputForTest(t, tc.body)
			if err == nil || !strings.Contains(err.Error(), tc.code) {
				t.Fatalf("%v, want %s", err, tc.code)
			}
		})
	}
}

func TestCompleteInputAndUnknownCapture(t *testing.T) {
	in, err := decodeInputForTest(t, `{"threadId":"t","runId":"r","protocolVersion":"1.12","parentRunId":"p","extra":{"x":1},"messages":[
 {"id":"s","role":"system","content":"s","name":"system"},
 {"id":"d","role":"developer","content":"d"},
 {"id":"a","role":"assistant","encryptedValue":"cipher","toolCalls":[
 {"id":"tc","type":"function","function":{"name":"f","arguments":"{bad","extra":true},"encryptedValue":"c"}]},
 {"id":"tool","role":"tool","toolCallId":"tc","content":"partial","error":"failure"},
 {"id":"reason","role":"reasoning","content":"why","encryptedValue":"secret"},
 {"id":"act","role":"activity","activityType":"card","content":{"arbitrary":true}},
 {"id":"u","role":"user","subagentRunId":"sub","content":"go","extra":42}],
 "tools":[{"name":"f","description":"d","parameters":true}],
 "state":0,"context":[{"description":"d","value":"v"}],"forwardedProps": { "raw" : [1, 2] },"resume":[]}`)
	if err != nil {
		t.Fatal(err)
	}
	if in.ParentRunID != "p" || in.Messages[2].ToolCalls[0].Function.Arguments != "{bad" ||
		in.Messages[3].Error != "failure" || in.Messages[4].EncryptedValue != "secret" {
		t.Fatalf("lost fields: %+v", in)
	}
	if len(in.UnknownMembers) != 3 || len(in.Diagnostics) != 4 {
		t.Fatalf("unknown=%v diagnostics=%v", in.UnknownMembers, in.Diagnostics)
	}
	if string(in.ForwardedProps) != `{ "raw" : [1, 2] }` {
		t.Fatalf("forwarded altered: %s", in.ForwardedProps)
	}
}

func TestPromptPolicy(t *testing.T) {
	for _, role := range []string{"assistant", "tool", "system", "developer", "reasoning", "activity"} {
		in := &RunAgentInput{Messages: []Message{{Role: "user", Content: json.RawMessage(`"old"`)}, {Role: role, Content: json.RawMessage(`"latest"`)}}}
		if _, _, err := PrepareInput(in, InputOptions{}, InputState{CheckpointUsable: true}); err == nil {
			t.Fatalf("accepted trailing %s", role)
		}
	}
	in := &RunAgentInput{Messages: []Message{{Role: "user", Content: json.RawMessage(`"old"`)}, {Role: "user", Content: json.RawMessage(`"new"`)}}}
	if _, _, err := PrepareInput(in, InputOptions{}, InputState{}); err == nil {
		t.Fatal("dropped history")
	}
	prompt, _, err := PrepareInput(in, InputOptions{}, InputState{CheckpointUsable: true})
	if err != nil || prompt != "new" {
		t.Fatalf("%q %v", prompt, err)
	}
	prompt, warnings, err := PrepareInput(in, InputOptions{HistoryFallback: true}, InputState{})
	if err != nil || !strings.Contains(prompt, `"text":"old"`) || !strings.HasSuffix(prompt, "new") || len(warnings) != 1 {
		t.Fatalf("%q %v %v", prompt, warnings, err)
	}
	in.Messages[1].Content = json.RawMessage(`""`)
	if _, _, err := PrepareInput(in, InputOptions{}, InputState{CheckpointUsable: true}); err == nil {
		t.Fatal("replayed old user")
	}
}

func TestDegradationAndResume(t *testing.T) {
	in, err := decodeInputForTest(t, `{
 "messages":[{"id":"u","role":"user","content":[
  {"type":"text","text":"hello"},
  {"type":"image","source":{"type":"url","value":"x"}}
 ]}],
 "tools":[{"name":"f","description":"f"}],
 "state":{"x":1},
 "context":[{"description":"one","value":"two"}]
}`)
	if err != nil {
		t.Fatal(err)
	}
	p, d, err := PrepareInput(in, InputOptions{}, InputState{})
	if err != nil || !strings.Contains(p, "<agent-adaptor-context>\n[{\"description\":\"one\",\"value\":\"two\"}]") {
		t.Fatalf("%q %v", p, err)
	}
	codes := map[string]bool{}
	for _, w := range d {
		codes[w.Code] = true
	}
	for _, code := range []string{"agui.legacy_identity", "agui.content_part_skipped", "agui.tools_unsupported", "agui.state_unsupported"} {
		if !codes[code] {
			t.Fatalf("missing %s: %v", code, d)
		}
	}
	p, d, err = PrepareInput(in, InputOptions{DisableContext: true}, InputState{})
	if err != nil || p != "hello" || d[len(d)-1].Code != "agui.context_disabled" {
		t.Fatalf("%q %v %v", p, d, err)
	}
	in.Resume = []ResumeEntry{{InterruptID: "i", Status: "resolved"}}
	if _, _, err = PrepareInput(in, InputOptions{}, InputState{}); err == nil {
		t.Fatal("accepted resume")
	}
	in.Resume = nil
	in.Messages[0].Content = json.RawMessage(`[{"type":"audio","source":{"type":"url","value":"x"}}]`)
	if _, _, err = PrepareInput(in, InputOptions{}, InputState{}); err == nil {
		t.Fatal("accepted empty projection")
	}
}

func TestFutureContentPartIsSkipped(t *testing.T) {
	in, err := decodeInputForTest(t, `{"messages":[{"id":"u","role":"user","content":[
 {"type":"binary","value":"opaque"}, {"type":"text","text":"go"}
]}]}`)
	if err != nil {
		t.Fatal(err)
	}
	prompt, warnings, err := PrepareInput(in, InputOptions{}, InputState{})
	if err != nil || prompt != "go" || len(in.UnknownMembers) != 1 {
		t.Fatalf("%q %v %v", prompt, in, err)
	}
	found := false
	for _, d := range warnings {
		if d.Code == "agui.content_part_skipped" && d.Path == "/messages/0/content/0" {
			found = true
		}
	}
	if !found {
		t.Fatal(warnings)
	}
}

func TestStateEmptiness(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state string
		warn  bool
	}{
		{name: "absent"},
		{name: "null", state: "null"},
		{name: "empty object", state: "{ \n\t }"},
		{name: "empty array", state: "[ ]"},
		{name: "empty string", state: `""`},
		{name: "object value", state: `{"key":1}`, warn: true},
		{name: "array value", state: `[1]`, warn: true},
		{name: "zero", state: "0", warn: true},
		{name: "false", state: "false", warn: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"messages":[{"id":"u","role":"user","content":"go"}]`
			if tc.state != "" {
				body += `,"state":` + tc.state
			}
			in, err := decodeInputForTest(t, body+"}")
			if err != nil {
				t.Fatal(err)
			}
			_, warnings, err := PrepareInput(in, InputOptions{}, InputState{})
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, warning := range warnings {
				found = found || warning.Code == "agui.state_unsupported"
			}
			if found != tc.warn {
				t.Fatalf("warnings=%v, want state warning=%v", warnings, tc.warn)
			}
		})
	}
}

func TestContentPartDiagnosticsFollowExecutionProjection(t *testing.T) {
	body := `{"messages":[
		{"id":"old","role":"user","content":[
			{"type":"binary","value":"old"},
			{"type":"image","source":{"type":"url","value":"old"}},
			{"type":"text","text":"prior"}
		]},
		{"id":"tool","role":"tool","toolCallId":"call","content":[
			{"type":"image","source":{"type":"url","value":"tool"}}
		]},
		{"id":"current","role":"user","content":[
			{"type":"binary","value":"current"},
			{"type":"image","source":{"type":"url","value":"current"}},
			{"type":"text","text":"go"}
		]}
	]}`
	for _, fallback := range []bool{false, true} {
		in, err := decodeInputForTest(t, body)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(in.Messages[2].Content), "binary") {
			t.Fatal("unknown part was not removed")
		}
		_, warnings, err := PrepareInput(in,
			InputOptions{HistoryFallback: fallback},
			InputState{CheckpointUsable: !fallback},
		)
		if err != nil {
			t.Fatal(err)
		}
		paths := map[string]int{}
		for _, warning := range warnings {
			if warning.Code == "agui.content_part_skipped" {
				paths[warning.Path]++
			}
		}
		want := 2
		if fallback {
			want = 4
		}
		if len(paths) != want {
			t.Fatalf("fallback=%v paths=%v", fallback, paths)
		}
		for path, count := range paths {
			if count != 1 || strings.HasPrefix(path, "/messages/1/") {
				t.Fatalf("duplicate or unprojected warning: %v", paths)
			}
		}
		for _, path := range []string{"/messages/2/content/0", "/messages/2/content/1"} {
			if paths[path] != 1 {
				t.Fatalf("lost original part position %s: %v", path, paths)
			}
		}
	}
}
