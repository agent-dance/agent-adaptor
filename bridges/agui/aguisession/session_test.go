package aguisession

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/bridges/agui"
	"github.com/agent-dance/agent-adaptor/hosttools/sessionrecorder"
	schema "github.com/santhosh-tekuri/jsonschema/v6"
)

type fakeRunner struct {
	makeStream func(context.Context, string) adaptor.Stream
}

func (f fakeRunner) Run(context.Context, string, ...adaptor.CallOption) (*adaptor.Result, error) {
	panic("must stream")
}
func (f fakeRunner) Stream(c context.Context, p string, _ ...adaptor.CallOption) adaptor.Stream {
	return f.makeStream(c, p)
}

type fakeStream struct {
	ch     chan adaptor.Event
	result *adaptor.Result
	err    error
	cancel atomic.Bool
}

func (f *fakeStream) Events() <-chan adaptor.Event     { return f.ch }
func (f *fakeStream) Result() (*adaptor.Result, error) { return f.result, f.err }
func (f *fakeStream) RunID() string                    { return "sdk" }
func (f *fakeStream) Cancel()                          { f.cancel.Store(true) }
func script(result *adaptor.Result, err error, es ...adaptor.Event) *fakeStream {
	ch := make(chan adaptor.Event, len(es))
	for _, e := range es {
		ch <- e
	}
	close(ch)
	return &fakeStream{ch: ch, result: result, err: err}
}
func recorder(t *testing.T) sessionrecorder.EventRecorder {
	t.Helper()
	r := sessionrecorder.NewEventRecorder(sessionrecorder.NewMemoryEventBackend())
	t.Cleanup(func() { _ = r.Close() })
	return r
}
func request(s *Session, thread, run string, ms []Message) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]any{"protocolVersion": "1.0", "threadId": thread, "runId": run, "messages": ms})
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("POST", "/", strings.NewReader(string(body))))
	return w
}
func user(id, text string) Message { return Message{"id": id, "role": "user", "content": text} }
func wireEvents(t *testing.T, w *httptest.ResponseRecorder) []map[string]any {
	t.Helper()
	out := []map[string]any{}
	for _, line := range strings.Split(w.Body.String(), "\n") {
		if strings.HasPrefix(line, "data: ") {
			var e map[string]any
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &e); err != nil {
				t.Fatal(err)
			}
			out = append(out, e)
		}
	}
	return out
}
func validate(t *testing.T, ref string, values []any) {
	t.Helper()
	raw, err := os.ReadFile("../testdata/ag-ui-schema-1.0.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	_ = json.Unmarshal(raw, &doc)
	doc["$ref"] = "#/$defs/" + ref
	c := schema.NewCompiler()
	if err = c.AddResource("fixture.json", doc); err != nil {
		t.Fatal(err)
	}
	compiled, err := c.Compile("fixture.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range values {
		b, _ := json.Marshal(v)
		var value any
		_ = json.Unmarshal(b, &value)
		if err = compiled.Validate(value); err != nil {
			t.Fatalf("%s schema: %v\n%s", ref, err, b)
		}
	}
}
func flattened(t *testing.T, rs []ProjectedRecord) []map[string]any {
	t.Helper()
	out := []map[string]any{}
	for _, r := range rs {
		for _, ev := range r.Events {
			b, e := ev.ToJSON()
			if e != nil {
				t.Fatal(e)
			}
			var v map[string]any
			_ = json.Unmarshal(b, &v)
			out = append(out, v)
		}
	}
	return out
}
func TestLiveHistoryAndMessages(t *testing.T) {
	for _, mode := range []string{"tools", "fallback", "business", "cancel", "infrastructure", "activity"} {
		t.Run(mode, func(t *testing.T) {
			var count int
			r := fakeRunner{func(_ context.Context, prompt string) adaptor.Stream {
				count++
				id := fmt.Sprintf("a%d", count)
				es := []adaptor.Event{adaptor.RunStarted{RunID: "provider"}}
				res := &adaptor.Result{Text: "answer"}
				var err error
				switch mode {
				case "fallback":
				case "business":
					err = &adaptor.RunError{Reason: "policy.denied", Message: "denied", Result: res}
					res = nil
				case "cancel":
					err = context.Canceled
				case "infrastructure":
					err = errors.New("broken pipe")
				default:
					es = append(es, adaptor.Thinking{MessageID: id + "-reason", Text: "thinking"}, adaptor.TextDelta{MessageID: id, Text: "answer"})
					for i := 1; i <= 13; i++ {
						tool := fmt.Sprintf("%s-tool%d", id, i)
						exit := 0
						if i == 13 {
							exit = 1
						}
						es = append(es, adaptor.ToolCall{ID: tool, Name: "shell", Phase: adaptor.PhaseStart, Args: map[string]any{"n": i}}, adaptor.ToolCall{ID: tool, Phase: adaptor.PhaseEnd, Result: map[string]any{"output": "output", "exitCode": exit}})
					}
					if mode == "activity" {
						es = append(es, adaptor.SubagentUpdate{Agent: "helper", Data: map[string]any{"delegation_id": id + "-sub", "kind": "started"}})
					}
				}
				es = append(es, adaptor.RunFinished{Failed: false})
				return script(res, err, es...)
			}}
			s := New(r, Options{Recorder: recorder(t), Input: agui.InputOptions{HistoryFallback: true}})
			prior := []Message{}
			var lastSeq uint64
			for turn := 1; turn <= 2; turn++ {
				u := user(fmt.Sprintf("u%d", turn), "go")
				input := append(append([]Message{}, prior...), u)
				w := request(s, "thread", fmt.Sprint(turn), input)
				if w.Code != 200 {
					t.Fatalf("%d %s", w.Code, w.Body)
				}
				live := wireEvents(t, w)
				records, err := s.Events(context.Background(), ThreadKey("thread"), lastSeq)
				if err != nil {
					t.Fatal(err)
				}
				// The POST already contains this user message; seed the official fold.
				liveMessages := append(append([]Message{}, prior...), u)
				// Fold the live translator events, skipping the recorded user triple.
				expectedLive := []map[string]any{}
				for _, rec := range records {
					if rec.HostSeq <= lastSeq {
						t.Fatal("unordered cursor")
					}
					lastSeq = rec.HostSeq
					for _, ev := range rec.Events {
						raw, _ := ev.ToJSON()
						var m map[string]any
						_ = json.Unmarshal(raw, &m)
						if strings.HasPrefix(str(m, "type"), "TEXT_MESSAGE_") && str(m, "messageId") == str(u, "id") {
							continue
						}
						expectedLive = append(expectedLive, m)
						liveMessages = Fold(liveMessages, ev)
					}
				}
				if !reflect.DeepEqual(live, expectedLive) {
					t.Fatalf("live != history\nlive %v\nhistory %v", live, expectedLive)
				}
				history, err := s.Messages(context.Background(), ThreadKey("thread"))
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(liveMessages, history) {
					t.Fatalf("messages differ\nlive %v\nhistory %v", liveMessages, history)
				}
				values := []any{}
				for _, e := range flattened(t, records) {
					values = append(values, e)
				}
				validate(t, "Event", values)
				values = nil
				for _, m := range history {
					values = append(values, m)
				}
				validate(t, "Message", values)
				again, _ := s.Events(context.Background(), ThreadKey("thread"), 0)
				again2, _ := s.Events(context.Background(), ThreadKey("thread"), 0)
				if !reflect.DeepEqual(flattened(t, again), flattened(t, again2)) {
					t.Fatal("replay not deterministic")
				}
				if mode == "tools" {
					found := false
					for _, m := range history {
						if str(m, "role") == "tool" && strings.HasSuffix(str(m, "toolCallId"), "tool13") {
							found = true
							if str(m, "error") == "" {
								t.Fatal("failed tool lost error")
							}
						}
					}
					if !found {
						t.Fatal("missing 13th tool")
					}
				}
				prior = history
			}
		})
	}
}

type failingRecorder struct {
	sessionrecorder.EventRecorder
	remaining int
}

func (f *failingRecorder) Record(c context.Context, k string, e adaptor.Event) (sessionrecorder.EventRecord, error) {
	f.remaining--
	if f.remaining == 0 {
		return sessionrecorder.EventRecord{}, errors.New("disk full")
	}
	return f.EventRecorder.Record(c, k, e)
}
func TestRecordingFailure(t *testing.T) {
	stream := script(&adaptor.Result{Text: "false success"}, nil, adaptor.TextDelta{MessageID: "a", Text: "partial"}, adaptor.RunFinished{})
	s := New(fakeRunner{func(context.Context, string) adaptor.Stream { return stream }}, Options{Recorder: &failingRecorder{EventRecorder: recorder(t), remaining: 6}})
	w := request(s, "t", "r", []Message{user("u", "go")})
	if !stream.cancel.Load() || !strings.Contains(w.Body.String(), "agui.recording_failed") || strings.Contains(w.Body.String(), "RUN_FINISHED") {
		t.Fatalf("not fail closed: %s", w.Body)
	}
	rs, err := s.Events(context.Background(), ThreadKey("t"), 0)
	if err != nil {
		t.Fatal(err)
	}
	es := flattened(t, rs)
	last := es[len(es)-1]
	if str(last, "code") != "run.incomplete" {
		t.Fatalf("false completion: %v", last)
	}
	vals := []any{}
	for _, e := range es {
		vals = append(vals, e)
	}
	validate(t, "Event", vals)
	// A retry of the failed public run is rejected; a new ID may execute.
	if retry := request(s, "t", "r", []Message{user("u", "go")}); retry.Code != 409 || !strings.Contains(retry.Body.String(), "duplicate_run") {
		t.Fatal(retry.Body)
	}
}
func TestAdmissionAndConcurrency(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	s := New(fakeRunner{func(ctx context.Context, p string) adaptor.Stream {
		ch := make(chan adaptor.Event)
		once.Do(func() { close(started) })
		go func() {
			select {
			case <-release:
			case <-ctx.Done():
			}
			close(ch)
		}()
		return &fakeStream{ch: ch}
	}}, Options{Recorder: recorder(t)})
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- request(s, "t", "r", []Message{user("u", "go")}) }()
	<-started
	if w := request(s, "t", "other", []Message{user("u", "go")}); w.Code != 409 || !strings.Contains(w.Body.String(), "run_active") {
		t.Fatal(w.Body)
	}
	other := make(chan *httptest.ResponseRecorder, 1)
	go func() { other <- request(s, "different", "r", []Message{user("u", "go")}) }()
	deadline := time.After(time.Second)
	for !s.Active(ThreadKey("different")) {
		select {
		case <-deadline:
			t.Fatal("different thread blocked")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	close(release)
	if (<-done).Code != 200 || (<-other).Code != 200 {
		t.Fatal("concurrent request failed")
	}
	if w := request(s, "t", "r", []Message{user("u", "go")}); w.Code != 409 || !strings.Contains(w.Body.String(), "duplicate_run") {
		t.Fatal(w.Body)
	}
}
func TestVerifierAndHandlers(t *testing.T) {
	s := New(fakeRunner{func(context.Context, string) adaptor.Stream { return script(&adaptor.Result{Text: "answer"}, nil) }}, Options{Recorder: recorder(t)})
	if w := request(s, "t", "one", []Message{user("u", "go")}); w.Code != 200 {
		t.Fatal(w.Body)
	}
	ms := checkedMessages(t, s, ThreadKey("t"))
	raw, _ := json.Marshal(append(ms, user("next", "next")))
	var inputMessages []agui.Message
	_ = json.Unmarshal(raw, &inputMessages)
	in := &agui.RunAgentInput{Messages: inputMessages}
	key, usable, err := s.VerifyHistory(context.Background(), in, s.runner, ThreadKey("t"))
	if err != nil || usable || key != ThreadKey("t") {
		t.Fatalf("%q %v %v", key, usable, err)
	}
	in.Messages[0].ID = "edited"
	_, _, err = s.VerifyHistory(context.Background(), in, s.runner, key)
	if err == nil || !strings.Contains(err.Error(), "agui.history_diverged") {
		t.Fatal(err)
	}
	s.opts.ResolveDivergence = func(context.Context, *agui.RunAgentInput, string) (string, error) { return "new-key", nil }
	key, usable, err = s.VerifyHistory(context.Background(), in, s.runner, key)
	if err != nil || usable || key != "new-key" {
		t.Fatalf("%s %v %v", key, usable, err)
	}
	for _, h := range []http.Handler{s.EventsHandler(), s.MessagesHandler()} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/?thread_id=t", nil))
		if w.Code != 200 || !json.Valid(w.Body.Bytes()) {
			t.Fatal(w.Body)
		}
	}
}

func TestLegacyIdentityAndPromptSelection(t *testing.T) {
	var prompt string
	s := New(fakeRunner{func(_ context.Context, p string) adaptor.Stream {
		prompt = p
		return script(&adaptor.Result{}, nil)
	}}, Options{Recorder: recorder(t), Input: agui.InputOptions{HistoryFallback: true}, ResolveDivergence: func(context.Context, *agui.RunAgentInput, string) (string, error) { return "fresh", nil }})
	for _, body := range []string{
		`{"messages":[{"id":"u","role":"user","content":"hello"}]}`,
		`{"threadId":"t","runId":"r","messages":[{"id":"old","role":"user","content":"older"},{"id":"a","role":"assistant","content":"answer"},{"id":"new","role":"user","content":"latest"}]}`,
	} {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest("POST", "/", strings.NewReader(body)))
		if w.Code != 200 {
			t.Fatal(w.Body)
		}
		es := wireEvents(t, w)
		if len(es) == 0 || str(es[0], "threadId") == "" || str(es[0], "runId") == "" {
			t.Fatal("missing generated identity")
		}
	}
	if !strings.HasSuffix(prompt, "latest") {
		t.Fatalf("wrong trailing prompt: %s", prompt)
	}
}

