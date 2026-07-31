package cursor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
	"github.com/agent-dance/agent-adaptor/internal/testutil"
)

func TestCursorRunPreservesUnclassifiedProcessOutcome(t *testing.T) {
	t.Setenv("CURSOR_HOME", "")
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
			command := testutil.WriteCommand(t, home, "fake-cursor-outcome", tc.posixBody, tc.windowsBody)
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

func TestCursorExitZeroProtocolFailuresAreClassified(t *testing.T) {
	t.Setenv("CURSOR_HOME", "")
	for _, tc := range []struct {
		name        string
		posixBody   string
		windowsBody string
	}{
		{
			name:        "missing terminal",
			posixBody:   "#!/bin/sh\ncat >/dev/null\nprintf '{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"partial\"}]}}\\n'\n",
			windowsBody: "@echo off\r\nset /p X=\r\necho {\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"partial\"}]}}\r\n",
		},
		{
			name:        "malformed before terminal",
			posixBody:   "#!/bin/sh\ncat >/dev/null\nprintf '{broken\\n'\nprintf '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":\"done\",\"session_id\":\"cursor-session\"}\\n'\n",
			windowsBody: "@echo off\r\nset /p X=\r\necho {broken\r\necho {\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":\"done\",\"session_id\":\"cursor-session\"}\r\n",
		},
		{
			name:        "terminal missing required session id",
			posixBody:   "#!/bin/sh\ncat >/dev/null\nprintf '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":\"done\"}\\n'\n",
			windowsBody: "@echo off\r\nset /p X=\r\necho {\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":\"done\"}\r\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			command := testutil.WriteCommand(t, home, "fake-cursor-protocol", tc.posixBody, tc.windowsBody)
			res, err := (adapter{}).Run(context.Background(), agentadaptor.Request{
				Prompt:    "go",
				Config:    Config{CommonConfig: CommonConfig{Command: command, CWD: home}},
				Workspace: agentadaptor.WorkspaceLease{CWD: home},
			}, &testutil.EventRecorder{})
			if err != nil {
				t.Fatalf("Driver.Run error = %v", err)
			}
			if res.Failure == nil || res.Failure.Code != agentadaptor.FailureAgentError {
				t.Fatalf("Failure = %#v, want protocol agent failure", res.Failure)
			}
			if res.Checkpoint != nil {
				t.Fatalf("Checkpoint = %#v, want nil", res.Checkpoint)
			}
		})
	}
}

func TestCursorForkIsRejectedBeforeProviderLaunch(t *testing.T) {
	t.Setenv("CURSOR_HOME", "")
	home := t.TempDir()
	_, err := (adapter{}).Run(context.Background(), agentadaptor.Request{
		Prompt:    "fork",
		Config:    Config{CommonConfig: CommonConfig{Command: filepath.Join(home, "must-not-run"), CWD: home}},
		Workspace: agentadaptor.WorkspaceLease{ID: "workspace-a", CWD: home},
		Session: &agentadaptor.SessionContext{
			Mode:  agentadaptor.SessionFork,
			State: &agentadaptor.SessionState{ResumeID: "parent-session"},
		},
	}, &testutil.EventRecorder{})
	if !errors.Is(err, engine.ErrResumeRejected) {
		t.Fatalf("error = %v, want safe fork rejection", err)
	}
}

func TestCursorProviderResumeRejectionTriggersContinueOrStartSignal(t *testing.T) {
	t.Setenv("CURSOR_HOME", "")
	home := t.TempDir()
	command := testutil.WriteCommand(t, home, "fake-cursor-resume-reject",
		"#!/bin/sh\ncat >/dev/null\nprintf 'session parent-session not found\\n' >&2\nexit 7\n",
		"@echo off\r\nset /p X=\r\n>&2 echo session parent-session not found\r\nexit /b 7\r\n",
	)
	_, err := (adapter{}).Run(context.Background(), agentadaptor.Request{
		Prompt:    "continue",
		Config:    Config{CommonConfig: CommonConfig{Command: command, CWD: home}},
		Workspace: agentadaptor.WorkspaceLease{ID: "workspace-a", CWD: home},
		Session: &agentadaptor.SessionContext{
			Mode:  agentadaptor.SessionContinueOrStart,
			State: &agentadaptor.SessionState{ResumeID: "parent-session"},
		},
	}, &testutil.EventRecorder{})
	if !errors.Is(err, engine.ErrResumeRejected) {
		t.Fatalf("error = %v, want ErrResumeRejected", err)
	}
}

