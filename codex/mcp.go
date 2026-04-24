package codex

import (
	"path/filepath"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/internal/mcpruntime"
	"github.com/agent-dance/agent-adaptor/internal/skillruntime"
)

func canonicalSharedCodexHome(bindings []agentadaptor.EnvBinding) string {
	return filepath.Join(skillruntime.ResolveHome(bindings), ".codex")
}

func syncCodexMCPProfile(config agentadaptor.CommonConfig, selection *agentadaptor.ProfileSelection, agent agentadaptor.AgentIdentity, codexHome string, payload agentadaptor.MCPPayload) error {
	profile := codexProfile(config, selection, agent)
	kind := mcpruntime.ClassifyProfile(profile, canonicalSharedCodexHome(config.Env))
	return mcpruntime.SyncCodexProfile(codexHome, kind, payload)
}
