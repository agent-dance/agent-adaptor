package engine

import "bytes"

// Exported shims over engine-internal helpers.
//
// The root package keeps a handful of components in place (the default skill
// materializer cluster backed by archive_*.go, the dualSink/HITL dispatcher,
// the RunOption setters, and internal test bridges). Those root files call
// the helpers below through same-named unexported delegates, so the moved
// pipeline code keeps its original identifiers and behavior.

// StableHash exposes stableHash for the root package.
func StableHash(parts ...any) string { return stableHash(parts...) }

// CloneStrings exposes cloneStrings for the root package.
func CloneStrings(values []string) []string { return cloneStrings(values) }

// CloneStringMap exposes cloneStringMap for the root package.
func CloneStringMap(values map[string]string) map[string]string { return cloneStringMap(values) }

// CloneAnyMap exposes cloneAnyMap for the root package.
func CloneAnyMap(values map[string]any) map[string]any { return cloneAnyMap(values) }

// CloneWorkspaceRuntimeConfig exposes cloneWorkspaceRuntimeConfig for the root package.
func CloneWorkspaceRuntimeConfig(cfg *WorkspaceRuntimeConfig) *WorkspaceRuntimeConfig {
	return cloneWorkspaceRuntimeConfig(cfg)
}

// CloneRuntimeServiceSpecs exposes cloneRuntimeServiceSpecs for the root package.
func CloneRuntimeServiceSpecs(values []RuntimeServiceSpec) []RuntimeServiceSpec {
	return cloneRuntimeServiceSpecs(values)
}

// CloneRuntimeServiceRefs exposes cloneRuntimeServiceRefs for the root package.
func CloneRuntimeServiceRefs(values []RuntimeServiceRef) []RuntimeServiceRef {
	return cloneRuntimeServiceRefs(values)
}

// CloneRuntimePayload exposes cloneRuntimePayload for the root package.
func CloneRuntimePayload(payload RuntimePayload) RuntimePayload { return cloneRuntimePayload(payload) }

// CloneRuntimeServiceReports exposes cloneRuntimeServiceReports for the root package.
func CloneRuntimeServiceReports(values []RuntimeServiceReport) []RuntimeServiceReport {
	return cloneRuntimeServiceReports(values)
}

// CloneTranscriptItems exposes cloneTranscriptItems for the root package.
func CloneTranscriptItems(values []TranscriptItem) []TranscriptItem {
	return cloneTranscriptItems(values)
}

// CloneTranscriptItem exposes cloneTranscriptItem for the root package.
func CloneTranscriptItem(value TranscriptItem) TranscriptItem { return cloneTranscriptItem(value) }

// CloneUsagePointer exposes cloneUsagePointer for the root package.
func CloneUsagePointer(value *Usage) *Usage { return cloneUsagePointer(value) }

// CloneRawStreams exposes cloneRawStreams for the root package.
func CloneRawStreams(value *RawStreams) *RawStreams { return cloneRawStreams(value) }

// CloneRunQuestion exposes cloneRunQuestion for the root package.
func CloneRunQuestion(question *RunQuestion) *RunQuestion { return cloneRunQuestion(question) }

// CloneRunFailure exposes cloneRunFailure for the root package.
func CloneRunFailure(failure *RunFailure) *RunFailure { return cloneRunFailure(failure) }

// CloneDecisionRequest exposes cloneDecisionRequest for the root package.
func CloneDecisionRequest(req *DecisionRequest) *DecisionRequest { return cloneDecisionRequest(req) }

// CloneFloat64Pointer exposes cloneFloat64Pointer for the root package.
func CloneFloat64Pointer(value *float64) *float64 { return cloneFloat64Pointer(value) }

// CloneConfigSchema exposes cloneConfigSchema for the root package.
func CloneConfigSchema(schema *ConfigSchema) *ConfigSchema { return cloneConfigSchema(schema) }

// CloneEnvBindings exposes cloneEnvBindings for the root package.
func CloneEnvBindings(values []EnvBinding) []EnvBinding { return cloneEnvBindings(values) }

// WriteCanonicalJSON exposes writeCanonicalJSON for the root package.
func WriteCanonicalJSON(buf *bytes.Buffer, v any) error { return writeCanonicalJSON(buf, v) }

// MergeStringMaps exposes mergeStringMaps for the root package.
func MergeStringMaps(parts ...map[string]string) map[string]string { return mergeStringMaps(parts...) }

// EnsureBaseCWD exposes ensureBaseCWD for the root package.
func EnsureBaseCWD(cwd string) string { return ensureBaseCWD(cwd) }

// ExtractCommonConfig exposes extractCommonConfig for the root package.
func ExtractCommonConfig(cfg any) CommonConfig { return extractCommonConfig(cfg) }

// SpecWorkspaceRequest exposes the closed WorkspaceSpec sum-type's normalized
// request data for the root package (WorkspaceSpec.workspaceRequest is
// unexported and therefore only callable from inside engine).
func SpecWorkspaceRequest(spec WorkspaceSpec) WorkspaceRequestData {
	return spec.workspaceRequest()
}
