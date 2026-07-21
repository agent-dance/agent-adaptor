package main

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/examples/internal/exampleutil"
)

const (
	proofSkillName    = "write-proof"
	reviewSkillName   = "review-note"
	proofExpectedText = "WRITE_PROOF_OK"
)

func main() {
	agent := flag.String("agent", "", "Local CLI agent to use: "+exampleutil.SupportedAgents()+" (default codex, or AGENT_ADAPTOR_EXAMPLE_AGENT)")
	model := flag.String("model", "", "Model to use. Defaults by agent or CODEX_MODEL/CLAUDE_MODEL/CURSOR_MODEL.")
	command := flag.String("command", "", "Optional explicit local CLI command. Defaults by agent or CODEX_COMMAND/CLAUDE_COMMAND/CURSOR_COMMAND/PATH.")
	timeout := flag.Duration("timeout", 5*time.Minute, "Maximum time to wait for the skills example")
	keepWorkspace := flag.Bool("keep-workspace", false, "Keep the temporary workspace after the example finishes")
	flag.Parse()

	workspaceDir, err := os.MkdirTemp("", "agent-adaptor-skills-live-*")
	exampleutil.Must(err, "create temporary workspace")
	cleanup := !*keepWorkspace
	defer func() {
		if cleanup {
			_ = os.RemoveAll(workspaceDir)
		}
	}()

	agentCfg := exampleutil.ResolveLiveAgentConfig(*agent, *model, *command, workspaceDir)
	if agentCfg.Agent == exampleutil.AgentCodex {
		agentCfg.SkipGitRepoCheck = true
	}
	skillDir := locateWriteProofSkill()
	proofPath := filepath.Join(workspaceDir, "proof.txt")
	clonedProfileDir := filepath.Join(workspaceDir, agentCfg.Agent+"-profile")

	skillSet := agentadaptor.SkillSet{
		proofSkillName: {
			Key:    proofSkillName,
			Source: agentadaptor.SkillFromPath{Path: skillDir},
		},
		reviewSkillName: {
			Key:    reviewSkillName,
			Source: agentadaptor.SkillFromPath{Path: locateExampleSkill(reviewSkillName)},
		},
	}

	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(exampleutil.NewLiveAgentBinding(
			agentCfg,
			agentadaptor.WithCloneProfile(clonedProfileDir, agentadaptor.CloneProfileOptions{
				IncludeSettings: true,
				IncludeMCP:      true,
				IncludeSkills:   true,
				AuthMode:        agentadaptor.CloneProfileAuthLink,
			}),
			agentadaptor.WithDefaultIdentity(agentadaptor.AgentIdentity{
				ID:       "skills-agent",
				TenantID: "examples",
				Name:     "skills-live",
			}),
			agentadaptor.WithDefaultSkills(agentadaptor.Key(proofSkillName), agentadaptor.Key(reviewSkillName)),
		)),
		agentadaptor.WithSkillSet(skillSet),
	)

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	defaultAdmin := sdk.Admin().Default()
	listedSkills, err := defaultAdmin.ListSkills(ctx)
	exampleutil.Must(err, "list default skills")
	exampleutil.Check(listedSkills.Supported, "expected listed skills to be supported")
	exampleutil.Check(len(listedSkills.Selected) == 2, "expected two default selected skills, got %d", len(listedSkills.Selected))

	selectedSkills, err := defaultAdmin.SetSelectedSkills(ctx, []string{proofSkillName})
	exampleutil.Must(err, "set selected default skills")
	exampleutil.Check(len(selectedSkills.Selected) == 1 && selectedSkills.Selected[0] == proofSkillName, "expected selected skills to contain only %q, got %#v", proofSkillName, selectedSkills.Selected)

	prompt := "Use the write-proof skill. Create the file at " + filepath.ToSlash(proofPath) +
		" with exactly this content: " + proofExpectedText + ". Do not modify any other files."
	result, err := sdk.Run(ctx, prompt,
		agentadaptor.WithSkills(agentadaptor.Key(proofSkillName)),
		exampleutil.NonInteractiveRunOption(agentadaptor.IsolationWorkspaceWrite),
	)
	exampleutil.Must(err, "run skills-live example")
	exampleutil.Check(result.Failure == nil, "skills run failed: %#v", result.Failure)
	exampleutil.Check(result.DriverType == agentCfg.DriverType, "expected driver type %q, got %q", agentCfg.DriverType, result.DriverType)
	exampleutil.Check(result.ExitCode == 0, "expected exit code 0, got %d", result.ExitCode)

	content, err := os.ReadFile(proofPath)
	exampleutil.Must(err, "read proof output %q; the selected real CLI must follow the write-proof skill instruction", proofPath)
	exampleutil.Check(strings.TrimSpace(string(content)) == proofExpectedText, "expected proof file content %q, got %q", proofExpectedText, strings.TrimSpace(string(content)))

	exampleutil.PrintJSON(map[string]any{
		"example":        "skills-live",
		"agent":          exampleutil.LiveAgentSummary(agentCfg),
		"verification":   "Confirmed runtime skill injection by asking the selected real local CLI to use write-proof and create the proof file.",
		"workspace":      workspaceDir,
		"cloned_profile": clonedProfileDir,
		"proof": map[string]any{
			"path":     proofPath,
			"contents": strings.TrimSpace(string(content)),
		},
		"list_skills":         listedSkills,
		"set_selected_skills": selectedSkills,
		"run_result":          result,
	})
}

func locateWriteProofSkill() string {
	return locateExampleSkill(proofSkillName)
}

func locateExampleSkill(name string) string {
	_, file, _, ok := runtime.Caller(0)
	exampleutil.Check(ok, "locate current example source")
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "internal", "skills", name))
}
