package codex

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
	"github.com/agent-dance/agent-adaptor/internal/testutil"
)

func TestDescriptorAdvertisesExpectedMCPCapabilities(t *testing.T) {
	caps := (adapter{}).Descriptor().MCP
	if !caps.Supported || !caps.Stdio || !caps.HTTP || caps.SSE {
		t.Fatalf("unexpected Codex MCP capability: %#v", caps)
	}
}

func TestDescriptorAdvertisesTruthfulRunPolicyCapabilities(t *testing.T) {
	caps := (adapter{}).Descriptor().RunPolicyCaps
	if !caps.Isolation || !caps.WebSearch || caps.Browser {
		t.Fatalf("unexpected Codex feature policy capabilities: %#v", caps)
	}
	if !caps.Permission.AutoApprove || caps.Permission.Ask || caps.Permission.AutoReject || caps.Permission.Retry {
		t.Fatalf("unexpected Codex permission capabilities: %#v", caps.Permission)
	}
	if caps.PlanReview.Ask || caps.PlanReview.AutoApprove || caps.PlanReview.AutoReject || caps.PlanReview.Retry {
		t.Fatalf("Codex has no independent plan-review control: %#v", caps.PlanReview)
	}
}

func TestDescriptorAdvertisesStructuredOutputCapabilities(t *testing.T) {
	caps := (adapter{}).Descriptor().StructuredOutput
	if !caps.JSONSchemaNative || !caps.JSONSchemaPromptValidate || !caps.WorksWithRun {
		t.Fatalf("unexpected Codex structured-output capability: %#v", caps)
	}
	if !caps.WorksWithStreaming {
		t.Fatalf("Codex app-server output-schema support is not advertised: %#v", caps)
	}
}

func TestParseCheckpointRequiresRecognizedCodexEvent(t *testing.T) {
	stdout := `{"type":"tool.result","thread_id":"ignore-me"}
{"type":"thread.started","thread_id":"codex-thread"}
{"type":"turn.completed"}`

	checkpoint := snapshotCodexStdout(stdout).checkpoint(0)
	if checkpoint == nil || checkpoint.State == nil {
		t.Fatal("expected checkpoint")
	}
	if checkpoint.State.ResumeID != "codex-thread" || checkpoint.State.DisplayID != "codex-thread" {
		t.Fatalf("unexpected checkpoint: %#v", checkpoint.State)
	}
}

func TestParseCheckpointRejectsSessionOnlyPayload(t *testing.T) {
	checkpoint := snapshotCodexStdout(`{"thread_id":"codex-thread"}`).checkpoint(0)
	if checkpoint != nil {
		t.Fatalf("session-only payload is not terminal proof: %#v", checkpoint)
	}
}

func TestParseCodexJSONLUsesThreadStartedAssistantOutputAndUsage(t *testing.T) {
	stdout := `{"type":"thread.started","thread_id":"thread-123"}
{"type":"item.completed","item":{"type":"agent_message","text":"First update"}}
{"type":"item.completed","item":{"type":"agent_message","text":"Final answer"}}
{"type":"turn.completed","usage":{"input_tokens":12,"cached_input_tokens":4,"output_tokens":7}}`

	parsed := snapshotCodexStdout(stdout)
	checkpoint := parsed.checkpoint(0)
	if checkpoint == nil || checkpoint.State == nil || checkpoint.State.ResumeID != "thread-123" || checkpoint.State.DisplayID != "thread-123" {
		t.Fatalf("unexpected parsed checkpoint=%#v", checkpoint)
	}
	if parsed.buildOutput() != "Final answer" {
		t.Fatalf("unexpected assistant output: %q", parsed.buildOutput())
	}
	if parsed.usage == nil || parsed.usage.InputTokens != 12 || parsed.usage.CachedInputTokens != 4 || parsed.usage.OutputTokens != 7 {
		t.Fatalf("unexpected usage parsing: %#v", parsed.usage)
	}
}