func TestCursorSafeExtraArgsCannotReplaceResolvedInvocation(t *testing.T) {
	got := cursorSafeExtraArgs([]string{
		"--output-format", "text",
		"--workspace=/other",
		"--resume", "other-session",
		"--model", "other-model",
		"--mode=ask",
		"--print", "--force", "-f", "--yolo",
		"--custom", "kept",
	})
	if strings.Join(got, " ") != "--custom kept" {
		t.Fatalf("safe extra args = %#v, want unrelated args only", got)
	}
}

func TestCursorRunPreservesAndGuardsSessionState(t *testing.T) {
	t.Setenv("CURSOR_HOME", "")
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	command := testutil.WriteCommand(t, home, "fake-cursor",
		"#!/bin/sh\nset -eu\nprompt=$(cat)\nprintf 'stderr:%s\\n' \"$prompt\" >&2\nprintf '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":\"done\",\"session_id\":\"cursor-session\"}\\n'\n",
		"@echo off\r\nsetlocal\r\nset /p PROMPT=\r\n>&2 echo stderr:%PROMPT%\r\necho {\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":\"done\",\"session_id\":\"cursor-session\"}\r\n",
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
		Model: "gpt-5",
	}
	req := agentadaptor.Request{
		Prompt:    "hello from cursor",
		Config:    cfg,
		Workspace: agentadaptor.WorkspaceLease{ID: "workspace-a", CWD: workspace},
		ProfilePayload: agentadaptor.ProfilePayload{
			Fingerprint:                     "concrete-profile-a",
			SessionCompatibilityFingerprint: "session-profile-a",
		},
	}

	events := &testutil.EventRecorder{}
	first, err := adapter{}.Run(context.Background(), req, events)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if first.Checkpoint == nil || first.Checkpoint.State == nil || !first.Checkpoint.Valid {
		t.Fatalf("expected valid checkpoint, got %#v", first.Checkpoint)
	}
	if first.Checkpoint.State.Data[agentadaptor.SessionParamWorkspaceID] != "workspace-a" {
		t.Fatalf("expected workspace guard, got %#v", first.Checkpoint.State.Data)
	}
	if first.Checkpoint.State.Data[agentadaptor.SessionParamProfileFingerprint] != "session-profile-a" {
		t.Fatalf("expected session compatibility guard, got %#v", first.Checkpoint.State.Data)
	}
	if len(first.Transcript) == 0 {
		t.Fatalf("expected transcript items, got %#v", first.Transcript)
	}
	if first.RawStreams == nil || !strings.Contains(first.RawStreams.Stdout, `"subtype":"success"`) {
		t.Fatalf("expected raw stdout to be captured, got %#v", first.RawStreams)
	}
	assertHasInvocationAndSpawn(t, events.Snapshot())

	continueReq := req
	continueReq.ProfilePayload.Fingerprint = "concrete-profile-b"
	continueReq.Session = &agentadaptor.SessionContext{State: first.Checkpoint.State}
	if _, err := (adapter{}).Run(context.Background(), continueReq, &testutil.EventRecorder{}); err != nil {
		t.Fatalf("resume run: %v", err)
	}

	rejectReq := req
	rejectReq.Workspace = agentadaptor.WorkspaceLease{ID: "workspace-b", CWD: workspace}
	rejectReq.Session = &agentadaptor.SessionContext{State: first.Checkpoint.State}
	_, err = (adapter{}).Run(context.Background(), rejectReq, &testutil.EventRecorder{})
	if !errors.Is(err, engine.ErrResumeRejected) {
		t.Fatalf("expected ErrResumeRejected, got %v", err)
	}
}

