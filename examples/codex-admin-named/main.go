// codex-admin-named is the v1 Inspect panel example.
//
// RE-THEME NOTE (v1 API redesign)
//
// The directory name is historical. This example used to demonstrate two things
// that no longer exist in v1:
//
//   - The *named agent registry* (agentadaptor.New + WithDefaultAgent /
//     WithAgent("review") + sdk.Agent("review")). v1 deletes it outright: an
//     Agent is a plain Go value, so "several agents" is just several variables.
//     A map[string]*adaptor.Agent is a host concern, not an SDK feature — and it
//     removes the whole error path where sdk.Agent(name) could fail at run time.
//   - sdk.Admin(), whose two-level Admin()/Admin().Agent(name) shape only made
//     sense because of that registry. It is now agent.Inspect(), a read-only
//     panel hanging off the agent you already hold, plus the three mutating
//     control-plane verbs that stayed on the Agent itself: ProfileState,
//     SyncProfile, SelectSkills.
//
// The theme is therefore now "two agents, two variables, one Inspect panel
// each". The directory keeps its name so the example index stays stable across
// the migration.
//
// Usage:
//
//	go run ./examples/codex-admin-named -agent=codex
package main

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/agent-dance/agent-adaptor/examples/internal/exampleutil"
	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/profile"
	"github.com/agent-dance/agent-adaptor/skill"
)

const (
	writeSkillName  = "write-proof"
	reviewSkillName = "review-note"
)

