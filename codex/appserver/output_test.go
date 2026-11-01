package appserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/processx"
)

type gatedOutputReader struct {
	reader io.Reader
	gate   <-chan struct{}
	eof    bool
}

func (r *gatedOutputReader) Read(p []byte) (int, error) {
	<-r.gate
	n, err := r.reader.Read(p)
	if errors.Is(err, io.EOF) {
		r.eof = true
	}
	return n, err
}

func TestRunDrainsOutputAfterProcessExit(t *testing.T) {
	fake := buildFakeAppserver(t)
	for _, persistent := range []bool{false, true} {
		for _, scenario := range []string{"success", "failed", "malformed-tail", "cancel"} {
			// A healthy persistent turn deliberately keeps the process alive;
			// force shutdown through native output validation for this case.
			name := scenario
			if persistent {
				name = "persistent/" + name
			}
			t.Run(name, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				reaped := make(chan struct{})
				var reader *gatedOutputReader
				opts := Options{
					Command: fake,
					Env:     []driver.EnvBinding{{Name: "FAKE_APPSERVER_SCENARIO", Value: scenario}},
					Prompt:  "hello", RunID: "delayed-output",
					processHooks: &processHooks{
						stderrReader: func(r io.Reader) io.Reader {
							reader = &gatedOutputReader{reader: r, gate: reaped}
							return reader
						},
						afterWait: func() { close(reaped) },
					},
				}
				if persistent && scenario == "success" {
					opts.OutputSchema = &driver.OutputSchema{
						Format:     driver.OutputFormatJSONSchema,
						SchemaJSON: []byte(`{"type":"object"}`),
						OnInvalid:  driver.StructuredOutputFailRun,
					}
				}
				sink := &recordingSink{onStream: func(payload driver.StreamPayload) {
					if scenario == "cancel" && payload.Kind == driver.StreamRunStarted {
						cancel()
					}
				}}
				var result driver.Response
				var err error
				if persistent {
					p, openErr := Open(ctx, opts, sink)
					if openErr != nil {
						t.Fatalf("Open: %v", openErr)
					}
					defer p.TerminateAndWait(context.Background())
					result, _, err = p.RunTurn(ctx, opts, sink)
					select {
					case <-p.Done():
					default:
						t.Fatal("failed/cancelled turn returned before process/reader join")
					}
				} else {
					result, err = Run(ctx, opts, sink)
				}
				if scenario == "cancel" {
					if !errors.Is(err, context.Canceled) || result.Failure == nil || result.Failure.Code != driver.FailureCancelled {
						t.Fatalf("cancel classification: err=%v failure=%+v", err, result.Failure)
					}
				} else if scenario == "malformed-tail" {
					if err == nil || !strings.Contains(result.RawStreams.Stdout, "end-of-raw-tail") {
						t.Fatalf("corrupt protocol tail lost: err=%v stdout length=%d", err, len(result.RawStreams.Stdout))
					}
				} else if err != nil {
					t.Fatalf("Run: %v", err)
				}
				if scenario == "failed" && (result.Failure == nil || result.Failure.Code != driver.FailureAgentError) {
					t.Fatalf("provider failure reclassified during cleanup: %+v", result.Failure)
				}
				if scenario != "success" || persistent {
					if result.Checkpoint != nil {
						t.Fatal("unhealthy turn retained checkpoint")
					}
				}
				want := "stderr-after-terminal"
				if scenario == "cancel" {
					want = "stderr-before-cancel"
				}
				if result.RawStreams == nil || result.RawStreams.Stderr != want {
					t.Fatalf("stderr captured before drain: got=%+v want=%q", result.RawStreams, want)
				}
				if reader == nil || !reader.eof {
					t.Fatal("result returned before stderr reader reached EOF")
				}
				verifyAppserverContracts(t, sink, result)
			})
		}
	}
}

func TestProcessOutputExitBoundsDrain(t *testing.T) {
	cmd := exec.Command(buildFakeAppserver(t))
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := newProcessOutput(cmd, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer output.close()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = stdin.Close()
	// Intentionally retain our duplicate writers, simulating inherited pipes
	// held by a descendant after the parent process exits.
	stdout, stderr := &syncBuffer{}, &syncBuffer{}
	stream := newStdioStream(stdin, io.TeeReader(outputPipeReader{output.stdout}, stdout))
	go func() {
		var frame any
		_ = stream.ReadObject(&frame)
	}()
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		_, _ = io.Copy(stderr, output.stderr)
	}()
	done := make(chan error, 1)
	started := time.Now()
	go func() { done <- output.wait(cmd, stream, stderrDone, nil, nil) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Wait: %v", err)
		}
	case <-time.After(2 * outputDrainTimeout):
		output.close()
		<-done
		t.Fatal("post-exit output drain did not respect its bound")
	}
	if time.Since(started) < outputDrainTimeout {
		t.Fatal("post-exit drain closed pipes before allowing its grace period")
	}
	if got := stderr.String(); got != "stderr-after-terminal" {
		t.Fatalf("bounded drain lost observed stderr: %q", got)
	}
}

