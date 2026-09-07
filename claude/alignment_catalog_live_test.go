//go:build claude_live

package claude_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/capability"
	"github.com/agent-dance/agent-adaptor/claude"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/profile"
	"github.com/agent-dance/agent-adaptor/skill"
)

func TestAlignmentLiveDeclaredSkillAndSubagentCompleted(t *testing.T) {
	alignmentLiveGate(t)
	const (
		skillKey     = "alignment/catalog/skill-proof"
		skillRuntime = "alignment-skill-proof"
		agentKey     = "alignment/catalog/subagent-proof"
		agentRuntime = "alignment-subagent-proof"
	)
	cfg := alignmentLiveConfig(t, envOr("CLAUDE_MODEL_P3", "claude-haiku-4-5"))
	profileDir, skillDir := t.TempDir(), t.TempDir()
	skillBody := "---\nname: " + skillRuntime + "\ndescription: Explicit Skill tool invocation probe for the alignment test.\n---\n\nAcknowledge that the skill was loaded, then continue the user's requested subagent call.\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillBody), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.CWD, "declared-agent.txt"), []byte("declared subagent read probe\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	declaredSkill := skill.Dir(skillDir)
	declaredSkill.Key = skillKey
	declaredSkill.Metadata = map[string]string{skill.MetadataRuntimeName: skillRuntime}
	agentInstructions := "Use Read to read declared-agent.txt in the workspace. Return its contents and finish. Do not launch another agent."
	a := adaptor.New(claude.Driver(cfg),
		adaptor.WithWorkspace(cfg.CWD),
		adaptor.WithProfile(profile.Dedicated(profileDir)),
		adaptor.WithPolicy(alignmentLivePolicy()),
		adaptor.WithProfileResources(profile.Resources{
			Skills: []skill.Ref{declaredSkill},
			Agents: []profile.SubAgent{{Key: agentKey, RuntimeName: agentRuntime,
				Description:  "Read the declared-agent.txt alignment fixture and return its contents.",
				Instructions: agentInstructions, ToolPolicy: &profile.ToolPolicy{Allow: []string{"Read"}}}},
		}),
	)
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := a.Close(closeCtx); err != nil {
			t.Errorf("close isolated live agent: %v", err)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	if _, err := a.SyncProfile(ctx); err != nil {
		t.Fatalf("materialize declared catalog: %v", err)
	}
	// File checks establish fixture readiness only. They are never counted as
	// invocation evidence; the assertions below require formal completed calls.
	materializedSkill, err := os.ReadFile(filepath.Join(profileDir, "skills", skillRuntime, "SKILL.md"))
	if err != nil || string(materializedSkill) != skillBody {
		t.Fatalf("declared skill was not materialized exactly: %v", err)
	}
	materializedAgent, err := os.ReadFile(filepath.Join(profileDir, "agents", agentRuntime+".md"))
	if err != nil || !strings.Contains(string(materializedAgent), agentInstructions) || !strings.Contains(string(materializedAgent), agentRuntime) {
		t.Fatalf("declared subagent was not materialized: %v", err)
	}

	type callKey struct{ scope, id string }
	type expectedCall struct{ runtime, field string }
	want := map[capability.Ref]expectedCall{
		{Kind: capability.Skill, Key: skillKey, Operation: "activate"}: {skillRuntime, "skill"},
		{Kind: capability.Subagent, Key: agentKey, Operation: "spawn"}: {agentRuntime, "subagent_type"},
	}
	started := map[callKey]capability.Ref{}
	completed := map[capability.Ref][]callKey{}
	finished := map[callKey]bool{}
	successfulResults := map[callKey]bool{}
	stream := a.Stream(ctx, "First invoke the Skill tool with skill exactly "+skillRuntime+". After that tool succeeds, invoke the Agent tool with subagent_type exactly "+agentRuntime+" and ask it to read declared-agent.txt. Wait for the subagent to finish, then reply done. Perform both real tool calls; do not replace them with reading their definitions or describing what they would do.")
	for event := range stream.Events() {
		switch e := event.(type) {
		case adaptor.ToolResult:
			successfulResults[callKey{e.ScopeID, e.ID}] = e.Result["is_error"] == false
		case adaptor.CapabilityInvocation:
			fact := e.Invocation
			if _, expected := want[fact.Ref]; !expected {
				continue
			}
			if fact.Source != capability.Provider || fact.Evidence != capability.ProviderProtocol || fact.InvocationID == "" {
				t.Errorf("declared capability lacks formal invocation evidence: kind=%s key=%q source=%s evidence=%s", fact.Ref.Kind, fact.Ref.Key, fact.Source, fact.Evidence)
				continue
			}
			key := callKey{fact.ScopeID, fact.InvocationID}
			switch fact.Phase {
			case capability.Started:
				if _, duplicate := started[key]; duplicate {
					t.Error("duplicate declared capability start")
				}
				started[key] = fact.Ref
			case capability.Completed:
				if started[key] != fact.Ref || !successfulResults[key] || fact.ErrorCode != "" {
					t.Errorf("declared capability completion lacks a matching start and successful tool result: kind=%s key=%q", fact.Ref.Kind, fact.Ref.Key)
				}
				if finished[key] {
					t.Error("duplicate declared capability completion")
				}
				finished[key] = true
				completed[fact.Ref] = append(completed[fact.Ref], key)
			default:
				t.Errorf("declared capability did not complete successfully: kind=%s key=%q phase=%s", fact.Ref.Kind, fact.Ref.Key, fact.Phase)
			}
		}
	}
	result, err := stream.Result()
	if err != nil {
		t.Fatalf("declared capability live run: %v", err)
	}
	if result.Raw().Stdout == "" || result.Raw().Terminal == nil {
		t.Fatal("formal provider audit output missing")
	}
	for ref, expected := range want {
		keys := completed[ref]
		if len(keys) == 0 {
			t.Errorf("required ProviderProtocol Completed fact missing: kind=%s canonical key=%q", ref.Kind, ref.Key)
			continue
		}
		for _, key := range keys {
			matched := false
			for _, item := range result.Transcript() {
				if item.Kind != driver.TranscriptToolCall || item.ScopeID != key.scope || item.ToolUseID != key.id {
					continue
				}
				input, _ := item.Input.(map[string]any)
				nameMatches := ref.Kind == capability.Skill && item.ToolName == "Skill" || ref.Kind == capability.Subagent && (item.ToolName == "Agent" || item.ToolName == "Task")
				matched = nameMatches && input[expected.field] == expected.runtime
			}
			if !matched {
				t.Errorf("completed canonical fact lacks the exact formal tool input: kind=%s key=%q", ref.Kind, ref.Key)
			}
		}
		t.Logf("formal capability completed: kind=%s canonical key=%q operation=%s", ref.Kind, ref.Key, ref.Operation)
	}
}
