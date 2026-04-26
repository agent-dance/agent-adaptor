package agentadaptor

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

// --- clone helpers --------------------------------------------------------

// cloneSkill produces a deep copy of a Skill value. Metadata is cloned but
// the Source interface is reused by value; SkillSource implementations are
// intentionally immutable so sharing them is safe.
func cloneSkill(s Skill) Skill {
	return Skill{
		Key:      s.Key,
		Source:   s.Source,
		Required: s.Required,
		Reason:   s.Reason,
		Metadata: cloneStringMap(s.Metadata),
	}
}

func cloneSkills(values []Skill) []Skill {
	if len(values) == 0 {
		return nil
	}
	out := make([]Skill, len(values))
	for i, v := range values {
		out[i] = cloneSkill(v)
	}
	return out
}

func cloneSkillRefs(values []SkillRef) []SkillRef {
	if len(values) == 0 {
		return nil
	}
	out := make([]SkillRef, 0, len(values))
	for _, v := range values {
		switch ref := v.(type) {
		case nil:
			continue
		case SkillKey:
			out = append(out, ref)
		case Skill:
			out = append(out, cloneSkill(ref))
		default:
			// The SkillRef sum-type is closed; we never expect to see an
			// out-of-family implementation in the SDK, but the defensive
			// branch keeps cloning total.
			out = append(out, v)
		}
	}
	return out
}

func cloneResolvedSkill(e ResolvedSkill) ResolvedSkill {
	return ResolvedSkill{
		Key:         e.Key,
		RuntimeName: e.RuntimeName,
		SourcePath:  e.SourcePath,
		Required:    e.Required,
		Reason:      e.Reason,
		Metadata:    cloneStringMap(e.Metadata),
	}
}

func cloneResolvedSkills(r ResolvedSkills) ResolvedSkills {
	entries := make([]ResolvedSkill, len(r.Entries))
	for i, entry := range r.Entries {
		entries[i] = cloneResolvedSkill(entry)
	}
	return ResolvedSkills{
		Mode:        r.Mode,
		Entries:     entries,
		Warnings:    cloneStrings(r.Warnings),
		Fingerprint: r.Fingerprint,
	}
}

func cloneSnapshotEntries(values []SnapshotEntry) []SnapshotEntry {
	if len(values) == 0 {
		return nil
	}
	out := make([]SnapshotEntry, len(values))
	copy(out, values)
	return out
}

// --- key / slug helpers ---------------------------------------------------

func normalizeSkillKey(value string) string {
	return strings.TrimSpace(value)
}

func skillSlug(value string) string {
	trimmed := strings.TrimSpace(strings.ToLower(value))
	if trimmed == "" {
		return ""
	}
	trimmed = strings.ReplaceAll(trimmed, "\\", "/")
	parts := strings.Split(trimmed, "/")
	return parts[len(parts)-1]
}

// defaultSkillRuntimeName derives the runtime directory name used when the
// materializer writes a skill to disk. It prefers the Metadata override
// (SkillMetadataRuntimeName), falls back to the slugged Key, and finally to
// "skill" when the Key is empty.
func defaultSkillRuntimeName(s Skill) string {
	if override := strings.TrimSpace(s.Metadata[SkillMetadataRuntimeName]); override != "" {
		return override
	}
	slug := skillSlug(s.Key)
	if slug == "" {
		slug = "skill"
	}
	if strings.ToLower(strings.TrimSpace(s.Key)) == slug {
		return slug
	}
	// The slug differs from the original Key (e.g. "team/retention" vs
	// "retention"). Preserve uniqueness by suffixing a short content hash.
	return slug + "--" + stableHash(s.Key)[:10]
}

// --- skill equivalence -----------------------------------------------------

// skillsEquivalent reports whether two Skill values describe the same
// content (Key, Source, Required, Reason, Metadata). This is the invariant
// enforced by ErrSkillKeyConflict during additive merging.
func skillsEquivalent(a, b Skill) bool {
	if normalizeSkillKey(a.Key) != normalizeSkillKey(b.Key) {
		return false
	}
	if a.Required != b.Required {
		return false
	}
	if strings.TrimSpace(a.Reason) != strings.TrimSpace(b.Reason) {
		return false
	}
	if !stringMapsEqual(a.Metadata, b.Metadata) {
		return false
	}
	return skillSourcesEquivalent(a.Source, b.Source)
}

