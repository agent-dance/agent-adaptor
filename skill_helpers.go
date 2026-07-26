package agentadaptor

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// This file keeps the components that must stay declared in the root
// package after the P0.2 engine extraction:
//
//   - the built-in default SkillMaterializer, whose archive handling is
//     implemented by archive_source.go / archive_materializer.go (those
//     files are untouched until P3.2 and live in this package);
//   - the conflict-aware skill merger, which the run-time resolution layer
//     uses (the resolution layer itself moves to internal/engine in batch 3,
//     at which point the merger follows it).
//
// Shared low-level helpers (clone/equivalence/slug/hash) moved to
// internal/engine and are reached through the unexported delegates in
// engine_bridge.go.

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
