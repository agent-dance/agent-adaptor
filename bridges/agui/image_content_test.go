package agui_test

import (
	"encoding/json"
	"reflect"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/bridges/agui"
)

func TestImageContentRoleAndWire(t *testing.T) {
	for _, role := range []adaptor.Role{adaptor.RoleAssistant, adaptor.RoleUser} {
		t.Run(string(role), func(t *testing.T) {
			tr := agui.NewEventTranslator()
			all := tr.Translate(adaptor.RunStarted{RunID: "r"})
			text := tr.Translate(adaptor.TextDelta{MessageID: "m", Role: role, Phase: adaptor.PhaseStart, UserID: "human"})
			input := adaptor.ImageContent{MessageID: "m", Type: "binary", MIMEType: "image/png", URL: "/甲?q=x", Filename: "甲.png", Role: role, UserID: "human"}
			images := tr.Translate(input)
			if len(images) != 1 {
				t.Fatalf("images=%d", len(images))
			}
			wire := frameMap(t, images[0])
			value := wire["value"].(map[string]any)
			if wire["name"] != "image.content" || value["role"] != frameMap(t, text[0])["role"] || value["message_id"] != "m" {
				t.Fatalf("image/text mismatch: %v", wire)
			}
			wantImage := map[string]any{"type": "binary", "mime_type": "image/png", "url": "/甲?q=x", "filename": "甲.png"}
			if !reflect.DeepEqual(value["image"], wantImage) {
				t.Fatalf("image reference changed: %v", value)
			}
			if role == adaptor.RoleUser {
				if value["user_id"] != "human" {
					t.Fatal("user identity lost")
				}
			} else if _, ok := value["user_id"]; ok {
				t.Fatal("assistant inherited user identity")
			}
			if _, ok := frameMap(t, text[0])["user_id"]; ok {
				t.Fatal("text protocol acquired unsupported identity")
			}
			all = append(all, text...)
			all = append(all, images...)
			all = append(all, tr.CloseRun(nil)...)
			assertVerified(t, all)
		})
	}
}

func TestImageContentBufferedWholeReferencesAndInvalidDegradation(t *testing.T) {
	tr := agui.NewEventTranslator()
	first := adaptor.ImageContent{MessageID: "m", Type: "host-image-v2", MIMEType: "image/jpeg", URL: "/one"}
	if got := tr.Translate(first); len(got) != 0 {
		t.Fatal("pre-start image not buffered")
	}
	first.URL = "/two"
	_ = tr.Translate(first)
	events := tr.Translate(adaptor.RunStarted{RunID: "r"})
	if len(events) != 3 {
		t.Fatalf("events=%d", len(events))
	}
	for i, want := range []string{"/one", "/two"} {
		value := frameMap(t, events[i+1])["value"].(map[string]any)
		if value["image"].(map[string]any)["url"] != want {
			t.Fatal("whole images aliased or reordered")
		}
		if _, ok := value["user_id"]; ok {
			t.Fatal("unknown identity invented")
		}
	}
	bad := tr.Translate(adaptor.ImageContent{MessageID: "m", Type: "binary", MIMEType: "image/png"})
	wire := frameMap(t, bad[0])
	value := wire["value"].(map[string]any)
	if wire["name"] != "stream.dropped" || value["reason"] != "invalid_image_content" || value["dropped_count"] != float64(1) {
		t.Fatalf("no degradation: %v", wire)
	}
	if _, ok := value["image"]; ok {
		t.Fatal("invalid image emitted as content")
	}
	_ = tr.CloseRun(nil)
	if got := tr.Translate(first); len(got) != 0 {
		t.Fatal("post-terminal image delivered")
	}
}

func TestUserTurnIdentityIsExplicitAndStableAcrossTriple(t *testing.T) {
	var in agui.RunAgentInput
	if err := json.Unmarshal([]byte(`{"threadId":"thread","user_id":"untrusted","messages":[{"id":"m","role":"user","name":"untrusted","user_id":"untrusted","content":"question"}]}`), &in); err != nil {
		t.Fatal(err)
	}
	for _, userID := range []string{"", " user/甲 "} {
		events := in.UserTurnEventsWithUserID("run", userID)
		if len(events) != 3 {
			t.Fatal("user triple lost")
		}
		for i, e := range events {
			text := e.(adaptor.TextDelta)
			if text.UserID != userID || text.Role != adaptor.RoleUser || text.MessageID != "m" || text.Meta().Sequence != 0 || text.Meta().RunID != "run" {
				t.Fatalf("event %d=%+v", i, text)
			}
		}
	}
	for _, e := range in.UserTurnEvents("run") {
		if e.(adaptor.TextDelta).UserID != "" {
			t.Fatal("legacy helper inferred an untrusted identity")
		}
	}
	var absent *agui.RunAgentInput
	if absent.UserTurnEventsWithUserID("r", "host") != nil {
		t.Fatal("nil input invented turn")
	}
}

// Preserve source compatibility for hosts that bind the original method rather
// than calling it directly; changing it to variadic would break these shapes.
var _ interface{ UserTurnEvents(string) []adaptor.Event } = (*agui.RunAgentInput)(nil)
var _ func(*agui.RunAgentInput, string) []adaptor.Event = (*agui.RunAgentInput).UserTurnEvents

func TestUserTurnLegacyMethodValueSignature(t *testing.T) {
	input := &agui.RunAgentInput{Messages: []agui.Message{{Role: "user", Content: json.RawMessage(`"hello"`)}}}
	var legacy func(string) []adaptor.Event = input.UserTurnEvents
	if events := legacy("run"); len(events) != 3 {
		t.Fatal("legacy method value no longer synthesizes the triple")
	}
}
