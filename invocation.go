package adaptor

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/agent-dance/agent-adaptor/v2/driver"
	"github.com/agent-dance/agent-adaptor/v2/internal/engine"
)

const invocationCleanupTimeout = 5 * time.Second

// invocationTarget adds Thread coordination to the one execution pipeline.
// A nil target is the stateless Agent path. Mode and forkFromKey are
// snapshotted before the goroutine starts, so a successful first Thread run
// cannot race a second call's mode selection.
type invocationTarget struct {
	thread      *Thread
	mode        driver.SessionMode
	forkFromKey string
}

// startInvocation is the only asynchronous execution entry. Agent.Send and
// Thread.Send differ solely in whether they supply a Thread target.
func (a *Agent) startInvocation(ctx context.Context, prompt string, opts []CallOption, target *invocationTarget) Stream {
	threadKey := ""
	if target != nil && target.thread != nil {
		threadKey = target.thread.key
	}
	st, eff, runCtx, ok := a.openStream(ctx, opts, threadKey)
	if !ok {
		return st
	}
	st.sink.enableAuthoritativeLifecycle()
	st.hub.startMessage(eff.message, prompt)
	// The descriptor governs prelaunch admission. Resolution atomically trims
	// capabilities and reconciles queued messages before Driver.Run can claim.
	go a.executeInvocation(runCtx, st, prompt, &eff, target)
	return st
}

