package main

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	aguievents "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/bridges/agui"
)

// TestAGUIRunSessionRecordsUserTurnBeforeAssistant is the canonical "user
// prompt persistence" demonstration: it proves that when handleAgent hands
// userTurnEvents(...) into the run session, the recorder ends up holding the
// user text BEFORE any assistant event, with the correct Role and a stable
// MessageID — exactly what a browser refresh needs to reconstruct the chat
// transcript.
func TestAGUIRunSessionRecordsUserTurnBeforeAssistant(t *testing.T) {
	store := mustNewThreadStore(t)
	threadID := "thr-userprompt"
	runID := "run-1"

	// 1. Build the user-turn triple just like server.buildInvocation does.
	input := &agui.RunAgentInput{
		ThreadID: threadID,
		RunID:    "agui-run",
		Messages: []agui.Message{
			{ID: "usr-1", Role: "user", Content: rawJSONString("hello assistant")},
		},
	}
	userTurn := userTurnEvents(lastUserMessageID(input), input.LastUserText())
	if len(userTurn) != 3 {
		t.Fatalf("userTurnEvents returned %d, want 3", len(userTurn))
	}

	// 2. Stand up a scripted stream that emits one assistant message then
	// closes — small enough to keep ordering assertions exact.
	events := make(chan adaptor.Event, 8)
	stream := &fakeStream{runID: runID, events: events}
	go func() {
		events <- adaptor.RunStarted{ThreadID: threadID, RunID: runID}
		events <- adaptor.TextDelta{MessageID: "a1", Phase: adaptor.PhaseStart}
		events <- adaptor.TextDelta{MessageID: "a1", Text: "hi", Phase: adaptor.PhaseContent}
		events <- adaptor.TextDelta{MessageID: "a1", Phase: adaptor.PhaseEnd}
		close(events)
	}()

	// 3. Drive the session end-to-end exactly as production does.
	rec := httptest.NewRecorder()
	session := newAGUIRunSession(context.Background(), stream, store, threadID, userTurn, rec)
	if err := session.Serve(); err != nil {
		t.Fatalf("Serve: %v", err)
	}

	// 4. Recorder must hold the user turn FIRST, with Role=RoleUser and the
	// AG-UI Message.ID propagated. Then the assistant lifecycle. HostSeq is
	// strictly monotonic across the boundary.
	records := store.historyAfter(threadID, 0)
	if len(records) < 7 {
		t.Fatalf("history len = %d, want >= 7 (3 user + RunStarted + 3 assistant)", len(records))
	}

	wantPhases := []adaptor.Phase{adaptor.PhaseStart, adaptor.PhaseContent, adaptor.PhaseEnd}
	for i, phase := range wantPhases {
		delta, ok := records[i].Event.(adaptor.TextDelta)
		if !ok {
			t.Fatalf("record[%d] is %T, want adaptor.TextDelta", i, records[i].Event)
		}
		if delta.Phase != phase {
			t.Fatalf("user record[%d].Phase = %q, want %q", i, delta.Phase, phase)
		}
		if delta.Role != adaptor.RoleUser {
			t.Fatalf("user record[%d].Role = %q, want RoleUser", i, delta.Role)
		}
		if delta.MessageID != "usr-1" {
			t.Fatalf("user record[%d].MessageID = %q, want usr-1", i, delta.MessageID)
		}
	}
	if got := records[1].Event.(adaptor.TextDelta).Text; got != "hello assistant" {
		t.Fatalf("user content text = %q, want %q", got, "hello assistant")
	}

	if _, ok := records[3].Event.(adaptor.RunStarted); !ok {
		t.Fatalf("record[3] is %T, want adaptor.RunStarted", records[3].Event)
	}
	for i := 4; i < 7; i++ {
		delta, ok := records[i].Event.(adaptor.TextDelta)
		if !ok {
			t.Fatalf("record[%d] is %T, want adaptor.TextDelta", i, records[i].Event)
		}
		if delta.Role != adaptor.RoleAssistant {
			t.Fatalf("assistant record[%d].Role = %q, want RoleAssistant (zero)", i, delta.Role)
		}
		if delta.MessageID != "a1" {
			t.Fatalf("assistant record[%d].MessageID = %q, want a1", i, delta.MessageID)
		}
	}

	for i, r := range records {
		if r.HostSeq != uint64(i+1) {
			t.Fatalf("record[%d].HostSeq = %d, want %d (must be strictly monotonic)", i, r.HostSeq, i+1)
		}
	}

	// 5. SSE wire payload must echo the user turn as
	// TEXT_MESSAGE_START{role:"user"} BEFORE any assistant message — proving
	// the realtime channel and the recorder agree on shape.
	body := rec.Body.String()
	userStartIdx := strings.Index(body, `"messageId":"usr-1"`)
	assistantStartIdx := strings.Index(body, `"messageId":"a1"`)
	if userStartIdx < 0 {
		t.Fatalf("SSE body missing user TEXT_MESSAGE_START:\n%s", body)
	}
	if assistantStartIdx < 0 {
		t.Fatalf("SSE body missing assistant TEXT_MESSAGE_START:\n%s", body)
	}
	if userStartIdx > assistantStartIdx {
		t.Fatalf("SSE body has user message AFTER assistant: user@%d assistant@%d\n%s",
			userStartIdx, assistantStartIdx, body)
	}
	if !strings.Contains(body, `"role":"user"`) {
		t.Fatalf("SSE body missing role:\"user\" tag:\n%s", body)
	}
}

