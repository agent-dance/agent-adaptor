package claude

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor"
	agentadaptor "github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/testutil"
)

func TestBuildClaudeExecArgsIncludesPartialMessagesWhenStreaming(t *testing.T) {
	cfg := Config{Model: "claude-sonnet-4"}
	req := agentadaptor.Request{Streaming: true}
	args, err := buildClaudeExecArgs(cfg, req, false)
	if err != nil {
		t.Fatalf("build args: %v", err)
	}
	found := false
	for _, a := range args {
		if a == "--include-partial-messages" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected --include-partial-messages in %#v", args)
	}

	argsBatch, err := buildClaudeExecArgs(cfg, agentadaptor.Request{Streaming: false}, false)
	if err != nil {
		t.Fatalf("build batch args: %v", err)
	}
	for _, a := range argsBatch {
		if a == "--include-partial-messages" {
			t.Fatalf("batch path must not add partial flag: %#v", argsBatch)
		}
	}
}

func TestBuildClaudeExecArgsBrowserPolicyMatrix(t *testing.T) {
	tests := []struct {
		name      string
		policy    agentadaptor.FeatureLevel
		extra     []string
		want      string
		forbidden string
		wantExtra string
	}{
		{name: "inherit preserves constructor args", policy: agentadaptor.FeatureInherit, extra: []string{"--chrome", "--custom-browser-test"}, want: "--chrome", wantExtra: "--custom-browser-test"},
		{name: "allow overrides constructor deny", policy: agentadaptor.FeatureAllow, extra: []string{"--no-chrome", "--custom-browser-test"}, want: "--chrome", forbidden: "--no-chrome", wantExtra: "--custom-browser-test"},
		{name: "deny overrides constructor allow", policy: agentadaptor.FeatureDeny, extra: []string{"--chrome", "--custom-browser-test"}, want: "--no-chrome", forbidden: "--chrome", wantExtra: "--custom-browser-test"},
	}
	for _, interactive := range []bool{false, true} {
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				args, err := buildClaudeExecArgs(Config{CommonConfig: CommonConfig{ExtraArgs: tt.extra}}, agentadaptor.Request{
					Policy: agentadaptor.RunPolicy{Browser: tt.policy},
				}, interactive)
				if err != nil {
					t.Fatalf("buildClaudeExecArgs: %v", err)
				}
				count := func(value string) int {
					n := 0
					for _, arg := range args {
						if arg == value {
							n++
						}
					}
					return n
				}
				if got := count(tt.want); got != 1 {
					t.Fatalf("%q count = %d in %v", tt.want, got, args)
				}
				if tt.forbidden != "" && count(tt.forbidden) != 0 {
					t.Fatalf("forbidden %q present in %v", tt.forbidden, args)
				}
				if tt.wantExtra != "" && count(tt.wantExtra) != 1 {
					t.Fatalf("unrelated ExtraArgs not preserved: %v", args)
				}
			})
		}
	}
}

func TestBuildClaudeExecArgsSessionForkUsesIndependentProviderSession(t *testing.T) {
	base := agentadaptor.Request{Session: &agentadaptor.SessionContext{
		Mode:  agentadaptor.SessionContinueOnly,
		State: &agentadaptor.SessionState{ResumeID: "parent-session"},
	}}
	continued, err := buildClaudeExecArgs(Config{}, base, false)
	if err != nil {
		t.Fatalf("continue args: %v", err)
	}
	assertArgPair(t, continued, "--resume", "parent-session")
	assertArgCount(t, continued, "--fork-session", 0)

	forked := base
	forked.Session = &agentadaptor.SessionContext{
		Mode:  agentadaptor.SessionFork,
		State: &agentadaptor.SessionState{ResumeID: "parent-session"},
	}
	forkArgs, err := buildClaudeExecArgs(Config{}, forked, false)
	if err != nil {
		t.Fatalf("fork args: %v", err)
	}
	assertArgPair(t, forkArgs, "--resume", "parent-session")
	assertArgCount(t, forkArgs, "--resume", 1)
	assertArgCount(t, forkArgs, "--fork-session", 1)
	resumeAt, forkAt := indexOfArg(forkArgs, "--resume"), indexOfArg(forkArgs, "--fork-session")
	if resumeAt < 0 || forkAt != resumeAt+2 {
		t.Fatalf("fork must be encoded as --resume ID --fork-session, got %v", forkArgs)
	}
}

func TestBuildClaudeExecArgsFiltersSDKManagedExtraArgs(t *testing.T) {
	extra := []string{
		"--output-format", "text",
		"--input-format=plain",
		"--permission-mode", "bypassPermissions",
		"--dangerously-skip-permissions",
		"--permission-prompt-tool=unsafe",
		"--settings", "dangerous-settings.json",
		"--setting-sources=user,project",
		"--allowedTools", "Bash", "Write", "Edit",
		"--tools", "Read", "Grep",
		"--resume", "wrong-parent",
		"--fork-session",
		"--session-id=wrong-session",
		"--model", "wrong-model",
		"--effort=low",
		"--max-turns", "999",
		"--model", "--custom-after-malformed", "kept",
		"--custom-provider-flag", "custom-value",
	}
	req := agentadaptor.Request{
		Streaming:     true,
		ModelOverride: "ignored-by-this-helper",
		Policy: agentadaptor.RunPolicy{HumanDecision: agentadaptor.HumanDecisionPolicy{
			Permission: agentadaptor.HumanDecisionAutoReject,
		}},
		Session: &agentadaptor.SessionContext{
			Mode:  agentadaptor.SessionContinueOnly,
			State: &agentadaptor.SessionState{ResumeID: "right-parent"},
		},
	}
	args, err := buildClaudeExecArgs(Config{
		CommonConfig:   CommonConfig{ExtraArgs: extra},
		Model:          "right-model",
		Effort:         "high",
		MaxTurnsPerRun: 7,
	}, req, true)
	if err != nil {
		t.Fatalf("build args: %v", err)
	}
	for _, forbidden := range []string{"text", "plain", "bypassPermissions", "unsafe", "dangerous-settings.json", "Bash", "Write", "Edit", "Read", "Grep", "wrong-parent", "wrong-session", "wrong-model", "low", "999"} {
		if indexOfArg(args, forbidden) >= 0 {
			t.Fatalf("managed ExtraArgs value %q leaked into %v", forbidden, args)
		}
	}
	assertArgPair(t, args, "--output-format", "stream-json")
	assertArgPair(t, args, "--input-format", "stream-json")
	assertArgPair(t, args, "--permission-prompt-tool", "stdio")
	assertArgPair(t, args, "--resume", "right-parent")
	assertArgPair(t, args, "--model", "right-model")
	assertArgPair(t, args, "--effort", "high")
	assertArgPair(t, args, "--max-turns", "7")
	assertArgPair(t, args, "--custom-provider-flag", "custom-value")
	assertArgPair(t, args, "--custom-after-malformed", "kept")
	assertArgCount(t, args, "--dangerously-skip-permissions", 0)
}

