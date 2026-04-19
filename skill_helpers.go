package agentadaptor

import (
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func cloneSkillFiles(values []SkillFile) []SkillFile {
	if len(values) == 0 {
		return nil
	}
	out := make([]SkillFile, len(values))
	copy(out, values)
	return out
}

func cloneSkills(values []Skill) []Skill {
	if len(values) == 0 {
		return nil
	}
	out := make([]Skill, len(values))
	for index, value := range values {
		out[index] = Skill{
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

func cloneRuntimeEntries(values []SkillRuntimeEntry) []SkillRuntimeEntry {
	if len(values) == 0 {
		return nil
	}
	out := make([]SkillRuntimeEntry, len(values))
	copy(out, values)
	return out
}

func cloneSkillEntries(values []SkillEntry) []SkillEntry {
	if len(values) == 0 {
		return nil
	}
	out := make([]SkillEntry, len(values))
	copy(out, values)
	return out
}

func normalizeSkillRef(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	return strings.ToLower(trimmed)
}

func skillSlug(value string) string {
	trimmed := normalizeSkillRef(value)
	if trimmed == "" {
		return ""
	}
	trimmed = strings.ReplaceAll(trimmed, "\\", "/")
	parts := strings.Split(trimmed, "/")
	return parts[len(parts)-1]
}

func defaultSkillRuntimeName(key string) string {
	slug := skillSlug(key)
	if slug == "" {
		slug = "skill"
	}
	if normalizeSkillRef(key) == slug {
		return slug
	}
	return slug + "--" + stableHash(key)[:10]
}

func canonicalSkillRef(reference string, available []Skill) string {
	normalized := normalizeSkillRef(reference)
	if normalized == "" {
		return ""
	}
	for _, skill := range available {
		if normalizeSkillRef(skill.Key) == normalized {
			return skill.Key
		}
	}
	var runtimeMatches []Skill
	for _, skill := range available {
		if normalizeSkillRef(skillRuntimeName(skill)) == normalized {
			runtimeMatches = append(runtimeMatches, skill)
		}
	}
	if len(runtimeMatches) == 1 {
		return runtimeMatches[0].Key
	}
	var slugMatches []Skill
	for _, skill := range available {
		if skillSlug(skill.Key) == normalized {
			slugMatches = append(slugMatches, skill)
		}
	}
	if len(slugMatches) == 1 {
		return slugMatches[0].Key
	}
	return normalized
}

func mergeUniqueStrings(parts ...[]string) []string {
	if len(parts) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0)
	for _, part := range parts {
		for _, value := range part {
			trimmed := strings.TrimSpace(value)
			if trimmed == "" {
				continue
			}
			if _, exists := seen[trimmed]; exists {
				continue
			}
			seen[trimmed] = struct{}{}
			out = append(out, trimmed)
		}
	}
	return out
}

func mergeUniqueSkills(parts ...[]Skill) []Skill {
	seen := map[string]struct{}{}
	out := make([]Skill, 0)
	for _, part := range parts {
		for _, skill := range part {
			key := strings.TrimSpace(skill.Key)
			if key == "" {
				key = skillRuntimeName(skill)
			}
			if key == "" {
				continue
			}
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, skill)
		}
	}
	return out
}

func requiredSkills(skills []Skill) []Skill {
	out := make([]Skill, 0)
	for _, skill := range skills {
		if skill.Required {
			out = append(out, skill)
		}
	}
	return out
}

func skillKeys(skills []Skill) []string {
	out := make([]string, 0, len(skills))
	for _, skill := range skills {
		if trimmed := strings.TrimSpace(skill.Key); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func skillRuntimeName(skill Skill) string {
	if trimmed := strings.TrimSpace(skill.Runtime); trimmed != "" {
		return trimmed
	}
	return defaultSkillRuntimeName(skill.Key)
}

func looksLikePathReference(ref string) bool {
	trimmed := strings.TrimSpace(ref)
	if trimmed == "" {
		return false
	}
	if filepath.IsAbs(trimmed) {
		return true
	}
	if strings.HasPrefix(trimmed, ".") {
		return true
	}
	if strings.Contains(trimmed, `\`) || strings.Contains(trimmed, "/") {
		return true
	}
	if strings.HasSuffix(strings.ToLower(trimmed), ".md") {
		return true
	}
	return false
}

func resolveSkillPathHint(pathHint string, workspace WorkspaceLease) string {
	trimmed := strings.TrimSpace(pathHint)
	if trimmed == "" {
		return ""
	}
	if filepath.IsAbs(trimmed) {
		return filepath.Clean(trimmed)
	}
	base := workspace.CWD
	if strings.TrimSpace(base) == "" {
		base = ensureBaseCWD("")
	}
	return filepath.Clean(filepath.Join(base, trimmed))
}

func resolveExistingSkillSource(pathHint string, workspace WorkspaceLease) string {
	resolved := resolveSkillPathHint(pathHint, workspace)
	if resolved == "" {
		return ""
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return ""
	}
	if info.IsDir() {
		skillPath := filepath.Join(resolved, "SKILL.md")
		if stat, err := os.Stat(skillPath); err == nil && !stat.IsDir() {
			return resolved
		}
		return ""
	}
	if strings.EqualFold(filepath.Base(resolved), "SKILL.md") {
		return filepath.Dir(resolved)
	}
	return ""
}

func managedSkillCacheRoot() string {
	root, err := os.UserCacheDir()
	if err != nil || strings.TrimSpace(root) == "" {
		root = os.TempDir()
	}
	return filepath.Join(root, "agent-adaptor", "skill-cache")
}

func inlineSkillFileMap(skill Skill) map[string]string {
	files := map[string]string{}
	for _, file := range skill.Files {
		normalized := normalizePortablePath(file.Path)
		if normalized == "" {
			continue
		}
		files[normalized] = file.Content
	}
	if _, exists := files["SKILL.md"]; !exists && strings.TrimSpace(skill.Content) != "" {
		files["SKILL.md"] = skill.Content
	}
	return files
}

func normalizePortablePath(input string) string {
	trimmed := strings.TrimSpace(strings.ReplaceAll(input, "\\", "/"))
	if trimmed == "" {
		return ""
	}
	trimmed = strings.TrimPrefix(trimmed, "./")
	trimmed = strings.TrimPrefix(trimmed, "/")
	parts := make([]string, 0)
	for _, part := range strings.Split(trimmed, "/") {
		switch part {
		case "", ".":
			continue
		case "..":
			if len(parts) > 0 {
				parts = parts[:len(parts)-1]
			}
		default:
			parts = append(parts, part)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return filepath.ToSlash(filepath.Clean(strings.Join(parts, "/")))
}

func materializeSkillSource(skill Skill, workspace WorkspaceLease) (string, error) {
	if source := resolveExistingSkillSource(skill.PathHint, workspace); source != "" {
		return source, nil
	}

	files := inlineSkillFileMap(skill)
	if len(files) == 0 {
		return "", errors.New("skill does not resolve to an existing source directory and does not provide inline content")
	}
	if _, ok := files["SKILL.md"]; !ok {
		return "", errors.New("skill bundle is missing SKILL.md")
	}

	runtimeName := skillRuntimeName(skill)
	cacheKey := stableHash(skill.Key, runtimeName, skill.Required, skill.RequiredReason, files, skill.Metadata)
	targetDir := filepath.Join(managedSkillCacheRoot(), runtimeName+"--"+cacheKey[:12])
	readyMarker := filepath.Join(targetDir, ".agent-adaptor-ready")
	if _, err := os.Stat(readyMarker); err == nil {
		return targetDir, nil
	}

	if err := os.MkdirAll(filepath.Dir(targetDir), 0o755); err != nil {
		return "", err
	}
	tmpDir := fmt.Sprintf("%s.tmp-%d-%d", targetDir, os.Getpid(), rand.New(rand.NewSource(time.Now().UnixNano())).Int63())
	if err := os.RemoveAll(tmpDir); err != nil {
		return "", err
	}
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return "", err
	}
	success := false
	defer func() {
		if !success {
			_ = os.RemoveAll(tmpDir)
		}
	}()

	for relativePath, content := range files {
		targetPath := filepath.Join(tmpDir, filepath.FromSlash(relativePath))
		if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(targetPath, []byte(content), 0o644); err != nil {
			return "", err
		}
	}
	if err := os.WriteFile(readyMarkerFor(tmpDir), []byte(cacheKey), 0o644); err != nil {
		return "", err
	}

	if err := os.Rename(tmpDir, targetDir); err != nil {
		if _, statErr := os.Stat(readyMarker); statErr == nil {
			success = true
			return targetDir, nil
		}
		return "", err
	}
	success = true
	return targetDir, nil
}

func readyMarkerFor(root string) string {
	return filepath.Join(root, ".agent-adaptor-ready")
}

func prepareSkillPayload(req SkillAssemblyRequest) SkillPayload {
	available := cloneSkills(req.Available)
	if len(available) == 0 {
		available = cloneSkills(req.Resolved)
	}
	canonicalRequested := make([]string, 0, len(req.Requested))
	for _, ref := range req.Requested {
		if normalized := canonicalSkillRef(ref, available); normalized != "" {
			canonicalRequested = append(canonicalRequested, normalized)
		}
	}
	for _, skill := range requiredSkills(available) {
		canonicalRequested = append(canonicalRequested, skill.Key)
	}
	canonicalRequested = mergeUniqueStrings(canonicalRequested)

	resolved := mergeUniqueSkills(req.Resolved, requiredSkills(available))
	runtimeCatalog := cloneSkills(available)
	if len(runtimeCatalog) == 0 {
		runtimeCatalog = cloneSkills(resolved)
	}
	runtimeEntries := make([]SkillRuntimeEntry, 0, len(runtimeCatalog))
	warnings := make([]string, 0)
	for _, skill := range runtimeCatalog {
		sourcePath, err := materializeSkillSource(skill, req.Workspace)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("skill %q is unavailable: %v", skill.Key, err))
			continue
		}
		runtimeEntries = append(runtimeEntries, SkillRuntimeEntry{
			Key:            skill.Key,
			RuntimeName:    skillRuntimeName(skill),
			SourcePath:     sourcePath,
			Required:       skill.Required,
			RequiredReason: strings.TrimSpace(skill.RequiredReason),
		})
	}
	sort.Slice(runtimeEntries, func(left, right int) bool {
		return runtimeEntries[left].Key < runtimeEntries[right].Key
	})
	warnings = mergeUniqueStrings(warnings)

	mode := SkillSyncEphemeral
	if len(runtimeEntries) == 0 && len(canonicalRequested) == 0 {
		mode = SkillSyncUnsupported
	}

	return SkillPayload{
		Mode:           mode,
		Requested:      canonicalRequested,
		Resolved:       cloneSkills(resolved),
		RuntimeEntries: cloneRuntimeEntries(runtimeEntries),
		Warnings:       cloneStrings(warnings),
		Fingerprint:    stableHash(req.DriverType, req.TenantID, canonicalRequested, resolved, runtimeEntries, warnings),
	}
}
