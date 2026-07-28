package adaptor_test

// Thread codec assertions replayed through the two v1 touch points that
// consume the codec: the persist pipeline and
// Thread.Checkpoint (the audit window onto the driver resume handle).

import (
	"context"
	"errors"
	"maps"
	"sync"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/memory"
	"github.com/agent-dance/agent-adaptor/threadstore"
)

// codecFake is a fakeDriver that additionally implements
// driver.SessionCodecProvider.
type codecFake struct {
	*fakeDriver
	codec driver.SessionCodec
}

type typedNilCodecFake struct{ *fakeDriver }

type noCodecFake struct {
	inner    *fakeDriver
	supports bool
}

func (d *noCodecFake) Descriptor() driver.Descriptor {
	desc := d.inner.Descriptor()
	desc.Sessions.SupportsResume = d.supports
	return desc
}
func (d *noCodecFake) ValidateConfig(cfg any) error { return d.inner.ValidateConfig(cfg) }
func (d *noCodecFake) SessionConfigFingerprint() (string, error) {
	return d.inner.SessionConfigFingerprint()
}
func (d *noCodecFake) Run(ctx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
	return d.inner.Run(ctx, req, sink)
}

type observingStore struct {
	inner *memory.Store
	mu    sync.Mutex
	calls int
}

func newObservingStore() *observingStore { return &observingStore{inner: memory.NewStore()} }
func (s *observingStore) called() {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
}
func (s *observingStore) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}
func (s *observingStore) Resolve(ctx context.Context, q threadstore.Query) (*threadstore.Record, error) {
	s.called()
	return s.inner.Resolve(ctx, q)
}
func (s *observingStore) Finalize(ctx context.Context, req threadstore.FinalizeRequest) error {
	s.called()
	return s.inner.Finalize(ctx, req)
}
func (s *observingStore) AcquireLease(ctx context.Context, target, owner string, ttl time.Duration) (threadstore.Lease, error) {
	s.called()
	return s.inner.AcquireLease(ctx, target, owner, ttl)
}
func (s *observingStore) RenewLease(ctx context.Context, lease threadstore.Lease, ttl time.Duration) error {
	s.called()
	return s.inner.RenewLease(ctx, lease, ttl)
}
func (s *observingStore) ReleaseLease(ctx context.Context, lease threadstore.Lease) error {
	s.called()
	return s.inner.ReleaseLease(ctx, lease)
}

func (*typedNilCodecFake) SessionCodec() driver.SessionCodec {
	var codec *allowlistCodec
	return codec
}

var (
	_ driver.Driver               = (*codecFake)(nil)
	_ driver.SessionCodecProvider = (*codecFake)(nil)
)

func (d *codecFake) SessionCodec() driver.SessionCodec { return d.codec }

// allowlistCodec keeps only the "cwd" session parameter — everything else
// in State.Data is transient and must not survive normalization.
type allowlistCodec struct{}

type rejectingAllowlistCodec struct{ allowlistCodec }

func (rejectingAllowlistCodec) FromParams(driver.SessionParams) *driver.SessionState { return nil }

func (allowlistCodec) Name() string { return "allowlist" }

func (allowlistCodec) ToParams(state *driver.SessionState) driver.SessionParams {
	if state == nil {
		return driver.SessionParams{}
	}
	values := map[string]string{}
	if cwd, ok := state.Data["cwd"]; ok {
		values["cwd"] = cwd
	}
	return driver.SessionParams{ResumeID: state.ResumeID, DisplayID: state.DisplayID, Values: values}
}

func (allowlistCodec) FromParams(params driver.SessionParams) *driver.SessionState {
	if params.ResumeID == "" && params.DisplayID == "" && len(params.Values) == 0 {
		return nil
	}
	displayID := params.DisplayID
	if displayID == "" {
		displayID = params.ResumeID
	}
	return &driver.SessionState{ResumeID: params.ResumeID, DisplayID: displayID, Data: maps.Clone(params.Values)}
}

func (allowlistCodec) GuardFingerprint(params driver.SessionParams) string {
	return "allowlist:" + params.ResumeID
}

// checkpointing returns a runFunc producing the given checkpoint state.
func checkpointing(state driver.SessionState) func(context.Context, driver.Request, driver.EventSink) (driver.Response, error) {
	return func(_ context.Context, _ driver.Request, _ driver.EventSink) (driver.Response, error) {
		copyState := state
		copyState.Data = maps.Clone(state.Data)
		return driver.Response{Output: "ok", Checkpoint: &driver.Checkpoint{State: &copyState, Valid: true}}, nil
	}
}