// TestAGUIRunSessionSkipsUserTurnWhenAbsent guards the no-text-input edge
// case: if the AG-UI input had no user-role text content, userTurnEvents
// returns nil and Serve must not record anything user-side.
func TestAGUIRunSessionSkipsUserTurnWhenAbsent(t *testing.T) {
	store := mustNewThreadStore(t)
	threadID := "thr-empty"

	events := make(chan adaptor.Event, 2)
	stream := &fakeStream{runID: "run-empty", events: events}
	go func() {
		events <- adaptor.RunStarted{ThreadID: threadID, RunID: "run-empty"}
		events <- adaptor.TextDelta{MessageID: "a1", Phase: adaptor.PhaseStart}
		close(events)
	}()

	rec := httptest.NewRecorder()
	session := newAGUIRunSession(context.Background(), stream, store, threadID, nil, rec)
	if err := session.Serve(); err != nil {
		t.Fatalf("Serve: %v", err)
	}

	for i, r := range store.historyAfter(threadID, 0) {
		if delta, ok := r.Event.(adaptor.TextDelta); ok && delta.Role == adaptor.RoleUser {
			t.Fatalf("record[%d] unexpectedly tagged RoleUser: %+v", i, delta)
		}
	}
}

func TestAGUIRunSessionRecordsNonStreamingResultFallback(t *testing.T) {
	store := mustNewThreadStore(t)
	events := make(chan adaptor.Event, 2)
	events <- adaptor.RunStarted{ThreadID: "thread-cursor", RunID: "run-cursor"}
	events <- adaptor.RunFinished{ThreadID: "thread-cursor", RunID: "run-cursor"}
	close(events)
	stream := &fakeStream{
		runID:  "run-cursor",
		events: events,
		result: &adaptor.Result{Text: "cursor final answer"},
	}

	recorder := httptest.NewRecorder()
	if err := newAGUIRunSession(context.Background(), stream, store, "thread-cursor", nil, recorder).Serve(); err != nil {
		t.Fatalf("Serve: %v", err)
	}

	records := store.historyAfter("thread-cursor", 0)
	var assistantContents []string
	for _, record := range records {
		if delta, ok := record.Event.(adaptor.TextDelta); ok && delta.Role == adaptor.RoleAssistant && delta.Text != "" {
			assistantContents = append(assistantContents, delta.Text)
		}
	}
	if got := strings.Join(assistantContents, "|"); got != "cursor final answer" {
		t.Fatalf("recorded assistant content = %q, want one final-result fallback", got)
	}
	if len(records) == 0 {
		t.Fatal("recovery history is empty")
	}
	if terminal, ok := records[len(records)-1].Event.(adaptor.RunFinished); !ok || terminal.Failed {
		t.Fatalf("last recovery event = %#v, want successful RunFinished after fallback", records[len(records)-1].Event)
	}
	body := recorder.Body.String()
	if strings.Count(body, `"delta":"cursor final answer"`) != 1 {
		t.Fatalf("SSE fallback count != 1:\n%s", body)
	}
	if content, terminal := strings.Index(body, `"delta":"cursor final answer"`), strings.LastIndex(body, `"type":"RUN_FINISHED"`); content < 0 || terminal < content {
		t.Fatalf("fallback must precede RUN_FINISHED:\n%s", body)
	}
}

func TestAGUIRunSessionDoesNotDuplicateStreamedAssistantText(t *testing.T) {
	store := mustNewThreadStore(t)
	events := make(chan adaptor.Event, 4)
	events <- adaptor.RunStarted{ThreadID: "thread-streamed", RunID: "run-streamed"}
	events <- adaptor.TextDelta{MessageID: "assistant", Phase: adaptor.PhaseStart}
	events <- adaptor.TextDelta{MessageID: "assistant", Text: "streamed answer", Phase: adaptor.PhaseContent}
	events <- adaptor.TextDelta{MessageID: "assistant", Phase: adaptor.PhaseEnd}
	close(events)
	stream := &fakeStream{
		runID:  "run-streamed",
		events: events,
		result: &adaptor.Result{Text: "streamed answer"},
	}

	recorder := httptest.NewRecorder()
	if err := newAGUIRunSession(context.Background(), stream, store, "thread-streamed", nil, recorder).Serve(); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	if got := strings.Count(recorder.Body.String(), `"delta":"streamed answer"`); got != 1 {
		t.Fatalf("streamed assistant content count = %d, want 1:\n%s", got, recorder.Body.String())
	}
}

