package codebuddy

import (
	"github.com/agent-dance/agent-adaptor/v2/driver"
	"github.com/agent-dance/agent-adaptor/v2/internal/engine"
	"github.com/agent-dance/agent-adaptor/v2/internal/mcpruntime"
)

func profileAndKind(config CommonConfig, selection *driver.ProfileSelection) (driver.AgentProfile, engine.ProfileKind) {
	profile := resolveProfile(config, selection)
	return profile, mcpruntime.ClassifyProfile(profile, canonicalSharedConfigDir(config.Env))
}