func TestThreadCheckpointNotFound(t *testing.T) {
	agent := adaptor.New(newFakeDriver(), adaptor.WithThreadStore(memory.NewStore()))
	if _, err := agent.Thread("never-ran").Checkpoint(context.Background()); !errors.Is(err, adaptor.ErrThreadNotFound) {
		t.Fatalf("Checkpoint on missing thread: err = %v, want ErrThreadNotFound", err)
	}
}

func TestThreadCheckpointRejectsInvalidDurableState(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*threadstore.Record)
		want   error
	}{
		{name: "nil_state", mutate: func(record *threadstore.Record) { record.State = nil }, want: adaptor.ErrThreadCheckpointMissing},
		{name: "empty_resume_id", mutate: func(record *threadstore.Record) {
			record.State = &driver.SessionState{DisplayID: "display-only"}
		}, want: adaptor.ErrThreadIncompatible},
		{name: "codec_mismatch", mutate: func(record *threadstore.Record) {
			record.SessionCodec = "other/codec"
		}, want: adaptor.ErrThreadIncompatible},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			fake := newFakeDriver()
			fake.runFunc = checkpointing(driver.SessionState{ResumeID: "resume-1"})
			store := memory.NewStore()
			agent := adaptor.New(fake, adaptor.WithThreadStore(store))
			thread := agent.Thread("invalid-checkpoint")
			if _, err := thread.Run(ctx, "seed"); err != nil {
				t.Fatalf("seed: %v", err)
			}
			record := *activeRecord(t, store, thread.Key())
			tc.mutate(&record)
			overwriteActiveRecord(t, store, record)

			checkpoint, err := thread.Checkpoint(ctx)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Checkpoint error = %v, want %v", err, tc.want)
			}
			if checkpoint != nil {
				t.Fatalf("Checkpoint = %+v, want nil on invalid durable state", checkpoint)
			}
		})
	}
}

