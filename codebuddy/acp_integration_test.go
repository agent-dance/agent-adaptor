package codebuddy

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"testing"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/sourcegraph/jsonrpc2"
)

// fakeDecisionSink is an EventSink that also satisfies DecisionCapableSink so
// the ACP engine's Ask path can be exercised with a canned host decision. It
// records the decision requests and every StreamPayload for assertions.
type fakeDecisionSink struct {
	mu       sync.Mutex
	streams  []agentadaptor.StreamPayload
	requests []agentadaptor.DecisionRequest
	respond  func(agentadaptor.DecisionRequest) agentadaptor.DecisionResponse
}

func (f *fakeDecisionSink) Emit(agentadaptor.RunEvent) error { return nil }

func (f *fakeDecisionSink) EmitStream(p agentadaptor.StreamPayload) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.streams = append(f.streams, p)
	return nil
}

func (f *fakeDecisionSink) RequestDecision(_ context.Context, req agentadaptor.DecisionRequest) (agentadaptor.DecisionResponse, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	respond := f.respond
	f.mu.Unlock()
	resp := respond(req)
	resp.RequestID = req.RequestID
	return resp, nil
}

func (f *fakeDecisionSink) streamKinds() map[agentadaptor.StreamKind]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[agentadaptor.StreamKind]int{}
	for _, p := range f.streams {
		out[p.Kind]++
	}
	return out
}

// fakeACPServer answers the driver's client handshake and, during
// session/prompt, runs a caller-supplied script that emits notifications and
// server-initiated requests (e.g. session/request_permission).
type fakeACPServer struct {
	sessionID string
	onPrompt  func(ctx context.Context, conn *jsonrpc2.Conn)
}

func (f *fakeACPServer) Handle(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	switch req.Method {
	case acpInitialize:
		_ = conn.Reply(ctx, req.ID, map[string]any{"protocolVersion": acpProtocolVersion})
	case acpSessionNew:
		_ = conn.Reply(ctx, req.ID, map[string]any{"sessionId": f.sessionID})
	case acpSessionLoad:
		_ = conn.Reply(ctx, req.ID, map[string]any{})
	case acpSessionPrompt:
		// Run the prompt script off the read loop so nested server->client
		// calls do not block dispatch, then reply to end the turn.
		go func() {
			if f.onPrompt != nil {
				f.onPrompt(ctx, conn)
			}
			_ = conn.Reply(ctx, req.ID, map[string]any{"stopReason": "end_turn"})
		}()
	default:
		_ = conn.ReplyWithError(ctx, req.ID, &jsonrpc2.Error{Code: jsonrpc2.CodeMethodNotFound, Message: "unexpected " + req.Method})
	}
}

// acpExchange wires an in-memory JSON-RPC duplex between the real CodeBuddy ACP
// client (acpHandler + acpState + establishACPSession) and a fake server, then
// drives the initialize / session/new / session/prompt handshake exactly like
// runACP does. It returns the assembled driver result and the prompt error.
func acpExchange(t *testing.T, policy agentadaptor.HumanDecisionPolicy, sink agentadaptor.EventSink, onPrompt func(ctx context.Context, conn *jsonrpc2.Conn)) (agentadaptor.DriverRunResult, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	clientToServerR, clientToServerW := io.Pipe()
	serverToClientR, serverToClientW := io.Pipe()
	clientStream := newStdioStream(clientToServerW, serverToClientR)
	serverStream := newStdioStream(serverToClientW, clientToServerR)

	server := &fakeACPServer{sessionID: "sess-xyz", onPrompt: onPrompt}
	serverConn := jsonrpc2.NewConn(ctx, serverStream, server)
	defer serverConn.Close()

	state := newACPState("run-acp", sink, policy)
	clientConn := jsonrpc2.NewConn(ctx, clientStream, &acpHandler{state: state})
	state.conn = clientConn
	defer clientConn.Close()

	var initResult json.RawMessage
	if err := clientConn.Call(ctx, acpInitialize, map[string]any{
		"protocolVersion":    acpProtocolVersion,
		"clientCapabilities": map[string]any{"fs": map[string]any{"readTextFile": false, "writeTextFile": false}},
	}, &initResult); err != nil {
		t.Fatalf("initialize: %v", err)
	}

	req := agentadaptor.DriverRunRequest{RunID: "run-acp"}
	prep := runPrep{effectiveCWD: t.TempDir(), prompt: "do the thing", reportedModel: "claude-sonnet-5"}
	sessionID, err := establishACPSession(ctx, clientConn, req, prep)
	if err != nil {
		t.Fatalf("establish session: %v", err)
	}
	if sessionID != "sess-xyz" {
		t.Fatalf("session id = %q, want sess-xyz", sessionID)
	}
	state.setSession(sessionID)

	var promptResult struct {
		StopReason string `json:"stopReason"`
	}
	promptErr := clientConn.Call(ctx, acpSessionPrompt, map[string]any{
		"sessionId": sessionID,
		"prompt":    []map[string]any{{"type": "text", "text": prep.prompt}},
	}, &promptResult)

	checkpoint := &agentadaptor.DriverCheckpoint{
		State: &agentadaptor.DriverSessionState{ResumeID: sessionID, DisplayID: sessionID},
		Valid: true,
	}
	return state.result(promptResult.StopReason, checkpoint, prep.reportedModel, ""), promptErr
}

