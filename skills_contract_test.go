package adaptor_test

// P3.7 migration of the root skill baselines onto the v1 surface. Mapping
// (root test → here; roots stay untouched):
//
//	skills_sdk_test.go
//	  TestSDKRunMaterializesFSSkillsAndCanonicalizesSelectedRefs
//	      → TestSkillsFSMaterializationCanonicalizesSelectedRefs
//	  TestAdminSetSelectedSkillsUpdatesProcessLocalSelection
//	      → TestSelectSkillsUpdatesProcessLocalSelection
//	skill_contract_test.go
//	  additive merging                → TestSkillsAdditiveMergingWithRequired
//	                                    (+ the merge table rows in merge_semantics_test.go)
//	  required always selected        → TestSkillsAdditiveMergingWithRequired
//	  same key same value merges      → TestSkillSameKeySameValueMerges
//	  same key different value        → TestSkillSameKeyDifferentValueConflicts
//	  bare key, no provider           → TestSkillBareKeyNotFound
//	  bare key, provider empty        → TestSkillBareKeyNotFound
//	  nil Source                      → TestSkillNilSourceFails
//	  materializer failure pre-launch → TestSkillMaterializationFailureIsPreLaunch
//	  cache root honours env          → TestSkillCacheRootHonoursEnv
//	  caches identical                → TestSkillCachesIdenticalAcrossRuns
//	  concurrent safe                 → TestSkillResolutionConcurrentSafe
//	  FS reproduces tree              → TestSkillsFSMaterializationCanonicalizesSelectedRefs
//	  fingerprint stable / changes    → TestSkillFingerprintStableAndSelectionSensitive
//	  snapshot includes candidates    → TestInspectSkillsSnapshotCandidates
//	  catalogue error propagates      → TestInspectSkillsPropagatesCatalogueError
//	  provider without catalog        → TestInspectSkillsSnapshotCandidates
//	  catalogue not auto-selected     → TestInspectSkillsSnapshotCandidates
//
// Legacy admin_profile_test.go / skill_dirscan_test.go rows have no next/
// equivalent surface yet — recorded as a deviation in the P3 report.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/agent-dance/agent-adaptor/driver"
	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/skill"
)

// ---- helpers ----

func equalUnordered(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	g := append([]string(nil), got...)
	w := append([]string(nil), want...)
	sort.Strings(g)
	sort.Strings(w)
	for i := range g {
		if g[i] != w[i] {
			return false
		}
	}
	return true
}

func sliceContains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func skillKeys(skills []driver.Skill) []string {
	keys := make([]string, 0, len(skills))
	for _, s := range skills {
		keys = append(keys, s.Key)
	}
	return keys
}

// setSkillCache pins the materialization cache inside the test sandbox and
// returns the root.
func setSkillCache(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv(skill.SkillCacheRootEnv, root)
	return root
}

// ---- provider fixtures (root skill_contract_test.go fixtures on the
// ctx-scoped provider signature) ----

// emptyResolveProvider resolves nothing: every key lookup comes back empty.
type emptyResolveProvider struct{}

func (emptyResolveProvider) GetSkills(context.Context, []string) (map[string]driver.Skill, error) {
	return map[string]driver.Skill{}, nil
}

// erroringCatalogProvider resolves keys from a fixed map but fails catalogue
// enumeration.
type erroringCatalogProvider struct {
	skills       map[string]driver.Skill
	catalogueErr error
}

func (p erroringCatalogProvider) GetSkills(_ context.Context, keys []string) (map[string]driver.Skill, error) {
	out := map[string]driver.Skill{}
	for _, key := range keys {
		if s, ok := p.skills[key]; ok {
			out[key] = s
		}
	}
	return out, nil
}

func (p erroringCatalogProvider) Catalogue(context.Context) ([]driver.Skill, error) {
	return nil, p.catalogueErr
}

