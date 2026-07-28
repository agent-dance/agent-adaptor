package cursor

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	adaptertest "github.com/agent-dance/agent-adaptor/adaptertest"
	"github.com/agent-dance/agent-adaptor/driver"
)

// cursorLiveGate decides whether the live conformance probes (EVT-*,
// RUN-*, TRN-*, RSP-*) run. They skip when the Cursor `agent` CLI is not
// in PATH (the CI path), and even with the CLI present they stay opt-in
// via AGENT_ADAPTOR_LIVE_CONFORMANCE=1 so plain `go test` never triggers a
// paid provider run.
func cursorLiveGate(t *testing.T) (bool, adaptertest.Option) {
	t.Helper()
	if _, err := exec.LookPath("agent"); err != nil {
		return false, adaptertest.SkipLiveRun("agent CLI not in PATH")
	}
	if os.Getenv("AGENT_ADAPTOR_LIVE_CONFORMANCE") != "1" {
		return false, adaptertest.SkipLiveRun("agent CLI found; set AGENT_ADAPTOR_LIVE_CONFORMANCE=1 to run the live conformance probes")
	}
	return true, adaptertest.WithLiveRun("")
}

// TestCursorDriverConformance runs the SPI conformance suite against the
// cursor.Driver constructor. Hermetic clauses always run against an isolated
// temp HOME; live clauses are gated by cursorLiveGate.
// The SO-02 structured probe self-skips: the descriptor does not declare
// JSONSchemaNative, and the suite never sends an undeclared mode (SO-03).
func TestCursorDriverConformance(t *testing.T) {
	live, liveOpt := cursorLiveGate(t)
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := Config{Model: "gpt-5"}
	cfg.CWD = workspace
	if !live {
		// Hermetic isolation ensures probes do not read or write the
		// operator's real HOME or Cursor home.
		t.Setenv("CURSOR_HOME", "")
		cfg.Env = []driver.EnvBinding{
			{Name: "HOME", Value: home},
			{Name: "USERPROFILE", Value: home},
		}
	}

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
		adaptertest.WithExpectedDetectedModel("gpt-5"),
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