// executeInvocation owns every phase between option resolution and terminal
// teardown. In particular, this file contains the sole production Driver.Run
// call and the sole ThreadSessionPlan.Persist call in the root execution path.
func (a *Agent) executeInvocation(ctx context.Context, st *runStream, prompt string, eff *RunSettings, target *invocationTarget) {
	defer a.unregisterRun(st.runID)
	var (
		resources      *runResources
		plan           *engine.ThreadSessionPlan
		threadContract threadDriverContract
		result         *Result
		resultErr      error
	)

	defer func() {
		// Stop renewal before releasing leases. ReleaseContext remains bounded
		// even for a broken store and its error is part of the observable run
		// outcome instead of disappearing in a defer.
		if plan != nil {
			plan.StopLeaseRenewal()
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), invocationCleanupTimeout)
			releaseErr := plan.ReleaseContext(cleanupCtx)
			cancel()
			if releaseErr != nil {
				releaseErr = target.thread.threadError(releaseErr)
				if resultErr == nil {
					result = nil
					resultErr = releaseErr
				} else {
					resultErr = errors.Join(resultErr, releaseErr)
				}
			}
		}

		backfillRunServices(resources, result, resultErr)
		if teardownErr := resources.finish(ctx); teardownErr != nil {
			teardownErr = fmt.Errorf("adaptor: run %s teardown: %w", st.runID, teardownErr)
			if resultErr == nil {
				result = nil
				resultErr = teardownErr
			} else {
				resultErr = errors.Join(resultErr, teardownErr)
			}
		}
		reason := MessageRunFailed
		if ctx.Err() != nil {
			reason = MessageCancelled
		}
		st.hub.finish(reason)
		actual := result
		var business *RunError
		if actual == nil && errors.As(resultErr, &business) {
			actual = business.Result
		}
		if actual != nil {
			actual.messages = st.hub.outcomes()
			actual.transcript = append(actual.transcript, st.hub.messageTranscript()...)
		}
		summaries := resources.collectSubagents()
		if actual != nil {
			actual.subagents = cloneSubagents(summaries)
			for _, v := range summaries {
				actual.Usage = addUsage(actual.Usage, v.Usage)
			}
		}

		st.res, st.err = result, resultErr
		st.sink.completeAuthoritativeLifecycle(result, resultErr)
		st.sink.broker.publishTerminal(Done{Result: result, Err: resultErr}, nil)
		st.sink.close()
		close(st.done)
		st.cancel()
	}()

	if target != nil {
		var contractErr error
		threadContract, contractErr = validateThreadDriverContract(a.driver)
		if contractErr != nil {
			resultErr = target.thread.threadError(contractErr)
			return
		}
		if a.toolThreadErr != nil {
			resultErr = target.thread.threadError(a.toolThreadErr)
			return
		}
	}

	// Hosted Tool profile resolution may call provider code and allocate an
	// Agent-owned directory. Keep it inside lifecycle admission so Close sees,
	// cancels, and drains the work before removing profiles and the Tool runtime.
	// Thread-only contract checks stay ahead of this resource-producing phase.
	if profileErr := a.prepareHostedToolProfile(ctx, eff); profileErr != nil {
		resultErr = fmt.Errorf("adaptor: run %s: %w", st.runID, profileErr)
		return
	}

	resources, resultErr = a.acquireRun(ctx, st.runID, eff, st.sink)
	if resultErr != nil {
		resultErr = fmt.Errorf("adaptor: run %s: %w", st.runID, resultErr)
		return
	}

	resolved, err := a.resolveRun(ctx, st.runID, prompt, eff, resources)
	if err != nil {
		resultErr = fmt.Errorf("adaptor: run %s: %w", st.runID, err)
		return
	}

	var identity driver.AgentIdentity
	if eff.identity != nil {
		identity = eff.identity.driverIdentity()
	}
	fingerprint := ""
	if target != nil {
		mcpCompatibilityFingerprint := a.stabilizeHostedToolCompatibility(&resolved.req)
		fingerprint = a.threadInvocationFingerprint(identity, resolved.req, threadContract, mcpCompatibilityFingerprint)
		req := engine.SessionRequest{
			Namespace: threadNamespace,
			Key:       target.thread.key,
			Mode:      target.mode,
		}
		if target.mode == driver.SessionFork {
			// Keep the host key raw. The engine acquires the parent key and
			// record leases and resolves the parent under those leases.
			req.ForkFromKey = target.forkFromKey
		}
		plan, err = engine.PrepareThreadSessionForDriver(
			ctx, engineStore{store: a.defaults.threadStore, messages: st.hub, messageKey: target.thread.key}, req, identity, a.driver, fingerprint,
		)
		if err != nil {
			resultErr = target.thread.threadError(err)
			return
		}
		if plan == nil {
			resultErr = fmt.Errorf("adaptor: thread %q: internal: no session plan", target.thread.key)
			return
		}
		plan.StartLeaseRenewal(ctx, st.cancel)
	}

	request := resolved.req
	if plan != nil {
		request.Session = plan.DriverSession(a.driver)
	}
	if !st.hub.policy.Disabled {
		request.Messages = st.hub
	}
	if resources != nil {
		request.Prompt = resources.preamble + request.Prompt
	}
	// Resolve against the final request, including the enabled MessagePort,
	// resolved session and attachment preamble. This is the same request passed
	// to the first Driver.Run, so transport negotiation sees the actual inputs.
	if resolver, ok := a.driver.(driver.MessageCapabilityResolver); ok {
		st.hub.setCapability(resolver.ResolveMessageCapability(request))
	}
	if st.hub.startWasDeduped() {
		result = &Result{RunID: st.runID}
		return
	}
	var aggregate *Result
	var continuation []driver.MessageItem
	first := true
	for {
		st.hub.setPrompt(continuation)
		before := st.hub.turnCount()
		response, runErr := a.driver.Run(ctx, request, st.sink)
		if plan != nil && runErr != nil {
			var rejected *engine.ResumeRejectedError
			if errors.As(runErr, &rejected) {
				if first && plan.Reused() && plan.Mode() == driver.SessionContinueOrStart {
					if freshErr := plan.PrepareFresh(ctx, a.driver.Descriptor().Type, fingerprint); freshErr != nil {
						resultErr = target.thread.threadError(freshErr)
						return
					}
					request.Session = plan.DriverSession(a.driver)
					continue
				}
				runErr = fmt.Errorf("%w: %w", ErrResumeRejected, runErr)
			}
		}
		response, runErr = classifyInvocationOutcome(ctx, response, runErr, st.sink.pendingFailure())
		if runErr == nil {
			response.StructuredOutput, response.Failure = engine.FinalizeStructuredOutput(resolved.schema, resolved.source, response.Output, response.StructuredOutput, response.Failure)
		}
		success := runErr == nil && response.Failure == nil && st.sink.pendingFailure() == nil
		st.hub.ackPrompt(success, runErr)
		turnRes, turnErr := finalizeRun(st.runID, st.sink, response, runErr)
		actual := turnRes
		var business *RunError
		if actual == nil && errors.As(turnErr, &business) {
			actual = business.Result
		}
		if actual == nil {
			actual = resultFromResponse(st.runID, response)
		}
		if st.hub.turnCount() == before {
			st.hub.completeTurn(actual, turnErr)
		}
		aggregate = mergeExecutionResult(aggregate, actual, st.hub, before)
		if plan != nil {
			plan.StopLeaseRenewal()
			if renewErr := plan.RenewalError(); renewErr != nil {
				resultErr = target.thread.threadError(renewErr)
				return
			}
			if invocationCanPersist(ctx, a.driver, response, runErr, st.sink.pendingFailure()) {
				if _, persistErr := plan.Persist(ctx, identity, a.driver, fingerprint, response.Checkpoint); persistErr != nil {
					resultErr = target.thread.threadError(persistErr)
					return
				}
				target.thread.markEstablished()
			} else if success {
				if ctx.Err() != nil {
					resultErr = ctx.Err()
				} else {
					resultErr = target.thread.threadError(engine.ErrSessionCheckpointMissing)
				}
				return
			}
		}
		if turnErr != nil {
			if business != nil {
				finalError := *business
				finalError.Result = aggregate
				turnErr = &finalError
			}
			resultErr = turnErr
			return
		}
		if st.hub.turnCount() >= st.hub.policy.MaxTurns {
			st.hub.finish(MessageLimitExceeded)
			result = aggregate
			return
		}
		if st.hub.closeIfIdle() {
			result = aggregate
			return
		}
		// Driver.Run has returned. A final in-run empty claim is not an atomic
		// execution seal: messages admitted afterwards must be resumed by core.
		if !a.driver.Descriptor().Sessions.SupportsResume || !invocationCanPersist(ctx, a.driver, response, runErr, st.sink.pendingFailure()) {
			st.hub.finish(MessageProviderRejected)
			result = aggregate
			return
		}
		continuation = st.hub.Claim(driver.BoundaryTurnEnd)
		if len(continuation) == 0 {
			st.hub.finish(MessageProviderRejected)
			result = aggregate
			return
		}
		var texts []string
		for _, item := range continuation {
			texts = append(texts, item.Text)
		}
		request.Prompt = strings.Join(texts, "\n\n")
		if !st.hub.cap.PersistsDelivered {
			// Replay provider-ephemeral boundary input when changing transport turns.
			var replay []string
			for _, m := range st.hub.outcomes() {
				if m.State == MessageDelivered && m.Mechanism != "core.prompt" && m.Mechanism != "core.resume" && m.Mechanism != "core.dedupe" {
					mc := MessageContext{ID: m.ID, Text: m.Text, From: m.From, Timing: m.Timing}
					text := frameMessage(mc)
					if st.hub.policy.Format != nil {
						text = st.hub.policy.Format(mc)
					}
					replay = append(replay, text)
				}
			}
			if len(replay) > 0 {
				request.Prompt = strings.Join(replay, "\n\n") + "\n\n" + request.Prompt
			}
		}
		sessionID := st.runID
		if request.Session != nil {
			sessionID = request.Session.EngineSessionID
		}
		request.Session = &driver.SessionContext{EngineSessionID: sessionID, Mode: driver.SessionContinueOnly, State: engine.NormalizeSessionState(a.driver, response.Checkpoint.State)}
		if target == nil {
			request.Spawn = true
		}
		if plan != nil {
			plan.StartLeaseRenewal(ctx, st.cancel)
		}
		first = false
	}
}

