package aguisession

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	events "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/bridges/agui"
	"github.com/agent-dance/agent-adaptor/bridges/internal/bridgekey"
	"github.com/agent-dance/agent-adaptor/hosttools/sessionrecorder"
)

// ProjectedRecord preserves source-record cursor granularity. One record may
// yield multiple events; clients must consume the whole record before advancing.
type ProjectedRecord struct {
	// HostSeq is the recorder-assigned thread cursor.
	HostSeq sessionrecorder.HostSeq `json:"host_seq"`
	// Events is the ordered projection of this record; JSON encodes [] when empty.
	Events []events.Event `json:"events"`
}

// MarshalJSON keeps the event list an array even for source records with no
// public projection, including filtered notices and deferred message openers.
func (r ProjectedRecord) MarshalJSON() ([]byte, error) {
	type record ProjectedRecord
	if r.Events == nil {
		r.Events = []events.Event{}
	}
	return json.Marshal(record(r))
}

type projector struct {
	opts           []agui.EventTranslatorOption
	t              *agui.EventTranslator
	terminal       bool
	incomplete     bool
	lateIncomplete bool
	now            time.Time
	last           sessionrecorder.EventRecord
	runID          string
	userMessageID  string
	hasUserMessage bool
}

type cancelledTerminal string

func (e cancelledTerminal) Error() string { return string(e) }

func (e cancelledTerminal) Unwrap() error { return context.Canceled }

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
	var err error = &adaptor.RunError{Reason: adaptor.FailureReason(code), Message: message}
	if code == "run.cancelled" {
		err = cancelledTerminal(message)
	}
	out := p.t.CloseRun(err)
	for _, ev := range out {
		ev.GetBaseEvent().SetTimestamp(p.now.UnixMilli())
	}
	return out
}

func (p *projector) apply(rec sessionrecorder.EventRecord) []events.Event {
	p.last = rec
	p.now = rec.RecordedAt
	var out []events.Event
	if n, ok := rec.Event.(adaptor.Notice); ok && n.Kind == BoundaryKind {
		p.runID = str(n.Data, "runId")
		p.userMessageID = str(n.Data, "userMessageId")
		p.hasUserMessage = n.Data["userMessage"] != nil
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
		if p.hasUserMessage {
			// defaultApplyEvents RUN_STARTED adds missing input messages by ID. A
			// live client retains its existing user; cold replay receives its exact
			// name, metadata and content-part representation without a text append.
			raw, _ := json.Marshal(n.Data["userMessage"])
			var submitted agui.Message
			_ = json.Unmarshal(raw, &submitted)
			out[0].(*agui.RunStartedEvent).Input = &agui.RunAgentInput{
				ThreadID: str(n.Data, "threadId"), RunID: p.runID,
				Messages: []agui.Message{submitted},
			}
		}
		if str(n.Data, "projectionVersion") != ProjectionVersion {
			out = append(out, p.fail("agui.projection_version_unsupported", "unsupported or missing recording projection version")...)
		}
	} else if p.t != nil {
		if n, ok := rec.Event.(adaptor.Notice); ok && n.Kind == TerminalKind {
			// Stream.RunID and the resolved key are recorded even when the
			// provider emitted no source events. Update audit metadata without
			// letting its informational lifecycle select the outcome.
			p.t.Translate(adaptor.WithEventMeta(adaptor.RunFinished{}, n.Meta()))
			if code := str(n.Data, "code"); code != "" {
				out = p.fail(code, str(n.Data, "message"))
			} else {
				p.terminal = true
				out = p.t.CloseRun(nil)
			}
		} else {
			if e, ok := rec.Event.(adaptor.TextDelta); ok && p.hasUserMessage &&
				e.Role == adaptor.RoleUser && e.MessageID == p.userMessageID {
				return nil
			}
			meta := rec.Event.Meta()
			if meta.Time.IsZero() {
				meta.Time = rec.RecordedAt
			}
			out = p.t.Translate(adaptor.WithEventMeta(p.scopeSource(rec.Event), meta))
		}
	}
	// Constructors use wall-clock timestamps; recorded time is the replay authority.
	for _, ev := range out {
		ev.GetBaseEvent().SetTimestamp(rec.RecordedAt.UnixMilli())
	}
	return out
}

// Provider message and tool IDs are run-local (Claude reasoning IDs, for
// example, use thinking-0). Scope them before translation so the client cannot
// append a new turn to an earlier turn with the same provider ID. Submitted user
// IDs and approval request IDs remain client/host identities respectively.
func (p *projector) scopeSource(ev adaptor.Event) adaptor.Event {
	messageID := func(id string) string {
		if id == "" {
			return ""
		}
		return bridgekey.Encode("agui-message", p.runID, id)
	}
	toolID := func(id string) string {
		if id == "" {
			return ""
		}
		return bridgekey.Encode("agui-tool", p.runID, id)
	}
	switch e := ev.(type) {
	case adaptor.TextDelta:
		if e.Role != adaptor.RoleUser || e.MessageID != p.userMessageID {
			e.MessageID = messageID(e.MessageID)
		}
		return e
	case adaptor.Thinking:
		e.MessageID = messageID(e.MessageID)
		return e
	case adaptor.ToolCall:
		e.ID = toolID(e.ID)
		return e
	case adaptor.ToolResult:
		e.ID = toolID(e.ID)
		return e
	case adaptor.SubagentUpdate:
		data := make(map[string]any, len(e.Data)+1)
		for k, v := range e.Data {
			data[k] = v
		}
		id := str(data, "delegation_id")
		if id == "" {
			id = e.Agent
		}
		data["delegation_id"] = messageID(id)
		for _, field := range []string{"parent_tool_call_id", "remote_tool_call_id"} {
			if id := str(data, field); id != "" {
				data[field] = toolID(id)
			}
		}
		e.Data = data
		return e
	default:
		return ev
	}
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

// Events reads the entire projection state, returning only records after cursor.
// Inactive unterminated runs receive run.incomplete at their last record cursor.
func (s *Session) Events(ctx context.Context, key string, after sessionrecorder.HostSeq) ([]ProjectedRecord, error) {
	records, state, err := s.readRecords(ctx, key, 0, false)
	if err != nil {
		return nil, err
	}
	p := newProjector(s.opts.Events)
	return p.project(records, after, !state.active, s.incompleteCodeFor(state)), nil
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
			writeError(w, &agui.InputError{Code: "agui.identity_required", Message: "thread_id required"})
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
				writeError(w, &agui.InputError{Code: "agui.invalid_cursor", Message: "after must be an unsigned integer"})
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
