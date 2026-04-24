package cursor

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

func TestNewReturnsTypedBinding(t *testing.T) {
	binding := New(agentadaptor.CursorConfig{Model: "gpt-5"})
	if binding.TypedConfig().Model != "gpt-5" {
		t.Fatalf("expected typed config model to round-trip, got %#v", binding.TypedConfig())
	}
}

func TestDescriptorAdvertisesExpectedMCPCapabilities(t *testing.T) {
	caps := NewAdapter().Descriptor().MCP
	if !caps.Supported || !caps.Stdio || !caps.HTTP || !caps.SSE {
		t.Fatalf("unexpected Cursor MCP capability: %#v", caps)
	}
}

func TestParseCheckpointRequiresRecognizedCursorEvent(t *testing.T) {
	stdout := `{"kind":"tool.result","session_id":"ignore-me"}
{"type":"run.completed","session_id":"cursor-session","display_id":"cursor-display"}`

	checkpoint := parseCheckpoint(stdout, 0)
	if checkpoint == nil || checkpoint.State == nil {
		t.Fatal("expected checkpoint")
	}
	if checkpoint.State.ResumeID != "cursor-session" || checkpoint.State.DisplayID != "cursor-display" {
		t.Fatalf("unexpected checkpoint: %#v", checkpoint.State)
	}
}

func TestParseCheckpointAcceptsSessionOnlyPayload(t *testing.T) {
	checkpoint := parseCheckpoint(`{"session_id":"cursor-session"}`, 0)
	if checkpoint == nil || checkpoint.State == nil || checkpoint.State.ResumeID != "cursor-session" {
		t.Fatalf("unexpected checkpoint: %#v", checkpoint)
	}
}

func TestDetectModelFallsBackToCursorConfigFile(t *testing.T) {
	t.Setenv("CURSOR_HOME", "")
	home := t.TempDir()
	configDir := filepath.Join(home, ".cursor")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte("{\"model\":\"claude-sonnet-4\"}\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	detected, err := NewAdapter().(interface {
		DetectModel(context.Context, any, *agentadaptor.ProfileSelection) (*agentadaptor.DetectedModel, error)
	}).DetectModel(context.Background(), agentadaptor.CursorConfig{
		CommonConfig: agentadaptor.CommonConfig{
			Env: []agentadaptor.EnvBinding{{Name: "HOME", Value: home}},
		},
	}, nil)
	if err != nil {
		t.Fatalf("detect model: %v", err)
	}
	if detected == nil || detected.Model != "claude-sonnet-4" || detected.Source != "config_file" {
		t.Fatalf("unexpected detected model: %#v", detected)
	}
}

func TestDetectModelUsesExplicitProfileOptionAsCursorHome(t *testing.T) {
	profileDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(profileDir, "config.json"), []byte("{\"model\":\"gpt-5\"}\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	detected, err := NewAdapter().(interface {
		DetectModel(context.Context, any, *agentadaptor.ProfileSelection) (*agentadaptor.DetectedModel, error)
	}).DetectModel(context.Background(), agentadaptor.CursorConfig{
		CommonConfig: agentadaptor.CommonConfig{
			Env: []agentadaptor.EnvBinding{{Name: "CURSOR_HOME", Value: profileDir}},
		},
	}, nil)
	if err != nil {
		t.Fatalf("detect model: %v", err)
	}
	if detected == nil || detected.Model != "gpt-5" || detected.Source != "config_file" {
		t.Fatalf("unexpected detected model: %#v", detected)
	}
}

