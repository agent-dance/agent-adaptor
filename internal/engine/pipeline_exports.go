package engine

import "context"

// This file exposes the engine operations used by the public Agent pipeline.
// The wrappers keep orchestration in the root package while preserving one
// implementation for resolution, profile snapshots, structured output,
// workspace leases, and runtime services.

// ResolveSkills runs the skill resolution algorithm — candidate
// registration, default/run absorption, batched provider fetch, required
// auto-selection, conflict detection, materialization, fingerprinting —
// against an explicit provider and materializer.
//
// Rationale for the entry: the algorithm's semantics (source labels,
// ErrSkillNotFound wording, SkillMaterializationError, sorted selection,
// SkillSyncEphemeral/Unsupported downgrade, stableHash recipe) are the
// public contract. A nil provider performs no fetch; a nil materializer uses
// the process default.
func ResolveSkills(
	ctx context.Context,
	provider SkillProvider,
	materializer SkillMaterializer,
	identity AgentIdentity,
	defaultRefs []SkillRef,
	runRefs []SkillRef,
	candidateRefs []SkillRef,
) (ResolvedSkills, []string, []Skill, error) {
	return resolveSkillsWith(ctx, provider, materializer, identity, defaultRefs, runRefs, candidateRefs)
}

// CollectSkillCandidates returns the candidate pool for Inspector.Skills and
// Agent.SelectSkills: the Agent defaults plus the provider catalogue entries.
// Catalogue errors propagate verbatim.
//
// Inspect().Skills and SelectSkills use the same pool composition.
func CollectSkillCandidates(ctx context.Context, provider SkillProvider, identity AgentIdentity, defaultSkills []SkillRef) ([]SkillRef, error) {
	return collectSkillCandidatesFrom(ctx, provider, identity, defaultSkills)
}

// BuildSkillSnapshot routes a resolved payload to the Driver's ListSkills
// when it is skill-aware, or synthesises the truthful "unsupported"
// snapshot otherwise (Supported=false, SkillSyncUnsupported, cloned
// selection/warnings, payload fingerprint).
//
// The returned snapshot reports actual materialization support.
func BuildSkillSnapshot(ctx context.Context, adapter Driver, config any, payload ResolvedSkills, selected []string, resolved []Skill, profile *ProfileSelection) (SkillSnapshot, error) {
	return buildSkillSnapshot(ctx, adapter, config, payload, selected, resolved, profile)
}

// SyncSkillSnapshot routes a resolved payload to the Driver's SyncSkills
// when it is skill-aware, with the same unsupported fallback as
// BuildSkillSnapshot. SyncProfile and SelectSkills share this operation.
func SyncSkillSnapshot(ctx context.Context, adapter Driver, config any, payload ResolvedSkills, selected []string, resolved []Skill, profile *ProfileSelection) (SkillSnapshot, error) {
	return syncSkillSnapshot(ctx, adapter, config, payload, selected, resolved, profile)
}

// SummarizeEnvironment folds raw environment checks into the aggregate
// report with fail > warn > pass precedence and canonical summaries.
// (fail > warn > pass precedence, canonical summary strings).
func SummarizeEnvironment(driverType string, checks []EnvironmentCheck) EnvironmentReport {
	return summarizeEnvironment(driverType, checks)
}

// SnapshotProfilePayload converts a computed ProfilePayload into the
// ProfileSnapshot shape ProfileState/SyncProfile return
// return for Drivers without profileResourceDriver: a PortableCore skills row
// plus honest unsupported rows (desired-but-not-observed warnings when
// synced is false, not-materialized warnings + Error when synced is true).
func SnapshotProfilePayload(driverType string, profile AgentProfile, selection *ProfileSelection, payload ProfilePayload, synced bool) ProfileSnapshot {
	return snapshotFromPayload(driverType, profile, selection, payload, synced)
}

// CloneProfileSelection deep-copies a profile selection (nil-safe).
// Drivers receive defensive copies rather than caller-owned state.
func CloneProfileSelection(sel *ProfileSelection) *ProfileSelection {
	return cloneProfileSelection(sel)
}

// ProfileResourceDriver is the optional Driver capability ProfileState and
// SyncProfile probe before falling back to payload-derived snapshots.
type ProfileResourceDriver = profileResourceDriver

