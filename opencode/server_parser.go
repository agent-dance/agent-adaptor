package opencode

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/driverutil"
	"github.com/agent-dance/agent-adaptor/internal/engine"
)

// These wire types follow the official 1.14.30 server schema. Unknown payloads
// remain raw system records; they never manufacture assistant text or state.
type serverEvent struct {
	Type       string          `json:"type"`
	Properties json.RawMessage `json:"properties"`
}
type serverError struct {
	Name string `json:"name"`
	Data struct {
		Message string `json:"message"`
	} `json:"data"`
}

func (e *serverError) message() string {
	if e.Data.Message != "" {
		return e.Data.Message
	}
	return e.Name
}

type serverMessage struct {
	ID         string          `json:"id"`
	SessionID  string          `json:"sessionID"`
	Role       string          `json:"role"`
	ParentID   string          `json:"parentID"`
	ProviderID string          `json:"providerID"`
	ModelID    string          `json:"modelID"`
	Finish     string          `json:"finish"`
	Summary    json.RawMessage `json:"summary"`
	Time       struct {
		Completed int64 `json:"completed"`
	} `json:"time"`
	Error      *serverError    `json:"error"`
	Structured json.RawMessage `json:"structured"`
}
type serverPart struct {
	part
	Time *struct {
		Start int64  `json:"start"`
		End   *int64 `json:"end"`
	} `json:"time"`
}
type partState struct {
	part   serverPart
	text   string
	ended  bool
	args   bool
	result bool
}
type serverParser struct {
	events                                               driverutil.ProtocolSink
	sessionID, lastMessage, protocolError, providerError string
	messages                                             map[string]serverMessage
	parts                                                map[string]*partState
	order                                                []string
	steps                                                map[string]bool
	decisions                                            map[string]bool
	usage                                                *driver.Usage
	terminal                                             *driver.TerminalPayload
	idle, active                                         bool
	child                                                bool
	delegated                                            map[string]*delegatedSession
}

func newServerParser(sink driver.EventSink, streaming bool) *serverParser {
	return &serverParser{events: driverutil.ProtocolSink{Sink: sink, Streaming: streaming}, messages: map[string]serverMessage{}, parts: map[string]*partState{}, steps: map[string]bool{}, decisions: map[string]bool{}, delegated: map[string]*delegatedSession{}}
}
func (p *serverParser) consume(raw []byte) (serverEvent, error) {
	var e serverEvent
	if json.Unmarshal(raw, &e) != nil || e.Type == "" || !json.Valid(e.Properties) {
		return e, errors.New("OpenCode SSE event is malformed")
	}
	var props struct {
		SessionID string         `json:"sessionID"`
		Info      *serverMessage `json:"info"`
		Part      *serverPart    `json:"part"`
		MessageID string         `json:"messageID"`
		PartID    string         `json:"partID"`
		Field     string         `json:"field"`
		Delta     string         `json:"delta"`
		Status    struct {
			Type string `json:"type"`
		} `json:"status"`
		Error *serverError `json:"error"`
	}
	if err := json.Unmarshal(e.Properties, &props); err != nil {
		return e, err
	}
	// The instance bus also carries other sessions and background operations.
	// Only explicit, matching session IDs enter this invocation's semantics.
	if props.SessionID != "" && props.SessionID != p.sessionID {
		return serverEvent{}, nil
	}
	switch e.Type {
	case "server.connected", "server.heartbeat":
		return e, nil
	case "session.status", "session.idle", "session.error", "message.updated", "message.part.updated", "message.part.delta", "permission.asked", "question.asked":
		if props.SessionID == "" {
			return e, fmt.Errorf("OpenCode %s lacks sessionID", e.Type)
		}
	default:
		p.events.Record(driverutil.UnknownRecord(raw, e.Type))
		return e, nil
	}
	switch e.Type {
	case "session.status":
		if props.Status.Type == "busy" {
			p.active = true
		}
		if props.Status.Type == "idle" && p.active {
			p.idle = true
		}
	case "session.idle":
		if p.active {
			p.idle = true
		}
	case "session.error":
		if props.Error == nil || props.Error.message() == "" {
			return e, errors.New("OpenCode session.error lacks error")
		}
		p.providerError = props.Error.message()
		p.terminal = &driver.TerminalPayload{Event: e.Type, JSON: bytes.Clone(raw)}
		p.events.Record(driver.TranscriptItem{Kind: driver.TranscriptFailure, Text: p.providerError, IsError: true, Data: map[string]any{"event": json.RawMessage(bytes.Clone(raw))}})
	case "message.updated":
		m := props.Info
		if m == nil || m.SessionID != p.sessionID || m.ID == "" || (m.Role != "assistant" && m.Role != "user") {
			return e, errors.New("OpenCode message.updated has invalid info")
		}
		if old, ok := p.messages[m.ID]; ok && old.Role != m.Role {
			return e, errors.New("OpenCode message changed role")
		}
		p.messages[m.ID] = *m
		if m.Role == "assistant" && !bytes.Equal(m.Summary, []byte("true")) {
			p.active = true
			p.lastMessage = m.ID
			if m.Error != nil {
				p.providerError = m.Error.message()
				p.terminal = &driver.TerminalPayload{Event: e.Type, JSON: bytes.Clone(raw)}
			}
		}
	case "message.part.updated":
		b := props.Part
		if b == nil || b.SessionID != p.sessionID || b.ID == "" || b.MessageID == "" {
			return e, errors.New("OpenCode message.part.updated has invalid part")
		}
		m, ok := p.messages[b.MessageID]
		if !ok {
			return e, errors.New("OpenCode part precedes its message")
		}
		if m.Role != "assistant" || bytes.Equal(m.Summary, []byte("true")) {
			p.events.Record(driverutil.UnknownRecord(raw, e.Type))
			return e, nil
		}
		if err := p.updatePart(*b, raw); err != nil {
			return e, err
		}
	case "message.part.delta":
		if m, ok := p.messages[props.MessageID]; ok && (m.Role != "assistant" || bytes.Equal(m.Summary, []byte("true"))) {
			p.events.Record(driverutil.UnknownRecord(raw, e.Type))
			return e, nil
		}
		s := p.parts[props.PartID]
		if s == nil || s.part.MessageID != props.MessageID || s.ended || props.Field != "text" || (s.part.Type != "text" && s.part.Type != "reasoning") {
			return e, errors.New("OpenCode delta lacks an active matching text part")
		}
		s.text += props.Delta
		kind := driver.StreamTextContent
		if s.part.Type == "reasoning" {
			kind = driver.StreamReasoningContent
		}
		p.events.Stream(driver.StreamPayload{Kind: kind, MessageID: props.PartID, Delta: props.Delta})
	}
	return e, nil
}

