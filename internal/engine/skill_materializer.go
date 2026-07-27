package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// This file holds the SDK's built-in SkillMaterializer. It used to live in
// the root package and was reached from here through an init()-time factory
// injection; P5.2 moved the implementation down so the engine is
// self-sufficient (a broken injection could otherwise make archive skills
// fail to materialize silently). The zip/tar/tgz extraction machinery it
// dispatches to lives in archive_materializer.go.

// defaultSkillMaterializer implements SkillMaterializer using the
// XDG-ish cache root described in docs/skill-api-design.md §3. The
// archive-handling subset is configurable via DefaultMaterializerOption
// (see archive_materializer.go).
type defaultSkillMaterializer struct {
	cfg materializerConfig
}

// Source projections are deliberately structural. Public source values live
// in package skill, while the engine still carries migration-only source
// names; consuming their behavior through these narrow interfaces avoids a
// dependency cycle and keeps one materialization implementation.
type skillPathProjection interface{ SkillPath() string }
type skillFSProjection interface{ SkillFS() (fs.FS, string) }
type inlineSkillProjection interface{ InlineSkillMD() string }
type archiveSkillProjection interface {
	SkillArchive() (func(context.Context) (io.ReadCloser, error), string, string, string)
}

type archiveSourceView struct {
	archive     func(context.Context) (io.ReadCloser, error)
	format      SkillArchiveFormat
	subpath     string
	fingerprint string
}

// newDefaultSkillMaterializer is the engine-internal entry point used when
// no host materializer is installed. It is the zero-option form of
// NewDefaultSkillMaterializer.
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
	case skillPathProjection:
		cleaned := cleanSkillPath(src.SkillPath())
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

	case skillFSProjection:
		fsys, root := src.SkillFS()
		return m.writeFromFS(s, fsys, root)
	case inlineSkillProjection:
		return m.writeFromInline(s, src.InlineSkillMD())
	case archiveSkillProjection:
		open, format, subpath, fingerprint := src.SkillArchive()
		return m.writeFromArchive(ctx, s, archiveSourceView{
			archive: open, format: SkillArchiveFormat(format), subpath: subpath, fingerprint: fingerprint,
		})
	default:
		return "", fmt.Errorf("agentadaptor: skill %q has unsupported source type %T", s.Key, s.Source)
	}
}

// writeFromArchive reads, validates, and extracts a SkillFromArchive
// source. The implementation lives in archive_materializer.go but the
// dispatch happens here so the materializer's "if a known source, do
// this; otherwise unsupported" pattern stays in one place.
func (m *defaultSkillMaterializer) writeFromArchive(ctx context.Context, s Skill, src archiveSourceView) (string, error) {
	if src.archive == nil {
		return "", fmt.Errorf("agentadaptor: skill %q SkillFromArchive.Archive is nil", s.Key)
	}
	rc, err := src.archive(ctx)
	if err != nil {
		return "", fmt.Errorf("agentadaptor: skill %q archive open: %w", s.Key, err)
	}
	raw, err := readArchiveBytes(ctx, rc, m.cfg.maxArchiveBytes)
	if err != nil {
		return "", fmt.Errorf("agentadaptor: skill %q: %w", s.Key, err)
	}
	extraction, err := extractArchive(raw, src.format, src.subpath, m.cfg)
	if err != nil {
		return "", fmt.Errorf("agentadaptor: skill %q: %w", s.Key, err)
	}
	if _, ok := extraction.Files["SKILL.md"]; !ok {
		return "", fmt.Errorf("agentadaptor: skill %q archive missing SKILL.md (subpath=%q)", s.Key, src.subpath)
	}
	// writeFiles computes its own content-addressed fingerprint from
	// the per-file hashes; identical archive bytes automatically yield
	// identical file maps and therefore the same cache hit. The
	// The declaration fingerprint is intentionally not consulted here:
	// materialized cache identity is a function of actual content.
	return m.writeFiles(s, extraction.Files)
}

func (m *defaultSkillMaterializer) writeFromFS(s Skill, fsys fs.FS, root string) (string, error) {
	files, err := collectFSFiles(fsys, strings.TrimSpace(root))
	if err != nil {
		return "", fmt.Errorf("agentadaptor: skill %q fs.FS walk failed: %w", s.Key, err)
	}
	if _, ok := files["SKILL.md"]; !ok {
		return "", fmt.Errorf("agentadaptor: skill %q fs.FS tree missing SKILL.md", s.Key)
	}
	return m.writeFiles(s, files)
}

func (m *defaultSkillMaterializer) writeFromInline(s Skill, skillMD string) (string, error) {
	trimmed := strings.TrimSpace(skillMD)
	if trimmed == "" {
		return "", fmt.Errorf("agentadaptor: skill %q SkillFromInline.SkillMD is empty", s.Key)
	}
	return m.writeFiles(s, map[string][]byte{"SKILL.md": []byte(skillMD)})
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
