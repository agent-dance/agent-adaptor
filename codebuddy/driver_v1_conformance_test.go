package codebuddy

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	adaptertestv1 "github.com/agent-dance/agent-adaptor/adaptertest/v1"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
)

// codebuddyV1LiveGate decides whether the live conformance probes (EVT-*,
// RUN-*, TRN-*, RSP-*, SO-02) run. They skip when the codebuddy CLI is not
// in PATH (the CI path), and even with the CLI present they stay opt-in
// via AGENT_ADAPTOR_LIVE_CONFORMANCE=1, mirroring the codebuddy_live
// build-tag posture so plain `go test` never triggers a paid provider run.
func codebuddyV1LiveGate(t *testing.T) (bool, adaptertestv1.Option) {
	t.Helper()
	if _, err := exec.LookPath("codebuddy"); err != nil {
		return false, adaptertestv1.SkipLiveRun("codebuddy CLI not in PATH")
	}
	if os.Getenv("AGENT_ADAPTOR_LIVE_CONFORMANCE") == "" {
		return false, adaptertestv1.SkipLiveRun("codebuddy CLI found; set AGENT_ADAPTOR_LIVE_CONFORMANCE=1 to run the live conformance probes")
	}
	return true, adaptertestv1.WithLiveRun("")
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

// TestCodeBuddyDriverV1Conformance runs the v1 SPI conformance suite
// (adaptertest/v1) against the codebuddy.Driver constructor. Hermetic
// clauses always run against an isolated temp HOME; live clauses are gated by
// codebuddyV1LiveGate.
func TestCodeBuddyDriverV1Conformance(t *testing.T) {
	live, liveOpt := codebuddyV1LiveGate(t)
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := Config{Model: "claude-sonnet-5"}
	cfg.CWD = workspace
	if !live {
		// Hermetic isolation, matching the v0 conformance test: probes must
		// not read or write the operator's real HOME or config dir.
		t.Setenv("CODEBUDDY_CONFIG_DIR", "")
		cfg.Env = []driver.EnvBinding{
			{Name: "HOME", Value: home},
			{Name: "USERPROFILE", Value: home},
		}
	}

	opts := []adaptertestv1.Option{
		adaptertestv1.WithConfig(cfg),
		adaptertestv1.WithSessionState(&driver.SessionState{
			ResumeID: "codebuddy-v1-session",
			Data: map[string]string{
				driver.SessionParamCWD:                workspace,
				driver.SessionParamWorkspaceID:        "workspace-a",
				driver.SessionParamProfileFingerprint: "profile-a",
			},
		}),
		adaptertestv1.WithSessionKeys(
			driver.SessionParamCWD,
			driver.SessionParamWorkspaceID,
			driver.SessionParamProfileFingerprint,
		),
		adaptertestv1.WithGuardKeys(
			driver.SessionParamCWD,
			driver.SessionParamWorkspaceID,
			driver.SessionParamProfileFingerprint,
		),
		adaptertestv1.WithWorkspace(workspace),
		adaptertestv1.WithExpectedDetectedModel("claude-sonnet-5"),
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
