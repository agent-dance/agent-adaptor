package codebuddy

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/memory"
	"github.com/agent-dance/agent-adaptor/threadstore"
)

const handoffHelperEnv = "GO_WANT_CODEBUDDY_HANDOFF_HELPER"

func init() {
	if os.Getenv(handoffHelperEnv) == "1" {
		os.Exit(runHandoffHelper())
	}
}

// This is a real resident subprocess. Its only waiting operation is reading
// the control pipe; cancellation/Close terminates it through the real Driver.
func runHandoffHelper() int {
	recordCodeBuddyPersistentHelperSpawn(os.Getenv("SPAWN_FILE"), os.Getenv("PID_FILE"), os.Getenv("OVERLAP_FILE"))
	scanner := bufio.NewScanner(os.Stdin)
	emit := func(v any) { b, _ := json.Marshal(v); fmt.Println(string(b)) }
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "agent-adaptor-initialize") {
			fmt.Println(`{"type":"control_response","response":{"subtype":"success","request_id":"agent-adaptor-initialize","response":{}}}`)
			continue
		}
		var frame struct {
			Type    string
			Message struct{ Content string }
		}
		if json.Unmarshal([]byte(line), &frame) != nil || frame.Type != "user" {
			return 79
		}
		prompt := frame.Message.Content
		appendCodeBuddyPersistentHelperLine(os.Getenv("HANDOFF_PROMPTS"), prompt)
		emit(map[string]any{"type": "system", "subtype": "init", "session_id": "codebuddy-persistent-session", "model": "handoff-model"})
		emit(map[string]any{"type": "stream_event", "event": map[string]any{"type": "message_start", "message": map[string]any{"id": prompt, "usage": map[string]any{"input_tokens": 7, "output_tokens": 3, "cache_read_input_tokens": 2}}}})
		emit(map[string]any{"type": "assistant", "session_id": "codebuddy-persistent-session", "message": map[string]any{"content": []any{map[string]any{"type": "text", "text": "answer:" + prompt}}}})
		fmt.Fprint(os.Stderr, "stderr:"+prompt)
		if os.Getenv("HANDOFF_NEWLINE") == "1" {
			fmt.Fprint(os.Stderr, "\n")
		}
		if prompt == "cancel" {
			continue
		}
		if prompt == "fail" {
			return 23
		}
		emit(map[string]any{"type": "result", "subtype": "success", "is_error": false, "session_id": "codebuddy-persistent-session", "result": "answer:" + prompt, "usage": map[string]any{"input_tokens": 7, "output_tokens": 3, "cache_read_input_tokens": 2}})
	}
	return 0
}

type handoffGate struct {
	entered, release, terminal           chan struct{}
	enterOnce, releaseOnce, terminalOnce sync.Once
}

func newHandoffGate(hold bool) *handoffGate {
	g := &handoffGate{entered: make(chan struct{}), release: make(chan struct{}), terminal: make(chan struct{})}
	if !hold {
		g.unblock()
	}
	return g
}
func (g *handoffGate) unblock() { g.releaseOnce.Do(func() { close(g.release) }) }

type handoffSink struct {
	driver.DecisionCapableSink
	ctx  context.Context
	gate *handoffGate
}

func (s handoffSink) Emit(event driver.RunEvent) error {
	if event.Type == driver.RunEventChunk && event.Stream == "stderr" {
		s.gate.enterOnce.Do(func() {
			close(s.gate.entered)
			select {
			case <-s.gate.release:
			case <-s.ctx.Done():
			}
		})
	}
	if event.Type == driver.RunEventChunk && event.Stream == "stdout" && isResultLine(string(event.Bytes)) {
		// The terminal cannot overtake admission of the stderr callback. The
		// callback is held before parser.onChunk, exactly as in the Linux race.
		select {
		case <-s.gate.entered:
		case <-s.ctx.Done():
		}
		s.gate.terminalOnce.Do(func() { close(s.gate.terminal) })
	}
	return s.DecisionCapableSink.Emit(event)
}

type handoffDriver struct {
	configuredDriver
	gates map[string]*handoffGate // immutable after construction
}

func (d handoffDriver) Run(ctx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
	return d.configuredDriver.Run(ctx, req, handoffSink{sink.(driver.DecisionCapableSink), ctx, d.gates[req.Prompt]})
}

type handoffFixture struct {
	*persistentCodeBuddyFixture
	store   *memory.Store
	prompts string
}

