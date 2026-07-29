//go:build e2e

package e2e_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/codex"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/memory"
)

func TestCodexE2ERunnerClonesProviderSettingsAndSharesNativeAuth(t *testing.T) {
	home := t.TempDir()
	nativeHome := filepath.Join(home, ".codex")
	if err := os.MkdirAll(nativeHome, 0o700); err != nil {
		t.Fatalf("create native Codex home: %v", err)
	}
	nativeConfig := []byte("model_provider = 'custom'\nmodel = 'custom-native-model'\n")
	if err := os.WriteFile(filepath.Join(nativeHome, "config.toml"), nativeConfig, 0o600); err != nil {
		t.Fatalf("write native Codex config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nativeHome, "auth.json"), []byte(`{"OPENAI_API_KEY":"test-only"}`), 0o600); err != nil {
		t.Fatalf("write native Codex auth: %v", err)
	}
	t.Setenv("AGENT_ADAPTOR_E2E_CODEX_MODEL", "")

	common := driver.CommonConfig{Env: []driver.EnvBinding{
		{Name: "HOME", Value: home},
		{Name: "USERPROFILE", Value: home},
	}}
	cfg := codexE2EConfig(common)
	if cfg.Model != "" {
		t.Fatalf("default Codex E2E model = %q, want native-profile selection", cfg.Model)
	}

	scenarioRoot := t.TempDir()
	world := &scenarioWorld{
		root:      scenarioRoot,
		provider:  providerCodex,
		workspace: filepath.Join(scenarioRoot, "workspace"),
		store:     memory.NewStore(),
	}
	if err := os.MkdirAll(world.workspace, 0o700); err != nil {
		t.Fatalf("create scenario workspace: %v", err)
	}
	agent := adaptor.New(codex.Driver(cfg), world.agentOptions(adaptor.Policy{})...)
	snapshot, err := agent.ProfileState(context.Background())
	if err != nil {
		t.Fatalf("ProfileState: %v", err)
	}
	cloneHome := filepath.Join(scenarioRoot, "codex-profile")
	if got := filepath.Clean(snapshot.Profile.Dir); got != filepath.Clean(cloneHome) {
		t.Fatalf("Codex E2E profile = %q, want isolated clone %q", got, cloneHome)
	}
	clonedConfig, err := os.ReadFile(filepath.Join(cloneHome, "config.toml"))
	if err != nil {
		t.Fatalf("read cloned Codex config: %v", err)
	}
	if string(clonedConfig) != string(nativeConfig) {
		t.Fatalf("cloned Codex config = %q, want native provider settings %q", clonedConfig, nativeConfig)
	}
	nativeAuth, err := os.Stat(filepath.Join(nativeHome, "auth.json"))
	if err != nil {
		t.Fatalf("stat native Codex auth: %v", err)
	}
	clonedAuth, err := os.Stat(filepath.Join(cloneHome, "auth.json"))
	if err != nil {
		t.Fatalf("stat cloned Codex auth: %v", err)
	}
	if !os.SameFile(nativeAuth, clonedAuth) {
		t.Fatal("cloned Codex auth does not share the native credential file")
	}
}

func TestCodexE2ERunnerHonorsExplicitModelOverride(t *testing.T) {
	t.Setenv("AGENT_ADAPTOR_E2E_CODEX_MODEL", "custom-smoke-model")
	if got := codexE2EConfig(driver.CommonConfig{}).Model; got != "custom-smoke-model" {
		t.Fatalf("Codex E2E model override = %q", got)
	}
}
