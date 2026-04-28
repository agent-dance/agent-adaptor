package claude

import (
	"path/filepath"
	"testing"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/adaptertest"
)

func TestClaudeAdapterConformance(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home := t.TempDir()
	adaptertest.Run(t, adaptertest.Subject{
		Name:    DriverType,
		Adapter: NewAdapter(),
		Config: agentadaptor.ClaudeConfig{
			CommonConfig: agentadaptor.CommonConfig{
				CWD: filepath.Join(home, "workspace"),
				Env: []agentadaptor.EnvBinding{
					{Name: "HOME", Value: home},
					{Name: "USERPROFILE", Value: home},
				},
			},
			Model: "claude-sonnet-4",
		},
		SessionState: &agentadaptor.DriverSessionState{
			ResumeID: "claude-session",
			Data: map[string]string{
				agentadaptor.SessionParamCWD:             filepath.Join(home, "workspace"),
				agentadaptor.SessionParamWorkspaceID:     "workspace-a",
				agentadaptor.SessionParamPromptBundleKey: "bundle-a",
			},
		},
		RequiredSessionKeys: []string{
			agentadaptor.SessionParamCWD,
			agentadaptor.SessionParamWorkspaceID,
			agentadaptor.SessionParamPromptBundleKey,
		},
		RequiredConfigFields:  []string{"command", "cwd", "model"},
		ExpectedDetectedModel: "claude-sonnet-4",
		ProfileResources: agentadaptor.ProfileResources{
			Agents: []agentadaptor.AgentSpec{{
				Key:          "reviewer",
				Description:  "Reviews risky changes.",
				Instructions: "Check correctness and tests.",
			}},
			Hooks: []agentadaptor.HookSpec{{
				Key:     "pre",
				Event:   agentadaptor.HookEventPreTool,
				Command: "true",
			}},
			Instructions: &agentadaptor.InstructionsBundleRef{
				ID:      "team",
				Content: "Prefer concise, evidence-backed answers.",
			},
			Config: []agentadaptor.ProfileConfigPatch{{
				Key:        "permission",
				Capability: "permission",
				Values:     map[string]any{"mode": "acceptEdits"},
			}},
		},
		ExpectedProfileResources: []adaptertest.ExpectedProfileResource{
			{Kind: agentadaptor.ProfileResourceAgents, Managed: []string{"reviewer"}, Support: agentadaptor.ProfileResourceSupportPortableCore, Materialization: agentadaptor.ProfileResourceMaterializationNativeManaged},
			{Kind: agentadaptor.ProfileResourceHooks, Managed: []string{"pre"}, Support: agentadaptor.ProfileResourceSupportPortableCore, Materialization: agentadaptor.ProfileResourceMaterializationNativeManaged},
			{Kind: agentadaptor.ProfileResourceInstructions, Managed: []string{"team"}, Support: agentadaptor.ProfileResourceSupportPortableCore, Materialization: agentadaptor.ProfileResourceMaterializationNativeManaged},
			{Kind: agentadaptor.ProfileResourceConfig, Managed: []string{"permission"}, Support: agentadaptor.ProfileResourceSupportPortableExtended, Materialization: agentadaptor.ProfileResourceMaterializationNativeManaged},
		},
		ProfileResourceCases: []adaptertest.ProfileResourceCase{
			{
				Name: "portable_extended_agents_warning",
				ProfileResources: agentadaptor.ProfileResources{
					Agents: []agentadaptor.AgentSpec{{
						Key:          "reviewer-extended",
						Description:  "Reviews risky changes.",
						Instructions: "Check correctness and tests.",
						SandboxMode:  "workspace-write",
					}},
				},
				ExpectedSyncResources: []adaptertest.ExpectedProfileResource{{
					Kind:            agentadaptor.ProfileResourceAgents,
					Managed:         []string{"reviewer-extended"},
					Support:         agentadaptor.ProfileResourceSupportPortableExtended,
					Materialization: agentadaptor.ProfileResourceMaterializationNativeManaged,
					Warnings:        []string{"sandbox mode is not mapped for Claude agents"},
				}},
			},
			{
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
					Warnings:        []string{"SDK-managed prompt fallback"},
				}},
			},
			{
				Name: "native_escape_config_patch",
				ProfileResources: agentadaptor.ProfileResources{
					Config: []agentadaptor.ProfileConfigPatch{{
						Key:      "native-permission",
						FileKind: agentadaptor.ProfileConfigFileJSON,
						Path:     "settings.local.json",
						Values:   map[string]any{"permissionMode": "acceptEdits"},
					}},
				},
				ExpectedSyncResources: []adaptertest.ExpectedProfileResource{{
					Kind:            agentadaptor.ProfileResourceConfig,
					Managed:         []string{"native-permission"},
					Support:         agentadaptor.ProfileResourceSupportNativeEscape,
					Materialization: agentadaptor.ProfileResourceMaterializationNativeManaged,
				}},
			},
			{
				Name: "unsupported_config_capability",
				ProfileResources: agentadaptor.ProfileResources{
					Config: []agentadaptor.ProfileConfigPatch{{
						Key:        "sandbox",
						Capability: "sandbox",
						Values:     map[string]any{"mode": "workspace-write"},
					}},
				},
				ExpectedSnapshotResources: []adaptertest.ExpectedProfileResource{{
					Kind:            agentadaptor.ProfileResourceConfig,
					Support:         agentadaptor.ProfileResourceSupportUnsupported,
					Materialization: agentadaptor.ProfileResourceMaterializationNotMaterialized,
					Warnings: []string{
						"config capability patches are not materialized by this adapter yet",
						`config capability patch "sandbox" is unsupported by this adapter`,
					},
				}},
				ExpectedSyncErrorContains: `config capability patch "sandbox" is unsupported by adapter "claude"`,
			},
		},
	})
}
