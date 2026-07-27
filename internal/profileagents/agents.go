package profileagents

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	toml "github.com/pelletier/go-toml/v2"
)

const resourceKind = string(engine.ProfileResourceAgents)

func Snapshot(driverType, profileDir string, payload driver.AgentPayload, synced bool) engine.ResourceSnapshot {
	out := engine.ResourceSnapshot{
		Kind:            engine.ProfileResourceAgents,
		Fingerprint:     payload.Fingerprint,
		Support:         engine.ProfileResourceSupportPortableCore,
		Materialization: engine.ProfileResourceMaterializationNotMaterialized,
		Warnings:        cloneStrings(payload.Warnings),
	}
	if len(payload.Agents) == 0 {
		return out
	}
	warnings := collectWarnings(driverType, payload)
	out.Warnings = append(out.Warnings, warnings...)
	if anySourcePath(payload) {
		out.Support = engine.ProfileResourceSupportNativeEscape
	} else if len(warnings) > 0 {
		out.Support = engine.ProfileResourceSupportPortableExtended
	}
	if synced {
		for _, spec := range payload.Agents {
			out.Managed = append(out.Managed, spec.Key)
		}
		sort.Strings(out.Managed)
		out.Materialization = engine.ProfileResourceMaterializationNativeManaged
	} else {
		out.Warnings = append(out.Warnings, "agent resources are desired but not observed by ProfileSnapshot; call SyncProfile to materialize them")
	}
	return out
}

func Sync(ctx context.Context, driverType, profileDir string, payload driver.AgentPayload) (engine.ResourceSnapshot, error) {
	if strings.TrimSpace(profileDir) == "" {
		return engine.ResourceSnapshot{}, fmt.Errorf("profile agents require profile directory")
	}
	root, ext, err := layout(driverType, profileDir)
	if err != nil {
		return engine.ResourceSnapshot{}, err
	}
	lock, err := profilestate.AcquireLock(ctx, profileDir, profilestate.LockOptions{StaleAfter: 10 * time.Minute})
	if err != nil {
		return engine.ResourceSnapshot{}, err
	}
	defer lock.Release()

	manifest, err := profilestate.LoadManifest(profileDir)
	if err != nil {
		return engine.ResourceSnapshot{}, err
	}
	entries := make([]profilereconcile.DirectoryEntry, 0, len(payload.Agents))
	for _, spec := range payload.Agents {
		entry, err := directoryEntry(driverType, spec, ext)
		if err != nil {
			return engine.ResourceSnapshot{}, err
		}
		entries = append(entries, entry)
	}
	dirSnapshot, err := profilereconcile.ReconcileDirectory(profilereconcile.DirectoryOptions{
		Root:       root,
		Kind:       resourceKind,
		Manifest:   &manifest,
		Entries:    entries,
		AllowPrune: true,
	})
	if err != nil {
		return engine.ResourceSnapshot{}, err
	}
	if err := profilestate.SaveManifest(profileDir, manifest); err != nil {
		return engine.ResourceSnapshot{}, err
	}
	snapshot := Snapshot(driverType, profileDir, payload, true)
	snapshot.Managed = dirSnapshot.Managed
	snapshot.External = dirSnapshot.External
	return snapshot, nil
}

func layout(driverType, profileDir string) (string, string, error) {
	switch driverType {
	case "codex":
		return filepath.Join(profileDir, "agents"), ".toml", nil
	case "claude", "cursor":
		return filepath.Join(profileDir, "agents"), ".md", nil
	default:
		return "", "", fmt.Errorf("profile agents are unsupported by adapter %q", driverType)
	}
}

func directoryEntry(driverType string, spec driver.AgentSpec, ext string) (profilereconcile.DirectoryEntry, error) {
	name := agentName(spec)
	runtimeName := runtimeFileName(spec, ext)
	entry := profilereconcile.DirectoryEntry{
		Key:         spec.Key,
		RuntimeName: runtimeName,
		SourcePath:  strings.TrimSpace(spec.SourcePath),
		Fingerprint: fingerprint(spec),
		Metadata: map[string]string{
			"provider": driverType,
			"name":     name,
		},
	}
	if entry.SourcePath != "" {
		return entry, nil
	}
	content, err := render(driverType, spec, name)
	if err != nil {
		return profilereconcile.DirectoryEntry{}, err
	}
	entry.Content = content
	return entry, nil
}

