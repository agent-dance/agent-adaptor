//go:build codex_live

package appserver_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"github.com/agent-dance/agent-adaptor/v2/codex"
	"github.com/agent-dance/agent-adaptor/v2/memory"
)

// TestAppServerHaiku is the canonical end-to-end smoke test for the codex
// app-server streaming path. It requires a locally installed and
// authenticated `codex` CLI and is therefore gated behind the `codex_live`
// build tag.
//
// Run with:
//
//	AGENT_ADAPTOR_LIVE_CONFORMANCE=1 go test -tags=codex_live -run TestAppServerHaiku ./codex/...
func TestAppServerHaiku(t *testing.T) {
	if os.Getenv("AGENT_ADAPTOR_LIVE_CONFORMANCE") != "1" {
		t.Skip("set AGENT_ADAPTOR_LIVE_CONFORMANCE=1 in addition to -tags codex_live")
	}
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("codex CLI not in PATH")
	}
	workspace := t.TempDir()

	agent := adaptor.New(
		codex.Driver(liveCodexConfig(workspace)),
		adaptor.WithThreadStore(memory.NewStore()),
		adaptor.WithPolicy(adaptor.Policy{
			Sandbox: adaptor.ReadOnly,
			Approvals: adaptor.ApprovalPolicy{
				Permission: adaptor.ApprovalAutoApprove,
			},
		}),
	)
	closeLiveAgent(t, agent)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	thread := agent.Thread("codex_live_test/haiku")
	stream := thread.Send(ctx, "Write a haiku about streaming text. Reply with only the haiku.")
	defer stream.Cancel()

	if stream.RunID() == "" {
		t.Fatal("RunID() returned empty string")
	}

	var events []adaptor.Event
	for event := range stream.Events() {
		events = append(events, event)
	}
	res, err := stream.Result()
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("no typed Events received")
	}

	var (
		deltas        int
		sawStart      bool
		sawFinish     bool
		lastSeq       uint64
		assembledByID = map[string]*strings.Builder{}
		firstAgentID  string
	)
	for i, event := range events {
		meta := event.Meta()
		if meta.Sequence == 0 {
			t.Fatalf("stream[%d]: Sequence not assigned", i)
		}
		if meta.Sequence <= lastSeq {
			t.Fatalf("stream[%d]: Sequence not monotonic (prev=%d got=%d)", i, lastSeq, meta.Sequence)
		}
		lastSeq = meta.Sequence
		if meta.RunID != stream.RunID() {
			t.Fatalf("stream[%d]: RunID mismatch want=%q got=%q", i, stream.RunID(), meta.RunID)
		}
		switch event := event.(type) {
		case adaptor.RunStarted:
			sawStart = true
		case adaptor.RunFinished:
			sawFinish = true
			if event.Usage == nil || event.Usage.InputTokens == 0 {
				t.Fatalf("run.finished missing usage: %+v", event.Usage)
			}
		case adaptor.TextDelta:
			if event.Phase != adaptor.PhaseContent || event.Text == "" {
				continue
			}
			deltas++
			// Accumulate per-item so we can verify that the on-wire
			// delta order matches the model's intended prose. A reorder
			// bug (the original motivation for switching off jrpc2)
			// shows up here as scrambled text vs. res.Output.
			if firstAgentID == "" {
				firstAgentID = event.MessageID
			}
			b, ok := assembledByID[event.MessageID]
			if !ok {
				b = &strings.Builder{}
				assembledByID[event.MessageID] = b
			}
			b.WriteString(event.Text)
		}
	}
	if !sawStart {
		t.Fatal("no StreamRunStarted observed")
	}
	if !sawFinish {
		t.Fatal("no StreamRunFinished observed")
	}
	if deltas < 3 {
		t.Fatalf("expected >= 3 TextDelta events; got %d", deltas)
	}
	if res.Text == "" {
		t.Fatalf("final output is empty; stream text should have accumulated")
	}
	if firstAgentID == "" {
		t.Fatal("no delta had a populated ItemID")
	}
	assembled := assembledByID[firstAgentID].String()
	if assembled == "" {
		t.Fatal("assembled stream text is empty")
	}
	// Ordering invariant: delta order must reconstruct the final output
	// verbatim. Any out-of-order delivery (the 2026-04 jrpc2 bug) makes
	// assembled drift from res.Output.
	if !strings.Contains(res.Text, assembled) && !strings.Contains(assembled, res.Text) {
		t.Fatalf("delta order does not reconstruct final output:\n  assembled=%q\n  final    =%q",
			assembled, res.Text)
	}
	checkpoint, err := thread.Checkpoint(ctx)
	if err != nil || checkpoint == nil || !checkpoint.Valid || checkpoint.State == nil || checkpoint.State.ResumeID == "" {
		t.Fatalf("thread checkpoint missing: checkpoint=%+v err=%v", checkpoint, err)
	}
	if res.Usage == nil || res.Usage.InputTokens == 0 {
		t.Fatalf("result.Usage missing or zero: %+v", res.Usage)
	}
	raw := res.Raw()
	if raw.Stdout == "" || raw.Terminal == nil || raw.Terminal.Event != "turn/completed" || len(raw.Terminal.JSON) == 0 {
		t.Fatalf("result.Raw missing app-server stdout/provider terminal: %+v", raw)
	}
	if len(res.Transcript()) == 0 {
		t.Fatal("result.Transcript is empty")
	}
	t.Logf("text=%q deltas=%d usage=%+v", res.Text, deltas, res.Usage)
}

