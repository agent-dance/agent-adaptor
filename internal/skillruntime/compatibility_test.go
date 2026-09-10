package skillruntime

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/profilestate"
	"github.com/agent-dance/agent-adaptor/profile"
)

func TestCompatibilityPruneMatchesReconciler(t *testing.T) {
	for _, tc := range []struct {
		name                                 string
		mode                                 ProfileSkillPruneMode
		external, broken, rename, wantPruned bool
	}{
		{name: "managed-link", mode: ProfileSkillPruneManaged, wantPruned: true},
		{name: "user-tree", mode: ProfileSkillPruneManaged, external: true},
		{name: "healthy-retained", mode: ProfileSkillPruneBrokenManaged},
		{name: "broken-pruned", mode: ProfileSkillPruneBrokenManaged, broken: true, wantPruned: true},
		{name: "no-prune", mode: ProfileSkillPruneNone},
		{name: "renamed", mode: ProfileSkillPruneNone, rename: true, wantPruned: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			home := filepath.Join(dir, "skills")
			source := createProfileSkillDir(t, t.TempDir(), "alpha", "A")
			old := driver.ResolvedSkill{Key: "alpha", RuntimeName: "alpha", SourcePath: source}
			opts := ProfileSkillReconcileOptions{ProfileDir: dir, SkillsHome: home, Payload: driver.ResolvedSkills{Entries: []driver.ResolvedSkill{old}}, ConflictMode: ProfileSkillConflictError, PruneMode: ProfileSkillPruneManaged}
			if _, err := ReconcileProfileSkills(context.Background(), opts); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(home, "alpha")
			if tc.external {
				if err := os.Remove(target); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(target, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(target, "SKILL.md"), []byte("actual content"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.broken {
				if err := os.RemoveAll(source); err != nil {
					t.Fatal(err)
				}
			}
			manifest, err := profilestate.LoadManifest(dir)
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(filepath.Join(dir, profilestate.ManifestName))
			if err != nil {
				t.Fatal(err)
			}
			opts.Payload = driver.ResolvedSkills{}
			if tc.rename {
				next := old
				next.RuntimeName = "renamed"
				opts.Payload.Entries = []driver.ResolvedSkill{next}
			}
			opts.PruneMode = tc.mode
			view, err := CompatibilityTargets(dir, manifest, &opts.Payload, tc.mode)
			if err != nil {
				t.Fatal(err)
			}
			if view.Pruned["skills/alpha"] != tc.wantPruned {
				t.Fatalf("prune projection: %#v", view)
			}
			if _, err := os.Lstat(target); err != nil {
				t.Fatal("snapshot changed target", err)
			}
			after, err := os.ReadFile(filepath.Join(dir, profilestate.ManifestName))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("snapshot changed manifest", err)
			}
			if _, err := ReconcileProfileSkills(context.Background(), opts); err != nil {
				t.Fatal(err)
			}
			_, err = os.Lstat(target)
			if tc.wantPruned != os.IsNotExist(err) {
				t.Fatalf("projection differs from actual reconciliation: %v", err)
			}
			if !tc.wantPruned && err != nil {
				t.Fatal(err)
			}
			if tc.external {
				raw, err := os.ReadFile(filepath.Join(target, "SKILL.md"))
				if err != nil || string(raw) != "actual content" {
					t.Fatal("user tree changed", err)
				}
			}
		})
	}
}

func TestCompatibilityPruneRequiresOwnership(t *testing.T) {
	for _, tamper := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing-proof", true: "tampered-link"}[tamper], func(t *testing.T) {
			dir := t.TempDir()
			home := filepath.Join(dir, "skills")
			source := createProfileSkillDir(t, t.TempDir(), "alpha", "A")
			opts := ProfileSkillReconcileOptions{ProfileDir: dir, SkillsHome: home, Payload: driver.ResolvedSkills{Entries: []driver.ResolvedSkill{{Key: "alpha", RuntimeName: "alpha", SourcePath: source}}}, ConflictMode: ProfileSkillConflictError, PruneMode: ProfileSkillPruneManaged}
			if _, err := ReconcileProfileSkills(context.Background(), opts); err != nil {
				t.Fatal(err)
			}
			manifest, err := profilestate.LoadManifest(dir)
			if err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(home, "alpha")
			if tamper {
				if err := os.Remove(target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), target); err != nil {
					t.Fatal(err)
				}
			} else {
				entry, _ := manifest.Entry(profileSkillManifestKind, "alpha")
				delete(entry.Metadata, manifestSourceHashKey)
				manifest.Set(entry)
			}
			if _, err := CompatibilityTargets(dir, manifest, &driver.ResolvedSkills{}, ProfileSkillPruneManaged); !errors.Is(err, profile.ErrUnsafe) {
				t.Fatal("unproved removal must fail", err)
			}
			if _, err := os.Lstat(target); err != nil {
				t.Fatal("rejected snapshot changed target", err)
			}
		})
	}
}