// countedOutputReader verifies that closing read ends actually releases blocked
// reads, independently of whether the inherited writer ever closes.
type countedOutputReader struct {
	reader       io.Reader
	active       atomic.Int32
	finished     chan struct{}
	once         sync.Once
	observed     chan struct{}
	observedOnce sync.Once
}

func (r *countedOutputReader) Read(b []byte) (int, error) {
	r.active.Add(1)
	defer r.active.Add(-1)
	n, err := r.reader.Read(b)
	if n > 0 {
		r.observedOnce.Do(func() { close(r.observed) })
	}
	if err != nil {
		r.once.Do(func() { close(r.finished) })
	}
	return n, err
}

func TestRunBoundsInheritedOutputAfterExit(t *testing.T) {
	fake := buildFakeAppserver(t)
	for _, persistent := range []bool{false, true} {
		for _, held := range []string{"stdout", "both"} {
			for _, scenario := range []string{"exit-before-terminal", "exit-before-reply", "cancel"} {
				name := "one-shot/" + held + "/" + scenario
				if persistent {
					name = "persistent/" + held + "/" + scenario
				}
				t.Run(name, func(t *testing.T) {
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					fire := make(chan time.Time, 1)
					armed := make(chan time.Duration, 1)
					reaped := make(chan struct{})
					textEnd := make(chan struct{})
					var stopped atomic.Int32
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
					var pipes *processOutput
					var retained []*processx.PipeFile
					var readers []*countedOutputReader
					wrap := func(r io.Reader) io.Reader {
						reader := &countedOutputReader{reader: r, finished: make(chan struct{}), observed: make(chan struct{})}
						readers = append(readers, reader)
						return reader
					}
					opts := Options{
						Command: fake, Prompt: "hello", RunID: "inherited-output",
						Env: []driver.EnvBinding{{Name: "FAKE_APPSERVER_SCENARIO", Value: scenario}},
						processHooks: &processHooks{
							stdoutReader: wrap, stderrReader: wrap,
							afterClose: closed,
							afterStart: func(p *processOutput) {
								pipes = p
								retained = append(retained, p.stdoutWriter)
								p.stdoutWriter = nil
								if held == "both" {
									retained = append(retained, p.stderrWriter)
									p.stderrWriter = nil
								}
							},
							afterWait: func() { close(reaped) },
							newDrainTimer: func(d time.Duration) (<-chan time.Time, func()) {
								armed <- d
								return fire, func() { stopped.Add(1) }
							},
						},
					}
					sink := &recordingSink{onStream: func(payload driver.StreamPayload) {
						if payload.Kind == driver.StreamTextEnd {
							close(textEnd)
						}
						if scenario == "cancel" && payload.Kind == driver.StreamRunStarted {
							cancel()
						}
					}}
					var result driver.Response
					var runErr error
					var proc *Process
					done := make(chan struct{})
					go func() {
						defer close(done)
						if persistent {
							var err error
							proc, err = Open(ctx, opts, sink)
							if err != nil {
								runErr = err
								return
							}
							result, _, runErr = proc.RunTurn(ctx, opts, sink)
						} else {
							result, runErr = Run(ctx, opts, sink)
						}
					}()
					// Watchdogs detect hangs, but neither advance the fake clock nor
					// cancel the host context on the independent-exit path.
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
					if scenario == "exit-before-terminal" {
						<-textEnd
					}
					capturedReaders := append([]*countedOutputReader(nil), readers...)
					if len(capturedReaders) != 2 {
						t.Fatalf("readers = %d", len(capturedReaders))
					}
					for _, reader := range capturedReaders {
						<-reader.observed
					}
					select {
					case <-done:
						t.Fatal("drain returned before EOF or deadline")
					default:
					}
					fire <- time.Now() // advance the five-second drain deadline
					select {
					case <-done:
					case <-time.After(10 * time.Second):
						t.Fatal("drain did not release inherited handles")
					}
					if runErr == nil || result.Failure == nil || result.Checkpoint != nil {
						t.Fatalf("unhealthy exit: err=%v result=%+v", runErr, result)
					}
					if scenario == "cancel" {
						if !errors.Is(runErr, context.Canceled) || result.Failure.Code != driver.FailureCancelled {
							t.Fatalf("cancellation reclassified: %v %+v", runErr, result.Failure)
						}
					} else {
						var exitErr *exec.ExitError
						if ctx.Err() != nil || result.Failure.Code != driver.FailureAgentError || !strings.Contains(runErr.Error(), "exited") || (!errors.As(runErr, &exitErr) || exitErr.ExitCode() != 17) {
							t.Fatalf("exit classified incorrectly: err=%v failure=%+v", runErr, result.Failure)
						}
					}
					wantStderr := "stderr-before-exit"
					if scenario == "cancel" {
						wantStderr = "stderr-before-cancel"
					}
					wantStdout := "turn/started"
					if scenario == "exit-before-reply" {
						// No turn stdout was emitted. Persistent Raw excludes the
						// initialization/thread replies, while one-shot Raw keeps them.
						wantStdout = ""
						if !persistent {
							wantStdout = "thread-fake"
						}
					}
					if result.RawStreams == nil || result.RawStreams.Stderr != wantStderr || !strings.Contains(result.RawStreams.Stdout, wantStdout) {
						t.Fatalf("observed output lost: %+v", result.RawStreams)
					}
					if persistent {
						select {
						case <-proc.Done():
						default:
							t.Fatal("persistent readers not joined")
						}
					}
					for _, reader := range capturedReaders {
						select {
						case <-reader.finished:
						default:
							t.Fatal("reader still blocked")
						}
						if reader.active.Load() != 0 {
							t.Fatal("reader goroutine leaked")
						}
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
					if stopped.Load() != 1 {
						t.Fatalf("timer not released: %d", stopped.Load())
					}
					verifyAppserverContracts(t, sink, result)
				})
			}
		}
	}
}

func TestOutputPipeReaderCutoff(t *testing.T) {
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_ = readEnd.Close()
	_ = writeEnd.Close()
	for _, tc := range []struct {
		name, payload string
		want          error
	}{
		{"complete", `{}`, nil},
		{"truncated", `{"method":`, io.ErrUnexpectedEOF},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The pipe closes only after the decoder has consumed the bytes
			// captured before cutoff. Partial frames must remain protocol errors.
			decoder := json.NewDecoder(outputPipeReader{io.MultiReader(strings.NewReader(tc.payload), readEnd)})
			var frame any
			if err := decoder.Decode(&frame); !errors.Is(err, tc.want) {
				t.Fatalf("Decode: %v, want %v", err, tc.want)
			}
			if tc.want == nil {
				if err := decoder.Decode(&frame); !errors.Is(err, io.EOF) {
					t.Fatalf("end of complete frame: %v", err)
				}
			}
		})
	}
}

