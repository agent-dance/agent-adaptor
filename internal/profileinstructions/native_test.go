package profileinstructions

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/agent-dance/agent-adaptor/internal/profilestate"
)

func TestNativeInstructionManifestCannotEscapeProfile(t *testing.T) {
	dir := t.TempDir()
	external := filepath.Join(t.TempDir(), "external.md")
	if err := os.WriteFile(external, []byte("external"), 0600); err != nil {
		t.Fatal(err)
	}
	manifest := profilestate.Manifest{}
	manifest.Set(profilestate.ManifestEntry{Kind: resourceKind, Key: "untrusted", Path: external, Metadata: map[string]string{"provider": "pi", "content_sha256": nativeContentHash([]byte("external"))}})
	if err := profilestate.SaveManifest(dir, manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := SyncNativeFiles(context.Background(), "pi", dir, []string{"SYSTEM.md"}, nil); err == nil {
		t.Fatal("external manifest path accepted")
	}
	if raw, err := os.ReadFile(external); err != nil || string(raw) != "external" {
		t.Fatal("external file changed")
	}
}

func TestNativeInstructionCanceledSyncDoesNotInitialize(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "absent")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := SyncNativeFiles(ctx, "pi", dir, []string{"SYSTEM.md"}, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("canceled sync initialized profile")
	}
}

func TestNativeInstructionSyncPreservesSymlink(t *testing.T) {
	dir := t.TempDir()
	external := filepath.Join(t.TempDir(), "external.md")
	if err := os.WriteFile(external, []byte("external"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "SYSTEM.md")
	if err := os.Symlink(external, path); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := SyncNativeFiles(context.Background(), "pi", dir, []string{"SYSTEM.md"}, &NativeFile{Name: "SYSTEM.md", Key: "desired", Content: "overwrite"}); err == nil {
		t.Fatal("external symlink overwritten")
	}
	if raw, err := os.ReadFile(external); err != nil || string(raw) != "external" {
		t.Fatal("symlink target changed")
	}
	if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink replaced")
	}
}