func TestAppServerPersistentTwoTurns(t *testing.T) {
	if os.Getenv("AGENT_ADAPTOR_LIVE_CONFORMANCE") != "1" {
		t.Skip("set AGENT_ADAPTOR_LIVE_CONFORMANCE=1 in addition to -tags codex_live")
	}
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("codex CLI not in PATH")
	}
	workspace := t.TempDir()
	agent := adaptor.New(
		codex.Driver(liveCodexConfig(workspace)),
		adaptor.WithThreadStore(memory.NewStore()),
		adaptor.WithWorkspace(workspace),
		adaptor.WithPolicy(adaptor.Policy{
			Sandbox: adaptor.ReadOnly,
			Approvals: adaptor.ApprovalPolicy{
				Permission: adaptor.ApprovalAutoApprove,
			},
		}),
	)
	closeLiveAgent(t, agent)
	thread := agent.Thread("codex_live/persistent-two-turns")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	spawns := 0
	for turn, prompt := range []string{
		"Remember the exact token PERSISTENT-CODEX. Reply only ACK.",
		"Reply only with the exact token from the previous turn.",
	} {
		stream := thread.Send(ctx, prompt)
		for event := range stream.Events() {
			if process, ok := event.(adaptor.ProcessInfo); ok && process.Kind == adaptor.ProcessSpawn {
				spawns++
			}
		}
		result, err := stream.Result()
		if err != nil {
			t.Fatalf("turn %d: %v", turn+1, err)
		}
		if turn == 1 && !strings.Contains(result.Text, "PERSISTENT-CODEX") {
			t.Fatalf("second turn lost conversation context: %q", result.Text)
		}
	}
	if spawns != 1 {
		t.Fatalf("two default Thread turns spawned %d app-server processes, want 1", spawns)
	}
}

