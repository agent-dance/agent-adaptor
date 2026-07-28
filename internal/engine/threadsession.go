package engine

import (
	"context"
	"strings"
)

// ThreadSessionPlan owns one Thread operation's leases, compatibility state,
// resume/fork plan, and atomic persistence request.
type ThreadSessionPlan struct {
	plan  *resolvedSessionPlan
	store SessionStore
}

// PrepareThreadSession resolves a session plan against store for the given
// request:
//
//   - Stateless mode (or an empty request) yields (nil, nil).
//   - A nil store yields ErrSessionStoreRequired.
//   - Key and session leases are acquired (busy → *SessionBusyError), the
//     current record is resolved, and the per-mode plan logic runs
//     (ContinueOnly / ContinueOrStart / StartNew / Fork).
//
// On success the caller owns the plan and must call Release when done.
func PrepareThreadSession(
	ctx context.Context,
	store SessionStore,
	req SessionRequest,
	identity AgentIdentity,
	driverType string,
	fingerprint string,
) (*ThreadSessionPlan, error) {
	plan, err := prepareSessionPlan(ctx, store, req, identity, driverType, fingerprint)
	if err != nil {
		return nil, err
	}
	if plan == nil {
		return nil, nil
	}
	return &ThreadSessionPlan{plan: plan, store: store}, nil
}

// PrepareThreadSessionForDriver is the v1 coordinator entry. In addition to
// normal planning it derives the codec identity from the configured Driver
// and proves every reused checkpoint or fork parent's checkpoint can be
// normalized by that exact codec before returning a runnable plan. Callers
// must prefer this entry over supplying SessionRequest.SessionCodec themselves.
func PrepareThreadSessionForDriver(
	ctx context.Context,
	store SessionStore,
	req SessionRequest,
	identity AgentIdentity,
	driverImpl Driver,
	fingerprint string,
) (*ThreadSessionPlan, error) {
	codec, err := resumeSessionCodecFor(driverImpl)
	if err != nil {
		return nil, err
	}
	driverType := driverImpl.Descriptor().Type
	req.SessionCodec = strings.TrimSpace(codec.Name())
	plan, err := prepareSessionPlan(ctx, store, req, identity, driverType, fingerprint)
	if err != nil || plan == nil {
		return nil, err
	}
	if plan.record != nil && (plan.reused || req.Mode == SessionFork) {
		if stateErr := validateResumableRecord(plan.record, codec); stateErr != nil {
			// Structurally invalid durable state is corruption, not a provider
			// resume rejection. Starting over here would silently discard a
			// conversation without giving the provider a chance to classify it.
			return nil, plan.releaseAfter(stateErr)
		}
	}
	return &ThreadSessionPlan{plan: plan, store: store}, nil
}

// DriverSession builds the per-run driver session context. Driver state is
// forwarded only when a record exists and the plan either reuses it or forks
// from it, normalized through the driver's session codec.
func (p *ThreadSessionPlan) DriverSession(adapter Driver) *SessionContext {
	var state *SessionState
	if p.plan.record != nil && (p.plan.reused || p.plan.request.Mode == SessionFork) {
		state = normalizeSessionState(adapter, p.plan.record.DriverState)
	}
	return &SessionContext{
		EngineSessionID: p.plan.engineID,
		Mode:            p.plan.request.Mode,
		State:           state,
		PreviousID:      p.plan.previousID,
	}
}

// StartLeaseRenewal spawns the ticker-driven renewal goroutine over every
// held lease. On renewal failure the error is recorded (RenewalError) and
// cancel is invoked so the in-flight run aborts instead of continuing
// without exclusivity.
func (p *ThreadSessionPlan) StartLeaseRenewal(ctx context.Context, cancel context.CancelFunc) {
	p.plan.startLeaseRenewal(ctx, p.store, cancel)
}

// StopLeaseRenewal stops the renewal goroutine and waits for it to exit.
func (p *ThreadSessionPlan) StopLeaseRenewal() {
	p.plan.stopLeaseRenewal()
}

// RenewalError reports the first lease renewal failure, if any. Callers
// must check it after StopLeaseRenewal and treat a non-nil result as fatal
// for the run (the session state must not be persisted).
func (p *ThreadSessionPlan) RenewalError() error {
	return p.plan.renewalError()
}

// Release stops renewal and releases all held leases in reverse acquisition
// order. It is safe to call after Persist and on every error path.
func (p *ThreadSessionPlan) Release() {
	p.plan.release()
}

// ReleaseContext stops renewal and releases every held lease, returning any
// store error. The call is bounded by ctx even when a broken Store ignores
// cancellation. Final coordinators use this method and surface its error.
func (p *ThreadSessionPlan) ReleaseContext(ctx context.Context) error {
	return p.plan.releaseContext(ctx)
}

// Reused reports whether the plan resumes an existing compatible record.
func (p *ThreadSessionPlan) Reused() bool {
	return p.plan.reused
}

// Mode reports the effective session mode of the plan.
func (p *ThreadSessionPlan) Mode() SessionMode {
	return p.plan.request.Mode
}

// PrepareFresh rewires the plan to a brand-new session after the driver
// rejected a resume: the old engine ID becomes PreviousID (archived on
// persist) and a new session lease is acquired. This resume-rejection
// fallback only applies when Reused() and the mode is
// SessionContinueOrStart.
func (p *ThreadSessionPlan) PrepareFresh(ctx context.Context, driverType, fingerprint string) error {
	return p.plan.prepareFresh(ctx, p.store, driverType, fingerprint)
}

// Persist finalizes the post-run session state through the store: it
// validates held leases, saves the record (state normalized through the
// driver codec), archives the previous record when one was displaced, and
// rebinds the key's active mapping. A nil/invalid checkpoint yields
// ErrSessionCheckpointMissing.
func (p *ThreadSessionPlan) Persist(
	ctx context.Context,
	identity AgentIdentity,
	adapter Driver,
	fingerprint string,
	checkpoint *Checkpoint,
) (*SessionRef, error) {
	return persistSessionPlan(ctx, p.store, p.plan, identity, adapter, fingerprint, checkpoint)
}