func TestCompatibilityPruneRejectsUnprovedCopiedContent(t *testing.T) {
	for _, drift := range []string{"content", "mode"} {
		t.Run(drift, func(t *testing.T) {
			dir := t.TempDir()
			home := filepath.Join(dir, "skills")
			source := createProfileSkillDir(t, t.TempDir(), "alpha", "A")
			opts := ProfileSkillReconcileOptions{ProfileDir: dir, SkillsHome: home, Payload: driver.ResolvedSkills{Entries: []driver.ResolvedSkill{{Key: "alpha", RuntimeName: "alpha", SourcePath: source}}}, ConflictMode: ProfileSkillConflictError, PruneMode: ProfileSkillPruneManaged}
			if _, err := ReconcileProfileSkills(context.Background(), opts); err != nil {
				t.Fatal(err)
			}
			manifest, err := profilestate.LoadManifest(dir)
			if err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(home, "alpha")
			if err := os.Remove(target); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(target, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(target, sourceMarkerName), []byte(source), 0600); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(filepath.Join(source, "SKILL.md"))
			if err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(target, "SKILL.md")
			if err := os.WriteFile(file, raw, 0644); err != nil {
				t.Fatal(err)
			}
			if drift == "content" {
				if err := os.WriteFile(filepath.Join(target, "user-note"), []byte("preserve"), 0600); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Chmod(file, 0700); err != nil {
				t.Fatal(err)
			}
			if _, err := CompatibilityTargets(dir, manifest, &driver.ResolvedSkills{}, ProfileSkillPruneManaged); !errors.Is(err, profile.ErrUnsafe) {
				t.Fatal("copied content drift must not be erased", err)
			}
			if _, err := os.Lstat(file); err != nil {
				t.Fatal("snapshot removed copied content", err)
			}
		})
	}
}

func TestCompatibilityRejectsManagedPathIOFailure(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "skills")
	source := createProfileSkillDir(t, t.TempDir(), "alpha", "A")
	opts := ProfileSkillReconcileOptions{ProfileDir: dir, SkillsHome: home, Payload: driver.ResolvedSkills{Entries: []driver.ResolvedSkill{{Key: "alpha", RuntimeName: "alpha", SourcePath: source}}}, ConflictMode: ProfileSkillConflictError, PruneMode: ProfileSkillPruneManaged}
	if _, err := ReconcileProfileSkills(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	manifest, err := profilestate.LoadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(home); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(home, []byte("invalid parent"), 0600); err != nil {
		t.Fatal(err)
	}
	_, probeErr := os.Lstat(filepath.Join(home, "alpha"))
	if probeErr == nil {
		t.Fatal("child of a regular file unexpectedly exists")
	}
	// Windows reports ERROR_PATH_NOT_FOUND for a non-directory parent, so
	// child inspection alone cannot distinguish this corruption from absence.
	_, err = CompatibilityTargets(dir, manifest, &driver.ResolvedSkills{}, ProfileSkillPruneManaged)
	if !errors.Is(err, profile.ErrUnsafe) {
		t.Fatal("invalid skills home was erased by prune", err)
	}
	if raw, err := os.ReadFile(home); err != nil || string(raw) != "invalid parent" {
		t.Fatal("compatibility inspection changed the invalid parent", err)
	}
}

func TestCompatibilityDistinguishesMissingAndInvalidSkillsHome(t *testing.T) {
	dir := t.TempDir()
	manifest := profilestate.Manifest{}
	if _, err := CompatibilityTargets(dir, manifest, nil, ProfileSkillPruneManaged); err != nil {
		t.Fatal("absent skills home must remain valid", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "skills"), []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := CompatibilityTargets(dir, manifest, nil, ProfileSkillPruneManaged); !errors.Is(err, profile.ErrUnsafe) {
		t.Fatal("invalid skills home must fail even before a managed entry is inspected", err)
	}
}
