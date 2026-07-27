package codex

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
	"github.com/agent-dance/agent-adaptor/internal/testutil"
)

func TestCodexRunPreservesAndGuardsSessionState(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	command := testutil.WriteCommand(t, home, "fake-codex",
		"#!/bin/sh\nset -eu\nprompt=$(cat)\nprintf 'stderr:%s\\n' \"$prompt\" >&2\nprintf '{\"type\":\"thread.started\",\"thread_id\":\"codex-session\"}\\n'\nprintf '{\"type\":\"turn.completed\"}\\n'\n",
		"@echo off\r\nsetlocal\r\nset /p PROMPT=\r\n>&2 echo stderr:%PROMPT%\r\necho {\"type\":\"thread.started\",\"thread_id\":\"codex-session\"}\r\necho {\"type\":\"turn.completed\"}\r\n",
	)

	cfg := Config{
		CommonConfig: CommonConfig{
			Command: command,
			CWD:     workspace,
			Env: []driver.EnvBinding{
				{Name: "HOME", Value: home},
				{Name: "USERPROFILE", Value: home},
				{Name: "CODEX_HOME", Value: filepath.Join(home, ".codex")},
			},
		},
		Model: "gpt-5.4",
	}
	req := driver.Request{
		Prompt:    "hello from codex",
		Config:    cfg,
		Workspace: driver.WorkspaceLease{ID: "workspace-a", CWD: workspace},
	}

	events := &testutil.EventRecorder{}
	first, err := (adapter{}).Run(context.Background(), req, events)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if first.Checkpoint == nil || first.Checkpoint.State == nil || !first.Checkpoint.Valid {
		t.Fatalf("expected valid checkpoint, got %#v", first.Checkpoint)
	}
	if first.Checkpoint.State.Data[driver.SessionParamCWD] != workspace {
		t.Fatalf("expected cwd session param, got %#v", first.Checkpoint.State.Data)
	}
	if first.Checkpoint.State.Data[driver.SessionParamWorkspaceID] != "workspace-a" {
		t.Fatalf("expected workspace guard, got %#v", first.Checkpoint.State.Data)
	}
	if len(first.Transcript) == 0 {
		t.Fatalf("expected transcript items, got %#v", first.Transcript)
	}
	if first.RawStreams == nil || !strings.Contains(first.RawStreams.Stdout, "turn.completed") {
		t.Fatalf("expected raw stdout to be captured, got %#v", first.RawStreams)
	}
	assertHasInvocationAndSpawn(t, events.Snapshot())

	continueReq := req
	continueReq.Session = &driver.SessionContext{State: first.Checkpoint.State}
	if _, err := (adapter{}).Run(context.Background(), continueReq, &testutil.EventRecorder{}); err != nil {
		t.Fatalf("resume run: %v", err)
	}

	rejectReq := req
	rejectReq.Workspace = driver.WorkspaceLease{ID: "workspace-b", CWD: workspace}
	rejectReq.Session = &driver.SessionContext{State: first.Checkpoint.State}
	_, err = (adapter{}).Run(context.Background(), rejectReq, &testutil.EventRecorder{})
	if !errors.Is(err, engine.ErrResumeRejected) {
		t.Fatalf("expected ErrResumeRejected, got %v", err)
	}
}

