package opencode

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/driverutil"
	"github.com/agent-dance/agent-adaptor/internal/engine"
	"github.com/agent-dance/agent-adaptor/internal/profilejson"
)

func mcpProfileLayout(root string) profilejson.Layout {
	return profilejson.Layout{Provider: DriverType, Root: root, File: "opencode.jsonc", Field: "mcp", Kind: driver.ProfileResourceMCP, Materialization: driver.ProfileResourceMaterializationNativeManaged}
}

func mcpProfilePlan(root string, payload driver.MCPPayload) (profilejson.Layout, map[string]json.RawMessage, error) {
	layout := mcpProfileLayout(root)
	layout.Metadata = map[string]map[string]string{}
	validated, err := engine.ResolveMCPPayload(&engine.MCPConfig{Servers: payload.Servers}, nil, configuredDriver{}.Descriptor().MCP)
	if err != nil {
		return layout, nil, err
	}
	keys := make([]string, 0, len(validated.Servers))
	for _, spec := range validated.Servers {
		keys = append(keys, spec.Key)
	}
	if err := validateMCPNames(keys); err != nil {
		return layout, nil, err
	}
	entries := map[string]json.RawMessage{}
	for _, spec := range validated.Servers {
		entry, err := nativeMCPEntry(spec, nil, true)
		if err != nil {
			return layout, nil, err
		}
		raw, err := marshalNativeMCP(entry, spec.BearerTokenEnvVar)
		if err != nil {
			return layout, nil, err
		}
		entries[spec.Key] = raw
		// Keep only semantics absent from native config in the manifest. Do
		// not duplicate command environments or literal authorization headers.
		layout.Metadata[spec.Key] = map[string]string{"transport": string(spec.Transport), "bearer_env": spec.BearerTokenEnvVar, "required": strconv.FormatBool(spec.Required), "required_reason": spec.RequiredReason}
	}
	return layout, entries, nil
}

