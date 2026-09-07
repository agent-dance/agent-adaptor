package codebuddy

import (
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
	"github.com/agent-dance/agent-adaptor/todo"
)

// observationState is run-local. The parser dispatch mutex covers both the
// normalized state transition and its publication to the single sink.
type observationState struct {
	ctx           context.Context
	catalog       *capabilityobs.Catalog
	servers       []string
	tracker       *capabilityobs.Tracker
	table         *todoobs.Table
	calls         map[string]*observedCall
	notices       map[string]bool
	suppressed    bool
	unresolvedIDs map[string]bool
}
type observedCall struct {
	name       string
	input      map[string]any
	result     map[string]any
	conflicted bool
	capability bool
}

func (p *parser) configureObservations(ctx context.Context, req driver.Request) {
	entries := make([]capabilityobs.Entry, 0)
	for _, v := range req.Skills.Entries {
		entries = append(entries, capabilityobs.Entry{Kind: capability.Skill, RuntimeName: v.RuntimeName, Key: v.Key})
	}
	servers := make([]string, 0, len(req.MCP.Servers))
	for _, v := range req.MCP.Servers {
		entries = append(entries, capabilityobs.Entry{Kind: capability.MCP, RuntimeName: v.Key, Key: v.Key})
		servers = append(servers, v.Key)
	}
	for _, v := range req.ProfilePayload.Agents.Agents {
		entries = append(entries, capabilityobs.Entry{Kind: capability.Subagent, RuntimeName: v.RuntimeName, Key: v.Key})
	}
	catalog, err := capabilityobs.NewCatalog(entries)
	table, _ := todoobs.NewTable(todoobs.Scope{})
	p.runID = req.RunID
	p.observation = &observationState{ctx: ctx, catalog: catalog, servers: servers, tracker: capabilityobs.NewTracker(), table: table, calls: map[string]*observedCall{}, notices: map[string]bool{}, unresolvedIDs: map[string]bool{}}
	if err != nil {
		p.observationNotice("observation_catalog_invalid")
	}
	// stream-json may carry partial wrappers even without public delta demand.
	// Reconstruct through the same parser while keeping public delta selection.
	p.enableOutputReconstruction(req.RunID)
}
func (p *parser) observationNotice(code string) {
	o := p.observation
	if o == nil || o.notices[code] {
		return
	}
	o.notices[code] = true
	if p.sink != nil {
		_ = p.sink.Emit(driver.RunEvent{Type: driver.RunEventRuntime, Data: map[string]any{"code": code}, Text: "CodeBuddy observation unavailable for a protocol fact."})
	}
}
func (p *parser) emitCapability(v *capability.Invocation, err error) {
	if err != nil {
		p.observationNotice("capability_protocol_conflict")
		return
	}
	if v != nil && p.sink != nil {
		_ = p.sink.EmitStream(driver.StreamPayload{Kind: driver.StreamCapabilityInvocation, RunID: p.runID, Capability: v})
	}
}
func (p *parser) observeToolUse(name, id string, input map[string]any) {
	o := p.observation
	if o == nil || id == "" {
		return
	}
	if o.suppressed {
		o.unresolvedIDs[id] = true
		if old := o.calls[id]; old != nil {
			old.conflicted = true
		}
		return
	}
	if o.unresolvedIDs[id] || input == nil {
		return
	}
	if old := o.calls[id]; old != nil {
		if old.name != name || !reflect.DeepEqual(old.input, input) {
			old.conflicted = true
			p.observationNotice("observation_tool_conflict")
		}
		return
	}
	// Retain input only for tools that can contribute a formal observed fact.
	if name != "Skill" && name != "Task" && name != "Agent" && !strings.HasPrefix(name, "mcp__") && !todoTool(name) {
		return
	}
	call := &observedCall{name: name, input: input}
	o.calls[id] = call
	ref, err := o.resolveCapability(name, input)
	if err != nil {
		if !todoTool(name) {
			p.observationNotice("capability_unresolved")
		}
		return
	}
	call.capability = true
	p.emitCapability(o.tracker.Start(capability.Invocation{InvocationID: id, Ref: ref, Phase: capability.Started, Evidence: capability.ProviderProtocol, Source: capability.Provider, OccurredAt: time.Now().UTC()}))
}
func (o *observationState) resolveCapability(name string, input map[string]any) (capability.Ref, error) {
	var kind capability.Kind
	var runtimeName, operation string
	switch name {
	case "Skill":
		kind = capability.Skill
		operation = "activate"
		runtimeName = exactString(input, "skill")
		command := exactString(input, "command")
		if runtimeName != "" && command != "" && runtimeName != command {
			return capability.Ref{}, capabilityobs.ErrAmbiguous
		}
		if runtimeName == "" {
			runtimeName = command
		}
	case "Task", "Agent":
		kind = capability.Subagent
		runtimeName = exactString(input, "subagent_type")
		operation = "spawn"
	default:
		// CodeBuddy 2.137.1 emits mcp__<exact server>__<exact tool>. Enumerate
		// every catalog prefix; underscores and Unicode are literal, not aliases.
		matches := map[capability.Ref]bool{}
		for _, server := range o.servers {
			suffix, ok := strings.CutPrefix(name, "mcp__"+server+"__")
			if !ok || suffix == "" {
				continue
			}
			key, err := o.catalog.Lookup(capability.MCP, server)
			if err != nil {
				return capability.Ref{}, err
			}
			matches[capability.Ref{Kind: capability.MCP, Key: key, Operation: suffix}] = true
		}
		if len(matches) != 1 {
			return capability.Ref{}, capabilityobs.ErrAmbiguous
		}
		for ref := range matches {
			return ref, nil
		}
	}
	key, err := o.catalog.Lookup(kind, runtimeName)
	return capability.Ref{Kind: kind, Key: key, Operation: operation}, err
}
func (p *parser) observeToolResult(block map[string]any) {
	o := p.observation
	if o == nil || o.suppressed {
		return
	}
	id := exactString(block, "tool_use_id")
	call := o.calls[id]
	if call == nil || call.conflicted {
		return
	}
	if call.result != nil {
		if !reflect.DeepEqual(call.result, block) {
			p.observationNotice("observation_result_conflict")
		}
		return
	}
	// A malformed success flag cannot certify execution. Absence is the
	// ToolResultBlock protocol's default false; explicit wrong types are invalid.
	failed := false
	if value, exists := block["is_error"]; exists {
		var ok bool
		failed, ok = value.(bool)
		if !ok {
			p.observationNotice("observation_result_invalid")
			return
		}
	}
	if !validObservedToolContent(block["content"]) {
		p.observationNotice("observation_result_invalid")
		return
	}
	call.result = block
	if call.capability {
		phase := capability.Completed
		code := capability.ErrorCode("")
		if failed {
			phase = capability.Failed
			code = capability.ToolFailed
		}
		p.emitCapability(o.tracker.Terminal(capabilityobs.Key{InvocationID: id}, phase, code, time.Now().UTC(), nil))
	}
	if !failed && todoTool(call.name) {
		p.confirmTodo(id, call, block)
	}
}
func (p *parser) closeObservations() {
	o := p.observation
	if o == nil {
		return
	}
	phase, code := capability.Interrupted, capability.RunInterrupted
	if o.ctx != nil && errors.Is(o.ctx.Err(), context.Canceled) {
		phase, code = capability.Cancelled, capability.RunCancelled
	}
	facts, err := o.tracker.Close(phase, code, time.Now().UTC())
	if err != nil {
		p.observationNotice("capability_protocol_conflict")
		return
	}
	for i := range facts {
		p.emitCapability(&facts[i], nil)
	}
}
func todoTool(name string) bool {
	return name == "TodoWrite" || name == "TaskCreate" || name == "TaskUpdate" || name == "TaskList"
}
func syntheticTodoID(runID, callID, index string) string {
	// JSON array + base64 is collision-free even for separator-bearing IDs.
	raw, _ := json.Marshal([]string{runID, "", callID, index})
	return "synthetic:" + base64.RawURLEncoding.EncodeToString(raw)
}
func (p *parser) publishTodo(snapshot *todo.Snapshot, err error) {
	if err != nil {
		code := "todo_invalid"
		if errors.Is(err, todoobs.ErrUnknownID) {
			code = "todo_unknown_id"
		}
		p.observationNotice(code)
		return
	}
	if snapshot != nil && p.sink != nil {
		_ = p.sink.EmitStream(driver.StreamPayload{Kind: driver.StreamTodoUpdated, RunID: p.runID, Todo: snapshot})
	}
}

func validObservedToolContent(value any) bool {
	switch v := value.(type) {
	case string:
		return true
	case []any:
		for _, entry := range v {
			block, ok := entry.(map[string]any)
			if !ok {
				return false
			}
			switch exactString(block, "type") {
			case "text":
				if _, ok := block["text"].(string); !ok {
					return false
				}
			case "image":
				if topObject(block, "source") == nil {
					return false
				}
			default:
				return false
			}
		}
		return true
	default:
		return false
	}
}

func observationParentUnproved(payload map[string]any) bool {
	value, exists := payload["parent_tool_use_id"]
	if !exists || value == nil {
		return false
	}
	text, ok := value.(string)
	return !ok || text != ""
}
