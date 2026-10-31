package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/agent-dance/agent-adaptor/driver"
)

// Check the provider's resolved config before initializing its MCP clients.
// Higher-precedence managed configuration must not re-enable suppressed slots.
func (c *serverClient) validateMCPStartup(ctx context.Context, payload driver.MCPPayload, disabled []string) error {
	if len(payload.Servers) == 0 && len(disabled) == 0 {
		return nil
	}
	raw, err := c.call(ctx, "GET", "/config", nil)
	if err != nil {
		return err
	}
	var config struct {
		MCP map[string]struct {
			Enabled *bool `json:"enabled"`
		} `json:"mcp"`
	}
	if err := decodeNativeObject(raw, &config); err != nil {
		return fmt.Errorf("opencode: malformed resolved MCP config: %w", err)
	}
	keys := make([]string, 0, len(config.MCP)+len(payload.Servers))
	for key, entry := range config.MCP {
		if entry.Enabled == nil || *entry.Enabled {
			keys = append(keys, key)
		}
	}
	suppressed := append([]string(nil), disabled...)
	for _, spec := range payload.Servers {
		keys = append(keys, spec.Key)
		suppressed = append(suppressed, spec.Key)
	}
	sort.Strings(suppressed)
	for _, key := range suppressed {
		entry, exists := config.MCP[key]
		if !exists || entry.Enabled == nil || *entry.Enabled {
			return fmt.Errorf("opencode: native configuration did not disable MCP server %q before installation", key)
		}
	}
	sort.Strings(keys)
	return validateMCPNames(keys)
}

func runtimeMCPEntries(payload driver.MCPPayload, env []driver.EnvBinding) (map[string]map[string]any, error) {
	entries := make(map[string]map[string]any, len(payload.Servers))
	for _, spec := range payload.Servers {
		entry, err := nativeMCPEntry(spec, env, false)
		if err != nil {
			return nil, err
		}
		entries[spec.Key] = entry
	}
	return entries, nil
}

// POST /mcp connects with the submitted native config, bypassing config-file
// deep merging and template expansion. The startup overlay disables the old
// slots, so they cannot connect with stale credentials before replacement.
func (c *serverClient) installMCP(ctx context.Context, entries map[string]map[string]any) error {
	keys := make([]string, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		raw, err := c.call(ctx, "POST", "/mcp", map[string]any{"name": key, "config": entries[key]})
		if err != nil {
			return err
		}
		var result map[string]struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			return fmt.Errorf("opencode: malformed MCP installation status: %w", err)
		}
		switch result[key].Status {
		case "connected", "failed", "needs_auth", "needs_client_registration":
		default:
			return fmt.Errorf("opencode: missing or invalid MCP installation status for %q", key)
		}
	}
	return nil
}

// The native /mcp endpoint reports actual connection status after config load.
// Required declarations are checked before creating/delivering a session prompt.
func (c *serverClient) requireMCP(ctx context.Context, payload driver.MCPPayload, sink driver.EventSink) error {
	if len(payload.Servers) == 0 {
		return nil
	}
	raw, err := c.call(ctx, "GET", "/mcp", nil)
	if err != nil {
		return err
	}
	var status map[string]struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(raw, &status); err != nil {
		return fmt.Errorf("opencode: malformed MCP connection status: %w", err)
	}
	for _, spec := range payload.Servers {
		observed := status[spec.Key].Status
		switch observed {
		case "connected", "disabled", "failed", "needs_auth", "needs_client_registration":
		default:
			return fmt.Errorf("opencode: missing or invalid MCP connection status for %q", spec.Key)
		}
		if err := sink.Emit(driver.RunEvent{Type: driver.RunEventInvocation, Text: "OpenCode MCP connection status", Data: map[string]any{"mcp_key": spec.Key, "status": observed, "required": spec.Required}}); err != nil {
			return err
		}
		if spec.Required && observed != "connected" {
			return fmt.Errorf("opencode: required MCP server %q is not connected", spec.Key)
		}
	}
	return nil
}
