package agui

import (
	aguievents "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
	adaptor "github.com/agent-dance/agent-adaptor"
)

func imageContentEvent(e adaptor.ImageContent) aguievents.Event {
	if err := e.Validate(); err != nil {
		return customEvent("stream.dropped", map[string]any{
			"count": 1, "by_kind": map[string]int{"image.content": 1},
			"reason": "invalid_image_content", "source": "agui",
			"meta": observationMeta(e.Meta()),
		})
	}
	image := map[string]any{"type": e.Type, "mime_type": e.MIMEType, "url": e.URL}
	if e.Filename != "" {
		image["filename"] = e.Filename
	}
	value := map[string]any{"message_id": e.MessageID, "role": messageRole(e.Role), "image": image}
	if e.Role == adaptor.RoleUser && e.UserID != "" {
		value["user_id"] = e.UserID
	}
	return customEvent("image.content", value)
}
