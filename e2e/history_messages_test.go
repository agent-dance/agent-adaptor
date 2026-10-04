package e2e_test

import (
	"context"
	"encoding/json"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/bridges/agui"
	"github.com/agent-dance/agent-adaptor/hosttools/sessionrecorder"
)

// A host restarts after recording two runs, serves a bounded user-turn window,
// then restores the latest run for its UI. This crosses the public AG-UI input,
// typed Event, durable recorder, query and AG-UI output boundaries together.
func TestHistoryMessagesSurviveRestartAndBoundedReplay(t *testing.T) {
	ctx := context.Background()
	const session = "conversation"
	const run = "run-current"
	const userID = "trusted-user/甲"
	dir := t.TempDir()
	backend, err := sessionrecorder.NewJSONLEventBackend(dir)
	if err != nil {
		t.Fatal(err)
	}
	recorder := sessionrecorder.NewEventRecorder(backend)
	record := func(runID string, event adaptor.Event) {
		t.Helper()
		meta := event.Meta()
		meta.RunID = runID
		if _, err := recorder.Record(ctx, session, adaptor.WithEventMeta(event, meta)); err != nil {
			t.Fatal(err)
		}
	}
	// An older run must never be pulled into the current run's aligned tail.
	record("run-old", adaptor.RunStarted{})                                    // 1
	record("run-old", adaptor.TextDelta{MessageID: "old", Text: "old answer"}) // 2
	record("run-old", adaptor.RunFinished{})                                   // 3
	record(run, adaptor.RunStarted{})                                          // 4
	input := agui.RunAgentInput{ThreadID: session, Messages: []agui.Message{{
		ID: "question", Role: "user", Name: "untrusted-display-name", Content: json.RawMessage(`"Explain this picture"`),
	}}}
	for _, event := range input.UserTurnEventsWithUserID(run, userID) {
		record(run, event) // 5, 6, 7: user start/content/end
	}
	record(run, adaptor.ImageContent{ // 8
		MessageID: "question", Type: "binary", MIMEType: "image/png",
		URL: "/attachments/question.png", Filename: "问题.png", Role: adaptor.RoleUser, UserID: userID,
	})
	record(run, adaptor.TextDelta{MessageID: "answer", Phase: adaptor.PhaseStart})       // 9
	record(run, adaptor.TextDelta{MessageID: "answer", Text: "Here is the explanation"}) // 10
	record(run, adaptor.TextDelta{MessageID: "answer", Phase: adaptor.PhaseEnd})         // 11
	record(run, adaptor.ImageContent{                                                    // 12: zero role is assistant, with no asking-user attribution
		MessageID: "answer", Type: "binary", MIMEType: "image/svg+xml", URL: "/attachments/answer.svg",
	})
	record(run, adaptor.RunFinished{}) // 13
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}

	backend, err = sessionrecorder.NewJSONLEventBackend(dir)
	if err != nil {
		t.Fatal(err)
	}
	recorder = sessionrecorder.NewEventRecorder(backend)
	t.Cleanup(func() {
		if err := recorder.Close(); err != nil {
			t.Error(err)
		}
	})

	// The client resumes strictly after 5, capped at 8. Later records already
	// exist on disk, but may not leak into this page. The start was trimmed, so
	// each remaining user record must retain its own trustworthy attribution.
	page, err := sessionrecorder.Range(ctx, recorder, session, 5, 8)
	if err != nil {
		t.Fatal(err)
	}
	assertHistoryMessageCursors(t, page, 6, 8)
	for _, r := range page {
		switch event := r.Event.(type) {
		case adaptor.TextDelta:
			if event.MessageID != "question" || event.Role != adaptor.RoleUser || event.UserID != userID {
				t.Fatalf("trimmed text lost attribution: %+v", event)
			}
		case adaptor.ImageContent:
			if event.MessageID != "question" || event.UserID != userID || event.URL != "/attachments/question.png" || event.Filename != "问题.png" {
				t.Fatalf("image changed across JSONL reload: %+v", event)
			}
		default:
			t.Fatalf("bounded user page contains unexpected %T", r.Event)
		}
	}
	pageWire := replayHistoryMessageFrames(t, page)
	assertHistoryMessageRole(t, pageWire, "question", "user")
	assertHistoryImageFrame(t, pageWire, "question", "user", userID, "/attachments/question.png", "image/png")

	// Only the lower bound may expand: a user image restores its own message's
	// text start, after the run start. The page still ends at HostSeq 8.
	alignedPage, err := sessionrecorder.RangeFromRunStart(ctx, recorder, session, 7, 8)
	if err != nil {
		t.Fatal(err)
	}
	assertHistoryMessageCursors(t, alignedPage, 5, 8)

	// A reconnect asks for the latest two rows (assistant image + terminal).
	// Run alignment must restore exactly this run, including the user triple
	// and both complete images, without fabricating an older run's boundary.
	tail, err := sessionrecorder.TailFromRunStart(ctx, recorder, session, 2)
	if err != nil {
		t.Fatal(err)
	}
	assertHistoryMessageCursors(t, tail, 4, 13)
	for _, r := range tail {
		if r.Event.Meta().RunID != run {
			t.Fatalf("aligned tail crossed into another run: %+v", r.Event.Meta())
		}
	}
	wire := replayHistoryMessageFrames(t, tail)
	assertHistoryMessageRole(t, wire, "question", "user")
	assertHistoryMessageRole(t, wire, "answer", "assistant")
	assertHistoryImageFrame(t, wire, "question", "user", userID, "/attachments/question.png", "image/png")
	assertHistoryImageFrame(t, wire, "answer", "assistant", "", "/attachments/answer.svg", "image/svg+xml")
}

