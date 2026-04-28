package cursor

import (
	"path/filepath"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/internal/mcpruntime"
	"github.com/agent-dance/agent-adaptor/internal/skillruntime"
)

func canonicalSharedCursorHome(bindings []agentadaptor.EnvBinding) string {
	return filepath.Join(skillruntime.ResolveHome(bindings), ".cursor")
}

func cursorProfileAndKind(config agentadaptor.CommonConfig, selection *agentadaptor.ProfileSelection) (agentadaptor.AgentProfile, agentadaptor.ProfileKind) {
	profile := cursorProfile(config, selection)
	return profile, mcpruntime.ClassifyProfile(profile, canonicalSharedCursorHome(config.Env))
}

func syncCursorMCPProfile(config agentadaptor.CommonConfig, selection *agentadaptor.ProfileSelection, payload agentadaptor.MCPPayload) error {
	profile, kind := cursorProfileAndKind(config, selection)
	return mcpruntime.SyncCursorProfile(profile.Dir, kind, payload)
}
