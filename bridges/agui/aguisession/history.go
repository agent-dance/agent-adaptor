package aguisession

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	events "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/bridges/agui"
	"github.com/agent-dance/agent-adaptor/hosttools/sessionrecorder"
)

// ProjectedRecord preserves source-record cursor granularity. One record may
// yield multiple events; clients must consume the whole record before advancing.
type ProjectedRecord struct {
	// HostSeq is the recorder-assigned thread cursor.
	HostSeq sessionrecorder.HostSeq `json:"host_seq"`
	// Events is the ordered projection of this record.
	Events []events.Event `json:"events"`
}

type projector struct {
	opts           []agui.EventTranslatorOption
	t              *agui.EventTranslator
	terminal       bool
	incomplete     bool
	lateIncomplete bool
	now            time.Time
	last           sessionrecorder.EventRecord
}

func newProjector(opts []agui.EventTranslatorOption) *projector {
	return &projector{opts: opts}
}

func str(m map[string]any, k string) string {
	v, _ := m[k].(string)
	return v
}

func (p *projector) fail(code, message string) []events.Event {
	if p.t == nil {
		return nil
	}
	p.terminal = true
	return p.t.CloseRun(&adaptor.RunError{Reason: adaptor.FailureReason(code), Message: message})
}

func (p *projector) apply(rec sessionrecorder.EventRecord) []events.Event {
	p.last = rec
	p.now = rec.RecordedAt
	var out []events.Event
	if n, ok := rec.Event.(adaptor.Notice); ok && n.Kind == BoundaryKind {
		opts := append([]agui.EventTranslatorOption(nil), p.opts...)
		var diagnostics []agui.Diagnostic
		raw, _ := json.Marshal(n.Data["diagnostics"])
		_ = json.Unmarshal(raw, &diagnostics)
		opts = append(opts,
			agui.WithEventClock(func() time.Time { return p.now }),
			agui.WithRunIdentity(str(n.Data, "threadId"), str(n.Data, "runId"), str(n.Data, "parentRunId")),
			agui.WithInputDiagnostics(diagnostics),
		)
		p.t = agui.NewEventTranslator(opts...)
		p.terminal = false
		p.incomplete = false
		out = p.t.Translate(adaptor.RunStarted{})
	} else if p.t != nil {
		if n, ok := rec.Event.(adaptor.Notice); ok && n.Kind == TerminalKind {
			if code := str(n.Data, "code"); code != "" {
				out = p.fail(code, str(n.Data, "message"))
			} else {
				p.terminal = true
				out = p.t.CloseRun(nil)
			}
		} else {
			meta := rec.Event.Meta()
			if meta.Time.IsZero() {
				meta.Time = rec.RecordedAt
			}
			out = p.t.Translate(adaptor.WithEventMeta(rec.Event, meta))
		}
	}
	// Constructors use wall-clock timestamps; recorded time is the replay authority.
	for _, ev := range out {
		ev.GetBaseEvent().SetTimestamp(rec.RecordedAt.UnixMilli())
	}
	return out
}

// project advances the same state for full-history reads and incremental follow.
// after is a delivery cursor; last is a separate raw-record cursor so an observer
// can reconstruct the prefix once without emitting it or reading it again.
func (p *projector) project(
	records []sessionrecorder.EventRecord, after sessionrecorder.HostSeq,
	finish bool, incompleteCode string,
) []ProjectedRecord {
	p.lateIncomplete = false
	out := []ProjectedRecord{}
	for _, rec := range records {
		if rec.HostSeq <= p.last.HostSeq {
			continue
		}
		if n, ok := rec.Event.(adaptor.Notice); ok && n.Kind == BoundaryKind {
			// A new boundary proves the previous run ended, even without a terminal.
			out = p.closeIncomplete(out, after, incompleteCode)
		}
		es := p.apply(rec)
		if rec.HostSeq > after {
			out = append(out, ProjectedRecord{HostSeq: rec.HostSeq, Events: es})
		}
	}
	if finish {
		out = p.closeIncomplete(out, after, incompleteCode)
	}
	return out
}