func TestCodexNativeStructuredOutputUsesLastCompletedAgentMessageAndCoreValidation(t *testing.T) {
	parsed := snapshotCodexStdout(`{"type":"thread.started","thread_id":"thread-structured"}
{"type":"item.completed","item":{"id":"msg-1","type":"agent_message","text":"{\"project_name\":\"draft\"}"}}
{"type":"item.completed","item":{"id":"msg-2","type":"agent_message","text":"{\"project_name\":\"agent-adaptor\"}"}}
{"type":"turn.completed","usage":{"input_tokens":4,"output_tokens":2}}`)

	candidate := parsed.nativeStructuredOutputForOutcome(0, "", false, nil)
	if candidate == nil || string(candidate.RawJSON) != `{"project_name":"agent-adaptor"}` {
		t.Fatalf("expected last completed agent_message, got %#v", candidate)
	}
	if candidate.Valid {
		t.Fatal("Driver parser must not claim schema validity before core validation")
	}
	schema := &driver.OutputSchema{
		Format:     driver.OutputFormatJSONSchema,
		SchemaJSON: json.RawMessage(`{"type":"object","properties":{"project_name":{"const":"agent-adaptor"}},"required":["project_name"],"additionalProperties":false}`),
		OnInvalid:  driver.StructuredOutputFailRun,
	}
	validated, failure := engine.FinalizeStructuredOutput(schema, driver.StructuredOutputSourceNative, parsed.buildOutput(), candidate, nil)
	if failure != nil || validated == nil || !validated.Valid || string(validated.RawJSON) != `{"project_name":"agent-adaptor"}` {
		t.Fatalf("expected core-validated native output, value=%#v failure=%#v", validated, failure)
	}
	if got := parsed.buildOutput(); got != `{"project_name":"agent-adaptor"}` {
		t.Fatalf("assistant Output must use only the final completed message, got %q", got)
	}
	if parsed.terminal == nil || parsed.terminal.Event != "turn.completed" || strings.Contains(string(parsed.terminal.JSON), "project_name") {
		t.Fatalf("terminal Result must remain the official turn.completed envelope, got %#v", parsed.terminal)
	}
}

func TestCodexForkAlwaysSelectsAppServerTransport(t *testing.T) {
	if usesCodexAppServer(driver.Request{}) {
		t.Fatal("stateless batch run unexpectedly selected app-server")
	}
	if !usesCodexAppServer(driver.Request{Streaming: true}) {
		t.Fatal("streaming run did not select app-server")
	}
	if !usesCodexAppServer(driver.Request{Session: &driver.SessionContext{
		Mode:  driver.SessionFork,
		State: &driver.SessionState{ResumeID: "parent"},
	}}) {
		t.Fatal("non-streaming fork must select app-server thread/fork")
	}
	if usesCodexAppServer(driver.Request{Session: &driver.SessionContext{
		Mode:  driver.SessionContinueOnly,
		State: &driver.SessionState{ResumeID: "parent"},
	}}) {
		t.Fatal("ordinary batch resume unexpectedly selected app-server")
	}
}

func TestCodexForkMapsOnlyParentToAppServerForkSelector(t *testing.T) {
	parent := &driver.SessionContext{
		Mode:  driver.SessionFork,
		State: &driver.SessionState{ResumeID: "parent"},
	}
	resumeID, forkID := codexAppServerThreadIDs(parent)
	if resumeID != "" || forkID != "parent" {
		t.Fatalf("fork selectors = resume %q fork %q", resumeID, forkID)
	}
	continued := &driver.SessionContext{
		Mode:  driver.SessionContinueOnly,
		State: &driver.SessionState{ResumeID: "continued"},
	}
	resumeID, forkID = codexAppServerThreadIDs(continued)
	if resumeID != "continued" || forkID != "" {
		t.Fatalf("continue selectors = resume %q fork %q", resumeID, forkID)
	}
}

func TestCodexAppServerPreservesConstructedModelEffortAndFastMode(t *testing.T) {
	model, effort, serviceTier := codexAppServerConfigProjection(Config{
		Model:           "  gpt-test  ",
		ReasoningEffort: "xhigh",
		FastMode:        true,
	})
	if model != "gpt-test" || effort != "xhigh" || serviceTier != "fast" {
		t.Fatalf("app-server config projection = model %q effort %q tier %q", model, effort, serviceTier)
	}
}