func (p *serverParser) updatePart(b serverPart, raw []byte) error {
	s, exists := p.parts[b.ID]
	if exists && (s.part.Type != b.Type || s.part.MessageID != b.MessageID || s.part.CallID != b.CallID) {
		return errors.New("OpenCode part identity changed")
	}
	if !exists {
		s = &partState{part: b}
		p.parts[b.ID] = s
		p.order = append(p.order, b.ID)
	}
	previous := s.part
	s.part = b
	switch b.Type {
	case "text", "reasoning":
		start, content, end, transcript := driver.StreamTextStart, driver.StreamTextContent, driver.StreamTextEnd, driver.TranscriptAssistant
		if b.Type == "reasoning" {
			start, content, end, transcript = driver.StreamReasoningStart, driver.StreamReasoningContent, driver.StreamReasoningEnd, driver.TranscriptThinking
		}
		if !exists {
			p.events.Stream(driver.StreamPayload{Kind: start, MessageID: b.ID})
		}
		if s.ended {
			if previous.Text != b.Text {
				return errors.New("OpenCode completed text changed")
			}
			return nil
		}
		// Snapshots can fill missing deltas, but a provider plugin may transform
		// the completed text. In that case retain the authoritative snapshot in
		// Transcript/Result without replaying already delivered deltas.
		if strings.HasPrefix(b.Text, s.text) && len(b.Text) > len(s.text) {
			p.events.Stream(driver.StreamPayload{Kind: content, MessageID: b.ID, Delta: b.Text[len(s.text):]})
		}
		s.text = b.Text
		if b.Time != nil && b.Time.End != nil {
			s.ended = true
			p.events.Record(driver.TranscriptItem{Kind: transcript, Text: b.Text})
			p.events.Stream(driver.StreamPayload{Kind: end, MessageID: b.ID})
		}
	case "step-start":
		if !exists {
			if p.steps[b.MessageID] {
				return errors.New("OpenCode step already active")
			}
			p.steps[b.MessageID] = true
			p.events.Stream(driver.StreamPayload{Kind: driver.StreamStepStarted, Name: b.MessageID})
		}
	case "step-finish":
		if exists {
			return nil
		}
		if !p.steps[b.MessageID] {
			return errors.New("OpenCode step finish without start")
		}
		delete(p.steps, b.MessageID)
		p.events.Stream(driver.StreamPayload{Kind: driver.StreamStepFinished, Name: b.MessageID})
		var usage *driver.Usage
		if b.Tokens != nil {
			usage = &driver.Usage{InputTokens: b.Tokens.Input + b.Tokens.Cache.Read + b.Tokens.Cache.Write, OutputTokens: b.Tokens.Output + b.Tokens.Reasoning, CachedInputTokens: b.Tokens.Cache.Read}
			if b.Cost != nil {
				usage.EstimatedCostMilli = int64(math.Round(*b.Cost * 1000))
			}
			if p.usage == nil {
				p.usage = &driver.Usage{}
			}
			p.usage.InputTokens += usage.InputTokens
			p.usage.OutputTokens += usage.OutputTokens
			p.usage.CachedInputTokens += usage.CachedInputTokens
			p.usage.EstimatedCostMilli += usage.EstimatedCostMilli
		}
		p.events.Record(driver.TranscriptItem{Kind: driver.TranscriptResult, SessionID: p.sessionID, Usage: usage, CostUSD: b.Cost, Subtype: b.Reason})
	case "tool":
		if b.CallID == "" || b.Tool == "" {
			return errors.New("OpenCode tool part lacks identity")
		}
		status := b.State.Status
		if status != "pending" && status != "running" && status != "completed" && status != "error" {
			return errors.New("OpenCode tool status is invalid")
		}
		if s.result {
			if status != previous.State.Status {
				return errors.New("OpenCode completed tool changed status")
			}
			return nil
		}
		if status != "pending" && !s.args {
			s.args = true
			p.events.Record(driver.TranscriptItem{Kind: driver.TranscriptToolCall, ToolUseID: b.CallID, ToolName: b.Tool, Input: b.State.Input})
			// The server exposes complete arguments, not argument deltas. Delay
			// the public start until that snapshot exists, matching the declared
			// ToolCallArgs=false contract (including pending-to-running updates).
			p.events.Stream(driver.StreamPayload{Kind: driver.StreamToolCallStart, ToolCallID: b.CallID, Name: b.Tool, Args: b.State.Input})
			p.events.Stream(driver.StreamPayload{Kind: driver.StreamToolCallEnd, ToolCallID: b.CallID})
			s.ended = true
		}
		if status == "completed" || status == "error" {
			s.result = true
			text := b.State.Output
			if status == "error" {
				text = b.State.Error
			}
			var payload map[string]any
			_ = json.Unmarshal(raw, &payload)
			p.events.Record(driver.TranscriptItem{Kind: driver.TranscriptToolResult, ToolUseID: b.CallID, ToolName: b.Tool, Text: text, IsError: status == "error", Data: payload})
			p.events.Stream(driver.StreamPayload{Kind: driver.StreamToolCallResult, ToolCallID: b.CallID, Result: map[string]any{"output": text, "isError": status == "error", "provider": payload}})
		}
	default:
		p.events.Record(driverutil.UnknownRecord(raw, "message.part.updated"))
	}
	return nil
}