func (p *projector) closeIncomplete(
	out []ProjectedRecord, after sessionrecorder.HostSeq, code string,
) []ProjectedRecord {
	if p.t == nil || p.terminal {
		return out
	}
	p.incomplete = true
	es := p.fail(code, "run has no recorded terminal")
	if p.last.HostSeq <= after {
		// Already delivered records cannot acquire new events at an acknowledged
		// cursor. The observer reports a transport error instead of inventing a seq.
		p.lateIncomplete = true
		return out
	}
	for _, ev := range es {
		ev.GetBaseEvent().SetTimestamp(p.last.RecordedAt.UnixMilli())
	}
	if len(out) > 0 && out[len(out)-1].HostSeq == p.last.HostSeq {
		out[len(out)-1].Events = append(out[len(out)-1].Events, es...)
	} else {
		out = append(out, ProjectedRecord{HostSeq: p.last.HostSeq, Events: es})
	}
	return out
}

func (s *Session) incompleteCode(key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.opts.Unattended && s.recordingFailures[key] {
		return "agui.recording_failed"
	}
	return "run.incomplete"
}

// Events reads the entire projection state, returning only records after cursor.
// Inactive unterminated runs receive run.incomplete at their last record cursor.
func (s *Session) Events(ctx context.Context, key string, after sessionrecorder.HostSeq) ([]ProjectedRecord, error) {
	active := s.Active(key)
	// Admission can change during I/O. Recheck before diagnosing an incomplete run.
	records, err := s.opts.Recorder.Since(ctx, RecorderKey(key), 0)
	if err != nil {
		return nil, err
	}
	p := newProjector(s.opts.Events)
	return p.project(records, after, !active && !s.Active(key), s.incompleteCode(key)), nil
}

// Messages folds the recorded projection into AG-UI Message objects. Reasoning
// remains role=reasoning; assistant-ui fromAgUiMessages gates it on showThinking.
func (s *Session) Messages(ctx context.Context, key string) ([]Message, error) {
	records, err := s.Events(ctx, key, 0)
	if err != nil {
		return nil, err
	}
	return foldRecords(records), nil
}

func foldRecords(records []ProjectedRecord) []Message {
	f := []Message{}
	for _, r := range records {
		for _, e := range r.Events {
			f = Fold(f, e)
		}
	}
	return f
}

// EventsHandler returns JSON projected records for ?thread_id=T&after=N.
// Authorization runs before reading; resolver mapping is shared with POST.
func (s *Session) EventsHandler() http.Handler { return s.historyHandler(historyEvents) }

// MessagesHandler returns a JSON Message[] for ?thread_id=T.
func (s *Session) MessagesHandler() http.Handler { return s.historyHandler(historyMessages) }

type historyView uint8

const (
	historyEvents historyView = iota
	historyMessages
	historySnapshot
)

func (s *Session) historyHandler(view historyView) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			methodError(w, http.MethodGet)
			return
		}
		id := r.URL.Query().Get("thread_id")
		if id == "" {
			writeError(w, fmt.Errorf("thread_id required"))
			return
		}
		key, err := s.resolve(r, Access{Op: OpRead, ThreadID: id})
		if err != nil {
			writeError(w, err)
			return
		}
		after := uint64(0)
		if raw := r.URL.Query().Get("after"); raw != "" {
			after, err = strconv.ParseUint(raw, 10, 64)
			if err != nil {
				writeError(w, err)
				return
			}
		}
		var value any
		switch view {
		case historyMessages:
			value, err = s.Messages(r.Context(), key)
		case historySnapshot:
			value, err = s.Snapshot(r.Context(), key)
		default:
			value, err = s.Events(r.Context(), key, after)
		}
		if err != nil {
			writeError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(value)
	})
}
