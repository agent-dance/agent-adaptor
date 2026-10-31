package mcpruntime

import (
	"context"
	"testing"

	"github.com/agent-dance/agent-adaptor/driver"
)

func TestDeclaredMCPSyncPreservesUndeclaredAndExternal(t *testing.T) {
	for _, provider := range []string{"claude", "codex", "cursor", "codebuddy"} {
		t.Run(provider, func(t *testing.T) {
			root := t.TempDir()
			ctx := context.Background()
			payload := driver.MCPPayload{Servers: []driver.MCPServerSpec{{Key: "managed", Transport: driver.MCPTransportStdio, Command: "test-command"}}}
			if _, err := SyncDeclaredResource(ctx, provider, root, ProfileKindShared, payload, true); err != nil {
				t.Fatal(err)
			}
			state, err := SyncDeclaredResource(ctx, provider, root, ProfileKindHostManaged, driver.MCPPayload{}, false)
			if err != nil || len(state.Managed) != 1 {
				t.Fatalf("undeclared MCP lost: %+v %v", state, err)
			}
			layout, err := layoutFor(provider, root)
			if err != nil {
				t.Fatal(err)
			}
			native, err := readStructuredRoot(layout)
			if err != nil {
				t.Fatal(err)
			}
			entries, err := sectionMap(native, layout.field)
			if err != nil {
				t.Fatal(err)
			}
			entries["external"] = map[string]any{"command": "external-command"}
			if err := writeSection(layout, native, entries); err != nil {
				t.Fatal(err)
			}
			state, err = SyncDeclaredResource(ctx, provider, root, ProfileKindShared, driver.MCPPayload{}, true)
			if err != nil || len(state.Managed) != 0 || len(state.External) != 1 {
				t.Fatalf("explicit clear=%+v %v", state, err)
			}
			if _, err := SyncDeclaredResource(ctx, provider, root, ProfileKindShared, payload, true); err != nil {
				t.Fatal(err)
			}
			native, err = readStructuredRoot(layout)
			if err != nil {
				t.Fatal(err)
			}
			entries, err = sectionMap(native, layout.field)
			if err != nil {
				t.Fatal(err)
			}
			entries["managed"] = map[string]any{"command": "externally-edited"}
			if err := writeSection(layout, native, entries); err != nil {
				t.Fatal(err)
			}
			if _, err := SyncDeclaredResource(ctx, provider, root, ProfileKindShared, driver.MCPPayload{}, true); err == nil {
				t.Fatal("externally edited entry removed")
			}
		})
	}
}
