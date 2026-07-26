package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// RunParams is the exported mirror of the root package's runOptions state.
// The root package keeps the RunOption setters (its unexported runOptions
// struct is constructed by internal tests), applies them, and hands the
// resolved values to Core.Execute through this struct. Field semantics are
// identical to the historical runOptions fields.
type RunParams struct {
	Session         *SessionRequest
	Workspace       WorkspaceSpec
	Runtime         *WorkspaceRuntimeConfig
	Skills          []SkillRef
	MCP             *MCPConfig
	Agents          *AgentPayload
	Hooks           *HookPayload
	ProfileConfig   *ProfileConfigPayload
	OutputSchema    *OutputSchema
	OutputSchemaErr error
	Model           string
	RunPolicy       *RunPolicy
	Instructions    *InstructionsBundleRef
	InstructionsSet bool
	Metadata        map[string]string
	Agent           *AgentIdentity
	// Streaming is tri-state: nil means "inherit from binding defaults",
	// non-nil wins over the binding default.
	Streaming *bool
	// RunIDPreset is set by the root package's Start() so the resolved run
	// shares the same ID that the RunHandle exposes before Wait() returns.
	RunIDPreset string

	// per-Kind typed HITL handlers (RunOption-level beat AgentOption-level).
	PermissionHandler PermissionHandler
	PlanReviewHandler PlanReviewHandler
	QuestionHandler   QuestionHandler
}

// DecisionSink is the engine-facing view of the root package's dualSink: the
// runner binds the resolved policy, typed handlers, and capability matrix to
// it before adapter.Run starts. The concrete implementation (and the whole
// HITL dispatcher) stays in the root package because its internal tests
// construct it and reach into unexported state.
type DecisionSink interface {
	BindRun(runID string, policy HumanDecisionPolicy, handlers DecisionHandlers, caps RunPolicyCapabilities)
}

// PendingFailureSource is implemented by the root package's sinks (dualSink
// directly; seqSink by delegation) so the engine can read the stashed HITL
// failure for the ErrSessionCheckpointMissing tolerance path.
type PendingFailureSource interface {
	PendingRunFailure() *RunFailure
}

func pendingFailureFromSink(sink EventSink) *RunFailure {
	if src, ok := sink.(PendingFailureSource); ok {
		return src.PendingRunFailure()
	}
	return nil
}

// shouldSkipSessionPersistOnFailure reports whether a missing checkpoint may
// be tolerated because the run ended in a structured human-decision failure.
func shouldSkipSessionPersistOnFailure(result RunResult) bool {
	if result.Failure == nil {
		return false
	}
	// A human decision reject/timeout may intentionally abort the run before
	// the provider emits a new resumable checkpoint. Per the session contract,
	// failed runs must not persist unhealthy state; treating the missing
	// checkpoint as a hard SDK error would misclassify an expected business
	// outcome as infrastructure failure.
	if result.Failure.IsHumanDecision() {
		return true
	}
	return false
}

// SkipSessionPersistOnFailure exposes shouldSkipSessionPersistOnFailure for
// the root package (RunHandle.Wait applies the same tolerance).
func SkipSessionPersistOnFailure(result RunResult) bool {
	return shouldSkipSessionPersistOnFailure(result)
}

// resolvedInvocation is the fully merged view of one Run/Start call.
type resolvedInvocation struct {
	runID          string
	prompt         string
	adapter        DriverAdapter
	config         any
	agent          AgentIdentity
	workspace      WorkspaceLease
	runtime        RuntimePayload
	skills         ResolvedSkills
	mcp            MCPPayload
	profilePayload ProfilePayload
	profile        *ProfileSelection
	policy         RunPolicy
	handlers       DecisionHandlers
	instructions   *InstructionsBundleRef
	session        SessionRequest
	metadata       map[string]string
	outputSchema   *OutputSchema
	outputSource   StructuredOutputSource
	fingerprint    string
	streaming      bool
	model          string
}

