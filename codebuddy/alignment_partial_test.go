package codebuddy

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/memory"
	"github.com/agent-dance/agent-adaptor/threadstore"
)

const alignmentCodeBuddyPartialText = "已观察的部分回答"
const alignmentCodeBuddyPartialStderr = "fixture diagnostic without newline"

// The helper runs the real control handshake and official partial-message
// protocol. Only its final transport outcome varies; no real CLI is invoked.
func emitAlignmentCodeBuddyPartialTurn(writer *bufio.Writer, userFrame string) (bool, int) {
	if !strings.Contains(userFrame, "alignment-partial:") {
		return false, 0
	}
	mode := ""
	for _, candidate := range []string{"cancel", "deadline", "nonzero", "missing_terminal", "malformed_terminal", "malformed_json", "provider_error", "no_assistant"} {
		if strings.Contains(userFrame, "alignment-partial:"+candidate) {
			mode = candidate
			break
		}
	}
	_, _ = fmt.Fprint(os.Stderr, alignmentCodeBuddyPartialStderr)
	_, _ = fmt.Fprint(writer, alignmentCodeBuddyPartialProtocol(mode))
	_ = writer.Flush()
	if mode == "cancel" || mode == "deadline" {
		_ = os.WriteFile(os.Getenv("ALIGNMENT_CODEBUDDY_READY"), []byte("written"), 0o600)
		time.Sleep(30 * time.Second)
	}
	if mode == "nonzero" {
		return true, 23
	}
	return true, 0
}

func alignmentCodeBuddyPartialProtocol(mode string) string {
	writer := &strings.Builder{}
	_, _ = fmt.Fprintln(writer, `{"type":"system","subtype":"init","session_id":"unhealthy-partial-session","model":"fake"}`)
	if mode != "no_assistant" {
		_, _ = fmt.Fprintln(writer, `{"type":"stream_event","event":{"type":"message_start","message":{"id":"partial-message","usage":{"input_tokens":10,"cache_read_input_tokens":2}}}}`)
		_, _ = fmt.Fprintln(writer, `{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"text"}}}`)
		_, _ = fmt.Fprintf(writer, "{\"type\":\"stream_event\",\"event\":{\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":%q}}}\n", alignmentCodeBuddyPartialText)
		_, _ = fmt.Fprintln(writer, `{"type":"stream_event","event":{"type":"content_block_stop","index":0}}`)
		_, _ = fmt.Fprintln(writer, `{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}}`)
	}
	switch mode {
	case "malformed_terminal":
		_, _ = fmt.Fprintln(writer, `{"type":"result","subtype":"success","is_error":false,"session_id":"unhealthy-partial-session"}`)
		_, _ = fmt.Fprintln(writer, "final stdout diagnostic")
	case "malformed_json":
		_, _ = fmt.Fprintln(writer, `{"type":"result",broken`)
	case "provider_error":
		_, _ = fmt.Fprintln(writer, `{"type":"error","message":"official fixture failure"}`)
	case "no_assistant":
		_, _ = fmt.Fprintln(writer, `{"arbitrary":{"text":"must not become assistant output","session_id":"not-a-checkpoint"}}`)
	}
	return writer.String()
}

func TestAlignmentCodeBuddyPartialThreadResults(t *testing.T) {
	for _, mode := range []string{"cancel", "deadline", "nonzero", "missing_terminal", "malformed_terminal", "malformed_json", "provider_error", "no_assistant"} {
		t.Run(mode, func(t *testing.T) {
			var runSnapshot *alignmentCodeBuddyResultSnapshot
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
					store := memory.NewStore()
					ready := filepath.Join(t.TempDir(), "ready")
					fx := newPersistentCodeBuddyFixtureWithOptions(t, []driver.EnvBinding{{Name: "ALIGNMENT_CODEBUDDY_READY", Value: ready}}, adaptor.WithThreadStore(store))
					defer fx.close()
					fx.run(t, "healthy")
					query := threadstore.Query{Key: "persistent-test/thread"}
					before, err := store.Resolve(context.Background(), query)
					if err != nil || before == nil {
						t.Fatalf("healthy record = %#v, %v", before, err)
					}
					result, err := runAlignmentCodeBuddyPartial(t, fx.thread, mode, stream, ready)
					partial := assertAlignmentCodeBuddyPartial(t, result, err, mode)
					after, resolveErr := store.Resolve(context.Background(), query)
					if resolveErr != nil || !reflect.DeepEqual(before, after) {
						t.Errorf("failed turn changed healthy record: before=%#v after=%#v err=%v", before, after, resolveErr)
					}
					if got := fx.spawnCount(t); got != 1 {
						t.Errorf("delivered prompt replayed: spawns=%d", got)
					}
					if fx.pool.lookup("codebuddy-persistent-session") != nil || fx.pool.lookup("unhealthy-partial-session") != nil {
						t.Error("failed turn retained a persistent writer")
					}
					if runSnapshot == nil {
						runSnapshot = partial
					} else if !reflect.DeepEqual(runSnapshot, partial) {
						t.Errorf("Run and Stream.Result differ: Run=%#v Stream=%#v", runSnapshot, partial)
					}
					fx.run(t, "healthy resume")
					if got := fx.spawnCount(t); got != 2 || fx.overlapCount(t) != 0 {
						t.Errorf("replacement writer: spawns=%d overlap=%d", got, fx.overlapCount(t))
					}
				})
			}
		})
	}
}

