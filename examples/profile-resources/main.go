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

func main() {
	agent := flag.String("agent", "", "Local CLI agent to use: "+exampleutil.SupportedAgents()+" (default codex, or AGENT_ADAPTOR_EXAMPLE_AGENT)")
	model := flag.String("model", "", "Model to use. Defaults by agent or CODEX_MODEL/CLAUDE_MODEL/CURSOR_MODEL.")
	command := flag.String("command", "", "Optional explicit local CLI command. Defaults by agent or CODEX_COMMAND/CLAUDE_COMMAND/CURSOR_COMMAND/PATH.")
	timeout := flag.Duration("timeout", 5*time.Minute, "Maximum time to wait for each run")
	keepWorkspace := flag.Bool("keep-workspace", false, "Keep the temporary workspace/profile after the example finishes")
	flag.Parse()

	workspaceDir, err := os.MkdirTemp("", "agent-adaptor-profile-resources-*")
	exampleutil.Must(err, "create temporary workspace")
	if !*keepWorkspace {
		defer func() { _ = os.RemoveAll(workspaceDir) }()
	}

	agentCfg := exampleutil.ResolveLiveAgentConfig(*agent, *model, *command, workspaceDir)
	if agentCfg.Agent == exampleutil.AgentCodex {
		agentCfg.ExtraArgs = append(agentCfg.ExtraArgs, "--skip-git-repo-check")
	}
	profileDir := filepath.Join(workspaceDir, agentCfg.Agent+"-profile")

	instructionsPath := filepath.Join(workspaceDir, "instructions", "team.md")
	runInstructionsPath := filepath.Join(workspaceDir, "instructions", "incident-hotfix.md")
	writeText(instructionsPath, "# Team defaults\n\nPrefer concise answers and mention the active profile resource mode.")
	writeText(runInstructionsPath, "# Incident hotfix\n\nFocus on blast radius, rollback options, and customer-visible impact.")

	defaultResources := agentadaptor.ProfileResources{
		Skills: []agentadaptor.SkillRef{
			agentadaptor.Key("repo-map"),
			agentadaptor.Key("write-proof"),
		},
		Agents: []agentadaptor.AgentSpec{
			{
				Key:          "reviewer",
				RuntimeName:  "reviewer",
				Description:  "Code review specialist",
				Instructions: "Review code changes for correctness, tests, and maintainability.",
				ToolPolicy: &agentadaptor.AgentToolPolicy{
					Allow: []string{"read", "search"},
					Deny:  []string{"network"},
				},
				Skills:   []string{"repo-map", "write-proof"},
				Metadata: map[string]string{"owner": "platform"},
			},
			{
				Key:          "security",
				RuntimeName:  "security-reviewer",
				Description:  "Security review specialist",
				Instructions: "Review for auth, secret handling, and unsafe local execution.",
			},
		},
		Hooks: []agentadaptor.HookSpec{
			{
				Key:         "pre-tool-audit",
				Event:       agentadaptor.HookEventPreTool,
				MatcherSpec: agentadaptor.HookMatcher{Subject: agentadaptor.HookMatcherSubjectCommand, Syntax: agentadaptor.HookMatcherSyntaxPrefix, Pattern: "shell"},
				Handler: agentadaptor.HookHandler{
					Type:    agentadaptor.HookHandlerCommand,
					Command: "agent-adaptor-example-hook",
					Args:    []string{"--format=json"},
					Env:     map[string]string{"AUDIT_STREAM": "profile-resources"},
				},
				FailPolicy: agentadaptor.HookFailPolicyOpen,
				Disabled:   true,
			},
		},
		Instructions: &agentadaptor.InstructionsBundleRef{
			ID:          "team-defaults",
			Path:        instructionsPath,
			Fingerprint: "instructions-v1",
			Scope:       agentadaptor.InstructionScopeProject,
			Mode:        agentadaptor.InstructionModeAdditive,
		},
		Config: defaultConfigPatches(agentCfg),
	}

	overrideResources := agentadaptor.ProfileResources{
		Skills: []agentadaptor.SkillRef{
			agentadaptor.Key("incident-diagnostics"),
		},
		Agents: []agentadaptor.AgentSpec{
			{
				Key:          "incident-reviewer",
				RuntimeName:  "incident-reviewer",
				Description:  "Incident hotfix reviewer",
				Instructions: "Focus on blast radius, rollback options, and customer-visible impact.",
			},
		},
		Hooks: []agentadaptor.HookSpec{
			{
				Key:   "post-run-summary",
				Event: agentadaptor.HookEventStop,
				Handler: agentadaptor.HookHandler{
					Type:    agentadaptor.HookHandlerCommand,
					Command: "agent-adaptor-example-hook",
					Args:    []string{"--emit-summary"},
				},
				Disabled: true,
			},
		},
		Instructions: &agentadaptor.InstructionsBundleRef{
			ID:          "incident-hotfix",
			Path:        runInstructionsPath,
			Fingerprint: "instructions-hotfix-v1",
			Scope:       agentadaptor.InstructionScopeRun,
		},
		Config: overrideConfigPatches(agentCfg),
	}

	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(exampleutil.NewLiveAgentBinding(
			agentCfg,
			agentadaptor.WithCloneProfile(profileDir, agentadaptor.CloneProfileOptions{
				IncludeSettings: true,
				IncludeMCP:      true,
				IncludeSkills:   true,
				AuthMode:        agentadaptor.CloneProfileAuthLink,
			}),
			agentadaptor.WithDefaultIdentity(agentadaptor.AgentIdentity{
				ID:        "task-42",
				TenantID:  "acme",
				ProfileID: "implementer",
				Name:      "implementer",
			}),
			agentadaptor.WithDefaultProfileResources(defaultResources),
			agentadaptor.WithDefaultMetadata("example", "profile-resources"),
		)),
		agentadaptor.WithSkillSet(agentadaptor.SkillSet{
			"repo-map": {
				Key:    "repo-map",
				Source: agentadaptor.SkillFromInline{SkillMD: "# repo-map\n\nSummarize the current repository shape before implementation."},
			},
			"write-proof": {
				Key:    "write-proof",
				Source: agentadaptor.SkillFromPath{Path: locateExampleSkill("write-proof")},
			},
			"incident-diagnostics": {
				Key:    "incident-diagnostics",
				Source: agentadaptor.SkillFromInline{SkillMD: "# incident-diagnostics\n\nPrioritize impact, rollback, and customer communication."},
			},
		}),
	)

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	admin := sdk.Admin().Default()
	beforeSync, err := admin.ProfileSnapshot(ctx)
	exampleutil.Must(err, "snapshot default profile resources before sync")
	afterSync, err := admin.SyncProfile(ctx)
	exampleutil.Must(err, "sync default profile resources")

	defaultResult, err := sdk.Run(ctx,
		"Reply with one short sentence confirming the binding-level profile resources are active.",
		exampleutil.NonInteractiveRunOption(agentadaptor.IsolationWorkspaceWrite),
	)
	exampleutil.Must(err, "run profile resources example with binding defaults")
	exampleutil.Check(defaultResult.DriverType == agentCfg.DriverType, "expected default driver type %q, got %q", agentCfg.DriverType, defaultResult.DriverType)
	checkRunSucceeded("default", defaultResult)

	overrideResult, err := sdk.Run(ctx,
		"Reply with one short sentence confirming the incident-hotfix profile resources are active.",
		agentadaptor.WithProfileResources(overrideResources),
		agentadaptor.WithMetadata("request_id", "incident-2026-04-28"),
		exampleutil.NonInteractiveRunOption(agentadaptor.IsolationWorkspaceWrite),
	)
	exampleutil.Must(err, "run profile resources example with per-run resources")
	exampleutil.Check(overrideResult.DriverType == agentCfg.DriverType, "expected override driver type %q, got %q", agentCfg.DriverType, overrideResult.DriverType)
	checkRunSucceeded("override", overrideResult)

	exampleutil.PrintJSON(map[string]any{
		"example":   "profile-resources",
		"agent":     exampleutil.LiveAgentSummary(agentCfg),
		"workspace": workspaceDir,
		"profile":   profileDir,
		"host_view": map[string]any{
			"default_profile_mode": "WithCloneProfile + WithDefaultProfileResources",
			"per_run_mode":         "WithProfileResources replaces agents/hooks/instructions/config and adds skills",
		},
		"default_resources":  profileResourcesSummary(defaultResources),
		"override_resources": profileResourcesSummary(overrideResources),
		"profile_snapshot": map[string]any{
			"before_sync": beforeSync,
			"after_sync":  afterSync,
		},
		"runs": map[string]any{
			"default": map[string]any{
				"run_id":      defaultResult.RunID,
				"driver_type": defaultResult.DriverType,
				"output":      strings.TrimSpace(defaultResult.Output),
			},
			"override": map[string]any{
				"run_id":      overrideResult.RunID,
				"driver_type": overrideResult.DriverType,
				"output":      strings.TrimSpace(overrideResult.Output),
			},
		},
	})
}