func render(driverType string, spec driver.AgentSpec, name string) (string, error) {
	switch driverType {
	case "codex":
		return renderCodex(spec, name)
	case "claude":
		return renderMarkdown(spec, name, true), nil
	case "cursor":
		return renderMarkdown(spec, name, false), nil
	default:
		return "", fmt.Errorf("profile agents are unsupported by adapter %q", driverType)
	}
}

func renderCodex(spec driver.AgentSpec, name string) (string, error) {
	values := map[string]any{
		"name":                   name,
		"description":            description(spec),
		"developer_instructions": instructions(spec),
	}
	if strings.TrimSpace(spec.Model) != "" {
		values["model"] = strings.TrimSpace(spec.Model)
	}
	if strings.TrimSpace(spec.ReasoningEffort) != "" {
		values["model_reasoning_effort"] = strings.TrimSpace(spec.ReasoningEffort)
	}
	if strings.TrimSpace(spec.SandboxMode) != "" {
		values["sandbox_mode"] = strings.TrimSpace(spec.SandboxMode)
	}
	raw, err := toml.Marshal(values)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func renderMarkdown(spec driver.AgentSpec, name string, claude bool) string {
	lines := []string{
		"---",
		"name: " + yamlString(name),
		"description: " + yamlString(description(spec)),
	}
	if claude {
		if strings.TrimSpace(spec.Model) != "" {
			lines = append(lines, "model: "+yamlString(strings.TrimSpace(spec.Model)))
		}
		if strings.TrimSpace(spec.PermissionMode) != "" {
			lines = append(lines, "permissionMode: "+yamlString(strings.TrimSpace(spec.PermissionMode)))
		}
		if strings.TrimSpace(spec.ReasoningEffort) != "" {
			lines = append(lines, "effort: "+yamlString(strings.TrimSpace(spec.ReasoningEffort)))
		}
		if spec.ToolPolicy != nil {
			if len(spec.ToolPolicy.Allow) > 0 {
				lines = append(lines, "tools: "+yamlInlineList(spec.ToolPolicy.Allow))
			}
			if len(spec.ToolPolicy.Deny) > 0 {
				lines = append(lines, "disallowedTools: "+yamlInlineList(spec.ToolPolicy.Deny))
			}
		}
		if len(spec.MCPServers) > 0 {
			lines = append(lines, yamlBlockList("mcpServers", spec.MCPServers)...)
		}
		if len(spec.Skills) > 0 {
			lines = append(lines, yamlBlockList("skills", spec.Skills)...)
		}
	}
	lines = append(lines, "---", "", instructions(spec), "")
	return strings.Join(lines, "\n")
}

func collectWarnings(driverType string, payload driver.AgentPayload) []string {
	warnings := make([]string, 0)
	for _, spec := range payload.Agents {
		for _, warning := range unsupportedFields(driverType, spec) {
			warnings = append(warnings, fmt.Sprintf("agent %q: %s", spec.Key, warning))
		}
	}
	sort.Strings(warnings)
	return warnings
}

func unsupportedFields(driverType string, spec driver.AgentSpec) []string {
	var warnings []string
	if len(spec.Native) > 0 {
		warnings = append(warnings, "native agent fields are not materialized by the generic adapter layout")
	}
	switch driverType {
	case "codex":
		if spec.ToolPolicy != nil {
			warnings = append(warnings, "tool policy is not mapped for Codex agents")
		}
		if strings.TrimSpace(spec.PermissionMode) != "" {
			warnings = append(warnings, "permission mode is not mapped for Codex agents")
		}
		if len(spec.MCPServers) > 0 {
			warnings = append(warnings, "MCP server references are not mapped for Codex agents")
		}
		if len(spec.Skills) > 0 {
			warnings = append(warnings, "skill references are not mapped for Codex agents")
		}
		if len(spec.Hooks) > 0 {
			warnings = append(warnings, "agent-local hooks are not mapped for Codex agents")
		}
	case "claude":
		if strings.TrimSpace(spec.SandboxMode) != "" {
			warnings = append(warnings, "sandbox mode is not mapped for Claude agents; use isolation/provider-native source when needed")
		}
		if len(spec.Hooks) > 0 {
			warnings = append(warnings, "agent-local hooks are not mapped for Claude agents")
		}
	case "cursor":
		if strings.TrimSpace(spec.Model) != "" ||
			strings.TrimSpace(spec.ReasoningEffort) != "" ||
			spec.ToolPolicy != nil ||
			strings.TrimSpace(spec.PermissionMode) != "" ||
			strings.TrimSpace(spec.SandboxMode) != "" ||
			len(spec.MCPServers) > 0 ||
			len(spec.Skills) > 0 ||
			len(spec.Hooks) > 0 {
			warnings = append(warnings, "extended agent fields are not mapped for Cursor agents")
		}
	}
	return warnings
}

func runtimeFileName(spec driver.AgentSpec, ext string) string {
	name := strings.TrimSpace(spec.RuntimeName)
	if name == "" {
		name = spec.Key
	}
	if strings.TrimSpace(spec.SourcePath) != "" && filepath.Ext(name) == "" {
		if sourceExt := filepath.Ext(spec.SourcePath); sourceExt != "" {
			ext = sourceExt
		}
	}
	base := name
	if filepath.Ext(base) != "" {
		ext = filepath.Ext(base)
		base = strings.TrimSuffix(base, ext)
	}
	return safeFileName(base) + ext
}

func agentName(spec driver.AgentSpec) string {
	name := strings.TrimSpace(spec.RuntimeName)
	if name == "" {
		name = spec.Key
	}
	if ext := filepath.Ext(name); ext != "" {
		name = strings.TrimSuffix(name, ext)
	}
	return safeFileName(name)
}

func description(spec driver.AgentSpec) string {
	if strings.TrimSpace(spec.Description) != "" {
		return strings.TrimSpace(spec.Description)
	}
	return fmt.Sprintf("SDK-managed profile agent %s", spec.Key)
}

func instructions(spec driver.AgentSpec) string {
	if strings.TrimSpace(spec.Instructions) != "" {
		return strings.TrimSpace(spec.Instructions)
	}
	if strings.TrimSpace(spec.Content) != "" {
		return strings.TrimSpace(spec.Content)
	}
	return "Follow the host-provided role description for this agent."
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
		return "agent"
	}
	return out
}

