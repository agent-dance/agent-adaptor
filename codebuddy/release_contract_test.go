package codebuddy

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
	"github.com/agent-dance/agent-adaptor/internal/testutil"
)

func TestCodeBuddyTerminalResultIsAuthoritativeOutput(t *testing.T) {
	p := newParser(nil)
	stdout := strings.Join([]string{
		`{"type":"assistant","session_id":"s","message":{"content":[{"type":"text","text":"intermediate one"}]}}`,
		`{"type":"assistant","session_id":"s","message":{"content":[{"type":"text","text":"intermediate two"}]}}`,
		`{"type":"result","subtype":"success","is_error":false,"session_id":"s","result":"official final"}`,
	}, "\n") + "\n"
	if err := p.onChunk("stdout", []byte(stdout), timeNow()); err != nil {
		t.Fatalf("parse: %v", err)
	}
	p.finalize()

	if got := p.buildOutput(); got != "official final" {
		t.Fatalf("Output = %q, want official terminal result", got)
	}
}

func TestCodeBuddyEmptyTerminalResultRemainsAuthoritative(t *testing.T) {
	p := newParser(nil)
	stdout := strings.Join([]string{
		`{"type":"assistant","session_id":"s","message":{"content":[{"type":"text","text":"intermediate"}]}}`,
		`{"type":"result","subtype":"success","is_error":false,"session_id":"s","result":""}`,
	}, "\n") + "\n"
	_ = p.onChunk("stdout", []byte(stdout), timeNow())
	p.finalize()
	if failure := p.failureForOutcome(0); failure != nil {
		t.Fatalf("failure = %#v", failure)
	}
	if got := p.buildOutput(); got != "" {
		t.Fatalf("Output = %q, want authoritative empty terminal result", got)
	}
}

func TestCodeBuddyMissingTerminalResultFailsButPreservesLastAssistantText(t *testing.T) {
	p := newParser(nil)
	stdout := strings.Join([]string{
		`{"type":"assistant","session_id":"s","message":{"content":[{"type":"text","text":"intermediate"}]}}`,
		`{"type":"assistant","session_id":"s","message":{"content":[{"type":"text","text":"final assistant frame"}]}}`,
		`{"type":"result","subtype":"success","is_error":false,"session_id":"s"}`,
	}, "\n") + "\n"
	if err := p.onChunk("stdout", []byte(stdout), timeNow()); err != nil {
		t.Fatalf("parse: %v", err)
	}
	p.finalize()

	if got := p.buildOutput(); got != "final assistant frame" {
		t.Fatalf("Output = %q, want only last assistant frame", got)
	}
	if failure := p.failureForOutcome(0); failure == nil || !strings.Contains(failure.Message, "required result") {
		t.Fatalf("failure = %#v, want missing required result", failure)
	}
	if checkpoint := p.checkpoint(0); checkpoint != nil {
		t.Fatalf("checkpoint = %#v, want nil for malformed terminal", checkpoint)
	}
}

