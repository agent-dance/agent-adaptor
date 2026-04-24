package agentadaptor_test

// Skill contract invariants — black-box tests that exercise the public API
// surface documented in docs/skill-api-design.md. They intentionally stay
// ignorant of internal merger / materializer implementation details so that
// future refactors can change the wiring as long as the contract holds.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

// notEnumerableProvider models an upstream SkillProvider that cannot
// enumerate its catalogue. It is used to verify the ErrSkillsNotEnumerable
// path in both the Run and Admin flows.
type notEnumerableProvider struct{}

func (notEnumerableProvider) List(_ context.Context, _ string) ([]agentadaptor.Skill, error) {
	return nil, agentadaptor.ErrSkillsNotEnumerable
}

// TestSkillsAdditiveMerging verifies that WithDefaultSkills, WithSkills and
// Required skills from the provider all contribute to the Selected set, and
// that binding-default inline skills stay visible to later SetSelectedSkills
// calls as bare keys.
func TestSkillsAdditiveMerging(t *testing.T) {
	driver := &fakeDriver{}
	base := agentadaptor.InlineSkill("team/base", "---\nname: base\n---\n")
	optional := agentadaptor.InlineSkill("team/optional", "---\nname: optional\n---\n")
	required := agentadaptor.Require(
		agentadaptor.InlineSkill("system/required", "---\nname: required\n---\n"),
		"Always on",
	)

	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(fakeBinding("default", driver,
			agentadaptor.WithDefaultSkills(base),
		)),
		agentadaptor.WithSkillSet(agentadaptor.SkillSet{
			"team/base":       base,
			"team/optional":   optional,
			"system/required": required,
		}),
	)

	// Run without per-run WithSkills: expect Required ∪ Default only.
	if _, err := sdk.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("run: %v", err)
	}
	keys := driver.lastSkills.Keys()
	if !equalUnordered(keys, []string{"team/base", "system/required"}) {
		t.Fatalf("unexpected default selection: %#v", keys)
	}

	// Per-run WithSkills must be additive to the default set, not a
	// replacement. Bare keys must resolve against WithSkillSet.
	if _, err := sdk.Run(context.Background(), "hello", agentadaptor.WithSkills(agentadaptor.Key("team/optional"))); err != nil {
		t.Fatalf("run with skills: %v", err)
	}
	keys = driver.lastSkills.Keys()
	if !equalUnordered(keys, []string{"team/base", "system/required", "team/optional"}) {
		t.Fatalf("expected additive merge, got %#v", keys)
	}
}

// TestSkillsRequiredAlwaysSelected verifies that Required skills from the
// provider land in the Selected set even when the caller never mentions them
// and never passes WithDefaultSkills.
func TestSkillsRequiredAlwaysSelected(t *testing.T) {
	driver := &fakeDriver{}
	required := agentadaptor.Require(
		agentadaptor.InlineSkill("system/required", "---\nname: required\n---\n"),
		"Mandatory",
	)
	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(fakeBinding("default", driver)),
		agentadaptor.WithSkillSet(agentadaptor.SkillSet{"system/required": required}),
	)
	if _, err := sdk.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("run: %v", err)
	}
	keys := driver.lastSkills.Keys()
	if len(keys) != 1 || keys[0] != "system/required" {
		t.Fatalf("expected required skill to be auto-selected, got %#v", keys)
	}
}

// TestSkillsSameKeySameValueMerges verifies that two Skill values sharing
// the same key are treated as one skill when they are structurally equal.
func TestSkillsSameKeySameValueMerges(t *testing.T) {
	driver := &fakeDriver{}
	skill := agentadaptor.InlineSkill("team/shared", "---\nname: shared\n---\n")

	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(fakeBinding("default", driver,
			agentadaptor.WithDefaultSkills(skill),
		)),
		agentadaptor.WithSkillSet(agentadaptor.SkillSet{"team/shared": skill}),
	)

	if _, err := sdk.Run(context.Background(), "hello", agentadaptor.WithSkills(skill)); err != nil {
		t.Fatalf("run: %v", err)
	}
	keys := driver.lastSkills.Keys()
	if len(keys) != 1 || keys[0] != "team/shared" {
		t.Fatalf("expected a single merged entry for same-key / same-value skill, got %#v", keys)
	}
}

