package mcpruntime

import (
	"fmt"
	"path/filepath"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

func SyncCursorProfile(cursorHome string, kind ProfileKind, payload agentadaptor.MCPPayload) error {
	path := filepath.Join(cursorHome, "mcp.json")
	root, err := readJSONObject(path)
	if err != nil {
		return fmt.Errorf("read Cursor MCP config: %w", err)
	}
	return syncJSONServers(path, root, "mcpServers", kind, payload.Servers, cursorServerConfig)
}

func cursorServerConfig(server agentadaptor.MCPServerSpec) (map[string]any, error) {
	headers, err := mergeBearerHeader(server.Headers, server.BearerTokenEnvVar, "${env:%s}", server.Key)
	if err != nil {
		return nil, err
	}

	entry := map[string]any{
		"type": string(server.Transport),
	}
	switch server.Transport {
	case agentadaptor.MCPTransportStdio:
		entry["command"] = server.Command
		if len(server.Args) > 0 {
			entry["args"] = append([]string(nil), server.Args...)
		}
		if len(server.Env) > 0 {
			entry["env"] = cloneStringMapAny(server.Env)
		}
	case agentadaptor.MCPTransportHTTP, agentadaptor.MCPTransportSSE:
		entry["url"] = server.URL
		if len(headers) > 0 {
			entry["headers"] = headers
		}
	default:
		return nil, fmt.Errorf("%w: Cursor MCP server %q does not support transport %q", agentadaptor.ErrMCPTransportUnsupported, server.Key, server.Transport)
	}
	return entry, nil
}