func (p *serverParser) finish(response driver.Response, terminal []byte, runErr error, req driver.Request) driver.Response {
	response = p.buildResponse(response, terminal, runErr, req)
	p.complete(&response, runErr)
	return response
}

func (p *serverParser) buildResponse(response driver.Response, terminal []byte, runErr error, req driver.Request) driver.Response {
	if response.RawStreams == nil {
		response.RawStreams = &driver.RawStreams{}
	}
	response.RawStreams.Terminal = p.terminal
	message := p.providerError
	if p.protocolError != "" {
		message = p.protocolError
	}
	var final struct {
		Info  serverMessage `json:"info"`
		Parts []serverPart  `json:"parts"`
	}
	healthy := false
	if len(terminal) > 0 {
		response.RawStreams.Terminal = &driver.TerminalPayload{Event: "session.message", JSON: bytes.Clone(terminal)}
		if json.Unmarshal(terminal, &final) != nil || final.Parts == nil || final.Info.ID != p.lastMessage || final.Info.SessionID != p.sessionID || final.Info.Role != "assistant" || final.Info.Time.Completed == 0 {
			message = "OpenCode terminal message is malformed or incomplete"
		} else {
			response.Model, response.Provider = final.Info.ModelID, final.Info.ProviderID
			if final.Info.Error != nil {
				message = final.Info.Error.message()
			}
			healthy = final.Info.Finish == "stop" || final.Info.Finish == "length"
			if len(final.Info.Structured) > 0 && !bytes.Equal(final.Info.Structured, []byte("null")) {
				healthy = true
				if req.OutputSchema != nil && req.StructuredOutputSource == driver.StructuredOutputSourceNative {
					response.StructuredOutput = &driver.StructuredOutput{Format: driver.OutputFormatJSONSchema, Source: driver.StructuredOutputSourceNative, RawJSON: bytes.Clone(final.Info.Structured)}
				}
			}
			seen := map[string]bool{}
			for _, b := range final.Parts {
				if b.SessionID != p.sessionID || b.MessageID != final.Info.ID || b.ID == "" {
					message = "OpenCode terminal part has conflicting identity"
					break
				}
				s := p.parts[b.ID]
				if seen[b.ID] || s == nil || s.part.Type != b.Type || s.part.MessageID != b.MessageID || (b.Type == "text" && s.part.Text != b.Text) {
					message = "OpenCode terminal part disagrees with the event stream"
					break
				}
				seen[b.ID] = true
				if b.Type == "text" {
					response.Output += b.Text
				}
			}
			for id, part := range p.parts {
				if part.part.MessageID == final.Info.ID && !seen[id] {
					message = "OpenCode terminal message omitted an observed part"
				}
			}
		}
	}
	if message == "" && (!healthy || !p.idle || len(p.steps) != 0) {
		message = "OpenCode ended without a healthy terminal message and idle barrier"
	}
	// Close all started lifecycles even on abort, while refusing a checkpoint
	// when the official stream left tools or text incomplete.
	if incomplete := p.closeOpenParts(); incomplete != "" && message == "" {
		message = incomplete
	}
	if response.Output == "" && len(terminal) == 0 {
		for _, id := range p.order {
			s := p.parts[id]
			if s.part.Type == "text" && s.part.MessageID == p.lastMessage {
				response.Output += s.text
			}
		}
	}
	response.Usage = p.usage
	if message != "" && runErr == nil {
		response.Failure = &driver.RunFailure{Code: driver.FailureAgentError, Message: message}
	}
	if req.OutputSchema != nil && runErr == nil {
		response.StructuredOutput, response.Failure = engine.FinalizeStructuredOutput(req.OutputSchema, req.StructuredOutputSource, response.Output, response.StructuredOutput, response.Failure)
	}
	response.Transcript = p.events.Transcript
	if response.Failure == nil && runErr == nil && p.events.Err == nil && healthy && req.Session != nil && req.Session.Mode != driver.SessionStateless {
		response.Checkpoint = &driver.Checkpoint{Valid: true, State: &driver.SessionState{ResumeID: p.sessionID, DisplayID: p.sessionID}}
	}
	return response
}

