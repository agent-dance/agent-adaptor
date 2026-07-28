package claude

import (
	"strings"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
)

func TestConfiguredDriverSessionFingerprintCoversEveryConfigField(t *testing.T) {
	t.Parallel()
	base := Config{
		CommonConfig: CommonConfig{
			Command: "claude", CWD: "/repo",
			Env: []driver.EnvBinding{{Name: "TOKEN", Value: "secret-one"}},
			Instructions: &driver.InstructionsBundleRef{
				ID: "instructions", Path: "/rules", Content: "rules", Fingerprint: "rules-v1",
				Scope: driver.InstructionScopeProject, Mode: driver.InstructionModeReplace,
				Native: map[string]any{"nested": map[string]any{"enabled": true}},
			},
			PromptTemplate: "{{prompt}}", BootstrapPromptTemplate: "bootstrap {{prompt}}",
			WorkspaceStrategy: &driver.WorkspaceStrategy{
				Type: driver.WorkspaceStrategyGitWorktree, BaseRef: "main",
				BranchTemplate: "agent/{id}", WorktreeParentDir: "/worktrees",
			},
			WorkspaceRuntime: &driver.WorkspaceRuntimeConfig{Services: []driver.RuntimeServiceSpec{{
				ID: "service", Name: "runtime", URL: "http://127.0.0.1", Description: "service",
				Lifecycle: driver.RuntimeLifecycleEphemeral, ReuseKey: "shared", Command: "server",
				CWD: "/service", Port: 8080, Metadata: map[string]string{"region": "local"},
			}}},
			Timeout: time.Minute, GracePeriod: time.Second, ExtraArgs: []string{"--verbose"},
		},
		Model: "claude-model", Effort: "high", MaxTurnsPerRun: 7,
	}
	baseFingerprint := configuredSessionFingerprint(t, Driver(base))
	cases := []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "command", mutate: func(c *Config) { c.Command = "claude-custom" }},
		{name: "cwd", mutate: func(c *Config) { c.CWD = "/other" }},
		{name: "env name", mutate: func(c *Config) { c.Env[0].Name = "OTHER_TOKEN" }},
		{name: "secret env value", mutate: func(c *Config) { c.Env[0].Value = "secret-two" }},
		{name: "instructions id", mutate: func(c *Config) { c.Instructions.ID = "other" }},
		{name: "instructions path", mutate: func(c *Config) { c.Instructions.Path = "/other-rules" }},
		{name: "instructions content", mutate: func(c *Config) { c.Instructions.Content = "other rules" }},
		{name: "instructions fingerprint", mutate: func(c *Config) { c.Instructions.Fingerprint = "rules-v2" }},
		{name: "instructions scope", mutate: func(c *Config) { c.Instructions.Scope = driver.InstructionScopeUser }},
		{name: "instructions mode", mutate: func(c *Config) { c.Instructions.Mode = driver.InstructionModeAdditive }},
		{name: "instructions native", mutate: func(c *Config) { c.Instructions.Native["nested"].(map[string]any)["enabled"] = false }},
		{name: "prompt template", mutate: func(c *Config) { c.PromptTemplate = "changed {{prompt}}" }},
		{name: "bootstrap prompt template", mutate: func(c *Config) { c.BootstrapPromptTemplate = "changed" }},
		{name: "workspace type", mutate: func(c *Config) { c.WorkspaceStrategy.Type = driver.WorkspaceStrategyCloudSandbox }},
		{name: "workspace base ref", mutate: func(c *Config) { c.WorkspaceStrategy.BaseRef = "develop" }},
		{name: "workspace branch template", mutate: func(c *Config) { c.WorkspaceStrategy.BranchTemplate = "changed/{id}" }},
		{name: "workspace parent", mutate: func(c *Config) { c.WorkspaceStrategy.WorktreeParentDir = "/other-worktrees" }},
		{name: "runtime service id", mutate: func(c *Config) { c.WorkspaceRuntime.Services[0].ID = "other" }},
		{name: "runtime service name", mutate: func(c *Config) { c.WorkspaceRuntime.Services[0].Name = "other" }},
		{name: "runtime service url", mutate: func(c *Config) { c.WorkspaceRuntime.Services[0].URL = "http://localhost" }},
		{name: "runtime service description", mutate: func(c *Config) { c.WorkspaceRuntime.Services[0].Description = "other" }},
		{name: "runtime service lifecycle", mutate: func(c *Config) { c.WorkspaceRuntime.Services[0].Lifecycle = driver.RuntimeLifecycleShared }},
		{name: "runtime service reuse key", mutate: func(c *Config) { c.WorkspaceRuntime.Services[0].ReuseKey = "other" }},
		{name: "runtime service command", mutate: func(c *Config) { c.WorkspaceRuntime.Services[0].Command = "other-server" }},
		{name: "runtime service cwd", mutate: func(c *Config) { c.WorkspaceRuntime.Services[0].CWD = "/other-service" }},
		{name: "runtime service port", mutate: func(c *Config) { c.WorkspaceRuntime.Services[0].Port = 9090 }},
		{name: "runtime service metadata", mutate: func(c *Config) { c.WorkspaceRuntime.Services[0].Metadata["region"] = "remote" }},
		{name: "timeout", mutate: func(c *Config) { c.Timeout = 2 * time.Minute }},
		{name: "grace period", mutate: func(c *Config) { c.GracePeriod = 2 * time.Second }},
		{name: "extra args", mutate: func(c *Config) { c.ExtraArgs[0] = "--debug" }},
		{name: "model", mutate: func(c *Config) { c.Model = "other-model" }},
		{name: "effort", mutate: func(c *Config) { c.Effort = "low" }},
		{name: "max turns", mutate: func(c *Config) { c.MaxTurnsPerRun = 8 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			changed := cloneConfig(base)
			tc.mutate(&changed)
			if got := configuredSessionFingerprint(t, Driver(changed)); got == baseFingerprint {
				t.Fatalf("changing %s did not change the session config fingerprint", tc.name)
			}
		})
	}
}

