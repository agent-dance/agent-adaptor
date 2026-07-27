package mcpruntime

import (
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
	"github.com/agent-dance/agent-adaptor/internal/profilekind"
)

type ProfileKind = engine.ProfileKind

const (
	ProfileKindShared      = engine.ProfileKindShared
	ProfileKindHostManaged = engine.ProfileKindHostManaged
	// ProfileKindDedicated is kept as an internal compatibility alias for MCP
	// tests/callers that predate the shared vs host-managed terminology.
	ProfileKindDedicated = engine.ProfileKindHostManaged
)

func ClassifyProfile(profile driver.AgentProfile, canonicalShared string) ProfileKind {
	return profilekind.Classify(profile, canonicalShared)
}

func samePathForOS(left, right, goos string) bool {
	return profilekind.SamePathForOSForTest(left, right, goos)
}
