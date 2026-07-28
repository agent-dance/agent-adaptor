package mcpruntime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentadaptor "github.com/agent-dance/agent-adaptor/driver"
)

func TestSyncCodeBuddyProfileRendersType(t *testing.T) {
	configDir := t.TempDir()
	path := filepath.Join(configDir, ".mcp.json")

	if err := SyncCodeBuddyProfile(configDir, ProfileKindDedicated, agentadaptor.MCPPayload{
		Servers: []agentadaptor.MCPServerSpec{
			{
				Key:               "remote-http",
				Transport:         agentadaptor.MCPTransportHTTP,
				URL:               "https://example.com/mcp",
				BearerTokenEnvVar: "CB_TOKEN",
			},
			{
				Key:       "remote-sse",
				Transport: agentadaptor.MCPTransportSSE,
				URL:       "https://example.com/sse",
			},
			{
				Key:       "local-stdio",
				Transport: agentadaptor.MCPTransportStdio,
				Command:   "npx",
				Args:      []string{"demo-mcp"},
				Env:       map[string]string{"API_KEY": "secret"},
			},
		},
	}); err != nil {
		t.Fatalf("sync codebuddy profile: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read codebuddy config: %v", err)
	}
	text := string(raw)
	for _, want := range []string{
		`"mcpServers"`,
		`"remote-http"`,
		`"type": "http"`,
		`"Authorization": "Bearer ${CB_TOKEN}"`,
		`"remote-sse"`,
		`"type": "sse"`,
		`"local-stdio"`,
		`"type": "stdio"`,
		`"command": "npx"`,
		`"API_KEY": "secret"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected codebuddy .mcp.json to contain %q, got:\n%s", want, text)
		}
	}
	// 对齐正式合同：使用 type 字段而非 provider 的 transportType 变体。
	if strings.Contains(text, "transportType") {
		t.Fatalf("codebuddy MCP config should not use transportType, got:\n%s", text)
	}
}
