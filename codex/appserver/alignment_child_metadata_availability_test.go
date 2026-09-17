package appserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/sourcegraph/jsonrpc2"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/capability"
	"github.com/agent-dance/agent-adaptor/driver"
)

// The real stdio peer only makes metadata available after sending the parent's
// terminal. The first formal error is not evidence against the child's identity.
func TestAlignmentChildMetadataAvailabilityWire(t *testing.T) {
	command := alignmentFixture(t)
	for _, resident := range []bool{false, true} {
		t.Run(fmt.Sprintf("resident-%v", resident), func(t *testing.T) {
			capture := filepath.Join(t.TempDir(), "capture.jsonl")
			opts := alignmentOptions(command, capture)
			opts.CWD = t.TempDir()
			opts.ResolvedAgents = []driver.AgentSpec{{Key: "canonical", RuntimeName: "reviewer"}}
			opts.Env = append(opts.Env, driver.EnvBinding{Name: "ALIGNMENT_SCENARIO", Value: "metadata-availability"}, driver.EnvBinding{Name: "CODEX_HOME", Value: t.TempDir()})
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			sink := &recordingSink{}
			var response driver.Response
			var err error
			if resident {
				p, e := Open(ctx, opts, sink)
				if e != nil {
					t.Fatal(e)
				}
				defer p.TerminateAndWait(ctx)
				response, _, err = p.RunTurn(ctx, opts, sink)
			} else {
				response, err = Run(ctx, opts, sink)
			}
			if err != nil || response.Checkpoint == nil || !response.Checkpoint.Valid || response.RawStreams == nil || response.RawStreams.Terminal == nil || response.Output != "answer" || response.Usage == nil || response.Usage.InputTokens != 0 || response.Usage.OutputTokens != 2 {
				t.Fatalf("healthy parent output changed: err=%v response=%+v", err, response)
			}
			if !strings.Contains(response.RawStreams.Stdout, "failed to read session metadata") {
				t.Fatal("first availability error Raw lost")
			}
			data, e := os.ReadFile(capture)
			if e != nil {
				t.Fatal(e)
			}
			reads := 0
			for _, line := range bytes.Split(data, []byte{'\n'}) {
				var row struct {
					Method string
					Params struct {
						ThreadID     string
						IncludeTurns *bool
					}
				}
				if json.Unmarshal(line, &row) == nil && row.Method == "thread/read" {
					reads++
					if row.Params.ThreadID != "agent-child" || row.Params.IncludeTurns == nil || *row.Params.IncludeTurns {
						t.Fatal("metadata request changed identity or included turns")
					}
				}
			}
			facts, _ := alignmentFacts(sink)
			if reads != 2 || len(facts) != 2 || facts[0].Phase != capability.Started || facts[1].Phase != capability.Completed || facts[1].Ref.Key != "canonical" {
				t.Fatalf("availability recovery missing: reads=%d facts=%+v", reads, facts)
			}
		})
	}
}

const alignmentMetadataUnavailableMessage = "failed to read thread: thread-store internal error: failed to read session metadata /private/fixture: rollout at /private/fixture is empty"

func TestAlignmentChildMetadataAvailabilityClass(t *testing.T) {
	for _, tc := range []struct {
		name, message string
		code          int64
		want          bool
	}{
		{"exact", alignmentMetadataUnavailableMessage, -32603, true},
		{"wrong-code", alignmentMetadataUnavailableMessage, -32601, false},
		{"unanchored", "prefix " + alignmentMetadataUnavailableMessage, -32603, false},
		{"other-store-error", "failed to read thread: thread-store internal error: permission denied is empty", -32603, false},
		{"non-session-start", strings.TrimSuffix(alignmentMetadataUnavailableMessage, " is empty") + " does not start with session metadata", -32603, false},
		{"unknown", "unavailable", -32603, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if childMetadataUnavailable(&jsonrpc2.Error{Code: tc.code, Message: tc.message}) != tc.want {
				t.Fatal("availability classification changed")
			}
		})
	}
	if childMetadataUnavailable(errors.New(alignmentMetadataUnavailableMessage)) {
		t.Fatal("non-RPC text admitted recovery")
	}
}

