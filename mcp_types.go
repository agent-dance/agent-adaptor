package agentadaptor

import (
	"fmt"
	"slices"
	"strings"
)

// MCPTransport identifies how a model-context-protocol server is reached.
// Built-in adapters translate these values into provider-specific profile or
// CLI configuration only when their descriptor advertises support.
type MCPTransport string

const (
	// MCPTransportStdio starts a local command and speaks MCP over stdio.
	MCPTransportStdio MCPTransport = "stdio"
	// MCPTransportHTTP connects to an HTTP MCP endpoint.
	MCPTransportHTTP MCPTransport = "http"
	// MCPTransportSSE connects to an SSE-based MCP endpoint.
	MCPTransportSSE MCPTransport = "sse"
)

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
}

// MCPConfig is the binding-level or per-run collection of MCP servers. Per-run
// WithMCP replaces the full effective config rather than appending to defaults.
type MCPConfig struct {
	Servers []MCPServerSpec
}

// MCPPayload is the normalized adapter-facing MCP configuration after
// validation, capability checks, sorting, and fingerprinting.
type MCPPayload struct {
	Servers     []MCPServerSpec
	Fingerprint string
	Warnings    []string
}

// MCPCapability describes which MCP transports an adapter supports. The SDK
// validates host-provided MCPConfig against this before invoking the adapter.
type MCPCapability struct {
	Supported bool
	Stdio     bool
	HTTP      bool
	SSE       bool
}

func cloneMCPConfig(cfg *MCPConfig) *MCPConfig {
	if cfg == nil {
		return nil
	}
	return &MCPConfig{Servers: cloneMCPServerSpecs(cfg.Servers)}
}

func cloneMCPServerSpecs(values []MCPServerSpec) []MCPServerSpec {
	if len(values) == 0 {
		return nil
	}
	out := make([]MCPServerSpec, 0, len(values))
	for _, value := range values {
		out = append(out, MCPServerSpec{
			Key:               value.Key,
			Transport:         value.Transport,
			Command:           value.Command,
			Args:              cloneStrings(value.Args),
			Env:               cloneStringMap(value.Env),
			URL:               value.URL,
			Headers:           cloneStringMap(value.Headers),
			BearerTokenEnvVar: value.BearerTokenEnvVar,
			Required:          value.Required,
			RequiredReason:    value.RequiredReason,
		})
	}
	return out
}

func cloneMCPPayload(payload MCPPayload) MCPPayload {
	return MCPPayload{
		Servers:     cloneMCPServerSpecs(payload.Servers),
		Fingerprint: payload.Fingerprint,
		Warnings:    cloneStrings(payload.Warnings),
	}
}

func resolveMCPPayload(defaults, override *MCPConfig, caps MCPCapability) (MCPPayload, error) {
	effective := defaults
	if override != nil {
		effective = override
	}
	if effective == nil {
		return MCPPayload{}, nil
	}
	return prepareMCPPayload(*effective, caps)
}

func prepareMCPPayload(cfg MCPConfig, caps MCPCapability) (MCPPayload, error) {
	if len(cfg.Servers) == 0 {
		return MCPPayload{Fingerprint: stableHash("mcp", []MCPServerSpec{})}, nil
	}
	if !caps.Supported {
		return MCPPayload{}, fmt.Errorf("%w: adapter does not support MCP servers", ErrMCPUnsupported)
	}

	normalized := make([]MCPServerSpec, 0, len(cfg.Servers))
	seen := map[string]struct{}{}
	for _, server := range cfg.Servers {
		spec, err := normalizeMCPServerSpec(server)
		if err != nil {
			return MCPPayload{}, err
		}
		if _, exists := seen[spec.Key]; exists {
			return MCPPayload{}, fmt.Errorf("%w: duplicate MCP server key %q", ErrInvalidMCPConfig, spec.Key)
		}
		if err := validateMCPCapability(spec, caps); err != nil {
			return MCPPayload{}, err
		}
		seen[spec.Key] = struct{}{}
		normalized = append(normalized, spec)
	}

	slices.SortFunc(normalized, func(a, b MCPServerSpec) int {
		return strings.Compare(a.Key, b.Key)
	})

	return MCPPayload{
		Servers:     normalized,
		Fingerprint: stableHash("mcp", normalized),
	}, nil
}

