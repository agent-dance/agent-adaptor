package claude

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

func TestBuildClaudeExecArgsIncludesPartialMessagesWhenStreaming(t *testing.T) {
	cfg := agentadaptor.ClaudeConfig{Model: "claude-sonnet-4"}
	req := agentadaptor.DriverRunRequest{Streaming: true}
	args := buildClaudeExecArgs(cfg, req, "", false)
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

	argsBatch := buildClaudeExecArgs(cfg, agentadaptor.DriverRunRequest{Streaming: false}, "", false)
	for _, a := range argsBatch {
		if a == "--include-partial-messages" {
			t.Fatalf("batch path must not add partial flag: %#v", argsBatch)
		}
	}
}

func TestDescriptorAdvertisesExpectedMCPCapabilities(t *testing.T) {
	caps := NewAdapter().Descriptor().MCP
	if !caps.Supported || !caps.Stdio || !caps.HTTP || !caps.SSE {
		t.Fatalf("unexpected Claude MCP capability: %#v", caps)
	}
}

func TestBuildClaudeExecArgsInteractiveEnablesStdioPermissionPrompt(t *testing.T) {
	cfg := agentadaptor.ClaudeConfig{Model: "claude-sonnet-4"}
	args := buildClaudeExecArgs(cfg, agentadaptor.DriverRunRequest{}, "", true)
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
	cap := NewAdapter().(interface {
		StreamCapability() agentadaptor.StreamCapability
	}).StreamCapability()
	if !cap.Native || !cap.TokenLevel || !cap.Reasoning || !cap.ToolCallArgs || cap.HITL {
		t.Fatalf("unexpected capability: %#v", cap)
	}
}

func TestNewReturnsTypedBinding(t *testing.T) {
	binding := New(agentadaptor.ClaudeConfig{Model: "claude-sonnet-4"})
	if binding.TypedConfig().Model != "claude-sonnet-4" {
		t.Fatalf("expected typed config model to round-trip, got %#v", binding.TypedConfig())
	}
}

func TestParseCheckpointRequiresRecognizedClaudeEvent(t *testing.T) {
	stdout := `{"type":"tool.result","session_id":"ignore-me"}
{"event":"turn.completed","session_id":"claude-session","display_id":"claude-display"}`

	checkpoint := parseCheckpoint(stdout, 0)
	if checkpoint == nil || checkpoint.State == nil {
		t.Fatal("expected checkpoint")
	}
	if checkpoint.State.ResumeID != "claude-session" || checkpoint.State.DisplayID != "claude-display" {
		t.Fatalf("unexpected checkpoint: %#v", checkpoint.State)
	}
}

