package a2a

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	a2aproto "github.com/a2aproject/a2a-go/v2/a2a"
)

const maxA2AJSONInteger = uint64(1<<53 - 1)

func convertAgentCard(card *a2aproto.AgentCard) (AgentCard, error) {
	if card == nil {
		return AgentCard{}, fmt.Errorf("%w: nil card", ErrInvalidAgentCard)
	}
	raw, _ := rawMap(card)
	fingerprint, err := fingerprint(card)
	if err != nil {
		return AgentCard{}, err
	}
	out := AgentCard{
		Name:               card.Name,
		Description:        card.Description,
		Version:            card.Version,
		DocumentationURL:   card.DocumentationURL,
		IconURL:            card.IconURL,
		DefaultInputModes:  append([]string(nil), card.DefaultInputModes...),
		DefaultOutputModes: append([]string(nil), card.DefaultOutputModes...),
		Fingerprint:        fingerprint,
		Raw:                raw,
		Capabilities: Capabilities{
			Streaming:         card.Capabilities.Streaming,
			PushNotifications: card.Capabilities.PushNotifications,
			ExtendedAgentCard: card.Capabilities.ExtendedAgentCard,
		},
	}
	if card.Provider != nil {
		out.Provider = &Provider{Organization: card.Provider.Org, URL: card.Provider.URL}
	}
	for _, ext := range card.Capabilities.Extensions {
		out.Capabilities.Extensions = append(out.Capabilities.Extensions, Extension{
			URI: ext.URI, Description: ext.Description, Required: ext.Required, Params: cloneMap(ext.Params),
		})
	}
	for i, iface := range card.SupportedInterfaces {
		if iface == nil {
			continue
		}
		if i == 0 {
			out.URL = iface.URL
		}
		out.SupportedInterfaces = append(out.SupportedInterfaces, AgentInterface{
			URL: iface.URL, ProtocolBinding: TransportProtocol(iface.ProtocolBinding), Tenant: iface.Tenant,
			ProtocolVersion: string(iface.ProtocolVersion),
		})
	}
	for _, skill := range card.Skills {
		out.Skills = append(out.Skills, Skill{
			ID: skill.ID, Name: skill.Name, Description: skill.Description,
			Tags: append([]string(nil), skill.Tags...), Examples: append([]string(nil), skill.Examples...),
			InputModes: append([]string(nil), skill.InputModes...), OutputModes: append([]string(nil), skill.OutputModes...),
		})
	}
	return out, nil
}

func validateAgentCard(card *a2aproto.AgentCard) error {
	if card == nil {
		return fmt.Errorf("%w: nil card", ErrInvalidAgentCard)
	}
	if card.Name == "" {
		return fmt.Errorf("%w: missing name", ErrInvalidAgentCard)
	}
	if card.Version == "" {
		return fmt.Errorf("%w: missing version", ErrInvalidAgentCard)
	}
	if len(card.SupportedInterfaces) == 0 {
		return fmt.Errorf("%w: missing supportedInterfaces", ErrInvalidAgentCard)
	}
	seenSkill := map[string]struct{}{}
	for _, iface := range card.SupportedInterfaces {
		if iface == nil || iface.URL == "" {
			return fmt.Errorf("%w: interface url is required", ErrInvalidAgentCard)
		}
		if iface.ProtocolBinding == "" {
			return fmt.Errorf("%w: interface protocolBinding is required", ErrInvalidAgentCard)
		}
		if iface.ProtocolVersion != "" && iface.ProtocolVersion != a2aproto.Version {
			return fmt.Errorf("%w: unsupported protocol version %q", ErrInvalidAgentCard, iface.ProtocolVersion)
		}
	}
	for _, skill := range card.Skills {
		if skill.ID == "" {
			return fmt.Errorf("%w: skill id is required", ErrInvalidAgentCard)
		}
		if _, exists := seenSkill[skill.ID]; exists {
			return fmt.Errorf("%w: duplicate skill %q", ErrInvalidAgentCard, skill.ID)
		}
		seenSkill[skill.ID] = struct{}{}
	}
	return nil
}

func upstreamMessage(in Message) (*a2aproto.Message, error) {
	role := a2aproto.MessageRoleUser
	if in.Role == "agent" || in.Role == string(a2aproto.MessageRoleAgent) {
		role = a2aproto.MessageRoleAgent
	}
	parts, err := upstreamParts(in.Parts)
	if err != nil {
		return nil, err
	}
	metadata, err := normalizeProtocolMap(in.Metadata)
	if err != nil {
		return nil, fmt.Errorf("%w: message metadata: %v", ErrProtocol, err)
	}
	msg := a2aproto.NewMessage(role, parts...)
	if in.ID != "" {
		msg.ID = in.ID
	}
	msg.ContextID = in.ContextID
	msg.TaskID = a2aproto.TaskID(in.TaskID)
	msg.Metadata = metadata
	msg.Extensions = append([]string(nil), in.Extensions...)
	for _, id := range in.ReferenceTasks {
		msg.ReferenceTasks = append(msg.ReferenceTasks, a2aproto.TaskID(id))
	}
	return msg, nil
}

