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
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"github.com/agent-dance/agent-adaptor/v2/driver"
	"github.com/agent-dance/agent-adaptor/v2/hosttools/sessionrecorder"
)

// steerDriver is a resumable driver whose first turn waits for release. With
// the zero message capability, core delivers supplements at turn_end by
// resuming the healthy checkpoint inside the same execution.
type steerDriver struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
	mu      sync.Mutex
	prompts []string
}

func newSteerDriver() *steerDriver {
	return &steerDriver{started: make(chan struct{}), release: make(chan struct{})}
}

func (d *steerDriver) Descriptor() driver.Descriptor {
	return driver.Descriptor{Type: "steer-test", DisplayName: "Steer test", Sessions: driver.SessionCapability{SupportsResume: true}}
}
func (*steerDriver) ValidateConfig(any) error                  { return nil }
func (*steerDriver) SessionConfigFingerprint() (string, error) { return "steer-test/v1", nil }
func (*steerDriver) SessionCodec() driver.SessionCodec         { return steerCodec{} }
func (*steerDriver) StreamCapability() driver.StreamCapability {
	return driver.StreamCapability{Native: true, TokenLevel: true}
}

func (d *steerDriver) Run(ctx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
	d.mu.Lock()
	d.prompts = append(d.prompts, req.Prompt)
	turn := len(d.prompts)
	d.mu.Unlock()
	// Provider IDs are turn-local: every turn reuses the same message ID.
	id := "answer"
	text := fmt.Sprintf("turn %d answer", turn)
	emit := func(ps ...driver.StreamPayload) error {
		for _, p := range ps {
			if err := sink.EmitStream(p); err != nil {
				return err
			}
		}
		return nil
	}
	if err := emit(driver.StreamPayload{Kind: driver.StreamRunStarted},
		driver.StreamPayload{Kind: driver.StreamTextStart, MessageID: id},
		driver.StreamPayload{Kind: driver.StreamTextContent, MessageID: id, Delta: text},
		driver.StreamPayload{Kind: driver.StreamTextEnd, MessageID: id}); err != nil {
		return driver.Response{}, err
	}
	if turn == 1 {
		// Turn 1 has produced its answer; input arrives before the turn ends.
		d.once.Do(func() { close(d.started) })
		select {
		case <-d.release:
		case <-ctx.Done():
			return driver.Response{}, ctx.Err()
		}
	}
	if err := emit(driver.StreamPayload{Kind: driver.StreamRunFinished}); err != nil {
		return driver.Response{}, err
	}
	_ = sink.Emit(driver.RunEvent{Type: driver.RunEventTurnCompleted, Turn: &driver.TurnCompleted{Text: text, CompletedAt: time.Now()}})
	return driver.Response{Output: text, Checkpoint: &driver.Checkpoint{Valid: true,
		State: &driver.SessionState{ResumeID: "steer-session"}}}, nil
}

func (d *steerDriver) prompt(i int) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if i >= len(d.prompts) {
		return ""
	}
	return d.prompts[i]
}

type steerCodec struct{}

func (steerCodec) Name() string { return "steer-test/v1" }
func (steerCodec) ToParams(s *driver.SessionState) driver.SessionParams {
	if s == nil {
		return driver.SessionParams{}
	}
	return driver.SessionParams{ResumeID: s.ResumeID}
}
func (steerCodec) FromParams(p driver.SessionParams) *driver.SessionState {
	if p.ResumeID == "" {
		return nil
	}
	return &driver.SessionState{ResumeID: p.ResumeID}
}
func (steerCodec) GuardFingerprint(driver.SessionParams) string { return "steer-test/v1" }

func postMessage(s *Session, thread, id, text string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]string{"threadId": thread, "messageId": id, "text": text})
	w := httptest.NewRecorder()
	s.MessageHandler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/message", strings.NewReader(string(body))))
	return w
}

func errorCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("error body %q: %v", w.Body, err)
	}
	return body.Error.Code
}

// wireSignature reduces AG-UI events to the identity-bearing fields compared
// between the live POST stream and the replayed history.
func wireSignature(events []map[string]any) []string {
	out := []string{}
	for _, e := range events {
		typ, _ := e["type"].(string)
		switch typ {
		case "TEXT_MESSAGE_START":
			out = append(out, fmt.Sprintf("%s %v %v", typ, e["messageId"], e["role"]))
		case "TEXT_MESSAGE_CONTENT":
			out = append(out, fmt.Sprintf("%s %v %v", typ, e["messageId"], e["delta"]))
		case "TEXT_MESSAGE_END":
			out = append(out, fmt.Sprintf("%s %v", typ, e["messageId"]))
		case "RUN_STARTED", "RUN_FINISHED", "RUN_ERROR":
			out = append(out, typ)
		case "CUSTOM":
			if e["name"] == "message.update" {
				value, _ := e["value"].(map[string]any)
				out = append(out, fmt.Sprintf("%s %v %v", e["name"], value["id"], value["state"]))
			}
		}
	}
	return out
}

