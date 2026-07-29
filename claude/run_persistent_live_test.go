//go:build claude_live

package claude_test

import (
	"context"
	"strings"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
)

func TestClaudePersistentProcessReuse(t *testing.T) {
	requireClaudeCLI(t)
	agent := newStreamingAgent(t, true)
	defer closeClaudeLiveAgent(t, agent)
	thread := agent.Thread("claude_live/persistent-reuse")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	spawns := 0
	for turn, prompt := range []string{
		"Remember the exact token PERSISTENT-CLAUDE. Reply only ACK.",
		"Reply only with the exact token from the previous turn.",
	} {
		result, turnSpawns, err := runClaudePersistentTurn(ctx, thread, prompt)
		if err != nil {
			t.Fatalf("turn %d: %v", turn+1, err)
		}
		spawns += turnSpawns
		if turn == 1 && !strings.Contains(result.Text, "PERSISTENT-CLAUDE") {
			t.Fatalf("second turn lost conversation context: %q", result.Text)
		}
	}
	if spawns != 1 {
		t.Fatalf("two default Thread turns spawned %d Claude processes, want 1", spawns)
	}
}

func runClaudePersistentTurn(ctx context.Context, thread *adaptor.Thread, prompt string) (*adaptor.Result, int, error) {
	stream := thread.Stream(ctx, prompt)
	spawns := 0
	for event := range stream.Events() {
		if process, ok := event.(adaptor.ProcessInfo); ok && process.Kind == adaptor.ProcessSpawn {
			spawns++
		}
	}
	result, err := stream.Result()
	return result, spawns, err
}

func closeClaudeLiveAgent(t *testing.T, agent *adaptor.Agent) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := agent.Close(ctx); err != nil {
		t.Errorf("Agent.Close: %v", err)
	}
}