// Execute is the single execution entry point used by the root package's
// Runner.Run and Runner.Start. It reproduces the historical runnerImpl.run
// pipeline: resolve the invocation, bind the decision sink, coordinate the
// session plan, execute the adapter, and persist the checkpoint.
func (c *Core) Execute(
	ctx context.Context,
	agentName string,
	binding AgentBinding,
	prompt string,
	params RunParams,
	sink EventSink,
	decision DecisionSink,
) (RunResult, error) {
	invocation, cleanup, err := c.resolveInvocation(ctx, agentName, binding, prompt, params)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		return RunResult{}, err
	}

	if decision != nil {
		decision.BindRun(invocation.runID, invocation.policy.HumanDecision, invocation.handlers, invocation.adapter.Descriptor().RunPolicyCaps)
	}

	if err := validateDecisionCapabilities(invocation); err != nil {
		return RunResult{}, err
	}

	driverType := invocation.adapter.Descriptor().Type
	sessionPlan, err := c.prepareSession(ctx, invocation.session, invocation.agent, driverType, invocation.fingerprint)
	if err != nil {
		return RunResult{}, err
	}
	runCtx := ctx
	var runCancel context.CancelFunc
	if sessionPlan != nil {
		runCtx, runCancel = context.WithCancel(ctx)
		sessionPlan.startLeaseRenewal(runCtx, c.sessionStore, runCancel)
		defer runCancel()
		defer sessionPlan.release()
	}

	result, checkpoint, err := c.executeWithSessionPlan(runCtx, sink, invocation, sessionPlan)
	if sessionPlan != nil {
		sessionPlan.stopLeaseRenewal()
		if renewErr := sessionPlan.renewalError(); renewErr != nil {
			return RunResult{}, renewErr
		}
	}
	if err != nil {
		return RunResult{}, err
	}

	sessionRef, err := c.persistSession(
		runCtx,
		sessionPlan,
		invocation.agent,
		invocation.adapter,
		invocation.fingerprint,
		checkpoint,
	)
	if err != nil {
		if errors.Is(err, ErrSessionCheckpointMissing) {
			if result.Failure == nil {
				if f := pendingFailureFromSink(sink); f != nil {
					result.Failure = cloneRunFailure(f)
				}
			}
			if shouldSkipSessionPersistOnFailure(result) {
				return result, nil
			}
		}
		return RunResult{}, err
	}
	result.Session = sessionRef
	return result, nil
}

// validateDecisionCapabilities cross-checks the resolved policy against the
// adapter's declared HumanDecision / Question support matrix. Unsupported
// Ask modes are hard errors. Retry support is enforced later, when a specific
// Kind actually tries to use FailureRetry.
func validateDecisionCapabilities(inv resolvedInvocation) error {
	caps := inv.adapter.Descriptor().RunPolicyCaps
	p := inv.policy.HumanDecision

	checkMode := func(kind HumanDecisionKind, mode string, support HumanDecisionSupport, modeAsk, modeAutoApprove, modeAutoReject string) error {
		switch mode {
		case modeAsk:
			if !support.Ask {
				return fmt.Errorf("%w: adapter=%s kind=%s mode=%s", ErrHumanDecisionModeUnsupported, adapterLabel(caps), kind, mode)
			}
		case modeAutoApprove:
			if !support.AutoApprove {
				return fmt.Errorf("%w: adapter=%s kind=%s mode=%s", ErrHumanDecisionModeUnsupported, adapterLabel(caps), kind, mode)
			}
		case modeAutoReject:
			if !support.AutoReject {
				return fmt.Errorf("%w: adapter=%s kind=%s mode=%s", ErrHumanDecisionModeUnsupported, adapterLabel(caps), kind, mode)
			}
		}
		return nil
	}

	if err := checkMode(HumanDecisionPermission, string(p.Permission), caps.Permission,
		string(HumanDecisionAsk), string(HumanDecisionAutoApprove), string(HumanDecisionAutoReject)); err != nil {
		return err
	}
	if err := checkMode(HumanDecisionPlanReview, string(p.PlanReview), caps.PlanReview,
		string(HumanDecisionAsk), string(HumanDecisionAutoApprove), string(HumanDecisionAutoReject)); err != nil {
		return err
	}

	// Question uses its own support type (no AutoApprove).
	switch p.Question {
	case QuestionAsk:
		if !caps.Question.Ask {
			return fmt.Errorf("%w: adapter=%s kind=%s mode=%s", ErrHumanDecisionModeUnsupported, adapterLabel(caps), HumanDecisionQuestion, p.Question)
		}
	case QuestionAutoReject:
		if !caps.Question.AutoReject {
			// Spec §3.8: when the adapter does not model Question at all
			// (all QuestionSupport fields false), QuestionAutoReject is a
			// no-op — treat it as Unset to avoid breaking the portable
			// safe-default policy (see §5.4.3).
			if !caps.Question.Ask && !caps.Question.Retry {
				break
			}
			return fmt.Errorf("%w: adapter=%s kind=%s mode=%s", ErrHumanDecisionModeUnsupported, adapterLabel(caps), HumanDecisionQuestion, p.Question)
		}
	}

	return nil
}

