package appserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/capability"
	"github.com/agent-dance/agent-adaptor/driver"
)

func TestAlignmentMultiplexForeign(t *testing.T) {
	for _, when := range []string{"before", "after"} {
		for _, id := range []string{"child", "grandchild", "unrelated"} {
			for _, method := range []string{NotifyTurnStarted, NotifyTurnCompleted, NotifyItemStarted, NotifyItemCompleted, NotifyItemAgentMessageDelta, NotifyItemReasoningTextDelta, NotifyItemReasoningSummaryTextDelta, NotifyItemReasoningSummaryPartAdded, NotifyItemCommandExecutionOutputDelta, NotifyItemFileChangeOutputDelta, NotifyItemPlanDelta, NotifyTurnPlanUpdated, NotifyThreadTokenUsageUpdated, NotifyError} {
				t.Run(when+"/"+id+"/"+method, func(t *testing.T) {
					sink := &recordingSink{}
					s := newRunState("run", sink)
					s.setThread("parent")
					s.setTurn("turn")
					terminal := json.RawMessage(`{"threadId":"parent","turn":{"id":"turn","status":"completed"}}`)
					if when == "after" {
						s.onNotification(NotifyTurnCompleted, terminal)
					}
					before := s.snapshot(Options{}, "parent", "", "", 0, "", false)
					count := len(sink.streams)
					// Scope is valid; deep future variants belong to the other thread and
					// must neither become parent semantics nor require a parent decoder.
					raw, _ := json.Marshal(map[string]any{"threadId": id, "turnId": "other-turn", "turn": map[string]any{"id": "other-turn", "status": "future"}, "item": map[string]any{"id": "x", "type": "future"}, "tokenUsage": nil, "error": "opaque"})
					s.onNotification(method, raw)
					after := s.snapshot(Options{}, "parent", "", "", 0, "", false)
					if s.protocolError() != nil || !reflect.DeepEqual(before, after) || len(sink.streams) != count || len(s.observation.children) != 0 {
						t.Fatalf("foreign frame changed parent: error=%v before=%+v after=%+v", s.protocolError(), before, after)
					}
					if when == "before" {
						s.onNotification(NotifyTurnCompleted, terminal)
					}
					if s.protocolError() != nil || !s.snapshot(Options{}, "parent", string(raw), "", 0, "", false).Checkpoint.Valid {
						t.Fatal("foreign frame prevented healthy parent")
					}
				})
			}
		}
	}
}

func TestAlignmentChildMetadataConflictImmediate(t *testing.T) {
	for _, kind := range []string{"role", "parent"} {
		t.Run(kind, func(t *testing.T) {
			s := newRunState("run", &recordingSink{})
			s.setThread("parent")
			s.setTurn("turn")
			s.onNotification(NotifyThreadStarted, alignmentChildMetadata("child", "parent", "reviewer"))
			parent, role := "parent", "writer"
			if kind == "parent" {
				parent, role = "other", "reviewer"
			}
			s.onNotification(NotifyThreadStarted, alignmentChildMetadata("child", parent, role))
			if s.protocolError() == nil {
				t.Fatal("contradictory established identity did not fail at metadata merge")
			}
			s.onNotification(NotifyThreadStarted, alignmentChildMetadata("child", "parent", "reviewer"))
			if !s.observation.children["child"].conflict {
				t.Fatal("contradiction healed")
			}
		})
	}
}