func normalizeMCPServerSpec(server MCPServerSpec) (MCPServerSpec, error) {
	key := strings.TrimSpace(server.Key)
	if key == "" {
		return MCPServerSpec{}, fmt.Errorf("%w: MCP server key is required", ErrInvalidMCPConfig)
	}

	spec := MCPServerSpec{
		Key:               key,
		Transport:         MCPTransport(strings.ToLower(strings.TrimSpace(string(server.Transport)))),
		Command:           strings.TrimSpace(server.Command),
		Args:              cloneStrings(server.Args),
		Env:               normalizeStringMap(server.Env, false),
		URL:               strings.TrimSpace(server.URL),
		Headers:           normalizeStringMap(server.Headers, false),
		BearerTokenEnvVar: strings.TrimSpace(server.BearerTokenEnvVar),
		Required:          server.Required,
		RequiredReason:    strings.TrimSpace(server.RequiredReason),
	}

	switch spec.Transport {
	case MCPTransportStdio:
		if spec.Command == "" {
			return MCPServerSpec{}, fmt.Errorf("%w: MCP stdio server %q requires command", ErrInvalidMCPConfig, spec.Key)
		}
		if spec.URL != "" {
			return MCPServerSpec{}, fmt.Errorf("%w: MCP stdio server %q cannot set URL", ErrInvalidMCPConfig, spec.Key)
		}
		if len(spec.Headers) > 0 {
			return MCPServerSpec{}, fmt.Errorf("%w: MCP stdio server %q cannot set headers", ErrInvalidMCPConfig, spec.Key)
		}
		if spec.BearerTokenEnvVar != "" {
			return MCPServerSpec{}, fmt.Errorf("%w: MCP stdio server %q cannot set bearer token env var", ErrInvalidMCPConfig, spec.Key)
		}
	case MCPTransportHTTP, MCPTransportSSE:
		if spec.URL == "" {
			return MCPServerSpec{}, fmt.Errorf("%w: MCP %s server %q requires URL", ErrInvalidMCPConfig, spec.Transport, spec.Key)
		}
		if spec.Command != "" {
			return MCPServerSpec{}, fmt.Errorf("%w: MCP %s server %q cannot set command", ErrInvalidMCPConfig, spec.Transport, spec.Key)
		}
		if len(spec.Args) > 0 {
			return MCPServerSpec{}, fmt.Errorf("%w: MCP %s server %q cannot set args", ErrInvalidMCPConfig, spec.Transport, spec.Key)
		}
		if len(spec.Env) > 0 {
			return MCPServerSpec{}, fmt.Errorf("%w: MCP %s server %q cannot set env", ErrInvalidMCPConfig, spec.Transport, spec.Key)
		}
	default:
		return MCPServerSpec{}, fmt.Errorf("%w: MCP server %q has unsupported transport %q", ErrInvalidMCPConfig, spec.Key, server.Transport)
	}

	return spec, nil
}

func validateMCPCapability(spec MCPServerSpec, caps MCPCapability) error {
	var supported bool
	switch spec.Transport {
	case MCPTransportStdio:
		supported = caps.Stdio
	case MCPTransportHTTP:
		supported = caps.HTTP
	case MCPTransportSSE:
		supported = caps.SSE
	}
	if supported {
		return nil
	}
	return fmt.Errorf("%w: transport %q is not supported for MCP server %q", ErrMCPTransportUnsupported, spec.Transport, spec.Key)
}

func normalizeStringMap(values map[string]string, trimValues bool) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		trimmedKey := strings.TrimSpace(key)
		if trimmedKey == "" {
			continue
		}
		if trimValues {
			value = strings.TrimSpace(value)
		}
		out[trimmedKey] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
