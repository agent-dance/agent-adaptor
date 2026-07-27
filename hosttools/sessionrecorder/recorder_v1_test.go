package sessionrecorder_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/hosttools/sessionrecorder"
	adaptor "github.com/agent-dance/agent-adaptor/next"
)

func TestEventRecorderAssignsSequentialHostSeqAndSince(t *testing.T) {
	t.Parallel()

	rec := sessionrecorder.NewEventRecorder(sessionrecorder.NewMemoryEventBackend())
	defer rec.Close()
	ctx := context.Background()

	events := []adaptor.Event{
		adaptor.RunStarted{RunID: "r1", ThreadID: "th1"},
		adaptor.TextDelta{MessageID: "m1", Text: "hello", Phase: adaptor.PhaseContent},
		adaptor.RunFinished{RunID: "r1", ThreadID: "th1"},
	}
	for i, ev := range events {
		record, err := rec.Record(ctx, "sess-a", ev)
		if err != nil {
			t.Fatalf("Record %d: %v", i, err)
		}
		if got, want := record.HostSeq, sessionrecorder.HostSeq(i+1); got != want {
			t.Fatalf("HostSeq = %d, want %d", got, want)
		}
	}

	all, err := rec.Since(ctx, "sess-a", 0)
	if err != nil {
		t.Fatalf("Since(0): %v", err)
	}
	if len(all) != len(events) {
		t.Fatalf("Since(0) len = %d, want %d", len(all), len(events))
	}
	for i, r := range all {
		if r.HostSeq != sessionrecorder.HostSeq(i+1) {
			t.Fatalf("Since(0)[%d].HostSeq = %d", i, r.HostSeq)
		}
		if !reflect.DeepEqual(r.Event, events[i]) {
			t.Fatalf("Since(0)[%d].Event = %#v, want %#v", i, r.Event, events[i])
		}
	}

	// Strictly-greater cursor: after 2 only the third record remains.
	tail, err := rec.Since(ctx, "sess-a", 2)
	if err != nil {
		t.Fatalf("Since(2): %v", err)
	}
	if len(tail) != 1 || tail[0].HostSeq != 3 {
		t.Fatalf("Since(2) = %+v, want exactly HostSeq 3", tail)
	}
	empty, err := rec.Since(ctx, "sess-a", 3)
	if err != nil {
		t.Fatalf("Since(3): %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("Since(3) = %+v, want empty", empty)
	}

	// HostSeq is per-session scoped.
	other, err := rec.Record(ctx, "sess-b", adaptor.Dropped{Count: 1})
	if err != nil {
		t.Fatalf("Record sess-b: %v", err)
	}
	if other.HostSeq != 1 {
		t.Fatalf("sess-b HostSeq = %d, want 1", other.HostSeq)
	}
}

func TestEventRecordJSONRoundTripAllKinds(t *testing.T) {
	t.Parallel()

	createdAt := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	deadline := createdAt.Add(90 * time.Second)

	cases := []struct {
		kind  string
		event adaptor.Event
	}{
		{"text.delta", adaptor.TextDelta{MessageID: "m1", Text: "hi", Role: adaptor.RoleUser, Phase: adaptor.PhaseContent}},
		{"thinking", adaptor.Thinking{MessageID: "m2", Text: "pondering", Phase: adaptor.PhaseStart}},
		{"tool.call", adaptor.ToolCall{ID: "t1", Name: "bash", Args: map[string]any{"cmd": "ls"}, Phase: adaptor.PhaseStart}},
		{"tool.result", adaptor.ToolResult{ID: "t1", Result: map[string]any{"text": "ok"}}},
		{"run.started", adaptor.RunStarted{RunID: "r1", ThreadID: "th1"}},
		{"run.finished", adaptor.RunFinished{
			RunID: "r1", ThreadID: "th1",
			Usage:  &adaptor.Usage{InputTokens: 10, OutputTokens: 20},
			Failed: true, Reason: adaptor.ReasonAgentError, Message: "boom",
		}},
		{"process.info", adaptor.ProcessInfo{Kind: "stderr", Bytes: []byte("warn\n"), Metadata: map[string]string{"pid": "42"}}},
		{"notice", adaptor.Notice{Kind: adaptor.NoticeStep, Text: "plan", Data: map[string]any{"phase": "started"}}},
		{"dropped", adaptor.Dropped{Count: 3}},
		{"subagent.update", adaptor.SubagentUpdate{Agent: "researcher", Kind: adaptor.SubagentDelta, Delta: "partial", Data: map[string]any{"seq": "9"}}},
		{"approval.request", &adaptor.ApprovalRequest{
			ID: "q1", RunID: "r1", Kind: adaptor.ApprovalPermission,
			Title: "run rm?", Source: "bash", ToolCallID: "tc1",
			Choices:   []adaptor.Choice{{Key: "yes", Label: "Approve"}, {Key: "no", Label: "Reject"}},
			Details:   map[string]any{"tool": "bash"},
			CreatedAt: createdAt, Deadline: deadline, Attempt: 1,
		}},
	}

	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			t.Parallel()

			original := sessionrecorder.EventRecord{
				HostSeq:    7,
				RecordedAt: createdAt,
				Event:      tc.event,
			}
			raw, err := json.Marshal(original)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var envelope struct {
				HostSeq    sessionrecorder.HostSeq `json:"host_seq"`
				RecordedAt time.Time               `json:"recorded_at"`
				Kind       string                  `json:"kind"`
				Event      json.RawMessage         `json:"event"`
			}
			if err := json.Unmarshal(raw, &envelope); err != nil {
				t.Fatalf("decode envelope: %v", err)
			}
			if envelope.Kind != tc.kind {
				t.Fatalf("wire kind = %q, want %q", envelope.Kind, tc.kind)
			}
			if envelope.HostSeq != 7 || len(envelope.Event) == 0 {
				t.Fatalf("envelope = %+v", envelope)
			}

			var decoded sessionrecorder.EventRecord
			if err := json.Unmarshal(raw, &decoded); err != nil {
				t.Fatalf("unmarshal record: %v", err)
			}
			if decoded.HostSeq != original.HostSeq || !decoded.RecordedAt.Equal(original.RecordedAt) {
				t.Fatalf("decoded header = %+v", decoded)
			}
			if !reflect.DeepEqual(decoded.Event, tc.event) {
				t.Fatalf("decoded event = %#v, want %#v", decoded.Event, tc.event)
			}
		})
	}
}