func TestSupplementLiveAndHistoryAgree(t *testing.T) {
	d := newSteerDriver()
	agent := adaptor.New(d)
	defer agent.Close(context.Background())
	s := New(agent, Options{Recorder: recorder(t)})
	live := make(chan *httptest.ResponseRecorder, 1)
	go func() { live <- request(s, "t", "r1", []Message{user("u1", "start")}) }()
	<-d.started
	// Admission order is recording order; wait until turn 1's text is recorded.
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		messages, err := s.Messages(context.Background(), ThreadKey("t"))
		if err == nil && len(messages) == 2 && str(messages[1], "role") == "assistant" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("turn 1 answer was not recorded: %v %v", messages, err)
		}
	}

	w := postMessage(s, "t", "s1", "also check the tests")
	if w.Code != http.StatusAccepted {
		t.Fatalf("supplement HTTP %d: %s", w.Code, w.Body)
	}
	var accepted Accepted
	if err := json.Unmarshal(w.Body.Bytes(), &accepted); err != nil || accepted != (Accepted{ThreadID: "t", RunID: "r1", MessageID: "s1", State: "accepted"}) {
		t.Fatalf("accepted = %+v (%v)", accepted, err)
	}
	close(d.release)
	response := <-live
	if response.Code != http.StatusOK {
		t.Fatalf("run HTTP %d: %s", response.Code, response.Body)
	}
	if got := d.prompt(1); !strings.Contains(got, "also check the tests") {
		t.Fatalf("supplement was not delivered to the provider: %q", got)
	}

	liveEvents := wireEvents(t, response)
	validate(t, "Event", anySlice(liveEvents))
	records, err := s.Events(context.Background(), ThreadKey("t"), 0)
	if err != nil {
		t.Fatal(err)
	}
	history := flattened(t, records)
	// The POST client already owns its submitted user message, so only the
	// replay carries it; every other identity matches the live wire exactly.
	liveSig, historySig := wireSignature(liveEvents), wireSignature(history)
	historySig = dropSubmittedUser(historySig, "u1")
	if strings.Join(liveSig, "\n") != strings.Join(historySig, "\n") {
		t.Fatalf("live and history differ\nlive:\n%s\nhistory:\n%s", strings.Join(liveSig, "\n"), strings.Join(historySig, "\n"))
	}
	joined := strings.Join(liveSig, "\n")
	for _, want := range []string{
		"TEXT_MESSAGE_START s1 user", "TEXT_MESSAGE_CONTENT s1 also check the tests",
		"message.update s1 2", "RUN_FINISHED",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("live wire lacks %q:\n%s", want, joined)
		}
	}

	messages := checkedMessages(t, s, ThreadKey("t"))
	var roles []string
	for _, m := range messages {
		roles = append(roles, str(m, "role")+":"+str(m, "id"))
	}
	if len(messages) != 4 || str(messages[0], "id") != "u1" || str(messages[1], "role") != "assistant" ||
		str(messages[2], "id") != "s1" || str(messages[3], "role") != "assistant" || str(messages[1], "id") == str(messages[3], "id") {
		t.Fatalf("history messages = %v, want user, assistant turn 1, supplement, assistant turn 2", roles)
	}
	if messages[1]["content"] != "turn 1 answer" || messages[3]["content"] != "turn 2 answer" {
		t.Fatalf("turn contents = %#v / %#v", messages[1]["content"], messages[3]["content"])
	}
	for _, m := range messages {
		if str(m, "id") == "s1" && m["content"] != "also check the tests" {
			t.Fatalf("supplement content = %#v", m["content"])
		}
	}

	// The next turn's client history must include the supplement to verify.
	var prior []Message
	for _, m := range messages {
		prior = append(prior, m)
	}
	if w := request(s, "t", "r2", append(prior, user("u2", "next"))); w.Code != http.StatusOK {
		t.Fatalf("next turn with supplement history: %d %s", w.Code, w.Body)
	}
}

