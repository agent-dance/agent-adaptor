package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"path/filepath"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/claude"
	"github.com/agent-dance/agent-adaptor/codex"
	"github.com/agent-dance/agent-adaptor/examples/internal/exampleutil"
)

func main() {
	codexCommand := flag.String("codex-command", "", "Optional Codex CLI executable")
	claudeCommand := flag.String("claude-command", "", "Optional Claude CLI executable")
	flag.Parse()

	environment, err := exampleutil.NewTemporaryAgentEnvironment("named-agent-review")
	if err != nil {
		log.Fatal(err)
	}
	defer environment.Cleanup()
	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(codex.New(agentadaptor.CodexConfig{
			CommonConfig: agentadaptor.CommonConfig{
				Command: *codexCommand, CWD: environment.WorkspaceDir,
			},
			SkipGitRepoCheck: true,
		}, environment.CloneProfileOption())),
		agentadaptor.WithAgent("review", claude.New(agentadaptor.ClaudeConfig{
			CommonConfig: agentadaptor.CommonConfig{Command: *claudeCommand, CWD: environment.WorkspaceDir},
		}, agentadaptor.WithCloneProfile(
			filepath.Join(environment.RootDir, "review-profile"),
			agentadaptor.CloneProfileOptions{IncludeSettings: true, AuthMode: agentadaptor.CloneProfileAuthLink},
		))),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	draft, err := sdk.Run(ctx, "Propose a concise plan for improving package documentation.",
		exampleutil.NonInteractiveRunOption(agentadaptor.IsolationReadOnly))
	mustSucceed(draft, err)

	reviewer, err := sdk.Agent("review")
	if err != nil {
		log.Fatal(err)
	}
	review, err := reviewer.Run(ctx, "Review this proposal and identify one risk:\n\n"+draft.Output,
		exampleutil.NonInteractiveRunOption(agentadaptor.IsolationReadOnly))
	mustSucceed(review, err)

	fmt.Printf("IMPLEMENTER\n%s\n\nREVIEWER\n%s\n", draft.Output, review.Output)
}

func mustSucceed(result agentadaptor.RunResult, err error) {
	if err != nil {
		log.Fatal(err)
	}
	if result.Failure != nil {
		log.Fatalf("agent run failed: %s", result.Failure.Message)
	}
}
