package claude

import (
	"bufio"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/agent-dance/agent-adaptor/v2/driver"
)

type blockedHookStdin struct {
	prompt  chan struct{}
	hook    chan struct{}
	release chan struct{}
	closed  sync.Once
	writes  int
	partial int
}

func (s *blockedHookStdin) Write(b []byte) (int, error) {
	s.writes++
	if s.writes == 1 {
		close(s.prompt)
		return len(b), nil
	}
	close(s.hook)
	<-s.release
	return s.partial, io.ErrClosedPipe
}

func (s *blockedHookStdin) Close() error {
	s.closed.Do(func() { close(s.release) })
	return nil
}

// A resident provider may stop consuming a timed-out hook response and finish
// the turn without exiting its process. A response larger than the stdin pipe
// then stays blocked even though the reader has received the final result.
func TestMessageResidentResultSettlesBlockedHookWriter(t *testing.T) {
	for _, partial := range []int{0, 16} {
		t.Run(map[int]string{0: "no-bytes", 16: "partial-frame"}[partial], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				stdin := &blockedHookStdin{prompt: make(chan struct{}), hook: make(chan struct{}), release: make(chan struct{}), partial: partial}
				stdout, provider := io.Pipe()
				defer stdout.Close()
				defer provider.Close()
				lp := &liveProcess{stdin: stdin, stdout: bufio.NewReader(stdout), stderr: &lockedBuffer{}, hooksInitialized: true}
				port := newMessageTestPort(driver.MessageItem{ID: "hook", Text: "large hook context"})
				parser := newClaudeParser(&messageTestSink{})
				parser.messages = newMessageSession(context.Background(), port)
				done := make(chan struct{})
				var runErr error
				var raw driver.RawStreams
				go func() {
					defer close(done)
					raw, _, runErr = lp.messageTurn(context.Background(), "initial", &messageTestSink{}, parser, nil, false)
				}()
				<-stdin.prompt
				if _, err := provider.Write(messageFrame(messageHook("PreToolUse", "", ""))); err != nil {
					t.Fatal(err)
				}
				<-stdin.hook
				port.mu.Lock()
				port.pending = append(port.pending, driver.MessageItem{ID: "queued", Text: "never written"})
				port.mu.Unlock()
				if _, err := provider.Write(messageFrame(messageHook("PostToolUse", "", ""))); err != nil {
					t.Fatal(err)
				}
				if _, err := io.WriteString(provider, "{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"session_id\":\"session\",\"result\":\"done\"}\n"); err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				hung := false
				select {
				case <-done:
				default:
					hung = true
				}
				// Unblock for cleanup if the regression leaves the writer stuck.
				_ = stdin.Close()
				<-done
				if hung {
					t.Fatal("resident turn received its final result but hung waiting for a blocked hook write; claim remains unacked until external pipe close")
				}
				if !lp.isClosed() || !errors.Is(runErr, io.ErrClosedPipe) {
					t.Fatalf("resident closed=%t error=%v", lp.isClosed(), runErr)
				}
				if raw.Stdout == "" || raw.Terminal == nil || raw.Terminal.Event != "result" || len(parser.transcript) == 0 {
					t.Fatalf("retired resident lost its terminal protocol observations: %+v", raw)
				}
				for _, id := range []string{"hook", "queued"} {
					acks := port.acks[id]
					if len(acks) != 1 || acks[0].Delivered || !acks[0].Requeue || !errors.Is(acks[0].Err, io.ErrClosedPipe) || acks[0].MaybeDelivered != (id == "hook" && partial > 0) {
						t.Fatalf("%s receipts=%+v", id, acks)
					}
				}
				if stdin.writes != 2 {
					t.Fatalf("writes=%d, want prompt and one hook attempt", stdin.writes)
				}
			})
		})
	}
}
