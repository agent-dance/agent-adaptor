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

	events "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"github.com/agent-dance/agent-adaptor/v2/bridges/agui"
	"github.com/agent-dance/agent-adaptor/v2/driver"
	"github.com/agent-dance/agent-adaptor/v2/hosttools/sessionrecorder"
	"github.com/agent-dance/agent-adaptor/v2/memory"
)

func cloneMessages(t *testing.T, ms []Message) []Message {
	t.Helper()
	b, err := json.Marshal(ms)
	if err != nil {
		t.Fatal(err)
	}
	var copy []Message
	if err := decodeOpaque(b, &copy); err != nil {
		t.Fatal(err)
	}
	return copy
}

func postBody(s *Session, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("POST", "/", strings.NewReader(body)))
	return w
}

// This compares the actual POST wire, not another pass over the history fold.
func foldWire(t *testing.T, initial []Message, wire []map[string]any) []Message {
	t.Helper()
	ms := cloneMessages(t, initial)
	for _, e := range wire {
		raw, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		ms = Fold(ms, jsonEvent{raw: raw})
	}
	return ms
}

func TestAuthoritativeResultEdges(t *testing.T) {
	for _, tc := range []struct {
		name, code, text string
		err              error
		source           []adaptor.Event
	}{
		{name: "empty business reason", code: "run.error", text: "partial", err: &adaptor.RunError{Message: "failed"}},
		{name: "empty business message", code: "agent_error", text: "partial", err: &adaptor.RunError{Reason: adaptor.ReasonAgentError}},
		{name: "ignored start payload", text: "answer", source: []adaptor.Event{adaptor.TextDelta{MessageID: "a", Phase: adaptor.PhaseStart, Text: "ignored"}}},
		{name: "missing message id", text: "answer", source: []adaptor.Event{adaptor.TextDelta{Text: "ignored"}}},
		{name: "unrelated user event", text: "answer", source: []adaptor.Event{adaptor.TextDelta{MessageID: "other-user", Role: adaptor.RoleUser, Text: "other"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := New(fakeRunner{func(context.Context, string) adaptor.Stream {
				return script(&adaptor.Result{Text: tc.text}, tc.err, tc.source...)
			}}, Options{Recorder: recorder(t)})
			u := user("u", "go")
			w := request(s, "t", "r", []Message{u})
			es := wireEvents(t, w)
			last := es[len(es)-1]
			want := "RUN_FINISHED"
			if tc.err != nil {
				want = "RUN_ERROR"
			}
			if str(last, "type") != want || str(last, "code") != tc.code {
				t.Fatalf("wrong terminal %v", last)
			}
			if tc.err != nil && str(last, "message") == "" {
				t.Fatal("empty error message")
			}
			history := checkedMessages(t, s, ThreadKey("t"))
			if !reflect.DeepEqual(foldWire(t, []Message{u}, es), history) {
				t.Fatal("wire/history differ")
			}
			found := false
			for _, m := range history {
				if str(m, "role") == "assistant" && str(m, "content") == tc.text {
					found = true
				}
			}
			if !found {
				t.Fatal("Result.Text lost", history)
			}
			records, err := s.Events(context.Background(), ThreadKey("t"), 0)
			if err != nil {
				t.Fatal(err)
			}
			var sequence []events.Event
			for _, r := range records {
				sequence = append(sequence, r.Events...)
			}
			if err := agui.VerifySequence(sequence); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestReservedSourceNoticesAndProjectionVersion(t *testing.T) {
	for _, kind := range []string{BoundaryKind, TerminalKind} {
		stream := script(&adaptor.Result{Text: "partial"}, nil, adaptor.Notice{Kind: kind, Data: map[string]any{"runId": "forged"}}, adaptor.TextDelta{MessageID: "a", Text: "discarded"})
		s := New(fakeRunner{func(context.Context, string) adaptor.Stream { return stream }}, Options{Recorder: recorder(t)})
		w := request(s, "t", "r", []Message{user("u", "go")})
		if !stream.cancel.Load() || !strings.Contains(w.Body.String(), "agui.reserved_notice") || strings.Contains(w.Body.String(), "RUN_FINISHED") {
			t.Fatal(w.Body)
		}
		records, err := s.opts.Recorder.Since(context.Background(), RecorderKey(ThreadKey("t")), 0)
		if err != nil {
			t.Fatal(err)
		}
		for i, r := range records {
			if n, ok := r.Event.(adaptor.Notice); ok && n.Kind == kind && i > 0 && i < len(records)-1 {
				t.Fatal("source control persisted")
			}
		}
		checkedMessages(t, s, ThreadKey("t"))
	}
	for _, version := range []string{"", "agui-session/1", "future"} {
		p := newProjector(nil)
		records := []sessionrecorder.EventRecord{
			{HostSeq: 1, RecordedAt: time.Unix(100, 0), Event: adaptor.Notice{Kind: BoundaryKind, Data: map[string]any{"threadId": "t", "runId": "r", "projectionVersion": version}}},
			{HostSeq: 2, RecordedAt: time.Unix(101, 0), Event: adaptor.Notice{Kind: TerminalKind}},
		}
		es := flattened(t, p.project(records, 0, true, "run.incomplete"))
		if len(es) != 2 || str(es[1], "code") != "agui.projection_version_unsupported" {
			t.Fatal(es)
		}
	}
}

func TestRecordingFailsAtEveryWrite(t *testing.T) {
	// boundary + user triple + three source records + terminal = eight writes.
	for failAt := 1; failAt <= 8; failAt++ {
		t.Run(fmt.Sprint(failAt), func(t *testing.T) {
			stream := script(nil, nil, adaptor.TextDelta{MessageID: "a", Phase: adaptor.PhaseStart}, adaptor.TextDelta{MessageID: "a", Text: "answer"}, adaptor.TextDelta{MessageID: "a", Phase: adaptor.PhaseEnd})
			var calls int
			s := New(fakeRunner{func(context.Context, string) adaptor.Stream { calls++; return stream }}, Options{Recorder: &failingRecorder{EventRecorder: recorder(t), remaining: failAt}})
			w := request(s, "t", "r", []Message{user("u", "go")})
			if !strings.Contains(w.Body.String(), "agui.recording_failed") || strings.Contains(w.Body.String(), "RUN_FINISHED") {
				t.Fatal(w.Body)
			}
			if s.Active(ThreadKey("t")) {
				t.Fatal("reservation leaked")
			}
			if failAt == 1 {
				if calls != 0 || w.Code != 500 {
					t.Fatal(calls, w.Code, w.Body)
				}
				if retry := request(s, "t", "r", []Message{user("u", "go")}); retry.Code != 200 {
					t.Fatal("unrecorded boundary consumed run id", retry.Body)
				}
				return
			}
			if !stream.cancel.Load() {
				t.Fatal("stream not cancelled")
			}
			es, err := s.Events(context.Background(), ThreadKey("t"), 0)
			if err != nil {
				t.Fatal(err)
			}
			durable, err := s.opts.Recorder.Since(context.Background(), RecorderKey(ThreadKey("t")), 0)
			if err != nil {
				t.Fatal(err)
			}
			live := wireEvents(t, w)
			if live[len(live)-1]["timestamp"] != float64(durable[len(durable)-1].RecordedAt.UnixMilli()) {
				t.Fatal("emergency terminal ignored last durable timestamp")
			}
			flat := flattened(t, es)
			if str(flat[len(flat)-1], "code") != "run.incomplete" {
				t.Fatal(flat)
			}
			checkedMessages(t, s, ThreadKey("t"))
		})
	}
}

func TestCancelledRequestPersistsTerminal(t *testing.T) {
	rec := contextCheckingRecorder{recorder(t)}
	entered := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := New(fakeRunner{func(ctx context.Context, _ string) adaptor.Stream {
		ch := make(chan adaptor.Event)
		go func() {
			defer close(ch)
			close(entered)
			for {
				select {
				case ch <- adaptor.TextDelta{MessageID: "a", Text: "x"}:
				case <-ctx.Done():
					return
				}
			}
		}()
		return &contextResultStream{fakeStream: fakeStream{ch: ch}, ctx: ctx}
	}}, Options{Recorder: rec})
	w := &cancelOnWrite{header: make(http.Header), cancel: cancel}
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Handler().ServeHTTP(w, httptest.NewRequest("POST", "/", strings.NewReader(`{"threadId":"t","runId":"r","messages":[{"id":"u","role":"user","content":"go"}]}`)).WithContext(ctx))
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("no stream")
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("drain blocked after disconnect")
	}
	records, err := rec.Since(context.Background(), RecorderKey(ThreadKey("t")), 0)
	if err != nil {
		t.Fatal(err)
	}
	terminal, ok := records[len(records)-1].Event.(adaptor.Notice)
	if !ok || terminal.Kind != TerminalKind || str(terminal.Data, "code") != "run.cancelled" {
		t.Fatalf("cancellation lost: %#v", terminal)
	}
	if s.Active(ThreadKey("t")) {
		t.Fatal("active after drain")
	}
	checkedMessages(t, s, ThreadKey("t"))
}

type contextResultStream struct {
	fakeStream
	ctx context.Context
}

func (s *contextResultStream) Result() (*adaptor.Result, error) { return nil, s.ctx.Err() }

type cancelOnWrite struct {
	header http.Header
	cancel context.CancelFunc
}

func (w *cancelOnWrite) Header() http.Header       { return w.header }
func (w *cancelOnWrite) WriteHeader(int)           {}
func (w *cancelOnWrite) Write([]byte) (int, error) { w.cancel(); return 0, errors.New("disconnected") }

// A context-aware recorder verifies cancellation is detached only for cleanup
// writes. It also mutates the host's original source object after persistence.
func TestRecordedValuesAreFrozenBeforeObserve(t *testing.T) {
	args := map[string]any{"n": 1}
	s := New(fakeRunner{func(context.Context, string) adaptor.Stream {
		return script(nil, nil, adaptor.ToolCall{ID: "t", Name: "shell", Phase: adaptor.PhaseStart, Args: args}, adaptor.ToolCall{ID: "t", Phase: adaptor.PhaseEnd})
	}}, Options{Recorder: recorder(t), Observe: func(_ string, ev adaptor.Event) {
		if call, ok := ev.(adaptor.ToolCall); ok && call.Args != nil {
			call.Args["n"] = 999
		}
	}})
	w := request(s, "t", "r", []Message{user("u", "go")})
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	history := checkedMessages(t, s, ThreadKey("t"))
	b, _ := json.Marshal(history)
	if strings.Contains(string(b), "999") || !strings.Contains(string(b), `\"n\":1`) {
		t.Fatal(string(b))
	}
	if !reflect.DeepEqual(history, foldWire(t, []Message{user("u", "go")}, wireEvents(t, w))) {
		t.Fatal("Observe mutated projection")
	}
}

// A fixed Thread must reserve its actual host key even when wire IDs differ.
type statelessDriver struct{}

func (statelessDriver) Descriptor() driver.Descriptor {
	return driver.Descriptor{Type: "stateless-test", DisplayName: "Stateless test"}
}
func (statelessDriver) ValidateConfig(any) error { return nil }
func (statelessDriver) Run(context.Context, driver.Request, driver.EventSink) (driver.Response, error) {
	return driver.Response{}, nil
}

type barrierRecorder struct {
	sessionrecorder.EventRecorder
	key              string
	entered, release chan struct{}
	once             sync.Once
}

func (r *barrierRecorder) Since(ctx context.Context, key string, after sessionrecorder.HostSeq) ([]sessionrecorder.EventRecord, error) {
	if key == r.key {
		r.once.Do(func() {
			close(r.entered)
			select {
			case <-r.release:
			case <-ctx.Done():
			}
		})
	}
	return r.EventRecorder.Since(ctx, key, after)
}

func TestFixedThreadAdmissionUsesHostKey(t *testing.T) {
	agent := adaptor.New(statelessDriver{}, adaptor.WithThreadStore(memory.NewStore()))
	defer agent.Close(context.Background())
	fixed := agent.Thread("host-key")
	rec := &barrierRecorder{EventRecorder: recorder(t), key: RecorderKey(fixed.Key()), entered: make(chan struct{}), release: make(chan struct{})}
	s := New(fixed, Options{Recorder: rec})
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- request(s, "public-one", "r1", []Message{user("u1", "go")}) }()
	select {
	case <-rec.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("host key not reserved")
	}
	second := request(s, "public-two", "r2", []Message{user("u2", "go")})
	if second.Code != 409 || !strings.Contains(second.Body.String(), "agui.run_active") {
		t.Fatal(second.Code, second.Body)
	}
	close(rec.release)
	select {
	case first := <-done:
		if first.Code != 200 {
			t.Fatal(first.Body)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("first did not finish")
	}
	if s.Active(fixed.Key()) || s.Active(ThreadKey("public-one")) {
		t.Fatal("reservation leaked")
	}
}

func TestLegacyResolverMappingAndRelease(t *testing.T) {
	s := New(fakeRunner{func(context.Context, string) adaptor.Stream { return script(nil, nil) }}, Options{Recorder: recorder(t), Resolve: func(*http.Request, Access) (string, error) { return "host-key", nil }})
	body := `{"runId":"r","messages":[{"id":"u","role":"user","content":"go"}]}`
	if w := postBody(s, body); w.Code != 200 {
		t.Fatal(w.Body)
	}
	if len(checkedMessages(t, s, "host-key")) != 1 {
		t.Fatal("legacy resolver ignored")
	}
	if w := postBody(s, body); w.Code != 409 || !strings.Contains(w.Body.String(), "duplicate_run") {
		t.Fatal(w.Code, w.Body)
	}
	if s.Active("host-key") {
		t.Fatal("failed duplicate leaked reservation")
	}
}

func TestDivergenceRequiresFreshDestination(t *testing.T) {
	s := New(fakeRunner{func(context.Context, string) adaptor.Stream { return script(nil, nil) }}, Options{Recorder: recorder(t), Input: agui.InputOptions{HistoryFallback: true}, ResolveDivergence: func(context.Context, *agui.RunAgentInput, string) (string, error) {
		return ThreadKey("destination"), nil
	}})
	request(s, "destination", "existing", []Message{user("existing-user", "different")})
	w := request(s, "source", "r", []Message{user("old", "old"), user("new", "new")})
	if w.Code != 409 || !strings.Contains(w.Body.String(), "history_diverged") {
		t.Fatal(w.Code, w.Body)
	}
	if s.Active(ThreadKey("source")) || s.Active(ThreadKey("destination")) {
		t.Fatal("failed branch leaked reservation")
	}
}

func TestSessionHTTPErrorStatuses(t *testing.T) {
	internal := errors.New("backend unavailable")
	for _, tc := range []struct {
		name    string
		status  int
		code    string
		resolve func(*http.Request, Access) (string, error)
		url     string
	}{
		{name: "resolver infrastructure", status: 500, code: "agui.request_failed", resolve: func(*http.Request, Access) (string, error) { return "", internal }, url: "/?thread_id=t"},
		{name: "explicit denial", status: 403, code: "host.denied", resolve: func(*http.Request, Access) (string, error) {
			return "", &agui.InputError{Code: "host.denied", Message: "denied", Status: 403}
		}, url: "/?thread_id=t"},
		{name: "identity", status: 400, code: "agui.identity_required", url: "/"},
		{name: "cursor", status: 400, code: "agui.invalid_cursor", url: "/?thread_id=t&after=-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := New(fakeRunner{}, Options{Recorder: recorder(t), Resolve: tc.resolve})
			w := httptest.NewRecorder()
			s.EventsHandler().ServeHTTP(w, httptest.NewRequest("GET", tc.url, nil))
			if w.Code != tc.status || w.Header().Get("Content-Type") != "application/json" || !strings.Contains(w.Body.String(), tc.code) {
				t.Fatal(w.Code, w.Body)
			}
		})
	}
}

// This golden is consumed by the real HttpAgent in verify-client.mjs. Unlike
// the original same-reducer comparisons, it checks Go history against JS state
// and checks the actual next-turn input produced by prepareRunAgentInput.
func TestSessionOfficialClientFixture(t *testing.T) {
	type fixture struct {
		Name    string           `json:"name"`
		Input   []Message        `json:"input"`
		Events  []map[string]any `json:"events"`
		History []Message        `json:"history"`
	}
	var fixtures []fixture
	for _, mode := range []string{"tools", "fallback", "business", "cancel", "infrastructure"} {
		turn := 0
		r := sessionrecorder.NewEventRecorder(sessionrecorder.NewMemoryEventBackend(), sessionrecorder.WithEventClock(func() time.Time { return time.Unix(1700000000, 0) }))
		t.Cleanup(func() { _ = r.Close() })
		s := New(fakeRunner{func(context.Context, string) adaptor.Stream {
			turn++
			var source []adaptor.Event
			var err error
			result := &adaptor.Result{Text: "answer"}
			if mode == "tools" {
				source = append(source, adaptor.RunStarted{RunID: "provider"}, adaptor.Thinking{MessageID: "thinking-0", Text: "thinking"}, adaptor.TextDelta{MessageID: "a", Text: fmt.Sprintf("answer %d", turn)})
				source = append(source,
					adaptor.SubagentUpdate{Member: "helper", Kind: adaptor.SubagentStarted, Data: map[string]any{"delegation_id": "child"}},
					adaptor.SubagentUpdate{Member: "helper", Data: map[string]any{"delegation_id": "child", "kind": "subagent.tool_call.start", "remote_tool_call_id": "tool13", "tool_name": "shell"}},
				)
				for i := 1; i <= 13; i++ {
					id := fmt.Sprintf("tool%d", i)
					exit := 0
					if i == 13 {
						exit = 1
					}
					source = append(source, adaptor.ToolCall{ID: id, Name: "shell", Phase: adaptor.PhaseStart, Args: map[string]any{"n": i}}, adaptor.ToolCall{ID: id, Phase: adaptor.PhaseEnd, Result: map[string]any{"output": "output", "exitCode": exit}})
				}
				source = append(source, adaptor.SubagentUpdate{Member: "helper", Kind: adaptor.SubagentFinished, Data: map[string]any{"delegation_id": "child", "kind": "subagent.failed", "status": "failed"}})
			}
			switch mode {
			case "business":
				err = &adaptor.RunError{Reason: adaptor.ReasonAgentError, Message: "failed", Result: result}
				result = nil
			case "cancel":
				err = context.Canceled
			case "infrastructure":
				err = errors.New("infrastructure failed")
			}
			return script(result, err, source...)
		}}, Options{Recorder: r, Input: agui.InputOptions{HistoryFallback: true}, Events: []agui.EventTranslatorOption{agui.WithProjectionHook(func(ev events.Event) []events.Event {
			if ev.GetBaseEvent().Metadata == nil {
				ev.GetBaseEvent().Metadata = map[string]any{}
			}
			ev.GetBaseEvent().Metadata["hookTimestamp"] = ev.GetBaseEvent().Timestamp()
			return []events.Event{ev}
		})}})
		prior := []Message{}
		for i := 1; i <= 2; i++ {
			u := user(fmt.Sprintf("u%d", i), "go")
			// The full submitted user shape must survive history loading. A live
			// client's seed already owns these fields and its content-part array.
			if i == 1 {
				u["content"] = []any{map[string]any{"type": "text", "text": "go"}}
				u["name"] = "human"
				u["metadata"] = map[string]any{"userTag": true}
			}
			input := append(cloneMessages(t, prior), u)
			w := request(s, "t", fmt.Sprintf("r%d", i), input)
			if w.Code != 200 {
				t.Fatal(w.Code, w.Body)
			}
			live := wireEvents(t, w)
			history := checkedMessages(t, s, ThreadKey("t"))
			if !reflect.DeepEqual(foldWire(t, input, live), history) {
				t.Fatalf("wire/history mismatch: %s turn %d", mode, i)
			}
			var typed []events.Event
			records, err := s.Events(context.Background(), ThreadKey("t"), 0)
			if err != nil {
				t.Fatal(err)
			}
			// Verify the current run only; history intentionally contains multiple runs.
			for _, r := range records {
				for _, e := range r.Events {
					if string(e.Type()) == "RUN_STARTED" {
						typed = nil
					}
					typed = append(typed, e)
				}
			}
			if err := agui.VerifySequence(typed); err != nil {
				t.Fatal(err)
			}
			values := make([]any, len(live))
			for i, e := range live {
				values[i] = e
			}
			validate(t, "Event", values)
			fixtures = append(fixtures, fixture{Name: fmt.Sprintf("%s-%d", mode, i), Input: input, Events: live, History: history})
			prior = history
		}
	}
	raw, err := json.MarshalIndent(fixtures, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, '\n')
	path := "testdata/client-session.json"
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, raw, 0644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != string(raw) {
		t.Fatal("reviewed client-session fixture changed")
	}
}

type flushFailureWriter struct{ header http.Header }

func (w *flushFailureWriter) Header() http.Header         { return w.header }
func (w *flushFailureWriter) WriteHeader(int)             {}
func (w *flushFailureWriter) Write(p []byte) (int, error) { return len(p), nil }
func (w *flushFailureWriter) FlushError() error           { return errors.New("flush failed") }

func idleRunner() fakeRunner {
	return fakeRunner{func(ctx context.Context, _ string) adaptor.Stream {
		ch := make(chan adaptor.Event)
		go func() { <-ctx.Done(); close(ch) }()
		return &contextResultStream{fakeStream: fakeStream{ch: ch}, ctx: ctx}
	}}
}

func TestFlushFailureCancelsDrain(t *testing.T) {
	s := New(idleRunner(), Options{Recorder: recorder(t)})
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Handler().ServeHTTP(&flushFailureWriter{header: make(http.Header)}, httptest.NewRequest("POST", "/", strings.NewReader(`{"threadId":"t","runId":"r","messages":[{"id":"u","role":"user","content":"go"}]}`)))
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("flush failure left run blocked")
	}
	records, err := s.Events(context.Background(), ThreadKey("t"), 0)
	if err != nil {
		t.Fatal(err)
	}
	es := flattened(t, records)
	if str(es[len(es)-1], "code") != "run.cancelled" {
		t.Fatal(es)
	}
}

type stalledWriter struct {
	header   http.Header
	deadline time.Time
	ping     context.CancelFunc
}

func (w *stalledWriter) Header() http.Header                 { return w.header }
func (w *stalledWriter) WriteHeader(int)                     {}
func (w *stalledWriter) SetWriteDeadline(at time.Time) error { w.deadline = at; return nil }
func (w *stalledWriter) Write(p []byte) (int, error) {
	if w.ping != nil {
		if strings.HasPrefix(string(p), ":keep-alive") {
			w.ping()
		}
		return len(p), nil
	}
	if w.deadline.IsZero() {
		return 0, errors.New("deadline not armed")
	}
	timer := time.NewTimer(time.Until(w.deadline))
	defer timer.Stop()
	<-timer.C
	return 0, os.ErrDeadlineExceeded
}
func (w *stalledWriter) Flush() {}

func TestStalledSSEWriteAndKeepAlive(t *testing.T) {
	for _, ping := range []bool{false, true} {
		t.Run(fmt.Sprint(ping), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s := New(idleRunner(), Options{Recorder: recorder(t), WriteTimeout: 250 * time.Millisecond, KeepAlivePing: 25 * time.Millisecond})
			w := &stalledWriter{header: make(http.Header)}
			if ping {
				w.ping = cancel
			}
			done := make(chan struct{})
			go func() {
				defer close(done)
				s.Handler().ServeHTTP(w, httptest.NewRequest("POST", "/", strings.NewReader(`{"threadId":"t","runId":"r","messages":[{"id":"u","role":"user","content":"go"}]}`)).WithContext(ctx))
			}()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("write timeout/ping did not release drain")
			}
			if !w.deadline.IsZero() || s.Active(ThreadKey("t")) {
				t.Fatal("deadline or reservation leaked")
			}
			checkedMessages(t, s, ThreadKey("t"))
		})
	}
}

