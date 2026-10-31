package codex

import (
	"path/filepath"

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
