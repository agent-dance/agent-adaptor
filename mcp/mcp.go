package mcp

import "github.com/agent-dance/agent-adaptor/driver"

// Server declares one MCP server for the run. It is the consumer-facing name
// for [driver.MCPServerSpec], so values built here are accepted anywhere the
// SDK or the driver SPI takes the spec today, and every configuration knob
// remains an exported struct field.
//
// For stdio servers, Command, Args, and Env describe the process to launch.
// For HTTP/SSE servers, URL, Headers, and BearerTokenEnvVar describe the
// remote endpoint. Required (with RequiredReason) marks servers the host
// expects to be present for the run.
type Server = driver.MCPServerSpec

// Transport identifies how an MCP server is reached. It is the
// consumer-facing name for [driver.MCPTransport].
type Transport = driver.MCPTransport

// ToolApprovalMode is the provider-neutral approval mode for an exact tool.
type ToolApprovalMode = driver.MCPToolApprovalMode

// ToolPolicy is one exact MCP tool's policy.
type ToolPolicy = driver.MCPToolPolicy

// Re-exported transport values so Server literals and field checks do not
// need a driver import.
const (
	// TransportStdio starts a local command and speaks MCP over stdio.
	TransportStdio = driver.MCPTransportStdio
	// TransportHTTP connects to an HTTP MCP endpoint.
	TransportHTTP = driver.MCPTransportHTTP
	// TransportSSE connects to an SSE-based MCP endpoint.
	TransportSSE = driver.MCPTransportSSE
)

const (
	// ToolApprovalPrompt preserves an approval prompt for the exact tool.
	ToolApprovalPrompt = driver.MCPToolApprovalPrompt
	// ToolApprovalApprove authorizes only the exact tool on this server.
	ToolApprovalApprove = driver.MCPToolApprovalApprove
)

// Option customizes a Server produced by [Stdio], [HTTP], or [SSE].
//
// Options are transport-scoped by the fields they set. [Args] and [Env] are
// valid only for stdio servers; [WithHeader], [WithHeaders], and
// [WithBearerTokenEnv] are valid only for HTTP/SSE servers; [Required] is
// valid for every transport. Applying an option to the wrong constructor is
// not silently ignored: it leaves the incompatible field on Server so the
// SDK's normal MCP validation returns ErrInvalidMCPConfig before launch.
type Option func(*Server)

// HTTP declares a remote MCP server reached over HTTP. name becomes the
// server key (must be unique within one WithMCP declaration) and url the
// endpoint. Nil options are ignored.
func HTTP(name, url string, opts ...Option) Server {
	return remote(name, url, TransportHTTP, opts)
}

// SSE declares a remote MCP server reached over server-sent events. It is
// identical to HTTP except for the transport.
func SSE(name, url string, opts ...Option) Server {
	return remote(name, url, TransportSSE, opts)
}

func remote(name, url string, transport Transport, opts []Option) Server {
	server := Server{Key: name, Transport: transport, URL: url}
	applyOptions(&server, opts)
	return server
}

func applyOptions(server *Server, opts []Option) {
	for _, opt := range opts {
		if opt != nil {
			opt(server)
		}
	}
}

// Stdio declares a local MCP server launched as a subprocess speaking MCP
// over stdio. name becomes the server key and command the executable. Use
// [Args], [Env], and [Required] to finish the declaration in one expression:
//
//	srv := mcp.Stdio("repo-tools", "npx",
//		mcp.Args("repo-mcp", "--verbose"),
//		mcp.Env(map[string]string{"REPO_TOKEN_FILE": "/run/secrets/repo"}),
//		mcp.Required("repository access is mandatory"),
//	)
//
// Nil options are ignored. Option inputs are snapshotted, and applying the
// same option to multiple servers produces independent slices and maps.
func Stdio(name, command string, opts ...Option) Server {
	server := Server{Key: name, Transport: TransportStdio, Command: command}
	applyOptions(&server, opts)
	return server
}

// Args replaces the process arguments for a stdio server. The input is
// copied when Args is called and copied again for each server, so neither
// caller mutation nor option reuse can alias a Server's state. Calling Args
// with no values clears an earlier Args option.
func Args(args ...string) Option {
	snapshot := cloneStrings(args)
	return func(server *Server) {
		server.Args = cloneStrings(snapshot)
	}
}

// Env adds process environment entries for a stdio server. Later Env options
// overwrite earlier values for the same key. The map is copied when Env is
// called and its entries are copied again for each server, so option reuse
// cannot alias a Server's state. A nil or empty map has no effect.
func Env(env map[string]string) Option {
	snapshot := cloneStringMap(env)
	return func(server *Server) {
		if len(snapshot) == 0 {
			return
		}
		if server.Env == nil {
			server.Env = make(map[string]string, len(snapshot))
		}
		for key, value := range snapshot {
			server.Env[key] = value
		}
	}
}

// WithHeader adds one HTTP header sent on every request to a remote server.
// Later options overwrite earlier values for the same key.
func WithHeader(key, value string) Option {
	return func(server *Server) {
		if server.Headers == nil {
			server.Headers = make(map[string]string, 1)
		}
		server.Headers[key] = value
	}
}

// WithHeaders adds every entry of headers to the server's request headers.
// The map is copied; later mutation of the caller's map is not observed.
func WithHeaders(headers map[string]string) Option {
	snapshot := cloneStringMap(headers)
	return func(server *Server) {
		if len(snapshot) == 0 {
			return
		}
		if server.Headers == nil {
			server.Headers = make(map[string]string, len(snapshot))
		}
		for key, value := range snapshot {
			server.Headers[key] = value
		}
	}
}

// WithBearerTokenEnv names the environment variable whose value is presented
// as the bearer token when connecting to a remote server. The token itself
// never enters the declaration.
func WithBearerTokenEnv(envVar string) Option {
	return func(server *Server) {
		server.BearerTokenEnvVar = envVar
	}
}

// Required marks the server as one the host expects to be present for the
// run, with a human-readable reason surfaced when it is not.
func Required(reason string) Option {
	return func(server *Server) {
		server.Required = true
		server.RequiredReason = reason
	}
}

// WithToolApproval sets the approval mode for one exact tool on this server.
// Repeating an equivalent declaration is idempotent. A conflicting duplicate
// is retained as an invalid closed-enum value so normal validation fails the
// run before driver launch rather than silently choosing an order-dependent
// winner.
func WithToolApproval(name string, mode ToolApprovalMode) Option {
	return func(server *Server) {
		if server.Tools == nil {
			server.Tools = make(map[string]ToolPolicy, 1)
		}
		policy := ToolPolicy{ApprovalMode: mode}
		if existing, ok := server.Tools[name]; ok && existing != policy {
			policy.ApprovalMode = ToolApprovalMode("conflict")
		}
		server.Tools[name] = policy
	}
}

func cloneStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	return append([]string(nil), values...)
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	clone := make(map[string]string, len(values))
	for key, value := range values {
		clone[key] = value
	}
	return clone
}
