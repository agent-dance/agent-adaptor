package engine

// Defensive-copy and preparation operations used by the public Agent pipeline.

// --- skill helpers ----------------------------------------------------------

// CloneSkillRefs exposes cloneSkillRefs for the root package.
func CloneSkillRefs(values []SkillRef) []SkillRef { return cloneSkillRefs(values) }

// CloneResolvedSkills exposes cloneResolvedSkills for the root package.
func CloneResolvedSkills(r ResolvedSkills) ResolvedSkills { return cloneResolvedSkills(r) }

// NormalizeSkillKey exposes normalizeSkillKey for the root package.
func NormalizeSkillKey(value string) string { return normalizeSkillKey(value) }

// --- MCP ---------------------------------------------------------------------

// CloneMCPServerSpecs exposes cloneMCPServerSpecs for the root package.
func CloneMCPServerSpecs(values []MCPServerSpec) []MCPServerSpec {
	return cloneMCPServerSpecs(values)
}

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

// ValidateThreadSessionDriver validates the explicit resume capability and
// returns the stable codec name without touching a Thread store.
func ValidateThreadSessionDriver(driver Driver) (string, error) {
	codec, err := resumeSessionCodecFor(driver)
	if err != nil {
		return "", err
	}
	return codec.Name(), nil
}

// NormalizeSessionState exposes normalizeSessionState for the root package.
func NormalizeSessionState(driver Driver, state *SessionState) *SessionState {
	return normalizeSessionState(driver, state)
}

// NormalizeResumableSessionState validates and normalizes a stored checkpoint
// through the configured Driver's required Thread codec.
func NormalizeResumableSessionState(driver Driver, state *SessionState) (*SessionState, error) {
	codec, err := resumeSessionCodecFor(driver)
	if err != nil {
		return nil, err
	}
	return normalizeResumableSessionState(codec, state)
}

// --- profile resources ----------------------------------------------------------

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
