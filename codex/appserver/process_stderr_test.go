package appserver

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/v2/driver"
)

func TestResidentStderrCapturePreservesEachExecution(t *testing.T) {
	barrierDir := t.TempDir()
	opts := Options{
		Command: buildFakeAppserver(t),
		Env: []driver.EnvBinding{
			{Name: "FAKE_APPSERVER_SCENARIO", Value: "stderr-barrier"},
			{Name: "FAKE_APPSERVER_STDERR_BARRIER_DIR", Value: barrierDir},
		},
		Prompt: "capture stderr",
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	process, err := Open(ctx, opts, &recordingSink{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer closeCancel()
		if err := process.TerminateAndWait(closeCtx); err != nil {
			t.Error(err)
		}
	}()
	var previous *driver.RawStreams
	for turn := 1; turn <= 2; turn++ {
		opts.RunID = fmt.Sprintf("stderr-run-%d", turn)
		type outcome struct {
			result driver.Response
			err    error
		}
		done := make(chan outcome, 1)
		go func() {
			result, _, err := process.RunTurn(ctx, opts, &recordingSink{})
			done <- outcome{result: result, err: err}
		}()
		marker := fmt.Sprintf("stderr-turn-%d\n", turn)
		// Confirm the independent stderr reader has captured this execution's
		// marker before allowing the fixture to publish its stdout terminal.
		for !strings.Contains(process.stderr.String(), marker) {
			select {
			case <-ctx.Done():
				t.Fatalf("stderr capture barrier: %v", ctx.Err())
			case result := <-done:
				t.Fatalf("execution bypassed stderr barrier: %+v", result)
			default:
				runtime.Gosched()
			}
		}
		if err := os.WriteFile(filepath.Join(barrierDir, fmt.Sprintf("release-%d", turn)), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		select {
		case result := <-done:
			if result.err != nil || result.result.Failure != nil || result.result.RawStreams == nil {
				t.Fatalf("execution %d: %+v", turn, result)
			}
			raw := result.result.RawStreams
			if raw.Stderr != marker || raw.Terminal == nil || raw.Terminal.Event != NotifyTurnCompleted || !strings.Contains(raw.Stdout, `"method":"turn/completed"`) {
				t.Fatalf("execution %d raw=%+v, want exactly %q", turn, raw, marker)
			}
			if previous != nil && previous.Stderr != "stderr-turn-1\n" {
				t.Fatalf("later capture mutated earlier response: %+v", previous)
			}
			previous = raw
		case <-ctx.Done():
			t.Fatalf("execution %d: %v", turn, ctx.Err())
		}
	}
}
