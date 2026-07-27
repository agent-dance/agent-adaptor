package profilesnapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
)

func Build(driverType string, profile driver.AgentProfile, kind engine.ProfileKind, payload driver.ProfilePayload, skills driver.SkillSnapshot, synced bool) engine.ProfileSnapshot {
	return engine.ProfileSnapshot{
		DriverType:  driverType,
		Profile:     profile,
		Kind:        kind,
		Fingerprint: payload.Fingerprint,
		Warnings:    cloneStrings(payload.Warnings),
		Resources: []engine.ResourceSnapshot{
			skillResource(payload.Skills.Fingerprint, skills),
			mcpResource(payload.MCP, synced),
			unsupportedResource(engine.ProfileResourceAgents, payload.Agents.Fingerprint, agentKeys(payload.Agents), nil, synced),
			unsupportedResource(engine.ProfileResourceHooks, payload.Hooks.Fingerprint, hookKeys(payload.Hooks), nil, synced),
			unsupportedResource(engine.ProfileResourceInstructions, instructionFingerprint(payload.Instructions), instructionKeys(payload.Instructions), nil, synced),
			unsupportedResource(engine.ProfileResourceConfig, payload.Config.Fingerprint, configPatchKeys(payload.Config), cloneStrings(payload.Config.Warnings), synced),
		},
	}
}

func skillResource(fingerprint string, snapshot driver.SkillSnapshot) engine.ResourceSnapshot {
	out := engine.ResourceSnapshot{
		Kind:            engine.ProfileResourceSkills,
		Fingerprint:     fingerprint,
		Support:         engine.ProfileResourceSupportPortableCore,
		Materialization: engine.ProfileResourceMaterializationFileManaged,
		Warnings:        cloneStrings(snapshot.Warnings),
	}
	for _, entry := range snapshot.Entries {
		switch {
		case entry.Managed && (entry.State == driver.SkillStateInstalled || entry.State == driver.SkillStateConfigured):
			out.Managed = append(out.Managed, entry.Key)
		case entry.State == driver.SkillStateExternal:
			out.External = append(out.External, entry.Key)
		}
	}
	sort.Strings(out.Managed)
	sort.Strings(out.External)
	return out
}

func mcpResource(payload driver.MCPPayload, synced bool) engine.ResourceSnapshot {
	keys := mcpKeys(payload)
	out := engine.ResourceSnapshot{
		Kind:            engine.ProfileResourceMCP,
		Fingerprint:     payload.Fingerprint,
		Support:         engine.ProfileResourceSupportPortableCore,
		Materialization: engine.ProfileResourceMaterializationNotMaterialized,
		Warnings:        cloneStrings(payload.Warnings),
	}
	if len(keys) == 0 {
		return out
	}
	if synced {
		out.Managed = keys
		out.Materialization = engine.ProfileResourceMaterializationNativeManaged
		return out
	}
	out.Warnings = append(out.Warnings, "mcp resources are desired but not observed by ProfileSnapshot; call SyncProfile to materialize them")
	return out
}

func unsupportedResource(kind engine.ProfileResourceKind, fingerprint string, desired []string, warnings []string, synced bool) engine.ResourceSnapshot {
	out := engine.ResourceSnapshot{
		Kind:            kind,
		Fingerprint:     fingerprint,
		Support:         engine.ProfileResourceSupportUnsupported,
		Materialization: engine.ProfileResourceMaterializationNotMaterialized,
		Warnings:        cloneStrings(warnings),
	}
	if len(desired) == 0 {
		return out
	}
	message := fmt.Sprintf("%s resources are not materialized by this adapter yet", kind)
	out.Warnings = append(out.Warnings, message)
	if synced {
		out.Error = message
	}
	return out
}

func mcpKeys(payload driver.MCPPayload) []string {
	out := make([]string, 0, len(payload.Servers))
	for _, server := range payload.Servers {
		out = append(out, server.Key)
	}
	sort.Strings(out)
	return out
}

func agentKeys(payload driver.AgentPayload) []string {
	out := make([]string, 0, len(payload.Agents))
	for _, agent := range payload.Agents {
		out = append(out, agent.Key)
	}
	sort.Strings(out)
	return out
}

func hookKeys(payload driver.HookPayload) []string {
	out := make([]string, 0, len(payload.Hooks))
	for _, hook := range payload.Hooks {
		out = append(out, hook.Key)
	}
	sort.Strings(out)
	return out
}

func configPatchKeys(payload driver.ProfileConfigPayload) []string {
	out := make([]string, 0, len(payload.Patches))
	for _, patch := range payload.Patches {
		out = append(out, patch.Key)
	}
	sort.Strings(out)
	return out
}

func instructionKeys(ref *driver.InstructionsBundleRef) []string {
	if ref == nil {
		return nil
	}
	if strings.TrimSpace(ref.ID) != "" {
		return []string{strings.TrimSpace(ref.ID)}
	}
	if strings.TrimSpace(ref.Path) != "" {
		return []string{strings.TrimSpace(ref.Path)}
	}
	if strings.TrimSpace(ref.Fingerprint) != "" {
		return []string{strings.TrimSpace(ref.Fingerprint)}
	}
	if strings.TrimSpace(ref.Content) != "" {
		return []string{"inline-instructions"}
	}
	return []string{"instructions"}
}

func instructionFingerprint(ref *driver.InstructionsBundleRef) string {
	if ref == nil {
		return ""
	}
	if ref.Fingerprint != "" {
		return ref.Fingerprint
	}
	content := ref.Content
	if strings.TrimSpace(ref.Path) != "" && strings.TrimSpace(content) == "" {
		if raw, err := os.ReadFile(strings.TrimSpace(ref.Path)); err == nil {
			content = string(raw)
		}
	}
	return stableHash("instructions", ref.ID, ref.Path, content, ref.Scope, ref.Mode, ref.Native)
}

func stableHash(parts ...any) string {
	raw, err := json.Marshal(parts)
	if err != nil {
		raw = []byte(fmt.Sprint(parts...))
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func cloneStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	return append([]string(nil), values...)
}
