package mcpruntime

import (
	"context"
	"encoding/json"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/profilestate"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHostedCompatibilityNormalizesOnlyProvenOwnedEntry(t *testing.T) {
	for _, provider := range []string{"claude", "codex", "cursor", "codebuddy"} {
		t.Run(provider, func(t *testing.T) {
			dir := t.TempDir()
			path, raw, err := HostedCompatibilityBaseline(provider, dir)
			if err != nil || len(raw) != 0 {
				t.Fatal(err)
			}
			if _, err := SyncResource(context.Background(), provider, dir, ProfileKindHostManaged, hostedToolsPayload("http://127.0.0.1:1/mcp")); err != nil {
				t.Fatal(err)
			}
			_, normalized, err := HostedCompatibilityBaseline(provider, dir)
			if err != nil || len(normalized) != 0 {
				t.Fatalf("owned entry changed baseline: %s %v", normalized, err)
			}
			l, _ := layoutFor(provider, dir)
			root, err := readStructuredRoot(l)
			if err != nil {
				t.Fatal(err)
			}
			root["unknown_user_setting"] = "preserve"
			if err := writeStructuredRoot(l, root); err != nil {
				t.Fatal(err)
			}
			_, normalized, err = HostedCompatibilityBaseline(provider, dir)
			if err != nil || !strings.Contains(string(normalized), "unknown_user_setting") {
				t.Fatal("unknown user configuration was lost")
			}
			if path != filepath.Base(l.path) {
				t.Fatal("wrong provider layout")
			}
		})
	}
}
func TestEveryNewManagedMCPRecordsRenderedFingerprint(t *testing.T) {
	dir := t.TempDir()
	payload := driver.MCPPayload{Servers: []driver.MCPServerSpec{{Key: "ordinary", Transport: driver.MCPTransportHTTP, URL: "https://example.invalid"}}}
	if _, err := SyncResource(context.Background(), "claude", dir, ProfileKindHostManaged, payload); err != nil {
		t.Fatal(err)
	}
	m, err := profilestate.LoadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	entry, _ := m.Entry(resourceKind, "ordinary")
	if len(entry.Metadata["rendered_fingerprint"]) != 64 {
		t.Fatal("ordinary entry lacks render proof")
	}
	_, raw, err := HostedCompatibilityBaseline("claude", dir)
	if err != nil || !strings.Contains(string(raw), "ordinary") {
		t.Fatal("ordinary actual configuration was dropped")
	}
}
func TestStrictProfileJSONKeepsLargeNumbersAndRejectsDuplicates(t *testing.T) {
	raw, err := StrictProfileJSON([]byte(`{"large":9007199254740993,"other":{"value":1}}`))
	if err != nil || !strings.Contains(string(raw), "9007199254740993") {
		t.Fatalf("integer changed: %s %v", raw, err)
	}
	if _, err := StrictProfileJSON([]byte(`{"v":1,"v":2}`)); err == nil {
		t.Fatal("duplicate key accepted")
	}
	var v map[string]json.RawMessage
	json.Unmarshal(raw, &v)
	if string(v["large"]) != "9007199254740993" {
		t.Fatal("integer rounded")
	}
}
func TestHostedCleanupRejectsLinkedConfiguration(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(outside, []byte("do not touch"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, ".claude.json")); err != nil {
		t.Fatal(err)
	}
	if err := RemoveHostedToolProfile(context.Background(), "claude", dir); err == nil {
		t.Fatal("linked config accepted")
	}
	raw, _ := os.ReadFile(outside)
	if string(raw) != "do not touch" {
		t.Fatal("external target modified")
	}
}
