package adaptor

import (
	"context"
	"slices"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
)

// This file is the P3.6 inspection surface (design §2.9): a read-only panel
// behind Agent.Inspect() plus the three profile verbs on Agent itself
// (ProfileState / SyncProfile / SelectSkills). Semantics mirror the legacy
// Admin implementation (internal/engine/admin.go) block for block — the same
// probe-interface assertions, the same honest fallbacks when a driver does
// not implement a probe, and the same truthful materialization reporting in
// snapshots (a desired-but-unmaterialized resource is a warning/error, never
// silently reported as applied).
//
// Differences from legacy Admin, by design:
//   - no registry/name — the panel inspects this one Agent;
//   - config is always nil — v1 drivers carry their own config internally
//     (same rule as the run path in buildRequest);
//   - the SelectSkills override lives on the Agent (mutex-guarded), not in a
//     central SDK map.

// Consumer-facing aliases for driver-owned inspection reports. Profile
// snapshot values are declared below as root-owned DTOs: an application-facing
// report must never expose the migration engine's concrete types.
type (
	// EnvironmentReport is the result of an environment health check.
	EnvironmentReport = driver.EnvironmentReport
	// EnvironmentCheck is a single environment check line item.
	EnvironmentCheck = driver.EnvironmentCheck
	// ModelInfo describes one model the agent can run.
	ModelInfo = driver.ModelInfo
	// QuotaReport is the live quota/billing snapshot (Available=false when
	// the driver cannot observe quota).
	QuotaReport = driver.QuotaReport
	// ConfigSchema describes the driver's configuration surface.
	ConfigSchema = driver.ConfigSchema
	// SkillSnapshot reports the resolved skill set and its sync state.
	SkillSnapshot = driver.SkillSnapshot
	// AgentProfile is the driver-local profile report.
	AgentProfile = driver.AgentProfile
)

// ProfileKind classifies where the effective provider profile lives.
type ProfileKind string

const (
	ProfileKindShared      ProfileKind = "shared"
	ProfileKindHostManaged ProfileKind = "host_managed"
)

// ProfileResourceKind names one provider-visible resource family.
type ProfileResourceKind string

const (
	ProfileResourceSkills       ProfileResourceKind = "skills"
	ProfileResourceMCP          ProfileResourceKind = "mcp"
	ProfileResourceAgents       ProfileResourceKind = "agents"
	ProfileResourceHooks        ProfileResourceKind = "hooks"
	ProfileResourceInstructions ProfileResourceKind = "instructions"
	ProfileResourceConfig       ProfileResourceKind = "config"
)

// ProfileResourceSupport describes how portable a resource is for the bound
// driver.
type ProfileResourceSupport string

const (
	ProfileResourceSupportPortableCore     ProfileResourceSupport = "portable_core"
	ProfileResourceSupportPortableExtended ProfileResourceSupport = "portable_extended"
	ProfileResourceSupportNativeEscape     ProfileResourceSupport = "native_escape"
	ProfileResourceSupportFallback         ProfileResourceSupport = "fallback"
	ProfileResourceSupportUnsupported      ProfileResourceSupport = "unsupported"
)

// ProfileResourceMaterialization describes how a desired resource became
// provider-visible.
type ProfileResourceMaterialization string

const (
	ProfileResourceMaterializationNativeManaged   ProfileResourceMaterialization = "native_managed"
	ProfileResourceMaterializationFileManaged     ProfileResourceMaterialization = "file_managed"
	ProfileResourceMaterializationPromptInjected  ProfileResourceMaterialization = "prompt_injected"
	ProfileResourceMaterializationFallback        ProfileResourceMaterialization = "fallback"
	ProfileResourceMaterializationNotMaterialized ProfileResourceMaterialization = "not_materialized"
)

// ResourceSnapshot is one resource row inside a [ProfileSnapshot].
type ResourceSnapshot struct {
	Kind            ProfileResourceKind
	Fingerprint     string
	Managed         []string
	External        []string
	Support         ProfileResourceSupport
	Materialization ProfileResourceMaterialization
	Warnings        []string
	Error           string
}

// ProfileSnapshot reports the desired versus observed profile resource state
// returned by [Agent.ProfileState] and [Agent.SyncProfile].
type ProfileSnapshot struct {
	DriverType  string
	Profile     AgentProfile
	Kind        ProfileKind
	Fingerprint string
	Resources   []ResourceSnapshot
	Warnings    []string
}

