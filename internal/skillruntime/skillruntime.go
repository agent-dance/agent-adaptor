package skillruntime

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/agent-dance/agent-adaptor/driver"
)

const sourceMarkerName = ".agent-adaptor-source-path"

// InstalledSkillTarget records what the adapter found on disk for one
// installed skill (in adapter-native skills directory).
type InstalledSkillTarget struct {
	TargetPath string
	Kind       string
}

// EphemeralSnapshotOptions contains everything needed to build an Admin-layer
// snapshot for adapters whose skill-sync mode is ephemeral (e.g. Claude,
// which re-materialises every run rather than installing into a home dir).
type EphemeralSnapshotOptions struct {
	DriverType       string
	Payload          driver.ResolvedSkills
	Selected         []string
	Resolved         []driver.Skill
	ConfiguredDetail string
	MissingDetail    string
	Externals        map[string]InstalledSkillTarget
	LocationLabel    string
	ExternalDetail   string
}

// PersistentSnapshotOptions contains everything needed to build an
// Admin-layer snapshot for adapters whose skill-sync mode is persistent
// (e.g. Cursor, Codex installing to ~/.cursor/skills).
type PersistentSnapshotOptions struct {
	DriverType             string
	Payload                driver.ResolvedSkills
	Selected               []string
	Resolved               []driver.Skill
	Installed              map[string]InstalledSkillTarget
	SkillsHome             string
	LocationLabel          string
	InstalledDetail        string
	MissingDetail          string
	ExternalConflictDetail string
	ExternalDetail         string
}

func ResolveBinding(bindings []driver.EnvBinding, key string) string {
	for _, binding := range bindings {
		if strings.EqualFold(strings.TrimSpace(binding.Name), key) {
			return strings.TrimSpace(binding.Value)
		}
	}
	return ""
}

func WithBinding(bindings []driver.EnvBinding, key, value string) []driver.EnvBinding {
	out := make([]driver.EnvBinding, 0, len(bindings)+1)
	replaced := false
	for _, binding := range bindings {
		if strings.EqualFold(strings.TrimSpace(binding.Name), key) {
			if !replaced {
				out = append(out, driver.EnvBinding{Name: key, Value: value})
				replaced = true
			}
			continue
		}
		out = append(out, binding)
	}
	if !replaced {
		out = append(out, driver.EnvBinding{Name: key, Value: value})
	}
	return out
}

func ApplyProfileBinding(bindings []driver.EnvBinding, profileDir, key string) []driver.EnvBinding {
	if ResolveBinding(bindings, key) != "" {
		return append([]driver.EnvBinding(nil), bindings...)
	}
	if strings.TrimSpace(profileDir) == "" {
		return append([]driver.EnvBinding(nil), bindings...)
	}
	return WithBinding(bindings, key, filepath.Clean(profileDir))
}

func ResolveHome(bindings []driver.EnvBinding) string {
	for _, key := range []string{"HOME", "USERPROFILE"} {
		if value := ResolveBinding(bindings, key); value != "" {
			return filepath.Clean(value)
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return filepath.Clean(home)
}

// ManagedSkillCacheRoot mirrors the SDK default materializer's cache root and
// is re-exported here so adapters (which use it for "is this path managed by
// us" checks) need not import internal SDK helpers.
func ManagedSkillCacheRoot() string {
	if override := strings.TrimSpace(os.Getenv("AGENT_ADAPTOR_SKILL_CACHE_ROOT")); override != "" {
		return filepath.Clean(override)
	}
	root, err := os.UserCacheDir()
	if err != nil || strings.TrimSpace(root) == "" {
		root = os.TempDir()
	}
	return filepath.Join(root, "agent-adaptor", "skill-cache")
}

func ReadInstalledSkillTargets(skillsHome string) (map[string]InstalledSkillTarget, error) {
	entries, err := os.ReadDir(skillsHome)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]InstalledSkillTarget{}, nil
		}
		return nil, err
	}
	out := make(map[string]InstalledSkillTarget, len(entries))
	for _, entry := range entries {
		fullPath := filepath.Join(skillsHome, entry.Name())
		info, err := os.Lstat(fullPath)
		if err != nil {
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			linkedPath, err := os.Readlink(fullPath)
			if err != nil {
				out[entry.Name()] = InstalledSkillTarget{TargetPath: "", Kind: "symlink"}
				continue
			}
			resolved := linkedPath
			if !filepath.IsAbs(resolved) {
				resolved = filepath.Join(filepath.Dir(fullPath), resolved)
			}
			out[entry.Name()] = InstalledSkillTarget{
				TargetPath: filepath.Clean(resolved),
				Kind:       "symlink",
			}
			continue
		}
		if info.IsDir() {
			markerPath := filepath.Join(fullPath, sourceMarkerName)
			markerBytes, err := os.ReadFile(markerPath)
			if err == nil {
				out[entry.Name()] = InstalledSkillTarget{
					TargetPath: strings.TrimSpace(string(markerBytes)),
					Kind:       "directory",
				}
				continue
			}
			out[entry.Name()] = InstalledSkillTarget{
				TargetPath: filepath.Clean(fullPath),
				Kind:       "directory",
			}
			continue
		}
		out[entry.Name()] = InstalledSkillTarget{
			TargetPath: filepath.Clean(fullPath),
			Kind:       "file",
		}
	}
	return out, nil
}