// TestSkillsSameKeyDifferentValueConflicts verifies that the "same key,
// same value" invariant is enforced: two different Skill values under the
// same key must surface ErrSkillKeyConflict without ever being merged.
func TestSkillsSameKeyDifferentValueConflicts(t *testing.T) {
	driver := &fakeDriver{}
	providerSkill := agentadaptor.InlineSkill("team/shared", "---\nprovider\n---\n")
	bindingSkill := agentadaptor.InlineSkill("team/shared", "---\nbinding\n---\n")

	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(fakeBinding("default", driver,
			agentadaptor.WithDefaultSkills(bindingSkill),
		)),
		agentadaptor.WithSkillSet(agentadaptor.SkillSet{"team/shared": providerSkill}),
	)

	_, err := sdk.Run(context.Background(), "hello")
	if err == nil {
		t.Fatalf("expected ErrSkillKeyConflict for same-key / different-value, got nil")
	}
	if !errors.Is(err, agentadaptor.ErrSkillKeyConflict) {
		t.Fatalf("expected ErrSkillKeyConflict, got %v", err)
	}
	var conflict *agentadaptor.SkillKeyConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("expected *SkillKeyConflictError, got %T", err)
	}
	if conflict.Key != "team/shared" {
		t.Fatalf("conflict key mismatch, want team/shared got %q", conflict.Key)
	}
}

// TestSkillsBareKeyWithoutProviderReturnsNotFound verifies that a bare
// SkillKey that can't be resolved anywhere surfaces ErrSkillNotFound with a
// message that mentions the offending key.
func TestSkillsBareKeyWithoutProviderReturnsNotFound(t *testing.T) {
	driver := &fakeDriver{}
	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(fakeBinding("default", driver,
			agentadaptor.WithDefaultSkills(agentadaptor.Key("team/missing")),
		)),
	)
	_, err := sdk.Run(context.Background(), "hello")
	if !errors.Is(err, agentadaptor.ErrSkillNotFound) {
		t.Fatalf("expected ErrSkillNotFound, got %v", err)
	}
	if !strings.Contains(err.Error(), "team/missing") {
		t.Fatalf("expected error to mention the missing key, got %v", err)
	}
}

// TestSkillsBareKeyWithNonEnumerableProviderReturnsNotEnumerable verifies
// that a bare SkillKey combined with a provider that refuses to enumerate
// surfaces ErrSkillsNotEnumerable (so hosts can tell the two failure modes
// apart when rendering the error).
func TestSkillsBareKeyWithNonEnumerableProviderReturnsNotEnumerable(t *testing.T) {
	driver := &fakeDriver{}
	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(fakeBinding("default", driver,
			agentadaptor.WithDefaultSkills(agentadaptor.Key("team/opaque")),
		)),
		agentadaptor.WithSkillProvider(notEnumerableProvider{}),
	)
	_, err := sdk.Run(context.Background(), "hello")
	if !errors.Is(err, agentadaptor.ErrSkillsNotEnumerable) {
		t.Fatalf("expected ErrSkillsNotEnumerable, got %v", err)
	}
}

// TestSkillsMissingSourceRejected verifies that a Skill value with a nil
// Source is rejected at resolve time with ErrSkillSourceMissing rather than
// being silently skipped.
func TestSkillsMissingSourceRejected(t *testing.T) {
	driver := &fakeDriver{}
	broken := agentadaptor.Skill{Key: "team/broken"} // Source == nil
	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(fakeBinding("default", driver,
			agentadaptor.WithDefaultSkills(broken),
		)),
	)
	_, err := sdk.Run(context.Background(), "hello")
	if !errors.Is(err, agentadaptor.ErrSkillSourceMissing) {
		t.Fatalf("expected ErrSkillSourceMissing, got %v", err)
	}
}

