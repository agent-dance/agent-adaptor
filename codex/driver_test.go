package codex

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

func TestNewReturnsTypedBinding(t *testing.T) {
	binding := New(agentadaptor.CodexConfig{Model: "gpt-5.4"})
	if binding.TypedConfig().Model != "gpt-5.4" {
		t.Fatalf("expected typed config model to round-trip, got %#v", binding.TypedConfig())
	}
}

func TestDescriptorAdvertisesExpectedMCPCapabilities(t *testing.T) {
	caps := NewAdapter().Descriptor().MCP
	if !caps.Supported || !caps.Stdio || !caps.HTTP || caps.SSE {
		t.Fatalf("unexpected Codex MCP capability: %#v", caps)
	}
}

func TestParseCheckpointRequiresRecognizedCodexEvent(t *testing.T) {
	stdout := `{"type":"tool.result","thread_id":"ignore-me"}
{"type":"thread.started","thread_id":"codex-thread"}`

	checkpoint := snapshotCodexStdout(stdout).checkpoint(0)
	if checkpoint == nil || checkpoint.State == nil {
		t.Fatal("expected checkpoint")
	}
	if checkpoint.State.ResumeID != "codex-thread" || checkpoint.State.DisplayID != "codex-thread" {
		t.Fatalf("unexpected checkpoint: %#v", checkpoint.State)
	}
}

func TestParseCheckpointAcceptsSessionOnlyPayload(t *testing.T) {
	checkpoint := snapshotCodexStdout(`{"thread_id":"codex-thread"}`).checkpoint(0)
	if checkpoint == nil || checkpoint.State == nil || checkpoint.State.ResumeID != "codex-thread" {
		t.Fatalf("unexpected checkpoint: %#v", checkpoint)
	}
}

