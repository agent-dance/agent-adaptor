package codebuddy

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/capability"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/profile"
	"github.com/agent-dance/agent-adaptor/skill"
)

const alignmentCatalogSkillKey = "alignment/catalog/skill"
const alignmentCatalogSkillName = "alignment-catalog-skill"
const alignmentCatalogAgentKey = "alignment/catalog/agent"
const alignmentCatalogAgentName = "alignment-catalog-agent"

func alignmentCatalogResources() profile.Resources {
	s := skill.Inline(alignmentCatalogSkillKey, "---\nname: "+alignmentCatalogSkillName+"\ndescription: A deterministic acceptance skill.\n---\nActivate this skill explicitly, then report SKILL_ACTIVATED.")
	s.Metadata = map[string]string{skill.MetadataRuntimeName: alignmentCatalogSkillName}
	return profile.Resources{Skills: []skill.Ref{s}, Agents: []profile.SubAgent{{Key: alignmentCatalogAgentKey, RuntimeName: alignmentCatalogAgentName, Description: "A deterministic acceptance subagent.", Instructions: "Reply with exactly SUBAGENT_COMPLETED. Do not start another agent or access files."}}}
}

func TestAlignmentCodeBuddyAgentsSyncWithoutCLI(t *testing.T) {
	root, marker := t.TempDir(), filepath.Join(t.TempDir(), "cli-access")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{CommonConfig: CommonConfig{Command: executable, Env: []driver.EnvBinding{{Name: "HOME", Value: root}, {Name: "USERPROFILE", Value: root}, {Name: "ALIGNMENT_CODEBUDDY_PROFILE_CANARY", Value: marker}}}}
	a := adaptor.New(Driver(cfg), adaptor.WithProfile(profile.Dedicated(root)), adaptor.WithProfileResources(alignmentCatalogResources()))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, syncErr := a.SyncProfile(ctx)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("SyncProfile launched CLI canary")
	}
	t.Log("CLI canary touches=0; public Config + Dedicated + Skills/Agents declaration")
	if syncErr != nil {
		t.Fatalf("declared CodeBuddy Subagent cannot materialize before CLI: %v", syncErr)
	}
	for path, want := range map[string]string{filepath.Join("agents", alignmentCatalogAgentName+".md"): "SUBAGENT_COMPLETED", filepath.Join("skills", alignmentCatalogSkillName, "SKILL.md"): "SKILL_ACTIVATED"} {
		raw, err := os.ReadFile(filepath.Join(root, path))
		if err != nil || !strings.Contains(string(raw), want) {
			t.Fatalf("materialized catalog file missing: %s: %v", path, err)
		}
	}
}

// Require a completed formal invocation joined to its real tool lifecycle and
// Transcript. Materialized resources and assistant text cannot satisfy this.
func alignmentRequireCatalogInvocation(t *testing.T, events []adaptor.Event, result *adaptor.Result, ref capability.Ref, runtimeName string) {
	t.Helper()
	starts := map[string]bool{}
	for _, event := range events {
		if e, ok := event.(adaptor.CapabilityInvocation); ok && e.Invocation.Ref == ref && e.Invocation.Evidence == capability.ProviderProtocol && e.Invocation.Source == capability.Provider && e.Invocation.InvocationID != "" && e.Invocation.Phase == capability.Started {
			starts[e.Invocation.InvocationID] = true
		}
	}
	for _, event := range events {
		e, ok := event.(adaptor.CapabilityInvocation)
		if !ok || e.Invocation.Ref != ref || e.Invocation.Evidence != capability.ProviderProtocol || e.Invocation.Source != capability.Provider || e.Invocation.Phase != capability.Completed || !starts[e.Invocation.InvocationID] {
			continue
		}
		id := e.Invocation.InvocationID
		call, resultEvent, transcriptCall, transcriptResult := false, false, false, false
		for _, event := range events {
			switch item := event.(type) {
			case adaptor.ToolCall:
				if item.ID == id && item.Phase == adaptor.PhaseStart {
					call = true
				}
			case adaptor.ToolResult:
				if item.ID == id {
					failed, ok := item.Result["is_error"].(bool)
					resultEvent = ok && !failed
				}
			}
		}
		for _, item := range result.Transcript() {
			if item.ToolUseID != id {
				continue
			}
			switch item.Kind {
			case driver.TranscriptToolCall:
				input, ok := item.Input.(map[string]any)
				if !ok {
					continue
				}
				if ref.Kind == capability.Skill {
					transcriptCall = item.ToolName == "Skill" && (exactString(input, "skill") == runtimeName || exactString(input, "command") == runtimeName)
				} else {
					transcriptCall = (item.ToolName == "Task" || item.ToolName == "Agent") && exactString(input, "subagent_type") == runtimeName
				}
			case driver.TranscriptToolResult:
				transcriptResult = !item.IsError
			}
		}
		if call && resultEvent && transcriptCall && transcriptResult {
			return
		}
	}
	t.Fatalf("required provider-protocol started/completed fact and tool/Transcript join missing for %s %s", ref.Kind, ref.Key)
}

