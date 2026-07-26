package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

// LeaseTTL and LeaseRenewInterval report the session lease timing knobs.
// They are function variables (not plain durations) because the historical
// package vars defaultLeaseTTL / defaultLeaseRenewInterval stay declared in
// the root package, where internal tests mutate them; the root package
// injects closures over those vars at init time so mutations keep flowing
// into the engine exactly as before.
var (
	LeaseTTL           = func() time.Duration { return 5 * time.Minute }
	LeaseRenewInterval = func() time.Duration { return 2 * time.Minute }
)

type resolvedSessionPlan struct {
	request       SessionRequest
	engineID      string
	record        *SessionRecord
	keyLeaseID    string
	reused        bool
	created       bool
	previousID    string
	compatibility SessionCompatibility
	owner         string
	store         SessionStore
	leaseMu       sync.Mutex
	leases        []SessionLease
	renewCancel   context.CancelFunc
	renewWG       sync.WaitGroup
	renewErrMu    sync.Mutex
	renewErr      error
}

func resolveSessionDefaults(req SessionRequest) SessionRequest {
	if req.Mode != "" {
		return req
	}
	if req.ID != "" {
		req.Mode = SessionContinueOnly
		return req
	}
	if req.Key != "" {
		req.Mode = SessionContinueOrStart
		return req
	}
	req.Mode = SessionStateless
	return req
}

func sessionKeyLeaseID(namespace, key string) string {
	return "key:" + namespace + ":" + key
}

func newEngineSessionID(driverType, fingerprint string) string {
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return stableHash("session", driverType, fingerprint, time.Now().UnixNano())
	}
	return stableHash("session", driverType, fingerprint, hex.EncodeToString(suffix[:]))
}

func newLeaseOwner(identity AgentIdentity, driverType string, req SessionRequest) string {
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return stableHash("lease_owner", time.Now().UnixNano(), identity, driverType, req)
	}
	return stableHash("lease_owner", identity, driverType, req, hex.EncodeToString(suffix[:]))
}

func sessionCompatibility(expected, actual string) SessionCompatibility {
	if expected == "" || actual == "" {
		return SessionCompatibility{
			Status: SessionCompatibilityIncompatible,
			Reason: "missing fingerprint",
		}
	}
	if expected == actual {
		return SessionCompatibility{
			Status:              SessionCompatibilityCompatible,
			ExpectedFingerprint: expected,
			ActualFingerprint:   actual,
		}
	}
	return SessionCompatibility{
		Status:              SessionCompatibilityIncompatible,
		Reason:              "run fingerprint changed",
		ExpectedFingerprint: expected,
		ActualFingerprint:   actual,
	}
}

func (p *resolvedSessionPlan) acquireLease(ctx context.Context, store SessionStore, target string) error {
	if target == "" || store == nil {
		return nil
	}
	lease, err := store.AcquireLease(ctx, target, p.owner, LeaseTTL())
	if err != nil {
		return err
	}
	p.leaseMu.Lock()
	p.rememberLeaseLocked(lease)
	p.leaseMu.Unlock()
	return nil
}

func (p *resolvedSessionPlan) release() {
	p.stopLeaseRenewal()

	p.leaseMu.Lock()
	leases := append([]SessionLease(nil), p.leases...)
	p.leases = nil
	p.leaseMu.Unlock()

	for index := len(leases) - 1; index >= 0; index-- {
		_ = p.releaseLease(leases[index])
	}
}

func (p *resolvedSessionPlan) releaseLease(lease SessionLease) error {
	if p == nil || p.store == nil {
		return nil
	}
	return p.store.ReleaseLease(context.Background(), lease)
}

func (p *resolvedSessionPlan) stopLeaseRenewal() {
	if p == nil {
		return
	}
	if p.renewCancel != nil {
		p.renewCancel()
		p.renewWG.Wait()
		p.renewCancel = nil
	}
}

func (p *resolvedSessionPlan) prepareFresh(ctx context.Context, store SessionStore, driverType, fingerprint string) error {
	p.previousID = p.engineID
	p.engineID = newEngineSessionID(driverType, fingerprint)
	p.reused = false
	p.created = true
	p.record = nil
	p.compatibility = SessionCompatibility{Status: SessionCompatibilityNew}
	return p.acquireLease(ctx, store, p.engineID)
}

func (p *resolvedSessionPlan) startLeaseRenewal(ctx context.Context, store SessionStore, cancel context.CancelFunc) {
	if store == nil {
		return
	}
	interval := leaseRenewInterval()
	if interval <= 0 {
		return
	}

	renewCtx, renewCancel := context.WithCancel(ctx)
	p.renewCancel = renewCancel
	p.renewWG.Add(1)
	go func() {
		defer p.renewWG.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-renewCtx.Done():
				return
			case <-ticker.C:
				for _, lease := range p.snapshotLeases() {
					if err := store.RenewLease(renewCtx, lease, LeaseTTL()); err != nil {
						p.setRenewalError(err)
						if cancel != nil {
							cancel()
						}
						return
					}
				}
			}
		}
	}()
}

