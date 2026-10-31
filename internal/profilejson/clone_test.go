package profilejson

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCloneCanceledDoesNotInitialize(t *testing.T) {
	target := filepath.Join(t.TempDir(), "absent")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Clone(ctx, t.TempDir(), target, ClonePlan{Files: []string{"config.json"}, KeepField: func(string) bool { return true }}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("canceled clone initialized destination")
	}
}

func TestPublishNewFileNeverReplacesExisting(t *testing.T) {
	target := filepath.Join(t.TempDir(), "existing")
	if err := os.WriteFile(target, []byte("external"), 0600); err != nil {
		t.Fatal(err)
	}
	if created, err := publishNewFile(target, []byte("copy")); created || err == nil {
		t.Fatalf("nonexclusive publish: %v %v", created, err)
	}
	raw, err := os.ReadFile(target)
	if err != nil || string(raw) != "external" {
		t.Fatal("publish changed existing file")
	}
	entries, err := os.ReadDir(filepath.Dir(target))
	if err != nil || len(entries) != 1 {
		t.Fatal("publish left a staging file")
	}
}

func TestCloneRejectsIndirectAndInvalidFilesBeforeWriting(t *testing.T) {
	for _, invalid := range []string{`{"duplicate":1,"duplicate":2}`, `{"bad":`, `[]`} {
		source, target := t.TempDir(), t.TempDir()
		if err := os.WriteFile(filepath.Join(source, "first.json"), []byte(`{"value":"ok"}`), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(source, "second.json"), []byte(invalid), 0600); err != nil {
			t.Fatal(err)
		}
		if err := Clone(context.Background(), source, target, ClonePlan{Files: []string{"first.json", "second.json"}, KeepField: func(string) bool { return true }}); err == nil {
			t.Fatal("invalid native source accepted")
		}
		if _, err := os.Stat(filepath.Join(target, "first.json")); !os.IsNotExist(err) {
			t.Fatal("clone partially published before validation")
		}
	}
	source, target := t.TempDir(), t.TempDir()
	real := filepath.Join(t.TempDir(), "real.json")
	if err := os.WriteFile(real, []byte(`{"value":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(source, "config.json")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := Clone(context.Background(), source, target, ClonePlan{Files: []string{"config.json"}, KeepField: func(string) bool { return true }}); err == nil {
		t.Fatal("clone followed indirect native file")
	}
}