// permissionPromptScript returns an onPrompt func that streams one text chunk
// then raises a session/request_permission with allow_once / reject_once
// options, capturing the outcome the client returns.
func permissionPromptScript(t *testing.T, toolCall map[string]any, outcomeOut *map[string]any) func(context.Context, *jsonrpc2.Conn) {
	t.Helper()
	return func(ctx context.Context, conn *jsonrpc2.Conn) {
		_ = conn.Notify(ctx, acpSessionUpdate, map[string]any{
			"sessionId": "sess-xyz",
			"update": map[string]any{
				"sessionUpdate": "agent_message_chunk",
				"content":       map[string]any{"type": "text", "text": "working"},
			},
		})
		params := map[string]any{
			"sessionId": "sess-xyz",
			"toolCall":  toolCall,
			"options": []map[string]any{
				{"optionId": "opt-allow", "name": "Allow", "kind": "allow_once"},
				{"optionId": "opt-reject", "name": "Reject", "kind": "reject_once"},
			},
		}
		var outcome map[string]any
		if err := conn.Call(ctx, acpRequestPermission, params, &outcome); err != nil {
			t.Errorf("request_permission call: %v", err)
			return
		}
		*outcomeOut = outcome
	}
}

func outcomeSelection(t *testing.T, outcome map[string]any) (string, string) {
	t.Helper()
	inner, ok := outcome["outcome"].(map[string]any)
	if !ok {
		t.Fatalf("outcome envelope missing: %#v", outcome)
	}
	kind, _ := inner["outcome"].(string)
	optionID, _ := inner["optionId"].(string)
	return kind, optionID
}

func TestACPEngineAskApprove(t *testing.T) {
	sink := &fakeDecisionSink{respond: func(req agentadaptor.DecisionRequest) agentadaptor.DecisionResponse {
		return agentadaptor.DecisionResponse{Result: agentadaptor.DecisionApproved}
	}}
	var outcome map[string]any
	res, err := acpExchange(t,
		agentadaptor.HumanDecisionPolicy{Permission: agentadaptor.HumanDecisionAsk},
		sink,
		permissionPromptScript(t, map[string]any{"toolCallId": "tc-1", "title": "Bash", "kind": "execute", "rawInput": map[string]any{"command": "ls"}}, &outcome),
	)
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}

	kind, optionID := outcomeSelection(t, outcome)
	if kind != "selected" || optionID != "opt-allow" {
		t.Fatalf("approve outcome = %q/%q, want selected/opt-allow", kind, optionID)
	}
	if len(sink.requests) != 1 {
		t.Fatalf("expected 1 decision request, got %d", len(sink.requests))
	}
	if sink.requests[0].Kind != agentadaptor.HumanDecisionPermission {
		t.Errorf("decision kind = %v, want Permission", sink.requests[0].Kind)
	}
	kinds := sink.streamKinds()
	if kinds[agentadaptor.StreamHITLRequested] != 1 || kinds[agentadaptor.StreamHITLResolved] != 1 {
		t.Errorf("expected one HITL requested+resolved pair, got %v", kinds)
	}
	if kinds[agentadaptor.StreamTextContent] < 1 {
		t.Errorf("expected the agent_message_chunk to surface as StreamTextContent, got %v", kinds)
	}
	if res.Output != "working" {
		t.Errorf("assembled output = %q, want working", res.Output)
	}
	if res.Failure != nil {
		t.Errorf("approve run must not record a failure, got %#v", res.Failure)
	}
}