func TestIsClaudeResumeRejectedIsConservative(t *testing.T) {
	for _, message := range []string{
		"No conversation found with session ID: deadbeef",
		"conversation with id deadbeef not found",
		"failed to resume session",
	} {
		if !isClaudeResumeRejected(message) {
			t.Fatalf("known resume rejection was not classified: %q", message)
		}
	}
	for _, message := range []string{
		"authentication session expired",
		"network unavailable while creating session",
		"model overloaded",
		"permission denied",
		"session could not start because model was not found",
		"failed to resume session because authentication expired",
		"unknown session policy returned by server",
		"session is unavailable because credentials expired",
	} {
		if isClaudeResumeRejected(message) {
			t.Fatalf("unrelated failure was classified as resume rejection: %q", message)
		}
	}
}

func assertArgPair(t *testing.T, args []string, name, value string) {
	t.Helper()
	for i := 0; i+1 < len(args); i++ {
		if args[i] == name && args[i+1] == value {
			return
		}
	}
	t.Fatalf("missing argv pair %q %q in %v", name, value, args)
}

func assertArgCount(t *testing.T, args []string, name string, want int) {
	t.Helper()
	got := 0
	for _, arg := range args {
		if arg == name {
			got++
		}
	}
	if got != want {
		t.Fatalf("argv %q count = %d, want %d in %v", name, got, want, args)
	}
}

func indexOfArg(args []string, want string) int {
	for i, arg := range args {
		if arg == want {
			return i
		}
	}
	return -1
}

func TestBuildClaudeExecArgsUsesJSONSchemaForNativeStructuredOutput(t *testing.T) {
	schema := []byte(`{"type":"object","properties":{"project_name":{"type":"string"}}}`)
	args, err := buildClaudeExecArgs(Config{Model: "claude-sonnet-4"}, agentadaptor.Request{
		OutputSchema: &agentadaptor.OutputSchema{
			Format:     agentadaptor.OutputFormatJSONSchema,
			Mode:       agentadaptor.StructuredOutputNativeStrict,
			SchemaJSON: schema,
		},
	}, false)
	if err != nil {
		t.Fatalf("build args: %v", err)
	}
	joined := " " + strings.Join(args, " ") + " "
	for _, want := range []string{" --output-format ", " json ", " --json-schema ", `"project_name"`} {
		if !strings.Contains(joined, want) {
			t.Fatalf("native structured args missing %q in %#v", strings.TrimSpace(want), args)
		}
	}
	for _, forbidden := range []string{" stream-json ", " --verbose "} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("native structured args must not contain %q: %#v", strings.TrimSpace(forbidden), args)
		}
	}
}

func TestBuildClaudeExecArgsUsesStreamJSONForStreamingStructuredOutput(t *testing.T) {
	schema := []byte(`{"type":"object","properties":{"project_name":{"type":"string"}}}`)
	args, err := buildClaudeExecArgs(Config{Model: "claude-sonnet-4"}, agentadaptor.Request{
		Streaming: true,
		OutputSchema: &agentadaptor.OutputSchema{
			Format:     agentadaptor.OutputFormatJSONSchema,
			Mode:       agentadaptor.StructuredOutputNativeStrict,
			SchemaJSON: schema,
		},
	}, false)
	if err != nil {
		t.Fatalf("build args: %v", err)
	}
	joined := " " + strings.Join(args, " ") + " "
	for _, want := range []string{" --output-format ", " stream-json ", " --verbose ", " --json-schema ", `"project_name"`, " --include-partial-messages "} {
		if !strings.Contains(joined, want) {
			t.Fatalf("streaming structured args missing %q in %#v", strings.TrimSpace(want), args)
		}
	}
	for _, forbidden := range []string{" --output-format json "} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("streaming structured args must not contain %q: %#v", strings.TrimSpace(forbidden), args)
		}
	}
}