func TestHTTPDisconnectCancelsRun(t *testing.T) {
	done := make(chan struct{})
	s := New(fakeRunner{func(ctx context.Context, _ string) adaptor.Stream {
		ch := make(chan adaptor.Event)
		go func() { <-ctx.Done(); close(ch); close(done) }()
		return &fakeStream{ch: ch}
	}}, Options{Recorder: recorder(t)})
	server := httptest.NewServer(s.Handler())
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", server.URL, strings.NewReader(`{"protocolVersion":"1.0","threadId":"t","runId":"r","messages":[{"id":"u","role":"user","content":"go"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	response.Body.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("disconnect did not cancel execution")
	}
}

func checkedMessages(t *testing.T, s *Session, key string) []Message {
	t.Helper()
	ms, err := s.Messages(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	values := make([]any, len(ms))
	for i, m := range ms {
		values[i] = m
	}
	validate(t, "Message", values)
	return ms
}

func TestClientNextTurnVerification(t *testing.T) {
	stream := script(nil, nil,
		adaptor.Thinking{MessageID: "reason", Text: "thinking"},
		adaptor.ToolCall{ID: "tool", Name: "shell", Phase: adaptor.PhaseStart},
		adaptor.ToolCall{ID: "tool", Phase: adaptor.PhaseEnd, Result: map[string]any{"exitCode": 1, "output": "failed"}},
	)
	s := New(fakeRunner{func(context.Context, string) adaptor.Stream { return stream }}, Options{Recorder: recorder(t)})
	u := user("u", "go")
	w := request(s, "t", "one", []Message{u})
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	// @ag-ui/client 1.0.3 index.mjs: prepareRunAgentInput clones messages
	// and filters only role=activity; reasoning, tools and tool-only assistants stay.
	// defaultApplyEvents does not promote result metadata to ToolMessage.error.
	client := []Message{u}
	records, err := s.Events(context.Background(), ThreadKey("t"), 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range records {
		for _, e := range r.Events {
			raw, _ := e.ToJSON()
			var wire map[string]any
			_ = json.Unmarshal(raw, &wire)
			if str(wire, "messageId") == "u" {
				continue
			}
			client = Fold(client, e)
		}
	}
	for _, m := range client {
		delete(m, "error")
	}
	filtered := []Message{}
	for _, m := range client {
		if str(m, "role") != "activity" {
			filtered = append(filtered, m)
		}
	}
	filtered = append(filtered, user("next", "next"))
	raw, _ := json.Marshal(filtered)
	var ms []agui.Message
	if err := json.Unmarshal(raw, &ms); err != nil {
		t.Fatal(err)
	}
	in := &agui.RunAgentInput{Messages: ms}
	if _, _, err := s.VerifyHistory(context.Background(), in, s.runner, ThreadKey("t")); err != nil {
		t.Fatal(err)
	}
	checkedMessages(t, s, ThreadKey("t"))
	for _, edit := range []string{"edit", "branch"} {
		copyInput := *in
		copyInput.Messages = append([]agui.Message(nil), ms...)
		if edit == "edit" {
			copyInput.Messages[0].ID = "edited"
		} else {
			copyInput.Messages = copyInput.Messages[1:]
		}
		if _, _, err := s.VerifyHistory(context.Background(), &copyInput, s.runner, ThreadKey("t")); err == nil {
			t.Fatal("accepted divergence")
		}
	}
}

func TestLegacyStatelessHistory(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		t.Run(fmt.Sprint(fallback), func(t *testing.T) {
			s := New(fakeRunner{func(context.Context, string) adaptor.Stream { return script(nil, nil) }}, Options{Recorder: recorder(t), Input: agui.InputOptions{HistoryFallback: fallback}})
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, httptest.NewRequest("POST", "/", strings.NewReader(`{"messages":[{"id":"old","role":"user","content":"old"},{"id":"a","role":"assistant","content":"answer"},{"id":"u","role":"user","content":"next"}]}`)))
			if fallback {
				if w.Code != 200 {
					t.Fatal(w.Body)
				}
			} else if w.Code != 409 || !strings.Contains(w.Body.String(), "agui.history_unrecoverable") {
				t.Fatal(w.Code, w.Body)
			}
		})
	}
}

