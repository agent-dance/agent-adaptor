package claude

import (
	"path/filepath"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/internal/mcpruntime"
	"github.com/agent-dance/agent-adaptor/internal/skillruntime"
)

func canonicalSharedClaudeConfigDir(bindings []agentadaptor.EnvBinding) string {
	return filepath.Join(skillruntime.ResolveHome(bindings), ".claude")
}

func claudeProfileAndKind(config agentadaptor.CommonConfig, selection *agentadaptor.ProfileSelection) (agentadaptor.AgentProfile, agentadaptor.ProfileKind) {
	profile := claudeProfile(config, selection)
	return profile, mcpruntime.ClassifyProfile(profile, canonicalSharedClaudeConfigDir(config.Env))
}

func syncClaudeMCPProfile(config agentadaptor.CommonConfig, selection *agentadaptor.ProfileSelection, payload agentadaptor.MCPPayload) error {
	profile, kind := claudeProfileAndKind(config, selection)
	return mcpruntime.SyncClaudeProfile(profile.Dir, kind, payload)
}