func TestBuildClaudeExecArgsInlinesReferencesWithoutLosingLargeNumbers(t *testing.T) {
	schema := []byte(`{"$schema":"https://json-schema.org/draft/2020-12/schema","$defs":{"item":{"type":"object","properties":{"id":{"type":"integer","minimum":9007199254740993}}}},"type":"object","properties":{"item":{"$ref":"#/$defs/item"}}}`)
	args, err := buildClaudeExecArgs(Config{}, agentadaptor.Request{
		OutputSchema: &agentadaptor.OutputSchema{
			Format:     agentadaptor.OutputFormatJSONSchema,
			Mode:       agentadaptor.StructuredOutputNativeStrict,
			SchemaJSON: schema,
		},
	}, false)
	if err != nil {
		t.Fatalf("build args: %v", err)
	}
	var prepared string
	for i := range args {
		if args[i] == "--json-schema" && i+1 < len(args) {
			prepared = args[i+1]
			break
		}
	}
	if prepared == "" {
		t.Fatalf("missing prepared schema in %#v", args)
	}
	for _, forbidden := range []string{`"$schema"`, `"$defs"`, `"$ref"`} {
		if strings.Contains(prepared, forbidden) {
			t.Fatalf("prepared schema contains %s: %s", forbidden, prepared)
		}
	}
	if !strings.Contains(prepared, `"minimum":9007199254740993`) {
		t.Fatalf("large number was not preserved: %s", prepared)
	}
}

func TestBuildClaudeExecArgsRejectsRecursiveSchemaReferences(t *testing.T) {
	schema := []byte(`{"$defs":{"node":{"type":"object","properties":{"children":{"type":"array","items":{"$ref":"#/$defs/node"}}}}},"$ref":"#/$defs/node"}`)
	_, err := buildClaudeExecArgs(Config{}, agentadaptor.Request{
		OutputSchema: &agentadaptor.OutputSchema{
			Format:     agentadaptor.OutputFormatJSONSchema,
			Mode:       agentadaptor.StructuredOutputNativeStrict,
			SchemaJSON: schema,
		},
	}, false)
	if !errors.Is(err, agentadaptor.ErrInvalidOutputSchema) || !strings.Contains(err.Error(), "recursive local reference") {
		t.Fatalf("error = %v, want recursive ErrInvalidOutputSchema", err)
	}
}

