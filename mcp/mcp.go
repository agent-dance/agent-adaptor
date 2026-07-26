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

// Option customizes a Server produced by the HTTP or SSE constructors.
// Every option effect is also reachable by assigning the corresponding
// exported Server field, so nothing here is load-bearing for capability.
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
	for _, opt := range opts {
		if opt != nil {
			opt(&server)
		}
	}
	return server
}

// Stdio declares a local MCP server launched as a subprocess speaking MCP
// over stdio. name becomes the server key, command the executable, and args
// its arguments (copied, so later mutation of the caller's slice is not
// observed). Stdio-only knobs without a constructor parameter — Env,
// Required, RequiredReason — are plain exported fields on the returned
// Server:
//
//	srv := mcp.Stdio("repo-tools", "npx", "repo-mcp")
//	srv.Env = map[string]string{"REPO_TOKEN_FILE": "/run/secrets/repo"}
//	srv.Required = true
func Stdio(name, command string, args ...string) Server {
	server := Server{Key: name, Transport: TransportStdio, Command: command}
	if len(args) > 0 {
		server.Args = append([]string(nil), args...)
	}
	return server
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
	return func(server *Server) {
		if len(headers) == 0 {
			return
		}
		if server.Headers == nil {
			server.Headers = make(map[string]string, len(headers))
		}
		for key, value := range headers {
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
