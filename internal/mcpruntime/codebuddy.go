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

// codebuddyServerConfig renders one MCP server into CodeBuddy's mcp.json shape.
// CodeBuddy 用 transportType 表示远程传输（streamable-http / sse），与
// claude/cursor 的 type 字段写法不同，因此需要专属 render。
func codebuddyServerConfig(server agentadaptor.MCPServerSpec) (map[string]any, error) {
	headers, err := mergeBearerHeader(server.Headers, server.BearerTokenEnvVar, "${%s}", server.Key)
	if err != nil {
		return nil, err
	}

	entry := map[string]any{}
	switch server.Transport {
	case agentadaptor.MCPTransportStdio:
		entry["command"] = server.Command
		if len(server.Args) > 0 {
			entry["args"] = append([]string(nil), server.Args...)
		}
		if len(server.Env) > 0 {
			entry["env"] = cloneStringMapAny(server.Env)
		}
	case agentadaptor.MCPTransportHTTP:
		entry["url"] = server.URL
		entry["transportType"] = "streamable-http"
		if len(headers) > 0 {
			entry["headers"] = headers
		}
	case agentadaptor.MCPTransportSSE:
		entry["url"] = server.URL
		entry["transportType"] = "sse"
		if len(headers) > 0 {
			entry["headers"] = headers
		}
	default:
		return nil, fmt.Errorf("%w: CodeBuddy MCP server %q does not support transport %q", agentadaptor.ErrMCPTransportUnsupported, server.Key, server.Transport)
	}
	return entry, nil
}
