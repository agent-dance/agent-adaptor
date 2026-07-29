package claude

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
	"github.com/agent-dance/agent-adaptor/internal/testutil"
)

func TestClaudeForkRejectsMissingParentBeforeMaterializationOrLaunch(t *testing.T) {
	for _, state := range []*agentadaptor.SessionState{nil, {ResumeID: "   "}} {
		_, err := (adapter{}).Run(context.Background(), agentadaptor.Request{
			Config: Config{CommonConfig: CommonConfig{
				Command: filepath.Join(t.TempDir(), "must-not-run-claude"),
			}},
			Session: &agentadaptor.SessionContext{Mode: agentadaptor.SessionFork, State: state},
			Skills: agentadaptor.ResolvedSkills{Entries: []agentadaptor.ResolvedSkill{{
				Key: "must-not-materialize", SourcePath: filepath.Join(t.TempDir(), "missing-skill"),
			}}},
		}, &testutil.EventRecorder{})
		if !errors.Is(err, engine.ErrResumeRejected) {
			t.Fatalf("fork state %#v: error = %v, want ErrResumeRejected before materialization/launch", state, err)
		}
	}
}

func TestClaudeForkRequiresDistinctChildCheckpoint(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	tests := []struct {
		name        string
		terminal    string
		wantReject  bool
		wantChildID string
	}{
		{
			name:       "missing child session ID",
			terminal:   `{"type":"result","subtype":"success","is_error":false,"result":"done"}`,
			wantReject: true,
		},
		{
			name:       "parent ID reused",
			terminal:   `{"type":"result","subtype":"success","is_error":false,"session_id":"parent-session","result":"done"}`,
			wantReject: true,
		},
		{
			name:        "distinct child ID",
			terminal:    `{"type":"result","subtype":"success","is_error":false,"session_id":"child-session","result":"done"}`,
			wantChildID: "child-session",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			command := testutil.WriteCommand(t, home, "fake-claude-fork-outcome",
				"#!/bin/sh\ncat >/dev/null\nprintf '%s\\n' '"+tc.terminal+"'\n",
				"@echo off\r\nset /p X=\r\necho "+tc.terminal+"\r\n",
			)
			resp, err := (adapter{}).Run(context.Background(), agentadaptor.Request{
				Prompt:    "fork",
				Config:    Config{CommonConfig: CommonConfig{Command: command, CWD: home}},
				Workspace: agentadaptor.WorkspaceLease{ID: "workspace", CWD: home},
				Session: &agentadaptor.SessionContext{
					Mode:  agentadaptor.SessionFork,
					State: &agentadaptor.SessionState{ResumeID: "parent-session"},
				},
			}, &testutil.EventRecorder{})
			if tc.wantReject {
				if !errors.Is(err, engine.ErrResumeRejected) {
					t.Fatalf("error = %v, want ErrResumeRejected", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if resp.Checkpoint == nil || resp.Checkpoint.State == nil || resp.Checkpoint.State.ResumeID != tc.wantChildID {
				t.Fatalf("checkpoint = %#v, want distinct child %q", resp.Checkpoint, tc.wantChildID)
			}
		})
	}
}

func TestClaudeRunPreservesUnclassifiedProcessOutcome(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	for _, tc := range []struct {
		name        string
		posixBody   string
		windowsBody string
		cancel      bool
	}{
		{
			name:        "exit without provider terminal",
			posixBody:   "#!/bin/sh\ncat >/dev/null\nprintf 'partial stdout\\n'\nprintf 'partial stderr\\n' >&2\nexit 7\n",
			windowsBody: "@echo off\r\nset /p X=\r\necho partial stdout\r\n>&2 echo partial stderr\r\nexit /b 7\r\n",
		},
		{
			name:        "cancel before provider terminal",
			posixBody:   "#!/bin/sh\ncat >/dev/null\nprintf 'partial stdout\\n'\nprintf 'partial stderr\\n' >&2\nexec sleep 30\n",
			windowsBody: "@echo off\r\nset /p X=\r\necho partial stdout\r\n>&2 echo partial stderr\r\nping -n 31 127.0.0.1 >nul\r\n",
			cancel:      true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			command := testutil.WriteCommand(t, home, "fake-claude-outcome", tc.posixBody, tc.windowsBody)
			ctx := context.Background()
			var sink agentadaptor.EventSink = &testutil.EventRecorder{}
			if tc.cancel {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
				defer cancel()
				// stderr is written after stdout in the fixture, so observing it
				// proves both raw streams have been produced before cancellation.
				sink = testutil.NewChunkCancelRecorder(cancel, "stderr")
			}
			res, err := (adapter{}).Run(ctx, agentadaptor.Request{
				Prompt:    "go",
				Config:    Config{CommonConfig: CommonConfig{Command: command, CWD: home}},
				Workspace: agentadaptor.WorkspaceLease{CWD: home},
			}, sink)
			if err != nil {
				t.Fatalf("Driver.Run error = %v", err)
			}
			if res.ExitCode == 0 || res.Failure != nil || res.Checkpoint != nil {
				t.Fatalf("outcome = %+v, want abnormal fields with no provider classification/checkpoint", res)
			}
			if tc.cancel && res.TimedOut {
				t.Fatalf("TimedOut = true for explicit cancellation outcome: %+v", res)
			}
			if tc.cancel && !errors.Is(ctx.Err(), context.Canceled) {
				t.Fatalf("context error = %v, want cancellation after observed chunks", ctx.Err())
			}
			if res.RawStreams == nil || !strings.Contains(res.RawStreams.Stdout, "partial stdout") || !strings.Contains(res.RawStreams.Stderr, "partial stderr") {
				t.Fatalf("raw streams = %#v", res.RawStreams)
			}
			if len(res.Transcript) < 2 {
				t.Fatalf("transcript = %#v, want partial stdout/stderr", res.Transcript)
			}
		})
	}
}

func TestClaudeRunCleanProtocolFailuresAreStructured(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	tests := []struct {
		name        string
		posixBody   string
		windowsBody string
	}{
		{
			name:        "empty stdout",
			posixBody:   "#!/bin/sh\ncat >/dev/null\nexit 0\n",
			windowsBody: "@echo off\r\nset /p X=\r\nexit /b 0\r\n",
		},
		{
			name:        "malformed stdout",
			posixBody:   "#!/bin/sh\ncat >/dev/null\nprintf 'not protocol\\n'\n",
			windowsBody: "@echo off\r\nset /p X=\r\necho not protocol\r\n",
		},
		{
			name:        "missing terminal",
			posixBody:   "#!/bin/sh\ncat >/dev/null\nprintf '{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"s\"}\\n'\n",
			windowsBody: "@echo off\r\nset /p X=\r\necho {\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"s\"}\r\n",
		},
		{
			name:        "non-success terminal",
			posixBody:   "#!/bin/sh\ncat >/dev/null\nprintf '{\"type\":\"result\",\"subtype\":\"error\",\"session_id\":\"s\",\"result\":\"provider failed\"}\\n'\n",
			windowsBody: "@echo off\r\nset /p X=\r\necho {\"type\":\"result\",\"subtype\":\"error\",\"session_id\":\"s\",\"result\":\"provider failed\"}\r\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			command := testutil.WriteCommand(t, home, "fake-claude-clean-protocol", tc.posixBody, tc.windowsBody)
			resp, err := (adapter{}).Run(context.Background(), agentadaptor.Request{
				Prompt:    "hello",
				Config:    Config{CommonConfig: CommonConfig{Command: command, CWD: home}},
				Workspace: agentadaptor.WorkspaceLease{ID: "workspace", CWD: home},
			}, &testutil.EventRecorder{})
			if err != nil {
				t.Fatalf("Run infrastructure error: %v", err)
			}
			if resp.Failure == nil || resp.Failure.Code != agentadaptor.FailureAgentError {
				t.Fatalf("Failure = %#v, want agent_error", resp.Failure)
			}
			if resp.Checkpoint != nil {
				t.Fatalf("failed protocol produced checkpoint %#v", resp.Checkpoint)
			}
		})
	}
}

