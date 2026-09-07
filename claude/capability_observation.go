package claude

import (
	"errors"
	"strings"
	"time"

	"github.com/agent-dance/agent-adaptor/capability"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/capabilityobs"
)

func (p *claudeParser) configureObservations(req driver.Request) {
	o := p.observations()
	entries := []capabilityobs.Entry{}
	for _, s := range req.Skills.Entries {
		entries = append(entries, capabilityobs.Entry{Kind: capability.Skill, RuntimeName: s.RuntimeName, Key: s.Key})
	}
	for _, a := range req.ProfilePayload.Agents.Agents {
		entries = append(entries, capabilityobs.Entry{Kind: capability.Subagent, RuntimeName: a.RuntimeName, Key: a.Key})
	}
	names := map[string]bool{}
	for _, server := range req.MCP.Servers {
		for _, alias := range []string{server.Key, claudeMCPAlias(server.Key)} {
			entries = append(entries, capabilityobs.Entry{Kind: capability.MCP, RuntimeName: alias, Key: server.Key})
			if !names[alias] {
				names[alias] = true
				o.mcpNames = append(o.mcpNames, alias)
			}
		}
	}
	var err error
	o.catalog, err = capabilityobs.NewCatalog(entries)
	if err != nil {
		p.observationNotice("capability_catalog_invalid")
	}
}

// Claude's server namespace replaces each non-ASCII identifier rune by one
// underscore. Keep original names as exact aliases; never trim separators or
// choose the longest prefix, since underscores are also legal operation bytes.
func claudeMCPAlias(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

func (p *claudeParser) resolveMCPTool(name string) (capability.Ref, error) {
	o := p.observations()
	candidates := map[capability.Ref]bool{}
	ambiguous := false
	for _, alias := range o.mcpNames {
		prefix := "mcp__" + alias + "__"
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		operation := strings.TrimPrefix(name, prefix)
		if !capabilityobs.ValidText(operation, 256, true) {
			continue
		}
		key, err := o.catalog.Lookup(capability.MCP, alias)
		if errors.Is(err, capabilityobs.ErrAmbiguous) {
			ambiguous = true
			continue
		}
		if err == nil {
			candidates[capability.Ref{Kind: capability.MCP, Key: key, Operation: operation}] = true
		}
	}
	if ambiguous || len(candidates) > 1 {
		return capability.Ref{}, capabilityobs.ErrAmbiguous
	}
	for ref := range candidates {
		return ref, nil
	}
	return capability.Ref{}, capabilityobs.ErrUnknown
}

func (p *claudeParser) observeCapability(call *observedTool) {
	o := p.observations()
	if o.catalog == nil || call.conflict {
		return
	}
	var ref capability.Ref
	var err error
	switch call.name {
	case "Skill":
		ref.Kind, ref.Operation = capability.Skill, "activate"
		ref.Key, err = o.catalog.Lookup(ref.Kind, claudeExactString(call.input, "skill"))
	case "Agent", "Task":
		ref.Kind, ref.Operation = capability.Subagent, "spawn"
		ref.Key, err = o.catalog.Lookup(ref.Kind, claudeExactString(call.input, "subagent_type"))
	default:
		ref, err = p.resolveMCPTool(call.name)
	}
	if err != nil {
		if errors.Is(err, capabilityobs.ErrAmbiguous) {
			p.observationNotice("capability_ambiguous")
		}
		return
	}
	fact, err := o.tracker.Start(capability.Invocation{InvocationID: call.key.id, Ref: ref, Phase: capability.Started, Evidence: capability.ProviderProtocol, Source: capability.Provider, ScopeID: call.scope.id, ParentScopeID: call.scope.parentScope, ParentToolCallID: call.scope.parentID, OccurredAt: time.Now().UTC()})
	if err != nil {
		p.observationNotice("capability_invalid")
		return
	}
	if fact != nil {
		p.stream.emitStream(driver.StreamPayload{Kind: driver.StreamCapabilityInvocation, Capability: fact})
	}
}
