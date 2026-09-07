package codebuddy

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	adaptertest "github.com/agent-dance/agent-adaptor/adaptertest"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
)

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
	// Both branches execute against private profile/workspace roots. The live
	// tag helper copies only explicitly supplied authentication material.
	profileDir := filepath.Join(home, "profile")
	if live {
		profileDir = codebuddyConformanceProfile(t)
	}
	cfg.Env = []driver.EnvBinding{{Name: "HOME", Value: home}, {Name: "USERPROFILE", Value: home}, {Name: "CODEBUDDY_CONFIG_DIR", Value: profileDir}}

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
