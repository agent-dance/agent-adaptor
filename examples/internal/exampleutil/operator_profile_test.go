package exampleutil

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/agent-dance/agent-adaptor/driver"
)

func TestOperatorProfileEnvUsesExistingCodexProfile(t *testing.T) {
	for _, override := range []bool{false, true} {
		t.Run(map[bool]string{false: "home profile", true: "explicit CODEX_HOME"}[override], func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			t.Setenv("CODEX_HOME", "")
			expected := filepath.Join(home, ".codex")
			if override {
				expected = filepath.Join(home, "custom-codex")
				t.Setenv("CODEX_HOME", expected)
			}
			if err := os.MkdirAll(expected, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(expected, "config.toml"), []byte("model = 'operator-model'\n"), 0600); err != nil {
				t.Fatal(err)
			}
			bindings, err := OperatorProfileEnv(AgentCodex)
			if err != nil {
				t.Fatal(err)
			}
			cfg := LiveAgentConfig{Agent: AgentCodex, Env: bindings}
			d := NewLiveDriver(cfg)
			profile, err := d.(driver.ProfileReporter).GetProfile(context.Background(), nil, driver.AgentIdentity{}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if profile.Dir != expected || profile.Managed {
				t.Fatalf("operator profile not selected: %+v", profile)
			}
			found := false
			for _, binding := range bindings {
				if binding.Name == "HOME" && binding.Value == home {
					found = true
				}
			}
			if !found {
				t.Fatal("HOME not explicitly bound")
			}
		})
	}
}

func TestOperatorProfileEnvPreservesOtherCLIOverrides(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, tc := range []struct{ agent, name string }{{AgentClaude, "CLAUDE_CONFIG_DIR"}, {AgentCursor, "CURSOR_HOME"}} {
		t.Run(tc.agent, func(t *testing.T) {
			expected := filepath.Join(home, tc.agent)
			t.Setenv(tc.name, expected)
			bindings, err := OperatorProfileEnv(tc.agent)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, b := range bindings {
				if b.Name == tc.name && b.Value == expected {
					found = true
				}
			}
			if !found {
				t.Fatal("explicit CLI profile override lost")
			}
		})
	}
}
