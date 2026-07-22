package codebuddy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/internal/testutil"
)

// fakeHeadlessCLI writes a stub `codebuddy` executable that drains stdin and
// emits a fixed stream-json transcript, letting the headless engine run end to
// end without the real CLI.
func fakeHeadlessCLI(t *testing.T, dir string) string {
	t.Helper()
	return testutil.WriteCommand(t, dir, "fake-codebuddy",
		"#!/bin/sh\nset -eu\ncat >/dev/null 2>&1 || true\n"+
			"printf '%s\\n' '{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"cb-sess\",\"model\":\"claude-haiku-4.5\"}'\n"+
			"printf '%s\\n' '{\"type\":\"assistant\",\"session_id\":\"cb-sess\",\"message\":{\"id\":\"m1\",\"content\":[{\"type\":\"text\",\"text\":\"HELLO\"}],\"model\":\"claude-haiku-4.5\",\"role\":\"assistant\"}}'\n"+
			"printf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":\"HELLO\",\"session_id\":\"cb-sess\",\"num_turns\":1,\"total_cost_usd\":0,\"usage\":{\"input_tokens\":10,\"output_tokens\":2}}'\n",
		"@echo off\r\nsetlocal\r\nset /p X=\r\n"+
			"echo {\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"cb-sess\",\"model\":\"claude-haiku-4.5\"}\r\n"+
			"echo {\"type\":\"assistant\",\"session_id\":\"cb-sess\",\"message\":{\"id\":\"m1\",\"content\":[{\"type\":\"text\",\"text\":\"HELLO\"}],\"model\":\"claude-haiku-4.5\",\"role\":\"assistant\"}}\r\n"+
			"echo {\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":\"HELLO\",\"session_id\":\"cb-sess\",\"num_turns\":1,\"total_cost_usd\":0,\"usage\":{\"input_tokens\":10,\"output_tokens\":2}}\r\n",
	)
}

func autoApprovePolicy() agentadaptor.RunPolicy {
	return agentadaptor.RunPolicy{HumanDecision: agentadaptor.HumanDecisionPolicy{
		Permission: agentadaptor.HumanDecisionAutoApprove,
		PlanReview: agentadaptor.HumanDecisionAutoApprove,
	}}
}

func TestCodeBuddyHeadlessRunProducesCheckpointAndArgs(t *testing.T) {
	t.Setenv("CODEBUDDY_CONFIG_DIR", "")
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	command := fakeHeadlessCLI(t, home)

	cfg := agentadaptor.CodeBuddyConfig{
		CommonConfig: agentadaptor.CommonConfig{
			Command: command,
			CWD:     workspace,
			Env: []agentadaptor.EnvBinding{
				{Name: "HOME", Value: home},
				{Name: "USERPROFILE", Value: home},
			},
		},
		Model: "claude-sonnet-5",
	}
	req := agentadaptor.DriverRunRequest{
		RunID:          "run-headless-1",
		Prompt:         "hello from codebuddy",
		Config:         cfg,
		Workspace:      agentadaptor.WorkspaceLease{ID: "workspace-a", CWD: workspace},
		Policy:         autoApprovePolicy(),
		ProfilePayload: agentadaptor.ProfilePayload{Fingerprint: "profile-a"},
	}

	events := &testutil.EventRecorder{}
	res, err := NewAdapter().Run(context.Background(), req, events)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if res.Output != "HELLO" {
		t.Errorf("output = %q, want HELLO", res.Output)
	}
	if res.Model != "claude-sonnet-5" {
		t.Errorf("reported model = %q, want claude-sonnet-5", res.Model)
	}
	if res.Usage == nil || res.Usage.InputTokens != 10 || res.Usage.OutputTokens != 2 {
		t.Errorf("usage = %+v", res.Usage)
	}
	if res.Checkpoint == nil || res.Checkpoint.State == nil || !res.Checkpoint.Valid {
		t.Fatalf("expected valid checkpoint, got %#v", res.Checkpoint)
	}
	data := res.Checkpoint.State.Data
	if res.Checkpoint.State.ResumeID != "cb-sess" {
		t.Errorf("resume id = %q, want cb-sess", res.Checkpoint.State.ResumeID)
	}
	if data[agentadaptor.SessionParamCWD] != workspace ||
		data[agentadaptor.SessionParamWorkspaceID] != "workspace-a" ||
		data[agentadaptor.SessionParamProfileFingerprint] != "profile-a" {
		t.Errorf("checkpoint guard data = %#v", data)
	}
	if res.RawStreams == nil || !strings.Contains(res.RawStreams.Stdout, "turn") && !strings.Contains(res.RawStreams.Stdout, "result") {
		t.Errorf("raw stdout not captured: %#v", res.RawStreams)
	}
	if len(res.Transcript) == 0 {
		t.Errorf("expected transcript items")
	}

	args := invocationArgs(t, events.Snapshot())
	assertArgsContain(t, args, "--print")
	assertArgsContain(t, args, "--output-format", "stream-json")
	assertArgsContain(t, args, "--permission-mode", "bypassPermissions")
	assertArgsContain(t, args, "--model", "claude-sonnet-5")
	if args[len(args)-1] != "hello from codebuddy" {
		t.Errorf("prompt should be the trailing positional arg, got %v", args)
	}
	assertHasSpawnAndChunk(t, events.Snapshot())
}