func TestDrainCancellationUnblocksFullOutput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rec := recorder(t)
	blocked := make(chan struct{})
	count := 0
	s := New(fakeRunner{}, Options{Recorder: rec, Observe: func(_ string, ev adaptor.Event) {
		if _, ok := ev.(adaptor.TextDelta); ok {
			count++
			if count == 34 {
				close(blocked)
			}
		}
	}})
	in := &agui.RunAgentInput{ThreadID: "t", RunID: "r", Messages: []agui.Message{{ID: "u", Role: "user", Content: json.RawMessage(`"go"`)}}}
	boundary, err := s.recordBoundary(ctx, ThreadKey("t"), &agui.PreparedRequest{Input: in})
	if err != nil {
		t.Fatal(err)
	}
	var source []adaptor.Event
	for i := 0; i < 100; i++ {
		source = append(source, adaptor.TextDelta{MessageID: "a", Text: "x"})
	}
	stream := script(nil, context.Canceled, source...)
	output := make(chan events.Event, 32)
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.drain(ctx, ThreadKey("t"), in, stream, s.openLive(ThreadKey("t"), in, stream), boundary, output)
	}()
	select {
	case <-blocked:
	case <-time.After(10 * time.Second):
		t.Fatal("drain did not fill queue")
	}
	if len(output) != cap(output) {
		t.Fatal("test did not reach backpressure", len(output))
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("cancelled drain blocked on output")
	}
	if !stream.cancel.Load() {
		t.Fatal("Cancel not propagated")
	}
	// Closed output is still drainable, and the authoritative terminal committed.
	for range output {
	}
	records, err := rec.Since(context.Background(), RecorderKey(ThreadKey("t")), 0)
	if err != nil {
		t.Fatal(err)
	}
	last := records[len(records)-1].Event.(adaptor.Notice)
	if last.Kind != TerminalKind || str(last.Data, "code") != "run.cancelled" {
		t.Fatal(last)
	}
}

