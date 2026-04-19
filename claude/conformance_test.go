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
	})
}
