package claude

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/v2/adaptertest"
	"github.com/agent-dance/agent-adaptor/v2/driver"
)

func claudeMessageFixture(transport string) func(*testing.T, adaptertest.MessageScenario) adaptertest.MessageFixture {
	return func(t *testing.T, scenario adaptertest.MessageScenario) adaptertest.MessageFixture {
		t.Helper()
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		home := t.TempDir()
		path := filepath.Join(home, "evidence.json")
		script, err := json.Marshal(scenario)
		if err != nil {
			t.Fatal(err)
		}
		d := Driver(Config{CommonConfig: CommonConfig{Command: exe, CWD: home, GracePeriod: 50 * time.Millisecond, Env: []driver.EnvBinding{{Name: messageHelperEnv, Value: "1"}, {Name: "CLAUDE_CONFIG_DIR", Value: home}, {Name: "CLAUDE_MSG_SCENARIO", Value: string(script)}, {Name: "CLAUDE_MSG_EVIDENCE", Value: path}}}})
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := d.(driver.ProcessLifecycleDriver).CloseProcesses(ctx); err != nil {
				t.Error(err)
			}
		})
		req := driver.Request{Prompt: "initial", Spawn: transport != "resident"}
		if transport == "resident" {
			req.Session = &driver.SessionContext{EngineSessionID: "adaptertest-thread"}
		}
		if transport == "native" {
			req.OutputSchema = &driver.OutputSchema{Format: driver.OutputFormatJSONSchema, SchemaJSON: json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"],"additionalProperties":false}`)}
			req.StructuredOutputSource = driver.StructuredOutputSourceNative
		}
		return adaptertest.MessageFixture{Driver: d, Request: req, Evidence: func() adaptertest.MessageEvidence {
			var e adaptertest.MessageEvidence
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(b, &e); err != nil {
				t.Fatal(err)
			}
			return e
		}}
	}
}

func runClaudeMessageConformanceHelper() int {
	var scenario adaptertest.MessageScenario
	if json.Unmarshal([]byte(os.Getenv("CLAUDE_MSG_SCENARIO")), &scenario) != nil {
		return 80
	}
	path := os.Getenv("CLAUDE_MSG_EVIDENCE")
	var e adaptertest.MessageEvidence
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &e)
	}
	save := func() {
		b, _ := json.Marshal(e)
		_ = os.WriteFile(path+".next", b, 0o600)
		_ = os.Rename(path+".next", path)
	}
	resumed := false
	for i, arg := range os.Args {
		if arg == "--resume" && i+1 < len(os.Args) && os.Args[i+1] == "messages-session" {
			resumed = true
		}
	}
	seenPrompt := false
	enc := json.NewEncoder(os.Stdout)
	terminal := func() {
		tc := driver.TurnCompleted{Text: "done", Usage: &driver.Usage{InputTokens: 7, OutputTokens: 9}}
		e.Resumed = resumed || seenPrompt
		seenPrompt = true
		e.Turns = append(e.Turns, tc)
		save()
		_ = enc.Encode(map[string]any{"type": "result", "subtype": "success", "is_error": false, "session_id": "messages-session", "result": "done", "usage": map[string]int{"input_tokens": 7, "output_tokens": 9}, "structured_output": map[string]bool{"ok": true}})
	}
	if !claudePersistentInputMode(os.Args[1:]) {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return 81
		}
		e.Received = append(e.Received, string(b))
		e.Turns = nil
		terminal()
		return 0
	}
	dec := json.NewDecoder(os.Stdin)
	read := func() (map[string]any, bool) {
		var frame map[string]any
		err := dec.Decode(&frame)
		return frame, err == nil
	}
	hook := func(event string) (string, bool) {
		_ = enc.Encode(messageHook(event, "", ""))
		frame, ok := read()
		if !ok {
			return "", false
		}
		output := claudeTopLevelObject(claudeTopLevelObject(frame, "response"), "response")
		text := claudeTopLevelString(claudeTopLevelObject(output, "hookSpecificOutput"), "additionalContext")
		if event == "Stop" {
			text = claudeTopLevelString(output, "reason")
		}
		if text != "" {
			e.Received = append(e.Received, text)
			save()
		}
		return text, true
	}
	for {
		frame, ok := read()
		if !ok {
			return 0
		}
		if claudeTopLevelString(frame, "type") == "control_request" {
			if _, err := acknowledgeClaudeInitialize(frame, enc); err != nil {
				return 82
			}
			continue
		}
		if claudeTopLevelString(frame, "type") != "user" {
			return 83
		}
		prompt := claudeTopLevelString(claudeTopLevelObject(frame, "message"), "content")
		e.Received = append(e.Received, prompt)
		e.Turns = nil
		save()
		_ = enc.Encode(map[string]any{"type": "system", "subtype": "init", "session_id": "messages-session", "model": "fake"})
		if prompt != "initial" || scenario.Name == "disabled" {
			terminal()
			continue
		}
		event := "PreToolUse"
		if scenario.Boundary == driver.BoundaryStop {
			event = "Stop"
		}
		if scenario.Name == "continuation" {
			event = "Stop"
		}
		if scenario.Name == "write_failure" || scenario.Name == "process_exit" || scenario.Name == "cancel" || scenario.Name == "timeout" {
			// completeStream reports the interrupted process turn with its observed
			// (empty) assistant text; no token accounting was reported by the peer.
			e.Turns = []driver.TurnCompleted{{}}
			save()
			if scenario.Name == "write_failure" || scenario.Name == "process_exit" {
				_ = os.Stdin.Close()
			}
			_ = enc.Encode(messageHook(event, "", ""))
			if scenario.Name == "process_exit" {
				return 84
			}
			if scenario.Name == "write_failure" {
				r, w, err := os.Pipe()
				if err != nil {
					return 85
				}
				defer w.Close()
				_, _ = io.Copy(io.Discard, r)
				return 0
			}
			_, _ = io.Copy(io.Discard, os.Stdin)
			return 0
		}
		if event != "Stop" {
			if _, ok := hook(event); !ok {
				return 86
			}
		}
		for {
			// Stop(block) ends one model turn. Its text is independently emitted
			// before the callback and expected in TurnCompleted if it continues.
			_ = enc.Encode(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{map[string]string{"type": "text", "text": "segment"}}}})
			text, ok := hook("Stop")
			if !ok {
				return 87
			}
			if text == "" {
				break
			}
			e.Turns = append(e.Turns, driver.TurnCompleted{Text: "segment"})
			save()
		}
		terminal()
	}
}