type staleReadRecorder struct {
	sessionrecorder.EventRecorder
	key              string
	entered, release chan struct{}
	used             atomic.Bool
}

func (r *staleReadRecorder) Since(ctx context.Context, key string, after sessionrecorder.HostSeq) ([]sessionrecorder.EventRecord, error) {
	records, err := r.EventRecorder.Since(ctx, key, after)
	if key == r.key && !r.used.Swap(true) {
		close(r.entered)
		select {
		case <-r.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return records, err
}

func TestEventsRetriesAcrossCompleteAdmission(t *testing.T) {
	rec := &staleReadRecorder{EventRecorder: recorder(t), key: RecorderKey(ThreadKey("t")), entered: make(chan struct{}), release: make(chan struct{})}
	s := New(fakeRunner{func(context.Context, string) adaptor.Stream { return script(&adaptor.Result{Text: "answer"}, nil) }}, Options{Recorder: rec})
	done := make(chan []ProjectedRecord, 1)
	go func() {
		rs, err := s.Events(context.Background(), ThreadKey("t"), 0)
		if err != nil {
			t.Error(err)
		}
		done <- rs
	}()
	<-rec.entered
	if w := request(s, "t", "r", []Message{user("u", "go")}); w.Code != 200 {
		t.Fatal(w.Body)
	}
	close(rec.release)
	select {
	case records := <-done:
		es := flattened(t, records)
		if len(es) == 0 || str(es[len(es)-1], "type") != "RUN_FINISHED" {
			t.Fatal("stale read cut", es)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("history retry blocked")
	}
}

func TestDivergenceDestinationFilledDuringResolution(t *testing.T) {
	dest := ThreadKey("destination")
	rec := &staleReadRecorder{EventRecorder: recorder(t), key: RecorderKey(dest), entered: make(chan struct{}), release: make(chan struct{})}
	s := New(fakeRunner{func(context.Context, string) adaptor.Stream { return script(nil, nil) }}, Options{Recorder: rec, Input: agui.InputOptions{HistoryFallback: true}, ResolveDivergence: func(context.Context, *agui.RunAgentInput, string) (string, error) { return dest, nil }})
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- request(s, "source", "branch", []Message{user("old", "old"), user("new", "new")}) }()
	<-rec.entered
	if w := request(s, "destination", "winner", []Message{user("winner", "go")}); w.Code != 200 {
		t.Fatal(w.Body)
	}
	close(rec.release)
	select {
	case w := <-done:
		if w.Code != 409 || !strings.Contains(w.Body.String(), "history_diverged") {
			t.Fatal(w.Code, w.Body)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("branch blocked")
	}
	if s.Active(dest) || s.Active(ThreadKey("source")) {
		t.Fatal("branch leaked reservation")
	}
	if got := checkedMessages(t, s, dest); len(got) != 1 || str(got[0], "id") != "winner" {
		t.Fatal(got)
	}
}

func TestCancelledSubagentHasCancelledHistory(t *testing.T) {
	s := New(fakeRunner{func(context.Context, string) adaptor.Stream {
		return script(nil, context.Canceled, adaptor.SubagentUpdate{Member: "child", Kind: adaptor.SubagentStarted})
	}}, Options{Recorder: recorder(t)})
	w := request(s, "t", "r", []Message{user("u", "go")})
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	ms := checkedMessages(t, s, ThreadKey("t"))
	found := false
	for _, m := range ms {
		if str(m, "role") == "activity" {
			found = true
			content := m["content"].(map[string]any)
			if str(content, "status") != "cancelled" {
				t.Fatal(content)
			}
		}
	}
	if !found {
		t.Fatal("missing activity")
	}
}

type contextCheckingRecorder struct{ sessionrecorder.EventRecorder }

func (r contextCheckingRecorder) Record(ctx context.Context, key string, ev adaptor.Event) (sessionrecorder.EventRecord, error) {
	if err := ctx.Err(); err != nil {
		return sessionrecorder.EventRecord{}, err
	}
	return r.EventRecorder.Record(ctx, key, ev)
}

func TestRichJSONLReconstructionMatchesMemory(t *testing.T) {
	clock := func() time.Time { return time.Unix(1700000000, 0) }
	dir := t.TempDir()
	open := func() sessionrecorder.EventRecorder {
		b, err := sessionrecorder.NewJSONLEventBackend(dir)
		if err != nil {
			t.Fatal(err)
		}
		return sessionrecorder.NewEventRecorder(b, sessionrecorder.WithEventClock(clock))
	}
	disk := open()
	mem := sessionrecorder.NewEventRecorder(sessionrecorder.NewMemoryEventBackend(), sessionrecorder.WithEventClock(clock))
	defer mem.Close()
	runner := fakeRunner{func(context.Context, string) adaptor.Stream {
		return script(nil, nil,
			adaptor.Thinking{MessageID: "thinking-0", Text: "reason"}, adaptor.TextDelta{MessageID: "a", Text: "answer"},
			adaptor.ToolCall{ID: "tool", Name: "shell", Phase: adaptor.PhaseStart, Args: map[string]any{"nested": []any{map[string]any{"n": 1}}}},
			adaptor.ToolCall{ID: "tool", Phase: adaptor.PhaseEnd, Result: map[string]any{"output": "failed", "exitCode": json.Number("1")}},
			adaptor.SubagentUpdate{Member: "helper", Kind: adaptor.SubagentStarted, Data: map[string]any{"delegation_id": "child"}},
		)
	}}
	memorySession := New(runner, Options{Recorder: mem, Input: agui.InputOptions{HistoryFallback: true}})
	diskSession := New(runner, Options{Recorder: disk, Input: agui.InputOptions{HistoryFallback: true}})
	prior := []Message{}
	for i := 1; i <= 2; i++ {
		input := append(cloneMessages(t, prior), user(fmt.Sprintf("u%d", i), "go"))
		a := request(memorySession, "t", fmt.Sprint(i), cloneMessages(t, input))
		b := request(diskSession, "t", fmt.Sprint(i), cloneMessages(t, input))
		if a.Code != 200 || b.Code != 200 || !reflect.DeepEqual(wireEvents(t, a), wireEvents(t, b)) {
			t.Fatal("backend changed live projection", a.Body, b.Body)
		}
		prior = checkedMessages(t, diskSession, ThreadKey("t"))
		if !reflect.DeepEqual(prior, checkedMessages(t, memorySession, ThreadKey("t"))) {
			t.Fatal("backend changed messages")
		}
	}
	before, err := diskSession.Events(context.Background(), ThreadKey("t"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := disk.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := open()
	defer reopened.Close()
	reconstructed := New(fakeRunner{}, Options{Recorder: reopened})
	after, err := reconstructed.Events(context.Background(), ThreadKey("t"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(flattened(t, before), flattened(t, after)) || !reflect.DeepEqual(prior, checkedMessages(t, reconstructed, ThreadKey("t"))) {
		t.Fatal("rich JSONL reconstruction changed")
	}
	roles := map[string]int{}
	for _, m := range prior {
		roles[str(m, "role")]++
	}
	if roles["user"] != 2 || roles["reasoning"] != 2 || roles["assistant"] != 2 || roles["tool"] != 2 || roles["activity"] != 2 {
		t.Fatal("repeated provider IDs merged turns", roles)
	}
}

type continuityCodec struct{}

func (continuityCodec) Name() string { return "continuity" }
func (continuityCodec) ToParams(s *driver.SessionState) driver.SessionParams {
	if s == nil {
		return driver.SessionParams{}
	}
	return driver.SessionParams{ResumeID: s.ResumeID}
}
func (continuityCodec) FromParams(p driver.SessionParams) *driver.SessionState {
	if p.ResumeID == "" {
		return nil
	}
	return &driver.SessionState{ResumeID: p.ResumeID}
}
func (continuityCodec) GuardFingerprint(p driver.SessionParams) string { return p.ResumeID }

type continuityDriver struct {
	calls    int
	requests []driver.Request
}

func (*continuityDriver) Descriptor() driver.Descriptor {
	return driver.Descriptor{Type: "continuity", DisplayName: "Continuity", Sessions: driver.SessionCapability{SupportsResume: true}}
}
func (*continuityDriver) ValidateConfig(any) error                  { return nil }
func (*continuityDriver) SessionCodec() driver.SessionCodec         { return continuityCodec{} }
func (*continuityDriver) SessionConfigFingerprint() (string, error) { return "continuity", nil }
func (d *continuityDriver) Run(_ context.Context, r driver.Request, _ driver.EventSink) (driver.Response, error) {
	d.calls++
	d.requests = append(d.requests, r)
	if d.calls == 2 {
		return driver.Response{Output: "partial"}, errors.New("failed after healthy checkpoint")
	}
	return driver.Response{Output: "answer", Checkpoint: &driver.Checkpoint{State: &driver.SessionState{ResumeID: "healthy"}, Valid: true}}, nil
}

func TestVerifierDoesNotAttestCheckpointBeforeFailedTurn(t *testing.T) {
	d := &continuityDriver{}
	agent := adaptor.New(d, adaptor.WithThreadStore(memory.NewStore()))
	defer agent.Close(context.Background())
	s := New(agent, Options{Recorder: recorder(t), Input: agui.InputOptions{HistoryFallback: true}})
	if w := request(s, "t", "one", []Message{user("u1", "first")}); !strings.Contains(w.Body.String(), "RUN_FINISHED") {
		t.Fatal(w.Body)
	}
	prior := checkedMessages(t, s, ThreadKey("t"))
	if w := request(s, "t", "two", append(prior, user("u2", "second"))); !strings.Contains(w.Body.String(), "RUN_ERROR") {
		t.Fatal(w.Body)
	}
	if d.calls != 2 || d.requests[1].Prompt != "second" || d.requests[1].Session.Mode != driver.SessionContinueOnly {
		t.Fatal("healthy checkpoint did not resume correctly", d.requests)
	}
	prior = checkedMessages(t, s, ThreadKey("t"))
	w := request(s, "t", "three", append(cloneMessages(t, prior), user("u3", "third")))
	if w.Code != 409 || !strings.Contains(w.Body.String(), "agui.history_unrecoverable") || d.calls != 2 {
		t.Fatal(w.Code, w.Body, d.calls)
	}
	// An authorized fresh branch can rehydrate the whole recorded text history.
	s.opts.ResolveDivergence = func(context.Context, *agui.RunAgentInput, string) (string, error) { return ThreadKey("fresh"), nil }
	w = request(s, "t", "three", append(cloneMessages(t, prior), user("u3", "third")))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "RUN_FINISHED") || d.calls != 3 {
		t.Fatal(w.Code, w.Body, d.calls)
	}
	if !strings.Contains(d.requests[2].Prompt, "<agent-adaptor-history>") || d.requests[2].Session.Mode != driver.SessionContinueOrStart {
		t.Fatal(d.requests[2])
	}
}

func TestUnrecordableEventsFailClosed(t *testing.T) {
	for _, value := range []any{make(chan int), json.Number("invalid")} {
		stream := script(&adaptor.Result{Text: "false success"}, nil, adaptor.Notice{Kind: "host.data", Data: map[string]any{"value": value}})
		s := New(fakeRunner{func(context.Context, string) adaptor.Stream { return stream }}, Options{Recorder: recorder(t)})
		w := request(s, "t", "r", []Message{user("u", "go")})
		if !strings.Contains(w.Body.String(), "agui.recording_failed") || strings.Contains(w.Body.String(), "RUN_FINISHED") || !stream.cancel.Load() {
			t.Fatal(w.Body)
		}
		records, err := s.opts.Recorder.Since(context.Background(), RecorderKey(ThreadKey("t")), 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range records {
			if n, ok := r.Event.(adaptor.Notice); ok && n.Kind == "host.data" {
				t.Fatal("unrecordable event contaminated history")
			}
		}
	}
	called := false
	s := New(fakeRunner{func(context.Context, string) adaptor.Stream { called = true; return script(nil, nil) }}, Options{Recorder: recorder(t), Resolve: func(_ *http.Request, a Access) (string, error) {
		a.Input.Messages[0].Metadata = map[string]any{"unrecordable": make(chan int)}
		return ThreadKey(a.ThreadID), nil
	}})
	w := request(s, "t", "r", []Message{user("u", "go")})
	if w.Code != 500 || !strings.Contains(w.Body.String(), "agui.recording_failed") || called {
		t.Fatal("unrecordable boundary executed", w.Code, w.Body, called)
	}
	records, err := s.opts.Recorder.Since(context.Background(), RecorderKey(ThreadKey("t")), 0)
	if err != nil || len(records) != 0 {
		t.Fatal("unrecordable boundary persisted", records, err)
	}
}
