package adaptor_test

// P2 contract migration, codec leg: the semantic assertions of the legacy
// session_codec_internal_test.go (passthrough round-trip, DisplayID
// defaulting, driver-owned codec normalization), replayed through the two
// v1 touch points that consume the codec: the persist pipeline and
// Thread.Checkpoint (the audit window onto the driver resume handle).

import (
	"context"
	"errors"
	"maps"
	"testing"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/memory"
	adaptor "github.com/agent-dance/agent-adaptor/next"
)

// codecFake is a fakeDriver that additionally implements
// driver.SessionCodecProvider.
type codecFake struct {
	*fakeDriver
	codec driver.SessionCodec
}

var (
	_ driver.Driver               = (*codecFake)(nil)
	_ driver.SessionCodecProvider = (*codecFake)(nil)
)

func (d *codecFake) SessionCodec() driver.SessionCodec { return d.codec }

// allowlistCodec keeps only the "cwd" session parameter — everything else
// in State.Data is transient and must not survive normalization.
type allowlistCodec struct{}

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

// TestThreadCheckpointPassthroughRoundTrip: without a driver codec the
// passthrough codec round-trips the state — Data preserved, DisplayID
// defaulting to ResumeID — and the returned snapshot is isolated from the
// store.
func TestThreadCheckpointPassthroughRoundTrip(t *testing.T) {
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