func profileSnapshotFromEngine(snapshot engine.ProfileSnapshot) ProfileSnapshot {
	resources := make([]ResourceSnapshot, len(snapshot.Resources))
	for i, resource := range snapshot.Resources {
		resources[i] = ResourceSnapshot{
			Kind:            ProfileResourceKind(resource.Kind),
			Fingerprint:     resource.Fingerprint,
			Managed:         slices.Clone(resource.Managed),
			External:        slices.Clone(resource.External),
			Support:         ProfileResourceSupport(resource.Support),
			Materialization: ProfileResourceMaterialization(resource.Materialization),
			Warnings:        slices.Clone(resource.Warnings),
			Error:           resource.Error,
		}
	}
	if snapshot.Resources == nil {
		resources = nil
	}
	return ProfileSnapshot{
		DriverType:  snapshot.DriverType,
		Profile:     snapshot.Profile,
		Kind:        ProfileKind(snapshot.Kind),
		Fingerprint: snapshot.Fingerprint,
		Resources:   resources,
		Warnings:    slices.Clone(snapshot.Warnings),
	}
}

// Inspector is the read-only inspection panel of one Agent, obtained via
// Agent.Inspect(). Every method degrades honestly when the driver does not
// implement the corresponding probe: a descriptor-derived or explicitly
// "unsupported" report, never a fabricated success.
type Inspector struct {
	agent *Agent
}

// Inspect returns the inspection panel for this agent.
func (a *Agent) Inspect() Inspector { return Inspector{agent: a} }

// Environment runs the driver's environment health check
// (driver.EnvironmentProbe). Drivers without the probe report a single
// informational "noop" check — visible, not invented.
func (in Inspector) Environment(ctx context.Context) (EnvironmentReport, error) {
	a := in.agent
	if probe, ok := a.driver.(driver.EnvironmentProbe); ok {
		return probe.CheckEnvironment(ctx, nil)
	}
	return engine.SummarizeEnvironment(a.driver.Descriptor().Type, []EnvironmentCheck{
		{Code: "noop", Level: "info", Message: "agent does not expose environment checks"},
	}), nil
}

// Models lists the models the agent can run (driver.ModelLister). Drivers
// without the prober fall back to the static descriptor model list.
func (in Inspector) Models(ctx context.Context) ([]ModelInfo, error) {
	a := in.agent
	if lister, ok := a.driver.(driver.ModelLister); ok {
		return lister.ListModels(ctx, nil)
	}
	return append([]ModelInfo(nil), a.driver.Descriptor().Models...), nil
}

// Quota reports live quota/billing state (driver.QuotaProbe). Drivers
// without the probe report Available=false with an explanatory error string.
func (in Inspector) Quota(ctx context.Context) (QuotaReport, error) {
	a := in.agent
	if probe, ok := a.driver.(driver.QuotaProbe); ok {
		return probe.GetQuota(ctx, nil, a.defaults.profile)
	}
	return QuotaReport{
		DriverType: a.driver.Descriptor().Type,
		Available:  false,
		Error:      "agent does not expose live quota data",
	}, nil
}

// ConfigSchema returns the driver's configuration schema
// (driver.ConfigSchemaProvider), falling back to the descriptor's static
// schema. The returned schema is a copy.
func (in Inspector) ConfigSchema(ctx context.Context) (*ConfigSchema, error) {
	a := in.agent
	if provider, ok := a.driver.(driver.ConfigSchemaProvider); ok {
		return provider.ConfigSchema(ctx, nil)
	}
	return engine.CloneConfigSchema(a.driver.Descriptor().ConfigSchema), nil
}

