package profilehooks

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

func TestSyncCodexHooksMaterializesHooksJSON(t *testing.T) {
	root := t.TempDir()
	payload := agentadaptor.HookPayload{
		Hooks: []agentadaptor.HookSpec{{
			Key:           "pre-shell",
			Event:         agentadaptor.HookEventPreShell,
			MatcherSpec:   agentadaptor.HookMatcher{Syntax: agentadaptor.HookMatcherSyntaxContains, Pattern: "git"},
			Handler:       agentadaptor.HookHandler{Type: agentadaptor.HookHandlerCommand, Command: "echo", Args: []string{"hello world"}},
			Timeout:       3 * time.Second,
			StatusMessage: "Checking shell",
		}},
		Fingerprint: "hooks-fp",
	}
	snapshot, err := Sync(context.Background(), "codex", root, payload)
	if err != nil {
		t.Fatalf("sync hooks: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "hooks.json"))
	if err != nil {
		t.Fatalf("read hooks: %v", err)
	}
	text := string(raw)
	for _, want := range []string{`"PreToolUse"`, `"matcher": "git"`, `"command": "echo 'hello world'"`, `"statusMessage": "Checking shell"`, `"timeout": 3`} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in %s", want, text)
		}
	}
	if snapshot.Materialization != agentadaptor.ProfileResourceMaterializationNativeManaged {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
}

func TestSyncCodexMCPShortcutUsesDefaultMatcher(t *testing.T) {
	root := t.TempDir()
	payload := agentadaptor.HookPayload{
		Hooks: []agentadaptor.HookSpec{{
			Key:     "pre-mcp",
			Event:   agentadaptor.HookEventPreMCP,
			Handler: agentadaptor.HookHandler{Type: agentadaptor.HookHandlerCommand, Command: "true"},
		}},
		Fingerprint: "hooks-fp",
	}
	if _, err := Sync(context.Background(), "codex", root, payload); err != nil {
		t.Fatalf("sync hooks: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "hooks.json"))
	if err != nil {
		t.Fatalf("read hooks: %v", err)
	}
	text := string(raw)
	for _, want := range []string{`"PreToolUse"`, `"matcher": "mcp__.*"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in %s", want, text)
		}
	}
}

func TestSyncHookCommandQuotesExecutablePath(t *testing.T) {
	root := t.TempDir()
	payload := agentadaptor.HookPayload{
		Hooks: []agentadaptor.HookSpec{{
			Key:   "spaced-command",
			Event: agentadaptor.HookEventPreShell,
			Handler: agentadaptor.HookHandler{
				Type:    agentadaptor.HookHandlerCommand,
				Command: filepath.Join(root, "my hook.sh"),
				Args:    []string{"hello world"},
			},
		}},
		Fingerprint: "hooks-fp",
	}
	if _, err := Sync(context.Background(), "codex", root, payload); err != nil {
		t.Fatalf("sync hooks: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "hooks.json"))
	if err != nil {
		t.Fatalf("read hooks: %v", err)
	}
	var doc struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode hooks JSON: %v\n%s", err, string(raw))
	}
	got := doc.Hooks["PreToolUse"][0].Hooks[0].Command
	want := fmt.Sprintf("%s %s", shellQuote(filepath.Join(root, "my hook.sh")), shellQuote("hello world"))
	if got != want {
		t.Fatalf("expected quoted command %q, got %q in %s", want, got, string(raw))
	}
}

func TestSyncClaudeHooksMaterializesSettingsJSON(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "settings.json"), []byte(`{"env":{"FOO":"bar"}}`), 0o644); err != nil {
		t.Fatalf("write existing settings: %v", err)
	}
	payload := agentadaptor.HookPayload{
		Hooks: []agentadaptor.HookSpec{{
			Key:     "prompt",
			Event:   agentadaptor.HookEventPromptSubmit,
			Handler: agentadaptor.HookHandler{Type: agentadaptor.HookHandlerPrompt, Prompt: "Check the prompt for policy issues."},
		}},
		Fingerprint: "hooks-fp",
	}
	snapshot, err := Sync(context.Background(), "claude", root, payload)
	if err != nil {
		t.Fatalf("sync hooks: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "settings.json"))
	if err != nil {
		t.Fatalf("read hooks: %v", err)
	}
	text := string(raw)
	for _, want := range []string{`"env"`, `"FOO": "bar"`, `"UserPromptSubmit"`, `"type": "prompt"`, `"prompt": "Check the prompt for policy issues."`} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in %s", want, text)
		}
	}
	if snapshot.Support != agentadaptor.ProfileResourceSupportPortableExtended {
		t.Fatalf("expected extended support, got %#v", snapshot)
	}
}

func TestSyncClaudeShortcutHooksUseDefaultMatchers(t *testing.T) {
	root := t.TempDir()
	payload := agentadaptor.HookPayload{
		Hooks: []agentadaptor.HookSpec{
			{Key: "pre-shell", Event: agentadaptor.HookEventPreShell, Handler: agentadaptor.HookHandler{Type: agentadaptor.HookHandlerCommand, Command: "true"}},
			{Key: "pre-mcp", Event: agentadaptor.HookEventPreMCP, Handler: agentadaptor.HookHandler{Type: agentadaptor.HookHandlerCommand, Command: "true"}},
			{Key: "pre-read", Event: agentadaptor.HookEventPreFileRead, Handler: agentadaptor.HookHandler{Type: agentadaptor.HookHandlerCommand, Command: "true"}},
			{Key: "post-edit", Event: agentadaptor.HookEventPostFileEdit, Handler: agentadaptor.HookHandler{Type: agentadaptor.HookHandlerCommand, Command: "true"}},
		},
		Fingerprint: "hooks-fp",
	}
	if _, err := Sync(context.Background(), "claude", root, payload); err != nil {
		t.Fatalf("sync hooks: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "settings.json"))
	if err != nil {
		t.Fatalf("read hooks: %v", err)
	}
	text := string(raw)
	for _, want := range []string{`"matcher": "Bash"`, `"matcher": "mcp__.*"`, `"matcher": "Read"`, `"matcher": "Edit|Write|apply_patch"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in %s", want, text)
		}
	}
}

func TestSyncClaudeHooksRejectsExternalHooks(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "settings.json"), []byte(`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"echo"}]}]}}`), 0o644); err != nil {
		t.Fatalf("write external hooks: %v", err)
	}
	payload := agentadaptor.HookPayload{
		Hooks:       []agentadaptor.HookSpec{{Key: "stop", Event: agentadaptor.HookEventStop, Handler: agentadaptor.HookHandler{Type: agentadaptor.HookHandlerCommand, Command: "echo"}}},
		Fingerprint: "hooks-fp",
	}
	if _, err := Sync(context.Background(), "claude", root, payload); err == nil || !strings.Contains(err.Error(), "external entry") {
		t.Fatalf("expected external conflict, got %v", err)
	}
}

