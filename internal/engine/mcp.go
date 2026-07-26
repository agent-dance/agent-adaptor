package engine

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// MCPConfig is the binding-level or per-run collection of MCP servers. Per-run
// WithMCP replaces the full effective config rather than appending to defaults.
type MCPConfig struct {
	Servers []MCPServerSpec
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
		out = append(out, cloneMCPServerSpec(value))
	}
	return out
}

func cloneMCPServerSpec(value MCPServerSpec) MCPServerSpec {
	return MCPServerSpec{
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
	}
}

func cloneMCPServerSpecPtr(value *MCPServerSpec) *MCPServerSpec {
	if value == nil {
		return nil
	}
	spec := cloneMCPServerSpec(*value)
	return &spec
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

func resolveMCPPayloadWithRuntime(defaults, override *MCPConfig, refs []RuntimeServiceRef, caps MCPCapability) (MCPPayload, error) {
	effective := MCPConfig{}
	if defaults != nil {
		effective.Servers = cloneMCPServerSpecs(defaults.Servers)
	}
	if override != nil {
		effective.Servers = cloneMCPServerSpecs(override.Servers)
	}

	runtimeServers, err := mcpServersFromRuntimeRefs(refs)
	if err != nil {
		return MCPPayload{}, err
	}
	effective.Servers = append(effective.Servers, runtimeServers...)
	if len(effective.Servers) == 0 {
		return MCPPayload{}, nil
	}
	return prepareMCPPayload(effective, caps)
}

func mcpServersFromRuntimeRefs(refs []RuntimeServiceRef) ([]MCPServerSpec, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	out := make([]MCPServerSpec, 0, len(refs))
	for _, ref := range refs {
		spec, ok, err := mcpServerFromRuntimeRef(ref)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, spec)
		}
	}
	return out, nil
}

// mcpServerFromRuntimeRef materializes the MCP server a runtime service ref
// publishes for the run, if any. The typed RuntimeServiceRef.MCP field wins;
// when it is nil the legacy "agentadaptor.mcp.*" metadata keys are parsed as
// a migration-period fallback (compat path scheduled for removal in P5).
func mcpServerFromRuntimeRef(ref RuntimeServiceRef) (MCPServerSpec, bool, error) {
	if ref.MCP != nil {
		return mcpServerFromTypedRef(ref), true, nil
	}

	metadata := ref.Metadata
	if !runtimeMCPEnabled(metadata) {
		return MCPServerSpec{}, false, nil
	}

	spec := MCPServerSpec{
		Key:               strings.TrimSpace(metadata["agentadaptor.mcp.key"]),
		Transport:         MCPTransport(strings.TrimSpace(metadata["agentadaptor.mcp.transport"])),
		Command:           strings.TrimSpace(metadata["agentadaptor.mcp.command"]),
		URL:               strings.TrimSpace(metadata["agentadaptor.mcp.url"]),
		BearerTokenEnvVar: strings.TrimSpace(metadata["agentadaptor.mcp.bearer_token_env_var"]),
		RequiredReason:    strings.TrimSpace(metadata["agentadaptor.mcp.required_reason"]),
	}
	if spec.Key == "" {
		spec.Key = strings.TrimSpace(ref.Name)
	}
	if spec.Key == "" {
		spec.Key = strings.TrimSpace(ref.ID)
	}
	if spec.Transport == "" {
		if spec.Command != "" {
			spec.Transport = MCPTransportStdio
		} else {
			spec.Transport = MCPTransportHTTP
		}
	}
	if spec.URL == "" && spec.Transport != MCPTransportStdio {
		spec.URL = strings.TrimSpace(ref.URL)
	}
	if spec.Command == "" && spec.Transport == MCPTransportStdio {
		spec.Command = strings.TrimSpace(ref.Command)
	}

	var err error
	if spec.Args, err = parseRuntimeMCPStringSlice(metadata, "agentadaptor.mcp.args_json"); err != nil {
		return MCPServerSpec{}, false, err
	}
	if spec.Env, err = parseRuntimeMCPStringMap(metadata, "agentadaptor.mcp.env_json"); err != nil {
		return MCPServerSpec{}, false, err
	}
	if spec.Headers, err = parseRuntimeMCPStringMap(metadata, "agentadaptor.mcp.headers_json"); err != nil {
		return MCPServerSpec{}, false, err
	}
	if spec.Required, err = parseRuntimeMCPBool(metadata, "agentadaptor.mcp.required"); err != nil {
		return MCPServerSpec{}, false, err
	}

	return spec, true, nil
}

// mcpServerFromTypedRef materializes the typed RuntimeServiceRef.MCP field
// with the same defaulting rules as the legacy metadata path: an empty Key
// falls back to the ref's Name then ID, an empty Transport is inferred from
// Command (stdio) versus URL (http), and empty URL/Command default from the
// ref's own endpoint fields.
func mcpServerFromTypedRef(ref RuntimeServiceRef) MCPServerSpec {
	spec := cloneMCPServerSpec(*ref.MCP)
	if strings.TrimSpace(spec.Key) == "" {
		spec.Key = strings.TrimSpace(ref.Name)
	}
	if spec.Key == "" {
		spec.Key = strings.TrimSpace(ref.ID)
	}
	if strings.TrimSpace(string(spec.Transport)) == "" {
		if strings.TrimSpace(spec.Command) != "" {
			spec.Transport = MCPTransportStdio
		} else {
			spec.Transport = MCPTransportHTTP
		}
	}
	if strings.TrimSpace(spec.URL) == "" && spec.Transport != MCPTransportStdio {
		spec.URL = strings.TrimSpace(ref.URL)
	}
	if strings.TrimSpace(spec.Command) == "" && spec.Transport == MCPTransportStdio {
		spec.Command = strings.TrimSpace(ref.Command)
	}
	return spec
}

func runtimeMCPEnabled(metadata map[string]string) bool {
	if len(metadata) == 0 {
		return false
	}
	raw, ok := metadata["agentadaptor.mcp.enabled"]
	if !ok {
		return false
	}
	enabled, err := strconv.ParseBool(strings.TrimSpace(raw))
	return err == nil && enabled
}

func parseRuntimeMCPStringSlice(metadata map[string]string, key string) ([]string, error) {
	raw := strings.TrimSpace(metadata[key])
	if raw == "" {
		return nil, nil
	}
	var values []string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, fmt.Errorf("%w: runtime MCP metadata %s must be a JSON string array: %v", ErrInvalidMCPConfig, key, err)
	}
	return cloneStrings(values), nil
}

func parseRuntimeMCPStringMap(metadata map[string]string, key string) (map[string]string, error) {
	raw := strings.TrimSpace(metadata[key])
	if raw == "" {
		return nil, nil
	}
	var values map[string]string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, fmt.Errorf("%w: runtime MCP metadata %s must be a JSON object with string values: %v", ErrInvalidMCPConfig, key, err)
	}
	return cloneStringMap(values), nil
}

func parseRuntimeMCPBool(metadata map[string]string, key string) (bool, error) {
	raw := strings.TrimSpace(metadata[key])
	if raw == "" {
		return false, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%w: runtime MCP metadata %s must be boolean: %v", ErrInvalidMCPConfig, key, err)
	}
	return value, nil
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