func assertHistoryMessageCursors(t *testing.T, records []sessionrecorder.EventRecord, first, last sessionrecorder.HostSeq) {
	t.Helper()
	if len(records) != int(last-first+1) {
		t.Fatalf("history returned %d records, want cursors %d..%d", len(records), first, last)
	}
	for i, record := range records {
		if record.HostSeq != first+sessionrecorder.HostSeq(i) {
			t.Fatalf("history cursor %d = %d", i, record.HostSeq)
		}
	}
}

func replayHistoryMessageFrames(t *testing.T, records []sessionrecorder.EventRecord) []map[string]any {
	t.Helper()
	translator := agui.NewEventTranslator()
	events := translator.Translate(nil)
	for _, record := range records {
		events = append(events, translator.Translate(record.Event)...)
	}
	// Replaying history has no live Stream.Result; the host closes its
	// observational projection after the requested records have been consumed.
	events = append(events, translator.CloseRun(nil)...)
	if err := agui.VerifySequence(events); err != nil {
		t.Fatalf("restored history produced invalid AG-UI: %v", err)
	}
	var frames []map[string]any
	for _, event := range events {
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		var frame map[string]any
		if err := json.Unmarshal(data, &frame); err != nil {
			t.Fatal(err)
		}
		frames = append(frames, frame)
	}
	return frames
}

func assertHistoryMessageRole(t *testing.T, frames []map[string]any, messageID, role string) {
	t.Helper()
	var count int
	for _, frame := range frames {
		if frame["type"] == "TEXT_MESSAGE_START" && frame["messageId"] == messageID {
			count++
			if frame["role"] != role {
				t.Fatalf("message %s role = %v, want %s", messageID, frame["role"], role)
			}
		}
	}
	if count != 1 {
		t.Fatalf("message %s has %d AG-UI starts", messageID, count)
	}
}

func assertHistoryImageFrame(t *testing.T, frames []map[string]any, messageID, role, userID, url, mediaType string) {
	t.Helper()
	var count int
	for _, frame := range frames {
		if frame["type"] != "CUSTOM" || frame["name"] != "image.content" {
			continue
		}
		value := frame["value"].(map[string]any)
		if value["message_id"] != messageID {
			continue
		}
		count++
		image := value["image"].(map[string]any)
		if value["role"] != role || image["url"] != url || image["mime_type"] != mediaType || image["type"] != "binary" {
			t.Fatalf("image wire lost content or role: %v", value)
		}
		if userID == "" {
			if _, exists := value["user_id"]; exists {
				t.Fatal("assistant image was assigned an asking-user identity")
			}
		} else if value["user_id"] != userID {
			t.Fatalf("image user identity = %v, want %q", value["user_id"], userID)
		}
	}
	if count != 1 {
		t.Fatalf("message %s has %d image frames", messageID, count)
	}
}

func TestHistoryUserImageReplayWithoutRunStarted(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	open := func() sessionrecorder.EventRecorder {
		t.Helper()
		backend, err := sessionrecorder.NewJSONLEventBackend(dir)
		if err != nil {
			t.Fatal(err)
		}
		return sessionrecorder.NewEventRecorder(backend)
	}
	recorder := open()
	const session = "host-message-history"
	// A host can record user messages before execution starts, so these two
	// durable turns deliberately have no RunStarted. Reusing a client message
	// ID in a later run must not recover the previous user's attribution.
	input := agui.RunAgentInput{Messages: []agui.Message{{ID: "reused", Role: "user", Content: json.RawMessage(`"Describe my attachment"`)}}}
	for _, run := range []string{"earlier", "latest"} {
		events := input.UserTurnEventsWithUserID(run, "user-"+run)
		events = append(events, adaptor.WithEventMeta(adaptor.ImageContent{
			MessageID: "reused", Type: "binary", MIMEType: "image/png", URL: "/" + run + ".png",
			Role: adaptor.RoleUser, UserID: "user-" + run,
		}, adaptor.EventMeta{RunID: run}))
		for _, event := range events {
			if _, err := recorder.Record(ctx, session, event); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
	recorder = open()
	t.Cleanup(func() {
		if err := recorder.Close(); err != nil {
			t.Error(err)
		}
	})
	page, err := sessionrecorder.TailFromRunStart(ctx, recorder, session, 1)
	if err != nil {
		t.Fatal(err)
	}
	assertHistoryMessageCursors(t, page, 5, 8)
	for _, record := range page {
		if record.Event.Meta().RunID != "latest" {
			t.Fatal("image replay crossed to an earlier run")
		}
		switch event := record.Event.(type) {
		case adaptor.TextDelta:
			if event.UserID != "user-latest" {
				t.Fatal("recovered text belongs to the wrong asking user")
			}
		case adaptor.ImageContent:
			if event.UserID != "user-latest" {
				t.Fatal("image lost asking-user attribution")
			}
		default:
			t.Fatalf("replay synthesized an unrecorded %T", event)
		}
	}
	wire := replayHistoryMessageFrames(t, page)
	assertHistoryMessageRole(t, wire, "reused", "user")
	assertHistoryImageFrame(t, wire, "reused", "user", "user-latest", "/latest.png", "image/png")
}
