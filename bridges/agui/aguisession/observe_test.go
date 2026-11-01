package aguisession

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/bridges/agui"
	"github.com/agent-dance/agent-adaptor/hosttools/sessionrecorder"
)

type pacedStream struct {
	ctx    context.Context
	ch     chan adaptor.Event
	cancel context.CancelFunc
	once   sync.Once
}

func (s *pacedStream) Events() <-chan adaptor.Event { return s.ch }

func (s *pacedStream) RunID() string { return "paced" }

func (s *pacedStream) Cancel() {
	s.once.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
	})
}

func (s *pacedStream) Result() (*adaptor.Result, error) {
	if err := s.ctx.Err(); err != nil {
		return nil, err
	}
	return &adaptor.Result{}, nil
}

func unattended(t *testing.T, max int) (*Session, chan adaptor.Event, <-chan struct{}) {
	t.Helper()
	input := make(chan adaptor.Event)
	started := make(chan struct{})
	var once sync.Once
	s := New(fakeRunner{func(ctx context.Context, _ string) adaptor.Stream {
		ctx, cancel := context.WithCancel(ctx)
		ch := make(chan adaptor.Event)
		once.Do(func() { close(started) })
		go func() {
			defer close(ch)
			for {
				select {
				case <-ctx.Done():
					return
				case ev, ok := <-input:
					if !ok {
						return
					}
					select {
					case ch <- ev:
					case <-ctx.Done():
						return
					}
				}
			}
		}()
		return &pacedStream{ctx: ctx, ch: ch, cancel: cancel}
	}}, Options{Recorder: recorder(t), Unattended: true, BaseContext: context.Background(), MaxConcurrentRuns: max})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	return s, input, started
}

func launchRequest(s *Session, thread, run string) (context.CancelFunc, <-chan *httptest.ResponseRecorder) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		w := httptest.NewRecorder()
		body := fmt.Sprintf(`{"threadId":%q,"runId":%q,"messages":[{"id":"u","role":"user","content":"go"}]}`, thread, run)
		s.Handler().ServeHTTP(w, httptest.NewRequest("POST", "/", strings.NewReader(body)).WithContext(ctx))
		done <- w
	}()
	return cancel, done
}

func await(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out")
	}
}