func skillSourcesEquivalent(a, b SkillSource) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	switch left := a.(type) {
	case SkillFromPath:
		right, ok := b.(SkillFromPath)
		if !ok {
			return false
		}
		return cleanSkillPath(left.Path) == cleanSkillPath(right.Path)
	case SkillFromFS:
		right, ok := b.(SkillFromFS)
		if !ok {
			return false
		}
		if left.Root != right.Root {
			return false
		}
		// fs.FS identity is the most reliable comparison we can make without
		// recursive enumeration. Hosts that want content-level equivalence
		// should wrap their FS in a stable value type.
		return fsIdentityEqual(left.FS, right.FS)
	case SkillFromInline:
		right, ok := b.(SkillFromInline)
		if !ok {
			return false
		}
		return left.SkillMD == right.SkillMD
	}
	return false
}

func stringMapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if other, ok := b[key]; !ok || other != value {
			return false
		}
	}
	return true
}

// fsIdentityEqual reports whether two fs.FS values can be treated as the
// same logical filesystem for the purpose of skill equivalence.
//
// The check is deliberately conservative:
//
//   - Both nil → equal.
//   - One nil → not equal.
//   - Different dynamic types → not equal.
//   - Same dynamic type that is comparable → use Go's `==` on the
//     interface values. This covers the common cases (pointer identity
//     for *fstest.MapFS, embed.FS values, small named structs used as
//     fs.FS adapters) without the cost or surprise of reflect.DeepEqual.
//   - Same dynamic type that is NOT comparable but has an addressable
//     data pointer (map, slice, chan, func, pointer) → compare the
//     underlying data pointer. Two references to the same map / slice
//     header are logically the same FS; two independent allocations are
//     not. This keeps fstest.MapFS and similar test helpers usable as
//     skill sources without forcing hosts to wrap them.
//   - Anything else (e.g. a non-comparable struct value) → not equal.
//     Hosts that need content equivalence should hand the SDK the same
//     FS instance twice, or wrap their FS in a comparable named type.
func fsIdentityEqual(a, b fs.FS) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	ta := reflect.TypeOf(a)
	tb := reflect.TypeOf(b)
	if ta != tb {
		return false
	}
	if ta == nil {
		return false
	}
	if ta.Comparable() {
		return a == b
	}
	switch ta.Kind() {
	case reflect.Map, reflect.Slice, reflect.Chan, reflect.Func, reflect.Pointer, reflect.UnsafePointer:
		return reflect.ValueOf(a).Pointer() == reflect.ValueOf(b).Pointer()
	default:
		return false
	}
}

func cleanSkillPath(p string) string {
	trimmed := strings.TrimSpace(p)
	if trimmed == "" {
		return ""
	}
	if filepath.IsAbs(trimmed) {
		return filepath.Clean(trimmed)
	}
	abs, err := filepath.Abs(trimmed)
	if err != nil {
		return filepath.Clean(trimmed)
	}
	return filepath.Clean(abs)
}

// --- materializer ---------------------------------------------------------

// defaultSkillMaterializer implements SkillMaterializer using the
// XDG-ish cache root described in docs/skill-api-design.md §3. The
// archive-handling subset is configurable via DefaultMaterializerOption
// (see archive_materializer.go).
type defaultSkillMaterializer struct {
	cfg materializerConfig
}

func newDefaultSkillMaterializer() SkillMaterializer {
	return &defaultSkillMaterializer{cfg: defaultMaterializerConfig()}
}

// SkillCacheRootEnv is the environment variable that overrides the default
// materializer's cache root. Adapters inspect the same variable (indirectly
// through internal/skillruntime.ManagedSkillCacheRoot) to identify the
// paths they are allowed to manage, so exposing it here keeps both sides of
// the contract in sync.
const SkillCacheRootEnv = "AGENT_ADAPTOR_SKILL_CACHE_ROOT"

