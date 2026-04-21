package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	skillName        = "write-proof"
	sessionID        = "mock-codex-skills-session"
	pathPrefix       = "Create the file at "
	contentPrefix    = " with exactly this content: "
	promptSuffix     = ". Do not modify any other files."
	skillPromptIntro = "Use the write-proof skill."
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}

func run() error {
	args := os.Args[1:]
	if len(args) > 0 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Println("mock-codex: codex-compatible verifier for the codex-skills-live example")
		return nil
	}
	if len(args) == 0 || args[0] != "exec" {
		return fmt.Errorf("mock-codex expects `exec`, got %q", strings.Join(args, " "))
	}

	codexHome := strings.TrimSpace(os.Getenv("CODEX_HOME"))
	if codexHome == "" {
		return fmt.Errorf("mock-codex expected CODEX_HOME to be set")
	}

	skillDir := filepath.Join(codexHome, "skills", skillName)
	skillFile := filepath.Join(skillDir, "SKILL.md")
	if info, err := os.Stat(skillFile); err != nil || info.IsDir() {
		return fmt.Errorf("mock-codex expected injected skill bundle at %s", skillFile)
	}

	promptBytes, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("read prompt: %w", err)
	}
	proofPath, proofContent, err := parsePrompt(string(promptBytes))
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(proofPath), 0o755); err != nil {
		return fmt.Errorf("create proof dir: %w", err)
	}
	if err := os.WriteFile(proofPath, []byte(proofContent), 0o644); err != nil {
		return fmt.Errorf("write proof file: %w", err)
	}

	lines := []map[string]any{
		{
			"type":      "thread.started",
			"thread_id": sessionID,
		},
		{
			"type": "item.completed",
			"item": map[string]any{
				"type": "agent_message",
				"text": "mock codex verifier created the requested proof file",
			},
		},
		{
			"type":       "session.updated",
			"session_id": sessionID,
			"display_id": sessionID,
		},
		{
			"type":       "turn.completed",
			"session_id": sessionID,
			"display_id": sessionID,
			"usage": map[string]any{
				"input_tokens":        1,
				"output_tokens":       1,
				"cached_input_tokens": 0,
			},
		},
	}
	for _, line := range lines {
		raw, err := json.Marshal(line)
		if err != nil {
			return fmt.Errorf("marshal output line: %w", err)
		}
		fmt.Println(string(raw))
	}
	return nil
}

func parsePrompt(prompt string) (string, string, error) {
	trimmed := strings.TrimSpace(prompt)
	if !strings.Contains(trimmed, skillPromptIntro) {
		return "", "", fmt.Errorf("mock-codex expected prompt to explicitly invoke %q", skillName)
	}

	pathStart := strings.Index(trimmed, pathPrefix)
	if pathStart < 0 {
		return "", "", fmt.Errorf("mock-codex could not find proof path marker in prompt")
	}
	pathStart += len(pathPrefix)
	contentStart := strings.Index(trimmed[pathStart:], contentPrefix)
	if contentStart < 0 {
		return "", "", fmt.Errorf("mock-codex could not find proof content marker in prompt")
	}
	contentStart += pathStart

	proofPath := strings.TrimSpace(trimmed[pathStart:contentStart])
	contentStart += len(contentPrefix)

	contentEnd := strings.Index(trimmed[contentStart:], promptSuffix)
	if contentEnd < 0 {
		return "", "", fmt.Errorf("mock-codex could not find proof prompt suffix")
	}
	contentEnd += contentStart

	proofContent := trimmed[contentStart:contentEnd]
	if strings.TrimSpace(proofPath) == "" || strings.TrimSpace(proofContent) == "" {
		return "", "", fmt.Errorf("mock-codex parsed empty proof path or content")
	}
	return filepath.FromSlash(proofPath), proofContent, nil
}
