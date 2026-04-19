package skillruntime

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

const sourceMarkerName = ".agent-adaptor-source-path"

type InstalledSkillTarget struct {
	TargetPath string
	Kind       string
}

type EphemeralSnapshotOptions struct {
	DriverType       string
	Payload          agentadaptor.SkillPayload
	ConfiguredDetail string
	MissingDetail    string
	Externals        map[string]InstalledSkillTarget
	LocationLabel    string
	ExternalDetail   string
}

type PersistentSnapshotOptions struct {
	DriverType             string
	Payload                agentadaptor.SkillPayload
	Installed              map[string]InstalledSkillTarget
	SkillsHome             string
	LocationLabel          string
	InstalledDetail        string
	MissingDetail          string
	ExternalConflictDetail string
	ExternalDetail         string
}

func ResolveBinding(bindings []agentadaptor.EnvBinding, key string) string {
	for _, binding := range bindings {
		if strings.EqualFold(strings.TrimSpace(binding.Name), key) {
			return strings.TrimSpace(binding.Value)
		}
	}
	return ""
}

func WithBinding(bindings []agentadaptor.EnvBinding, key, value string) []agentadaptor.EnvBinding {
	out := make([]agentadaptor.EnvBinding, 0, len(bindings)+1)
	replaced := false
	for _, binding := range bindings {
		if strings.EqualFold(strings.TrimSpace(binding.Name), key) {
			if !replaced {
				out = append(out, agentadaptor.EnvBinding{Name: key, Value: value})
				replaced = true
			}
			continue
		}
		out = append(out, binding)
	}
	if !replaced {
		out = append(out, agentadaptor.EnvBinding{Name: key, Value: value})
	}
	return out
}

func ApplyProfileBinding(bindings []agentadaptor.EnvBinding, profileDir, key string) []agentadaptor.EnvBinding {
	if ResolveBinding(bindings, key) != "" {
		return append([]agentadaptor.EnvBinding(nil), bindings...)
	}
	if strings.TrimSpace(profileDir) == "" {
		return append([]agentadaptor.EnvBinding(nil), bindings...)
	}
	return WithBinding(bindings, key, filepath.Clean(profileDir))
}