func newHandoffFixture(t *testing.T, newline bool, gates map[string]*handoffGate) *handoffFixture {
	t.Helper()
	root := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(root, "profile")
	if err := os.MkdirAll(profile, 0700); err != nil {
		t.Fatal(err)
	}
	fx := &persistentCodeBuddyFixture{pool: newPersistentPool(), spawnFile: filepath.Join(root, "spawns"), pidFile: filepath.Join(root, "pid"), overlap: filepath.Join(root, "overlap"), profileDir: profile}
	prompts := filepath.Join(root, "prompts")
	env := []driver.EnvBinding{{Name: handoffHelperEnv, Value: "1"}, {Name: "HOME", Value: root}, {Name: "CODEBUDDY_CONFIG_DIR", Value: profile}, {Name: "SPAWN_FILE", Value: fx.spawnFile}, {Name: "PID_FILE", Value: fx.pidFile}, {Name: "OVERLAP_FILE", Value: fx.overlap}, {Name: "HANDOFF_PROMPTS", Value: prompts}}
	if newline {
		env = append(env, driver.EnvBinding{Name: "HANDOFF_NEWLINE", Value: "1"})
	}
	store := memory.NewStore()
	d := handoffDriver{configuredDriver{adapter: adapter{persistent: fx.pool}, cfg: Config{CommonConfig: CommonConfig{Command: exe, CWD: root, Env: env, GracePeriod: 50 * time.Millisecond}}}, gates}
	fx.agent = adaptor.New(d, adaptor.WithThreadStore(store))
	fx.thread = fx.agent.Thread("handoff")
	t.Cleanup(fx.close)
	return &handoffFixture{fx, store, prompts}
}

type handoffOutcome struct {
	result *adaptor.Result
	err    error
}

func handoffRun(ctx context.Context, th *adaptor.Thread, prompt string, stream bool) <-chan handoffOutcome {
	done := make(chan handoffOutcome, 1)
	go func() {
		if !stream {
			r, e := th.Run(ctx, prompt)
			done <- handoffOutcome{r, e}
			return
		}
		s := th.Stream(ctx, prompt)
		for range s.Events() {
		}
		r, e := s.Result()
		again, againErr := s.Result()
		if again != r || againErr != e {
			done <- handoffOutcome{nil, errors.New("repeated Result changed")}
			return
		}
		done <- handoffOutcome{r, e}
	}()
	return done
}
func handoffAwait(t *testing.T, ctx context.Context, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-ctx.Done():
		t.Fatal("handoff barrier timed out", ctx.Err())
	}
}
func handoffResult(t *testing.T, ctx context.Context, done <-chan handoffOutcome) handoffOutcome {
	t.Helper()
	select {
	case out := <-done:
		return out
	case <-ctx.Done():
		t.Fatal("handoff completion timed out", ctx.Err())
		return handoffOutcome{}
	}
}
func handoffSnapshot(t *testing.T, r *adaptor.Result) string {
	t.Helper()
	b, e := json.Marshal(alignmentCodeBuddyResultSnapshot{r.Text, r.Summary, r.Model, r.Provider, r.Raw(), r.Transcript(), r.Usage, r.Metadata, r.Services()})
	if e != nil {
		t.Fatal(e)
	}
	return string(b)
}
func handoffAudit(t *testing.T, r *adaptor.Result, prompt string, newline, terminal bool) {
	t.Helper()
	want := "stderr:" + prompt
	if newline {
		want += "\n"
	}
	if r.Text != "answer:"+prompt || r.Summary != "" || r.Provider != "codebuddy" || r.Raw().Stderr != want {
		t.Fatalf("lost output audit: text=%q summary=%q raw=%+v", r.Text, r.Summary, r.Raw())
	}
	if r.Usage == nil || r.Usage.InputTokens != 7 || r.Usage.OutputTokens != 3 || r.Usage.CachedInputTokens != 2 {
		t.Fatalf("usage=%+v", r.Usage)
	}
	found := false
	for _, item := range r.Transcript() {
		if item.Kind == "stderr" && item.Text == "stderr:"+prompt {
			found = true
		}
	}
	if !found {
		t.Errorf("admitted stderr was not finalized into Transcript: %+v", r.Transcript())
	}
	if (r.Raw().Terminal != nil) != terminal {
		t.Errorf("terminal=%+v", r.Raw().Terminal)
	}
	if terminal && !strings.Contains(r.Raw().Stdout, string(r.Raw().Terminal.JSON)) {
		t.Error("terminal not from captured stdout")
	}
}