func TestSyncCursorHooksRejectsUnsupportedHandler(t *testing.T) {
	root := t.TempDir()
	payload := agentadaptor.HookPayload{
		Hooks: []agentadaptor.HookSpec{{
			Key:     "http",
			Event:   agentadaptor.HookEventPreShell,
			Handler: agentadaptor.HookHandler{Type: agentadaptor.HookHandlerHTTP, URL: "https://example.test/hook"},
		}},
		Fingerprint: "hooks-fp",
	}
	if _, err := Sync(context.Background(), "cursor", root, payload); err == nil || !strings.Contains(err.Error(), "http hooks are unsupported") {
		t.Fatalf("expected unsupported handler error, got %v", err)
	}
}

func TestSyncHooksRejectsExternalConflict(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hooks.json"), []byte(`{"hooks":{}}`), 0o644); err != nil {
		t.Fatalf("write external hooks: %v", err)
	}
	payload := agentadaptor.HookPayload{
		Hooks:       []agentadaptor.HookSpec{{Key: "stop", Event: agentadaptor.HookEventStop, Handler: agentadaptor.HookHandler{Type: agentadaptor.HookHandlerCommand, Command: "echo"}}},
		Fingerprint: "hooks-fp",
	}
	if _, err := Sync(context.Background(), "codex", root, payload); err == nil || !strings.Contains(err.Error(), "external entry") {
		t.Fatalf("expected external conflict, got %v", err)
	}
}
