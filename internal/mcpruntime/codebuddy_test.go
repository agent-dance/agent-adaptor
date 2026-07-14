package mcpruntime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

func TestSyncCodeBuddyProfileRendersTransportType(t *testing.T) {
	configDir := t.TempDir()
	path := filepath.Join(configDir, "mcp.json")

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
		`"transportType": "streamable-http"`,
		`"Authorization": "Bearer ${CB_TOKEN}"`,
		`"remote-sse"`,
		`"transportType": "sse"`,
		`"local-stdio"`,
		`"command": "npx"`,
		`"API_KEY": "secret"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected codebuddy mcp.json to contain %q, got:\n%s", want, text)
		}
	}
	// CodeBuddy stdio servers must NOT carry a "type" field (unlike claude).
	if strings.Contains(text, `"type": "stdio"`) {
		t.Fatalf("codebuddy stdio server should not have a type field, got:\n%s", text)
	}
}
