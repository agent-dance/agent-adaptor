package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/codex"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	root, err := os.MkdirTemp("", "agent-adaptor-basic-*")
	if err != nil {
		return fmt.Errorf("create isolated environment: %w", err)
	}
	defer os.RemoveAll(root)
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		return fmt.Errorf("create temporary workspace: %w", err)
	}

	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(codex.New(agentadaptor.CodexConfig{
			CommonConfig: agentadaptor.CommonConfig{
				CWD: workspace,
			},
			SkipGitRepoCheck: true,
		}, agentadaptor.WithCloneProfile(
			filepath.Join(root, "profile"),
			agentadaptor.CloneProfileOptions{IncludeSettings: true, AuthMode: agentadaptor.CloneProfileAuthLink},
		))),
	)

	result, err := sdk.Run(ctx, "Reply in one sentence confirming the SDK call succeeded.")
	if err != nil {
		return fmt.Errorf("run agent: %w", err)
	}
	if result.Failure != nil {
		return fmt.Errorf("agent run failed: %s", result.Failure.Message)
	}

	fmt.Println(result.Output)
	return nil
}
