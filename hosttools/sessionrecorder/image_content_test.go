package sessionrecorder_test

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/bridges/agui"
	"github.com/agent-dance/agent-adaptor/hosttools/sessionrecorder"
)

func TestMessageImagesAndIdentitySurviveJSONLReopen(t *testing.T) {
	dir := t.TempDir()
	backend, err := sessionrecorder.NewJSONLEventBackend(dir)
	if err != nil {
		t.Fatal(err)
	}
	rec := sessionrecorder.NewEventRecorder(backend)
	input := &agui.RunAgentInput{Messages: []agui.Message{{ID: "message", Role: "user", Content: json.RawMessage(`"question"`)}}}
	events := input.UserTurnEventsWithUserID("run", " user/甲 ")
	events = append(events,
		adaptor.WithEventMeta(adaptor.ImageContent{MessageID: "message", Type: "host-image-v2", MIMEType: "image/png", URL: "/one", Filename: "甲.png", Role: adaptor.RoleUser, UserID: " user/甲 "}, adaptor.EventMeta{RunID: "run", Sequence: 4}),
		adaptor.WithEventMeta(adaptor.ImageContent{MessageID: "message", Type: "binary", MIMEType: "image/jpeg", URL: "/two", Role: adaptor.RoleUser, UserID: " user/甲 "}, adaptor.EventMeta{RunID: "run", Sequence: 5}),
		adaptor.TextDelta{MessageID: "assistant", Text: "answer"},
	)
	for i, e := range events {
		meta := e.Meta()
		meta.Time = meta.Time.UTC()
		events[i] = adaptor.WithEventMeta(e, meta)
		if _, err := rec.Record(context.Background(), "conversation", events[i]); err != nil {
			t.Fatal(err)
		}
	}
	if err := rec.Close(); err != nil {
		t.Fatal(err)
	}
	backend, err = sessionrecorder.NewJSONLEventBackend(dir)
	if err != nil {
		t.Fatal(err)
	}
	rec = sessionrecorder.NewEventRecorder(backend)
	defer rec.Close()
	all, err := rec.Since(context.Background(), "conversation", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != len(events)-1 {
		t.Fatalf("history count=%d", len(all))
	}
	for i, r := range all {
		if r.HostSeq != sessionrecorder.HostSeq(i+2) || !reflect.DeepEqual(r.Event, events[i+1]) {
			t.Fatalf("history %d changed: %#v", i, r)
		}
	}
}

func TestMessageIdentityLegacyAndAssistantSnapshots(t *testing.T) {
	legacy := `{"host_seq":1,"recorded_at":"2026-01-01T00:00:00Z","kind":"text.delta","meta":{},"event":{"MessageID":"m","Text":"old","Role":"user","Phase":""}}`
	var record sessionrecorder.EventRecord
	if err := json.Unmarshal([]byte(legacy), &record); err != nil {
		t.Fatal(err)
	}
	if record.Event.(adaptor.TextDelta).UserID != "" {
		t.Fatal("historical identity invented")
	}
	for _, event := range []adaptor.Event{
		adaptor.TextDelta{MessageID: "a", Text: "answer", UserID: "human"},
		adaptor.ImageContent{MessageID: "a", Type: "binary", MIMEType: "image/png", URL: "/x", UserID: "human"},
	} {
		record := sessionrecorder.EventRecord{Event: event}
		raw, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "human") || strings.Contains(string(raw), "UserID") {
			t.Fatalf("assistant attribution persisted: %s", raw)
		}
		var restored sessionrecorder.EventRecord
		if err := json.Unmarshal(raw, &restored); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(restored.Event, adaptor.WithEventMeta(event, event.Meta())) {
			t.Fatal("assistant snapshot changed")
		}
	}
}