func TestCodexForkRejectsMissingParentBeforeProcessLaunch(t *testing.T) {
	_, err := (adapter{}).Run(context.Background(), driver.Request{
		Config:  Config{CommonConfig: CommonConfig{Command: filepath.Join(t.TempDir(), "must-not-launch")}},
		Session: &driver.SessionContext{Mode: driver.SessionFork},
	}, &testutil.EventRecorder{})
	if !errors.Is(err, engine.ErrResumeRejected) {
		t.Fatalf("error = %v, want ErrResumeRejected", err)
	}
}

func TestCodexRunAddsOutputSchemaArgAndParsesStructuredResult(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	argFile := filepath.Join(home, "args.txt")
	command := testutil.WriteCommand(t, home, "fake-codex-structured",
		"#!/bin/sh\nset -eu\nprintf '%s\\n' \"$@\" > \"$ARG_FILE\"\nschema=''\nwhile [ \"$#\" -gt 0 ]; do\n  if [ \"$1\" = '--output-schema' ]; then shift; schema=\"$1\"; break; fi\n  shift\ndone\n[ -s \"$schema\" ]\ncat >/dev/null\nprintf '%s\\n' '{\"type\":\"thread.started\",\"thread_id\":\"codex-session\"}'\nprintf '%s\\n' '{\"type\":\"item.completed\",\"item\":{\"id\":\"msg-1\",\"type\":\"agent_message\",\"text\":\"{\\\"project_name\\\":\\\"agent-adaptor\\\"}\"}}'\nprintf '%s\\n' '{\"type\":\"turn.completed\",\"usage\":{\"input_tokens\":4,\"output_tokens\":2}}'\n",
		"@echo off\r\nsetlocal enabledelayedexpansion\r\n> \"%ARG_FILE%\" echo %*\r\nset \"NEXT=\"\r\nset \"SCHEMA=\"\r\nfor %%A in (%*) do (\r\n  if defined NEXT (\r\n    set \"SCHEMA=%%~A\"\r\n    set \"NEXT=\"\r\n  )\r\n  if \"%%~A\"==\"--output-schema\" set \"NEXT=1\"\r\n)\r\nif not exist \"!SCHEMA!\" exit /b 3\r\nfor %%S in (\"!SCHEMA!\") do if %%~zS LEQ 0 exit /b 4\r\necho {\"type\":\"thread.started\",\"thread_id\":\"codex-session\"}\r\necho {\"type\":\"item.completed\",\"item\":{\"id\":\"msg-1\",\"type\":\"agent_message\",\"text\":\"{\\\"project_name\\\":\\\"agent-adaptor\\\"}\"}}\r\necho {\"type\":\"turn.completed\",\"usage\":{\"input_tokens\":4,\"output_tokens\":2}}\r\n",
	)

	result, err := (adapter{}).Run(context.Background(), driver.Request{
		Prompt:                 "extract",
		Config:                 Config{CommonConfig: CommonConfig{Command: command, CWD: workspace, Env: []driver.EnvBinding{{Name: "HOME", Value: home}, {Name: "USERPROFILE", Value: home}, {Name: "CODEX_HOME", Value: filepath.Join(home, ".codex")}, {Name: "ARG_FILE", Value: argFile}}}},
		Workspace:              driver.WorkspaceLease{ID: "workspace-a", CWD: workspace},
		StructuredOutputSource: driver.StructuredOutputSourceNative,
		OutputSchema: &driver.OutputSchema{
			Format:     driver.OutputFormatJSONSchema,
			SchemaJSON: []byte(`{"type":"object","properties":{"project_name":{"type":"string"}},"required":["project_name"]}`),
		},
	}, &testutil.EventRecorder{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	rawArgs, err := os.ReadFile(argFile)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	if !strings.Contains(string(rawArgs), "--output-schema") {
		t.Fatalf("expected --output-schema in args, got %s", string(rawArgs))
	}
	if result.StructuredOutput == nil || string(result.StructuredOutput.RawJSON) != `{"project_name":"agent-adaptor"}` {
		t.Fatalf("expected native structured output, got %#v (output=%q raw=%#v transcript=%#v failure=%#v)", result.StructuredOutput, result.Output, result.RawStreams, result.Transcript, result.Failure)
	}
	if !result.StructuredOutput.Valid || len(result.StructuredOutput.ValidationErrors) != 0 || result.StructuredOutput.SchemaHash == "" {
		t.Fatalf("expected direct Driver response to use core schema validation, got %#v", result.StructuredOutput)
	}
	if result.Output != `{"project_name":"agent-adaptor"}` {
		t.Fatalf("expected assistant Text to remain distinct, got %q", result.Output)
	}
	if result.RawStreams == nil || result.RawStreams.Terminal == nil || result.RawStreams.Terminal.Event != "turn.completed" {
		t.Fatalf("expected official terminal payload, got %#v", result.RawStreams)
	}
	if len(result.Transcript) < 2 || result.Transcript[len(result.Transcript)-2].Kind != driver.TranscriptAssistant || result.Transcript[len(result.Transcript)-1].Kind != driver.TranscriptResult {
		t.Fatalf("expected assistant/result transcript layers, got %#v", result.Transcript)
	}
}

func TestCodexRunMissingNativeOutputFailsAndDoesNotCheckpoint(t *testing.T) {
	home := t.TempDir()
	command := testutil.WriteCommand(t, home, "fake-codex-missing-structured",
		"#!/bin/sh\nset -eu\ncat >/dev/null\nprintf '{\"type\":\"thread.started\",\"thread_id\":\"codex-missing-output\"}\\n'\nprintf '{\"type\":\"turn.completed\"}\\n'\n",
		"@echo off\r\nsetlocal\r\nset /p PROMPT=\r\necho {\"type\":\"thread.started\",\"thread_id\":\"codex-missing-output\"}\r\necho {\"type\":\"turn.completed\"}\r\n",
	)
	result, err := (adapter{}).Run(context.Background(), driver.Request{
		Prompt:                 "extract",
		Config:                 Config{CommonConfig: CommonConfig{Command: command, CWD: home}},
		Workspace:              driver.WorkspaceLease{CWD: home},
		StructuredOutputSource: driver.StructuredOutputSourceNative,
		OutputSchema: &driver.OutputSchema{
			Format:     driver.OutputFormatJSONSchema,
			SchemaJSON: json.RawMessage(`{"type":"object"}`),
			OnInvalid:  driver.StructuredOutputFailRun,
		},
	}, &testutil.EventRecorder{})
	if err != nil {
		t.Fatalf("Driver.Run error = %v", err)
	}
	if result.StructuredOutput == nil || result.StructuredOutput.Valid || len(result.StructuredOutput.ValidationErrors) == 0 {
		t.Fatalf("missing native value = %#v", result.StructuredOutput)
	}
	if result.Failure == nil || result.Failure.Code != driver.FailurePolicyError {
		t.Fatalf("Failure = %#v, want policy error", result.Failure)
	}
	if result.Checkpoint != nil {
		t.Fatalf("missing native output polluted checkpoint: %#v", result.Checkpoint)
	}
	if result.RawStreams == nil || result.RawStreams.Terminal == nil || result.RawStreams.Terminal.Event != "turn.completed" {
		t.Fatalf("raw terminal lost: %#v", result.RawStreams)
	}
}

func TestIsCodexUnknownSessionErrorMatchesPaperclipPatterns(t *testing.T) {
	if !isCodexUnknownSessionError("", "Error: thread/resume: thread/resume failed: no rollout found for thread id d448e715-7607-4bcc-91fc-7a3c0c5a9632") {
		t.Fatal("expected missing-rollout thread error to be classified as unknown session")
	}
	if isCodexUnknownSessionError("", "model overloaded") {
		t.Fatal("did not expect unrelated codex failure to be classified as unknown session")
	}
}

func TestDetectModelFallsBackToCodexConfigFile(t *testing.T) {
	t.Setenv("CODEX_HOME", "")
	home := t.TempDir()
	configDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte("model = \"gpt-5.3-codex\"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	detected, err := (adapter{}).DetectModel(context.Background(), Config{
		CommonConfig: CommonConfig{
			Env: []driver.EnvBinding{{Name: "HOME", Value: home}},
		},
	}, nil)
	if err != nil {
		t.Fatalf("detect model: %v", err)
	}
	if detected == nil || detected.Model != "gpt-5.3-codex" || detected.Source != "config_file" {
		t.Fatalf("unexpected detected model: %#v", detected)
	}
}

func TestDetectModelUsesExplicitProfileOptionAsCodexHome(t *testing.T) {
	profileDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(profileDir, "config.toml"), []byte("model = \"gpt-5.4\"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	detected, err := (adapter{}).DetectModel(context.Background(), Config{
		CommonConfig: CommonConfig{
			Env: []driver.EnvBinding{{Name: "CODEX_HOME", Value: profileDir}},
		},
	}, nil)
	if err != nil {
		t.Fatalf("detect model: %v", err)
	}
	if detected == nil || detected.Model != "gpt-5.4" || detected.Source != "config_file" {
		t.Fatalf("unexpected detected model: %#v", detected)
	}
}

