package skillruntime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/profilestate"
	"github.com/agent-dance/agent-adaptor/profile"
)

func managedCloneFixture(t *testing.T) (string, string, string, driver.ResolvedSkills) {
	t.Helper()
	source, target, cache := t.TempDir(), t.TempDir(), t.TempDir()
	resolved := filepath.Join(cache, "materialized")
	if err := os.MkdirAll(resolved, 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(resolved, "SKILL.md"), "---\nname: fixture\n---\nfixture")
	writeFile(t, filepath.Join(resolved, "extra.txt"), "unknown regular attachment")
	payload := driver.ResolvedSkills{Entries: []driver.ResolvedSkill{{Key: "fixture", RuntimeName: "fixture", SourcePath: resolved}}}
	if _, err := ReconcileProfileSkills(context.Background(), ProfileSkillReconcileOptions{ProfileDir: source, SkillsHome: filepath.Join(source, "skills"), Payload: payload, ManagedRoots: []string{cache}, ConflictMode: ProfileSkillConflictError, PruneMode: ProfileSkillPruneNone}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(filepath.Join(source, "skills", "fixture"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("managed-link regression requires symlink creation; copied-tree protection is also tested independently")
	}
	return source, target, cache, payload
}

func TestManagedCloneCopiesAndRetainsActualResources(t *testing.T) {
	source, target, cache, payload := managedCloneFixture(t)
	original := payload.Entries[0].SourcePath
	if err := os.Chmod(filepath.Join(original, "extra.txt"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := copyProfileSkills(source, target, []string{"skills"}); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(target, "skills", "fixture")
	info, err := os.Lstat(dest)
	if err != nil || !info.IsDir() {
		t.Fatal("not an ordinary tree", err)
	}
	for _, name := range []string{"SKILL.md", "extra.txt"} {
		a, err := os.ReadFile(filepath.Join(original, name))
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(filepath.Join(dest, name))
		if err != nil || string(a) != string(b) {
			t.Fatal("copy lost bytes", err)
		}
		ai, _ := os.Stat(filepath.Join(original, name))
		bi, _ := os.Stat(filepath.Join(dest, name))
		if ai.Mode() != bi.Mode() {
			t.Fatal("mode changed")
		}
	}
	opts := ProfileSkillReconcileOptions{ProfileDir: target, SkillsHome: filepath.Join(target, "skills"), Payload: payload, ManagedRoots: []string{cache}, ConflictMode: ProfileSkillConflictError, PruneMode: ProfileSkillPruneNone}
	if _, err := ReconcileProfileSkills(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dest, "extra.txt"), "user change")
	writeFile(t, filepath.Join(dest, "user-added.txt"), "user attachment")
	if err := copyProfileSkills(source, target, []string{"skills"}); err != nil {
		t.Fatal(err)
	}
	assertFileContains(t, filepath.Join(dest, "extra.txt"), "user change")
	assertFileContains(t, filepath.Join(original, "extra.txt"), "unknown regular attachment")
	manifest, err := profilestate.LoadManifest(target)
	if err != nil {
		t.Fatal(err)
	}
	view, err := CompatibilityTargets(target, manifest, &payload, ProfileSkillPruneManaged)
	if err != nil || len(view.Targets) != 0 || len(view.Pruned) != 0 {
		t.Fatalf("copied contents were projected away: %#v %v", view, err)
	}
	if _, err := CompatibilityTargets(target, manifest, &driver.ResolvedSkills{}, ProfileSkillPruneManaged); !errors.Is(err, profile.ErrUnsafe) {
		t.Fatalf("copied prune must refuse unproved contents: %v", err)
	}
}

func TestManagedCloneRejectsUnsafeInputs(t *testing.T) {
	cases := []string{"no_manifest", "bad_hash", "bad_fingerprint", "outside_manifest", "duplicate_path", "duplicate_alias", "broken", "replaced", "nested_link", "marker_collision", "destination_link", "destination_conflict", "skills_link", "manifest_link", "source_root_link"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			source, target, _, payload := managedCloneFixture(t)
			resolved := payload.Entries[0].SourcePath
			path := filepath.Join(source, "skills", "fixture")
			manifest, err := profilestate.LoadManifest(source)
			if err != nil {
				t.Fatal(err)
			}
			entry := manifest.KindEntries(profileSkillManifestKind)[0]
			symlink := func(from, to string) {
				t.Helper()
				if err := os.Symlink(from, to); err != nil {
					t.Fatal(err)
				}
			}
			switch name {
			case "no_manifest":
				if err := os.Remove(filepath.Join(source, profilestate.ManifestName)); err != nil {
					t.Fatal(err)
				}
			case "bad_hash":
				entry.Metadata[manifestSourceHashKey] = "changed"
				manifest.Set(entry)
				err = profilestate.SaveManifest(source, manifest)
			case "bad_fingerprint":
				entry.Fingerprint = "changed"
				manifest.Set(entry)
				err = profilestate.SaveManifest(source, manifest)
			case "outside_manifest":
				entry.Path = filepath.Join(source, "outside")
				manifest.Set(entry)
				err = profilestate.SaveManifest(source, manifest)
			case "duplicate_alias":
				entry.Key = "second"
				entry.Path = filepath.Dir(entry.Path) + string(filepath.Separator) + "." + string(filepath.Separator) + filepath.Base(entry.Path)
				manifest.Entries[profilestate.EntryID(entry.Kind, entry.Key)] = entry
				err = profilestate.SaveManifest(source, manifest)
			case "duplicate_path":
				entry.Key = "second"
				manifest.Set(entry)
				err = profilestate.SaveManifest(source, manifest)
			case "broken":
				err = os.RemoveAll(resolved)
			case "replaced":
				err = os.Remove(path)
				symlink(t.TempDir(), path)
			case "nested_link":
				symlink(filepath.Join(resolved, "SKILL.md"), filepath.Join(resolved, "nested"))
			case "marker_collision":
				writeFile(t, filepath.Join(resolved, sourceMarkerName), "user contents")
			case "destination_link":
				err = os.MkdirAll(filepath.Join(target, "skills"), 0755)
				symlink(t.TempDir(), filepath.Join(target, "skills", "fixture"))
			case "destination_conflict":
				writeFile(t, filepath.Join(target, "skills", "fixture", "keep"), "user data")
			case "skills_link":
				err = os.Rename(filepath.Join(source, "skills"), filepath.Join(source, "real-skills"))
				symlink(filepath.Join(source, "real-skills"), filepath.Join(source, "skills"))
			case "manifest_link":
				err = os.Rename(filepath.Join(source, profilestate.ManifestName), filepath.Join(source, "real-manifest"))
				symlink(filepath.Join(source, "real-manifest"), filepath.Join(source, profilestate.ManifestName))
			case "source_root_link":
				err = os.Rename(resolved, resolved+"-moved")
				symlink(resolved+"-moved", resolved)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := copyProfileSkills(source, target, []string{"skills"}); err == nil {
				t.Fatal("unsafe clone accepted")
			}
			if name == "destination_conflict" {
				assertFileContains(t, filepath.Join(target, "skills", "fixture", "keep"), "user data")
			}
		})
	}
}

// This is deliberately independent of symlink availability: copied fallback
// materializations also use the same source marker on every platform.
func TestManagedCloneChangedSourcePreservesCopiedTree(t *testing.T) {
	for _, path := range []string{"compatibility", "reconcile"} {
		t.Run(path, func(t *testing.T) {
			dir, cache := t.TempDir(), t.TempDir()
			oldSource, newSource := filepath.Join(cache, "old"), filepath.Join(cache, "new")
			for _, p := range []string{oldSource, newSource} {
				writeFile(t, filepath.Join(p, "SKILL.md"), "skill")
			}
			target := filepath.Join(dir, "skills", "fixture")
			writeFile(t, filepath.Join(target, sourceMarkerName), oldSource)
			writeFile(t, filepath.Join(target, "SKILL.md"), "user modified skill")
			writeFile(t, filepath.Join(target, "user.txt"), "do not remove")
			old := driver.ResolvedSkill{Key: "fixture", RuntimeName: "fixture", SourcePath: oldSource}
			manifest := profilestate.Manifest{}
			manifest.Set(profileSkillManifestEntry(old, target))
			if err := profilestate.SaveManifest(dir, manifest); err != nil {
				t.Fatal(err)
			}
			desired := driver.ResolvedSkills{Entries: []driver.ResolvedSkill{{Key: "fixture", RuntimeName: "fixture", SourcePath: newSource}}}
			var err error
			if path == "compatibility" {
				_, err = CompatibilityTargets(dir, manifest, &desired, ProfileSkillPruneManaged)
			} else {
				_, err = ReconcileProfileSkills(context.Background(), ProfileSkillReconcileOptions{ProfileDir: dir, SkillsHome: filepath.Join(dir, "skills"), Payload: desired, ManagedRoots: []string{cache}, ConflictMode: ProfileSkillConflictError, PruneMode: ProfileSkillPruneManaged})
			}
			if !errors.Is(err, profile.ErrUnsafe) {
				t.Errorf("changed source must reject before copied contents can be deleted: %v", err)
			}
			assertFileContains(t, filepath.Join(target, "user.txt"), "do not remove")
			assertFileContains(t, filepath.Join(target, "SKILL.md"), "user modified skill")
		})
	}
}

func TestManagedCloneRetainsDeletedAndChangedDestination(t *testing.T) {
	source, target, _, payload := managedCloneFixture(t)
	if err := copyProfileSkills(source, target, []string{"skills"}); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(target, "skills", "fixture")
	if err := os.Remove(filepath.Join(dest, "extra.txt")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(payload.Entries[0].SourcePath, "extra.txt"), "changed upstream")
	if err := copyProfileSkills(source, target, []string{"skills"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dest, "extra.txt")); !os.IsNotExist(err) {
		t.Fatalf("clone silently restored a deleted resource: %v", err)
	}
}

func TestManagedCloneBoundsAndSpecialNodes(t *testing.T) {
	cases := []string{"oversize", "aggregate_bytes", "aggregate_entries"}
	// A live AF_UNIX filesystem node is a POSIX fixture. Windows has different
	// socket sharing/rename rules; its regular/link/limit tests still run.
	if runtime.GOOS != "windows" {
		cases = append(cases, "socket")
	}
	for _, kind := range cases {
		t.Run(kind, func(t *testing.T) {
			source, target, _, payload := managedCloneFixture(t)
			path := filepath.Join(payload.Entries[0].SourcePath, "oversized")
			switch kind {
			case "oversize":
				f, err := os.Create(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := f.Truncate(cloneSkillByteLimit + 1); err != nil {
					t.Fatal(err)
				}
				f.Close()
			case "socket":
				// AF_UNIX limits the address, not the filesystem path. Keep the
				// bind name short even when the runner supplies a long TMPDIR.
				// This test and its ancestors are intentionally not parallel.
				t.Chdir(t.TempDir())
				socket := "s"
				listener, err := net.Listen("unix", socket)
				if err != nil {
					t.Fatal(err)
				}
				defer listener.Close()
				if err := os.Rename(socket, path); err != nil {
					t.Fatal(err)
				}
				if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeSocket == 0 {
					t.Fatalf("fixture did not create a real socket: %v", err)
				}
			default:
				root, err := os.OpenRoot(payload.Entries[0].SourcePath)
				if err != nil {
					t.Fatal(err)
				}
				defer root.Close()
				budget := &cloneSkillBudget{bytes: cloneSkillByteLimit - 1}
				if kind == "aggregate_entries" {
					budget = &cloneSkillBudget{entries: cloneSkillEntryLimit}
				}
				if _, err := readCloneSkillNode(root, "SKILL.md", nil, "", budget, false); !errors.Is(err, profile.ErrUnsafe) {
					t.Fatalf("aggregate limit ignored: %v", err)
				}
				return
			}
			if err := copyProfileSkills(source, target, []string{"skills"}); err == nil {
				t.Fatal("unsafe source accepted")
			}
			if _, err := os.Lstat(filepath.Join(target, "skills", "fixture", sourceMarkerName)); !os.IsNotExist(err) {
				t.Fatalf("failed copy published source marker: %v", err)
			}
		})
	}
}

func TestManagedCloneRejectsReplacementAndPartialRetry(t *testing.T) {
	source, target, _, _ := managedCloneFixture(t)
	root, err := os.OpenRoot(filepath.Join(source, "skills"))
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	info, err := root.Lstat("fixture")
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Remove("fixture"); err != nil {
		t.Fatal(err)
	}
	if err := root.Mkdir("fixture", 0700); err != nil {
		t.Fatal(err)
	}
	if err := cloneSkillUnchanged(root, "fixture", info); !errors.Is(err, profile.ErrUnsafe) {
		t.Fatalf("replacement accepted: %v", err)
	}
	// A partially written tree has no completed marker and cannot be adopted on
	// retry. Existing user bytes stay in place; no RemoveAll or overwrite occurs.
	source2, target2, _, _ := managedCloneFixture(t)
	writeFile(t, filepath.Join(target2, "skills", "fixture", "partial.txt"), "keep partial")
	for i := 0; i < 2; i++ {
		if err := copyProfileSkills(source2, target2, []string{"skills"}); err == nil {
			t.Fatal("partial tree was accepted")
		}
	}
	assertFileContains(t, filepath.Join(target2, "skills", "fixture", "partial.txt"), "keep partial")
	if _, err := os.Lstat(filepath.Join(target, "skills")); !os.IsNotExist(err) {
		t.Fatal("proof test unexpectedly wrote target")
	}
}

func TestManagedCloneSymlinkSourceReplacementStillAllowed(t *testing.T) {
	source, _, cache, payload := managedCloneFixture(t)
	replacement := filepath.Join(cache, "replacement")
	writeFile(t, filepath.Join(replacement, "SKILL.md"), "replacement")
	payload.Entries[0].SourcePath = replacement
	manifest, err := profilestate.LoadManifest(source)
	if err != nil {
		t.Fatal(err)
	}
	view, err := CompatibilityTargets(source, manifest, &payload, ProfileSkillPruneManaged)
	if err != nil || view.Targets["skills/fixture"] != replacement {
		t.Fatalf("proved link replacement regressed: %v", err)
	}
	if _, err := ReconcileProfileSkills(context.Background(), ProfileSkillReconcileOptions{ProfileDir: source, SkillsHome: filepath.Join(source, "skills"), Payload: payload, ManagedRoots: []string{cache}, ConflictMode: ProfileSkillConflictError, PruneMode: ProfileSkillPruneManaged}); err != nil {
		t.Fatal(err)
	}
	got, err := os.Readlink(filepath.Join(source, "skills", "fixture"))
	if err != nil || got != replacement {
		t.Fatalf("link not replaced: %s %v", got, err)
	}
}

func TestManagedCloneRetainedForestBudget(t *testing.T) {
	for _, kind := range []string{"bytes", "bytes_with_fresh", "entries"} {
		t.Run(kind, func(t *testing.T) {
			source, target, cache, payload := managedCloneFixture(t)
			add := func(name string) {
				t.Helper()
				dir := filepath.Join(cache, name)
				writeFile(t, filepath.Join(dir, "SKILL.md"), "tiny source")
				payload.Entries = append(payload.Entries, driver.ResolvedSkill{Key: name, RuntimeName: name, SourcePath: dir})
				if _, err := ReconcileProfileSkills(context.Background(), ProfileSkillReconcileOptions{ProfileDir: source, SkillsHome: filepath.Join(source, "skills"), Payload: payload, ManagedRoots: []string{cache}, ConflictMode: ProfileSkillConflictError, PruneMode: ProfileSkillPruneNone}); err != nil {
					t.Fatal(err)
				}
			}
			add("z-two")
			if err := copyProfileSkills(source, target, []string{"skills"}); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"fixture", "z-two"} {
				dir := filepath.Join(target, "skills", name)
				if kind == "entries" {
					for i := 0; i < cloneSkillEntryLimit/2; i++ {
						f, err := os.OpenFile(filepath.Join(dir, fmt.Sprintf("retained-%05d", i)), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
						if err != nil {
							t.Fatal(err)
						}
						if err := f.Close(); err != nil {
							t.Fatal(err)
						}
					}
				} else {
					f, err := os.Create(filepath.Join(dir, "retained.bin"))
					if err != nil {
						t.Fatal(err)
					}
					if err := f.Truncate(33 << 20); err != nil {
						f.Close()
						t.Fatal(err)
					}
					if err := f.Close(); err != nil {
						t.Fatal(err)
					}
				}
			}
			// Sorts between the two retained trees; copying it must not reset the
			// single retained-forest budget or hide either existing tree.
			if kind == "bytes_with_fresh" {
				add("m-new")
			}
			if err := copyProfileSkills(source, target, []string{"skills"}); !errors.Is(err, profile.ErrUnsafe) {
				t.Fatalf("retained forest exceeded shared %s budget: %v", kind, err)
			}
			for _, name := range []string{"fixture", "z-two"} {
				if kind == "entries" {
					entries, err := os.ReadDir(filepath.Join(target, "skills", name))
					if err != nil || len(entries) < cloneSkillEntryLimit/2 {
						t.Fatalf("rejection removed retained entries: %d %v", len(entries), err)
					}
				} else {
					info, err := os.Stat(filepath.Join(target, "skills", name, "retained.bin"))
					if err != nil || info.Size() != 33<<20 {
						t.Fatalf("rejection changed retained bytes: %v", err)
					}
				}
			}
		})
	}
}

func aliasedManagedCloneFixture(t *testing.T) (source, alias, target, link string, payload driver.ResolvedSkills) {
	t.Helper()
	source, target, cache := t.TempDir(), t.TempDir(), t.TempDir()
	resolved := filepath.Join(cache, "materialized")
	writeFile(t, filepath.Join(resolved, "SKILL.md"), "---\nname: fixture\n---\nfixture")
	writeFile(t, filepath.Join(resolved, "extra.txt"), "unknown regular attachment")
	payload = driver.ResolvedSkills{Entries: []driver.ResolvedSkill{{Key: "fixture", RuntimeName: "fixture", SourcePath: resolved}}}
	link = filepath.Join(t.TempDir(), "ancestor-alias")
	if err := os.Symlink(filepath.Dir(source), link); err != nil {
		t.Fatal(err)
	}
	alias = filepath.Join(link, filepath.Base(source))
	// The formal reconciler, not this test, records the original alias spelling.
	if _, err := ReconcileProfileSkills(context.Background(), ProfileSkillReconcileOptions{ProfileDir: alias, SkillsHome: filepath.Join(alias, "skills"), Payload: payload, ManagedRoots: []string{cache}, ConflictMode: ProfileSkillConflictError, PruneMode: ProfileSkillPruneNone}); err != nil {
		t.Fatal(err)
	}
	return source, alias, target, link, payload
}

func TestManagedCloneAncestorAlias(t *testing.T) {
	source, alias, target, _, _ := aliasedManagedCloneFixture(t)
	before, err := os.ReadFile(filepath.Join(source, profilestate.ManifestName))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := profilestate.LoadManifest(source)
	if err != nil {
		t.Fatal(err)
	}
	entry := manifest.KindEntries(profileSkillManifestKind)[0]
	rel, err := filepath.Rel(filepath.Join(source, "skills"), entry.Path)
	if err != nil || filepath.IsLocal(rel) {
		t.Fatal("fixture must have nonlocal lexical alias", rel, err)
	}
	for _, suffix := range []string{"", "skills"} {
		a, err := os.Stat(filepath.Join(source, suffix))
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.Stat(filepath.Join(alias, suffix))
		if err != nil || !os.SameFile(a, b) {
			t.Fatal("alias changed physical identity", err)
		}
	}
	if err := copyProfileSkills(source, target, []string{"skills"}); err != nil {
		t.Fatal(err)
	}
	assertFileContains(t, filepath.Join(target, "skills", "fixture", "extra.txt"), "unknown regular attachment")
	info, err := os.Lstat(filepath.Join(target, "skills", "fixture"))
	if err != nil || !info.IsDir() {
		t.Fatal("alias clone did not produce ordinary tree", err)
	}
	after, err := os.ReadFile(filepath.Join(source, profilestate.ManifestName))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("source manifest mutated", err)
	}
	if manifest.KindEntries(profileSkillManifestKind)[0].Path != entry.Path {
		t.Fatal("caller manifest mutated")
	}
}

func TestManagedCloneAliasProofRejectsForeignAndAmbiguousParents(t *testing.T) {
	for _, kind := range []string{"wrong_profile_same_target", "skills_only_identity", "duplicate_alias", "wrong_parent", "nested", "relative", "hash", "relative_link_hash"} {
		t.Run(kind, func(t *testing.T) {
			source, alias, target, _, payload := aliasedManagedCloneFixture(t)
			manifest, err := profilestate.LoadManifest(source)
			if err != nil {
				t.Fatal(err)
			}
			entry := manifest.KindEntries(profileSkillManifestKind)[0]
			switch kind {
			case "wrong_profile_same_target", "skills_only_identity":
				foreign := t.TempDir()
				if kind == "skills_only_identity" {
					if err := os.Symlink(filepath.Join(source, "skills"), filepath.Join(foreign, "skills")); err != nil {
						t.Fatal(err)
					}
					a, _ := os.Stat(filepath.Join(foreign, "skills"))
					b, _ := os.Stat(filepath.Join(source, "skills"))
					if !os.SameFile(a, b) {
						t.Fatal("fixture did not share skills target")
					}
				} else {
					if err := os.Mkdir(filepath.Join(foreign, "skills"), 0755); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(payload.Entries[0].SourcePath, filepath.Join(foreign, "skills", "fixture")); err != nil {
						t.Fatal(err)
					}
					a, _ := os.Stat(filepath.Join(foreign, "skills", "fixture"))
					b, _ := os.Stat(filepath.Join(source, "skills", "fixture"))
					if !os.SameFile(a, b) {
						t.Fatal("fixture leaf targets differ")
					}
				}
				entry.Path = filepath.Join(foreign, "skills", "fixture")
				manifest.Set(entry)
			case "duplicate_alias":
				second := payload.Entries[0]
				second.Key = "second-owner"
				manifest.Set(profileSkillManifestEntry(second, filepath.Join(source, "skills", "fixture")))
			case "wrong_parent":
				entry.Path = filepath.Join(alias, "other", "fixture")
				manifest.Set(entry)
			case "nested":
				entry.Path = filepath.Join(alias, "skills", "nested", "fixture")
				manifest.Set(entry)
			case "relative":
				entry.Path = filepath.Join("skills", "fixture")
				manifest.Set(entry)
			case "hash":
				entry.Metadata[manifestSourceHashKey] = "unproved"
				manifest.Set(entry)
			case "relative_link_hash":
				path := filepath.Join(alias, "skills", "fixture")
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				writeFile(t, filepath.Join(source, "cache", "SKILL.md"), "relative source")
				if err := os.Symlink(filepath.Join("..", "cache"), path); err != nil {
					t.Fatal(err)
				}
				proof := payload.Entries[0]
				proof.SourcePath = filepath.Join(alias, "cache")
				manifest.Set(profileSkillManifestEntry(proof, path))
			}
			if err := profilestate.SaveManifest(source, manifest); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(filepath.Join(source, profilestate.ManifestName))
			if err != nil {
				t.Fatal(err)
			}
			if err := copyProfileSkills(source, target, []string{"skills"}); !errors.Is(err, profile.ErrUnsafe) {
				t.Fatalf("unsafe alias accepted or lost safety error: %v", err)
			}
			after, err := os.ReadFile(filepath.Join(source, profilestate.ManifestName))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("rejection changed manifest", err)
			}
			if _, err := os.Lstat(filepath.Join(target, "skills", "fixture")); !os.IsNotExist(err) {
				t.Fatal("rejection published a copied tree", err)
			}
		})
	}
}

func TestManagedCloneAliasProofPinsSubsequentReads(t *testing.T) {
	for _, replace := range []string{"ancestor_alias", "skills_child"} {
		t.Run(replace, func(t *testing.T) {
			source, _, _, link, payload := aliasedManagedCloneFixture(t)
			src, err := os.OpenRoot(source)
			if err != nil {
				t.Fatal(err)
			}
			defer src.Close()
			skills, err := cloneSkillRoot(src, "skills")
			if err != nil {
				t.Fatal(err)
			}
			defer skills.Close()
			manifest, err := profilestate.LoadManifest(source)
			if err != nil {
				t.Fatal(err)
			}
			originalPath := manifest.KindEntries(profileSkillManifestKind)[0].Path
			local, err := cloneSkillManifestView(src, skills, manifest)
			if err != nil {
				t.Fatal(err)
			}
			if manifest.KindEntries(profileSkillManifestKind)[0].Path != originalPath {
				t.Fatal("private view modified caller map")
			}
			view, err := CompatibilityTargets(source, local, nil, ProfileSkillPruneNone)
			if err != nil {
				t.Fatal(err)
			}
			if replace == "ancestor_alias" {
				foreignParent := t.TempDir()
				foreign := filepath.Join(foreignParent, filepath.Base(source))
				writeFile(t, filepath.Join(foreign, "skills", "fixture", "sentinel"), "foreign untouched")
				if err := os.Remove(link); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(foreignParent, link); err != nil {
					t.Fatal(err)
				}
				tree, err := readCloneSkillSnapshot(src, skills, view.Targets)
				if err != nil {
					t.Fatal(err)
				}
				if len(tree.children) != 1 || tree.children[0].source != payload.Entries[0].SourcePath {
					t.Fatal("snapshot left proved held tree")
				}
				assertFileContains(t, filepath.Join(foreign, "skills", "fixture", "sentinel"), "foreign untouched")
				if _, err := cloneSkillManifestView(src, skills, manifest); !errors.Is(err, profile.ErrUnsafe) {
					t.Fatalf("retargeted alias accepted as original parent: %v", err)
				}
			} else {
				if err := os.Rename(filepath.Join(source, "skills"), filepath.Join(source, "previous-skills")); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(filepath.Join(source, "skills"), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(payload.Entries[0].SourcePath, filepath.Join(source, "skills", "fixture")); err != nil {
					t.Fatal(err)
				}
				if _, err := readCloneSkillSnapshot(src, skills, view.Targets); !errors.Is(err, profile.ErrUnsafe) {
					t.Fatalf("same-target replacement lost pinned skills identity: %v", err)
				}
			}
		})
	}
}