func TestCodeBuddyHeadlessResumeAndGuard(t *testing.T) {
	t.Setenv("CODEBUDDY_CONFIG_DIR", "")
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	command := fakeHeadlessCLI(t, home)
	cfg := agentadaptor.CodeBuddyConfig{
		CommonConfig: agentadaptor.CommonConfig{
			Command: command,
			CWD:     workspace,
			Env:     []agentadaptor.EnvBinding{{Name: "HOME", Value: home}, {Name: "USERPROFILE", Value: home}},
		},
		Model: "claude-sonnet-5",
	}
	base := agentadaptor.DriverRunRequest{
		Prompt:         "hi",
		Config:         cfg,
		Workspace:      agentadaptor.WorkspaceLease{ID: "workspace-a", CWD: workspace},
		Policy:         autoApprovePolicy(),
		ProfilePayload: agentadaptor.ProfilePayload{Fingerprint: "profile-a"},
	}

	first, err := NewAdapter().Run(context.Background(), base, &testutil.EventRecorder{})
	if err != nil {
		t.Fatalf("first run: %v", err)
	}

	// Matching resume: passes the guard and forwards --resume <session>.
	resume := base
	resume.Session = &agentadaptor.DriverSessionContext{State: first.Checkpoint.State}
	events := &testutil.EventRecorder{}
	if _, err := NewAdapter().Run(context.Background(), resume, events); err != nil {
		t.Fatalf("resume run: %v", err)
	}
	assertArgsContain(t, invocationArgs(t, events.Snapshot()), "--resume", "cb-sess")

	// Profile fingerprint mismatch: resume is rejected.
	reject := base
	reject.ProfilePayload = agentadaptor.ProfilePayload{Fingerprint: "profile-b"}
	reject.Session = &agentadaptor.DriverSessionContext{State: first.Checkpoint.State}
	if _, err := NewAdapter().Run(context.Background(), reject, &testutil.EventRecorder{}); !errors.Is(err, agentadaptor.ErrResumeRejected) {
		t.Fatalf("expected ErrResumeRejected, got %v", err)
	}
}

func TestCodeBuddyHeadlessStructuredOutput(t *testing.T) {
	t.Setenv("CODEBUDDY_CONFIG_DIR", "")
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	command := testutil.WriteCommand(t, home, "fake-codebuddy-json",
		"#!/bin/sh\nset -eu\ncat >/dev/null 2>&1 || true\n"+
			"printf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":\"{\\\"answer\\\":42}\",\"structured_output\":{\"answer\":42},\"session_id\":\"cb-json\",\"usage\":{\"input_tokens\":5,\"output_tokens\":1}}'\n",
		"@echo off\r\nsetlocal\r\nset /p X=\r\n"+
			"echo {\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":\"{\\\"answer\\\":42}\",\"structured_output\":{\"answer\":42},\"session_id\":\"cb-json\",\"usage\":{\"input_tokens\":5,\"output_tokens\":1}}\r\n",
	)

	cfg := agentadaptor.CodeBuddyConfig{
		CommonConfig: agentadaptor.CommonConfig{
			Command: command,
			CWD:     workspace,
			Env:     []agentadaptor.EnvBinding{{Name: "HOME", Value: home}, {Name: "USERPROFILE", Value: home}},
		},
		Model: "claude-sonnet-5",
	}
	req := agentadaptor.DriverRunRequest{
		Prompt:    "give me json",
		Config:    cfg,
		Workspace: agentadaptor.WorkspaceLease{ID: "workspace-a", CWD: workspace},
		Policy:    autoApprovePolicy(),
		OutputSchema: &agentadaptor.OutputSchema{
			Mode:       agentadaptor.StructuredOutputNativeStrict,
			SchemaJSON: []byte(`{"type":"object","properties":{"answer":{"type":"integer"}}}`),
		},
	}

	events := &testutil.EventRecorder{}
	res, err := NewAdapter().Run(context.Background(), req, events)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	args := invocationArgs(t, events.Snapshot())
	assertArgsContain(t, args, "--output-format", "json")
	if !hasArg(args, "--json-schema") {
		t.Errorf("expected --json-schema flag, got %v", args)
	}
	if res.StructuredOutput == nil || !res.StructuredOutput.Valid {
		t.Fatalf("expected valid structured output, got %#v", res.StructuredOutput)
	}
	if !strings.Contains(string(res.StructuredOutput.RawJSON), `"answer"`) {
		t.Errorf("structured output raw = %s", res.StructuredOutput.RawJSON)
	}
}

