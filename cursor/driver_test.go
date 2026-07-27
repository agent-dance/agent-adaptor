package cursor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentadaptor "github.com/agent-dance/agent-adaptor/driver"
)

func TestDescriptorAdvertisesExpectedMCPCapabilities(t *testing.T) {
	caps := adapter{}.Descriptor().MCP
	if !caps.Supported || !caps.Stdio || !caps.HTTP || !caps.SSE {
		t.Fatalf("unexpected Cursor MCP capability: %#v", caps)
	}
}

func TestDescriptorAdvertisesStructuredOutputCapabilities(t *testing.T) {
	caps := adapter{}.Descriptor().StructuredOutput
	if caps.JSONSchemaNative || !caps.JSONSchemaPromptValidate || !caps.WorksWithRun || !caps.WorksWithStreaming {
		t.Fatalf("unexpected Cursor structured-output capability: %#v", caps)
	}
	if caps.WorksWithHITL {
		t.Fatalf("Cursor structured output should not advertise HITL support: %#v", caps)
	}
}

func TestRunRejectsNativeStrictStructuredOutput(t *testing.T) {
	_, err := adapter{}.Run(context.Background(), agentadaptor.Request{
		OutputSchema: &agentadaptor.OutputSchema{
			Format:     agentadaptor.OutputFormatJSONSchema,
			Mode:       agentadaptor.StructuredOutputNativeStrict,
			SchemaJSON: []byte(`{"type":"object"}`),
		},
	}, nil)
	if !errors.Is(err, agentadaptor.ErrStructuredOutputUnsupported) {
		t.Fatalf("expected ErrStructuredOutputUnsupported, got %v", err)
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
	if checkpoint != nil {
		t.Fatalf("session-only payload is not terminal proof: %#v", checkpoint)
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
	if detected == nil || detected.Model != "claude-sonnet-4" || detected.Source != "config_file" {
		t.Fatalf("unexpected detected model: %#v", detected)
	}
}

func TestDetectModelUsesExplicitProfileOptionAsCursorHome(t *testing.T) {
	profileDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(profileDir, "config.json"), []byte("{\"model\":\"gpt-5\"}\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	detected, err := any(adapter{}).(interface {
		DetectModel(context.Context, any, *agentadaptor.ProfileSelection) (*agentadaptor.DetectedModel, error)
	}).DetectModel(context.Background(), Config{
		CommonConfig: CommonConfig{
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

	detected, err := any(adapter{}).(interface {
		DetectModel(context.Context, any, *agentadaptor.ProfileSelection) (*agentadaptor.DetectedModel, error)
	}).DetectModel(context.Background(), Config{
		CommonConfig: CommonConfig{
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
	profile, err := any(adapter{}).(interface {
		GetProfile(context.Context, any, agentadaptor.AgentIdentity, *agentadaptor.ProfileSelection) (agentadaptor.AgentProfile, error)
	}).GetProfile(context.Background(), Config{
		CommonConfig: CommonConfig{
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

func TestGetProfileCloneCanShareNativeCursorAuth(t *testing.T) {
	t.Setenv("CURSOR_HOME", "")
	home := t.TempDir()
	nativeProfile := filepath.Join(home, ".cursor")
	if err := os.MkdirAll(nativeProfile, 0o755); err != nil {
		t.Fatalf("mkdir native profile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nativeProfile, "config.json"), []byte("{\"model\":\"gpt-5\"}\n"), 0o644); err != nil {
		t.Fatalf("write native config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nativeProfile, "cli-config.json"), []byte(`{"authInfo":{"email":"dev@example.com"}}`), 0o600); err != nil {
		t.Fatalf("write native cli config: %v", err)
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
	rawConfig, err := os.ReadFile(filepath.Join(target, "config.json"))
	if err != nil {
		t.Fatalf("read cloned config: %v", err)
	}
	if !strings.Contains(string(rawConfig), "gpt-5") {
		t.Fatalf("expected cloned Cursor config, got %s", string(rawConfig))
	}
	sourceInfo, sourceErr := os.Stat(filepath.Join(nativeProfile, "cli-config.json"))
	targetInfo, targetErr := os.Stat(filepath.Join(target, "cli-config.json"))
	if sourceErr != nil || targetErr != nil {
		t.Fatalf("stat auth files: source=%v target=%v", sourceErr, targetErr)
	}
	if !os.SameFile(sourceInfo, targetInfo) {
		t.Fatalf("expected cloned profile cli-config.json to share native Cursor auth")
	}
}

func TestConfigSchemaIncludesGroupsDefaultsAndOptions(t *testing.T) {
	schema := adapter{}.Descriptor().ConfigSchema
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

	report, err := any(adapter{}).(interface {
		CheckEnvironment(context.Context, any) (agentadaptor.EnvironmentReport, error)
	}).CheckEnvironment(context.Background(), Config{
		CommonConfig: CommonConfig{
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
	report, err := any(adapter{}).(interface {
		CheckEnvironment(context.Context, any) (agentadaptor.EnvironmentReport, error)
	}).CheckEnvironment(context.Background(), Config{
		CommonConfig: CommonConfig{
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
