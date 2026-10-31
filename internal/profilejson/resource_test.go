package profilejson

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/profilestate"
)

func testLayout(root string) Layout {
	return Layout{Provider: "test", Root: root, File: "config.jsonc", Field: "mcp", Kind: driver.ProfileResourceMCP, Materialization: driver.ProfileResourceMaterializationNativeManaged}
}

func TestJSONCResourceOwnershipAndPreservation(t *testing.T) {
	ctx := context.Background()
	l := testLayout(filepath.Join(t.TempDir(), "missing"))
	before, err := Observe(l, nil, "")
	if err != nil || len(before.Entries) != 0 {
		t.Fatalf("missing: %+v %v", before, err)
	}
	if _, err := os.Stat(l.Root); !os.IsNotExist(err) {
		t.Fatal("observation initialized profile")
	}
	if err := os.MkdirAll(l.Root, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(l.Root, l.File)
	original := "{\n // keep leading comment\n \"large\": 9007199254740993,\n \"mcp\": {\"external\": {\"url\":\"https://example.test\"},},\n // keep trailing comment\n}\n"
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	key := "tool/~/世界"
	desired := map[string]json.RawMessage{key: json.RawMessage(`{"z":2,"a":1}`)}
	state, err := Sync(ctx, l, desired, "desired")
	if err != nil {
		t.Fatal(err)
	}
	if !state.Managed[key] || len(state.Snapshot.External) != 1 || state.Snapshot.Fingerprint != "desired" || len(state.Snapshot.Warnings) != 0 {
		t.Fatalf("ownership=%+v", state)
	}
	raw, _ := os.ReadFile(path)
	for _, literal := range []string{"// keep leading comment", "// keep trailing comment", "9007199254740993", `"external"`} {
		if !strings.Contains(string(raw), literal) {
			t.Fatalf("lost native syntax %q", literal)
		}
	}
	if _, err := Sync(ctx, l, map[string]json.RawMessage{"external": json.RawMessage(`{}`)}, ""); err == nil {
		t.Fatal("external entry adopted")
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(raw) {
		t.Fatal("conflict changed config")
	}
	if _, err := Sync(ctx, l, nil, ""); err != nil {
		t.Fatal(err)
	}
	state, err = Observe(l, nil, "")
	if err != nil || len(state.Managed) != 0 || len(state.Entries) != 1 || state.Entries["external"] == nil {
		t.Fatalf("clear=%+v %v", state, err)
	}
}

func TestJSONCExternalEditAndManifestEscape(t *testing.T) {
	l := testLayout(t.TempDir())
	ctx := context.Background()
	desired := map[string]json.RawMessage{"owned": json.RawMessage(`{"value":"original"}`)}
	if _, err := Sync(ctx, l, desired, ""); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(l.Root, l.File)
	raw, _ := os.ReadFile(path)
	changed := strings.ReplaceAll(string(raw), "original", "external")
	if err := os.WriteFile(path, []byte(changed), 0600); err != nil {
		t.Fatal(err)
	}
	state, err := Observe(l, desired, "")
	if err != nil || len(state.Managed) != 0 || len(state.Snapshot.External) != 1 {
		t.Fatalf("external edit: %+v %v", state, err)
	}
	for _, entries := range []map[string]json.RawMessage{desired, nil} {
		if _, err := Sync(ctx, l, entries, ""); err == nil {
			t.Fatal("external edit overwritten")
		}
	}
	current, _ := os.ReadFile(path)
	if string(current) != changed {
		t.Fatal("external bytes changed")
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	manifest, err := profilestate.LoadManifest(l.Root)
	if err != nil {
		t.Fatal(err)
	}
	entry, _ := manifest.Entry("mcp", "owned")
	entry.Path = filepath.Join(t.TempDir(), "outside.json")
	manifest.Set(entry)
	if err := profilestate.SaveManifest(l.Root, manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := Sync(ctx, l, nil, ""); err == nil {
		t.Fatal("manifest escape accepted")
	}
	if _, err := os.Stat(entry.Path); !os.IsNotExist(err) {
		t.Fatal("manifest escape touched outside path")
	}
}

func TestJSONCPreservesNativeStringEscapes(t *testing.T) {
	l := testLayout(t.TempDir())
	desired := map[string]json.RawMessage{"owned": json.RawMessage(`{"literal":"\u007bfile:missing-file}","nested":{"z":2,"a":1}}`)}
	state, err := Sync(context.Background(), l, desired, "")
	if err != nil || !state.Managed["owned"] {
		t.Fatalf("sync: %v", err)
	}
	path := filepath.Join(l.Root, l.File)
	raw, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(raw), `\u007bfile:missing-file}`) {
		t.Fatal("native literal was converted to a template")
	}
	// Whitespace/order do not change ownership, but template-enabling edits do.
	changed := strings.ReplaceAll(string(raw), `\u007bfile:`, "{file:")
	if err := os.WriteFile(path, []byte(changed), 0600); err != nil {
		t.Fatal(err)
	}
	state, err = Observe(l, desired, "")
	if err != nil || state.Managed["owned"] {
		t.Fatal("template-enabling edit retained ownership")
	}
	if _, err := Sync(context.Background(), l, desired, ""); err == nil {
		t.Fatal("template-enabling edit was overwritten")
	}
}

func TestJSONCRejectsAmbiguousAndIndirectFiles(t *testing.T) {
	for _, raw := range []string{`null`, `[]`, `{"mcp":{},"mcp":{}}`, `{"mcp":{"a":{},"a":{}}}`, `{"mcp":[]}`, `{broken`} {
		l := testLayout(t.TempDir())
		path := filepath.Join(l.Root, l.File)
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Sync(context.Background(), l, map[string]json.RawMessage{"a": json.RawMessage(`{}`)}, ""); err == nil {
			t.Fatalf("accepted %s", raw)
		}
		after, _ := os.ReadFile(path)
		if string(after) != raw {
			t.Fatal("invalid document changed")
		}
	}
	l := testLayout(filepath.Join(t.TempDir(), "absent"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Sync(ctx, l, nil, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
	if _, err := os.Stat(l.Root); !os.IsNotExist(err) {
		t.Fatal("canceled sync initialized profile")
	}
	l = testLayout(t.TempDir())
	outside := filepath.Join(t.TempDir(), "external.json")
	if err := os.WriteFile(outside, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(l.Root, l.File)); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := Sync(context.Background(), l, map[string]json.RawMessage{"a": json.RawMessage(`{}`)}, ""); err == nil {
		t.Fatal("followed native config symlink")
	}
}
