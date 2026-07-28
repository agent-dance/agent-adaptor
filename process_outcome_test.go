package adaptor_test

import (
	"context"
	"errors"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/memory"
)

func TestUnclassifiedAbnormalProcessOutcomeIsRunErrorWithPartialResult(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response driver.Response
		detail   string
	}{
		{
			name: "non-zero exit",
			response: driver.Response{
				ExitCode: 17,
			},
			detail: "exit_code",
		},
		{
			name: "signal",
			response: driver.Response{
				Signal: "signal: killed",
			},
			detail: "signal",
		},
		{
			name: "driver timeout without outer context deadline",
			response: driver.Response{
				TimedOut: true,
			},
			detail: "timed_out",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeDriver()
			fake.response = tc.response
			fake.response.Output = "partial assistant text"
			fake.response.Summary = "partial summary"
			fake.response.RawStreams = &driver.RawStreams{
				Stdout: "partial stdout",
				Stderr: "partial stderr",
			}
			fake.response.Transcript = []driver.TranscriptItem{{Kind: driver.TranscriptAssistant, Text: "partial assistant text"}}

			res, err := adaptor.New(fake).Run(context.Background(), "go")
			if res != nil {
				t.Fatalf("Result = %#v, want nil on failure", res)
			}
			var runErr *adaptor.RunError
			if !errors.As(err, &runErr) || !errors.Is(err, adaptor.ErrAgentFailed) {
				t.Fatalf("error = %T %v, want *RunError matching ErrAgentFailed", err, err)
			}
			if runErr.Result == nil || runErr.Result.Text != "partial assistant text" || runErr.Result.Summary != "partial summary" {
				t.Fatalf("partial Result = %#v", runErr.Result)
			}
			raw := runErr.Result.Raw()
			if raw.Stdout != "partial stdout" || raw.Stderr != "partial stderr" {
				t.Fatalf("partial Raw = %#v", raw)
			}
			transcript := runErr.Result.Transcript()
			if len(transcript) != 1 || transcript[0].Text != "partial assistant text" {
				t.Fatalf("partial Transcript = %#v", transcript)
			}
			if _, ok := runErr.Details[tc.detail]; !ok {
				t.Fatalf("Details = %#v, want %q", runErr.Details, tc.detail)
			}
		})
	}
}

func TestOuterCancellationKeepsContextIdentityWithoutProviderFailure(t *testing.T) {
	fake := newFakeDriver()
	fake.runFunc = func(ctx context.Context, _ driver.Request, _ driver.EventSink) (driver.Response, error) {
		<-ctx.Done()
		return driver.Response{
			Output:     "partial",
			ExitCode:   -1,
			Signal:     ctx.Err().Error(),
			TimedOut:   errors.Is(ctx.Err(), context.DeadlineExceeded),
			RawStreams: &driver.RawStreams{Stdout: "before cancellation"},
		}, nil
	}

	_, err := adaptor.New(fake).Run(context.Background(), "go", adaptor.WithTimeout(20*time.Millisecond))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %T %v, want context.DeadlineExceeded", err, err)
	}
	var runErr *adaptor.RunError
	if errors.As(err, &runErr) {
		t.Fatalf("bare outer cancellation became RunError: %#v", runErr)
	}
}

func TestProviderFailureWinsConcurrentCancellationAndPreservesResult(t *testing.T) {
	fake := newFakeDriver()
	fake.runFunc = func(ctx context.Context, _ driver.Request, _ driver.EventSink) (driver.Response, error) {
		<-ctx.Done()
		return driver.Response{
			Output:     "provider partial",
			ExitCode:   -1,
			TimedOut:   true,
			RawStreams: &driver.RawStreams{Stdout: "provider raw"},
			Failure: &driver.RunFailure{
				Code:    driver.FailureAgentError,
				Message: "official provider terminal failure",
			},
		}, ctx.Err()
	}

	_, err := adaptor.New(fake).Run(context.Background(), "go", adaptor.WithTimeout(20*time.Millisecond))
	var runErr *adaptor.RunError
	if !errors.As(err, &runErr) || !errors.Is(err, adaptor.ErrAgentFailed) {
		t.Fatalf("error = %T %v, want provider *RunError", err, err)
	}
	if runErr.Message != "official provider terminal failure" || runErr.Result == nil || runErr.Result.Raw().Stdout != "provider raw" {
		t.Fatalf("provider failure/result = %#v", runErr)
	}
}

func TestAbnormalOutcomeCannotPersistThreadCheckpoint(t *testing.T) {
	store := memory.NewStore()
	fake := newFakeDriver()
	fake.response = driver.Response{
		Output:   "partial",
		ExitCode: 9,
		Checkpoint: &driver.Checkpoint{
			Valid: true,
			State: &driver.SessionState{ResumeID: "must-not-persist"},
		},
	}
	agent := adaptor.New(fake, adaptor.WithThreadStore(store))

	_, err := agent.Thread("outcome-thread").Run(context.Background(), "go")
	if !errors.Is(err, adaptor.ErrAgentFailed) {
		t.Fatalf("error = %T %v, want ErrAgentFailed", err, err)
	}
	if _, checkpointErr := agent.Thread("outcome-thread").Checkpoint(context.Background()); !errors.Is(checkpointErr, adaptor.ErrThreadNotFound) {
		t.Fatalf("Checkpoint error = %v, want ErrThreadNotFound", checkpointErr)
	}
}
