//go:build claude_live

package claude_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/claude"
	"github.com/agent-dance/agent-adaptor/memory"
	adaptor "github.com/agent-dance/agent-adaptor"
)

func requireClaudeCLI(t *testing.T) {
	t.Helper()
	if os.Getenv("AGENT_ADAPTOR_LIVE_CONFORMANCE") != "1" {
		t.Skip("set AGENT_ADAPTOR_LIVE_CONFORMANCE=1 in addition to -tags claude_live")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude CLI not in PATH")
	}
}

func TestClaudeStreamingHaiku(t *testing.T) {
	requireClaudeCLI(t)
	agent := newStreamingAgent(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	stream := agent.Stream(ctx, "Write a haiku about autumn. Reply with only the haiku.")
	var assembled strings.Builder
	textDeltas := 0
	sawFinishWithUsage := false
	for event := range stream.Events() {
		switch typed := event.(type) {
		case adaptor.TextDelta:
			if typed.Phase == adaptor.PhaseContent && typed.Text != "" {
				textDeltas++
				assembled.WriteString(typed.Text)
			}
		case adaptor.RunFinished:
			sawFinishWithUsage = typed.Usage != nil && typed.Usage.InputTokens > 0
		}
	}
	result, err := stream.Result()
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	if textDeltas < 3 {
		t.Fatalf("need >=3 TextDelta events; got %d", textDeltas)
	}
	if !sawFinishWithUsage {
		t.Fatal("missing RunFinished with observed input-token usage")
	}
	a, b := strings.TrimSpace(assembled.String()), strings.TrimSpace(result.Text)
	if a != "" && b != "" && !strings.Contains(b, a) && !strings.Contains(a, b) {
		t.Fatalf("delta text diverges from Text: stream=%q result=%q", a, b)
	}
}

func TestClaudeStreamingResumeThreadID(t *testing.T) {
	requireClaudeCLI(t)
	agent := newStreamingAgent(t, true)
	thread := agent.Thread("claude_live_resume/thread-a")
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	first := thread.Stream(ctx, "Say only the word hello.")
	firstThreadID := providerThreadID(first)
	if _, err := first.Result(); err != nil {
		t.Fatalf("first Result: %v", err)
	}
	checkpoint, err := thread.Checkpoint(ctx)
	if err != nil || checkpoint == nil || checkpoint.State == nil || checkpoint.State.ResumeID == "" {
		t.Fatalf("Checkpoint = (%#v, %v)", checkpoint, err)
	}

	second := thread.Stream(ctx, "Say only the word goodbye.")
	secondThreadID := providerThreadID(second)
	if _, err := second.Result(); err != nil {
		t.Fatalf("second Result: %v", err)
	}
	if firstThreadID != "" && secondThreadID != "" && firstThreadID != secondThreadID {
		t.Fatalf("resume thread mismatch first=%q second=%q", firstThreadID, secondThreadID)
	}
}

func providerThreadID(stream adaptor.Stream) string {
	var threadID string
	for event := range stream.Events() {
		if started, ok := event.(adaptor.RunStarted); ok && threadID == "" {
			threadID = started.ThreadID
		}
	}
	return threadID
}

func TestClaudeStreamingToolUseRead(t *testing.T) {
	requireClaudeCLI(t)
	agent := newStreamingAgent(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	stream := agent.Stream(ctx,
		"In this workspace, call read_file on go.mod in the repo root if available, otherwise reply NO_TOOL. Reply with one word OK or NO_TOOL.")
	started := 0
	var args strings.Builder
	for event := range stream.Events() {
		if call, ok := event.(adaptor.ToolCall); ok {
			if call.Phase == adaptor.PhaseStart {
				started++
			}
			if call.Phase == adaptor.PhaseContent {
				args.WriteString(call.ArgsDelta)
			}
		}
	}
	if _, err := stream.Result(); err != nil {
		t.Fatalf("Result: %v", err)
	}
	if started == 0 {
		t.Skip("model did not invoke a tool in this environment")
	}
	if strings.TrimSpace(args.String()) == "" {
		return
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(args.String())), &payload); err != nil {
		t.Fatalf("concatenated tool args not valid JSON: %v raw=%q", err, args.String())
	}
}

func newStreamingAgent(t *testing.T, withThreads bool) *adaptor.Agent {
	t.Helper()
	options := []adaptor.Option{
		adaptor.WithWorkspace(t.TempDir()),
		adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalPolicy{
			Permission: adaptor.ApprovalAutoApprove,
			PlanReview: adaptor.ApprovalAutoApprove,
			Question:   adaptor.QuestionAutoDeny,
		}}),
	}
	if withThreads {
		options = append(options, adaptor.WithThreadStore(memory.NewStore()))
	}
	return adaptor.New(claude.Driver(claude.Config{Model: "claude-haiku-4"}), options...)
}
