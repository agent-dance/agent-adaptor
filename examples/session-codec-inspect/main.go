package main

import (
	"flag"
	"fmt"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/claude"
	"github.com/agent-dance/agent-adaptor/codex"
	"github.com/agent-dance/agent-adaptor/cursor"
	"github.com/agent-dance/agent-adaptor/examples/internal/exampleutil"
)

func main() {
	agent := flag.String("agent", "", "Adapter to inspect: "+exampleutil.SupportedAgents()+" (default codex, or AGENT_ADAPTOR_EXAMPLE_AGENT)")
	flag.Parse()

	name := exampleutil.ResolveLiveAgent(*agent)
	codec := agentadaptor.SessionCodecFor(adapterFor(name))
	params := codec.ToParams(&agentadaptor.DriverSessionState{
		ResumeID: name + "-session-42",
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

func adapterFor(agent string) agentadaptor.DriverAdapter {
	switch agent {
	case exampleutil.AgentClaude:
		return claude.NewAdapter()
	case exampleutil.AgentCursor:
		return cursor.NewAdapter()
	default:
		return codex.NewAdapter()
	}
}