func TestAppServerStructuredOutput(t *testing.T) {
	if os.Getenv("AGENT_ADAPTOR_LIVE_CONFORMANCE") != "1" {
		t.Skip("set AGENT_ADAPTOR_LIVE_CONFORMANCE=1 in addition to -tags codex_live")
	}
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("codex CLI not in PATH")
	}
	workspace := t.TempDir()
	agent := adaptor.New(
		codex.Driver(liveCodexConfig(workspace)),
		adaptor.WithThreadStore(memory.NewStore()),
		adaptor.WithWorkspace(workspace),
		adaptor.WithPolicy(adaptor.Policy{Sandbox: adaptor.ReadOnly, Approvals: adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAutoApprove}}),
	)
	closeLiveAgent(t, agent)
	thread := agent.Thread("codex_live/structured-output")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	type reply struct {
		OK    bool   `json:"ok"`
		Step  int    `json:"step"`
		Token string `json:"token"`
	}
	prompts := []string{
		`Remember the exact token SCHEMA-CONTINUITY. Return ok=true, step=1, and token="SCHEMA-CONTINUITY".`,
		`Return ok=true, step=2, and the exact token remembered in the previous turn.`,
		`Return ok=true, step=3, and the same remembered token.`,
	}
	spawns := 0
	baselineSpawns := 0
	resumeID := ""
	for turn, prompt := range prompts {
		opts := []adaptor.CallOption{adaptor.WithSchema[reply]()}
		if turn == 2 {
			opts = append(opts, adaptor.WithSpawn())
		}
		stream := thread.Send(ctx, prompt, opts...)
		var done *adaptor.Done
		drain := func() {
			for event := range stream.Events() {
				if process, ok := event.(adaptor.ProcessInfo); ok && process.Kind == adaptor.ProcessSpawn {
					spawns++
				}
				if value, ok := event.(adaptor.Done); ok {
					done = &value
				}
			}
		}
		var result *adaptor.Result
		var err error
		if turn == 0 {
			// Result must finish without an Events consumer. Read its closed
			// event buffer afterwards only to audit process and Done evidence.
			result, err = stream.Result()
			drain()
		} else {
			drain()
			result, err = stream.Result()
		}
		if err != nil || result == nil {
			t.Fatalf("turn %d: Result err=%v", turn+1, err)
		}
		if done == nil || done.Err != nil || done.Result != result {
			t.Fatalf("turn %d: Done differs from Result", turn+1)
		}
		var value reply
		if err := result.Decode(&value); err != nil || !value.OK || value.Step != turn+1 || value.Token != "SCHEMA-CONTINUITY" {
			t.Fatalf("turn %d: Decode=%+v err=%v", turn+1, value, err)
		}
		raw := result.Raw()
		if result.Metadata["transport"] != "app-server" || raw.Stdout == "" || raw.Terminal == nil || raw.Terminal.Event != "turn/completed" || !json.Valid(raw.Terminal.JSON) {
			t.Fatalf("turn %d: missing app-server raw/terminal evidence", turn+1)
		}
		if len(result.Transcript()) == 0 || result.Usage == nil || result.Usage.InputTokens <= 0 || result.Usage.OutputTokens <= 0 {
			t.Fatalf("turn %d: missing transcript/usage evidence: %+v", turn+1, result.Usage)
		}
		checkpoint, err := thread.Checkpoint(ctx)
		if err != nil || checkpoint == nil || !checkpoint.Valid || checkpoint.State == nil || checkpoint.State.ResumeID == "" {
			t.Fatalf("turn %d: missing healthy checkpoint: %v", turn+1, err)
		}
		if resumeID == "" {
			resumeID = checkpoint.State.ResumeID
		} else if checkpoint.State.ResumeID != resumeID {
			t.Fatalf("turn %d: provider thread identity changed", turn+1)
		}
		if turn == 0 {
			// ProcessInfo is ordinary and may be dropped while Result runs
			// without a consumer. Only later requests observe Events promptly.
			baselineSpawns = spawns
		} else {
			wantSpawns := baselineSpawns
			if turn == 2 {
				wantSpawns++
			}
			if spawns != wantSpawns {
				t.Fatalf("turn %d: spawns=%d, want %d", turn+1, spawns, wantSpawns)
			}
		}
		t.Logf("turn=%d step=%d spawns=%d usage=%+v", turn+1, value.Step, spawns, result.Usage)
	}
}

func liveCodexConfig(workspace string) codex.Config {
	return codex.Config{
		CommonConfig:    codex.CommonConfig{CWD: workspace},
		Model:           strings.TrimSpace(os.Getenv("AGENT_ADAPTOR_CODEX_LIVE_MODEL")),
		ReasoningEffort: "low",
	}
}

func closeLiveAgent(t *testing.T, agent *adaptor.Agent) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := agent.Close(ctx); err != nil {
			t.Errorf("Agent.Close: %v", err)
		}
	})
}
