package profilestate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAtomicWriteFileReplacesContentAndCleansTemp(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "settings", "config.json")

	if err := AtomicWriteFile(path, []byte("one"), 0o644); err != nil {
		t.Fatalf("write initial: %v", err)
	}
	if err := AtomicWriteFile(path, []byte("two"), 0o644); err != nil {
		t.Fatalf("replace: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(raw) != "two" {
		t.Fatalf("expected replaced content, got %q", string(raw))
	}
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".config.json.tmp-*"))
	if err != nil {
		t.Fatalf("glob temp files: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("expected no temp files, got %#v", matches)
	}
}

func TestProfileLockIsExclusive(t *testing.T) {
	root := t.TempDir()
	first, err := AcquireLock(context.Background(), root, LockOptions{})
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}
	defer first.Release()

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if _, err := AcquireLock(ctx, root, LockOptions{RetryInterval: 5 * time.Millisecond}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline while lock is held, got %v", err)
	}
	if err := first.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	second, err := AcquireLock(context.Background(), root, LockOptions{})
	if err != nil {
		t.Fatalf("second lock after release: %v", err)
	}
	if err := second.Release(); err != nil {
		t.Fatalf("second release: %v", err)
	}
}

func TestManifestRoundTrip(t *testing.T) {
	root := t.TempDir()
	manifest, err := LoadManifest(root)
	if err != nil {
		t.Fatalf("load empty: %v", err)
	}
	manifest.Set(ManifestEntry{Kind: "agents", Key: "reviewer", Path: filepath.Join(root, "agents", "reviewer.md"), Fingerprint: "fp"})
	if err := SaveManifest(root, manifest); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := LoadManifest(root)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	entry, ok := loaded.Entry("agents", "reviewer")
	if !ok || entry.Fingerprint != "fp" {
		t.Fatalf("expected manifest entry, got %#v ok=%v", entry, ok)
	}
}
