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

func syncCursorMCPProfile(config agentadaptor.CommonConfig, payload agentadaptor.MCPPayload) error {
	profile := cursorProfile(config)
	kind := mcpruntime.ClassifyProfile(profile, canonicalSharedCursorHome(config.Env))
	return mcpruntime.SyncCursorProfile(profile.Dir, kind, payload)
}