func TestCodeBuddyHeadlessMaterializesMCPAndSkills(t *testing.T) {
	t.Setenv("CODEBUDDY_CONFIG_DIR", "")
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	skillDir := filepath.Join(home, "source", "analysis")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# Analysis\n"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	command := fakeHeadlessCLI(t, home)

	cfg := agentadaptor.CodeBuddyConfig{
		CommonConfig: agentadaptor.CommonConfig{
			Command: command,
			CWD:     workspace,
			Env:     []agentadaptor.EnvBinding{{Name: "HOME", Value: home}, {Name: "USERPROFILE", Value: home}},
		},
		Model: "claude-sonnet-5",
	}
	req := agentadaptor.DriverRunRequest{
		Prompt:    "hi",
		Config:    cfg,
		Workspace: agentadaptor.WorkspaceLease{ID: "workspace-a", CWD: workspace},
		Policy:    autoApprovePolicy(),
		Skills: agentadaptor.ResolvedSkills{
			Mode: agentadaptor.SkillSyncPersistent,
			Entries: []agentadaptor.ResolvedSkill{
				{Key: "analysis", RuntimeName: "analysis", SourcePath: skillDir},
			},
			Fingerprint: "bundle-a",
		},
		MCP: agentadaptor.MCPPayload{Servers: []agentadaptor.MCPServerSpec{
			{Key: "local", Transport: agentadaptor.MCPTransportStdio, Command: "echo", Args: []string{"hi"}},
			{Key: "remote", Transport: agentadaptor.MCPTransportHTTP, URL: "https://example.com/mcp"},
		}},
	}

	if _, err := NewAdapter().Run(context.Background(), req, &testutil.EventRecorder{}); err != nil {
		t.Fatalf("run: %v", err)
	}

	mcpPath := filepath.Join(home, ".codebuddy", ".mcp.json")
	raw, err := os.ReadFile(mcpPath)
	if err != nil {
		t.Fatalf("expected .mcp.json written to profile dir: %v", err)
	}
	text := string(raw)
	for _, want := range []string{`"mcpServers"`, `"local"`, `"remote"`, `"type": "http"`} {
		if !strings.Contains(text, want) {
			t.Errorf(".mcp.json missing %q, got:\n%s", want, text)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".codebuddy", "skills", "analysis")); err != nil {
		t.Errorf("expected skill linked into CodeBuddy skills home: %v", err)
	}
}

func TestCodeBuddyHeadlessNonZeroExitSurfacesError(t *testing.T) {
	t.Setenv("CODEBUDDY_CONFIG_DIR", "")
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	command := testutil.WriteCommand(t, home, "fake-codebuddy-fail",
		"#!/bin/sh\ncat >/dev/null 2>&1 || true\n"+
			"printf '%s\\n' '{\"type\":\"error\",\"message\":\"boom\"}'\n"+
			">&2 printf 'fatal: boom\\n'\nexit 3\n",
		"@echo off\r\nsetlocal\r\nset /p X=\r\n"+
			"echo {\"type\":\"error\",\"message\":\"boom\"}\r\n"+
			">&2 echo fatal: boom\r\nexit /b 3\r\n",
	)
	cfg := agentadaptor.CodeBuddyConfig{
		CommonConfig: agentadaptor.CommonConfig{
			Command: command,
			CWD:     workspace,
			Env:     []agentadaptor.EnvBinding{{Name: "HOME", Value: home}, {Name: "USERPROFILE", Value: home}},
		},
		Model: "claude-sonnet-5",
	}
	req := agentadaptor.DriverRunRequest{
		Prompt:    "hi",
		Config:    cfg,
		Workspace: agentadaptor.WorkspaceLease{ID: "workspace-a", CWD: workspace},
		Policy:    autoApprovePolicy(),
	}

	res, err := NewAdapter().Run(context.Background(), req, &testutil.EventRecorder{})
	if err != nil {
		t.Fatalf("run should not hard-error on CLI non-zero exit, got %v", err)
	}
	if res.ExitCode != 3 {
		t.Errorf("exit code = %d, want 3", res.ExitCode)
	}
	if res.Failure == nil || !strings.Contains(res.Failure.Message, "boom") {
		t.Errorf("expected agent failure surfaced, got %#v", res.Failure)
	}
}

// --- assertion helpers ------------------------------------------------------

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

func assertArgsContain(t *testing.T, args []string, expected ...string) {
	t.Helper()
	if !containsSubsequence(args, expected) {
		t.Fatalf("expected args %v to contain subsequence %v", args, expected)
	}
}

func containsSubsequence(args, expected []string) bool {
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

func assertHasSpawnAndChunk(t *testing.T, events []agentadaptor.RunEvent) {
	t.Helper()
	var spawn, chunk, item bool
	for _, e := range events {
		switch e.Type {
		case agentadaptor.RunEventSpawn:
			spawn = true
		case agentadaptor.RunEventChunk:
			chunk = true
		case agentadaptor.RunEventItem:
			item = true
		}
	}
	if !spawn || !chunk || !item {
		t.Fatalf("expected spawn/chunk/item events, got %#v", events)
	}
}
