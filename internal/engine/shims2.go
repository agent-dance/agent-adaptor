package engine

// Batch-2 exported shims: contract-type helpers the root package still calls
// while the pipeline core remains in the root package (removed or slimmed in
// batch 3 as callers move into engine).

// --- skill helpers ----------------------------------------------------------

// CloneSkill exposes cloneSkill for the root package.
func CloneSkill(s Skill) Skill { return cloneSkill(s) }

// CloneSkills exposes cloneSkills for the root package.
func CloneSkills(values []Skill) []Skill { return cloneSkills(values) }

// CloneSkillRefs exposes cloneSkillRefs for the root package.
func CloneSkillRefs(values []SkillRef) []SkillRef { return cloneSkillRefs(values) }

// CloneResolvedSkills exposes cloneResolvedSkills for the root package.
func CloneResolvedSkills(r ResolvedSkills) ResolvedSkills { return cloneResolvedSkills(r) }

// NormalizeSkillKey exposes normalizeSkillKey for the root package.
func NormalizeSkillKey(value string) string { return normalizeSkillKey(value) }

// SkillsEquivalent exposes skillsEquivalent for the root package.
func SkillsEquivalent(a, b Skill) bool { return skillsEquivalent(a, b) }

// SkillDiffDetail exposes skillDiffDetail for the root package.
func SkillDiffDetail(a, b Skill) string { return skillDiffDetail(a, b) }

// DefaultSkillRuntimeName exposes defaultSkillRuntimeName for the root package.
func DefaultSkillRuntimeName(s Skill) string { return defaultSkillRuntimeName(s) }

// CleanSkillPath exposes cleanSkillPath for the root package.
func CleanSkillPath(p string) string { return cleanSkillPath(p) }

// --- MCP ---------------------------------------------------------------------

// CloneMCPConfig exposes cloneMCPConfig for the root package.
func CloneMCPConfig(cfg *MCPConfig) *MCPConfig { return cloneMCPConfig(cfg) }

// CloneMCPServerSpecs exposes cloneMCPServerSpecs for the root package.
func CloneMCPServerSpecs(values []MCPServerSpec) []MCPServerSpec {
	return cloneMCPServerSpecs(values)
}

// CloneMCPPayload exposes cloneMCPPayload for the root package.
func CloneMCPPayload(payload MCPPayload) MCPPayload { return cloneMCPPayload(payload) }

// ResolveMCPPayload exposes resolveMCPPayload for the root package.
func ResolveMCPPayload(defaults, override *MCPConfig, caps MCPCapability) (MCPPayload, error) {
	return resolveMCPPayload(defaults, override, caps)
}

// ResolveMCPPayloadWithRuntime exposes resolveMCPPayloadWithRuntime for the
// root package.
func ResolveMCPPayloadWithRuntime(defaults, override *MCPConfig, refs []RuntimeServiceRef, caps MCPCapability) (MCPPayload, error) {
	return resolveMCPPayloadWithRuntime(defaults, override, refs, caps)
}

// --- session codec -------------------------------------------------------------

// NormalizeSessionState exposes normalizeSessionState for the root package.
func NormalizeSessionState(driver DriverAdapter, state *DriverSessionState) *DriverSessionState {
	return normalizeSessionState(driver, state)
}

// SessionDisplayID exposes sessionDisplayID for the root package.
func SessionDisplayID(driver DriverAdapter, state *DriverSessionState) string {
	return sessionDisplayID(driver, state)
}

// --- profile resources ----------------------------------------------------------

// CloneProfileResources exposes cloneProfileResources for the root package.
func CloneProfileResources(resources ProfileResources) ProfileResources {
	return cloneProfileResources(resources)
}

// CloneProfilePayload exposes cloneProfilePayload for the root package.
func CloneProfilePayload(payload ProfilePayload) ProfilePayload { return cloneProfilePayload(payload) }

// CloneAgentSpecs exposes cloneAgentSpecs for the root package.
func CloneAgentSpecs(values []AgentSpec) []AgentSpec { return cloneAgentSpecs(values) }

// CloneHookSpecs exposes cloneHookSpecs for the root package.
func CloneHookSpecs(values []HookSpec) []HookSpec { return cloneHookSpecs(values) }

// CloneProfileConfigPatches exposes cloneProfileConfigPatches for the root package.
func CloneProfileConfigPatches(values []ProfileConfigPatch) []ProfileConfigPatch {
	return cloneProfileConfigPatches(values)
}

// CloneInstructions exposes cloneInstructions for the root package.
func CloneInstructions(ref *InstructionsBundleRef) *InstructionsBundleRef {
	return cloneInstructions(ref)
}

// CloneBool exposes cloneBool for the root package.
func CloneBool(b *bool) *bool { return cloneBool(b) }

// PrepareAgentPayload exposes prepareAgentPayload for the root package.
func PrepareAgentPayload(specs []AgentSpec) (AgentPayload, error) { return prepareAgentPayload(specs) }

// PrepareHookPayload exposes prepareHookPayload for the root package.
func PrepareHookPayload(specs []HookSpec) (HookPayload, error) { return prepareHookPayload(specs) }

// PrepareProfileConfigPayload exposes prepareProfileConfigPayload for the root package.
func PrepareProfileConfigPayload(patches []ProfileConfigPatch) (ProfileConfigPayload, error) {
	return prepareProfileConfigPayload(patches)
}

// PrepareInstructionsBundle exposes prepareInstructionsBundle for the root package.
func PrepareInstructionsBundle(ref *InstructionsBundleRef) (*InstructionsBundleRef, error) {
	return prepareInstructionsBundle(ref)
}

// BuildProfilePayload exposes buildProfilePayload for the root package.
func BuildProfilePayload(skills ResolvedSkills, mcp MCPPayload, agents AgentPayload, hooks HookPayload, instructions *InstructionsBundleRef, config ProfileConfigPayload, declared ProfileResourceDeclarations) ProfilePayload {
	return buildProfilePayload(skills, mcp, agents, hooks, instructions, config, declared)
}

// InstructionFingerprint exposes instructionFingerprint for the root package.
func InstructionFingerprint(ref *InstructionsBundleRef) string { return instructionFingerprint(ref) }

// MCPKeys exposes mcpKeys for the root package.
func MCPKeys(payload MCPPayload) []string { return mcpKeys(payload) }

// AgentKeys exposes agentKeys for the root package.
func AgentKeys(payload AgentPayload) []string { return agentKeys(payload) }

// HookKeys exposes hookKeys for the root package.
func HookKeys(payload HookPayload) []string { return hookKeys(payload) }

// InstructionKeys exposes instructionKeys for the root package.
func InstructionKeys(ref *InstructionsBundleRef) []string { return instructionKeys(ref) }

// ConfigPatchKeys exposes configPatchKeys for the root package.
func ConfigPatchKeys(payload ProfileConfigPayload) []string { return configPatchKeys(payload) }