func TestBuildClaudeExecArgsPreservesDefinitionsProperty(t *testing.T) {
	schema := []byte(`{"type":"object","properties":{"definitions":{"type":"string"}},"required":["definitions"],"additionalProperties":false}`)
	args, err := buildClaudeExecArgs(Config{}, agentadaptor.Request{
		OutputSchema: &agentadaptor.OutputSchema{Format: agentadaptor.OutputFormatJSONSchema, Mode: agentadaptor.StructuredOutputNativeStrict, SchemaJSON: schema},
	}, false)
	if err != nil {
		t.Fatalf("build args: %v", err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, `"definitions":{"type":"string"}`) {
		t.Fatalf("schema business property was removed: %#v", args)
	}
}

func TestBuildClaudeExecArgsResolvesArrayJSONPointer(t *testing.T) {
	schema := []byte(`{"$defs":{"choice":{"allOf":[{"type":"string"}]}},"type":"object","properties":{"value":{"$ref":"#/$defs/choice/allOf/0"}}}`)
	args, err := buildClaudeExecArgs(Config{}, agentadaptor.Request{
		OutputSchema: &agentadaptor.OutputSchema{Format: agentadaptor.OutputFormatJSONSchema, Mode: agentadaptor.StructuredOutputNativeStrict, SchemaJSON: schema},
	}, false)
	if err != nil {
		t.Fatalf("build args: %v", err)
	}
	joined := strings.Join(args, " ")
	if strings.Contains(joined, `"$ref"`) || !strings.Contains(joined, `"value":{"type":"string"}`) {
		t.Fatalf("schema reference was not resolved: %#v", args)
	}
}

func TestBuildClaudeExecArgsDoesNotRewriteConstData(t *testing.T) {
	schema := []byte(`{"$defs":{"value":{"type":"string"}},"type":"object","const":{"$ref":"#/$defs/value"}}`)
	args, err := buildClaudeExecArgs(Config{}, agentadaptor.Request{
		OutputSchema: &agentadaptor.OutputSchema{Format: agentadaptor.OutputFormatJSONSchema, Mode: agentadaptor.StructuredOutputNativeStrict, SchemaJSON: schema},
	}, false)
	if err != nil {
		t.Fatalf("build args: %v", err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, `"const":{"$ref":"#/$defs/value"}`) {
		t.Fatalf("const data was rewritten: %#v", args)
	}
}

func TestDescriptorAdvertisesExpectedMCPCapabilities(t *testing.T) {
	caps := adapter{}.Descriptor().MCP
	if !caps.Supported || !caps.Stdio || !caps.HTTP || !caps.SSE {
		t.Fatalf("unexpected Claude MCP capability: %#v", caps)
	}
}

func TestDescriptorAdvertisesStructuredOutputCapabilities(t *testing.T) {
	caps := adapter{}.Descriptor().StructuredOutput
	if !caps.JSONSchemaNative || !caps.JSONSchemaPromptValidate || !caps.WorksWithRun {
		t.Fatalf("unexpected Claude structured-output capability: %#v", caps)
	}
	if !caps.WorksWithStreaming || caps.WorksWithHITL {
		t.Fatalf("Claude native structured output capability mismatch: %#v", caps)
	}
}

func TestBuildClaudeExecArgsInteractiveEnablesStdioPermissionPrompt(t *testing.T) {
	cfg := Config{Model: "claude-sonnet-4"}
	args, err := buildClaudeExecArgs(cfg, agentadaptor.Request{}, true)
	if err != nil {
		t.Fatalf("build args: %v", err)
	}
	joined := " " + strings.Join(args, " ") + " "
	for _, want := range []string{
		" --input-format ",
		" stream-json ",
		" --include-partial-messages ",
		" --replay-user-messages ",
		" --permission-prompt-tool ",
		" stdio ",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("interactive args missing %q in %#v", strings.TrimSpace(want), args)
		}
	}
}

func TestStreamCapabilityValues(t *testing.T) {
	cap := any(adapter{}).(interface {
		StreamCapability() agentadaptor.StreamCapability
	}).StreamCapability()
	if !cap.Native || !cap.TokenLevel || !cap.Reasoning || !cap.ToolCallArgs || !cap.HITL {
		t.Fatalf("unexpected capability: %#v", cap)
	}
}

func TestParseCheckpointRequiresRecognizedClaudeEvent(t *testing.T) {
	stdout := `{"type":"tool.result","session_id":"ignore-me"}
{"type":"result","subtype":"success","is_error":false,"session_id":"claude-session","display_id":"claude-display","result":""}`

	checkpoint := parseCheckpoint(stdout, 0)
	if checkpoint == nil || checkpoint.State == nil {
		t.Fatal("expected checkpoint")
	}
	if checkpoint.State.ResumeID != "claude-session" || checkpoint.State.DisplayID != "claude-display" {
		t.Fatalf("unexpected checkpoint: %#v", checkpoint.State)
	}
}

func TestParseClaudeResultStructuredOutput(t *testing.T) {
	parsed := snapshotClaudeStdout(`{"type":"result","subtype":"success","is_error":false,"session_id":"claude-structured","structured_output":{"project_name":"agent-adaptor"},"result":"ok"}`)
	if got := parsed.buildOutput(); got != "ok" {
		t.Fatalf("terminal-only structured Output = %q, want ok", got)
	}
	if parsed.structuredOutput == nil || string(parsed.structuredOutput.RawJSON) != `{"project_name":"agent-adaptor"}` {
		t.Fatalf("expected structured output, got %#v", parsed.structuredOutput)
	}
}

func TestClaudeStreamingStructuredOutputEndToEnd(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	command := testutil.WriteCommand(t, home, "fake-claude-structured",
		`#!/bin/sh
set -eu
cat >/dev/null
printf '%s\n' '{"type":"system","subtype":"init","session_id":"sess-structured","model":"claude-fixture"}'
printf '%s\n' '{"type":"stream_event","session_id":"sess-structured","event":{"type":"message_start","message":{"id":"msg-1"}}}'
printf '%s\n' '{"type":"stream_event","session_id":"sess-structured","event":{"type":"content_block_start","index":0,"content_block":{"type":"text"}}}'
printf '%s\n' '{"type":"stream_event","session_id":"sess-structured","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"done"}}}'
printf '%s\n' '{"type":"stream_event","session_id":"sess-structured","event":{"type":"content_block_stop","index":0}}'
printf '%s\n' '{"type":"assistant","session_id":"sess-structured","message":{"model":"claude-fixture","content":[{"type":"text","text":"done"}]}}'
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"session_id":"sess-structured","structured_output":{"project_name":"agent-adaptor"},"result":"done"}'
`,
		"@echo off\r\nmore > nul\r\necho {\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"sess-structured\",\"model\":\"claude-fixture\"}\r\necho {\"type\":\"stream_event\",\"session_id\":\"sess-structured\",\"event\":{\"type\":\"message_start\",\"message\":{\"id\":\"msg-1\"}}}\r\necho {\"type\":\"stream_event\",\"session_id\":\"sess-structured\",\"event\":{\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\"}}}\r\necho {\"type\":\"stream_event\",\"session_id\":\"sess-structured\",\"event\":{\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"done\"}}}\r\necho {\"type\":\"stream_event\",\"session_id\":\"sess-structured\",\"event\":{\"type\":\"content_block_stop\",\"index\":0}}\r\necho {\"type\":\"assistant\",\"session_id\":\"sess-structured\",\"message\":{\"model\":\"claude-fixture\",\"content\":[{\"type\":\"text\",\"text\":\"done\"}]}}\r\necho {\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"session_id\":\"sess-structured\",\"structured_output\":{\"project_name\":\"agent-adaptor\"},\"result\":\"done\"}\r\n",
	)
	agent := adaptor.New(Driver(Config{
		CommonConfig: CommonConfig{
			Command: command,
			CWD:     workspace,
			Env: []agentadaptor.EnvBinding{
				{Name: "HOME", Value: home},
				{Name: "USERPROFILE", Value: home},
			},
		},
		Model: "claude-sonnet-4",
	}))
	stream := agent.Stream(
		context.Background(),
		"extract project metadata",
		adaptor.WithSchemaJSON([]byte(`{"type":"object","properties":{"project_name":{"type":"string"}},"required":["project_name"],"additionalProperties":false}`)),
	)
	var events []adaptor.Event
	for event := range stream.Events() {
		events = append(events, event)
	}
	result, err := stream.Result()
	if err != nil {
		t.Fatalf("stream result: %v", err)
	}
	var structured map[string]string
	if err := result.Decode(&structured); err != nil || structured["project_name"] != "agent-adaptor" {
		t.Fatalf("Decode = (%#v, %v), want project_name=agent-adaptor", structured, err)
	}
	if result.Text != "done" || !strings.Contains(result.Raw().Stdout, `"structured_output"`) {
		t.Fatalf("result = %#v", result)
	}
	seenText := false
	seenFinished := false
	for _, event := range events {
		switch typed := event.(type) {
		case adaptor.TextDelta:
			seenText = seenText || typed.Text == "done"
		case adaptor.RunFinished:
			seenFinished = true
		}
	}
	if !seenText || !seenFinished {
		t.Fatalf("events = %#v", events)
	}

	runResult, err := agent.Run(
		context.Background(),
		"extract project metadata",
		adaptor.WithSchemaJSON([]byte(`{"type":"object","properties":{"project_name":{"type":"string"}},"required":["project_name"],"additionalProperties":false}`)),
	)
	if err != nil {
		t.Fatalf("Run result: %v", err)
	}
	if runResult.Text != result.Text || runResult.Summary != result.Summary ||
		runResult.Raw().Stdout != result.Raw().Stdout || runResult.Raw().Stderr != result.Raw().Stderr ||
		!reflect.DeepEqual(runResult.Raw().Terminal, result.Raw().Terminal) ||
		!reflect.DeepEqual(runResult.Transcript(), result.Transcript()) {
		t.Fatalf("Run and Stream.Result diverged:\nrun=%#v\nstream=%#v", runResult, result)
	}
	var runStructured map[string]string
	if err := runResult.Decode(&runStructured); err != nil || !reflect.DeepEqual(runStructured, structured) {
		t.Fatalf("Run Decode = (%#v, %v), Stream Decode = %#v", runStructured, err, structured)
	}
}

func TestClaudeNativeStructuredOutputValidationPrecedesCheckpoint(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home := t.TempDir()
	command := testutil.WriteCommand(t, home, "fake-claude-invalid-structured",
		"#!/bin/sh\ncat >/dev/null\nprintf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"session_id\":\"structured-invalid\",\"result\":\"done\",\"structured_output\":{\"project_name\":42}}'\n",
		"@echo off\r\nmore > nul\r\necho {\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"session_id\":\"structured-invalid\",\"result\":\"done\",\"structured_output\":{\"project_name\":42}}\r\n",
	)
	sink := &streamSink{}
	resp, err := (adapter{}).Run(context.Background(), agentadaptor.Request{
		RunID:     "run-invalid-structured",
		Streaming: true,
		Prompt:    "extract project metadata",
		Config:    Config{CommonConfig: CommonConfig{Command: command, CWD: home}},
		Workspace: agentadaptor.WorkspaceLease{ID: "workspace", CWD: home},
		OutputSchema: &agentadaptor.OutputSchema{
			Format:     agentadaptor.OutputFormatJSONSchema,
			Mode:       agentadaptor.StructuredOutputNativeStrict,
			SchemaJSON: []byte(`{"type":"object","properties":{"project_name":{"type":"string"}},"required":["project_name"],"additionalProperties":false}`),
			OnInvalid:  agentadaptor.StructuredOutputFailRun,
		},
	}, sink)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if resp.StructuredOutput == nil || resp.StructuredOutput.Valid {
		t.Fatalf("StructuredOutput = %#v, want invalid", resp.StructuredOutput)
	}
	if resp.Failure == nil || resp.Failure.Code != agentadaptor.FailurePolicyError {
		t.Fatalf("Failure = %#v, want policy_error", resp.Failure)
	}
	if resp.Checkpoint != nil {
		t.Fatalf("invalid structured output produced checkpoint %#v", resp.Checkpoint)
	}
	assertClaudeStreamTerminal(t, sink.snapshot(), agentadaptor.StreamRunError)
}

func TestClaudeDirectStreamTerminalMatchesFrozenProtocolOutcome(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	tests := []struct {
		name  string
		lines []string
	}{
		{
			name:  "success result missing result field",
			lines: []string{`{"type":"result","subtype":"success","is_error":false,"session_id":"missing-result"}`},
		},
		{
			name: "payload follows success result",
			lines: []string{
				`{"type":"result","subtype":"success","is_error":false,"session_id":"late-payload","result":"done"}`,
				`{"type":"system","subtype":"status"}`,
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			var shell, batch strings.Builder
			shell.WriteString("#!/bin/sh\ncat >/dev/null\n")
			batch.WriteString("@echo off\r\nmore > nul\r\n")
			for _, line := range tc.lines {
				shell.WriteString("printf '%s\\n' '")
				shell.WriteString(line)
				shell.WriteString("'\n")
				batch.WriteString("echo ")
				batch.WriteString(line)
				batch.WriteString("\r\n")
			}
			command := testutil.WriteCommand(t, home, "fake-claude-frozen-outcome", shell.String(), batch.String())
			sink := &streamSink{}
			resp, err := (adapter{}).Run(context.Background(), agentadaptor.Request{
				RunID:     "run-frozen-outcome",
				Streaming: true,
				Prompt:    "test",
				Config:    Config{CommonConfig: CommonConfig{Command: command, CWD: home}},
				Workspace: agentadaptor.WorkspaceLease{ID: "workspace", CWD: home},
			}, sink)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if resp.Failure == nil || resp.Checkpoint != nil {
				t.Fatalf("Response = failure %#v checkpoint %#v", resp.Failure, resp.Checkpoint)
			}
			assertClaudeStreamTerminal(t, sink.snapshot(), agentadaptor.StreamRunError)
		})
	}
}

func assertClaudeStreamTerminal(t *testing.T, payloads []agentadaptor.StreamPayload, want agentadaptor.StreamKind) {
	t.Helper()
	terminals := make([]agentadaptor.StreamKind, 0, 1)
	for _, payload := range payloads {
		if payload.Kind == agentadaptor.StreamRunFinished || payload.Kind == agentadaptor.StreamRunError {
			terminals = append(terminals, payload.Kind)
		}
	}
	if len(terminals) != 1 || terminals[0] != want {
		t.Fatalf("stream terminals = %v, want exactly [%s]; payloads=%#v", terminals, want, payloads)
	}
}

func TestParseCheckpointAcceptsSessionOnlyPayload(t *testing.T) {
	checkpoint := parseCheckpoint(`{"session_id":"claude-session"}`, 0)
	if checkpoint != nil {
		t.Fatalf("session-only payload is not terminal proof: %#v", checkpoint)
	}
}

func TestDetectModelFallsBackToClaudeSettingsFile(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home := t.TempDir()
	configDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "settings.json"), []byte("{\"model\":\"claude-opus-4\"}\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	detected, err := any(adapter{}).(interface {
		DetectModel(context.Context, any, *agentadaptor.ProfileSelection) (*agentadaptor.DetectedModel, error)
	}).DetectModel(context.Background(), Config{
		CommonConfig: CommonConfig{
			Env: []agentadaptor.EnvBinding{{Name: "HOME", Value: home}},
		},
	}, nil)
	if err != nil {
		t.Fatalf("detect model: %v", err)
	}
	if detected == nil || detected.Model != "claude-opus-4" || detected.Source != "config_file" {
		t.Fatalf("unexpected detected model: %#v", detected)
	}
}

