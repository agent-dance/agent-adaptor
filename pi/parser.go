package pi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"sort"
	"strings"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/clihelper"
	"github.com/agent-dance/agent-adaptor/internal/driverutil"
)

type contentBlock struct {
	Type      string         `json:"type"`
	Text      string         `json:"text"`
	Thinking  string         `json:"thinking"`
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}
type message struct {
	Role         string          `json:"role"`
	Content      json.RawMessage `json:"content"`
	Model        string          `json:"model"`
	Provider     string          `json:"provider"`
	StopReason   string          `json:"stopReason"`
	ErrorMessage string          `json:"errorMessage"`
	Usage        *struct {
		Input      int `json:"input"`
		Output     int `json:"output"`
		CacheRead  int `json:"cacheRead"`
		CacheWrite int `json:"cacheWrite"`
		Cost       struct {
			Total float64 `json:"total"`
		} `json:"cost"`
	} `json:"usage"`
}
type event struct {
	Type                  string    `json:"type"`
	ID                    string    `json:"id"`
	Message               *message  `json:"message"`
	Messages              []message `json:"messages"`
	WillRetry             bool      `json:"willRetry"`
	AssistantMessageEvent struct {
		Type         string        `json:"type"`
		ContentIndex int           `json:"contentIndex"`
		Delta        string        `json:"delta"`
		ID           string        `json:"id"`
		ToolName     string        `json:"toolName"`
		ToolCall     *contentBlock `json:"toolCall"`
	} `json:"assistantMessageEvent"`
	ToolCallID string         `json:"toolCallId"`
	ToolName   string         `json:"toolName"`
	Args       map[string]any `json:"args"`
	Result     map[string]any `json:"result"`
	IsError    bool           `json:"isError"`
}
type parser struct {
	events         driverutil.ProtocolSink
	lines          driverutil.Lines
	sessionID      string
	agentStarted   bool
	terminal       *driver.TerminalPayload
	malformed      bool
	settled        bool
	healthy        bool
	errorMessage   string
	text           string
	model          string
	provider       string
	usage          *driver.Usage
	messageNumber  int
	assistantOpen  bool
	lastStopReason string
	open           map[string]driver.StreamKind
	seen           map[string]bool
	tools          map[string]string
	callNames      map[string]string
	callEnds       map[string]bool
	callBlocks     map[int]string
}

