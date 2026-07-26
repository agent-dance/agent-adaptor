package cursor

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	adaptertestv1 "github.com/agent-dance/agent-adaptor/adaptertest/v1"
	"github.com/agent-dance/agent-adaptor/driver"
)

// cursorV1LiveGate decides whether the live conformance probes (EVT-*,
// RUN-*, TRN-*, RSP-*) run. They skip when the Cursor `agent` CLI is not
// in PATH (the CI path), and even with the CLI present they stay opt-in
// via AGENT_ADAPTOR_LIVE_CONFORMANCE=1 so plain `go test` never triggers a
// paid provider run.
func cursorV1LiveGate(t *testing.T) (bool, adaptertestv1.Option) {
	t.Helper()
	if _, err := exec.LookPath("agent"); err != nil {
		return false, adaptertestv1.SkipLiveRun("agent CLI not in PATH")
	}
	if os.Getenv("AGENT_ADAPTOR_LIVE_CONFORMANCE") == "" {
		return false, adaptertestv1.SkipLiveRun("agent CLI found; set AGENT_ADAPTOR_LIVE_CONFORMANCE=1 to run the live conformance probes")
	}
	return true, adaptertestv1.WithLiveRun("")
}

// TestCursorDriverV1Conformance runs the v1 SPI conformance suite
// (adaptertest/v1) against the cursor.Driver constructor. Hermetic clauses
// always run against an isolated temp HOME, mirroring
// TestCursorAdapterConformance; live clauses are gated by cursorV1LiveGate.
// The SO-02 structured probe self-skips: the descriptor does not declare
// JSONSchemaNative, and the suite never sends an undeclared mode (SO-03).
func TestCursorDriverV1Conformance(t *testing.T) {
	live, liveOpt := cursorV1LiveGate(t)
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := Config{Model: "gpt-5"}
	cfg.CWD = workspace
	if !live {
		// Hermetic isolation, matching the v0 conformance test: probes must
		// not read or write the operator's real HOME or Cursor home.
		t.Setenv("CURSOR_HOME", "")
		cfg.Env = []agentadaptor.EnvBinding{
			{Name: "HOME", Value: home},
			{Name: "USERPROFILE", Value: home},
		}
	}

	opts := []adaptertestv1.Option{
		adaptertestv1.WithConfig(cfg),
		adaptertestv1.WithSessionState(&driver.SessionState{
			ResumeID: "cursor-v1-session",
			Data: map[string]string{
				agentadaptor.SessionParamCWD:         workspace,
				agentadaptor.SessionParamWorkspaceID: "workspace-a",
			},
		}),
		adaptertestv1.WithSessionKeys(
			agentadaptor.SessionParamCWD,
			agentadaptor.SessionParamWorkspaceID,
		),
		adaptertestv1.WithGuardKeys(
			agentadaptor.SessionParamCWD,
			agentadaptor.SessionParamWorkspaceID,
			agentadaptor.SessionParamProfileFingerprint,
		),
		adaptertestv1.WithWorkspace(workspace),
		adaptertestv1.WithExpectedDetectedModel("gpt-5"),
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