// TestMaterializerFailureDegradesToWarning verifies that a materialization
// failure does NOT fail the Run: the offending key stays in Selected, the
// payload carries a warning, and the adapter receives the rest of the
// entries intact.
func TestMaterializerFailureDegradesToWarning(t *testing.T) {
	driver := &fakeDriver{}
	good := agentadaptor.InlineSkill("team/good", "---\nname: good\n---\n")
	bad := agentadaptor.Skill{
		Key:    "team/bad",
		Source: agentadaptor.SkillFromPath{Path: "/definitely/does/not/exist/ever"},
	}
	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(fakeBinding("default", driver,
			agentadaptor.WithDefaultSkills(good, bad),
		)),
	)

	if _, err := sdk.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("run should tolerate missing source and surface a warning, got %v", err)
	}

	entryKeys := make([]string, 0, len(driver.lastSkills.Entries))
	for _, entry := range driver.lastSkills.Entries {
		entryKeys = append(entryKeys, entry.Key)
	}
	if !equalUnordered(entryKeys, []string{"team/good"}) {
		t.Fatalf("expected only team/good in Entries, got %#v", entryKeys)
	}
	if len(driver.lastSkills.Warnings) == 0 {
		t.Fatalf("expected materialization warning, got none")
	}
	sawWarning := false
	for _, w := range driver.lastSkills.Warnings {
		if strings.Contains(w, "team/bad") && strings.Contains(w, "materialization") {
			sawWarning = true
			break
		}
	}
	if !sawWarning {
		t.Fatalf("expected warning to mention team/bad materialization, got %#v", driver.lastSkills.Warnings)
	}
	if len(driver.lastSkills.Fingerprint) == 0 {
		t.Fatalf("expected a non-empty fingerprint even when some entries failed")
	}
}

// TestMaterializerCacheRootHonoursEnv verifies that the default
// SkillMaterializer writes into AGENT_ADAPTOR_SKILL_CACHE_ROOT when the
// environment variable is set, so that adapters (which read the same env
// var through internal/skillruntime.ManagedSkillCacheRoot) can correctly
// classify the materialised path as managed.
func TestMaterializerCacheRootHonoursEnv(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv(agentadaptor.SkillCacheRootEnv, tmp)

	driver := &fakeDriver{}
	skill := agentadaptor.InlineSkill("team/env-cache", "---\nname: env\n---\n")
	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(fakeBinding("default", driver,
			agentadaptor.WithDefaultSkills(skill),
		)),
	)

	if _, err := sdk.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(driver.lastSkills.Entries) != 1 {
		t.Fatalf("expected one materialized skill, got %#v", driver.lastSkills.Entries)
	}
	sourcePath := driver.lastSkills.Entries[0].SourcePath
	tmpResolved, _ := filepath.EvalSymlinks(tmp)
	srcResolved, _ := filepath.EvalSymlinks(sourcePath)
	// EvalSymlinks may fail for non-existent parents; fall back to the
	// literal path when that happens.
	if tmpResolved == "" {
		tmpResolved = tmp
	}
	if srcResolved == "" {
		srcResolved = sourcePath
	}
	if !strings.HasPrefix(srcResolved, tmpResolved) {
		t.Fatalf("expected source path %q to be rooted under %q", srcResolved, tmpResolved)
	}
	if _, err := os.Stat(filepath.Join(sourcePath, "SKILL.md")); err != nil {
		t.Fatalf("expected SKILL.md in cached skill dir: %v", err)
	}
}

