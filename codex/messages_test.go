package codex

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/v2/driver"
	"github.com/agent-dance/agent-adaptor/v2/internal/testutil"
)

type codexMessageTestPort struct {
	mu        sync.Mutex
	signal    chan struct{}
	immediate []driver.MessageItem
	ends      [][]driver.MessageItem
	acks      map[string][]driver.MessageAck
	windows   []bool
}

func newCodexMessageTestPort() *codexMessageTestPort {
	return &codexMessageTestPort{signal: make(chan struct{}, 1), acks: map[string][]driver.MessageAck{}}
}
func (p *codexMessageTestPort) Window(b bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.windows = append(p.windows, b)
}
func (p *codexMessageTestPort) Signal() <-chan struct{} { return p.signal }
func (p *codexMessageTestPort) Claim(b driver.Boundary) []driver.MessageItem {
	p.mu.Lock()
	defer p.mu.Unlock()
	if b == driver.BoundaryImmediate {
		items := p.immediate
		p.immediate = nil
		return items
	}
	if len(p.ends) == 0 {
		return nil
	}
	items := p.ends[0]
	p.ends = p.ends[1:]
	return items
}
func (p *codexMessageTestPort) Ack(id string, a driver.MessageAck) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.acks[id] = append(p.acks[id], a)
}

func TestMessageCapabilityTransportResolver(t *testing.T) {
	d := Driver(Config{})
	resolver, ok := d.(driver.MessageCapabilityResolver)
	if !ok {
		t.Fatal("resolver missing")
	}
	want := driver.MessageCapability{Boundaries: []driver.Boundary{driver.BoundaryImmediate}, Interrupt: true, Continue: driver.ContinueInRun, PersistsDelivered: true, MinVersion: "0.100.0"}
	if !reflect.DeepEqual(d.Descriptor().Messages, want) {
		t.Fatalf("descriptor=%+v", d.Descriptor().Messages)
	}
	for _, req := range []driver.Request{{Streaming: true}, {Session: &driver.SessionContext{Mode: driver.SessionFork}}} {
		if got := resolver.ResolveMessageCapability(req); !reflect.DeepEqual(got, want) {
			t.Fatalf("app-server capability=%+v", got)
		}
	}
	for _, req := range []driver.Request{{}, {Session: &driver.SessionContext{EngineSessionID: "resident-but-batch"}}} {
		got := resolver.ResolveMessageCapability(req)
		if len(got.Boundaries) != 0 || got.Interrupt || got.Continue != driver.ContinueResume {
			t.Fatalf("exec capability=%+v", got)
		}
	}
}

func TestMessagesOneShotAndResident(t *testing.T) {
	for _, resident := range []bool{false, true} {
		name := "one-shot"
		if resident {
			name = "resident"
		}
		t.Run(name, func(t *testing.T) {
			for _, mode := range []string{"continuation", "steer", "interrupt"} {
				t.Run(mode, func(t *testing.T) {
					helperMode := ""
					if mode != "continuation" {
						helperMode = "messages_" + mode
					}
					a, req, spawns, _ := newPersistentCodexTest(t, helperMode)
					defer closeCodexTestAdapter(t, a)
					req.Spawn = !resident
					port := newCodexMessageTestPort()
					port.ends = [][]driver.MessageItem{{{ID: "end-1", Text: "first continuation", Timing: driver.TimingAfterTurn}}, {{ID: "end-2", Text: "second continuation", Timing: driver.TimingAfterTurn}}}
					if mode != "continuation" {
						timing := driver.TimingSoon
						if mode == "interrupt" {
							timing = driver.TimingInterrupt
						}
						port.immediate = []driver.MessageItem{{ID: "immediate", Text: "steered result", Timing: timing}}
						port.signal <- struct{}{}
					}
					req.Messages = port
					sink := &testutil.EventRecorder{}
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					result, err := a.Run(ctx, req, sink)
					if err != nil || result.Failure != nil {
						t.Fatalf("Run err=%v failure=%+v", err, result.Failure)
					}
					turnCount := 3
					if mode == "interrupt" {
						turnCount = 4
					}
					var turns []*driver.TurnCompleted
					for _, event := range sink.Snapshot() {
						if event.Type == driver.RunEventTurnCompleted {
							turns = append(turns, event.Turn)
						}
					}
					if len(turns) != turnCount {
						t.Fatalf("TurnCompleted count=%d, want %d", len(turns), turnCount)
					}
					for index, turn := range turns {
						if turn.Index != index || turn.CompletedAt.IsZero() || turn.Interrupted != (mode == "interrupt" && index == 0) {
							t.Fatalf("turn[%d]=%+v", index, turn)
						}
					}
					if result.Usage == nil || result.Usage.InputTokens != 2*turnCount || result.Usage.OutputTokens != turnCount {
						t.Fatalf("usage=%+v", result.Usage)
					}
					if result.Checkpoint == nil || !result.Checkpoint.Valid || result.Checkpoint.State.ResumeID != "thread-1" {
						t.Fatalf("checkpoint=%+v", result.Checkpoint)
					}
					if result.RawStreams == nil || strings.Count(result.RawStreams.Stdout, `"method":"turn/completed"`) != turnCount {
						t.Fatalf("raw=%+v", result.RawStreams)
					}
					if got := helperSpawnCount(t, spawns); got != 1 {
						t.Fatalf("spawns=%d", got)
					}
					if len(port.windows) != 1+2*turnCount || port.windows[0] {
						t.Fatalf("window sequence=%v", port.windows)
					}
					for i, b := range port.windows[1:] {
						if b != (i%2 == 0) {
							t.Fatalf("window sequence=%v", port.windows)
						}
					}
					for id, acks := range port.acks {
						if len(acks) != 1 || !acks[0].Delivered {
							t.Fatalf("Ack %s=%+v", id, acks)
						}
					}
					expectedAcks := 2
					if mode != "continuation" {
						expectedAcks++
					}
					if len(port.acks) != expectedAcks {
						t.Fatalf("acks=%+v", port.acks)
					}
					if mode == "steer" && !strings.Contains(result.RawStreams.Stdout, "steered result") {
						t.Fatal("steer text absent from parsed protocol")
					}
					if resident {
						nextReq := resumedCodexRequest(req, result)
						nextReq.Messages = nil
						next := runCodexTurn(t, a, nextReq)
						if next.Checkpoint == nil || helperSpawnCount(t, spawns) != 1 {
							t.Fatal("continuations invalidated resident process")
						}
					}
				})
			}
		})
	}
}

func TestMessageBatchTurnCompletion(t *testing.T) {
	for _, fixture := range []string{"happy-assistant.jsonl", "failure.jsonl"} {
		t.Run(fixture, func(t *testing.T) {
			sink := &testutil.EventRecorder{}
			parser := newCodexParser(sink)
			if err := parser.onChunk("stdout", loadFixture(t, fixture), time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			parser.finalize()
			var turns []*driver.TurnCompleted
			for _, e := range sink.Snapshot() {
				if e.Type == driver.RunEventTurnCompleted {
					turns = append(turns, e.Turn)
				}
			}
			if len(turns) != 1 || turns[0].Index != 0 || turns[0].Text != parser.buildOutput() || turns[0].CompletedAt.IsZero() || !reflect.DeepEqual(turns[0].Usage, parser.usage) {
				t.Fatalf("turns=%+v", turns)
			}
		})
	}
}