func TestDetectModelPrefersExplicitCodexHomeOverProfileOption(t *testing.T) {
	overrideDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(overrideDir, "config.toml"), []byte("model = \"gpt-5.3-codex\"\n"), 0o644); err != nil {
		t.Fatalf("write override config: %v", err)
	}

	detected, err := (adapter{}).DetectModel(context.Background(), Config{
		CommonConfig: CommonConfig{
			Env: []driver.EnvBinding{
				{Name: "CODEX_HOME", Value: overrideDir},
			},
		},
	}, nil)
	if err != nil {
		t.Fatalf("detect model: %v", err)
	}
	if detected == nil || detected.Model != "gpt-5.3-codex" || detected.Source != "config_file" {
		t.Fatalf("unexpected detected model: %#v", detected)
	}
}

func TestGetProfileReturnsManagedCodexHomeWhenUnset(t *testing.T) {
	profile, err := (adapter{}).GetProfile(context.Background(), Config{}, driver.AgentIdentity{TenantID: "examples"}, nil)
	if err != nil {
		t.Fatalf("get profile: %v", err)
	}
	if !profile.Supported || !profile.Managed || profile.Source != driver.AgentProfileSourceManaged || profile.EnvVar != "CODEX_HOME" {
		t.Fatalf("unexpected profile metadata: %#v", profile)
	}
	if want := resolveManagedCodexHome(driver.AgentIdentity{TenantID: "examples"}); profile.Dir != want {
		t.Fatalf("expected managed home %q, got %#v", want, profile)
	}
}

