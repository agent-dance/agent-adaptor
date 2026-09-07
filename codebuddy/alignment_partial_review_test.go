package codebuddy

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/memory"
	"github.com/agent-dance/agent-adaptor/threadstore"
)

type alignmentPartialWriter struct {
	lp       *liveProcess
	cause    error
	upstream io.WriteCloser
}

func (w alignmentPartialWriter) Write(b []byte) (int, error) {
	_, _ = (persistentStderr{lp: w.lp}).Write([]byte("observed diagnostic without newline"))
	if w.upstream != nil {
		n, err := w.upstream.Write(b[:1])
		return n, errors.Join(err, w.cause)
	}
	return 1, w.cause
}
func (w alignmentPartialWriter) Close() error {
	if w.upstream != nil {
		return w.upstream.Close()
	}
	return nil
}

func TestAlignmentCodeBuddyPartialShortWriteRetainsObservedDiagnostic(t *testing.T) {
	cause := errors.New("review partial stdin write")
	lp := &liveProcess{initialized: true, stderr: &lockedBuffer{}, stdout: bufio.NewReader(strings.NewReader("buffered stdout diagnostic\n"))}
	lp.stdin = alignmentPartialWriter{lp: lp, cause: cause}
	p := newParser(nil)
	p.control = &controlState{}
	p.enableOutputReconstruction("review")
	raw, sent, err := lp.turn(context.Background(), "prompt", nil, p)
	if !sent || !errors.Is(err, cause) {
		t.Fatalf("sent=%v err=%v", sent, err)
	}
	resp := buildPersistentCodeBuddyResponse(driver.Request{}, p, raw, runPrep{}, err)
	t.Logf("raw=%#v transcript=%#v pendingStderr=%q", raw, resp.Transcript, p.stderrLine.String())
	found := false
	for _, item := range resp.Transcript {
		if item.Kind == driver.TranscriptStderr && item.Text == "observed diagnostic without newline" {
			found = true
		}
	}
	if !found {
		t.Error("already observed stderr missing from partial Transcript")
	}
	if raw.Stdout != "buffered stdout diagnostic\n" {
		t.Error("failed turn did not drain buffered stdout")
	}
}

func TestAlignmentCodeBuddyPartialMultipleMessageUsage(t *testing.T) {
	p := newParser(nil)
	p.enableOutputReconstruction("review")
	var raw strings.Builder
	for i := 1; i <= 2; i++ {
		fmt.Fprintf(&raw, "{\"type\":\"stream_event\",\"event\":{\"type\":\"message_start\",\"message\":{\"id\":\"msg_%d\",\"usage\":{\"input_tokens\":%d,\"cache_read_input_tokens\":%d}}}}\n", i, 10*i, 2*i)
		fmt.Fprintf(&raw, "{\"type\":\"stream_event\",\"event\":{\"type\":\"message_delta\",\"usage\":{\"output_tokens\":%d}}}\n", 5*i)
		raw.WriteString("{\"type\":\"stream_event\",\"event\":{\"type\":\"message_stop\"}}\n")
	}
	if err := p.onChunk("stdout", []byte(raw.String()), time.Now()); err != nil {
		t.Fatal(err)
	}
	p.finalize()
	resp := buildPersistentCodeBuddyResponse(driver.Request{}, p, driver.RawStreams{Stdout: raw.String()}, runPrep{}, io.EOF)
	t.Logf("partial run Usage=%+v", resp.Usage)
	if resp.Usage == nil || resp.Usage.InputTokens != 30 || resp.Usage.CachedInputTokens != 6 || resp.Usage.OutputTokens != 15 {
		t.Error("two distinct observed message usages were reduced to per-field maxima")
	}
}

func TestAlignmentCodeBuddyPartialShortWriteWaitsWithoutReplay(t *testing.T) {
	store := memory.NewStore()
	fx := newPersistentCodeBuddyFixtureWithOptions(t, nil, adaptor.WithThreadStore(store))
	defer fx.close()
	fx.run(t, "warm")
	query := threadstore.Query{Key: "persistent-test/thread"}
	before, _ := store.Resolve(context.Background(), query)
	lp := fx.pool.lookup("codebuddy-persistent-session")
	if lp == nil || before == nil {
		t.Fatal("missing warmed process or healthy record")
	}
	cause := errors.New("injected partial prompt write")
	lp.stdin = alignmentPartialWriter{lp: lp, cause: cause, upstream: lp.stdin}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := fx.thread.Run(ctx, "partially delivered")
	var runErr *adaptor.RunError
	if result != nil || !errors.As(err, &runErr) || !errors.Is(err, cause) || runErr.Reason != adaptor.ReasonInfrastructure {
		t.Fatalf("partial write outcome = %#v, %v", result, err)
	}
	select {
	case <-lp.waitCh:
	default:
		t.Fatal("partial write returned before waiting for the failed process")
	}
	if lp.waitErr != nil && !errors.Is(err, lp.waitErr) {
		t.Fatalf("observed shutdown error not retained: wait=%v returned=%v", lp.waitErr, err)
	}
	if got := runErr.Result.Raw().Stderr; got != "observed diagnostic without newline" {
		t.Errorf("already observed stderr = %q", got)
	}
	after, _ := store.Resolve(context.Background(), query)
	if !reflect.DeepEqual(before, after) || fx.spawnCount(t) != 1 || fx.pool.lookup("codebuddy-persistent-session") != nil {
		t.Fatal("partial write changed checkpoint, replayed the prompt, or retained the failed writer")
	}
}

