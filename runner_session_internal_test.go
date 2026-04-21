package agentadaptor

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

type blockingTestConfig struct {
	Label string
}

type blockingTestAdapter struct {
	mu            sync.Mutex
	counter       int
	blockOn       string
	blockCh       chan struct{}
	startedCh     chan struct{}
	ignoreContext bool
}

type testLeaseRecord struct {
	Owner string
	Until time.Time
	Token string
}

type failingRenewSessionStore struct {
	*testSessionStore
	mu        sync.Mutex
	renewals  int
	failAfter int
}

type testSessionStore struct {
	mu       sync.Mutex
	records  map[string]SessionRecord
	keyIndex map[string]string
	leases   map[string]testLeaseRecord
}

func newTestSessionStore() *testSessionStore {
	return &testSessionStore{
		records:  map[string]SessionRecord{},
		keyIndex: map[string]string{},
		leases:   map[string]testLeaseRecord{},
	}
}

func newFailingRenewSessionStore(failAfter int) *failingRenewSessionStore {
	return &failingRenewSessionStore{
		testSessionStore: newTestSessionStore(),
		failAfter:        failAfter,
	}
}

func (s *testSessionStore) Resolve(_ context.Context, q SessionQuery) (*SessionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if q.ID != "" {
		record, ok := s.records[q.ID]
		if !ok {
			return nil, nil
		}
		if record.Status == SessionStatusArchived && !q.IncludeArchived {
			return nil, nil
		}
		copyRecord := record
		return &copyRecord, nil
	}
	if q.Namespace == "" || q.Key == "" {
		return nil, nil
	}
	id := s.keyIndex[q.Namespace+":"+q.Key]
	if id == "" {
		return nil, nil
	}
	record, ok := s.records[id]
	if !ok {
		return nil, nil
	}
	if record.Status == SessionStatusArchived && !q.IncludeArchived {
		return nil, nil
	}
	copyRecord := record
	return &copyRecord, nil
}

func (s *testSessionStore) Finalize(_ context.Context, req SessionFinalizeRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	for _, lease := range req.HeldLeases {
		current, ok := s.leases[lease.Target]
		if !ok || current.Owner != lease.Owner || current.Token != lease.Token || !current.Until.After(now) {
			return &SessionLeaseLostError{Target: lease.Target}
		}
	}

	copyRecord := req.Record
	s.records[copyRecord.ID] = copyRecord

	if req.ArchiveOld && req.PreviousID != "" {
		record, ok := s.records[req.PreviousID]
		if ok {
			record.Status = SessionStatusArchived
			record.UpdatedAt = now
			s.records[req.PreviousID] = record
		}
	}

	if req.RebindActive && req.Namespace != "" && req.Key != "" {
		s.keyIndex[req.Namespace+":"+req.Key] = req.Record.ID
	}
	return nil
}

func (s *testSessionStore) AcquireLease(_ context.Context, sessionID, owner string, ttl time.Duration) (SessionLease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	current, ok := s.leases[sessionID]
	if ok && current.Until.After(now) && current.Owner != owner {
		return SessionLease{}, &SessionBusyError{Target: sessionID}
	}
	token := current.Token
	if token == "" || !ok || !current.Until.After(now) {
		token = fmt.Sprintf("token-%s-%d", sessionID, now.UnixNano())
	}
	s.leases[sessionID] = testLeaseRecord{
		Owner: owner,
		Until: now.Add(ttl),
		Token: token,
	}
	return SessionLease{Target: sessionID, Owner: owner, Token: token}, nil
}

func (s *testSessionStore) RenewLease(_ context.Context, lease SessionLease, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.leases[lease.Target]
	if !ok || current.Owner != lease.Owner || current.Token != lease.Token || !current.Until.After(time.Now().UTC()) {
		return &SessionLeaseLostError{Target: lease.Target}
	}
	current.Until = time.Now().UTC().Add(ttl)
	s.leases[lease.Target] = current
	return nil
}

func (s *testSessionStore) ReleaseLease(_ context.Context, lease SessionLease) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.leases[lease.Target]
	if !ok {
		return nil
	}
	if current.Owner != lease.Owner || current.Token != lease.Token {
		return nil
	}
	delete(s.leases, lease.Target)
	return nil
}

func (s *failingRenewSessionStore) RenewLease(ctx context.Context, lease SessionLease, ttl time.Duration) error {
	s.mu.Lock()
	s.renewals++
	renewNo := s.renewals
	s.mu.Unlock()

	if renewNo > s.failAfter {
		return &SessionLeaseLostError{Target: lease.Target}
	}
	return s.testSessionStore.RenewLease(ctx, lease, ttl)
}

func (a *blockingTestAdapter) Descriptor() DriverDescriptor {
	return DriverDescriptor{
		Type:        "blocking-test",
		DisplayName: "Blocking Test",
		Sessions:    SessionCapability{SupportsResume: true},
	}
}

func (a *blockingTestAdapter) ValidateConfig(cfg any) error {
	switch cfg.(type) {
	case blockingTestConfig, *blockingTestConfig:
		return nil
	default:
		return fmt.Errorf("unexpected config type %T", cfg)
	}
}

