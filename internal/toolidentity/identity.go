// Package toolidentity owns the private constants shared by the Agent-owned
// Tool runtime and provider profile materializers. Keeping them here avoids an
// MCP SDK dependency in provider packages and prevents string markers from
// becoming public API.
package toolidentity

const (
	ServerKey         = "agent-adaptor-tools"
	BearerTokenEnvVar = "AGENT_ADAPTOR_TOOL_TOKEN"
	RequiredReason    = "host-defined Tools are part of this Agent"
	ManifestOwner     = "agent-adaptor/host-defined-tools/v1"
)
