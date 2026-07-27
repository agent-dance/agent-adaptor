package a2adelegation_test

// Local/Remote behavioral parity (P4 gate requirement): the same role
// registered once as delegation.Local (in-process Runner loopback) and once
// as delegation.Remote (real httptest A2A server consumed through
// clients/a2a) must yield the same DelegationEvent sequence and the same
// DelegationResult, modulo transport-assigned identifiers (delegation ID,
// task/context IDs, timestamps, raw payloads). Both sides speak the
// adapter.stream.v1 status DataPart profile, so the shared eventMapper is
// exercised on the identical decode path.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/hosttools/a2adelegation"
	adaptor "github.com/agent-dance/agent-adaptor"
)

// parityScript defines one role behavior realized on both transports.
type parityScript struct {
	deltas []string
	final  string
	fail   bool
}

// eventShape is the transport-independent projection of a DelegationEvent:
// everything semantic is kept; delegation IDs, task/context IDs (normalized
// to a placeholder), wall-clock times, and Raw payloads are excluded.
type eventShape struct {
	Kind      a2adelegation.DelegationEventKind
	AgentKey  string
	AgentName string
	Status    string
	Delta     string
	Text      string
	Role      string
	ToolName  string
	MessageID string
	TaskID    string
	Sequence  uint64
	ErrCode   string
}

func shapeOf(ev a2adelegation.Event, taskID string) eventShape {
	shape := eventShape{
		Kind:      ev.Kind,
		AgentKey:  ev.AgentKey,
		AgentName: ev.AgentName,
		Status:    ev.Status,
		Delta:     ev.Delta,
		Text:      ev.Text,
		Role:      ev.Role,
		ToolName:  ev.ToolName,
		MessageID: normalizeTaskID(ev.RemoteMessageID, taskID),
		TaskID:    normalizeTaskID(ev.RemoteTaskID, taskID),
		Sequence:  ev.Sequence,
	}
	if ev.Error != nil {
		shape.ErrCode = ev.Error.Code
	}
	return shape
}

func normalizeTaskID(value, taskID string) string {
	if taskID == "" {
		return value
	}
	return strings.ReplaceAll(value, taskID, "<task>")
}

// resultShape is the transport-independent projection of DelegationResult.
type resultShape struct {
	Agent    string
	Status   string
	Summary  string
	Messages []a2adelegation.DelegationMessage
	TaskID   string
	ErrCode  string
	ErrState string
}

func shapeOfResult(res a2adelegation.DelegationResult) resultShape {
	shape := resultShape{
		Agent:    res.Agent,
		Status:   res.Status,
		Summary:  res.Summary,
		Messages: res.Messages,
		TaskID:   normalizeTaskID(res.RemoteTaskID, res.RemoteTaskID),
	}
	if res.Error != nil {
		shape.ErrCode = res.Error.Code
		shape.ErrState = res.Error.RemoteStatus
	}
	return shape
}

// runDelegation executes one delegation on svc and returns the full ordered
// event sequence (subscription opens before Delegate, so nothing is missed;
// the terminal event is flushed before Delegate returns, so reading up to
// the terminal never blocks).
func runDelegation(t *testing.T, svc *a2adelegation.Service, runID string) ([]a2adelegation.Event, a2adelegation.DelegationResult, error) {
	t.Helper()
	subCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := svc.Bus().SubscribeRun(subCtx, runID)
	res, delegateErr := svc.Delegate(context.Background(), a2adelegation.DelegationRequest{
		RunID:     runID,
		Agent:     "echo",
		Objective: "run the parity objective",
	})
	var events []a2adelegation.Event
	for ev := range ch {
		events = append(events, ev)
		if isTerminalKind(ev.Kind) {
			break
		}
	}
	return events, res, delegateErr
}

func isTerminalKind(kind a2adelegation.DelegationEventKind) bool {
	switch kind {
	case a2adelegation.DelegationFinished, a2adelegation.DelegationFailed,
		a2adelegation.DelegationCancelled, a2adelegation.DelegationInputRequired:
		return true
	default:
		return false
	}
}

