package adaptor

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/activebudget"
	"github.com/agent-dance/agent-adaptor/internal/engine"
	"github.com/agent-dance/agent-adaptor/internal/systemprompt"
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

// startInvocation is the only asynchronous execution entry. Agent.Stream and
// Thread.Stream differ solely in whether they supply a Thread target; both Run
// methods are literally Stream + drain + Result.
func (a *Agent) startInvocation(ctx context.Context, prompt string, opts []CallOption, target *invocationTarget) Stream {
	threadKey := ""
	if target != nil && target.thread != nil {
		threadKey = target.thread.key
	}
	st, eff, runCtx, ok := a.openStream(ctx, opts, threadKey)
	if !ok {
		return st
	}
	go a.executeInvocation(runCtx, st, prompt, &eff, target)
	return st
}

// executeInvocation owns every phase between option resolution and terminal
// teardown. In particular, this file contains the sole production Driver.Run
// call and the sole ThreadSessionPlan.Persist call in the root execution path.
func (a *Agent) executeInvocation(ctx context.Context, st *runStream, prompt string, eff *RunSettings, target *invocationTarget) {
	defer a.unregisterRun(st.runID)
	st.sink.enableAuthoritativeLifecycle()
	var (
		resources       *runResources
		plan            *engine.ThreadSessionPlan
		threadContract  threadDriverContract
		result          *Result
		resultErr       error
		response        driver.Response
		driverEntered   bool
		coordinationErr error
		fallbackErr     error
		schemaRequested bool
	)

	defer func() {
		if st.budget != nil {
			st.budget.Stop()
		}
		st.sink.recordContext(ctx)
		if !driverEntered && ctx.Err() != nil {
			resultErr = errors.Join(resultErr, ctx.Err(), context.Cause(ctx))
		}
		if plan != nil {
			plan.StopLeaseRenewal()
			if renewErr := plan.RenewalError(); renewErr != nil && !errors.Is(coordinationErr, renewErr) {
				coordinationErr = errors.Join(coordinationErr, target.thread.threadError(renewErr))
			}
		}
		st.sink.recordOutcome(ctx, response.Failure, resultErr, coordinationErr)
		st.sink.finishTerminal()
		if !driverEntered {
			// The controller may select expiry before parent cancellation wins
			// the standard child cause. Preserve that selected evidence without
			// fabricating a pre-dispatch Result/RunError.
			resultErr = errors.Join(resultErr, st.sink.terminalSnapshot().cause)
		}
		// Every exit after dispatch maps the same accumulated Response exactly
		// once, including failed fallback preparation and Thread finalization.
		if driverEntered {
			// Interrupted schema runs cannot use Decode's schema-less Text
			// fallback. Mark missing data unavailable without validating output
			// or replacing the execution's original cause.
			if schemaRequested && response.StructuredOutput == nil {
				response.StructuredOutput = &driver.StructuredOutput{ValidationErrors: []string{"structured output was not validated before execution ended"}}
			}
			if resultErr != nil || coordinationErr != nil || response.Failure != nil || st.sink.pendingFailure() != nil {
				resultErr = errors.Join(resultErr, fallbackErr)
			}
			result, resultErr = finalizeRun(st.runID, st.sink, response, resultErr, coordinationErr)
		}
		// Stop renewal before releasing leases. ReleaseContext remains bounded
		// even for a broken store and its error is part of the observable run
		// outcome instead of disappearing in a defer.
		if plan != nil {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), invocationCleanupTimeout)
			releaseErr := plan.ReleaseContext(cleanupCtx)
			cancel()
			if releaseErr != nil {
				releaseErr = target.thread.threadError(releaseErr)
				result, resultErr = appendInvocationCleanupError(result, resultErr, releaseErr)
			}
		}

		backfillRunServices(resources, result, resultErr)
		if teardownErr := resources.finish(ctx); teardownErr != nil {
			teardownErr = fmt.Errorf("adaptor: run %s teardown: %w", st.runID, teardownErr)
			result, resultErr = appendInvocationCleanupError(result, resultErr, teardownErr)
		}
		st.res, st.err = result, resultErr
		st.sink.completeAuthoritativeLifecycle(result, resultErr)
		st.sink.close()
		close(st.done)
		st.cancel()
		if st.budget != nil {
			st.budget.Cancel(nil)
		}
	}()

	if target != nil {
		if a.defaults.threadStore == nil {
			resultErr = fmt.Errorf("%w (thread %q)", ErrThreadStoreRequired, target.thread.key)
			return
		}
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

	// Timing starts after static Thread checks and before the first resource.
	var limit time.Duration
	if eff.policy != nil {
		limit = eff.policy.ActiveExecutionTimeout
	}
	expired := &ActiveExecutionTimeoutError{Limit: limit}
	ctx, st.budget = activebudget.New(ctx, limit, expired, eff.budgetTiming.clock)
	st.sink.bindBudget(ctx, st.budget, expired)

	// Hosted Tool profile resolution may call provider code and allocate an
	// Agent-owned directory. Keep it inside lifecycle admission so Close sees,
	// cancels, and drains the work before removing profiles and the Tool runtime.
	// Thread-only contract checks stay ahead of this resource-producing phase.
	if profileErr := a.prepareHostedToolProfile(ctx, eff); profileErr != nil {
		resultErr = fmt.Errorf("adaptor: run %s: %w", st.runID, profileErr)
		return
	}

	// Lock the actual hosted execution directory through claim, resolution,
	// immutable snapshot and Driver return; unrelated profiles remain independent.
	profileGate, gateErr := a.lockHostedToolRun(ctx, eff.effectiveProfile)
	if gateErr != nil {
		resultErr = gateErr
		return
	}
	if profileGate != nil {
		defer profileGate.Unlock()
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
	schemaRequested = resolved.schema != nil

	var identity driver.AgentIdentity
	if eff.identity != nil {
		identity = eff.identity.driverIdentity()
	}
	mcpCompatibilityFingerprint, profileSnapshot, snapshotErr := a.stabilizeHostedToolCompatibility(ctx, &resolved.req)
	if snapshotErr != nil {
		resultErr = fmt.Errorf("adaptor: resolved profile snapshot: %w", snapshotErr)
		return
	}
	fingerprint := ""
	if target != nil {
		fingerprint = a.threadInvocationFingerprint(identity, resolved.req, threadContract, mcpCompatibilityFingerprint, profileSnapshot)
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
			ctx, engineStore{store: a.defaults.threadStore}, req, identity, a.driver, fingerprint,
		)
		if err != nil {
			resultErr = target.thread.threadError(err)
			return
		}
		if plan == nil {
			resultErr = fmt.Errorf("adaptor: thread %q: internal: no session plan", target.thread.key)
			return
		}
		plan.StartLeaseRenewal(ctx, func() {
			st.sink.recordOutcome(ctx, nil, nil, plan.RenewalError())
			st.cancel()
		})
	}

	request := resolved.req
	for {
		if ctx.Err() != nil && (driverEntered || errors.Is(context.Cause(ctx), ErrActiveExecutionTimeout)) {
			resultErr = errors.Join(resultErr, ctx.Err(), context.Cause(ctx))
			return
		}
		if plan != nil {
			request.Session = plan.DriverSession(a.driver)
		}
		// Architectural invariant: this is the only Driver.Run call in the
		// root execution pipeline. Retry changes only the prepared session
		// context.
		driverEntered = true
		attempt, runErr := a.driver.Run(ctx, request, st.sink)
		response = mergeInvocationAudit(response, attempt)
		// The Driver owns protocol classification; core fills an unclassified
		// abnormal process outcome while retaining specific approval/provider
		// failures and every observed context cause.
		response, resultErr = classifyInvocationOutcome(ctx, response, runErr, st.sink.pendingFailure())

		if plan != nil && resultErr != nil {
			var rejected *engine.ResumeRejectedError
			if errors.As(resultErr, &rejected) {
				if plan.Reused() && plan.Mode() == driver.SessionContinueOrStart && ctx.Err() == nil && st.sink.pendingFailure() == nil {
					if freshErr := plan.PrepareFresh(ctx, a.driver.Descriptor().Type, fingerprint); freshErr != nil {
						coordinationErr = target.thread.threadError(freshErr)
						return
					}
					fallbackErr = resultErr
					continue
				}
				resultErr = fmt.Errorf("%w: %w", ErrResumeRejected, resultErr)
			}
		}

		st.sink.recordOutcome(ctx, response.Failure, resultErr, nil)

		if plan != nil {
			plan.StopLeaseRenewal()
			if renewErr := plan.RenewalError(); renewErr != nil {
				coordinationErr = target.thread.threadError(renewErr)
				return
			}
		}

		if resultErr == nil && st.sink.pendingFailure() == nil {
			response.StructuredOutput, response.Failure = engine.FinalizeStructuredOutput(
				resolved.schema, resolved.source, response.Output, response.StructuredOutput, response.Failure,
			)
		}

		// Structured validation may create a new concrete failure after Run.
		st.sink.recordOutcome(ctx, response.Failure, resultErr, nil)
		if plan == nil && resultErr == nil && response.Failure == nil && st.sink.pendingFailure() == nil {
			resultErr = st.sink.finishExecution(ctx)
		}
		if plan != nil && invocationCanPersist(ctx, a.driver, response, resultErr, st.sink.pendingFailure()) {
			if sealErr := st.sink.finishExecution(ctx); sealErr != nil {
				resultErr = sealErr
				return
			}
			// Architectural invariant: this is the only Thread persistence point.
			if _, persistErr := plan.Persist(ctx, identity, a.driver, fingerprint, response.Checkpoint); persistErr != nil {
				coordinationErr = target.thread.threadError(persistErr)
				return
			}
			target.thread.markEstablished()
		} else if plan != nil && resultErr == nil && response.Failure == nil && st.sink.pendingFailure() == nil {
			// A nominally successful Thread run must prove it is both healthy
			// and resumable. Failed/cancelled/business-failure runs simply skip
			// persistence, preserving the previous healthy active record.
			if cancelErr := ctx.Err(); cancelErr != nil {
				resultErr = errors.Join(cancelErr, context.Cause(ctx))
				return
			}
			coordinationErr = target.thread.threadError(engine.ErrSessionCheckpointMissing)
			return
		}

		return
	}
}