// Materialize implements SkillMaterializer.
func (m *defaultSkillMaterializer) Materialize(ctx context.Context, s Skill) (string, error) {
	if s.Source == nil {
		return "", fmt.Errorf("%w: skill %q", ErrSkillSourceMissing, s.Key)
	}
	switch src := s.Source.(type) {
	case SkillFromPath:
		cleaned := cleanSkillPath(src.Path)
		if cleaned == "" {
			return "", fmt.Errorf("agentadaptor: skill %q has empty SkillFromPath.Path", s.Key)
		}
		info, err := os.Stat(cleaned)
		if err != nil {
			return "", fmt.Errorf("agentadaptor: skill %q path %q unavailable: %w", s.Key, cleaned, err)
		}
		if info.IsDir() {
			skillFile := filepath.Join(cleaned, "SKILL.md")
			if stat, err := os.Stat(skillFile); err != nil || stat.IsDir() {
				return "", fmt.Errorf("agentadaptor: skill %q path %q does not contain SKILL.md", s.Key, cleaned)
			}
			return cleaned, nil
		}
		if strings.EqualFold(filepath.Base(cleaned), "SKILL.md") {
			return filepath.Dir(cleaned), nil
		}
		return "", fmt.Errorf("agentadaptor: skill %q path %q is not a SKILL.md file or directory", s.Key, cleaned)

	case SkillFromFS:
		return m.writeFromFS(s, src)
	case SkillFromInline:
		return m.writeFromInline(s, src)
	case SkillFromArchive:
		return m.writeFromArchive(ctx, s, src)
	default:
		return "", fmt.Errorf("agentadaptor: skill %q has unsupported source type %T", s.Key, s.Source)
	}
}

// writeFromArchive reads, validates, and extracts a SkillFromArchive
// source. The implementation lives in archive_materializer.go but the
// dispatch happens here so the materializer's "if a known source, do
// this; otherwise unsupported" pattern stays in one place.
func (m *defaultSkillMaterializer) writeFromArchive(ctx context.Context, s Skill, src SkillFromArchive) (string, error) {
	if src.Archive == nil {
		return "", fmt.Errorf("agentadaptor: skill %q SkillFromArchive.Archive is nil", s.Key)
	}
	rc, err := src.Archive(ctx)
	if err != nil {
		return "", fmt.Errorf("agentadaptor: skill %q archive open: %w", s.Key, err)
	}
	raw, err := readArchiveBytes(ctx, rc, m.cfg.maxArchiveBytes)
	if err != nil {
		return "", fmt.Errorf("agentadaptor: skill %q: %w", s.Key, err)
	}
	extraction, err := extractArchive(raw, src.Format, src.Subpath, m.cfg)
	if err != nil {
		return "", fmt.Errorf("agentadaptor: skill %q: %w", s.Key, err)
	}
	if _, ok := extraction.Files["SKILL.md"]; !ok {
		return "", fmt.Errorf("agentadaptor: skill %q archive missing SKILL.md (subpath=%q)", s.Key, src.Subpath)
	}
	// writeFiles computes its own content-addressed fingerprint from
	// the per-file hashes; identical archive bytes automatically yield
	// identical file maps and therefore the same cache hit. The
	// host-supplied src.Fingerprint is currently advisory (carried in
	// SkillFromArchive's doc as the cache key hint) and not consulted
	// here so the cache remains a function of materialised content.
	return m.writeFiles(s, extraction.Files)
}

func (m *defaultSkillMaterializer) writeFromFS(s Skill, src SkillFromFS) (string, error) {
	files, err := collectFSFiles(src.FS, strings.TrimSpace(src.Root))
	if err != nil {
		return "", fmt.Errorf("agentadaptor: skill %q fs.FS walk failed: %w", s.Key, err)
	}
	if _, ok := files["SKILL.md"]; !ok {
		return "", fmt.Errorf("agentadaptor: skill %q fs.FS tree missing SKILL.md", s.Key)
	}
	return m.writeFiles(s, files)
}

func (m *defaultSkillMaterializer) writeFromInline(s Skill, src SkillFromInline) (string, error) {
	trimmed := strings.TrimSpace(src.SkillMD)
	if trimmed == "" {
		return "", fmt.Errorf("agentadaptor: skill %q SkillFromInline.SkillMD is empty", s.Key)
	}
	return m.writeFiles(s, map[string][]byte{"SKILL.md": []byte(src.SkillMD)})
}