func newParser(sink driver.EventSink, streaming bool) *parser {
	return &parser{events: driverutil.ProtocolSink{Sink: sink, Streaming: streaming}, open: map[string]driver.StreamKind{}, seen: map[string]bool{}, tools: map[string]string{}, callNames: map[string]string{}, callEnds: map[string]bool{}, callBlocks: map[int]string{}}
}
func (p *parser) line(raw []byte) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return
	}
	var e event
	if err := json.Unmarshal(raw, &e); err != nil || e.Type == "" {
		p.malformed = true
		return
	}
	if p.settled {
		p.malformed = true
		return
	}
	switch e.Type {
	case "session":
		if e.ID == "" || p.agentStarted || p.sessionID != "" {
			p.malformed = true
			return
		}
		p.sessionID = e.ID
		p.events.Record(driver.TranscriptItem{Kind: driver.TranscriptInit, SessionID: e.ID})
	case "agent_start":
		if p.agentStarted && p.terminal == nil {
			p.malformed = true
			return
		}
		p.agentStarted = true
		p.healthy = false
		p.errorMessage = ""
		p.terminal = nil
		p.lastStopReason = ""
	case "message_start":
		if !p.agentStarted || e.Message == nil || p.terminal != nil || p.assistantOpen {
			p.malformed = true
			return
		}
		if e.Message.Role == "assistant" {
			p.assistantOpen = true
			p.messageNumber++
			p.seen = map[string]bool{}
			p.callBlocks = map[int]string{}
		}
	case "message_update":
		if !p.assistantOpen || p.terminal != nil {
			p.malformed = true
			return
		}
		p.delta(e)
	case "message_end":
		if !p.agentStarted || e.Message == nil || p.terminal != nil {
			p.malformed = true
			return
		}
		if e.Message.Role == "assistant" {
			if !p.assistantOpen {
				p.malformed = true
				return
			}
			p.assistantOpen = false
		}
		p.message(*e.Message)
	case "tool_execution_start":
		if !p.agentStarted || p.terminal != nil || e.ToolCallID == "" || e.ToolName == "" || p.tools[e.ToolCallID] != "" {
			p.malformed = true
			return
		}
		p.tools[e.ToolCallID] = e.ToolName
		p.events.Record(driver.TranscriptItem{Kind: driver.TranscriptToolCall, ToolUseID: e.ToolCallID, ToolName: e.ToolName, Input: e.Args})
		p.startCall(e.ToolCallID, e.ToolName, e.Args)
		p.endCall(e.ToolCallID)
	case "tool_execution_end":
		if p.terminal != nil || e.ToolCallID == "" || e.ToolName == "" || p.tools[e.ToolCallID] != e.ToolName || e.Result == nil {
			p.malformed = true
			return
		}
		encoded, _ := json.Marshal(e.Result)
		var result struct {
			Content []contentBlock `json:"content"`
		}
		_ = json.Unmarshal(encoded, &result)
		var texts []string
		for _, block := range result.Content {
			if block.Type == "text" {
				texts = append(texts, block.Text)
			}
		}
		p.events.Record(driver.TranscriptItem{Kind: driver.TranscriptToolResult, ToolUseID: e.ToolCallID, ToolName: e.ToolName, Text: strings.Join(texts, "\n"), IsError: e.IsError, Data: e.Result})
		streamResult := maps.Clone(e.Result)
		streamResult["isError"] = e.IsError
		p.events.Stream(driver.StreamPayload{Kind: driver.StreamToolCallResult, ToolCallID: e.ToolCallID, Result: streamResult})
		delete(p.tools, e.ToolCallID)
	case "agent_end":
		if !p.agentStarted || p.terminal != nil || p.assistantOpen {
			p.malformed = true
		}
		p.terminal = &driver.TerminalPayload{Event: e.Type, JSON: bytes.Clone(raw)}
		p.healthy = false
		for i := len(e.Messages) - 1; i >= 0; i-- {
			if m := e.Messages[i]; m.Role == "assistant" {
				p.healthy = p.agentStarted && !e.WillRetry && m.StopReason == p.lastStopReason && (m.StopReason == "stop" || m.StopReason == "length")
				if m.StopReason == "error" || m.StopReason == "aborted" {
					p.errorMessage = m.ErrorMessage
					if p.errorMessage == "" {
						p.errorMessage = "Pi response " + m.StopReason
					}
				}
				break
			}
		}
		p.events.Record(driver.TranscriptItem{Kind: driver.TranscriptResult, SessionID: p.sessionID, IsError: !p.healthy})
	case "agent_settled":
		if p.terminal == nil {
			p.malformed = true
		}
		p.settled = true
	case "turn_start", "turn_end", "tool_execution_update":
		// message_end and tool_execution_end carry the authoritative snapshots.
	default:
		p.events.Record(driverutil.UnknownRecord(raw, e.Type))
	}
}
func (p *parser) blockID(index int, thinking bool) string {
	return fmt.Sprintf("pi-%d-%d-%t", p.messageNumber, index, thinking)
}
func (p *parser) start(id string, thinking bool) {
	if _, ok := p.open[id]; ok {
		return
	}
	kind := driver.StreamTextStart
	if thinking {
		kind = driver.StreamReasoningStart
	}
	p.open[id] = kind
	p.seen[id] = true
	p.events.Stream(driver.StreamPayload{Kind: kind, MessageID: id})
}
func (p *parser) end(id string) {
	kind, ok := p.open[id]
	if !ok {
		return
	}
	end := driver.StreamTextEnd
	if kind == driver.StreamReasoningStart {
		end = driver.StreamReasoningEnd
	}
	p.events.Stream(driver.StreamPayload{Kind: end, MessageID: id})
	delete(p.open, id)
}
func (p *parser) delta(e event) {
	a := e.AssistantMessageEvent
	if a.ContentIndex < 0 {
		p.malformed = true
		return
	}
	if strings.HasPrefix(a.Type, "toolcall_") {
		switch a.Type {
		case "toolcall_start":
			if a.ID == "" || a.ToolName == "" || p.callBlocks[a.ContentIndex] != "" || p.callNames[a.ID] != "" {
				p.malformed = true
				return
			}
			p.callBlocks[a.ContentIndex] = a.ID
			p.startCall(a.ID, a.ToolName, nil)
		case "toolcall_delta":
			callID := p.callBlocks[a.ContentIndex]
			if callID == "" || p.callEnds[callID] {
				p.malformed = true
				return
			}
			if a.Delta != "" {
				p.events.Stream(driver.StreamPayload{Kind: driver.StreamToolCallArgs, ToolCallID: callID, Delta: a.Delta})
			}
		case "toolcall_end":
			callID := p.callBlocks[a.ContentIndex]
			if a.ToolCall == nil || callID == "" || a.ToolCall.ID != callID || a.ToolCall.Name != p.callNames[callID] || p.callEnds[callID] {
				p.malformed = true
				return
			}
			p.endCall(callID)
		}
		return
	}
	thinking := strings.HasPrefix(a.Type, "thinking_")
	id := p.blockID(a.ContentIndex, thinking)
	switch a.Type {
	case "text_start", "thinking_start":
		p.start(id, thinking)
	case "text_delta", "thinking_delta":
		p.start(id, thinking)
		kind := driver.StreamTextContent
		if thinking {
			kind = driver.StreamReasoningContent
		}
		if a.Delta != "" {
			p.events.Stream(driver.StreamPayload{Kind: kind, MessageID: id, Delta: a.Delta})
		}
	case "text_end", "thinking_end":
		p.end(id)
	}
}

