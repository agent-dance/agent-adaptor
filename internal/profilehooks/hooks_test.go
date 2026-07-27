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

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
)

func TestSyncCodexHooksMaterializesHooksJSON(t *testing.T) {
	root := t.TempDir()
	payload := driver.HookPayload{
		Hooks: []driver.HookSpec{{
			Key:           "pre-shell",
			Event:         driver.HookEventPreShell,
			MatcherSpec:   driver.HookMatcher{Syntax: driver.HookMatcherSyntaxContains, Pattern: "git"},
			Handler:       driver.HookHandler{Type: driver.HookHandlerCommand, Command: "echo", Args: []string{"hello world"}},
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
	if snapshot.Materialization != engine.ProfileResourceMaterializationNativeManaged {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
}

func TestSyncCodexMCPShortcutUsesDefaultMatcher(t *testing.T) {
	root := t.TempDir()
	payload := driver.HookPayload{
		Hooks: []driver.HookSpec{{
			Key:     "pre-mcp",
			Event:   driver.HookEventPreMCP,
			Handler: driver.HookHandler{Type: driver.HookHandlerCommand, Command: "true"},
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
	payload := driver.HookPayload{
		Hooks: []driver.HookSpec{{
			Key:   "spaced-command",
			Event: driver.HookEventPreShell,
			Handler: driver.HookHandler{
				Type:    driver.HookHandlerCommand,
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
	payload := driver.HookPayload{
		Hooks: []driver.HookSpec{{
			Key:     "prompt",
			Event:   driver.HookEventPromptSubmit,
			Handler: driver.HookHandler{Type: driver.HookHandlerPrompt, Prompt: "Check the prompt for policy issues."},
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
	if snapshot.Support != engine.ProfileResourceSupportPortableExtended {
		t.Fatalf("expected extended support, got %#v", snapshot)
	}
}

func TestSyncClaudeShortcutHooksUseDefaultMatchers(t *testing.T) {
	root := t.TempDir()
	payload := driver.HookPayload{
		Hooks: []driver.HookSpec{
			{Key: "pre-shell", Event: driver.HookEventPreShell, Handler: driver.HookHandler{Type: driver.HookHandlerCommand, Command: "true"}},
			{Key: "pre-mcp", Event: driver.HookEventPreMCP, Handler: driver.HookHandler{Type: driver.HookHandlerCommand, Command: "true"}},
			{Key: "pre-read", Event: driver.HookEventPreFileRead, Handler: driver.HookHandler{Type: driver.HookHandlerCommand, Command: "true"}},
			{Key: "post-edit", Event: driver.HookEventPostFileEdit, Handler: driver.HookHandler{Type: driver.HookHandlerCommand, Command: "true"}},
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
	payload := driver.HookPayload{
		Hooks:       []driver.HookSpec{{Key: "stop", Event: driver.HookEventStop, Handler: driver.HookHandler{Type: driver.HookHandlerCommand, Command: "echo"}}},
		Fingerprint: "hooks-fp",
	}
	if _, err := Sync(context.Background(), "claude", root, payload); err == nil || !strings.Contains(err.Error(), "external entry") {
		t.Fatalf("expected external conflict, got %v", err)
	}
}

func TestSyncCursorHooksRejectsUnsupportedHandler(t *testing.T) {
	root := t.TempDir()
	payload := driver.HookPayload{
		Hooks: []driver.HookSpec{{
			Key:     "http",
			Event:   driver.HookEventPreShell,
			Handler: driver.HookHandler{Type: driver.HookHandlerHTTP, URL: "https://example.test/hook"},
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
	payload := driver.HookPayload{
		Hooks:       []driver.HookSpec{{Key: "stop", Event: driver.HookEventStop, Handler: driver.HookHandler{Type: driver.HookHandlerCommand, Command: "echo"}}},
		Fingerprint: "hooks-fp",
	}
	if _, err := Sync(context.Background(), "codex", root, payload); err == nil || !strings.Contains(err.Error(), "external entry") {
		t.Fatalf("expected external conflict, got %v", err)
	}
}
