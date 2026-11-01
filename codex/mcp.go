package codex

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/agent-dance/agent-adaptor/v2/driver"
	"github.com/agent-dance/agent-adaptor/v2/internal/engine"
	"github.com/agent-dance/agent-adaptor/v2/internal/mcpruntime"
	"github.com/agent-dance/agent-adaptor/v2/internal/skillruntime"
	toml "github.com/pelletier/go-toml/v2"
)

func canonicalSharedCodexHome(bindings []driver.EnvBinding) string {
	return filepath.Join(skillruntime.ResolveHome(bindings), ".codex")
}

func codexProfileAndKind(config driver.CommonConfig, selection *driver.ProfileSelection, agent driver.AgentIdentity) (driver.AgentProfile, engine.ProfileKind) {
	profile := codexProfile(config, selection, agent)
	return profile, mcpruntime.ClassifyProfile(profile, canonicalSharedCodexHome(config.Env))
}

func syncCodexMCPProfile(config driver.CommonConfig, selection *driver.ProfileSelection, agent driver.AgentIdentity, codexHome string, payload driver.MCPPayload) error {
	_, kind := codexProfileAndKind(config, selection, agent)
	return mcpruntime.SyncCodexProfile(codexHome, kind, payload)
}

// codexMCPPolicyArgs makes the policy invocation-local even when two Agents
// materialize the same shared profile. Native servers not declared by this
// invocation retain their owner's configuration.
func codexMCPPolicyArgs(payload driver.MCPPayload, policy driver.RunPolicy) ([]string, error) {
	if len(payload.Servers) == 0 {
		return nil, nil
	}
	servers := make(map[string]any, len(payload.Servers))
	for _, server := range payload.Servers {
		// Use the same resolved server config as profile materialization. This
		// preserves an explicit server default when that API is introduced,
		// while exact tool policies retain their native Codex precedence.
		entry, err := mcpruntime.CodexServerConfig(server, policy)
		if err != nil {
			return nil, err
		}
		if _, duplicate := servers[server.Key]; duplicate {
			return nil, fmt.Errorf("%w: duplicate Codex MCP server key %q", engine.ErrInvalidMCPConfig, server.Key)
		}
		// Literal stdio environment values are already materialized in the
		// profile and retained by Codex's recursive table merge. Keep them out
		// of argv; bearer_token_env_var is only a variable name, never its value.
		delete(entry, "env")
		servers[server.Key] = entry
	}
	// Codex splits -c paths on every dot without parsing quoted segments.
	// Encode keys inside the TOML value instead, including each transport so
	// the overlay cannot create an orphan server with only approval settings.
	var encoded bytes.Buffer
	if err := toml.NewEncoder(&encoded).SetTablesInline(true).Encode(map[string]any{"mcp_servers": servers}); err != nil {
		return nil, fmt.Errorf("encode Codex MCP invocation override: %w", err)
	}
	return []string{"-c", strings.TrimSpace(encoded.String())}, nil
}
