package adaptor

import (
	"reflect"
	"testing"
	"time"
)

func TestImageContentValidationAndHostPublication(t *testing.T) {
	valid := ImageContent{MessageID: "m", Type: "binary", MIMEType: "image/png", URL: "/attachments/a.png"}
	for _, typ := range []string{"binary", "host-image-v2", "图片"} {
		e := valid
		e.Type = typ
		if err := e.Validate(); err != nil || !validHostEvent(e) {
			t.Fatalf("host discriminator %q rejected: %v", typ, err)
		}
	}
	cases := []struct {
		name string
		edit func(*ImageContent)
	}{
		{"message", func(e *ImageContent) { e.MessageID = "" }},
		{"type", func(e *ImageContent) { e.Type = "" }},
		{"utf8", func(e *ImageContent) { e.Type = "\xff" }},
		{"url", func(e *ImageContent) { e.URL = "" }},
		{"mime", func(e *ImageContent) { e.MIMEType = "text/plain" }},
		{"wildcard", func(e *ImageContent) { e.MIMEType = "image/*" }},
		{"malformed", func(e *ImageContent) { e.MIMEType = "image/" }},
		{"role", func(e *ImageContent) { e.Role = "system" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := valid
			tc.edit(&e)
			if e.Validate() == nil || validHostEvent(e) {
				t.Fatal("invalid image admitted")
			}
		})
	}
	if eventMayDrop(valid) || eventKind(valid) != "image.content" {
		t.Fatal("whole images must be reliable typed events")
	}
}

func TestMessageIdentitySnapshotsAndImageMetadata(t *testing.T) {
	for _, role := range []Role{RoleAssistant, RoleUser, "unknown"} {
		wantID := ""
		if role == RoleUser {
			wantID = " user/甲 "
		}
		originalText := TextDelta{MessageID: "m", Text: "q", Role: role, UserID: " user/甲 "}
		originalImage := ImageContent{MessageID: "m", Type: "binary", MIMEType: "image/png", URL: "/x", Role: role, UserID: " user/甲 "}
		meta := EventMeta{RunID: "run", Sequence: 8, Time: time.Unix(2, 0), Source: &EventSourceMeta{RunID: "source"}}
		text := WithEventMeta(originalText, meta).(TextDelta)
		image := WithEventMeta(&originalImage, meta).(ImageContent)
		if text.UserID != wantID || image.UserID != wantID {
			t.Fatalf("role %q identity survived incorrectly", role)
		}
		if !reflect.DeepEqual(image.Meta(), meta) || originalText.UserID != " user/甲 " || originalImage.UserID != " user/甲 " {
			t.Fatal("metadata lost or producer value mutated")
		}
		meta.Source.RunID = "mutated"
		if image.Meta().Source.RunID != "source" {
			t.Fatal("image metadata aliases producer")
		}
	}
}