// threadInvocationFingerprint covers every resolved value that can change
// resume correctness. The construction config is supplied by the driver via a
// stable, secret-safe contract; the remaining values are the concrete request
// handed to that same configured driver (including the acquired workspace and
// runtime-service attachment payloads).
func (a *Agent) threadInvocationFingerprint(identity driver.AgentIdentity, req driver.Request, contract threadDriverContract, mcpCompatibilityFingerprint string) string {
	runtimeCompatibility := threadRuntimeCompatibility(req.Runtime, mcpCompatibilityFingerprint)
	a.normalizeHostedToolServiceCompatibility(&runtimeCompatibility)
	// Hosted Tool normalization changes one URL after the generic view was
	// sorted. Re-sort so an ephemeral port cannot indirectly change collection
	// order when other runtime services are present.
	sortThreadRuntimeCompatibility(&runtimeCompatibility)
	return engine.StableHash(
		"adaptor/thread-invocation/v1",
		a.driver.Descriptor().Type,
		contract.codecName,
		contract.configFingerprint,
		identity,
		req.ModelOverride,
		req.Workspace,
		runtimeCompatibility,
		a.hostedToolProfileCompatibility(req.Profile),
		req.ProfilePayload.SessionFingerprint(),
		req.Skills.Fingerprint,
		engine.InstructionFingerprint(req.Instructions),
	)
}

