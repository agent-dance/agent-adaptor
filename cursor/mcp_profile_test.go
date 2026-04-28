package cursor

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

func TestSyncProfileResourcesReportsManagedAndExternalMCP(t *testing.T) {
	profileDir := t.TempDir()
	path := filepath.Join(profileDir, "mcp.json")
	if err := os.WriteFile(path, []byte(`{"mcpServers":{"external":{"type":"stdio","command":"external"}}}`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	snapshot, err := adapter{}.SyncProfileResources(
		context.Background(),
		agentadaptor.CursorConfig{},
		agentadaptor.AgentIdentity{},
		&agentadaptor.ProfileSelection{Mode: agentadaptor.ProfileModeDedicated, Dir: profileDir},
		agentadaptor.ProfilePayload{
			MCP: agentadaptor.MCPPayload{
				Fingerprint: "mcp-fp",
				Servers: []agentadaptor.MCPServerSpec{{
					Key:       "managed",
					Transport: agentadaptor.MCPTransportStdio,
					Command:   "npx",
				}},
			},
		},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("sync profile resources: %v", err)
	}
	resource := resourceByKind(t, snapshot, agentadaptor.ProfileResourceMCP)
	if !sameStrings(resource.Managed, []string{"managed"}) || !sameStrings(resource.External, []string{"external"}) {
		t.Fatalf("unexpected MCP resource snapshot: %#v", resource)
	}
}

func resourceByKind(t *testing.T, snapshot agentadaptor.ProfileSnapshot, kind agentadaptor.ProfileResourceKind) agentadaptor.ResourceSnapshot {
	t.Helper()
	for _, resource := range snapshot.Resources {
		if resource.Kind == kind {
			return resource
		}
	}
	t.Fatalf("missing resource %s", kind)
	return agentadaptor.ResourceSnapshot{}
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