func ResolveHome(bindings []agentadaptor.EnvBinding) string {
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

func ManagedSkillCacheRoot() string {
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

func BuildEphemeralSnapshot(options EphemeralSnapshotOptions) agentadaptor.SkillSnapshot {
	availableByKey := map[string]agentadaptor.SkillRuntimeEntry{}
	availableRuntimeNames := map[string]struct{}{}
	entries := make([]agentadaptor.SkillEntry, 0, len(options.Payload.RuntimeEntries))
	desiredSet := map[string]struct{}{}
	for _, desired := range options.Payload.Requested {
		desiredSet[desired] = struct{}{}
	}
	for _, runtimeEntry := range options.Payload.RuntimeEntries {
		availableByKey[runtimeEntry.Key] = runtimeEntry
		availableRuntimeNames[runtimeEntry.RuntimeName] = struct{}{}
		_, desired := desiredSet[runtimeEntry.Key]
		entries = append(entries, agentadaptor.SkillEntry{
			Key:            runtimeEntry.Key,
			RuntimeName:    runtimeEntry.RuntimeName,
			Desired:        desired,
			Managed:        true,
			Required:       runtimeEntry.Required,
			RequiredReason: runtimeEntry.RequiredReason,
			State:          chooseEphemeralState(desired),
			Origin:         skillOrigin(runtimeEntry.Required),
			OriginLabel:    skillOriginLabel(runtimeEntry.Required),
			SourcePath:     runtimeEntry.SourcePath,
			Detail:         detailWhen(desired, options.ConfiguredDetail),
		})
	}

	warnings := append([]string(nil), options.Payload.Warnings...)
	for _, desired := range options.Payload.Requested {
		if _, ok := availableByKey[desired]; ok {
			continue
		}
		warnings = append(warnings, fmt.Sprintf(`desired skill %q is not available from the runtime skill catalog`, desired))
		entries = append(entries, agentadaptor.SkillEntry{
			Key:         desired,
			Desired:     true,
			Managed:     true,
			State:       agentadaptor.SkillStateMissing,
			Origin:      agentadaptor.SkillOriginUnknown,
			OriginLabel: "External or unavailable",
			Detail:      options.MissingDetail,
		})
	}

	for name, installed := range options.Externals {
		if _, managed := availableRuntimeNames[name]; managed {
			continue
		}
		entries = append(entries, agentadaptor.SkillEntry{
			Key:           name,
			RuntimeName:   name,
			Managed:       false,
			State:         agentadaptor.SkillStateExternal,
			Origin:        agentadaptor.SkillOriginUser,
			OriginLabel:   "User-installed",
			LocationLabel: options.LocationLabel,
			ReadOnly:      true,
			TargetPath:    installed.TargetPath,
			Detail:        options.ExternalDetail,
		})
	}

	sortSkillEntries(entries)
	return agentadaptor.SkillSnapshot{
		DriverType: options.DriverType,
		Supported:  true,
		Mode:       agentadaptor.SkillSyncEphemeral,
		Desired:    append([]string(nil), options.Payload.Requested...),
		Resolved:   cloneSkills(options.Payload.Resolved),
		Entries:    cloneSkillEntries(entries),
		Warnings:   dedupeStrings(warnings),
	}
}

func BuildPersistentSnapshot(options PersistentSnapshotOptions) agentadaptor.SkillSnapshot {
	availableByKey := map[string]agentadaptor.SkillRuntimeEntry{}
	desiredSet := map[string]struct{}{}
	for _, desired := range options.Payload.Requested {
		desiredSet[desired] = struct{}{}
	}
	entries := make([]agentadaptor.SkillEntry, 0, len(options.Payload.RuntimeEntries))
	for _, runtimeEntry := range options.Payload.RuntimeEntries {
		availableByKey[runtimeEntry.Key] = runtimeEntry
		installed, ok := options.Installed[runtimeEntry.RuntimeName]
		desired := hasString(desiredSet, runtimeEntry.Key)
		state := agentadaptor.SkillStateAvailable
		managed := false
		detail := ""
		targetPath := filepath.Join(options.SkillsHome, runtimeEntry.RuntimeName)
		if ok && normalizePath(installed.TargetPath) == normalizePath(runtimeEntry.SourcePath) {
			managed = true
			targetPath = filepath.Join(options.SkillsHome, runtimeEntry.RuntimeName)
			if desired {
				state = agentadaptor.SkillStateInstalled
			} else {
				state = agentadaptor.SkillStateStale
			}
			detail = options.InstalledDetail
		} else if ok {
			state = agentadaptor.SkillStateExternal
			targetPath = installed.TargetPath
			if desired {
				detail = options.ExternalConflictDetail
			} else {
				detail = options.ExternalDetail
			}
		} else if desired {
			state = agentadaptor.SkillStateMissing
			detail = options.MissingDetail
		}
		entries = append(entries, agentadaptor.SkillEntry{
			Key:            runtimeEntry.Key,
			RuntimeName:    runtimeEntry.RuntimeName,
			Desired:        desired,
			Managed:        managed,
			Required:       runtimeEntry.Required,
			RequiredReason: runtimeEntry.RequiredReason,
			State:          state,
			Origin:         skillOrigin(runtimeEntry.Required),
			OriginLabel:    skillOriginLabel(runtimeEntry.Required),
			SourcePath:     runtimeEntry.SourcePath,
			TargetPath:     targetPath,
			Detail:         detail,
		})
	}

	warnings := append([]string(nil), options.Payload.Warnings...)
	for _, desired := range options.Payload.Requested {
		if _, ok := availableByKey[desired]; ok {
			continue
		}
		warnings = append(warnings, fmt.Sprintf(`desired skill %q is not available from the runtime skill catalog`, desired))
		entries = append(entries, agentadaptor.SkillEntry{
			Key:         desired,
			Desired:     true,
			Managed:     true,
			State:       agentadaptor.SkillStateMissing,
			Origin:      agentadaptor.SkillOriginUnknown,
			OriginLabel: "External or unavailable",
			Detail:      options.MissingDetail,
		})
	}

	for name, installed := range options.Installed {
		if runtimeNameInPayload(name, options.Payload.RuntimeEntries) {
			continue
		}
		entries = append(entries, agentadaptor.SkillEntry{
			Key:           name,
			RuntimeName:   name,
			Managed:       false,
			State:         agentadaptor.SkillStateExternal,
			Origin:        agentadaptor.SkillOriginUser,
			OriginLabel:   "User-installed",
			LocationLabel: options.LocationLabel,
			ReadOnly:      true,
			TargetPath:    installed.TargetPath,
			Detail:        options.ExternalDetail,
		})
	}

	sortSkillEntries(entries)
	return agentadaptor.SkillSnapshot{
		DriverType: options.DriverType,
		Supported:  true,
		Mode:       agentadaptor.SkillSyncPersistent,
		Desired:    append([]string(nil), options.Payload.Requested...),
		Resolved:   cloneSkills(options.Payload.Resolved),
		Entries:    cloneSkillEntries(entries),
		Warnings:   dedupeStrings(warnings),
	}
}

func SelectedRuntimeEntries(payload agentadaptor.SkillPayload) []agentadaptor.SkillRuntimeEntry {
	desired := map[string]struct{}{}
	for _, key := range payload.Requested {
		desired[key] = struct{}{}
	}
	out := make([]agentadaptor.SkillRuntimeEntry, 0, len(payload.RuntimeEntries))
	for _, entry := range payload.RuntimeEntries {
		if _, ok := desired[entry.Key]; ok {
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

func runtimeNameInPayload(runtimeName string, values []agentadaptor.SkillRuntimeEntry) bool {
	for _, value := range values {
		if value.RuntimeName == runtimeName {
			return true
		}
	}
	return false
}

func sortSkillEntries(values []agentadaptor.SkillEntry) {
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

func chooseEphemeralState(desired bool) agentadaptor.SkillState {
	if desired {
		return agentadaptor.SkillStateConfigured
	}
	return agentadaptor.SkillStateAvailable
}

func detailWhen(condition bool, value string) string {
	if condition {
		return value
	}
	return ""
}

func skillOrigin(required bool) agentadaptor.SkillOrigin {
	if required {
		return agentadaptor.SkillOriginRequired
	}
	return agentadaptor.SkillOriginManaged
}

func skillOriginLabel(required bool) string {
	if required {
		return "Required by agent-adaptor"
	}
	return "Managed by agent-adaptor"
}

func cloneSkills(values []agentadaptor.Skill) []agentadaptor.Skill {
	if len(values) == 0 {
		return nil
	}
	out := make([]agentadaptor.Skill, len(values))
	for index, value := range values {
		out[index] = agentadaptor.Skill{
			Key:            value.Key,
			Runtime:        value.Runtime,
			Content:        value.Content,
			PathHint:       value.PathHint,
			Metadata:       cloneStringMap(value.Metadata),
			Files:          cloneSkillFiles(value.Files),
			Required:       value.Required,
			RequiredReason: value.RequiredReason,
		}
	}
	return out
}

func cloneSkillEntries(values []agentadaptor.SkillEntry) []agentadaptor.SkillEntry {
	if len(values) == 0 {
		return nil
	}
	out := make([]agentadaptor.SkillEntry, len(values))
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

func cloneSkillFiles(values []agentadaptor.SkillFile) []agentadaptor.SkillFile {
	if len(values) == 0 {
		return nil
	}
	out := make([]agentadaptor.SkillFile, len(values))
	copy(out, values)
	return out
}
