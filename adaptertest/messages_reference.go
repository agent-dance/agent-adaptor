package adaptertest

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/v2/driver"
)

type referenceMessageDriver struct {
	referenceDriver
	messages   *referenceMessageScript
	capability driver.MessageCapability
}

func (d referenceMessageDriver) Descriptor() driver.Descriptor {
	desc := d.referenceDriver.Descriptor()
	desc.Messages = d.capability
	return desc
}

type referenceMessageScript struct {
	scenario MessageScenario
	evidence MessageEvidence
	history  []string
}

func referenceMessageCapability() driver.MessageCapability {
	return driver.MessageCapability{Boundaries: []driver.Boundary{driver.BoundaryImmediate, driver.BoundaryTool, driver.BoundaryStop}, Interrupt: true, Continue: driver.ContinueInRun, ConfirmedDelivery: true, PersistsDelivered: true, MaxUnits: 128, Unit: driver.MessageUnitUTF16}
}

// NewReferenceMessageFixture is a complete in-memory WithMessageFixture
// factory. It demonstrates independent provider receipts, confirmation loss,
// failed writes, process exit, cancellation/timeout, continuation and retained
// history. Real drivers replace this provider script with their fake CLI.
func NewReferenceMessageFixture(t *testing.T, scenario MessageScenario) MessageFixture {
	t.Helper()
	script := &referenceMessageScript{scenario: scenario}
	d := referenceMessageDriver{messages: script, capability: referenceMessageCapability()}
	return MessageFixture{Driver: d, Request: driver.Request{Prompt: "initial"}, Evidence: func() MessageEvidence { return script.evidence }}
}

func (d referenceMessageDriver) Run(ctx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
	s := d.messages
	s.evidence.Turns = nil
	s.evidence.Resumed = req.Session != nil && req.Session.State != nil
	if req.Session != nil && req.Session.State != nil {
		s.evidence.Retained = append([]string(nil), s.history...)
		s.evidence.Received = append(s.evidence.Received, req.Prompt)
	}
	port := req.Messages
	if port != nil {
		port.Window(true)
		defer port.Window(false)
	}
	turn := func(text string, interrupted bool) {
		usage := &driver.Usage{InputTokens: 2, OutputTokens: 1}
		tc := driver.TurnCompleted{Index: len(s.evidence.Turns), Text: text, Usage: usage, Interrupted: interrupted, CompletedAt: time.Now().UTC()}
		s.evidence.Turns = append(s.evidence.Turns, tc)
		_ = sink.Emit(driver.RunEvent{Type: driver.RunEventTurnCompleted, Turn: &tc})
	}
	deliver := func(items []driver.MessageItem) error {
		units := 0
		for _, item := range items {
			n := messageUnits(item.Text, d.capability.Unit)
			if units > 0 {
				n += 2
			}
			a := driver.MessageAck{Mechanism: "reference.provider"}
			switch {
			case ctx.Err() != nil:
				a.Err = ctx.Err()
			case s.scenario.Name == "write_failure" || s.scenario.Name == "process_exit":
				a.Err = errors.New("provider disconnected before receiving input")
				a.Requeue = true
			case units+n > d.capability.MaxUnits:
				a.Err = errors.New("provider envelope exceeded")
				a.Requeue = true
			default:
				units += n
				// This is the fake provider's receive side, independent of the port Ack.
				s.evidence.Received = append(s.evidence.Received, item.Text)
				s.history = append(s.history, item.Text)
				if s.scenario.Name == "missing_echo" {
					a.Err = errors.New("echo lost")
					a.Requeue = true
					a.MaybeDelivered = true
				} else {
					s.evidence.Confirmed = append(s.evidence.Confirmed, item.Text)
					a.Delivered = true
				}
			}
			port.Ack(item.ID, a)
		}
		return ctx.Err()
	}
	if port != nil && (req.Session == nil || req.Session.State == nil) {
		for _, b := range d.capability.Boundaries {
			if b == driver.BoundaryTurnEnd {
				continue
			}
			if b == driver.BoundaryStop && s.scenario.Boundary != driver.BoundaryStop {
				// Stop closes the turn and is also its turn-end delivery point;
				// only the Stop scenario's callback claims here. One Stop claim
				// returns Soon and AfterTurn input together in admission order.
				continue
			}
			if err := deliver(port.Claim(b)); err != nil {
				return driver.Response{}, err
			}
		}
	}
	interrupted := s.scenario.Name == "interrupt" && (req.Session == nil || req.Session.State == nil)
	turn("first", interrupted)
	if interrupted {
		turn("restart", false)
	}
	for port != nil {
		items := port.Claim(driver.BoundaryTurnEnd)
		if len(items) == 0 {
			break
		}
		if err := deliver(items); err != nil {
			return driver.Response{}, err
		}
		texts := make([]string, len(items))
		for i, item := range items {
			texts[i] = item.Text
		}
		turn(strings.Join(texts, "\n\n"), false)
	}
	if s.scenario.Name == "write_failure" || s.scenario.Name == "process_exit" {
		return driver.Response{ExitCode: 1}, nil
	}
	last := s.evidence.Turns[len(s.evidence.Turns)-1]
	var usage *driver.Usage
	for _, tc := range s.evidence.Turns {
		if tc.Usage != nil {
			if usage == nil {
				usage = &driver.Usage{}
			}
			usage.InputTokens += tc.Usage.InputTokens
			usage.OutputTokens += tc.Usage.OutputTokens
			usage.CachedInputTokens += tc.Usage.CachedInputTokens
			usage.EstimatedCostMilli += tc.Usage.EstimatedCostMilli
		}
	}
	return driver.Response{Output: last.Text, Usage: usage, Checkpoint: &driver.Checkpoint{Valid: true, State: &driver.SessionState{ResumeID: "reference-messages"}}}, nil
}
