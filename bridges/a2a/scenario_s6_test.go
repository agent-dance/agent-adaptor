// Scenario S6 (design doc): publish an agent over A2A.
//
//	agent := adaptor.New(driver, adaptor.WithThreadStore(store))
//	srv := a2a.NewServer(agent, a2a.ServerOptions{
//	    AgentCard: a2a.AgentCard{Name: "Local Codex", ...},
//	    Session:   a2a.ThreadByContextID(),
//	})
//	http.Handle("/a2a", srv.Handler())
//
// Two SendMessage calls sharing contextId "ctx-1" must land on the same
// conversation thread: the second driver invocation resumes the checkpoint
// persisted by the first (Session.Mode == continue_or_start with the stored
// ResumeID).
package a2a_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor"
	a2a "github.com/agent-dance/agent-adaptor/bridges/a2a"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/memory"
)

// scriptedDriver is a minimal driver.Driver for bridge
// tests. It records every Request and delegates each turn to the script.
type scriptedDriver struct {
	mu       sync.Mutex
	requests []driver.Request
	run      func(turn int, req driver.Request, sink driver.EventSink) (driver.Response, error)
}

type scriptedSessionCodec struct{}

func (scriptedSessionCodec) Name() string { return "scripted/session" }
func (scriptedSessionCodec) ToParams(state *driver.SessionState) driver.SessionParams {
	if state == nil {
		return driver.SessionParams{}
	}
	return driver.SessionParams{ResumeID: state.ResumeID, DisplayID: state.DisplayID, Values: state.Data}
}
func (scriptedSessionCodec) FromParams(params driver.SessionParams) *driver.SessionState {
	if params.ResumeID == "" && params.DisplayID == "" && len(params.Values) == 0 {
		return nil
	}
	return &driver.SessionState{ResumeID: params.ResumeID, DisplayID: params.DisplayID, Data: params.Values}
}
func (scriptedSessionCodec) GuardFingerprint(params driver.SessionParams) string {
	return params.ResumeID
}

func (d *scriptedDriver) Descriptor() driver.Descriptor {
	return driver.Descriptor{
		Type:        "fake",
		DisplayName: "Fake",
		Sessions:    driver.SessionCapability{SupportsResume: true},
	}
}

func (d *scriptedDriver) ValidateConfig(any) error { return nil }

func (d *scriptedDriver) SessionConfigFingerprint() (string, error) {
	return "scripted", nil
}
func (d *scriptedDriver) SessionCodec() driver.SessionCodec { return scriptedSessionCodec{} }

func (d *scriptedDriver) Run(ctx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
	d.mu.Lock()
	turn := len(d.requests)
	d.requests = append(d.requests, req)
	run := d.run
	d.mu.Unlock()
	if run == nil {
		return driver.Response{Output: "ok"}, nil
	}
	return run(turn, req, sink)
}

func (d *scriptedDriver) request(t *testing.T, i int) driver.Request {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	if i >= len(d.requests) {
		t.Fatalf("driver saw %d requests, want index %d", len(d.requests), i)
	}
	return d.requests[i]
}