type threadDriverContract struct {
	codecName         string
	configFingerprint string
}

// validateThreadDriverContract is the Thread prelaunch gate. It runs before
// workspace/runtime/profile acquisition and before any store operation, so an
// incomplete resume declaration cannot acquire a lease or launch the Driver.
func validateThreadDriverContract(d driver.Driver) (contract threadDriverContract, err error) {
	codecName, err := engine.ValidateThreadSessionDriver(d)
	if err != nil {
		return threadDriverContract{}, err
	}
	fingerprinter, ok := d.(driver.SessionConfigFingerprinter)
	if !ok {
		return threadDriverContract{}, &engine.SessionIncompatibleError{Reason: "resume-capable driver does not implement SessionConfigFingerprinter"}
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			contract = threadDriverContract{}
			err = &engine.SessionIncompatibleError{Reason: fmt.Sprintf("driver config fingerprinter panicked (%T)", recovered)}
		}
	}()
	first, fpErr := fingerprinter.SessionConfigFingerprint()
	if fpErr != nil {
		return threadDriverContract{}, &engine.SessionIncompatibleError{Reason: "driver config fingerprint failed: " + fpErr.Error()}
	}
	first = strings.TrimSpace(first)
	if first == "" {
		return threadDriverContract{}, &engine.SessionIncompatibleError{Reason: "driver returned an empty config fingerprint"}
	}
	second, fpErr := fingerprinter.SessionConfigFingerprint()
	if fpErr != nil {
		return threadDriverContract{}, &engine.SessionIncompatibleError{Reason: "driver config fingerprint stability check failed: " + fpErr.Error()}
	}
	if strings.TrimSpace(second) != first {
		return threadDriverContract{}, &engine.SessionIncompatibleError{Reason: "driver config fingerprint is not stable"}
	}
	return threadDriverContract{codecName: strings.TrimSpace(codecName), configFingerprint: first}, nil
}