func TestDetectModelPrefersExplicitCursorHomeOverProfileOption(t *testing.T) {
	overrideDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(overrideDir, "config.json"), []byte("{\"model\":\"claude-sonnet-4\"}\n"), 0o644); err != nil {
		t.Fatalf("write override config: %v", err)
	}

	detected, err := NewAdapter().(interface {
		DetectModel(context.Context, any, *agentadaptor.ProfileSelection) (*agentadaptor.DetectedModel, error)
	}).DetectModel(context.Background(), agentadaptor.CursorConfig{
		CommonConfig: agentadaptor.CommonConfig{
			Env: []agentadaptor.EnvBinding{
				{Name: "CURSOR_HOME", Value: overrideDir},
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

func TestGetProfilePrefersExplicitCursorHomeOverProfileOption(t *testing.T) {
	overrideDir := t.TempDir()
	profile, err := NewAdapter().(interface {
		GetProfile(context.Context, any, agentadaptor.AgentIdentity, *agentadaptor.ProfileSelection) (agentadaptor.AgentProfile, error)
	}).GetProfile(context.Background(), agentadaptor.CursorConfig{
		CommonConfig: agentadaptor.CommonConfig{
			Env: []agentadaptor.EnvBinding{
				{Name: "CURSOR_HOME", Value: overrideDir},
			},
		},
	}, agentadaptor.AgentIdentity{}, nil)
	if err != nil {
		t.Fatalf("get profile: %v", err)
	}
	if !profile.Supported || profile.Dir != overrideDir || profile.Source != agentadaptor.AgentProfileSourceBindingEnv || profile.EnvVar != "CURSOR_HOME" {
		t.Fatalf("unexpected profile: %#v", profile)
	}
}

func TestGetProfileUsesProcessEnvForCursorWhenUnset(t *testing.T) {
	profileDir := t.TempDir()
	t.Setenv("CURSOR_HOME", profileDir)

	profile, err := NewAdapter().(interface {
		GetProfile(context.Context, any, agentadaptor.AgentIdentity, *agentadaptor.ProfileSelection) (agentadaptor.AgentProfile, error)
	}).GetProfile(context.Background(), agentadaptor.CursorConfig{}, agentadaptor.AgentIdentity{}, nil)
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
	if modelField.Name != "model" || modelField.Group != "model" || len(modelField.Options) == 0 || modelField.Default != "gpt-5" {
		t.Fatalf("unexpected model field: %#v", modelField)
	}
	commandField := schemaFieldByName(t, schema, "command")
	if commandField.Name != "command" || commandField.Group != "command" || commandField.Default != "agent" {
		t.Fatalf("unexpected command field: %#v", commandField)
	}
}

func TestCheckEnvironmentReportsConfigFileState(t *testing.T) {
	t.Setenv("CURSOR_HOME", "")
	home := t.TempDir()
	configDir := filepath.Join(home, ".cursor")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte("{\"model\":\"claude-sonnet-4\"}\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	report, err := NewAdapter().(interface {
		CheckEnvironment(context.Context, any) (agentadaptor.EnvironmentReport, error)
	}).CheckEnvironment(context.Background(), agentadaptor.CursorConfig{
		CommonConfig: agentadaptor.CommonConfig{
			Env: []agentadaptor.EnvBinding{{Name: "HOME", Value: home}},
		},
	})
	if err != nil {
		t.Fatalf("check environment: %v", err)
	}
	assertCheckPresent(t, report.Checks, "cursor_config_present")
	assertCheckPresent(t, report.Checks, "cursor_config_model")
}

func TestCheckEnvironmentReportsCursorNativeAuth(t *testing.T) {
	t.Setenv("CURSOR_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	cursorHome := t.TempDir()
	if err := os.WriteFile(filepath.Join(cursorHome, "cli-config.json"), []byte(`{"authInfo":{"email":"dev@example.com","displayName":"Dev Operator","userId":42}}`), 0o644); err != nil {
		t.Fatalf("write auth config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cursorHome, "config.json"), []byte("{\"model\":\"claude-sonnet-4\"}\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	report, err := NewAdapter().(interface {
		CheckEnvironment(context.Context, any) (agentadaptor.EnvironmentReport, error)
	}).CheckEnvironment(context.Background(), agentadaptor.CursorConfig{
		CommonConfig: agentadaptor.CommonConfig{
			Env: []agentadaptor.EnvBinding{{Name: "CURSOR_HOME", Value: cursorHome}},
		},
	})
	if err != nil {
		t.Fatalf("check environment: %v", err)
	}
	assertCheckPresent(t, report.Checks, "cursor_auth_present")
	assertCheckWithDetail(t, report.Checks, "cursor_auth_email", "dev@example.com")
	assertCheckWithDetail(t, report.Checks, "cursor_auth_display_name", "Dev Operator")
	assertCheckWithDetail(t, report.Checks, "cursor_auth_user_id", "42")
}

func TestCheckEnvironmentReportsOpenAIAPIKeyForCursorAPIMode(t *testing.T) {
	report, err := NewAdapter().(interface {
		CheckEnvironment(context.Context, any) (agentadaptor.EnvironmentReport, error)
	}).CheckEnvironment(context.Background(), agentadaptor.CursorConfig{
		CommonConfig: agentadaptor.CommonConfig{
			Env: []agentadaptor.EnvBinding{{Name: "OPENAI_API_KEY", Value: "test-key"}},
		},
	})
	if err != nil {
		t.Fatalf("check environment: %v", err)
	}
	assertCheckPresent(t, report.Checks, "cursor_openai_api_key_present")
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