func yamlString(value string) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return `""`
	}
	return string(raw)
}

func yamlInlineList(values []string) string {
	cleaned := cleanStrings(values)
	parts := make([]string, 0, len(cleaned))
	for _, value := range cleaned {
		parts = append(parts, yamlString(value))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func yamlBlockList(name string, values []string) []string {
	cleaned := cleanStrings(values)
	if len(cleaned) == 0 {
		return nil
	}
	out := []string{name + ":"}
	for _, value := range cleaned {
		out = append(out, "  - "+yamlString(value))
	}
	return out
}

func cleanStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func anySourcePath(payload driver.AgentPayload) bool {
	for _, spec := range payload.Agents {
		if strings.TrimSpace(spec.SourcePath) != "" {
			return true
		}
	}
	return false
}

func fingerprint(spec driver.AgentSpec) string {
	raw, err := json.Marshal(spec)
	if err != nil {
		raw = []byte(spec.Key + spec.RuntimeName + spec.Instructions + spec.Content + spec.SourcePath + spec.SourceFingerprint)
	}
	if strings.TrimSpace(spec.SourcePath) != "" {
		if stat, err := os.Stat(spec.SourcePath); err == nil {
			raw = append(raw, stat.ModTime().UTC().Format(time.RFC3339Nano)...)
			raw = append(raw, fmt.Sprintf(":%d", stat.Size())...)
		}
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func cloneStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, len(values))
	copy(out, values)
	return out
}
