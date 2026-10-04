package agui

import (
	aguievents "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
	adaptor "github.com/agent-dance/agent-adaptor"
)

func imageContentEvent(e adaptor.ImageContent) aguievents.Event {
	if err := e.Validate(); err != nil {
		meta := e.Meta()
		value := droppedValue(adaptor.Dropped{
			Count: 1, ByKind: map[string]int{"image.content": 1},
			FirstSequence: meta.Sequence, LastSequence: meta.Sequence,
			Reason: "invalid_image_content", Source: "agui",
		})
		value["meta"] = observationMeta(meta)
		return customEvent("stream.dropped", value)
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