func TestGetProfileUsesExplicitCodexHomeOverProfileOption(t *testing.T) {
	overrideDir := t.TempDir()
	profile, err := (adapter{}).GetProfile(context.Background(), Config{
		CommonConfig: CommonConfig{
			Env: []driver.EnvBinding{
				{Name: "CODEX_HOME", Value: overrideDir},
			},
		},
	}, driver.AgentIdentity{}, nil)
	if err != nil {
		t.Fatalf("get profile: %v", err)
	}
	if profile.Dir != overrideDir || profile.Source != driver.AgentProfileSourceBindingEnv || profile.Managed {
		t.Fatalf("unexpected profile: %#v", profile)
	}
}

func TestGetProfileCloneCanShareNativeCodexAuth(t *testing.T) {
	t.Setenv("CODEX_HOME", "")
	home := t.TempDir()
	nativeProfile := filepath.Join(home, ".codex")
	if err := os.MkdirAll(nativeProfile, 0o755); err != nil {
		t.Fatalf("mkdir native profile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nativeProfile, "config.toml"), []byte("model_provider = 'codex-lb'\n"), 0o644); err != nil {
		t.Fatalf("write native config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nativeProfile, "auth.json"), []byte(`{"tokens":{"access_token":"native"}}`), 0o600); err != nil {
		t.Fatalf("write native auth: %v", err)
	}
	target := filepath.Join(t.TempDir(), "isolated")

	profile, err := (adapter{}).GetProfile(context.Background(), Config{
		CommonConfig: CommonConfig{
			Env: []driver.EnvBinding{{Name: "HOME", Value: home}, {Name: "USERPROFILE", Value: home}},
		},
	}, driver.AgentIdentity{}, &driver.ProfileSelection{
		Mode: driver.ProfileModeClone,
		Dir:  target,
		Clone: &driver.CloneProfileOptions{
			IncludeSettings: true,
			AuthMode:        driver.CloneProfileAuthLink,
		},
	})
	if err != nil {
		t.Fatalf("get profile: %v", err)
	}
	if profile.Dir != target || profile.Source != driver.AgentProfileSourceProfileOption {
		t.Fatalf("unexpected profile: %#v", profile)
	}
	rawConfig, err := os.ReadFile(filepath.Join(target, "config.toml"))
	if err != nil {
		t.Fatalf("read cloned config: %v", err)
	}
	if !strings.Contains(string(rawConfig), "codex-lb") {
		t.Fatalf("expected cloned Codex settings, got %s", string(rawConfig))
	}
	sourceInfo, sourceErr := os.Stat(filepath.Join(nativeProfile, "auth.json"))
	targetInfo, targetErr := os.Stat(filepath.Join(target, "auth.json"))
	if sourceErr != nil || targetErr != nil {
		t.Fatalf("stat auth files: source=%v target=%v", sourceErr, targetErr)
	}
	if !os.SameFile(sourceInfo, targetInfo) {
		t.Fatalf("expected cloned profile auth.json to share native Codex auth.json")
	}
}

func TestConfigSchemaIncludesGroupsDefaultsAndOptions(t *testing.T) {
	schema := (adapter{}).Descriptor().ConfigSchema
	if schema == nil || len(schema.Fields) == 0 {
		t.Fatalf("expected config schema, got %#v", schema)
	}
	modelField := schemaFieldByName(t, schema, "model")
	if modelField.Name != "model" || modelField.Group != "model" || len(modelField.Options) == 0 || modelField.Default != "gpt-5.4" {
		t.Fatalf("unexpected model field: %#v", modelField)
	}
	commandField := schemaFieldByName(t, schema, "command")
	if commandField.Name != "command" || commandField.Group != "command" || commandField.Default != "codex" {
		t.Fatalf("unexpected command field: %#v", commandField)
	}
}

func TestCheckEnvironmentReportsConfigFileState(t *testing.T) {
	t.Setenv("CODEX_HOME", "")
	home := t.TempDir()
	configDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte("model = \"gpt-5.3-codex\"\nmodel_provider = \"codex-lb\"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	report, err := (adapter{}).CheckEnvironment(context.Background(), Config{
		CommonConfig: CommonConfig{
			Env: []driver.EnvBinding{{Name: "HOME", Value: home}},
		},
	})
	if err != nil {
		t.Fatalf("check environment: %v", err)
	}
	assertCheckPresent(t, report.Checks, "codex_config_present")
	assertCheckPresent(t, report.Checks, "codex_config_model")
	assertCheckPresent(t, report.Checks, "codex_config_provider")
}

func TestCheckEnvironmentReportsCodexAuthMetadata(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	home := t.TempDir()
	configDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	idToken := testJWT(t, map[string]any{
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_user_email": "dev@example.com",
			"chatgpt_plan_type":  "pro",
		},
	})
	auth := `{"tokens":{"access_token":"token-value","id_token":"` + idToken + `","account_id":"acct-123"},"last_refresh":"2026-04-19T12:00:00Z"}`
	if err := os.WriteFile(filepath.Join(configDir, "auth.json"), []byte(auth), 0o644); err != nil {
		t.Fatalf("write auth: %v", err)
	}

	report, err := (adapter{}).CheckEnvironment(context.Background(), Config{
		CommonConfig: CommonConfig{
			Env: []driver.EnvBinding{{Name: "HOME", Value: home}},
		},
	})
	if err != nil {
		t.Fatalf("check environment: %v", err)
	}
	assertCheckPresent(t, report.Checks, "codex_auth_present")
	assertCheckWithDetail(t, report.Checks, "codex_auth_email", "dev@example.com")
	assertCheckWithDetail(t, report.Checks, "codex_auth_plan", "pro")
	assertCheckWithDetail(t, report.Checks, "codex_auth_account", "acct-123")
	assertCheckWithDetail(t, report.Checks, "codex_auth_last_refresh", "2026-04-19T12:00:00Z")
}