func writeText(path, content string) {
	exampleutil.Must(os.MkdirAll(filepath.Dir(path), 0o755), "create directory for %q", path)
	exampleutil.Must(os.WriteFile(path, []byte(content+"\n"), 0o644), "write %q", path)
}

func locateExampleSkill(name string) string {
	_, file, _, ok := runtime.Caller(0)
	exampleutil.Check(ok, "locate current example source")
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "internal", "skills", name))
}

func checkRunSucceeded(label string, result agentadaptor.RunResult) {
	if result.ExitCode == 0 {
		return
	}
	stdout, stderr := "", ""
	if result.RawStreams != nil {
		stdout = strings.TrimSpace(result.RawStreams.Stdout)
		stderr = strings.TrimSpace(result.RawStreams.Stderr)
	}
	exampleutil.Fatalf("%s run exit code %d; stdout=%q stderr=%q output=%q", label, result.ExitCode, firstRunLine(stdout), firstRunLine(stderr), strings.TrimSpace(result.Output))
}

func firstRunLine(value string) string {
	if value == "" {
		return ""
	}
	if idx := strings.IndexByte(value, '\n'); idx >= 0 {
		return value[:idx]
	}
	return value
}

func profileResourcesSummary(resources agentadaptor.ProfileResources) map[string]any {
	return map[string]any{
		"skills":       skillRefKeys(resources.Skills),
		"agents":       agentKeys(resources.Agents),
		"hooks":        hookKeys(resources.Hooks),
		"instructions": resources.Instructions,
		"config":       configKeys(resources.Config),
	}
}