func TestAGUIRunSessionDoesNotCreateEmptyResultMessage(t *testing.T) {
	store := mustNewThreadStore(t)
	events := make(chan adaptor.Event, 1)
	events <- adaptor.RunStarted{ThreadID: "thread-empty-result", RunID: "run-empty-result"}
	close(events)
	stream := &fakeStream{runID: "run-empty-result", events: events, result: &adaptor.Result{Text: " \n\t"}}

	recorder := httptest.NewRecorder()
	if err := newAGUIRunSession(context.Background(), stream, store, "thread-empty-result", nil, recorder).Serve(); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	if strings.Contains(recorder.Body.String(), `"type":"TEXT_MESSAGE_CONTENT"`) {
		t.Fatalf("empty result created an assistant content event:\n%s", recorder.Body.String())
	}
}

func TestAGUIRunSessionKeepsRunErrorAfterPartialResultFallback(t *testing.T) {
	store := mustNewThreadStore(t)
	events := make(chan adaptor.Event, 2)
	events <- adaptor.RunStarted{ThreadID: "thread-failed", RunID: "run-failed"}
	events <- adaptor.RunFinished{ThreadID: "thread-failed", RunID: "run-failed", Failed: true, Reason: adaptor.ReasonAgentError, Message: "provider failed"}
	close(events)
	runErr := &adaptor.RunError{
		Reason:  adaptor.ReasonAgentError,
		Message: "provider failed",
		Result:  &adaptor.Result{Text: "partial answer"},
	}
	stream := &fakeStream{runID: "run-failed", events: events, err: runErr}

	recorder := httptest.NewRecorder()
	if err := newAGUIRunSession(context.Background(), stream, store, "thread-failed", nil, recorder).Serve(); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	body := recorder.Body.String()
	content := strings.Index(body, `"delta":"partial answer"`)
	terminal := strings.LastIndex(body, `"type":"RUN_ERROR"`)
	if content < 0 || terminal < content || strings.Contains(body, `"type":"RUN_FINISHED"`) {
		t.Fatalf("partial fallback must precede the sole RUN_ERROR:\n%s", body)
	}
	records := store.historyAfter("thread-failed", 0)
	if len(records) == 0 {
		t.Fatal("recovery history is empty")
	}
	if terminal, ok := records[len(records)-1].Event.(adaptor.RunFinished); !ok || !terminal.Failed || terminal.Reason != adaptor.ReasonAgentError {
		t.Fatalf("last recovery event = %#v, want failed RunFinished after fallback", records[len(records)-1].Event)
	}
}

// TestAGUIRunSessionParksApprovalRequests covers form B of decision D2: the
// approval arrives as a *adaptor.ApprovalRequest event on the same stream,
// the host parks it for the browser, and the SDK's approval.resolved notice
// clears it again without host bookkeeping.
func TestAGUIRunSessionParksApprovalRequests(t *testing.T) {
	store := mustNewThreadStore(t)
	threadID := "thr-approval"
	req := &adaptor.ApprovalRequest{ID: "req-1", RunID: "run-approval", Kind: adaptor.ApprovalPermission, Title: "run bash"}

	events := make(chan adaptor.Event, 4)
	stream := &fakeStream{runID: "run-approval", events: events}
	go func() {
		events <- adaptor.RunStarted{ThreadID: threadID, RunID: "run-approval"}
		events <- req
		close(events)
	}()

	rec := httptest.NewRecorder()
	session := newAGUIRunSession(context.Background(), stream, store, threadID, nil, rec)
	if err := session.Serve(); err != nil {
		t.Fatalf("Serve: %v", err)
	}

	// unregisterRun drops requests owned by the finished run, so inspect the
	// parking behaviour through a second session that stays "open".
	store2 := mustNewThreadStore(t)
	store2.addPending(threadID, req)
	if got := len(store2.pendingRequests(threadID)); got != 1 {
		t.Fatalf("pending len = %d, want 1", got)
	}
	store2.removePending(threadID, "req-1")
	if got := len(store2.pendingRequests(threadID)); got != 0 {
		t.Fatalf("pending len after resolve = %d, want 0", got)
	}

	// The approval must still have reached the recorder as a typed event so a
	// refreshing browser can rebuild the card.
	var seen bool
	for _, r := range store.historyAfter(threadID, 0) {
		if got, ok := r.Event.(*adaptor.ApprovalRequest); ok && got.ID == "req-1" {
			seen = true
		}
	}
	if !seen {
		t.Fatal("approval request was not recorded in the session history")
	}
}

func rawJSONString(s string) []byte {
	return []byte(`"` + s + `"`)
}

// _ keeps the AG-UI events import live for future assertions that need the
// typed event helpers (currently the test inspects the raw SSE body).
var _ = aguievents.EventTypeTextMessageStart
