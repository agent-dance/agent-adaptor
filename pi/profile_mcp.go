package pi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
	"github.com/agent-dance/agent-adaptor/internal/profilejson"
)

const mcpProfileFile = "agent-adaptor-mcp.json"

func mcpProfileLayout(root string) profilejson.Layout {
	return profilejson.Layout{Provider: DriverType, Root: root, File: mcpProfileFile, Field: "servers", Kind: driver.ProfileResourceMCP, Materialization: driver.ProfileResourceMaterializationFileManaged}
}

func mcpProfileEntries(payload driver.MCPPayload) (map[string]json.RawMessage, error) {
	validated, err := engine.ResolveMCPPayload(&engine.MCPConfig{Servers: payload.Servers}, nil, configuredDriver{}.Descriptor().MCP)
	if err != nil {
		return nil, err
	}
	entries := map[string]json.RawMessage{}
	for _, spec := range validated.Servers {
		raw, err := json.Marshal(spec)
		if err != nil {
			return nil, err
		}
		entries[spec.Key] = raw
	}
	return entries, nil
}

// Pi has no native MCP client. Durable declarations are file-managed input to
// this Driver's official Pi extension, with fresh per-invocation connections.
func resolveProfileMCP(req driver.Request, root string) (driver.MCPPayload, error) {
	servers := map[string]driver.MCPServerSpec{}
	if !req.ProfilePayload.Declared.MCP {
		entries, err := profilejson.ReadEntries(root, mcpProfileFile, "servers")
		if err != nil {
			return driver.MCPPayload{}, err
		}
		for key, raw := range entries {
			var spec driver.MCPServerSpec
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&spec); err != nil {
				return driver.MCPPayload{}, fmt.Errorf("pi: invalid persistent MCP declaration %q: %w", key, err)
			}
			if spec.Key != "" && spec.Key != key {
				return driver.MCPPayload{}, fmt.Errorf("pi: persistent MCP key mismatch %q", key)
			}
			spec.Key = key
			servers[key] = spec
		}
	}
	for _, spec := range req.MCP.Servers {
		servers[spec.Key] = spec
	}
	var effective []driver.MCPServerSpec
	for _, spec := range servers {
		effective = append(effective, spec)
	}
	sort.Slice(effective, func(i, j int) bool { return effective[i].Key < effective[j].Key })
	return engine.ResolveMCPPayload(&engine.MCPConfig{Servers: effective}, nil, configuredDriver{}.Descriptor().MCP)
}
