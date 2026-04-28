package cursor

import (
	"path/filepath"
	"testing"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/adaptertest"
)

func TestCursorAdapterConformance(t *testing.T) {
	t.Setenv("CURSOR_HOME", "")
	home := t.TempDir()
	adaptertest.Run(t, adaptertest.Subject{
		Name:    DriverType,
		Adapter: NewAdapter(),
		Config: agentadaptor.CursorConfig{
			CommonConfig: agentadaptor.CommonConfig{
				CWD: filepath.Join(home, "workspace"),
				Env: []agentadaptor.EnvBinding{
					{Name: "HOME", Value: home},
					{Name: "USERPROFILE", Value: home},
				},
			},
			Model: "gpt-5",
		},
		SessionState: &agentadaptor.DriverSessionState{
			ResumeID: "cursor-session",
			Data: map[string]string{
				agentadaptor.SessionParamCWD:         filepath.Join(home, "workspace"),
				agentadaptor.SessionParamWorkspaceID: "workspace-a",
			},
		},
		RequiredSessionKeys:   []string{agentadaptor.SessionParamCWD, agentadaptor.SessionParamWorkspaceID},
		RequiredConfigFields:  []string{"command", "cwd", "model"},
		ExpectedDetectedModel: "gpt-5",
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
				Key:        "sandbox",
				Capability: "sandbox",
				Values:     map[string]any{"mode": "enabled"},
			}},
		},
		ExpectedProfileResources: []adaptertest.ExpectedProfileResource{
			{Kind: agentadaptor.ProfileResourceAgents, Managed: []string{"reviewer"}, Support: agentadaptor.ProfileResourceSupportPortableCore, Materialization: agentadaptor.ProfileResourceMaterializationNativeManaged},
			{Kind: agentadaptor.ProfileResourceHooks, Managed: []string{"pre"}, Support: agentadaptor.ProfileResourceSupportPortableCore, Materialization: agentadaptor.ProfileResourceMaterializationNativeManaged},
			{Kind: agentadaptor.ProfileResourceInstructions, Managed: []string{"team"}, Support: agentadaptor.ProfileResourceSupportFallback, Materialization: agentadaptor.ProfileResourceMaterializationPromptInjected},
			{Kind: agentadaptor.ProfileResourceConfig, Managed: []string{"sandbox"}, Support: agentadaptor.ProfileResourceSupportPortableExtended, Materialization: agentadaptor.ProfileResourceMaterializationNativeManaged},
		},
		ProfileResourceCases: []adaptertest.ProfileResourceCase{
			{
				Name: "portable_extended_agents_warning",
				ProfileResources: agentadaptor.ProfileResources{
					Agents: []agentadaptor.AgentSpec{{
						Key:          "reviewer-extended",
						Description:  "Reviews risky changes.",
						Instructions: "Check correctness and tests.",
						Model:        "gpt-5",
					}},
				},
				ExpectedSyncResources: []adaptertest.ExpectedProfileResource{{
					Kind:            agentadaptor.ProfileResourceAgents,
					Managed:         []string{"reviewer-extended"},
					Support:         agentadaptor.ProfileResourceSupportPortableExtended,
					Materialization: agentadaptor.ProfileResourceMaterializationNativeManaged,
					Warnings:        []string{"extended agent fields are not mapped for Cursor agents"},
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
						Key:      "native-display",
						FileKind: agentadaptor.ProfileConfigFileJSON,
						Path:     "cli-config.local.json",
						Values:   map[string]any{"theme": "dark"},
					}},
				},
				ExpectedSyncResources: []adaptertest.ExpectedProfileResource{{
					Kind:            agentadaptor.ProfileResourceConfig,
					Managed:         []string{"native-display"},
					Support:         agentadaptor.ProfileResourceSupportNativeEscape,
					Materialization: agentadaptor.ProfileResourceMaterializationNativeManaged,
				}},
			},
			{
				Name: "unsupported_config_capability",
				ProfileResources: agentadaptor.ProfileResources{
					Config: []agentadaptor.ProfileConfigPatch{{
						Key:        "model",
						Capability: "model",
						Values:     map[string]any{"model": "gpt-5"},
					}},
				},
				ExpectedSnapshotResources: []adaptertest.ExpectedProfileResource{{
					Kind:            agentadaptor.ProfileResourceConfig,
					Support:         agentadaptor.ProfileResourceSupportUnsupported,
					Materialization: agentadaptor.ProfileResourceMaterializationNotMaterialized,
					Warnings: []string{
						"config capability patches are not materialized by this adapter yet",
						`config capability patch "model" is unsupported by this adapter`,
					},
				}},
				ExpectedSyncErrorContains: `config capability patch "model" is unsupported by adapter "cursor"`,
			},
		},
	})
}
