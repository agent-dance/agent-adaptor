package main

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	aguievents "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/pkg/bridges/agui"
)

// TestAGUIRunSessionRecordsUserTurnBeforeAssistantWithoutEcho is the canonical
// "user prompt persistence" demonstration: it proves that when handleAgent
// hands UserTurnPayloads(handle.RunID()) into the run session, the
// recorder ends up holding the user text BEFORE any assistant event,
// with the correct Role and a stable MessageID. CopilotKit already renders the
// submitted user message optimistically, so the backend must not echo that turn
// over SSE and duplicate the visible chat bubble.
func TestAGUIRunSessionRecordsUserTurnBeforeAssistantWithoutEcho(t *testing.T) {
	store := newThreadStore()
	threadID := "thr-userprompt"
	runID := "run-1"

	// 1. Build the user-turn triple just like server.handleAgent does.
	input := &agui.RunAgentInput{
		ThreadID: threadID,
		RunID:    "agui-run",
		Messages: []agui.Message{
			{ID: "usr-1", Role: "user", Content: rawJSONString("hello assistant")},
		},
	}
	userTurn := input.UserTurnPayloads(runID)
	if len(userTurn) != 3 {
		t.Fatalf("UserTurnPayloads returned %d, want 3", len(userTurn))
	}

	// 2. Stand up a fake RunHandle that emits two assistant events then
	// closes — small enough to keep ordering assertions exact.
	streamCh := make(chan agentadaptor.StreamPayload, 4)
	handle := &fakeRunHandle{
		runID:    runID,
		streamCh: streamCh,
	}
	go func() {
		streamCh <- agentadaptor.StreamPayload{
			Kind: agentadaptor.StreamTextStart, MessageID: "a1", ThreadID: threadID, RunID: runID,
		}
		streamCh <- agentadaptor.StreamPayload{
			Kind: agentadaptor.StreamTextContent, MessageID: "a1", Delta: "hi", ThreadID: threadID, RunID: runID,
		}
		streamCh <- agentadaptor.StreamPayload{
			Kind: agentadaptor.StreamTextEnd, MessageID: "a1", ThreadID: threadID, RunID: runID,
		}
		close(streamCh)
	}()

	// 3. Drive the session end-to-end exactly as production does.
	rec := httptest.NewRecorder()
	session := newAGUIRunSession(context.Background(), handle, store, threadID, userTurn, rec)
	if err := session.Serve(); err != nil {
		t.Fatalf("Serve: %v", err)
	}

	// 4. Recorder must hold user turn FIRST, with Role=RoleUser and the
	// AG-UI Message.ID propagated. Then the assistant lifecycle. HostSeq
	// is strictly monotonic across the boundary.
	records := store.historyAfter(threadID, 0)
	if len(records) < 6 {
		t.Fatalf("history len = %d, want >= 6 (3 user + 3 assistant)", len(records))
	}

	wantKinds := []agentadaptor.StreamKind{
		agentadaptor.StreamTextStart,
		agentadaptor.StreamTextContent,
		agentadaptor.StreamTextEnd,
		agentadaptor.StreamTextStart,
		agentadaptor.StreamTextContent,
		agentadaptor.StreamTextEnd,
	}
	for i, k := range wantKinds {
		if records[i].Payload.Kind != k {
			t.Fatalf("record[%d].Kind = %q, want %q", i, records[i].Payload.Kind, k)
		}
	}

	for i := 0; i < 3; i++ {
		p := records[i].Payload
		if p.Role != agentadaptor.RoleUser {
			t.Fatalf("user record[%d].Role = %q, want RoleUser", i, p.Role)
		}
		if p.MessageID != "usr-1" {
			t.Fatalf("user record[%d].MessageID = %q, want usr-1", i, p.MessageID)
		}
	}
	if got := records[1].Payload.Delta; got != "hello assistant" {
		t.Fatalf("user content delta = %q, want %q", got, "hello assistant")
	}

	for i := 3; i < 6; i++ {
		p := records[i].Payload
		if p.Role != agentadaptor.RoleAssistant {
			t.Fatalf("assistant record[%d].Role = %q, want RoleAssistant (zero)", i, p.Role)
		}
		if p.MessageID != "a1" {
			t.Fatalf("assistant record[%d].MessageID = %q, want a1", i, p.MessageID)
		}
	}

	for i, r := range records {
		if r.HostSeq != uint64(i+1) {
			t.Fatalf("record[%d].HostSeq = %d, want %d (must be strictly monotonic)", i, r.HostSeq, i+1)
		}
	}

	// 5. The recorder owns the synthetic user turn, while SSE begins with
	// assistant output because CopilotKit already rendered the submitted user
	// message locally.
	body := rec.Body.String()
	userStartIdx := strings.Index(body, `"messageId":"usr-1"`)
	assistantStartIdx := strings.Index(body, `"messageId":"a1"`)
	if userStartIdx >= 0 || strings.Contains(body, `"role":"user"`) {
		t.Fatalf("SSE body unexpectedly echoed the recorder-only user turn:\n%s", body)
	}
	if assistantStartIdx < 0 {
		t.Fatalf("SSE body missing assistant TEXT_MESSAGE_START:\n%s", body)
	}
}

