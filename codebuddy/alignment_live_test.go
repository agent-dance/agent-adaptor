//go:build codebuddy_live

package codebuddy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/capability"
	"github.com/agent-dance/agent-adaptor/tool"
)

// These are paid, real-provider checks. The tag and explicit environment gate
// are independent; missing CLI/authentication under an enabled gate is failure.
func TestAlignmentLiveCodeBuddyNativeAppendAndResume(t *testing.T) {
	requireCodeBuddyCLI(t)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	nonce := "T15_" + hex.EncodeToString(random[:])
	agent := newLiveAgent(t, t.TempDir(), false)
	prompt := "Report the private acceptance marker if one was supplied in your system instructions. Otherwise answer NONE."
	plain, err := agent.Run(ctx, prompt, adaptor.WithPolicy(livePolicyHeadless))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plain.Text, nonce) {
		t.Fatal("control unexpectedly contained random marker")
	}
	appendText := "Acceptance marker: " + nonce + ". When asked for the private acceptance marker, reply with exactly that marker."
	thread := agent.Thread("alignment/live/append")
	spawns := 0
	for turn := 0; turn < 2; turn++ {
		result, events, err := collectLiveStream(ctx, thread, prompt, adaptor.WithAppendSystemPrompt(appendText), adaptor.WithPolicy(livePolicyHeadless))
		if err != nil {
			t.Fatalf("append turn %d: %v", turn, err)
		}
		if !strings.Contains(result.Text, nonce) || result.Raw().Terminal == nil {
			t.Fatal("model did not receive native append or terminal missing")
		}
		for _, event := range events {
			if process, ok := event.(adaptor.ProcessInfo); ok && process.Kind == adaptor.ProcessSpawn {
				spawns++
			}
		}
	}
	if spawns != 1 {
		t.Fatalf("append same-content turns spawns=%d", spawns)
	}
	_, err = agent.Thread("alignment/live/append", adaptor.ResumeOnly()).Run(ctx, prompt, adaptor.WithAppendSystemPrompt(""))
	if !errors.Is(err, adaptor.ErrThreadIncompatible) {
		t.Fatal("ResumeOnly clearing did not reject prior append checkpoint")
	}
	result, events, err := collectLiveStream(ctx, thread, prompt, adaptor.WithAppendSystemPrompt(appendText), adaptor.WithSpawn(), adaptor.WithPolicy(livePolicyHeadless))
	if err != nil || !strings.Contains(result.Text, nonce) {
		t.Fatalf("WithSpawn lost append: %v", err)
	}
	spawns = 0
	for _, event := range events {
		if process, ok := event.(adaptor.ProcessInfo); ok && process.Kind == adaptor.ProcessSpawn {
			spawns++
		}
	}
	if spawns != 1 {
		t.Fatal("WithSpawn did not use a new process")
	}
}
func TestAlignmentLiveCodeBuddyCapabilityAndTodoResults(t *testing.T) {
	requireCodeBuddyCLI(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	var actualEcho atomic.Int32
	definition := tool.Define("alignment_echo", "Return the supplied text for the acceptance check.", func(_ context.Context, input struct {
		Text string `json:"text"`
	}) (string, error) {
		if input.Text == "VERIFY" {
			actualEcho.Add(1)
		}
		return input.Text, nil
	}, tool.ReadOnly(), tool.Revision("alignment-t15/v1"))
	agent := newLiveAgent(t, t.TempDir(), false, adaptor.WithTools(definition), adaptor.WithProfileResources(alignmentCatalogResources()), adaptor.WithBlockingEvents())
	// Public SyncProfile materializes the declared catalog. This alone cannot
	// satisfy the execution assertions below.
	snapshot, err := agent.SyncProfile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{filepath.Join("agents", alignmentCatalogAgentName+".md"): "SUBAGENT_COMPLETED", filepath.Join("skills", alignmentCatalogSkillName, "SKILL.md"): "SKILL_ACTIVATED"} {
		raw, err := os.ReadFile(filepath.Join(snapshot.Profile.Dir, path))
		if err != nil || !strings.Contains(string(raw), want) {
			t.Fatalf("declared live catalog did not materialize: %s: %v", path, err)
		}
	}
	observer := &alignmentObservationService{events: map[string][]adaptor.Event{}}
	result, events, err := collectLiveStream(ctx, agent, "Use the Skill tool to activate "+alignmentCatalogSkillName+". Then use Task or Agent with subagent_type "+alignmentCatalogAgentName+" and wait for its completed result. Call alignment_echo with text VERIFY. Then use TaskCreate to create a task named acceptance check, TaskUpdate to complete that exact task ID, and TaskList to confirm the full list. Finish by clearing the todo list using TodoWrite with oldTodos containing the observed list and newTodos=[]. Do not merely describe these operations.", adaptor.WithRunServices(observer), adaptor.WithPolicy(livePolicyHeadless))
	if err != nil {
		t.Fatal(err)
	}
	alignmentRequireCatalogInvocation(t, events, result, capability.Ref{Kind: capability.Skill, Key: alignmentCatalogSkillKey, Operation: "activate"}, alignmentCatalogSkillName)
	alignmentRequireCatalogInvocation(t, events, result, capability.Ref{Kind: capability.Subagent, Key: alignmentCatalogAgentKey, Operation: "spawn"}, alignmentCatalogAgentName)
	if actualEcho.Load() == 0 {
		t.Fatal("hosted MCP tool did not execute its real callback")
	}
	completed, actualID, cleared := false, false, false
	for _, event := range events {
		switch e := event.(type) {
		case adaptor.CapabilityInvocation:
			if e.Invocation.Ref.Kind == capability.MCP && e.Invocation.Phase == capability.Completed {
				completed = true
			}
		case adaptor.TodoUpdated:
			if len(e.Snapshot.Items) == 0 && e.Snapshot.Items != nil {
				cleared = true
			}
			for _, item := range e.Snapshot.Items {
				if !item.SyntheticID {
					actualID = true
				}
			}
		}
	}
	if !completed || !actualID || !cleared || result.Raw().Terminal == nil {
		t.Fatalf("required real facts missing: MCP complete=%v actual task ID=%v clear=%v", completed, actualID, cleared)
	}
}
