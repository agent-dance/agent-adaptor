package mcpruntime

import (
	"context"
	"fmt"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

func SyncCodeBuddyProfile(configDir string, kind ProfileKind, payload agentadaptor.MCPPayload) error {
	_, err := SyncResource(context.Background(), "codebuddy", configDir, kind, payload)
	return err
}

// codebuddyServerConfig renders one MCP server into CodeBuddy's .mcp.json shape.
// 按官方文档与 codebuddy_agent_sdk 的字段约定，使用 type（stdio / sse / http），
func codebuddyServerConfig(server agentadaptor.MCPServerSpec) (map[string]any, error) {
	headers, err := mergeBearerHeader(server.Headers, server.BearerTokenEnvVar, "${%s}", server.Key)
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
		entry["env"] = cloneStringMapAny(server.Env)
	case agentadaptor.MCPTransportHTTP, agentadaptor.MCPTransportSSE:
		entry["url"] = server.URL
		if len(headers) > 0 {
			entry["headers"] = headers
		}
	default:
		return nil, fmt.Errorf("%w: CodeBuddy MCP server %q does not support transport %q", agentadaptor.ErrMCPTransportUnsupported, server.Key, server.Transport)
	}
	return entry, nil
}