func alignmentMetadataState(t *testing.T, ctx context.Context, client *Client) (*runState, *recordingSink) {
	t.Helper()
	sink := &recordingSink{}
	s := newRunState("run", sink, Options{ResolvedAgents: []driver.AgentSpec{{Key: "canonical", RuntimeName: "reviewer"}}})
	s.setThread("parent")
	s.setTurn("turn")
	s.metadataReads = newChildMetadataReads(ctx, client)
	t.Cleanup(func() {
		s.metadataReads.cancel()
		_ = client.Close()
		if err := s.metadataReads.join(ctx); err != nil {
			t.Error(err)
		}
	})
	return s, sink
}
func alignmentSpawn(s *runState, method, status, receiver string) {
	raw, _ := json.Marshal(map[string]any{"threadId": "parent", "turnId": "turn", "item": map[string]any{"id": "spawn", "type": "collabAgentToolCall", "tool": "spawnAgent", "status": status, "senderThreadId": "parent", "receiverThreadIds": []string{receiver}}})
	s.onNotification(method, raw)
}
func alignmentReadRequest(t *testing.T, ctx context.Context, stream *alignmentRPCStream) json.RawMessage {
	t.Helper()
	var req struct {
		ID     json.RawMessage
		Method string
		Params struct {
			ThreadID     string
			IncludeTurns *bool
		}
	}
	raw := alignmentRPCWait(t, ctx, stream.writes)
	if err := json.Unmarshal(raw, &req); err != nil || req.Method != "thread/read" || req.Params.ThreadID != "child" || req.Params.IncludeTurns == nil || *req.Params.IncludeTurns {
		t.Fatalf("not metadata-only exact receiver: %s %v", raw, err)
	}
	return req.ID
}
func TestAlignmentChildMetadataRPC(t *testing.T) {
	for _, kind := range []string{"valid", "unavailable", "wrong-id", "wrong-parent", "conflicting-role", "unknown-role", "missing-role", "unsupported", "established-conflict", "known-missing-parent"} {
		t.Run(kind, func(t *testing.T) {
			ctx, client, wire := alignmentRPCSetup(t, nil)
			s, sink := alignmentMetadataState(t, ctx, client)
			alignmentSpawn(s, NotifyItemStarted, "inProgress", "child")
			alignmentSpawn(s, NotifyItemStarted, "inProgress", "child")
			id := alignmentReadRequest(t, ctx, wire)
			if kind == "known-missing-parent" {
				s.onNotification(NotifyThreadStarted, alignmentChildMetadata("child", "parent", ""))
			}
			if kind == "established-conflict" {
				s.onNotification(NotifyThreadStarted, alignmentChildMetadata("child", "parent", "reviewer"))
			}
			alignmentSpawn(s, NotifyItemCompleted, "completed", "child")
			raw := alignmentChildMetadata("child", "parent", "reviewer")
			switch kind {
			case "unavailable":
				raw = json.RawMessage(`null`)
			case "wrong-id":
				raw = alignmentChildMetadata("other", "parent", "reviewer")
			case "known-missing-parent":
				raw = alignmentChildMetadata("child", "", "reviewer")
			case "wrong-parent":
				raw = alignmentChildMetadata("child", "other", "reviewer")
			case "unknown-role":
				raw = alignmentChildMetadata("child", "parent", "unknown")
			case "missing-role":
				raw = alignmentChildMetadata("child", "parent", "")
			case "conflicting-role":
				raw = json.RawMessage(`{"thread":{"id":"child","agentRole":"reviewer","source":{"subAgent":{"thread_spawn":{"parent_thread_id":"parent","agent_role":"writer"}}}}}`)
			case "established-conflict":
				raw = alignmentChildMetadata("child", "parent", "writer")
			}
			frame := fmt.Sprintf(`{"id":%s,"result":%s}`, id, raw)
			if kind == "unsupported" {
				frame = fmt.Sprintf(`{"id":%s,"error":{"code":-32601,"message":"private detail must not become a notice"}}`, id)
			}
			wire.frames <- alignmentRPCFrame{raw: frame}
			s.onNotification(NotifyTurnCompleted, json.RawMessage(`{"threadId":"parent","turn":{"id":"turn","status":"completed"}}`))
			if !s.metadataReads.settle(true, nil) {
				t.Fatal("reader/worker deadlock")
			}
			if err := s.applyChildMetadataResults(); err != nil {
				t.Fatal(err)
			}
			if (s.protocolError() != nil) != (kind == "established-conflict") {
				t.Fatalf("failure classification %v", s.protocolError())
			}
			facts, _ := alignmentFacts(sink)
			if kind == "valid" && (len(facts) != 2 || facts[1].Phase != capability.Completed || facts[1].Ref.Key != "canonical") {
				t.Fatalf("missing formal proof: %+v", facts)
			}
			if kind != "valid" && len(facts) != 0 {
				t.Fatalf("invalid fact: %+v", facts)
			}
			if kind == "known-missing-parent" {
				child := s.observation.children["child"]
				if child.role != "" || child.rejected || child.conflict || !s.observation.notices["capability_unresolved"] {
					t.Fatal("incomplete metadata became contradiction or combined role proof")
				}
			}
			if kind == "wrong-id" || kind == "wrong-parent" || kind == "conflicting-role" {
				// Direct merge also proves a second successful lookup cannot heal the
				// rejected receiver; an unrelated returned ID must never be registered.
				s.mergeChildMetadata(alignmentChildMetadata("child", "parent", "reviewer"), "child")
				if !s.observation.children["child"].rejected || len(s.observation.children) != 1 {
					t.Fatal("invalid lookup healed or registered other identity")
				}
			}
			select {
			case extra := <-wire.writes:
				t.Fatalf("duplicate read: %s", extra)
			default:
			}
		})
	}
}