func TestThreadCheckpointRejectsStateCodecCannotNormalize(t *testing.T) {
	ctx := context.Background()
	store := memory.NewStore()
	seedFake := newFakeDriver()
	seedFake.runFunc = checkpointing(driver.SessionState{ResumeID: "resume-1"})
	seedDriver := &codecFake{fakeDriver: seedFake, codec: allowlistCodec{}}
	if _, err := adaptor.New(seedDriver, adaptor.WithThreadStore(store)).Thread("codec-reject").Run(ctx, "seed"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	readDriver := &codecFake{fakeDriver: newFakeDriver(), codec: rejectingAllowlistCodec{}}
	checkpoint, err := adaptor.New(readDriver, adaptor.WithThreadStore(store)).Thread("codec-reject").Checkpoint(ctx)
	if !errors.Is(err, adaptor.ErrThreadIncompatible) {
		t.Fatalf("Checkpoint error = %v, want ErrThreadIncompatible", err)
	}
	if checkpoint != nil {
		t.Fatalf("Checkpoint = %+v, want nil when codec rejects state", checkpoint)
	}
}

func TestThreadSessionCapabilityPrelaunchMatrix(t *testing.T) {
	tests := []struct {
		name    string
		driver  func(*fakeDriver) driver.Driver
		wantErr bool
	}{
		{name: "unsupported_without_provider", driver: func(f *fakeDriver) driver.Driver {
			return &noCodecFake{inner: f, supports: false}
		}, wantErr: true},
		{name: "declared_without_provider", driver: func(f *fakeDriver) driver.Driver {
			return &noCodecFake{inner: f, supports: true}
		}, wantErr: true},
		{name: "declared_with_typed_nil_codec", driver: func(f *fakeDriver) driver.Driver {
			return &typedNilCodecFake{fakeDriver: f}
		}, wantErr: true},
		{name: "declared_with_valid_codec", driver: func(f *fakeDriver) driver.Driver {
			return &codecFake{fakeDriver: f, codec: allowlistCodec{}}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeDriver()
			fake.runFunc = checkpointing(driver.SessionState{ResumeID: "resume-1", Data: map[string]string{"cwd": "/repo"}})
			store := newObservingStore()
			agent := adaptor.New(tc.driver(fake), adaptor.WithThreadStore(store))
			_, err := agent.Thread("capability-matrix").Run(context.Background(), "go")
			if tc.wantErr {
				if !errors.Is(err, adaptor.ErrThreadIncompatible) {
					t.Fatalf("error = %v, want ErrThreadIncompatible", err)
				}
				if fake.runCount() != 0 {
					t.Fatalf("Driver.Run called %d times after prelaunch rejection", fake.runCount())
				}
				if calls := store.callCount(); calls != 0 {
					t.Fatalf("thread store observed %d calls after prelaunch rejection, want 0", calls)
				}
				return
			}
			if err != nil {
				t.Fatalf("valid resume capability rejected: %v", err)
			}
			if fake.runCount() != 1 || store.callCount() == 0 {
				t.Fatalf("valid path: runs=%d store calls=%d", fake.runCount(), store.callCount())
			}
		})
	}
}

// TestThreadCheckpointExplicitCodecRoundTrip proves an explicitly declared
// codec preserves Data, defaults DisplayID to ResumeID, and returns snapshots
// isolated from the store.
func TestThreadCheckpointExplicitCodecRoundTrip(t *testing.T) {
	ctx := context.Background()
	fake := newFakeDriver()
	fake.runFunc = checkpointing(driver.SessionState{
		ResumeID: "pt-resume-1",
		// DisplayID intentionally empty: passthrough defaults it.
		Data: map[string]string{"cwd": "/repo"},
	})
	agent := adaptor.New(fake, adaptor.WithThreadStore(memory.NewStore()))
	th := agent.Thread("k")
	if _, err := th.Run(ctx, "seed"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	cp, err := th.Checkpoint(ctx)
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	if !cp.Valid || cp.State == nil {
		t.Fatalf("checkpoint = %+v, want a valid state", cp)
	}
	if cp.State.ResumeID != "pt-resume-1" {
		t.Fatalf("ResumeID = %q, want pt-resume-1", cp.State.ResumeID)
	}
	if cp.State.DisplayID != "pt-resume-1" {
		t.Fatalf("DisplayID = %q, want the ResumeID default", cp.State.DisplayID)
	}
	if cp.State.Data["cwd"] != "/repo" {
		t.Fatalf("Data = %+v, want cwd preserved", cp.State.Data)
	}

	// Audit snapshots are copies: mutating one must not corrupt the store.
	cp.State.Data["cwd"] = "/mutated"
	again, err := th.Checkpoint(ctx)
	if err != nil {
		t.Fatalf("second Checkpoint: %v", err)
	}
	if again.State.Data["cwd"] != "/repo" {
		t.Fatalf("stored state mutated through the snapshot: %+v", again.State.Data)
	}
}

// TestThreadCodecNormalizesPersistedAndExposedState: a driver-owned codec
// participates at both consumption points — the persisted record holds the
// codec-normalized snapshot (transient keys dropped at Finalize time) and
// Thread.Checkpoint reports the same normalized view.
func TestThreadCodecNormalizesPersistedAndExposedState(t *testing.T) {
	ctx := context.Background()
	fake := newFakeDriver()
	fake.runFunc = checkpointing(driver.SessionState{
		ResumeID:  "codec-resume-1",
		DisplayID: "session one",
		Data: map[string]string{
			"cwd":       "/repo",
			"transient": "scratch-buffer",
		},
	})
	cd := &codecFake{fakeDriver: fake, codec: allowlistCodec{}}
	store := memory.NewStore()
	agent := adaptor.New(cd, adaptor.WithThreadStore(store))
	th := agent.Thread("k")
	if _, err := th.Run(ctx, "seed"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// The persisted record is the codec snapshot, not the raw driver state.
	rec := activeRecord(t, store, "k")
	if rec.State == nil || rec.State.ResumeID != "codec-resume-1" {
		t.Fatalf("stored state = %+v, want codec-resume-1", rec.State)
	}
	if _, leaked := rec.State.Data["transient"]; leaked {
		t.Fatalf("transient key persisted past the codec: %+v", rec.State.Data)
	}
	if rec.State.Data["cwd"] != "/repo" {
		t.Fatalf("allowlisted key lost: %+v", rec.State.Data)
	}

	cp, err := th.Checkpoint(ctx)
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	if !cp.Valid || cp.State == nil || cp.State.ResumeID != "codec-resume-1" {
		t.Fatalf("checkpoint = %+v, want the codec-normalized resume handle", cp)
	}
	if cp.State.DisplayID != "session one" {
		t.Fatalf("DisplayID = %q, want the driver-provided label", cp.State.DisplayID)
	}
	if _, leaked := cp.State.Data["transient"]; leaked {
		t.Fatalf("transient key exposed through Checkpoint: %+v", cp.State.Data)
	}
}