func TestDetectModelUsesExplicitProfileOptionAsClaudeConfigDir(t *testing.T) {
	profileDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(profileDir, "settings.json"), []byte("{\"model\":\"claude-sonnet-4\"}\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	detected, err := any(adapter{}).(interface {
		DetectModel(context.Context, any, *agentadaptor.ProfileSelection) (*agentadaptor.DetectedModel, error)
	}).DetectModel(context.Background(), Config{
		CommonConfig: CommonConfig{
			Env: []agentadaptor.EnvBinding{{Name: "CLAUDE_CONFIG_DIR", Value: profileDir}},
		},
	}, nil)
	if err != nil {
		t.Fatalf("detect model: %v", err)
	}
	if detected == nil || detected.Model != "claude-sonnet-4" || detected.Source != "config_file" {
		t.Fatalf("unexpected detected model: %#v", detected)
	}
}

func TestDetectModelPrefersExplicitClaudeConfigDirOverProfileOption(t *testing.T) {
	profileDir := t.TempDir()
	overrideDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(profileDir, "settings.json"), []byte("{\"model\":\"claude-opus-4\"}\n"), 0o644); err != nil {
		t.Fatalf("write profile config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(overrideDir, "settings.json"), []byte("{\"model\":\"claude-sonnet-4\"}\n"), 0o644); err != nil {
		t.Fatalf("write override config: %v", err)
	}

	detected, err := any(adapter{}).(interface {
		DetectModel(context.Context, any, *agentadaptor.ProfileSelection) (*agentadaptor.DetectedModel, error)
	}).DetectModel(context.Background(), Config{
		CommonConfig: CommonConfig{
			Env: []agentadaptor.EnvBinding{
				{Name: "CLAUDE_CONFIG_DIR", Value: overrideDir},
			},
		},
	}, nil)
	if err != nil {
		t.Fatalf("detect model: %v", err)
	}
	if detected == nil || detected.Model != "claude-sonnet-4" || detected.Source != "config_file" {
		t.Fatalf("unexpected detected model: %#v", detected)
	}
}

