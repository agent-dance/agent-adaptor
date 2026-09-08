package appserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/systemprompt"
)

type alignmentTurnInput struct {
	writes chan json.RawMessage
	closed chan struct{}
	once   sync.Once
}

func (w *alignmentTurnInput) Write(p []byte) (int, error) {
	w.writes <- append(json.RawMessage(nil), p...)
	return len(p), nil
}
func (w *alignmentTurnInput) Close() error {
	w.once.Do(func() { close(w.closed) })
	return nil
}

// Hold the actual stdio decoder's first response before the pinned reader can
// dispatch it. Raw bytes still pass through the same tee as production. Closing
// stdin cannot retract a response already decoded from independent stdout.
type alignmentTurnResponseGate struct {
	*stdioStream
	decoded chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *alignmentTurnResponseGate) ReadObject(v interface{}) error {
	err := s.stdioStream.ReadObject(v)
	s.once.Do(func() {
		close(s.decoded)
		<-s.release
	})
	return err
}

func TestAlignmentCancelledTurnStartDrainsConfirmedAudit(t *testing.T) {
	for _, scenario := range []string{"confirmed", "missing-turn", "rpc-error", "wrong-response-id", "wrong-thread", "wrong-turn"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, timeout := context.WithTimeout(context.Background(), 5*time.Second)
			defer timeout()
			stdin := &alignmentTurnInput{writes: make(chan json.RawMessage, 4), closed: make(chan struct{})}
			stdout, output := io.Pipe()
			raw := &syncBuffer{}
			stream := newStdioStream(stdin, io.TeeReader(stdout, raw))
			gate := &alignmentTurnResponseGate{stdioStream: stream, decoded: make(chan struct{}), release: make(chan struct{})}
			client := NewClient(context.Background(), gate)
			p := &Process{client: client, stream: stream, threadID: "current-thread", appendFingerprint: systemprompt.Fingerprint(""), stdout: raw, stderr: &syncBuffer{}, waitCh: make(chan struct{}), cancel: func() {}}
			go func() { <-client.DisconnectNotify(); close(p.waitCh) }()
			var release sync.Once
			unblock := func() { release.Do(func() { close(gate.release) }) }
			t.Cleanup(func() {
				unblock()
				_ = output.Close()
				_ = client.Close()
				_ = stdout.Close()
			})
			runCtx, cancel := context.WithCancelCause(ctx)
			defer cancel(nil)
			cause := errors.New("cancel after response decode before dispatch")
			type outcome struct {
				response driver.Response
				sent     bool
				err      error
			}
			done := make(chan outcome, 1)
			go func() {
				r, sent, err := p.RunTurn(runCtx, Options{Prompt: "one prompt", RunID: "cancelled-run"}, &recordingSink{})
				done <- outcome{r, sent, err}
			}()
			var request struct {
				ID     json.RawMessage
				Method string
			}
			if err := json.Unmarshal(alignmentRPCWait(t, ctx, stdin.writes), &request); err != nil || request.Method != MethodTurnStart {
				t.Fatalf("expected exactly one turn/start: %+v %v", request, err)
			}
			ack := map[string]any{"id": request.ID, "result": map[string]any{"turn": map[string]any{"id": "current-turn", "status": "inProgress"}}}
			switch scenario {
			case "missing-turn":
				ack["result"] = map[string]any{"turn": map[string]any{"id": ""}}
			case "rpc-error":
				delete(ack, "result")
				ack["error"] = map[string]any{"code": -32000, "message": "request rejected"}
			case "wrong-response-id":
				ack["id"] = "unrelated-request"
			}
			thread, turn := "current-thread", "current-turn"
			if scenario == "wrong-thread" {
				thread = "other-thread"
			}
			if scenario == "wrong-turn" {
				turn = "other-turn"
			}
			frames := []any{ack}
			for _, notification := range []struct {
				method string
				params map[string]any
			}{
				{NotifyTurnStarted, map[string]any{"threadId": thread, "turn": map[string]any{"id": turn, "status": "inProgress"}}},
				{NotifyItemStarted, map[string]any{"threadId": thread, "turnId": turn, "item": map[string]any{"id": "item", "type": "agentMessage", "text": "partial-text"}}},
				{NotifyItemAgentMessageDelta, map[string]any{"threadId": thread, "turnId": turn, "itemId": "item", "delta": "partial-text"}},
				{NotifyItemCompleted, map[string]any{"threadId": thread, "turnId": turn, "item": map[string]any{"id": "item", "type": "agentMessage", "text": "partial-text"}}},
				{NotifyThreadTokenUsageUpdated, map[string]any{"threadId": thread, "turnId": turn, "tokenUsage": map[string]any{"last": map[string]any{"inputTokens": 7, "outputTokens": 3, "totalTokens": 10, "cachedInputTokens": 0, "reasoningOutputTokens": 0}, "total": map[string]any{"inputTokens": 7, "outputTokens": 3, "totalTokens": 10, "cachedInputTokens": 0, "reasoningOutputTokens": 0}}}},
			} {
				frames = append(frames, map[string]any{"method": notification.method, "params": notification.params})
			}
			var wire strings.Builder
			encoder := json.NewEncoder(&wire)
			for _, frame := range frames {
				if err := encoder.Encode(frame); err != nil {
					t.Fatal(err)
				}
			}
			writeDone := make(chan error, 1)
			go func() {
				_, err := io.WriteString(output, wire.String())
				_ = output.Close()
				writeDone <- err
			}()
			alignmentRPCWait(t, ctx, gate.decoded)
			cancel(cause)
			// Cleanup closing stdin proves the RPC wait already returned the
			// cancellation; no response or notification has been dispatched yet.
			alignmentRPCWait(t, ctx, stdin.closed)
			unblock()
			got := alignmentRPCWait(t, ctx, done)
			if err := alignmentRPCWait(t, ctx, writeDone); err != nil {
				t.Fatal(err)
			}
			if !got.sent || !errors.Is(got.err, context.Canceled) || !errors.Is(got.err, cause) || got.response.Checkpoint != nil {
				t.Fatalf("cancellation/replay/checkpoint: sent=%v err=%v response=%+v", got.sent, got.err, got.response)
			}
			if got.response.RawStreams == nil || got.response.RawStreams.Stdout != wire.String() {
				t.Fatalf("raw drain incomplete: %+v", got.response.RawStreams)
			}
			select {
			case extra := <-stdin.writes:
				t.Fatalf("cancelled turn replayed or added RPC: %s", extra)
			default:
			}
			if scenario == "confirmed" {
				if got.response.Output != "partial-text" || got.response.Usage == nil || got.response.Usage.InputTokens != 7 || got.response.Usage.OutputTokens != 3 || len(got.response.Transcript) < 2 {
					t.Fatalf("confirmed audit lost after cancellation: %+v", got.response)
				}
			} else if got.response.Output != "" || got.response.Usage != nil || len(got.response.Transcript) != 1 {
				t.Fatalf("unconfirmed scope entered partial audit: %+v", got.response)
			}
		})
	}
}

