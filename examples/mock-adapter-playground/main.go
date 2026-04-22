package main

import (
	"context"
	"encoding/json"
	"flag"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/examples/internal/exampleutil"
	"github.com/agent-dance/agent-adaptor/examples/internal/mockkit"
)

func main() {
	timeout := flag.Duration("timeout", 30*time.Second, "Maximum time to wait for the mock playground run")
	flag.Parse()

	driver := mockkit.NewRecordingDriver("Mock Playground")
	binding := agentadaptor.BindTyped(driver, mockkit.Config{Label: "playground"},
		agentadaptor.WithDefaultIdentity(agentadaptor.AgentIdentity{
			ID:       "binding-agent",
			TenantID: "binding-tenant",
			Name:     "binding-default",
		}),
		agentadaptor.WithDefaultWorkspace(agentadaptor.SharedWorkspace{}),
		agentadaptor.WithDefaultRunPolicy(agentadaptor.RunPolicy{
			Approvals: agentadaptor.ApprovalAsk,
			Isolation: agentadaptor.IsolationReadOnly,
			WebSearch: agentadaptor.FeatureDeny,
		}),
		agentadaptor.WithDefaultInstructions(&agentadaptor.InstructionsBundleRef{
			ID:   "instructions-default",
			Path: "/defaults/instructions.md",
		}),
		agentadaptor.WithDefaultMetadata("layer", "binding"),
		agentadaptor.WithDefaultMetadata("winner", "binding"),
	)

	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(binding),
		agentadaptor.WithWorkspaceManager(mockkit.ObservingWorkspaceManager{}),
	)

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	result, err := sdk.Run(ctx, "Return the normalized request for the mock playground example.",
		agentadaptor.WithAgentIdentity(agentadaptor.AgentIdentity{
			ID:       "run-agent",
			TenantID: "run-tenant",
			Name:     "run-override",
		}),
		agentadaptor.WithWorkspace(agentadaptor.GitWorktreeWorkspace{
			BaseRef:        "main",
			BranchTemplate: "example/{task}",
		}),
		agentadaptor.WithRunPolicy(agentadaptor.RunPolicy{
			Approvals: agentadaptor.ApprovalOff,
			Isolation: agentadaptor.IsolationWorkspaceWrite,
			WebSearch: agentadaptor.FeatureAllow,
		}),
		agentadaptor.WithInstructions(&agentadaptor.InstructionsBundleRef{
			ID:   "instructions-override",
			Path: "/overrides/instructions.md",
		}),
		agentadaptor.WithMetadata("winner", "override"),
		agentadaptor.WithMetadata("request-id", "mock-playground"),
	)
	exampleutil.Must(err, "run mock adapter playground")
	exampleutil.Check(result.ExitCode == 0, "expected exit code 0, got %d", result.ExitCode)

	request := driver.LastRequest()
	exampleutil.Check(request.Agent.ID == "run-agent", "expected per-call agent ID to win, got %q", request.Agent.ID)
	exampleutil.Check(request.Agent.TenantID == "run-tenant", "expected per-call tenant ID to win, got %q", request.Agent.TenantID)
	exampleutil.Check(request.Workspace.Mode == agentadaptor.WorkspaceModeIsolated, "expected isolated workspace mode, got %q", request.Workspace.Mode)
	exampleutil.Check(request.Workspace.StrategyType == agentadaptor.WorkspaceStrategyGitWorktree, "expected git_worktree strategy, got %q", request.Workspace.StrategyType)
	exampleutil.Check(request.Workspace.Metadata["base_ref"] == "main", "expected base_ref metadata to be main, got %#v", request.Workspace.Metadata)
	exampleutil.Check(request.Workspace.Metadata["workspace_manager"] == "mockkit", "expected workspace manager metadata to be mockkit, got %#v", request.Workspace.Metadata)
	exampleutil.Check(request.Policy.Approvals == agentadaptor.ApprovalOff, "expected approval override to win, got %q", request.Policy.Approvals)
	exampleutil.Check(request.Policy.Isolation == agentadaptor.IsolationWorkspaceWrite, "expected isolation override to win, got %q", request.Policy.Isolation)
	exampleutil.Check(request.Policy.WebSearch == agentadaptor.FeatureAllow, "expected websearch override to win, got %q", request.Policy.WebSearch)
	exampleutil.Check(request.Instructions != nil && request.Instructions.Path == "/overrides/instructions.md", "expected instructions override to win, got %#v", request.Instructions)
	exampleutil.Check(request.Metadata["layer"] == "binding", "expected binding metadata to persist, got %#v", request.Metadata)
	exampleutil.Check(request.Metadata["winner"] == "override", "expected per-call metadata to override binding metadata, got %#v", request.Metadata)
	exampleutil.Check(request.Metadata["request-id"] == "mock-playground", "expected request-id metadata to be set, got %#v", request.Metadata)

	exampleutil.Check(result.RawStreams != nil, "expected RawStreams to be populated")
	var outputRequest agentadaptor.DriverRunRequest
	err = json.Unmarshal([]byte(result.RawStreams.Stdout), &outputRequest)
	exampleutil.Must(err, "decode mock driver raw stdout")

	exampleutil.PrintJSON(map[string]any{
		"example":      "mock-adapter-playground",
		"typed_config": binding.TypedConfig(),
		"request":      outputRequest,
	})
}
