//go:build claude_live

package claude_test

import (
	"context"
	"sync"
	"testing"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/claude"
	"github.com/agent-dance/agent-adaptor/memory"
)

// TestClaudePersistentProcessReuse proves route 1: with PersistentProcess
// enabled, a multi-turn session reuses ONE live claude subprocess. The
// persistent path emits a RunEventSpawn tagged persistent=true only when it
// actually spawns; a reused turn emits none. So we assert exactly one spawn on
// turn 1 and zero on turns 2+, and that reused turns are faster (no cold start).
//
// Run with: go test -tags claude_live -run TestClaudePersistentProcessReuse ./claude/
func TestClaudePersistentProcessReuse(t *testing.T) {
	requireClaudeCLI(t)
	cwd := t.TempDir()

	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(claude.New(agentadaptor.ClaudeConfig{
			CommonConfig:      agentadaptor.CommonConfig{CWD: cwd},
			Model:             "claude-haiku-4",
			PersistentProcess: true,
		},
			agentadaptor.WithDefaultRunPolicy(agentadaptor.PolicyAutonomous),
		)),
		agentadaptor.WithSessionStore(memory.NewSessionStore()),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	prompts := []string{
		"Reply with only the number 1 and nothing else.",
		"Reply with only the number 2 and nothing else.",
		"Reply with only the number 3 and nothing else.",
		"Reply with only the number 4 and nothing else.",
	}

	type turn struct {
		spawns  int
		latency time.Duration
		output  string
	}
	turns := make([]turn, 0, len(prompts))

	var sess *agentadaptor.SessionRef
	for i, p := range prompts {
		opts := []agentadaptor.RunOption{}
		if sess == nil {
			opts = append(opts, agentadaptor.WithSessionKey("claude_live_persistent", "v1"))
		} else {
			opts = append(opts, agentadaptor.WithSession(agentadaptor.SessionRequest{
				Namespace: sess.Namespace,
				Key:       sess.Key,
				ID:        sess.ID,
				Mode:      agentadaptor.SessionContinueOnly,
			}))
		}

		start := time.Now()
		h, err := sdk.Start(ctx, p, opts...)
		if err != nil {
			t.Fatalf("turn %d Start: %v", i+1, err)
		}

		var mu sync.Mutex
		spawns := 0
		done := make(chan struct{})
		go func() {
			defer close(done)
			for ev := range h.Events() {
				if ev.Type == agentadaptor.RunEventSpawn {
					if persistent, _ := ev.Data["persistent"].(bool); persistent {
						mu.Lock()
						spawns++
						mu.Unlock()
					}
				}
			}
		}()

		res, err := h.Wait(ctx)
		if err != nil {
			t.Fatalf("turn %d Wait: %v", i+1, err)
		}
		<-done
		latency := time.Since(start)

		if res.Session == nil || res.Session.ID == "" {
			t.Fatalf("turn %d missing session ref: %+v", i+1, res.Session)
		}
		sess = res.Session

		mu.Lock()
		s := spawns
		mu.Unlock()
		turns = append(turns, turn{spawns: s, latency: latency, output: res.Output})
		t.Logf("turn %d: spawns=%d latency=%s output=%q", i+1, s, latency, res.Output)
	}

	// Turn 1 must spawn exactly one persistent process.
	if turns[0].spawns != 1 {
		t.Fatalf("turn 1 expected exactly 1 persistent spawn, got %d", turns[0].spawns)
	}
	// Turns 2+ must reuse it: zero spawns.
	for i := 1; i < len(turns); i++ {
		if turns[i].spawns != 0 {
			t.Fatalf("turn %d expected 0 spawns (process reuse), got %d", i+1, turns[i].spawns)
		}
	}

	// Reused turns should be faster than the cold turn-1 (which pays process
	// cold start). Compare the fastest reused turn against turn 1 to keep the
	// assertion robust against model-latency jitter.
	fastest := turns[1].latency
	for i := 2; i < len(turns); i++ {
		if turns[i].latency < fastest {
			fastest = turns[i].latency
		}
	}
	if fastest >= turns[0].latency {
		t.Fatalf("expected a reused turn faster than cold turn 1: cold=%s fastest_reuse=%s", turns[0].latency, fastest)
	}
	t.Logf("cold turn1=%s fastest reused=%s saved>=%s", turns[0].latency, fastest, turns[0].latency-fastest)
}

