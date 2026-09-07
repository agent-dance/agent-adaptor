package claude

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"

	"github.com/agent-dance/agent-adaptor/capability"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/capabilityobs"
	"github.com/agent-dance/agent-adaptor/internal/todoobs"
)

type toolKey struct{ scope, id string }
type toolScope struct{ id, parentScope, parentID string }
type observedTool struct {
	key                                                  toolKey
	scope                                                toolScope
	name                                                 string
	input                                                map[string]any
	args                                                 strings.Builder
	incremental, ended, transcript, resultSeen, conflict bool
	result                                               any
}

// All state transitions and publications happen under claudeParser.mu. The
// neutral tracker cannot by itself order external sink calls.
type toolObservation struct {
	calls    map[toolKey]*observedTool
	order    []*observedTool
	notices  map[string]bool
	catalog  *capabilityobs.Catalog
	mcpNames []string
	tracker  *capabilityobs.Tracker
	tables   map[string]*todoobs.Table
	closed   bool
}

func (p *claudeParser) observations() *toolObservation {
	if p.tools == nil {
		p.tools = &toolObservation{calls: map[toolKey]*observedTool{}, notices: map[string]bool{}, tracker: capabilityobs.NewTracker(), tables: map[string]*todoobs.Table{}}
	}
	return p.tools
}

func (p *claudeParser) observationNotice(code string) {
	o := p.observations()
	if o.notices[code] {
		return
	}
	o.notices[code] = true
	if p.sink != nil {
		_ = p.sink.Emit(driver.RunEvent{Type: driver.RunEventRuntime, Text: "Claude protocol observation unavailable", Data: map[string]any{"code": code}})
	}
}

func claudeTuple(parts ...string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(parts)
	return "aa1:" + base64.RawURLEncoding.EncodeToString(bytes.TrimSuffix(b.Bytes(), []byte{'\n'}))
}

func (p *claudeParser) wrapperScope(wrapper map[string]any) (toolScope, bool) {
	raw, exists := wrapper["parent_tool_use_id"]
	if !exists || raw == nil {
		return toolScope{}, true
	}
	parent, ok := raw.(string)
	if !ok {
		p.observationNotice("parent_unresolved")
		return toolScope{}, false
	}
	return p.toolScope(parent)
}

func (p *claudeParser) toolScope(parent string) (toolScope, bool) {
	if parent == "" {
		return toolScope{}, true
	}
	var found *observedTool
	for key, call := range p.observations().calls {
		if key.id == parent {
			if found != nil {
				p.observationNotice("parent_unresolved")
				return toolScope{}, false
			}
			found = call
		}
	}
	if found == nil {
		p.observationNotice("parent_unresolved")
		return toolScope{}, false
	}
	scope := toolScope{claudeTuple("scope", found.key.scope, parent), found.key.scope, parent}
	if !capabilityobs.ValidText(scope.id, 2048, false) {
		p.observationNotice("parent_unresolved")
		return toolScope{}, false
	}
	return scope, true
}

func (p *claudeParser) toolPayload(call *observedTool, kind driver.StreamKind) driver.StreamPayload {
	return driver.StreamPayload{Kind: kind, ToolCallID: call.key.id, Name: call.name, ScopeID: call.scope.id, ParentScopeID: call.scope.parentScope, ParentToolCallID: call.scope.parentID}
}

func (p *claudeParser) startTool(scope toolScope, id, name string, input map[string]any, incremental bool) (*observedTool, bool) {
	o := p.observations()
	if o.closed || !capabilityobs.ValidText(id, 2048, true) || name == "" {
		p.observationNotice("tool_identity_missing")
		return nil, false
	}
	key := toolKey{scope.id, id}
	if old := o.calls[key]; old != nil {
		if old.name != name || old.scope != scope || (!incremental && old.input != nil && !reflect.DeepEqual(old.input, input)) {
			old.conflict = true
			p.observationNotice("tool_conflict")
		}
		return old, false
	}
	call := &observedTool{key: key, scope: scope, name: name, incremental: incremental}
	if !incremental {
		call.input = cloneToolInput(input)
	}
	o.calls[key] = call
	o.order = append(o.order, call)
	p.stream.markRunStarted()
	pl := p.toolPayload(call, driver.StreamToolCallStart)
	if !incremental {
		pl.Args = cloneToolInput(input)
	}
	p.stream.emitStream(pl)
	if !incremental {
		p.observeCapability(call)
	}
	return call, true
}

func cloneToolInput(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	raw, _ := json.Marshal(input)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}

func (p *claudeParser) appendToolArgs(call *observedTool, delta string) {
	if call == nil || call.ended || call.conflict {
		return
	}
	if !call.incremental {
		call.conflict = true
		p.observationNotice("tool_conflict")
		return
	}
	call.args.WriteString(delta)
	pl := p.toolPayload(call, driver.StreamToolCallArgs)
	pl.Delta = delta
	p.stream.emitStream(pl)
}