func TestParseCodexJSONLUsesThreadStartedSummaryAndUsage(t *testing.T) {
	stdout := `{"type":"thread.started","thread_id":"thread-123"}
{"type":"item.completed","item":{"type":"agent_message","text":"First update"}}
{"type":"item.completed","item":{"type":"agent_message","text":"Final answer"}}
{"type":"turn.completed","usage":{"input_tokens":12,"cached_input_tokens":4,"output_tokens":7}}`

	parsed := snapshotCodexStdout(stdout)
	if parsed.sessionID != "thread-123" || parsed.displayID != "thread-123" {
		t.Fatalf("unexpected parsed session id=%q display=%q", parsed.sessionID, parsed.displayID)
	}
	if parsed.summary != "Final answer" {
		t.Fatalf("expected last agent message summary, got %q", parsed.summary)
	}
	if parsed.buildOutput() != "First update\n\nFinal answer" {
		t.Fatalf("unexpected assistant output: %q", parsed.buildOutput())
	}
	if parsed.usage == nil || parsed.usage.InputTokens != 12 || parsed.usage.CachedInputTokens != 4 || parsed.usage.OutputTokens != 7 {
		t.Fatalf("unexpected usage parsing: %#v", parsed.usage)
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

	detected, err := NewAdapter().(interface {
		DetectModel(context.Context, any, *agentadaptor.ProfileSelection) (*agentadaptor.DetectedModel, error)
	}).DetectModel(context.Background(), agentadaptor.CodexConfig{
		CommonConfig: agentadaptor.CommonConfig{
			Env: []agentadaptor.EnvBinding{{Name: "HOME", Value: home}},
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

	detected, err := NewAdapter().(interface {
		DetectModel(context.Context, any, *agentadaptor.ProfileSelection) (*agentadaptor.DetectedModel, error)
	}).DetectModel(context.Background(), agentadaptor.CodexConfig{
		CommonConfig: agentadaptor.CommonConfig{
			Env: []agentadaptor.EnvBinding{{Name: "CODEX_HOME", Value: profileDir}},
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

	detected, err := NewAdapter().(interface {
		DetectModel(context.Context, any, *agentadaptor.ProfileSelection) (*agentadaptor.DetectedModel, error)
	}).DetectModel(context.Background(), agentadaptor.CodexConfig{
		CommonConfig: agentadaptor.CommonConfig{
			Env: []agentadaptor.EnvBinding{
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
	profile, err := NewAdapter().(interface {
		GetProfile(context.Context, any, agentadaptor.AgentIdentity, *agentadaptor.ProfileSelection) (agentadaptor.AgentProfile, error)
	}).GetProfile(context.Background(), agentadaptor.CodexConfig{}, agentadaptor.AgentIdentity{TenantID: "examples"}, nil)
	if err != nil {
		t.Fatalf("get profile: %v", err)
	}
	if !profile.Supported || !profile.Managed || profile.Source != agentadaptor.AgentProfileSourceManaged || profile.EnvVar != "CODEX_HOME" {
		t.Fatalf("unexpected profile metadata: %#v", profile)
	}
	if want := resolveManagedCodexHome(agentadaptor.AgentIdentity{TenantID: "examples"}); profile.Dir != want {
		t.Fatalf("expected managed home %q, got %#v", want, profile)
	}
}

func TestGetProfileUsesExplicitCodexHomeOverProfileOption(t *testing.T) {
	overrideDir := t.TempDir()
	profile, err := NewAdapter().(interface {
		GetProfile(context.Context, any, agentadaptor.AgentIdentity, *agentadaptor.ProfileSelection) (agentadaptor.AgentProfile, error)
	}).GetProfile(context.Background(), agentadaptor.CodexConfig{
		CommonConfig: agentadaptor.CommonConfig{
			Env: []agentadaptor.EnvBinding{
				{Name: "CODEX_HOME", Value: overrideDir},
			},
		},
	}, agentadaptor.AgentIdentity{}, nil)
	if err != nil {
		t.Fatalf("get profile: %v", err)
	}
	if profile.Dir != overrideDir || profile.Source != agentadaptor.AgentProfileSourceBindingEnv || profile.Managed {
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

	profile, err := NewAdapter().(interface {
		GetProfile(context.Context, any, agentadaptor.AgentIdentity, *agentadaptor.ProfileSelection) (agentadaptor.AgentProfile, error)
	}).GetProfile(context.Background(), agentadaptor.CodexConfig{
		CommonConfig: agentadaptor.CommonConfig{
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
	schema := NewAdapter().Descriptor().ConfigSchema
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

	report, err := NewAdapter().(interface {
		CheckEnvironment(context.Context, any) (agentadaptor.EnvironmentReport, error)
	}).CheckEnvironment(context.Background(), agentadaptor.CodexConfig{
		CommonConfig: agentadaptor.CommonConfig{
			Env: []agentadaptor.EnvBinding{{Name: "HOME", Value: home}},
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

	report, err := NewAdapter().(interface {
		CheckEnvironment(context.Context, any) (agentadaptor.EnvironmentReport, error)
	}).CheckEnvironment(context.Background(), agentadaptor.CodexConfig{
		CommonConfig: agentadaptor.CommonConfig{
			Env: []agentadaptor.EnvBinding{{Name: "HOME", Value: home}},
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

	report, err := NewAdapter().(interface {
		GetQuota(context.Context, any, *agentadaptor.ProfileSelection) (agentadaptor.QuotaReport, error)
	}).GetQuota(context.Background(), agentadaptor.CodexConfig{
		CommonConfig: agentadaptor.CommonConfig{
			Env: []agentadaptor.EnvBinding{{Name: "HOME", Value: home}},
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

func TestGetQuotaUsesProfileSelection(t *testing.T) {
	profileDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(profileDir, "auth.json"), []byte(`{"tokens":{"access_token":"token"}}`), 0o644); err != nil {
		t.Fatalf("write auth: %v", err)
	}
	report, err := NewAdapter().(interface {
		GetQuota(context.Context, any, *agentadaptor.ProfileSelection) (agentadaptor.QuotaReport, error)
	}).GetQuota(context.Background(), agentadaptor.CodexConfig{}, &agentadaptor.ProfileSelection{Mode: agentadaptor.ProfileModeDedicated, Dir: profileDir})
	if err != nil {
		t.Fatalf("get quota: %v", err)
	}
	if report.Error == "no local Codex auth token found in auth.json" {
		t.Fatalf("quota probe ignored selected profile: %#v", report)
	}
}
