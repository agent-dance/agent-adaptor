package agentadaptor

// Unexported same-name delegates for helpers that moved into internal/engine.
//
// Root files that stay in this package (the default skill materializer
// cluster backed by archive_*.go, the dualSink/HITL dispatcher, RunOption
// setters, and pipeline files pending later extraction batches) keep calling
// these helpers by their historical names; the bodies now live in engine.

import (
	"bytes"

	"github.com/agent-dance/agent-adaptor/internal/engine"
)

func stableHash(parts ...any) string { return engine.StableHash(parts...) }

func writeCanonicalJSON(buf *bytes.Buffer, v any) error { return engine.WriteCanonicalJSON(buf, v) }

func cloneStrings(values []string) []string { return engine.CloneStrings(values) }

func cloneStringMap(values map[string]string) map[string]string {
	return engine.CloneStringMap(values)
}

func cloneAnyMap(values map[string]any) map[string]any { return engine.CloneAnyMap(values) }

func cloneWorkspaceRuntimeConfig(cfg *WorkspaceRuntimeConfig) *WorkspaceRuntimeConfig {
	return engine.CloneWorkspaceRuntimeConfig(cfg)
}

func cloneRuntimeServiceSpecs(values []RuntimeServiceSpec) []RuntimeServiceSpec {
	return engine.CloneRuntimeServiceSpecs(values)
}

func cloneRuntimeServiceRefs(values []RuntimeServiceRef) []RuntimeServiceRef {
	return engine.CloneRuntimeServiceRefs(values)
}

func cloneRuntimePayload(payload RuntimePayload) RuntimePayload {
	return engine.CloneRuntimePayload(payload)
}

func cloneRuntimeServiceReports(values []RuntimeServiceReport) []RuntimeServiceReport {
	return engine.CloneRuntimeServiceReports(values)
}

func cloneTranscriptItems(values []TranscriptItem) []TranscriptItem {
	return engine.CloneTranscriptItems(values)
}

func cloneTranscriptItem(value TranscriptItem) TranscriptItem {
	return engine.CloneTranscriptItem(value)
}

func cloneUsagePointer(value *Usage) *Usage { return engine.CloneUsagePointer(value) }

func cloneRawStreams(value *RawStreams) *RawStreams { return engine.CloneRawStreams(value) }

func cloneRunQuestion(question *RunQuestion) *RunQuestion {
	return engine.CloneRunQuestion(question)
}

func cloneRunFailure(failure *RunFailure) *RunFailure { return engine.CloneRunFailure(failure) }

func cloneDecisionRequest(req *DecisionRequest) *DecisionRequest {
	return engine.CloneDecisionRequest(req)
}

func cloneFloat64Pointer(value *float64) *float64 { return engine.CloneFloat64Pointer(value) }

func cloneConfigSchema(schema *ConfigSchema) *ConfigSchema { return engine.CloneConfigSchema(schema) }

func cloneEnvBindings(values []EnvBinding) []EnvBinding { return engine.CloneEnvBindings(values) }

func mergeStringMaps(parts ...map[string]string) map[string]string {
	return engine.MergeStringMaps(parts...)
}

func ensureBaseCWD(cwd string) string { return engine.EnsureBaseCWD(cwd) }

func extractCommonConfig(cfg any) CommonConfig { return engine.ExtractCommonConfig(cfg) }

// --- skill helpers (moved in batch 2) ---------------------------------------

func cloneSkill(s Skill) Skill { return engine.CloneSkill(s) }

func cloneSkills(values []Skill) []Skill { return engine.CloneSkills(values) }

func cloneSkillRefs(values []SkillRef) []SkillRef { return engine.CloneSkillRefs(values) }

func cloneResolvedSkills(r ResolvedSkills) ResolvedSkills { return engine.CloneResolvedSkills(r) }

func normalizeSkillKey(value string) string { return engine.NormalizeSkillKey(value) }

func skillsEquivalent(a, b Skill) bool { return engine.SkillsEquivalent(a, b) }

func skillDiffDetail(a, b Skill) string { return engine.SkillDiffDetail(a, b) }

func defaultSkillRuntimeName(s Skill) string { return engine.DefaultSkillRuntimeName(s) }

func cleanSkillPath(p string) string { return engine.CleanSkillPath(p) }

// --- MCP (moved in batch 2) ---------------------------------------------------

func cloneMCPConfig(cfg *MCPConfig) *MCPConfig { return engine.CloneMCPConfig(cfg) }

func cloneMCPServerSpecs(values []MCPServerSpec) []MCPServerSpec {
	return engine.CloneMCPServerSpecs(values)
}

func cloneMCPPayload(payload MCPPayload) MCPPayload { return engine.CloneMCPPayload(payload) }

func resolveMCPPayload(defaults, override *MCPConfig, caps MCPCapability) (MCPPayload, error) {
	return engine.ResolveMCPPayload(defaults, override, caps)
}