func TestGetProfileUsesDedicatedProfileOptionForClaude(t *testing.T) {
	profileDir := t.TempDir()
	profile, err := any(adapter{}).(interface {
		GetProfile(context.Context, any, agentadaptor.AgentIdentity, *agentadaptor.ProfileSelection) (agentadaptor.AgentProfile, error)
	}).GetProfile(context.Background(), Config{
		CommonConfig: CommonConfig{
			Env: []agentadaptor.EnvBinding{{Name: "CLAUDE_CONFIG_DIR", Value: profileDir}},
		},
	}, agentadaptor.AgentIdentity{}, nil)
	if err != nil {
		t.Fatalf("get profile: %v", err)
	}
	if !profile.Supported || profile.Dir != profileDir || profile.Source != agentadaptor.AgentProfileSourceBindingEnv || profile.EnvVar != "CLAUDE_CONFIG_DIR" {
		t.Fatalf("unexpected profile: %#v", profile)
	}
}

func TestGetProfileUsesProcessEnvForClaudeWhenUnset(t *testing.T) {
	profileDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", profileDir)

	profile, err := any(adapter{}).(interface {
		GetProfile(context.Context, any, agentadaptor.AgentIdentity, *agentadaptor.ProfileSelection) (agentadaptor.AgentProfile, error)
	}).GetProfile(context.Background(), Config{}, agentadaptor.AgentIdentity{}, nil)
	if err != nil {
		t.Fatalf("get profile: %v", err)
	}
	if !profile.Supported || profile.Dir != profileDir || profile.Source != agentadaptor.AgentProfileSourceProcessEnv {
		t.Fatalf("unexpected profile: %#v", profile)
	}
}

