package adaptor_test

// The programmable fake Driver backing the S1/S2/S4 scenario tests and the
// RunError path tests.

import (
	"context"
	"maps"
	"sync"
	"testing"

	"github.com/agent-dance/agent-adaptor/driver"
)

// fakeDriver is a programmable driver.Driver. Configure either a canned
// response/error or a runFunc; every received Request is recorded so tests
// can assert the option-merge outcome that reached the SPI boundary.
type fakeDriver struct {
	mu       sync.Mutex
	requests []driver.Request

	// response/err are returned when runFunc is nil.
	response driver.Response
	err      error

	// runFunc, when set, fully controls the run (blocking, ctx
	// inspection, per-call responses, ...).
	runFunc func(ctx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error)

	// caps is advertised through Descriptor().RunPolicyCaps (approval
	// retry support gating).
	caps driver.RunPolicyCapabilities

	// descriptor, when non-nil, replaces the default descriptor wholesale.
	// Capability-matrix tests use it to advertise Models, MCP transport
	// support, and StructuredOutput support.
	descriptor *driver.Descriptor

	// streamCaps selects the provider-native transport independently of the
	// consumer-facing Run/Stream method.
	streamCaps driver.StreamCapability
}

var _ driver.Driver = (*fakeDriver)(nil)
var _ driver.SessionConfigFingerprinter = (*fakeDriver)(nil)
var _ driver.SessionCodecProvider = (*fakeDriver)(nil)
var _ driver.StreamSupport = (*fakeDriver)(nil)

type fakeSessionCodec struct{}

func (fakeSessionCodec) Name() string { return "fake/session/v1" }
func (fakeSessionCodec) ToParams(state *driver.SessionState) driver.SessionParams {
	if state == nil {
		return driver.SessionParams{}
	}
	return driver.SessionParams{ResumeID: state.ResumeID, DisplayID: state.DisplayID, Values: maps.Clone(state.Data)}
}
func (fakeSessionCodec) FromParams(params driver.SessionParams) *driver.SessionState {
	if params.ResumeID == "" && params.DisplayID == "" && len(params.Values) == 0 {
		return nil
	}
	displayID := params.DisplayID
	if displayID == "" {
		displayID = params.ResumeID
	}
	return &driver.SessionState{ResumeID: params.ResumeID, DisplayID: displayID, Data: maps.Clone(params.Values)}
}
func (fakeSessionCodec) GuardFingerprint(params driver.SessionParams) string {
	return params.ResumeID
}

func newFakeDriver() *fakeDriver {
	return &fakeDriver{
		response: driver.Response{Output: "ok"},
		caps: driver.RunPolicyCapabilities{
			Isolation:  true,
			WebSearch:  true,
			Browser:    true,
			Permission: driver.HumanDecisionSupport{Ask: true, AutoApprove: true, AutoReject: true},
			PlanReview: driver.HumanDecisionSupport{Ask: true, AutoApprove: true, AutoReject: true},
			Question:   driver.QuestionSupport{Ask: true, AutoReject: true},
		},
	}
}

func (f *fakeDriver) Descriptor() driver.Descriptor {
	if f.descriptor != nil {
		return *f.descriptor
	}
	return driver.Descriptor{
		Type:          "fake",
		DisplayName:   "Fake Driver",
		Sessions:      driver.SessionCapability{SupportsResume: true},
		RunPolicyCaps: f.caps,
	}
}

func (f *fakeDriver) ValidateConfig(any) error { return nil }

func (f *fakeDriver) SessionConfigFingerprint() (string, error) {
	return "fake-driver-config/v1", nil
}

func (f *fakeDriver) SessionCodec() driver.SessionCodec { return fakeSessionCodec{} }

func (f *fakeDriver) StreamCapability() driver.StreamCapability { return f.streamCaps }

func (f *fakeDriver) Run(ctx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	fn := f.runFunc
	resp, err := f.response, f.err
	f.mu.Unlock()

	if fn != nil {
		return fn(ctx, req, sink)
	}
	if err != nil {
		return driver.Response{}, err
	}
	return resp, nil
}

// request returns the i-th recorded request, failing the test when fewer
// runs were observed.
func (f *fakeDriver) request(t *testing.T, i int) driver.Request {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if i >= len(f.requests) {
		t.Fatalf("fake driver saw %d request(s), want index %d", len(f.requests), i)
	}
	return f.requests[i]
}

// lastRequest returns the most recent recorded request.
func (f *fakeDriver) lastRequest(t *testing.T) driver.Request {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		t.Fatal("fake driver saw no requests")
	}
	return f.requests[len(f.requests)-1]
}

// runCount returns how many runs the driver observed.
func (f *fakeDriver) runCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

// blockUntilCancelled makes the fake behave like a hung child process: it
// returns only when ctx ends, propagating ctx.Err() as an infrastructure
// failure.
func (f *fakeDriver) blockUntilCancelled() {
	f.runFunc = func(ctx context.Context, _ driver.Request, _ driver.EventSink) (driver.Response, error) {
		<-ctx.Done()
		return driver.Response{}, ctx.Err()
	}
}
