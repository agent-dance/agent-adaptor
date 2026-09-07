package cursor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	adaptertest "github.com/agent-dance/agent-adaptor/adaptertest"
	"github.com/agent-dance/agent-adaptor/driver"
)

// Both the build tag and environment opt-in are required. Once explicitly
// enabled, unavailable CLI/auth is a failing required probe, not a skip.
func cursorLiveGate(t *testing.T) (bool, adaptertest.Option) {
	t.Helper()
	if !cursorLiveBuild || os.Getenv("AGENT_ADAPTOR_LIVE_CONFORMANCE") != "1" {
		return false, adaptertest.SkipLiveRun("requires cursor_live build tag and AGENT_ADAPTOR_LIVE_CONFORMANCE=1")
	}
	command := os.Getenv("AGENT_ADAPTOR_CURSOR_COMMAND")
	if command == "" {
		command = "agent"
	}
	if _, err := exec.LookPath(command); err != nil {
		t.Fatal("Cursor live conformance enabled but CLI unavailable")
	}
	return true, adaptertest.WithLiveRun("")
}

// Profiles are always freshly isolated. B06 supplies an API key through its
// approved environment; this test never reads/copies the operator profile.
func cursorIsolatedConfig(t *testing.T, live bool) Config {
	t.Helper()
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Model: "gpt-5", CommonConfig: CommonConfig{CWD: workspace, Env: []driver.EnvBinding{{Name: "HOME", Value: home}, {Name: "USERPROFILE", Value: home}, {Name: "CURSOR_HOME", Value: filepath.Join(home, "cursor")}}}}
	if live {
		cfg.Command = os.Getenv("AGENT_ADAPTOR_CURSOR_COMMAND")
		if cfg.Command == "" {
			cfg.Command = "agent"
		}
		if model := os.Getenv("AGENT_ADAPTOR_CURSOR_MODEL"); model != "" {
			cfg.Model = model
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, cfg.Command, "--version")
		command.Dir = workspace
		command.Env = os.Environ()
		for _, binding := range cfg.Env {
			command.Env = append(command.Env, binding.Name+"="+binding.Value)
		}
		version, err := command.Output()
		if err != nil {
			t.Fatal("Cursor --version failed in isolated live environment")
		}
		if len(version) == 0 || len(version) > 4096 {
			t.Fatal("invalid Cursor version response")
		}
		t.Logf("Cursor CLI version: %s; isolated HOME/profile/workspace; model=%s", strings.TrimSpace(string(version)), cfg.Model)
	}
	return cfg
}

func TestAlignmentCursorLiveRequiresBothGates(t *testing.T) {
	t.Setenv("AGENT_ADAPTOR_LIVE_CONFORMANCE", "0")
	if live, _ := cursorLiveGate(t); live {
		t.Fatal("environment gate bypass")
	}
	if !cursorLiveBuild {
		t.Setenv("AGENT_ADAPTOR_LIVE_CONFORMANCE", "1")
		if live, _ := cursorLiveGate(t); live {
			t.Fatal("build tag gate bypass")
		}
	}
}

// TestCursorDriverConformance runs the SPI conformance suite against the
// cursor.Driver constructor. Hermetic clauses always run against an isolated
// temp HOME; live clauses are gated by cursorLiveGate.
// The SO-02 structured probe self-skips: the descriptor does not declare
// JSONSchemaNative, and the suite never sends an undeclared mode (SO-03).
func TestCursorDriverConformance(t *testing.T) {
	live, liveOpt := cursorLiveGate(t)
	cfg := cursorIsolatedConfig(t, live)
	workspace := cfg.CWD

	opts := []adaptertest.Option{
		adaptertest.WithConfig(cfg),
		adaptertest.WithSessionState(&driver.SessionState{
			ResumeID: "cursor-session",
			Data: map[string]string{
				driver.SessionParamCWD:         workspace,
				driver.SessionParamWorkspaceID: "workspace-a",
			},
		}),
		adaptertest.WithSessionKeys(
			driver.SessionParamCWD,
			driver.SessionParamWorkspaceID,
		),
		adaptertest.WithGuardKeys(
			driver.SessionParamCWD,
			driver.SessionParamWorkspaceID,
			driver.SessionParamProfileFingerprint,
		),
		adaptertest.WithWorkspace(workspace),
		adaptertest.WithExpectedDetectedModel(cfg.Model),
		adaptertest.WithRequiredConfigFields("command", "cwd", "model"),
		adaptertest.ExpectRejectForeignConfig(),
		liveOpt,
	}
	if live {
		opts = append(opts, adaptertest.WithLiveStructuredOutput())
	} else {
		// SyncSkills reconciles on-disk state; only probe it under the
		// hermetic temp HOME.
		opts = append(opts, adaptertest.WithSyncSkillsProbe())
	}

	adaptertest.TestDriver(t, func() driver.Driver { return Driver(cfg) }, opts...)
}