func (p *resolvedSessionPlan) snapshotLeases() []SessionLease {
	p.leaseMu.Lock()
	defer p.leaseMu.Unlock()
	return append([]SessionLease(nil), p.leases...)
}

func (p *resolvedSessionPlan) rememberLeaseLocked(lease SessionLease) {
	for index, existing := range p.leases {
		if existing.Target == lease.Target {
			p.leases[index] = lease
			return
		}
	}
	p.leases = append(p.leases, lease)
}

func (p *resolvedSessionPlan) setRenewalError(err error) {
	if err == nil {
		return
	}
	p.renewErrMu.Lock()
	defer p.renewErrMu.Unlock()
	if p.renewErr == nil {
		p.renewErr = err
	}
}

func (p *resolvedSessionPlan) renewalError() error {
	p.renewErrMu.Lock()
	defer p.renewErrMu.Unlock()
	return p.renewErr
}

func leaseRenewInterval() time.Duration {
	interval := LeaseRenewInterval()
	if interval <= 0 || interval >= LeaseTTL() {
		interval = LeaseTTL() / 2
	}
	if interval <= 0 {
		return time.Second
	}
	return interval
}

func (s *Core) prepareSession(
	ctx context.Context,
	req SessionRequest,
	identity AgentIdentity,
	driverType string,
	fingerprint string,
) (*resolvedSessionPlan, error) {
	return prepareSessionPlan(ctx, s.sessionStore, req, identity, driverType, fingerprint)
}

// prepareSessionPlan is the store-parameterized session resolution logic.
// It backs both the legacy Core.Execute path (via Core.prepareSession) and
// the v1 Thread path (via PrepareThreadSession), so session mode semantics,
// fingerprint gating, and lease acquisition stay single-sourced.
func prepareSessionPlan(
	ctx context.Context,
	store SessionStore,
	req SessionRequest,
	identity AgentIdentity,
	driverType string,
	fingerprint string,
) (*resolvedSessionPlan, error) {
	req = resolveSessionDefaults(req)
	if req.Mode == SessionStateless {
		return nil, nil
	}
	if store == nil {
		return nil, ErrSessionStoreRequired
	}

	plan := &resolvedSessionPlan{
		request: req,
		owner:   newLeaseOwner(identity, driverType, req),
		store:   store,
	}

	if req.Namespace != "" && req.Key != "" {
		plan.keyLeaseID = sessionKeyLeaseID(req.Namespace, req.Key)
		if err := plan.acquireLease(ctx, store, plan.keyLeaseID); err != nil {
			plan.release()
			return nil, &SessionBusyError{Target: req.Namespace + "/" + req.Key}
		}
	}

	var current *SessionRecord
	switch {
	case req.ID != "":
		record, err := store.Resolve(ctx, SessionQuery{
			ID:              req.ID,
			IncludeArchived: true,
		})
		if err != nil {
			plan.release()
			return nil, err
		}
		current = record
	case req.Namespace != "" && req.Key != "":
		record, err := store.Resolve(ctx, SessionQuery{
			Namespace: req.Namespace,
			Key:       req.Key,
		})
		if err != nil {
			plan.release()
			return nil, err
		}
		current = record
	}

	if current != nil && current.ID != "" {
		if err := plan.acquireLease(ctx, store, current.ID); err != nil {
			plan.release()
			return nil, &SessionBusyError{Target: current.ID}
		}
	}

	plan.record = current

	switch req.Mode {
	case SessionContinueOnly:
		if current == nil {
			plan.release()
			return nil, ErrSessionNotFound
		}
		compat := sessionCompatibility(fingerprint, current.CompatibilityFingerprint)
		plan.compatibility = compat
		if compat.Status != SessionCompatibilityCompatible {
			plan.release()
			return nil, &SessionIncompatibleError{
				Reason:              compat.Reason,
				ExpectedFingerprint: compat.ExpectedFingerprint,
				ActualFingerprint:   compat.ActualFingerprint,
			}
		}
		plan.engineID = current.ID
		plan.reused = true
	case SessionContinueOrStart:
		if current == nil {
			plan.engineID = newEngineSessionID(driverType, fingerprint)
			if err := plan.acquireLease(ctx, store, plan.engineID); err != nil {
				plan.release()
				return nil, &SessionBusyError{Target: plan.engineID}
			}
			plan.created = true
			plan.compatibility = SessionCompatibility{Status: SessionCompatibilityNew}
			return plan, nil
		}
		compat := sessionCompatibility(fingerprint, current.CompatibilityFingerprint)
		plan.compatibility = compat
		if compat.Status == SessionCompatibilityCompatible {
			plan.engineID = current.ID
			plan.reused = true
			return plan, nil
		}
		plan.previousID = current.ID
		plan.engineID = newEngineSessionID(driverType, fingerprint)
		if err := plan.acquireLease(ctx, store, plan.engineID); err != nil {
			plan.release()
			return nil, &SessionBusyError{Target: plan.engineID}
		}
		plan.created = true
	case SessionStartNew:
		if current != nil {
			plan.previousID = current.ID
		}
		plan.engineID = newEngineSessionID(driverType, fingerprint)
		if err := plan.acquireLease(ctx, store, plan.engineID); err != nil {
			plan.release()
			return nil, &SessionBusyError{Target: plan.engineID}
		}
		plan.created = true
		plan.compatibility = SessionCompatibility{Status: SessionCompatibilityNew}
	case SessionFork:
		if req.ForkFrom == "" {
			plan.release()
			return nil, fmt.Errorf("%w: fork_from missing", ErrSessionNotFound)
		}
		parent, err := store.Resolve(ctx, SessionQuery{
			ID:              req.ForkFrom,
			IncludeArchived: true,
		})
		if err != nil {
			plan.release()
			return nil, err
		}
		if parent == nil {
			plan.release()
			return nil, ErrSessionNotFound
		}
		plan.record = parent
		plan.engineID = newEngineSessionID(driverType, fingerprint)
		if err := plan.acquireLease(ctx, store, plan.engineID); err != nil {
			plan.release()
			return nil, &SessionBusyError{Target: plan.engineID}
		}
		plan.created = true
		plan.compatibility = SessionCompatibility{Status: SessionCompatibilityNew}
	default:
		plan.release()
		return nil, fmt.Errorf("%w: unsupported mode %q", ErrInvalidDriverConfig, req.Mode)
	}

	return plan, nil
}

