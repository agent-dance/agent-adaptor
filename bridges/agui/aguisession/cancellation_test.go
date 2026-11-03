package aguisession

import (
	"context"
	"strings"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
)

// Some providers surface a killed process as a business agent_error. The
// session's own context is the cancellation authority, not that classification.
func TestSessionCancellationOverridesDriverFailure(t *testing.T) {
	for _, mode := range []string{"host cancel", "shutdown", "request disconnect", "driver failure"} {
		t.Run(mode, func(t *testing.T) {
			started := make(chan struct{})
			release := make(chan struct{})
			runner := fakeRunner{makeStream: func(ctx context.Context, _ string) adaptor.Stream {
				ch := make(chan adaptor.Event)
				go func() {
					close(started)
					if mode == "driver failure" {
						<-release
					} else {
						<-ctx.Done()
					}
					close(ch)
				}()
				return &fakeStream{ch: ch, err: &adaptor.RunError{Reason: adaptor.ReasonAgentError, Message: "provider process exited with status -1"}}
			}}
			s := New(runner, Options{Recorder: recorder(t), Unattended: mode != "request disconnect", BaseContext: context.Background()})
			defer s.Shutdown(context.Background())
			disconnect, done := launchRequest(s, "t", "r")
			await(t, started)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			switch mode {
			case "host cancel":
				if err := s.Cancel(ctx, ThreadKey("t")); err != nil {
					t.Fatal(err)
				}
			case "shutdown":
				if err := s.Shutdown(ctx); err != nil {
					t.Fatal(err)
				}
			case "request disconnect":
				disconnect()
			default:
				close(release)
			}
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("request did not drain")
			}
			records, err := s.Events(ctx, ThreadKey("t"), 0)
			if err != nil {
				t.Fatal(err)
			}
			events := flattened(t, records)
			last := events[len(events)-1]
			want := "run.cancelled"
			if mode == "driver failure" {
				want = "agent_error"
			}
			if last["type"] != "RUN_ERROR" || last["code"] != want {
				t.Fatalf("terminal = %v", last)
			}
			if !strings.Contains(last["message"].(string), "provider process exited") {
				t.Fatal("provider diagnostic was lost")
			}
		})
	}
}