func TestAlignmentChildMetadataLimitsAndCancel(t *testing.T) {
	for _, reason := range []string{"caller", "total-finalization"} {
		t.Run(reason, func(t *testing.T) {
			ctx, client, wire := alignmentRPCSetup(t, nil)
			own, cancel := context.WithCancel(ctx)
			defer cancel()
			r := newChildMetadataReads(own, client)
			for i := 0; i < 128; i++ {
				if !r.admit(fmt.Sprint(i)) {
					t.Fatal("early capacity rejection")
				}
			}
			if !r.admit("0") || r.admit("overflow") {
				t.Fatal("capacity/replay")
			}
			alignmentRPCWait(t, ctx, wire.writes)
			if reason == "caller" {
				cancel()
			} else {
				r.cancel()
			}
			if !r.settle(false, nil) || !r.abandoned {
				t.Fatal("cancelled dispatched call not joined/suspended")
			}
			select {
			case <-wire.writes:
				t.Fatal("query queue continued after abandoned response")
			default:
			}
			if r.admit("late") {
				t.Fatal("admitted after seal")
			}
		})
	}
}
func TestAlignmentChildMetadataBlockedSend(t *testing.T) {
	ctx, client, wire := alignmentRPCSetup(t, func(s *alignmentRPCStream) { s.blockWrite = true; s.writeFailure = io.ErrClosedPipe })
	r := newChildMetadataReads(ctx, client)
	r.admit("child")
	alignmentRPCWait(t, ctx, wire.writeEntered)
	start := time.Now()
	if r.settle(false, nil) {
		t.Fatal("context cancellation pretended to unblock synchronous write")
	}
	if time.Since(start) > time.Second {
		t.Fatal("settlement was not bounded")
	}
	// The real pinned connection must remain reader-owned. Closing its owned
	// stream, not Conn.Close or a recovered panic, releases the send before join.
	_ = client.Close()
	if err := r.join(ctx); err != nil {
		t.Fatal(err)
	}
	outcome := <-r.results
	if !errors.Is(outcome.err, io.ErrClosedPipe) || !r.abandoned || wire.closeCount.Load() != 1 {
		t.Fatalf("write cause/ownership lost: %v", outcome.err)
	}
}
func TestAlignmentChildMetadataLateAnnouncements(t *testing.T) {
	for _, queued := range []bool{false, true} {
		t.Run(fmt.Sprint(queued), func(t *testing.T) {
			sink := &recordingSink{}
			s := newRunState("run", sink, Options{ResolvedAgents: []driver.AgentSpec{{Key: "canonical", RuntimeName: "reviewer"}}})
			s.setThread("parent")
			if !queued {
				s.setTurn("turn")
			}
			alignmentSpawn(s, NotifyItemStarted, "inProgress", "child")
			alignmentSpawn(s, NotifyItemCompleted, "completed", "child")
			s.onNotification(NotifyTurnCompleted, json.RawMessage(`{"threadId":"parent","turn":{"id":"turn","status":"completed"}}`))
			s.onNotification(NotifyThreadStarted, alignmentChildMetadata("child", "parent", "reviewer"))
			if queued {
				s.setTurn("turn")
			}
			facts, _ := alignmentFacts(sink)
			if len(facts) != 0 || len(s.observation.children) != 0 || s.protocolError() != nil {
				t.Fatal("late announcement crossed terminal/FIFO boundary")
			}
			s.freezeNotifications()
			s.onNotification(NotifyThreadStarted, alignmentChildMetadata("child", "parent", "writer"))
			if len(s.observation.children) != 0 || s.protocolError() != nil {
				t.Fatal("frozen result changed")
			}
		})
	}
}