func TestSupplementRecordedBeforeDoneAndTerminal(t *testing.T) {
	d := newSteerDriver()
	agent := adaptor.New(d)
	defer agent.Close(context.Background())
	rec := recorder(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := New(agent, Options{Recorder: rec, Unattended: true, BaseContext: ctx})
	go func() { _ = request(s, "t", "r1", []Message{user("u1", "start")}) }()
	<-d.started
	if w := postMessage(s, "t", "s1", "guidance"); w.Code != http.StatusAccepted {
		t.Fatalf("supplement HTTP %d: %s", w.Code, w.Body)
	}
	close(d.release)
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	raw, err := rec.Since(context.Background(), RecorderKey(ThreadKey("t")), 0)
	if err != nil {
		t.Fatal(err)
	}
	supplementAt, doneAt, terminalAt := -1, -1, -1
	for i, r := range raw {
		switch e := r.Event.(type) {
		case adaptor.Notice:
			if e.Kind == SupplementKind {
				supplementAt = i
			}
			if e.Kind == TerminalKind {
				terminalAt = i
			}
		case adaptor.Done:
			doneAt = i
		}
	}
	if supplementAt < 0 || !(supplementAt < doneAt && doneAt < terminalAt && terminalAt == len(raw)-1) {
		t.Fatalf("record order supplement=%d done=%d terminal=%d of %d", supplementAt, doneAt, terminalAt, len(raw))
	}
}

func TestSupplementRejectionsCreateNoHistory(t *testing.T) {
	t.Run("inactive", func(t *testing.T) {
		agent := adaptor.New(newSteerDriver())
		defer agent.Close(context.Background())
		rec := recorder(t)
		s := New(agent, Options{Recorder: rec})
		w := postMessage(s, "t", "s1", "guidance")
		if w.Code != http.StatusConflict || errorCode(t, w) != "agui.run_inactive" {
			t.Fatalf("inactive supplement: %d %s", w.Code, w.Body)
		}
		if raw, _ := rec.Since(context.Background(), RecorderKey(ThreadKey("t")), 0); len(raw) != 0 {
			t.Fatalf("inactive supplement wrote history: %d records", len(raw))
		}
	})
	for name, tc := range map[string]struct {
		opts []adaptor.CallOption
		id   string
		code int
		want string
	}{
		"disabled":  {opts: []adaptor.CallOption{adaptor.WithPolicy(adaptor.Policy{Messages: adaptor.MessagePolicy{Disabled: true}})}, id: "s1", code: http.StatusUnprocessableEntity, want: "agui.message_rejected"},
		"duplicate": {id: "u1", code: http.StatusConflict, want: "agui.duplicate_message"},
	} {
		t.Run(name, func(t *testing.T) {
			d := newSteerDriver()
			agent := adaptor.New(d)
			defer agent.Close(context.Background())
			rec := recorder(t)
			s := New(agent, Options{Recorder: rec, CallOptions: tc.opts})
			done := make(chan struct{})
			go func() { defer close(done); _ = request(s, "t", "r1", []Message{user("u1", "start")}) }()
			<-d.started
			w := postMessage(s, "t", tc.id, "guidance")
			close(d.release)
			<-done
			if w.Code != tc.code || errorCode(t, w) != tc.want {
				t.Fatalf("supplement: %d %s", w.Code, w.Body)
			}
			raw, _ := rec.Since(context.Background(), RecorderKey(ThreadKey("t")), 0)
			for _, r := range raw {
				if n, ok := r.Event.(adaptor.Notice); ok && n.Kind == SupplementKind {
					t.Fatalf("rejected supplement was recorded: %+v", n)
				}
			}
			if _, terminal := raw[len(raw)-1].Event.(adaptor.Notice); !terminal {
				t.Fatalf("run did not finish after rejection: last=%T", raw[len(raw)-1].Event)
			}
		})
	}
	t.Run("malformed", func(t *testing.T) {
		agent := adaptor.New(newSteerDriver())
		defer agent.Close(context.Background())
		s := New(agent, Options{Recorder: recorder(t)})
		for _, body := range []string{`{`, `{"threadId":"t","messageId":"m","text":"x","extra":1}`, `{"threadId":"t","messageId":"m","text":"x"}{}`, `{"threadId":"t","messageId":"","text":"x"}`, `{"messageId":"m","text":"x"}`} {
			w := httptest.NewRecorder()
			s.MessageHandler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/message", strings.NewReader(body)))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("%s: %d %s", body, w.Code, w.Body)
			}
		}
		w := httptest.NewRecorder()
		s.MessageHandler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/message", nil))
		if w.Code != http.StatusMethodNotAllowed {
			t.Fatalf("GET: %d", w.Code)
		}
	})
}

