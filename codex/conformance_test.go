package codex

import (
	"path/filepath"
	"testing"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/adaptertest"
)

func TestCodexAdapterConformance(t *testing.T) {
	home := t.TempDir()
	adaptertest.Run(t, adaptertest.Subject{
		Name:    DriverType,
		Adapter: NewAdapter(),
		Config: agentadaptor.CodexConfig{
			CommonConfig: agentadaptor.CommonConfig{
				CWD: filepath.Join(home, "workspace"),
				Env: []agentadaptor.EnvBinding{
					{Name: "HOME", Value: home},
					{Name: "USERPROFILE", Value: home},
				},
			},
			Model: "gpt-5.4",
		},
		SessionState: &agentadaptor.DriverSessionState{
			ResumeID: "codex-session",
			Data: map[string]string{
				agentadaptor.SessionParamCWD:         filepath.Join(home, "workspace"),
				agentadaptor.SessionParamWorkspaceID: "workspace-a",
			},
		},
		RequiredSessionKeys:   []string{agentadaptor.SessionParamCWD, agentadaptor.SessionParamWorkspaceID},
		RequiredConfigFields:  []string{"command", "cwd", "model"},
		ExpectedDetectedModel: "gpt-5.4",
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
				Values:     map[string]any{"mode": "workspace-write"},
			}},
		},
		ExpectedProfileResources: []adaptertest.ExpectedProfileResource{
			{Kind: agentadaptor.ProfileResourceAgents, Managed: []string{"reviewer"}, Support: agentadaptor.ProfileResourceSupportPortableCore, Materialization: agentadaptor.ProfileResourceMaterializationNativeManaged},
			{Kind: agentadaptor.ProfileResourceHooks, Managed: []string{"pre"}, Support: agentadaptor.ProfileResourceSupportPortableCore, Materialization: agentadaptor.ProfileResourceMaterializationNativeManaged},
			{Kind: agentadaptor.ProfileResourceInstructions, Managed: []string{"team"}, Support: agentadaptor.ProfileResourceSupportPortableCore, Materialization: agentadaptor.ProfileResourceMaterializationNativeManaged},
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
						ToolPolicy:   &agentadaptor.AgentToolPolicy{Allow: []string{"shell"}},
					}},
				},
				ExpectedSyncResources: []adaptertest.ExpectedProfileResource{{
					Kind:            agentadaptor.ProfileResourceAgents,
					Managed:         []string{"reviewer-extended"},
					Support:         agentadaptor.ProfileResourceSupportPortableExtended,
					Materialization: agentadaptor.ProfileResourceMaterializationNativeManaged,
					Warnings:        []string{"tool policy is not mapped for Codex agents"},
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
						Key:      "native-sandbox",
						FileKind: agentadaptor.ProfileConfigFileTOML,
						Path:     "profiles/native.toml",
						Values:   map[string]any{"sandbox_mode": "workspace-write"},
					}},
				},
				ExpectedSyncResources: []adaptertest.ExpectedProfileResource{{
					Kind:            agentadaptor.ProfileResourceConfig,
					Managed:         []string{"native-sandbox"},
					Support:         agentadaptor.ProfileResourceSupportNativeEscape,
					Materialization: agentadaptor.ProfileResourceMaterializationNativeManaged,
				}},
			},
			{
				Name: "unsupported_config_capability",
				ProfileResources: agentadaptor.ProfileResources{
					Config: []agentadaptor.ProfileConfigPatch{{
						Key:        "env",
						Capability: "env",
						Values:     map[string]any{"FOO": "bar"},
					}},
				},
				ExpectedSnapshotResources: []adaptertest.ExpectedProfileResource{{
					Kind:            agentadaptor.ProfileResourceConfig,
					Support:         agentadaptor.ProfileResourceSupportUnsupported,
					Materialization: agentadaptor.ProfileResourceMaterializationNotMaterialized,
					Warnings: []string{
						"config capability patches are not materialized by this adapter yet",
						`config capability patch "env" is unsupported by this adapter`,
					},
				}},
				ExpectedSyncErrorContains: `config capability patch "env" is unsupported by adapter "codex"`,
			},
		},
	})
}
