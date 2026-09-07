package cursor

import (
	"crypto/sha256"
	"encoding/json"
	"time"

	"github.com/agent-dance/agent-adaptor/capability"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/capabilityobs"
)

// Cursor's print protocol identifies an MCP server separately from its tool.
// No Claude aliases, slash commands, paths, or natural-language names apply.
// Bounds cover both active calls and replay tombstones for this one run.
const cursorObservationLimit = 4096

type cursorCapabilityObservation struct {
	catalog     *capabilityobs.Catalog
	tracker     *capabilityobs.Tracker
	refs        map[string]capability.Ref
	startHashes map[string][32]byte
	notices     map[string]bool
}

func (p *cursorParser) configureCapabilities(req driver.Request) {
	p.capabilities = &cursorCapabilityObservation{tracker: capabilityobs.NewTracker(), refs: map[string]capability.Ref{}, notices: map[string]bool{}, startHashes: map[string][32]byte{}}
	if len(req.MCP.Servers)+len(req.ProfilePayload.Agents.Agents) > cursorObservationLimit {
		p.capabilityNotice("catalog_limit")
		return
	}
	entries := make([]capabilityobs.Entry, 0, len(req.MCP.Servers)+len(req.ProfilePayload.Agents.Agents))
	for _, server := range req.MCP.Servers {
		entries = append(entries, capabilityobs.Entry{Kind: capability.MCP, RuntimeName: server.Key, Key: server.Key})
	}
	for _, agent := range req.ProfilePayload.Agents.Agents {
		entries = append(entries, capabilityobs.Entry{Kind: capability.Subagent, RuntimeName: agent.RuntimeName, Key: agent.Key})
	}
	catalog, err := capabilityobs.NewCatalog(entries)
	if err != nil {
		p.capabilityNotice("invalid_catalog")
		return
	}
	p.capabilities.catalog = catalog
}

// observeCapability is called under the parser dispatch lock, after the
// existing formal envelope/session checks. Tracker transitions and publication
// are serialized together; no producer can publish a terminal before its start.
func (p *cursorParser) observeCapability(id, variant, subtype string, call map[string]any) {
	c := p.capabilities
	if c == nil || c.catalog == nil || p.protocolMalformed {
		return
	}
	args := cursorObject(call, "args")
	var kind capability.Kind
	var name, operation string
	switch variant {
	case "mcpToolCall":
		kind = capability.MCP
		name = cursorExactString(args, "serverIdentifier")
		// providerIdentifier is the older, separately fixture-proven spelling.
		// A present serverIdentifier is authoritative, including invalid values.
		if _, present := args["serverIdentifier"]; !present {
			name = cursorExactString(args, "providerIdentifier")
		}
		for _, field := range []string{"toolName", "name"} {
			value, present := args[field]
			if !present {
				continue
			}
			text, ok := value.(string)
			if !ok || !capabilityobs.ValidText(text, 256, true) || operation != "" && operation != text {
				p.capabilityNotice("invalid_reference")
				return
			}
			operation = text
		}
	case "taskToolCall":
		kind = capability.Subagent
		name = cursorExactString(cursorObject(cursorObject(args, "subagentType"), "custom"), "name")
		operation = "spawn"
	default:
		return
	}
	key, err := c.catalog.Lookup(kind, name)
	if err != nil {
		p.capabilityNotice("unresolved_reference")
		return
	}
	ref := capability.Ref{Kind: kind, Key: key, Operation: operation}
	if !capabilityobs.ValidText(id, 2048, true) || !capabilityobs.ValidText(operation, 256, true) {
		p.capabilityNotice("invalid_reference")
		return
	}
	at := time.Now().UTC()
	if subtype == "started" {
		canonical, _ := json.Marshal(args)
		digest := sha256.Sum256(canonical)
		if old, exists := c.startHashes[id]; exists && old != digest {
			p.capabilityNotice("lifecycle_conflict")
			return
		}
		if _, exists := c.refs[id]; !exists && len(c.refs) >= cursorObservationLimit {
			p.capabilityNotice("invocation_limit")
			return
		}
		value, err := c.tracker.Start(capability.Invocation{InvocationID: id, Ref: ref, Phase: capability.Started, Evidence: capability.ProviderProtocol, Source: capability.Provider, OccurredAt: at})
		if err != nil {
			p.capabilityNotice("lifecycle_conflict")
			return
		}
		c.refs[id] = ref
		c.startHashes[id] = digest
		p.emitCapability(value)
		return
	}
	// A terminal must correlate to the exact previously accepted identity.
	// Missing, changed or ambiguous catalog evidence cannot close another call.
	if old, exists := c.refs[id]; !exists || old != ref {
		p.capabilityNotice("unresolved_terminal")
		return
	}
	phase, code, ok := cursorCapabilityResult(cursorObject(call, "result"))
	if !ok {
		p.capabilityNotice("unrecognized_result")
		return
	}
	value, err := c.tracker.Terminal(capabilityobs.Key{InvocationID: id}, phase, code, at, nil)
	if err != nil {
		p.capabilityNotice("lifecycle_conflict")
		return
	}
	p.emitCapability(value)
}

