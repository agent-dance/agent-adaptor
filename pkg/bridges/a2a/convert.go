package a2a

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	a2aproto "github.com/a2aproject/a2a-go/v2/a2a"
)

const redactedMarker = "[REDACTED]"

var (
	inlineSecretPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)("?(?:authorization|proxy-authorization|x-api-key|api[-_]?key|access_token|refresh_token|id_token|client_secret|secret|password|passwd|cookie|set-cookie)"?\s*[:=]\s*"?)(?:(?:bearer|basic)\s+[^"\s,;]+|[^"\s,;]+)("?)`),
		regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]+`),
	}
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
	return a2aproto.NewDataPart(protocolDataValue(data))
}

func outboundParts(parts []Part) []*a2aproto.Part {
	if len(parts) == 0 {
		return []*a2aproto.Part{textPart("")}
	}
	out := make([]*a2aproto.Part, 0, len(parts))
	for _, p := range parts {
		var up *a2aproto.Part
		switch p.Kind {
		case PartRaw:
			up = a2aproto.NewRawPart(p.Raw)
		case PartData:
			up = a2aproto.NewDataPart(protocolDataValue(p.Data))
		case PartURL:
			up = a2aproto.NewFileURLPart(a2aproto.URL(p.URL), p.MediaType)
		default:
			up = a2aproto.NewTextPart(p.Text)
		}
		up.MediaType = p.MediaType
		up.Filename = p.Filename
		up.Metadata = cloneMap(p.Metadata)
		out = append(out, up)
	}
	return out
}

func protocolDataValue(data any) any {
	if normalized, ok := normalizeJSONValue(data); ok {
		return normalized
	}
	return data
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

func sanitizeRemoteMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	sanitized, ok := sanitizeRemoteValue(in).(map[string]any)
	if !ok {
		return nil
	}
	return sanitized
}

func sanitizeRemoteValue(v any) any {
	normalized, ok := normalizeJSONValue(v)
	if !ok {
		return redactRemoteValue("", v)
	}
	return redactRemoteValue("", normalized)
}

func normalizeJSONValue(v any) (any, bool) {
	if v == nil {
		return nil, true
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, false
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, false
	}
	return out, true
}

func redactRemoteValue(key string, v any) any {
	switch typed := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for k, child := range typed {
			if isSensitiveKey(k) {
				out[k] = redactedMarker
				continue
			}
			out[k] = redactRemoteValue(k, child)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, child := range typed {
			out[i] = redactRemoteValue(key, child)
		}
		return out
	case string:
		if isSensitiveKey(key) {
			return redactedMarker
		}
		return redactInlineSecrets(typed)
	default:
		return v
	}
}

func redactInlineSecrets(s string) string {
	out := s
	for i, pattern := range inlineSecretPatterns {
		switch i {
		case 0:
			out = pattern.ReplaceAllString(out, "${1}"+redactedMarker+"$2")
		default:
			out = pattern.ReplaceAllStringFunc(out, func(match string) string {
				parts := strings.Fields(match)
				if len(parts) == 0 {
					return redactedMarker
				}
				return parts[0] + " " + redactedMarker
			})
		}
	}
	return out
}

func isSensitiveKey(key string) bool {
	switch normalizeSensitiveKey(key) {
	case "authorization",
		"proxyauthorization",
		"apikey",
		"xapikey",
		"token",
		"accesstoken",
		"refreshtoken",
		"idtoken",
		"clientsecret",
		"secret",
		"password",
		"passwd",
		"cookie",
		"setcookie",
		"bearer",
		"privatekey",
		"credential",
		"credentials":
		return true
	default:
		return false
	}
}

func normalizeSensitiveKey(key string) string {
	key = strings.ToLower(strings.TrimSpace(key))
	replacer := strings.NewReplacer("-", "", "_", "", " ", "")
	return replacer.Replace(key)
}