func (a *blockingTestAdapter) Run(ctx context.Context, req DriverRunRequest, _ EventSink) (DriverRunResult, error) {
	if a.startedCh != nil {
		select {
		case a.startedCh <- struct{}{}:
		default:
		}
	}
	if a.blockCh != nil && req.Prompt == a.blockOn {
		if a.ignoreContext {
			<-a.blockCh
		} else {
			select {
			case <-ctx.Done():
				return DriverRunResult{}, ctx.Err()
			case <-a.blockCh:
			}
		}
	}

	a.mu.Lock()
	a.counter++
	next := a.counter
	a.mu.Unlock()

	state := &DriverSessionState{
		ResumeID:  fmt.Sprintf("blocking-session-%d", next),
		DisplayID: fmt.Sprintf("blocking-display-%d", next),
	}
	if req.Session != nil && req.Session.State != nil && req.Session.State.ResumeID != "" {
		state = req.Session.State
	}

	return DriverRunResult{
		Output:   "ok",
		ExitCode: 0,
		Checkpoint: &DriverCheckpoint{
			State: state,
			Valid: true,
		},
	}, nil
}

func TestChannelEventSinkEmitDoesNotBlockWhenBufferFull(t *testing.T) {
	sink := newChannelEventSink()
	defer sink.close()

	for i := 0; i < cap(sink.events); i++ {
		if err := sink.Emit(newEvent(RunEventLifecycle, fmt.Sprintf("event-%d", i))); err != nil {
			t.Fatalf("fill buffer: %v", err)
		}
	}

	done := make(chan struct{})
	go func() {
		_ = sink.Emit(newEvent(RunEventLifecycle, "overflow"))
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		<-sink.events
		<-done
		t.Fatal("Emit blocked on a full events channel")
	}

	<-sink.events
	if err := sink.Emit(newEvent(RunEventLifecycle, "after-overflow")); err != nil {
		t.Fatalf("emit after drain: %v", err)
	}

	summaryFound := false
	timeout := time.After(500 * time.Millisecond)
	for !summaryFound {
		select {
		case event := <-sink.events:
			if event.Type == RunEventLifecycle && event.Metadata["reason"] == "overflow" {
				summaryFound = true
				if event.Metadata["dropped_count"] != "1" {
					t.Fatalf("expected dropped_count=1, got %#v", event.Metadata)
				}
			}
		case <-timeout:
			t.Fatal("expected overflow summary event after buffer had space again")
		}
	}
}

func TestChannelEventSinkEmitAfterCloseDoesNotPanic(t *testing.T) {
	sink := newChannelEventSink()
	sink.close()

	panicCh := make(chan any, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() {
			if recovered := recover(); recovered != nil {
				panicCh <- recovered
			}
		}()
		_ = sink.Emit(newEvent(RunEventLifecycle, "late-event"))
	}()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Emit after close did not return")
	}

	select {
	case recovered := <-panicCh:
		t.Fatalf("Emit panicked after close: %v", recovered)
	default:
	}
}

func TestLeaseRenewalKeepsLongRunningRunExclusive(t *testing.T) {
	originalTTL := defaultLeaseTTL
	originalRenewInterval := defaultLeaseRenewInterval
	defaultLeaseTTL = 80 * time.Millisecond
	defaultLeaseRenewInterval = 20 * time.Millisecond
	defer func() {
		defaultLeaseTTL = originalTTL
		defaultLeaseRenewInterval = originalRenewInterval
	}()

	store := newTestSessionStore()
	adapter := &blockingTestAdapter{
		blockOn:   "block",
		blockCh:   make(chan struct{}),
		startedCh: make(chan struct{}, 1),
	}

	sdk := New(
		WithSessionStore(store),
		WithDefaultAgent(Bind(adapter, blockingTestConfig{Label: "default"})),
	)

	handle, err := sdk.Start(context.Background(), "block", WithSessionKey("company", "issue-1"))
	if err != nil {
		t.Fatalf("start long run: %v", err)
	}

	select {
	case <-adapter.startedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for long run to start")
	}

	time.Sleep(2 * defaultLeaseTTL)

	_, err = sdk.Run(context.Background(), "again", WithSessionKey("company", "issue-1"))
	if !errors.Is(err, ErrSessionBusy) {
		close(adapter.blockCh)
		_, _ = handle.Wait(context.Background())
		t.Fatalf("expected ErrSessionBusy after lease renewal window, got %v", err)
	}

	close(adapter.blockCh)
	if _, err := handle.Wait(context.Background()); err != nil {
		t.Fatalf("wait long run: %v", err)
	}
}

func TestLeaseRenewalFailureDoesNotPersistSessionState(t *testing.T) {
	originalTTL := defaultLeaseTTL
	originalRenewInterval := defaultLeaseRenewInterval
	defaultLeaseTTL = 80 * time.Millisecond
	defaultLeaseRenewInterval = 20 * time.Millisecond
	defer func() {
		defaultLeaseTTL = originalTTL
		defaultLeaseRenewInterval = originalRenewInterval
	}()

	store := newFailingRenewSessionStore(2)
	adapter := &blockingTestAdapter{
		blockOn:       "long-run",
		blockCh:       make(chan struct{}),
		ignoreContext: true,
	}

	sdk := New(
		WithSessionStore(store),
		WithDefaultAgent(Bind(adapter, blockingTestConfig{Label: "default"})),
	)

	done := make(chan error, 1)
	go func() {
		_, err := sdk.Run(context.Background(), "long-run", WithSessionKey("company", "issue-1"))
		done <- err
	}()

	time.Sleep(2 * defaultLeaseTTL)
	close(adapter.blockCh)

	select {
	case err := <-done:
		if !errors.Is(err, ErrSessionLeaseLost) {
			t.Fatalf("expected renewal failure to surface as ErrSessionLeaseLost, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for long run to finish")
	}

	record, err := store.Resolve(context.Background(), SessionQuery{
		Namespace: "company",
		Key:       "issue-1",
	})
	if err != nil {
		t.Fatalf("resolve session after failed renewal: %v", err)
	}
	if record != nil {
		t.Fatalf("expected no persisted session after renewal failure, got %#v", record)
	}
}