func TestAlignmentCodeBuddyPartialFirstCancellationDoesNotCheckpoint(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			store := memory.NewStore()
			ready := filepath.Join(t.TempDir(), "ready")
			fx := newPersistentCodeBuddyFixtureWithOptions(t, []driver.EnvBinding{{Name: "ALIGNMENT_CODEBUDDY_READY", Value: ready}}, adaptor.WithThreadStore(store))
			defer fx.close()
			result, err := runAlignmentCodeBuddyPartial(t, fx.thread, "cancel", stream, ready)
			assertAlignmentCodeBuddyPartial(t, result, err, "cancel")
			record, resolveErr := store.Resolve(context.Background(), threadstore.Query{Key: "persistent-test/thread"})
			if resolveErr != nil || record != nil {
				t.Fatalf("first cancellation persisted a record: %#v, %v", record, resolveErr)
			}
		})
	}
}

func TestAlignmentCodeBuddyPartialResponseNeverCertifiesFailedTurn(t *testing.T) {
	longFinal := strings.Repeat("完整终局文本", 1000)
	for _, cause := range []error{errors.New("original transport error"), context.Canceled, context.DeadlineExceeded} {
		t.Run(cause.Error(), func(t *testing.T) {
			p := newParser(nil)
			p.enableOutputReconstruction("partial-response")
			terminal := fmt.Sprintf(`{"type":"result","subtype":"success","is_error":false,"session_id":"session","result":%q,"usage":{"input_tokens":0,"output_tokens":0}}`, longFinal)
			stdout := "{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"intermediate\"}]}}\n" + terminal + "\n"
			_ = p.onChunk("stdout", []byte(stdout), timeNow())
			p.finalize()
			if p.checkpoint(0) == nil {
				t.Fatal("fixture lacks an otherwise healthy terminal checkpoint")
			}
			resp := buildPersistentCodeBuddyResponse(driver.Request{}, p, driver.RawStreams{Stdout: stdout}, runPrep{}, cause)
			if resp.Checkpoint != nil || resp.ExitCode == 0 || resp.TimedOut != errors.Is(cause, context.DeadlineExceeded) {
				t.Fatalf("failed control turn certified healthy outcome: %#v", resp)
			}
			if resp.Output != longFinal || resp.Summary != "" || resp.RawStreams.Stdout != stdout || string(resp.RawStreams.Terminal.JSON) != terminal {
				t.Fatal("authoritative final text, bounded Summary, or terminal/raw contract changed")
			}
			if resp.Usage == nil || *resp.Usage != (driver.Usage{}) {
				t.Fatalf("observed zero terminal usage = %#v", resp.Usage)
			}
		})
	}
}

func runAlignmentCodeBuddyPartial(t *testing.T, runner adaptor.Runner, mode string, stream bool, ready string) (*adaptor.Result, error) {
	t.Helper()
	limit := 10 * time.Second
	if mode == "deadline" {
		limit = 2 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	if mode == "cancel" {
		done := make(chan struct{})
		defer func() { cancel(); <-done }()
		go func() {
			defer close(done)
			ticker := time.NewTicker(time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if _, err := os.Stat(ready); err == nil {
						cancel()
						return
					}
				}
			}
		}()
	}
	prompt := "alignment-partial:" + mode
	if !stream {
		return runner.Run(ctx, prompt)
	}
	s := runner.Stream(ctx, prompt)
	for range s.Events() {
	}
	result, err := s.Result()
	again, againErr := s.Result()
	if result != again || err != againErr {
		t.Error("repeated Result returned different outcome")
	}
	return result, err
}