func TestAlignmentChildMetadataWire(t *testing.T) {
	command := alignmentFixture(t)
	for _, resident := range []bool{false, true} {
		for _, scenario := range []string{"metadata", "metadata-unavailable", "metadata-provider-failure"} {
			t.Run(fmt.Sprintf("resident-%v/%s", resident, scenario), func(t *testing.T) {
				opts := alignmentOptions(command, filepath.Join(t.TempDir(), "capture.jsonl"))
				opts.CWD = t.TempDir()
				opts.ResolvedAgents = []driver.AgentSpec{{Key: "canonical", RuntimeName: "reviewer"}}
				opts.Env = append(opts.Env, driver.EnvBinding{Name: "ALIGNMENT_SCENARIO", Value: scenario}, driver.EnvBinding{Name: "CODEX_HOME", Value: t.TempDir()})
				ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
				defer cancel()
				sink := &recordingSink{}
				var result driver.Response
				var err error
				if resident {
					p, e := Open(ctx, opts, sink)
					if e != nil {
						t.Fatal(e)
					}
					defer p.TerminateAndWait(ctx)
					result, _, err = p.RunTurn(ctx, opts, sink)
				} else {
					result, err = Run(ctx, opts, sink)
				}
				if err != nil {
					t.Fatalf("unexpected transport error: %v", err)
				}
				failed := scenario == "metadata-provider-failure"
				if (result.Failure != nil) != failed || (result.Checkpoint == nil) != failed || result.Output != "answer" || result.Usage == nil || result.Usage.OutputTokens != 2 || result.RawStreams == nil || result.RawStreams.Terminal == nil {
					t.Fatal("parent outcome changed")
				}
				if !strings.Contains(result.RawStreams.Stdout, "must-not-publish") || !strings.Contains(result.RawStreams.Stdout, "late-announcement") {
					t.Fatal("foreign raw lost")
				}
				for _, item := range result.Transcript {
					if item.Text == "must-not-publish" || item.SessionID == "grandchild" {
						t.Fatal("foreign semantics published")
					}
				}
				facts, _ := alignmentFacts(sink)
				if scenario == "metadata-unavailable" {
					if len(facts) != 0 {
						t.Fatal("unavailable lookup invented facts")
					}
				} else if len(facts) != 2 || facts[1].Phase != capability.Completed || facts[1].Ref.Key != "canonical" {
					t.Fatalf("official read/receiver proof missing: %+v", facts)
				}
				assertTerminalLifecycle(t, sink.streams, btoi(!failed), btoi(failed))
			})
		}
	}
}
func btoi(v bool) int {
	if v {
		return 1
	}
	return 0
}

func TestAlignmentChildMetadataLateResponseNextTurn(t *testing.T) {
	command := alignmentFixture(t)
	audit := filepath.Join(t.TempDir(), "capture.jsonl")
	opts := alignmentOptions(command, audit)
	opts.CWD = t.TempDir()
	opts.ResolvedAgents = []driver.AgentSpec{{Key: "canonical", RuntimeName: "reviewer"}}
	opts.Env = append(opts.Env, driver.EnvBinding{Name: "ALIGNMENT_SCENARIO", Value: "metadata-late"}, driver.EnvBinding{Name: "CODEX_HOME", Value: t.TempDir()})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p, err := Open(ctx, opts, &recordingSink{})
	if err != nil {
		t.Fatal(err)
	}
	defer p.TerminateAndWait(ctx)
	sinkA := &recordingSink{}
	a, _, err := p.RunTurn(ctx, opts, sinkA)
	if err != nil || a.Checkpoint == nil || !p.metadataPaused || p.IsClosed() {
		t.Fatalf("optional response timeout poisoned writer: %v paused=%v closed=%v", err, p.metadataPaused, p.IsClosed())
	}
	frozen, _ := json.Marshal(a)
	sinkB := &recordingSink{}
	b, _, err := p.RunTurn(ctx, opts, sinkB)
	if err != nil || b.Checkpoint == nil || b.Output != "answer" || b.Usage == nil || b.Usage.OutputTokens != 2 {
		t.Fatalf("late reply polluted next turn: %v", err)
	}
	after, _ := json.Marshal(a)
	if !bytes.Equal(frozen, after) || strings.Contains(a.RawStreams.Stdout, "late-wrong-role") || !strings.Contains(b.RawStreams.Stdout, "late-wrong-role") {
		t.Fatal("Raw/result capture boundary changed")
	}
	for _, sink := range []*recordingSink{sinkA, sinkB} {
		facts, _ := alignmentFacts(sink)
		if len(facts) != 0 {
			t.Fatal("late response became capability")
		}
		assertTerminalLifecycle(t, sink.streams, 1, 0)
	}
	data, err := os.ReadFile(audit)
	if err != nil {
		t.Fatal(err)
	}
	reads := 0
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		var req struct{ Method string }
		_ = json.Unmarshal(line, &req)
		if req.Method == "thread/read" {
			reads++
		}
	}
	if reads != 1 {
		t.Fatalf("unanswered calls accumulated across turns: %d", reads)
	}
	p.client.callsMu.Lock()
	active := len(p.client.calls)
	p.client.callsMu.Unlock()
	if active != 0 {
		t.Fatalf("unjoined RPC workers: %d", active)
	}
}

