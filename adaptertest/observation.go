package adaptertest

import (
	"fmt"
	"reflect"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/agent-dance/agent-adaptor/capability"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/todo"
)

// These checks intentionally use only public SPI/leaf values, without calling
// the implementation's catalog, tracker, table or Event validation helpers.
func verifyDescriptorSnapshot(d driver.Driver) []Violation {
	snapshot := d.Descriptor()
	for _, get := range []func(driver.Descriptor) *driver.StructuredOutputHITLCapability{
		func(d driver.Descriptor) *driver.StructuredOutputHITLCapability { return d.StructuredOutput.NativeHITL },
		func(d driver.Descriptor) *driver.StructuredOutputHITLCapability {
			return d.StructuredOutput.PromptValidateHITL
		},
	} {
		matrix := get(snapshot)
		if matrix == nil {
			continue
		}
		original := *matrix
		matrix.Permission = !matrix.Permission
		fresh := get(d.Descriptor())
		independent := fresh != nil && *fresh == original
		*matrix = original
		if !independent {
			return []Violation{violationf("DRV-03", "Descriptor shares mutable HITL matrices between snapshots")}
		}
	}
	return nil
}

func verifyObservationSupport(s driver.ObservationSupport, payloads []driver.StreamPayload) []Violation {
	var out []Violation
	for i, p := range payloads {
		supported := true
		if p.Kind == driver.StreamTodoUpdated {
			supported = s.Todos
		}
		if p.Kind == driver.StreamCapabilityInvocation && p.Capability != nil {
			switch p.Capability.Ref.Kind {
			case capability.Skill:
				supported = s.Skills
			case capability.MCP:
				supported = s.MCP
			case capability.Subagent:
				supported = s.Subagents
			default:
				supported = false
			}
		}
		if !supported {
			out = append(out, violationf("OBS-01", "payload %d emits an observation unavailable in the resolved transport", i))
		}
	}
	return out
}

func observationString(s string, max int, required, text bool) bool {
	if (required && s == "") || len(s) > max || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) && !(text && (r == '\n' || r == '\t')) {
			return false
		}
	}
	return true
}
func observationTime(t time.Time) bool { return !t.IsZero() && t.Location() == time.UTC }
func observationParent(scope, parentScope, parentID, id string) bool {
	return observationString(scope, 2048, false, false) && observationString(parentScope, 2048, false, false) && observationString(parentID, 2048, false, false) &&
		(parentID != "" || parentScope == "") && !(parentID != "" && parentID == id && parentScope == scope)
}
func validCapability(v capability.Invocation) bool {
	if !observationString(v.InvocationID, 2048, true, false) || !observationString(v.Ref.Key, 512, true, false) || !observationString(v.Ref.Operation, 256, true, false) ||
		!observationParent(v.ScopeID, v.ParentScopeID, v.ParentToolCallID, "") || !observationTime(v.OccurredAt) {
		return false
	}
	switch v.Ref.Kind {
	case capability.Skill:
		if v.Ref.Operation != "activate" {
			return false
		}
	case capability.Subagent:
		if v.Ref.Operation != "spawn" {
			return false
		}
	case capability.MCP:
	default:
		return false
	}
	// A Driver is the provider boundary. Host/relay facts enter through the
	// run-bound publisher, never through Driver.EmitStream.
	if v.Source != capability.Provider || (v.Evidence != capability.ProviderProtocol && v.Evidence != capability.NativeInputAccepted) {
		return false
	}
	switch v.Phase {
	case capability.Started:
		if v.Duration != nil || v.ErrorCode != "" {
			return false
		}
	case capability.Completed:
		if v.ErrorCode != "" {
			return false
		}
	case capability.Failed, capability.Cancelled, capability.Interrupted:
	default:
		return false
	}
	if v.Duration != nil && *v.Duration < 0 {
		return false
	}
	switch v.ErrorCode {
	case "", capability.ToolFailed, capability.RunCancelled, capability.RunInterrupted, capability.ProtocolError, capability.DelegationFailed:
	default:
		return false
	}
	return true
}
func validTodo(v todo.Snapshot) bool {
	if v.Items == nil || len(v.Items) > 128 || v.Revision == 0 || !observationTime(v.OccurredAt) || !observationParent(v.ScopeID, v.ParentScopeID, v.ParentToolCallID, "") {
		return false
	}
	if v.Source != todo.ToolResult && v.Source != todo.PlanUpdate {
		return false
	}
	seen := map[string]bool{}
	for _, item := range v.Items {
		if !observationString(item.ID, 2048, true, false) || !observationString(item.Content, 4096, true, true) || seen[item.ID] {
			return false
		}
		seen[item.ID] = true
		switch item.Status {
		case todo.Pending, todo.InProgress, todo.Completed, todo.Cancelled:
		default:
			return false
		}
	}
	return true
}

