package main

import (
	"context"
	"flag"
	"path/filepath"
	"runtime"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/codex"
	"github.com/agent-dance/agent-adaptor/examples/internal/exampleutil"
	"github.com/agent-dance/agent-adaptor/examples/internal/mockkit"
)

const (
	defaultSkillName = "write-proof"
	reviewSkillName  = "review-note"
)

func main() {
	defaultModel := flag.String("default-model", "gpt-5.4", "Codex model for the default agent")
	reviewModel := flag.String("review-model", "gpt-5.4", "Codex model for the named review agent")
	command := flag.String("command", "", "Optional explicit Codex-compatible command. Defaults to the healthy external Codex command discovered from PATH.")
	timeout := flag.Duration("timeout", 5*time.Minute, "Maximum time to wait for each run")
	flag.Parse()

	commandPath, commandNote := exampleutil.RequireHealthyCodexCommand(*command)
	skillCatalog := mockkit.StaticSkillCatalog{
		Entries: map[string]agentadaptor.Skill{
			defaultSkillName: {
				Key:      defaultSkillName,
				Runtime:  defaultSkillName,
				PathHint: locateExampleSkill(defaultSkillName),
			},
			reviewSkillName: {
				Key:      reviewSkillName,
				Runtime:  reviewSkillName,
				PathHint: locateExampleSkill(reviewSkillName),
			},
		},
	}

	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(codex.New(
			agentadaptor.CodexConfig{
				CommonConfig: agentadaptor.CommonConfig{
					Command: commandPath,
				},
				Model: *defaultModel,
			},
			agentadaptor.WithDefaultIdentity(agentadaptor.AgentIdentity{
				ID:       "default-agent",
				TenantID: "examples",
				Name:     "default",
			}),
			agentadaptor.WithDefaultSkills(defaultSkillName, reviewSkillName),
		)),
		agentadaptor.WithAgent("review", codex.New(
			agentadaptor.CodexConfig{
				CommonConfig: agentadaptor.CommonConfig{
					Command: commandPath,
				},
				Model: *reviewModel,
			},
			agentadaptor.WithDefaultIdentity(agentadaptor.AgentIdentity{
				ID:       "review-agent",
				TenantID: "examples",
				Name:     "review",
			}),
			agentadaptor.WithDefaultSkills(reviewSkillName),
		)),
		agentadaptor.WithSkillCatalog(skillCatalog),
	)

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	defaultResult, err := sdk.Run(ctx, "Reply with 'default agent ok' and one short sentence.")
	exampleutil.Must(err, "run default codex agent")
	exampleutil.Check(defaultResult.DriverType == codex.DriverType, "expected default driver type %q, got %q", codex.DriverType, defaultResult.DriverType)
	exampleutil.Check(defaultResult.ExitCode == 0, "expected default exit code 0, got %d", defaultResult.ExitCode)

	reviewRunner, err := sdk.Agent("review")
	exampleutil.Must(err, "lookup review agent")
	reviewResult, err := reviewRunner.Run(ctx, "Reply with 'review agent ok' and one short sentence.")
	exampleutil.Must(err, "run review codex agent")
	exampleutil.Check(reviewResult.DriverType == codex.DriverType, "expected review driver type %q, got %q", codex.DriverType, reviewResult.DriverType)
	exampleutil.Check(reviewResult.ExitCode == 0, "expected review exit code 0, got %d", reviewResult.ExitCode)

	admin := sdk.Admin()
	agents := admin.Agents()
	exampleutil.Check(len(agents) == 2, "expected 2 agents, got %d", len(agents))

	defaultAdmin := admin.Default()
	defaultInfo := defaultAdmin.Info()
	exampleutil.Check(defaultInfo.Default, "expected default agent info to be marked as default")
	exampleutil.Check(defaultInfo.Name == "default", "expected default agent name to be default, got %q", defaultInfo.Name)
	exampleutil.Check(defaultInfo.DriverType == codex.DriverType, "expected default admin driver type %q, got %q", codex.DriverType, defaultInfo.DriverType)

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
	exampleutil.Check(len(defaultSkills.Desired) == 2, "expected default agent desired skills length 2, got %d", len(defaultSkills.Desired))
	exampleutil.Check(len(defaultSkills.Warnings) == 0, "expected default skills warnings to be empty, got %#v", defaultSkills.Warnings)
	exampleutil.Check(!snapshotHasState(defaultSkills, agentadaptor.SkillStateMissing), "expected default skills to avoid missing entries, got %#v", defaultSkills.Entries)

	syncedSkills, err := reviewAdmin.SyncSkills(ctx, []string{defaultSkillName})
	exampleutil.Must(err, "sync review agent skills")
	exampleutil.Check(syncedSkills.Supported, "expected synced skills to be supported")
	exampleutil.Check(len(syncedSkills.Desired) == 1, "expected synced desired skills length 1, got %d", len(syncedSkills.Desired))
	exampleutil.Check(syncedSkills.Desired[0] == defaultSkillName, "expected synced skill to be %q, got %q", defaultSkillName, syncedSkills.Desired[0])
	exampleutil.Check(len(syncedSkills.Warnings) == 0, "expected synced skills warnings to be empty, got %#v", syncedSkills.Warnings)
	exampleutil.Check(!snapshotHasState(syncedSkills, agentadaptor.SkillStateMissing), "expected synced skills to avoid missing entries, got %#v", syncedSkills.Entries)

	exampleutil.PrintJSON(map[string]any{
		"example": "codex-admin-named",
		"command": map[string]any{
			"path": commandPath,
			"note": commandNote,
		},
		"agents": agents,
		"default": map[string]any{
			"result": defaultResult,
			"info":   defaultInfo,
			"skills": defaultSkills,
		},
		"review": map[string]any{
			"result":           reviewResult,
			"info":             reviewInfo,
			"environment":      envReport,
			"profile":          profile,
			"model_count":      len(models),
			"config_schema":    schema,
			"quota":            quota,
			"synced_skill_set": syncedSkills,
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