// BuildEphemeralSnapshot produces an Admin-layer snapshot for ephemeral
// adapters. Selected is the union of provider-required + host-selected keys;
// Payload.Entries contains the fully-materialised skill targets.
func BuildEphemeralSnapshot(options EphemeralSnapshotOptions) driver.SkillSnapshot {
	entriesByKey := map[string]driver.ResolvedSkill{}
	availableRuntimeNames := map[string]struct{}{}
	entries := make([]driver.SnapshotEntry, 0, len(options.Payload.Entries))
	selectedSet := map[string]struct{}{}
	for _, key := range options.Selected {
		selectedSet[key] = struct{}{}
	}
	for _, runtimeEntry := range options.Payload.Entries {
		entriesByKey[runtimeEntry.Key] = runtimeEntry
		availableRuntimeNames[runtimeEntry.RuntimeName] = struct{}{}
		_, selected := selectedSet[runtimeEntry.Key]
		entries = append(entries, driver.SnapshotEntry{
			Key:            runtimeEntry.Key,
			RuntimeName:    runtimeEntry.RuntimeName,
			Selected:       selected,
			Managed:        true,
			Required:       runtimeEntry.Required,
			RequiredReason: runtimeEntry.Reason,
			State:          chooseEphemeralState(selected),
			Origin:         skillOrigin(runtimeEntry.Required),
			OriginLabel:    skillOriginLabel(runtimeEntry.Required),
			SourcePath:     runtimeEntry.SourcePath,
			Detail:         detailWhen(selected, options.ConfiguredDetail),
		})
	}

	warnings := append([]string(nil), options.Payload.Warnings...)
	for _, selected := range options.Selected {
		if _, ok := entriesByKey[selected]; ok {
			continue
		}
		warnings = append(warnings, fmt.Sprintf(`selected skill %q is not available from the runtime skill catalog`, selected))
		entries = append(entries, driver.SnapshotEntry{
			Key:         selected,
			Selected:    true,
			Managed:     true,
			State:       driver.SkillStateMissing,
			Origin:      driver.SkillOriginUnknown,
			OriginLabel: "External or unavailable",
			Detail:      options.MissingDetail,
		})
	}

	for name, installed := range options.Externals {
		if _, managed := availableRuntimeNames[name]; managed {
			continue
		}
		entries = append(entries, driver.SnapshotEntry{
			Key:           name,
			RuntimeName:   name,
			Managed:       false,
			State:         driver.SkillStateExternal,
			Origin:        driver.SkillOriginUser,
			OriginLabel:   "User-installed",
			LocationLabel: options.LocationLabel,
			ReadOnly:      true,
			TargetPath:    installed.TargetPath,
			Detail:        options.ExternalDetail,
		})
	}

	sortSnapshotEntries(entries)
	return driver.SkillSnapshot{
		DriverType:  options.DriverType,
		Supported:   true,
		Mode:        driver.SkillSyncEphemeral,
		Selected:    append([]string(nil), options.Selected...),
		Resolved:    cloneSkills(options.Resolved),
		Entries:     cloneSnapshotEntries(entries),
		Warnings:    dedupeStrings(warnings),
		Fingerprint: options.Payload.Fingerprint,
	}
}