func (p *claudeParser) endTool(call *observedTool) {
	if call == nil || call.ended {
		return
	}
	if call.incremental {
		if call.args.Len() == 0 {
			call.input = map[string]any{}
		} else if err := json.Unmarshal([]byte(call.args.String()), &call.input); err != nil || call.input == nil {
			call.conflict = true
			p.observationNotice("tool_arguments_invalid")
		}
		if !call.conflict {
			p.observeCapability(call)
		}
	}
	call.ended = true
	p.stream.emitStream(p.toolPayload(call, driver.StreamToolCallEnd))
}

func (p *claudeParser) completeToolWrapper(block map[string]any, scope toolScope, resolved bool) {
	id, name := claudeExactString(block, "id"), claudeExactString(block, "name")
	input, validInput := block["input"].(map[string]any)
	item := driver.TranscriptItem{Kind: driver.TranscriptToolCall, ToolUseID: id, ToolName: name, Input: block["input"]}
	if !resolved || id == "" || !validInput {
		p.emit(item)
		if !validInput {
			p.observationNotice("tool_arguments_invalid")
		}
		return
	}
	call, fresh := p.startTool(scope, id, name, input, false)
	if call == nil {
		p.emit(item)
		return
	}
	if !fresh && call.incremental && !call.ended {
		p.endTool(call)
	}
	if call.input != nil && !reflect.DeepEqual(call.input, input) {
		call.conflict = true
		p.observationNotice("tool_conflict")
	}
	if call.conflict || call.transcript {
		return
	}
	p.endTool(call)
	call.transcript = true
	item.ScopeID, item.ParentScopeID, item.ParentToolCallID = scope.id, scope.parentScope, scope.parentID
	p.emit(item)
}

// A wrapper supplies one tool_use_result object. Only a single result block
// may consume it; multiple blocks do not provide a structured-result binding.
func (p *claudeParser) observeToolResult(block, wrapper map[string]any, structured any) (*observedTool, bool) {
	id := claudeExactString(block, "tool_use_id")
	if id == "" {
		return nil, true
	}
	if flag, exists := block["is_error"]; exists {
		if _, ok := flag.(bool); !ok {
			p.observationNotice("tool_result_invalid")
			return nil, true
		}
	}
	o := p.observations()
	var call *observedTool
	parent, hasParent := wrapper["parent_tool_use_id"]
	if hasParent {
		parentID, valid := parent.(string)
		if parent == nil {
			valid = true
		}
		if !valid {
			p.observationNotice("parent_unresolved")
			return nil, true
		}
		scope, resolved := p.toolScope(parentID)
		if !resolved {
			return nil, true
		}
		call = o.calls[toolKey{scope.id, id}]
	} else {
		// A bare result could replay any earlier call. Completed calls remain
		// candidates; selecting the only unfinished call would guess its scope.
		for key, candidate := range o.calls {
			if key.id == id {
				if call != nil {
					p.observationNotice("tool_result_ambiguous")
					return nil, true
				}
				call = candidate
			}
		}
	}
	if call == nil {
		p.observationNotice("tool_result_unresolved")
		return nil, true
	}
	result := map[string]any{"block": block, "structured": structured}
	if call.resultSeen {
		if !reflect.DeepEqual(call.result, result) {
			p.observationNotice("tool_result_conflict")
		}
		return call, false
	}
	call.resultSeen = true
	call.result = result
	p.endTool(call)
	pl := p.toolPayload(call, driver.StreamToolCallResult)
	isError, _ := block["is_error"].(bool)
	pl.Result = map[string]any{"text": claudeResultText(block["content"]), "is_error": isError, "tool_use_id": id}
	p.stream.emitStream(pl)
	if call.conflict {
		return call, true
	}
	phase, code := capability.Completed, capability.ErrorCode("")
	if isError {
		phase, code = capability.Failed, capability.ToolFailed
	}
	fact, err := o.tracker.Terminal(capabilityobs.Key{ScopeID: call.scope.id, InvocationID: id}, phase, code, time.Now().UTC(), nil)
	if err == nil && fact != nil {
		p.stream.emitStream(driver.StreamPayload{Kind: driver.StreamCapabilityInvocation, Capability: fact})
	}
	if !isError {
		p.observeTodo(call, structured)
	}
	return call, true
}

func (p *claudeParser) closeObservations(cause error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closeToolObservations(cause)
}

func (p *claudeParser) closeToolObservations(cause error) {
	if p.tools == nil || p.tools.closed || p.stream == nil {
		return
	}
	for _, call := range p.tools.order {
		if !call.ended {
			call.conflict = true
			p.endTool(call)
		}
	}
	p.tools.closed = true
	phase, code := capability.Interrupted, capability.RunInterrupted
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		phase, code = capability.Cancelled, capability.RunCancelled
	}
	facts, _ := p.tools.tracker.Close(phase, code, time.Now().UTC())
	for i := range facts {
		p.stream.emitStream(driver.StreamPayload{Kind: driver.StreamCapabilityInvocation, Capability: &facts[i]})
	}
}
