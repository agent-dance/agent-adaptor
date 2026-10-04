package sse

import (
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor"
)

func TestRawImageAndUserAttribution(t *testing.T) {
	for _, role := range []adaptor.Role{adaptor.RoleAssistant, adaptor.RoleUser} {
		image := adaptor.WithEventMeta(adaptor.ImageContent{MessageID: "m", Type: "binary", MIMEType: "image/png", URL: "/a.png", Role: role, UserID: "u"}, adaptor.EventMeta{RunID: "run", Sequence: 9})
		name, raw := rawFrameFor(image)
		body := raw.(map[string]any)
		if name != "image.content" || body["image"].(map[string]any)["url"] != "/a.png" || body["meta"].(map[string]any)["sequence"] != uint64(9) {
			t.Fatalf("image/meta lost: %s %+v", name, body)
		}
		_, textRaw := rawFrameFor(adaptor.TextDelta{MessageID: "m", Role: role, UserID: "u"})
		for _, value := range []map[string]any{body, textRaw.(map[string]any)} {
			id, exists := value["user_id"]
			if role == adaptor.RoleUser && id != "u" || role != adaptor.RoleUser && exists {
				t.Fatal("incorrect host attribution")
			}
		}
	}
	name, raw := rawFrameFor(adaptor.WithEventMeta(adaptor.ImageContent{}, adaptor.EventMeta{Sequence: 10}))
	body := raw.(map[string]any)
	if name != "stream.dropped" || body["reason"] != "invalid_image_content" || body["meta"].(map[string]any)["sequence"] != uint64(10) {
		t.Fatalf("invalid image silently lost: %v", body)
	}
}