func marshalNativeMCP(entry map[string]any, bearerEnv string) ([]byte, error) {
	// Only the explicit bearer environment reference is a native template.
	// Portable command arguments, environment values and headers are literals.
	copy := make(map[string]any, len(entry))
	for key, value := range entry {
		copy[key] = value
	}
	if bearerEnv != "" {
		headers := map[string]any{}
		for key, value := range entry["headers"].(map[string]string) {
			headers[key] = value
		}
		// Insert the intentional template after escaping the complete entry.
		delete(headers, "Authorization")
		copy["headers"] = headers
	}
	raw, err := json.Marshal(copy)
	if err != nil {
		return nil, err
	}
	raw = bytes.ReplaceAll(raw, []byte("{env:"), []byte(`\u007benv:`))
	raw = bytes.ReplaceAll(raw, []byte("{file:"), []byte(`\u007bfile:`))
	if bearerEnv == "" {
		return raw, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	var headers map[string]json.RawMessage
	if err := json.Unmarshal(fields["headers"], &headers); err != nil {
		return nil, err
	}
	headers["Authorization"], _ = json.Marshal("Bearer {env:" + bearerEnv + "}")
	fields["headers"], err = json.Marshal(headers)
	if err != nil {
		return nil, err
	}
	return json.Marshal(fields)
}

func nativeMCPEntry(spec driver.MCPServerSpec, env []driver.EnvBinding, durable bool) (map[string]any, error) {
	entry := map[string]any{"enabled": true}
	switch spec.Transport {
	case driver.MCPTransportStdio:
		entry["type"] = "local"
		entry["command"] = append([]string{spec.Command}, spec.Args...)
		if len(spec.Env) > 0 {
			entry["environment"] = spec.Env
		}
	case driver.MCPTransportHTTP, driver.MCPTransportSSE:
		entry["type"], entry["url"], entry["oauth"] = "remote", spec.URL, false
		headers := map[string]string{}
		for key, value := range spec.Headers {
			headers[key] = value
		}
		if spec.BearerTokenEnvVar != "" {
			for key := range headers {
				if strings.EqualFold(key, "Authorization") {
					return nil, fmt.Errorf("opencode: MCP %q cannot combine Authorization and a bearer environment reference", spec.Key)
				}
			}
			var token string
			if durable {
				if strings.ContainsAny(spec.BearerTokenEnvVar, "{}\r\n") {
					return nil, fmt.Errorf("opencode: invalid MCP bearer environment reference for %q", spec.Key)
				}
				token = "{env:" + spec.BearerTokenEnvVar + "}"
			} else {
				token, _ = driverutil.ResolvedEnvValue(env, spec.BearerTokenEnvVar)
				if token == "" {
					return nil, fmt.Errorf("opencode: MCP bearer environment %q is unset", spec.BearerTokenEnvVar)
				}
			}
			headers["Authorization"] = "Bearer " + token
		}
		if len(headers) > 0 {
			entry["headers"] = headers
		}
	default:
		return nil, fmt.Errorf("opencode: unsupported MCP transport %q", spec.Transport)
	}
	return entry, nil
}

func observeProfileMCP(root string, payload driver.MCPPayload) (profilejson.State, error) {
	layout, desired, err := mcpProfilePlan(root, payload)
	if err != nil {
		return profilejson.State{}, err
	}
	state, err := profilejson.Observe(layout, desired, payload.Fingerprint)
	if err != nil {
		return state, err
	}
	base, err := profilejson.ReadEntries(root, "opencode.json", "mcp")
	if err != nil {
		return state, err
	}
	for key, raw := range base {
		if _, overridden := state.Entries[key]; overridden {
			continue
		}
		state.Entries[key] = raw
		state.Snapshot.External = append(state.Snapshot.External, key)
	}
	sort.Strings(state.Snapshot.External)
	return state, nil
}

// Only entries whose native value still matches SDK ownership can supply
// durable declarations. External edits are left for OpenCode to load natively.
func resolveProfileMCP(req driver.Request, root string) (driver.MCPPayload, []string, string, error) {
	state, err := observeProfileMCP(root, driver.MCPPayload{})
	if err != nil {
		return driver.MCPPayload{}, nil, "", err
	}
	sources := map[string]driver.MCPServerSpec{}
	for key, owner := range state.Owners {
		spec, err := decodePersistentMCP(key, state.Entries[key], owner.Metadata)
		if err != nil {
			return driver.MCPPayload{}, nil, "", fmt.Errorf("opencode: invalid persisted MCP declaration %q", key)
		}
		_, rendered, err := mcpProfilePlan(root, driver.MCPPayload{Servers: []driver.MCPServerSpec{spec}})
		if err != nil || !bytes.Equal(rendered[key], state.Entries[key]) {
			return driver.MCPPayload{}, nil, "", fmt.Errorf("opencode: persisted MCP declaration %q differs from native content", key)
		}
		sources[key] = spec
	}
	fingerprint, err := driver.CanonicalSessionConfigFingerprint("opencode/profile-mcp/v1", sources)
	if err != nil {
		return driver.MCPPayload{}, nil, "", err
	}
	var disabled []string
	if req.ProfilePayload.Declared.MCP {
		for key := range sources {
			disabled = append(disabled, key)
		}
		sources = map[string]driver.MCPServerSpec{}
	}
	for _, spec := range req.MCP.Servers {
		sources[spec.Key] = spec
	}
	var effective []driver.MCPServerSpec
	for _, spec := range sources {
		effective = append(effective, spec)
	}
	payload, err := engine.ResolveMCPPayload(&engine.MCPConfig{Servers: effective}, nil, configuredDriver{}.Descriptor().MCP)
	if err == nil {
		keys := make([]string, 0, len(payload.Servers))
		for _, spec := range payload.Servers {
			keys = append(keys, spec.Key)
		}
		err = validateMCPNames(keys)
	}
	sort.Strings(disabled)
	return payload, disabled, fingerprint, err
}

// OpenCode's official sanitizer replaces each non-ASCII UTF-16 code unit.
// Reject aliases explicitly rather than silently dropping a server's tools.
func validateMCPNames(keys []string) error {
	seen := map[string]string{}
	for _, key := range keys {
		var out strings.Builder
		for _, c := range utf16.Encode([]rune(key)) {
			if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' {
				out.WriteByte(byte(c))
			} else {
				out.WriteByte('_')
			}
		}
		name := out.String()
		if prior, exists := seen[name]; exists && prior != key {
			return fmt.Errorf("opencode: MCP server names %q and %q collide after native normalization", prior, key)
		}
		seen[name] = key
	}
	return nil
}

func decodePersistentMCP(key string, raw json.RawMessage, metadata map[string]string) (driver.MCPServerSpec, error) {
	spec := driver.MCPServerSpec{Key: key, Transport: driver.MCPTransport(metadata["transport"]), BearerTokenEnvVar: metadata["bearer_env"], RequiredReason: metadata["required_reason"]}
	var err error
	spec.Required, err = strconv.ParseBool(metadata["required"])
	if err != nil {
		return spec, err
	}
	var native struct {
		Type        string            `json:"type"`
		Enabled     bool              `json:"enabled"`
		Command     []string          `json:"command"`
		Environment map[string]string `json:"environment"`
		URL         string            `json:"url"`
		Headers     map[string]string `json:"headers"`
		OAuth       bool              `json:"oauth"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&native); err != nil {
		return spec, err
	}
	if native.Type == "local" {
		if len(native.Command) == 0 {
			return spec, fmt.Errorf("missing native MCP command")
		}
		spec.Command, spec.Args, spec.Env = native.Command[0], native.Command[1:], native.Environment
	} else {
		spec.URL, spec.Headers = native.URL, native.Headers
	}
	if spec.BearerTokenEnvVar != "" {
		if spec.Headers["Authorization"] != "Bearer {env:"+spec.BearerTokenEnvVar+"}" {
			return spec, fmt.Errorf("native bearer reference mismatch")
		}
		delete(spec.Headers, "Authorization")
	}
	return spec, nil
}
