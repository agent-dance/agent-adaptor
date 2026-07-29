package adaptor

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
)

func TestAgentCloseIsBoundedIdempotentAndRejectsNewRuns(t *testing.T) {
	closeErr := errors.New("close failed")
	d := &lifecycleTestDriver{
		started:  make(chan struct{}),
		release:  make(chan struct{}),
		closeErr: closeErr,
	}
	agent := New(d)

	firstDone := make(chan error, 1)
	go func() { firstDone <- agent.Close(context.Background()) }()
	<-d.started

	stream := agent.Stream(context.Background(), "must not run")
	if _, err := stream.Result(); !errors.Is(err, ErrAgentClosed) {
		t.Fatalf("run after Close started = %v, want ErrAgentClosed", err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := agent.Close(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("concurrent bounded Close = %v, want context deadline", err)
	}

	close(d.release)
	if err := <-firstDone; !errors.Is(err, closeErr) {
		t.Fatalf("first Close = %v, want configured error", err)
	}
	if err := agent.Close(context.Background()); !errors.Is(err, closeErr) {
		t.Fatalf("idempotent Close = %v, want same configured error", err)
	}
	d.mu.Lock()
	calls := d.closeCalls
	runs := d.runCalls
	d.mu.Unlock()
	if calls != 1 || runs != 0 {
		t.Fatalf("lifecycle calls: Close=%d Run=%d, want 1/0", calls, runs)
	}
}

func TestNilAgentCloseReturnsErrAgentClosed(t *testing.T) {
	var agent *Agent
	if err := agent.Close(context.Background()); !errors.Is(err, ErrAgentClosed) {
		t.Fatalf("nil Agent.Close = %v, want ErrAgentClosed", err)
	}
}

type lifecycleTestDriver struct {
	mu         sync.Mutex
	started    chan struct{}
	release    chan struct{}
	closeErr   error
	closeCalls int
	runCalls   int
}

func (d *lifecycleTestDriver) Descriptor() driver.Descriptor {
	return driver.Descriptor{
		Type:    "lifecycle-test",
		Process: driver.ProcessCapability{Persistent: true},
	}
}

func (*lifecycleTestDriver) ValidateConfig(any) error { return nil }

func (d *lifecycleTestDriver) Run(context.Context, driver.Request, driver.EventSink) (driver.Response, error) {
	d.mu.Lock()
	d.runCalls++
	d.mu.Unlock()
	return driver.Response{}, nil
}

func (d *lifecycleTestDriver) CloseProcesses(ctx context.Context) error {
	d.mu.Lock()
	d.closeCalls++
	d.mu.Unlock()
	close(d.started)
	select {
	case <-d.release:
		return d.closeErr
	case <-ctx.Done():
		return ctx.Err()
	}
}