type alignmentMetadataWriteProbe struct {
	io.Writer
	entered  chan struct{}
	finished chan error
	once     sync.Once
}

func (p *alignmentMetadataWriteProbe) Write(b []byte) (int, error) {
	lookup := bytes.Contains(b, []byte(`"method":"thread/read"`))
	if lookup {
		p.once.Do(func() { close(p.entered) })
	}
	n, err := p.Writer.Write(b)
	if lookup {
		p.finished <- err
	}
	return n, err
}
func TestAlignmentChildMetadataPipeBlockedSend(t *testing.T) {
	command := alignmentFixture(t)
	opts := alignmentOptions(command, filepath.Join(t.TempDir(), "capture.jsonl"))
	opts.CWD = t.TempDir()
	opts.ResolvedAgents = []driver.AgentSpec{{Key: "canonical", RuntimeName: "reviewer"}}
	opts.Env = append(opts.Env, driver.EnvBinding{Name: "ALIGNMENT_SCENARIO", Value: "metadata-block-write"}, driver.EnvBinding{Name: "CODEX_HOME", Value: t.TempDir()})
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	p, err := Open(ctx, opts, &recordingSink{})
	if err != nil {
		t.Fatal(err)
	}
	defer p.TerminateAndWait(ctx)
	probe := &alignmentMetadataWriteProbe{Writer: p.stream.stdin, entered: make(chan struct{}), finished: make(chan error, 1)}
	// No call is active after Open. Wrap only the test's actual pipe encoder;
	// production Close retains its original owned pipe handle.
	p.stream.enc = json.NewEncoder(probe)
	start := time.Now()
	result, _, err := p.RunTurn(ctx, opts, &recordingSink{})
	alignmentRPCWait(t, ctx, probe.entered)
	writeErr := alignmentRPCWait(t, ctx, probe.finished)
	if err == nil || writeErr == nil || !errors.Is(err, writeErr) || !strings.Contains(err.Error(), "metadata worker did not settle") || !p.IsClosed() {
		t.Fatalf("blocked native pipe cause/retirement lost: run=%v write=%v", err, writeErr)
	}
	if time.Since(start) > 8*time.Second || result.Checkpoint != nil || result.RawStreams == nil || result.RawStreams.Terminal == nil || result.Output != "answer" || result.Usage == nil || result.Usage.OutputTokens != 2 {
		t.Fatal("blocked send lost bounded partial result")
	}
	select {
	case <-p.Done():
	default:
		t.Fatal("process was not reaped before return")
	}
	p.client.callsMu.Lock()
	active := len(p.client.calls)
	p.client.callsMu.Unlock()
	if active != 0 {
		t.Fatal("blocked sender was not joined")
	}
}

func TestAlignmentChildMetadataErrorLeaves(t *testing.T) {
	caller := errors.New("caller budget")
	for _, tc := range []struct {
		name     string
		err      error
		optional bool
	}{
		{"cancel", context.Canceled, true},
		{"deadline", fmt.Errorf("wait: %w", context.DeadlineExceeded), true},
		{"both-context", errors.Join(context.Canceled, context.DeadlineExceeded), true},
		{"write", errors.Join(context.Canceled, io.ErrClosedPipe), false},
		{"EOF", errors.Join(context.DeadlineExceeded, io.EOF), false},
		{"closed", errors.Join(context.Canceled, os.ErrClosed), false},
		{"custom-caller", errors.Join(context.Canceled, caller), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newRunState("run", &recordingSink{})
			s.metadataReads = &childMetadataReads{results: make(chan childMetadataResult, 1)}
			s.metadataReads.results <- childMetadataResult{id: "child", err: tc.err}
			err := s.applyChildMetadataResults()
			if (err == nil) != tc.optional || (!tc.optional && !errors.Is(err, tc.err)) {
				t.Fatalf("lost error classification/cause: %v", err)
			}
			if err != nil && strings.Contains(err.Error(), "transport") {
				t.Fatal("unproven transport classification")
			}
		})
	}
}

