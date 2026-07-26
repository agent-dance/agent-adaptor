package adaptor

import (
	"context"
	"strings"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
)

// This file is the P3 request-resolution pipeline: it turns the merged
// effective settings of one invocation into a fully-populated driver.Request
// in the exact order of the legacy resolveInvocation path — schema-error
// short-circuit, instructions preparation, structured output negotiation,
// prompt instruction injection, MCP validation, skill resolution +
// injection, profile resource payload assembly. Semantic blocks are engine
// truth (ResolveSkills / ResolveMCPPayloadWithRuntime / Prepare* /
// BuildProfilePayload / FinalizeStructuredOutput); only the pass-through
// field mapping is local.

// resolvedRun is everything the execution paths (Agent.Stream goroutine,
// Thread.execute) need from one resolution: the request itself, the
// normalized schema + negotiated source for post-run structured output
// finalization, and the profile payload fingerprint for the thread
// compatibility recipe.
type resolvedRun struct {
	req                driver.Request
	schema             *driver.OutputSchema
	source             driver.StructuredOutputSource
	payloadFingerprint string
}

// resolveRun resolves one invocation. Every failure here is a pre-launch
// failure: the driver is never started, and the error surfaces through the
// stream's Result() (or Run's error return) with the engine sentinel chain
// intact (ErrInvalidOutputSchema, ErrMCPTransportUnsupported,
// ErrSkillNotFound, ...).
func (a *Agent) resolveRun(ctx context.Context, runID, prompt string, eff *RunSettings) (resolvedRun, error) {
	// 1. Schema construction failures recorded at option-build time fail
	// the run before anything else (legacy outputSchemaErr short-circuit).
	if eff.outputSchemaErr != nil {
		return resolvedRun{}, eff.outputSchemaErr
	}
	desc := a.driver.Descriptor()

	var identity driver.AgentIdentity
	if eff.identity != nil {
		identity = eff.identity.driverIdentity()
	}
	var policy driver.RunPolicy
	if eff.policy != nil {
		policy = eff.policy.driverPolicy()
	}

	// 2. Instructions: the merged bundle is normalized exactly like the
	// legacy path (trim, path/content exclusivity, file fingerprint).
	instructions, err := engine.PrepareInstructionsBundle(eff.instructions)
	if err != nil {
		return resolvedRun{}, err
	}

	// 3. Structured output: normalize the schema, then negotiate the
	// source against the driver's declared capability matrix. next/ always
	// streams (Run == Stream + drain), so the negotiation always demands
	// streaming-compatible support — see the P3 report deviation note.
	schema, err := engine.NormalizeOutputSchema(eff.outputSchema)
	if err != nil {
		return resolvedRun{}, err
	}
	source, err := engine.ResolveStructuredOutputSource(desc, schema, true, policy)
	if err != nil {
		return resolvedRun{}, err
	}
	if schema != nil && source == driver.StructuredOutputSourcePromptValidate {
		if instruction := engine.StructuredOutputPromptInstruction(schema); instruction != "" {
			prompt = instruction + "\n\n" + prompt
		}
	}

	// 4. MCP: the merged config replaces as a whole value; validation
	// (transport support, key uniqueness, per-transport field rules)
	// happens before the driver launches. next/ has no runtime manager
	// yet, so the ensured-services slice is empty.
	mcpPayload, err := engine.ResolveMCPPayloadWithRuntime(eff.mcp, nil, nil, desc.MCP)
	if err != nil {
		return resolvedRun{}, err
	}

	// 5. Skills: defaults below the clone boundary carry the default
	// source label, per-call appends above it carry the run label —
	// preserving the legacy default/run source semantics inside the
	// merger. The candidate pool is the agent defaults (run-path rule);
	// an Inspect-level SelectSkills override substitutes the default refs
	// exactly like the legacy admin selection.
	defaultRefs := a.skillDefaultRefs(eff.skills[:eff.defaultSkillBoundary])
	runRefs := eff.skills[eff.defaultSkillBoundary:]
	candidates := eff.skills[:eff.defaultSkillBoundary]
	skillPayload, _, _, err := engine.ResolveSkills(ctx, a.defaults.skillProvider, a.defaults.skillMaterializer, identity, defaultRefs, runRefs, candidates)
	if err != nil {
		return resolvedRun{}, err
	}
	if sd, ok := a.driver.(driver.SkillSupport); ok {
		if err := sd.InjectSkills(ctx, nil, engine.CloneResolvedSkills(skillPayload), engine.CloneProfileSelection(a.defaults.profile)); err != nil {
			return resolvedRun{}, err
		}
	}

	// 6. Declared profile resources: replace + declare. A non-nil pointer
	// with an empty slice is an explicit "none" declaration.
	var agentSpecs []driver.AgentSpec
	if eff.agents != nil {
		agentSpecs = *eff.agents
	}
	agentPayload, err := engine.PrepareAgentPayload(agentSpecs)
	if err != nil {
		return resolvedRun{}, err
	}
	var hookSpecs []driver.HookSpec
	if eff.hooks != nil {
		hookSpecs = *eff.hooks
	}
	hookPayload, err := engine.PrepareHookPayload(hookSpecs)
	if err != nil {
		return resolvedRun{}, err
	}
	var patches []driver.ProfileConfigPatch
	if eff.configPatches != nil {
		patches = *eff.configPatches
	}
	configPayload, err := engine.PrepareProfileConfigPayload(patches)
	if err != nil {
		return resolvedRun{}, err
	}
	declared := driver.ProfileResourceDeclarations{
		Agents:       eff.agents != nil,
		Hooks:        eff.hooks != nil,
		Config:       eff.configPatches != nil,
		Instructions: eff.instructionsSet || eff.instructions != nil,
	}
	profilePayload := engine.BuildProfilePayload(skillPayload, mcpPayload, agentPayload, hookPayload, instructions, configPayload, declared)

	// 7. Request assembly: base fields via buildRequest, resolved payloads
	// overlaid. The payloads are single-use values built above, so direct
	// assignment preserves the legacy defensive-copy guarantees.
	req := buildRequest(runID, prompt, eff)
	req.Instructions = instructions
	req.Skills = engine.CloneResolvedSkills(skillPayload)
	req.MCP = mcpPayload
	req.ProfilePayload = profilePayload
	req.Profile = engine.CloneProfileSelection(a.defaults.profile)
	req.OutputSchema = engine.CloneOutputSchema(schema)

	return resolvedRun{
		req:                req,
		schema:             schema,
		source:             source,
		payloadFingerprint: profilePayload.Fingerprint,
	}, nil
}

// skillDefaultRefs returns the default-scope skill refs for one resolution:
// the SelectSkills override when one is active (bare keys re-resolved
// through the provider, exactly like the legacy admin selectedRefsFor),
// otherwise the agent-default refs.
func (a *Agent) skillDefaultRefs(defaults []driver.SkillRef) []driver.SkillRef {
	a.mu.Lock()
	selection := a.skillSelection
	a.mu.Unlock()
	if selection == nil {
		return defaults
	}
	refs := make([]driver.SkillRef, 0, len(selection))
	for _, key := range selection {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		refs = append(refs, driver.SkillKey(key))
	}
	return refs
}