func (s *Core) persistSession(
	ctx context.Context,
	plan *resolvedSessionPlan,
	identity AgentIdentity,
	driver DriverAdapter,
	fingerprint string,
	checkpoint *DriverCheckpoint,
) (*SessionRef, error) {
	return persistSessionPlan(ctx, s.sessionStore, plan, identity, driver, fingerprint, checkpoint)
}

// persistSessionPlan is the store-parameterized post-run persistence logic
// shared by the legacy Core path and the v1 Thread path (see
// prepareSessionPlan).
func persistSessionPlan(
	ctx context.Context,
	store SessionStore,
	plan *resolvedSessionPlan,
	identity AgentIdentity,
	driver DriverAdapter,
	fingerprint string,
	checkpoint *DriverCheckpoint,
) (*SessionRef, error) {
	if plan == nil || store == nil || plan.request.Mode == SessionStateless {
		return nil, nil
	}

	if checkpoint == nil || !checkpoint.Valid || checkpoint.State == nil {
		return nil, ErrSessionCheckpointMissing
	}
	persistedState := normalizeSessionState(driver, checkpoint.State)

	record := SessionRecord{
		ID:                       plan.engineID,
		Namespace:                plan.request.Namespace,
		Key:                      plan.request.Key,
		Status:                   SessionStatusActive,
		DriverType:               driver.Descriptor().Type,
		Agent:                    identity,
		Fingerprint:              fingerprint,
		CompatibilityFingerprint: fingerprint,
		DriverState:              persistedState,
		CreatedAt:                time.Now().UTC(),
		UpdatedAt:                time.Now().UTC(),
	}

	if plan.reused && plan.record != nil {
		record.CreatedAt = plan.record.CreatedAt
	}

	if err := store.Finalize(ctx, SessionFinalizeRequest{
		Record:       record,
		PreviousID:   plan.previousID,
		Namespace:    plan.request.Namespace,
		Key:          plan.request.Key,
		HeldLeases:   plan.snapshotLeases(),
		ArchiveOld:   plan.previousID != "",
		RebindActive: plan.request.Namespace != "" && plan.request.Key != "",
	}); err != nil {
		return nil, err
	}

	displayID := plan.engineID
	if persistedState != nil {
		if resolvedDisplayID := sessionDisplayID(driver, persistedState); resolvedDisplayID != "" {
			displayID = resolvedDisplayID
		}
	}
	if plan.compatibility.Status == "" {
		if plan.reused {
			plan.compatibility = SessionCompatibility{
				Status:              SessionCompatibilityCompatible,
				ExpectedFingerprint: fingerprint,
				ActualFingerprint:   fingerprint,
			}
		} else {
			plan.compatibility = SessionCompatibility{Status: SessionCompatibilityNew}
		}
	}

	return &SessionRef{
		ID:            plan.engineID,
		Namespace:     plan.request.Namespace,
		Key:           plan.request.Key,
		DisplayID:     displayID,
		Reused:        plan.reused,
		Created:       plan.created,
		PreviousID:    plan.previousID,
		Compatibility: plan.compatibility,
	}, nil
}
