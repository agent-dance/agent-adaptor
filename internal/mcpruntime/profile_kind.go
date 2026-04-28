package mcpruntime

import (
	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/internal/profilekind"
)

type ProfileKind = agentadaptor.ProfileKind

const (
	ProfileKindShared      = agentadaptor.ProfileKindShared
	ProfileKindHostManaged = agentadaptor.ProfileKindHostManaged
	// ProfileKindDedicated is kept as an internal compatibility alias for MCP
	// tests/callers that predate the shared vs host-managed terminology.
	ProfileKindDedicated = agentadaptor.ProfileKindHostManaged
)

func ClassifyProfile(profile agentadaptor.AgentProfile, canonicalShared string) ProfileKind {
	return profilekind.Classify(profile, canonicalShared)
}

func samePathForOS(left, right, goos string) bool {
	return profilekind.SamePathForOSForTest(left, right, goos)
}