// FinalizeStructuredOutput applies the post-run structured output
// contract to a driver response: suppress unrequested structured output,
// prompt-validate raw text when the source is PromptValidate, synthesise
// the "driver did not return native structured output" invalid marker,
// backfill Source/Format/Mode/SchemaHash, re-validate returned RawJSON,
// and escalate invalid output into a FailurePolicyError failure when the
// schema's OnInvalid is StructuredOutputFailRun.
func FinalizeStructuredOutput(
	schema *OutputSchema,
	source StructuredOutputSource,
	output string,
	structured *StructuredOutput,
	failure *RunFailure,
) (*StructuredOutput, *RunFailure) {
	failure = cloneRunFailure(failure)
	if schema == nil {
		return nil, failure
	}

	structured = cloneStructuredOutput(structured)
	if structured == nil {
		if source == StructuredOutputSourcePromptValidate {
			structured = validateStructuredOutput(schema, source, []byte(output))
		} else {
			structured = &StructuredOutput{
				Format:           schema.Format,
				Mode:             schema.Mode,
				Source:           source,
				Valid:            false,
				ValidationErrors: []string{"driver did not return native structured output"},
				SchemaHash:       schemaHash(schema),
			}
		}
	} else {
		if structured.Source == "" {
			structured.Source = source
		}
		if structured.Format == "" {
			structured.Format = schema.Format
		}
		if structured.Mode == "" {
			structured.Mode = schema.Mode
		}
		if structured.SchemaHash == "" {
			structured.SchemaHash = schemaHash(schema)
		}
		if len(structured.RawJSON) > 0 {
			structured = validateStructuredOutput(schema, structured.Source, structured.RawJSON)
		} else if !structured.Valid && len(structured.ValidationErrors) == 0 {
			structured.ValidationErrors = []string{"structured output RawJSON is empty"}
		}
	}

	if structured != nil && !structured.Valid && schema.OnInvalid == StructuredOutputFailRun && failure == nil {
		failure = &RunFailure{
			Code:    FailurePolicyError,
			Message: "structured output validation failed",
			Metadata: map[string]any{
				"validation_errors": append([]string(nil), structured.ValidationErrors...),
				"schema_hash":       structured.SchemaHash,
			},
		}
	}
	return structured, failure
}

// Workspace and runtime-service wiring.

// ResolveWorkspaceLease runs one WorkspaceManager.Resolve. A nil manager uses the
// passthrough manager (base CWD defaulting to os.Getwd, shared/project-primary
// request data, "workspace_manager: passthrough" metadata, stableHash lease ID
// and fingerprint).
func ResolveWorkspaceLease(ctx context.Context, manager WorkspaceManager, req WorkspaceRequest) (WorkspaceLease, error) {
	if manager == nil {
		manager = passthroughWorkspaceManager{}
	}
	return manager.Resolve(ctx, req)
}

// ReleaseWorkspaceLease runs one WorkspaceManager.Release. A nil manager is a
// no-op because nothing was leased through a manager.
func ReleaseWorkspaceLease(ctx context.Context, manager WorkspaceManager, lease WorkspaceLease, mode WorkspaceReleaseMode) error {
	if manager == nil {
		return nil
	}
	return manager.Release(ctx, lease, mode)
}

// PrepareRuntimePayload resolves one run's runtime services: clone the desired
// specs, fingerprint them, call RuntimeServiceManager.Ensure, collect the
// subprocess-only SecretEnv, and normalize the returned refs against the
// requested specs (ID/Name/URL/lifecycle/metadata backfill, status/health
// defaults, owner attribution).
//
// req.Desired is filled by the callee; a nil manager behaves like the noop default,
// which keeps "declared but unmanaged" services out of the driver payload
// instead of inventing endpoints.
func PrepareRuntimePayload(ctx context.Context, manager RuntimeServiceManager, req RuntimeServiceRequest, desired []RuntimeServiceSpec) (RuntimePayload, error) {
	return prepareRuntimePayload(ctx, manager, req, desired)
}

// ReleaseRuntimeServicesByRun runs one RuntimeServiceManager.ReleaseByRun.
// A nil manager is a no-op.
func ReleaseRuntimeServicesByRun(ctx context.Context, manager RuntimeServiceManager, runID string) error {
	if manager == nil {
		return nil
	}
	return manager.ReleaseByRun(ctx, runID)
}

// NormalizeRuntimeServiceRefs backfills and defaults a set of ensured refs
// against the requested specs, exactly like the Ensure post-processing step.
// The root pipeline uses it for refs from a RunServiceProvider attachment
// rather than from a RuntimeServiceManager, so both sources reach the driver in
// the same normalized shape.
func NormalizeRuntimeServiceRefs(requested []RuntimeServiceSpec, ensured []RuntimeServiceRef, owner AgentIdentity) []RuntimeServiceRef {
	return normalizeRuntimeServiceRefs(requested, ensured, owner)
}

// CollectRuntimeSecretEnv gathers the subprocess-only secret bindings of a set
// of refs (per-run MCP bearer tokens and friends) into the payload-level
// SecretEnv slice driverutil.RuntimeEnvBindings injects into driver env.
func CollectRuntimeSecretEnv(refs []RuntimeServiceRef) []EnvBinding {
	return collectRuntimeSecretEnv(refs)
}

// RuntimeReportsFromRefs synthesises the truthful runtime-service reports for
// drivers that ensure services but report none back.
func RuntimeReportsFromRefs(refs []RuntimeServiceRef, owner AgentIdentity) []RuntimeServiceReport {
	return runtimeReportsFromRefs(refs, owner)
}