func (p *parser) startCall(id, name string, args map[string]any) {
	if previous := p.callNames[id]; previous != "" {
		if previous != name {
			p.malformed = true
		}
		return
	}
	p.callNames[id] = name
	p.events.Stream(driver.StreamPayload{Kind: driver.StreamToolCallStart, ToolCallID: id, Name: name, Args: args})
}
func (p *parser) endCall(id string) {
	if !p.callEnds[id] {
		p.callEnds[id] = true
		p.events.Stream(driver.StreamPayload{Kind: driver.StreamToolCallEnd, ToolCallID: id})
	}
}
func (p *parser) message(m message) {
	var blocks []contentBlock
	content := bytes.TrimSpace(m.Content)
	if m.Role == "assistant" && (len(content) == 0 || content[0] != '[') {
		p.malformed = true
		return
	}
	if len(content) > 0 && content[0] == '[' {
		if err := json.Unmarshal(m.Content, &blocks); err != nil {
			p.malformed = true
			return
		}
	}
	if m.Role == "assistant" {
		p.lastStopReason = m.StopReason
		p.model = m.Model
		p.provider = m.Provider
		if sink, ok := p.events.Sink.(interface{ observeModel(string, string) }); ok {
			sink.observeModel(m.Model, m.Provider)
		}
		var text strings.Builder
		for i, block := range blocks {
			thinking := block.Type == "thinking"
			if block.Type != "text" && !thinking {
				continue
			}
			value := block.Text
			kind := driver.TranscriptAssistant
			if thinking {
				value = block.Thinking
				kind = driver.TranscriptThinking
			} else {
				text.WriteString(value)
			}
			id := p.blockID(i, thinking)
			if value != "" {
				p.events.Record(driver.TranscriptItem{Kind: kind, Text: value})
				if !p.seen[id] {
					p.start(id, thinking)
					streamKind := driver.StreamTextContent
					if thinking {
						streamKind = driver.StreamReasoningContent
					}
					p.events.Stream(driver.StreamPayload{Kind: streamKind, MessageID: id, Delta: value})
				}
			}
			p.end(id)
		}
		p.text = text.String()
		if m.Usage != nil {
			if p.usage == nil {
				p.usage = &driver.Usage{}
			}
			p.usage.InputTokens += m.Usage.Input + m.Usage.CacheRead + m.Usage.CacheWrite
			p.usage.OutputTokens += m.Usage.Output
			p.usage.CachedInputTokens += m.Usage.CacheRead
			p.usage.EstimatedCostMilli += int64(math.Round(m.Usage.Cost.Total * 1000))
		}
	} else if m.Role == "user" {
		var text string
		if len(blocks) > 0 {
			for _, block := range blocks {
				if block.Type == "text" {
					text += block.Text
				}
			}
		} else {
			_ = json.Unmarshal(m.Content, &text)
		}
		if text != "" {
			p.events.Record(driver.TranscriptItem{Kind: driver.TranscriptUser, Text: text})
		}
	}
}
func (p *parser) finish(result clihelper.CommandResult, runErr error) driver.Response {
	response := p.buildResponse(result, runErr)
	p.complete(&response, runErr)
	return response
}
func (p *parser) buildResponse(result clihelper.CommandResult, runErr error) driver.Response {
	var failure *driver.RunFailure
	message := p.errorMessage
	if p.malformed {
		message = "Pi JSON event protocol was malformed"
	} else if !p.healthy || p.terminal == nil || p.assistantOpen || len(p.open) > 0 || len(p.tools) > 0 || len(p.callEnds) != len(p.callNames) {
		if message == "" {
			message = "Pi ended without a successful agent_end"
		}
	}
	if runErr != nil {
		message = runErr.Error()
	} else if result.ExitCode != 0 || result.Signal != "" || result.TimedOut {
		message = "Pi process did not exit successfully"
	}
	if message != "" && runErr == nil {
		failure = &driver.RunFailure{Code: driver.FailureAgentError, Message: message}
	}
	keys := make([]string, 0, len(p.open))
	for key := range p.open {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		p.end(key)
	}
	keys = keys[:0]
	for key := range p.callNames {
		if !p.callEnds[key] {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		p.endCall(key)
	}
	raw := result.RawStreams
	raw.Terminal = p.terminal
	response := driver.Response{Output: p.text, RawStreams: &raw, Transcript: p.events.Transcript, Usage: p.usage, Failure: failure, Provider: p.provider, Model: p.model, ExitCode: result.ExitCode, Signal: result.Signal, TimedOut: result.TimedOut}
	if failure == nil && runErr == nil && p.events.Err == nil && p.sessionID != "" {
		response.Checkpoint = &driver.Checkpoint{Valid: true, State: &driver.SessionState{ResumeID: p.sessionID, DisplayID: p.sessionID}}
	}
	return response
}

func (p *parser) complete(response *driver.Response, runErr error) {
	terminal := driver.StreamPayload{Kind: driver.StreamRunFinished, Usage: p.usage}
	if response.Failure != nil {
		terminal.Kind = driver.StreamRunError
		terminal.Error = response.Failure
	}
	if runErr != nil {
		code := driver.FailureAgentError
		if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
			code = driver.FailureCancelled
		}
		terminal.Kind, terminal.Error = driver.StreamRunError, &driver.RunFailure{Code: code, Message: runErr.Error()}
	}
	p.events.Stream(terminal)
	if runErr != nil || p.events.Err != nil || response.Failure != nil {
		response.Checkpoint = nil
	}
}
