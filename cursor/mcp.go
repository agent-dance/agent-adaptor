package cursor

import (
	"path/filepath"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
	"github.com/agent-dance/agent-adaptor/internal/mcpruntime"
	"github.com/agent-dance/agent-adaptor/internal/skillruntime"
)

func canonicalSharedCursorHome(bindings []driver.EnvBinding) string {
	return filepath.Join(skillruntime.ResolveHome(bindings), ".cursor")
}

func cursorProfileAndKind(config driver.CommonConfig, selection *driver.ProfileSelection) (driver.AgentProfile, engine.ProfileKind) {
	profile := cursorProfile(config, selection)
	return profile, mcpruntime.ClassifyProfile(profile, canonicalSharedCursorHome(config.Env))
}

func syncCursorMCPProfile(config driver.CommonConfig, selection *driver.ProfileSelection, payload driver.MCPPayload) error {
	profile, kind := cursorProfileAndKind(config, selection)
	return mcpruntime.SyncCursorProfile(profile.Dir, kind, payload)
}
