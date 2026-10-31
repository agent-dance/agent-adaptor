package opencode

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/profilejson"
	"github.com/agent-dance/agent-adaptor/internal/testutil"
)

func TestPersistentMCPDeclarationAndCallClear(t *testing.T) {
	root := t.TempDir()
	spec := driver.MCPServerSpec{Key: "persisted", Transport: driver.MCPTransportHTTP, URL: "https://example.test/mcp", BearerTokenEnvVar: "MCP_SECRET", Required: true}
	layout, entries, err := mcpProfilePlan(root, driver.MCPPayload{Servers: []driver.MCPServerSpec{spec}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := profilejson.Sync(context.Background(), layout, entries, ""); err != nil {
		t.Fatal(err)
	}
	payload, disabled, originalFP, err := resolveProfileMCP(driver.Request{}, root)
	if err != nil || len(payload.Servers) != 1 || !payload.Servers[0].Required || len(disabled) != 0 {
		t.Fatalf("resolved=%+v %v", payload, err)
	}
	req := driver.Request{ProfilePayload: driver.ProfilePayload{Declared: driver.ProfileResourceDeclarations{MCP: true}}}
	req.MCP, disabled, _, err = resolveProfileMCP(req, root)
	if err != nil || len(req.MCP.Servers) != 0 || len(disabled) != 1 {
		t.Fatalf("clear: %+v %v %v", req.MCP, disabled, err)
	}
	env, cleanup, err := runEnvironment(req, nil, wantsServer(req), disabled...)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	var config struct {
		MCP map[string]struct{ Enabled bool }
	}
	for _, binding := range env {
		if binding.Name == "OPENCODE_CONFIG_CONTENT" {
			if err := json.Unmarshal([]byte(binding.Value), &config); err != nil {
				t.Fatal(err)
			}
		}
	}
	if value, ok := config.MCP["persisted"]; !ok || value.Enabled {
		t.Fatal("clear did not disable native entry")
	}
	spec.Required = false
	layout, entries, err = mcpProfilePlan(root, driver.MCPPayload{Servers: []driver.MCPServerSpec{spec}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := profilejson.Sync(context.Background(), layout, entries, ""); err != nil {
		t.Fatal(err)
	}
	_, _, newFP, err := resolveProfileMCP(driver.Request{}, root)
	if err != nil || newFP == originalFP {
		t.Fatal("required policy drift omitted from checkpoint guard")
	}
}

func TestMCPManifestDoesNotDuplicateNativeSecrets(t *testing.T) {
	root := t.TempDir()
	layout, entries, err := mcpProfilePlan(root, driver.MCPPayload{Servers: []driver.MCPServerSpec{
		{Key: "local", Transport: driver.MCPTransportStdio, Command: "test", Env: map[string]string{"SECRET": "literal-command-secret"}},
		{Key: "remote", Transport: driver.MCPTransportHTTP, URL: "https://example.test", Headers: map[string]string{"Authorization": "Bearer literal-header-secret"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := profilejson.Sync(context.Background(), layout, entries, ""); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, ".agent-adaptor-profile-manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "literal-command-secret") || strings.Contains(string(raw), "literal-header-secret") {
		t.Fatal("native secrets duplicated into ownership manifest")
	}
	payload, _, _, err := resolveProfileMCP(driver.Request{}, root)
	if err != nil || len(payload.Servers) != 2 || payload.Servers[0].Env["SECRET"] != "literal-command-secret" || payload.Servers[1].Headers["Authorization"] != "Bearer literal-header-secret" {
		t.Fatal("native secrets did not retain transport semantics")
	}
}

func TestRequiredMCPFailurePreventsPrompt(t *testing.T) {
	command, capture := testutil.ProtocolCommand(t, serverHappy, "", 0)
	if err := os.WriteFile(filepath.Join(filepath.Dir(capture), "mcp-status.json"), []byte(`{"required":{"status":"failed","error":"provider detail"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	d := Driver(Config{Command: command, Env: []driver.EnvBinding{{Name: "OPENCODE_CONFIG_DIR", Value: t.TempDir()}}})
	resp, err := d.Run(context.Background(), driver.Request{MCP: driver.MCPPayload{Servers: []driver.MCPServerSpec{{Key: "required", Required: true, Transport: driver.MCPTransportHTTP, URL: "http://127.0.0.1:1"}}}}, &testutil.EventRecorder{})
	if err == nil || !strings.Contains(err.Error(), "not connected") || resp.Checkpoint != nil {
		t.Fatalf("required failure: %v %+v", err, resp)
	}
	if _, err := os.Stat(capture); !os.IsNotExist(err) {
		t.Fatal("prompt delivered without required MCP")
	}
}

func TestServerPersistentMCPPolicyDrift(t *testing.T) {
	f := persistentServerFixture(t)
	f.run(t)
	spec := driver.MCPServerSpec{Key: "durable", Transport: driver.MCPTransportHTTP, URL: "http://127.0.0.1:1", Required: false}
	syncMCP := func() {
		layout, entries, err := mcpProfilePlan(f.profile, driver.MCPPayload{Servers: []driver.MCPServerSpec{spec}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := profilejson.Sync(context.Background(), layout, entries, ""); err != nil {
			t.Fatal(err)
		}
	}
	syncMCP()
	f.run(t)
	if len(f.pids(t)) != 2 {
		t.Fatal("new durable MCP did not replace writer")
	}
	// Required lives in declaration metadata, not native config. Its change
	// must still invalidate the old checkpoint and persistent process.
	spec.Required = true
	syncMCP()
	f.run(t)
	if len(f.pids(t)) != 3 {
		t.Fatal("MCP required policy reused stale writer")
	}
	f.run(t)
	if len(f.pids(t)) != 3 {
		t.Fatal("unchanged MCP restarted writer")
	}
}