func TestAlignmentChildMetadataAvailabilityGates(t *testing.T) {
	for _, kind := range []string{"valid", "again", "wrong-id", "wrong-parent", "role-conflict", "known-parent-conflict", "known-role", "failed", "interrupted", "missing-terminal", "malformed-terminal", "protocol", "cancel", "closed", "eof", "owner-denied", "other-error", "first-invalid"} {
		t.Run(kind, func(t *testing.T) {
			parent, client, wire := alignmentRPCSetup(t, nil)
			ctx, cancel := context.WithCancel(parent)
			defer cancel()
			s, sink := alignmentMetadataState(t, parent, client)
			// Use the cancellable child for lookup work while test cleanup keeps its
			// independent bounded parent context.
			s.metadataReads.cancel()
			if err := s.metadataReads.join(parent); err != nil {
				t.Fatal(err)
			}
			s.metadataReads = newChildMetadataReads(ctx, client)
			alignmentSpawn(s, NotifyItemStarted, "inProgress", "child")
			alignmentSpawn(s, NotifyItemCompleted, "completed", "child")
			firstID := alignmentReadRequest(t, parent, wire)
			if kind == "known-role" {
				s.onNotification(NotifyThreadStarted, alignmentChildMetadata("child", "parent", "reviewer"))
			}
			if kind == "known-parent-conflict" {
				s.onNotification(NotifyThreadStarted, alignmentChildMetadata("child", "parent", ""))
			}
			message := alignmentMetadataUnavailableMessage
			if kind == "other-error" {
				message = "unclassified store error"
			}
			first := fmt.Sprintf(`{"id":%s,"error":{"code":-32603,"message":%q}}`, firstID, message)
			if kind == "first-invalid" {
				first = fmt.Sprintf(`{"id":%s,"result":%s}`, firstID, alignmentChildMetadata("wrong", "parent", "reviewer"))
			}
			wire.frames <- alignmentRPCFrame{raw: first}
			status := "completed"
			if kind == "failed" || kind == "interrupted" {
				status = kind
			}
			if kind == "missing-terminal" {
				s.metadataReads.seal()
			} else if kind == "malformed-terminal" {
				s.onNotification(NotifyTurnCompleted, json.RawMessage(`{"threadId":"parent","turn":{"id":"turn","status":"future"}}`))
			} else {
				s.onNotification(NotifyTurnCompleted, json.RawMessage(fmt.Sprintf(`{"threadId":"parent","turn":{"id":"turn","status":%q}}`, status)))
			}
			if kind == "protocol" {
				s.recordProtocolError(errors.New("prior protocol cause"))
			}
			if kind == "cancel" {
				cancel()
			}
			if kind == "closed" {
				_ = client.Close()
			}
			stream := &stdioStream{readDone: make(chan struct{})}
			if kind == "eof" {
				close(stream.readDone)
			}
			allowed := kind != "owner-denied" && ctx.Err() == nil && s.protocolError() == nil
			eligible := s.childMetadataRecovery(allowed, client, stream)
			stopPeer, peerDone := make(chan struct{}), make(chan struct{})
			reads := 1
			var peerErr error
			go func() {
				defer close(peerDone)
				for {
					select {
					case raw := <-wire.writes:
						reads++
						var req struct {
							ID     json.RawMessage
							Method string
							Params struct {
								ThreadID     string
								IncludeTurns *bool
							}
						}
						if json.Unmarshal(raw, &req) != nil || req.Method != "thread/read" || req.Params.ThreadID != "child" || req.Params.IncludeTurns == nil || *req.Params.IncludeTurns {
							peerErr = errors.New("invalid recovery request")
							return
						}
						result := alignmentChildMetadata("child", "parent", "reviewer")
						switch kind {
						case "wrong-id":
							result = alignmentChildMetadata("wrong", "parent", "reviewer")
						case "wrong-parent", "known-parent-conflict":
							result = alignmentChildMetadata("child", "other", "reviewer")
						case "role-conflict":
							result = json.RawMessage(`{"thread":{"id":"child","agentRole":"reviewer","source":{"subAgent":{"thread_spawn":{"parent_thread_id":"parent","agent_role":"writer"}}}}}`)
						}
						frame := fmt.Sprintf(`{"id":%s,"result":%s}`, req.ID, result)
						if kind == "again" {
							frame = fmt.Sprintf(`{"id":%s,"error":{"code":-32603,"message":%q}}`, req.ID, message)
						}
						select {
						case wire.frames <- alignmentRPCFrame{raw: frame}:
						case <-stopPeer:
							return
						}
					case <-stopPeer:
						return
					case <-parent.Done():
						return
					}
				}
			}()
			joined := s.metadataReads.settle(ctx.Err() == nil && s.protocolError() == nil, eligible)
			close(stopPeer)
			<-peerDone
			if !joined || peerErr != nil {
				t.Fatalf("settlement/peer failure: joined=%t err=%v", joined, peerErr)
			}
			applyErr := s.applyChildMetadataResults()
			if kind != "closed" && kind != "cancel" && applyErr != nil {
				t.Fatal(applyErr)
			}
			retry := kind == "valid" || kind == "again" || kind == "wrong-id" || kind == "wrong-parent" || kind == "role-conflict" || kind == "known-parent-conflict"
			wantReads := 1
			if retry {
				wantReads = 2
			}
			if reads != wantReads {
				t.Fatalf("recovery sends=%d want=%d", reads, wantReads)
			}
			facts, _ := alignmentFacts(sink)
			wantFacts := kind == "valid" || kind == "known-role"
			if wantFacts {
				if len(facts) != 2 || facts[1].Phase != capability.Completed {
					t.Fatalf("valid proof absent: %+v", facts)
				}
			} else if len(facts) != 0 {
				t.Fatalf("unproven capability: %+v", facts)
			}
			if kind == "wrong-id" || kind == "wrong-parent" || kind == "role-conflict" || kind == "first-invalid" {
				s.mergeChildMetadata(alignmentChildMetadata("child", "parent", "reviewer"), "child")
				if !s.observation.children["child"].rejected {
					t.Fatal("invalid recovery healed")
				}
			}
			if kind == "known-parent-conflict" && s.protocolError() == nil {
				t.Fatal("known identity conflict not fatal")
			}
		})
	}
}

