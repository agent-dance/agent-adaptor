package main

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
)

// All report types are private: this executable defines no SDK API.
type report struct {
	Version int       `json:"version"`
	Agent   string    `json:"agent"`
	Effort  string    `json:"effort"`
	Started time.Time `json:"started"`
	Ended   time.Time `json:"ended"`
	Passed  bool      `json:"passed"`
	Steps   []*step   `json:"steps"`
}
type step struct {
	Number           int           `json:"number"`
	Name             string        `json:"name"`
	Section          string        `json:"proposal_section"`
	Started          time.Time     `json:"started"`
	Ended            time.Time     `json:"ended"`
	Workspace        string        `json:"workspace,omitempty"`
	Calls            []hostCall    `json:"host_calls"`
	Timeline         []eventRecord `json:"timeline"`
	Checks           []check       `json:"checks"`
	Passed           bool          `json:"passed"`
	EnvironmentError string        `json:"environment_error,omitempty"`
}
type hostCall struct {
	At         time.Time `json:"at"`
	Method     string    `json:"method"`
	Arguments  any       `json:"arguments,omitempty"`
	RunID      string    `json:"run_id,omitempty"`
	Error      string    `json:"error,omitempty"`
	ErrorClass string    `json:"error_class"`
}
type check struct {
	Name     string `json:"name"`
	Expected any    `json:"expected"`
	Observed any    `json:"observed"`
	Passed   bool   `json:"passed"`
}
type eventRecord struct {
	Order      int            `json:"order"`
	At         time.Time      `json:"at"`
	Type       string         `json:"type"`
	RunID      string         `json:"run_id"`
	Sequence   uint64         `json:"sequence"`
	Turn       int            `json:"turn,omitempty"`
	ID         string         `json:"message_id,omitempty"`
	State      string         `json:"state,omitempty"`
	Boundary   string         `json:"boundary,omitempty"`
	Mechanism  string         `json:"mechanism,omitempty"`
	Reason     string         `json:"reason,omitempty"`
	Text       string         `json:"text,omitempty"`
	MessageIDs []string       `json:"message_ids,omitempty"`
	Task       string         `json:"task,omitempty"`
	Member     string         `json:"member,omitempty"`
	Kind       string         `json:"kind,omitempty"`
	ErrorClass string         `json:"error_class,omitempty"`
	Usage      *adaptor.Usage `json:"usage,omitempty"`
}

func errorClass(err error) string {
	switch {
	case err == nil:
		return "none"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, adaptor.ErrThreadBusy):
		return "thread_busy"
	case errors.Is(err, adaptor.ErrRunFinished):
		return "run_finished"
	case errors.Is(err, adaptor.ErrMessageUnsupported):
		return "message_unsupported"
	}
	var re *adaptor.RunError
	if errors.As(err, &re) {
		return "run_error:" + string(re.Reason)
	}
	return fmt.Sprintf("%T", err)
}
func (s *step) call(method string, args any, runID string, err error) {
	c := hostCall{At: time.Now().UTC(), Method: method, Arguments: args, RunID: runID, ErrorClass: errorClass(err)}
	if err != nil {
		c.Error = err.Error()
	}
	s.Calls = append(s.Calls, c)
}
func (s *step) equal(name string, want, got any) {
	s.expect(name, want, got, reflect.DeepEqual(want, got))
}
func (s *step) expect(name string, want, got any, pass bool) {
	s.Checks = append(s.Checks, check{name, reportValue(want), reportValue(got), pass})
}
func (s *step) finish() {
	s.Ended = time.Now().UTC()
	s.Passed = len(s.Checks) > 0
	for _, c := range s.Checks {
		s.Passed = s.Passed && c.Passed
	}
}
func excerpt(s string) string {
	r := []rune(s)
	if len(r) > 240 {
		return string(r[:240]) + "…"
	}
	return s
}
func recordEvent(e adaptor.Event) eventRecord {
	m := e.Meta()
	r := eventRecord{At: m.Time, Type: strings.TrimPrefix(fmt.Sprintf("%T", e), "adaptor."), RunID: m.RunID, Sequence: m.Sequence}
	switch e := e.(type) {
	case adaptor.MessageUpdate:
		r.ID = e.ID
		r.State = messageState(e.State)
		r.Turn = e.Turn
		r.Boundary = e.Boundary
		r.Mechanism = e.Mechanism
		r.Reason = string(e.Reason)
		r.Text = e.Text
	case adaptor.TurnEnd:
		r.Turn = e.Turn
		r.MessageIDs = e.MessageIDs
		r.ErrorClass = errorClass(e.Err)
		if e.Result != nil {
			r.Text = excerpt(e.Result.Text)
			r.Usage = e.Result.Usage
		}
	case adaptor.Done:
		r.ErrorClass = errorClass(e.Err)
		if e.Result != nil {
			r.Turn = e.Result.Turns
			r.Text = excerpt(e.Result.Text)
			r.Usage = e.Result.Usage
		}
	case adaptor.SubagentUpdate:
		r.Task = e.Task
		r.Member = e.Member
		r.State = e.State
		r.Kind = string(e.Kind)
		r.Text = excerpt(e.Summary)
	case adaptor.Notice:
		r.Kind = e.Kind
	case adaptor.ToolCall:
		r.Kind = e.Name
		r.State = string(e.Phase)
	case adaptor.ProcessInfo:
		r.Kind = e.Kind
	}
	return r
}
func messageState(s adaptor.MessageState) string {
	switch s {
	case adaptor.MessageAccepted:
		return "accepted"
	case adaptor.MessageDelivered:
		return "delivered"
	case adaptor.MessageFailed:
		return "failed"
	}
	return fmt.Sprint(s)
}

