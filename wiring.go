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
	schema     *driver.OutputSchema
	source     driver.StructuredOutputSource
	streaming  bool
	candidates []transportCandidate
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
	source, initialErr := engine.ResolveStructuredOutputSource(desc, schema, streaming, policy)
	candidates := []transportCandidate{}
	if initialErr == nil {
		candidates = append(candidates, transportCandidate{streaming: streaming, source: source})
	}
	// A batch alternative cannot silently silence any applicable Ask. This
	// restriction applies to a switch from rich, not to legacy batch drivers.
	if streaming {
		batchDesc := desc
		caps := &batchDesc.StructuredOutput
		caps.JSONSchemaNative = caps.JSONSchemaNative && !engine.StructuredOutputHasHITLAsk(caps.NativeHITL, policy)
		caps.JSONSchemaPromptValidate = caps.JSONSchemaPromptValidate && !engine.StructuredOutputHasHITLAsk(caps.PromptValidateHITL, policy)
		if schema != nil || !engine.StructuredOutputHasHITLAsk(nil, policy) {
			if batchSource, batchErr := engine.ResolveStructuredOutputSource(batchDesc, schema, false, policy); batchErr == nil {
				candidates = append(candidates, transportCandidate{streaming: false, source: batchSource})
			}
		}
	}
	if len(candidates) == 0 {
		return resolvedStructuredOutput{}, initialErr
	}
	selected := candidates[0]
	return resolvedStructuredOutput{schema: schema, source: selected.source, streaming: selected.streaming, candidates: candidates}, nil
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
	demand := ObservationDemand{}
	if res != nil {
		demand = res.observation
	}
	chosen := eff.resolvedOutput.selectObservation(desc.Observation, demand)
	source := chosen.source
	providerStreaming := chosen.streaming
	if res != nil && res.sink != nil {
		unavailable := observationUnavailable(desc.Observation, eff.resolvedOutput.candidates, demand)
		if len(unavailable) > 0 {
			res.sink.push(Notice{Kind: NoticeRuntime, Data: map[string]any{"code": "observation_unavailable", "capabilities": unavailable}})
		}
	}
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
	req.AppendSystemPrompt = eff.appendSystemPrompt
	res.applyRequest(&req)
	req.Instructions = instructions
	req.Skills = engine.CloneResolvedSkills(skillPayload)
	req.MCP = mcpPayload
	req.ProfilePayload = profilePayload
	req.Profile = engine.CloneProfileSelection(eff.effectiveProfile)
	req.OutputSchema = engine.CloneOutputSchema(schema)
	req.StructuredOutputSource = source
	req.Streaming = providerStreaming
	req.Observation = driver.ObservationDemand{CapabilityInvocations: demand.CapabilityInvocations, Todos: demand.Todos}

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

// Each candidate was proven before resource acquisition; attachments only rank
// immutable candidates. Schema parsing and mechanism eligibility never rerun.
type transportCandidate struct {
	streaming bool
	source    driver.StructuredOutputSource
}

func (r resolvedStructuredOutput) selectObservation(caps driver.ObservationCapabilities, demand ObservationDemand) transportCandidate {
	selected := transportCandidate{r.streaming, r.source}
	score := observationScore(caps, selected.streaming, demand)
	for _, candidate := range r.candidates {
		if next := observationScore(caps, candidate.streaming, demand); next > score {
			score = next
			selected = candidate
		}
	}
	return selected
}
func observationScore(caps driver.ObservationCapabilities, streaming bool, demand ObservationDemand) int {
	support := caps.Batch
	if streaming {
		support = caps.Streaming
	}
	score := 0
	if demand.CapabilityInvocations {
		for _, ok := range []bool{support.Skills, support.MCP, support.Subagents} {
			if ok {
				score++
			}
		}
	}
	if demand.Todos && support.Todos {
		score++
	}
	return score
}
func observationUnavailable(caps driver.ObservationCapabilities, candidates []transportCandidate, demand ObservationDemand) []string {
	var support driver.ObservationSupport
	for _, candidate := range candidates {
		available := caps.Batch
		if candidate.streaming {
			available = caps.Streaming
		}
		support.Skills = support.Skills || available.Skills
		support.MCP = support.MCP || available.MCP
		support.Subagents = support.Subagents || available.Subagents
		support.Todos = support.Todos || available.Todos
	}
	out := []string{}
	if demand.CapabilityInvocations {
		if !support.Skills {
			out = append(out, "skill")
		}
		if !support.MCP {
			out = append(out, "mcp")
		}
		if !support.Subagents {
			out = append(out, "subagent")
		}
	}
	if demand.Todos && !support.Todos {
		out = append(out, "todo")
	}
	return out
}