func TestAlignmentEmptyTurnResponseFailsWithoutCancellation(t *testing.T) {
	for _, turnID := range []string{"", " \t"} {
		t.Run(turnID, func(t *testing.T) {
			ctx, client, stream := alignmentRPCSetup(t, nil)
			state := newRunState("run", &recordingSink{})
			state.setThread("thread")
			client.setTurnStartHandler(state.observeTurnStartResponse)
			client.SetNotificationHandler(state.onNotification)
			done := make(chan error, 1)
			go func() {
				_, err := client.TurnStart(ctx, TurnStartParams{ThreadID: "thread"})
				done <- err
			}()
			var request struct{ ID json.RawMessage }
			if err := json.Unmarshal(alignmentRPCWait(t, ctx, stream.writes), &request); err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(map[string]any{"id": request.ID, "result": map[string]any{"turn": map[string]any{"id": turnID}}})
			if err != nil {
				t.Fatal(err)
			}
			stream.frames <- alignmentRPCFrame{raw: string(raw)}
			if err := alignmentRPCWait(t, ctx, done); err == nil || err.Error() != "appserver: turn/start returned empty turn id" {
				t.Fatalf("empty turn response: %v", err)
			}
			if ctx.Err() != nil {
				t.Fatalf("invalid response waited for cancellation: %v", ctx.Err())
			}
			select {
			case <-client.DisconnectNotify():
				t.Fatal("invalid response waited for EOF")
			default:
			}
			state.bindReceivedTurn()
			if state.turnID != "" || state.receivedTurnID != "" {
				t.Fatal("invalid response bound a turn")
			}
		})
	}
}
