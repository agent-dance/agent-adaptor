package profilesnapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

func Build(driverType string, profile agentadaptor.AgentProfile, kind agentadaptor.ProfileKind, payload agentadaptor.ProfilePayload, skills agentadaptor.SkillSnapshot, synced bool) agentadaptor.ProfileSnapshot {
	return agentadaptor.ProfileSnapshot{
		DriverType:  driverType,
		Profile:     profile,
		Kind:        kind,
		Fingerprint: payload.Fingerprint,
		Warnings:    cloneStrings(payload.Warnings),
		Resources: []agentadaptor.ResourceSnapshot{
			skillResource(payload.Skills.Fingerprint, skills),
			mcpResource(payload.MCP, synced),
			unsupportedResource(agentadaptor.ProfileResourceAgents, payload.Agents.Fingerprint, agentKeys(payload.Agents), nil, synced),
			unsupportedResource(agentadaptor.ProfileResourceHooks, payload.Hooks.Fingerprint, hookKeys(payload.Hooks), nil, synced),
			unsupportedResource(agentadaptor.ProfileResourceInstructions, instructionFingerprint(payload.Instructions), instructionKeys(payload.Instructions), nil, synced),
			unsupportedResource(agentadaptor.ProfileResourceConfig, payload.Config.Fingerprint, configPatchKeys(payload.Config), cloneStrings(payload.Config.Warnings), synced),
		},
	}
}

func skillResource(fingerprint string, snapshot agentadaptor.SkillSnapshot) agentadaptor.ResourceSnapshot {
	out := agentadaptor.ResourceSnapshot{
		Kind:            agentadaptor.ProfileResourceSkills,
		Fingerprint:     fingerprint,
		Support:         agentadaptor.ProfileResourceSupportPortableCore,
		Materialization: agentadaptor.ProfileResourceMaterializationFileManaged,
		Warnings:        cloneStrings(snapshot.Warnings),
	}
	for _, entry := range snapshot.Entries {
		switch {
		case entry.Managed && (entry.State == agentadaptor.SkillStateInstalled || entry.State == agentadaptor.SkillStateConfigured):
			out.Managed = append(out.Managed, entry.Key)
		case entry.State == agentadaptor.SkillStateExternal:
			out.External = append(out.External, entry.Key)
		}
	}
	sort.Strings(out.Managed)
	sort.Strings(out.External)
	return out
}

func mcpResource(payload agentadaptor.MCPPayload, synced bool) agentadaptor.ResourceSnapshot {
	keys := mcpKeys(payload)
	out := agentadaptor.ResourceSnapshot{
		Kind:            agentadaptor.ProfileResourceMCP,
		Fingerprint:     payload.Fingerprint,
		Support:         agentadaptor.ProfileResourceSupportPortableCore,
		Materialization: agentadaptor.ProfileResourceMaterializationNotMaterialized,
		Warnings:        cloneStrings(payload.Warnings),
	}
	if len(keys) == 0 {
		return out
	}
	if synced {
		out.Managed = keys
		out.Materialization = agentadaptor.ProfileResourceMaterializationNativeManaged
		return out
	}
	out.Warnings = append(out.Warnings, "mcp resources are desired but not observed by ProfileSnapshot; call SyncProfile to materialize them")
	return out
}

func unsupportedResource(kind agentadaptor.ProfileResourceKind, fingerprint string, desired []string, warnings []string, synced bool) agentadaptor.ResourceSnapshot {
	out := agentadaptor.ResourceSnapshot{
		Kind:            kind,
		Fingerprint:     fingerprint,
		Support:         agentadaptor.ProfileResourceSupportUnsupported,
		Materialization: agentadaptor.ProfileResourceMaterializationNotMaterialized,
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

func mcpKeys(payload agentadaptor.MCPPayload) []string {
	out := make([]string, 0, len(payload.Servers))
	for _, server := range payload.Servers {
		out = append(out, server.Key)
	}
	sort.Strings(out)
	return out
}

func agentKeys(payload agentadaptor.AgentPayload) []string {
	out := make([]string, 0, len(payload.Agents))
	for _, agent := range payload.Agents {
		out = append(out, agent.Key)
	}
	sort.Strings(out)
	return out
}

func hookKeys(payload agentadaptor.HookPayload) []string {
	out := make([]string, 0, len(payload.Hooks))
	for _, hook := range payload.Hooks {
		out = append(out, hook.Key)
	}
	sort.Strings(out)
	return out
}

func configPatchKeys(payload agentadaptor.ProfileConfigPayload) []string {
	out := make([]string, 0, len(payload.Patches))
	for _, patch := range payload.Patches {
		out = append(out, patch.Key)
	}
	sort.Strings(out)
	return out
}

func instructionKeys(ref *agentadaptor.InstructionsBundleRef) []string {
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

func instructionFingerprint(ref *agentadaptor.InstructionsBundleRef) string {
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