func TestParseCheckpointAcceptsSessionOnlyPayload(t *testing.T) {
	checkpoint := parseCheckpoint(`{"session_id":"claude-session"}`, 0)
	if checkpoint == nil || checkpoint.State == nil || checkpoint.State.ResumeID != "claude-session" {
		t.Fatalf("unexpected checkpoint: %#v", checkpoint)
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

	detected, err := NewAdapter().(interface {
		DetectModel(context.Context, any, *agentadaptor.ProfileSelection) (*agentadaptor.DetectedModel, error)
	}).DetectModel(context.Background(), agentadaptor.ClaudeConfig{
		CommonConfig: agentadaptor.CommonConfig{
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

	detected, err := NewAdapter().(interface {
		DetectModel(context.Context, any, *agentadaptor.ProfileSelection) (*agentadaptor.DetectedModel, error)
	}).DetectModel(context.Background(), agentadaptor.ClaudeConfig{
		CommonConfig: agentadaptor.CommonConfig{
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

	detected, err := NewAdapter().(interface {
		DetectModel(context.Context, any, *agentadaptor.ProfileSelection) (*agentadaptor.DetectedModel, error)
	}).DetectModel(context.Background(), agentadaptor.ClaudeConfig{
		CommonConfig: agentadaptor.CommonConfig{
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
	profile, err := NewAdapter().(interface {
		GetProfile(context.Context, any, agentadaptor.AgentIdentity, *agentadaptor.ProfileSelection) (agentadaptor.AgentProfile, error)
	}).GetProfile(context.Background(), agentadaptor.ClaudeConfig{
		CommonConfig: agentadaptor.CommonConfig{
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

	profile, err := NewAdapter().(interface {
		GetProfile(context.Context, any, agentadaptor.AgentIdentity, *agentadaptor.ProfileSelection) (agentadaptor.AgentProfile, error)
	}).GetProfile(context.Background(), agentadaptor.ClaudeConfig{}, agentadaptor.AgentIdentity{}, nil)
	if err != nil {
		t.Fatalf("get profile: %v", err)
	}
	if !profile.Supported || profile.Dir != profileDir || profile.Source != agentadaptor.AgentProfileSourceProcessEnv {
		t.Fatalf("unexpected profile: %#v", profile)
	}
}

func TestConfigSchemaIncludesGroupsDefaultsAndOptions(t *testing.T) {
	schema := NewAdapter().Descriptor().ConfigSchema
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
	models, err := NewAdapter().(interface {
		ListModels(context.Context, any) ([]agentadaptor.ModelInfo, error)
	}).ListModels(context.Background(), agentadaptor.ClaudeConfig{
		CommonConfig: agentadaptor.CommonConfig{
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
	schema, err := NewAdapter().(interface {
		ConfigSchema(context.Context, any) (*agentadaptor.ConfigSchema, error)
	}).ConfigSchema(context.Background(), agentadaptor.ClaudeConfig{
		CommonConfig: agentadaptor.CommonConfig{
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

	report, err := NewAdapter().(interface {
		CheckEnvironment(context.Context, any) (agentadaptor.EnvironmentReport, error)
	}).CheckEnvironment(context.Background(), agentadaptor.ClaudeConfig{
		CommonConfig: agentadaptor.CommonConfig{
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

	report, err := NewAdapter().(interface {
		CheckEnvironment(context.Context, any) (agentadaptor.EnvironmentReport, error)
	}).CheckEnvironment(context.Background(), agentadaptor.ClaudeConfig{
		CommonConfig: agentadaptor.CommonConfig{
			Env: []agentadaptor.EnvBinding{{Name: "CLAUDE_CONFIG_DIR", Value: configDir}},
		},
	})
	if err != nil {
		t.Fatalf("check environment: %v", err)
	}
	assertCheckPresent(t, report.Checks, "claude_credentials_present")
}

func TestCheckEnvironmentReportsClaudeBedrockMode(t *testing.T) {
	report, err := NewAdapter().(interface {
		CheckEnvironment(context.Context, any) (agentadaptor.EnvironmentReport, error)
	}).CheckEnvironment(context.Background(), agentadaptor.ClaudeConfig{
		CommonConfig: agentadaptor.CommonConfig{
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
	report, err := NewAdapter().(interface {
		CheckEnvironment(context.Context, any) (agentadaptor.EnvironmentReport, error)
	}).CheckEnvironment(context.Background(), agentadaptor.ClaudeConfig{
		CommonConfig: agentadaptor.CommonConfig{
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

	detected, err := NewAdapter().(interface {
		DetectModel(context.Context, any, *agentadaptor.ProfileSelection) (*agentadaptor.DetectedModel, error)
	}).DetectModel(context.Background(), agentadaptor.ClaudeConfig{
		CommonConfig: agentadaptor.CommonConfig{
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

	report, err := NewAdapter().(interface {
		GetQuota(context.Context, any, *agentadaptor.ProfileSelection) (agentadaptor.QuotaReport, error)
	}).GetQuota(context.Background(), agentadaptor.ClaudeConfig{
		CommonConfig: agentadaptor.CommonConfig{
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
