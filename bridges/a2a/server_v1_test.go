package a2a_test

import (
	"encoding/json"
	"strings"
	"testing"

	a2a "github.com/agent-dance/agent-adaptor/bridges/a2a"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/memory"
	adaptor "github.com/agent-dance/agent-adaptor/next"
)

// Redaction anchor: the v1 server keeps the legacy default ExposurePolicy —
// only the summary crosses the wire; metadata/usage/provider result/
// transcript/raw streams and run identity stay private unless opted in.
func TestNewServerV1DefaultExposureOmitsDiagnostics(t *testing.T) {
	t.Parallel()

	fake := &scriptedV1Driver{run: func(int, driver.Request, driver.EventSink) (driver.Response, error) {
		return driver.Response{
			Output:   "hello",
			Summary:  "safe summary",
			Metadata: map[string]string{"authorization": "Bearer super-secret"},
			Usage:    &driver.Usage{InputTokens: 12, OutputTokens: 34},
			RawStreams: &driver.RawStreams{
				Stdout:   "Authorization: Bearer hidden",
				Terminal: &driver.TerminalPayload{Event: "done", JSON: json.RawMessage(`{"token":"secret-token"}`)},
			},
			Transcript: []driver.TranscriptItem{{
				Kind: driver.TranscriptAssistant, Text: "hidden transcript",
			}},
		}, nil
	}}
	srv := a2a.NewServerV1(adaptor.New(fake), a2a.ServerOptionsV1{AgentCard: v1TestCard()})

	envelope := decodeV1Task(t, postRPC(t, srv.Handler(),
		`{"jsonrpc":"2.0","id":"1","method":"SendMessage","params":{"message":{"messageId":"m1","role":"ROLE_USER","parts":[{"text":"hello bridge"}]}}}`))
	if envelope.Error != nil {
		t.Fatalf("error = %+v", envelope.Error)
	}
	if envelope.Result.Task == nil || envelope.Result.Task.Status.State != "TASK_STATE_COMPLETED" {
		t.Fatalf("task = %+v", envelope.Result.Task)
	}
	artifact := findTaskArtifact(t, envelope.Result.Task.Artifacts, a2a.ArtifactAgentAdaptorResult)
	data := artifact.Parts[0].Data
	if got := data["summary"]; got != "safe summary" {
		t.Fatalf("summary = %#v", got)
	}
	for _, forbidden := range []string{"metadata", "usage", "result", "transcript", "raw_streams", "run_id", "driver_type"} {
		if _, ok := data[forbidden]; ok {
			t.Fatalf("unexpected diagnostic field %q in %+v", forbidden, data)
		}
	}
}

// Lifecycle anchor: a business failure (driver Response.Failure) surfaces as
// TASK_STATE_FAILED with the failure message preserved — the v1 counterpart
// of the legacy RunResult.Failure mapping, reached via *adaptor.RunError.
func TestNewServerV1BusinessFailureMapsToFailedTask(t *testing.T) {
	t.Parallel()

	fake := &scriptedV1Driver{run: func(int, driver.Request, driver.EventSink) (driver.Response, error) {
		return driver.Response{
			Output: "partial",
			Failure: &driver.RunFailure{
				Code:    driver.FailureAgentError,
				Message: "boom",
			},
		}, nil
	}}
	srv := a2a.NewServerV1(adaptor.New(fake), a2a.ServerOptionsV1{AgentCard: v1TestCard()})

	envelope := decodeV1Task(t, postRPC(t, srv.Handler(),
		`{"jsonrpc":"2.0","id":"1","method":"SendMessage","params":{"message":{"messageId":"m1","role":"ROLE_USER","parts":[{"text":"hello bridge"}]}}}`))
	if envelope.Error != nil {
		t.Fatalf("error = %+v", envelope.Error)
	}
	task := envelope.Result.Task
	if task == nil || task.Status.State != "TASK_STATE_FAILED" {
		t.Fatalf("task = %+v", task)
	}
	if task.Status.Message == nil || len(task.Status.Message.Parts) == 0 || task.Status.Message.Parts[0].Text != "boom" {
		t.Fatalf("failure message = %+v", task.Status.Message)
	}
}

// ThreadByContextID needs an *adaptor.Agent (only an Agent can mint threads):
// a contextID-bearing request against any other Runner is rejected as an
// invalid-params JSON-RPC error before the run starts.
func TestThreadByContextIDRequiresAgentRunner(t *testing.T) {
	t.Parallel()

	fake := &scriptedV1Driver{}
	agent := adaptor.New(fake, adaptor.WithThreadStore(memory.NewStore()))
	// A Thread is a Runner but not an Agent — binding must fail.
	srv := a2a.NewServerV1(agent.Thread("base-thread"), a2a.ServerOptionsV1{
		AgentCard: v1TestCard(),
		Session:   a2a.ThreadByContextID(),
	})

	envelope := decodeV1Task(t, postRPC(t, srv.Handler(),
		`{"jsonrpc":"2.0","id":"1","method":"SendMessage","params":{"message":{"messageId":"m1","contextId":"ctx-9","role":"ROLE_USER","parts":[{"text":"hello bridge"}]}}}`))
	if envelope.Error == nil || !strings.Contains(envelope.Error.Message, "ThreadByContextID requires") {
		t.Fatalf("error envelope = %+v, want ThreadByContextID requirement", envelope.Error)
	}
	fake.mu.Lock()
	runs := len(fake.requests)
	fake.mu.Unlock()
	if runs != 0 {
		t.Fatalf("driver ran %d times, want 0", runs)
	}
}

// A2A servers mint a contextID for messages that arrive without one, so
// ThreadByContextID still binds a (fresh) thread: the run must succeed and
// the driver must see a session-coordinated request.
func TestThreadByContextIDMintedContextStillThreads(t *testing.T) {
	t.Parallel()

	fake := &scriptedV1Driver{run: func(int, driver.Request, driver.EventSink) (driver.Response, error) {
		return driver.Response{
			Output: "done", Summary: "threaded",
			Checkpoint: &driver.Checkpoint{
				State: &driver.SessionState{ResumeID: "sess-minted"},
				Valid: true,
			},
		}, nil
	}}
	srv := a2a.NewServerV1(adaptor.New(fake, adaptor.WithThreadStore(memory.NewStore())), a2a.ServerOptionsV1{
		AgentCard: v1TestCard(),
		Session:   a2a.ThreadByContextID(),
	})

	envelope := decodeV1Task(t, postRPC(t, srv.Handler(),
		`{"jsonrpc":"2.0","id":"1","method":"SendMessage","params":{"message":{"messageId":"m1","role":"ROLE_USER","parts":[{"text":"hello bridge"}]}}}`))
	if envelope.Error != nil {
		t.Fatalf("error = %+v", envelope.Error)
	}
	if envelope.Result.Task == nil || envelope.Result.Task.Status.State != "TASK_STATE_COMPLETED" {
		t.Fatalf("task = %+v (status: %s)", envelope.Result.Task, v1StatusText(envelope))
	}
	req := fake.request(t, 0)
	if req.Session == nil || req.Session.Mode != driver.SessionContinueOrStart {
		t.Fatalf("session = %+v, want thread-coordinated mode %q", req.Session, driver.SessionContinueOrStart)
	}
}
