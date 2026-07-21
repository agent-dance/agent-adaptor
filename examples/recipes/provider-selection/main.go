package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/claude"
	"github.com/agent-dance/agent-adaptor/codex"
	"github.com/agent-dance/agent-adaptor/cursor"
	"github.com/agent-dance/agent-adaptor/examples/internal/exampleutil"
)

func main() {
	agent := flag.String("agent", "codex", "Agent to run: codex, claude, or cursor")
	command := flag.String("command", "", "Optional CLI executable override")
	model := flag.String("model", "", "Optional model override")
	flag.Parse()

	environment, err := exampleutil.NewTemporaryAgentEnvironment("provider-selection")
	if err != nil {
		log.Fatal(err)
	}
	defer environment.Cleanup()
	sdk := agentadaptor.New(agentadaptor.WithDefaultAgent(binding(
		*agent, *command, *model, environment.WorkspaceDir, environment.CloneProfileOption(),
	)))

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	info := sdk.Admin().Default().Info()
	descriptor := info.Descriptor
	if !descriptor.Workspace.Supported {
		log.Fatalf("%s does not support an SDK-managed workspace", info.DisplayName)
	}
	fmt.Printf("selected=%s sessions=%t structured_output=%t plan_review_ask=%t\n",
		info.DriverType,
		descriptor.Sessions.SupportsResume,
		descriptor.StructuredOutput.WorksWithRun,
		descriptor.RunPolicyCaps.PlanReview.Ask,
	)
	report, err := sdk.Admin().Default().CheckEnvironment(ctx)
	if err != nil {
		log.Fatal(err)
	}
	if !report.Healthy {
		log.Fatalf("%s environment is not ready: %s", *agent, report.Summary)
	}

	result, err := sdk.Run(ctx, "Reply with the selected agent name in one sentence.")
	if err != nil {
		log.Fatal(err)
	}
	if result.Failure != nil {
		log.Fatalf("agent run failed: %s", result.Failure.Message)
	}
	fmt.Printf("driver=%s output=%s\n", result.DriverType, result.Output)
}

func binding(agent, command, model, cwd string, opts ...agentadaptor.AgentOption) agentadaptor.AgentBinding {
	common := agentadaptor.CommonConfig{Command: command, CWD: cwd}
	switch agent {
	case "codex":
		return codex.New(agentadaptor.CodexConfig{
			CommonConfig:     common,
			Model:            model,
			SkipGitRepoCheck: true,
		}, opts...)
	case "claude":
		return claude.New(agentadaptor.ClaudeConfig{CommonConfig: common, Model: model}, opts...)
	case "cursor":
		return cursor.New(agentadaptor.CursorConfig{CommonConfig: common, Model: model}, opts...)
	default:
		log.Fatalf("unsupported agent %q", agent)
		return nil
	}
}
