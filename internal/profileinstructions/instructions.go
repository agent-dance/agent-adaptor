package profileinstructions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
	"github.com/agent-dance/agent-adaptor/internal/profilereconcile"
	"github.com/agent-dance/agent-adaptor/internal/profilestate"
)

type Prepared struct {
	Path           string
	Content        string
	Snapshot       engine.ResourceSnapshot
	PromptFallback bool
}

const resourceKind = string(engine.ProfileResourceInstructions)

func Snapshot(driverType, profileDir string, ref *driver.InstructionsBundleRef, synced bool) engine.ResourceSnapshot {
	target := targetFor(driverType, profileDir, ref)
	out := engine.ResourceSnapshot{
		Kind:            engine.ProfileResourceInstructions,
		Fingerprint:     fingerprint(ref),
		Support:         target.Support,
		Materialization: engine.ProfileResourceMaterializationNotMaterialized,
	}
	if ref == nil {
		return out
	}
	key := instructionKey(ref)
	if synced {
		out.Managed = []string{key}
		out.Materialization = target.Materialization
		if target.Warning != "" {
			out.Warnings = []string{target.Warning}
		}
		return out
	}
	out.Warnings = []string{"instructions are desired but not observed by ProfileSnapshot; call SyncProfile to materialize them"}
	if target.Warning != "" {
		out.Warnings = append(out.Warnings, target.Warning)
	}
	return out
}

func Sync(ctx context.Context, driverType, profileDir string, ref *driver.InstructionsBundleRef) (engine.ResourceSnapshot, string, error) {
	if strings.TrimSpace(profileDir) == "" {
		return engine.ResourceSnapshot{}, "", fmt.Errorf("profile instructions require profile directory")
	}
	lock, err := profilestate.AcquireLock(ctx, profileDir, profilestate.LockOptions{StaleAfter: 10 * time.Minute})
	if err != nil {
		return engine.ResourceSnapshot{}, "", err
	}
	defer lock.Release()

	manifest, err := profilestate.LoadManifest(profileDir)
	if err != nil {
		return engine.ResourceSnapshot{}, "", err
	}

	if ref == nil {
		if err := pruneProfileInstructionEntries(profileDir, &manifest, ""); err != nil {
			return engine.ResourceSnapshot{}, "", err
		}
		if err := profilestate.SaveManifest(profileDir, manifest); err != nil {
			return engine.ResourceSnapshot{}, "", err
		}
		return Snapshot(driverType, profileDir, nil, true), "", nil
	}

	target := targetFor(driverType, profileDir, ref)
	content, sourcePath, err := instructionContent(ref)
	if err != nil {
		return engine.ResourceSnapshot{}, "", err
	}
	if err := ensureManagedTargetAvailable(instructionKey(ref), target.Path, &manifest); err != nil {
		return engine.ResourceSnapshot{}, "", err
	}
	previousEntry, hadPrevious := manifest.Entry(resourceKind, instructionKey(ref))
	if err := profilestate.AtomicWriteFile(target.Path, []byte(content), 0o644); err != nil {
		return engine.ResourceSnapshot{}, "", err
	}
	manifest.Set(profilestate.ManifestEntry{
		Kind:        resourceKind,
		Key:         instructionKey(ref),
		Path:        target.Path,
		Fingerprint: fingerprint(ref),
		SourcePath:  sourcePath,
		Metadata: map[string]string{
			"provider":        driverType,
			"scope":           string(ref.Scope),
			"mode":            string(ref.Mode),
			"support":         string(target.Support),
			"materialization": string(target.Materialization),
			"native":          fmt.Sprint(target.Native),
		},
	})
	if hadPrevious && filepath.Clean(previousEntry.Path) != filepath.Clean(target.Path) {
		if err := removeProfileInstructionPath(profileDir, previousEntry.Path); err != nil {
			return engine.ResourceSnapshot{}, "", err
		}
	}
	if err := pruneProfileInstructionEntries(profileDir, &manifest, target.Path); err != nil {
		return engine.ResourceSnapshot{}, "", err
	}
	if err := profilestate.SaveManifest(profileDir, manifest); err != nil {
		return engine.ResourceSnapshot{}, "", err
	}
	return Snapshot(driverType, profileDir, ref, true), target.Path, nil
}

