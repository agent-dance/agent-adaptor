package codex

import (
	"path/filepath"
	"strconv"

	"github.com/agent-dance/agent-adaptor/v2/driver"
	"github.com/agent-dance/agent-adaptor/v2/internal/engine"
	"github.com/agent-dance/agent-adaptor/v2/internal/mcpruntime"
	"github.com/agent-dance/agent-adaptor/v2/internal/skillruntime"
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
	var args []string
	for _, server := range payload.Servers {
		// Use the same resolved server config as profile materialization. This
		// preserves an explicit server default when that API is introduced,
		// while exact tool policies retain their native Codex precedence.
		entry, err := mcpruntime.CodexServerConfig(server, policy)
		if err != nil {
			return nil, err
		}
		key := "mcp_servers." + strconv.Quote(server.Key) + "."
		args = append(args, "-c", key+"default_tools_approval_mode="+strconv.Quote(entry["default_tools_approval_mode"].(string)),
			"-c", key+"enabled="+strconv.FormatBool(entry["enabled"].(bool)))
	}
	return args, nil
}