func TestAlignmentChildMetadataIdentityCapacity(t *testing.T) {
	for _, fill := range []string{"accepted", "rejected"} {
		t.Run(fill, func(t *testing.T) {
			s := newRunState("run", &recordingSink{})
			s.setThread("parent")
			s.setTurn("turn")
			bad := func(id string) json.RawMessage {
				return json.RawMessage(fmt.Sprintf(`{"thread":{"id":%q,"agentRole":"reviewer","source":{"subAgent":{"thread_spawn":{"parent_thread_id":"parent","agent_role":"writer"}}}}}`, id))
			}
			for i := 0; i < childMetadataLimit; i++ {
				id := fmt.Sprint(i)
				if fill == "accepted" {
					s.observeChildThread(alignmentChildMetadata(id, "parent", "reviewer"))
				} else {
					s.observeChildThread(bad(id))
				}
			}
			s.observeChildThread(bad("overflow"))
			s.observeChildThread(alignmentChildMetadata("overflow", "parent", "reviewer"))
			if len(s.observation.children) != 128 || s.protocolError() != nil {
				t.Fatalf("first-invalid exceeded bound/became accepted: %v", s.protocolError())
			}
			if _, ok := s.observation.children["overflow"]; ok {
				t.Fatal("saturated invalid identity healed")
			}
			s.observeChildThread(alignmentChildMetadata("0", "parent", "reviewer"))
			if fill == "rejected" && !s.observation.children["0"].rejected {
				t.Fatal("retained tombstone healed")
			}
			s.observeChildThread(alignmentChildMetadata("0", "parent", "writer"))
			if (s.protocolError() != nil) != (fill == "accepted") {
				t.Fatal("full table lost known identity classification")
			}
		})
	}
}

func TestAlignmentChildMetadataTotalSettlement(t *testing.T) {
	ctx, client, wire := alignmentRPCSetup(t, nil)
	r := newChildMetadataReads(ctx, client)
	defer r.cancel()
	for i := 0; i < 4; i++ {
		r.admit(fmt.Sprint(i))
	}
	// Each of the first two requests returns within its own 1s deadline. The
	// third is cancelled by the single 2s finalization budget, not a 4s sum.
	peerDone := make(chan struct{})
	go func() {
		defer close(peerDone)
		for i := 0; i < 2; i++ {
			var req struct{ ID json.RawMessage }
			select {
			case raw := <-wire.writes:
				_ = json.Unmarshal(raw, &req)
			case <-ctx.Done():
				return
			}
			timer := time.NewTimer(750 * time.Millisecond)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return
			}
			wire.frames <- alignmentRPCFrame{raw: fmt.Sprintf(`{"id":%s,"result":null}`, req.ID)}
		}
	}()
	start := time.Now()
	if !r.settle(true, nil) {
		t.Fatal("response waiter failed to join")
	}
	<-peerDone
	if elapsed := time.Since(start); elapsed > childMetadataSettlement+500*time.Millisecond || elapsed < 1500*time.Millisecond {
		t.Fatalf("total settlement budget: %s", elapsed)
	}
	if !r.abandoned {
		t.Fatal("total cancellation did not pause future optional reads")
	}
	for i := 0; i < 3; i++ {
		result := <-r.results
		if i == 2 && !errors.Is(result.err, context.Canceled) {
			t.Fatalf("last call did not observe total cancellation: %v", result.err)
		}
	}
	select {
	case <-r.results:
		t.Fatal("queue continued after abandoned response")
	default:
	}
}

func TestAlignmentChildMetadataKnownIdentityNeedsNoRead(t *testing.T) {
	ctx, client, wire := alignmentRPCSetup(t, nil)
	s, sink := alignmentMetadataState(t, ctx, client)
	s.onNotification(NotifyThreadStarted, alignmentChildMetadata("child", "parent", "reviewer"))
	alignmentSpawn(s, NotifyItemStarted, "inProgress", "child")
	alignmentSpawn(s, NotifyItemCompleted, "completed", "child")
	if !s.metadataReads.settle(true, nil) {
		t.Fatal("idle worker not joined")
	}
	select {
	case <-wire.writes:
		t.Fatal("known complete metadata re-queried")
	default:
	}
	facts, _ := alignmentFacts(sink)
	if len(facts) != 2 || facts[1].Phase != capability.Completed {
		t.Fatal("existing double-proof path changed")
	}
}