type observer struct {
	events <-chan adaptor.Event
	all    []adaptor.Event
	done   *adaptor.Done
	closed bool
	order  *int
}

func observe(events <-chan adaptor.Event, order *int) *observer {
	return &observer{events: events, order: order}
}
func (o *observer) next(ctx context.Context, s *step) (adaptor.Event, bool) {
	if o.closed {
		return nil, false
	}
	select {
	case e, ok := <-o.events:
		if !ok {
			o.closed = true
			return nil, false
		}
		o.all = append(o.all, e)
		r := recordEvent(e)
		*o.order++
		r.Order = *o.order
		s.Timeline = append(s.Timeline, r)
		if d, ok := e.(adaptor.Done); ok {
			o.done = &d
		}
		return e, true
	case <-ctx.Done():
		s.expect("event wait completed", "event before deadline", ctx.Err().Error(), false)
		return nil, false
	}
}
func (o *observer) until(ctx context.Context, s *step, predicate func(adaptor.Event) bool) adaptor.Event {
	for {
		e, ok := o.next(ctx, s)
		if !ok {
			return nil
		}
		if predicate(e) {
			return e
		}
	}
}
func (o *observer) drain(ctx context.Context, s *step) {
	for !o.closed {
		if _, ok := o.next(ctx, s); !ok {
			return
		}
	}
}
func isTool(e adaptor.Event) bool {
	t, ok := e.(adaptor.ToolCall)
	return ok && t.Phase == adaptor.PhaseStart
}
func isDone(e adaptor.Event) bool { _, ok := e.(adaptor.Done); return ok }
func delivered(id string) func(adaptor.Event) bool {
	return func(e adaptor.Event) bool {
		m, ok := e.(adaptor.MessageUpdate)
		return ok && m.ID == id && m.State == adaptor.MessageDelivered
	}
}
func turnEnd(e adaptor.Event) bool { _, ok := e.(adaptor.TurnEnd); return ok }
func (o *observer) outcomes(id string) []adaptor.MessageUpdate {
	var out []adaptor.MessageUpdate
	for _, e := range o.all {
		if m, ok := e.(adaptor.MessageUpdate); ok && m.ID == id {
			out = append(out, m)
		}
	}
	return out
}
func text(r *adaptor.Result) string {
	if r == nil {
		return ""
	}
	return r.Text
}

func reportValue(v any) any {
	if err, ok := v.(error); ok && err != nil {
		return map[string]string{"message": err.Error(), "class": errorClass(err)}
	}
	return v
}
