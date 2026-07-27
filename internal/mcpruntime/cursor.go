package mcpruntime

import (
	"context"
	"fmt"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
)

func SyncCursorProfile(cursorHome string, kind ProfileKind, payload driver.MCPPayload) error {
	_, err := SyncResource(context.Background(), "cursor", cursorHome, kind, payload)
	return err
}

func cursorServerConfig(server driver.MCPServerSpec) (map[string]any, error) {
	headers, err := mergeBearerHeader(server.Headers, server.BearerTokenEnvVar, "${env:%s}", server.Key)
	if err != nil {
		return nil, err
	}

	entry := map[string]any{
		"type": string(server.Transport),
	}
	switch server.Transport {
	case driver.MCPTransportStdio:
		entry["command"] = server.Command
		if len(server.Args) > 0 {
			entry["args"] = append([]string(nil), server.Args...)
		}
		if len(server.Env) > 0 {
			entry["env"] = cloneStringMapAny(server.Env)
		}
	case driver.MCPTransportHTTP, driver.MCPTransportSSE:
		entry["url"] = server.URL
		if len(headers) > 0 {
			entry["headers"] = headers
		}
	default:
		return nil, fmt.Errorf("%w: Cursor MCP server %q does not support transport %q", engine.ErrMCPTransportUnsupported, server.Key, server.Transport)
	}
	return entry, nil
}
