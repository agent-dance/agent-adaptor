package aguisession

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"github.com/agent-dance/agent-adaptor/v2/bridges/agui"
)

// maxMessageBytes bounds a supplemental-input request body.
const maxMessageBytes = 4 << 20

var errRecordingFailed = errors.New("aguisession: event recording failed")

// liveRun is the admission state for in-run input to one active execution.
// The run's drain is the only recorder writer; supplements reach it through
// inbox and are recorded in drain order, before Done and the terminal.
type liveRun struct {
	stream   adaptor.Stream
	threadID string
	runID    string
	inbox    chan supplement

	mu       sync.Mutex
	sealed   bool
	inflight int
	idle     chan struct{}
	ids      map[string]bool
}

type supplement struct {
	id, text string
	recorded chan error
}

func (s *Session) openLive(key string, in *agui.RunAgentInput, stream adaptor.Stream) *liveRun {
	live := &liveRun{stream: stream, threadID: in.ThreadID, runID: in.RunID,
		inbox: make(chan supplement), ids: map[string]bool{}}
	s.mu.Lock()
	s.live[key] = live
	s.mu.Unlock()
	return live
}

func (s *Session) closeLive(key string, live *liveRun) {
	s.mu.Lock()
	if s.live[key] == live {
		delete(s.live, key)
	}
	s.mu.Unlock()
}

// enter reserves an admission slot; it fails once the drain has sealed the run.
func (l *liveRun) enter(id string) (duplicate, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.sealed {
		return false, false
	}
	l.inflight++
	return l.ids[id], true
}

func (l *liveRun) leave(id string, recorded bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if recorded {
		l.ids[id] = true
	}
	l.inflight--
	if l.inflight == 0 && l.idle != nil {
		close(l.idle)
		l.idle = nil
	}
}

// seal stops new admissions and records supplements already in flight. After
// Events closes, Stream.Send can no longer admit input, so waiting terminates.
func (l *liveRun) seal(record func(supplement)) {
	l.mu.Lock()
	l.sealed = true
	var idle chan struct{}
	if l.inflight > 0 {
		idle = make(chan struct{})
		l.idle = idle
	}
	l.mu.Unlock()
	if idle == nil {
		return
	}
	for {
		select {
		case sup := <-l.inbox:
			record(sup)
		case <-idle:
			return
		}
	}
}

// Accepted describes supplemental input admitted to an active run.
type Accepted struct {
	// ThreadID and RunID are the run's public AG-UI identities.
	ThreadID string `json:"threadId"`
	RunID    string `json:"runId"`
	// MessageID is the client message identity, retained in history.
	MessageID string `json:"messageId"`
	// State is always "accepted"; delivery is reported by message.update events
	// on the original run's event stream.
	State string `json:"state"`
}

// Supplement admits user text to the active run at an authorized opaque key
// through Stream.Send, using the human author and Soon timing. It never starts
// a run: without an active run it returns a MessageError wrapping
// adaptor.ErrRunFinished. Admission is not delivery; message.update events on
// the original run report the outcome. Accepted input is recorded once as a
// user message (retaining messageID) in the run's history before its terminal
// and is echoed to the run's live observers; a rejected input creates no
// history. ctx bounds admission and unclaimed input, as for Stream.Send.
//
// A non-nil error with a non-empty Accepted means admission succeeded but the
// history write failed; the run is then cancelled and records
// agui.recording_failed. Retrying with the same messageID is safe.
func (s *Session) Supplement(ctx context.Context, key, messageID, text string) (Accepted, error) {
	if messageID == "" || text == "" {
		return Accepted{}, &agui.InputError{Code: "agui.invalid_message", Message: "messageId and nonempty text are required"}
	}
	if err := ctx.Err(); err != nil {
		return Accepted{}, err
	}
	s.mu.Lock()
	live := s.live[key]
	s.mu.Unlock()
	inactive := &adaptor.MessageError{ID: messageID, Text: text, Reason: adaptor.MessageRunFailed, Err: adaptor.ErrRunFinished}
	if live == nil {
		return Accepted{}, inactive
	}
	duplicate, ok := live.enter(messageID)
	if !ok {
		return Accepted{}, inactive
	}
	recorded := false
	defer func() { live.leave(messageID, recorded) }()
	if !duplicate {
		if err := s.requireNewMessageID(ctx, key, messageID); err != nil {
			return Accepted{}, err
		}
	}
	err := live.stream.Send(ctx, text,
		adaptor.MessageID(messageID), adaptor.From(adaptor.Author{Kind: adaptor.AuthorHuman}))
	if err != nil {
		return Accepted{}, err
	}
	accepted := Accepted{ThreadID: live.threadID, RunID: live.runID, MessageID: messageID, State: "accepted"}
	if duplicate {
		// The core deduplicates repeated IDs; history already holds this input.
		recorded = true
		return accepted, nil
	}
	sup := supplement{id: messageID, text: text, recorded: make(chan error, 1)}
	// The drain receives until this admission leaves, so the hand-off and the
	// bounded recorder write cannot outlive the run.
	live.inbox <- sup
	if err := <-sup.recorded; err != nil {
		return accepted, &agui.InputError{Status: http.StatusInternalServerError,
			Code: "agui.recording_failed", Message: "input was accepted but history recording failed"}
	}
	recorded = true
	return accepted, nil
}

