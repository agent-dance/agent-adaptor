package appserver

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/v2/driver"
)

type cancelAfterTurnSink struct {
	recordingSink
	cancel context.CancelFunc
}

func (s *cancelAfterTurnSink) Emit(event driver.RunEvent) error {
	if err := s.recordingSink.Emit(event); err != nil {
		return err
	}
	if event.Type == driver.RunEventTurnCompleted {
		s.cancel()
	}
	return nil
}

// A cancellation caused by a consumer observing the completed turn must not
// replace a provider failure that the parser already observed.
func TestProviderFailureBeforeCancellationKeepsFailure(t *testing.T) {
	fake := buildFakeAppserver(t)
	for _, resident := range []bool{false, true} {
		t.Run(map[bool]string{false: "one-shot", true: "resident"}[resident], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			sink := &cancelAfterTurnSink{cancel: cancel}
			opts := Options{Command: fake, Env: []driver.EnvBinding{{Name: "FAKE_APPSERVER_SCENARIO", Value: "failed"}}}
			var result driver.Response
			var err error
			if resident {
				process, openErr := Open(ctx, opts, sink)
				if openErr != nil {
					t.Fatal(openErr)
				}
				t.Cleanup(func() { _ = process.TerminateAndWait(context.Background()) })
				result, _, err = process.RunTurn(ctx, opts, sink)
			} else {
				result, err = Run(ctx, opts, sink)
			}
			if !errors.Is(ctx.Err(), context.Canceled) {
				t.Fatalf("terminal observer did not cancel execution: %v", ctx.Err())
			}
			if errors.Is(err, context.Canceled) || result.Failure == nil || result.Failure.Code == driver.FailureCancelled {
				t.Fatalf("provider failure replaced by cancellation: err=%v failure=%+v", err, result.Failure)
			}
			if result.Checkpoint != nil || result.RawStreams == nil || result.RawStreams.Terminal == nil {
				t.Fatalf("failure output contract lost: %+v", result)
			}
		})
	}
}
