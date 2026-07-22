package codebuddy

import (
	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/internal/mcpruntime"
)

func profileAndKind(config agentadaptor.CommonConfig, selection *agentadaptor.ProfileSelection) (agentadaptor.AgentProfile, agentadaptor.ProfileKind) {
	profile := resolveProfile(config, selection)
	return profile, mcpruntime.ClassifyProfile(profile, canonicalSharedConfigDir(config.Env))
}
