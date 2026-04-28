package profilereconcile

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/agent-dance/agent-adaptor/internal/profilestate"
)

type DirectoryEntry struct {
	Key         string
	RuntimeName string
	SourcePath  string
	Content     string
	Fingerprint string
	Mode        fs.FileMode
	Metadata    map[string]string
}

type DirectoryOptions struct {
	Root       string
	Kind       string
	Entries    []DirectoryEntry
	Manifest   *profilestate.Manifest
	AllowPrune bool
}

type DirectorySnapshot struct {
	Managed  []string
	External []string
}

func ReconcileDirectory(opts DirectoryOptions) (DirectorySnapshot, error) {
	root := filepath.Clean(strings.TrimSpace(opts.Root))
	kind := strings.TrimSpace(opts.Kind)
	if root == "." || root == "" {
		return DirectorySnapshot{}, fmt.Errorf("profile directory reconciler requires root")
	}
	if kind == "" {
		return DirectorySnapshot{}, fmt.Errorf("profile directory reconciler requires kind")
	}
	if opts.Manifest == nil {
		return DirectorySnapshot{}, fmt.Errorf("profile directory reconciler requires manifest")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return DirectorySnapshot{}, err
	}

	desired := map[string]DirectoryEntry{}
	desiredNames := map[string]string{}
	for _, entry := range opts.Entries {
		normalized, err := normalizeDirectoryEntry(entry)
		if err != nil {
			return DirectorySnapshot{}, err
		}
		if _, exists := desired[normalized.Key]; exists {
			return DirectorySnapshot{}, fmt.Errorf("%s resource %q is declared more than once", kind, normalized.Key)
		}
		if owner, exists := desiredNames[normalized.RuntimeName]; exists {
			return DirectorySnapshot{}, fmt.Errorf("%s resource runtime name %q is shared by %q and %q", kind, normalized.RuntimeName, owner, normalized.Key)
		}
		desired[normalized.Key] = normalized
		desiredNames[normalized.RuntimeName] = normalized.Key
	}
	previous := map[string]profilestate.ManifestEntry{}
	for _, entry := range opts.Manifest.KindEntries(kind) {
		previous[entry.Key] = entry
	}
	desiredPaths := map[string]struct{}{}
	for _, entry := range desired {
		desiredPaths[filepath.Clean(filepath.Join(root, entry.RuntimeName))] = struct{}{}
	}

	for key, entry := range desired {
		target := filepath.Join(root, entry.RuntimeName)
		if err := ensureManagedTargetAvailable(kind, key, target, opts.Manifest); err != nil {
			return DirectorySnapshot{}, err
		}
		if err := materializeDirectoryEntry(target, entry); err != nil {
			return DirectorySnapshot{}, fmt.Errorf("materialize %s resource %q: %w", kind, key, err)
		}
		opts.Manifest.Set(profilestate.ManifestEntry{
			Kind:        kind,
			Key:         key,
			Path:        target,
			Fingerprint: directoryEntryFingerprint(entry),
			SourcePath:  entry.SourcePath,
			Metadata:    cloneStringMap(entry.Metadata),
		})
		if oldEntry, ok := previous[key]; ok && filepath.Clean(oldEntry.Path) != filepath.Clean(target) {
			if _, keep := desiredPaths[filepath.Clean(oldEntry.Path)]; !keep {
				if err := removeManagedPath(root, kind, key, oldEntry.Path); err != nil {
					return DirectorySnapshot{}, err
				}
			}
		}
	}

	if opts.AllowPrune {
		if err := pruneStaleDirectoryEntries(root, kind, desired, opts.Manifest); err != nil {
			return DirectorySnapshot{}, err
		}
	}
	return snapshotDirectory(root, kind, opts.Manifest)
}

func normalizeDirectoryEntry(entry DirectoryEntry) (DirectoryEntry, error) {
	entry.Key = strings.TrimSpace(entry.Key)
	entry.RuntimeName = strings.TrimSpace(entry.RuntimeName)
	entry.SourcePath = strings.TrimSpace(entry.SourcePath)
	entry.Fingerprint = strings.TrimSpace(entry.Fingerprint)
	if entry.Key == "" {
		return DirectoryEntry{}, fmt.Errorf("profile directory resource key is required")
	}
	if entry.RuntimeName == "" {
		entry.RuntimeName = entry.Key
	}
	if !safeRuntimeName(entry.RuntimeName) {
		return DirectoryEntry{}, fmt.Errorf("profile directory resource %q has unsafe runtime name %q", entry.Key, entry.RuntimeName)
	}
	if entry.SourcePath != "" && entry.Content != "" {
		return DirectoryEntry{}, fmt.Errorf("profile directory resource %q cannot set both source path and content", entry.Key)
	}
	if entry.SourcePath == "" && entry.Content == "" {
		return DirectoryEntry{}, fmt.Errorf("profile directory resource %q requires source path or content", entry.Key)
	}
	if entry.Mode == 0 {
		entry.Mode = 0o644
	}
	return entry, nil
}

