package codex

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	adaptertest "github.com/agent-dance/agent-adaptor/adaptertest"
	"github.com/agent-dance/agent-adaptor/driver"
)

// codexLiveGate decides whether the live conformance probes (EVT-*,
// RUN-*, TRN-*, RSP-*, SO-02) run. Both the build tag and environment gate
// are required; once enabled, a missing CLI is a failed prerequisite.
func codexLiveGate(t *testing.T) (bool, adaptertest.Option) {
	t.Helper()
	if !codexLiveCompiled || os.Getenv("AGENT_ADAPTOR_LIVE_CONFORMANCE") != "1" {
		return false, adaptertest.SkipLiveRun("requires codex_live build tag and AGENT_ADAPTOR_LIVE_CONFORMANCE=1")
	}
	if _, err := exec.LookPath("codex"); err != nil {
		t.Fatal("authorized live runner requires codex CLI")
	}
	return true, adaptertest.WithLiveRun("")
}

// TestCodexDriverConformance runs the SPI conformance suite against the
// codex.Driver constructor. Hermetic clauses always run against an isolated
// temp HOME; live clauses are gated by codexLiveGate.
func TestCodexDriverConformance(t *testing.T) {
	live, liveOpt := codexLiveGate(t)
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := Config{Model: "gpt-5.4"}
	cfg.CWD = workspace
	if live {
		cfg = alignmentLiveConfig(t)
		cfg.CWD = workspace
	} else {
		// Hermetic isolation ensures probes do not read or write the
		// operator's real HOME.
		cfg.Env = []driver.EnvBinding{
			{Name: "HOME", Value: home},
			{Name: "USERPROFILE", Value: home},
		}
	}

	opts := []adaptertest.Option{
		adaptertest.WithConfig(cfg),
		adaptertest.WithSessionState(&driver.SessionState{
			ResumeID: "codex-session",
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
			appendSystemPromptFingerprintKey,
		),
		adaptertest.WithWorkspace(workspace),
		adaptertest.WithExpectedDetectedModel("gpt-5.4"),
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