func TestCursorRunUsesCurrentForceFlagForAutoApprove(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell argv capture in this test is POSIX-only")
	}
	t.Setenv("CURSOR_HOME", "")
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	argsPath := filepath.Join(home, "args.txt")
	command := testutil.WriteCommand(t, home, "fake-cursor-args",
		"#!/bin/sh\nset -eu\nprintf '%s\\n' \"$@\" >"+argsPath+"\ncat >/dev/null\nprintf '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":\"done\",\"session_id\":\"cursor-session\"}\\n'\n",
		"@echo off\r\nsetlocal\r\nset /p PROMPT=\r\necho {\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":\"done\",\"session_id\":\"cursor-session\"}\r\n",
	)

	_, err := (adapter{}).Run(context.Background(), agentadaptor.Request{
		Prompt:    "hello from cursor",
		Config:    Config{CommonConfig: CommonConfig{Command: command, CWD: workspace, Env: []agentadaptor.EnvBinding{{Name: "HOME", Value: home}, {Name: "USERPROFILE", Value: home}}}},
		Workspace: agentadaptor.WorkspaceLease{ID: "workspace-a", CWD: workspace},
		Policy: agentadaptor.RunPolicy{HumanDecision: agentadaptor.HumanDecisionPolicy{
			Permission: agentadaptor.HumanDecisionAutoApprove,
		}},
	}, &testutil.EventRecorder{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	rawArgs, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	args := strings.Split(strings.TrimSpace(string(rawArgs)), "\n")
	if !containsString(args, "--force") {
		t.Fatalf("expected --force in cursor args, got %#v", args)
	}
	if containsString(args, "--yolo") {
		t.Fatalf("did not expect deprecated --yolo in cursor args, got %#v", args)
	}
}

func TestCursorRunMapsDedicatedProfileOptionToCursorHome(t *testing.T) {
	profileDir := t.TempDir()
	workspace := filepath.Join(profileDir, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	command := testutil.WriteCommand(t, profileDir, "fake-cursor-profile",
		"#!/bin/sh\nset -eu\ncat >/dev/null\nprintf '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":\"done\",\"session_id\":\"cursor-session\"}\\n'\n",
		"@echo off\r\nsetlocal\r\nset /p PROMPT=\r\necho {\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":\"done\",\"session_id\":\"cursor-session\"}\r\n",
	)

	cfg := Config{
		CommonConfig: CommonConfig{
			Command: command,
			CWD:     workspace,
		},
		Model: "gpt-5",
	}

	events := &testutil.EventRecorder{}
	_, err := (adapter{}).Run(context.Background(), agentadaptor.Request{
		Prompt:    "hello from cursor",
		Config:    cfg,
		Profile:   (&agentadaptor.ProfileSelection{Mode: agentadaptor.ProfileModeDedicated, Dir: profileDir}),
		Workspace: agentadaptor.WorkspaceLease{ID: "workspace-a", CWD: workspace},
	}, events)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	assertInvocationEnvKeysContain(t, events.Snapshot(), "CURSOR_HOME")
}

func TestCursorResumeProfileMismatchDoesNotWriteProfileResources(t *testing.T) {
	t.Setenv("CURSOR_HOME", "")
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	cursorHome := filepath.Join(home, ".cursor")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	skillDir := createCursorSkillDir(t, filepath.Join(home, "source"), "analysis")
	command := testutil.WriteCommand(t, home, "fake-cursor-no-write",
		"#!/bin/sh\nset -eu\ncat >/dev/null\nprintf '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":\"done\",\"session_id\":\"cursor-session\"}\\n'\n",
		"@echo off\r\nsetlocal\r\nset /p PROMPT=\r\necho {\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":\"done\",\"session_id\":\"cursor-session\"}\r\n",
	)
	req := agentadaptor.Request{
		Prompt:    "hello",
		Config:    Config{CommonConfig: CommonConfig{Command: command, CWD: workspace, Env: []agentadaptor.EnvBinding{{Name: "HOME", Value: home}, {Name: "USERPROFILE", Value: home}, {Name: "CURSOR_HOME", Value: cursorHome}}}},
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
			ResumeID: "cursor-session",
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
	if _, err := os.Stat(filepath.Join(cursorHome, "skills")); !os.IsNotExist(err) {
		t.Fatalf("expected CURSOR_HOME/skills not to be written, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(cursorHome, "mcp.json")); !os.IsNotExist(err) {
		t.Fatalf("expected CURSOR_HOME/mcp.json not to be written, err=%v", err)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
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