func TestGetProfileCloneCanShareNativeClaudeAuth(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home := t.TempDir()
	nativeProfile := filepath.Join(home, ".claude")
	if err := os.MkdirAll(nativeProfile, 0o755); err != nil {
		t.Fatalf("mkdir native profile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nativeProfile, "settings.json"), []byte("{\"model\":\"claude-sonnet-4\"}\n"), 0o644); err != nil {
		t.Fatalf("write native settings: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nativeProfile, ".credentials.json"), []byte(`{"claudeAiOauth":{"accessToken":"native"}}`), 0o600); err != nil {
		t.Fatalf("write native credentials: %v", err)
	}
	target := filepath.Join(t.TempDir(), "isolated")

	profile, err := any(adapter{}).(interface {
		GetProfile(context.Context, any, agentadaptor.AgentIdentity, *agentadaptor.ProfileSelection) (agentadaptor.AgentProfile, error)
	}).GetProfile(context.Background(), Config{
		CommonConfig: CommonConfig{
			Env: []agentadaptor.EnvBinding{{Name: "HOME", Value: home}, {Name: "USERPROFILE", Value: home}},
		},
	}, agentadaptor.AgentIdentity{}, &agentadaptor.ProfileSelection{
		Mode: agentadaptor.ProfileModeClone,
		Dir:  target,
		Clone: &agentadaptor.CloneProfileOptions{
			IncludeSettings: true,
			AuthMode:        agentadaptor.CloneProfileAuthLink,
		},
	})
	if err != nil {
		t.Fatalf("get profile: %v", err)
	}
	if profile.Dir != target || profile.Source != agentadaptor.AgentProfileSourceProfileOption {
		t.Fatalf("unexpected profile: %#v", profile)
	}
	rawSettings, err := os.ReadFile(filepath.Join(target, "settings.json"))
	if err != nil {
		t.Fatalf("read cloned settings: %v", err)
	}
	if !strings.Contains(string(rawSettings), "claude-sonnet-4") {
		t.Fatalf("expected cloned Claude settings, got %s", string(rawSettings))
	}
	sourceInfo, sourceErr := os.Stat(filepath.Join(nativeProfile, ".credentials.json"))
	targetInfo, targetErr := os.Stat(filepath.Join(target, ".credentials.json"))
	if sourceErr != nil || targetErr != nil {
		t.Fatalf("stat auth files: source=%v target=%v", sourceErr, targetErr)
	}
	if !os.SameFile(sourceInfo, targetInfo) {
		t.Fatalf("expected cloned profile .credentials.json to share native Claude credentials")
	}
}

func TestConfigSchemaIncludesGroupsDefaultsAndOptions(t *testing.T) {
	schema := adapter{}.Descriptor().ConfigSchema
	if schema == nil || len(schema.Fields) == 0 {
		t.Fatalf("expected config schema, got %#v", schema)
	}
	modelField := schemaFieldByName(t, schema, "model")
	if modelField.Name != "model" || modelField.Group != "model" || len(modelField.Options) == 0 || modelField.Default != "claude-sonnet-4" {
		t.Fatalf("unexpected model field: %#v", modelField)
	}
	commandField := schemaFieldByName(t, schema, "command")
	if commandField.Name != "command" || commandField.Group != "command" || commandField.Default != "claude" {
		t.Fatalf("unexpected command field: %#v", commandField)
	}
}

func TestListModelsUsesBedrockIdentifiersWhenBedrockAuthEnabled(t *testing.T) {
	models, err := any(adapter{}).(interface {
		ListModels(context.Context, any) ([]agentadaptor.ModelInfo, error)
	}).ListModels(context.Background(), Config{
		CommonConfig: CommonConfig{
			Env: []agentadaptor.EnvBinding{{Name: "CLAUDE_CODE_USE_BEDROCK", Value: "true"}},
		},
	})
	if err != nil {
		t.Fatalf("list models: %v", err)
	}
	if len(models) == 0 || models[0].ID != "us.anthropic.claude-opus-4-6-v1" {
		t.Fatalf("unexpected models: %#v", models)
	}
}

func TestConfigSchemaHydratesBedrockModelOptions(t *testing.T) {
	schema, err := any(adapter{}).(interface {
		ConfigSchema(context.Context, any) (*agentadaptor.ConfigSchema, error)
	}).ConfigSchema(context.Background(), Config{
		CommonConfig: CommonConfig{
			Env: []agentadaptor.EnvBinding{{Name: "ANTHROPIC_BEDROCK_BASE_URL", Value: "https://bedrock.local"}},
		},
	})
	if err != nil {
		t.Fatalf("config schema: %v", err)
	}
	if schema == nil || len(schema.Fields) == 0 {
		t.Fatalf("unexpected schema: %#v", schema)
	}
	modelField := schemaFieldByName(t, schema, "model")
	if modelField.Default != "us.anthropic.claude-sonnet-4-5-20250929-v2:0" {
		t.Fatalf("unexpected bedrock default: %#v", modelField)
	}
	if len(modelField.Options) == 0 || modelField.Options[0].Value != "us.anthropic.claude-opus-4-6-v1" {
		t.Fatalf("unexpected bedrock options: %#v", modelField.Options)
	}
}

func TestCheckEnvironmentReportsConfigFileState(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home := t.TempDir()
	configDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "settings.json"), []byte("{\"model\":\"claude-opus-4\",\"env\":{\"ANTHROPIC_BASE_URL\":\"http://127.0.0.1:2455\"}}\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	report, err := any(adapter{}).(interface {
		CheckEnvironment(context.Context, any) (agentadaptor.EnvironmentReport, error)
	}).CheckEnvironment(context.Background(), Config{
		CommonConfig: CommonConfig{
			Env: []agentadaptor.EnvBinding{{Name: "HOME", Value: home}},
		},
	})
	if err != nil {
		t.Fatalf("check environment: %v", err)
	}
	assertCheckPresent(t, report.Checks, "claude_config_present")
	assertCheckPresent(t, report.Checks, "claude_config_model")
	assertCheckPresent(t, report.Checks, "claude_config_base_url")
}

func TestCheckEnvironmentReportsClaudeCredentialsFromConfigDir(t *testing.T) {
	configDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(configDir, ".credentials.json"), []byte(`{"claudeAiOauth":{"accessToken":"secret-token"}}`), 0o644); err != nil {
		t.Fatalf("write credentials: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "settings.json"), []byte("{\"model\":\"claude-sonnet-4\"}\n"), 0o644); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	report, err := any(adapter{}).(interface {
		CheckEnvironment(context.Context, any) (agentadaptor.EnvironmentReport, error)
	}).CheckEnvironment(context.Background(), Config{
		CommonConfig: CommonConfig{
			Env: []agentadaptor.EnvBinding{{Name: "CLAUDE_CONFIG_DIR", Value: configDir}},
		},
	})
	if err != nil {
		t.Fatalf("check environment: %v", err)
	}
	assertCheckPresent(t, report.Checks, "claude_credentials_present")
}