func TestClaudeRunClassifiesProviderResumeRejection(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home := t.TempDir()
	command := testutil.WriteCommand(t, home, "fake-claude-resume-rejected",
		"#!/bin/sh\ncat >/dev/null\nprintf 'No conversation found with session ID: missing-session\\n' >&2\nexit 1\n",
		"@echo off\r\nset /p X=\r\n>&2 echo No conversation found with session ID: missing-session\r\nexit /b 1\r\n",
	)
	base := agentadaptor.Request{
		Prompt:    "continue",
		Config:    Config{CommonConfig: CommonConfig{Command: command, CWD: home}},
		Workspace: agentadaptor.WorkspaceLease{ID: "workspace", CWD: home},
	}
	resumed := base
	resumed.Session = &agentadaptor.SessionContext{
		Mode:  agentadaptor.SessionContinueOnly,
		State: &agentadaptor.SessionState{ResumeID: "missing-session"},
	}
	if _, err := (adapter{}).Run(context.Background(), resumed, &testutil.EventRecorder{}); !errors.Is(err, engine.ErrResumeRejected) {
		t.Fatalf("resume error = %v, want ErrResumeRejected", err)
	}
	if _, err := (adapter{}).Run(context.Background(), base, &testutil.EventRecorder{}); err != nil {
		t.Fatalf("stateless run must not classify stderr as resume rejection: %v", err)
	}
}

