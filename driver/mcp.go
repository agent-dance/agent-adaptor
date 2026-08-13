package driver

// MCPTransport identifies how a model-context-protocol server is reached.
// Built-in drivers translate these values into provider-specific profile or
// CLI configuration only when their descriptor advertises support.
type MCPTransport string

// MCPToolApprovalMode is the closed, provider-neutral approval policy for one
// exact tool on one exact MCP server.
type MCPToolApprovalMode string

const (
	// MCPTransportStdio starts a local command and speaks MCP over stdio.
	MCPTransportStdio MCPTransport = "stdio"
	// MCPTransportHTTP connects to an HTTP MCP endpoint.
	MCPTransportHTTP MCPTransport = "http"
	// MCPTransportSSE connects to an SSE-based MCP endpoint.
	MCPTransportSSE MCPTransport = "sse"
)

const (
	// MCPToolApprovalPrompt keeps the provider's approval prompt for this tool.
	MCPToolApprovalPrompt MCPToolApprovalMode = "prompt"
	// MCPToolApprovalApprove authorizes this exact tool without an additional
	// provider approval prompt. It does not affect other tools or operations.
	MCPToolApprovalApprove MCPToolApprovalMode = "approve"
)

// MCPToolPolicy configures one exact MCP tool.
type MCPToolPolicy struct {
	ApprovalMode MCPToolApprovalMode
}

// MCPServerSpec is one host-declared MCP server. For stdio servers, Command
// and Args describe the process to launch. For HTTP/SSE servers, URL, Headers,
// and BearerTokenEnvVar describe the remote endpoint. Required marks servers
// the host expects to be present for the run.
type MCPServerSpec struct {
	Key               string
	Transport         MCPTransport
	Command           string
	Args              []string
	Env               map[string]string
	URL               string
	Headers           map[string]string
	BearerTokenEnvVar string
	Required          bool
	RequiredReason    string
	// Tools contains exact tool-name overrides. Drivers must never interpret
	// keys as prefixes, globs, or a server-wide default.
	Tools map[string]MCPToolPolicy
}

// MCPPayload is the normalized driver-facing MCP configuration after
// validation, capability checks, sorting, and fingerprinting.
type MCPPayload struct {
	Servers     []MCPServerSpec
	Fingerprint string
	Warnings    []string
}

// MCPCapability describes which MCP transports a driver supports. The SDK
// validates host-provided MCPConfig against this before invoking the driver.
type MCPCapability struct {
	Supported     bool
	Stdio         bool
	HTTP          bool
	SSE           bool
	ToolApprovals bool
}
