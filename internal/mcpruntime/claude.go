package mcpruntime

import (
	"context"
	"fmt"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
)

func SyncClaudeProfile(configDir string, kind ProfileKind, payload driver.MCPPayload) error {
	_, err := SyncResource(context.Background(), "claude", configDir, kind, payload)
	return err
}

func claudeServerConfig(server driver.MCPServerSpec) (map[string]any, error) {
	headers, err := mergeBearerHeader(server.Headers, server.BearerTokenEnvVar, "${%s}", server.Key)
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
		entry["env"] = cloneStringMapAny(server.Env)
	case driver.MCPTransportHTTP, driver.MCPTransportSSE:
		entry["url"] = server.URL
		if len(headers) > 0 {
			entry["headers"] = headers
		}
	default:
		return nil, fmt.Errorf("%w: Claude MCP server %q does not support transport %q", engine.ErrMCPTransportUnsupported, server.Key, server.Transport)
	}
	return entry, nil
}