func main() {
	agentName := flag.String("agent", "", "Local CLI agent to use: "+exampleutil.SupportedAgents()+" (default codex, or AGENT_ADAPTOR_EXAMPLE_AGENT)")
	writerModel := flag.String("writer-model", "", "Model for the writer agent. Defaults by agent or CODEX_MODEL/CLAUDE_MODEL/CURSOR_MODEL.")
	reviewModel := flag.String("review-model", "", "Model for the reviewer agent. Defaults by agent or CODEX_MODEL/CLAUDE_MODEL/CURSOR_MODEL.")
	command := flag.String("command", "", "Optional explicit local CLI command. Defaults by agent or CODEX_COMMAND/CLAUDE_COMMAND/CURSOR_COMMAND/PATH.")
	timeout := flag.Duration("timeout", 5*time.Minute, "Maximum time to wait for each run")
	keepProfiles := flag.Bool("keep-profiles", false, "Keep the temporary cloned profiles after the example finishes")
	flag.Parse()

	cwd, err := os.Getwd()
	exampleutil.Must(err, "resolve current working directory")
	writerCfg := exampleutil.ResolveLiveAgentConfig(*agentName, *writerModel, *command, cwd)
	reviewCfg := writerCfg
	reviewCfg.Model = exampleutil.ResolveAgentModel(writerCfg.Agent, *reviewModel)

	profileRoot, err := os.MkdirTemp("", "agent-adaptor-inspect-*")
	exampleutil.Must(err, "create temporary profile root")
	if !*keepProfiles {
		defer func() { _ = os.RemoveAll(profileRoot) }()
	}

	clone := []profile.CloneOption{
		profile.CopySettings(),
		profile.CopyMCP(),
		profile.CopySkills(),
		// LinkAuth shares the login state without copying token files.
		profile.LinkAuth(),
	}

	// Two agents = two variables. Each owns its driver value, its cloned
	// provider profile, its identity, and its default skills. Nothing registers
	// them anywhere; if a host needs lookup-by-name it owns that map.
	writer := adaptor.New(
		exampleutil.NewLiveDriver(writerCfg),
		adaptor.WithProfile(profile.CloneNative(filepath.Join(profileRoot, "writer"), clone...)),
		adaptor.WithIdentity(adaptor.Identity{ID: "writer-agent", Tenant: "examples", Name: "writer"}),
		adaptor.WithSkills(skill.Dir(exampleSkillDir(writeSkillName)), skill.Dir(exampleSkillDir(reviewSkillName))),
		exampleutil.NonInteractive(adaptor.WorkspaceWrite),
	)

	reviewer := adaptor.New(
		exampleutil.NewLiveDriver(reviewCfg),
		adaptor.WithProfile(profile.CloneNative(filepath.Join(profileRoot, "reviewer"), clone...)),
		adaptor.WithIdentity(adaptor.Identity{ID: "review-agent", Tenant: "examples", Name: "reviewer"}),
		adaptor.WithSkills(skill.Dir(exampleSkillDir(reviewSkillName))),
		exampleutil.NonInteractive(adaptor.WorkspaceWrite),
	)

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	writerRes, err := writer.Run(ctx, "Reply with 'writer agent ok' and one short sentence.")
	exampleutil.Must(err, "run writer agent")
	reviewRes, err := reviewer.Run(ctx, "Reply with 'review agent ok' and one short sentence.")
	exampleutil.Must(err, "run reviewer agent")

	// Inspect() is the read-only panel: it never mutates and it degrades
	// honestly — a driver that cannot probe something returns an explicitly
	// unsupported report instead of a fabricated success.
	panel := reviewer.Inspect()

	env, err := panel.Environment(ctx)
	exampleutil.Must(err, "probe reviewer environment")
	exampleutil.Check(env.Healthy, "expected the reviewer environment to be healthy: %#v", env)

	models, err := panel.Models(ctx)
	exampleutil.Must(err, "list reviewer models")
	exampleutil.Check(len(models) > 0, "expected a non-empty model list")

	schema, err := panel.ConfigSchema(ctx)
	exampleutil.Must(err, "load reviewer config schema")
	quota, err := panel.Quota(ctx)
	exampleutil.Must(err, "load reviewer quota report")

	writerSkills, err := writer.Inspect().Skills(ctx)
	exampleutil.Must(err, "read writer skills")
	exampleutil.Check(writerSkills.Supported, "expected the writer driver to support skills")
	exampleutil.Check(len(writerSkills.Selected) == 2, "expected 2 selected writer skills, got %d", len(writerSkills.Selected))
	exampleutil.Check(len(writerSkills.Warnings) == 0, "expected no writer skill warnings, got %#v", writerSkills.Warnings)

	// SelectSkills is a control-plane *verb* and therefore lives on the Agent,
	// not on the read-only Inspector. It returns the same snapshot shape.
	selected, err := reviewer.SelectSkills(ctx, []string{writeSkillName})
	exampleutil.Must(err, "select reviewer skills")
	exampleutil.Check(len(selected.Selected) == 1 && selected.Selected[0] == writeSkillName,
		"expected the reviewer selection to be [%s], got %#v", writeSkillName, selected.Selected)

	// ProfileState reports desired-vs-observed for the cloned profile; nothing
	// is written. SyncProfile is the mutating twin.
	state, err := reviewer.ProfileState(ctx)
	exampleutil.Must(err, "read reviewer profile state")

	exampleutil.PrintJSON(map[string]any{
		"example":      "inspect-panel",
		"agent":        exampleutil.LiveAgentSummary(writerCfg),
		"profile_root": profileRoot,
		"writer": map[string]any{
			"run_id": writerRes.RunID,
			"model":  writerRes.Model,
			"skills": writerSkills.Selected,
		},
		"reviewer": map[string]any{
			"run_id":          reviewRes.RunID,
			"model":           reviewRes.Model,
			"environment":     env,
			"model_count":     len(models),
			"config_schema":   schema,
			"quota":           quota,
			"selected_skills": selected.Selected,
			"profile": map[string]any{
				"kind":        state.Kind,
				"fingerprint": state.Fingerprint,
				"resources":   state.Resources,
				"warnings":    state.Warnings,
			},
		},
	})
}

func exampleSkillDir(name string) string {
	_, file, _, ok := runtime.Caller(0)
	exampleutil.Check(ok, "locate current example source")
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "internal", "skills", name))
}