func TestAlignmentChildMetadataAvailabilityCapacity(t *testing.T) {
	for _, budget := range []bool{false, true} {
		t.Run(fmt.Sprintf("budget-%v", budget), func(t *testing.T) {
			ctx, client, wire := alignmentRPCSetup(t, nil)
			r := newChildMetadataReads(ctx, client)
			defer r.cancel()
			count := 128
			if budget {
				count = 4
			}
			eligible := make(map[string]bool)
			for i := 0; i < count; i++ {
				id := fmt.Sprint(i)
				eligible[id] = true
				if !r.admit(id) {
					t.Fatal("receiver not admitted")
				}
			}
			if !budget && r.admit("overflow") {
				t.Fatal("129th identity admitted")
			}
			stop, done := make(chan struct{}), make(chan struct{})
			counts := map[string]int{}
			go func() {
				defer close(done)
				for {
					select {
					case raw := <-wire.writes:
						var req struct {
							ID     json.RawMessage
							Params struct{ ThreadID string }
						}
						_ = json.Unmarshal(raw, &req)
						counts[req.Params.ThreadID]++
						if budget && counts[req.Params.ThreadID] == 2 {
							timer := time.NewTimer(750 * time.Millisecond)
							select {
							case <-timer.C:
							case <-stop:
								timer.Stop()
								return
							case <-ctx.Done():
								timer.Stop()
								return
							}
						}
						// Even the second unavailable reply cannot trigger a third read.
						frame := fmt.Sprintf(`{"id":%s,"error":{"code":-32603,"message":%q}}`, req.ID, alignmentMetadataUnavailableMessage)
						select {
						case wire.frames <- alignmentRPCFrame{raw: frame}:
						case <-stop:
							return
						}
					case <-stop:
						return
					case <-ctx.Done():
						return
					}
				}
			}()
			started := time.Now()
			joined := r.settle(true, eligible)
			elapsed := time.Since(started)
			close(stop)
			<-done
			if !joined {
				t.Fatal("result channel or recovery worker deadlocked")
			}
			if elapsed > childMetadataSettlement+500*time.Millisecond {
				t.Fatalf("budget multiplied by queue: %s", elapsed)
			}
			if len(r.results) != count {
				t.Fatalf("final result count=%d want=%d", len(r.results), count)
			}
			for _, n := range counts {
				if n > 2 {
					t.Fatal("more than one recovery")
				}
			}
			if budget {
				if !r.abandoned || elapsed < 1500*time.Millisecond || counts["3"] != 1 {
					t.Fatalf("shared budget/stop changed: elapsed=%s abandoned=%t counts=%v", elapsed, r.abandoned, counts)
				}
			} else {
				for id := range eligible {
					if counts[id] != 2 {
						t.Fatal("bounded recovery not completed")
					}
				}
			}
		})
	}
}

type alignmentAvailabilityBlockedStream struct {
	*alignmentRPCStream
	calls   atomic.Int64
	entered chan struct{}
	failure error
}

