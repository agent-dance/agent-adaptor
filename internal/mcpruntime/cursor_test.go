package mcpruntime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentadaptor "github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/profilestate"
)

func TestSyncCursorProfileDedicatedPreservesExternalServersAndApprovalState(t *testing.T) {
	cursorHome := t.TempDir()
	path := filepath.Join(cursorHome, "mcp.json")
	if err := os.WriteFile(path, []byte(`{
  "approvalState": {"approved": ["keep-me"]},
  "mcpServers": {
    "old": {"type":"stdio","command":"old"}
  }
}`), 0o644); err != nil {
		t.Fatalf("write cursor config: %v", err)
	}

	if err := SyncCursorProfile(cursorHome, ProfileKindDedicated, agentadaptor.MCPPayload{
		Servers: []agentadaptor.MCPServerSpec{
			{
				Key:               "remote-demo",
				Transport:         agentadaptor.MCPTransportHTTP,
				URL:               "https://example.com/mcp",
				BearerTokenEnvVar: "CURSOR_MCP_TOKEN",
			},
			{
				Key:       "stdio-demo",
				Transport: agentadaptor.MCPTransportStdio,
				Command:   "npx",
				Args:      []string{"demo-mcp"},
				Env:       map[string]string{"API_KEY": "secret"},
			},
		},
	}); err != nil {
		t.Fatalf("sync cursor profile: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read cursor config: %v", err)
	}
	text := string(raw)
	if !strings.Contains(text, `"old"`) {
		t.Fatalf("expected dedicated Cursor sync to preserve external server, got:\n%s", text)
	}
	for _, want := range []string{
		`"approvalState": {`,
		`"remote-demo"`,
		`"type": "http"`,
		`"Authorization": "Bearer ${env:CURSOR_MCP_TOKEN}"`,
		`"stdio-demo"`,
		`"type": "stdio"`,
		`"API_KEY": "secret"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected Cursor config to contain %q, got:\n%s", want, text)
		}
	}
}

func TestSyncCursorProfileSharedPreservesExistingServers(t *testing.T) {
	cursorHome := t.TempDir()
	path := filepath.Join(cursorHome, "mcp.json")
	if err := os.WriteFile(path, []byte(`{"mcpServers":{"old":{"type":"stdio","command":"old"}}}`), 0o644); err != nil {
		t.Fatalf("write cursor config: %v", err)
	}

	if err := SyncCursorProfile(cursorHome, ProfileKindShared, agentadaptor.MCPPayload{
		Servers: []agentadaptor.MCPServerSpec{
			{
				Key:       "remote-demo",
				Transport: agentadaptor.MCPTransportSSE,
				URL:       "https://example.com/sse",
			},
		},
	}); err != nil {
		t.Fatalf("sync shared cursor profile: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read cursor config: %v", err)
	}
	text := string(raw)
	if !strings.Contains(text, `"old"`) || !strings.Contains(text, `"remote-demo"`) {
		t.Fatalf("expected shared Cursor sync to preserve old server and add new one, got:\n%s", text)
	}
}

func TestSnapshotResourceReportsManagedAndExternalCursorServers(t *testing.T) {
	cursorHome := t.TempDir()
	path := filepath.Join(cursorHome, "mcp.json")
	if err := os.WriteFile(path, []byte(`{"mcpServers":{"external":{"type":"stdio","command":"old"},"managed":{"type":"stdio","command":"npx"}}}`), 0o644); err != nil {
		t.Fatalf("write cursor config: %v", err)
	}
	manifest := profilestate.Manifest{}
	manifest.Set(profilestate.ManifestEntry{
		Kind: resourceKind,
		Key:  "managed",
		Path: path,
	})
	if err := profilestate.SaveManifest(cursorHome, manifest); err != nil {
		t.Fatalf("save manifest: %v", err)
	}

	snapshot, err := SnapshotResource("cursor", cursorHome, agentadaptor.MCPPayload{Fingerprint: "mcp-fp"}, true)
	if err != nil {
		t.Fatalf("snapshot resource: %v", err)
	}
	if !sameStrings(snapshot.Managed, []string{"managed"}) || !sameStrings(snapshot.External, []string{"external"}) {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
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
