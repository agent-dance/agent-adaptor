package codex

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	adaptertestv1 "github.com/agent-dance/agent-adaptor/adaptertest/v1"
	"github.com/agent-dance/agent-adaptor/driver"
)

// codexV1LiveGate decides whether the live conformance probes (EVT-*,
// RUN-*, TRN-*, RSP-*, SO-02) run. They skip when the codex CLI is not in
// PATH (the CI path), and even with the CLI present they stay opt-in via
// AGENT_ADAPTOR_LIVE_CONFORMANCE=1, mirroring the codex_live build-tag
// posture so plain `go test` never triggers a paid provider run.
func codexV1LiveGate(t *testing.T) (bool, adaptertestv1.Option) {
	t.Helper()
	if _, err := exec.LookPath("codex"); err != nil {
		return false, adaptertestv1.SkipLiveRun("codex CLI not in PATH")
	}
	if os.Getenv("AGENT_ADAPTOR_LIVE_CONFORMANCE") == "" {
		return false, adaptertestv1.SkipLiveRun("codex CLI found; set AGENT_ADAPTOR_LIVE_CONFORMANCE=1 to run the live conformance probes")
	}
	return true, adaptertestv1.WithLiveRun("")
}

// TestCodexDriverV1Conformance runs the v1 SPI conformance suite
// (adaptertest/v1) against the codex.Driver constructor. Hermetic clauses
// always run against an isolated temp HOME, mirroring
// TestCodexAdapterConformance; live clauses are gated by codexV1LiveGate.
func TestCodexDriverV1Conformance(t *testing.T) {
	live, liveOpt := codexV1LiveGate(t)
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := Config{Model: "gpt-5.4"}
	cfg.CWD = workspace
	if !live {
		// Hermetic isolation, matching the v0 conformance test: probes must
		// not read or write the operator's real HOME.
		cfg.Env = []driver.EnvBinding{
			{Name: "HOME", Value: home},
			{Name: "USERPROFILE", Value: home},
		}
	}

	opts := []adaptertestv1.Option{
		adaptertestv1.WithConfig(cfg),
		adaptertestv1.WithSessionState(&driver.SessionState{
			ResumeID: "codex-v1-session",
			Data: map[string]string{
				driver.SessionParamCWD:         workspace,
				driver.SessionParamWorkspaceID: "workspace-a",
			},
		}),
		adaptertestv1.WithSessionKeys(
			driver.SessionParamCWD,
			driver.SessionParamWorkspaceID,
		),
		adaptertestv1.WithGuardKeys(
			driver.SessionParamCWD,
			driver.SessionParamWorkspaceID,
			driver.SessionParamProfileFingerprint,
		),
		adaptertestv1.WithWorkspace(workspace),
		adaptertestv1.WithExpectedDetectedModel("gpt-5.4"),
		adaptertestv1.WithRequiredConfigFields("command", "cwd", "model"),
		adaptertestv1.ExpectRejectForeignConfig(),
		liveOpt,
	}
	if live {
		opts = append(opts, adaptertestv1.WithLiveStructuredOutput())
	} else {
		// SyncSkills reconciles on-disk state; only probe it under the
		// hermetic temp HOME.
		opts = append(opts, adaptertestv1.WithSyncSkillsProbe())
	}

	adaptertestv1.TestDriver(t, func() driver.Driver { return Driver(cfg) }, opts...)
}
