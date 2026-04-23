package mcpruntime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

func TestSyncClaudeProfileDedicatedReplacesMCPServersAndInterpolatesBearerEnv(t *testing.T) {
	configDir := t.TempDir()
	path := filepath.Join(configDir, ".claude.json")
	if err := os.WriteFile(path, []byte(`{
  "firstStartTime": "2026-04-23T13:03:25.725Z",
  "mcpServers": {
    "old": {"type":"stdio","command":"old"}
  }
}`), 0o644); err != nil {
		t.Fatalf("write claude config: %v", err)
	}

	if err := SyncClaudeProfile(configDir, ProfileKindDedicated, agentadaptor.MCPPayload{
		Servers: []agentadaptor.MCPServerSpec{
			{
				Key:       "remote-demo",
				Transport: agentadaptor.MCPTransportHTTP,
				URL:       "https://example.com/mcp",
				Headers:   map[string]string{"X-API-Key": "demo"},
			},
			{
				Key:               "remote-auth",
				Transport:         agentadaptor.MCPTransportSSE,
				URL:               "https://example.com/sse",
				BearerTokenEnvVar: "MCP_TOKEN",
			},
		},
	}); err != nil {
		t.Fatalf("sync claude profile: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read claude config: %v", err)
	}
	text := string(raw)
	if strings.Contains(text, `"old"`) {
		t.Fatalf("expected dedicated Claude sync to prune stale server, got:\n%s", text)
	}
	for _, want := range []string{
		`"firstStartTime": "2026-04-23T13:03:25.725Z"`,
		`"type": "http"`,
		`"type": "sse"`,
		`"Authorization": "Bearer ${MCP_TOKEN}"`,
		`"X-API-Key": "demo"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected Claude config to contain %q, got:\n%s", want, text)
		}
	}
}

func TestSyncClaudeProfileSharedPreservesExistingServers(t *testing.T) {
	configDir := t.TempDir()
	path := filepath.Join(configDir, ".claude.json")
	if err := os.WriteFile(path, []byte(`{"mcpServers":{"old":{"type":"stdio","command":"old"}}}`), 0o644); err != nil {
		t.Fatalf("write claude config: %v", err)
	}

	if err := SyncClaudeProfile(configDir, ProfileKindShared, agentadaptor.MCPPayload{
		Servers: []agentadaptor.MCPServerSpec{
			{
				Key:       "stdio-demo",
				Transport: agentadaptor.MCPTransportStdio,
				Command:   "npx",
			},
		},
	}); err != nil {
		t.Fatalf("sync shared claude profile: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read claude config: %v", err)
	}
	text := string(raw)
	if !strings.Contains(text, `"old"`) || !strings.Contains(text, `"stdio-demo"`) {
		t.Fatalf("expected shared Claude sync to preserve old server and add new one, got:\n%s", text)
	}
}
