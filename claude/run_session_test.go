package claude

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/internal/testutil"
)

func TestClaudeRunPreservesAndGuardsSessionState(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	skillDir := filepath.Join(home, "skills", "analysis")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# Analysis\n"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	command := testutil.WriteCommand(t, home, "fake-claude",
		"#!/bin/sh\nset -eu\nprompt=$(cat)\nprintf 'stderr:%s\\n' \"$prompt\" >&2\nprintf '{\"event\":\"turn.completed\",\"session_id\":\"claude-session\",\"display_id\":\"claude-display\"}\\n'\n",
		"@echo off\r\nsetlocal\r\nset /p PROMPT=\r\n>&2 echo stderr:%PROMPT%\r\necho {\"event\":\"turn.completed\",\"session_id\":\"claude-session\",\"display_id\":\"claude-display\"}\r\n",
	)

	cfg := agentadaptor.ClaudeConfig{
		CommonConfig: agentadaptor.CommonConfig{
			Command: command,
			CWD:     workspace,
			Env: []agentadaptor.EnvBinding{
				{Name: "HOME", Value: home},
				{Name: "USERPROFILE", Value: home},
			},
		},
		Model: "claude-sonnet-4",
	}
	payloadA := agentadaptor.SkillPayload{
		Mode:      agentadaptor.SkillSyncEphemeral,
		Requested: []string{"analysis"},
		RuntimeEntries: []agentadaptor.SkillRuntimeEntry{
			{Key: "analysis", RuntimeName: "analysis", SourcePath: skillDir},
		},
		Fingerprint: "bundle-a",
	}
	payloadB := agentadaptor.SkillPayload{
		Mode:      agentadaptor.SkillSyncEphemeral,
		Requested: []string{"analysis"},
		RuntimeEntries: []agentadaptor.SkillRuntimeEntry{
			{Key: "analysis", RuntimeName: "analysis", SourcePath: skillDir},
		},
		Fingerprint: "bundle-b",
	}
	req := agentadaptor.DriverRunRequest{
		Prompt:    "hello from claude",
		Config:    cfg,
		Workspace: agentadaptor.WorkspaceLease{ID: "workspace-a", CWD: workspace},
		Skills:    payloadA,
	}

	events := &testutil.EventRecorder{}
	first, err := NewAdapter().Run(context.Background(), req, events)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if first.Checkpoint == nil || first.Checkpoint.State == nil || !first.Checkpoint.Valid {
		t.Fatalf("expected valid checkpoint, got %#v", first.Checkpoint)
	}
	if first.Checkpoint.State.Data[agentadaptor.SessionParamPromptBundleKey] != "bundle-a" {
		t.Fatalf("expected prompt bundle guard, got %#v", first.Checkpoint.State.Data)
	}
	if len(first.Transcript) == 0 || first.Transcript[0].Type != agentadaptor.TranscriptStructured {
		t.Fatalf("expected transcript items, got %#v", first.Transcript)
	}
	assertHasInvocationAndSpawn(t, events.Snapshot())

	continueReq := req
	continueReq.Session = &agentadaptor.DriverSessionContext{State: first.Checkpoint.State}
	if _, err := NewAdapter().Run(context.Background(), continueReq, &testutil.EventRecorder{}); err != nil {
		t.Fatalf("resume run: %v", err)
	}

	rejectReq := req
	rejectReq.Skills = payloadB
	rejectReq.Session = &agentadaptor.DriverSessionContext{State: first.Checkpoint.State}
	_, err = NewAdapter().Run(context.Background(), rejectReq, &testutil.EventRecorder{})
	if !errors.Is(err, agentadaptor.ErrResumeRejected) {
		t.Fatalf("expected ErrResumeRejected, got %v", err)
	}
}

