package clihelper

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
)

func assertStdinLifecycleDone(t *testing.T, ctrl *stdinController) {
	t.Helper()
	select {
	case <-ctrl.done:
	default:
		t.Fatal("completion returned without notifying done")
	}
	if err := ctrl.Write([]byte("late\n")); !errors.Is(err, ErrStdinClosed) {
		t.Fatalf("late Write: %v", err)
	}
	if err := ctrl.Close(); err != nil {
		t.Fatalf("late Close: %v", err)
	}
}

func TestStdinLifecycleBlockedWrite(t *testing.T) {
	for _, full := range []bool{false, true} {
		for _, finish := range []string{"close", "done"} {
			t.Run(fmt.Sprintf("full_%t/%s", full, finish), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					ctrl := newStdinController()
					if full {
						ctrl.signalReady()
						for i := 0; i < cap(ctrl.ch); i++ {
							if err := ctrl.Write([]byte("queued\n")); err != nil {
								t.Fatal(err)
							}
						}
					}
					returned := make(chan error, 1)
					go func() { returned <- ctrl.Write([]byte("blocked\n")) }()
					// The bubble has no OS I/O: Wait proves the Write is blocked on its
					// ready gate or full queue, rather than guessing with a sleep.
					synctest.Wait()
					select {
					case err := <-returned:
						t.Fatalf("Write did not block: %v", err)
					default:
					}
					if finish == "close" {
						if err := ctrl.Close(); err != nil {
							t.Fatal(err)
						}
					} else {
						ctrl.markDone()
					}
					synctest.Wait()
					select {
					case err := <-returned:
						if !errors.Is(err, ErrStdinClosed) {
							t.Fatalf("blocked Write: %v", err)
						}
					default:
						t.Fatal("termination did not release blocked Write")
					}
					if full && len(ctrl.ch) != cap(ctrl.ch) {
						t.Fatal("blocked frame was accepted or queued frames discarded")
					}
					ctrl.markDone()
					assertStdinLifecycleDone(t, ctrl)
				})
			})
		}
	}
}

func TestStdinLifecycleFinisherOrder(t *testing.T) {
	for _, closeFirst := range []bool{false, true} {
		for _, order := range []string{"writer_first", "process_first", "together"} {
			t.Run(fmt.Sprintf("close_first_%t/%s", closeFirst, order), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					ctrl := newStdinController()
					if closeFirst {
						if err := ctrl.Close(); err != nil {
							t.Fatal(err)
						}
						select {
						case <-ctrl.done:
							t.Fatal("Close became an abort instead of draining input")
						default:
						}
					}
					writerGate, processGate := make(chan struct{}), make(chan struct{})
					if order == "together" {
						processGate = writerGate
					}
					writerDone, processDone := make(chan struct{}), make(chan struct{})
					go func() { <-writerGate; ctrl.markDone(); close(writerDone) }()
					go func() { <-processGate; ctrl.markDone(); close(processDone) }()
					synctest.Wait()
					switch order {
					case "writer_first":
						close(writerGate)
						<-writerDone
						close(processGate)
					case "process_first":
						close(processGate)
						<-processDone
						close(writerGate)
					default:
						close(writerGate)
					}
					<-writerDone
					<-processDone
					ctrl.markDone()
					assertStdinLifecycleDone(t, ctrl)
					// readiness arriving after termination must never reopen Write.
					ctrl.signalReady()
					assertStdinLifecycleDone(t, ctrl)
				})
			})
		}
	}
}

func TestStdinLifecycleConcurrentFinishers(t *testing.T) {
	ctrl := newStdinController()
	start := make(chan struct{})
	var wg sync.WaitGroup
	// This is concurrency pressure, not a claim that a start barrier forces
	// both pre-fix calls into the unsafe default branch. Native T25 is the red.
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; ctrl.markDone(); _ = ctrl.Close() }()
	}
	close(start)
	joined := make(chan struct{})
	go func() { wg.Wait(); close(joined) }()
	select {
	case <-joined:
	case <-time.After(3 * time.Second):
		t.Fatal("finishers did not return")
	}
	assertStdinLifecycleDone(t, ctrl)
}

const stdinLifecycleChildEnv = "AGENT_ADAPTOR_STDIN_LIFECYCLE_CHILD"

