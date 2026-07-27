package codebuddy

import (
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
	"github.com/agent-dance/agent-adaptor/internal/mcpruntime"
)

func profileAndKind(config CommonConfig, selection *driver.ProfileSelection) (driver.AgentProfile, engine.ProfileKind) {
	profile := resolveProfile(config, selection)
	return profile, mcpruntime.ClassifyProfile(profile, canonicalSharedConfigDir(config.Env))
}