// BuildPersistentSnapshot produces an Admin-layer snapshot for persistent
// adapters (those that install skills into a home dir). It records whether
// each resolved skill currently exists on disk (Installed vs Missing),
// whether external / user-managed entries shadow the managed ones, and any
// leftover user-installed entries not covered by the Selected set.
func BuildPersistentSnapshot(options PersistentSnapshotOptions) driver.SkillSnapshot {
	entriesByKey := map[string]driver.ResolvedSkill{}
	selectedSet := map[string]struct{}{}
	for _, key := range options.Selected {
		selectedSet[key] = struct{}{}
	}
	entries := make([]driver.SnapshotEntry, 0, len(options.Payload.Entries))
	for _, runtimeEntry := range options.Payload.Entries {
		entriesByKey[runtimeEntry.Key] = runtimeEntry
		installed, ok := options.Installed[runtimeEntry.RuntimeName]
		selected := hasString(selectedSet, runtimeEntry.Key)
		state := driver.SkillStateAvailable
		managed := false
		detail := ""
		targetPath := filepath.Join(options.SkillsHome, runtimeEntry.RuntimeName)
		if ok && normalizePath(installed.TargetPath) == normalizePath(runtimeEntry.SourcePath) {
			managed = true
			targetPath = filepath.Join(options.SkillsHome, runtimeEntry.RuntimeName)
			if selected {
				state = driver.SkillStateInstalled
			} else {
				state = driver.SkillStateStale
			}
			detail = options.InstalledDetail
		} else if ok {
			state = driver.SkillStateExternal
			targetPath = installed.TargetPath
			if selected {
				detail = options.ExternalConflictDetail
			} else {
				detail = options.ExternalDetail
			}
		} else if selected {
			state = driver.SkillStateMissing
			detail = options.MissingDetail
		}
		entries = append(entries, driver.SnapshotEntry{
			Key:            runtimeEntry.Key,
			RuntimeName:    runtimeEntry.RuntimeName,
			Selected:       selected,
			Managed:        managed,
			Required:       runtimeEntry.Required,
			RequiredReason: runtimeEntry.Reason,
			State:          state,
			Origin:         skillOrigin(runtimeEntry.Required),
			OriginLabel:    skillOriginLabel(runtimeEntry.Required),
			SourcePath:     runtimeEntry.SourcePath,
			TargetPath:     targetPath,
			Detail:         detail,
		})
	}

	warnings := append([]string(nil), options.Payload.Warnings...)
	for _, selected := range options.Selected {
		if _, ok := entriesByKey[selected]; ok {
			continue
		}
		warnings = append(warnings, fmt.Sprintf(`selected skill %q is not available from the runtime skill catalog`, selected))
		entries = append(entries, driver.SnapshotEntry{
			Key:         selected,
			Selected:    true,
			Managed:     true,
			State:       driver.SkillStateMissing,
			Origin:      driver.SkillOriginUnknown,
			OriginLabel: "External or unavailable",
			Detail:      options.MissingDetail,
		})
	}

	for name, installed := range options.Installed {
		if runtimeNameInEntries(name, options.Payload.Entries) {
			continue
		}
		entries = append(entries, driver.SnapshotEntry{
			Key:           name,
			RuntimeName:   name,
			Managed:       false,
			State:         driver.SkillStateExternal,
			Origin:        driver.SkillOriginUser,
			OriginLabel:   "User-installed",
			LocationLabel: options.LocationLabel,
			ReadOnly:      true,
			TargetPath:    installed.TargetPath,
			Detail:        options.ExternalDetail,
		})
	}

	sortSnapshotEntries(entries)
	return driver.SkillSnapshot{
		DriverType:  options.DriverType,
		Supported:   true,
		Mode:        driver.SkillSyncPersistent,
		Selected:    append([]string(nil), options.Selected...),
		Resolved:    cloneSkills(options.Resolved),
		Entries:     cloneSnapshotEntries(entries),
		Warnings:    dedupeStrings(warnings),
		Fingerprint: options.Payload.Fingerprint,
	}
}

