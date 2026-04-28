package skillruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/internal/profilestate"
)

const (
	profileSkillManifestKind  = "skills"
	manifestSourceHashKey     = "source_hash"
	defaultProfileLockTimeout = 10 * time.Minute
)

type ProfileSkillConflictMode string

const (
	ProfileSkillConflictPreserve ProfileSkillConflictMode = "preserve"
	ProfileSkillConflictError    ProfileSkillConflictMode = "error"
)

type ProfileSkillPruneMode string

const (
	ProfileSkillPruneNone          ProfileSkillPruneMode = "none"
	ProfileSkillPruneBrokenManaged ProfileSkillPruneMode = "broken_managed"
	ProfileSkillPruneManaged       ProfileSkillPruneMode = "managed"
)

type ProfileSkillChange struct {
	Key         string
	RuntimeName string
	Action      string
}

type ProfileSkillReconcileOptions struct {
	ProfileDir              string
	SkillsHome              string
	Payload                 agentadaptor.ResolvedSkills
	Selected                []string
	ManagedRoots            []string
	ConflictMode            ProfileSkillConflictMode
	PruneMode               ProfileSkillPruneMode
	PruneMatchingUnselected bool
}

type ProfileSkillReconcileResult struct {
	Changes []ProfileSkillChange
}

func ReconcileProfileSkills(ctx context.Context, opts ProfileSkillReconcileOptions) (ProfileSkillReconcileResult, error) {
	profileDir := filepath.Clean(strings.TrimSpace(opts.ProfileDir))
	if profileDir == "." || profileDir == "" {
		return ProfileSkillReconcileResult{}, fmt.Errorf("profile skill reconciler requires profile directory")
	}
	skillsHome := filepath.Clean(strings.TrimSpace(opts.SkillsHome))
	if skillsHome == "." || skillsHome == "" {
		return ProfileSkillReconcileResult{}, fmt.Errorf("profile skill reconciler requires skills home")
	}
	if err := os.MkdirAll(skillsHome, 0o755); err != nil {
		return ProfileSkillReconcileResult{}, err
	}

	lock, err := profilestate.AcquireLock(ctx, profileDir, profilestate.LockOptions{StaleAfter: defaultProfileLockTimeout})
	if err != nil {
		return ProfileSkillReconcileResult{}, err
	}
	defer lock.Release()

	manifest, err := profilestate.LoadManifest(profileDir)
	if err != nil {
		return ProfileSkillReconcileResult{}, err
	}
	installed, err := ReadInstalledSkillTargets(skillsHome)
	if err != nil {
		return ProfileSkillReconcileResult{}, err
	}
	desired, err := desiredProfileSkillEntries(opts.Payload, opts.Selected)
	if err != nil {
		return ProfileSkillReconcileResult{}, err
	}
	selectedKeys := selectedKeySet(opts.Selected)
	previousByKey := map[string]profilestate.ManifestEntry{}
	for _, entry := range manifest.KindEntries(profileSkillManifestKind) {
		previousByKey[entry.Key] = entry
	}

	result := ProfileSkillReconcileResult{Changes: make([]ProfileSkillChange, 0)}
	for _, entry := range desired {
		target := filepath.Join(skillsHome, entry.RuntimeName)
		if strings.TrimSpace(entry.SourcePath) == "" {
			continue
		}
		installedEntry, hasInstalled := installed[entry.RuntimeName]
		if hasInstalled && !profileSkillTargetCanBeManaged(target, installedEntry, entry.SourcePath, opts.ManagedRoots) {
			if prior, ok := previousByKey[entry.Key]; ok && filepath.Clean(prior.Path) == filepath.Clean(target) {
				manifest.Remove(profileSkillManifestKind, entry.Key)
			}
			if opts.ConflictMode == ProfileSkillConflictError {
				occupiedBy := strings.TrimSpace(installedEntry.TargetPath)
				if occupiedBy == "" {
					occupiedBy = target
				}
				return ProfileSkillReconcileResult{}, fmt.Errorf("materialize skill %q: runtime name %q is occupied by external installation %q", entry.Key, entry.RuntimeName, occupiedBy)
			}
			continue
		}
		change, err := EnsureSkillTarget(entry.SourcePath, target, opts.ManagedRoots)
		if err != nil {
			return ProfileSkillReconcileResult{}, fmt.Errorf("materialize skill %q: %w", entry.Key, err)
		}
		manifest.Set(profileSkillManifestEntry(entry, target))
		if change != "skipped" {
			result.Changes = append(result.Changes, ProfileSkillChange{Key: entry.Key, RuntimeName: entry.RuntimeName, Action: change})
		}
		if prior, ok := previousByKey[entry.Key]; ok && filepath.Clean(prior.Path) != filepath.Clean(target) {
			removed, removeErr := pruneManagedProfileSkillPath(skillsHome, prior, installed, opts.ManagedRoots)
			if removeErr != nil {
				return ProfileSkillReconcileResult{}, removeErr
			}
			if removed {
				result.Changes = append(result.Changes, ProfileSkillChange{Key: entry.Key, RuntimeName: runtimeNameFromPath(prior.Path), Action: "removed"})
			}
		}
	}

	for _, manifestEntry := range manifest.KindEntries(profileSkillManifestKind) {
		if _, keep := desired[manifestEntry.Key]; keep {
			continue
		}
		switch opts.PruneMode {
		case ProfileSkillPruneManaged:
			removed, removeErr := pruneManagedProfileSkillPath(skillsHome, manifestEntry, installed, opts.ManagedRoots)
			if removeErr != nil {
				return ProfileSkillReconcileResult{}, removeErr
			}
			manifest.Remove(profileSkillManifestKind, manifestEntry.Key)
			if removed {
				result.Changes = append(result.Changes, ProfileSkillChange{Key: manifestEntry.Key, RuntimeName: runtimeNameFromPath(manifestEntry.Path), Action: "removed"})
			}
		case ProfileSkillPruneBrokenManaged:
			removed, stillManaged, removeErr := pruneBrokenManagedProfileSkillPath(skillsHome, manifestEntry, installed, opts.ManagedRoots)
			if removeErr != nil {
				return ProfileSkillReconcileResult{}, removeErr
			}
			if removed {
				manifest.Remove(profileSkillManifestKind, manifestEntry.Key)
				result.Changes = append(result.Changes, ProfileSkillChange{Key: manifestEntry.Key, RuntimeName: runtimeNameFromPath(manifestEntry.Path), Action: "removed"})
				continue
			}
			if !stillManaged {
				manifest.Remove(profileSkillManifestKind, manifestEntry.Key)
			}
		case ProfileSkillPruneNone:
		default:
			return ProfileSkillReconcileResult{}, fmt.Errorf("unsupported profile skill prune mode %q", opts.PruneMode)
		}
	}
	if opts.PruneMatchingUnselected {
		for _, entry := range opts.Payload.Entries {
			if _, selected := selectedKeys[strings.TrimSpace(entry.Key)]; selected {
				continue
			}
			target := filepath.Join(skillsHome, strings.TrimSpace(entry.RuntimeName))
			installedEntry, ok := installed[strings.TrimSpace(entry.RuntimeName)]
			if !ok {
				continue
			}
			if normalizePath(installedEntry.TargetPath) != normalizePath(entry.SourcePath) {
				continue
			}
			if err := os.RemoveAll(target); err != nil && !os.IsNotExist(err) {
				return ProfileSkillReconcileResult{}, fmt.Errorf("prune stale skill %q: %w", entry.Key, err)
			}
			manifest.Remove(profileSkillManifestKind, strings.TrimSpace(entry.Key))
			result.Changes = append(result.Changes, ProfileSkillChange{Key: strings.TrimSpace(entry.Key), RuntimeName: strings.TrimSpace(entry.RuntimeName), Action: "removed"})
		}
	}
	sort.Slice(result.Changes, func(i, j int) bool {
		if result.Changes[i].RuntimeName == result.Changes[j].RuntimeName {
			if result.Changes[i].Action == result.Changes[j].Action {
				return result.Changes[i].Key < result.Changes[j].Key
			}
			return result.Changes[i].Action < result.Changes[j].Action
		}
		return result.Changes[i].RuntimeName < result.Changes[j].RuntimeName
	})
	if err := profilestate.SaveManifest(profileDir, manifest); err != nil {
		return ProfileSkillReconcileResult{}, err
	}
	return result, nil
}