func TestGetQuotaReadsCodexWHAMUsage(t *testing.T) {
	t.Setenv("CODEX_HOME", "")
	home := t.TempDir()
	configDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	auth := `{"tokens":{"access_token":"token-value","account_id":"acct-123"}}`
	if err := os.WriteFile(filepath.Join(configDir, "auth.json"), []byte(auth), 0o644); err != nil {
		t.Fatalf("write auth: %v", err)
	}

	previousClient := codexQuotaHTTPClient
	codexQuotaHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if got := r.Header.Get("Authorization"); got != "Bearer token-value" {
			t.Fatalf("unexpected auth header: %q", got)
		}
		if got := r.Header.Get("ChatGPT-Account-Id"); got != "acct-123" {
			t.Fatalf("unexpected account header: %q", got)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"rate_limit":{"primary_window":{"used_percent":0.42,"reset_at":"2026-04-20T00:00:00Z"},"secondary_window":{"used_percent":72,"reset_at":1770000000}},"credits":{"unlimited":false,"balance":1234}}`)),
		}, nil
	})}
	defer func() { codexQuotaHTTPClient = previousClient }()

	previous := codexQuotaUsageURL
	codexQuotaUsageURL = "https://unit.test/codex-quota"
	defer func() { codexQuotaUsageURL = previous }()

	report, err := (adapter{}).GetQuota(context.Background(), Config{
		CommonConfig: CommonConfig{
			Env: []driver.EnvBinding{{Name: "HOME", Value: home}},
		},
	}, nil)
	if err != nil {
		t.Fatalf("get quota: %v", err)
	}
	if !report.Available || report.Source != "codex_wham" || len(report.Windows) != 3 {
		t.Fatalf("unexpected quota report: %#v", report)
	}
	if report.Windows[0].UsedPercent == nil || *report.Windows[0].UsedPercent != 42 {
		t.Fatalf("unexpected primary window: %#v", report.Windows[0])
	}
	if report.Windows[2].ValueLabel != "$12.34 remaining" {
		t.Fatalf("unexpected credits window: %#v", report.Windows[2])
	}
}

func testJWT(t *testing.T, payload map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal jwt payload: %v", err)
	}
	return "header." + base64.RawURLEncoding.EncodeToString(raw) + ".sig"
}

func assertCheckPresent(t *testing.T, checks []driver.EnvironmentCheck, code string) {
	t.Helper()
	for _, check := range checks {
		if check.Code == code {
			return
		}
	}
	t.Fatalf("expected check %q in %#v", code, checks)
}

func assertCheckWithDetail(t *testing.T, checks []driver.EnvironmentCheck, code, detail string) {
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

func schemaFieldByName(t *testing.T, schema *driver.ConfigSchema, name string) driver.ConfigField {
	t.Helper()
	for _, field := range schema.Fields {
		if field.Name == name {
			return field
		}
	}
	t.Fatalf("missing schema field %q in %#v", name, schema.Fields)
	return driver.ConfigField{}
}

func TestGetQuotaUsesProfileSelection(t *testing.T) {
	profileDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(profileDir, "auth.json"), []byte(`{"tokens":{"access_token":"token"}}`), 0o644); err != nil {
		t.Fatalf("write auth: %v", err)
	}
	report, err := (adapter{}).GetQuota(context.Background(), Config{}, &driver.ProfileSelection{Mode: driver.ProfileModeDedicated, Dir: profileDir})
	if err != nil {
		t.Fatalf("get quota: %v", err)
	}
	if report.Error == "no local Codex auth token found in auth.json" {
		t.Fatalf("quota probe ignored selected profile: %#v", report)
	}
}