func TestJSONLReconstructionAndRetry(t *testing.T) {
	dir := t.TempDir()
	open := func() sessionrecorder.EventRecorder {
		backend, err := sessionrecorder.NewJSONLEventBackend(dir)
		if err != nil {
			t.Fatal(err)
		}
		return sessionrecorder.NewEventRecorder(backend)
	}
	rec := open()
	runner := fakeRunner{func(context.Context, string) adaptor.Stream {
		return script(nil, &adaptor.RunError{Reason: adaptor.ReasonAgentError, Message: "failed"})
	}}
	s := New(runner, Options{Recorder: rec})
	u := user("u", "go")
	if w := request(s, "t", "failed", []Message{u}); w.Code != 200 {
		t.Fatal(w.Body)
	}
	if w := request(s, "t", "failed", []Message{u}); w.Code != 409 || !strings.Contains(w.Body.String(), "duplicate_run") {
		t.Fatal(w.Body)
	}
	prior := checkedMessages(t, s, ThreadKey("t"))
	if w := request(s, "t", "new", append(prior, user("u2", "again"))); w.Code != 409 { // no checkpoint: explicit rehydration required
		t.Fatal(w.Body)
	}
	s.opts.Input.HistoryFallback = true
	if w := request(s, "t", "new", append(prior, user("u2", "again"))); w.Code != 200 {
		t.Fatal(w.Body)
	}
	before, err := s.Events(context.Background(), ThreadKey("t"), 0)
	if err != nil {
		t.Fatal(err)
	}
	var last uint64
	boundaries := 0
	for _, r := range before {
		if r.HostSeq <= last {
			t.Fatal("HostSeq not strictly increasing across runs")
		}
		last = r.HostSeq
		for _, e := range r.Events {
			raw, _ := e.ToJSON()
			if strings.Contains(string(raw), `"type":"RUN_STARTED"`) {
				boundaries++
			}
		}
	}
	if boundaries != 2 {
		t.Fatal("missing run boundaries", boundaries)
	}
	messages := checkedMessages(t, s, ThreadKey("t"))
	if err := rec.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := open()
	defer reopened.Close()
	reconstructed := New(runner, Options{Recorder: reopened})
	after, err := reconstructed.Events(context.Background(), ThreadKey("t"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(flattened(t, before), flattened(t, after)) {
		t.Fatal("events changed after reopen")
	}
	if !reflect.DeepEqual(messages, checkedMessages(t, reconstructed, ThreadKey("t"))) {
		t.Fatal("messages changed after reopen")
	}
}

func TestHTTPMethodsAndAccess(t *testing.T) {
	var ops []Op
	s := New(fakeRunner{func(context.Context, string) adaptor.Stream { return script(nil, nil) }}, Options{Recorder: recorder(t), Resolve: func(_ *http.Request, a Access) (string, error) { ops = append(ops, a.Op); return "mapped", nil }})
	request(s, "t", "r", []Message{user("u", "go")})
	for _, h := range []http.Handler{s.EventsHandler(), s.MessagesHandler()} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/?thread_id=t", nil))
		if w.Code != 200 {
			t.Fatal(w.Body)
		}
	}
	if !reflect.DeepEqual(ops, []Op{OpStart, OpRead, OpRead}) {
		t.Fatal(ops)
	}
	for _, h := range []http.Handler{s.Handler(), s.EventsHandler(), s.MessagesHandler()} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("DELETE", "/", nil))
		if w.Code != 405 || w.Header().Get("Allow") == "" || !json.Valid(w.Body.Bytes()) {
			t.Fatal(w.Code, w.Header(), w.Body)
		}
	}
}