// Skills resolves and reports the agent's effective skill set: the default
// refs (or the SelectSkills override when one is active) resolved against
// the provider, with the inline defaults + the provider catalogue as
// non-selected candidates — exactly the legacy Admin.ListSkills recipe. The
// snapshot's sync mode reports truthfully whether the driver can observe
// installed skills (SkillAwareDriver) or the SDK only knows the desired set.
func (in Inspector) Skills(ctx context.Context) (SkillSnapshot, error) {
	a := in.agent
	identity := a.inspectIdentity()
	defaultRefs := a.skillDefaultRefs(a.defaults.skills)
	// Inline default Skill values + the upstream SkillCatalog (when the
	// provider implements it) join as non-selected candidates so the full
	// catalogue participates in the snapshot even under a SelectSkills
	// override.
	candidates, err := engine.CollectSkillCandidates(ctx, a.defaults.skillProvider, identity, a.defaults.skills)
	if err != nil {
		return SkillSnapshot{}, err
	}
	payload, selected, resolved, err := engine.ResolveSkills(ctx, a.defaults.skillProvider, a.defaults.skillMaterializer, identity, defaultRefs, nil, candidates)
	if err != nil {
		return SkillSnapshot{}, err
	}
	return engine.BuildSkillSnapshot(ctx, a.driver, nil, payload, selected, resolved, a.defaults.profile)
}

// ProfileState reports the desired vs observed profile resource state
// without changing anything. Drivers that implement the profile resource
// extension answer authoritatively; for everyone else the SDK builds the
// snapshot from the desired payload and reports non-skill resources as
// desired-but-not-observed (synced=false) — the truthful-materialization
// contract.
func (a *Agent) ProfileState(ctx context.Context) (ProfileSnapshot, error) {
	state, err := a.profileState(ctx)
	if err != nil {
		return ProfileSnapshot{}, err
	}
	if prd, ok := a.driver.(engine.ProfileResourceDriver); ok {
		snapshot, err := prd.SnapshotProfileResources(ctx, nil, a.inspectIdentity(), a.defaults.profile, state.payload, state.selected, state.resolved)
		return profileSnapshotFromEngine(snapshot), err
	}
	profileInfo, err := a.getProfile(ctx)
	if err != nil {
		return ProfileSnapshot{}, err
	}
	return profileSnapshotFromEngine(engine.SnapshotProfilePayload(a.driver.Descriptor().Type, profileInfo, a.defaults.profile, state.payload, false)), nil
}

// SyncProfile pushes the desired profile resources to the driver and reports
// the resulting state. Drivers with the profile resource extension perform
// the full sync; for everyone else the SDK syncs the one portable resource
// (skills, via the driver's skill support) and reports the rest as
// not-materialized errors (synced=true) rather than pretending they applied.
func (a *Agent) SyncProfile(ctx context.Context) (ProfileSnapshot, error) {
	state, err := a.profileState(ctx)
	if err != nil {
		return ProfileSnapshot{}, err
	}
	if prd, ok := a.driver.(engine.ProfileResourceDriver); ok {
		snapshot, err := prd.SyncProfileResources(ctx, nil, a.inspectIdentity(), a.defaults.profile, state.payload, state.selected, state.resolved)
		return profileSnapshotFromEngine(snapshot), err
	}
	if _, err := engine.SyncSkillSnapshot(ctx, a.driver, nil, state.payload.Skills, state.selected, state.resolved, a.defaults.profile); err != nil {
		return ProfileSnapshot{}, err
	}
	profileInfo, err := a.getProfile(ctx)
	if err != nil {
		return ProfileSnapshot{}, err
	}
	return profileSnapshotFromEngine(engine.SnapshotProfilePayload(a.driver.Descriptor().Type, profileInfo, a.defaults.profile, state.payload, true)), nil
}

// SelectSkills installs a process-local skill selection override: the given
// keys replace the agent-default refs for every subsequent Run/Stream/Thread
// resolution and Inspect().Skills report, until the next SelectSkills call.
// Keys must reference skills visible through the SkillProvider or the inline
// defaults (bare-key selection semantics — inline Skill values can only be
// introduced via WithSkills); unknown keys fail with ErrSkillNotFound and the
// override is NOT installed. The resolved selection is synced to the driver
// before the override takes effect, mirroring legacy Admin.SetSelectedSkills.
func (a *Agent) SelectSkills(ctx context.Context, keys []string) (SkillSnapshot, error) {
	refs := make([]driver.SkillRef, 0, len(keys))
	normalized := make([]string, 0, len(keys))
	for _, key := range keys {
		trimmed := engine.NormalizeSkillKey(key)
		if trimmed == "" {
			continue
		}
		refs = append(refs, driver.SkillKey(trimmed))
		normalized = append(normalized, trimmed)
	}
	identity := a.inspectIdentity()
	// Inline defaults + catalogue re-enter as non-selected candidates so
	// SelectSkills can target them by key (legacy comment carried over:
	// keys in neither pool report ErrSkillNotFound).
	candidates, err := engine.CollectSkillCandidates(ctx, a.defaults.skillProvider, identity, a.defaults.skills)
	if err != nil {
		return SkillSnapshot{}, err
	}
	payload, selected, resolved, err := engine.ResolveSkills(ctx, a.defaults.skillProvider, a.defaults.skillMaterializer, identity, refs, nil, candidates)
	if err != nil {
		return SkillSnapshot{}, err
	}
	snapshot, err := engine.SyncSkillSnapshot(ctx, a.driver, nil, payload, selected, resolved, a.defaults.profile)
	if err != nil {
		return SkillSnapshot{}, err
	}
	a.mu.Lock()
	a.skillSelection = normalized
	a.mu.Unlock()
	return snapshot, nil
}

