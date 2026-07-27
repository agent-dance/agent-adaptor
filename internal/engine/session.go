package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/agent-dance/agent-adaptor/internal/keycodec"
)

// LeaseTTL and LeaseRenewInterval report the session lease timing knobs.
// The defaults are self-sufficient: no wiring is required for the session
// pipeline to work. They stay function variables (not plain durations) so
// tests — both engine-side and facade-side — can shorten the timings
// without touching the production call sites.
var (
	LeaseTTL           = func() time.Duration { return 5 * time.Minute }
	LeaseRenewInterval = func() time.Duration { return 2 * time.Minute }
)

const defaultLeaseReleaseTimeout = 5 * time.Second

type resolvedSessionPlan struct {
	request          SessionRequest
	engineID         string
	record           *SessionRecord
	keyLeaseID       string
	reused           bool
	created          bool
	previousID       string
	compatibility    SessionCompatibility
	requireKeyAbsent bool
	owner            string
	store            SessionStore
	leaseMu          sync.Mutex
	leases           []SessionLease
	renewMu          sync.Mutex
	renewCancel      context.CancelFunc
	renewWG          sync.WaitGroup
	renewErrMu       sync.Mutex
	renewErr         error
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

func validateSessionRequest(req SessionRequest) error {
	hasTuple := req.Namespace != "" && req.Key != ""
	hasID := req.ID != ""
	if (req.Namespace == "") != (req.Key == "") {
		return fmt.Errorf("%w: namespace and key must be supplied together", ErrInvalidSessionRequest)
	}
	hasForkID := req.ForkFrom != ""
	hasForkKey := req.ForkFromKey != ""
	switch req.Mode {
	case SessionStateless:
		if hasID || hasTuple || hasForkID || hasForkKey || req.SessionCodec != "" {
			return fmt.Errorf("%w: stateless mode cannot include session selectors", ErrInvalidSessionRequest)
		}
	case SessionContinueOnly:
		if hasID == hasTuple || hasForkID || hasForkKey {
			return fmt.Errorf("%w: continue-only requires exactly one ID or key selector", ErrInvalidSessionRequest)
		}
	case SessionContinueOrStart, SessionStartNew:
		if !hasTuple || hasID || hasForkID || hasForkKey {
			return fmt.Errorf("%w: %s requires exactly one key selector", ErrInvalidSessionRequest, req.Mode)
		}
	case SessionFork:
		if !hasTuple || hasID || hasForkID == hasForkKey {
			return fmt.Errorf("%w: fork requires one target key and exactly one parent selector", ErrInvalidSessionRequest)
		}
		if hasForkKey && req.SessionCodec == "" {
			return fmt.Errorf("%w: structured fork requires session codec", ErrInvalidSessionRequest)
		}
	default:
		return fmt.Errorf("%w: unsupported mode %q", ErrInvalidSessionRequest, req.Mode)
	}
	return nil
}

func sessionKeyLeaseID(namespace, key string) string {
	return keycodec.Encode("session-key", namespace, key)
}

func sessionRecordLeaseID(id string) string {
	return keycodec.Encode("session-record", id)
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
	ctx, cancel := context.WithTimeout(context.Background(), defaultLeaseReleaseTimeout)
	defer cancel()
	_ = p.releaseContext(ctx)
}

func (p *resolvedSessionPlan) releaseContext(ctx context.Context) error {
	if p == nil {
		return nil
	}
	p.stopLeaseRenewal()

	p.leaseMu.Lock()
	leases := append([]SessionLease(nil), p.leases...)
	p.leases = nil
	p.leaseMu.Unlock()

	if len(leases) == 0 || p.store == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	done := make(chan error, 1)
	go func() {
		var releaseErrors []error
		for index := len(leases) - 1; index >= 0; index-- {
			if err := p.store.ReleaseLease(ctx, leases[index]); err != nil {
				releaseErrors = append(releaseErrors, err)
			}
		}
		done <- errors.Join(releaseErrors...)
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return fmt.Errorf("release session leases: %w", ctx.Err())
	}
}

func (p *resolvedSessionPlan) stopLeaseRenewal() {
	if p == nil {
		return
	}
	p.renewMu.Lock()
	defer p.renewMu.Unlock()
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
	return p.acquireLease(ctx, store, sessionRecordLeaseID(p.engineID))
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
	p.renewMu.Lock()
	defer p.renewMu.Unlock()
	if p.renewCancel != nil {
		// A plan owns one renewal loop. Treat repeated starts as an idempotent
		// request instead of racing two tickers over the same lease set.
		renewCancel()
		return
	}
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
	if err := validateSessionRequest(req); err != nil {
		return nil, err
	}
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
	case req.Mode == SessionFork:
		record, err := store.Resolve(ctx, SessionQuery{
			Namespace: req.Namespace,
			Key:       req.Key,
		})
		if err != nil {
			plan.release()
			return nil, err
		}
		current = record
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

	plan.record = current
	acquireCurrent := func() error {
		if current == nil || current.ID == "" {
			return nil
		}
		if err := plan.acquireLease(ctx, store, sessionRecordLeaseID(current.ID)); err != nil {
			return &SessionBusyError{Target: current.ID}
		}
		return nil
	}

	switch req.Mode {
	case SessionContinueOnly:
		if current == nil {
			plan.release()
			return nil, ErrSessionNotFound
		}
		if err := acquireCurrent(); err != nil {
			plan.release()
			return nil, err
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
			if err := plan.acquireLease(ctx, store, sessionRecordLeaseID(plan.engineID)); err != nil {
				plan.release()
				return nil, &SessionBusyError{Target: plan.engineID}
			}
			plan.created = true
			plan.compatibility = SessionCompatibility{Status: SessionCompatibilityNew}
			return plan, nil
		}
		if err := acquireCurrent(); err != nil {
			plan.release()
			return nil, err
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
		if err := plan.acquireLease(ctx, store, sessionRecordLeaseID(plan.engineID)); err != nil {
			plan.release()
			return nil, &SessionBusyError{Target: plan.engineID}
		}
		plan.created = true
	case SessionStartNew:
		if current != nil {
			if err := acquireCurrent(); err != nil {
				plan.release()
				return nil, err
			}
			plan.previousID = current.ID
		}
		plan.engineID = newEngineSessionID(driverType, fingerprint)
		if err := plan.acquireLease(ctx, store, sessionRecordLeaseID(plan.engineID)); err != nil {
			plan.release()
			return nil, &SessionBusyError{Target: plan.engineID}
		}
		plan.created = true
		plan.compatibility = SessionCompatibility{Status: SessionCompatibilityNew}
	case SessionFork:
		if current != nil {
			plan.release()
			return nil, fmt.Errorf("%w: %s", ErrThreadAlreadyExists, req.Key)
		}
		parent, err := prepareForkParent(ctx, store, plan, req)
		if err != nil {
			plan.release()
			return nil, err
		}
		if parent == nil {
			plan.release()
			return nil, ErrSessionNotFound
		}
		if err := validateForkParent(parent, identity, driverType, fingerprint, req.SessionCodec); err != nil {
			plan.release()
			return nil, err
		}
		plan.record = parent
		plan.engineID = newEngineSessionID(driverType, fingerprint)
		if err := plan.acquireLease(ctx, store, sessionRecordLeaseID(plan.engineID)); err != nil {
			plan.release()
			return nil, &SessionBusyError{Target: plan.engineID}
		}
		plan.created = true
		plan.requireKeyAbsent = true
		plan.compatibility = SessionCompatibility{Status: SessionCompatibilityNew}
	default:
		plan.release()
		return nil, fmt.Errorf("%w: unsupported mode %q", ErrInvalidSessionRequest, req.Mode)
	}

	return plan, nil
}

// prepareForkParent acquires the parent key lease before its record lease and
// resolves the record again while both are held. That order makes a fork's
// parent snapshot stable without ever pre-resolving it in the public Thread
// layer. The target key lease has already been acquired by the caller.
func prepareForkParent(ctx context.Context, store SessionStore, plan *resolvedSessionPlan, req SessionRequest) (*SessionRecord, error) {
	var parent *SessionRecord
	var err error
	if req.ForkFromKey != "" {
		parentKeyLease := sessionKeyLeaseID(req.Namespace, req.ForkFromKey)
		if err := plan.acquireLease(ctx, store, parentKeyLease); err != nil {
			return nil, &SessionBusyError{Target: req.ForkFromKey}
		}
		parent, err = store.Resolve(ctx, SessionQuery{Namespace: req.Namespace, Key: req.ForkFromKey})
	} else {
		parent, err = store.Resolve(ctx, SessionQuery{ID: req.ForkFrom, IncludeArchived: true})
		if err == nil && parent != nil && parent.Namespace != "" && parent.Key != "" {
			if leaseErr := plan.acquireLease(ctx, store, sessionKeyLeaseID(parent.Namespace, parent.Key)); leaseErr != nil {
				return nil, &SessionBusyError{Target: parent.Key}
			}
		}
	}
	if err != nil || parent == nil {
		return parent, err
	}
	if err := plan.acquireLease(ctx, store, sessionRecordLeaseID(parent.ID)); err != nil {
		return nil, &SessionBusyError{Target: parent.ID}
	}
	// Resolve by immutable internal ID after all coordinating leases are held,
	// so codec/checkpoint/fingerprint validation observes one stable snapshot.
	return store.Resolve(ctx, SessionQuery{ID: parent.ID, IncludeArchived: true})
}

func validateForkParent(parent *SessionRecord, identity AgentIdentity, driverType, fingerprint, codec string) error {
	if parent == nil {
		return ErrSessionNotFound
	}
	if parent.DriverState == nil {
		return ErrSessionCheckpointMissing
	}
	if parent.DriverType == "" || parent.DriverType != driverType {
		return &SessionIncompatibleError{Reason: "fork parent driver changed"}
	}
	if parent.Agent != identity {
		return &SessionIncompatibleError{Reason: "fork parent identity changed"}
	}
	compat := sessionCompatibility(fingerprint, parent.CompatibilityFingerprint)
	if compat.Status != SessionCompatibilityCompatible {
		return &SessionIncompatibleError{
			Reason:              compat.Reason,
			ExpectedFingerprint: compat.ExpectedFingerprint,
			ActualFingerprint:   compat.ActualFingerprint,
		}
	}
	if codec != "" && (parent.SessionCodec == "" || parent.SessionCodec != codec) {
		return &SessionIncompatibleError{Reason: "fork parent session codec changed"}
	}
	return nil
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
		SessionCodec:             SessionCodecFor(driver).Name(),
		DriverState:              persistedState,
		CreatedAt:                time.Now().UTC(),
		UpdatedAt:                time.Now().UTC(),
	}

	if plan.reused && plan.record != nil {
		record.CreatedAt = plan.record.CreatedAt
	}

	if err := store.Finalize(ctx, SessionFinalizeRequest{
		Record:           record,
		PreviousID:       plan.previousID,
		Namespace:        plan.request.Namespace,
		Key:              plan.request.Key,
		HeldLeases:       plan.snapshotLeases(),
		ArchiveOld:       plan.previousID != "",
		RebindActive:     plan.request.Namespace != "" && plan.request.Key != "",
		RequireKeyAbsent: plan.requireKeyAbsent,
	}); err != nil {
		var conflict interface{ ThreadAlreadyExists() bool }
		if errors.As(err, &conflict) && conflict.ThreadAlreadyExists() {
			return nil, fmt.Errorf("%w: %w", ErrThreadAlreadyExists, err)
		}
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