func safeRuntimeName(value string) bool {
	if value == "" || value == "." || value == ".." {
		return false
	}
	if strings.ContainsAny(value, `/\`) {
		return false
	}
	return filepath.Base(value) == value
}

func ensureManagedTargetAvailable(kind, key, target string, manifest *profilestate.Manifest) error {
	if _, err := os.Lstat(target); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if entry, ok := manifest.Entry(kind, key); ok && filepath.Clean(entry.Path) == filepath.Clean(target) {
		return nil
	}
	return fmt.Errorf("%s resource %q target %s is occupied by an external entry", kind, key, target)
}

func materializeDirectoryEntry(target string, entry DirectoryEntry) error {
	if entry.Content != "" {
		return profilestate.AtomicWriteFile(target, []byte(entry.Content), entry.Mode)
	}
	info, err := os.Stat(entry.SourcePath)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return copyDirectoryAtomically(entry.SourcePath, target)
	}
	return copyFileAtomically(entry.SourcePath, target, entry.Mode)
}

func copyFileAtomically(source, target string, mode fs.FileMode) error {
	raw, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	if mode == 0 {
		if info, statErr := os.Stat(source); statErr == nil {
			mode = info.Mode().Perm()
		} else {
			mode = 0o644
		}
	}
	return profilestate.AtomicWriteFile(target, raw, mode)
}

func copyDirectoryAtomically(source, target string) error {
	parent := filepath.Dir(target)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(parent, "."+filepath.Base(target)+".tmp-*")
	if err != nil {
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(tmp)
		}
	}()
	if err := copyTree(source, tmp); err != nil {
		return err
	}
	if err := os.RemoveAll(target); err != nil {
		return err
	}
	if err := os.Rename(tmp, target); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func copyTree(source, target string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		out := filepath.Join(target, rel)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(out, info.Mode().Perm())
		}
		if info.Mode()&os.ModeType != 0 {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		dst, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
		if err != nil {
			_ = in.Close()
			return err
		}
		_, copyErr := io.Copy(dst, in)
		closeDstErr := dst.Close()
		closeInErr := in.Close()
		switch {
		case copyErr != nil:
			return copyErr
		case closeDstErr != nil:
			return closeDstErr
		default:
			return closeInErr
		}
	})
}

func pruneStaleDirectoryEntries(root, kind string, desired map[string]DirectoryEntry, manifest *profilestate.Manifest) error {
	for _, entry := range manifest.KindEntries(kind) {
		if _, keep := desired[entry.Key]; keep {
			continue
		}
		if err := removeManagedPath(root, kind, entry.Key, entry.Path); err != nil {
			return err
		}
		manifest.Remove(kind, entry.Key)
	}
	return nil
}

func removeManagedPath(root, kind, key, path string) error {
	if strings.TrimSpace(path) == "" || !pathWithin(root, path) {
		return fmt.Errorf("refusing to prune %s resource %q outside root: %s", kind, key, path)
	}
	return os.RemoveAll(path)
}

func snapshotDirectory(root, kind string, manifest *profilestate.Manifest) (DirectorySnapshot, error) {
	managedByPath := map[string]string{}
	for _, entry := range manifest.KindEntries(kind) {
		if pathWithin(root, entry.Path) {
			managedByPath[filepath.Clean(entry.Path)] = entry.Key
		}
	}
	out := DirectorySnapshot{}
	for _, key := range managedByPath {
		out.Managed = append(out.Managed, key)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return DirectorySnapshot{}, err
	}
	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		if _, managed := managedByPath[filepath.Clean(path)]; managed {
			continue
		}
		out.External = append(out.External, entry.Name())
	}
	sort.Strings(out.Managed)
	sort.Strings(out.External)
	return out, nil
}

func pathWithin(root, path string) bool {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if path == root {
		return true
	}
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != "." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".."
}

func directoryEntryFingerprint(entry DirectoryEntry) string {
	if entry.Fingerprint != "" {
		return entry.Fingerprint
	}
	raw, err := json.Marshal([]any{entry.Key, entry.RuntimeName, entry.SourcePath, entry.Content, entry.Metadata})
	if err != nil {
		raw = []byte(entry.Key + entry.RuntimeName + entry.SourcePath + entry.Content)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}
