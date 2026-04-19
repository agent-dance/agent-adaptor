package cursor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/internal/testutil"
)

func TestCursorRunPreservesAndGuardsSessionState(t *testing.T) {
	t.Setenv("CURSOR_HOME", "")
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	command := testutil.WriteCommand(t, home, "fake-cursor",
		"#!/bin/sh\nset -eu\nprompt=$(cat)\nprintf 'stderr:%s\\n' \"$prompt\" >&2\nprintf '{\"type\":\"run.completed\",\"session_id\":\"cursor-session\",\"display_id\":\"cursor-display\"}\\n'\n",
		"@echo off\r\nsetlocal\r\nset /p PROMPT=\r\n>&2 echo stderr:%PROMPT%\r\necho {\"type\":\"run.completed\",\"session_id\":\"cursor-session\",\"display_id\":\"cursor-display\"}\r\n",
	)

	cfg := agentadaptor.CursorConfig{
		CommonConfig: agentadaptor.CommonConfig{
			Command: command,
			CWD:     workspace,
			Env: []agentadaptor.EnvBinding{
				{Name: "HOME", Value: home},
				{Name: "USERPROFILE", Value: home},
			},
		},
		Model: "gpt-5",
	}
	req := agentadaptor.DriverRunRequest{
		Prompt:    "hello from cursor",
		Config:    cfg,
		Workspace: agentadaptor.WorkspaceLease{ID: "workspace-a", CWD: workspace},
	}

	events := &testutil.EventRecorder{}
	first, err := NewAdapter().Run(context.Background(), req, events)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if first.Checkpoint == nil || first.Checkpoint.State == nil || !first.Checkpoint.Valid {
		t.Fatalf("expected valid checkpoint, got %#v", first.Checkpoint)
	}
	if first.Checkpoint.State.Data[agentadaptor.SessionParamWorkspaceID] != "workspace-a" {
		t.Fatalf("expected workspace guard, got %#v", first.Checkpoint.State.Data)
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
	rejectReq.Workspace = agentadaptor.WorkspaceLease{ID: "workspace-b", CWD: workspace}
	rejectReq.Session = &agentadaptor.DriverSessionContext{State: first.Checkpoint.State}
	_, err = NewAdapter().Run(context.Background(), rejectReq, &testutil.EventRecorder{})
	if !errors.Is(err, agentadaptor.ErrResumeRejected) {
		t.Fatalf("expected ErrResumeRejected, got %v", err)
	}
}

func TestCursorRunMapsAgentProfileDirToCursorHome(t *testing.T) {
	profileDir := t.TempDir()
	workspace := filepath.Join(profileDir, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	command := testutil.WriteCommand(t, profileDir, "fake-cursor-profile",
		"#!/bin/sh\nset -eu\ncat >/dev/null\nprintf '{\"type\":\"run.completed\",\"session_id\":\"cursor-session\"}\\n'\n",
		"@echo off\r\nsetlocal\r\nset /p PROMPT=\r\necho {\"type\":\"run.completed\",\"session_id\":\"cursor-session\"}\r\n",
	)

	cfg := agentadaptor.CursorConfig{
		CommonConfig: agentadaptor.CommonConfig{
			Command:         command,
			CWD:             workspace,
			AgentProfileDir: profileDir,
		},
		Model: "gpt-5",
	}

	events := &testutil.EventRecorder{}
	_, err := NewAdapter().Run(context.Background(), agentadaptor.DriverRunRequest{
		Prompt:    "hello from cursor",
		Config:    cfg,
		Workspace: agentadaptor.WorkspaceLease{ID: "workspace-a", CWD: workspace},
	}, events)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	assertInvocationEnvKeysContain(t, events.Snapshot(), "CURSOR_HOME")
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