func desiredProfileSkillEntries(payload agentadaptor.ResolvedSkills, selected []string) (map[string]agentadaptor.ResolvedSkill, error) {
	desiredKeys := selectedKeySet(selected)
	filterSelected := selected != nil
	desired := make(map[string]agentadaptor.ResolvedSkill, len(payload.Entries))
	runtimeOwners := map[string]string{}
	for _, entry := range payload.Entries {
		entry.Key = strings.TrimSpace(entry.Key)
		entry.RuntimeName = strings.TrimSpace(entry.RuntimeName)
		entry.SourcePath = strings.TrimSpace(entry.SourcePath)
		if entry.Key == "" {
			return nil, fmt.Errorf("profile skill reconciler requires skill key")
		}
		if filterSelected {
			if _, keep := desiredKeys[entry.Key]; !keep {
				continue
			}
		}
		if entry.RuntimeName == "" {
			return nil, fmt.Errorf("profile skill %q requires runtime name", entry.Key)
		}
		if filepath.Base(entry.RuntimeName) != entry.RuntimeName || strings.ContainsAny(entry.RuntimeName, `/\`) {
			return nil, fmt.Errorf("profile skill %q has unsafe runtime name %q", entry.Key, entry.RuntimeName)
		}
		if owner, exists := runtimeOwners[entry.RuntimeName]; exists && owner != entry.Key {
			return nil, fmt.Errorf("profile skill runtime name %q is shared by %q and %q", entry.RuntimeName, owner, entry.Key)
		}
		runtimeOwners[entry.RuntimeName] = entry.Key
		desired[entry.Key] = entry
	}
	return desired, nil
}

func selectedKeySet(selected []string) map[string]struct{} {
	out := make(map[string]struct{}, len(selected))
	for _, key := range selected {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		out[key] = struct{}{}
	}
	return out
}

func profileSkillTargetCanBeManaged(target string, installed InstalledSkillTarget, desiredSource string, managedRoots []string) bool {
	target = filepath.Clean(target)
	if strings.TrimSpace(installed.TargetPath) == "" {
		return false
	}
	if normalizePath(installed.TargetPath) == normalizePath(desiredSource) {
		return true
	}
	return installedSkillTargetWithinRoots(installed, managedRoots)
}

func pruneManagedProfileSkillPath(skillsHome string, entry profilestate.ManifestEntry, installed map[string]InstalledSkillTarget, managedRoots []string) (bool, error) {
	if strings.TrimSpace(entry.Path) == "" {
		return false, nil
	}
	if !pathWithinRoot(skillsHome, entry.Path) {
		return false, fmt.Errorf("refusing to prune managed skill outside skills home: %s", entry.Path)
	}
	runtimeName := runtimeNameFromPath(entry.Path)
	installedEntry, ok := installed[runtimeName]
	if ok && !installedSkillMatchesManifest(installedEntry, entry, managedRoots) {
		return false, nil
	}
	if err := os.RemoveAll(entry.Path); err != nil && !os.IsNotExist(err) {
		return false, err
	}
	return true, nil
}

func pruneBrokenManagedProfileSkillPath(skillsHome string, entry profilestate.ManifestEntry, installed map[string]InstalledSkillTarget, managedRoots []string) (bool, bool, error) {
	if strings.TrimSpace(entry.Path) == "" {
		return false, false, nil
	}
	if !pathWithinRoot(skillsHome, entry.Path) {
		return false, false, fmt.Errorf("refusing to inspect managed skill outside skills home: %s", entry.Path)
	}
	runtimeName := runtimeNameFromPath(entry.Path)
	installedEntry, ok := installed[runtimeName]
	if !ok {
		if err := os.RemoveAll(entry.Path); err != nil && !os.IsNotExist(err) {
			return false, false, err
		}
		return true, false, nil
	}
	if !installedSkillMatchesManifest(installedEntry, entry, managedRoots) {
		return false, false, nil
	}
	info, err := os.Lstat(entry.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return true, false, nil
		}
		return false, true, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		if _, err := os.Stat(entry.Path); err == nil {
			return false, true, nil
		} else if !os.IsNotExist(err) {
			return false, true, err
		}
		if err := os.Remove(entry.Path); err != nil && !os.IsNotExist(err) {
			return false, true, err
		}
		return true, false, nil
	}
	return false, true, nil
}

func profileSkillManifestEntry(entry agentadaptor.ResolvedSkill, target string) profilestate.ManifestEntry {
	metadata := map[string]string{}
	if sourceHash := hashedSourcePath(entry.SourcePath); sourceHash != "" {
		metadata[manifestSourceHashKey] = sourceHash
	}
	if len(metadata) == 0 {
		metadata = nil
	}
	return profilestate.ManifestEntry{
		Kind:        profileSkillManifestKind,
		Key:         entry.Key,
		Path:        filepath.Clean(target),
		Fingerprint: profileSkillFingerprint(entry),
		Metadata:    metadata,
	}
}

func profileSkillFingerprint(entry agentadaptor.ResolvedSkill) string {
	sum := sha256.Sum256([]byte(entry.Key + "\x00" + entry.RuntimeName + "\x00" + hashedSourcePath(entry.SourcePath)))
	return hex.EncodeToString(sum[:])
}

func hashedSourcePath(sourcePath string) string {
	sourcePath = strings.TrimSpace(sourcePath)
	if sourcePath == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(normalizePath(sourcePath)))
	return hex.EncodeToString(sum[:])
}

func installedSkillMatchesManifest(installed InstalledSkillTarget, entry profilestate.ManifestEntry, managedRoots []string) bool {
	if installedSkillTargetWithinRoots(installed, managedRoots) {
		return true
	}
	sourceHash := strings.TrimSpace(entry.Metadata[manifestSourceHashKey])
	if sourceHash == "" {
		return false
	}
	return sourceHash == hashedSourcePath(installed.TargetPath)
}

func installedSkillTargetWithinRoots(target InstalledSkillTarget, roots []string) bool {
	targetPath := strings.TrimSpace(target.TargetPath)
	if targetPath == "" {
		return false
	}
	return pathWithinRoots(filepath.Clean(targetPath), roots)
}

func manifestEntryByKey(manifest profilestate.Manifest, key string) (profilestate.ManifestEntry, bool) {
	return manifest.Entry(profileSkillManifestKind, key)
}

func runtimeNameFromPath(path string) string {
	return filepath.Base(filepath.Clean(path))
}

func pathWithinRoot(root, path string) bool {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if path == root {
		return true
	}
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
