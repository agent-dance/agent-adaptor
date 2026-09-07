package adaptor

import (
	"context"
	"strings"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
)

// The request-resolution pipeline turns the merged effective settings of one
// invocation into a fully populated driver.Request. It validates option-time
// schema errors, prepares instructions, negotiates structured output, resolves
// MCP and skills, and assembles the profile payload before Driver.Run starts.

// resolvedStructuredOutput is computed once after policy validation and before
// any resources are acquired. Request assembly consumes the same decision.
type resolvedStructuredOutput struct {
	schema    *driver.OutputSchema
	source    driver.StructuredOutputSource
	streaming bool
}

func (a *Agent) resolveStructuredOutput(desc driver.Descriptor, eff *RunSettings) (resolvedStructuredOutput, error) {
	if eff.outputSchemaErr != nil {
		return resolvedStructuredOutput{}, eff.outputSchemaErr
	}
	schema, err := engine.NormalizeOutputSchema(eff.outputSchema)
	if err != nil {
		return resolvedStructuredOutput{}, err
	}
	var policy driver.RunPolicy
	if eff.policy != nil {
		policy = eff.policy.driverPolicy()
	}
	streaming := providerRichTransport(a.driver)
	source, err := engine.ResolveStructuredOutputSource(desc, schema, streaming, policy)
	// Schema cannot sacrifice an effective Ask's interactive transport.
	// With no such demand, a batch-only schema mechanism remains eligible.
	asks := policy.HumanDecision.Permission == driver.HumanDecisionAsk ||
		policy.HumanDecision.PlanReview == driver.HumanDecisionAsk ||
		policy.HumanDecision.Question == driver.QuestionAsk
	if err != nil && streaming && !asks {
		if batchSource, batchErr := engine.ResolveStructuredOutputSource(desc, schema, false, policy); batchErr == nil {
			streaming, source, err = false, batchSource, nil
		}
	}
	if err != nil {
		return resolvedStructuredOutput{}, err
	}
	return resolvedStructuredOutput{schema: schema, source: source, streaming: streaming}, nil
}

// resolvedRun is everything the invocation coordinator needs from one
// resolution: the request itself and the normalized schema + negotiated
// source for post-run structured output finalization.
type resolvedRun struct {
	req    driver.Request
	schema *driver.OutputSchema
	source driver.StructuredOutputSource
}

// resolveRun assembles one invocation after static schema/policy preflight.
// Every remaining failure is pre-launch: the driver is never started, and
// Result preserves the engine sentinel chain (ErrMCPTransportUnsupported,
// ErrSkillNotFound, ...).
func (a *Agent) resolveRun(ctx context.Context, runID, prompt string, eff *RunSettings, res *runResources) (resolvedRun, error) {
	desc := a.driver.Descriptor()

	var identity driver.AgentIdentity
	if eff.identity != nil {
		identity = eff.identity.driverIdentity()
	}

	// 2. Instructions: normalize whitespace, path/content exclusivity, and
	// file fingerprints before the Driver observes the bundle.
	instructions, err := engine.PrepareInstructionsBundle(eff.instructions)
	if err != nil {
		return resolvedRun{}, err
	}

	// 3. Reuse the schema/transport decision made before resource acquisition;
	// consumer Run and Stream share this exact resolved invocation.
	schema := eff.resolvedOutput.schema
	source := eff.resolvedOutput.source
	providerStreaming := eff.resolvedOutput.streaming
	if schema != nil && source == driver.StructuredOutputSourcePromptValidate {
		if instruction := engine.StructuredOutputPromptInstruction(schema); instruction != "" {
			prompt = instruction + "\n\n" + prompt
		}
	}

	// 4. MCP: the merged config replaces as a whole value; validation
	// (transport support, key uniqueness, per-transport field rules)
	// happens before the driver launches. The ensured runtime services of
	// this run — WithServices endpoints and RunServiceProvider attachments
	// alike — contribute their typed ServiceRef.MCP servers here, appended
	// to the host's own WithMCP set rather than replacing it. A service
	// whose MCP key collides with a host server fails the run before launch
	// (key uniqueness), which is the intended loud failure.
	mcpPayload, err := engine.ResolveMCPPayloadWithRuntime(eff.engineMCPConfig(), nil, res.runtimeRefs(), desc.MCP)
	if err != nil {
		return resolvedRun{}, err
	}

	// 5. Skills: defaults below the clone boundary carry the default
	// source label, while per-call appends above it carry the run label.
	// The candidate pool is the agent defaults;
	// an Inspect-level SelectSkills override substitutes the default refs
	// for this resolution.
	defaultRefs := a.skillDefaultRefs(eff.skills[:eff.defaultSkillBoundary])
	runRefs := eff.skills[eff.defaultSkillBoundary:]
	candidates := eff.skills[:eff.defaultSkillBoundary]
	skillPayload, _, _, err := engine.ResolveSkills(ctx, a.defaults.skillProvider, a.defaults.skillMaterializer, identity, defaultRefs, runRefs, candidates)
	if err != nil {
		return resolvedRun{}, err
	}
	if sd, ok := a.driver.(driver.SkillSupport); ok {
		if err := sd.InjectSkills(ctx, nil, engine.CloneResolvedSkills(skillPayload), engine.CloneProfileSelection(eff.effectiveProfile)); err != nil {
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
	// assignment preserves the pipeline's defensive-copy guarantees.
	req := buildRequest(runID, prompt, eff)
	res.applyRequest(&req)
	req.Instructions = instructions
	req.Skills = engine.CloneResolvedSkills(skillPayload)
	req.MCP = mcpPayload
	req.ProfilePayload = profilePayload
	req.Profile = engine.CloneProfileSelection(eff.effectiveProfile)
	req.OutputSchema = engine.CloneOutputSchema(schema)
	req.StructuredOutputSource = source
	req.Streaming = providerStreaming

	return resolvedRun{
		req:    req,
		schema: schema,
		source: source,
	}, nil
}

func providerRichTransport(d driver.Driver) bool {
	support, ok := d.(driver.StreamSupport)
	if !ok {
		return false
	}
	capability := support.StreamCapability()
	return capability.Native || capability.TokenLevel || capability.Reasoning || capability.ToolCallArgs || capability.HITL
}

// skillDefaultRefs returns the default-scope skill refs for one resolution:
// the SelectSkills override when one is active (bare keys re-resolved
// through the provider), otherwise the agent-default refs.
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
