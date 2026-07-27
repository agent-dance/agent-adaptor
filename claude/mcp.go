package claude

import (
	"path/filepath"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
	"github.com/agent-dance/agent-adaptor/internal/mcpruntime"
	"github.com/agent-dance/agent-adaptor/internal/skillruntime"
)

func canonicalSharedClaudeConfigDir(bindings []driver.EnvBinding) string {
	return filepath.Join(skillruntime.ResolveHome(bindings), ".claude")
}

func claudeProfileAndKind(config driver.CommonConfig, selection *driver.ProfileSelection) (driver.AgentProfile, engine.ProfileKind) {
	profile := claudeProfile(config, selection)
	return profile, mcpruntime.ClassifyProfile(profile, canonicalSharedClaudeConfigDir(config.Env))
}

func syncClaudeMCPProfile(config driver.CommonConfig, selection *driver.ProfileSelection, payload driver.MCPPayload) error {
	profile, kind := claudeProfileAndKind(config, selection)
	return mcpruntime.SyncClaudeProfile(profile.Dir, kind, payload)
}