// threadRuntimeCompatibility is the stable, secret-free part of the resolved
// runtime environment that can affect whether a provider session is safe to
// resume. RuntimePayload itself is deliberately not hashable for this purpose:
// SecretEnv carries freshly issued credentials whose values must reach every
// driver invocation but must neither invalidate a healthy Thread nor enter a
// durable compatibility fingerprint.
//
// Requested declarations and normalized ensured refs retain the fields that
// describe service identity and the actual endpoint exposed to the driver.
// Status and Health are observations, not endpoint identity, and are omitted
// so a transient probe result does not split a conversation. MCP is represented
// by the already-normalized effective MCP fingerprint; this covers both host
// servers and runtime-published servers without duplicating raw attachment
// material. Secret environment variable names are compatibility-relevant and
// non-secret, while their values are intentionally absent.
func threadRuntimeCompatibility(runtime driver.RuntimePayload, mcpFingerprint string) threadRuntimeCompatibilityView {
	view := threadRuntimeCompatibilityView{
		MCPFingerprint: mcpFingerprint,
		Requested:      make([]threadRuntimeServiceSpecView, 0, len(runtime.Requested)),
		Ensured:        make([]threadRuntimeServiceRefView, 0, len(runtime.Ensured)),
		SecretEnvNames: make([]string, 0, len(runtime.SecretEnv)),
	}
	for _, spec := range runtime.Requested {
		view.Requested = append(view.Requested, threadRuntimeServiceSpecView{
			ID:          spec.ID,
			Name:        spec.Name,
			URL:         spec.URL,
			Description: spec.Description,
			Lifecycle:   spec.Lifecycle,
			ReuseKey:    spec.ReuseKey,
			Command:     spec.Command,
			CWD:         spec.CWD,
			Port:        spec.Port,
			Metadata:    maps.Clone(spec.Metadata),
		})
	}
	for _, ref := range runtime.Ensured {
		view.Ensured = append(view.Ensured, threadRuntimeServiceRefView{
			ID:           ref.ID,
			Name:         ref.Name,
			URL:          ref.URL,
			Lifecycle:    ref.Lifecycle,
			ReuseKey:     ref.ReuseKey,
			Command:      ref.Command,
			CWD:          ref.CWD,
			Port:         ref.Port,
			OwnerAgentID: ref.OwnerAgentID,
			Metadata:     maps.Clone(ref.Metadata),
		})
	}
	for _, binding := range runtime.SecretEnv {
		if name := strings.TrimSpace(binding.Name); name != "" {
			view.SecretEnvNames = append(view.SecretEnvNames, name)
		}
	}

	sortThreadRuntimeCompatibility(&view)
	return view
}

func sortThreadRuntimeCompatibility(view *threadRuntimeCompatibilityView) {
	if view == nil {
		return
	}
	// Service collection order is not semantic: managers may discover the
	// same endpoints in a different order on the next process or run.
	slices.SortFunc(view.Requested, func(a, b threadRuntimeServiceSpecView) int {
		return strings.Compare(engine.StableHash(a), engine.StableHash(b))
	})
	slices.SortFunc(view.Ensured, func(a, b threadRuntimeServiceRefView) int {
		return strings.Compare(engine.StableHash(a), engine.StableHash(b))
	})
	slices.Sort(view.SecretEnvNames)
	view.SecretEnvNames = slices.Compact(view.SecretEnvNames)
}

type threadRuntimeCompatibilityView struct {
	Requested      []threadRuntimeServiceSpecView
	Ensured        []threadRuntimeServiceRefView
	MCPFingerprint string
	SecretEnvNames []string
}

type threadRuntimeServiceSpecView struct {
	ID          string
	Name        string
	URL         string
	Description string
	Lifecycle   driver.RuntimeServiceLifecycle
	ReuseKey    string
	Command     string
	CWD         string
	Port        int
	Metadata    map[string]string
}

type threadRuntimeServiceRefView struct {
	ID           string
	Name         string
	URL          string
	Lifecycle    driver.RuntimeServiceLifecycle
	ReuseKey     string
	Command      string
	CWD          string
	Port         int
	OwnerAgentID string
	Metadata     map[string]string
}

func invocationCanPersist(ctx context.Context, d driver.Driver, resp driver.Response, runErr error, pending *driver.RunFailure) bool {
	validCheckpoint := resp.Checkpoint != nil && resp.Checkpoint.Valid && resp.Checkpoint.State != nil &&
		strings.TrimSpace(resp.Checkpoint.State.ResumeID) != ""
	if validCheckpoint {
		normalized := engine.NormalizeSessionState(d, resp.Checkpoint.State)
		validCheckpoint = normalized != nil && strings.TrimSpace(normalized.ResumeID) != ""
	}
	return ctx.Err() == nil && runErr == nil && pending == nil && resp.Failure == nil &&
		resp.ExitCode == 0 && resp.Signal == "" && !resp.TimedOut &&
		validCheckpoint
}

