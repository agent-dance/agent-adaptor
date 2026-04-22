//go:build codex_live

package appserver_test

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/codex"
	"github.com/agent-dance/agent-adaptor/internal/testutil"
	"github.com/agent-dance/agent-adaptor/memory"
)

// TestAppServerHaiku is the canonical end-to-end smoke test for the codex
// app-server streaming path. It requires a locally installed and
// authenticated `codex` CLI and is therefore gated behind the `codex_live`
// build tag.
//
// Run with:
//
//	go test -tags=codex_live -run TestAppServerHaiku ./codex/...
func TestAppServerHaiku(t *testing.T) {
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("codex CLI not in PATH")
	}

	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(codex.New(agentadaptor.CodexConfig{
			CommonConfig: agentadaptor.CommonConfig{CWD: "/tmp"},
			Model:        "gpt-5.4",
		})),
		agentadaptor.WithSessionStore(memory.NewSessionStore()),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	handle, err := sdk.Start(ctx, "Write a haiku about streaming text. Reply with only the haiku.",
		agentadaptor.WithStreaming(),
		agentadaptor.WithSessionKey("codex_live_test", "haiku"),
		agentadaptor.WithRunPolicy(agentadaptor.RunPolicy{
			Approvals: agentadaptor.ApprovalOff,
			Isolation: agentadaptor.IsolationReadOnly,
		}),
	)
	if err != nil {
		t.Fatalf("sdk.Start: %v", err)
	}
	defer handle.Cancel(ctx)

	if handle.RunID() == "" {
		t.Fatal("RunID() returned empty string")
	}

	// Drain both channels concurrently.
	recorder := testutil.EventRecorder{}
	streamDone := make(chan struct{})
	go func() {
		defer close(streamDone)
		for p := range handle.StreamEvents() {
			_ = recorder.EmitStream(p)
		}
	}()
	eventsDone := make(chan struct{})
	go func() {
		defer close(eventsDone)
		for ev := range handle.Events() {
			_ = recorder.Emit(ev)
		}
	}()

	res, err := handle.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	<-streamDone
	<-eventsDone

	stream := recorder.StreamSnapshot()
	if len(stream) == 0 {
		t.Fatal("no StreamPayloads received")
	}

	var (
		deltas        int
		sawStart      bool
		sawFinish     bool
		seenKinds     = map[agentadaptor.StreamKind]int{}
		lastSeq       uint64
		assembledByID = map[string]*strings.Builder{}
		firstAgentID  string
	)
	for i, p := range stream {
		if p.Sequence == 0 {
			t.Fatalf("stream[%d]: Sequence not assigned", i)
		}
		if p.Sequence <= lastSeq {
			t.Fatalf("stream[%d]: Sequence not monotonic (prev=%d got=%d)", i, lastSeq, p.Sequence)
		}
		lastSeq = p.Sequence
		if p.RunID != handle.RunID() {
			t.Fatalf("stream[%d]: RunID mismatch want=%q got=%q", i, handle.RunID(), p.RunID)
		}
		seenKinds[p.Kind]++
		switch p.Kind {
		case agentadaptor.StreamRunStarted:
			sawStart = true
		case agentadaptor.StreamRunFinished:
			sawFinish = true
			if p.Usage == nil || p.Usage.InputTokens == 0 {
				t.Fatalf("run.finished missing usage: %+v", p.Usage)
			}
		case agentadaptor.StreamTextContent:
			deltas++
			// Accumulate per-item so we can verify that the on-wire
			// delta order matches the model's intended prose. A reorder
			// bug (the original motivation for switching off jrpc2)
			// shows up here as scrambled text vs. res.Output.
			if firstAgentID == "" {
				firstAgentID = p.MessageID
			}
			b, ok := assembledByID[p.MessageID]
			if !ok {
				b = &strings.Builder{}
				assembledByID[p.MessageID] = b
			}
			b.WriteString(p.Delta)
		}
	}
	if !sawStart {
		t.Fatal("no StreamRunStarted observed")
	}
	if !sawFinish {
		t.Fatal("no StreamRunFinished observed")
	}
	if deltas < 3 {
		t.Fatalf("expected >= 3 StreamTextContent deltas; got %d (kinds=%v)", deltas, seenKinds)
	}
	if res.Output == "" {
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
	if !strings.Contains(res.Output, assembled) && !strings.Contains(assembled, res.Output) {
		t.Fatalf("delta order does not reconstruct final output:\n  assembled=%q\n  final    =%q",
			assembled, res.Output)
	}
	if res.Session == nil || res.Session.ID == "" {
		t.Fatalf("result.Session missing: %+v", res.Session)
	}
	if res.Usage == nil || res.Usage.InputTokens == 0 {
		t.Fatalf("result.Usage missing or zero: %+v", res.Usage)
	}
	t.Logf("text=%q deltas=%d usage=%+v", res.Output, deltas, res.Usage)
}
