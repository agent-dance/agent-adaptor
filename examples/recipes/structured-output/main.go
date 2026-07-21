package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/examples/internal/exampleutil"
)

type ProjectMetadata struct {
	ProjectName          string   `json:"project_name"`
	ProgrammingLanguages []string `json:"programming_languages"`
}

func main() {
	agent := flag.String("agent", "codex", "Agent to run: codex, claude, or cursor")
	command := flag.String("command", "", "Optional CLI executable override")
	model := flag.String("model", "", "Optional model override")
	flag.Parse()

	cwd, err := os.Getwd()
	if err != nil {
		log.Fatal(err)
	}
	cfg := exampleutil.ResolveLiveAgentConfig(*agent, *model, *command, cwd)
	environment, err := exampleutil.NewTemporaryAgentEnvironment("structured-output")
	if err != nil {
		log.Fatal(err)
	}
	defer environment.Cleanup()
	sdk := agentadaptor.New(agentadaptor.WithDefaultAgent(
		exampleutil.NewLiveAgentBinding(cfg, environment.CloneProfileOption()),
	))

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	result, err := sdk.Run(ctx, "Extract the project name and programming languages from this repository.",
		exampleutil.NonInteractiveRunOption(agentadaptor.IsolationReadOnly),
		agentadaptor.WithJSONSchemaOutputFor[ProjectMetadata](
			agentadaptor.PreferNativeOutput(),
			agentadaptor.StructuredOutputName("project_metadata"),
		),
	)
	if err != nil {
		log.Fatal(err)
	}
	if result.Failure != nil {
		log.Fatalf("agent run failed: %s", result.Failure.Message)
	}
	metadata, err := agentadaptor.DecodeStructuredOutput[ProjectMetadata](result)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("source=%s project=%s languages=%v\n",
		result.StructuredOutput.Source, metadata.ProjectName, metadata.ProgrammingLanguages)
}
