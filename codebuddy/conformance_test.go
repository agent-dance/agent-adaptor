package codebuddy

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	adaptertest "github.com/agent-dance/agent-adaptor/adaptertest"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
)

// codebuddyLiveGate decides whether the live conformance probes (EVT-*,
// RUN-*, TRN-*, RSP-*, SO-02) run. They skip when the codebuddy CLI is not
// in PATH (the CI path), and even with the CLI present they stay opt-in
// via AGENT_ADAPTOR_LIVE_CONFORMANCE=1, mirroring the codebuddy_live
// build-tag posture so plain `go test` never triggers a paid provider run.
func codebuddyLiveGate(t *testing.T) (bool, adaptertest.Option) {
	t.Helper()
	if _, err := exec.LookPath("codebuddy"); err != nil {
		return false, adaptertest.SkipLiveRun("codebuddy CLI not in PATH")
	}
	if os.Getenv("AGENT_ADAPTOR_LIVE_CONFORMANCE") != "1" {
		return false, adaptertest.SkipLiveRun("codebuddy CLI found; set AGENT_ADAPTOR_LIVE_CONFORMANCE=1 to run the live conformance probes")
	}
	return true, adaptertest.WithLiveRun("")
}

func TestCodeBuddyProfileInstructionMaterialization(t *testing.T) {
	for _, tc := range []struct {
		name            string
		instructions    driver.InstructionsBundleRef
		wantSupport     engine.ProfileResourceSupport
		wantMaterialize engine.ProfileResourceMaterialization
	}{
		{
			name:            "provider-native default scope",
			instructions:    driver.InstructionsBundleRef{ID: "team", Content: "Prefer concise answers."},
			wantSupport:     engine.ProfileResourceSupportPortableCore,
			wantMaterialize: engine.ProfileResourceMaterializationNativeManaged,
		},
		{
			name:            "run-scoped prompt fallback",
			instructions:    driver.InstructionsBundleRef{ID: "run", Content: "Answer for this run.", Scope: driver.InstructionScopeRun},
			wantSupport:     engine.ProfileResourceSupportFallback,
			wantMaterialize: engine.ProfileResourceMaterializationPromptInjected,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			configured := Driver(Config{CommonConfig: CommonConfig{Env: []driver.EnvBinding{
				{Name: "HOME", Value: home},
				{Name: "USERPROFILE", Value: home},
			}}})
			profileDriver, ok := configured.(engine.ProfileResourceDriver)
			if !ok {
				t.Fatal("configured driver lost profile resource capability")
			}
			snapshot, err := profileDriver.SyncProfileResources(
				context.Background(), nil, driver.AgentIdentity{}, nil,
				driver.ProfilePayload{
					Instructions: &tc.instructions,
					Declared:     driver.ProfileResourceDeclarations{Instructions: true},
				},
				nil, nil,
			)
			if err != nil {
				t.Fatalf("SyncProfileResources: %v", err)
			}
			resource, ok := profileResourceByKind(snapshot, engine.ProfileResourceInstructions)
			if !ok {
				t.Fatalf("instruction resource missing: %+v", snapshot.Resources)
			}
			if resource.Support != tc.wantSupport || resource.Materialization != tc.wantMaterialize {
				t.Fatalf("instruction resource = %+v, want support=%s materialization=%s", resource, tc.wantSupport, tc.wantMaterialize)
			}
			if len(resource.Managed) != 1 || resource.Managed[0] != tc.instructions.ID {
				t.Fatalf("managed instructions = %v, want [%s]", resource.Managed, tc.instructions.ID)
			}
		})
	}
}

func profileResourceByKind(snapshot engine.ProfileSnapshot, kind engine.ProfileResourceKind) (engine.ResourceSnapshot, bool) {
	for _, resource := range snapshot.Resources {
		if resource.Kind == kind {
			return resource, true
		}
	}
	return engine.ResourceSnapshot{}, false
}

// TestCodeBuddyDriverConformance runs the SPI conformance suite against the
// codebuddy.Driver constructor. Hermetic clauses always run against an
// isolated temp HOME; live clauses are gated by codebuddyLiveGate.
func TestCodeBuddyDriverConformance(t *testing.T) {
	live, liveOpt := codebuddyLiveGate(t)
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := Config{Model: "claude-sonnet-5"}
	cfg.CWD = workspace
	if !live {
		// Hermetic isolation ensures probes do not read or write the
		// operator's real HOME or config directory.
		t.Setenv("CODEBUDDY_CONFIG_DIR", "")
		cfg.Env = []driver.EnvBinding{
			{Name: "HOME", Value: home},
			{Name: "USERPROFILE", Value: home},
		}
	}

	opts := []adaptertest.Option{
		adaptertest.WithConfig(cfg),
		adaptertest.WithSessionState(&driver.SessionState{
			ResumeID: "codebuddy-session",
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
		adaptertest.WithExpectedDetectedModel("claude-sonnet-5"),
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
