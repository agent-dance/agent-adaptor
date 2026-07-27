package claude

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	adaptertestv1 "github.com/agent-dance/agent-adaptor/adaptertest/v1"
	"github.com/agent-dance/agent-adaptor/driver"
)

// claudeV1LiveGate decides whether the live conformance probes (EVT-*,
// RUN-*, TRN-*, RSP-*, SO-02) run. They skip when the claude CLI is not in
// PATH (the CI path), and even with the CLI present they stay opt-in via
// AGENT_ADAPTOR_LIVE_CONFORMANCE=1, mirroring the claude_live build-tag
// posture so plain `go test` never triggers a paid provider run.
func claudeV1LiveGate(t *testing.T) (bool, adaptertestv1.Option) {
	t.Helper()
	if _, err := exec.LookPath("claude"); err != nil {
		return false, adaptertestv1.SkipLiveRun("claude CLI not in PATH")
	}
	if os.Getenv("AGENT_ADAPTOR_LIVE_CONFORMANCE") == "" {
		return false, adaptertestv1.SkipLiveRun("claude CLI found; set AGENT_ADAPTOR_LIVE_CONFORMANCE=1 to run the live conformance probes")
	}
	return true, adaptertestv1.WithLiveRun("")
}

// TestClaudeDriverV1Conformance runs the v1 SPI conformance suite
// (adaptertest/v1) against the claude.Driver constructor. Hermetic clauses
// always run against an isolated temp HOME, mirroring
// TestClaudeAdapterConformance; live clauses are gated by claudeV1LiveGate.
func TestClaudeDriverV1Conformance(t *testing.T) {
	live, liveOpt := claudeV1LiveGate(t)
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := Config{Model: "claude-sonnet-4"}
	cfg.CWD = workspace
	if !live {
		// Hermetic isolation, matching the v0 conformance test: probes must
		// not read or write the operator's real HOME or config dir.
		t.Setenv("CLAUDE_CONFIG_DIR", "")
		cfg.Env = []agentadaptor.EnvBinding{
			{Name: "HOME", Value: home},
			{Name: "USERPROFILE", Value: home},
		}
	}

	opts := []adaptertestv1.Option{
		adaptertestv1.WithConfig(cfg),
		adaptertestv1.WithSessionState(&driver.SessionState{
			ResumeID: "claude-v1-session",
			Data: map[string]string{
				driver.SessionParamCWD:             workspace,
				driver.SessionParamWorkspaceID:     "workspace-a",
				driver.SessionParamPromptBundleKey: "bundle-a",
			},
		}),
		adaptertestv1.WithSessionKeys(
			driver.SessionParamCWD,
			driver.SessionParamWorkspaceID,
			driver.SessionParamPromptBundleKey,
		),
		adaptertestv1.WithGuardKeys(
			driver.SessionParamCWD,
			driver.SessionParamWorkspaceID,
			driver.SessionParamProfileFingerprint,
		),
		adaptertestv1.WithWorkspace(workspace),
		adaptertestv1.WithExpectedDetectedModel("claude-sonnet-4"),
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