func TestCodexRunMapsDedicatedProfileOptionToCodexHome(t *testing.T) {
	profileDir := t.TempDir()
	workspace := filepath.Join(profileDir, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	command := testutil.WriteCommand(t, profileDir, "fake-codex-profile",
		"#!/bin/sh\nset -eu\ncat >/dev/null\nprintf '{\"type\":\"session.updated\",\"session_id\":\"codex-session\"}\\n'\n",
		"@echo off\r\nsetlocal\r\nset /p PROMPT=\r\necho {\"type\":\"session.updated\",\"session_id\":\"codex-session\"}\r\n",
	)

	cfg := Config{
		CommonConfig: CommonConfig{
			Command: command,
			CWD:     workspace,
		},
		Model: "gpt-5.4",
	}

	events := &testutil.EventRecorder{}
	_, err := (adapter{}).Run(context.Background(), driver.Request{
		Prompt:    "hello from codex",
		Config:    cfg,
		Profile:   (&driver.ProfileSelection{Mode: driver.ProfileModeDedicated, Dir: profileDir}),
		Workspace: driver.WorkspaceLease{ID: "workspace-a", CWD: workspace},
	}, events)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	assertInvocationEnvKeysContain(t, events.Snapshot(), "CODEX_HOME")
}

func TestCodexResumeProfileMismatchDoesNotWriteProfileResources(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(map[bool]string{false: "exec", true: "streaming"}[streaming], func(t *testing.T) {
			home := t.TempDir()
			workspace := filepath.Join(home, "workspace")
			codexHome := filepath.Join(home, ".codex")
			if err := os.MkdirAll(workspace, 0o755); err != nil {
				t.Fatalf("mkdir workspace: %v", err)
			}
			skillDir := createSkillDir(t, filepath.Join(home, "source"), "analysis")
			command := testutil.WriteCommand(t, home, "fake-codex-no-write",
				"#!/bin/sh\nset -eu\ncat >/dev/null\nprintf '{\"type\":\"session.updated\",\"session_id\":\"codex-session\"}\\n'\n",
				"@echo off\r\nsetlocal\r\nset /p PROMPT=\r\necho {\"type\":\"session.updated\",\"session_id\":\"codex-session\"}\r\n",
			)
			req := driver.Request{
				Prompt:    "hello",
				Config:    Config{CommonConfig: CommonConfig{Command: command, CWD: workspace, Env: []driver.EnvBinding{{Name: "HOME", Value: home}, {Name: "USERPROFILE", Value: home}, {Name: "CODEX_HOME", Value: codexHome}}}},
				Workspace: driver.WorkspaceLease{ID: "workspace-a", CWD: workspace},
				Skills: driver.ResolvedSkills{Entries: []driver.ResolvedSkill{
					{Key: "analysis", RuntimeName: "analysis", SourcePath: skillDir},
				}},
				MCP: driver.MCPPayload{Servers: []driver.MCPServerSpec{{
					Key:       "local",
					Transport: driver.MCPTransportStdio,
					Command:   "echo",
				}}},
				ProfilePayload: driver.ProfilePayload{Fingerprint: "new-profile"},
				Session: &driver.SessionContext{State: &driver.SessionState{
					ResumeID: "codex-session",
					Data: map[string]string{
						driver.SessionParamCWD:                workspace,
						driver.SessionParamWorkspaceID:        "workspace-a",
						driver.SessionParamProfileFingerprint: "old-profile",
					},
				}},
				Streaming: streaming,
			}
			_, err := (adapter{}).Run(context.Background(), req, &testutil.EventRecorder{})
			if !errors.Is(err, engine.ErrResumeRejected) {
				t.Fatalf("expected ErrResumeRejected, got %v", err)
			}
			if _, err := os.Stat(filepath.Join(codexHome, "skills")); !os.IsNotExist(err) {
				t.Fatalf("expected CODEX_HOME/skills not to be written, err=%v", err)
			}
			if _, err := os.Stat(filepath.Join(codexHome, "config.toml")); !os.IsNotExist(err) {
				t.Fatalf("expected CODEX_HOME/config.toml not to be written, err=%v", err)
			}
		})
	}
}

func TestCodexResumeProfileMismatchDoesNotInitializeDedicatedProfile(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	profileDir := filepath.Join(home, "dedicated-codex")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	command := testutil.WriteCommand(t, home, "fake-codex-dedicated-no-init",
		"#!/bin/sh\nset -eu\ncat >/dev/null\nprintf '{\"type\":\"session.updated\",\"session_id\":\"codex-session\"}\\n'\n",
		"@echo off\r\nsetlocal\r\nset /p PROMPT=\r\necho {\"type\":\"session.updated\",\"session_id\":\"codex-session\"}\r\n",
	)
	req := driver.Request{
		Prompt:    "hello",
		Config:    Config{CommonConfig: CommonConfig{Command: command, CWD: workspace, Env: []driver.EnvBinding{{Name: "HOME", Value: home}, {Name: "USERPROFILE", Value: home}}}},
		Profile:   &driver.ProfileSelection{Mode: driver.ProfileModeDedicated, Dir: profileDir},
		Workspace: driver.WorkspaceLease{ID: "workspace-a", CWD: workspace},
		ProfilePayload: driver.ProfilePayload{
			Fingerprint: "new-profile",
		},
		Session: &driver.SessionContext{State: &driver.SessionState{
			ResumeID: "codex-session",
			Data: map[string]string{
				driver.SessionParamCWD:                workspace,
				driver.SessionParamWorkspaceID:        "workspace-a",
				driver.SessionParamProfileFingerprint: "old-profile",
			},
		}},
	}
	_, err := (adapter{}).Run(context.Background(), req, &testutil.EventRecorder{})
	if !errors.Is(err, engine.ErrResumeRejected) {
		t.Fatalf("expected ErrResumeRejected, got %v", err)
	}
	if _, err := os.Stat(profileDir); !os.IsNotExist(err) {
		t.Fatalf("expected dedicated profile not to be initialized, err=%v", err)
	}
}

func assertHasInvocationAndSpawn(t *testing.T, events []driver.RunEvent) {
	t.Helper()

	hasInvocation := false
	hasSpawn := false
	hasChunk := false
	hasItem := false
	for _, event := range events {
		switch event.Type {
		case driver.RunEventInvocation:
			hasInvocation = true
		case driver.RunEventSpawn:
			hasSpawn = true
		case driver.RunEventChunk:
			hasChunk = true
		case driver.RunEventItem:
			hasItem = true
		}
	}
	if !hasInvocation || !hasSpawn || !hasChunk || !hasItem {
		t.Fatalf("expected invocation/spawn/chunk/item events, got %#v", events)
	}
}

func assertInvocationEnvKeysContain(t *testing.T, events []driver.RunEvent, expected string) {
	t.Helper()
	for _, event := range events {
		if event.Type != driver.RunEventInvocation {
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
