package mcpruntime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/internal/profilestate"
)

func TestSyncCodexProfileDedicatedPrunesOnlyManagedServersAndWritesManifest(t *testing.T) {
	codexHome := t.TempDir()
	configPath := filepath.Join(codexHome, "config.toml")
	if err := os.WriteFile(configPath, []byte(`
model = "gpt-5.4"

[mcp_servers.external]
command = "external"

[mcp_servers.stale]
command = "stale"
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	manifest := profilestate.Manifest{}
	manifest.Set(profilestate.ManifestEntry{
		Kind: resourceKind,
		Key:  "stale",
		Path: configPath,
	})
	if err := profilestate.SaveManifest(codexHome, manifest); err != nil {
		t.Fatalf("save manifest: %v", err)
	}
	jsonPath := filepath.Join(codexHome, "config.json")
	originalJSON := `{"model":"should-stay-untouched"}`
	if err := os.WriteFile(jsonPath, []byte(originalJSON), 0o644); err != nil {
		t.Fatalf("write config.json: %v", err)
	}

	snapshot, err := SyncResource(context.Background(), "codex", codexHome, ProfileKindDedicated, agentadaptor.MCPPayload{
		Fingerprint: "mcp-fp",
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
	if !sameStrings(snapshot.Managed, []string{"remote-demo", "stdio-demo"}) {
		t.Fatalf("unexpected managed snapshot: %#v", snapshot)
	}
	if !sameStrings(snapshot.External, []string{"external"}) {
		t.Fatalf("expected external server to be preserved, got %#v", snapshot.External)
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
		`[mcp_servers.external]`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected Codex config to contain %q, got:\n%s", want, text)
		}
	}
	if strings.Contains(text, "[mcp_servers.stale]") {
		t.Fatalf("expected dedicated sync to prune stale managed MCP server, got:\n%s", text)
	}

	unchangedJSON, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatalf("read config.json: %v", err)
	}
	if string(unchangedJSON) != originalJSON {
		t.Fatalf("expected config.json to remain untouched, got %q", string(unchangedJSON))
	}
	manifest, err = profilestate.LoadManifest(codexHome)
	if err != nil {
		t.Fatalf("reload manifest: %v", err)
	}
	if _, ok := manifest.Entry(resourceKind, "stale"); ok {
		t.Fatalf("expected stale manifest entry to be removed, got %#v", manifest.KindEntries(resourceKind))
	}
	for _, key := range []string{"stdio-demo", "remote-demo"} {
		entry, ok := manifest.Entry(resourceKind, key)
		if !ok || filepath.Clean(entry.Path) != filepath.Clean(configPath) {
			t.Fatalf("expected manifest entry for %q, got %#v", key, entry)
		}
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
