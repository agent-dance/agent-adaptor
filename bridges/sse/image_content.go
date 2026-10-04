package sse

import adaptor "github.com/agent-dance/agent-adaptor"

func imageContentValue(e adaptor.ImageContent) map[string]any {
	image := map[string]any{"type": e.Type, "mime_type": e.MIMEType, "url": e.URL}
	if e.Filename != "" {
		image["filename"] = e.Filename
	}
	value := map[string]any{"message_id": e.MessageID, "role": string(e.Role), "image": image}
	if e.Role == adaptor.RoleUser && e.UserID != "" {
		value["user_id"] = e.UserID
	}
	return value
}
