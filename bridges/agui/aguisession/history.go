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
	opts     []agui.EventTranslatorOption
	t        *agui.EventTranslator
	terminal bool
	now      time.Time
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

// Events reads the entire projection state, returning only records after cursor.
// Inactive unterminated runs receive run.incomplete at their last record cursor.
func (s *Session) Events(ctx context.Context, key string, after sessionrecorder.HostSeq) ([]ProjectedRecord, error) {
	s.mu.Lock()
	active := s.active[key]
	s.mu.Unlock()
	// Admission can change between this snapshot and the read. Recheck activity
	// below before synthesizing an incomplete terminal for a newly started run.
	records, err := s.opts.Recorder.Since(ctx, RecorderKey(key), 0)
	if err != nil {
		return nil, err
	}
	p := newProjector(s.opts.Events)
	out := []ProjectedRecord{}
	for i, rec := range records {
		// A later run boundary also proves that an earlier run is no longer active.
		if n, ok := rec.Event.(adaptor.Notice); ok && n.Kind == BoundaryKind && p.t != nil && !p.terminal && i > 0 {
			previous := records[i-1]
			if previous.HostSeq > after {
				es := p.fail("run.incomplete", "run has no recorded terminal")
				for _, e := range es {
					e.GetBaseEvent().SetTimestamp(previous.RecordedAt.UnixMilli())
				}
				out = append(out, ProjectedRecord{previous.HostSeq, es})
			}
		}
		es := p.apply(rec)
		if rec.HostSeq > after {
			out = append(out, ProjectedRecord{rec.HostSeq, es})
		}
	}
	if !active && !s.Active(key) && p.t != nil && !p.terminal && len(records) > 0 {
		last := records[len(records)-1]
		if last.HostSeq > after {
			es := p.fail("run.incomplete", "run has no recorded terminal")
			for _, e := range es {
				e.GetBaseEvent().SetTimestamp(last.RecordedAt.UnixMilli())
			}
			out = append(out, ProjectedRecord{last.HostSeq, es})
		}
	}
	return out, nil
}

// Messages folds the recorded projection into AG-UI Message objects. Reasoning
// remains role=reasoning; assistant-ui fromAgUiMessages gates it on showThinking.
func (s *Session) Messages(ctx context.Context, key string) ([]Message, error) {
	records, err := s.Events(ctx, key, 0)
	if err != nil {
		return nil, err
	}
	f := []Message{}
	for _, r := range records {
		for _, e := range r.Events {
			f = Fold(f, e)
		}
	}
	return f, nil
}

// EventsHandler returns JSON projected records for ?thread_id=T&after=N.
// Authorization runs before reading; resolver mapping is shared with POST.
func (s *Session) EventsHandler() http.Handler { return s.historyHandler(false) }

// MessagesHandler returns a JSON Message[] for ?thread_id=T.
func (s *Session) MessagesHandler() http.Handler { return s.historyHandler(true) }

func (s *Session) historyHandler(messages bool) http.Handler {
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
		if messages {
			value, err = s.Messages(r.Context(), key)
		} else {
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