// requireNewMessageID keeps client message IDs unique across recorded history:
// folding merges equal IDs, which would corrupt both live and replayed views.
func (s *Session) requireNewMessageID(ctx context.Context, key, id string) error {
	records, err := s.opts.Recorder.Since(ctx, RecorderKey(key), 0)
	if err != nil {
		return err
	}
	for _, rec := range records {
		switch e := rec.Event.(type) {
		case adaptor.Notice:
			if (e.Kind == BoundaryKind && str(e.Data, "userMessageId") == id) ||
				(e.Kind == SupplementKind && str(e.Data, "messageId") == id) {
				return &agui.InputError{Code: "agui.duplicate_message", Status: http.StatusConflict,
					Message: "message ID already appears in recorded history"}
			}
		case adaptor.TextDelta:
			if e.Role == adaptor.RoleUser && e.MessageID == id {
				return &agui.InputError{Code: "agui.duplicate_message", Status: http.StatusConflict,
					Message: "message ID already appears in recorded history"}
			}
		}
	}
	return nil
}

// MessageHandler returns the companion POST endpoint for in-run input while
// the run's own AG-UI stream stays open. It accepts exactly one JSON object
// {"threadId","messageId","text"}, authorizes OpMessage through Resolve and
// answers 202 with Accepted. It never creates an AG-UI run or event consumer.
// Once dispatched, the input belongs to the run: cancelling this HTTP request
// does not withdraw it. Status codes: 400 malformed, 409 no active run
// (agui.run_inactive) or duplicate ID, 413 too large, 422 rejected by the run
// (agui.message_rejected), 500 recording failure after acceptance (the body
// then still carries state "accepted").
func (s *Session) MessageHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			methodError(w, http.MethodPost)
			return
		}
		var request struct {
			ThreadID  string `json:"threadId"`
			MessageID string `json:"messageId"`
			Text      string `json:"text"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxMessageBytes))
		decoder.DisallowUnknownFields()
		err := decoder.Decode(&request)
		if err == nil && !errors.Is(decoder.Decode(&struct{}{}), io.EOF) {
			err = errors.New("a single JSON object is required")
		}
		if err != nil {
			var large *http.MaxBytesError
			if errors.As(err, &large) {
				writeError(w, err)
				return
			}
			writeError(w, &agui.InputError{Code: "agui.invalid_message", Message: err.Error()})
			return
		}
		if request.ThreadID == "" {
			writeError(w, &agui.InputError{Code: "agui.identity_required", Message: "threadId required"})
			return
		}
		key, err := s.resolve(r, Access{Op: OpMessage, ThreadID: request.ThreadID})
		if err != nil {
			writeError(w, err)
			return
		}
		if err := r.Context().Err(); err != nil {
			writeError(w, &agui.InputError{Status: http.StatusRequestTimeout, Code: "agui.request_cancelled", Message: err.Error()})
			return
		}
		accepted, err := s.Supplement(context.WithoutCancel(r.Context()), key, request.MessageID, request.Text)
		if err != nil && accepted.State == "" {
			writeError(w, messageAdmissionError(err))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		status := http.StatusAccepted
		body := map[string]any{"threadId": accepted.ThreadID, "runId": accepted.RunID,
			"messageId": accepted.MessageID, "state": accepted.State}
		if err != nil {
			status = http.StatusInternalServerError
			body["error"] = err
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	})
}

func messageAdmissionError(err error) error {
	var in *agui.InputError
	if errors.As(err, &in) {
		return err
	}
	var rejected *adaptor.MessageError
	switch {
	case errors.Is(err, adaptor.ErrRunFinished):
		return &agui.InputError{Status: http.StatusConflict, Code: "agui.run_inactive",
			Message: "thread has no active run; start a new run instead"}
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return &agui.InputError{Status: http.StatusRequestTimeout, Code: "agui.request_cancelled", Message: err.Error()}
	case errors.As(err, &rejected):
		return &agui.InputError{Status: http.StatusUnprocessableEntity, Code: "agui.message_rejected", Message: err.Error()}
	}
	return err
}
