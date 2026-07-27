package claude

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentadaptor "github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/testutil"
	adaptor "github.com/agent-dance/agent-adaptor"
)

func TestBuildClaudeExecArgsIncludesPartialMessagesWhenStreaming(t *testing.T) {
	cfg := Config{Model: "claude-sonnet-4"}
	req := agentadaptor.Request{Streaming: true}
	args, err := buildClaudeExecArgs(cfg, req, "", false)
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

	argsBatch, err := buildClaudeExecArgs(cfg, agentadaptor.Request{Streaming: false}, "", false)
	if err != nil {
		t.Fatalf("build batch args: %v", err)
	}
	for _, a := range argsBatch {
		if a == "--include-partial-messages" {
			t.Fatalf("batch path must not add partial flag: %#v", argsBatch)
		}
	}
}

func TestBuildClaudeExecArgsUsesJSONSchemaForNativeStructuredOutput(t *testing.T) {
	schema := []byte(`{"type":"object","properties":{"project_name":{"type":"string"}}}`)
	args, err := buildClaudeExecArgs(Config{Model: "claude-sonnet-4"}, agentadaptor.Request{
		OutputSchema: &agentadaptor.OutputSchema{
			Format:     agentadaptor.OutputFormatJSONSchema,
			Mode:       agentadaptor.StructuredOutputNativeStrict,
			SchemaJSON: schema,
		},
	}, "", false)
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
	}, "", false)
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
	}, "", false)
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
	}, "", false)
	if !errors.Is(err, agentadaptor.ErrInvalidOutputSchema) || !strings.Contains(err.Error(), "recursive local reference") {
		t.Fatalf("error = %v, want recursive ErrInvalidOutputSchema", err)
	}
}

func TestBuildClaudeExecArgsPreservesDefinitionsProperty(t *testing.T) {
	schema := []byte(`{"type":"object","properties":{"definitions":{"type":"string"}},"required":["definitions"],"additionalProperties":false}`)
	args, err := buildClaudeExecArgs(Config{}, agentadaptor.Request{
		OutputSchema: &agentadaptor.OutputSchema{Format: agentadaptor.OutputFormatJSONSchema, Mode: agentadaptor.StructuredOutputNativeStrict, SchemaJSON: schema},
	}, "", false)
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
	}, "", false)
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
	}, "", false)
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
	args, err := buildClaudeExecArgs(cfg, agentadaptor.Request{}, "", true)
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
{"type":"result","subtype":"success","session_id":"claude-session","display_id":"claude-display"}`

	checkpoint := parseCheckpoint(stdout, 0)
	if checkpoint == nil || checkpoint.State == nil {
		t.Fatal("expected checkpoint")
	}
	if checkpoint.State.ResumeID != "claude-session" || checkpoint.State.DisplayID != "claude-display" {
		t.Fatalf("unexpected checkpoint: %#v", checkpoint.State)
	}
}

func TestParseClaudeResultStructuredOutput(t *testing.T) {
	parsed := snapshotClaudeStdout(`{"type":"result","subtype":"success","structured_output":{"project_name":"agent-adaptor"},"result":"ok"}`)
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
printf '%s\n' '{"type":"result","subtype":"success","session_id":"sess-structured","structured_output":{"project_name":"agent-adaptor"},"result":"done"}'
`,
		"@echo off\r\nmore > nul\r\necho {\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"sess-structured\",\"model\":\"claude-fixture\"}\r\necho {\"type\":\"stream_event\",\"session_id\":\"sess-structured\",\"event\":{\"type\":\"message_start\",\"message\":{\"id\":\"msg-1\"}}}\r\necho {\"type\":\"stream_event\",\"session_id\":\"sess-structured\",\"event\":{\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\"}}}\r\necho {\"type\":\"stream_event\",\"session_id\":\"sess-structured\",\"event\":{\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"done\"}}}\r\necho {\"type\":\"stream_event\",\"session_id\":\"sess-structured\",\"event\":{\"type\":\"content_block_stop\",\"index\":0}}\r\necho {\"type\":\"assistant\",\"session_id\":\"sess-structured\",\"message\":{\"model\":\"claude-fixture\",\"content\":[{\"type\":\"text\",\"text\":\"done\"}]}}\r\necho {\"type\":\"result\",\"subtype\":\"success\",\"session_id\":\"sess-structured\",\"structured_output\":{\"project_name\":\"agent-adaptor\"},\"result\":\"done\"}\r\n",
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
