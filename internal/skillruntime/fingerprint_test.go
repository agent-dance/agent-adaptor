package skillruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestProfileSkillsFingerprintObservesLinkedContent(t *testing.T) {
	ctx := context.Background()
	profile := t.TempDir()
	dir := filepath.Join(profile, "skills")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	file := filepath.Join(source, "SKILL.md")
	if err := os.WriteFile(file, []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(source, filepath.Join(dir, "proof")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	before, err := ProfileSkillsFingerprint(ctx, profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("other"), 0600); err != nil {
		t.Fatal(err)
	}
	after, err := ProfileSkillsFingerprint(ctx, profile)
	if err != nil || before == after {
		t.Fatalf("linked edit not observed: %v", err)
	}
	if err := os.WriteFile(file, []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	restored, err := ProfileSkillsFingerprint(ctx, profile)
	if err != nil || before != restored {
		t.Fatalf("fingerprint depends on timestamps: %v", err)
	}
	if err := os.Symlink(dir, filepath.Join(source, "cycle")); err != nil {
		t.Fatal(err)
	}
	if _, err := ProfileSkillsFingerprint(ctx, profile); err == nil {
		t.Fatal("symlink cycle accepted")
	}
}

func TestProfileSkillsFingerprintMissingAndCancellation(t *testing.T) {
	profile := filepath.Join(t.TempDir(), "missing")
	if _, err := ProfileSkillsFingerprint(context.Background(), profile); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(profile); !os.IsNotExist(err) {
		t.Fatal("probe initialized missing profile")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ProfileSkillsFingerprint(ctx, profile); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}
