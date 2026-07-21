package main

import (
	"context"
	"flag"
	"path/filepath"
	"strings"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/examples/internal/exampleutil"
)

func main() {
	var (
		agent       = flag.String("agent", "", "Local CLI agent to use: "+exampleutil.SupportedAgents()+" (default codex, or AGENT_ADAPTOR_EXAMPLE_AGENT).")
		command     = flag.String("command", "", "Optional explicit local CLI command. Defaults by agent or CODEX_COMMAND/CLAUDE_COMMAND/CURSOR_COMMAND/PATH.")
		model       = flag.String("model", "", "Model to use. Defaults by agent or CODEX_MODEL/CLAUDE_MODEL/CURSOR_MODEL.")
		profileMode = flag.String("profile-mode", "dedicated", "Profile mode: dedicated or native. Native uses the current provider profile and may update it.")
		profileDir  = flag.String("profile", "", "Dedicated provider profile directory to materialize. Defaults to a temp dir that is kept for inspection.")
		workspace   = flag.String("workspace", "", "Workspace cwd for agent runs. Defaults to a temp dir.")
		timeout     = flag.Duration("timeout", 2*time.Minute, "Context timeout.")
		probe       = flag.Bool("probe", true, "Run lightweight local probes that show the selected CLI can read the materialized profile where the provider exposes such probes.")
		runAgent    = flag.Bool("run", false, "Also run the selected agent after syncing the profile. The Run call uses no run options.")
		prompt      = flag.String("prompt", defaultPrompt(), "Prompt used when -run=true.")
	)
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	exampleDir := currentDir()
	resolvedWorkspace := ensureDir("workspace", *workspace, "agent-adaptor-full-profile-workspace-*")
	agentCfg := exampleutil.ResolveLiveAgentConfig(*agent, *model, *command, resolvedWorkspace)
	if agentCfg.Agent == exampleutil.AgentCodex {
		agentCfg.SkipGitRepoCheck = true
	}
	layout := layoutForAgent(agentCfg.Agent)

	profileOption, profileDescription, configuredProfile, artifactDir := selectProfileOption(agentCfg.Agent, layout, *profileMode, *profileDir)
	logRoot := configuredProfile
	if artifactDir != "" {
		logRoot = artifactDir
	}
	hookLog := filepath.Join(logRoot, "profile-hook.log")
	hookProbeLog := filepath.Join(logRoot, "profile-hook-probe.log")
	mcpLog := filepath.Join(logRoot, "profile-mcp.log")
	mcpProbeLog := filepath.Join(logRoot, "profile-mcp-probe.log")

	repoRoot := filepath.Dir(filepath.Dir(exampleDir))
	mcpServerPackage := "./examples/showcases/full-profile/mcpserver"
	hookPackage := "./examples/showcases/full-profile/hook"
	resources := profileResources(agentCfg.Agent, repoRoot, mcpServerPackage, hookPackage, hookLog, mcpLog)

	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(exampleutil.NewLiveAgentBinding(
			agentCfg,
			profileOption,
			agentadaptor.WithDefaultProfileResources(resources),
			agentadaptor.WithDefaultRunPolicy(agentadaptor.PolicyAutonomous),
		)),
		agentadaptor.WithSkillSet(agentadaptor.SkillSet{
			"profile-observer": agentadaptor.InlineSkill(
				"profile-observer",
				"---\nname: profile-observer\ndescription: Inspect the effective agent profile resources before making claims.\n---\n\nWhen active, verify the managed MCP, hooks, instructions, skills, and subagent files before claiming this demo is configured.\n",
			),
		}),
	)

	admin := sdk.Admin().Default()
	profileInfo, err := admin.GetProfile(ctx)
	exampleutil.Must(err, "resolve effective profile")
	effectiveProfile := profileInfo.Dir
	if strings.TrimSpace(effectiveProfile) == "" {
		effectiveProfile = configuredProfile
	}
	layout = withProfileRoot(layout, effectiveProfile)
	before, err := admin.ProfileSnapshot(ctx)
	exampleutil.Must(err, "snapshot before sync")
	after, err := admin.SyncProfile(ctx)
	exampleutil.Must(err, "sync profile resources")

	paths := map[string]string{
		"profile":   effectiveProfile,
		"workspace": resolvedWorkspace,
		"hook_log":  hookLog,
		"mcp_log":   mcpLog,
	}
	if artifactDir != "" {
		paths["artifact_dir"] = artifactDir
	}
	output := map[string]any{
		"demo":              "full-profile",
		"run_call_contract": "When -run=true this example calls sdk.Run(ctx, prompt) with no RunOption arguments.",
		"profile_mode":      profileDescription,
		"agent":             exampleutil.LiveAgentSummary(agentCfg),
		"paths":             paths,
		"profile_resources": map[string]any{
			"before_sync": summarizeProfile(before),
			"after_sync":  summarizeProfile(after),
		},
		"materialized_files": collectEvidence(layout, effectiveProfile, resolvedWorkspace),
	}

	if *probe {
		output["local_profile_probes"] = probeProfile(ctx, agentCfg, layout, effectiveProfile, resolvedWorkspace, repoRoot, mcpServerPackage, hookPackage, hookProbeLog, mcpProbeLog)
	}

	if *runAgent {
		result, runErr := sdk.Run(ctx, *prompt)
		exampleutil.Must(runErr, "run full profile example")
		exampleutil.Check(result.Failure == nil, "full profile run failed: %#v", result.Failure)
		output["run_result"] = runResultEvidence(result)
		output["materialized_files_after_run"] = collectEvidence(layout, effectiveProfile, resolvedWorkspace)
		output["runtime_artifacts"] = map[string]any{
			"hook_log": fileEvidence(hookLog),
			"mcp_log":  fileEvidence(mcpLog),
		}
	}

	exampleutil.PrintJSON(output)
}
