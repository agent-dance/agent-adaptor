package mcpruntime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentadaptor "github.com/agent-dance/agent-adaptor/driver"
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

func TestSyncCodexEmptyUnmanagedPayloadDoesNotRewriteConfig(t *testing.T) {
	codexHome := t.TempDir()
	configPath := filepath.Join(codexHome, "config.toml")
	original := []byte(`model_provider = "custom"

[[skills.config]]
path = "/native/custom/SKILL.md"
enabled = false

[model_providers.custom]
base_url = "https://example.invalid"
wire_api = "responses"
`)
	if err := os.WriteFile(configPath, original, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if _, err := SyncResource(context.Background(), "codex", codexHome, ProfileKindDedicated, agentadaptor.MCPPayload{}); err != nil {
		t.Fatalf("sync empty Codex MCP: %v", err)
	}
	got, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if string(got) != string(original) {
		t.Fatalf("empty unmanaged MCP sync rewrote config:\n%s", got)
	}
}

func TestSyncCodexProjectsExactToolApprovalsAndPrunesStaleAuthority(t *testing.T) {
	codexHome := t.TempDir()
	payload := agentadaptor.MCPPayload{
		Servers: []agentadaptor.MCPServerSpec{{
			Key: "yangxia-agent-ui", Transport: agentadaptor.MCPTransportHTTP,
			URL: "http://127.0.0.1:21901/mcp", BearerTokenEnvVar: "YANGXIA_RUN_TOKEN",
			Tools: map[string]agentadaptor.MCPToolPolicy{
				"yangxia_ui_close":       {ApprovalMode: agentadaptor.MCPToolApprovalApprove},
				"yangxia_ui_open":        {ApprovalMode: agentadaptor.MCPToolApprovalApprove},
				"yangxia_ui_patch":       {ApprovalMode: agentadaptor.MCPToolApprovalApprove},
				"yangxia_ui_wait_action": {ApprovalMode: agentadaptor.MCPToolApprovalApprove},
			},
		}},
	}
	if _, err := SyncResource(context.Background(), "codex", codexHome, ProfileKindDedicated, payload); err != nil {
		t.Fatalf("sync Codex approvals: %v", err)
	}
	configPath := filepath.Join(codexHome, "config.toml")
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(raw)
	for tool := range payload.Servers[0].Tools {
		if !strings.Contains(text, "[mcp_servers.yangxia-agent-ui.tools."+tool+"]") {
			t.Fatalf("missing exact tool approval for %q:\n%s", tool, text)
		}
	}
	if strings.Contains(text, "default_tools_approval_mode") || strings.Contains(text, "approval_policy") {
		t.Fatalf("exact policy widened to server/global approval:\n%s", text)
	}
	if strings.Contains(text, "YANGXIA_SECRET_VALUE") {
		t.Fatalf("secret value entered generated config:\n%s", text)
	}

	payload.Servers[0].Tools = nil
	if _, err := SyncResource(context.Background(), "codex", codexHome, ProfileKindDedicated, payload); err != nil {
		t.Fatalf("sync Codex without approvals: %v", err)
	}
	raw, err = os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read pruned config: %v", err)
	}
	text = string(raw)
	if strings.Contains(text, ".tools.") || strings.Contains(text, "approval_mode") {
		t.Fatalf("stale exact-tool authority survived sync:\n%s", text)
	}
}