func TestSupplementAuthorizesMessageOperation(t *testing.T) {
	agent := adaptor.New(newSteerDriver())
	defer agent.Close(context.Background())
	var ops []Op
	s := New(agent, Options{Recorder: recorder(t), Resolve: func(_ *http.Request, a Access) (string, error) {
		ops = append(ops, a.Op)
		return "", &errorStatus{}
	}})
	if w := postMessage(s, "t", "s1", "guidance"); w.Code == http.StatusAccepted || len(ops) != 1 || ops[0] != OpMessage {
		t.Fatalf("authorization: %d ops=%v", w.Code, ops)
	}
}

type errorStatus struct{}

func (*errorStatus) Error() string { return "denied" }

type failSupplementRecorder struct{ sessionrecorder.EventRecorder }

func (r failSupplementRecorder) Record(ctx context.Context, key string, ev adaptor.Event) (sessionrecorder.EventRecord, error) {
	if n, ok := ev.(adaptor.Notice); ok && n.Kind == SupplementKind {
		return sessionrecorder.EventRecord{}, errors.New("disk full")
	}
	return r.EventRecorder.Record(ctx, key, ev)
}

func TestSupplementRecordingFailureIsExplicit(t *testing.T) {
	d := newSteerDriver()
	agent := adaptor.New(d)
	defer agent.Close(context.Background())
	s := New(agent, Options{Recorder: failSupplementRecorder{recorder(t)}})
	live := make(chan *httptest.ResponseRecorder, 1)
	go func() { live <- request(s, "t", "r1", []Message{user("u1", "start")}) }()
	<-d.started
	w := postMessage(s, "t", "s1", "guidance")
	close(d.release)
	response := <-live
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if w.Code != http.StatusInternalServerError || body["state"] != "accepted" || body["messageId"] != "s1" {
		t.Fatalf("recording failure: %d %s", w.Code, w.Body)
	}
	if !strings.Contains(response.Body.String(), `"code":"agui.recording_failed"`) {
		t.Fatalf("run did not fail closed:\n%s", response.Body)
	}
}

func TestConcurrentSupplementsRaceRunEnd(t *testing.T) {
	for attempt := 0; attempt < 20; attempt++ {
		d := newSteerDriver()
		agent := adaptor.New(d)
		rec := recorder(t)
		s := New(agent, Options{Recorder: rec})
		done := make(chan struct{})
		go func() { defer close(done); _ = request(s, "t", "r1", []Message{user("u1", "start")}) }()
		<-d.started
		var wg sync.WaitGroup
		codes := make([]int, 8)
		for i := range codes {
			wg.Add(1)
			go func() {
				defer wg.Done()
				codes[i] = postMessage(s, "t", fmt.Sprintf("s%d", i), "guidance").Code
			}()
		}
		close(d.release)
		wg.Wait()
		<-done
		raw, err := rec.Since(context.Background(), RecorderKey(ThreadKey("t")), 0)
		if err != nil {
			t.Fatal(err)
		}
		recorded := map[string]int{}
		terminalAt := -1
		for i, r := range raw {
			if n, ok := r.Event.(adaptor.Notice); ok {
				switch n.Kind {
				case SupplementKind:
					if terminalAt >= 0 {
						t.Fatal("supplement recorded after the terminal")
					}
					recorded[str(n.Data, "messageId")]++
				case TerminalKind:
					terminalAt = i
				}
			}
		}
		for i, code := range codes {
			id := fmt.Sprintf("s%d", i)
			switch code {
			case http.StatusAccepted:
				if recorded[id] != 1 {
					t.Fatalf("accepted %s recorded %d times", id, recorded[id])
				}
			case http.StatusConflict:
				if recorded[id] != 0 {
					t.Fatalf("rejected %s was recorded", id)
				}
			default:
				t.Fatalf("%s: unexpected HTTP %d", id, code)
			}
		}
		if terminalAt != len(raw)-1 {
			t.Fatalf("terminal is not last: %d of %d", terminalAt, len(raw))
		}
		_ = agent.Close(context.Background())
	}
}

func anySlice(events []map[string]any) []any {
	out := make([]any, len(events))
	for i, e := range events {
		out[i] = e
	}
	return out
}

func dropSubmittedUser(sig []string, id string) []string {
	out := sig[:0:0]
	for _, line := range sig {
		if strings.HasPrefix(line, "TEXT_MESSAGE_") && strings.Contains(line, " "+id) &&
			(strings.HasSuffix(line, " "+id) || strings.Contains(line, " "+id+" ")) {
			continue
		}
		out = append(out, line)
	}
	return out
}
