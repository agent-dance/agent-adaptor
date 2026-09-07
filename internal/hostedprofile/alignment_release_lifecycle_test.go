package hostedprofile

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/profile"
)

// These tests use only the existing per-claim failure boundaries. Actual
// successor acquisition, filesystem bytes and a third contender form the oracle;
// no production state flag or owner-test helper determines expected behavior.
func alignmentClaimCall(t *testing.T, fn func(context.Context) error) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- fn(ctx) }()
	select {
	case err := <-done:
		return err
	case <-time.After(4 * time.Second):
		t.Fatal("local watchdog: Claim operation did not return")
		return nil
	}
}
func alignmentClaimAcquire(t *testing.T, s Spec) *Claim {
	t.Helper()
	type outcome struct {
		claim *Claim
		err   error
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan outcome, 1)
	go func() { c, e := Acquire(ctx, s); done <- outcome{c, e} }()
	select {
	case out := <-done:
		if out.err != nil {
			t.Fatal(out.err)
		}
		return out.claim
	case <-time.After(4 * time.Second):
		t.Fatal("local watchdog: Claim acquire did not return")
		return nil
	}
}
func TestAlignmentReleaseLifecycle(t *testing.T) {
	for _, stage := range []string{"lock-close", "root-close"} {
		t.Run(stage, func(t *testing.T) {
			source := filepath.Join(t.TempDir(), "source")
			if err := os.Mkdir(source, 0700); err != nil {
				t.Fatal(err)
			}
			spec := Spec{DriverType: "cursor", SourceDir: source}
			first := alignmentClaimAcquire(t, spec)
			defer func() {
				first.beforeLockClose = nil
				first.beforeRootClose = nil
				first.beforeStateWrite = nil
				first.beforeUnlock = nil
				if err := alignmentClaimCall(t, first.ReleaseClean); err != nil {
					t.Error(err)
				}
			}()
			nonce := make([]byte, 32)
			if _, err := rand.Read(nonce); err != nil {
				t.Fatal(err)
			}
			if err := alignmentClaimCall(t, func(ctx context.Context) error {
				return first.Initialize(ctx, func(_ context.Context, dir string) error {
					return os.WriteFile(filepath.Join(dir, "session.jsonl"), nonce, 0600)
				})
			}); err != nil {
				t.Fatal(err)
			}
			if err := alignmentClaimCall(t, first.BeginUse); err != nil {
				t.Fatal(err)
			}
			injected := errors.New("t20 post-unlock handle-close fault")
			var faults atomic.Int32
			hook := func() error { faults.Add(1); return injected }
			if stage == "lock-close" {
				first.beforeLockClose = hook
			} else {
				first.beforeRootClose = hook
			}
			if err := alignmentClaimCall(t, first.ReleaseClean); !errors.Is(err, injected) || faults.Load() != 1 {
				t.Fatalf("post-unlock fault lost: %v calls=%d", err, faults.Load())
			}

			// A separate actor becomes active while the old claim still owns its failed
			// handle. It does not release its lock until this test explicitly permits it.
			type acquisition struct {
				claim *Claim
				err   error
			}
			active := make(chan acquisition, 1)
			release := make(chan struct{})
			done := make(chan error, 1)
			var releaseOnce sync.Once
			releaseSuccessor := func() { releaseOnce.Do(func() { close(release) }) }
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			go func() {
				next, err := Acquire(ctx, spec)
				if err == nil {
					err = next.Initialize(ctx, func(context.Context, string) error { return errors.New("successor must not reseed") })
				}
				if err == nil {
					err = next.BeginUse(ctx)
				}
				if err == nil {
					err = os.WriteFile(filepath.Join(next.Dir(), "session.jsonl"), append(append([]byte{}, nonce...), []byte("\nsuccessor-active")...), 0600)
				}
				active <- acquisition{next, err}
				if next == nil {
					done <- err
					return
				}
				select {
				case <-release:
				case <-ctx.Done():
				}
				closeCtx, stop := context.WithTimeout(context.Background(), 3*time.Second)
				defer stop()
				done <- next.ReleaseClean(closeCtx)
			}()
			defer func() {
				releaseSuccessor()
				select {
				case err := <-done:
					if err != nil {
						t.Errorf("successor cleanup: %v", err)
					}
				case <-time.After(4 * time.Second):
					t.Error("local watchdog: successor cleanup did not return")
				}
			}()
			var next *Claim
			select {
			case out := <-active:
				if out.err != nil {
					t.Fatal(out.err)
				}
				next = out.claim
			case <-time.After(4 * time.Second):
				t.Fatal("successor active barrier not reached")
			}
			paths := []string{filepath.Join(filepath.Dir(next.Dir()), "state.json"), filepath.Join(filepath.Dir(next.Dir()), "owner.json"), filepath.Join(filepath.Dir(next.Dir()), "seed.json"), filepath.Join(filepath.Dir(next.Dir()), "owner.lock"), filepath.Join(next.Dir(), "session.jsonl")}
			before := make(map[string][]byte)
			for _, path := range paths {
				b, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				before[path] = b
			}
			if !bytes.Contains(before[paths[0]], []byte(`"phase":"active"`)) || !bytes.Equal(before[paths[4]], append(append([]byte{}, nonce...), []byte("\nsuccessor-active")...)) {
				t.Fatal("successor has not published the independent active state/session")
			}
			var illegalWrites atomic.Int32
			forbidden := func() error { illegalWrites.Add(1); return errors.New("old claim retried state/unlock after transfer") }
			first.beforeStateWrite = forbidden
			first.beforeUnlock = forbidden
			// Retry the still-failing handle with B active, then let handle cleanup finish.
			if err := alignmentClaimCall(t, first.ReleaseClean); !errors.Is(err, injected) {
				t.Fatalf("second handle fault changed: %v", err)
			}
			first.beforeLockClose = nil
			first.beforeRootClose = nil
			for i := 0; i < 2; i++ {
				if err := alignmentClaimCall(t, first.ReleaseClean); err != nil {
					t.Fatalf("old cleanup retry: %v", err)
				}
			}
			if illegalWrites.Load() != 0 {
				t.Fatalf("old generation retried state/unlock %d times", illegalWrites.Load())
			}
			for _, path := range paths {
				b, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(b, before[path]) {
					t.Errorf("old release changed successor file %s: %v", filepath.Base(path), err)
				}
			}
			if err := alignmentClaimCall(t, next.Validate); err != nil {
				t.Fatalf("successor no longer valid: %v", err)
			}
			if err := alignmentClaimCall(t, func(ctx context.Context) error {
				unexpected, err := Acquire(ctx, spec)
				if unexpected != nil {
					_ = unexpected.ReleaseUnused(ctx)
				}
				return err
			}); !errors.Is(err, profile.ErrInUse) {
				t.Fatalf("old release disturbed successor kernel lock: %v", err)
			}
		})
	}
}