func TestAlignmentCodeBuddyPersistentHandoffHealthy(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, newline := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%v/newline=%v", stream, newline), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				gate := newHandoffGate(true)
				defer gate.unblock()
				fx := newHandoffFixture(t, newline, map[string]*handoffGate{"first": gate, "second": newHandoffGate(false)})
				done := handoffRun(ctx, fx.thread, "first", stream)
				handoffAwait(t, ctx, gate.terminal)
				// The real child has delivered its terminal after callback admission.
				// This bounded negative assertion checks completion, not a probabilistic
				// overlap produced by sleeping or by a stream of uncoordinated writes.
				var early *handoffOutcome
				timer := time.NewTimer(100 * time.Millisecond)
				select {
				case out := <-done:
					early = &out
					t.Error("Result returned while admitted stderr callback was blocked")
				case <-timer.C:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				timer.Stop()
				// Admission bookkeeping must not hold the process lock around host code.
				lockAvailable := make(chan struct{})
				go func() {
					lp := fx.pool.lookup("codebuddy-persistent-session")
					if lp == nil {
						fx.pool.mu.Lock()
						for p := range fx.pool.all {
							lp = p
							break
						}
						fx.pool.mu.Unlock()
					}
					if lp != nil {
						lp.activeMu.Lock()
						lp.activeMu.Unlock()
					}
					close(lockAvailable)
				}()
				handoffAwait(t, ctx, lockAvailable)
				gate.unblock()
				var out handoffOutcome
				if early != nil {
					out = *early
				} else {
					out = handoffResult(t, ctx, done)
				}
				if out.err != nil {
					t.Fatal(out.err)
				}
				handoffAudit(t, out.result, "first", newline, true)
				before := handoffSnapshot(t, out.result)
				resident := fx.pool.lookup("codebuddy-persistent-session")
				if resident == nil {
					t.Fatal("healthy writer was stopped")
				}
				if _, err := (persistentStderr{lp: resident}).Write([]byte("unattributed idle diagnostic\n")); err != nil {
					t.Fatal(err)
				}
				if before != handoffSnapshot(t, out.result) {
					t.Error("idle stderr changed completed result")
				}
				next := handoffResult(t, ctx, handoffRun(ctx, fx.thread, "second", stream))
				if next.err != nil {
					t.Fatal(next.err)
				}
				handoffAudit(t, next.result, "second", newline, true)
				if before != handoffSnapshot(t, out.result) {
					t.Error("returned audit changed after next healthy turn")
				}
				if fx.spawnCount(t) != 1 || fx.overlapCount(t) != 0 {
					t.Error("healthy handoff broke resident single writer")
				}
				raw, e := os.ReadFile(fx.prompts)
				if e != nil || string(raw) != "first\nsecond\n" {
					t.Fatalf("prompt replay: %q %v", raw, e)
				}
			})
		}
	}
}

func TestAlignmentCodeBuddyPersistentHandoffFailure(t *testing.T) {
	for _, mode := range []string{"cancel", "fail"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", mode, stream), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				gate := newHandoffGate(true)
				defer gate.unblock()
				fx := newHandoffFixture(t, false, map[string]*handoffGate{"healthy": newHandoffGate(false), mode: gate, "recovered": newHandoffGate(false)})
				healthy := handoffResult(t, ctx, handoffRun(ctx, fx.thread, "healthy", stream))
				if healthy.err != nil {
					t.Fatal(healthy.err)
				}
				snapshot := handoffSnapshot(t, healthy.result)
				record, e := fx.store.Resolve(ctx, threadstore.Query{Key: "handoff"})
				if e != nil {
					t.Fatal(e)
				}
				before, _ := json.Marshal(record)
				runctx, stop := context.WithCancel(ctx)
				defer stop()
				done := handoffRun(runctx, fx.thread, mode, stream)
				handoffAwait(t, ctx, gate.entered)
				if mode == "cancel" {
					stop()
				} else {
					gate.unblock()
				}
				out := handoffResult(t, ctx, done)
				var re *adaptor.RunError
				if out.result != nil || !errors.As(out.err, &re) || re.Result == nil {
					t.Fatalf("failure=%+v", out)
				}
				if mode == "cancel" && (!errors.Is(out.err, context.Canceled) || re.Reason != adaptor.ReasonCancelled) {
					t.Fatalf("cancel cause=%v", out.err)
				}
				if mode == "fail" {
					var ee *exec.ExitError
					if !errors.As(out.err, &ee) || ee.ExitCode() != 23 || re.Reason != adaptor.ReasonAgentError {
						t.Fatalf("exit cause=%v", out.err)
					}
				}
				handoffAudit(t, re.Result, mode, false, false)
				record, e = fx.store.Resolve(ctx, threadstore.Query{Key: "handoff"})
				after, _ := json.Marshal(record)
				if e != nil || string(before) != string(after) {
					t.Error("failed callback handoff changed healthy checkpoint")
				}
				if snapshot != handoffSnapshot(t, healthy.result) {
					t.Error("failure mutated previously returned result")
				}
				recovered := handoffResult(t, ctx, handoffRun(ctx, fx.thread, "recovered", stream))
				if recovered.err != nil {
					t.Fatal(recovered.err)
				}
				handoffAudit(t, recovered.result, "recovered", false, true)
				if fx.spawnCount(t) != 2 || fx.overlapCount(t) != 0 {
					t.Error("failure handoff replayed or overlapped writers")
				}
				raw, e := os.ReadFile(fx.prompts)
				if e != nil || string(raw) != "healthy\n"+mode+"\nrecovered\n" {
					t.Fatalf("prompt replay: %q %v", raw, e)
				}
			})
		}
	}
}
