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
	})
}