// TestMaterializerCachesIdenticalSkills verifies that materialising the
// same skill twice does not rewrite the directory — the second call
// returns the same path and does not touch the existing files.
func TestMaterializerCachesIdenticalSkills(t *testing.T) {
	t.Setenv(agentadaptor.SkillCacheRootEnv, t.TempDir())

	driver := &fakeDriver{}
	skill := agentadaptor.InlineSkill("team/cached", "---\nname: cached\n---\n")
	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(fakeBinding("default", driver,
			agentadaptor.WithDefaultSkills(skill),
		)),
	)
	if _, err := sdk.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if len(driver.lastSkills.Entries) != 1 {
		t.Fatalf("expected one entry, got %#v", driver.lastSkills.Entries)
	}
	firstPath := driver.lastSkills.Entries[0].SourcePath
	readyMarker := filepath.Join(firstPath, ".agent-adaptor-ready")
	info, err := os.Stat(readyMarker)
	if err != nil {
		t.Fatalf("expected ready marker, got %v", err)
	}
	firstMtime := info.ModTime()

	if _, err := sdk.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("second run: %v", err)
	}
	secondPath := driver.lastSkills.Entries[0].SourcePath
	if secondPath != firstPath {
		t.Fatalf("expected cache hit to return the same path, got %q then %q", firstPath, secondPath)
	}
	info, err = os.Stat(readyMarker)
	if err != nil {
		t.Fatalf("ready marker disappeared: %v", err)
	}
	if !info.ModTime().Equal(firstMtime) {
		t.Fatalf("expected cache hit to leave the marker untouched, first=%v second=%v", firstMtime, info.ModTime())
	}
}

// TestMaterializerConcurrentSafe exercises a mild race between two
// concurrent Run calls that select the same inline skill. The default
// materializer uses atomic staging-then-rename, so both calls should
// resolve to the same sourcePath without error.
func TestMaterializerConcurrentSafe(t *testing.T) {
	t.Setenv(agentadaptor.SkillCacheRootEnv, t.TempDir())

	driver := &fakeDriver{}
	skill := agentadaptor.InlineSkill("team/concurrent", "---\nname: concurrent\n---\n")
	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(fakeBinding("default", driver,
			agentadaptor.WithDefaultSkills(skill),
		)),
	)

	var wg sync.WaitGroup
	var errCount atomic.Int32
	const N = 8
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := sdk.Run(context.Background(), "hello"); err != nil {
				t.Logf("concurrent run error: %v", err)
				errCount.Add(1)
			}
		}()
	}
	wg.Wait()
	if errCount.Load() != 0 {
		t.Fatalf("expected all concurrent runs to succeed, errCount=%d", errCount.Load())
	}
}

// TestMaterializerFromFSReproducesTree verifies that SkillFromFS replicates
// the directory tree under the cache root: SKILL.md plus any references.
func TestMaterializerFromFSReproducesTree(t *testing.T) {
	t.Setenv(agentadaptor.SkillCacheRootEnv, t.TempDir())

	driver := &fakeDriver{}
	fsys := fstest.MapFS{
		"root/SKILL.md":            &fstest.MapFile{Data: []byte("---\nname: root\n---\n")},
		"root/references/rules.md": &fstest.MapFile{Data: []byte("rules body")},
	}
	skill := agentadaptor.Skill{
		Key:    "team/tree",
		Source: agentadaptor.SkillFromFS{FS: fsys, Root: "root"},
	}
	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(fakeBinding("default", driver,
			agentadaptor.WithDefaultSkills(skill),
		)),
	)
	if _, err := sdk.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(driver.lastSkills.Entries) != 1 {
		t.Fatalf("expected one entry, got %#v", driver.lastSkills.Entries)
	}
	source := driver.lastSkills.Entries[0].SourcePath
	if data, err := os.ReadFile(filepath.Join(source, "SKILL.md")); err != nil {
		t.Fatalf("read SKILL.md: %v", err)
	} else if !strings.Contains(string(data), "root") {
		t.Fatalf("SKILL.md content mismatch, got %q", string(data))
	}
	if data, err := os.ReadFile(filepath.Join(source, "references", "rules.md")); err != nil {
		t.Fatalf("read reference: %v", err)
	} else if string(data) != "rules body" {
		t.Fatalf("reference content mismatch, got %q", string(data))
	}
}

