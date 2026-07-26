package engine

import "context"

// This file is the additive v1-facing seam for the P3 wiring wave (same
// pattern as threadsession.go for P2: reuse, not replicate). Every entry is
// a thin exported wrapper over the historical unexported semantic truth so
// the next/ facade shares byte-for-byte behavior with the legacy Core paths
// — merge rules, error text, fingerprints, fallback honesty — without next/
// needing a Core, an AgentBinding, or the root package.
//
// The legacy path keeps calling the unexported functions directly; nothing
// about its behavior changes.

// ResolveSkills runs the full v0.5 skill resolution algorithm — candidate
// registration, default/run absorption, batched provider fetch, required
// auto-selection, conflict detection, materialization, fingerprinting —
// against an explicit provider and materializer instead of a Core's fields.
//
// Rationale for the entry: the algorithm's semantics (source labels,
// ErrSkillNotFound wording, SkillMaterializationError, sorted selection,
// SkillSyncEphemeral/Unsupported downgrade, stableHash recipe) are the
// contract of the legacy Run and Admin paths; replicating them in next/
// would fork that truth. The body was lifted mechanically into
// resolveSkillsWith (skill_resolution.go) and (*Core).resolveSkills now
// delegates to it, so both facades run the identical code.
//
// A nil provider behaves like the legacy unset provider (no fetch); a nil
// materializer falls back to defaultSkillMaterializer(), exactly like the
// legacy Core field default.
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

// CollectSkillCandidates returns the admin candidate pool for ListSkills /
// SetSelectedSkills-style surfaces: the binding-inline defaults plus the
// provider's SkillCatalog entries when it implements one. Catalogue errors
// propagate verbatim.
//
// Rationale: (*Core).collectAdminCandidates now delegates to the lifted
// collectSkillCandidatesFrom; this wrapper exposes the same pool
// composition to next/ Inspect().Skills / SelectSkills without a Core.
func CollectSkillCandidates(ctx context.Context, provider SkillProvider, identity AgentIdentity, defaultSkills []SkillRef) ([]SkillRef, error) {
	return collectSkillCandidatesFrom(ctx, provider, identity, defaultSkills)
}

// BuildSkillSnapshot routes a resolved payload to the adapter's ListSkills
// when it is skill-aware, or synthesises the truthful "unsupported"
// snapshot otherwise (Supported=false, SkillSyncUnsupported, cloned
// selection/warnings, payload fingerprint).
//
// Rationale: buildSkillSnapshot (managers.go) is already a free function;
// this wrapper only exports it unchanged so next/ Inspect().Skills reports
// materialization exactly as Admin.ListSkills does.
func BuildSkillSnapshot(ctx context.Context, adapter DriverAdapter, config any, payload ResolvedSkills, selected []string, resolved []Skill, profile *ProfileSelection) (SkillSnapshot, error) {
	return buildSkillSnapshot(ctx, adapter, config, payload, selected, resolved, profile)
}

// SyncSkillSnapshot routes a resolved payload to the adapter's SyncSkills
// when it is skill-aware, with the same unsupported fallback as
// BuildSkillSnapshot. Exported unchanged for next/ SyncProfile /
// SelectSkills (legacy SetSelectedSkills semantics).
func SyncSkillSnapshot(ctx context.Context, adapter DriverAdapter, config any, payload ResolvedSkills, selected []string, resolved []Skill, profile *ProfileSelection) (SkillSnapshot, error) {
	return syncSkillSnapshot(ctx, adapter, config, payload, selected, resolved, profile)
}

// SummarizeEnvironment folds raw environment checks into the aggregate
// report exactly like the legacy Admin.CheckEnvironment fallback path
// (fail > warn > pass precedence, canonical summary strings).
//
// Rationale: summarizeEnvironment (admin_helpers.go) is the single source
// of the summary wording the baseline tests assert; the wrapper exports it
// unchanged for next/ Inspect().Environment.
func SummarizeEnvironment(driverType string, checks []EnvironmentCheck) EnvironmentReport {
	return summarizeEnvironment(driverType, checks)
}

// SnapshotProfilePayload converts a computed ProfilePayload into the
// ProfileSnapshot shape the legacy ProfileSnapshot/SyncProfile admin calls
// return for non-profileResourceDriver adapters: a PortableCore skills row
// plus honest unsupported rows (desired-but-not-observed warnings when
// synced is false, not-materialized warnings + Error when synced is true).
//
// Rationale: snapshotFromPayload (admin.go) consumes AgentDefaults only
// through profileKindForSnapshot, which reads nothing but defaults.Profile
// — an exported field — so wrapping it with AgentDefaults{Profile:
// selection} reuses the function with zero changes to admin.go.
func SnapshotProfilePayload(driverType string, profile AgentProfile, selection *ProfileSelection, payload ProfilePayload, synced bool) ProfileSnapshot {
	return snapshotFromPayload(driverType, profile, AgentDefaults{Profile: selection}, payload, synced)
}

// CloneProfileSelection deep-copies a profile selection (nil-safe).
// Exported unchanged (profile.go) so next/ can hand defensive copies to
// drivers exactly like the legacy request construction does.
func CloneProfileSelection(sel *ProfileSelection) *ProfileSelection {
	return cloneProfileSelection(sel)
}

// ProfileResourceDriver is the optional adapter capability the legacy
// ProfileSnapshot/SyncProfile admin calls probe for before falling back to
// payload-derived snapshots. Exported as an alias of the historical
// unexported interface so next/ probes the identical method set (interface
// assertions are structural, but the alias keeps one definition).
type ProfileResourceDriver = profileResourceDriver

// FinalizeStructuredOutput applies the legacy post-run structured output
// contract to a driver response: suppress unrequested structured output,
// prompt-validate raw text when the source is PromptValidate, synthesise
// the "adapter did not return native structured output" invalid marker,
// backfill Source/Format/Mode/SchemaHash, re-validate returned RawJSON,
// and escalate invalid output into a FailurePolicyError failure when the
// schema's OnInvalid is StructuredOutputFailRun.
//
// Rationale: finalizeStructuredOutput (execute.go) takes the unexported
// resolvedInvocation; constructing one in-package with just the two fields
// it reads (outputSchema, outputSource) reuses the function with zero
// changes to execute.go.
func FinalizeStructuredOutput(
	schema *OutputSchema,
	source StructuredOutputSource,
	output string,
	structured *StructuredOutput,
	failure *RunFailure,
) (*StructuredOutput, *RunFailure) {
	return finalizeStructuredOutput(
		resolvedInvocation{outputSchema: schema, outputSource: source},
		DriverRunResult{Output: output, StructuredOutput: structured, Failure: failure},
	)
}
