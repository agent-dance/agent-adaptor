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
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/bridges/agui"
	"github.com/agent-dance/agent-adaptor/hosttools/sessionrecorder"
)

func recordCompletedTurn(t *testing.T, r sessionrecorder.EventRecorder, key string) {
	t.Helper()
	recordEvent(t, r, key, adaptor.Notice{Kind: BoundaryKind, Data: map[string]any{
		"threadId": "t", "runId": "completed",
	}})
	in := agui.RunAgentInput{Messages: []agui.Message{{ID: "u0", Role: "user", Content: []byte(`"earlier"`)}}}
	for _, ev := range in.UserTurnEvents("completed") {
		recordEvent(t, r, key, ev)
	}
	for _, ev := range []adaptor.Event{
		adaptor.TextDelta{MessageID: "a0", Phase: adaptor.PhaseStart},
		adaptor.TextDelta{MessageID: "a0", Phase: adaptor.PhaseContent, Text: "earlier answer"},
		adaptor.TextDelta{MessageID: "a0", Phase: adaptor.PhaseEnd},
		adaptor.Notice{Kind: TerminalKind},
	} {
		recordEvent(t, r, key, ev)
	}
}

func waitForRecord(
	t *testing.T, r sessionrecorder.EventRecorder, key string,
	match func(sessionrecorder.EventRecord) bool,
) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	changes, stop, err := r.(sessionrecorder.Follower).Subscribe(ctx, RecorderKey(key))
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	var after sessionrecorder.HostSeq
	for {
		records, err := r.Since(ctx, RecorderKey(key), after)
		if err != nil {
			t.Fatal(err)
		}
		for _, rec := range records {
			if match(rec) {
				return
			}
			after = rec.HostSeq
		}
		select {
		case <-changes:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}

// The official client merges seq into message and tool-call metadata. It is a
// transport cursor, so compare conversation state after removing only that field
// and any empty containers introduced solely to carry it. Fold stays unchanged.
func withoutObserverSeq(t *testing.T, messages []Message) any {
	t.Helper()
	raw, err := json.Marshal(messages)
	if err != nil {
		t.Fatal(err)
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	var strip func(any)
	strip = func(value any) {
		switch value := value.(type) {
		case []any:
			for _, child := range value {
				strip(child)
			}
		case map[string]any:
			metadata, _ := value["metadata"].(map[string]any)
			ns, _ := metadata["agentAdaptor"].(map[string]any)
			if _, ok := ns["seq"]; ok {
				delete(ns, "seq")
				if len(ns) == 0 {
					delete(metadata, "agentAdaptor")
					if len(metadata) == 0 {
						delete(value, "metadata")
					}
				}
			}
			for _, child := range value {
				strip(child)
			}
		}
	}
	strip(value)
	return value
}

func TestSnapshotJoinsActiveRun(t *testing.T) {
	for _, exitCode := range []int{0, 1} {
		t.Run(fmt.Sprint(exitCode), func(t *testing.T) { testSnapshotJoinsActiveRun(t, exitCode) })
	}
}

func testSnapshotJoinsActiveRun(t *testing.T, exitCode int) {
	s, input, started := unattended(t, 1)
	at := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	s.opts.Recorder = sessionrecorder.NewEventRecorder(sessionrecorder.NewMemoryEventBackend(),
		sessionrecorder.WithEventClock(func() time.Time { return at }))
	t.Cleanup(func() { _ = s.opts.Recorder.Close() })
	s.opts.Input.HistoryFallback = true
	key := ThreadKey("t")
	recordCompletedTurn(t, s.opts.Recorder, key)
	completed := checkedMessages(t, s, key)
	body, err := json.Marshal(map[string]any{
		"threadId": "t", "runId": "active", "protocolVersion": "1.0",
		"messages": append(append([]Message{}, completed...), Message{
			"id": "u1", "role": "user", "name": "Ada", "content": []map[string]any{{"type": "text", "text": "continue"}},
			"metadata": map[string]any{"client": map[string]any{"ticket": "ticket-27"}},
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	postCtx, disconnect := context.WithCancel(context.Background())
	defer disconnect()
	post := make(chan struct{})
	go func() {
		defer close(post)
		s.Handler().ServeHTTP(httptest.NewRecorder(),
			httptest.NewRequest("POST", "/", strings.NewReader(string(body))).WithContext(postCtx))
	}()
	await(t, started)
	disconnect()
	await(t, post)
	input <- adaptor.Thinking{MessageID: "reason", Text: "considering"}
	input <- adaptor.TextDelta{MessageID: "a1", Phase: adaptor.PhaseStart}
	input <- adaptor.TextDelta{MessageID: "a1", Phase: adaptor.PhaseContent, Text: "partial "}
	input <- adaptor.ToolCall{ID: "tool", Name: "shell", Phase: adaptor.PhaseStart}
	waitForRecord(t, s.opts.Recorder, key, func(rec sessionrecorder.EventRecord) bool {
		ev, ok := rec.Event.(adaptor.ToolCall)
		return ok && ev.ID == "tool"
	})
	snapshot, err := s.Snapshot(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(snapshot.Messages, completed) {
		t.Fatalf("snapshot included the active turn: %v", snapshot.Messages)
	}
	if snapshot.ActiveRun == nil || snapshot.ActiveRun.ThreadID != "t" || snapshot.ActiveRun.RunID != "active" ||
		snapshot.Cursor+1 != snapshot.ActiveRun.BoundaryHostSeq {
		t.Fatalf("invalid active cut: %+v", snapshot)
	}
	validateMessages(t, snapshot.Messages)
	response := httptest.NewRecorder()
	s.SnapshotHandler().ServeHTTP(response, httptest.NewRequest("GET", "/?thread_id=t", nil))
	var httpSnapshot Snapshot
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &httpSnapshot) != nil ||
		!reflect.DeepEqual(httpSnapshot, snapshot) {
		t.Fatalf("active snapshot handler: %d %s", response.Code, response.Body)
	}
	type observation struct {
		records []ProjectedRecord
		err     error
	}
	observed := make(chan observation, 1)
	first := make(chan struct{}, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() {
		var records []ProjectedRecord
		err := s.Observe(ctx, key, snapshot.Cursor, func(rec ProjectedRecord) error {
			records = append(records, rec)
			select {
			case first <- struct{}{}:
			default:
			}
			return nil
		})
		observed <- observation{records: records, err: err}
	}()
	await(t, first)
	input <- adaptor.ToolCall{ID: "tool", Phase: adaptor.PhaseEnd, Result: map[string]any{
		"exitCode": exitCode, "output": "command output",
	}}
	input <- adaptor.TextDelta{MessageID: "a1", Phase: adaptor.PhaseContent, Text: "answer"}
	input <- adaptor.TextDelta{MessageID: "a1", Phase: adaptor.PhaseEnd}
	close(input)
	var result observation
	select {
	case result = <-observed:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if result.err != nil {
		t.Fatal(result.err)
	}
	events := flattened(t, result.records)
	// @ag-ui/client 1.0.3 verifyEvents (z in dist/index.mjs) rejects a
	// non-RUN_STARTED first content event and unmatched message/tool lifecycles.
	if len(events) == 0 || str(events[0], "type") != "RUN_STARTED" ||
		str(events[0], "threadId") != "t" || str(events[0], "runId") != "active" {
		t.Fatalf("not a run-boundary join: %v", events)
	}
	last := snapshot.Cursor
	folded := snapshot.Messages
	for _, rec := range result.records {
		if rec.HostSeq != last+1 {
			t.Fatalf("cursor gap or duplicate: %d after %d", rec.HostSeq, last)
		}
		last = rec.HostSeq
		for _, ev := range rec.Events {
			folded = Fold(folded, ev)
		}
	}
	for _, ev := range events {
		validate(t, "Event", []any{ev})
	}
	final := checkedMessages(t, s, key)
	if !reflect.DeepEqual(withoutObserverSeq(t, folded), withoutObserverSeq(t, final)) {
		t.Fatalf("snapshot + follow differs from final history:\n%v\n%v", folded, final)
	}
	validateMessages(t, folded)
	if exitCode == 0 {
		fixture := struct {
			Snapshot Snapshot          `json:"snapshot"`
			Records  []ProjectedRecord `json:"records"`
			History  []Message         `json:"history"`
		}{snapshot, result.records, final}
		// Snapshot was taken after the tool opener was durable and before its
		// result existed. This is an actual in-flight join, not a split static trace.
		raw, err := json.MarshalIndent(fixture, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		raw = append(raw, '\n')
		const fixturePath = "testdata/client-observe.json"
		if os.Getenv("UPDATE_GOLDEN") == "1" {
			if err := os.WriteFile(fixturePath, raw, 0644); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(fixturePath)
		if err != nil {
			t.Fatal(err)
		}
		if string(want) != string(raw) {
			t.Fatal("reviewed client-observe fixture changed")
		}

	}

	if err := s.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	idle, err := s.Snapshot(ctx, key)
	if err != nil || idle.ActiveRun != nil || idle.Cursor != last || !reflect.DeepEqual(idle.Messages, final) {
		t.Fatalf("idle snapshot: %+v, %v", idle, err)
	}
}

func validateMessages(t *testing.T, messages []Message) {
	t.Helper()
	values := make([]any, len(messages))
	for i, message := range messages {
		values[i] = message
	}
	validate(t, "Message", values)
}

func TestSnapshotHandler(t *testing.T) {
	var accesses []Access
	s := New(fakeRunner{}, Options{Recorder: recorder(t), Resolve: func(_ *http.Request, access Access) (string, error) {
		accesses = append(accesses, access)
		return "host-key", nil
	}})
	recordCompletedTurn(t, s.opts.Recorder, "host-key")
	w := httptest.NewRecorder()
	s.SnapshotHandler().ServeHTTP(w, httptest.NewRequest("GET", "/?thread_id=public", nil))
	var snapshot Snapshot
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &snapshot) != nil {
		t.Fatal(w.Code, w.Body)
	}
	if len(accesses) != 1 || accesses[0].Op != OpRead || accesses[0].ThreadID != "public" || accesses[0].Input != nil {
		t.Fatal(accesses)
	}
	if snapshot.ActiveRun != nil || snapshot.Cursor != 8 || len(snapshot.Messages) != 2 {
		t.Fatal(w.Body)
	}
	validateMessages(t, snapshot.Messages)
	// The simple history endpoint must remain a bare array.
	w = httptest.NewRecorder()
	s.MessagesHandler().ServeHTTP(w, httptest.NewRequest("GET", "/?thread_id=public", nil))
	if !strings.HasPrefix(w.Body.String(), "[") {
		t.Fatal(w.Body)
	}
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{"POST", "/?thread_id=public", 405},
		{"GET", "/", 400},
	} {
		w := httptest.NewRecorder()
		s.SnapshotHandler().ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		if w.Code != tc.status || !json.Valid(w.Body.Bytes()) {
			t.Fatal(w.Code, w.Body)
		}
		if tc.status == 405 && w.Header().Get("Allow") != "GET" {
			t.Fatal(w.Header())
		}
	}
	s.opts.Resolve = func(*http.Request, Access) (string, error) {
		return "", &agui.InputError{Status: 403, Code: "host.denied", Message: "denied"}
	}
	w = httptest.NewRecorder()
	s.SnapshotHandler().ServeHTTP(w, httptest.NewRequest("GET", "/?thread_id=public", nil))
	if w.Code != 403 || !json.Valid(w.Body.Bytes()) {
		t.Fatal(w.Code, w.Body)
	}
}

func TestSnapshotEmptyAndPendingAdmission(t *testing.T) {
	s := New(fakeRunner{}, Options{Recorder: recorder(t)})
	key := ThreadKey("t")
	empty, err := s.Snapshot(context.Background(), key)
	if err != nil || empty.Cursor != 0 || empty.ActiveRun != nil || empty.Messages == nil || len(empty.Messages) != 0 {
		t.Fatalf("empty snapshot: %+v, %v", empty, err)
	}
	s.mu.Lock()
	s.active[key] = true
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	if _, err := s.Snapshot(ctx, key); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	s.release(key)
}

type snapshotReadBarrier struct {
	sessionrecorder.EventRecorder
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (r *snapshotReadBarrier) Since(
	ctx context.Context, key string, after sessionrecorder.HostSeq,
) ([]sessionrecorder.EventRecord, error) {
	records, err := r.EventRecorder.Since(ctx, key, after)
	r.once.Do(func() {
		close(r.entered)
		select {
		case <-r.release:
		case <-ctx.Done():
		}
	})
	return records, err
}

func TestSnapshotRetriesAcrossRunTransitions(t *testing.T) {
	for _, transition := range []string{"admission", "completion"} {
		t.Run(transition, func(t *testing.T) {
			r := &snapshotReadBarrier{
				EventRecorder: recorder(t), entered: make(chan struct{}), release: make(chan struct{}),
			}
			s := New(fakeRunner{}, Options{Recorder: r})
			key := ThreadKey("t")
			recordCompletedTurn(t, r, key)
			publishBoundary := func() sessionrecorder.HostSeq {
				rec := recordEvent(t, r, key, adaptor.Notice{Kind: BoundaryKind, Data: map[string]any{
					"threadId": "t", "runId": "active",
				}})
				s.mu.Lock()
				s.active[key] = true
				s.boundaries[key] = rec.HostSeq
				s.mu.Unlock()
				return rec.HostSeq
			}
			var boundary sessionrecorder.HostSeq
			if transition == "completion" {
				boundary = publishBoundary()
			}
			type result struct {
				snapshot Snapshot
				err      error
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			done := make(chan result, 1)
			go func() {
				snapshot, err := s.Snapshot(ctx, key)
				done <- result{snapshot: snapshot, err: err}
			}()
			await(t, r.entered)
			if transition == "admission" {
				boundary = publishBoundary()
			} else {
				recordEvent(t, r, key, adaptor.Notice{Kind: TerminalKind})
				s.release(key)
			}
			close(r.release)
			select {
			case result := <-done:
				if result.err != nil {
					t.Fatal(result.err)
				}
				if transition == "admission" {
					if result.snapshot.ActiveRun == nil || result.snapshot.Cursor != boundary-1 {
						t.Fatalf("snapshot mixed admission states: %+v", result.snapshot)
					}
				} else if result.snapshot.ActiveRun != nil || result.snapshot.Cursor != boundary+1 {
					t.Fatalf("snapshot missed completion: %+v", result.snapshot)
				}
				validateMessages(t, result.snapshot.Messages)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		})
	}
}
