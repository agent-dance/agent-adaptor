package a2a

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	a2aproto "github.com/a2aproject/a2a-go/v2/a2a"
)

const (
	redactedMarker       = "[REDACTED]"
	unserializableMarker = "[UNSERIALIZABLE]"
	maxA2AJSONInteger    = uint64(1<<53 - 1)
)

var (
	inlineSecretPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)("?(?:authorization|proxy-authorization|x-api-key|api[-_]?key|access_token|refresh_token|id_token|client_secret|secret|password|passwd|cookie|set-cookie)"?\s*[:=]\s*"?)(?:(?:bearer|basic)\s+[^"\s,;]+|[^"\s,;]+)("?)`),
		regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]+`),
	}
)

func inboundRequest(execCtx taskInfo) (InboundRequest, error) {
	msg, err := convertMessage(execCtx.Message())
	if err != nil {
		return InboundRequest{}, err
	}
	return InboundRequest{
		TaskID: execCtx.TaskIDString(), ContextID: execCtx.ContextIDString(),
		Message: msg, Metadata: cloneMap(execCtx.MetadataMap()),
	}, nil
}

type taskInfo interface {
	TaskIDString() string
	ContextIDString() string
	Message() *a2aproto.Message
	MetadataMap() map[string]any
}

func convertMessage(msg *a2aproto.Message) (Message, error) {
	if msg == nil {
		return Message{}, nil
	}
	out := Message{
		ID: msg.ID, Role: string(msg.Role), TaskID: string(msg.TaskID), ContextID: msg.ContextID,
		Extensions: append([]string(nil), msg.Extensions...), Metadata: cloneMap(msg.Metadata),
	}
	for _, id := range msg.ReferenceTasks {
		out.ReferenceTasks = append(out.ReferenceTasks, string(id))
	}
	for _, part := range msg.Parts {
		converted, err := convertPart(part)
		if err != nil {
			return Message{}, err
		}
		out.Parts = append(out.Parts, converted)
	}
	return out, nil
}

func convertPart(p *a2aproto.Part) (Part, error) {
	if p == nil {
		return Part{}, nil
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
		normalized, ok := normalizeJSONValue(v.Value)
		if !ok || normalized == nil {
			return Part{}, fmt.Errorf("inbound data part is null or contains a number outside the interoperable JSON safe range")
		}
		out.Kind = PartData
		out.Data = v.Value
	case a2aproto.URL:
		out.Kind = PartURL
		out.URL = string(v)
	default:
		return Part{}, fmt.Errorf("inbound part has unsupported content type %T", p.Content)
	}
	return out, nil
}

func textPart(text string) *a2aproto.Part {
	return a2aproto.NewTextPart(text)
}

func dataPart(data any) *a2aproto.Part {
	return a2aproto.NewDataPart(protocolDataValue(data))
}

func outboundParts(parts []Part) ([]*a2aproto.Part, error) {
	if len(parts) == 0 {
		return nil, fmt.Errorf("artifact parts are required")
	}
	out := make([]*a2aproto.Part, 0, len(parts))
	for i, p := range parts {
		var up *a2aproto.Part
		switch p.Kind {
		case PartRaw:
			if p.Raw == nil {
				return nil, fmt.Errorf("part %d raw content is nil", i)
			}
			up = a2aproto.NewRawPart(p.Raw)
		case PartData:
			if p.Data == nil {
				return nil, fmt.Errorf("part %d data content is nil", i)
			}
			normalized, ok := normalizeJSONValue(p.Data)
			if !ok {
				return nil, fmt.Errorf("part %d data is not JSON-compatible", i)
			}
			if normalized == nil {
				return nil, fmt.Errorf("part %d data content resolves to null", i)
			}
			up = a2aproto.NewDataPart(normalized)
		case PartURL:
			if strings.TrimSpace(p.URL) == "" {
				return nil, fmt.Errorf("part %d URL is empty", i)
			}
			up = a2aproto.NewFileURLPart(a2aproto.URL(p.URL), p.MediaType)
		case PartText, "":
			up = a2aproto.NewTextPart(p.Text)
		default:
			return nil, fmt.Errorf("part %d has unsupported kind %q", i, p.Kind)
		}
		metadata, err := normalizeJSONMap(p.Metadata)
		if err != nil {
			return nil, fmt.Errorf("part %d metadata: %w", i, err)
		}
		up.MediaType = p.MediaType
		up.Filename = p.Filename
		up.Metadata = metadata
		out = append(out, up)
	}
	return out, nil
}

func protocolDataValue(data any) any {
	if normalized, ok := normalizeJSONValue(data); ok {
		return normalized
	}
	return unserializableMarker
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
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&out); err != nil {
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
		return unserializableMarker
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
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&out); err != nil {
		return nil, false
	}
	return normalizeDecodedJSONNumbers(out)
}

func normalizeDecodedJSONNumbers(value any) (any, bool) {
	switch typed := value.(type) {
	case json.Number:
		raw := typed.String()
		if strings.ContainsAny(raw, ".eE") {
			number, err := strconv.ParseFloat(raw, 64)
			if err != nil || math.IsInf(number, 0) || math.IsNaN(number) {
				return nil, false
			}
			if math.Trunc(number) == number && math.Abs(number) > float64(maxA2AJSONInteger) {
				return nil, false
			}
			return number, true
		}
		if number, err := strconv.ParseInt(raw, 10, 64); err == nil {
			if number < -int64(maxA2AJSONInteger) || number > int64(maxA2AJSONInteger) {
				return nil, false
			}
			return number, true
		}
		if number, err := strconv.ParseUint(raw, 10, 64); err == nil {
			if number > maxA2AJSONInteger {
				return nil, false
			}
			return number, true
		}
		return nil, false
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, child := range typed {
			normalized, ok := normalizeDecodedJSONNumbers(child)
			if !ok {
				return nil, false
			}
			out[key] = normalized
		}
		return out, true
	case []any:
		out := make([]any, len(typed))
		for i, child := range typed {
			normalized, ok := normalizeDecodedJSONNumbers(child)
			if !ok {
				return nil, false
			}
			out[i] = normalized
		}
		return out, true
	default:
		return value, true
	}
}

func normalizeJSONMap(in map[string]any) (map[string]any, error) {
	if len(in) == 0 {
		return nil, nil
	}
	normalized, ok := normalizeJSONValue(in)
	if !ok {
		return nil, fmt.Errorf("value is not JSON-compatible")
	}
	out, ok := normalized.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("value is not a JSON object")
	}
	return out, nil
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