func TestACPEngineAskRejectRecordsFailure(t *testing.T) {
	sink := &fakeDecisionSink{respond: func(req agentadaptor.DecisionRequest) agentadaptor.DecisionResponse {
		return agentadaptor.DecisionResponse{Result: agentadaptor.DecisionRejected}
	}}
	var outcome map[string]any
	res, err := acpExchange(t,
		agentadaptor.HumanDecisionPolicy{Permission: agentadaptor.HumanDecisionAsk},
		sink,
		permissionPromptScript(t, map[string]any{"toolCallId": "tc-2", "title": "Write", "kind": "edit"}, &outcome),
	)
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}

	kind, optionID := outcomeSelection(t, outcome)
	if kind != "selected" || optionID != "opt-reject" {
		t.Fatalf("reject outcome = %q/%q, want selected/opt-reject", kind, optionID)
	}
	if res.Failure == nil || res.Failure.Code != agentadaptor.FailureReject {
		t.Fatalf("expected FailureReject, got %#v", res.Failure)
	}
	if res.ExitCode == 0 {
		t.Errorf("rejected run should report non-zero exit code")
	}
}

func TestACPEngineAutoRejectWithoutHost(t *testing.T) {
	// A plain sink (not DecisionCapable) plus an AutoReject policy must
	// resolve the permission locally without asking the host.
	sink := &fakeDecisionSink{respond: func(req agentadaptor.DecisionRequest) agentadaptor.DecisionResponse {
		t.Errorf("AutoReject must not consult the host")
		return agentadaptor.DecisionResponse{Result: agentadaptor.DecisionRejected}
	}}
	var outcome map[string]any
	_, err := acpExchange(t,
		agentadaptor.HumanDecisionPolicy{Permission: agentadaptor.HumanDecisionAutoReject},
		sink,
		permissionPromptScript(t, map[string]any{"toolCallId": "tc-3", "title": "Bash", "kind": "execute"}, &outcome),
	)
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	kind, optionID := outcomeSelection(t, outcome)
	if kind != "selected" || optionID != "opt-reject" {
		t.Fatalf("auto-reject outcome = %q/%q, want selected/opt-reject", kind, optionID)
	}
	if len(sink.requests) != 0 {
		t.Fatalf("AutoReject should not emit host decision requests, got %d", len(sink.requests))
	}
}

func TestACPEnginePlanReviewKind(t *testing.T) {
	sink := &fakeDecisionSink{respond: func(req agentadaptor.DecisionRequest) agentadaptor.DecisionResponse {
		return agentadaptor.DecisionResponse{Result: agentadaptor.DecisionApproved}
	}}
	var outcome map[string]any
	_, err := acpExchange(t,
		agentadaptor.HumanDecisionPolicy{PlanReview: agentadaptor.HumanDecisionAsk},
		sink,
		permissionPromptScript(t, map[string]any{"toolCallId": "tc-4", "title": "ExitPlanMode", "kind": "plan"}, &outcome),
	)
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if len(sink.requests) != 1 || sink.requests[0].Kind != agentadaptor.HumanDecisionPlanReview {
		t.Fatalf("expected a PlanReview decision, got %#v", sink.requests)
	}
	kind, optionID := outcomeSelection(t, outcome)
	if kind != "selected" || optionID != "opt-allow" {
		t.Fatalf("plan approve outcome = %q/%q", kind, optionID)
	}
}

func TestACPEngineDeclinesUnknownServerRequest(t *testing.T) {
	// The client advertises no fs/terminal capabilities; any such
	// server-initiated request must be answered with method-not-found.
	sink := &fakeDecisionSink{respond: func(agentadaptor.DecisionRequest) agentadaptor.DecisionResponse {
		return agentadaptor.DecisionResponse{Result: agentadaptor.DecisionApproved}
	}}
	var callErr error
	_, err := acpExchange(t,
		agentadaptor.HumanDecisionPolicy{},
		sink,
		func(ctx context.Context, conn *jsonrpc2.Conn) {
			var res map[string]any
			callErr = conn.Call(ctx, "fs/readTextFile", map[string]any{"path": "/etc/hosts"}, &res)
		},
	)
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	var rpcErr *jsonrpc2.Error
	if callErr == nil {
		t.Fatalf("expected fs/readTextFile to be declined")
	}
	if !jsonrpcErrorIs(callErr, &rpcErr) || rpcErr.Code != jsonrpc2.CodeMethodNotFound {
		t.Fatalf("expected CodeMethodNotFound, got %v", callErr)
	}
}

func jsonrpcErrorIs(err error, target **jsonrpc2.Error) bool {
	if e, ok := err.(*jsonrpc2.Error); ok {
		*target = e
		return true
	}
	return false
}