func verifyObservationSequence(payloads []driver.StreamPayload) []Violation {
	var out []Violation
	type key struct{ scope, id string }
	invocations := map[key]capability.Invocation{}
	closed := map[key]bool{}
	snapshots := map[string]todo.Snapshot{}
	tools := map[key]driver.StreamPayload{}
	results := map[key]bool{}
	add := func(clause string, i int, message string) {
		out = append(out, violationf(clause, "payload %d: %s", i, message))
	}
	for i, p := range payloads {
		if p.Capability != nil && p.Kind != driver.StreamCapabilityInvocation {
			add("OBS-02", i, "Capability on another kind")
		}
		if p.Todo != nil && p.Kind != driver.StreamTodoUpdated {
			add("OBS-02", i, "Todo on another kind")
		}
		isObservation := p.Kind == driver.StreamCapabilityInvocation || p.Kind == driver.StreamTodoUpdated
		if isObservation && (p.Args != nil || p.Result != nil || p.Raw != nil || p.HITLRequested != nil || p.HITLResolved != nil || p.Role != "" || p.MessageID != "" || p.ToolCallID != "" || p.Name != "" || p.Delta != "" || p.Usage != nil || p.Error != nil || p.ScopeID != "" || p.ParentScopeID != "" || p.ParentToolCallID != "") {
			add("OBS-02", i, "observation carries a second semantic payload")
		}
		switch p.Kind {
		case driver.StreamCapabilityInvocation:
			if p.Capability == nil || p.Todo != nil {
				add("OBS-02", i, "capability.invocation requires only Capability")
				continue
			}
			v := *p.Capability
			if !validCapability(v) {
				add("OBS-03", i, "invalid capability value")
			}
			k := key{v.ScopeID, v.InvocationID}
			first, exists := invocations[k]
			if v.Phase == capability.Started {
				if exists {
					add("OBS-04", i, "capability start repeated in one scope")
				} else {
					invocations[k] = v
				}
			} else {
				if !exists || closed[k] {
					add("OBS-04", i, "capability terminal lacks a unique open start")
				} else if first.Ref != v.Ref || first.ParentScopeID != v.ParentScopeID || first.ParentToolCallID != v.ParentToolCallID || first.Source != v.Source || first.Evidence != v.Evidence {
					add("OBS-04", i, "capability identity changed within lifecycle")
				}
				closed[k] = true
			}
		case driver.StreamTodoUpdated:
			if p.Todo == nil || p.Capability != nil {
				add("OBS-02", i, "todo.updated requires only Todo")
				continue
			}
			v := *p.Todo
			if !validTodo(v) {
				add("OBS-05", i, "invalid full todo snapshot")
			}
			previous, exists := snapshots[v.ScopeID]
			if (!exists && v.Revision != 1) || (exists && v.Revision != previous.Revision+1) {
				add("OBS-06", i, "todo revision must advance from one within each scope")
			}
			if exists && reflect.DeepEqual(previous.Items, v.Items) && previous.Source == v.Source && previous.ParentScopeID == v.ParentScopeID && previous.ParentToolCallID == v.ParentToolCallID {
				add("OBS-06", i, "unchanged todo snapshot repeated")
			}
			snapshots[v.ScopeID] = v
		case driver.StreamToolCallStart:
			if !observationParent(p.ScopeID, p.ParentScopeID, p.ParentToolCallID, p.ToolCallID) {
				add("EVT-14", i, "invalid parent coordinates")
			}
			tools[key{p.ScopeID, p.ToolCallID}] = p
		case driver.StreamToolCallArgs, driver.StreamToolCallEnd, driver.StreamToolCallResult:
			k := key{p.ScopeID, p.ToolCallID}
			first, exists := tools[k]
			if exists && (first.ParentScopeID != p.ParentScopeID || first.ParentToolCallID != p.ParentToolCallID) {
				add("EVT-14", i, "tool parent changed within lifecycle")
			}
			if p.Kind == driver.StreamToolCallArgs && exists && first.Args != nil {
				add("EVT-14", i, "complete Args snapshot followed by ArgsDelta")
			}
			if p.Kind == driver.StreamToolCallResult {
				if results[k] {
					add("EVT-14", i, "tool result repeated within scope")
				}
				results[k] = true
			}
		case driver.StreamRunFinished, driver.StreamRunError:
			for k := range invocations {
				if !closed[k] {
					add("OBS-04", i, fmt.Sprintf("capability remains open at provider terminal in scope %q", k.scope))
				}
			}
		}
	}
	return out
}
