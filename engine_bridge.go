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