func alignmentCatalogPartial(name, id, input string) string {
	return strings.Join([]string{
		alignmentT21JSON(map[string]any{"type": "stream_event", "parent_tool_use_id": "", "event": map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "tool_use", "id": id, "name": name, "input": map[string]any{}}}}),
		alignmentT21JSON(map[string]any{"type": "stream_event", "parent_tool_use_id": "", "event": map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": input}}}),
		`{"type":"stream_event","parent_tool_use_id":"","event":{"type":"content_block_stop","index":0}}`,
	}, "\n")
}

func TestAlignmentCodeBuddyPublicMaterializedCatalogExecution(t *testing.T) {
	protocol := strings.Join([]string{`{"type":"system","subtype":"init","session_id":"codebuddy-persistent-session"}`,
		alignmentCatalogPartial("Skill", "skill-call", `{"command":"`+alignmentCatalogSkillName+`"}`),
		alignmentCall("Skill", "skill-call", `{"command":"`+alignmentCatalogSkillName+`"}`), alignmentResult("skill-call", `"SKILL_ACTIVATED"`, false, ""),
		alignmentCatalogPartial("Task", "agent-call", `{"subagent_type":"`+alignmentCatalogAgentName+`","prompt":"Do the acceptance check."}`),
		alignmentCall("Task", "agent-call", `{"subagent_type":"`+alignmentCatalogAgentName+`","prompt":"Do the acceptance check."}`), alignmentResult("agent-call", `"SUBAGENT_COMPLETED"`, false, ""),
		`{"type":"result","subtype":"success","is_error":false,"session_id":"codebuddy-persistent-session","result":"catalog execution","usage":{"input_tokens":1,"output_tokens":1}}`, ""}, "\n")
	path, verified := filepath.Join(t.TempDir(), "protocol"), filepath.Join(t.TempDir(), "catalog-verified")
	if err := os.WriteFile(path, []byte(protocol), 0600); err != nil {
		t.Fatal(err)
	}
	observer := &alignmentObservationService{events: map[string][]adaptor.Event{}}
	fx := newPersistentCodeBuddyFixtureWithOptions(t, []driver.EnvBinding{{Name: "ALIGNMENT_CODEBUDDY_NATIVE_EXEC", Value: "1"}, {Name: "ALIGNMENT_CODEBUDDY_PROTOCOL", Value: path}, {Name: "ALIGNMENT_CODEBUDDY_CATALOG_VERIFIED", Value: verified}}, adaptor.WithProfileResources(alignmentCatalogResources()), adaptor.WithRunServices(observer), adaptor.WithBlockingEvents())
	defer fx.close()
	stream := fx.agent.Stream(context.Background(), "Use the declared catalog")
	var events []adaptor.Event
	for e := range stream.Events() {
		events = append(events, e)
	}
	result, err := stream.Result()
	if err != nil {
		t.Fatal(err)
	}
	alignmentRequireCatalogInvocation(t, events, result, capability.Ref{Kind: capability.Skill, Key: alignmentCatalogSkillKey, Operation: "activate"}, alignmentCatalogSkillName)
	alignmentRequireCatalogInvocation(t, events, result, capability.Ref{Kind: capability.Subagent, Key: alignmentCatalogAgentKey, Operation: "spawn"}, alignmentCatalogAgentName)
	if _, err := os.Stat(verified); err != nil || fx.spawnCount(t) != 1 {
		t.Fatal("real fake process did not validate materialized resources")
	}
	if result.Raw().Stdout != protocol || result.Raw().Terminal == nil || result.Text != "catalog execution" {
		t.Fatal("public output contract changed")
	}
}
