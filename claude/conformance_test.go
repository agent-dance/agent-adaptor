package claude

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	adaptertest "github.com/agent-dance/agent-adaptor/adaptertest"
	"github.com/agent-dance/agent-adaptor/driver"
)

// claudeLiveGate decides whether the live conformance probes (EVT-*,
// RUN-*, TRN-*, RSP-*, SO-02) run. They skip when the claude CLI is not in
// PATH (the CI path), and even with the CLI present they stay opt-in via
// AGENT_ADAPTOR_LIVE_CONFORMANCE=1, mirroring the claude_live build-tag
// posture so plain `go test` never triggers a paid provider run.
func claudeLiveGate(t *testing.T) (bool, adaptertest.Option) {
	t.Helper()
	if _, err := exec.LookPath("claude"); err != nil {
		return false, adaptertest.SkipLiveRun("claude CLI not in PATH")
	}
	if os.Getenv("AGENT_ADAPTOR_LIVE_CONFORMANCE") != "1" {
		return false, adaptertest.SkipLiveRun("claude CLI found; set AGENT_ADAPTOR_LIVE_CONFORMANCE=1 to run the live conformance probes")
	}
	return true, adaptertest.WithLiveRun("")
}

// TestClaudeDriverConformance runs the SPI conformance suite against the
// claude.Driver constructor. Hermetic clauses always run against an isolated
// temp HOME; live clauses are gated by claudeLiveGate.
func TestClaudeDriverConformance(t *testing.T) {
	live, liveOpt := claudeLiveGate(t)
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := Config{Model: "claude-sonnet-4"}
	cfg.CWD = workspace
	if !live {
		// Hermetic isolation ensures probes do not read or write the
		// operator's real HOME or config directory.
		t.Setenv("CLAUDE_CONFIG_DIR", "")
		cfg.Env = []driver.EnvBinding{
			{Name: "HOME", Value: home},
			{Name: "USERPROFILE", Value: home},
		}
	}

	opts := []adaptertest.Option{
		adaptertest.WithConfig(cfg),
		adaptertest.WithSessionState(&driver.SessionState{
			ResumeID: "claude-session",
			Data: map[string]string{
				driver.SessionParamCWD:                workspace,
				driver.SessionParamWorkspaceID:        "workspace-a",
				driver.SessionParamProfileFingerprint: "profile-a",
			},
		}),
		adaptertest.WithSessionKeys(
			driver.SessionParamCWD,
			driver.SessionParamWorkspaceID,
			driver.SessionParamProfileFingerprint,
		),
		adaptertest.WithGuardKeys(
			driver.SessionParamCWD,
			driver.SessionParamWorkspaceID,
			driver.SessionParamProfileFingerprint,
		),
		adaptertest.WithWorkspace(workspace),
		adaptertest.WithExpectedDetectedModel("claude-sonnet-4"),
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
