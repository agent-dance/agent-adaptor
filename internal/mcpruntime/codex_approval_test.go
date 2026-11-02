package mcpruntime

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/agent-dance/agent-adaptor/v2/driver"
)

func TestF2CodexMCPPolicyConfig(t *testing.T) {
	for _, tc := range []struct {
		name       string
		permission driver.HumanDecisionMode
		isolation  driver.IsolationLevel
		mode       string
		enabled    bool
	}{
		{"default", "", "", "prompt", true},
		{"auto", driver.HumanDecisionAutoApprove, "", "approve", true},
		{"hitl", driver.HumanDecisionAsk, "", "prompt", true},
		{"deny", driver.HumanDecisionAutoReject, "", "prompt", true},
		{"read-only", driver.HumanDecisionAutoApprove, driver.IsolationReadOnly, "approve", true},
		{"read-only-hitl", driver.HumanDecisionAsk, driver.IsolationReadOnly, "prompt", true},
		{"read-only-deny", driver.HumanDecisionAutoReject, driver.IsolationReadOnly, "prompt", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("[mcp_servers.native]\ncommand='native'\ndefault_tools_approval_mode='auto'\n"), 0600); err != nil {
				t.Fatal(err)
			}
			payload := hostedToolsPayload("http://127.0.0.1:12345/mcp")
			payload.Servers = append(payload.Servers, driver.MCPServerSpec{Key: "host-supplied", Transport: driver.MCPTransportStdio, Command: "server"})
			_, err := SyncResource(context.Background(), "codex", dir, ProfileKindShared, payload, driver.RunPolicy{Isolation: tc.isolation, HumanDecision: driver.HumanDecisionPolicy{Permission: tc.permission}})
			if err != nil {
				t.Fatal(err)
			}
			root, err := readTOMLObject(filepath.Join(dir, "config.toml"))
			if err != nil {
				t.Fatal(err)
			}
			servers := root["mcp_servers"].(map[string]any)
			for _, spec := range payload.Servers {
				server := servers[spec.Key].(map[string]any)
				if server["default_tools_approval_mode"] != tc.mode || server["enabled"] != tc.enabled {
					t.Fatalf("%s: %+v", spec.Key, server)
				}
			}
			native := servers["native"].(map[string]any)
			if native["default_tools_approval_mode"] != "auto" || native["enabled"] != nil {
				t.Fatalf("native server mutated: %+v", native)
			}
		})
	}
}

// Models the native map produced by PR #26's exact tool policy renderer. The
// derived default must not rewrite any exact-tool authority or explicit default.
func TestF2CodexMCPExplicitPoliciesOverrideDerivedDefault(t *testing.T) {
	for _, mode := range []driver.HumanDecisionMode{driver.HumanDecisionAsk, driver.HumanDecisionAutoApprove} {
		for _, explicit := range []string{"", "prompt", "approve"} {
			t.Run(string(mode)+"/"+explicit, func(t *testing.T) {
				tools := map[string]any{"team_delegate": map[string]any{"approval_mode": "prompt"}, "exact-write": map[string]any{"approval_mode": "approve"}}
				entry := map[string]any{"tools": tools}
				if explicit != "" {
					entry["default_tools_approval_mode"] = explicit
				}
				applyCodexMCPApprovalDefault(entry, driver.RunPolicy{Isolation: driver.IsolationReadOnly, HumanDecision: driver.HumanDecisionPolicy{Permission: mode}})
				want := explicit
				if want == "" {
					want = "prompt"
					if mode == driver.HumanDecisionAutoApprove {
						want = "approve"
					}
				}
				expectedTools := map[string]any{"team_delegate": map[string]any{"approval_mode": "prompt"}, "exact-write": map[string]any{"approval_mode": "approve"}}
				if entry["default_tools_approval_mode"] != want || entry["enabled"] != true || !reflect.DeepEqual(entry["tools"], expectedTools) {
					t.Fatalf("explicit authority overwritten: %+v", entry)
				}
			})
		}
	}
}