func skillRefKeys(refs []agentadaptor.SkillRef) []string {
	keys := make([]string, 0, len(refs))
	for _, ref := range refs {
		switch value := ref.(type) {
		case agentadaptor.SkillKey:
			keys = append(keys, string(value))
		case agentadaptor.Skill:
			keys = append(keys, value.Key)
		default:
			keys = append(keys, "custom")
		}
	}
	return keys
}

func agentKeys(specs []agentadaptor.AgentSpec) []string {
	keys := make([]string, 0, len(specs))
	for _, spec := range specs {
		keys = append(keys, spec.Key)
	}
	return keys
}

func hookKeys(specs []agentadaptor.HookSpec) []string {
	keys := make([]string, 0, len(specs))
	for _, spec := range specs {
		keys = append(keys, spec.Key)
	}
	return keys
}

func configKeys(patches []agentadaptor.ProfileConfigPatch) []string {
	keys := make([]string, 0, len(patches))
	for _, patch := range patches {
		keys = append(keys, patch.Key)
	}
	return keys
}

func defaultConfigPatches(cfg exampleutil.LiveAgentConfig) []agentadaptor.ProfileConfigPatch {
	switch cfg.Agent {
	case exampleutil.AgentClaude:
		return []agentadaptor.ProfileConfigPatch{
			{Key: "model-default", Capability: "model", Values: map[string]any{"model": cfg.Model}},
			{Key: "example-env", Capability: "env", Values: map[string]any{"AGENT_ADAPTOR_EXAMPLE": "profile-resources"}},
		}
	case exampleutil.AgentCursor:
		return []agentadaptor.ProfileConfigPatch{
			{Key: "sandbox-defaults", Capability: "sandbox", Values: map[string]any{"mode": "enabled", "networkAccess": "user_config_only"}},
			{Key: "display-defaults", Capability: "display", Values: map[string]any{"showThinkingBlocks": true}},
		}
	default:
		return []agentadaptor.ProfileConfigPatch{
			{Key: "model-default", Capability: "model", Values: map[string]any{"model": cfg.Model}},
			{Key: "sandbox-defaults", Capability: "sandbox", Values: map[string]any{"mode": "workspace-write"}},
		}
	}
}

func overrideConfigPatches(cfg exampleutil.LiveAgentConfig) []agentadaptor.ProfileConfigPatch {
	switch cfg.Agent {
	case exampleutil.AgentClaude:
		return []agentadaptor.ProfileConfigPatch{
			{Key: "hotfix-permission", Capability: "permission", Values: map[string]any{"mode": "default"}},
		}
	case exampleutil.AgentCursor:
		return []agentadaptor.ProfileConfigPatch{
			{Key: "hotfix-approval", Capability: "approval", Values: map[string]any{"mode": "allowlist"}},
		}
	default:
		return []agentadaptor.ProfileConfigPatch{
			{Key: "hotfix-approval", Capability: "approval", Values: map[string]any{"mode": "on-request"}},
		}
	}
}