// TestFingerprintStableAcrossRuns verifies that two runs with identical
// skill selections produce identical fingerprints, so session resume guards
// do not spuriously trigger.
func TestFingerprintStableAcrossRuns(t *testing.T) {
	t.Setenv(agentadaptor.SkillCacheRootEnv, t.TempDir())

	driver := &fakeDriver{}
	a := agentadaptor.InlineSkill("team/a", "---\nname: a\n---\n")
	b := agentadaptor.InlineSkill("team/b", "---\nname: b\n---\n")
	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(fakeBinding("default", driver,
			agentadaptor.WithDefaultSkills(a, b),
		)),
	)

	if _, err := sdk.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("first run: %v", err)
	}
	first := driver.lastSkills.Fingerprint

	if _, err := sdk.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if driver.lastSkills.Fingerprint != first {
		t.Fatalf("expected identical fingerprint across runs with the same selection, got %q then %q", first, driver.lastSkills.Fingerprint)
	}
}

// TestFingerprintChangesWhenSelectionChanges verifies that adding a skill
// via WithSkills produces a different fingerprint than the default-only
// run. Without this, resume-guarded adapters cannot detect skill drift.
func TestFingerprintChangesWhenSelectionChanges(t *testing.T) {
	t.Setenv(agentadaptor.SkillCacheRootEnv, t.TempDir())

	driver := &fakeDriver{}
	base := agentadaptor.InlineSkill("team/base", "---\nname: base\n---\n")
	extra := agentadaptor.InlineSkill("team/extra", "---\nname: extra\n---\n")
	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(fakeBinding("default", driver,
			agentadaptor.WithDefaultSkills(base),
		)),
	)

	if _, err := sdk.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("first run: %v", err)
	}
	first := driver.lastSkills.Fingerprint

	if _, err := sdk.Run(context.Background(), "hello", agentadaptor.WithSkills(extra)); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if driver.lastSkills.Fingerprint == first {
		t.Fatalf("expected fingerprint to change when selection changes, got %q both times", first)
	}
}

// TestSnapshotResolvedIncludesUnselectedCandidates verifies that
// SkillSnapshot.Resolved mirrors the full merged catalogue — including
// skills that live in the provider but were not selected for this run.
// This is the contract the Admin UI relies on to render "available but
// off" entries.
func TestSnapshotResolvedIncludesUnselectedCandidates(t *testing.T) {
	driver := &fakeDriver{}
	selected := agentadaptor.InlineSkill("team/on", "---\nname: on\n---\n")
	unselected := agentadaptor.InlineSkill("team/off", "---\nname: off\n---\n")
	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(fakeBinding("default", driver,
			agentadaptor.WithDefaultSkills(selected),
		)),
		agentadaptor.WithSkillSet(agentadaptor.SkillSet{
			"team/on":  selected,
			"team/off": unselected,
		}),
	)

	snapshot, err := sdk.Admin().Default().ListSkills(context.Background())
	if err != nil {
		t.Fatalf("list skills: %v", err)
	}
	if len(snapshot.Selected) != 1 || snapshot.Selected[0] != "team/on" {
		t.Fatalf("expected only team/on to be selected, got %#v", snapshot.Selected)
	}
	resolvedKeys := make([]string, 0, len(snapshot.Resolved))
	for _, skill := range snapshot.Resolved {
		resolvedKeys = append(resolvedKeys, skill.Key)
	}
	if !equalUnordered(resolvedKeys, []string{"team/on", "team/off"}) {
		t.Fatalf("expected both skills in Resolved, got %#v", resolvedKeys)
	}
}

func equalUnordered(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	aa := append([]string(nil), a...)
	bb := append([]string(nil), b...)
	sort.Strings(aa)
	sort.Strings(bb)
	for i := range aa {
		if aa[i] != bb[i] {
			return false
		}
	}
	return true
}