func TestAlignmentChildMetadataFreezeWaitsForHandler(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	entered, release, handled, frozen := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	sink := &alignmentMetadataFreezeSink{recordingSink: &recordingSink{}, ctx: ctx, entered: entered, release: release}
	s := newRunState("run", sink)
	s.setThread("parent")
	s.setTurn("turn")
	go func() {
		s.onNotification(NotifyTurnCompleted, json.RawMessage(`{"threadId":"parent","turn":{"id":"turn","status":"completed"}}`))
		close(handled)
	}()
	alignmentRPCWait(t, ctx, entered)
	go func() { s.freezeNotifications(); close(frozen) }()
	select {
	case <-frozen:
		t.Fatal("freeze bypassed in-flight handler")
	default:
	}
	close(release)
	alignmentRPCWait(t, ctx, handled)
	alignmentRPCWait(t, ctx, frozen)
	before := s.snapshot(Options{}, "parent", "raw", "", 0, "", false)
	s.onNotification(NotifyError, json.RawMessage(`{"willRetry":false}`))
	s.onNotification(NotifyTurnCompleted, json.RawMessage(`{"threadId":"parent","turn":{"id":"turn","status":"completed"}}`))
	s.onNotification(NotifyThreadStarted, alignmentChildMetadata("child", "parent", "reviewer"))
	after := s.snapshot(Options{}, "parent", "raw", "", 0, "", false)
	if !reflect.DeepEqual(before, after) || s.protocolError() != nil || len(s.observation.children) != 0 {
		t.Fatal("notification crossed frozen snapshot boundary")
	}
	s.finishPublicResult(after, nil)
	assertTerminalLifecycle(t, sink.streams, 1, 0)
}

type alignmentMetadataFreezeSink struct {
	*recordingSink
	ctx     context.Context
	entered chan struct{}
	release chan struct{}
}

func (s *alignmentMetadataFreezeSink) Emit(e driver.RunEvent) error {
	if err := s.recordingSink.Emit(e); err != nil {
		return err
	}
	if e.Item != nil && e.Item.Kind == driver.TranscriptResult {
		close(s.entered)
		select {
		case <-s.release:
		case <-s.ctx.Done():
		}
	}
	return nil
}

func TestAlignmentChildMetadataWaitCauses(t *testing.T) {
	command := alignmentFixture(t)
	for _, scenario := range []string{"metadata-wait-exit", "metadata-wait-protocol-exit", "metadata-wait-decode-exit"} {
		t.Run(scenario, func(t *testing.T) {
			opts := alignmentOptions(command, filepath.Join(t.TempDir(), "capture.jsonl"))
			opts.CWD = t.TempDir()
			opts.ResolvedAgents = []driver.AgentSpec{{Key: "canonical", RuntimeName: "reviewer"}}
			opts.Env = append(opts.Env, driver.EnvBinding{Name: "ALIGNMENT_SCENARIO", Value: scenario}, driver.EnvBinding{Name: "CODEX_HOME", Value: t.TempDir()})
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			sink := &recordingSink{}
			result, err := Run(ctx, opts, sink)
			var exitErr *exec.ExitError
			if err == nil || !errors.As(err, &exitErr) || exitErr.ExitCode() != 17 || result.ExitCode != 17 || result.Checkpoint != nil {
				t.Errorf("actual Wait cause lost: %v; exit=%d", err, result.ExitCode)
			}
			if scenario == "metadata-wait-protocol-exit" && !strings.Contains(err.Error(), "belongs to turn") {
				t.Errorf("parent protocol cause lost: %v", err)
			}
			if scenario == "metadata-wait-decode-exit" && !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Errorf("decoder cause lost: %v", err)
			}
			if result.Output != "answer" || result.Usage == nil || result.Usage.OutputTokens != 2 || result.RawStreams == nil || result.RawStreams.Stderr != "fixture-stderr" || !strings.Contains(result.RawStreams.Stdout, "turn/completed") {
				t.Fatal("partial output/usage/Raw lost")
			}
			if scenario != "metadata-wait-protocol-exit" && result.RawStreams.Terminal == nil {
				t.Fatal("formal terminal lost")
			}
			assertTerminalLifecycle(t, sink.streams, 0, 1)
		})
	}
}