// Only explicit, fixture-proven result variants establish an outcome. A
// nonempty object, an unknown case, or conflicting oneof members prove none.
func cursorCapabilityResult(result map[string]any) (capability.Phase, capability.ErrorCode, bool) {
	candidates := 0
	var success bool
	for _, key := range []string{"success", "failure", "error", "result"} {
		raw, exists := result[key]
		if !exists {
			continue
		}
		candidates++
		switch key {
		case "success":
			switch v := raw.(type) {
			case bool:
				success = v
			case map[string]any:
				success = true
			default:
				return "", "", false
			}
		case "failure", "error":
			if _, ok := raw.(map[string]any); !ok {
				return "", "", false
			}
			success = false
		case "result":
			oneof, ok := raw.(map[string]any)
			if !ok {
				return "", "", false
			}
			if _, ok := oneof["value"].(map[string]any); !ok {
				return "", "", false
			}
			switch cursorExactString(oneof, "case") {
			case "success":
				success = true
			case "error", "rejected", "permissionDenied", "toolNotFound", "serverNotFound":
				success = false
			default:
				return "", "", false
			}
		}
	}
	if candidates != 1 {
		return "", "", false
	}
	if success {
		return capability.Completed, "", true
	}
	return capability.Failed, capability.ToolFailed, true
}

func cursorExactString(object map[string]any, key string) string {
	value, _ := object[key].(string)
	return value
}

func (p *cursorParser) emitCapability(value *capability.Invocation) {
	if value != nil && p.sink != nil {
		_ = p.sink.EmitStream(driver.StreamPayload{Kind: driver.StreamCapabilityInvocation, Capability: value})
	}
}

func (p *cursorParser) capabilityNotice(reason string) {
	if p.capabilities == nil || p.capabilities.notices[reason] {
		return
	}
	p.capabilities.notices[reason] = true
	if p.sink != nil {
		_ = p.sink.Emit(driver.RunEvent{Type: driver.RunEventRuntime, Text: "Cursor capability observation unavailable for an unrecognized fact", Data: map[string]any{"code": "cursor_observation_degraded", "reason": reason}})
	}
}

// A successful run is not proof that an outstanding tool succeeded. Close
// pending facts on every process outcome, retaining healthy checkpoint rules.
func (p *cursorParser) closeCapabilities(cancelled bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.capabilities == nil {
		return
	}
	phase, code := capability.Interrupted, capability.RunInterrupted
	if cancelled {
		phase, code = capability.Cancelled, capability.RunCancelled
	} else if p.protocolMalformed {
		phase, code = capability.Failed, capability.ProtocolError
	}
	values, _ := p.capabilities.tracker.Close(phase, code, time.Now().UTC())
	for i := range values {
		p.emitCapability(&values[i])
	}
}