// threadInvocationFingerprint covers every resolved value that can change
// resume correctness. The construction config is supplied by the driver via a
// stable, secret-safe contract; the remaining values are the concrete request
// handed to that same configured driver (including the acquired workspace and
// runtime-service attachment payloads).
func (a *Agent) threadInvocationFingerprint(identity driver.AgentIdentity, req driver.Request, contract threadDriverContract, mcpCompatibilityFingerprint string, profileSnapshot any) string {
	runtimeCompatibility := threadRuntimeCompatibility(req.Runtime, mcpCompatibilityFingerprint)
	a.normalizeHostedToolServiceCompatibility(&runtimeCompatibility)
	// Hosted Tool normalization changes one URL after the generic view was
	// sorted. Re-sort so an ephemeral port cannot indirectly change collection
	// order when other runtime services are present.
	sortThreadRuntimeCompatibility(&runtimeCompatibility)
	// Streaming is this turn's delivery choice. Incompatible checkpoint or
	// session environments remain guarded by the configured Driver/codec and
	// the resolved resource dimensions below, not by this per-turn boolean.
	base := engine.StableHash(
		"adaptor/thread-invocation/v1",
		a.driver.Descriptor().Type,
		contract.codecName,
		contract.configFingerprint,
		identity,
		req.ModelOverride,
		req.Workspace,
		runtimeCompatibility,
		profileSnapshot,
		req.ProfilePayload.SessionFingerprint(),
		req.Skills.Fingerprint,
		engine.InstructionFingerprint(req.Instructions),
	)
	if req.AppendSystemPrompt == "" {
		return base
	}
	return engine.StableHash("adaptor/thread-append-system-prompt/v1", base, systemprompt.Fingerprint(req.AppendSystemPrompt))
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
	if ctx.Err() != nil {
		runErr = errors.Join(runErr, ctx.Err(), context.Cause(ctx))
	}
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

func finalizeRun(runID string, sink *eventSink, resp driver.Response, err, coordinationErr error) (*Result, error) {
	pending := sink.pendingFailure()
	res := resultFromResponse(runID, resp)
	if outcome := sink.terminalSnapshot(); outcome.reason != "" {
		return nil, &RunError{Reason: outcome.reason, Message: outcome.message, Details: maps.Clone(outcome.details), Result: res, Cause: errors.Join(outcome.cause, err, coordinationErr)}
	}
	failure := resp.Failure
	if pending != nil {
		failure = pending
	}
	// A protocol-classified provider failure or SDK approval failure is more
	// specific than a concurrent process/context error. Preserve that verdict
	// and its partial Result instead of replacing it with a generic wrapper.
	if failure != nil {
		runErr := runErrorFromFailure(failure, res)
		runErr.Cause = errors.Join(err, coordinationErr)
		return nil, runErr
	}
	if coordinationErr != nil {
		return nil, &RunError{Reason: ReasonInfrastructure, Message: "Thread coordination failed", Result: res, Cause: errors.Join(err, coordinationErr)}
	}
	if err != nil {
		reason, message := ReasonInfrastructure, "execution failed"
		switch {
		case errors.Is(err, ErrActiveExecutionTimeout):
			reason, message = ReasonActiveExecutionTimeout, "active execution budget exhausted"
		case errors.Is(err, context.DeadlineExceeded):
			reason, message = ReasonDeadlineExceeded, "execution deadline exceeded"
		case errors.Is(err, context.Canceled):
			reason, message = ReasonCancelled, "execution cancelled"
		}
		if errors.Is(err, errApprovalAbort) {
			reason, message = ReasonAgentError, "approval aborted the run"
		}
		return nil, &RunError{Reason: reason, Message: message, Result: res, Cause: err}
	}
	return res, nil
}

// Cleanup is secondary to an already classified execution failure. A healthy
// success can become an infrastructure failure, retaining its committed Result.
// Pre-dispatch errors have no Result and keep their existing wrapped identity.
func appendInvocationCleanupError(res *Result, err, cleanup error) (*Result, error) {
	if runErr, ok := err.(*RunError); ok {
		runErr.Cause = errors.Join(runErr.Cause, cleanup)
		return nil, runErr
	}
	if res != nil {
		return nil, &RunError{Reason: ReasonInfrastructure, Message: "execution cleanup failed", Result: res, Cause: cleanup}
	}
	return nil, errors.Join(err, cleanup)
}

// A safe resume rejection can precede one fresh attempt in the same invocation.
// Retain audit observations from both attempts; execution health and checkpoint
// always belong solely to the latest attempt. No provider payload is parsed here.
func mergeInvocationAudit(previous, current driver.Response) driver.Response {
	if previous.RawStreams != nil {
		raw := cloneRawStreams(*previous.RawStreams)
		if current.RawStreams != nil {
			raw.Stdout += current.RawStreams.Stdout
			raw.Stderr += current.RawStreams.Stderr
			if current.RawStreams.Terminal != nil {
				raw.Terminal = cloneTerminalPayload(current.RawStreams.Terminal)
			}
		}
		current.RawStreams = &raw
	}
	current.Transcript = append(cloneTranscript(previous.Transcript), current.Transcript...)
	if previous.Usage != nil {
		usage := *previous.Usage
		if current.Usage != nil {
			usage.InputTokens += current.Usage.InputTokens
			usage.OutputTokens += current.Usage.OutputTokens
			usage.CachedInputTokens += current.Usage.CachedInputTokens
			usage.EstimatedCostMilli += current.Usage.EstimatedCostMilli
		}
		current.Usage = &usage
	}
	current.RuntimeServices = mergeServiceReports(previous.RuntimeServices, current.RuntimeServices)
	if previous.Metadata != nil {
		metadata := maps.Clone(previous.Metadata)
		maps.Copy(metadata, current.Metadata)
		current.Metadata = metadata
	}
	return current
}

func runErrorFromFailure(f *driver.RunFailure, res *Result) *RunError {
	return &RunError{
		Reason:  failureReason(f.Code),
		Message: f.Message,
		Details: maps.Clone(f.Metadata),
		Result:  res,
	}
}