// profileState is the desired-state assembly shared by ProfileState and
// SyncProfile: resolve the default skills (honoring a SelectSkills
// override), then compose the full profile payload from the agent defaults —
// the legacy defaultProfileState + defaultProfilePayloadWithSkills recipe.
type profileState struct {
	payload  driver.ProfilePayload
	selected []string
	resolved []driver.Skill
}

func (a *Agent) profileState(ctx context.Context) (profileState, error) {
	identity := a.inspectIdentity()
	defaultRefs := a.skillDefaultRefs(a.defaults.skills)
	candidates, err := engine.CollectSkillCandidates(ctx, a.defaults.skillProvider, identity, a.defaults.skills)
	if err != nil {
		return profileState{}, err
	}
	skills, selected, resolved, err := engine.ResolveSkills(ctx, a.defaults.skillProvider, a.defaults.skillMaterializer, identity, defaultRefs, nil, candidates)
	if err != nil {
		return profileState{}, err
	}
	mcpPayload, err := engine.ResolveMCPPayload(a.defaults.engineMCPConfig(), nil, a.driver.Descriptor().MCP)
	if err != nil {
		return profileState{}, err
	}
	var agentSpecs []driver.AgentSpec
	if a.defaults.agents != nil {
		agentSpecs = *a.defaults.agents
	}
	agentPayload, err := engine.PrepareAgentPayload(agentSpecs)
	if err != nil {
		return profileState{}, err
	}
	var hookSpecs []driver.HookSpec
	if a.defaults.hooks != nil {
		hookSpecs = *a.defaults.hooks
	}
	hookPayload, err := engine.PrepareHookPayload(hookSpecs)
	if err != nil {
		return profileState{}, err
	}
	var patches []driver.ProfileConfigPatch
	if a.defaults.configPatches != nil {
		patches = *a.defaults.configPatches
	}
	configPayload, err := engine.PrepareProfileConfigPayload(patches)
	if err != nil {
		return profileState{}, err
	}
	instructions, err := engine.PrepareInstructionsBundle(a.defaults.instructions)
	if err != nil {
		return profileState{}, err
	}
	declared := driver.ProfileResourceDeclarations{
		Agents:       a.defaults.agents != nil,
		Hooks:        a.defaults.hooks != nil,
		Config:       a.defaults.configPatches != nil,
		Instructions: a.defaults.instructionsSet || a.defaults.instructions != nil,
	}
	payload := engine.BuildProfilePayload(skills, mcpPayload, agentPayload, hookPayload, instructions, configPayload, declared)
	return profileState{payload: payload, selected: selected, resolved: resolved}, nil
}

// getProfile asks the driver for its local profile report
// (driver.ProfileReporter); drivers without the probe report
// Supported=false with the unsupported source marker — honest, not invented.
func (a *Agent) getProfile(ctx context.Context) (AgentProfile, error) {
	if reporter, ok := a.driver.(driver.ProfileReporter); ok {
		return reporter.GetProfile(ctx, nil, a.inspectIdentity(), a.defaults.profile)
	}
	return AgentProfile{
		DriverType: a.driver.Descriptor().Type,
		Supported:  false,
		Source:     driver.AgentProfileSourceUnsupported,
		Error:      "agent does not expose local profile semantics",
	}, nil
}

// inspectIdentity is the identity handed to inspection probes: the agent's
// default identity, or the zero identity when none is configured.
func (a *Agent) inspectIdentity() driver.AgentIdentity {
	if a.defaults.identity != nil {
		return a.defaults.identity.driverIdentity()
	}
	return driver.AgentIdentity{}
}
