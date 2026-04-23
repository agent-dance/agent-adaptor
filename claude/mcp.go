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

func syncClaudeMCPProfile(config agentadaptor.CommonConfig, payload agentadaptor.MCPPayload) error {
	profile := claudeProfile(config)
	kind := mcpruntime.ClassifyProfile(profile, canonicalSharedClaudeConfigDir(config.Env))
	return mcpruntime.SyncClaudeProfile(profile.Dir, kind, payload)
}