type brokenWriter struct {
	header    http.Header
	deadlines []time.Time
}

func (w *brokenWriter) Header() http.Header { return w.header }

func (w *brokenWriter) WriteHeader(int) {}

func (w *brokenWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func (w *brokenWriter) Flush() {}

func (w *brokenWriter) SetWriteDeadline(at time.Time) error {
	w.deadlines = append(w.deadlines, at)
	return nil
}

func TestWriteFailureCancelsAndDrains(t *testing.T) {
	stream := script(&adaptor.Result{Text: "answer"}, nil)
	s := New(fakeRunner{func(context.Context, string) adaptor.Stream { return stream }}, Options{Recorder: recorder(t), WriteTimeout: time.Second})
	w := &brokenWriter{header: make(http.Header)}
	s.Handler().ServeHTTP(w, httptest.NewRequest("POST", "/", strings.NewReader(`{"threadId":"t","runId":"r","messages":[{"id":"u","role":"user","content":"go"}]}`)))
	if !stream.cancel.Load() || s.Active(ThreadKey("t")) {
		t.Fatal("run not released")
	}
	if len(w.deadlines) < 2 || w.deadlines[0].IsZero() || !w.deadlines[len(w.deadlines)-1].IsZero() {
		t.Fatal(w.deadlines)
	}
	if w.header.Get("X-Accel-Buffering") != "no" || w.header.Get("Connection") != "keep-alive" {
		t.Fatal(w.header)
	}
	checkedMessages(t, s, ThreadKey("t"))
}

type readBarrierRecorder struct {
	sessionrecorder.EventRecorder
	entered chan struct{}
	release chan struct{}
}

func (r *readBarrierRecorder) Since(ctx context.Context, key string, after sessionrecorder.HostSeq) ([]sessionrecorder.EventRecord, error) {
	if key == RecorderKey(ThreadKey("slow")) {
		close(r.entered)
		select {
		case <-r.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return r.EventRecorder.Since(ctx, key, after)
}

func TestHistoryIODoesNotBlockAdmission(t *testing.T) {
	rec := &readBarrierRecorder{EventRecorder: recorder(t), entered: make(chan struct{}), release: make(chan struct{})}
	s := New(fakeRunner{func(context.Context, string) adaptor.Stream { return script(nil, nil) }}, Options{Recorder: rec})
	done := make(chan struct{})
	go func() { defer close(done); _, _ = s.Events(context.Background(), ThreadKey("slow"), 0) }()
	<-rec.entered
	defer close(rec.release)
	admitted := make(chan *httptest.ResponseRecorder, 1)
	go func() { admitted <- request(s, "other", "r", []Message{user("u", "go")}) }()
	select {
	case w := <-admitted:
		if w.Code != 200 {
			t.Fatal(w.Body)
		}
	case <-time.After(time.Second):
		t.Fatal("history I/O blocked admission")
	}
}