// remoteParityServer serves the httptest A2A fixture whose wire frames are
// generated from the same script the local Runner executes: an agent card
// with streaming enabled, and a SendStreamingMessage SSE stream of one
// working status, one adapter.stream.v1 status frame per text lifecycle
// event, and a terminal task.
func remoteParityServer(t *testing.T, script parityScript) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/agent-card.json":
			_, _ = fmt.Fprintf(w, `{
				"name":"echo",
				"description":"parity fixture",
				"version":"1.0.0",
				"supportedInterfaces":[{"url":%q,"protocolBinding":"JSONRPC","protocolVersion":"1.0"}],
				"capabilities":{"streaming":true},
				"defaultInputModes":["text/plain"],
				"defaultOutputModes":["text/plain"],
				"skills":[{"id":"echo","name":"Echo","description":"echo","tags":["echo"]}]
			}`, srv.URL+"/a2a")
		case "/a2a":
			var req struct {
				Method string `json:"method"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if req.Method != "SendStreamingMessage" {
				http.Error(w, "unexpected method "+req.Method, http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			for _, frame := range parityWireFrames(t, script) {
				_, _ = fmt.Fprintf(w, "data: %s\n\n", frame)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	return srv
}

// parityWireFrames renders the SSE JSON-RPC result frames for the script,
// mirroring exactly what the local loopback synthesizes in-process.
func parityWireFrames(t *testing.T, script parityScript) []string {
	t.Helper()
	rpc := func(result string) string {
		return `{"jsonrpc":"2.0","id":"1","result":` + result + `}`
	}
	statusFrame := func(seq uint64, event map[string]any) string {
		envelope := map[string]any{
			"schema": "adapter.stream.v1",
			"event":  event,
		}
		raw, err := json.Marshal(envelope)
		if err != nil {
			t.Fatalf("marshal envelope: %v", err)
		}
		return rpc(fmt.Sprintf(`{"statusUpdate":{"taskId":"task-1","contextId":"ctx-1","status":{"state":"TASK_STATE_WORKING","message":{"messageId":"sm-%d","role":"ROLE_AGENT","taskId":"task-1","contextId":"ctx-1","parts":[{"data":%s}]}}}}`, seq, raw))
	}
	frames := []string{
		rpc(`{"statusUpdate":{"taskId":"task-1","contextId":"ctx-1","status":{"state":"TASK_STATE_WORKING"}}}`),
	}
	if len(script.deltas) > 0 {
		seq := uint64(1)
		frames = append(frames, statusFrame(seq, map[string]any{"kind": "text.start", "sequence": seq, "message_id": "m1"}))
		for _, delta := range script.deltas {
			seq++
			frames = append(frames, statusFrame(seq, map[string]any{"kind": "text.content", "sequence": seq, "message_id": "m1", "delta": delta}))
		}
		seq++
		frames = append(frames, statusFrame(seq, map[string]any{"kind": "text.end", "sequence": seq, "message_id": "m1"}))
	}
	if script.fail {
		frames = append(frames, rpc(`{"task":{"id":"task-1","contextId":"ctx-1","status":{"state":"TASK_STATE_FAILED"}}}`))
	} else {
		finalMsg, err := json.Marshal(script.final)
		if err != nil {
			t.Fatalf("marshal final text: %v", err)
		}
		frames = append(frames, rpc(fmt.Sprintf(`{"task":{"id":"task-1","contextId":"ctx-1","status":{"state":"TASK_STATE_COMPLETED"},"history":[{"messageId":"task-1:final","role":"ROLE_AGENT","taskId":"task-1","contextId":"ctx-1","parts":[{"text":%s}]}]}}`, finalMsg)))
	}
	return frames
}

func runParityPair(t *testing.T, script parityScript) (local, remote struct {
	shapes []eventShape
	result resultShape
	err    error
}) {
	t.Helper()

	// Local side: in-process Runner behind delegation.Local.
	roleDriver := &scriptedRoleDriver{kind: "fake-parity", deltas: script.deltas, final: script.final}
	if script.fail {
		roleDriver.final = ""
		roleDriver.fail = &parityRunFailure
	}
	localSvc, err := a2adelegation.NewService(a2adelegation.Config{
		Agents: []a2adelegation.AgentRef{
			a2adelegation.Local("echo", adaptor.New(roleDriver), a2adelegation.Policy{MaxTimeout: time.Minute}),
		},
	})
	if err != nil {
		t.Fatalf("NewService(local): %v", err)
	}
	defer localSvc.Close()

	// Remote side: httptest A2A server behind delegation.Remote.
	srv := remoteParityServer(t, script)
	defer srv.Close()
	remoteSvc, err := a2adelegation.NewService(a2adelegation.Config{
		Agents: []a2adelegation.AgentRef{
			a2adelegation.Remote("echo", srv.URL, a2adelegation.Policy{MaxTimeout: time.Minute}),
		},
	})
	if err != nil {
		t.Fatalf("NewService(remote): %v", err)
	}
	defer remoteSvc.Close()

	localEvents, localResult, localErr := runDelegation(t, localSvc, "run-parity")
	remoteEvents, remoteResult, remoteErr := runDelegation(t, remoteSvc, "run-parity")

	local.shapes = shapesOf(localEvents, localResult.RemoteTaskID)
	local.result = shapeOfResult(localResult)
	local.err = localErr
	remote.shapes = shapesOf(remoteEvents, remoteResult.RemoteTaskID)
	remote.result = shapeOfResult(remoteResult)
	remote.err = remoteErr
	return local, remote
}

var parityRunFailure = driver.RunFailure{Message: "role failed"}

func shapesOf(events []a2adelegation.Event, taskID string) []eventShape {
	shapes := make([]eventShape, 0, len(events))
	for _, ev := range events {
		shapes = append(shapes, shapeOf(ev, taskID))
	}
	return shapes
}

func TestLocalRemoteParitySuccess(t *testing.T) {
	script := parityScript{
		deltas: []string{"hello ", "world"},
		final:  "hello world\nPARITY_DONE",
	}
	local, remote := runParityPair(t, script)

	if local.err != nil || remote.err != nil {
		t.Fatalf("errs: local=%v remote=%v, want both nil", local.err, remote.err)
	}
	if !reflect.DeepEqual(local.shapes, remote.shapes) {
		t.Fatalf("event shapes diverge:\nlocal:  %s\nremote: %s", dumpShapes(local.shapes), dumpShapes(remote.shapes))
	}
	// Documented canonical sequence: one delegation over the
	// adapter.stream.v1 profile yields started → status(working) →
	// (status+decoded) per frame → status(completed) → finished.
	wantKinds := []a2adelegation.DelegationEventKind{
		a2adelegation.DelegationStarted,
		a2adelegation.DelegationStatus,
		a2adelegation.DelegationStatus, a2adelegation.DelegationTextStart,
		a2adelegation.DelegationStatus, a2adelegation.DelegationTextDelta,
		a2adelegation.DelegationStatus, a2adelegation.DelegationTextDelta,
		a2adelegation.DelegationStatus, a2adelegation.DelegationTextEnd,
		a2adelegation.DelegationStatus,
		a2adelegation.DelegationFinished,
	}
	if len(local.shapes) != len(wantKinds) {
		t.Fatalf("event count = %d, want %d:\n%s", len(local.shapes), len(wantKinds), dumpShapes(local.shapes))
	}
	for i, want := range wantKinds {
		if local.shapes[i].Kind != want {
			t.Errorf("event[%d].Kind = %q, want %q", i, local.shapes[i].Kind, want)
		}
	}
	var text strings.Builder
	for _, shape := range local.shapes {
		if shape.Kind == a2adelegation.DelegationTextDelta {
			text.WriteString(shape.Delta)
		}
	}
	if text.String() != "hello world" {
		t.Errorf("streamed text = %q, want %q", text.String(), "hello world")
	}

	if !reflect.DeepEqual(local.result, remote.result) {
		t.Fatalf("results diverge:\nlocal:  %+v\nremote: %+v", local.result, remote.result)
	}
	if local.result.Status != "completed" {
		t.Errorf("status = %q, want completed", local.result.Status)
	}
	if local.result.Summary != script.final {
		t.Errorf("summary = %q, want %q", local.result.Summary, script.final)
	}
}

func TestLocalRemoteParityFailure(t *testing.T) {
	script := parityScript{fail: true}
	local, remote := runParityPair(t, script)

	if local.err == nil || remote.err == nil {
		t.Fatalf("errs: local=%v remote=%v, want both non-nil", local.err, remote.err)
	}
	var localDErr, remoteDErr *a2adelegation.DelegationError
	if !errors.As(local.err, &localDErr) || !errors.As(remote.err, &remoteDErr) {
		t.Fatalf("errs not *DelegationError: local=%T remote=%T", local.err, remote.err)
	}
	if localDErr.Code != remoteDErr.Code || localDErr.Code != "remote_failed" {
		t.Errorf("error codes: local=%q remote=%q, want both remote_failed", localDErr.Code, remoteDErr.Code)
	}
	if !reflect.DeepEqual(local.shapes, remote.shapes) {
		t.Fatalf("event shapes diverge:\nlocal:  %s\nremote: %s", dumpShapes(local.shapes), dumpShapes(remote.shapes))
	}
	wantKinds := []a2adelegation.DelegationEventKind{
		a2adelegation.DelegationStarted,
		a2adelegation.DelegationStatus,
		a2adelegation.DelegationStatus,
		a2adelegation.DelegationFailed,
	}
	if len(local.shapes) != len(wantKinds) {
		t.Fatalf("event count = %d, want %d:\n%s", len(local.shapes), len(wantKinds), dumpShapes(local.shapes))
	}
	for i, want := range wantKinds {
		if local.shapes[i].Kind != want {
			t.Errorf("event[%d].Kind = %q, want %q", i, local.shapes[i].Kind, want)
		}
	}
	if !reflect.DeepEqual(local.result, remote.result) {
		t.Fatalf("results diverge:\nlocal:  %+v\nremote: %+v", local.result, remote.result)
	}
	if local.result.Status != "failed" {
		t.Errorf("status = %q, want failed", local.result.Status)
	}
}

func dumpShapes(shapes []eventShape) string {
	var b strings.Builder
	for i, shape := range shapes {
		fmt.Fprintf(&b, "\n  [%d] %+v", i, shape)
	}
	return b.String()
}
