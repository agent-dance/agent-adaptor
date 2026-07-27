package adaptor

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
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
// call and the sole ThreadSessionPlan.Persist call in next/.
func (a *Agent) executeInvocation(ctx context.Context, st *runStream, prompt string, eff *RunSettings, target *invocationTarget) {
	var (
		resources *runResources
		plan      *engine.ThreadSessionPlan
		result    *Result
		resultErr error
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
		st.res, st.err = result, resultErr
		resources.finish(ctx, st.sink)
		close(st.done)
		st.cancel()
	}()

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
		if a.defaults.threadStore == nil {
			resultErr = fmt.Errorf("%w (thread %q)", ErrThreadStoreRequired, target.thread.key)
			return
		}
		fingerprint, err = a.threadInvocationFingerprint(identity, resolved.req)
		if err != nil {
			resultErr = target.thread.threadError(err)
			return
		}
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
		plan.StartLeaseRenewal(ctx, st.cancel)
	}

	request := resolved.req
	for {
		if plan != nil {
			request.Session = plan.DriverSession(a.driver)
		}
		// Architectural invariant: this is the only Driver.Run call in the
		// v1 facade. Retry changes only the prepared session context.
		response, runErr := a.driver.Run(ctx, request, st.sink)

		if plan != nil && runErr != nil {
			var rejected *engine.ResumeRejectedError
			if errors.As(runErr, &rejected) {
				if plan.Reused() && plan.Mode() == driver.SessionContinueOrStart {
					if freshErr := plan.PrepareFresh(ctx, a.driver.Descriptor().Type, fingerprint); freshErr != nil {
						resultErr = target.thread.threadError(freshErr)
						return
					}
					continue
				}
				runErr = fmt.Errorf("%w: %w", ErrResumeRejected, runErr)
			}
		}

		if plan != nil {
			plan.StopLeaseRenewal()
			if renewErr := plan.RenewalError(); renewErr != nil {
				resultErr = target.thread.threadError(renewErr)
				return
			}
		}

		if runErr == nil {
			response.StructuredOutput, response.Failure = engine.FinalizeStructuredOutput(
				resolved.schema, resolved.source, response.Output, response.StructuredOutput, response.Failure,
			)
		}

		if plan != nil && invocationCanPersist(ctx, a.driver, response, runErr, st.sink.pendingFailure()) {
			// Architectural invariant: this is the only v1 persistence point.
			if _, persistErr := plan.Persist(ctx, identity, a.driver, fingerprint, response.Checkpoint); persistErr != nil {
				resultErr = target.thread.threadError(persistErr)
				return
			}
			target.thread.markEstablished()
		} else if plan != nil && runErr == nil && response.Failure == nil && st.sink.pendingFailure() == nil {
			// A nominally successful Thread run must prove it is both healthy
			// and resumable. Failed/cancelled/business-failure runs simply skip
			// persistence, preserving the previous healthy active record.
			if healthErr := invocationPersistenceHealthError(ctx, response); healthErr != nil {
				resultErr = fmt.Errorf("adaptor: run %s: %w", st.runID, healthErr)
				return
			}
			resultErr = target.thread.threadError(engine.ErrSessionCheckpointMissing)
			return
		}

		result, resultErr = finalizeRun(st.runID, st.sink, response, runErr)
		return
	}
}

// threadInvocationFingerprint covers every resolved value that can change
// resume correctness. The construction config is supplied by the driver via a
// stable, secret-safe contract; the remaining values are the concrete request
// handed to that same configured driver (including the acquired workspace and
// runtime-service attachment payloads).
func (a *Agent) threadInvocationFingerprint(identity driver.AgentIdentity, req driver.Request) (string, error) {
	fingerprinter, ok := a.driver.(driver.SessionConfigFingerprinter)
	if !ok {
		return "", fmt.Errorf("%w: driver %q does not implement driver.SessionConfigFingerprinter", ErrThreadIncompatible, a.driver.Descriptor().Type)
	}
	configFingerprint, err := fingerprinter.SessionConfigFingerprint()
	if err != nil {
		return "", fmt.Errorf("%w: driver config fingerprint: %w", ErrThreadIncompatible, err)
	}
	if strings.TrimSpace(configFingerprint) == "" {
		return "", fmt.Errorf("%w: driver %q returned an empty config fingerprint", ErrThreadIncompatible, a.driver.Descriptor().Type)
	}
	codecName, err := validatedSessionCodecName(a.driver)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrThreadIncompatible, err)
	}
	return engine.StableHash(
		"adaptor/thread-invocation/v1",
		a.driver.Descriptor().Type,
		codecName,
		configFingerprint,
		identity,
		req.ModelOverride,
		req.Workspace,
		threadRuntimeCompatibility(req.Runtime, req.MCP.Fingerprint),
		req.Profile,
		req.ProfilePayload.Fingerprint,
		req.Skills.Fingerprint,
		engine.InstructionFingerprint(req.Instructions),
	), nil
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
	return view
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

func validatedSessionCodecName(d driver.Driver) (name string, err error) {
	codec := engine.SessionCodecFor(d)
	if codec == nil {
		return "", errors.New("driver session codec is nil")
	}
	value := reflect.ValueOf(codec)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		if value.IsNil() {
			return "", errors.New("driver session codec is nil")
		}
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			name = ""
			err = fmt.Errorf("driver session codec Name panicked (%T)", recovered)
		}
	}()
	name = strings.TrimSpace(codec.Name())
	if name == "" {
		return "", errors.New("driver session codec has an empty stable name")
	}
	return name, nil
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

func invocationPersistenceHealthError(ctx context.Context, resp driver.Response) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if resp.ExitCode != 0 {
		return fmt.Errorf("driver returned non-zero exit code %d without a classified failure", resp.ExitCode)
	}
	if resp.Signal != "" {
		return fmt.Errorf("driver terminated by signal %q without a classified failure", resp.Signal)
	}
	if resp.TimedOut {
		return context.DeadlineExceeded
	}
	return nil
}

func finalizeRun(runID string, sink *eventSink, resp driver.Response, err error) (*Result, error) {
	pending := sink.pendingFailure()
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("adaptor: run %s: %w", runID, err)
		}
		if pending != nil {
			return nil, runErrorFromFailure(pending, resultFromResponse(runID, resp))
		}
		if errors.Is(err, errApprovalAbort) {
			return nil, &RunError{
				Reason:  ReasonAgentError,
				Message: "approval aborted the run",
				Result:  resultFromResponse(runID, resp),
			}
		}
		return nil, fmt.Errorf("adaptor: run %s: %w", runID, err)
	}

	res := resultFromResponse(runID, resp)
	failure := resp.Failure
	if pending != nil {
		failure = pending
	}
	if failure != nil {
		return nil, runErrorFromFailure(failure, res)
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