// taskEnvelope decodes the non-streaming SendMessage JSON-RPC response.
type taskEnvelope struct {
	Result struct {
		Task *struct {
			ID     string `json:"id"`
			Status struct {
				State   string `json:"state"`
				Message *struct {
					Parts []taskArtifactPart `json:"parts"`
				} `json:"message"`
			} `json:"status"`
			Artifacts []taskArtifact `json:"artifacts"`
		} `json:"task"`
	} `json:"result"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// statusText flattens the status message parts for failure dumps.
func statusText(envelope taskEnvelope) string {
	task := envelope.Result.Task
	if task == nil || task.Status.Message == nil {
		return ""
	}
	var parts []string
	for _, p := range task.Status.Message.Parts {
		parts = append(parts, p.Text)
	}
	return strings.Join(parts, " | ")
}

func decodeTask(t *testing.T, resp *http.Response) taskEnvelope {
	t.Helper()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var envelope taskEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return envelope
}

func testCard() a2a.AgentCard {
	return a2a.AgentCard{
		Name: "Local Codex", Description: "test", Version: "1.0.0", URL: "https://example.com/a2a",
		Skills: []a2a.Skill{{ID: "chat", Name: "Chat", Description: "chat"}},
	}
}

func TestScenarioS6PublishAgentOverA2A(t *testing.T) {
	t.Parallel()

	fake := &scriptedDriver{run: func(turn int, req driver.Request, sink driver.EventSink) (driver.Response, error) {
		_ = sink.EmitStream(driver.StreamPayload{Kind: driver.StreamTextContent, Delta: "turn output"})
		if turn == 0 {
			return driver.Response{
				Output:  "hi there",
				Summary: "greeted",
				Checkpoint: &driver.Checkpoint{
					State: &driver.SessionState{ResumeID: "chat-sess-1"},
					Valid: true,
				},
			}, nil
		}
		// Thread runs require a resumable checkpoint from every turn —
		// the engine fails the run otherwise ("driver returned no
		// resumable checkpoint"), matching real resume-capable drivers.
		return driver.Response{
			Output:  "welcome back",
			Summary: "resumed",
			Checkpoint: &driver.Checkpoint{
				State: &driver.SessionState{ResumeID: "chat-sess-2"},
				Valid: true,
			},
		}, nil
	}}

	agent := adaptor.New(fake, adaptor.WithThreadStore(memory.NewStore()))
	srv := a2a.NewServer(agent, a2a.ServerOptions{
		AgentCard: testCard(),
		Session:   a2a.ThreadByContextID(),
	})
	handler := srv.Handler()

	first := decodeTask(t, postRPC(t, handler,
		`{"jsonrpc":"2.0","id":"1","method":"SendMessage","params":{"message":{"messageId":"m1","contextId":"ctx-1","role":"ROLE_USER","parts":[{"text":"hello there"}]}}}`))
	if first.Error != nil {
		t.Fatalf("turn 1 error = %+v", first.Error)
	}
	if first.Result.Task == nil || first.Result.Task.Status.State != "TASK_STATE_COMPLETED" {
		t.Fatalf("turn 1 task = %+v", first.Result.Task)
	}
	artifact := findTaskArtifact(t, first.Result.Task.Artifacts, a2a.ArtifactAgentAdaptorResult)
	if got := artifact.Parts[0].Data["summary"]; got != "greeted" {
		t.Fatalf("turn 1 summary = %#v", got)
	}

	second := decodeTask(t, postRPC(t, handler,
		`{"jsonrpc":"2.0","id":"2","method":"SendMessage","params":{"message":{"messageId":"m2","contextId":"ctx-1","role":"ROLE_USER","parts":[{"text":"back again"}]}}}`))
	if second.Error != nil {
		t.Fatalf("turn 2 error = %+v", second.Error)
	}
	if second.Result.Task == nil || second.Result.Task.Status.State != "TASK_STATE_COMPLETED" {
		t.Fatalf("turn 2 task = %+v (status: %s)", second.Result.Task, statusText(second))
	}

	// Thread continuity: turn 1 started fresh, turn 2 resumed the persisted
	// checkpoint under the same "a2a/ctx-1" thread key.
	turn1 := fake.request(t, 0)
	if turn1.Prompt != "hello there" {
		t.Fatalf("turn 1 prompt = %q", turn1.Prompt)
	}
	if turn1.Session != nil && turn1.Session.State != nil && turn1.Session.State.ResumeID != "" {
		t.Fatalf("turn 1 unexpectedly resumed: %+v", turn1.Session.State)
	}
	turn2 := fake.request(t, 1)
	if turn2.Prompt != "back again" {
		t.Fatalf("turn 2 prompt = %q", turn2.Prompt)
	}
	if turn2.Session == nil || turn2.Session.Mode != driver.SessionContinueOrStart {
		t.Fatalf("turn 2 session = %+v, want mode %q", turn2.Session, driver.SessionContinueOrStart)
	}
	if turn2.Session.State == nil || turn2.Session.State.ResumeID != "chat-sess-1" {
		t.Fatalf("turn 2 resume state = %+v, want ResumeID chat-sess-1", turn2.Session.State)
	}
}
