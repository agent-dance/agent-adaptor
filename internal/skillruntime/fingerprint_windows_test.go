package skillruntime

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestProfileSkillsFingerprintOrphanedJunction(t *testing.T) {
	profile := t.TempDir()
	skills := filepath.Join(profile, "skills")
	target := filepath.Join(t.TempDir(), "target")
	for _, path := range []string{skills, target} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(skills, "orphan")
	if raw, err := exec.Command("cmd.exe", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
		t.Fatalf("create junction: %v: %s", err, raw)
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	before, err := ProfileSkillsFingerprint(context.Background(), profile)
	if err != nil {
		t.Fatalf("orphaned junction must be represented as missing: %v", err)
	}
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "SKILL.md"), []byte("restored"), 0600); err != nil {
		t.Fatal(err)
	}
	after, err := ProfileSkillsFingerprint(context.Background(), profile)
	if err != nil || before == after {
		t.Fatalf("restored junction not observed: %v", err)
	}
}