func PrepareForRun(ctx context.Context, driverType, profileDir, workspaceDir string, ref *driver.InstructionsBundleRef) (Prepared, error) {
	snapshot, path, err := Sync(ctx, driverType, profileDir, ref)
	if err != nil {
		return Prepared{}, err
	}
	if ref == nil {
		if err := pruneProjectTargets(ctx, driverType, workspaceDir); err != nil {
			return Prepared{}, err
		}
		return Prepared{Snapshot: snapshot}, nil
	}
	if projectTarget, ok := projectTargetFor(driverType, workspaceDir, ref); ok {
		projectSnapshot, projectPath, err := syncProjectTarget(ctx, projectTarget, ref)
		if err != nil {
			return Prepared{}, err
		}
		content, _, err := instructionContent(ref)
		if err != nil {
			return Prepared{}, err
		}
		return Prepared{Path: projectPath, Content: content, Snapshot: projectSnapshot}, nil
	}
	if err := pruneProjectTargets(ctx, driverType, workspaceDir); err != nil {
		return Prepared{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Prepared{}, err
	}
	return Prepared{Path: path, Content: string(raw), Snapshot: snapshot, PromptFallback: targetFor(driverType, profileDir, ref).PromptFallback}, nil
}

func PromptPrefix(prepared Prepared, mode driver.InstructionMode) string {
	if !prepared.PromptFallback {
		return ""
	}
	content := strings.TrimSpace(prepared.Content)
	if content == "" {
		return ""
	}
	verb := "Apply these additional profile instructions"
	if mode == driver.InstructionModeReplace {
		verb = "Use these profile instructions as the active instruction bundle"
	}
	return fmt.Sprintf("%s from %s:\n\n%s", verb, prepared.Path, content)
}

func Mode(ref *driver.InstructionsBundleRef) driver.InstructionMode {
	if ref == nil {
		return driver.InstructionModeAdditive
	}
	return ref.Mode
}

func directoryEntry(ref *driver.InstructionsBundleRef) profilereconcile.DirectoryEntry {
	key := instructionKey(ref)
	entry := profilereconcile.DirectoryEntry{
		Key:         key,
		RuntimeName: safeFileName(key) + ".md",
		SourcePath:  strings.TrimSpace(ref.Path),
		Content:     ref.Content,
		Fingerprint: fingerprint(ref),
		Metadata: map[string]string{
			"scope": string(ref.Scope),
			"mode":  string(ref.Mode),
		},
	}
	return entry
}

type target struct {
	Path            string
	Support         engine.ProfileResourceSupport
	Materialization engine.ProfileResourceMaterialization
	Warning         string
	Native          bool
	PromptFallback  bool
}

func targetFor(driverType, profileDir string, ref *driver.InstructionsBundleRef) target {
	base := filepath.Join(profileDir, ".agent-adaptor", "instructions", safeFileName(instructionKey(ref))+".md")
	out := target{
		Path:            base,
		Support:         engine.ProfileResourceSupportFallback,
		Materialization: engine.ProfileResourceMaterializationPromptInjected,
		Warning:         "instructions are materialized as an SDK-managed prompt fallback, not provider-native rules",
		PromptFallback:  true,
	}
	if ref == nil || ref.Scope == driver.InstructionScopeRun {
		return out
	}
	switch driverType {
	case "codex":
		name := "AGENTS.md"
		if ref.Mode == driver.InstructionModeReplace {
			name = "AGENTS.override.md"
		}
		return target{
			Path:            filepath.Join(profileDir, name),
			Support:         engine.ProfileResourceSupportPortableCore,
			Materialization: engine.ProfileResourceMaterializationNativeManaged,
			Native:          true,
		}
	case "claude":
		return target{
			Path:            filepath.Join(profileDir, "CLAUDE.md"),
			Support:         engine.ProfileResourceSupportPortableCore,
			Materialization: engine.ProfileResourceMaterializationNativeManaged,
			Native:          true,
		}
	case "codebuddy":
		return target{
			Path:            filepath.Join(profileDir, "CODEBUDDY.md"),
			Support:         engine.ProfileResourceSupportPortableCore,
			Materialization: engine.ProfileResourceMaterializationNativeManaged,
			Native:          true,
		}
	default:
		return out
	}
}

func projectTargetFor(driverType, workspaceDir string, ref *driver.InstructionsBundleRef) (target, bool) {
	if driverType != "cursor" || ref == nil || strings.TrimSpace(workspaceDir) == "" {
		return target{}, false
	}
	switch ref.Scope {
	case driver.InstructionScopeProject, driver.InstructionScopeLocal:
	default:
		return target{}, false
	}
	return target{
		Path:            filepath.Join(workspaceDir, ".cursor", "rules", safeFileName(instructionKey(ref))+".mdc"),
		Support:         engine.ProfileResourceSupportPortableCore,
		Materialization: engine.ProfileResourceMaterializationNativeManaged,
		Native:          true,
	}, true
}

func syncProjectTarget(ctx context.Context, target target, ref *driver.InstructionsBundleRef) (engine.ResourceSnapshot, string, error) {
	root := filepath.Dir(target.Path)
	lock, err := profilestate.AcquireLock(ctx, root, profilestate.LockOptions{StaleAfter: 10 * time.Minute})
	if err != nil {
		return engine.ResourceSnapshot{}, "", err
	}
	defer lock.Release()

	manifest, err := profilestate.LoadManifest(root)
	if err != nil {
		return engine.ResourceSnapshot{}, "", err
	}
	content, _, err := instructionContent(ref)
	if err != nil {
		return engine.ResourceSnapshot{}, "", err
	}
	entry := profilereconcile.DirectoryEntry{
		Key:         instructionKey(ref),
		RuntimeName: filepath.Base(target.Path),
		Content:     renderCursorMDC(ref, content),
		Fingerprint: fingerprint(ref),
		Metadata: map[string]string{
			"provider":        "cursor",
			"scope":           string(ref.Scope),
			"mode":            string(ref.Mode),
			"support":         string(target.Support),
			"materialization": string(target.Materialization),
			"native":          "true",
		},
	}
	if _, err := profilereconcile.ReconcileDirectory(profilereconcile.DirectoryOptions{
		Root:       root,
		Kind:       resourceKind,
		Entries:    []profilereconcile.DirectoryEntry{entry},
		Manifest:   &manifest,
		AllowPrune: true,
	}); err != nil {
		return engine.ResourceSnapshot{}, "", err
	}
	if err := profilestate.SaveManifest(root, manifest); err != nil {
		return engine.ResourceSnapshot{}, "", err
	}
	snapshot := engine.ResourceSnapshot{
		Kind:            engine.ProfileResourceInstructions,
		Fingerprint:     fingerprint(ref),
		Managed:         []string{instructionKey(ref)},
		Support:         target.Support,
		Materialization: target.Materialization,
	}
	return snapshot, target.Path, nil
}

func pruneProjectTargets(ctx context.Context, driverType, workspaceDir string) error {
	if driverType != "cursor" || strings.TrimSpace(workspaceDir) == "" {
		return nil
	}
	root := filepath.Join(workspaceDir, ".cursor", "rules")
	if _, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	lock, err := profilestate.AcquireLock(ctx, root, profilestate.LockOptions{StaleAfter: 10 * time.Minute})
	if err != nil {
		return err
	}
	defer lock.Release()
	manifest, err := profilestate.LoadManifest(root)
	if err != nil {
		return err
	}
	if err := pruneProfileInstructionEntries(root, &manifest, ""); err != nil {
		return err
	}
	return profilestate.SaveManifest(root, manifest)
}

func renderCursorMDC(ref *driver.InstructionsBundleRef, content string) string {
	description := "Profile instructions managed by agent-adaptor"
	if id := strings.TrimSpace(ref.ID); id != "" {
		description = "Profile instructions " + id + " managed by agent-adaptor"
	}
	return fmt.Sprintf("---\ndescription: %s\nglobs:\nalwaysApply: true\n---\n\n%s\n", description, strings.TrimSpace(content))
}

func instructionContent(ref *driver.InstructionsBundleRef) (string, string, error) {
	if ref == nil {
		return "", "", nil
	}
	if strings.TrimSpace(ref.Content) != "" {
		return ref.Content, "", nil
	}
	path := strings.TrimSpace(ref.Path)
	if path == "" {
		return "", "", fmt.Errorf("instructions require path or content")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", "", err
	}
	if info.IsDir() {
		return "", "", fmt.Errorf("instructions path %s is a directory", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	return string(raw), path, nil
}

func ensureManagedTargetAvailable(key, path string, manifest *profilestate.Manifest) error {
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if entry, ok := manifest.Entry(resourceKind, key); ok && filepath.Clean(entry.Path) == filepath.Clean(path) {
		return nil
	}
	return fmt.Errorf("instructions resource %q target %s is occupied by an external entry", key, path)
}

func pruneProfileInstructionEntries(profileDir string, manifest *profilestate.Manifest, keepPath string) error {
	for _, entry := range manifest.KindEntries(resourceKind) {
		if keepPath != "" && filepath.Clean(entry.Path) == filepath.Clean(keepPath) {
			continue
		}
		if err := removeProfileInstructionPath(profileDir, entry.Path); err != nil {
			return err
		}
		manifest.Remove(resourceKind, entry.Key)
	}
	return nil
}

func removeProfileInstructionPath(profileDir, path string) error {
	if strings.TrimSpace(path) == "" || !pathWithin(profileDir, path) {
		return nil
	}
	return os.RemoveAll(path)
}

func pathWithin(root, path string) bool {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if path == root {
		return true
	}
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func instructionKey(ref *driver.InstructionsBundleRef) string {
	if ref == nil {
		return "instructions"
	}
	for _, value := range []string{ref.ID, ref.Fingerprint, filepath.Base(ref.Path)} {
		value = strings.TrimSpace(value)
		if value != "" && value != "." {
			return value
		}
	}
	if strings.TrimSpace(ref.Content) != "" {
		return "inline-instructions"
	}
	return "instructions"
}

func safeFileName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	builder := strings.Builder{}
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			builder.WriteRune(r)
		case r == '-', r == '_', r == '.':
			builder.WriteRune(r)
		default:
			builder.WriteRune('-')
		}
	}
	out := strings.Trim(builder.String(), "-.")
	if out == "" {
		return "instructions"
	}
	return out
}

func fingerprint(ref *driver.InstructionsBundleRef) string {
	if ref == nil {
		return ""
	}
	if strings.TrimSpace(ref.Fingerprint) != "" {
		return strings.TrimSpace(ref.Fingerprint)
	}
	content := ref.Content
	if strings.TrimSpace(ref.Path) != "" && strings.TrimSpace(content) == "" {
		if raw, err := os.ReadFile(strings.TrimSpace(ref.Path)); err == nil {
			content = string(raw)
		}
	}
	raw, err := json.Marshal([]any{ref.ID, ref.Path, content, ref.Scope, ref.Mode, ref.Native})
	if err != nil {
		raw = []byte(ref.ID + ref.Path + content)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func ManagedKeysForTest(refs ...*driver.InstructionsBundleRef) []string {
	keys := make([]string, 0, len(refs))
	for _, ref := range refs {
		keys = append(keys, instructionKey(ref))
	}
	sort.Strings(keys)
	return keys
}
