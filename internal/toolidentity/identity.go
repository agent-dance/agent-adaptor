// Package toolidentity owns the private constants shared by the Agent-owned
// Tool runtime and provider profile materializers. Keeping them here avoids an
// MCP SDK dependency in provider packages and prevents string markers from
// becoming public API.
package toolidentity

import "strings"

const (
	ServerKey                      = "agent-adaptor-tools"
	BearerTokenEnvVarPrefix        = "AGENT_ADAPTOR_TOOL_TOKEN_"
	CompatibilityBearerTokenEnvVar = "agent-owned://host-defined-tools/bearer-env"
	RequiredReason                 = "host-defined Tools are part of this Agent"
	ManifestOwner                  = "agent-adaptor/host-defined-tools/v1"
)

// IsBearerTokenEnvVar recognizes the per-Agent environment-variable names
// minted by the private Tool runtime. The 128-bit suffix prevents another MCP
// declaration from predicting and aliasing the credential carrier.
func IsBearerTokenEnvVar(name string) bool {
	if !strings.HasPrefix(name, BearerTokenEnvVarPrefix) || len(name) != len(BearerTokenEnvVarPrefix)+32 {
		return false
	}
	for _, char := range name[len(BearerTokenEnvVarPrefix):] {
		if (char >= '0' && char <= '9') || (char >= 'A' && char <= 'F') {
			continue
		}
		return false
	}
	return true
}
