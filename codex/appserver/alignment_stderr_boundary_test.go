package appserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/testutil"
)

const alignmentTurnStderr = "fixture-stderr" // Preserve the original 14 bytes.
const alignmentIdleStderr = "future-idle-stderr"

type alignmentAdmissionControl struct {
	conn    net.Conn
	decoder *json.Decoder
	encoder *json.Encoder
}

func (c *alignmentAdmissionControl) phase(t *testing.T, stage string, turn, n int) {
	t.Helper()
	var got struct {
		Stage string `json:"stage"`
		Turn  int    `json:"turn"`
		N     int    `json:"n"`
		Error string `json:"error"`
	}
	if err := c.decoder.Decode(&got); err != nil {
		t.Fatalf("control %s: %v", stage, err)
	}
	if got.Stage != stage || got.Turn != turn || got.N != n || got.Error != "" {
		t.Fatalf("child phase %s/%d: %+v", stage, turn, got)
	}
	t.Logf("child phase=%s turn=%d write_n=%d error=%q (not a receipt acknowledgment)", stage, turn, got.N, got.Error)
}
func (c *alignmentAdmissionControl) release(t *testing.T, command string) {
	t.Helper()
	if err := c.encoder.Encode(command); err != nil {
		t.Fatalf("release %s: %v", command, err)
	}
}

// Observe the actual buffer owned by Open and written by its production io.Copy.
// No child acknowledgment, mirror buffer, delay, or quiet period is a receipt
// oracle. A context deadline only makes a missing admission fail boundedly.
func alignmentAwaitStderr(t *testing.T, ctx context.Context, p *Process, offset int, want string) {
	t.Helper()
	for {
		got := p.stderr.Since(offset)
		if got == want {
			t.Logf("parent admitted range=[%d,%d) exact=%q", offset, offset+len(got), got)
			return
		}
		if !strings.HasPrefix(want, got) {
			t.Fatalf("stderr range at %d: got %q want %q", offset, got, want)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("stderr admission at %d: %v; got %q want %q", offset, ctx.Err(), got, want)
		default:
			runtime.Gosched()
		}
	}
}

func alignmentOpenAdmission(t *testing.T, command string) (context.Context, *Process, *alignmentAdmissionControl, Options) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	deadline, _ := ctx.Deadline()
	if err := listener.(*net.TCPListener).SetDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	opts := Options{Command: command, CWD: home, Prompt: "original prompt", Env: []driver.EnvBinding{
		{Name: "CODEX_HOME", Value: filepath.Join(home, "profile")},
		{Name: "ALIGNMENT_CAPTURE", Value: filepath.Join(home, "capture.jsonl")},
		{Name: "ALIGNMENT_SCENARIO", Value: "stderr-admission"},
		{Name: "ALIGNMENT_ADMISSION", Value: listener.Addr().String()},
	}}
	p, err := Open(ctx, opts, &testutil.EventRecorder{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := p.TerminateAndWait(cleanup); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
	conn, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.SetDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	// Release the child before process cleanup on every assertion failure.
	t.Cleanup(func() { stop(); _ = conn.Close() })
	return ctx, p, &alignmentAdmissionControl{conn: conn, decoder: json.NewDecoder(conn), encoder: json.NewEncoder(conn)}, opts
}

type alignmentAdmissionOutcome struct {
	response driver.Response
	sent     bool
	err      error
}

func alignmentAdmissionRun(ctx context.Context, p *Process, opts Options) <-chan alignmentAdmissionOutcome {
	done := make(chan alignmentAdmissionOutcome, 1)
	go func() {
		r, sent, err := p.RunTurn(ctx, opts, &testutil.EventRecorder{})
		done <- alignmentAdmissionOutcome{r, sent, err}
	}()
	return done
}
func alignmentAdmissionResult(t *testing.T, ctx context.Context, done <-chan alignmentAdmissionOutcome) alignmentAdmissionOutcome {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-ctx.Done():
		t.Fatalf("RunTurn did not return: %v", ctx.Err())
		return alignmentAdmissionOutcome{}
	}
}
func alignmentResponseSnapshot(t *testing.T, response driver.Response) string {
	t.Helper()
	data, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
func alignmentAssertAdmissionResult(t *testing.T, p *Process, got alignmentAdmissionOutcome, stdoutOffset, turn int) {
	t.Helper()
	r := got.response
	if got.err != nil || !got.sent || r.Failure != nil || r.Checkpoint == nil || !r.Checkpoint.Valid || p.IsClosed() {
		t.Fatalf("healthy resident: sent=%v err=%v closed=%v response=%s", got.sent, got.err, p.IsClosed(), alignmentResponseSnapshot(t, r))
	}
	if r.RawStreams == nil || r.RawStreams.Stderr != alignmentTurnStderr || r.RawStreams.Stdout != p.stdout.Since(stdoutOffset) {
		t.Fatalf("exact received Raw: %+v; stdout range=%q", r.RawStreams, p.stdout.Since(stdoutOffset))
	}
	if r.Output != "answer" || r.Usage == nil || !reflect.DeepEqual(*r.Usage, driver.Usage{InputTokens: 0, OutputTokens: 2}) || len(r.Transcript) == 0 {
		t.Fatalf("output/usage/transcript: %s", alignmentResponseSnapshot(t, r))
	}
	terminal := r.RawStreams.Terminal
	if terminal == nil || terminal.Event != "turn/completed" || !strings.Contains(r.RawStreams.Stdout, string(terminal.JSON)) {
		t.Fatalf("formal terminal absent from exact stdout: %+v", terminal)
	}
	var payload struct {
		ThreadID string `json:"threadId"`
		Turn     struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Usage  struct {
				Input  int `json:"inputTokens"`
				Output int `json:"outputTokens"`
			} `json:"usage"`
		} `json:"turn"`
	}
	if err := json.Unmarshal(terminal.JSON, &payload); err != nil || payload.ThreadID != p.ThreadID() || payload.Turn.ID != fmt.Sprintf("turn-%d", turn) || payload.Turn.Status != "completed" || payload.Turn.Usage.Input != 0 || payload.Turn.Usage.Output != 2 {
		t.Fatalf("terminal payload: %s err=%v", terminal.JSON, err)
	}
}