func (s *alignmentAvailabilityBlockedStream) WriteObject(v any) error {
	if s.calls.Add(1) == 2 {
		close(s.entered)
		<-s.closed // Deliberately synchronous: only transport Close releases this write.
		return errors.Join(context.Canceled, s.failure)
	}
	return s.alignmentRPCStream.WriteObject(v)
}
func TestAlignmentChildMetadataAvailabilityBlockedWrite(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	base := &alignmentRPCStream{ctx: ctx, frames: make(chan alignmentRPCFrame, 16), writes: make(chan json.RawMessage, 16), closed: make(chan struct{}), readDone: make(chan struct{})}
	writeErr := errors.New("actual recovery pipe write failure")
	wire := &alignmentAvailabilityBlockedStream{alignmentRPCStream: base, entered: make(chan struct{}), failure: writeErr}
	client := NewClient(context.Background(), wire)
	defer func() { _ = client.Close(); cancel(); <-client.DisconnectNotify() }()
	r := newChildMetadataReads(ctx, client)
	r.admit("child")
	first := alignmentReadRequest(t, ctx, base)
	base.frames <- alignmentRPCFrame{raw: fmt.Sprintf(`{"id":%s,"error":{"code":-32603,"message":%q}}`, first, alignmentMetadataUnavailableMessage)}
	settled := make(chan bool, 1)
	go func() { settled <- r.settle(true, map[string]bool{"child": true}) }()
	alignmentRPCWait(t, ctx, wire.entered)
	if alignmentRPCWait(t, ctx, settled) {
		t.Fatal("blocked synchronous write was treated as joined")
	}
	_ = client.Close() // Same required owner order: close first, then join.
	if err := r.join(ctx); err != nil {
		t.Fatal(err)
	}
	result := <-r.results
	if !errors.Is(result.err, writeErr) || !r.abandoned || childMetadataContextOnly(result.err) {
		t.Fatalf("real write cause lost: %v", result.err)
	}
	s := newRunState("run", &recordingSink{})
	s.metadataReads = r
	r.results <- result
	if err := s.applyChildMetadataResults(); !errors.Is(err, writeErr) {
		t.Fatalf("application swallowed write cause: %v", err)
	}
}

func TestAlignmentChildMetadataAvailabilityLateResponse(t *testing.T) {
	command := alignmentFixture(t)
	capture := filepath.Join(t.TempDir(), "capture.jsonl")
	opts := alignmentOptions(command, capture)
	opts.CWD = t.TempDir()
	opts.ResolvedAgents = []driver.AgentSpec{{Key: "canonical", RuntimeName: "reviewer"}}
	opts.Env = append(opts.Env, driver.EnvBinding{Name: "ALIGNMENT_SCENARIO", Value: "metadata-availability-late"}, driver.EnvBinding{Name: "CODEX_HOME", Value: t.TempDir()})
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	p, err := Open(ctx, opts, &recordingSink{})
	if err != nil {
		t.Fatal(err)
	}
	defer p.TerminateAndWait(ctx)
	sinkA := &recordingSink{}
	a, _, err := p.RunTurn(ctx, opts, sinkA)
	if err != nil || a.Checkpoint == nil || !p.metadataPaused || p.IsClosed() {
		t.Fatalf("abandoned recovery did not pause healthy resident: %v", err)
	}
	frozen, _ := json.Marshal(a)
	sinkB := &recordingSink{}
	b, _, err := p.RunTurn(ctx, opts, sinkB)
	if err != nil || b.Checkpoint == nil || b.Output != "answer" || b.Usage == nil || b.Usage.OutputTokens != 2 {
		t.Fatalf("late recovery polluted next turn: %v", err)
	}
	after, _ := json.Marshal(a)
	if !bytes.Equal(frozen, after) || strings.Contains(a.RawStreams.Stdout, "late-wrong-role") || !strings.Contains(b.RawStreams.Stdout, "late-wrong-role") {
		t.Fatal("late response crossed frozen result boundary")
	}
	for _, sink := range []*recordingSink{sinkA, sinkB} {
		facts, _ := alignmentFacts(sink)
		if len(facts) != 0 {
			t.Fatal("late response invented capability")
		}
		assertTerminalLifecycle(t, sink.streams, 1, 0)
	}
	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(data, []byte(`"method":"thread/read"`)) != 2 {
		t.Fatal("paused resident issued more metadata reads")
	}
}