func TestClaudeRunPreservesAndGuardsSessionState(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("HOME", t.TempDir())
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
		"#!/bin/sh\nset -eu\nprompt=$(cat)\nprintf 'stderr:%s\\n' \"$prompt\" >&2\nprintf '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"session_id\":\"claude-session\",\"display_id\":\"claude-display\",\"result\":\"done\"}\\n'\n",
		"@echo off\r\nsetlocal\r\nset /p PROMPT=\r\n>&2 echo stderr:%PROMPT%\r\necho {\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"session_id\":\"claude-session\",\"display_id\":\"claude-display\",\"result\":\"done\"}\r\n",
	)

	cfg := Config{
		CommonConfig: CommonConfig{
			Command: command,
			CWD:     workspace,
			Env: []agentadaptor.EnvBinding{
				{Name: "HOME", Value: home},
				{Name: "USERPROFILE", Value: home},
			},
		},
		Model: "claude-sonnet-4",
	}
	payloadA := agentadaptor.ResolvedSkills{
		Mode: agentadaptor.SkillSyncEphemeral,
		Entries: []agentadaptor.ResolvedSkill{
			{Key: "analysis", RuntimeName: "analysis", SourcePath: skillDir},
		},
		Fingerprint: "bundle-a",
	}
	payloadB := agentadaptor.ResolvedSkills{
		Mode: agentadaptor.SkillSyncEphemeral,
		Entries: []agentadaptor.ResolvedSkill{
			{Key: "analysis", RuntimeName: "analysis", SourcePath: skillDir},
		},
		Fingerprint: "bundle-b",
	}
	req := agentadaptor.Request{
		Prompt:         "hello from claude",
		Config:         cfg,
		Workspace:      agentadaptor.WorkspaceLease{ID: "workspace-a", CWD: workspace},
		Skills:         payloadA,
		ProfilePayload: agentadaptor.ProfilePayload{Fingerprint: "profile-a"},
	}

	events := &testutil.EventRecorder{}
	first, err := adapter{}.Run(context.Background(), req, events)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if first.Checkpoint == nil || first.Checkpoint.State == nil || !first.Checkpoint.Valid {
		t.Fatalf("expected valid checkpoint, got %#v", first.Checkpoint)
	}
	if first.Checkpoint.State.Data[agentadaptor.SessionParamProfileFingerprint] != "profile-a" {
		t.Fatalf("expected profile guard, got %#v", first.Checkpoint.State.Data)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills", "analysis")); err != nil {
		t.Fatalf("expected selected skill in Claude profile-local skills home: %v", err)
	}
	if len(first.Transcript) == 0 {
		t.Fatalf("expected transcript items, got %#v", first.Transcript)
	}
	if first.RawStreams == nil || !strings.Contains(first.RawStreams.Stdout, `"subtype":"success"`) {
		t.Fatalf("expected raw stdout to be captured, got %#v", first.RawStreams)
	}
	assertHasInvocationAndSpawn(t, events.Snapshot())
	assertInvocationArgsDoNotContain(t, events.Snapshot(), "--add-dir")

	continueReq := req
	continueReq.Session = &agentadaptor.SessionContext{State: first.Checkpoint.State}
	if _, err := (adapter{}).Run(context.Background(), continueReq, &testutil.EventRecorder{}); err != nil {
		t.Fatalf("resume run: %v", err)
	}

	rejectReq := req
	rejectReq.Skills = payloadB
	rejectReq.ProfilePayload = agentadaptor.ProfilePayload{Fingerprint: "profile-b"}
	rejectReq.Session = &agentadaptor.SessionContext{State: first.Checkpoint.State}
	_, err = (adapter{}).Run(context.Background(), rejectReq, &testutil.EventRecorder{})
	if !errors.Is(err, engine.ErrResumeRejected) {
		t.Fatalf("expected ErrResumeRejected, got %v", err)
	}

}

