// profiles/resources shows the resource half of the profile vocabulary: not only
// *where* the provider profile lives, but *what must exist inside it*.
//
// One import (profile/) covers both questions:
//
//	adaptor.WithProfile(profile.CloneNative(dir, profile.LinkAuth()))  // where
//	adaptor.WithProfileResources(profile.Resources{...})               // what
//
// WithProfile is construction scope only — the profile is part of what the
// Agent *is* and participates in the session fingerprint. WithProfileResources
// is a SharedOption, so the same value seeds the agent and can be swapped for a
// single Run. Merge rules per resource kind:
//
//	Skills        append   (the one append-merged family)
//	MCP           replaces when non-nil
//	Agents/Hooks/Config  replace and declare when non-nil (empty slice = "none")
//	Instructions  replaces and declares when non-nil
//
// ProfileState/SyncProfile report desired-vs-observed truthfully: a driver that
// cannot materialize a resource says so instead of pretending.
//
// Usage:
//
//	go run ./examples/profiles/resources -agent=codex
package main

import (
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/examples/internal/exampleutil"
	"github.com/agent-dance/agent-adaptor/profile"
	"github.com/agent-dance/agent-adaptor/skill"
)

func main() {
	agentName := flag.String("agent", "", "Local CLI agent to use: "+exampleutil.SupportedAgents()+" (default codex, or AGENT_ADAPTOR_EXAMPLE_AGENT)")
	model := flag.String("model", "", "Model to use. Defaults by agent or CODEX_MODEL/CLAUDE_MODEL/CURSOR_MODEL.")
	command := flag.String("command", "", "Optional explicit local CLI command. Defaults by agent or CODEX_COMMAND/CLAUDE_COMMAND/CURSOR_COMMAND/PATH.")
	timeout := flag.Duration("timeout", 5*time.Minute, "Maximum time to wait for each run")
	keepWorkspace := flag.Bool("keep-workspace", false, "Keep the temporary workspace/profile after the example finishes")
	flag.Parse()

	workspaceDir, err := os.MkdirTemp("", "agent-adaptor-profiles-resources-*")
	exampleutil.Must(err, "create temporary workspace")
	if !*keepWorkspace {
		defer func() { _ = os.RemoveAll(workspaceDir) }()
	}

	agentCfg := exampleutil.ResolveLiveAgentConfig(*agentName, *model, *command, workspaceDir)
	if agentCfg.Agent == exampleutil.AgentCodex {
		agentCfg.ExtraArgs = append(agentCfg.ExtraArgs, "--skip-git-repo-check")
	}
	profileDir := filepath.Join(workspaceDir, agentCfg.Agent+"-profile")

	instructionsPath := filepath.Join(workspaceDir, "instructions", "team.md")
	writeText(instructionsPath, "# Team defaults\n\nPrefer concise answers and mention the active profile resource mode.")

	// Agent-level desired state.
	defaultResources := profile.Resources{
		Skills: []adaptor.SkillRef{
			skill.Inline("repo-map", "# repo-map\n\nSummarize the current repository shape before implementation."),
			skill.Dir(exampleSkillDir("write-proof")),
		},
		Agents: []profile.SubAgent{
			{
				Key:          "reviewer",
				RuntimeName:  "reviewer",
				Description:  "Code review specialist",
				Instructions: "Review code changes for correctness, tests, and maintainability.",
				ToolPolicy: &profile.ToolPolicy{
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
		Hooks: []profile.Hook{
			{
				Key:         "pre-tool-audit",
				Event:       profile.HookEventPreTool,
				MatcherSpec: profile.HookMatcher{Subject: profile.HookMatcherSubjectCommand, Syntax: profile.HookMatcherSyntaxPrefix, Pattern: "shell"},
				Handler: profile.HookHandler{
					Type:    profile.HookHandlerCommand,
					Command: "agent-adaptor-example-hook",
					Args:    []string{"--format=json"},
					Env:     map[string]string{"AUDIT_STREAM": "profiles/resources"},
				},
				FailPolicy: profile.HookFailPolicyOpen,
				// Declared but disabled: the example ships no hook binary.
				Disabled: true,
			},
		},
		Instructions: &profile.Instructions{
			ID:          "team-defaults",
			Path:        instructionsPath,
			Fingerprint: "instructions-v1",
			Scope:       profile.InstructionScopeProject,
			Mode:        profile.InstructionModeAdditive,
		},
		Config: defaultConfigPatches(agentCfg),
	}

	// Per-run desired state for an incident hotfix. Note the asymmetry that
	// the merge rules produce: the incident skill *adds* to the agent's two
	// default skills, while agents/hooks/instructions/config *replace* them.
	overrideResources := profile.Resources{
		Skills: []adaptor.SkillRef{
			skill.Inline("incident-diagnostics", "# incident-diagnostics\n\nPrioritize impact, rollback, and customer communication."),
		},
		Agents: []profile.SubAgent{
			{
				Key:          "incident-reviewer",
				RuntimeName:  "incident-reviewer",
				Description:  "Incident hotfix reviewer",
				Instructions: "Focus on blast radius, rollback options, and customer-visible impact.",
			},
		},
		Hooks: []profile.Hook{
			{
				Key:   "post-run-summary",
				Event: profile.HookEventStop,
				Handler: profile.HookHandler{
					Type:    profile.HookHandlerCommand,
					Command: "agent-adaptor-example-hook",
					Args:    []string{"--emit-summary"},
				},
				Disabled: true,
			},
		},
		// profile.Text is the one-line spelling for an inline bundle; the
		// struct form above is for file-backed / scoped bundles.
		Instructions: profile.Text("Focus on blast radius, rollback options, and customer-visible impact."),
		Config:       overrideConfigPatches(agentCfg),
	}

	ai := adaptor.New(
		exampleutil.NewLiveDriver(agentCfg),
		adaptor.WithProfile(profile.CloneNative(profileDir,
			profile.CopySettings(), profile.CopyMCP(), profile.CopySkills(), profile.LinkAuth())),
		adaptor.WithIdentity(adaptor.Identity{ID: "task-42", Tenant: "acme", Profile: "implementer", Name: "implementer"}),
		adaptor.WithProfileResources(defaultResources),
		adaptor.WithMetadata("example", "profiles/resources"),
		exampleutil.NonInteractive(agentCfg.Agent, adaptor.WorkspaceWrite),
	)

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	// ProfileState observes without writing; SyncProfile is the mutating twin.
	beforeSync, err := ai.ProfileState(ctx)
	exampleutil.Must(err, "read profile state before sync")
	afterSync, err := ai.SyncProfile(ctx)
	exampleutil.Must(err, "sync profile resources")

	defaultRes := mustRun(ai, ctx, "Reply with one short sentence confirming the agent-level profile resources are active.")

	// Metadata merges per key, so "example" from New survives alongside the
	// per-run "request_id".
	overrideRes := mustRun(ai, ctx,
		"Reply with one short sentence confirming the incident-hotfix profile resources are active.",
		adaptor.WithProfileResources(overrideResources),
		adaptor.WithMetadata("request_id", "incident-2026-04-28"),
	)

	exampleutil.PrintJSON(map[string]any{
		"example":   "profiles/resources",
		"agent":     exampleutil.LiveAgentSummary(agentCfg),
		"workspace": workspaceDir,
		"profile":   profileDir,
		"host_view": map[string]any{
			"where": "adaptor.WithProfile(profile.CloneNative(dir, ...)) — construction scope only",
			"what":  "adaptor.WithProfileResources(profile.Resources{...}) — skills append, everything else replaces",
		},
		"default_resources":  resourcesSummary(defaultResources),
		"override_resources": resourcesSummary(overrideResources),
		"profile_snapshot": map[string]any{
			"before_sync": snapshotSummary(beforeSync),
			"after_sync":  snapshotSummary(afterSync),
		},
		"runs": map[string]any{
			"default": map[string]any{
				"run_id":   defaultRes.RunID,
				"metadata": defaultRes.Metadata,
				"output":   strings.TrimSpace(defaultRes.Text),
			},
			"override": map[string]any{
				"run_id":   overrideRes.RunID,
				"metadata": overrideRes.Metadata,
				"output":   strings.TrimSpace(overrideRes.Text),
			},
		},
	})
}

func mustRun(ai *adaptor.Agent, ctx context.Context, prompt string, opts ...adaptor.CallOption) *adaptor.Result {
	res, err := ai.Run(ctx, prompt, opts...)
	if err != nil {
		var runErr *adaptor.RunError
		if errors.As(err, &runErr) {
			exampleutil.Fatalf("run failed (%s): %s", runErr.Reason, runErr.Message)
		}
		exampleutil.Fatalf("run profiles/resources example: %v", err)
	}
	return res
}

func writeText(path, content string) {
	exampleutil.Must(os.MkdirAll(filepath.Dir(path), 0o755), "create directory for %q", path)
	exampleutil.Must(os.WriteFile(path, []byte(content+"\n"), 0o644), "write %q", path)
}

func exampleSkillDir(name string) string {
	_, file, _, ok := runtime.Caller(0)
	exampleutil.Check(ok, "locate current example source")
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "internal", "skills", name))
}

func resourcesSummary(res profile.Resources) map[string]any {
	agents := make([]string, 0, len(res.Agents))
	for _, spec := range res.Agents {
		agents = append(agents, spec.Key)
	}
	hooks := make([]string, 0, len(res.Hooks))
	for _, spec := range res.Hooks {
		hooks = append(hooks, spec.Key)
	}
	config := make([]string, 0, len(res.Config))
	for _, patch := range res.Config {
		config = append(config, patch.Key)
	}
	return map[string]any{
		"skills":       skillKeys(res.Skills),
		"agents":       agents,
		"hooks":        hooks,
		"instructions": res.Instructions,
		"config":       config,
	}
}

// skillKeys renders a SkillRef list for the report. A SkillRef is either a
// bare key (skill.Key, resolved by the host SkillProvider at run time) or a
// self-contained skill.Skill value.
func skillKeys(refs []adaptor.SkillRef) []string {
	keys := make([]string, 0, len(refs))
	for _, ref := range refs {
		switch value := ref.(type) {
		case skill.Skill:
			keys = append(keys, value.Key)
		case driver.SkillKey:
			keys = append(keys, string(value))
		default:
			keys = append(keys, "custom")
		}
	}
	return keys
}

func snapshotSummary(snapshot adaptor.ProfileSnapshot) map[string]any {
	return map[string]any{
		"kind":        snapshot.Kind,
		"fingerprint": snapshot.Fingerprint,
		"resources":   snapshot.Resources,
		"warnings":    snapshot.Warnings,
	}
}

func defaultConfigPatches(cfg exampleutil.LiveAgentConfig) []profile.ConfigPatch {
	switch cfg.Agent {
	case exampleutil.AgentClaude:
		return []profile.ConfigPatch{
			{Key: "model-default", Capability: "model", Values: map[string]any{"model": cfg.Model}},
			{Key: "example-env", Capability: "env", Values: map[string]any{"AGENT_ADAPTOR_EXAMPLE": "profiles/resources"}},
		}
	case exampleutil.AgentCursor:
		return []profile.ConfigPatch{
			{Key: "sandbox-defaults", Capability: "sandbox", Values: map[string]any{"mode": "enabled", "networkAccess": "user_config_only"}},
			{Key: "display-defaults", Capability: "display", Values: map[string]any{"showThinkingBlocks": true}},
		}
	default:
		return []profile.ConfigPatch{
			{Key: "model-default", Capability: "model", Values: map[string]any{"model": cfg.Model}},
			{Key: "sandbox-defaults", Capability: "sandbox", Values: map[string]any{"mode": "workspace-write"}},
		}
	}
}

func overrideConfigPatches(cfg exampleutil.LiveAgentConfig) []profile.ConfigPatch {
	switch cfg.Agent {
	case exampleutil.AgentClaude:
		return []profile.ConfigPatch{
			{Key: "hotfix-permission", Capability: "permission", Values: map[string]any{"mode": "default"}},
		}
	case exampleutil.AgentCursor:
		return []profile.ConfigPatch{
			{Key: "hotfix-approval", Capability: "approval", Values: map[string]any{"mode": "allowlist"}},
		}
	default:
		return []profile.ConfigPatch{
			{Key: "hotfix-approval", Capability: "approval", Values: map[string]any{"mode": "on-request"}},
		}
	}
}