func TestEventRecordMarshalRejectsNilAndUnknownKinds(t *testing.T) {
	t.Parallel()

	_, err := json.Marshal(sessionrecorder.EventRecord{HostSeq: 1, Event: nil})
	if err == nil || !strings.Contains(err.Error(), "nil event") {
		t.Fatalf("marshal nil event err = %v", err)
	}
	var nilApproval *adaptor.ApprovalRequest
	_, err = json.Marshal(sessionrecorder.EventRecord{HostSeq: 1, Event: nilApproval})
	if err == nil || !strings.Contains(err.Error(), "nil approval request") {
		t.Fatalf("marshal typed nil approval err = %v", err)
	}

	var decoded sessionrecorder.EventRecord
	err = json.Unmarshal([]byte(`{"host_seq":1,"recorded_at":"2026-07-01T12:00:00Z","kind":"bogus","event":{}}`), &decoded)
	if err == nil || !strings.Contains(err.Error(), `unknown event kind "bogus"`) {
		t.Fatalf("unmarshal unknown kind err = %v", err)
	}
	err = json.Unmarshal([]byte(`{"host_seq":1,"recorded_at":"2026-07-01T12:00:00Z","kind":"dropped","meta":{},"event":null}`), &decoded)
	if err == nil || !strings.Contains(err.Error(), "has no event payload") {
		t.Fatalf("unmarshal null event err = %v", err)
	}
}

func TestEventRecorderRefusesInvalidSessionKey(t *testing.T) {
	t.Parallel()

	rec := sessionrecorder.NewEventRecorder(sessionrecorder.NewMemoryEventBackend())
	defer rec.Close()
	ctx := context.Background()

	if _, err := rec.Record(ctx, "../evil", adaptor.Dropped{Count: 1}); !errors.Is(err, sessionrecorder.ErrInvalidSessionKey) {
		t.Fatalf("Record err = %v, want ErrInvalidSessionKey", err)
	}
	if _, err := rec.Since(ctx, "../evil", 0); !errors.Is(err, sessionrecorder.ErrInvalidSessionKey) {
		t.Fatalf("Since err = %v, want ErrInvalidSessionKey", err)
	}
}

func TestEventRecorderSessionsMostRecentFirst(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time {
		now = now.Add(time.Second)
		return now
	}
	rec := sessionrecorder.NewEventRecorder(sessionrecorder.NewMemoryEventBackend(), sessionrecorder.WithEventClock(clock))
	defer rec.Close()
	ctx := context.Background()

	mustRecord := func(key string) {
		t.Helper()
		if _, err := rec.Record(ctx, key, adaptor.TextDelta{MessageID: "m", Text: "x"}); err != nil {
			t.Fatalf("Record %s: %v", key, err)
		}
	}
	mustRecord("s-old")
	mustRecord("s-new")

	sessions, err := rec.Sessions(ctx)
	if err != nil {
		t.Fatalf("Sessions: %v", err)
	}
	if len(sessions) != 2 || sessions[0].Key != "s-new" || sessions[1].Key != "s-old" {
		t.Fatalf("sessions = %+v, want s-new first", sessions)
	}

	// A newer write flips the order and advances LastSeq.
	mustRecord("s-old")
	sessions, err = rec.Sessions(ctx)
	if err != nil {
		t.Fatalf("Sessions: %v", err)
	}
	if len(sessions) != 2 || sessions[0].Key != "s-old" || sessions[0].LastSeq != 2 {
		t.Fatalf("sessions = %+v, want s-old first with LastSeq 2", sessions)
	}
}

func TestNewEventRecorderPanicsOnNilBackend(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("expected panic for nil backend")
		}
	}()
	sessionrecorder.NewEventRecorder(nil)
}