func (c *Core) resolveInvocation(ctx context.Context, agentName string, binding AgentBinding, prompt string, resolvedOpts RunParams) (resolvedInvocation, func(), error) {
	if err := validateAgentBinding(binding); err != nil {
		return resolvedInvocation{}, nil, err
	}

	if resolvedOpts.OutputSchemaErr != nil {
		return resolvedInvocation{}, nil, resolvedOpts.OutputSchemaErr
	}

	defaults := binding.Defaults()
	identity := defaults.Agent
	if resolvedOpts.Agent != nil {
		identity = *resolvedOpts.Agent
	}

	workspaceSpec := defaults.Workspace
	if resolvedOpts.Workspace != nil {
		workspaceSpec = resolvedOpts.Workspace
	}
	runtimeOverride := resolvedOpts.Runtime

	defaultRefs := c.selectedRefsFor(agentName, defaults.Skills)
	runRefs := cloneSkillRefs(resolvedOpts.Skills)

	policy, err := mergeRunPolicy(defaults.RunPolicy, resolvedOpts.RunPolicy)
	if err != nil {
		return resolvedInvocation{}, nil, err
	}

	handlers := DecisionHandlers{
		Permission: defaults.PermissionHandler,
		PlanReview: defaults.PlanReviewHandler,
		Question:   defaults.QuestionHandler,
	}
	if resolvedOpts.PermissionHandler != nil {
		handlers.Permission = resolvedOpts.PermissionHandler
	}
	if resolvedOpts.PlanReviewHandler != nil {
		handlers.PlanReview = resolvedOpts.PlanReviewHandler
	}
	if resolvedOpts.QuestionHandler != nil {
		handlers.Question = resolvedOpts.QuestionHandler
	}

	instructions := cloneInstructions(defaults.Instructions)
	declared := profileDeclarationsFromDefaults(defaults)
	if resolvedOpts.InstructionsSet {
		instructions = cloneInstructions(resolvedOpts.Instructions)
		declared.Instructions = true
	}
	instructions, err = prepareInstructionsBundle(instructions)
	if err != nil {
		return resolvedInvocation{}, nil, err
	}

	metadata := mergeStringMaps(defaults.Metadata, resolvedOpts.Metadata)
	config := binding.Config()
	common := extractCommonConfig(config)

	streaming := false
	if defaults.Streaming != nil {
		streaming = *defaults.Streaming
	}
	if resolvedOpts.Streaming != nil {
		streaming = *resolvedOpts.Streaming
	}

	outputSchema, err := normalizeOutputSchema(resolvedOpts.OutputSchema)
	if err != nil {
		return resolvedInvocation{}, nil, err
	}
	outputSource, err := resolveStructuredOutputSource(binding.Adapter().Descriptor(), outputSchema, streaming, policy)
	if err != nil {
		return resolvedInvocation{}, nil, err
	}
	if outputSchema != nil && outputSource == StructuredOutputSourcePromptValidate {
		if instruction := structuredOutputPromptInstruction(outputSchema); instruction != "" {
			prompt = instruction + "\n\n" + prompt
		}
	}

	workspace, err := c.workspaceManager.Resolve(ctx, WorkspaceRequest{
		BaseCWD: common.CWD,
		Spec:    workspaceSpec,
		Metadata: mergeStringMaps(
			commonConfigMetadata(common),
			metadata,
		),
	})
	if err != nil {
		return resolvedInvocation{}, nil, err
	}

	runID := resolvedOpts.RunIDPreset
	if runID == "" {
		runID = newRunID(binding.Adapter().Descriptor().Type)
	}

	runtimePayload, err := c.prepareRuntime(ctx, runID, binding, identity, workspace, metadata, runtimeOverride)
	if err != nil {
		// Use a detached ctx so a cancelled parent still allows the
		// release to finish (otherwise we can leak workspaces when a
		// user cancels Run early). Values propagate so tracing / tenant
		// bindings are preserved.
		_ = c.workspaceManager.Release(context.WithoutCancel(ctx), workspace, WorkspaceReleaseKeep)
		return resolvedInvocation{}, nil, err
	}

	cleanup := func() {
		releaseCtx := context.WithoutCancel(ctx)
		if runID != "" {
			_ = c.runtimeManager.ReleaseByRun(releaseCtx, runID)
		}
		_ = c.workspaceManager.Release(releaseCtx, workspace, WorkspaceReleaseKeep)
	}

	mcpPayload, err := resolveMCPPayloadWithRuntime(defaults.MCP, resolvedOpts.MCP, runtimePayload.Ensured, binding.Adapter().Descriptor().MCP)
	if err != nil {
		cleanup()
		return resolvedInvocation{}, nil, err
	}

	skillPayload, _, _, err := c.resolveSkills(ctx, identity, defaultRefs, runRefs, defaults.Skills)
	if err != nil {
		cleanup()
		return resolvedInvocation{}, nil, err
	}

	if injector, ok := binding.Adapter().(SkillAwareDriver); ok {
		if err := injector.InjectSkills(ctx, binding.Config(), cloneResolvedSkills(skillPayload), cloneProfileSelection(defaults.Profile)); err != nil {
			cleanup()
			return resolvedInvocation{}, nil, err
		}
	}

	agentPayload, err := prepareAgentPayload(defaults.Agents)
	if err != nil {
		cleanup()
		return resolvedInvocation{}, nil, err
	}
	if resolvedOpts.Agents != nil {
		declared.Agents = true
		agentPayload, err = prepareAgentPayload(resolvedOpts.Agents.Agents)
		if err != nil {
			cleanup()
			return resolvedInvocation{}, nil, err
		}
	}
	hookPayload, err := prepareHookPayload(defaults.Hooks)
	if err != nil {
		cleanup()
		return resolvedInvocation{}, nil, err
	}
	if resolvedOpts.Hooks != nil {
		declared.Hooks = true
		hookPayload, err = prepareHookPayload(resolvedOpts.Hooks.Hooks)
		if err != nil {
			cleanup()
			return resolvedInvocation{}, nil, err
		}
	}
	configPayload, err := prepareProfileConfigPayload(defaults.ProfileConfig)
	if err != nil {
		cleanup()
		return resolvedInvocation{}, nil, err
	}
	if resolvedOpts.ProfileConfig != nil {
		declared.Config = true
		configPayload, err = prepareProfileConfigPayload(resolvedOpts.ProfileConfig.Patches)
		if err != nil {
			cleanup()
			return resolvedInvocation{}, nil, err
		}
	}
	profilePayload := buildProfilePayload(skillPayload, mcpPayload, agentPayload, hookPayload, instructions, configPayload, declared)

	sessionReq := SessionRequest{}
	if resolvedOpts.Session != nil {
		sessionReq = *resolvedOpts.Session
	}

	fingerprint := stableHash(
		binding.Adapter().Descriptor().Type,
		identity,
		extractDriverFingerprint(config),
		strings.TrimSpace(resolvedOpts.Model),
		workspace.Fingerprint,
		runtimePayload.Fingerprint,
		profilePayload.Fingerprint,
	)

	return resolvedInvocation{
		runID:          runID,
		prompt:         prompt,
		adapter:        binding.Adapter(),
		config:         config,
		agent:          identity,
		workspace:      workspace,
		runtime:        runtimePayload,
		skills:         skillPayload,
		mcp:            mcpPayload,
		profilePayload: profilePayload,
		profile:        cloneProfileSelection(defaults.Profile),
		policy:         policy,
		handlers:       handlers,
		instructions:   instructions,
		session:        sessionReq,
		metadata:       cloneStringMap(metadata),
		outputSchema:   cloneOutputSchema(outputSchema),
		outputSource:   outputSource,
		fingerprint:    fingerprint,
		streaming:      streaming,
		model:          strings.TrimSpace(resolvedOpts.Model),
	}, cleanup, nil
}

