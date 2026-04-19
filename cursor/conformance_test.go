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
	})
}
