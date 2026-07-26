package adaptor_test

// The programmable fake driver backing the S1/S2/S4 scenario tests and the
// RunError path tests.
//
// Placement note (P0.6): the task sheet allowed either internal/testutil or
// the next/ test package. It lives here so that next/'s gate
// (go build/vet/test ./next/...) depends only on next/ + driver/ — the
// legacy root package (currently being strangled into internal/engine in a
// parallel workstream) never enters this package's compile graph. When later
// phases need the fake outside next/, promote it to internal/testutil.

import (
	"context"
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
}

var _ driver.Driver = (*fakeDriver)(nil)

func newFakeDriver() *fakeDriver {
	return &fakeDriver{response: driver.Response{Output: "ok"}}
}

func (f *fakeDriver) Descriptor() driver.Descriptor {
	return driver.Descriptor{Type: "fake", DisplayName: "Fake Driver"}
}

func (f *fakeDriver) ValidateConfig(any) error { return nil }

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
