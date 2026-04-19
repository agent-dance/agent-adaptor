package main

import (
	"fmt"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/claude"
)

func main() {
	codec := agentadaptor.SessionCodecFor(claude.NewAdapter())
	params := codec.ToParams(&agentadaptor.DriverSessionState{
		ResumeID: "claude-session-42",
		Data: map[string]string{
			agentadaptor.SessionParamCWD:             "/workspace/repo",
			agentadaptor.SessionParamWorkspaceID:     "workspace-a",
			agentadaptor.SessionParamPromptBundleKey: "bundle-a",
		},
	})

	fmt.Println("display:", params.DisplayID)
	fmt.Println("cwd:", params.Values[agentadaptor.SessionParamCWD])
	fmt.Println("guard:", codec.GuardFingerprint(params))
}