// TestClaudePersistentStreamingReuse proves streaming turns also reuse one live
// process: turn 1 spawns (persistent=true), turns 2+ reuse it (zero spawns),
// and each turn still receives its own stream events routed to its handle
// (per-turn demux via the serialized read loop). Token-delta counts are logged
// rather than asserted so the test stays robust when the account cannot reach
// the model; the spawn-reuse assertion is the load-bearing proof.
//
// Run with: go test -tags claude_live -run TestClaudePersistentStreamingReuse ./claude/
func TestClaudePersistentStreamingReuse(t *testing.T) {
	requireClaudeCLI(t)
	cwd := t.TempDir()

	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(claude.New(agentadaptor.ClaudeConfig{
			CommonConfig:      agentadaptor.CommonConfig{CWD: cwd},
			Model:             "claude-haiku-4",
			PersistentProcess: true,
		},
			agentadaptor.WithDefaultRunPolicy(agentadaptor.PolicyAutonomous),
		)),
		agentadaptor.WithSessionStore(memory.NewSessionStore()),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	prompts := []string{
		"Write one short sentence about autumn.",
		"Now one short sentence about winter.",
		"Now one short sentence about spring.",
	}

	var sess *agentadaptor.SessionRef
	for i, p := range prompts {
		opts := []agentadaptor.RunOption{agentadaptor.WithStreaming()}
		if sess == nil {
			opts = append(opts, agentadaptor.WithSessionKey("claude_live_persistent_stream", "v1"))
		} else {
			opts = append(opts, agentadaptor.WithSession(agentadaptor.SessionRequest{
				Namespace: sess.Namespace,
				Key:       sess.Key,
				ID:        sess.ID,
				Mode:      agentadaptor.SessionContinueOnly,
			}))
		}

		h, err := sdk.Start(ctx, p, opts...)
		if err != nil {
			t.Fatalf("turn %d Start: %v", i+1, err)
		}

		var mu sync.Mutex
		spawns := 0
		var streamCount, deltaCount int
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			for ev := range h.Events() {
				if ev.Type == agentadaptor.RunEventSpawn {
					if persistent, _ := ev.Data["persistent"].(bool); persistent {
						mu.Lock()
						spawns++
						mu.Unlock()
					}
				}
			}
		}()
		go func() {
			defer wg.Done()
			for sp := range h.StreamEvents() {
				mu.Lock()
				streamCount++
				if sp.Kind == agentadaptor.StreamTextContent {
					deltaCount++
				}
				mu.Unlock()
			}
		}()

		res, err := h.Wait(ctx)
		if err != nil {
			t.Fatalf("turn %d Wait: %v", i+1, err)
		}
		wg.Wait()

		if res.Session == nil || res.Session.ID == "" {
			t.Fatalf("turn %d missing session ref", i+1)
		}
		sess = res.Session

		mu.Lock()
		s, sc, dc := spawns, streamCount, deltaCount
		mu.Unlock()
		t.Logf("turn %d: spawns=%d streamEvents=%d textDeltas=%d output=%q", i+1, s, sc, dc, res.Output)

		if i == 0 && s != 1 {
			t.Fatalf("turn 1 expected exactly 1 persistent spawn, got %d", s)
		}
		if i > 0 && s != 0 {
			t.Fatalf("turn %d expected 0 spawns (process reuse), got %d", i+1, s)
		}
		if sc == 0 {
			t.Fatalf("turn %d received no stream events; per-turn demux broken", i+1)
		}
	}
}

