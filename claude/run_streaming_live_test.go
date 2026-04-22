//go:build claude_live

package claude_test

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/claude"
	"github.com/agent-dance/agent-adaptor/internal/testutil"
	"github.com/agent-dance/agent-adaptor/memory"
)

func requireClaudeCLI(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude CLI not in PATH")
	}
}

func TestClaudeStreamingHaiku(t *testing.T) {
	requireClaudeCLI(t)
	cwd := t.TempDir()

	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(claude.New(agentadaptor.ClaudeConfig{
			CommonConfig: agentadaptor.CommonConfig{CWD: cwd},
			Model:        "claude-haiku-4",
		},
			agentadaptor.WithDefaultRunPolicy(agentadaptor.PolicyAutonomous),
		)),
		agentadaptor.WithSessionStore(memory.NewSessionStore()),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	rec := &testutil.EventRecorder{}
	handle, err := sdk.Start(ctx, "Write a haiku about autumn. Reply with only the haiku.",
		agentadaptor.WithStreaming(),
		agentadaptor.WithSessionKey("claude_live_haiku", "v1"),
	)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	streamDone := make(chan struct{})
	go func() {
		defer close(streamDone)
		for p := range handle.StreamEvents() {
			_ = rec.EmitStream(p)
		}
	}()
	eventsDone := make(chan struct{})
	go func() {
		defer close(eventsDone)
		for ev := range handle.Events() {
			_ = rec.Emit(ev)
		}
	}()

	res, err := handle.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	<-streamDone
	<-eventsDone

	stream := rec.StreamSnapshot()
	textDeltas := 0
	var assembled strings.Builder
	for _, p := range stream {
		if p.Kind == agentadaptor.StreamTextContent {
			textDeltas++
			assembled.WriteString(p.Delta)
		}
	}
	if textDeltas < 3 {
		t.Fatalf("need >=3 StreamTextContent; got %d streams=%d", textDeltas, len(stream))
	}
	sawFinish := false
	for _, p := range stream {
		if p.Kind == agentadaptor.StreamRunFinished && p.Usage != nil && p.Usage.InputTokens > 0 {
			sawFinish = true
			break
		}
	}
	if !sawFinish {
		t.Fatal("missing StreamRunFinished with InputTokens")
	}
	a := strings.TrimSpace(assembled.String())
	b := strings.TrimSpace(res.Output)
	if a != "" && b != "" && !strings.Contains(b, a) && !strings.Contains(a, b) {
		t.Fatalf("delta text diverges from Output:\n stream=%q\n output=%q", a, b)
	}
	t.Logf("deltas=%d output=%q usage=%v", textDeltas, res.Output, res.Usage)
}

func TestClaudeStreamingResumeThreadID(t *testing.T) {
	requireClaudeCLI(t)
	cwd := t.TempDir()

	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(claude.New(agentadaptor.ClaudeConfig{
			CommonConfig: agentadaptor.CommonConfig{CWD: cwd},
			Model:        "claude-haiku-4",
		},
			agentadaptor.WithDefaultRunPolicy(agentadaptor.PolicyAutonomous),
		)),
		agentadaptor.WithSessionStore(memory.NewSessionStore()),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	h1, err := sdk.Start(ctx, "Say only the word hello.",
		agentadaptor.WithStreaming(),
		agentadaptor.WithSessionKey("claude_live_resume", "thread-a"),
	)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	var threadStart string
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for p := range h1.StreamEvents() {
			if threadStart == "" && p.Kind == agentadaptor.StreamRunStarted && p.ThreadID != "" {
				threadStart = p.ThreadID
			}
		}
	}()
	go func() {
		defer wg.Done()
		for range h1.Events() {
		}
	}()

	r1, err := h1.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait 1: %v", err)
	}
	wg.Wait()

	if r1.Session == nil || r1.Session.ID == "" {
		t.Fatalf("missing session ref: %+v", r1.Session)
	}

	h2, err := sdk.Start(ctx, "Say only the word goodbye.",
		agentadaptor.WithStreaming(),
		agentadaptor.WithSession(agentadaptor.SessionRequest{
			Namespace: r1.Session.Namespace,
			Key:       r1.Session.Key,
			ID:        r1.Session.ID,
			Mode:      agentadaptor.SessionContinueOnly,
		}),
	)
	if err != nil {
		t.Fatalf("Start 2: %v", err)
	}
	for p := range h2.StreamEvents() {
		if p.Kind == agentadaptor.StreamRunStarted {
			if threadStart != "" && p.ThreadID != "" && p.ThreadID != threadStart {
				t.Fatalf("resume thread mismatch first=%q second=%q", threadStart, p.ThreadID)
			}
			break
		}
	}
	if _, err := h2.Wait(ctx); err != nil {
		t.Fatalf("Wait 2: %v", err)
	}
}

func TestClaudeStreamingToolUseRead(t *testing.T) {
	requireClaudeCLI(t)
	cwd := t.TempDir()

	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(claude.New(agentadaptor.ClaudeConfig{
			CommonConfig: agentadaptor.CommonConfig{CWD: cwd},
			Model:        "claude-haiku-4",
		},
			agentadaptor.WithDefaultRunPolicy(agentadaptor.PolicyAutonomous),
		)),
		agentadaptor.WithSessionStore(memory.NewSessionStore()),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	rec := &testutil.EventRecorder{}
	handle, err := sdk.Start(ctx,
		"In this workspace, call read_file on go.mod in the repo root if available, otherwise reply NO_TOOL. Reply with one word OK or NO_TOOL.",
		agentadaptor.WithStreaming(),
		agentadaptor.WithSessionKey("claude_live_tool", "t1"),
	)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	streamDone := make(chan struct{})
	go func() {
		defer close(streamDone)
		for p := range handle.StreamEvents() {
			_ = rec.EmitStream(p)
		}
	}()
	go func() {
		for range handle.Events() {
		}
	}()

	if _, err := handle.Wait(ctx); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	<-streamDone

	stream := rec.StreamSnapshot()
	started := 0
	for _, p := range stream {
		if p.Kind == agentadaptor.StreamToolCallStart {
			started++
		}
	}
	if started == 0 {
		t.Skip("model did not invoke a tool in this environment")
	}

	argsConcat := ""
	for _, p := range stream {
		if p.Kind == agentadaptor.StreamToolCallArgs {
			argsConcat += p.Delta
		}
	}
	if strings.TrimSpace(argsConcat) == "" {
		return
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(argsConcat)), &payload); err != nil {
		t.Fatalf("concatenated tool args not valid JSON: %v raw=%q", err, argsConcat)
	}
}