func TestAlignmentCodeBuddyPartialUsageSnapshotsDeduplicate(t *testing.T) {
	for _, tc := range []struct {
		name     string
		protocol string
		want     *driver.Usage
	}{
		{
			name: "cumulative snapshots and same ID replay",
			protocol: `{"type":"stream_event","event":{"type":"message_start","message":{"id":"one","usage":{"input_tokens":10,"cache_read_input_tokens":2}}}}
{"type":"stream_event","event":{"type":"message_delta","usage":{"output_tokens":3}}}
{"type":"stream_event","event":{"type":"message_delta","usage":{"output_tokens":5}}}
{"type":"stream_event","event":{"type":"message_delta","usage":{"output_tokens":5}}}
{"type":"stream_event","event":{"type":"message_stop"}}
{"type":"stream_event","event":{"type":"message_start","message":{"id":"two","usage":{"input_tokens":20,"cache_read_input_tokens":4}}}}
{"type":"stream_event","event":{"type":"message_delta","usage":{"output_tokens":10}}}
{"type":"stream_event","event":{"type":"message_stop"}}
{"type":"stream_event","event":{"type":"message_start","message":{"id":"one","usage":{"input_tokens":10,"cache_read_input_tokens":2}}}}
{"type":"stream_event","event":{"type":"message_delta","usage":{"output_tokens":5}}}
`,
			want: &driver.Usage{InputTokens: 30, OutputTokens: 15, CachedInputTokens: 6},
		},
		{
			name:     "observed zero",
			protocol: `{"type":"stream_event","event":{"type":"message_start","message":{"id":"zero","usage":{"input_tokens":0,"output_tokens":0,"cache_read_input_tokens":0}}}}`,
			want:     &driver.Usage{},
		},
		{
			name: "invalid counters and unknown message have no inferred usage",
			protocol: `{"type":"stream_event","event":{"type":"message_start","message":{"id":"invalid","usage":{"input_tokens":-1,"output_tokens":1.5,"cache_read_input_tokens":"2"}}}}
{"type":"stream_event","event":{"type":"message_stop"}}
{"type":"stream_event","event":{"type":"message_delta","usage":{"output_tokens":9}}}
{"type":"stream_event","event":{"type":"message_start","message":{"usage":{"input_tokens":99}}}}
`,
		},
		{
			name: "terminal zero overrides partial aggregates",
			protocol: `{"type":"stream_event","event":{"type":"message_start","message":{"id":"one","usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":2}}}}
{"type":"result","subtype":"success","is_error":false,"session_id":"s","result":"","usage":{"input_tokens":0,"output_tokens":0,"cache_read_input_tokens":0}}
`,
			want: &driver.Usage{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newParser(nil)
			p.enableOutputReconstruction("usage-snapshots")
			_ = p.onChunk("stdout", []byte(tc.protocol), time.Now())
			p.finalize()
			resp := buildPersistentCodeBuddyResponse(driver.Request{}, p, driver.RawStreams{Stdout: tc.protocol}, runPrep{}, io.EOF)
			if !reflect.DeepEqual(resp.Usage, tc.want) {
				t.Fatalf("usage=%#v want=%#v", resp.Usage, tc.want)
			}
			if resp.Checkpoint != nil {
				t.Fatal("partial usage made an interrupted checkpoint valid")
			}
		})
	}
}

func TestAlignmentCodeBuddyPartialObservedProcessCause(t *testing.T) {
	fx := newPersistentCodeBuddyFixture(t, nil)
	defer fx.close()
	var observed *liveProcess
	fx.pool.spawnProcess = func(spec persistentSpec, sink driver.EventSink) (*liveProcess, error) {
		lp, err := fx.pool.spawn(spec, sink)
		observed = lp
		return lp, err
	}
	_, err := fx.thread.Run(context.Background(), "alignment-partial:nonzero")
	var re *adaptor.RunError
	if !errors.As(err, &re) || observed == nil {
		t.Fatalf("err=%v observed=%v", err, observed)
	}
	<-observed.waitCh
	var original *exec.ExitError
	if !errors.As(observed.waitErr, &original) || original.ExitCode() != 23 {
		t.Fatalf("fixture waitErr=%v", observed.waitErr)
	}
	var wrapped *exec.ExitError
	t.Logf("observed wait cause=%T %v; returned reason=%s cause=%v", observed.waitErr, observed.waitErr, re.Reason, re.Cause)
	if !errors.As(err, &wrapped) {
		t.Error("actual observed exec.ExitError was discarded")
	}
	if wrapped != original {
		t.Error("returned process cause is not the original observed ExitError")
	}
	if re.Reason != adaptor.ReasonAgentError {
		t.Error("observed nonzero provider exit was classified as infrastructure")
	}
	for _, cause := range []error{nil, context.Canceled, context.DeadlineExceeded} {
		p := newParser(nil)
		resp := buildPersistentCodeBuddyResponse(driver.Request{}, p, driver.RawStreams{}, runPrep{}, errors.Join(io.EOF, original, cause))
		if resp.ExitCode != 23 || resp.Checkpoint != nil {
			t.Fatalf("observed exit/checkpoint = %d/%#v", resp.ExitCode, resp.Checkpoint)
		}
		if (resp.Failure != nil) != (cause == nil) {
			t.Errorf("process failure classification overrode context cause %v: %#v", cause, resp.Failure)
		}
		p.pendingFailure = &driver.RunFailure{Code: driver.FailureReject, Message: "approval rejected", HumanDecision: &driver.HumanDecisionFailure{}}
		resp = buildPersistentCodeBuddyResponse(driver.Request{}, p, driver.RawStreams{}, runPrep{}, errors.Join(original, cause))
		if resp.Failure != p.pendingFailure {
			t.Error("observed process error overrode the parser's approval failure")
		}
	}
}