// SelectedResolvedSkills returns the subset of payload.Entries whose Key is in
// selected. Helpers kept for adapters that want a straight-through list of
// already-selected skills.
func SelectedResolvedSkills(payload driver.ResolvedSkills, selected []string) []driver.ResolvedSkill {
	allowed := map[string]struct{}{}
	for _, key := range selected {
		allowed[key] = struct{}{}
	}
	out := make([]driver.ResolvedSkill, 0, len(payload.Entries))
	for _, entry := range payload.Entries {
		if _, ok := allowed[entry.Key]; ok {
			out = append(out, entry)
		}
	}
	return out
}

func EnsureSkillTarget(sourcePath, targetPath string, managedRoots []string) (string, error) {
	existing, err := os.Lstat(targetPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return "", err
		}
		if err := createManagedSkillTarget(sourcePath, targetPath); err != nil {
			return "", err
		}
		return "created", nil
	}

	if existing.Mode()&os.ModeSymlink != 0 {
		linkedPath, err := os.Readlink(targetPath)
		if err != nil {
			return "", err
		}
		resolved := linkedPath
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(filepath.Dir(targetPath), resolved)
		}
		resolved = filepath.Clean(resolved)
		if normalizePath(resolved) == normalizePath(sourcePath) {
			return "skipped", nil
		}
		if _, err := os.Stat(resolved); err == nil && !pathWithinRoots(resolved, managedRoots) {
			return "skipped", nil
		}
		if err := os.Remove(targetPath); err != nil {
			return "", err
		}
		if err := createManagedSkillTarget(sourcePath, targetPath); err != nil {
			return "", err
		}
		return "repaired", nil
	}

	if existing.IsDir() {
		markerPath := filepath.Join(targetPath, sourceMarkerName)
		markerBytes, err := os.ReadFile(markerPath)
		if err != nil {
			return "skipped", nil
		}
		if normalizePath(strings.TrimSpace(string(markerBytes))) == normalizePath(sourcePath) {
			return "skipped", nil
		}
		if !pathWithinRoots(strings.TrimSpace(string(markerBytes)), managedRoots) {
			return "skipped", nil
		}
		if err := os.RemoveAll(targetPath); err != nil {
			return "", err
		}
		if err := createManagedSkillTarget(sourcePath, targetPath); err != nil {
			return "", err
		}
		return "repaired", nil
	}

	return "skipped", nil
}

func RemoveManagedSkillTargets(skillsHome string, allowedRuntimeNames []string, managedRoots []string) ([]string, error) {
	allowed := map[string]struct{}{}
	for _, name := range allowedRuntimeNames {
		allowed[name] = struct{}{}
	}
	entries, err := os.ReadDir(skillsHome)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	removed := make([]string, 0)
	for _, entry := range entries {
		if _, keep := allowed[entry.Name()]; keep {
			continue
		}
		fullPath := filepath.Join(skillsHome, entry.Name())
		managed, err := managedTargetPath(fullPath, managedRoots)
		if err != nil || managed == "" {
			continue
		}
		if err := os.RemoveAll(fullPath); err != nil {
			return removed, err
		}
		removed = append(removed, entry.Name())
	}
	sort.Strings(removed)
	return removed, nil
}

func PruneBrokenManagedSkillTargets(skillsHome string, allowedRuntimeNames []string, managedRoots []string) ([]string, error) {
	allowed := map[string]struct{}{}
	for _, name := range allowedRuntimeNames {
		allowed[name] = struct{}{}
	}
	entries, err := os.ReadDir(skillsHome)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	removed := make([]string, 0)
	for _, entry := range entries {
		if _, keep := allowed[entry.Name()]; keep {
			continue
		}
		fullPath := filepath.Join(skillsHome, entry.Name())
		info, err := os.Lstat(fullPath)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			continue
		}
		linkedPath, err := os.Readlink(fullPath)
		if err != nil {
			continue
		}
		resolved := linkedPath
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(filepath.Dir(fullPath), resolved)
		}
		resolved = filepath.Clean(resolved)
		if !pathWithinRoots(resolved, managedRoots) {
			continue
		}
		if _, err := os.Stat(resolved); err == nil {
			continue
		}
		if err := os.Remove(fullPath); err != nil {
			return removed, err
		}
		removed = append(removed, entry.Name())
	}
	sort.Strings(removed)
	return removed, nil
}