func TestClaudeRunMapsAgentProfileDirToClaudeConfigDir(t *testing.T) {
	profileDir := t.TempDir()
	workspace := filepath.Join(profileDir, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	command := testutil.WriteCommand(t, profileDir, "fake-claude-profile",
		"#!/bin/sh\nset -eu\ncat >/dev/null\nprintf '{\"event\":\"turn.completed\",\"session_id\":\"claude-session\"}\\n'\n",
		"@echo off\r\nsetlocal\r\nset /p PROMPT=\r\necho {\"event\":\"turn.completed\",\"session_id\":\"claude-session\"}\r\n",
	)
	cfg := agentadaptor.ClaudeConfig{
		CommonConfig: agentadaptor.CommonConfig{
			Command:         command,
			CWD:             workspace,
			AgentProfileDir: profileDir,
		},
		Model: "claude-sonnet-4",
	}

	events := &testutil.EventRecorder{}
	_, err := NewAdapter().Run(context.Background(), agentadaptor.DriverRunRequest{
		Prompt:    "hello from claude",
		Config:    cfg,
		Workspace: agentadaptor.WorkspaceLease{ID: "workspace-a", CWD: workspace},
	}, events)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	assertInvocationEnvKeysContain(t, events.Snapshot(), "CLAUDE_CONFIG_DIR")
}

func TestClaudeRunOmitsAnthropicModelFlagInBedrockMode(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	command := testutil.WriteCommand(t, home, "fake-claude-bedrock",
		"#!/bin/sh\nset -eu\ncat >/dev/null\nprintf '{\"event\":\"turn.completed\",\"session_id\":\"claude-session\"}\\n'\n",
		"@echo off\r\nsetlocal\r\nset /p PROMPT=\r\necho {\"event\":\"turn.completed\",\"session_id\":\"claude-session\"}\r\n",
	)
	cfg := agentadaptor.ClaudeConfig{
		CommonConfig: agentadaptor.CommonConfig{
			Command: command,
			CWD:     workspace,
			Env: []agentadaptor.EnvBinding{
				{Name: "HOME", Value: home},
				{Name: "USERPROFILE", Value: home},
				{Name: "CLAUDE_CODE_USE_BEDROCK", Value: "true"},
			},
		},
		Model: "claude-sonnet-4",
	}

	events := &testutil.EventRecorder{}
	_, err := NewAdapter().Run(context.Background(), agentadaptor.DriverRunRequest{
		Prompt:    "hello from claude",
		Config:    cfg,
		Workspace: agentadaptor.WorkspaceLease{ID: "workspace-a", CWD: workspace},
	}, events)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	assertInvocationArgsDoNotContain(t, events.Snapshot(), "--model", "claude-sonnet-4")
}

func TestClaudeRunPreservesBedrockNativeModelFlag(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	command := testutil.WriteCommand(t, home, "fake-claude-bedrock-native",
		"#!/bin/sh\nset -eu\ncat >/dev/null\nprintf '{\"event\":\"turn.completed\",\"session_id\":\"claude-session\"}\\n'\n",
		"@echo off\r\nsetlocal\r\nset /p PROMPT=\r\necho {\"event\":\"turn.completed\",\"session_id\":\"claude-session\"}\r\n",
	)
	cfg := agentadaptor.ClaudeConfig{
		CommonConfig: agentadaptor.CommonConfig{
			Command: command,
			CWD:     workspace,
			Env: []agentadaptor.EnvBinding{
				{Name: "HOME", Value: home},
				{Name: "USERPROFILE", Value: home},
				{Name: "CLAUDE_CODE_USE_BEDROCK", Value: "true"},
			},
		},
		Model: "us.anthropic.claude-sonnet-4-5-20250929-v2:0",
	}

	events := &testutil.EventRecorder{}
	result, err := NewAdapter().Run(context.Background(), agentadaptor.DriverRunRequest{
		Prompt:    "hello from claude",
		Config:    cfg,
		Workspace: agentadaptor.WorkspaceLease{ID: "workspace-a", CWD: workspace},
	}, events)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Model != "us.anthropic.claude-sonnet-4-5-20250929-v2:0" {
		t.Fatalf("unexpected model: %#v", result)
	}
	assertInvocationArgsContain(t, events.Snapshot(), "--model", "us.anthropic.claude-sonnet-4-5-20250929-v2:0")
}

func assertHasInvocationAndSpawn(t *testing.T, events []agentadaptor.RunEvent) {
	t.Helper()

	hasInvocation := false
	hasSpawn := false
	hasTranscript := false
	for _, event := range events {
		if event.Type == agentadaptor.RunEventInvocation {
			hasInvocation = true
		}
		if event.Type == agentadaptor.RunEventSpawn {
			hasSpawn = true
		}
		if (event.Type == agentadaptor.RunEventStdout || event.Type == agentadaptor.RunEventStderr) && event.Data["transcript"] != nil {
			hasTranscript = true
		}
	}
	if !hasInvocation || !hasSpawn || !hasTranscript {
		t.Fatalf("expected invocation and spawn events, got %#v", events)
	}
}

func assertInvocationArgsContain(t *testing.T, events []agentadaptor.RunEvent, expected ...string) {
	t.Helper()
	args := invocationArgs(t, events)
	if !containsSubsequence(args, expected) {
		t.Fatalf("expected invocation args %v to contain %v", args, expected)
	}
}

func assertInvocationArgsDoNotContain(t *testing.T, events []agentadaptor.RunEvent, forbidden ...string) {
	t.Helper()
	args := invocationArgs(t, events)
	if containsSubsequence(args, forbidden) {
		t.Fatalf("expected invocation args %v not to contain %v", args, forbidden)
	}
}

func invocationArgs(t *testing.T, events []agentadaptor.RunEvent) []string {
	t.Helper()
	for _, event := range events {
		if event.Type != agentadaptor.RunEventInvocation {
			continue
		}
		args, ok := event.Data["args"].([]string)
		if !ok {
			t.Fatalf("unexpected invocation args payload: %#v", event.Data["args"])
		}
		return args
	}
	t.Fatalf("missing invocation event in %#v", events)
	return nil
}

func assertInvocationEnvKeysContain(t *testing.T, events []agentadaptor.RunEvent, expected string) {
	t.Helper()
	for _, event := range events {
		if event.Type != agentadaptor.RunEventInvocation {
			continue
		}
		keys, ok := event.Data["env_keys"].([]string)
		if !ok {
			t.Fatalf("unexpected env_keys payload: %#v", event.Data["env_keys"])
		}
		for _, key := range keys {
			if key == expected {
				return
			}
		}
		t.Fatalf("expected env_keys %v to contain %q", keys, expected)
	}
	t.Fatalf("missing invocation event in %#v", events)
}

func containsSubsequence(args []string, expected []string) bool {
	if len(expected) == 0 {
		return true
	}
	for i := 0; i+len(expected) <= len(args); i++ {
		match := true
		for j := range expected {
			if args[i+j] != expected[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