func TestRunOwnsPipeEnds(t *testing.T) {
	fake := buildFakeAppserver(t)
	for _, persistent := range []bool{false, true} {
		for _, startFailure := range []bool{false, true} {
			name := "one-shot"
			if persistent {
				name = "persistent"
			}
			if startFailure {
				name += "/start-failure"
			}
			t.Run(name, func(t *testing.T) {
				var mu sync.Mutex
				closes := make(map[*os.File]int)
				opts := Options{Command: fake, Prompt: "hello", processHooks: &processHooks{
					afterClose: func(f *os.File, err error) {
						mu.Lock()
						defer mu.Unlock()
						closes[f]++
						if err != nil {
							t.Errorf("owner Close(%s): %v", f.Name(), err)
						}
						if _, err := f.Read(make([]byte, 1)); !errors.Is(err, os.ErrClosed) {
							t.Errorf("owned File remains open: %v", err)
						}
					},
				}}
				if startFailure {
					opts.Command = t.TempDir() + "/missing-appserver"
				}
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				sink := &recordingSink{}
				var err error
				if persistent {
					var p *Process
					p, err = Open(ctx, opts, sink)
					if err == nil {
						defer p.TerminateAndWait(context.Background())
						if _, _, err := p.RunTurn(ctx, opts, sink); err != nil {
							t.Fatalf("RunTurn: %v", err)
						}
						for range 2 {
							if err := p.CloseGracefully(ctx, time.Second); err != nil {
								t.Fatalf("Close: %v", err)
							}
						}
						if err := p.TerminateAndWait(ctx); err != nil {
							t.Fatalf("TerminateAndWait after Close: %v", err)
						}
					}
				} else {
					_, err = Run(ctx, opts, sink)
				}
				if (err != nil) != startFailure {
					t.Fatalf("startFailure=%v: %v", startFailure, err)
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
}
