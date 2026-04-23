package mcpruntime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

func TestSyncCodexProfileDedicatedReplacesMCPServersButPreservesOtherConfig(t *testing.T) {
	codexHome := t.TempDir()
	configPath := filepath.Join(codexHome, "config.toml")
	if err := os.WriteFile(configPath, []byte(`
model = "gpt-5.4"

[mcp_servers.old]
command = "old"
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	jsonPath := filepath.Join(codexHome, "config.json")
	originalJSON := `{"model":"should-stay-untouched"}`
	if err := os.WriteFile(jsonPath, []byte(originalJSON), 0o644); err != nil {
		t.Fatalf("write config.json: %v", err)
	}

	err := SyncCodexProfile(codexHome, ProfileKindDedicated, agentadaptor.MCPPayload{
		Servers: []agentadaptor.MCPServerSpec{
			{
				Key:       "stdio-demo",
				Transport: agentadaptor.MCPTransportStdio,
				Command:   "npx",
				Args:      []string{"demo-mcp", "--flag", "value"},
				Env:       map[string]string{"API_KEY": "secret"},
			},
			{
				Key:               "remote-demo",
				Transport:         agentadaptor.MCPTransportHTTP,
				URL:               "https://example.com/mcp",
				BearerTokenEnvVar: "EXAMPLE_TOKEN",
			},
		},
	})
	if err != nil {
		t.Fatalf("sync codex MCP: %v", err)
	}

	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(raw)
	for _, want := range []string{
		`model = 'gpt-5.4'`,
		`[mcp_servers.stdio-demo]`,
		`command = 'npx'`,
		`args = ['demo-mcp', '--flag', 'value']`,
		`[mcp_servers.stdio-demo.env]`,
		`API_KEY = 'secret'`,
		`[mcp_servers.remote-demo]`,
		`url = 'https://example.com/mcp'`,
		`bearer_token_env_var = 'EXAMPLE_TOKEN'`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected Codex config to contain %q, got:\n%s", want, text)
		}
	}
	if strings.Contains(text, "[mcp_servers.old]") {
		t.Fatalf("expected dedicated sync to prune stale MCP server, got:\n%s", text)
	}

	unchangedJSON, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatalf("read config.json: %v", err)
	}
	if string(unchangedJSON) != originalJSON {
		t.Fatalf("expected config.json to remain untouched, got %q", string(unchangedJSON))
	}
}

func TestSyncCodexProfileSharedPreservesUnmanagedServers(t *testing.T) {
	codexHome := t.TempDir()
	configPath := filepath.Join(codexHome, "config.toml")
	if err := os.WriteFile(configPath, []byte(`
[mcp_servers.old]
command = "old"
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if err := SyncCodexProfile(codexHome, ProfileKindShared, agentadaptor.MCPPayload{
		Servers: []agentadaptor.MCPServerSpec{
			{
				Key:       "stdio-demo",
				Transport: agentadaptor.MCPTransportStdio,
				Command:   "npx",
			},
		},
	}); err != nil {
		t.Fatalf("sync shared codex MCP: %v", err)
	}

	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(raw)
	if !strings.Contains(text, "[mcp_servers.old]") || !strings.Contains(text, "[mcp_servers.stdio-demo]") {
		t.Fatalf("expected shared sync to preserve old MCP and add new one, got:\n%s", text)
	}
}