func createManagedSkillTarget(sourcePath, targetPath string) error {
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return err
	}
	if err := os.Symlink(sourcePath, targetPath); err == nil {
		return nil
	}
	if err := copyDir(sourcePath, targetPath); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(targetPath, sourceMarkerName), []byte(filepath.Clean(sourcePath)), 0o644)
}

func copyDir(sourcePath, targetPath string) error {
	if err := os.RemoveAll(targetPath); err != nil {
		return err
	}
	return filepath.Walk(sourcePath, func(current string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(sourcePath, current)
		if err != nil {
			return err
		}
		destination := targetPath
		if relative != "." {
			destination = filepath.Join(targetPath, relative)
		}
		if info.IsDir() {
			return os.MkdirAll(destination, 0o755)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			linked, err := os.Readlink(current)
			if err != nil {
				return err
			}
			return os.Symlink(linked, destination)
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return err
		}
		in, err := os.Open(current)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.Create(destination)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	})
}

func managedTargetPath(fullPath string, managedRoots []string) (string, error) {
	info, err := os.Lstat(fullPath)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		linkedPath, err := os.Readlink(fullPath)
		if err != nil {
			return "", err
		}
		resolved := linkedPath
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(filepath.Dir(fullPath), resolved)
		}
		resolved = filepath.Clean(resolved)
		if pathWithinRoots(resolved, managedRoots) {
			return resolved, nil
		}
		return "", nil
	}
	if !info.IsDir() {
		return "", nil
	}
	markerPath := filepath.Join(fullPath, sourceMarkerName)
	markerBytes, err := os.ReadFile(markerPath)
	if err != nil {
		return "", nil
	}
	resolved := filepath.Clean(strings.TrimSpace(string(markerBytes)))
	if pathWithinRoots(resolved, managedRoots) {
		return resolved, nil
	}
	return "", nil
}

func pathWithinRoots(candidate string, roots []string) bool {
	normalizedCandidate := normalizePath(candidate)
	for _, root := range roots {
		normalizedRoot := normalizePath(root)
		if normalizedRoot == "" {
			continue
		}
		if normalizedCandidate == normalizedRoot || strings.HasPrefix(normalizedCandidate, normalizedRoot+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func normalizePath(value string) string {
	return filepath.Clean(value)
}

func runtimeNameInEntries(runtimeName string, values []driver.ResolvedSkill) bool {
	for _, value := range values {
		if value.RuntimeName == runtimeName {
			return true
		}
	}
	return false
}

func sortSnapshotEntries(values []driver.SnapshotEntry) {
	sort.Slice(values, func(left, right int) bool {
		if values[left].Key == values[right].Key {
			return values[left].RuntimeName < values[right].RuntimeName
		}
		return values[left].Key < values[right].Key
	})
}

func dedupeStrings(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func hasString(values map[string]struct{}, key string) bool {
	_, ok := values[key]
	return ok
}

func chooseEphemeralState(selected bool) driver.SkillState {
	if selected {
		return driver.SkillStateConfigured
	}
	return driver.SkillStateAvailable
}

func detailWhen(condition bool, value string) string {
	if condition {
		return value
	}
	return ""
}

func skillOrigin(required bool) driver.SkillOrigin {
	if required {
		return driver.SkillOriginRequired
	}
	return driver.SkillOriginManaged
}

func skillOriginLabel(required bool) string {
	if required {
		return "Required by agent-adaptor"
	}
	return "Managed by agent-adaptor"
}

func cloneSkills(values []driver.Skill) []driver.Skill {
	if len(values) == 0 {
		return nil
	}
	out := make([]driver.Skill, len(values))
	for index, value := range values {
		out[index] = driver.Skill{
			Key:      value.Key,
			Source:   value.Source,
			Required: value.Required,
			Reason:   value.Reason,
			Metadata: cloneStringMap(value.Metadata),
		}
	}
	return out
}

func cloneSnapshotEntries(values []driver.SnapshotEntry) []driver.SnapshotEntry {
	if len(values) == 0 {
		return nil
	}
	out := make([]driver.SnapshotEntry, len(values))
	copy(out, values)
	return out
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
