package profilestate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	ManifestName = ".agent-adaptor-profile-manifest.json"
	LockName     = ".agent-adaptor-profile.lock"
)

type Manifest struct {
	Version   int                      `json:"version"`
	UpdatedAt time.Time                `json:"updated_at"`
	Entries   map[string]ManifestEntry `json:"entries,omitempty"`
}

type ManifestEntry struct {
	Kind        string            `json:"kind"`
	Key         string            `json:"key"`
	Path        string            `json:"path"`
	Fingerprint string            `json:"fingerprint,omitempty"`
	SourcePath  string            `json:"source_path,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

func EntryID(kind, key string) string {
	return strings.TrimSpace(kind) + ":" + strings.TrimSpace(key)
}

func LoadManifest(root string) (Manifest, error) {
	path := filepath.Join(root, ManifestName)
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return newManifest(), nil
	}
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("read profile manifest %s: %w", path, err)
	}
	manifest.ensure()
	return manifest, nil
}

func SaveManifest(root string, manifest Manifest) error {
	manifest.ensure()
	manifest.UpdatedAt = time.Now().UTC()
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return AtomicWriteFile(filepath.Join(root, ManifestName), raw, 0o644)
}

func (m *Manifest) Set(entry ManifestEntry) {
	m.ensure()
	entry.Kind = strings.TrimSpace(entry.Kind)
	entry.Key = strings.TrimSpace(entry.Key)
	entry.Path = filepath.Clean(strings.TrimSpace(entry.Path))
	m.Entries[EntryID(entry.Kind, entry.Key)] = entry
}

func (m *Manifest) Remove(kind, key string) {
	m.ensure()
	delete(m.Entries, EntryID(kind, key))
}

func (m *Manifest) KindEntries(kind string) []ManifestEntry {
	m.ensure()
	kind = strings.TrimSpace(kind)
	out := make([]ManifestEntry, 0)
	for _, entry := range m.Entries {
		if entry.Kind == kind {
			out = append(out, entry)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func (m *Manifest) Entry(kind, key string) (ManifestEntry, bool) {
	m.ensure()
	entry, ok := m.Entries[EntryID(kind, key)]
	return entry, ok
}

func (m *Manifest) ensure() {
	if m.Version == 0 {
		m.Version = 1
	}
	if m.Entries == nil {
		m.Entries = map[string]ManifestEntry{}
	}
}

func newManifest() Manifest {
	manifest := Manifest{Version: 1, UpdatedAt: time.Now().UTC(), Entries: map[string]ManifestEntry{}}
	return manifest
}

type LockOptions struct {
	RetryInterval time.Duration
	StaleAfter    time.Duration
}

type Lock struct {
	path string
	file *os.File
}

func AcquireLock(ctx context.Context, root string, opts LockOptions) (*Lock, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	retry := opts.RetryInterval
	if retry <= 0 {
		retry = 25 * time.Millisecond
	}
	path := filepath.Join(root, LockName)
	for {
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			if writeErr := writeLockInfo(file); writeErr != nil {
				_ = file.Close()
				_ = os.Remove(path)
				return nil, writeErr
			}
			return &Lock{path: path, file: file}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if opts.StaleAfter > 0 {
			_ = removeStaleLock(path, opts.StaleAfter)
		}
		timer := time.NewTimer(retry)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, fmt.Errorf("acquire profile lock %s: %w", path, ctx.Err())
		case <-timer.C:
		}
	}
}

func (l *Lock) Release() error {
	if l == nil {
		return nil
	}
	var err error
	if l.file != nil {
		err = l.file.Close()
		l.file = nil
	}
	if removeErr := os.Remove(l.path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) && err == nil {
		err = removeErr
	}
	return err
}

func writeLockInfo(file *os.File) error {
	host, _ := os.Hostname()
	info := map[string]any{
		"pid":         os.Getpid(),
		"host":        host,
		"acquired_at": time.Now().UTC().Format(time.RFC3339Nano),
	}
	raw, err := json.Marshal(info)
	if err != nil {
		return err
	}
	if _, err := file.Write(append(raw, '\n')); err != nil {
		return err
	}
	return file.Sync()
}

func removeStaleLock(path string, staleAfter time.Duration) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if time.Since(info.ModTime()) < staleAfter {
		return nil
	}
	return os.Remove(path)
}

func AtomicWriteFile(path string, data []byte, perm fs.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	cleanup = false
	syncDir(dir)
	return nil
}

func syncDir(dir string) {
	handle, err := os.Open(dir)
	if err != nil {
		return
	}
	defer handle.Close()
	_ = handle.Sync()
}