// writeFiles performs the atomic staging-then-rename dance common to all
// non-path sources. Concurrency-safe: the staging directory is created via
// os.MkdirTemp, which guarantees a unique path per goroutine/process; any
// failure path (including the rename-race fallback) removes the staging
// directory before returning.
func (m *defaultSkillMaterializer) writeFiles(s Skill, files map[string][]byte) (string, error) {
	runtimeName := defaultSkillRuntimeName(s)
	hashInput := make(map[string]string, len(files))
	for name, content := range files {
		hashInput[name] = stableHash(content)
	}
	fingerprint := stableHash(s.Key, runtimeName, s.Required, s.Reason, s.Metadata, hashInput)
	targetDir := filepath.Join(m.cfg.cacheRoot, runtimeName+"--"+fingerprint[:12])
	readyMarker := filepath.Join(targetDir, ".agent-adaptor-ready")
	if _, err := os.Stat(readyMarker); err == nil {
		return targetDir, nil
	}
	parent := filepath.Dir(targetDir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", err
	}
	tmpDir, err := os.MkdirTemp(parent, runtimeName+".tmp-")
	if err != nil {
		return "", err
	}
	cleanupTmp := true
	defer func() {
		if cleanupTmp {
			_ = os.RemoveAll(tmpDir)
		}
	}()
	for rel, content := range files {
		dest := filepath.Join(tmpDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(dest, content, 0o644); err != nil {
			return "", err
		}
	}
	if err := os.WriteFile(filepath.Join(tmpDir, ".agent-adaptor-ready"), []byte(fingerprint), 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmpDir, targetDir); err != nil {
		// A concurrent writer may have already promoted its own staging
		// directory to targetDir. If the ready marker is present the
		// content is identical (same fingerprint), so fall through to a
		// success return AFTER cleaning up our own tmpDir.
		if _, statErr := os.Stat(readyMarker); statErr == nil {
			return targetDir, nil
		}
		return "", err
	}
	cleanupTmp = false
	return targetDir, nil
}

// managedSkillCacheRoot returns the filesystem root under which the default
// SkillMaterializer writes materialized skills. Resolution order:
//
//  1. Explicit override (when non-empty).
//  2. SkillCacheRootEnv (AGENT_ADAPTOR_SKILL_CACHE_ROOT). Adapters rely on
//     this value for "is this path managed by us" checks, so the SDK MUST
//     honour it to avoid misclassifying managed symlinks as unmanaged.
//  3. os.UserCacheDir()/agent-adaptor/skill-cache.
//  4. os.TempDir()/agent-adaptor/skill-cache as a last-resort fallback.
//
// This function is deliberately kept in lock-step with
// internal/skillruntime.ManagedSkillCacheRoot, which surfaces the same
// resolution to adapter-side code.
func managedSkillCacheRoot(override string) string {
	if trimmed := strings.TrimSpace(override); trimmed != "" {
		return filepath.Clean(trimmed)
	}
	if envOverride := strings.TrimSpace(os.Getenv(SkillCacheRootEnv)); envOverride != "" {
		return filepath.Clean(envOverride)
	}
	root, err := os.UserCacheDir()
	if err != nil || strings.TrimSpace(root) == "" {
		root = os.TempDir()
	}
	return filepath.Join(root, "agent-adaptor", "skill-cache")
}

// collectFSFiles enumerates every file under root in fsys and returns their
// content keyed by forward-slashed relative path. An empty root is treated
// as ".".
func collectFSFiles(fsys fs.FS, root string) (map[string][]byte, error) {
	if fsys == nil {
		return nil, errors.New("fs is nil")
	}
	if root == "" {
		root = "."
	}
	out := map[string][]byte{}
	err := fs.WalkDir(fsys, root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		rel := path
		if root != "." {
			rel = strings.TrimPrefix(path, root)
			rel = strings.TrimPrefix(rel, "/")
		}
		if rel == "" {
			return nil
		}
		content, err := fs.ReadFile(fsys, path)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = content
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// --- conflict-aware merging -----------------------------------------------

type sourceLabel string

const (
	sourceLabelProvider  sourceLabel = "provider"
	sourceLabelCandidate sourceLabel = "binding:candidate"
	sourceLabelDefault   sourceLabel = "binding:default"
	sourceLabelRun       sourceLabel = "run:per-call"
)

type skillBucket struct {
	skill   Skill
	sources map[sourceLabel]struct{}
}

// skillMerger accumulates Skill candidates from multiple sources and
// enforces the "same key, same value" invariant.
type skillMerger struct {
	entries map[string]*skillBucket
	order   []string
}

func newSkillMerger() *skillMerger {
	return &skillMerger{entries: map[string]*skillBucket{}}
}

// add inserts a skill under the given sourceLabel. If a different skill has
// previously been recorded under the same key, a *SkillKeyConflictError is
// returned.
func (m *skillMerger) add(label sourceLabel, s Skill) error {
	key := normalizeSkillKey(s.Key)
	if key == "" {
		return fmt.Errorf("%w: source %q", ErrSkillKeyMissing, label)
	}
	if s.Source == nil {
		return fmt.Errorf("%w: skill %q from source %q", ErrSkillSourceMissing, key, label)
	}
	existing, ok := m.entries[key]
	if !ok {
		m.entries[key] = &skillBucket{skill: cloneSkill(s), sources: map[sourceLabel]struct{}{label: {}}}
		m.order = append(m.order, key)
		return nil
	}
	if !skillsEquivalent(existing.skill, s) {
		labels := []string{string(label)}
		for existingLabel := range existing.sources {
			labels = append(labels, string(existingLabel))
		}
		return &SkillKeyConflictError{
			Key:     key,
			Sources: labels,
			Detail:  skillDiffDetail(existing.skill, s),
		}
	}
	existing.sources[label] = struct{}{}
	return nil
}

// addKey records a bare SkillKey reference. It requires that the key is
// already present in the merger (via the provider enumeration). Missing keys
// produce ErrSkillNotFound, which surfaces as a user-facing "you passed a
// key that the provider does not recognise" message.
func (m *skillMerger) addKey(label sourceLabel, key string) error {
	key = normalizeSkillKey(key)
	if key == "" {
		return fmt.Errorf("%w: source %q", ErrSkillKeyMissing, label)
	}
	existing, ok := m.entries[key]
	if !ok {
		return fmt.Errorf("%w: key %q referenced by %s", ErrSkillNotFound, key, label)
	}
	existing.sources[label] = struct{}{}
	return nil
}

// selected returns the union of source labels for a given key. Used to tell
// downstream code whether the skill was explicitly selected (default or
// per-run) or merely visible via the provider.
func (m *skillMerger) selected(key string) bool {
	bucket, ok := m.entries[key]
	if !ok {
		return false
	}
	if _, defaulted := bucket.sources[sourceLabelDefault]; defaulted {
		return true
	}
	if _, perRun := bucket.sources[sourceLabelRun]; perRun {
		return true
	}
	return false
}

// has reports whether a key is known to the merger.
func (m *skillMerger) has(key string) bool {
	_, ok := m.entries[normalizeSkillKey(key)]
	return ok
}

// hasSource reports whether the given source label has contributed to
// the bucket for key. Used by the resolution layer to decide which
// merged entries belong to the run's selected set (anything from
// provider / default / run is selected; candidate-only entries are
// registered for SetSelectedSkills lookups but not auto-selected).
func (m *skillMerger) hasSource(key string, label sourceLabel) bool {
	bucket, ok := m.entries[normalizeSkillKey(key)]
	if !ok {
		return false
	}
	_, present := bucket.sources[label]
	return present
}

// skills returns the merged skills in insertion order.
func (m *skillMerger) skills() []Skill {
	out := make([]Skill, 0, len(m.order))
	for _, key := range m.order {
		out = append(out, cloneSkill(m.entries[key].skill))
	}
	return out
}

// skillDiffDetail renders a short human-readable diff between two Skills.
func skillDiffDetail(a, b Skill) string {
	diffs := make([]string, 0, 4)
	if a.Required != b.Required {
		diffs = append(diffs, fmt.Sprintf("Required %v vs %v", a.Required, b.Required))
	}
	if strings.TrimSpace(a.Reason) != strings.TrimSpace(b.Reason) {
		diffs = append(diffs, fmt.Sprintf("Reason %q vs %q", a.Reason, b.Reason))
	}
	if !stringMapsEqual(a.Metadata, b.Metadata) {
		diffs = append(diffs, "Metadata differs")
	}
	if !skillSourcesEquivalent(a.Source, b.Source) {
		diffs = append(diffs, fmt.Sprintf("Source %T vs %T", a.Source, b.Source))
	}
	return strings.Join(diffs, "; ")
}

// sortSkills returns a copy of skills sorted by Key for deterministic
// reporting / hashing.
func sortSkills(skills []Skill) []Skill {
	out := cloneSkills(skills)
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}
