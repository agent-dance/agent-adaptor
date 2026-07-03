package a2a

import (
	"encoding/json"
	"fmt"

	a2aproto "github.com/a2aproject/a2a-go/v2/a2a"
)

func inboundRequest(execCtx taskInfo) InboundRequest {
	msg := convertMessage(execCtx.Message())
	return InboundRequest{
		TaskID: execCtx.TaskIDString(), ContextID: execCtx.ContextIDString(),
		Message: msg, Metadata: cloneMap(execCtx.MetadataMap()),
	}
}

type taskInfo interface {
	TaskIDString() string
	ContextIDString() string
	Message() *a2aproto.Message
	MetadataMap() map[string]any
}

func convertMessage(msg *a2aproto.Message) Message {
	if msg == nil {
		return Message{}
	}
	out := Message{
		ID: msg.ID, Role: string(msg.Role), TaskID: string(msg.TaskID), ContextID: msg.ContextID,
		Extensions: append([]string(nil), msg.Extensions...), Metadata: cloneMap(msg.Metadata),
	}
	for _, id := range msg.ReferenceTasks {
		out.ReferenceTasks = append(out.ReferenceTasks, string(id))
	}
	for _, part := range msg.Parts {
		out.Parts = append(out.Parts, convertPart(part))
	}
	return out
}

func convertPart(p *a2aproto.Part) Part {
	if p == nil {
		return Part{}
	}
	out := Part{MediaType: p.MediaType, Filename: p.Filename, Metadata: cloneMap(p.Metadata)}
	switch v := p.Content.(type) {
	case a2aproto.Text:
		out.Kind = PartText
		out.Text = string(v)
	case a2aproto.Raw:
		out.Kind = PartRaw
		out.Raw = append([]byte(nil), []byte(v)...)
	case a2aproto.Data:
		out.Kind = PartData
		out.Data = v.Value
	case a2aproto.URL:
		out.Kind = PartURL
		out.URL = string(v)
	}
	return out
}

func textPart(text string) *a2aproto.Part {
	return a2aproto.NewTextPart(text)
}

func dataPart(data any) *a2aproto.Part {
	return a2aproto.NewDataPart(data)
}

func agentMessage(info a2aproto.TaskInfoProvider, text string) *a2aproto.Message {
	if text == "" {
		return nil
	}
	return a2aproto.NewMessageForTask(a2aproto.MessageRoleAgent, info, textPart(text))
}

func failureMessage(info a2aproto.TaskInfoProvider, msg string, details map[string]any) *a2aproto.Message {
	if msg == "" {
		msg = "agent run failed"
	}
	part := textPart(msg)
	if len(details) > 0 {
		part.Metadata = map[string]any{"agentadaptor.failure": details}
	}
	return a2aproto.NewMessageForTask(a2aproto.MessageRoleAgent, info, part)
}

func rawMap(v any) map[string]any {
	raw, err := json.Marshal(v)
	if err != nil {
		return map[string]any{"marshal_error": err.Error()}
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return map[string]any{"unmarshal_error": err.Error()}
	}
	return out
}

func cloneMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func stringMapAny(in map[string]string) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func mustJSON(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(raw)
}