// Reenter only this test in a private subprocess. No installed shell, CLI,
// provider, external network, timing delay or real user profile is required.
func TestStdinLifecycleProcess(t *testing.T) {
	if mode := os.Getenv(stdinLifecycleChildEnv); mode != "" {
		os.Exit(runStdinLifecycleChild(mode))
	}
	for _, mode := range []string{"writer_first", "process_first", "cancel", "observer_error"} {
		t.Run(mode, func(t *testing.T) {
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			home := t.TempDir()
			ctrl := newStdinController()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			stdoutSeen, stderrSeen := false, false // Observe is serialized by helper.
			var observedStdout, observedStderr strings.Builder
			ready := make(chan struct{})
			var readyOnce sync.Once
			observerErr := errors.New("fixture observer aborted")
			type outcome struct {
				result CommandResult
				err    error
			}
			completed := make(chan outcome, 1)
			go func() {
				result, err := Run(ctx, CommandRequest{Command: executable, Args: []string{"-test.run=^TestStdinLifecycleProcess$"}, Prompt: "prompt-first\n", Stdin: ctrl,
					CWD: home, Env: []driver.EnvBinding{{Name: stdinLifecycleChildEnv, Value: mode}, {Name: "HOME", Value: home}, {Name: "USERPROFILE", Value: home}},
					Observe: func(stream string, chunk []byte, _ time.Time) error {
						if stream == "stdout" {
							observedStdout.Write(chunk)
							stdoutSeen = strings.Contains(observedStdout.String(), "prompt-first\n")
						}
						if stream == "stderr" {
							observedStderr.Write(chunk)
							stderrSeen = strings.Contains(observedStderr.String(), "diagnostic\n")
						}
						if mode == "writer_first" && stdoutSeen {
							// The child emits only after stdin EOF. Hold output completion
							// until the writer's defer has notified done, forcing writer-first
							// relative to Run's later wg.Wait / cmd.Wait / markDone.
							select {
							case <-ctrl.done:
							case <-ctx.Done():
								return ctx.Err()
							}
						}
						if stdoutSeen && stderrSeen {
							readyOnce.Do(func() { close(ready) })
							if mode == "observer_error" {
								return observerErr
							}
						}
						return nil
					}}, &captureSink{})
				completed <- outcome{result, err}
			}()
			if mode == "writer_first" {
				for _, frame := range []string{"frame-one\n", "frame-two\n", "frame-three\n"} {
					if err := ctrl.Write([]byte(frame)); err != nil {
						t.Fatal(err)
					}
				}
				if err := ctrl.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "cancel" {
				select {
				case <-ready:
					cancel()
				case got := <-completed:
					t.Fatalf("exited before cancellation: %#v", got)
				case <-ctx.Done():
					t.Fatal("no observed output before cancellation")
				}
			}
			var got outcome
			select {
			case got = <-completed:
			case <-time.After(6 * time.Second):
				t.Fatal("Run did not terminate")
			}
			assertStdinLifecycleDone(t, ctrl)
			wantOut := "prompt-first\n"
			if mode == "writer_first" {
				wantOut += "frame-one\nframe-two\nframe-three\n"
			}
			if got.result.RawStreams.Stdout != wantOut || got.result.RawStreams.Stderr != "diagnostic\n" {
				t.Fatalf("stdin FIFO or captured output changed: %#v", got.result)
			}
			switch mode {
			case "writer_first", "process_first":
				if got.err != nil || got.result.ExitCode != 0 || got.result.Signal != "" || got.result.TimedOut {
					t.Fatalf("healthy completion: %#v %v", got.result, got.err)
				}
			case "observer_error":
				if !errors.Is(got.err, observerErr) {
					t.Fatalf("observer cause lost: %v", got.err)
				}
			case "cancel":
				if !errors.Is(ctx.Err(), context.Canceled) || got.result.TimedOut || (got.result.ExitCode == 0 && got.result.Signal == "") {
					t.Fatalf("cancel outcome: %#v %v", got.result, got.err)
				}
			}
		})
	}
}

func runStdinLifecycleChild(mode string) int {
	reader := bufio.NewReader(os.Stdin)
	if mode == "writer_first" {
		raw, err := io.ReadAll(reader)
		if err != nil {
			return 31
		}
		fmt.Fprint(os.Stdout, string(raw))
		fmt.Fprint(os.Stderr, "diagnostic\n")
		return 0
	}
	line, err := reader.ReadString('\n')
	if err != nil {
		return 32
	}
	fmt.Fprint(os.Stdout, line)
	fmt.Fprint(os.Stderr, "diagnostic\n")
	if mode != "process_first" {
		_, _ = io.Copy(io.Discard, reader)
	}
	return 0
}
