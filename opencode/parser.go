package opencode

import (
	"bytes"
	"encoding/json"
	"math"
	"sort"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/clihelper"
	"github.com/agent-dance/agent-adaptor/internal/driverutil"
)

type part struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	SessionID string `json:"sessionID"`
	MessageID string `json:"messageID"`
	Text      string `json:"text"`
	CallID    string `json:"callID"`
	Tool      string `json:"tool"`
	Reason    string `json:"reason"`
	State     struct {
		Status   string         `json:"status"`
		Input    map[string]any `json:"input"`
		Output   string         `json:"output"`
		Error    string         `json:"error"`
		Metadata map[string]any `json:"metadata"`
	} `json:"state"`
	Tokens *struct {
		Input     int `json:"input"`
		Output    int `json:"output"`
		Reasoning int `json:"reasoning"`
		Cache     struct {
			Read  int `json:"read"`
			Write int `json:"write"`
		} `json:"cache"`
	} `json:"tokens"`
	Cost *float64 `json:"cost"`
}
type event struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionID"`
	Part      *part  `json:"part"`
	Error     *struct {
		Name string `json:"name"`
		Data struct {
			Message string `json:"message"`
		} `json:"data"`
	} `json:"error"`
}
type parser struct {
	events       driverutil.ProtocolSink
	lines        driverutil.Lines
	sessionID    string
	started      bool
	malformed    bool
	errorMessage string
	terminal     *driver.TerminalPayload
	healthy      bool
	text         string
	textMessage  string
	usage        *driver.Usage
	seen         map[string]bool
	steps        map[string]bool
}

