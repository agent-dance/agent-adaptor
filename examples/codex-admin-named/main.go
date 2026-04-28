package main

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"runtime"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/examples/internal/exampleutil"
)

const (
	defaultSkillName = "write-proof"
	reviewSkillName  = "review-note"
)

func main() {
	agent := flag.String("agent", "", "Local CLI agent to use: "+exampleutil.SupportedAgents()+" (default codex, or AGENT_ADAPTOR_EXAMPLE_AGENT)")
	defaultModel := flag.String("default-model", "", "Model for the default agent. Defaults by agent or CODEX_MODEL/CLAUDE_MODEL/CURSOR_MODEL.")
	reviewModel := flag.String("review-model", "", "Model for the named review agent. Defaults by agent or CODEX_MODEL/CLAUDE_MODEL/CURSOR_MODEL.")
	command := flag.String("command", "", "Optional explicit local CLI command. Defaults by agent or CODEX_COMMAND/CLAUDE_COMMAND/CURSOR_COMMAND/PATH.")
	timeout := flag.Duration("timeout", 5*time.Minute, "Maximum time to wait for each run")
	keepProfiles := flag.Bool("keep-profiles", false, "Keep the temporary cloned profiles after the example finishes")
	flag.Parse()

	cwd, err := os.Getwd()
	exampleutil.Must(err, "resolve current working directory")
	defaultCfg := exampleutil.ResolveLiveAgentConfig(*agent, *defaultModel, *command, cwd)
	reviewCfg := defaultCfg
	reviewCfg.Model = exampleutil.ResolveAgentModel(defaultCfg.Agent, *reviewModel)

	profileRoot, err := os.MkdirTemp("", "agent-adaptor-admin-named-*")
	exampleutil.Must(err, "create temporary profile root")
	if !*keepProfiles {
		defer func() { _ = os.RemoveAll(profileRoot) }()
	}

	skillSet := agentadaptor.SkillSet{
		defaultSkillName: {
			Key:    defaultSkillName,
			Source: agentadaptor.SkillFromPath{Path: locateExampleSkill(defaultSkillName)},
		},
		reviewSkillName: {
			Key:    reviewSkillName,
			Source: agentadaptor.SkillFromPath{Path: locateExampleSkill(reviewSkillName)},
		},
	}

	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(exampleutil.NewLiveAgentBinding(
			defaultCfg,
			agentadaptor.WithCloneProfile(filepath.Join(profileRoot, "default"), agentadaptor.CloneProfileOptions{
				IncludeSettings: true,
				IncludeMCP:      true,
				IncludeSkills:   true,
				AuthMode:        agentadaptor.CloneProfileAuthLink,
			}),
			agentadaptor.WithDefaultIdentity(agentadaptor.AgentIdentity{
				ID:       "default-agent",
				TenantID: "examples",
				Name:     "default",
			}),
			agentadaptor.WithDefaultSkills(agentadaptor.Key(defaultSkillName), agentadaptor.Key(reviewSkillName)),
		)),
		agentadaptor.WithAgent("review", exampleutil.NewLiveAgentBinding(
			reviewCfg,
			agentadaptor.WithCloneProfile(filepath.Join(profileRoot, "review"), agentadaptor.CloneProfileOptions{
				IncludeSettings: true,
				IncludeMCP:      true,
				IncludeSkills:   true,
				AuthMode:        agentadaptor.CloneProfileAuthLink,
			}),
			agentadaptor.WithDefaultIdentity(agentadaptor.AgentIdentity{
				ID:       "review-agent",
				TenantID: "examples",
				Name:     "review",
			}),
			agentadaptor.WithDefaultSkills(agentadaptor.Key(reviewSkillName)),
		)),
		agentadaptor.WithSkillSet(skillSet),
	)

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	defaultResult, err := sdk.Run(ctx, "Reply with 'default agent ok' and one short sentence.",
		exampleutil.NonInteractiveRunOption(agentadaptor.IsolationWorkspaceWrite))
	exampleutil.Must(err, "run default agent")
	exampleutil.Check(defaultResult.DriverType == defaultCfg.DriverType, "expected default driver type %q, got %q", defaultCfg.DriverType, defaultResult.DriverType)
	exampleutil.Check(defaultResult.ExitCode == 0, "expected default exit code 0, got %d", defaultResult.ExitCode)

	reviewRunner, err := sdk.Agent("review")
	exampleutil.Must(err, "lookup review agent")
	reviewResult, err := reviewRunner.Run(ctx, "Reply with 'review agent ok' and one short sentence.",
		exampleutil.NonInteractiveRunOption(agentadaptor.IsolationWorkspaceWrite))
	exampleutil.Must(err, "run review agent")
	exampleutil.Check(reviewResult.DriverType == reviewCfg.DriverType, "expected review driver type %q, got %q", reviewCfg.DriverType, reviewResult.DriverType)
	exampleutil.Check(reviewResult.ExitCode == 0, "expected review exit code 0, got %d", reviewResult.ExitCode)

	admin := sdk.Admin()
	agents := admin.Agents()
	exampleutil.Check(len(agents) == 2, "expected 2 agents, got %d", len(agents))

	defaultAdmin := admin.Default()
	defaultInfo := defaultAdmin.Info()
	exampleutil.Check(defaultInfo.Default, "expected default agent info to be marked as default")
	exampleutil.Check(defaultInfo.Name == "default", "expected default agent name to be default, got %q", defaultInfo.Name)
	exampleutil.Check(defaultInfo.DriverType == defaultCfg.DriverType, "expected default admin driver type %q, got %q", defaultCfg.DriverType, defaultInfo.DriverType)

	reviewAdmin, err := admin.Agent("review")
	exampleutil.Must(err, "lookup review agent admin")
	reviewInfo := reviewAdmin.Info()
	exampleutil.Check(!reviewInfo.Default, "expected review agent info to not be default")
	exampleutil.Check(reviewInfo.Name == "review", "expected review agent name to be review, got %q", reviewInfo.Name)

	envReport, err := reviewAdmin.CheckEnvironment(ctx)
	exampleutil.Must(err, "check review agent environment")
	exampleutil.Check(envReport.Healthy, "expected review environment to be healthy: %#v", envReport)

	models, err := reviewAdmin.ListModels(ctx)
	exampleutil.Must(err, "list review agent models")
	exampleutil.Check(len(models) > 0, "expected review agent models to be non-empty")
	profile, err := reviewAdmin.GetProfile(ctx)
	exampleutil.Must(err, "load review profile")
	schema, err := reviewAdmin.ConfigSchema(ctx)
	exampleutil.Must(err, "load review config schema")
	quota, err := reviewAdmin.GetQuota(ctx)
	exampleutil.Must(err, "load review quota report")

	defaultSkills, err := defaultAdmin.ListSkills(ctx)
	exampleutil.Must(err, "list default agent skills")
	exampleutil.Check(defaultSkills.Supported, "expected default agent skills to be supported")
	exampleutil.Check(len(defaultSkills.Selected) == 2, "expected default agent selected skills length 2, got %d", len(defaultSkills.Selected))
	exampleutil.Check(len(defaultSkills.Warnings) == 0, "expected default skills warnings to be empty, got %#v", defaultSkills.Warnings)
	exampleutil.Check(!snapshotHasState(defaultSkills, agentadaptor.SkillStateMissing), "expected default skills to avoid missing entries, got %#v", defaultSkills.Entries)

	selectedSkills, err := reviewAdmin.SetSelectedSkills(ctx, []string{defaultSkillName})
	exampleutil.Must(err, "set selected skills for review agent")
	exampleutil.Check(selectedSkills.Supported, "expected selected skills snapshot to be supported")
	exampleutil.Check(len(selectedSkills.Selected) == 1, "expected selected skills length 1, got %d", len(selectedSkills.Selected))
	exampleutil.Check(selectedSkills.Selected[0] == defaultSkillName, "expected selected skill to be %q, got %q", defaultSkillName, selectedSkills.Selected[0])
	exampleutil.Check(len(selectedSkills.Warnings) == 0, "expected selected skills warnings to be empty, got %#v", selectedSkills.Warnings)
	exampleutil.Check(!snapshotHasState(selectedSkills, agentadaptor.SkillStateMissing), "expected selected skills to avoid missing entries, got %#v", selectedSkills.Entries)

	exampleutil.PrintJSON(map[string]any{
		"example":      "admin-named",
		"agent":        exampleutil.LiveAgentSummary(defaultCfg),
		"profile_root": profileRoot,
		"agents":       agents,
		"default": map[string]any{
			"result": defaultResult,
			"info":   defaultInfo,
			"skills": defaultSkills,
		},
		"review": map[string]any{
			"result":          reviewResult,
			"info":            reviewInfo,
			"environment":     envReport,
			"profile":         profile,
			"model_count":     len(models),
			"config_schema":   schema,
			"quota":           quota,
			"selected_skills": selectedSkills,
		},
	})
}

func locateExampleSkill(name string) string {
	_, file, _, ok := runtime.Caller(0)
	exampleutil.Check(ok, "locate current example source")
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "internal", "skills", name))
}

func snapshotHasState(snapshot agentadaptor.SkillSnapshot, state agentadaptor.SkillState) bool {
	for _, entry := range snapshot.Entries {
		if entry.State == state {
			return true
		}
	}
	return false
}