func TestInvalidImageCannotEnterRecorderOrAdvanceCursor(t *testing.T) {
	for _, durable := range []bool{false, true} {
		var backend sessionrecorder.EventBackend = sessionrecorder.NewMemoryEventBackend()
		if durable {
			var err error
			backend, err = sessionrecorder.NewJSONLEventBackend(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
		}
		rec := sessionrecorder.NewEventRecorder(backend)
		for _, e := range []adaptor.Event{
			adaptor.ImageContent{},
			adaptor.ImageContent{MessageID: "m", Type: "binary", URL: "/secret", MIMEType: "text/plain"},
			(*adaptor.ImageContent)(nil),
		} {
			if _, err := rec.Record(context.Background(), "s", e); err == nil {
				t.Fatal("invalid image accepted")
			}
		}
		r, err := rec.Record(context.Background(), "s", adaptor.TextDelta{MessageID: "m", Text: "ok"})
		if err != nil || r.HostSeq != 1 {
			t.Fatalf("invalid image consumed cursor: %v %d", err, r.HostSeq)
		}
		if err := rec.Close(); err != nil {
			t.Fatal(err)
		}
	}
	for _, raw := range []string{
		`{"kind":"image.content","meta":{},"event":{}}`,
		`{"kind":"image.content","meta":{},"event":{"MessageID":"m","Type":"binary","MIMEType":"image/png","URL":"/x","Unknown":true}}`,
		`{"kind":"image.content","meta":{},"event":{"MessageID":"m","Type":"binary","MIMEType":"image/png","URL":"/x","URL":"/y"}}`,
	} {
		var record sessionrecorder.EventRecord
		if err := json.Unmarshal([]byte(raw), &record); err == nil {
			t.Fatal("malformed image history accepted")
		}
	}
}

func TestMessageUTF8RejectionIsConsistentAcrossRecorderBoundaries(t *testing.T) {
	image := adaptor.ImageContent{MessageID: "m", Type: "binary", MIMEType: "image/png", URL: "/x", Filename: "x.png", Role: adaptor.RoleUser, UserID: "host"}
	badText := adaptor.TextDelta{MessageID: "m", Role: adaptor.RoleUser, UserID: "host\xff"}
	cases := []struct {
		name  string
		event adaptor.Event
	}{{"text", badText}, {"text-pointer", &badText}}
	for _, field := range []string{"message", "url", "filename", "user"} {
		e := image
		switch field {
		case "message":
			e.MessageID += "\xff"
		case "url":
			e.URL += "\xff"
		case "filename":
			e.Filename += "\xff"
		case "user":
			e.UserID += "\xff"
		}
		cases = append(cases, struct {
			name  string
			event adaptor.Event
		}{field, e})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := json.Marshal(sessionrecorder.EventRecord{Event: tc.event}); err == nil {
				t.Fatal("JSON envelope would silently replace invalid bytes")
			}
			for _, storage := range []string{"memory", "jsonl"} {
				t.Run(storage, func(t *testing.T) {
					var backend sessionrecorder.EventBackend = sessionrecorder.NewMemoryEventBackend()
					if storage == "jsonl" {
						var err error
						backend, err = sessionrecorder.NewJSONLEventBackend(t.TempDir())
						if err != nil {
							t.Fatal(err)
						}
					}
					rec := sessionrecorder.NewEventRecorder(backend)
					defer rec.Close()
					ctx := context.Background()
					if err := backend.Append(ctx, "s", sessionrecorder.EventRecord{HostSeq: 1, Event: tc.event}); err == nil {
						t.Fatal("backend accepted nonrepresentable message")
					}
					if _, err := rec.Record(ctx, "s", tc.event); err == nil {
						t.Fatal("recorder accepted nonrepresentable message")
					}
					stored, err := backend.Load(ctx, "s")
					if err != nil || len(stored) != 0 {
						t.Fatalf("rejected bytes reached storage: %v, %d records", err, len(stored))
					}
					valid, err := rec.Record(ctx, "s", image)
					if err != nil || valid.HostSeq != 1 {
						t.Fatalf("rejection consumed cursor: %v, %d", err, valid.HostSeq)
					}
				})
			}
		})
	}
}

func TestMessageUTF8ValidationKeepsLegacyTextAndIgnoredAssistantIdentity(t *testing.T) {
	for _, event := range []adaptor.Event{
		adaptor.TextDelta{MessageID: "m", Text: "old\xff", Role: adaptor.RoleUser, UserID: "host"},
		adaptor.TextDelta{MessageID: "m", Text: "answer", UserID: "ignored\xff"},
		adaptor.ImageContent{MessageID: "m", Type: "binary", MIMEType: "image/png", URL: "/x", UserID: "ignored\xff"},
	} {
		raw, err := json.Marshal(sessionrecorder.EventRecord{Event: event})
		if err != nil {
			t.Fatalf("validation expanded beyond the new message fields: %v", err)
		}
		if strings.Contains(string(raw), "ignored") {
			t.Fatal("ignored assistant attribution persisted")
		}
	}
	in := &agui.RunAgentInput{Messages: []agui.Message{{Role: "user", Content: json.RawMessage(`"question"`)}}}
	for _, event := range in.UserTurnEventsWithUserID("run", "host\xff") {
		if event.(adaptor.TextDelta).UserID != "host\xff" {
			t.Fatal("constructor silently changed identity bytes")
		}
	}
}