// catalogueOnlyProvider answers key lookups strictly (requested keys only,
// no required injection) and enumerates its full catalogue.
type catalogueOnlyProvider struct {
	skills map[string]driver.Skill
}

func (p catalogueOnlyProvider) GetSkills(_ context.Context, keys []string) (map[string]driver.Skill, error) {
	out := map[string]driver.Skill{}
	for _, key := range keys {
		if s, ok := p.skills[key]; ok {
			out[key] = s
		}
	}
	return out, nil
}

func (p catalogueOnlyProvider) Catalogue(context.Context) ([]driver.Skill, error) {
	out := make([]driver.Skill, 0, len(p.skills))
	for _, s := range p.skills {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// ---- tests ----

// TestSkillsFSMaterializationCanonicalizesSelectedRefs: an fs.FS-backed
// skill materializes its full tree (SKILL.md + references) to the local
// cache, provider-required skills join the selection, and the driver sees
// the canonical sorted key set.
func TestSkillsFSMaterializationCanonicalizesSelectedRefs(t *testing.T) {
	setSkillCache(t)
	fsys := fstest.MapFS{
		"main/SKILL.md":            &fstest.MapFile{Data: []byte("# root skill\n")},
		"main/references/rules.md": &fstest.MapFile{Data: []byte("rules body")},
	}
	fsSkill := skill.FS(fsys, "main")
	fsSkill.Key = "team/main"

	fake := newFakeDriver()
	agent := adaptor.New(fake,
		adaptor.WithSkills(fsSkill),
		adaptor.WithSkillProvider(skill.Set{
			"system/core": skill.Require(skill.Inline("system/core", "# core\n"), "tenant mandate"),
		}),
	)
	if _, err := agent.Run(context.Background(), "go"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	req := fake.lastRequest(t)
	wantKeys := []string{"system/core", "team/main"}
	gotKeys := req.Skills.Keys()
	if len(gotKeys) != len(wantKeys) || gotKeys[0] != wantKeys[0] || gotKeys[1] != wantKeys[1] {
		t.Fatalf("selected keys = %v, want canonical sorted %v", gotKeys, wantKeys)
	}

	var mainEntry *driver.ResolvedSkill
	for i := range req.Skills.Entries {
		if req.Skills.Entries[i].Key == "team/main" {
			mainEntry = &req.Skills.Entries[i]
		}
	}
	if mainEntry == nil {
		t.Fatal("no resolved entry for team/main")
	}
	// The FS tree is reproduced on disk, references included.
	skillMD, err := os.ReadFile(filepath.Join(mainEntry.SourcePath, "SKILL.md"))
	if err != nil {
		t.Fatalf("materialized SKILL.md: %v", err)
	}
	if !strings.Contains(string(skillMD), "root") {
		t.Errorf("SKILL.md content = %q, want the FS root document", skillMD)
	}
	rules, err := os.ReadFile(filepath.Join(mainEntry.SourcePath, "references", "rules.md"))
	if err != nil {
		t.Fatalf("materialized references/rules.md: %v", err)
	}
	if string(rules) != "rules body" {
		t.Errorf("rules.md = %q, want %q", rules, "rules body")
	}
}

// TestSelectSkillsUpdatesProcessLocalSelection: SelectSkills replaces the
// default selection for subsequent runs and inspection reports, process-
// locally (legacy Admin.SetSelectedSkills semantics).
func TestSelectSkillsUpdatesProcessLocalSelection(t *testing.T) {
	setSkillCache(t)
	ctx := context.Background()
	fake := newFakeDriver()
	agent := adaptor.New(fake,
		adaptor.WithSkills(skill.Key("team/default")),
		adaptor.WithSkillProvider(skill.Set{
			"team/default": skill.Inline("team/default", "# default\n"),
			"team/review":  skill.Inline("team/review", "# review\n"),
		}),
	)

	snap, err := agent.SelectSkills(ctx, []string{"team/review"})
	if err != nil {
		t.Fatalf("SelectSkills: %v", err)
	}
	if !equalUnordered(snap.Selected, []string{"team/review"}) {
		t.Errorf("snapshot.Selected = %v, want [team/review]", snap.Selected)
	}

	if _, err := agent.Run(ctx, "go"); err != nil {
		t.Fatalf("Run after SelectSkills: %v", err)
	}
	if keys := fake.lastRequest(t).Skills.Keys(); !equalUnordered(keys, []string{"team/review"}) {
		t.Errorf("run keys = %v, want the overridden selection", keys)
	}

	listed, err := agent.Inspect().Skills(ctx)
	if err != nil {
		t.Fatalf("Inspect().Skills: %v", err)
	}
	if !equalUnordered(listed.Selected, []string{"team/review"}) {
		t.Errorf("Inspect().Skills.Selected = %v, want the overridden selection", listed.Selected)
	}
}

// TestSelectSkillsUnknownKeyDoesNotInstallOverride: an unknown key fails with
// ErrSkillNotFound and the previous selection stays in force.
func TestSelectSkillsUnknownKeyDoesNotInstallOverride(t *testing.T) {
	setSkillCache(t)
	ctx := context.Background()
	fake := newFakeDriver()
	agent := adaptor.New(fake,
		adaptor.WithSkills(skill.Inline("team/default", "# default\n")),
		adaptor.WithSkillProvider(skill.Set{}),
	)

	if _, err := agent.SelectSkills(ctx, []string{"team/unknown"}); !errors.Is(err, adaptor.ErrSkillNotFound) {
		t.Fatalf("SelectSkills unknown key: err = %v, want ErrSkillNotFound", err)
	}
	// The failed override must not have been installed: the default still
	// resolves.
	if _, err := agent.Run(ctx, "go"); err != nil {
		t.Fatalf("Run after failed SelectSkills: %v", err)
	}
	if keys := fake.lastRequest(t).Skills.Keys(); !equalUnordered(keys, []string{"team/default"}) {
		t.Errorf("run keys = %v, want the untouched default selection", keys)
	}
}

// TestSkillsAdditiveMergingWithRequired: defaults, per-run refs, and
// provider-required skills form a union — nothing replaces anything.
func TestSkillsAdditiveMergingWithRequired(t *testing.T) {
	setSkillCache(t)
	fake := newFakeDriver()
	agent := adaptor.New(fake,
		adaptor.WithSkills(skill.Inline("team/default", "# default\n")),
		adaptor.WithSkillProvider(skill.Set{
			"team/optional":   skill.Inline("team/optional", "# optional\n"),
			"system/required": skill.Require(skill.Inline("system/required", "# required\n"), "compliance"),
		}),
	)
	if _, err := agent.Run(context.Background(), "go", adaptor.WithSkills(skill.Key("team/optional"))); err != nil {
		t.Fatalf("Run: %v", err)
	}
	keys := fake.lastRequest(t).Skills.Keys()
	if !equalUnordered(keys, []string{"system/required", "team/default", "team/optional"}) {
		t.Errorf("keys = %v, want default ∪ run ∪ required", keys)
	}

	// The required skill joins even when the caller referenced nothing.
	fake2 := newFakeDriver()
	agent2 := adaptor.New(fake2, adaptor.WithSkillProvider(skill.Set{
		"system/required": skill.Require(skill.Inline("system/required", "# required\n"), "compliance"),
	}))
	if _, err := agent2.Run(context.Background(), "go"); err != nil {
		t.Fatalf("Run without refs: %v", err)
	}
	if keys := fake2.lastRequest(t).Skills.Keys(); !equalUnordered(keys, []string{"system/required"}) {
		t.Errorf("keys = %v, want the provider-mandated required skill", keys)
	}
}

// TestSkillSameKeySameValueMerges: structurally equal duplicates collapse to
// one entry instead of conflicting.
func TestSkillSameKeySameValueMerges(t *testing.T) {
	setSkillCache(t)
	fake := newFakeDriver()
	agent := adaptor.New(fake, adaptor.WithSkills(skill.Inline("team/shared", "# shared\n")))
	if _, err := agent.Run(context.Background(), "go", adaptor.WithSkills(skill.Inline("team/shared", "# shared\n"))); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if keys := fake.lastRequest(t).Skills.Keys(); !equalUnordered(keys, []string{"team/shared"}) {
		t.Errorf("keys = %v, want one merged entry", keys)
	}
}

// TestSkillSameKeyDifferentValueConflicts: structural divergence under one
// key is ErrSkillKeyConflict, pre-launch, on both the run path and the
// inspection path.
func TestSkillSameKeyDifferentValueConflicts(t *testing.T) {
	setSkillCache(t)
	ctx := context.Background()

	t.Run("run path", func(t *testing.T) {
		fake := newFakeDriver()
		agent := adaptor.New(fake, adaptor.WithSkills(skill.Inline("team/shared", "# version A\n")))
		_, err := agent.Run(ctx, "go", adaptor.WithSkills(skill.Inline("team/shared", "# version B\n")))
		if !errors.Is(err, adaptor.ErrSkillKeyConflict) {
			t.Fatalf("err = %v, want ErrSkillKeyConflict", err)
		}
		var conflict *adaptor.SkillKeyConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("err = %v, want *SkillKeyConflictError in chain", err)
		}
		if conflict.Key != "team/shared" {
			t.Errorf("conflict.Key = %q, want team/shared", conflict.Key)
		}
		if fake.runCount() != 0 {
			t.Errorf("driver ran %d time(s), conflicts must fail pre-launch", fake.runCount())
		}
	})

	t.Run("inspect path", func(t *testing.T) {
		agent := adaptor.New(newFakeDriver(),
			adaptor.WithSkills(skill.Inline("team/shared", "# version A\n")),
			adaptor.WithSkillProvider(skill.Set{
				"team/shared": skill.Inline("team/shared", "# version B\n"),
			}),
		)
		if _, err := agent.Inspect().Skills(ctx); !errors.Is(err, adaptor.ErrSkillKeyConflict) {
			t.Fatalf("Inspect().Skills err = %v, want ErrSkillKeyConflict", err)
		}
	})
}

// TestSkillBareKeyNotFound: a bare key that no provider resolves fails with
// ErrSkillNotFound naming the key — with no provider configured and with a
// provider that returns nothing.
func TestSkillBareKeyNotFound(t *testing.T) {
	setSkillCache(t)
	ctx := context.Background()

	t.Run("no provider", func(t *testing.T) {
		fake := newFakeDriver()
		agent := adaptor.New(fake)
		_, err := agent.Run(ctx, "go", adaptor.WithSkills(skill.Key("team/missing")))
		if !errors.Is(err, adaptor.ErrSkillNotFound) {
			t.Fatalf("err = %v, want ErrSkillNotFound", err)
		}
		if !strings.Contains(err.Error(), "team/missing") {
			t.Errorf("err = %q, want the unresolved key named", err)
		}
		if fake.runCount() != 0 {
			t.Errorf("driver ran %d time(s), want pre-launch failure", fake.runCount())
		}
	})

	t.Run("provider resolves nothing", func(t *testing.T) {
		fake := newFakeDriver()
		agent := adaptor.New(fake, adaptor.WithSkillProvider(emptyResolveProvider{}))
		_, err := agent.Run(ctx, "go", adaptor.WithSkills(skill.Key("team/missing")))
		if !errors.Is(err, adaptor.ErrSkillNotFound) {
			t.Fatalf("err = %v, want ErrSkillNotFound", err)
		}
		if fake.runCount() != 0 {
			t.Errorf("driver ran %d time(s), want pre-launch failure", fake.runCount())
		}
	})
}

// TestSkillNilSourceFails: a Skill without a Source is rejected before the
// driver launches.
func TestSkillNilSourceFails(t *testing.T) {
	setSkillCache(t)
	fake := newFakeDriver()
	agent := adaptor.New(fake, adaptor.WithSkills(skill.Skill{Key: "team/empty"}))
	if _, err := agent.Run(context.Background(), "go"); !errors.Is(err, adaptor.ErrSkillSourceMissing) {
		t.Fatalf("err = %v, want ErrSkillSourceMissing", err)
	}
	if fake.runCount() != 0 {
		t.Errorf("driver ran %d time(s), want pre-launch failure", fake.runCount())
	}
}

// TestSkillMaterializationFailureIsPreLaunch: a selected skill whose source
// cannot be staged fails the run with the typed materialization error, and
// the driver is never invoked — on Run and on Stream.
//
// The bad archive is passed at RUN scope, matching the root baseline
// (TestSkillMaterializationFailure used a per-run WithSkills). Default-scope
// archive skills cannot reach materialization at all: the engine merger's
// skillSourcesEquivalent has no SkillFromArchive case, so the default ref and
// its own candidate-pool copy report a same-key/different-content conflict —
// a pre-existing engine limitation shared with the legacy run path
// (execute.go passes defaults.Skills as candidates too).
func TestSkillMaterializationFailureIsPreLaunch(t *testing.T) {
	setSkillCache(t)
	ctx := context.Background()
	badSkill := skill.Archive("team/bad", skill.ArchiveBytes([]byte("not a zip archive")), skill.WithFormat(skill.FormatZip))

	assertMatErr := func(t *testing.T, err error) {
		t.Helper()
		if !errors.Is(err, adaptor.ErrSkillMaterializationFailed) {
			t.Fatalf("err = %v, want ErrSkillMaterializationFailed", err)
		}
		var matErr *adaptor.SkillMaterializationError
		if !errors.As(err, &matErr) {
			t.Fatalf("err = %v, want *SkillMaterializationError in chain", err)
		}
		if matErr.Key != "team/bad" {
			t.Errorf("Key = %q, want team/bad", matErr.Key)
		}
		if matErr.RuntimeName == "" {
			t.Error("RuntimeName empty, want the derived runtime name")
		}
		if matErr.Cause == nil {
			t.Error("Cause nil, want the underlying archive error")
		}
	}

	t.Run("Run", func(t *testing.T) {
		fake := newFakeDriver()
		agent := adaptor.New(fake)
		_, err := agent.Run(ctx, "go", adaptor.WithSkills(badSkill))
		assertMatErr(t, err)
		if fake.runCount() != 0 {
			t.Errorf("driver ran %d time(s), want pre-launch failure", fake.runCount())
		}
	})

	t.Run("Stream", func(t *testing.T) {
		fake := newFakeDriver()
		agent := adaptor.New(fake)
		stream := agent.Stream(ctx, "go", adaptor.WithSkills(badSkill))
		for range stream.Events() {
		}
		_, err := stream.Result()
		assertMatErr(t, err)
		if fake.runCount() != 0 {
			t.Errorf("driver ran %d time(s), want pre-launch failure", fake.runCount())
		}
	})
}

// TestSkillCacheRootHonoursEnv: AGENT_ADAPTOR_SKILL_CACHE_ROOT relocates the
// materialization cache; every staged path lives under it.
func TestSkillCacheRootHonoursEnv(t *testing.T) {
	root := setSkillCache(t)
	fake := newFakeDriver()
	agent := adaptor.New(fake, adaptor.WithSkills(skill.Inline("team/cached", "# cached\n")))
	if _, err := agent.Run(context.Background(), "go"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	entries := fake.lastRequest(t).Skills.Entries
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	sourcePath := entries[0].SourcePath
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		resolvedRoot = root
	}
	resolvedSource, err := filepath.EvalSymlinks(sourcePath)
	if err != nil {
		resolvedSource = sourcePath
	}
	if !strings.HasPrefix(resolvedSource, resolvedRoot) {
		t.Errorf("SourcePath %q not under configured cache root %q", resolvedSource, resolvedRoot)
	}
	if _, err := os.Stat(filepath.Join(sourcePath, "SKILL.md")); err != nil {
		t.Errorf("materialized SKILL.md: %v", err)
	}
}

// TestSkillCachesIdenticalAcrossRuns: an identical skill re-resolves to the
// same cache directory without rewriting it (ready-marker mtime unchanged).
func TestSkillCachesIdenticalAcrossRuns(t *testing.T) {
	setSkillCache(t)
	ctx := context.Background()
	fake := newFakeDriver()
	agent := adaptor.New(fake, adaptor.WithSkills(skill.Inline("team/stable", "# stable\n")))

	if _, err := agent.Run(ctx, "first"); err != nil {
		t.Fatalf("run 1: %v", err)
	}
	firstPath := fake.request(t, 0).Skills.Entries[0].SourcePath
	marker := filepath.Join(firstPath, ".agent-adaptor-ready")
	info1, err := os.Stat(marker)
	if err != nil {
		t.Fatalf("ready marker after run 1: %v", err)
	}

	if _, err := agent.Run(ctx, "second"); err != nil {
		t.Fatalf("run 2: %v", err)
	}
	secondPath := fake.request(t, 1).Skills.Entries[0].SourcePath
	if secondPath != firstPath {
		t.Errorf("cache path changed between runs: %q vs %q", firstPath, secondPath)
	}
	info2, err := os.Stat(marker)
	if err != nil {
		t.Fatalf("ready marker after run 2: %v", err)
	}
	if !info2.ModTime().Equal(info1.ModTime()) {
		t.Errorf("ready marker rewritten: %v vs %v — cache must be reused, not restaged", info2.ModTime(), info1.ModTime())
	}
}

// TestSkillResolutionConcurrentSafe: 8 goroutines resolving the same skill
// through one agent complete without errors (deterministic concurrency
// check; -race is unavailable in this environment).
func TestSkillResolutionConcurrentSafe(t *testing.T) {
	setSkillCache(t)
	fake := newFakeDriver()
	agent := adaptor.New(fake, adaptor.WithSkills(skill.Inline("team/parallel", "# parallel\n")))

	const workers = 8
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := agent.Run(context.Background(), "go"); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent run: %v", err)
	}
	if fake.runCount() != workers {
		t.Errorf("driver saw %d runs, want %d", fake.runCount(), workers)
	}
}

// TestSkillFingerprintStableAndSelectionSensitive: the resolved-skills
// fingerprint is deterministic for an unchanged selection and moves when the
// selection changes.
func TestSkillFingerprintStableAndSelectionSensitive(t *testing.T) {
	setSkillCache(t)
	ctx := context.Background()
	fake := newFakeDriver()
	agent := adaptor.New(fake, adaptor.WithSkills(skill.Inline("team/fp", "# fp\n")))

	if _, err := agent.Run(ctx, "one"); err != nil {
		t.Fatalf("run 1: %v", err)
	}
	if _, err := agent.Run(ctx, "two"); err != nil {
		t.Fatalf("run 2: %v", err)
	}
	fp1 := fake.request(t, 0).Skills.Fingerprint
	fp2 := fake.request(t, 1).Skills.Fingerprint
	if fp1 == "" || fp1 != fp2 {
		t.Errorf("fingerprints %q / %q, want identical non-empty values", fp1, fp2)
	}

	if _, err := agent.Run(ctx, "three", adaptor.WithSkills(skill.Inline("team/other", "# other\n"))); err != nil {
		t.Fatalf("run 3: %v", err)
	}
	if fp3 := fake.request(t, 2).Skills.Fingerprint; fp3 == fp1 {
		t.Errorf("fingerprint unchanged (%q) after the selection changed", fp3)
	}
}

// TestInspectSkillsSnapshotCandidates: the inspection snapshot exposes the
// full candidate pool without auto-selecting it — inline defaults stay
// selected, catalogue entries appear as available-but-off, and providers
// without a catalogue contribute nothing.
func TestInspectSkillsSnapshotCandidates(t *testing.T) {
	setSkillCache(t)
	ctx := context.Background()

	t.Run("inline candidates beyond the selection", func(t *testing.T) {
		agent := adaptor.New(newFakeDriver(),
			adaptor.WithSkills(skill.Key("team/on")),
			adaptor.WithSkillProvider(skill.Set{
				"team/on":  skill.Inline("team/on", "# on\n"),
				"team/off": skill.Inline("team/off", "# off\n"),
			}),
		)
		snap, err := agent.Inspect().Skills(ctx)
		if err != nil {
			t.Fatalf("Inspect().Skills: %v", err)
		}
		if !equalUnordered(snap.Selected, []string{"team/on"}) {
			t.Errorf("Selected = %v, want only the referenced key", snap.Selected)
		}
		if !equalUnordered(skillKeys(snap.Resolved), []string{"team/off", "team/on"}) {
			t.Errorf("Resolved = %v, want the full catalogue", skillKeys(snap.Resolved))
		}
	})

	t.Run("catalogue contributes candidates, not selection", func(t *testing.T) {
		agent := adaptor.New(newFakeDriver(), adaptor.WithSkillProvider(catalogueOnlyProvider{
			skills: map[string]driver.Skill{
				"store/on-shelf": skill.Inline("store/on-shelf", "# shelf\n"),
			},
		}))
		snap, err := agent.Inspect().Skills(ctx)
		if err != nil {
			t.Fatalf("Inspect().Skills: %v", err)
		}
		if !sliceContains(skillKeys(snap.Resolved), "store/on-shelf") {
			t.Errorf("Resolved = %v, want the catalogue entry visible", skillKeys(snap.Resolved))
		}
		if sliceContains(snap.Selected, "store/on-shelf") {
			t.Errorf("Selected = %v, catalogue entries must not auto-select", snap.Selected)
		}
	})

	t.Run("provider without catalogue contributes nothing", func(t *testing.T) {
		agent := adaptor.New(newFakeDriver(), adaptor.WithSkillProvider(emptyResolveProvider{}))
		snap, err := agent.Inspect().Skills(ctx)
		if err != nil {
			t.Fatalf("Inspect().Skills: %v", err)
		}
		if len(snap.Selected) != 0 || len(snap.Resolved) != 0 {
			t.Errorf("Selected = %v, Resolved = %v; want both empty", snap.Selected, skillKeys(snap.Resolved))
		}
	})
}

// TestInspectSkillsPropagatesCatalogueError: catalogue failures surface
// verbatim from the inspection and selection surfaces.
func TestInspectSkillsPropagatesCatalogueError(t *testing.T) {
	setSkillCache(t)
	ctx := context.Background()
	sentinel := errors.New("catalogue backend down")
	agent := adaptor.New(newFakeDriver(), adaptor.WithSkillProvider(erroringCatalogProvider{
		skills:       map[string]driver.Skill{"team/x": skill.Inline("team/x", "# x\n")},
		catalogueErr: sentinel,
	}))

	if _, err := agent.Inspect().Skills(ctx); !errors.Is(err, sentinel) {
		t.Errorf("Inspect().Skills err = %v, want the catalogue error verbatim", err)
	}
	if _, err := agent.SelectSkills(ctx, []string{"team/x"}); !errors.Is(err, sentinel) {
		t.Errorf("SelectSkills err = %v, want the catalogue error verbatim", err)
	}
}