func TestClaudeRunMapsDedicatedProfileOptionToClaudeConfigDir(t *testing.T) {
	profileDir := t.TempDir()
	workspace := filepath.Join(profileDir, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	command := testutil.WriteCommand(t, profileDir, "fake-claude-profile",
		"#!/bin/sh\nset -eu\ncat >/dev/null\nprintf '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"session_id\":\"claude-session\",\"result\":\"done\"}\\n'\n",
		"@echo off\r\nsetlocal\r\nset /p PROMPT=\r\necho {\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"session_id\":\"claude-session\",\"result\":\"done\"}\r\n",
	)
	cfg := Config{
		CommonConfig: CommonConfig{
			Command: command,
			CWD:     workspace,
		},
		Model: "claude-sonnet-4",
	}

	events := &testutil.EventRecorder{}
	_, err := (adapter{}).Run(context.Background(), agentadaptor.Request{
		Prompt:    "hello from claude",
		Config:    cfg,
		Profile:   (&agentadaptor.ProfileSelection{Mode: agentadaptor.ProfileModeDedicated, Dir: profileDir}),
		Workspace: agentadaptor.WorkspaceLease{ID: "workspace-a", CWD: workspace},
	}, events)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	assertInvocationEnvKeysContain(t, events.Snapshot(), "CLAUDE_CONFIG_DIR")
}

func TestClaudeResumeProfileMismatchDoesNotWriteProfileResources(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	configDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	skillDir := createClaudeSkillDir(t, filepath.Join(home, "source"), "analysis")
	command := testutil.WriteCommand(t, home, "fake-claude-no-write",
		"#!/bin/sh\nset -eu\ncat >/dev/null\nprintf '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"session_id\":\"claude-session\",\"result\":\"done\"}\\n'\n",
		"@echo off\r\nsetlocal\r\nset /p PROMPT=\r\necho {\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"session_id\":\"claude-session\",\"result\":\"done\"}\r\n",
	)
	req := agentadaptor.Request{
		Prompt:    "hello",
		Config:    Config{CommonConfig: CommonConfig{Command: command, CWD: workspace, Env: []agentadaptor.EnvBinding{{Name: "HOME", Value: home}, {Name: "USERPROFILE", Value: home}, {Name: "CLAUDE_CONFIG_DIR", Value: configDir}}}},
		Workspace: agentadaptor.WorkspaceLease{ID: "workspace-a", CWD: workspace},
		Skills: agentadaptor.ResolvedSkills{Entries: []agentadaptor.ResolvedSkill{
			{Key: "analysis", RuntimeName: "analysis", SourcePath: skillDir},
		}},
		MCP: agentadaptor.MCPPayload{Servers: []agentadaptor.MCPServerSpec{{
			Key:       "local",
			Transport: agentadaptor.MCPTransportStdio,
			Command:   "echo",
		}}},
		ProfilePayload: agentadaptor.ProfilePayload{Fingerprint: "new-profile"},
		Session: &agentadaptor.SessionContext{State: &agentadaptor.SessionState{
			ResumeID: "claude-session",
			Data: map[string]string{
				agentadaptor.SessionParamCWD:                workspace,
				agentadaptor.SessionParamWorkspaceID:        "workspace-a",
				agentadaptor.SessionParamProfileFingerprint: "old-profile",
			},
		}},
	}
	_, err := (adapter{}).Run(context.Background(), req, &testutil.EventRecorder{})
	if !errors.Is(err, engine.ErrResumeRejected) {
		t.Fatalf("expected ErrResumeRejected, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(configDir, "skills")); !os.IsNotExist(err) {
		t.Fatalf("expected CLAUDE_CONFIG_DIR/skills not to be written, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(configDir, ".claude.json")); !os.IsNotExist(err) {
		t.Fatalf("expected Claude MCP config not to be written, err=%v", err)
	}
}

func TestClaudeRunModelOverrideReflectedInReportedModel(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	command := testutil.WriteCommand(t, home, "fake-claude-model",
		"#!/bin/sh\nset -eu\ncat >/dev/null\nprintf '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"session_id\":\"claude-session\",\"result\":\"done\"}\\n'\n",
		"@echo off\r\nsetlocal\r\nset /p PROMPT=\r\necho {\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"session_id\":\"claude-session\",\"result\":\"done\"}\r\n",
	)
	newReq := func(override string) agentadaptor.Request {
		return agentadaptor.Request{
			Prompt: "hello",
			Config: Config{
				CommonConfig: CommonConfig{
					Command: command,
					CWD:     workspace,
					Env:     []agentadaptor.EnvBinding{{Name: "HOME", Value: home}, {Name: "USERPROFILE", Value: home}},
				},
				Model: "claude-sonnet-4-6",
			},
			Workspace:     agentadaptor.WorkspaceLease{ID: "workspace-a", CWD: workspace},
			ModelOverride: override,
		}
	}

	// per-run override must drive both the --model flag and the reported model.
	overridden, err := (adapter{}).Run(context.Background(), newReq("claude-opus-4-1"), &testutil.EventRecorder{})
	if err != nil {
		t.Fatalf("run with override: %v", err)
	}
	if overridden.Model != "claude-opus-4-1" {
		t.Fatalf("reported Model = %q, want per-run override claude-opus-4-1", overridden.Model)
	}

	// blank override falls back to the binding model.
	fallback, err := (adapter{}).Run(context.Background(), newReq("   "), &testutil.EventRecorder{})
	if err != nil {
		t.Fatalf("run without override: %v", err)
	}
	if fallback.Model != "claude-sonnet-4-6" {
		t.Fatalf("reported Model = %q, want binding model claude-sonnet-4-6 when override is blank", fallback.Model)
	}
}

func TestClaudeRunOmitsAnthropicModelFlagInBedrockMode(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	command := testutil.WriteCommand(t, home, "fake-claude-bedrock",
		"#!/bin/sh\nset -eu\ncat >/dev/null\nprintf '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"session_id\":\"claude-session\",\"result\":\"done\"}\\n'\n",
		"@echo off\r\nsetlocal\r\nset /p PROMPT=\r\necho {\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"session_id\":\"claude-session\",\"result\":\"done\"}\r\n",
	)
	cfg := Config{
		CommonConfig: CommonConfig{
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
	_, err := (adapter{}).Run(context.Background(), agentadaptor.Request{
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
		"#!/bin/sh\nset -eu\ncat >/dev/null\nprintf '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"session_id\":\"claude-session\",\"result\":\"done\"}\\n'\n",
		"@echo off\r\nsetlocal\r\nset /p PROMPT=\r\necho {\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"session_id\":\"claude-session\",\"result\":\"done\"}\r\n",
	)
	cfg := Config{
		CommonConfig: CommonConfig{
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
	result, err := (adapter{}).Run(context.Background(), agentadaptor.Request{
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
	hasChunk := false
	hasItem := false
	for _, event := range events {
		switch event.Type {
		case agentadaptor.RunEventInvocation:
			hasInvocation = true
		case agentadaptor.RunEventSpawn:
			hasSpawn = true
		case agentadaptor.RunEventChunk:
			hasChunk = true
		case agentadaptor.RunEventItem:
			hasItem = true
		}
	}
	if !hasInvocation || !hasSpawn || !hasChunk || !hasItem {
		t.Fatalf("expected invocation/spawn/chunk/item events, got %#v", events)
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
