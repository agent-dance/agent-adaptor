package clihelper

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/v2/driver"
	"github.com/agent-dance/agent-adaptor/v2/internal/processx"
)

func TestCommandOutputExitHelper(t *testing.T) {
	mode := os.Getenv("CLIHELPER_OUTPUT_EXIT_HELPER")
	if mode == "" {
		return
	}
	_, _ = fmt.Fprint(os.Stdout, "stdout-before-exit")
	_, _ = fmt.Fprint(os.Stderr, "stderr-before-exit")
	if mode == "cancel" {
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	}
	os.Exit(17)
}

func TestRunBoundsInheritedOutputAfterExit(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, held := range []string{"stdout", "both"} {
		t.Run(held, func(t *testing.T) {
			fire := make(chan time.Time, 1)
			armed := make(chan time.Duration, 1)
			reaped := make(chan struct{})
			observed := []chan struct{}{make(chan struct{}), make(chan struct{})}
			var observedOnce [2]sync.Once
			var readersFinished atomic.Int32
			var closesMu sync.Mutex
			closes := make(map[*os.File]int)
			closed := func(f *os.File, err error) {
				closesMu.Lock()
				defer closesMu.Unlock()
				closes[f]++
				if err != nil {
					t.Errorf("owner Close(%s): %v", f.Name(), err)
				}
			}
			var pipes *commandOutput
			var retained []*processx.PipeFile
			hooks := &outputHooks{
				afterClose: closed,
				afterStart: func(p *commandOutput) {
					pipes = p
					retained = append(retained, p.stdoutWriter)
					p.stdoutWriter = nil
					if held == "both" {
						retained = append(retained, p.stderrWriter)
						p.stderrWriter = nil
					}
				},
				afterWait:   func() { close(reaped) },
				afterReader: func() { readersFinished.Add(1) },
				newDrainTimer: func(d time.Duration) (<-chan time.Time, func()) {
					armed <- d
					return fire, func() {}
				},
			}
			ctrl := newStdinController()
			defer ctrl.Close()
			done := make(chan struct{})
			var result CommandResult
			var runErr error
			go func() {
				defer close(done)
				result, runErr = run(context.Background(), CommandRequest{
					Command: binary, Args: []string{"-test.run=^TestCommandOutputExitHelper$"},
					Env:   []driver.EnvBinding{{Name: "CLIHELPER_OUTPUT_EXIT_HELPER", Value: "1"}},
					Stdin: ctrl,
					Observe: func(stream string, _ []byte, _ time.Time) error {
						i := 0
						if stream == "stderr" {
							i = 1
						}
						observedOnce[i].Do(func() { close(observed[i]) })
						return nil
					},
				}, &captureSink{}, hooks)
			}()
			select {
			case d := <-armed:
				if d != 5*time.Second {
					t.Fatalf("drain bound = %v", d)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("exit did not arm drain")
			}
			defer func() {
				pipes.close()
				for _, f := range retained {
					_ = f.Close()
				}
			}()
			<-reaped
			for _, ch := range observed {
				<-ch
			}
			select {
			case <-done:
				t.Fatal("returned before EOF or drain deadline")
			default:
			}
			fire <- time.Now()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("drain failed to release readers")
			}
			if runErr != nil || result.ExitCode != 17 {
				t.Fatalf("exit lost: result=%+v err=%v", result, runErr)
			}
			if result.RawStreams.Stdout != "stdout-before-exit" || result.RawStreams.Stderr != "stderr-before-exit" {
				t.Fatalf("output lost: %+v", result.RawStreams)
			}
			if readersFinished.Load() != 2 {
				t.Fatalf("reader goroutine leaked: finished=%d", readersFinished.Load())
			}
			for _, f := range []*os.File{pipes.stdout.File, pipes.stderr.File} {
				// Stat bypasses the closed-file check on Windows; Read checks the owner.
				if _, err := f.Read(make([]byte, 1)); !errors.Is(err, os.ErrClosed) {
					t.Fatalf("read fd not closed: %v", err)
				}
			}
			// Repeat cleanup to prove no second closer reaches any os.File.
			pipes.close()
			for _, f := range retained {
				_ = f.Close()
				_ = f.Close()
			}
			closesMu.Lock()
			if len(closes) != 6 {
				t.Errorf("closed %d pipe ends, want 6", len(closes))
			}
			for f, n := range closes {
				if n != 1 {
					t.Errorf("Close(%s) called %d times", f.Name(), n)
				}
			}
			closesMu.Unlock()
			select {
			case <-ctrl.done:
			default:
				t.Fatal("stdin writer not released on exit")
			}
		})
	}
}

func TestRunOwnsPipeEnds(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"eof", "start-failure", "cancel"} {
		t.Run(scenario, func(t *testing.T) {
			var mu sync.Mutex
			closes := make(map[*os.File]int)
			hooks := &outputHooks{afterClose: func(f *os.File, err error) {
				mu.Lock()
				defer mu.Unlock()
				closes[f]++
				if err != nil {
					t.Errorf("owner Close(%s): %v", f.Name(), err)
				}
				if _, err := f.Read(make([]byte, 1)); !errors.Is(err, os.ErrClosed) {
					t.Errorf("owned File remains open: %v", err)
				}
			}}
			ctrl := newStdinController()
			defer ctrl.Close()
			req := CommandRequest{
				Command: binary, Args: []string{"-test.run=^TestCommandOutputExitHelper$"},
				Env: []driver.EnvBinding{{Name: "CLIHELPER_OUTPUT_EXIT_HELPER", Value: "1"}}, Stdin: ctrl,
			}
			if scenario == "start-failure" {
				req.Command = t.TempDir() + "/missing-command"
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if scenario == "cancel" {
				req.Env[0].Value = "cancel"
				req.Observe = func(stream string, _ []byte, _ time.Time) error {
					if stream == "stdout" {
						cancel()
					}
					return nil
				}
			}
			result, err := run(ctx, req, &captureSink{}, hooks)
			if scenario == "cancel" {
				// Cancellation is a command outcome, never a pipe infrastructure error.
				if err != nil || !errors.Is(ctx.Err(), context.Canceled) || result.ExitCode == 0 || result.TimedOut || result.RawStreams.Stdout != "stdout-before-exit" {
					t.Fatalf("cancel: %+v %v", result, err)
				}
			} else if scenario == "start-failure" {
				if err == nil {
					t.Fatal("Start succeeded")
				}
			} else if err != nil || result.ExitCode != 17 {
				t.Fatalf("Run: %+v %v", result, err)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(closes) != 6 {
				t.Fatalf("closed %d pipe ends, want 6", len(closes))
			}
			for f, n := range closes {
				if n != 1 {
					t.Errorf("Close(%s) called %d times", f.Name(), n)
				}
			}
		})
	}
}
