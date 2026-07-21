package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/claude"
	"github.com/agent-dance/agent-adaptor/codex"
	"github.com/agent-dance/agent-adaptor/cursor"
)

func main() {
	agent := flag.String("agent", "codex", "Agent to inspect: codex, claude, or cursor")
	command := flag.String("command", "", "Optional CLI executable override")
	flag.Parse()

	cwd, err := os.Getwd()
	if err != nil {
		log.Fatal(err)
	}
	sdk := agentadaptor.New(agentadaptor.WithDefaultAgent(binding(*agent, *command, cwd)))
	admin := sdk.Admin().Default()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	environment, err := admin.CheckEnvironment(ctx)
	if err != nil {
		log.Fatal(err)
	}
	models, err := admin.ListModels(ctx)
	if err != nil {
		log.Fatal(err)
	}
	detected, err := admin.DetectModel(ctx)
	if err != nil {
		log.Fatal(err)
	}
	profile, err := admin.GetProfile(ctx)
	if err != nil {
		log.Fatal(err)
	}
	schema, err := admin.ConfigSchema(ctx)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("driver=%s healthy=%t models=%d detected=%v profile=%s config_fields=%d\n",
		admin.Info().DriverType, environment.Healthy, len(models), detected, profile.Dir, len(schema.Fields))
}

func binding(agent, command, cwd string) agentadaptor.AgentBinding {
	common := agentadaptor.CommonConfig{Command: command, CWD: cwd}
	switch agent {
	case "codex":
		return codex.New(agentadaptor.CodexConfig{CommonConfig: common})
	case "claude":
		return claude.New(agentadaptor.ClaudeConfig{CommonConfig: common})
	case "cursor":
		return cursor.New(agentadaptor.CursorConfig{CommonConfig: common})
	default:
		log.Fatalf("unsupported agent %q", agent)
		return nil
	}
}