// classifyInvocationOutcome supplies the single provider-agnostic fallback
// after a Driver has had the opportunity to parse its official protocol.
// Process outcome fields are audit data on driver.Response rather than a
// Driver.Run error, because non-zero exit can still carry useful raw output,
// transcript, usage, and a provider terminal payload. When no more specific
// provider or approval failure exists, convert that audit data into a
// structured business failure so resultFromResponse can preserve all of it.
func classifyInvocationOutcome(ctx context.Context, resp driver.Response, runErr error, pending *driver.RunFailure) (driver.Response, error) {
	if resp.Failure != nil || pending != nil {
		return resp, runErr
	}
	if runErr != nil {
		return resp, runErr
	}
	if err := ctx.Err(); err != nil {
		return resp, err
	}
	if resp.ExitCode == 0 && resp.Signal == "" && !resp.TimedOut {
		return resp, nil
	}

	metadata := make(map[string]any, 3)
	parts := make([]string, 0, 3)
	if resp.ExitCode != 0 {
		metadata["exit_code"] = resp.ExitCode
		parts = append(parts, fmt.Sprintf("exit code %d", resp.ExitCode))
	}
	if resp.Signal != "" {
		metadata["signal"] = resp.Signal
		parts = append(parts, fmt.Sprintf("signal %q", resp.Signal))
	}
	if resp.TimedOut {
		metadata["timed_out"] = true
		parts = append(parts, "timeout")
	}
	resp.Failure = &driver.RunFailure{
		Code:     driver.FailureAgentError,
		Message:  "driver process ended unsuccessfully: " + strings.Join(parts, ", "),
		Metadata: metadata,
	}
	return resp, nil
}

func finalizeRun(runID string, sink *eventSink, resp driver.Response, err error) (*Result, error) {
	pending := sink.pendingFailure()
	res := resultFromResponse(runID, resp)
	failure := resp.Failure
	if pending != nil {
		failure = pending
	}
	// A protocol-classified provider failure or SDK approval failure is more
	// specific than a concurrent process/context error. Preserve that verdict
	// and its partial Result instead of replacing it with a generic wrapper.
	if failure != nil {
		return nil, runErrorFromFailure(failure, res)
	}
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("adaptor: run %s: %w", runID, err)
		}
		if errors.Is(err, errApprovalAbort) {
			return nil, &RunError{
				Reason:  ReasonAgentError,
				Message: "approval aborted the run",
				Result:  res,
			}
		}
		return nil, fmt.Errorf("adaptor: run %s: %w", runID, err)
	}
	return res, nil
}

func runErrorFromFailure(f *driver.RunFailure, res *Result) *RunError {
	return &RunError{
		Reason:  failureReason(f.Code),
		Message: f.Message,
		Details: maps.Clone(f.Metadata),
		Result:  res,
	}
}

// mergeExecutionResult retains every call's audit data, the last turn's text,
// and the provider-reported usage for each individual turn exactly once.
func mergeExecutionResult(total, call *Result, h *messageHub, before int) *Result {
	if total == nil {
		total = &Result{RunID: call.RunID}
	}
	total.Text = call.Text
	total.Summary = call.Summary
	total.Model = call.Model
	total.Provider = call.Provider
	total.Metadata = maps.Clone(call.Metadata)
	total.structured = cloneStructuredOutput(call.structured)
	total.raw.Stdout += call.raw.Stdout
	total.raw.Stderr += call.raw.Stderr
	if call.raw.Terminal != nil {
		total.raw.Terminal = cloneTerminalPayload(call.raw.Terminal)
	}
	total.transcript = append(total.transcript, cloneTranscript(call.transcript)...)
	total.services = append(total.services, cloneServiceReports(call.services)...)
	h.mu.Lock()
	var usage *Usage
	for _, t := range h.turns[before:] {
		usage = addUsage(usage, t.Usage)
	}
	total.Turns = len(h.turns)
	if len(h.turns) > before {
		total.Text = h.turns[len(h.turns)-1].Text
	}
	h.mu.Unlock()
	if usage == nil {
		usage = call.Usage
	}
	total.Usage = addUsage(total.Usage, usage)
	return total
}
