package engine

import "context"

// ThreadSessionPlan is the additive v1-facing entry over the historical
// session coordination logic (P2 seam choice: reuse, not replicate). It
// wraps the unexported resolvedSessionPlan produced by prepareSessionPlan so
// the next/ Thread path shares the exact plan/lease/fingerprint/persistence
// semantics of the legacy Core.Execute path — mode mapping, key + session
// lease acquisition, ticker-driven renewal, compatibility gating, and
// Finalize archive/rebind all stay single-sourced in session.go.
//
// The legacy path keeps calling the unexported functions directly; nothing
// about its behavior changes.
type ThreadSessionPlan struct {
	plan  *resolvedSessionPlan
	store SessionStore
}

// PrepareThreadSession resolves a session plan against store for the given
// request. Semantics are identical to the legacy prepare step:
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

// DriverSession builds the per-run driver session context. It applies the
// same state-attachment rule as the legacy execute path: driver state is
// forwarded only when a record exists and the plan either reuses it or
// forks from it, normalized through the driver's session codec.
func (p *ThreadSessionPlan) DriverSession(adapter DriverAdapter) *DriverSessionContext {
	var state *DriverSessionState
	if p.plan.record != nil && (p.plan.reused || p.plan.request.Mode == SessionFork) {
		state = normalizeSessionState(adapter, p.plan.record.DriverState)
	}
	return &DriverSessionContext{
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
// persist) and a new session lease is acquired. Mirrors the legacy
// ResumeRejected fallback, which only applies when Reused() and the mode is
// SessionContinueOrStart.
func (p *ThreadSessionPlan) PrepareFresh(ctx context.Context, driverType, fingerprint string) error {
	return p.plan.prepareFresh(ctx, p.store, driverType, fingerprint)
}

// Persist finalizes the post-run session state through the store: it
// validates held leases, saves the record (state normalized through the
// driver codec), archives the previous record when one was displaced, and
// rebinds the key's active mapping. A nil/invalid checkpoint yields
// ErrSessionCheckpointMissing, which callers may tolerate exactly like the
// legacy path does (human-decision failures without a resumable
// checkpoint).
func (p *ThreadSessionPlan) Persist(
	ctx context.Context,
	identity AgentIdentity,
	adapter DriverAdapter,
	fingerprint string,
	checkpoint *DriverCheckpoint,
) (*SessionRef, error) {
	return persistSessionPlan(ctx, p.store, p.plan, identity, adapter, fingerprint, checkpoint)
}
