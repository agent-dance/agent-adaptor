package profilejson

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/agent-dance/agent-adaptor/internal/profilestate"
)

func TestJSONCFailedCommitRestoresNativeBytes(t *testing.T) {
	l := testLayout(t.TempDir())
	ctx := context.Background()
	original := map[string]json.RawMessage{"old": json.RawMessage(`{"value":"original"}`)}
	if _, err := Sync(ctx, l, original, ""); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(l.Root, l.File))
	if err != nil {
		t.Fatal(err)
	}
	path, err := syscall.UTF16PtrFromString(filepath.Join(l.Root, profilestate.ManifestName))
	if err != nil {
		t.Fatal(err)
	}
	handle, err := syscall.CreateFile(path, syscall.GENERIC_READ, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE, nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.CloseHandle(handle)
	if _, err := Sync(ctx, l, map[string]json.RawMessage{"new": json.RawMessage(`{"value":"changed"}`)}, ""); err == nil {
		t.Fatal("manifest sharing violation ignored")
	}
	after, err := os.ReadFile(filepath.Join(l.Root, l.File))
	if err != nil || string(before) != string(after) {
		t.Fatal("failed commit did not restore native bytes")
	}
	state, err := Observe(l, original, "")
	if err != nil || !state.Managed["old"] || len(state.Managed) != 1 {
		t.Fatalf("failed commit changed ownership: %+v %v", state, err)
	}
}

func TestCloneFailedManifestCommitRollsBackNewFiles(t *testing.T) {
	ctx := context.Background()
	source, target := t.TempDir(), t.TempDir()
	layout := testLayout(source)
	if _, err := Sync(ctx, layout, map[string]json.RawMessage{"owned": json.RawMessage(`{"value":1}`)}, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "settings.json"), []byte(`{"model":"test"}`), 0600); err != nil {
		t.Fatal(err)
	}
	manifest, err := profilestate.LoadManifest(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := profilestate.SaveManifest(target, manifest); err != nil {
		t.Fatal(err)
	}
	path, err := syscall.UTF16PtrFromString(filepath.Join(target, profilestate.ManifestName))
	if err != nil {
		t.Fatal(err)
	}
	handle, err := syscall.CreateFile(path, syscall.GENERIC_READ, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE, nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.CloseHandle(handle)
	plan := ClonePlan{Files: []string{"settings.json", layout.File}, KeepField: func(string) bool { return true }, Resources: []Layout{layout}}
	if err := Clone(ctx, source, target, plan); err == nil {
		t.Fatal("manifest sharing violation ignored")
	}
	for _, file := range plan.Files {
		if _, err := os.Stat(filepath.Join(target, file)); !os.IsNotExist(err) {
			t.Fatalf("failed clone left %s", file)
		}
	}
	entries, err := os.ReadDir(target)
	if err != nil || len(entries) != 1 || entries[0].Name() != profilestate.ManifestName {
		t.Fatal("failed clone left staging files or locks")
	}
	state, err := Observe(testLayout(target), nil, "")
	if err != nil || len(state.Managed) != 0 {
		t.Fatal("failed clone changed destination ownership")
	}
}
