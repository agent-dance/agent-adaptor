package a2a

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	a2aproto "github.com/a2aproject/a2a-go/v2/a2a"
)

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

func upstreamMessage(in Message) *a2aproto.Message {
	role := a2aproto.MessageRoleUser
	if in.Role == "agent" || in.Role == string(a2aproto.MessageRoleAgent) {
		role = a2aproto.MessageRoleAgent
	}
	msg := a2aproto.NewMessage(role, upstreamParts(in.Parts)...)
	if in.ID != "" {
		msg.ID = in.ID
	}
	msg.ContextID = in.ContextID
	msg.TaskID = a2aproto.TaskID(in.TaskID)
	msg.Metadata = cloneMap(in.Metadata)
	msg.Extensions = append([]string(nil), in.Extensions...)
	for _, id := range in.ReferenceTasks {
		msg.ReferenceTasks = append(msg.ReferenceTasks, a2aproto.TaskID(id))
	}
	return msg
}

func upstreamParts(parts []Part) []*a2aproto.Part {
	if len(parts) == 0 {
		return []*a2aproto.Part{a2aproto.NewTextPart("")}
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
	if data == nil {
		return nil
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return data
	}
	var normalized any
	if err := json.Unmarshal(raw, &normalized); err != nil {
		return data
	}
	return normalized
}

func convertTask(task *a2aproto.Task) Task {
	if task == nil {
		return Task{}
	}
	raw, _ := rawMap(task)
	out := Task{
		ID: string(task.ID), ContextID: task.ContextID, Status: convertStatus(task.Status),
		Metadata: cloneMap(task.Metadata), Raw: raw,
	}
	for _, msg := range task.History {
		out.Messages = append(out.Messages, convertMessage(msg))
	}
	for _, artifact := range task.Artifacts {
		out.Artifacts = append(out.Artifacts, convertArtifact(artifact))
	}
	return out
}

func convertMessage(msg *a2aproto.Message) Message {
	if msg == nil {
		return Message{}
	}
	raw, _ := rawMap(msg)
	out := Message{
		ID: msg.ID, Role: string(msg.Role), TaskID: string(msg.TaskID), ContextID: msg.ContextID,
		Extensions: append([]string(nil), msg.Extensions...), Metadata: cloneMap(msg.Metadata), Raw: raw,
	}
	for _, id := range msg.ReferenceTasks {
		out.ReferenceTasks = append(out.ReferenceTasks, string(id))
	}
	out.Parts = convertParts(msg.Parts)
	return out
}

func convertStatus(status a2aproto.TaskStatus) TaskStatus {
	out := TaskStatus{State: TaskState(status.State), Timestamp: status.Timestamp}
	if status.Message != nil {
		msg := convertMessage(status.Message)
		out.Message = &msg
	}
	return out
}

func convertArtifact(a *a2aproto.Artifact) Artifact {
	if a == nil {
		return Artifact{}
	}
	raw, _ := rawMap(a)
	return Artifact{
		ID: string(a.ID), Name: a.Name, Description: a.Description,
		Extensions: append([]string(nil), a.Extensions...), Metadata: cloneMap(a.Metadata),
		Parts: convertParts(a.Parts), Raw: raw,
	}
}

func convertParts(parts []*a2aproto.Part) []Part {
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
			part.Kind = PartData
			part.Data = v.Value
		case a2aproto.URL:
			part.Kind = PartURL
			part.URL = string(v)
		default:
			part.Kind = PartData
			part.Data = v
		}
		out = append(out, part)
	}
	return out
}

func eventFromUpstream(ev a2aproto.Event) (Event, error) {
	switch e := ev.(type) {
	case *a2aproto.Task:
		t := convertTask(e)
		kind := EventTask
		if executionFinalTask(t) {
			kind = EventTerminal
		}
		return Event{Kind: kind, Task: &t, TaskID: t.ID, ContextID: t.ContextID, Raw: t.Raw}, nil
	case *a2aproto.Message:
		m := convertMessage(e)
		return Event{Kind: EventTerminal, Message: &m, TaskID: m.TaskID, ContextID: m.ContextID, Raw: m.Raw}, nil
	case *a2aproto.TaskStatusUpdateEvent:
		status := convertStatus(e.Status)
		kind := EventStatus
		if executionFinalState(status.State) {
			kind = EventTerminal
		}
		raw, _ := rawMap(e)
		return Event{Kind: kind, Status: &status, TaskID: string(e.TaskID), ContextID: e.ContextID, Raw: raw}, nil
	case *a2aproto.TaskArtifactUpdateEvent:
		artifact := convertArtifact(e.Artifact)
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
