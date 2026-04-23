package mcpruntime

import (
	"fmt"
	"os"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

func syncJSONServers(
	path string,
	root map[string]any,
	field string,
	kind ProfileKind,
	servers []agentadaptor.MCPServerSpec,
	render func(agentadaptor.MCPServerSpec) (map[string]any, error),
) error {
	existing := mapFromAny(root[field])
	desired := map[string]any{}
	for _, server := range servers {
		entry, err := render(server)
		if err != nil {
			return err
		}
		desired[server.Key] = entry
	}

	switch {
	case kind == ProfileKindShared && len(desired) == 0:
		return nil
	case kind == ProfileKindShared:
		if existing == nil {
			existing = map[string]any{}
		}
		for key, value := range desired {
			existing[key] = value
		}
		root[field] = existing
	case len(desired) == 0:
		delete(root, field)
	default:
		root[field] = desired
	}

	if len(root) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return writeJSONObject(path, root)
}

func mergeBearerHeader(headers map[string]string, bearerEnvVar, format, key string) (map[string]any, error) {
	if headers == nil && bearerEnvVar == "" {
		return nil, nil
	}
	out := map[string]any{}
	for header, value := range headers {
		out[header] = value
	}
	if bearerEnvVar == "" {
		return out, nil
	}
	if _, exists := out["Authorization"]; exists {
		return nil, fmt.Errorf("%w: MCP server %q cannot set both Headers.Authorization and BearerTokenEnvVar", agentadaptor.ErrInvalidMCPConfig, key)
	}
	out["Authorization"] = fmt.Sprintf("Bearer "+format, bearerEnvVar)
	return out, nil
}

func cloneStringMapAny(values map[string]string) map[string]any {
	if len(values) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func mapFromAny(value any) map[string]any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, child := range typed {
			out[key] = child
		}
		return out
	case map[string]string:
		out := make(map[string]any, len(typed))
		for key, child := range typed {
			out[key] = child
		}
		return out
	default:
		return nil
	}
}