func TestAlignmentCodexStderrAdmission(t *testing.T) {
	command := filepath.Join(t.TempDir(), "fixture")
	buildCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if b, err := exec.CommandContext(buildCtx, "go", "build", "-o", command, "../testdata/alignment-provider/main.go").CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, b)
	}
	t.Run("received-and-future-idle-across-turns", func(t *testing.T) {
		ctx, p, control, opts := alignmentOpenAdmission(t, command)
		var first driver.Response
		var firstSnapshot string
		for turn := 1; turn <= 2; turn++ {
			stderrOffset, stdoutOffset := p.stderr.Len(), p.stdout.Len()
			done := alignmentAdmissionRun(ctx, p, opts)
			control.phase(t, "turn-written", turn, len(alignmentTurnStderr))
			alignmentAwaitStderr(t, ctx, p, stderrOffset, alignmentTurnStderr)
			// This send occurs only after the actual production buffer receipt above.
			control.release(t, "terminal")
			control.phase(t, "idle-held", turn, 0)
			got := alignmentAdmissionResult(t, ctx, done)
			alignmentAssertAdmissionResult(t, p, got, stdoutOffset, turn)
			snapshot := alignmentResponseSnapshot(t, got.response)
			if turn == 1 {
				first, firstSnapshot = got.response, snapshot
			}
			if alignmentResponseSnapshot(t, first) != firstSnapshot {
				t.Fatal("next turn changed first response")
			}
			// Negative control: the real child is held before writing these future
			// bytes. They cannot be observed or appear in the already published turn.
			if strings.Contains(got.response.RawStreams.Stderr, alignmentIdleStderr) {
				t.Fatal("future stderr inferred before write")
			}
			control.release(t, "idle")
			control.phase(t, "idle-written", turn, len(alignmentIdleStderr))
			alignmentAwaitStderr(t, ctx, p, stderrOffset, alignmentTurnStderr+alignmentIdleStderr)
			if alignmentResponseSnapshot(t, got.response) != snapshot || alignmentResponseSnapshot(t, first) != firstSnapshot {
				t.Fatal("later admitted idle stderr amended published response")
			}
			if p.IsClosed() {
				t.Fatal("healthy receipt depended on process exit")
			}
			control.release(t, "next")
		}
	})
	t.Run("cancel-held-terminal", func(t *testing.T) {
		ctx, p, control, opts := alignmentOpenAdmission(t, command)
		turnCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		offset := p.stderr.Len()
		done := alignmentAdmissionRun(turnCtx, p, opts)
		control.phase(t, "turn-written", 1, len(alignmentTurnStderr))
		alignmentAwaitStderr(t, ctx, p, offset, alignmentTurnStderr)
		cancel() // Production cancellation must stop the held child without a terminal.
		got := alignmentAdmissionResult(t, ctx, done)
		if !errors.Is(got.err, context.Canceled) || !got.sent || got.response.Checkpoint != nil || got.response.RawStreams == nil || got.response.RawStreams.Stderr != alignmentTurnStderr || got.response.RawStreams.Terminal != nil {
			t.Fatalf("cancelled gate: sent=%v err=%v response=%s", got.sent, got.err, alignmentResponseSnapshot(t, got.response))
		}
	})
}