func (c *Core) executeWithSessionPlan(
	ctx context.Context,
	sink EventSink,
	invocation resolvedInvocation,
	plan *resolvedSessionPlan,
) (RunResult, *DriverCheckpoint, error) {
	if len(invocation.runtime.Ensured) > 0 {
		serviceNames := make([]string, 0, len(invocation.runtime.Ensured))
		for _, service := range invocation.runtime.Ensured {
			if service.Name != "" {
				serviceNames = append(serviceNames, service.Name)
			}
		}
		_ = sink.Emit(RunEvent{
			Type:      RunEventRuntime,
			Text:      "runtime services ready",
			Timestamp: time.Now().UTC(),
			Data: map[string]any{
				"services": cloneRuntimeServiceRefs(invocation.runtime.Ensured),
				"names":    serviceNames,
			},
		})
	}

	driverReq := DriverRunRequest{
		RunID:          invocation.runID,
		Prompt:         invocation.prompt,
		Config:         invocation.config,
		ModelOverride:  invocation.model,
		Agent:          invocation.agent,
		Workspace:      invocation.workspace,
		Runtime:        cloneRuntimePayload(invocation.runtime),
		Skills:         cloneResolvedSkills(invocation.skills),
		MCP:            cloneMCPPayload(invocation.mcp),
		ProfilePayload: cloneProfilePayload(invocation.profilePayload),
		Profile:        cloneProfileSelection(invocation.profile),
		Policy:         invocation.policy,
		Instructions:   invocation.instructions,
		Metadata:       cloneStringMap(invocation.metadata),
		OutputSchema:   cloneOutputSchema(invocation.outputSchema),
		Streaming:      invocation.streaming,
	}
	if plan != nil {
		var state *DriverSessionState
		if plan.record != nil && (plan.reused || plan.request.Mode == SessionFork) {
			state = normalizeSessionState(invocation.adapter, plan.record.DriverState)
		}
		driverReq.Session = &DriverSessionContext{
			EngineSessionID: plan.engineID,
			Mode:            plan.request.Mode,
			State:           state,
			PreviousID:      plan.previousID,
		}
	}

	runResult, err := invocation.adapter.Run(ctx, driverReq, sink)
	if err != nil {
		var rejected *ResumeRejectedError
		if errors.As(err, &rejected) && plan != nil && plan.reused && plan.request.Mode == SessionContinueOrStart {
			if err := plan.prepareFresh(ctx, c.sessionStore, invocation.adapter.Descriptor().Type, invocation.fingerprint); err != nil {
				return RunResult{}, nil, err
			}
			return c.executeWithSessionPlan(ctx, sink, invocation, plan)
		}
		return RunResult{}, nil, err
	}

	runtimeReports := cloneRuntimeServiceReports(runResult.RuntimeServices)
	if len(runtimeReports) == 0 {
		runtimeReports = runtimeReportsFromRefs(invocation.runtime.Ensured, invocation.agent)
	}
	structuredOutput, failure := finalizeStructuredOutput(invocation, runResult)

	return RunResult{
		RunID:            invocation.runID,
		DriverType:       invocation.adapter.Descriptor().Type,
		Output:           runResult.Output,
		RawStreams:       cloneRawStreams(runResult.RawStreams),
		Transcript:       cloneTranscriptItems(runResult.Transcript),
		ExitCode:         runResult.ExitCode,
		Signal:           runResult.Signal,
		TimedOut:         runResult.TimedOut,
		Usage:            cloneUsagePointer(runResult.Usage),
		Metadata:         cloneStringMap(runResult.Metadata),
		Provider:         runResult.Provider,
		Biller:           runResult.Biller,
		Model:            runResult.Model,
		BillingType:      runResult.BillingType,
		CostUSD:          cloneFloat64Pointer(runResult.CostUSD),
		Summary:          runResult.Summary,
		Result:           cloneAnyMap(runResult.Result),
		StructuredOutput: structuredOutput,
		RuntimeServices:  runtimeReports,
		Question:         cloneRunQuestion(runResult.Question),
		Failure:          failure,
	}, runResult.Checkpoint, nil
}