func upstreamParts(parts []Part) ([]*a2aproto.Part, error) {
	if len(parts) == 0 {
		return []*a2aproto.Part{a2aproto.NewTextPart("")}, nil
	}
	out := make([]*a2aproto.Part, 0, len(parts))
	for i, p := range parts {
		var up *a2aproto.Part
		switch p.Kind {
		case PartRaw:
			if p.Raw == nil {
				return nil, fmt.Errorf("%w: part %d raw content is nil", ErrProtocol, i)
			}
			up = a2aproto.NewRawPart(p.Raw)
		case PartData:
			if p.Data == nil {
				return nil, fmt.Errorf("%w: part %d data content is nil", ErrProtocol, i)
			}
			data, err := protocolDataValue(p.Data)
			if err != nil {
				return nil, fmt.Errorf("%w: part %d data: %v", ErrProtocol, i, err)
			}
			if data == nil {
				return nil, fmt.Errorf("%w: part %d data content resolves to null", ErrProtocol, i)
			}
			up = a2aproto.NewDataPart(data)
		case PartURL:
			if p.URL == "" {
				return nil, fmt.Errorf("%w: part %d URL is empty", ErrProtocol, i)
			}
			up = a2aproto.NewFileURLPart(a2aproto.URL(p.URL), p.MediaType)
		case PartText, "":
			up = a2aproto.NewTextPart(p.Text)
		default:
			return nil, fmt.Errorf("%w: part %d has unsupported kind %q", ErrProtocol, i, p.Kind)
		}
		metadata, err := normalizeProtocolMap(p.Metadata)
		if err != nil {
			return nil, fmt.Errorf("%w: part %d metadata: %v", ErrProtocol, i, err)
		}
		up.MediaType = p.MediaType
		up.Filename = p.Filename
		up.Metadata = metadata
		out = append(out, up)
	}
	return out, nil
}

func protocolDataValue(data any) (any, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	var normalized any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&normalized); err != nil {
		return nil, err
	}
	return normalizeProtocolNumbers(normalized)
}

func normalizeProtocolNumbers(value any) (any, error) {
	switch typed := value.(type) {
	case json.Number:
		raw := typed.String()
		if strings.ContainsAny(raw, ".eE") {
			number, err := strconv.ParseFloat(raw, 64)
			if err != nil || math.IsInf(number, 0) || math.IsNaN(number) {
				return nil, fmt.Errorf("number %q is outside the supported range", raw)
			}
			if math.Trunc(number) == number && math.Abs(number) > float64(maxA2AJSONInteger) {
				return nil, fmt.Errorf("integer %q is outside the interoperable JSON safe range", raw)
			}
			return number, nil
		}
		if number, err := strconv.ParseInt(raw, 10, 64); err == nil {
			if number < -int64(maxA2AJSONInteger) || number > int64(maxA2AJSONInteger) {
				return nil, fmt.Errorf("integer %q is outside the interoperable JSON safe range", raw)
			}
			return number, nil
		}
		if number, err := strconv.ParseUint(raw, 10, 64); err == nil {
			if number > maxA2AJSONInteger {
				return nil, fmt.Errorf("integer %q is outside the interoperable JSON safe range", raw)
			}
			return number, nil
		}
		return nil, fmt.Errorf("integer %q is outside the supported 64-bit range", raw)
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, child := range typed {
			normalized, err := normalizeProtocolNumbers(child)
			if err != nil {
				return nil, err
			}
			out[key] = normalized
		}
		return out, nil
	case []any:
		out := make([]any, len(typed))
		for i, child := range typed {
			normalized, err := normalizeProtocolNumbers(child)
			if err != nil {
				return nil, err
			}
			out[i] = normalized
		}
		return out, nil
	default:
		return value, nil
	}
}

func normalizeProtocolMap(in map[string]any) (map[string]any, error) {
	if len(in) == 0 {
		return nil, nil
	}
	normalized, err := protocolDataValue(in)
	if err != nil {
		return nil, err
	}
	out, ok := normalized.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("value is not a JSON object")
	}
	return out, nil
}

func convertTask(task *a2aproto.Task) (Task, error) {
	if task == nil {
		return Task{}, nil
	}
	raw, _ := rawMap(task)
	status, err := convertStatus(task.Status)
	if err != nil {
		return Task{}, err
	}
	out := Task{
		ID: string(task.ID), ContextID: task.ContextID, Status: status,
		Metadata: cloneMap(task.Metadata), Raw: raw,
	}
	for _, msg := range task.History {
		converted, err := convertMessage(msg)
		if err != nil {
			return Task{}, err
		}
		out.Messages = append(out.Messages, converted)
	}
	for _, artifact := range task.Artifacts {
		converted, err := convertArtifact(artifact)
		if err != nil {
			return Task{}, err
		}
		out.Artifacts = append(out.Artifacts, converted)
	}
	return out, nil
}