func TestUnattendedDisconnectAndFollow(t *testing.T) {
	for iteration := 0; iteration < 20; iteration++ {
		t.Run(fmt.Sprint(iteration), func(t *testing.T) {
			s, input, started := unattended(t, 1)
			disconnect, post := launchRequest(s, "t", "r")
			await(t, started)
			disconnect()
			select {
			case <-post:
			case <-time.After(10 * time.Second):
				t.Fatal("request did not detach")
			}
			if !s.Active(ThreadKey("t")) {
				t.Fatal("disconnect cancelled run")
			}
			input <- adaptor.TextDelta{MessageID: "a", Phase: adaptor.PhaseStart}
			records, err := s.opts.Recorder.Since(context.Background(), RecorderKey(ThreadKey("t")), 0)
			if err != nil {
				t.Fatal(err)
			}
			cursor := records[len(records)-1].HostSeq
			received := make(chan []ProjectedRecord, 1)
			go func() {
				var out []ProjectedRecord
				err := s.Observe(context.Background(), ThreadKey("t"), cursor, func(r ProjectedRecord) error {
					out = append(out, r)
					return nil
				})
				if err != nil {
					t.Error(err)
				}
				received <- out
			}()
			for i := 0; i < 12; i++ {
				input <- adaptor.TextDelta{MessageID: "a", Text: "x", Phase: adaptor.PhaseContent}
			}
			close(input)
			var observed []ProjectedRecord
			select {
			case observed = <-received:
			case <-time.After(10 * time.Second):
				t.Fatal("observer did not close")
			}
			var last = cursor
			for _, rec := range observed {
				if rec.HostSeq != last+1 {
					t.Fatalf("gap/duplicate %d after %d", rec.HostSeq, last)
				}
				last = rec.HostSeq
				for _, ev := range rec.Events {
					raw, _ := ev.ToJSON()
					var value map[string]any
					_ = json.Unmarshal(raw, &value)
					validate(t, "Event", []any{value})
					metadata := value["metadata"].(map[string]any)["agentAdaptor"].(map[string]any)
					if metadata["seq"] != float64(rec.HostSeq) {
						t.Fatal(metadata)
					}
				}
			}
			all, err := s.opts.Recorder.Since(context.Background(), RecorderKey(ThreadKey("t")), cursor)
			if err != nil || len(all) != len(observed) {
				t.Fatal("missing suffix", len(all), len(observed), err)
			}
			var replay []ProjectedRecord
			if err := s.Observe(context.Background(), ThreadKey("t"), cursor, func(r ProjectedRecord) error {
				replay = append(replay, r)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if len(replay) != len(observed) {
				t.Fatal("completed catch-up differs")
			}
		})
	}
}

func TestUnattendedCancelCapacityShutdown(t *testing.T) {
	s, _, started := unattended(t, 1)
	disconnect, post := launchRequest(s, "t", "r")
	defer disconnect()
	await(t, started)
	if w := request(s, "other", "r2", []Message{user("u", "go")}); w.Code != 429 {
		t.Fatal(w.Code, w.Body)
	}
	if err := s.Cancel(context.Background(), ThreadKey("t")); err != nil {
		t.Fatal(err)
	}
	w := <-post
	if !strings.Contains(w.Body.String(), "run.cancelled") {
		t.Fatal(w.Body)
	}
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if w := request(s, "new", "r3", []Message{user("u", "go")}); w.Code != 503 {
		t.Fatal(w.Code, w.Body)
	}
}

func TestRevokeObserver(t *testing.T) {
	s, _, started := unattended(t, 1)
	disconnect, post := launchRequest(s, "t", "r")
	await(t, started)
	disconnect()
	<-post
	delivered := make(chan struct{})
	done := make(chan struct{})
	var once sync.Once
	go func() {
		defer close(done)
		_ = s.Observe(context.Background(), ThreadKey("t"), 0, func(ProjectedRecord) error {
			once.Do(func() { close(delivered) })
			return nil
		})
	}()
	await(t, delivered)
	s.Revoke(ThreadKey("t"))
	await(t, done)
	if !s.Active(ThreadKey("t")) {
		t.Fatal("revoke cancelled execution")
	}
}

// The wrapper deliberately exposes Follower, as third-party recorder decorators
// should do to retain prompt change notifications.
type notifyingFailure struct{ *failingRecorder }

func (r notifyingFailure) Subscribe(ctx context.Context, key string) (<-chan struct{}, func(), error) {
	return r.EventRecorder.(sessionrecorder.Follower).Subscribe(ctx, key)
}

func TestUnattendedRecordingFailure(t *testing.T) {
	stream := script(&adaptor.Result{Text: "answer"}, nil, adaptor.TextDelta{MessageID: "a", Text: "partial"})
	s := New(fakeRunner{func(context.Context, string) adaptor.Stream { return stream }}, Options{
		Recorder:   notifyingFailure{&failingRecorder{EventRecorder: recorder(t), remaining: 5}},
		Unattended: true, BaseContext: context.Background(),
	})
	w := request(s, "t", "r", []Message{user("u", "go")})
	if strings.Contains(w.Body.String(), "RUN_FINISHED") || !stream.cancel.Load() {
		t.Fatal(w.Body)
	}
	if !strings.Contains(w.Body.String(), "RUN_ERROR") {
		t.Fatal("no failed terminal", w.Body)
	}
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestShutdownCancelsActiveUnattendedRun(t *testing.T) {
	s, _, started := unattended(t, 1)
	disconnect, post := launchRequest(s, "t", "r")
	defer disconnect()
	await(t, started)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if s.Active(ThreadKey("t")) {
		t.Fatal("shutdown left active run")
	}
	if w := <-post; !strings.Contains(w.Body.String(), "run.cancelled") {
		t.Fatal(w.Body)
	}
}

func TestFollowAuthorizationRecheckAndCancelHandler(t *testing.T) {
	s, _, started := unattended(t, 1)
	var denied atomic.Bool
	var followChecks atomic.Int32
	s.opts.Resolve = func(_ *http.Request, a Access) (string, error) {
		if a.Op == OpFollow {
			followChecks.Add(1)
			if denied.Load() {
				return "", fmt.Errorf("revoked")
			}
		}
		return ThreadKey(a.ThreadID), nil
	}
	disconnect, post := launchRequest(s, "t", "r")
	await(t, started)
	denied.Store(true)
	select {
	case <-post:
	case <-time.After(10 * time.Second):
		t.Fatal("authorization not rechecked")
	}
	disconnect()
	if followChecks.Load() < 1 || !s.Active(ThreadKey("t")) {
		t.Fatal("authorization cancelled run")
	}
	w := httptest.NewRecorder()
	s.CancelHandler().ServeHTTP(w, httptest.NewRequest("POST", "/cancel?thread_id=t", nil))
	if w.Code != 204 {
		t.Fatal(w.Code, w.Body)
	}
}

func TestUnattendedHTTPDisconnect(t *testing.T) {
	s, input, started := unattended(t, 1)
	server := httptest.NewServer(s.Handler())
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	body := `{"threadId":"t","runId":"r","messages":[{"id":"u","role":"user","content":"go"}]}`
	req, err := http.NewRequestWithContext(ctx, "POST", server.URL, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	await(t, started)
	cancel()
	response.Body.Close()
	if !s.Active(ThreadKey("t")) {
		t.Fatal("disconnect ended run")
	}
	// Produce content after the connection is gone and require successful
	// completion; merely checking Active immediately after cancel is too weak.
	input <- adaptor.TextDelta{MessageID: "after-disconnect", Text: "survived"}
	close(input)
	waitForRecord(t, s.opts.Recorder, ThreadKey("t"), func(rec sessionrecorder.EventRecord) bool {
		n, ok := rec.Event.(adaptor.Notice)
		if !ok || n.Kind != TerminalKind {
			return false
		}
		if str(n.Data, "code") != "" {
			t.Fatalf("disconnect cancelled run: %v", n.Data)
		}
		return true
	})
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCompletedHTTPObservationAndAuthorization(t *testing.T) {
	s := New(fakeRunner{func(context.Context, string) adaptor.Stream {
		return script(&adaptor.Result{Text: "answer"}, nil)
	}}, Options{Recorder: recorder(t), Unattended: true, BaseContext: context.Background()})
	w := request(s, "t", "r", []Message{user("u", "go")})
	if w.Code != 200 || strings.Contains(w.Body.String(), `"messageId":"u"`) {
		t.Fatal(w.Body)
	}
	read := httptest.NewRecorder()
	s.ObserveHandler().ServeHTTP(read, httptest.NewRequest("GET", "/observe?thread_id=t&after=0", nil))
	if !strings.Contains(read.Body.String(), "RUN_FINISHED") || !strings.Contains(read.Body.String(), `"id":"u","role":"user"`) {
		t.Fatal(read.Body)
	}
	for _, ev := range wireEvents(t, read) {
		validate(t, "Event", []any{ev})
	}
	s.opts.Resolve = func(_ *http.Request, a Access) (string, error) {
		return "", &agui.InputError{Status: 403, Code: "host.denied", Message: string(a.Op)}
	}
	for _, tc := range []struct {
		handler http.Handler
		method  string
	}{
		{s.ObserveHandler(), "GET"}, {s.CancelHandler(), "POST"},
	} {
		response := httptest.NewRecorder()
		tc.handler.ServeHTTP(response, httptest.NewRequest(tc.method, "/?thread_id=t", nil))
		if response.Code != 403 || !json.Valid(response.Body.Bytes()) {
			t.Fatal(response.Code, response.Body)
		}
	}
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRecordingFailureAfterConsumedCursor(t *testing.T) {
	s := New(fakeRunner{func(context.Context, string) adaptor.Stream {
		return script(nil, nil)
	}}, Options{Recorder: recorder(t)})
	key := ThreadKey("t")
	rec, err := s.opts.Recorder.Record(context.Background(), RecorderKey(key), adaptor.Notice{
		Kind: BoundaryKind, Data: map[string]any{"threadId": "t", "runId": "r", "projectionVersion": ProjectionVersion},
	})
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.recordingFailures[key] = true
	s.mu.Unlock()
	err = s.Observe(context.Background(), key, rec.HostSeq, func(ProjectedRecord) error {
		t.Fatal("invented cursor")
		return nil
	})
	var input *agui.InputError
	if !errors.As(err, &input) || input.Code != "agui.recording_failed" {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.ObserveHandler().ServeHTTP(w, httptest.NewRequest("GET", "/observe?thread_id=t&after=1", nil))
	events := wireEvents(t, w)
	if len(events) != 1 || str(events[0], "code") != "agui.recording_failed" {
		t.Fatal(w.Body)
	}
	validate(t, "Event", []any{events[0]})
}