func finalizeStructuredOutput(invocation resolvedInvocation, runResult DriverRunResult) (*StructuredOutput, *RunFailure) {
	failure := cloneRunFailure(runResult.Failure)
	if invocation.outputSchema == nil {
		return nil, failure
	}

	structured := cloneStructuredOutput(runResult.StructuredOutput)
	if structured == nil {
		if invocation.outputSource == StructuredOutputSourcePromptValidate {
			structured = validateStructuredOutput(invocation.outputSchema, invocation.outputSource, []byte(runResult.Output))
		} else {
			structured = &StructuredOutput{
				Format:           invocation.outputSchema.Format,
				Mode:             invocation.outputSchema.Mode,
				Source:           invocation.outputSource,
				Valid:            false,
				ValidationErrors: []string{"adapter did not return native structured output"},
				SchemaHash:       schemaHash(invocation.outputSchema),
			}
		}
	} else {
		if structured.Source == "" {
			structured.Source = invocation.outputSource
		}
		if structured.Format == "" {
			structured.Format = invocation.outputSchema.Format
		}
		if structured.Mode == "" {
			structured.Mode = invocation.outputSchema.Mode
		}
		if structured.SchemaHash == "" {
			structured.SchemaHash = schemaHash(invocation.outputSchema)
		}
		if len(structured.RawJSON) > 0 {
			structured = validateStructuredOutput(invocation.outputSchema, structured.Source, structured.RawJSON)
		} else if !structured.Valid && len(structured.ValidationErrors) == 0 {
			structured.ValidationErrors = []string{"structured output RawJSON is empty"}
		}
	}

	if structured != nil && !structured.Valid && invocation.outputSchema.OnInvalid == StructuredOutputFailRun && failure == nil {
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

// adapterLabel returns a best-effort diagnostic label for error messages.
// RunPolicyCapabilities is a value type without an adapter name, so callers
// fall back to a generic label. (RunPolicyCapabilities now lives in the
// driver package, so this is a free function instead of an unexported
// method.)
func adapterLabel(RunPolicyCapabilities) string { return "adapter" }

func extractDriverFingerprint(cfg any) string {
	return stableHash(cfg)
}

func commonConfigMetadata(cfg CommonConfig) map[string]string {
	if cfg.CWD == "" {
		return nil
	}
	return map[string]string{
		"cwd": cfg.CWD,
	}
}