func TestCodeBuddyParserRejectsAliasesAndTerminalWithoutOwnSession(t *testing.T) {
	alias := newParser(nil)
	_ = alias.onChunk("stdout", []byte(`{"event":"result","subtype":"success","is_error":false,"sessionId":"alias","result":"wrong"}`+"\n"), timeNow())
	alias.finalize()
	if alias.terminalSeen || alias.checkpoint(0) != nil {
		t.Fatalf("non-official aliases formed terminal/checkpoint: terminal=%v checkpoint=%#v", alias.terminalSeen, alias.checkpoint(0))
	}
	if failure := alias.failureForOutcome(0); failure == nil || !strings.Contains(failure.Message, "without a terminal") {
		t.Fatalf("alias-only zero-exit failure = %#v", failure)
	}

	missingSession := newParser(nil)
	_ = missingSession.onChunk("stdout", []byte(strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"init-only"}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"done"}`,
	}, "\n")+"\n"), timeNow())
	missingSession.finalize()
	if missingSession.checkpoint(0) != nil {
		t.Fatalf("init session ID was incorrectly promoted without terminal session_id")
	}
	if failure := missingSession.failureForOutcome(0); failure == nil || !strings.Contains(failure.Message, "session_id") {
		t.Fatalf("missing terminal session failure = %#v", failure)
	}
}

func TestCodeBuddyErrorResultUsesOfficialErrorsArrayOnly(t *testing.T) {
	p := newParser(nil)
	_ = p.onChunk("stdout", []byte(`{"type":"result","subtype":"error_during_execution","is_error":true,"session_id":"s","result":"non-official alias","errors":["official failure"]}`+"\n"), timeNow())
	p.finalize()
	if p.errorMessage != "official failure" || p.buildOutput() != "" {
		t.Fatalf("error message/output = %q/%q, want official errors only", p.errorMessage, p.buildOutput())
	}
	if len(p.transcript) == 0 || p.transcript[len(p.transcript)-1].Text != "official failure" {
		t.Fatalf("terminal transcript = %#v", p.transcript)
	}
}

func TestCodeBuddyMalformedTrailingFrameEmitsStreamErrorNotFinished(t *testing.T) {
	recorder := &testutil.EventRecorder{}
	p := newParser(recorder)
	p.enableStreaming("run-malformed-tail")
	_ = p.onChunk("stdout", []byte(strings.Join([]string{
		`{"type":"result","subtype":"success","is_error":false,"session_id":"s","result":"done"}`,
		`{"type":"system","subtype":"unexpected-after-terminal"}`,
	}, "\n")+"\n"), timeNow())
	p.finalize()
	p.completeStream(p.failureForOutcome(0), 0, "", false)

	var sawError, sawFinished bool
	for _, payload := range recorder.StreamSnapshot() {
		sawError = sawError || payload.Kind == driver.StreamRunError
		sawFinished = sawFinished || payload.Kind == driver.StreamRunFinished
	}
	if !sawError || sawFinished {
		t.Fatalf("terminal stream = %#v, want only RunError", recorder.StreamSnapshot())
	}
}

func TestCodeBuddySuccessfulTerminalCannotFinishBeforeProcessOutcome(t *testing.T) {
	recorder := &testutil.EventRecorder{}
	p := newParser(recorder)
	p.enableStreaming("run-nonzero-after-terminal")
	_ = p.onChunk("stdout", []byte(`{"type":"result","subtype":"success","is_error":false,"session_id":"s","result":"done"}`+"\n"), timeNow())
	p.finalize()
	for _, payload := range recorder.StreamSnapshot() {
		if payload.Kind == driver.StreamRunFinished || payload.Kind == driver.StreamRunError {
			t.Fatalf("terminal payload escaped before process outcome: %#v", recorder.StreamSnapshot())
		}
	}
	p.completeStream(nil, 7, "", false)
	payloads := recorder.StreamSnapshot()
	if len(payloads) == 0 || payloads[len(payloads)-1].Kind != driver.StreamRunError {
		t.Fatalf("final stream = %#v, want RunError", payloads)
	}
	for _, payload := range payloads {
		if payload.Kind == driver.StreamRunFinished {
			t.Fatalf("nonzero process emitted RunFinished: %#v", payloads)
		}
	}
}

func TestCodeBuddySafeExtraArgsCannotOverrideResolvedInvocation(t *testing.T) {
	cfg := Config{
		CommonConfig: CommonConfig{ExtraArgs: []string{
			"--output-format=json", "--input-format", "text", "--json-schema", `{}`,
			"--resume", "attacker", "--fork-session", "--session-id", "attacker-id", "--continue",
			"--model", "attacker-model", "--effort=low", "--max-turns", "99",
			"--permission-mode", "default", "--dangerously-skip-permissions", "-y", "-p",
			"--custom-provider-flag", "kept",
			"--model", "--custom-after-malformed-managed-flag", "still-kept",
		}},
		Model:          "resolved-model",
		Effort:         "high",
		MaxTurnsPerRun: 3,
	}
	req := driver.Request{Session: &driver.SessionContext{
		Mode:  driver.SessionContinueOnly,
		State: &driver.SessionState{ResumeID: "resolved-parent"},
	}}
	args := buildExecArgs(cfg, req, PermissionPlan)

	for _, want := range [][]string{
		{"--output-format", "stream-json"},
		{"--resume", "resolved-parent"},
		{"--model", "resolved-model"},
		{"--effort", "high"},
		{"--max-turns", "3"},
		{"--permission-mode", "plan"},
		{"--custom-provider-flag", "kept"},
		{"--custom-after-malformed-managed-flag", "still-kept"},
	} {
		if !containsSubsequence(args, want) {
			t.Errorf("args %v missing %v", args, want)
		}
	}
	for _, forbidden := range []string{"attacker", "attacker-id", "attacker-model", "99", "default", "-y"} {
		if hasArg(args, forbidden) {
			t.Errorf("constructor ExtraArgs override %q survived: %v", forbidden, args)
		}
	}
	if hasArg(args, "--fork-session") {
		t.Fatalf("unsupported constructor fork flag survived: %v", args)
	}
}

func TestCodeBuddyForkRejectsBeforeProviderLaunch(t *testing.T) {
	t.Setenv("CODEBUDDY_CONFIG_DIR", "")
	home := t.TempDir()
	mustNotLaunch := filepath.Join(home, "must-not-launch")
	base := driver.Request{
		RunID:     "fork-run",
		Prompt:    "branch",
		Config:    isolatedCodeBuddyConfig(mustNotLaunch, home),
		Workspace: driver.WorkspaceLease{CWD: home},
		Policy:    autoApprovePolicy(),
	}

	for _, session := range []*driver.SessionContext{
		{Mode: driver.SessionFork, State: &driver.SessionState{ResumeID: "parent-session"}},
		{Mode: driver.SessionFork},
	} {
		fork := base
		fork.Session = session
		events := &testutil.EventRecorder{}
		_, err := (adapter{}).Run(context.Background(), fork, events)
		if !errors.Is(err, engine.ErrResumeRejected) || !strings.Contains(err.Error(), "does not expose") {
			t.Fatalf("fork error = %v, want explicit ErrResumeRejected", err)
		}
		for _, event := range events.Snapshot() {
			if event.Type == driver.RunEventInvocation || event.Type == driver.RunEventSpawn {
				t.Fatalf("unsupported fork launched provider: %#v", event)
			}
		}
	}

	// The pure argv builder is defensive too: if called directly with a fork,
	// it emits neither an unpublished fork flag nor ordinary parent resume.
	args := buildExecArgs(Config{}, driver.Request{Session: &driver.SessionContext{
		Mode:  driver.SessionFork,
		State: &driver.SessionState{ResumeID: "parent-session"},
	}}, PermissionUnset)
	if hasArg(args, "--fork-session") || hasArg(args, "--resume") {
		t.Fatalf("unsupported fork leaked session argv: %v", args)
	}
}

func TestCodeBuddyExitZeroProtocolFailuresAreBusinessFailures(t *testing.T) {
	t.Setenv("CODEBUDDY_CONFIG_DIR", "")
	for _, tc := range []struct {
		name        string
		posixOutput string
		winOutput   string
		want        string
	}{
		{
			name:        "missing terminal",
			posixOutput: "printf '%s\\n' 'partial output'\n",
			winOutput:   "echo partial output\r\n",
			want:        "without a terminal",
		},
		{
			name: "malformed",
			posixOutput: "printf '%s\\n' '{broken'\n" +
				"printf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"session_id\":\"s\",\"result\":\"done\"}'\n",
			winOutput: "echo {broken\r\n" +
				"echo {\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"session_id\":\"s\",\"result\":\"done\"}\r\n",
			want: "malformed",
		},
		{
			name:        "non-success terminal",
			posixOutput: "printf '%s\\n' '{\"type\":\"result\",\"subtype\":\"error_during_execution\",\"is_error\":true,\"session_id\":\"s\",\"errors\":[\"provider failed\"]}'\n",
			winOutput:   "echo {\"type\":\"result\",\"subtype\":\"error_during_execution\",\"is_error\":true,\"session_id\":\"s\",\"errors\":[\"provider failed\"]}\r\n",
			want:        "provider failed",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			command := testutil.WriteCommand(t, home, "fake-codebuddy-protocol", "#!/bin/sh\n"+tc.posixOutput, "@echo off\r\n"+tc.winOutput)
			res, err := (adapter{}).Run(context.Background(), driver.Request{
				Prompt: "go", Config: isolatedCodeBuddyConfig(command, home),
				Workspace: driver.WorkspaceLease{CWD: home}, Policy: autoApprovePolicy(),
			}, &testutil.EventRecorder{})
			if err != nil {
				t.Fatalf("Driver.Run infrastructure error = %v", err)
			}
			if res.ExitCode != 0 || res.Failure == nil || !strings.Contains(res.Failure.Message, tc.want) {
				t.Fatalf("response = %#v, want zero-exit protocol failure containing %q", res, tc.want)
			}
			if res.Checkpoint != nil {
				t.Fatalf("protocol failure checkpoint = %#v", res.Checkpoint)
			}
		})
	}
}

func TestCodeBuddyNativeStructuredOutputValidatedBeforeCheckpoint(t *testing.T) {
	t.Setenv("CODEBUDDY_CONFIG_DIR", "")
	home := t.TempDir()
	command := testutil.WriteCommand(t, home, "fake-codebuddy-invalid-structured",
		"#!/bin/sh\nprintf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"session_id\":\"structured-session\",\"result\":\"{\\\"answer\\\":\\\"wrong\\\"}\",\"structured_output\":{\"answer\":\"wrong\"}}'\n",
		"@echo off\r\necho {\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"session_id\":\"structured-session\",\"result\":\"{\\\"answer\\\":\\\"wrong\\\"}\",\"structured_output\":{\"answer\":\"wrong\"}}\r\n",
	)
	res, err := (adapter{}).Run(context.Background(), driver.Request{
		Prompt: "json", Config: isolatedCodeBuddyConfig(command, home),
		Workspace: driver.WorkspaceLease{CWD: home}, Policy: autoApprovePolicy(),
		StructuredOutputSource: driver.StructuredOutputSourceNative,
		OutputSchema: &driver.OutputSchema{
			Format:     driver.OutputFormatJSONSchema,
			SchemaJSON: []byte(`{"type":"object","properties":{"answer":{"type":"integer"}},"required":["answer"]}`),
			OnInvalid:  driver.StructuredOutputFailRun,
		},
	}, &testutil.EventRecorder{})
	if err != nil {
		t.Fatalf("Driver.Run infrastructure error = %v", err)
	}
	if res.Output != `{"answer":"wrong"}` {
		t.Fatalf("Output = %q, want terminal result", res.Output)
	}
	if res.StructuredOutput == nil || res.StructuredOutput.Valid || len(res.StructuredOutput.ValidationErrors) == 0 {
		t.Fatalf("structured output = %#v, want locally validated invalid value", res.StructuredOutput)
	}
	if res.Failure == nil || res.Failure.Code != driver.FailurePolicyError {
		t.Fatalf("failure = %#v, want schema policy failure", res.Failure)
	}
	if res.Checkpoint != nil {
		t.Fatalf("schema-invalid run checkpoint = %#v", res.Checkpoint)
	}
}

func TestCodeBuddyResumeRejectionMapsSentinel(t *testing.T) {
	t.Setenv("CODEBUDDY_CONFIG_DIR", "")
	for _, tc := range []struct {
		name        string
		posixBody   string
		windowsBody string
	}{
		{
			name:        "nonzero stderr",
			posixBody:   "#!/bin/sh\nprintf '%s\\n' 'Error: conversation parent-session not found' >&2\nexit 7\n",
			windowsBody: "@echo off\r\n>&2 echo Error: conversation parent-session not found\r\nexit /b 7\r\n",
		},
		{
			name:        "official error result",
			posixBody:   "#!/bin/sh\nprintf '%s\\n' '{\"type\":\"result\",\"subtype\":\"error_during_execution\",\"is_error\":true,\"session_id\":\"parent-session\",\"errors\":[\"No conversation found for session parent-session\"]}'\n",
			windowsBody: "@echo off\r\necho {\"type\":\"result\",\"subtype\":\"error_during_execution\",\"is_error\":true,\"session_id\":\"parent-session\",\"errors\":[\"No conversation found for session parent-session\"]}\r\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			command := testutil.WriteCommand(t, home, "fake-codebuddy-missing-session", tc.posixBody, tc.windowsBody)
			_, err := (adapter{}).Run(context.Background(), driver.Request{
				Prompt: "resume", Config: isolatedCodeBuddyConfig(command, home),
				Workspace: driver.WorkspaceLease{CWD: home}, Policy: autoApprovePolicy(),
				Session: &driver.SessionContext{
					Mode:  driver.SessionContinueOrStart,
					State: &driver.SessionState{ResumeID: "parent-session"},
				},
			}, &testutil.EventRecorder{})
			if !errors.Is(err, engine.ErrResumeRejected) {
				t.Fatalf("resume rejection error = %v, want ErrResumeRejected", err)
			}
		})
	}
}

func TestIsCodeBuddyResumeRejectedIsConservative(t *testing.T) {
	for _, message := range []string{
		"Error: conversation parent-session not found",
		"No conversation found for session parent-session",
		"session parent-session expired",
		"failed to resume session parent-session",
	} {
		if !isCodeBuddyResumeRejected(1, "", "", message) {
			t.Errorf("isCodeBuddyResumeRejected(%q) = false, want true", message)
		}
	}
	for _, message := range []string{
		"session service unavailable: network timeout",
		"authentication invalid for session request",
		"model unavailable in this region",
		"unable to load provider configuration",
	} {
		if isCodeBuddyResumeRejected(1, "", "", message) {
			t.Errorf("isCodeBuddyResumeRejected(%q) = true, want false", message)
		}
	}
}

func isolatedCodeBuddyConfig(command, home string) Config {
	return Config{CommonConfig: CommonConfig{
		Command: command,
		CWD:     home,
		Env: []driver.EnvBinding{
			{Name: "HOME", Value: home},
			{Name: "USERPROFILE", Value: home},
		},
	}}
}
