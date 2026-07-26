package agentadaptor

// Unexported same-name delegates for helpers that moved into internal/engine.
//
// Root files that stay in this package (the default skill materializer
// cluster backed by archive_*.go, the dualSink/HITL dispatcher, and the
// RunOption setters) keep calling these helpers by their historical names;
// the bodies now live in engine. Only delegates with remaining root-package
// callers are kept — everything else is reached inside engine directly.

import "github.com/agent-dance/agent-adaptor/internal/engine"

func stableHash(parts ...any) string { return engine.StableHash(parts...) }

func cloneAnyMap(values map[string]any) map[string]any { return engine.CloneAnyMap(values) }

func cloneRuntimeServiceSpecs(values []RuntimeServiceSpec) []RuntimeServiceSpec {
	return engine.CloneRuntimeServiceSpecs(values)
}

func cloneTranscriptItem(value TranscriptItem) TranscriptItem {
	return engine.CloneTranscriptItem(value)
}

func cloneRunFailure(failure *RunFailure) *RunFailure { return engine.CloneRunFailure(failure) }

func cloneDecisionRequest(req *DecisionRequest) *DecisionRequest {
	return engine.CloneDecisionRequest(req)
}

// --- skill helpers ------------------------------------------------------------

func cloneSkill(s Skill) Skill { return engine.CloneSkill(s) }

func cloneSkillRefs(values []SkillRef) []SkillRef { return engine.CloneSkillRefs(values) }

func normalizeSkillKey(value string) string { return engine.NormalizeSkillKey(value) }

func skillsEquivalent(a, b Skill) bool { return engine.SkillsEquivalent(a, b) }

func skillDiffDetail(a, b Skill) string { return engine.SkillDiffDetail(a, b) }

func defaultSkillRuntimeName(s Skill) string { return engine.DefaultSkillRuntimeName(s) }

func cleanSkillPath(p string) string { return engine.CleanSkillPath(p) }

// --- MCP ------------------------------------------------------------------------

func cloneMCPServerSpecs(values []MCPServerSpec) []MCPServerSpec {
	return engine.CloneMCPServerSpecs(values)
}

// --- profile resources ------------------------------------------------------------

func cloneProfileResources(resources ProfileResources) ProfileResources {
	return engine.CloneProfileResources(resources)
}

func cloneAgentSpecs(values []AgentSpec) []AgentSpec { return engine.CloneAgentSpecs(values) }

func cloneHookSpecs(values []HookSpec) []HookSpec { return engine.CloneHookSpecs(values) }

func cloneProfileConfigPatches(values []ProfileConfigPatch) []ProfileConfigPatch {
	return engine.CloneProfileConfigPatches(values)
}

func cloneInstructions(ref *InstructionsBundleRef) *InstructionsBundleRef {
	return engine.CloneInstructions(ref)
}
