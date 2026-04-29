// COPY ME: this file is a host-facing recipe registry, not SDK-internal.
//
// Pattern: every fixed task in your product (incident-hotfix, scheduled-review,
// data-migration, nightly-scan, customer-triage, …) becomes one Recipe with
// the same five fields. Adding a new recipe = adding ~10 lines below.
package main

import (
	"path/filepath"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/examples/internal/exampleutil"
)

// Recipe is one declared "task script": resources to inject + a sample prompt
// + how the recipe is meant to be triggered (binding default vs per-run).
type Recipe struct {
	Name        string
	Description string
	Trigger     string // "default (binding-level)" or "per-run via WithProfileResources"
	Resources   agentadaptor.ProfileResources
	Prompt      string
}

// Recipes returns the demo registry. Two recipes is the minimum needed to show
// the additive-vs-replace overlay rule (§4.5 of AGENTS.md). Add more by
// pasting a new entry into the map.
func Recipes(cfg exampleutil.LiveAgentConfig, instructionsDir string) map[string]Recipe {
	return map[string]Recipe{
		"base-coding":     baseCoding(cfg, instructionsDir),
		"incident-hotfix": incidentHotfix(cfg, instructionsDir),
	}
}

func baseCoding(cfg exampleutil.LiveAgentConfig, dir string) Recipe {
	return Recipe{
		Name:        "base-coding",
		Description: "Team default; safe coding tasks.",
		Trigger:     "default (binding-level)",
		Resources: agentadaptor.ProfileResources{
			Skills: []agentadaptor.SkillRef{
				agentadaptor.Key("repo-map"),
				agentadaptor.Key("write-proof"),
			},
			Agents: []agentadaptor.AgentSpec{{
				Key: "reviewer", RuntimeName: "reviewer",
				Description:  "Code review specialist",
				Instructions: "Review code changes for correctness, tests, and maintainability.",
			}},
			Hooks: []agentadaptor.HookSpec{{
				Key:         "pre-tool-audit",
				Event:       agentadaptor.HookEventPreTool,
				MatcherSpec: agentadaptor.HookMatcher{Subject: agentadaptor.HookMatcherSubjectCommand, Syntax: agentadaptor.HookMatcherSyntaxPrefix, Pattern: "shell"},
				Handler:     agentadaptor.HookHandler{Type: agentadaptor.HookHandlerCommand, Command: "agent-adaptor-example-hook", Args: []string{"--format=json"}},
				FailPolicy:  agentadaptor.HookFailPolicyOpen,
				Disabled:    true,
			}},
			Instructions: &agentadaptor.InstructionsBundleRef{
				ID: "team-defaults", Path: filepath.Join(dir, "team-defaults.md"),
				Fingerprint: "instructions-v1", Scope: agentadaptor.InstructionScopeProject, Mode: agentadaptor.InstructionModeAdditive,
			},
			Config: configFor(cfg, "base"),
		},
		Prompt: "Reply with one short sentence confirming the base-coding recipe is active.",
	}
}

func incidentHotfix(cfg exampleutil.LiveAgentConfig, dir string) Recipe {
	return Recipe{
		Name:        "incident-hotfix",
		Description: "Override for incident response.",
		Trigger:     "per-run via WithProfileResources",
		Resources: agentadaptor.ProfileResources{
			Skills: []agentadaptor.SkillRef{agentadaptor.Key("incident-diagnostics")},
			Agents: []agentadaptor.AgentSpec{{
				Key: "incident-reviewer", RuntimeName: "incident-reviewer",
				Description:  "Incident hotfix reviewer",
				Instructions: "Focus on blast radius, rollback options, and customer-visible impact.",
			}},
			Hooks: []agentadaptor.HookSpec{{
				Key:      "post-run-summary",
				Event:    agentadaptor.HookEventStop,
				Handler:  agentadaptor.HookHandler{Type: agentadaptor.HookHandlerCommand, Command: "agent-adaptor-example-hook", Args: []string{"--emit-summary"}},
				Disabled: true,
			}},
			Instructions: &agentadaptor.InstructionsBundleRef{
				ID: "incident-hotfix", Path: filepath.Join(dir, "incident-hotfix.md"),
				Fingerprint: "instructions-hotfix-v1", Scope: agentadaptor.InstructionScopeRun,
			},
			Config: configFor(cfg, "incident"),
		},
		Prompt: "Reply with one short sentence confirming the incident-hotfix recipe is active.",
	}
}

// configFor returns driver-specific ProfileConfigPatches for the given recipe.
// It is the place to encode "this recipe should run with a tighter sandbox /
// stricter approval / different model on driver X" without polluting the
// shared Recipe definitions above.
func configFor(cfg exampleutil.LiveAgentConfig, kind string) []agentadaptor.ProfileConfigPatch {
	switch cfg.Agent {
	case exampleutil.AgentClaude:
		if kind == "incident" {
			return []agentadaptor.ProfileConfigPatch{{Key: "hotfix-permission", Capability: "permission", Values: map[string]any{"mode": "default"}}}
		}
		return []agentadaptor.ProfileConfigPatch{{Key: "model-default", Capability: "model", Values: map[string]any{"model": cfg.Model}}}
	case exampleutil.AgentCursor:
		if kind == "incident" {
			return []agentadaptor.ProfileConfigPatch{{Key: "hotfix-approval", Capability: "approval", Values: map[string]any{"mode": "allowlist"}}}
		}
		return []agentadaptor.ProfileConfigPatch{{Key: "sandbox-defaults", Capability: "sandbox", Values: map[string]any{"mode": "enabled"}}}
	default: // codex
		if kind == "incident" {
			return []agentadaptor.ProfileConfigPatch{{Key: "hotfix-approval", Capability: "approval", Values: map[string]any{"mode": "on-request"}}}
		}
		return []agentadaptor.ProfileConfigPatch{{Key: "model-default", Capability: "model", Values: map[string]any{"model": cfg.Model}}}
	}
}
