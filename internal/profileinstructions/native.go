package profileinstructions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/profilekind"
	"github.com/agent-dance/agent-adaptor/internal/profilestate"
)

// NativeFile is a Driver-selected, single-file instruction delivery plan. The
// Driver owns native filename, scope and replacement semantics; this helper
// owns only file reconciliation and truthful ownership observations.
type NativeFile struct {
	Name, Key, Content, Fingerprint string
}

func PlanNativeFile(ref *driver.InstructionsBundleRef, name string) (*NativeFile, error) {
	if ref == nil {
		return nil, nil
	}
	if ref.Path != "" && ref.Content != "" {
		return nil, errors.New("instructions cannot set both path and content")
	}
	content, _, err := instructionContent(ref)
	if err != nil {
		return nil, err
	}
	key := strings.TrimSpace(ref.ID)
	if key == "" {
		key = "instructions"
	}
	return &NativeFile{Name: name, Key: key, Content: content, Fingerprint: fingerprint(ref)}, nil
}

type nativeObservation struct {
	path, name      string
	raw             []byte
	exists, regular bool
	mode            os.FileMode
	owner           *profilestate.ManifestEntry
}

// ObserveNativeFiles never initializes a profile. Ownership requires both the
// manifest and the exact bytes last written by this implementation. An external
// edit or symlink is visible as external, not an SDK-managed success.
func ObserveNativeFiles(provider, profileDir string, names []string, desired *NativeFile) (driver.ResourceSnapshot, error) {
	manifest, err := profilestate.LoadManifest(profileDir)
	if err != nil {
		return driver.ResourceSnapshot{}, err
	}
	observations, err := observeNativeFiles(provider, profileDir, names, manifest)
	if err != nil {
		return driver.ResourceSnapshot{}, err
	}
	out := driver.ResourceSnapshot{Kind: driver.ProfileResourceInstructions, Support: driver.ProfileResourceSupportPortableCore, Materialization: driver.ProfileResourceMaterializationNotMaterialized}
	matched := desired == nil
	if desired != nil {
		out.Fingerprint = desired.Fingerprint
	}
	for _, file := range observations {
		if !file.exists {
			continue
		}
		if file.owner != nil {
			out.Managed = append(out.Managed, file.owner.Key)
			out.Materialization = driver.ProfileResourceMaterializationNativeManaged
		} else {
			out.External = append(out.External, file.name)
		}
		if desired != nil && file.name == desired.Name && file.owner != nil && file.owner.Key == desired.Key && string(file.raw) == desired.Content {
			matched = true
		}
	}
	if !matched {
		out.Warnings = append(out.Warnings, "Desired instructions are not installed with matching SDK ownership and content.")
	}
	sort.Strings(out.Managed)
	sort.Strings(out.External)
	return out, nil
}