// TestClaudePersistentInteractiveReuse proves the final route-1 dimension:
// interactive Phase 3 HITL turns also reuse ONE live claude subprocess. Each
// turn drives the model to call ExitPlanMode, the PlanReview handler approves,
// and the CLI continues over the SAME long-lived stdin (turn boundary = the
// per-turn result frame, stdin never closed). We assert turn 1 spawns exactly
// one persistent process, turn 2 reuses it (zero spawns), and the PlanReview
// handler fires on both turns — i.e. control_request/control_response survives
// process reuse.
//
// Run with: go test -tags claude_live -run TestClaudePersistentInteractiveReuse -v ./claude/
func TestClaudePersistentInteractiveReuse(t *testing.T) {
	requirePhase3CLI(t)
	cmd := claudeCLIName()
	cwd := t.TempDir()

	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(claude.New(agentadaptor.ClaudeConfig{
			CommonConfig:      agentadaptor.CommonConfig{CWD: cwd, Command: cmd},
			Model:             envOr("CLAUDE_MODEL_P3", "claude-haiku-4-5"),
			PersistentProcess: true,
		})),
		agentadaptor.WithSessionStore(memory.NewSessionStore()),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	plan := func(topic string) string {
		return "Enter plan mode, design a two-step plan for " + topic + " (do not actually edit). " +
			"Call ExitPlanMode with the plan. Do not ask any questions. Do not use any other tools."
	}
	prompts := []string{
		plan("refactoring the file `main.go`"),
		plan("splitting the file `handler.go`"),
	}

	policy := agentadaptor.WithRunPolicy(agentadaptor.RunPolicy{
		HumanDecision: agentadaptor.HumanDecisionPolicy{
			Permission: agentadaptor.HumanDecisionAutoApprove, // Phase 3 requires this
			PlanReview: agentadaptor.HumanDecisionAsk,
			Question:   agentadaptor.QuestionAutoReject,
		},
	})

	var sess *agentadaptor.SessionRef
	for i, p := range prompts {
		var planCallsMu sync.Mutex
		planCalls := 0
		handler := agentadaptor.WithPlanReviewHandler(func(_ context.Context, _ agentadaptor.PlanReviewRequest) (agentadaptor.PlanReviewResponse, error) {
			planCallsMu.Lock()
			planCalls++
			planCallsMu.Unlock()
			return agentadaptor.PlanReviewResponse{Result: agentadaptor.ApprovalApproved}, nil
		})

		opts := []agentadaptor.RunOption{policy, handler}
		if sess == nil {
			opts = append(opts, agentadaptor.WithSessionKey("claude_live_persistent_hitl", "v1"))
		} else {
			opts = append(opts, agentadaptor.WithSession(agentadaptor.SessionRequest{
				Namespace: sess.Namespace,
				Key:       sess.Key,
				ID:        sess.ID,
				Mode:      agentadaptor.SessionContinueOnly,
			}))
		}

		h, err := sdk.Start(ctx, p, opts...)
		if err != nil {
			t.Fatalf("turn %d Start: %v", i+1, err)
		}

		var mu sync.Mutex
		spawns := 0
		done := make(chan struct{})
		go func() {
			defer close(done)
			for ev := range h.Events() {
				if ev.Type == agentadaptor.RunEventSpawn {
					if persistent, _ := ev.Data["persistent"].(bool); persistent {
						mu.Lock()
						spawns++
						mu.Unlock()
					}
				}
			}
		}()

		res, err := h.Wait(ctx)
		if err != nil {
			t.Fatalf("turn %d Wait: %v", i+1, err)
		}
		<-done

		if res.Session == nil || res.Session.ID == "" {
			t.Fatalf("turn %d missing session ref", i+1)
		}
		sess = res.Session

		mu.Lock()
		s := spawns
		mu.Unlock()
		planCallsMu.Lock()
		pc := planCalls
		planCallsMu.Unlock()
		t.Logf("turn %d: spawns=%d planReviewCalls=%d output=%q", i+1, s, pc, res.Output)

		if pc == 0 {
			t.Fatalf("turn %d: PlanReview handler never invoked; model likely skipped ExitPlanMode — output=%q", i+1, res.Output)
		}
		if res.Failure != nil {
			t.Fatalf("turn %d: approved plan should not fail: %+v", i+1, res.Failure)
		}
		if i == 0 && s != 1 {
			t.Fatalf("turn 1 expected exactly 1 persistent spawn, got %d", s)
		}
		if i > 0 && s != 0 {
			t.Fatalf("turn %d expected 0 spawns (interactive process reuse), got %d", i+1, s)
		}
	}
}