// TestAGUIRunSessionSkipsUserTurnWhenAbsent guards the no-text-input edge
// case: if the AG-UI input had no user-role text content, UserTurnPayloads
// returns nil and Serve must not record anything user-side.
func TestAGUIRunSessionSkipsUserTurnWhenAbsent(t *testing.T) {
	store := newThreadStore()
	threadID := "thr-empty"
	runID := "run-empty"

	streamCh := make(chan agentadaptor.StreamPayload, 1)
	handle := &fakeRunHandle{runID: runID, streamCh: streamCh}
	go func() {
		streamCh <- agentadaptor.StreamPayload{
			Kind: agentadaptor.StreamTextStart, MessageID: "a1", ThreadID: threadID, RunID: runID,
		}
		close(streamCh)
	}()

	rec := httptest.NewRecorder()
	session := newAGUIRunSession(context.Background(), handle, store, threadID, nil, rec)
	if err := session.Serve(); err != nil {
		t.Fatalf("Serve: %v", err)
	}

	for i, r := range store.historyAfter(threadID, 0) {
		if r.Payload.Role == agentadaptor.RoleUser {
			t.Fatalf("record[%d] unexpectedly tagged RoleUser: %+v", i, r.Payload)
		}
	}
}

func TestAGUIRunSessionReportsWaitError(t *testing.T) {
	streamCh := make(chan agentadaptor.StreamPayload)
	close(streamCh)
	handle := &fakeRunHandle{
		runID:    "run-error",
		streamCh: streamCh,
		waitErr:  errors.New("provider wait failed"),
	}

	rec := httptest.NewRecorder()
	session := newAGUIRunSession(context.Background(), handle, newThreadStore(), "thr-error", nil, rec)
	if err := session.Serve(); err != nil {
		t.Fatalf("Serve: %v", err)
	}

	body := rec.Body.String()
	if !strings.Contains(body, `"type":"RUN_ERROR"`) || !strings.Contains(body, "provider wait failed") {
		t.Fatalf("SSE body must expose the Wait error as RUN_ERROR:\n%s", body)
	}
	if strings.Contains(body, `"type":"RUN_FINISHED"`) {
		t.Fatalf("SSE body must not report RUN_FINISHED before a Wait error:\n%s", body)
	}
}

func TestAGUIRunSessionReportsStructuredFailure(t *testing.T) {
	streamCh := make(chan agentadaptor.StreamPayload)
	close(streamCh)
	handle := &fakeRunHandle{
		runID:    "run-failure",
		streamCh: streamCh,
		waitResult: agentadaptor.RunResult{Failure: &agentadaptor.RunFailure{
			Code:    agentadaptor.FailureReject,
			Message: "host rejected the plan",
		}},
	}

	rec := httptest.NewRecorder()
	session := newAGUIRunSession(context.Background(), handle, newThreadStore(), "thr-failure", nil, rec)
	if err := session.Serve(); err != nil {
		t.Fatalf("Serve: %v", err)
	}

	body := rec.Body.String()
	if !strings.Contains(body, `"type":"RUN_ERROR"`) || !strings.Contains(body, "host rejected the plan") {
		t.Fatalf("SSE body must expose RunResult.Failure as RUN_ERROR:\n%s", body)
	}
	if !strings.Contains(body, `"code":"decision_rejected"`) {
		t.Fatalf("SSE body must preserve the structured failure code:\n%s", body)
	}
}

func rawJSONString(s string) []byte {
	return []byte(`"` + s + `"`)
}

// _ keeps the AG-UI events import live for future assertions that need
// the typed event helpers (currently the test inspects raw SSE body).
var _ = aguievents.EventTypeTextMessageStart