func TestConfiguredDriverSessionFingerprintUsesConstructionSnapshot(t *testing.T) {
	t.Parallel()
	firstNative := map[string]any{"alpha": "one", "beta": []string{"two"}}
	firstMetadata := map[string]string{"alpha": "one", "beta": "two"}
	cfg := Config{CommonConfig: CommonConfig{
		Env:          []driver.EnvBinding{{Name: "TOKEN", Value: "secret"}},
		Instructions: &driver.InstructionsBundleRef{Native: firstNative},
		WorkspaceRuntime: &driver.WorkspaceRuntimeConfig{Services: []driver.RuntimeServiceSpec{{
			ID: "service", Metadata: firstMetadata,
		}}},
	}}
	bound := Driver(cfg)
	want := configuredSessionFingerprint(t, bound)

	cfg.Env[0].Value = "mutated"
	firstNative["alpha"] = "mutated"
	firstMetadata["alpha"] = "mutated"
	if got := configuredSessionFingerprint(t, bound); got != want {
		t.Fatalf("construction snapshot changed after caller mutation: %q != %q", got, want)
	}

	secondNative := map[string]any{}
	secondNative["beta"] = []string{"two"}
	secondNative["alpha"] = "one"
	secondMetadata := map[string]string{}
	secondMetadata["beta"] = "two"
	secondMetadata["alpha"] = "one"
	equivalent := Driver(Config{CommonConfig: CommonConfig{
		Env:          []driver.EnvBinding{{Name: "TOKEN", Value: "secret"}},
		Instructions: &driver.InstructionsBundleRef{Native: secondNative},
		WorkspaceRuntime: &driver.WorkspaceRuntimeConfig{Services: []driver.RuntimeServiceSpec{{
			ID: "service", Metadata: secondMetadata,
		}}},
	}})
	if got := configuredSessionFingerprint(t, equivalent); got != want {
		t.Fatalf("map insertion order changed fingerprint: %q != %q", got, want)
	}
}

func TestConfiguredDriverSessionFingerprintErrorDoesNotLeakSecret(t *testing.T) {
	t.Parallel()
	const secret = "claude-secret-value"
	d := Driver(Config{CommonConfig: CommonConfig{Instructions: &driver.InstructionsBundleRef{
		Native: map[string]any{secret: func() {}},
	}}})
	fingerprinter := d.(driver.SessionConfigFingerprinter)
	_, err := fingerprinter.SessionConfigFingerprint()
	if err == nil {
		t.Fatal("expected unstable native config to fail")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("fingerprint error leaked secret: %q", err)
	}
}

func configuredSessionFingerprint(t *testing.T, d driver.Driver) string {
	t.Helper()
	fingerprinter, ok := d.(driver.SessionConfigFingerprinter)
	if !ok {
		t.Fatalf("%T does not implement driver.SessionConfigFingerprinter", d)
	}
	fingerprint, err := fingerprinter.SessionConfigFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if fingerprint == "" {
		t.Fatal("SessionConfigFingerprint returned empty fingerprint")
	}
	return fingerprint
}