type alignmentCodeBuddyResultSnapshot struct {
	Text, Summary, Model, Provider string
	Raw                            adaptor.RawStreams
	Transcript                     []adaptor.TranscriptItem
	Usage                          *adaptor.Usage
	Metadata                       map[string]string
	Services                       []adaptor.ServiceReport
}

func assertAlignmentCodeBuddyPartial(t *testing.T, result *adaptor.Result, err error, mode string) *alignmentCodeBuddyResultSnapshot {
	t.Helper()
	var runErr *adaptor.RunError
	if result != nil || !errors.As(err, &runErr) || runErr.Result == nil {
		t.Fatalf("outcome = %#v, %v; want nil, RunError with partial Result", result, err)
	}
	partial := runErr.Result
	wantText := alignmentCodeBuddyPartialText
	if mode == "no_assistant" {
		wantText = ""
	}
	if partial.Text != wantText || partial.Summary != "" || partial.Provider != "codebuddy" {
		t.Errorf("partial Text/Summary/Provider = %q/%q/%q", partial.Text, partial.Summary, partial.Provider)
	}
	raw := partial.Raw()
	stdout := strings.TrimPrefix(raw.Stdout, "{\"type\":\"control_response\",\"response\":{\"subtype\":\"success\",\"request_id\":\"agent-adaptor-initialize\",\"response\":{}}}\n")
	if stdout != alignmentCodeBuddyPartialProtocol(mode) || raw.Stderr != alignmentCodeBuddyPartialStderr {
		t.Errorf("raw output missing: %#v", raw)
	}
	if mode == "provider_error" || mode == "malformed_terminal" {
		if raw.Terminal == nil || !json.Valid(raw.Terminal.JSON) || !strings.Contains(raw.Stdout, string(raw.Terminal.JSON)) {
			t.Errorf("official terminal missing: %#v", raw.Terminal)
		}
	} else if raw.Terminal != nil {
		t.Errorf("invented terminal: %#v", raw.Terminal)
	}
	if mode != "no_assistant" && !reflect.DeepEqual(partial.Usage, &adaptor.Usage{InputTokens: 10, CachedInputTokens: 2, OutputTokens: 5}) {
		t.Errorf("observed partial usage lost: %#v", partial.Usage)
	}
	var assistant, stderr int
	for _, item := range partial.Transcript() {
		if item.Kind == driver.TranscriptAssistant && item.Text == wantText {
			assistant++
		}
		if item.Kind == driver.TranscriptStderr && item.Text == alignmentCodeBuddyPartialStderr {
			stderr++
		}
	}
	if (wantText != "" && assistant != 1) || stderr != 1 {
		t.Errorf("partial transcript lost/duplicated: %#v", partial.Transcript())
	}
	switch mode {
	case "cancel":
		if runErr.Reason != adaptor.ReasonCancelled || !errors.Is(err, context.Canceled) {
			t.Errorf("cancel cause changed: %#v", runErr)
		}
	case "deadline":
		if runErr.Reason != adaptor.ReasonDeadlineExceeded || !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("deadline cause changed: %#v", runErr)
		}
	case "malformed_terminal":
		if runErr.Reason != adaptor.ReasonAgentError {
			t.Errorf("malformed terminal reason = %q", runErr.Reason)
		}
	case "nonzero":
		var exitErr *exec.ExitError
		if runErr.Reason != adaptor.ReasonAgentError || !errors.As(err, &exitErr) || exitErr.ExitCode() != 23 || !errors.Is(err, io.EOF) {
			t.Errorf("observed nonzero process outcome/cause lost: reason=%q err=%v", runErr.Reason, err)
		}
	default:
		if mode == "provider_error" && runErr.Reason != adaptor.ReasonAgentError {
			t.Errorf("official provider failure reason = %q", runErr.Reason)
		}
		if !errors.Is(err, io.EOF) {
			t.Errorf("original disconnect cause lost: %v", err)
		}
	}
	return &alignmentCodeBuddyResultSnapshot{partial.Text, partial.Summary, partial.Model, partial.Provider, raw, partial.Transcript(), partial.Usage, partial.Metadata, partial.Services()}
}