// SyncNativeFiles serializes changes with other profile resource writers. It
// preflights all ownership before any file replacement or removal. A nil plan
// explicitly clears owned files. External files and externally edited formerly
// owned files are never overwritten or removed.
func SyncNativeFiles(ctx context.Context, provider, profileDir string, names []string, desired *NativeFile) (snapshot driver.ResourceSnapshot, err error) {
	if strings.TrimSpace(profileDir) == "" {
		return snapshot, errors.New("native instructions require a profile directory")
	}
	if err := validateNativeNames(names, desired); err != nil {
		return snapshot, err
	}
	if err := ctx.Err(); err != nil {
		return snapshot, err
	}
	lock, err := profilestate.AcquireLock(ctx, profileDir, profilestate.LockOptions{StaleAfter: 10 * time.Minute})
	if err != nil {
		return snapshot, err
	}
	defer func() { err = errors.Join(err, lock.Release()) }()
	manifest, err := profilestate.LoadManifest(profileDir)
	if err != nil {
		return snapshot, err
	}
	files, err := observeNativeFiles(provider, profileDir, names, manifest)
	if err != nil {
		return snapshot, err
	}
	for _, entry := range manifest.KindEntries(resourceKind) {
		if entry.Metadata["provider"] != provider {
			continue
		}
		known := false
		for _, file := range files {
			if !profilekind.SamePath(entry.Path, file.path) {
				continue
			}
			known = true
			if file.exists && file.owner == nil {
				return snapshot, fmt.Errorf("%s: instructions file %s was externally modified; refusing to replace or remove it", provider, file.path)
			}
		}
		if !known {
			return snapshot, fmt.Errorf("%s: instruction manifest path is outside native instruction files", provider)
		}
	}
	for _, file := range files {
		if desired != nil && desired.Name == file.name && file.exists && file.owner == nil {
			return snapshot, fmt.Errorf("%s: instructions target %s is occupied by an external entry", provider, file.path)
		}
	}
	if err := ctx.Err(); err != nil {
		return snapshot, err
	}
	// The manifest is the commit point. Restore files if a later write or
	// removal fails, without overwriting a concurrent external replacement.
	var written *nativeObservation
	var removed []nativeObservation
	committed := false
	defer func() {
		if err == nil || committed {
			return
		}
		if written != nil {
			info, statErr := os.Lstat(written.path)
			var current []byte
			readErr := statErr
			if statErr == nil && info.Mode().IsRegular() {
				current, readErr = os.ReadFile(written.path)
			}
			if readErr == nil && statErr == nil && info.Mode().IsRegular() && string(current) == desired.Content {
				if written.exists {
					err = errors.Join(err, profilestate.AtomicWriteFile(written.path, written.raw, written.mode))
				} else {
					err = errors.Join(err, os.Remove(written.path))
				}
			} else {
				err = errors.Join(err, fmt.Errorf("instructions rollback: written file %s changed externally", written.path))
			}
		}
		for _, file := range removed {
			if _, statErr := os.Lstat(file.path); os.IsNotExist(statErr) {
				err = errors.Join(err, profilestate.AtomicWriteFile(file.path, file.raw, file.mode))
			} else {
				err = errors.Join(err, fmt.Errorf("instructions rollback: removed file %s changed externally", file.path))
			}
		}
	}()
	if desired != nil {
		path := filepath.Join(profileDir, desired.Name)
		if err := profilestate.AtomicWriteFile(path, []byte(desired.Content), 0600); err != nil {
			return snapshot, err
		}
		for _, file := range files {
			if file.name == desired.Name {
				written = &file
				break
			}
		}
	}
	for _, file := range files {
		if file.owner == nil || (desired != nil && desired.Name == file.name) {
			continue
		}
		if err := os.Remove(file.path); err != nil && !os.IsNotExist(err) {
			return snapshot, err
		}
		removed = append(removed, file)
	}
	for _, entry := range manifest.KindEntries(resourceKind) {
		if entry.Metadata["provider"] == provider {
			manifest.Remove(resourceKind, entry.Key)
		}
	}
	if desired != nil {
		manifest.Set(profilestate.ManifestEntry{Kind: resourceKind, Key: desired.Key, Path: filepath.Join(profileDir, desired.Name), Fingerprint: desired.Fingerprint, Metadata: map[string]string{"provider": provider, "content_sha256": nativeContentHash([]byte(desired.Content))}})
	}
	if err := profilestate.SaveManifest(profileDir, manifest); err != nil {
		return snapshot, err
	}
	committed = true
	return ObserveNativeFiles(provider, profileDir, names, desired)
}

func observeNativeFiles(provider, profileDir string, names []string, manifest profilestate.Manifest) ([]nativeObservation, error) {
	if err := validateNativeNames(names, nil); err != nil {
		return nil, err
	}
	var out []nativeObservation
	for _, name := range names {
		file := nativeObservation{path: filepath.Join(profileDir, name), name: name}
		info, err := os.Lstat(file.path)
		if os.IsNotExist(err) {
			out = append(out, file)
			continue
		}
		if err != nil {
			return nil, err
		}
		file.exists, file.regular, file.mode = true, info.Mode().IsRegular(), info.Mode().Perm()
		if file.regular {
			file.raw, err = os.ReadFile(file.path)
			if err != nil {
				return nil, err
			}
			for _, entry := range manifest.KindEntries(resourceKind) {
				if entry.Metadata["provider"] == provider && profilekind.SamePath(entry.Path, file.path) && entry.Metadata["content_sha256"] == nativeContentHash(file.raw) {
					file.owner = &entry
					break
				}
			}
		}
		out = append(out, file)
	}
	return out, nil
}

func validateNativeNames(names []string, desired *NativeFile) error {
	found := desired == nil
	seen := map[string]bool{}
	for _, name := range names {
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\:`) || filepath.Base(name) != name || seen[name] {
			return errors.New("instructions require unique native filenames directly inside the profile")
		}
		seen[name] = true
		if desired != nil && desired.Name == name {
			found = true
		}
	}
	if !found {
		return errors.New("instruction plan does not name an allowed native file")
	}
	return nil
}

func nativeContentHash(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