func resolveMCPPayloadWithRuntime(defaults, override *MCPConfig, refs []RuntimeServiceRef, caps MCPCapability) (MCPPayload, error) {
	return engine.ResolveMCPPayloadWithRuntime(defaults, override, refs, caps)
}

// --- session codec (moved in batch 2) ------------------------------------------

func normalizeSessionState(driver DriverAdapter, state *DriverSessionState) *DriverSessionState {
	return engine.NormalizeSessionState(driver, state)
}

func sessionDisplayID(driver DriverAdapter, state *DriverSessionState) string {
	return engine.SessionDisplayID(driver, state)
}

// --- profile resources (moved in batch 2) ---------------------------------------

func cloneProfileResources(resources ProfileResources) ProfileResources {
	return engine.CloneProfileResources(resources)
}

func cloneProfilePayload(payload ProfilePayload) ProfilePayload {
	return engine.CloneProfilePayload(payload)
}

func cloneAgentSpecs(values []AgentSpec) []AgentSpec { return engine.CloneAgentSpecs(values) }

func cloneHookSpecs(values []HookSpec) []HookSpec { return engine.CloneHookSpecs(values) }

func cloneProfileConfigPatches(values []ProfileConfigPatch) []ProfileConfigPatch {
	return engine.CloneProfileConfigPatches(values)
}

func cloneInstructions(ref *InstructionsBundleRef) *InstructionsBundleRef {
	return engine.CloneInstructions(ref)
}

func cloneBool(b *bool) *bool { return engine.CloneBool(b) }

func prepareAgentPayload(specs []AgentSpec) (AgentPayload, error) {
	return engine.PrepareAgentPayload(specs)
}

func prepareHookPayload(specs []HookSpec) (HookPayload, error) {
	return engine.PrepareHookPayload(specs)
}

func prepareProfileConfigPayload(patches []ProfileConfigPatch) (ProfileConfigPayload, error) {
	return engine.PrepareProfileConfigPayload(patches)
}

func prepareInstructionsBundle(ref *InstructionsBundleRef) (*InstructionsBundleRef, error) {
	return engine.PrepareInstructionsBundle(ref)
}

func buildProfilePayload(skills ResolvedSkills, mcp MCPPayload, agents AgentPayload, hooks HookPayload, instructions *InstructionsBundleRef, config ProfileConfigPayload, declared ProfileResourceDeclarations) ProfilePayload {
	return engine.BuildProfilePayload(skills, mcp, agents, hooks, instructions, config, declared)
}

func instructionFingerprint(ref *InstructionsBundleRef) string {
	return engine.InstructionFingerprint(ref)
}

func mcpKeys(payload MCPPayload) []string { return engine.MCPKeys(payload) }

func agentKeys(payload AgentPayload) []string { return engine.AgentKeys(payload) }

func hookKeys(payload HookPayload) []string { return engine.HookKeys(payload) }

func instructionKeys(ref *InstructionsBundleRef) []string { return engine.InstructionKeys(ref) }

func configPatchKeys(payload ProfileConfigPayload) []string { return engine.ConfigPatchKeys(payload) }

// --- structured output (moved in batch 2) ----------------------------------------

func cloneOutputSchema(schema *OutputSchema) *OutputSchema { return engine.CloneOutputSchema(schema) }

func cloneStructuredOutput(value *StructuredOutput) *StructuredOutput {
	return engine.CloneStructuredOutput(value)
}

func normalizeOutputSchema(schema *OutputSchema) (*OutputSchema, error) {
	return engine.NormalizeOutputSchema(schema)
}

func resolveStructuredOutputSource(desc DriverDescriptor, schema *OutputSchema, streaming bool, policy RunPolicy) (StructuredOutputSource, error) {
	return engine.ResolveStructuredOutputSource(desc, schema, streaming, policy)
}

func validateStructuredOutput(schema *OutputSchema, source StructuredOutputSource, raw []byte) *StructuredOutput {
	return engine.ValidateStructuredOutput(schema, source, raw)
}

func schemaHash(schema *OutputSchema) string { return engine.SchemaHash(schema) }

func structuredOutputPromptInstruction(schema *OutputSchema) string {
	return engine.StructuredOutputPromptInstruction(schema)
}

// --- transitional: still root-owned, pending batch 3 ------------------------------

// profileDeclarationsFromDefaults reads AgentDefaults.profileDeclared, which
// is only visible in this package until AgentDefaults moves to engine in
// batch 3.
func profileDeclarationsFromDefaults(defaults AgentDefaults) ProfileResourceDeclarations {
	declared := defaults.profileDeclared
	if len(defaults.Agents) > 0 {
		declared.Agents = true
	}
	if len(defaults.Hooks) > 0 {
		declared.Hooks = true
	}
	if len(defaults.ProfileConfig) > 0 {
		declared.Config = true
	}
	if defaults.Instructions != nil {
		declared.Instructions = true
	}
	return declared
}
