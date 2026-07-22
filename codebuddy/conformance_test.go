package codebuddy

import (
	"path/filepath"
	"testing"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/adaptertest"
)

// TestCodeBuddyAdapterConformance runs the shared adapter SPI contract suite
// (descriptor, config validation, environment, models, config schema, detected
// model, profile, skills, session codec round-trip, profile resources) against
// the CodeBuddy adapter, matching the coverage the claude/codex/cursor drivers
// already carry.
func TestCodeBuddyAdapterConformance(t *testing.T) {
	t.Setenv("CODEBUDDY_CONFIG_DIR", "")
	home := t.TempDir()
	adaptertest.Run(t, adaptertest.Subject{
		Name:    DriverType,
		Adapter: NewAdapter(),
		Config: agentadaptor.CodeBuddyConfig{
			CommonConfig: agentadaptor.CommonConfig{
				CWD: filepath.Join(home, "workspace"),
				Env: []agentadaptor.EnvBinding{
					{Name: "HOME", Value: home},
					{Name: "USERPROFILE", Value: home},
				},
			},
			Model: "claude-sonnet-5",
		},
		SessionState: &agentadaptor.DriverSessionState{
			ResumeID: "codebuddy-session",
			Data: map[string]string{
				agentadaptor.SessionParamCWD:                filepath.Join(home, "workspace"),
				agentadaptor.SessionParamWorkspaceID:        "workspace-a",
				agentadaptor.SessionParamProfileFingerprint: "profile-a",
			},
		},
		RequiredSessionKeys: []string{
			agentadaptor.SessionParamCWD,
			agentadaptor.SessionParamWorkspaceID,
			agentadaptor.SessionParamProfileFingerprint,
		},
		RequiredConfigFields:  []string{"command", "cwd", "model"},
		ExpectedDetectedModel: "claude-sonnet-5",
		// CodeBuddy natively materializes team instructions as CODEBUDDY.md
		// (portable-core, native-managed), mirroring Claude's CLAUDE.md.
		ProfileResources: agentadaptor.ProfileResources{
			Instructions: &agentadaptor.InstructionsBundleRef{
				ID:      "team",
				Content: "Prefer concise, evidence-backed answers.",
			},
		},
		ExpectedProfileResources: []adaptertest.ExpectedProfileResource{
			{
				Kind:            agentadaptor.ProfileResourceInstructions,
				Managed:         []string{"team"},
				Support:         agentadaptor.ProfileResourceSupportPortableCore,
				Materialization: agentadaptor.ProfileResourceMaterializationNativeManaged,
			},
		},
		ProfileResourceCases: []adaptertest.ProfileResourceCase{
			{
				// Run-scoped instructions fall back to SDK prompt injection.
				Name: "fallback_run_scoped_instructions",
				ProfileResources: agentadaptor.ProfileResources{
					Instructions: &agentadaptor.InstructionsBundleRef{
						ID:      "run-fallback",
						Content: "Use fallback instructions for this run.",
						Scope:   agentadaptor.InstructionScopeRun,
					},
				},
				ExpectedSyncResources: []adaptertest.ExpectedProfileResource{{
					Kind:            agentadaptor.ProfileResourceInstructions,
					Managed:         []string{"run-fallback"},
					Support:         agentadaptor.ProfileResourceSupportFallback,
					Materialization: agentadaptor.ProfileResourceMaterializationPromptInjected,
				}},
			},
		},
	})
}