func convertMessage(msg *a2aproto.Message) (Message, error) {
	if msg == nil {
		return Message{}, nil
	}
	raw, _ := rawMap(msg)
	out := Message{
		ID: msg.ID, Role: string(msg.Role), TaskID: string(msg.TaskID), ContextID: msg.ContextID,
		Extensions: append([]string(nil), msg.Extensions...), Metadata: cloneMap(msg.Metadata), Raw: raw,
	}
	for _, id := range msg.ReferenceTasks {
		out.ReferenceTasks = append(out.ReferenceTasks, string(id))
	}
	parts, err := convertParts(msg.Parts)
	if err != nil {
		return Message{}, err
	}
	out.Parts = parts
	return out, nil
}

func convertStatus(status a2aproto.TaskStatus) (TaskStatus, error) {
	out := TaskStatus{State: TaskState(status.State), Timestamp: status.Timestamp}
	if status.Message != nil {
		msg, err := convertMessage(status.Message)
		if err != nil {
			return TaskStatus{}, err
		}
		out.Message = &msg
	}
	return out, nil
}

func convertArtifact(a *a2aproto.Artifact) (Artifact, error) {
	if a == nil {
		return Artifact{}, nil
	}
	raw, _ := rawMap(a)
	parts, err := convertParts(a.Parts)
	if err != nil {
		return Artifact{}, err
	}
	return Artifact{
		ID: string(a.ID), Name: a.Name, Description: a.Description,
		Extensions: append([]string(nil), a.Extensions...), Metadata: cloneMap(a.Metadata),
		Parts: parts, Raw: raw,
	}, nil
}

func convertParts(parts []*a2aproto.Part) ([]Part, error) {
	out := make([]Part, 0, len(parts))
	for _, p := range parts {
		if p == nil {
			continue
		}
		part := Part{MediaType: p.MediaType, Filename: p.Filename, Metadata: cloneMap(p.Metadata)}
		switch v := p.Content.(type) {
		case a2aproto.Text:
			part.Kind = PartText
			part.Text = string(v)
		case a2aproto.Raw:
			part.Kind = PartRaw
			part.Raw = append([]byte(nil), []byte(v)...)
		case a2aproto.Data:
			normalized, err := protocolDataValue(v.Value)
			if err != nil || normalized == nil {
				return nil, fmt.Errorf("%w: inbound data part is null or contains a number outside the interoperable JSON safe range", ErrProtocol)
			}
			part.Kind = PartData
			part.Data = v.Value
		case a2aproto.URL:
			part.Kind = PartURL
			part.URL = string(v)
		default:
			return nil, fmt.Errorf("%w: inbound part has unsupported content type %T", ErrProtocol, p.Content)
		}
		out = append(out, part)
	}
	return out, nil
}

func eventFromUpstream(ev a2aproto.Event) (Event, error) {
	switch e := ev.(type) {
	case *a2aproto.Task:
		t, err := convertTask(e)
		if err != nil {
			return Event{}, err
		}
		kind := EventTask
		if executionFinalTask(t) {
			kind = EventTerminal
		}
		return Event{Kind: kind, Task: &t, TaskID: t.ID, ContextID: t.ContextID, Raw: t.Raw}, nil
	case *a2aproto.Message:
		m, err := convertMessage(e)
		if err != nil {
			return Event{}, err
		}
		return Event{Kind: EventTerminal, Message: &m, TaskID: m.TaskID, ContextID: m.ContextID, Raw: m.Raw}, nil
	case *a2aproto.TaskStatusUpdateEvent:
		status, err := convertStatus(e.Status)
		if err != nil {
			return Event{}, err
		}
		kind := EventStatus
		if executionFinalState(status.State) {
			kind = EventTerminal
		}
		raw, _ := rawMap(e)
		return Event{Kind: kind, Status: &status, TaskID: string(e.TaskID), ContextID: e.ContextID, Raw: raw}, nil
	case *a2aproto.TaskArtifactUpdateEvent:
		artifact, err := convertArtifact(e.Artifact)
		if err != nil {
			return Event{}, err
		}
		raw, _ := rawMap(e)
		return Event{
			Kind: EventArtifact, Artifact: &artifact, TaskID: string(e.TaskID), ContextID: e.ContextID,
			Append: e.Append, LastChunk: e.LastChunk, Raw: raw,
		}, nil
	default:
		return Event{}, fmt.Errorf("%w: unknown upstream event %T", ErrProtocol, ev)
	}
}

func fingerprint(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func rawMap(v any) (map[string]any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
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