func TestCheckEnvironmentReportsClaudeBedrockMode(t *testing.T) {
	report, err := any(adapter{}).(interface {
		CheckEnvironment(context.Context, any) (agentadaptor.EnvironmentReport, error)
	}).CheckEnvironment(context.Background(), Config{
		CommonConfig: CommonConfig{
			Env: []agentadaptor.EnvBinding{
				{Name: "CLAUDE_CODE_USE_BEDROCK", Value: "true"},
				{Name: "AWS_REGION", Value: "us-east-1"},
			},
		},
	})
	if err != nil {
		t.Fatalf("check environment: %v", err)
	}
	assertCheckPresent(t, report.Checks, "claude_bedrock_auth")
	assertCheckWithDetail(t, report.Checks, "claude_bedrock_region", "us-east-1")
}

func TestCheckEnvironmentWarnsOnIncompatibleBedrockBindingModel(t *testing.T) {
	report, err := any(adapter{}).(interface {
		CheckEnvironment(context.Context, any) (agentadaptor.EnvironmentReport, error)
	}).CheckEnvironment(context.Background(), Config{
		CommonConfig: CommonConfig{
			Env: []agentadaptor.EnvBinding{{Name: "CLAUDE_CODE_USE_BEDROCK", Value: "true"}},
		},
		Model: "claude-sonnet-4",
	})
	if err != nil {
		t.Fatalf("check environment: %v", err)
	}
	assertCheckPresent(t, report.Checks, "claude_binding_model_ignored")
}

func TestDetectModelIgnoresIncompatibleBedrockBindingModel(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home := t.TempDir()
	configDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "settings.json"), []byte("{\"model\":\"us.anthropic.claude-sonnet-4-5-20250929-v2:0\"}\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	detected, err := any(adapter{}).(interface {
		DetectModel(context.Context, any, *agentadaptor.ProfileSelection) (*agentadaptor.DetectedModel, error)
	}).DetectModel(context.Background(), Config{
		CommonConfig: CommonConfig{
			Env: []agentadaptor.EnvBinding{
				{Name: "HOME", Value: home},
				{Name: "CLAUDE_CODE_USE_BEDROCK", Value: "true"},
			},
		},
		Model: "claude-sonnet-4",
	}, nil)
	if err != nil {
		t.Fatalf("detect model: %v", err)
	}
	if detected == nil || detected.Model != "us.anthropic.claude-sonnet-4-5-20250929-v2:0" || detected.Source != "config_file" {
		t.Fatalf("unexpected detected model: %#v", detected)
	}
}

func TestGetQuotaReadsClaudeOAuthUsage(t *testing.T) {
	configDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(configDir, ".credentials.json"), []byte(`{"claudeAiOauth":{"accessToken":"oauth-token"}}`), 0o644); err != nil {
		t.Fatalf("write credentials: %v", err)
	}

	previousClient := claudeQuotaHTTPClient
	claudeQuotaHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if got := r.Header.Get("Authorization"); got != "Bearer oauth-token" {
			t.Fatalf("unexpected auth header: %q", got)
		}
		if got := r.Header.Get("anthropic-beta"); got != "oauth-2025-04-20" {
			t.Fatalf("unexpected beta header: %q", got)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"five_hour":{"utilization":0.4,"resets_at":"2026-04-20T00:00:00Z"},"seven_day":{"utilization":72,"resets_at":"2026-04-26T00:00:00Z"},"extra_usage":{"is_enabled":true,"monthly_limit":2000,"used_credits":500,"utilization":0.25,"currency":"USD"}}`)),
		}, nil
	})}
	defer func() { claudeQuotaHTTPClient = previousClient }()

	previous := claudeQuotaUsageURL
	claudeQuotaUsageURL = "https://unit.test/claude-quota"
	defer func() { claudeQuotaUsageURL = previous }()

	report, err := any(adapter{}).(interface {
		GetQuota(context.Context, any, *agentadaptor.ProfileSelection) (agentadaptor.QuotaReport, error)
	}).GetQuota(context.Background(), Config{
		CommonConfig: CommonConfig{
			Env: []agentadaptor.EnvBinding{{Name: "CLAUDE_CONFIG_DIR", Value: configDir}},
		},
	}, nil)
	if err != nil {
		t.Fatalf("get quota: %v", err)
	}
	if !report.Available || report.Source != "anthropic_oauth_usage" || len(report.Windows) != 3 {
		t.Fatalf("unexpected quota report: %#v", report)
	}
	if report.Windows[0].UsedPercent == nil || *report.Windows[0].UsedPercent != 40 {
		t.Fatalf("unexpected current session window: %#v", report.Windows[0])
	}
	if report.Windows[2].ValueLabel != "USD 5.00 / USD 20.00" {
		t.Fatalf("unexpected extra usage window: %#v", report.Windows[2])
	}
}

func assertCheckPresent(t *testing.T, checks []agentadaptor.EnvironmentCheck, code string) {
	t.Helper()
	for _, check := range checks {
		if check.Code == code {
			return
		}
	}
	t.Fatalf("expected check %q in %#v", code, checks)
}

func assertCheckWithDetail(t *testing.T, checks []agentadaptor.EnvironmentCheck, code, detail string) {
	t.Helper()
	for _, check := range checks {
		if check.Code == code {
			if check.Detail != detail {
				t.Fatalf("expected %q detail %q, got %#v", code, detail, check)
			}
			return
		}
	}
	t.Fatalf("expected check %q in %#v", code, checks)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func schemaFieldByName(t *testing.T, schema *agentadaptor.ConfigSchema, name string) agentadaptor.ConfigField {
	t.Helper()
	for _, field := range schema.Fields {
		if field.Name == name {
			return field
		}
	}
	t.Fatalf("missing schema field %q in %#v", name, schema.Fields)
	return agentadaptor.ConfigField{}
}