func (p *serverParser) closeOpenParts() string {
	message := ""
	if len(p.steps) > 0 {
		message = "OpenCode ended with an incomplete step"
	}
	for _, id := range p.order {
		s := p.parts[id]
		if !s.ended && (s.part.Type == "text" || s.part.Type == "reasoning" || (s.part.Type == "tool" && s.args)) {
			if message == "" {
				message = "OpenCode ended with an incomplete part"
			}
			kind := driver.StreamTextEnd
			if s.part.Type == "reasoning" {
				kind = driver.StreamReasoningEnd
			}
			if s.part.Type == "tool" {
				kind = driver.StreamToolCallEnd
			}
			p.events.Stream(driver.StreamPayload{Kind: kind, MessageID: id, ToolCallID: s.part.CallID})
			s.ended = true
		}
		if s.part.Type == "tool" && !s.result && message == "" {
			message = "OpenCode ended with an incomplete tool result"
		}
	}
	for _, id := range p.order {
		s := p.parts[id]
		if s.part.Type == "step-start" && p.steps[s.part.MessageID] {
			p.events.Stream(driver.StreamPayload{Kind: driver.StreamStepFinished, Name: s.part.MessageID})
			delete(p.steps, s.part.MessageID)
		}
	}
	return message
}

func (p *serverParser) complete(response *driver.Response, runErr error) {
	terminalError := response.Failure
	if runErr != nil {
		terminalError = &driver.RunFailure{Code: driver.FailureAgentError, Message: runErr.Error()}
	}
	end := driver.StreamPayload{Kind: driver.StreamRunFinished, Usage: p.usage}
	if terminalError != nil {
		end.Kind, end.Error = driver.StreamRunError, terminalError
	}
	p.events.Stream(end)
	if runErr != nil || p.events.Err != nil {
		response.Checkpoint = nil
	}
}
