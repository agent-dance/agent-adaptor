package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"

	"github.com/agent-dance/agent-adaptor/driver"
)

type delegatedSession struct {
	parser           *serverParser
	parentID, partID string
	active           bool
}

// A child becomes part of this invocation only through the formal task tool's
// state.metadata.sessionId, verified against the session API's parentID. Session
// creation, names and arbitrary JSON fields never grant authority to respond.
func (p *serverParser) consumeDelegated(ctx context.Context, client *serverClient, raw []byte) (*serverParser, serverEvent, error) {
	var envelope struct {
		Type       string `json:"type"`
		Properties struct {
			SessionID string      `json:"sessionID"`
			Part      *serverPart `json:"part"`
		} `json:"properties"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return p, serverEvent{}, errors.New("OpenCode SSE event is malformed")
	}
	target := p
	if id := envelope.Properties.SessionID; id != "" && id != p.sessionID {
		child := p.delegated[id]
		if child == nil || !child.active {
			return p, serverEvent{}, nil
		}
		for owner := child.parentID; owner != p.sessionID; {
			ancestor := p.delegated[owner]
			if ancestor == nil || !ancestor.active {
				return p, serverEvent{}, nil
			}
			owner = ancestor.parentID
		}
		target = child.parser
	}
	event, err := target.consume(raw)
	if err != nil {
		return target, event, err
	}
	b := envelope.Properties.Part
	if event.Type == "message.part.updated" && b != nil && b.Tool == "task" && b.Type == "tool" {
		id, exists := b.State.Metadata["sessionId"]
		if exists {
			childID, ok := id.(string)
			if !ok || childID == "" {
				return target, event, errors.New("opencode: malformed task child identifier")
			}
			if err := p.bindDelegated(ctx, client, target, b, childID); err != nil {
				return target, event, err
			}
		} else if b.State.Status == "completed" || b.State.Status == "error" {
			for childID, child := range p.delegated {
				if child.parentID == target.sessionID && child.partID == b.ID {
					if err := p.bindDelegated(ctx, client, target, b, childID); err != nil {
						return target, event, err
					}
				}
			}
		}
	}
	return target, event, nil
}

func (p *serverParser) bindDelegated(ctx context.Context, client *serverClient, parent *serverParser, part *serverPart, childID string) error {
	if childID == p.sessionID || childID == parent.sessionID {
		return errors.New("opencode: task cannot bind its ancestor session")
	}
	for id := parent.sessionID; id != p.sessionID; {
		ancestor := p.delegated[id]
		if ancestor == nil || ancestor.parentID == childID {
			return errors.New("opencode: task session ancestry is invalid")
		}
		id = ancestor.parentID
	}
	child := p.delegated[childID]
	if child == nil || child.partID != part.ID || child.parentID != parent.sessionID {
		if child != nil && child.active {
			return errors.New("opencode: two active tasks target the same child session")
		}
		if part.State.Status != "running" {
			return errors.New("opencode: task completed without an observed active child")
		}
		if err := client.verifyChild(ctx, childID, parent.sessionID); err != nil {
			return err
		}
		for id, old := range p.delegated {
			if old.parentID == parent.sessionID && old.partID == part.ID && id != childID {
				return errors.New("opencode: task changed its child session")
			}
		}
		parentCall := part.CallID
		if parent.child {
			parentCall = childIdentifier(parent.sessionID, parentCall)
		}
		sink := &delegatedSink{root: p, sessionID: childID, parentID: parent.sessionID, callID: parentCall}
		parser := newServerParser(sink, p.events.Streaming)
		parser.sessionID, parser.decisions, parser.child = childID, p.decisions, true
		child = &delegatedSession{parser: parser, parentID: parent.sessionID, partID: part.ID, active: true}
		p.delegated[childID] = child
		parser.events.Record(driver.TranscriptItem{Kind: driver.TranscriptInit, SessionID: childID})
	}
	if !child.active {
		return nil
	}
	if part.State.Status == "completed" || part.State.Status == "error" {
		incomplete := p.closeDelegated(childID)
		if incomplete != "" && part.State.Status == "completed" {
			return errors.New("opencode: successful task left incomplete child content")
		}
	}
	return nil
}

// End descendants first so nested tools cannot outlive their owning task.
// A failed task may legitimately interrupt partial child output; its parent
// can handle that tool failure. A successful task must have settled children.
func (p *serverParser) closeDelegated(childID string) string {
	child := p.delegated[childID]
	var descendants []string
	for id, next := range p.delegated {
		if next.active && next.parentID == childID {
			descendants = append(descendants, id)
		}
	}
	sort.Strings(descendants)
	message := ""
	for _, id := range descendants {
		p.closeDelegated(id)
		message = "active delegated task"
	}
	if incomplete := child.parser.closeOpenParts(); incomplete != "" {
		message = incomplete
	}
	child.active = false
	return message
}

func (c *serverClient) verifyChild(ctx context.Context, childID, parentID string) error {
	raw, err := c.call(ctx, "GET", "/session/"+url.PathEscape(childID), nil)
	if err != nil {
		return err
	}
	var info struct {
		ID       string `json:"id"`
		ParentID string `json:"parentID"`
	}
	if err := decodeNativeObject(raw, &info); err != nil {
		return err
	}
	if info.ID != childID || info.ParentID != parentID {
		return errors.New("opencode: task child does not belong to its invoking session")
	}
	return nil
}

func childIdentifier(sessionID, id string) string {
	if id == "" {
		return ""
	}
	raw, _ := json.Marshal([2]string{sessionID, id})
	return "opencode-child:" + string(raw)
}

type delegatedSink struct {
	root                        *serverParser
	sessionID, parentID, callID string
}

func (s *delegatedSink) Emit(event driver.RunEvent) error {
	if event.Type != driver.RunEventItem || event.Item == nil {
		return errors.New("opencode: invalid delegated semantic event")
	}
	item := *event.Item
	item.SessionID = s.sessionID
	item.ToolUseID = childIdentifier(s.sessionID, item.ToolUseID)
	metadata := map[string]string{}
	for key, value := range item.Metadata {
		metadata[key] = value
	}
	metadata["opencode.parent_session_id"], metadata["opencode.parent_tool_call_id"] = s.parentID, s.callID
	item.Metadata = metadata
	s.root.events.Record(item)
	if item.Kind == driver.TranscriptResult && item.Usage != nil {
		if s.root.usage == nil {
			s.root.usage = &driver.Usage{}
		}
		s.root.usage.InputTokens += item.Usage.InputTokens
		s.root.usage.OutputTokens += item.Usage.OutputTokens
		s.root.usage.CachedInputTokens += item.Usage.CachedInputTokens
		s.root.usage.EstimatedCostMilli += item.Usage.EstimatedCostMilli
	}
	return s.root.events.Err
}

func (s *delegatedSink) EmitStream(event driver.StreamPayload) error {
	switch event.Kind {
	case driver.StreamRunStarted, driver.StreamRunFinished, driver.StreamRunError:
		return errors.New("opencode: delegated session cannot open a second run lifecycle")
	}
	event.ThreadID = s.sessionID
	event.MessageID = childIdentifier(s.sessionID, event.MessageID)
	event.ToolCallID = childIdentifier(s.sessionID, event.ToolCallID)
	if event.Kind == driver.StreamStepStarted || event.Kind == driver.StreamStepFinished {
		event.Name = childIdentifier(s.sessionID, event.Name)
	}
	if event.Raw == nil {
		event.Raw = map[string]any{}
	}
	event.Raw["opencode.session_id"], event.Raw["opencode.parent_session_id"], event.Raw["opencode.parent_tool_call_id"] = s.sessionID, s.parentID, s.callID
	s.root.events.Stream(event)
	return s.root.events.Err
}

func (s *delegatedSink) RequestDecision(ctx context.Context, request driver.DecisionRequest) (driver.DecisionResponse, error) {
	sink, ok := s.root.events.Sink.(driver.DecisionCapableSink)
	if !ok {
		return driver.DecisionResponse{}, fmt.Errorf("opencode: delegated approval requires a decision-capable sink")
	}
	return sink.RequestDecision(ctx, request)
}