func newParser(sink driver.EventSink, streaming bool) *parser {
	return &parser{events: driverutil.ProtocolSink{Sink: sink, Streaming: streaming}, seen: map[string]bool{}, steps: map[string]bool{}}
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
	switch e.Type {
	case "step_start", "step_finish", "text", "reasoning", "tool_use", "error":
		if e.SessionID == "" {
			p.malformed = true
			return
		}
		if p.sessionID == "" {
			p.sessionID = e.SessionID
			p.events.Record(driver.TranscriptItem{Kind: driver.TranscriptInit, SessionID: e.SessionID})
		} else if p.sessionID != e.SessionID {
			p.malformed = true
			return
		}
	default:
		p.events.Record(driverutil.UnknownRecord(raw, e.Type))
		return
	}
	if e.Type == "error" {
		if e.Error == nil {
			p.malformed = true
			return
		}
		p.errorMessage = e.Error.Data.Message
		if p.errorMessage == "" {
			p.errorMessage = e.Error.Name
		}
		if p.errorMessage == "" {
			p.errorMessage = "OpenCode session error"
		}
		p.terminal = &driver.TerminalPayload{Event: e.Type, JSON: bytes.Clone(raw)}
		p.healthy = false
		p.events.Record(driver.TranscriptItem{Kind: driver.TranscriptFailure, Text: p.errorMessage, IsError: true})
		return
	}
	if e.Part == nil || e.Part.ID == "" || e.Part.MessageID == "" || e.Part.SessionID != e.SessionID {
		p.malformed = true
		return
	}
	b := e.Part
	if p.seen[b.ID] {
		p.malformed = true
		return
	}
	p.seen[b.ID] = true
	p.started = true
	switch e.Type {
	case "step_start":
		if b.Type != "step-start" || p.steps[b.MessageID] {
			p.malformed = true
			return
		}
		p.healthy = false
		if p.textMessage != b.MessageID {
			p.text = ""
			p.textMessage = b.MessageID
		}
		p.steps[b.MessageID] = true
		p.events.Stream(driver.StreamPayload{Kind: driver.StreamStepStarted, Name: b.MessageID})
	case "step_finish":
		if b.Type != "step-finish" || !p.steps[b.MessageID] {
			p.malformed = true
			return
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
		p.events.Record(driver.TranscriptItem{Kind: driver.TranscriptResult, SessionID: e.SessionID, Usage: usage, CostUSD: b.Cost, Subtype: b.Reason})
		p.terminal = &driver.TerminalPayload{Event: e.Type, JSON: bytes.Clone(raw)}
		p.healthy = b.Reason == "stop"
	case "text", "reasoning":
		if b.Type != e.Type || !p.steps[b.MessageID] {
			p.malformed = true
			return
		}
		if b.Text == "" {
			return
		}
		kind := driver.TranscriptAssistant
		start, delta, end := driver.StreamTextStart, driver.StreamTextContent, driver.StreamTextEnd
		if e.Type == "reasoning" {
			kind = driver.TranscriptThinking
			start, delta, end = driver.StreamReasoningStart, driver.StreamReasoningContent, driver.StreamReasoningEnd
		} else {
			if p.textMessage != b.MessageID {
				p.text = ""
				p.textMessage = b.MessageID
			}
			p.text += b.Text
		}
		p.events.Record(driver.TranscriptItem{Kind: kind, Text: b.Text})
		p.events.Stream(driver.StreamPayload{Kind: start, MessageID: b.ID})
		p.events.Stream(driver.StreamPayload{Kind: delta, MessageID: b.ID, Delta: b.Text})
		p.events.Stream(driver.StreamPayload{Kind: end, MessageID: b.ID})
	case "tool_use":
		if b.Type != "tool" || !p.steps[b.MessageID] || b.Tool == "" || b.CallID == "" || (b.State.Status != "completed" && b.State.Status != "error") {
			p.malformed = true
			return
		}
		p.events.Record(driver.TranscriptItem{Kind: driver.TranscriptToolCall, ToolUseID: b.CallID, ToolName: b.Tool, Input: b.State.Input})
		output := b.State.Output
		if b.State.Status == "error" {
			output = b.State.Error
		}
		p.events.Record(driver.TranscriptItem{Kind: driver.TranscriptToolResult, ToolUseID: b.CallID, ToolName: b.Tool, Text: output, IsError: b.State.Status == "error", Data: b.State.Metadata})
		result := map[string]any{"output": output, "isError": b.State.Status == "error"}
		p.events.Stream(driver.StreamPayload{Kind: driver.StreamToolCallStart, ToolCallID: b.CallID, Name: b.Tool, Args: b.State.Input})
		p.events.Stream(driver.StreamPayload{Kind: driver.StreamToolCallEnd, ToolCallID: b.CallID})
		p.events.Stream(driver.StreamPayload{Kind: driver.StreamToolCallResult, ToolCallID: b.CallID, Result: result})
	}
}
func (p *parser) finish(result clihelper.CommandResult, runErr error) driver.Response {
	message := p.errorMessage
	if p.malformed {
		message = "OpenCode run JSON protocol was malformed"
	} else if !p.healthy || p.terminal == nil || len(p.steps) > 0 {
		if message == "" {
			message = "OpenCode ended without a successful final step_finish"
		}
	}
	if runErr != nil {
		message = runErr.Error()
	} else if result.ExitCode != 0 || result.Signal != "" || result.TimedOut {
		message = "OpenCode process did not exit successfully"
	}
	var failure *driver.RunFailure
	if message != "" {
		failure = &driver.RunFailure{Code: driver.FailureAgentError, Message: message}
	}
	steps := make([]string, 0, len(p.steps))
	for key := range p.steps {
		steps = append(steps, key)
	}
	sort.Strings(steps)
	for _, key := range steps {
		p.events.Stream(driver.StreamPayload{Kind: driver.StreamStepFinished, Name: key})
	}
	terminal := driver.StreamPayload{Kind: driver.StreamRunFinished, Usage: p.usage}
	if failure != nil {
		terminal.Kind = driver.StreamRunError
		terminal.Error = failure
	}
	p.events.Stream(terminal)
	raw := result.RawStreams
	raw.Terminal = p.terminal
	response := driver.Response{Output: p.text, RawStreams: &raw, Transcript: p.events.Transcript, Usage: p.usage, Failure: failure, ExitCode: result.ExitCode, Signal: result.Signal, TimedOut: result.TimedOut}
	if failure == nil && p.events.Err == nil && p.sessionID != "" {
		response.Checkpoint = &driver.Checkpoint{Valid: true, State: &driver.SessionState{ResumeID: p.sessionID, DisplayID: p.sessionID}}
	}
	return response
}
