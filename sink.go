package adaptor

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/activebudget"
)

// defaultEventBuffer is the unified event channel buffer when
// WithEventBuffer is not used.
const defaultEventBuffer = 1024

// eventSink implements driver.EventSink: it translates RunEvents
// and StreamPayloads into typed Events on one channel, applies the
// backpressure strategy, and implements driver.DecisionCapableSink so
// approval requests route through the unified stream / OnApproval callback.
type eventSink struct {
	events <-chan Event
	broker *eventBroker

	runID   string
	policy  ApprovalPolicy // effective (defaults materialized)
	handler ApprovalHandler
	caps    driver.RunPolicyCapabilities

	// retryWarned dedupes the retry-degradation warning per kind.
	retryMu     sync.Mutex
	retryWarned map[driver.HumanDecisionKind]struct{}

	// Each approval has its own token and responder; no lock spans human wait.
	decSeq   atomic.Uint64
	budget   *activebudget.Controller
	terminal invocationTerminal

	// outstanding tracks unanswered event-form requests so close() can
	// expire them (a response after run end fails fast).
	outstandingMu sync.Mutex
	outstanding   map[string]*ApprovalRequest

	failMu  sync.Mutex
	failure *driver.RunFailure

	// Core owns every admitted run envelope, including Driver-only execution.
	// The final Go outcome, after teardown, determines the one public terminal.
	lifecycleMu      sync.Mutex
	lifecycleActive  bool
	lifecycleEnded   bool
	providerRunID    string
	providerThreadID string
	driverTerminal   *RunFinished
	terminalSource   *EventSourceMeta
	observerCtx      context.Context
	observerInfo     RunEventInfo
	observers        []runObserverState
	observerDeadline atomic.Int64 // earliest cleanup deadline, independent of publication
}

type eventSinkConfig struct {
	runID     string
	threadKey string
	buffer    int
	blocking  bool
	policy    ApprovalPolicy
	handler   ApprovalHandler
	caps      driver.RunPolicyCapabilities
}

func newEventSink(cfg eventSinkConfig) *eventSink {
	buf := cfg.buffer
	if buf <= 0 {
		buf = defaultEventBuffer
	}
	broker := newEventBroker(cfg.runID, cfg.threadKey, buf, cfg.blocking)
	return &eventSink{
		events:      broker.events,
		broker:      broker,
		runID:       cfg.runID,
		policy:      effectiveApprovalPolicy(cfg.policy),
		handler:     cfg.handler,
		caps:        cfg.caps,
		retryWarned: map[driver.HumanDecisionKind]struct{}{},
		outstanding: map[string]*ApprovalRequest{},
	}
}

// ---------------------------------------------------------------------------
// EventSink: translation + backpressure
// ---------------------------------------------------------------------------

func (s *eventSink) Emit(ev driver.RunEvent) error {
	var source *EventSourceMeta
	if ev.Seq != 0 || !ev.Timestamp.IsZero() {
		source = &EventSourceMeta{Sequence: ev.Seq, Timestamp: ev.Timestamp}
	}
	s.pushWithSource(eventFromRunEvent(ev), source)
	return nil
}

func (s *eventSink) EmitStream(p driver.StreamPayload) error {
	if err := validateObservationPayload(p); err != nil {
		return err
	}
	source := sourceMetaFromStreamPayload(p)
	if s.captureDriverLifecycle(p, source) {
		return nil
	}
	event := eventFromStreamPayload(p)
	if p.Kind == driver.StreamRunFinished || p.Kind == driver.StreamRunError {
		// In a Driver-only stream the provider owns the lifecycle envelope.
		// Its terminal is still a hard public boundary: use the broker's
		// reserved terminal slot and atomically seal out any later payloads.
		s.broker.publishTerminal(event, source)
		return nil
	}
	s.pushWithSource(event, source)
	return nil
}

func sourceMetaFromStreamPayload(p driver.StreamPayload) *EventSourceMeta {
	sequence := p.Sequence
	if sequence == 0 {
		sequence = p.Seq
	}
	if p.RunID == "" && p.ThreadID == "" && p.TurnID == "" && sequence == 0 && p.Timestamp.IsZero() {
		return nil
	}
	return &EventSourceMeta{
		RunID: p.RunID, ThreadID: p.ThreadID, TurnID: p.TurnID,
		Sequence: sequence, Timestamp: p.Timestamp,
	}
}

// enableAuthoritativeLifecycle starts the core-owned public run envelope
// before resource acquisition or Driver dispatch.
func (s *eventSink) enableAuthoritativeLifecycle() {
	if s == nil || s.broker == nil {
		return
	}
	s.lifecycleMu.Lock()
	if s.lifecycleActive {
		s.lifecycleMu.Unlock()
		return
	}
	s.lifecycleActive = true
	s.lifecycleMu.Unlock()
	s.push(RunStarted{RunID: s.runID})
}

// captureDriverLifecycle suppresses duplicate Driver run-envelope events
// while the core owns the merged lifecycle. Non-lifecycle payloads continue
// through the broker immediately. The terminal is retained as a source of
// provider coordinates/usage, but the final Go outcome remains authoritative.
func (s *eventSink) captureDriverLifecycle(p driver.StreamPayload, source *EventSourceMeta) bool {
	if s == nil {
		return false
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if !s.lifecycleActive {
		return false
	}
	switch p.Kind {
	case driver.StreamRunStarted:
		if p.RunID != "" {
			s.providerRunID = p.RunID
		}
		if p.ThreadID != "" {
			s.providerThreadID = p.ThreadID
		}
		return true
	case driver.StreamRunFinished, driver.StreamRunError:
		if s.driverTerminal == nil {
			terminal, _ := eventFromStreamPayload(p).(RunFinished)
			if terminal.Usage != nil {
				usage := *terminal.Usage
				terminal.Usage = &usage
			}
			s.driverTerminal = &terminal
			s.terminalSource = cloneEventSourceMeta(source)
		}
		return true
	default:
		return s.driverTerminal != nil
	}
}

// completeAuthoritativeLifecycle publishes the merged stream's unique
// terminal event. It is called after host event pumps and run-scoped
// resources have completed teardown, immediately before broker close.
func (s *eventSink) completeAuthoritativeLifecycle(res *Result, runErr error) {
	if s == nil || s.broker == nil {
		return
	}
	s.lifecycleMu.Lock()
	if !s.lifecycleActive || s.lifecycleEnded {
		s.lifecycleMu.Unlock()
		return
	}
	s.lifecycleEnded = true
	terminal := RunFinished{RunID: s.providerRunID, ThreadID: s.providerThreadID}
	if s.driverTerminal != nil {
		terminal = *s.driverTerminal
		if terminal.Usage != nil {
			usage := *terminal.Usage
			terminal.Usage = &usage
		}
	}
	if terminal.RunID == "" {
		terminal.RunID = s.runID
	}
	if terminal.ThreadID == "" {
		terminal.ThreadID = s.providerThreadID
	}
	source := cloneEventSourceMeta(s.terminalSource)
	s.lifecycleMu.Unlock()

	if terminal.Usage == nil && res != nil && res.Usage != nil {
		usage := *res.Usage
		terminal.Usage = &usage
	}
	terminal.Failed = runErr != nil
	terminal.Reason = ""
	terminal.Message = ""
	if runErr != nil {
		var business *RunError
		outcome := s.terminalSnapshot()
		switch {
		case errors.As(runErr, &business):
			terminal.Reason = business.Reason
			terminal.Message = business.Message
		case outcome.reason != "":
			terminal.Reason, terminal.Message = outcome.reason, outcome.message
		case errors.Is(runErr, ErrActiveExecutionTimeout):
			terminal.Reason = ReasonActiveExecutionTimeout
			terminal.Message = "active execution budget exhausted"
		case errors.Is(runErr, context.DeadlineExceeded):
			terminal.Reason = ReasonDeadlineExceeded
			terminal.Message = "run deadline exceeded"
		case errors.Is(runErr, context.Canceled):
			terminal.Reason = ReasonCancelled
			terminal.Message = runErr.Error()
		default:
			terminal.Reason = ReasonInfrastructure
			terminal.Message = "run infrastructure failed"
		}
	}
	s.broker.publishTerminal(terminal, source)
}

// push delivers one event under the configured backpressure strategy.
//
// Drop mode (default): non-blocking send; overflow increments a counter
// that is flushed as one aggregated Dropped{Count} marker before the next
// event that fits. The marker is delivered before that event and is never
// duplicated.
//
// Blocking mode (WithBlockingEvents): the send waits for the consumer;
// close() releases pending senders. Emitting on a closed sink is a no-op,
// never a panic.
func (s *eventSink) push(ev Event) {
	s.pushWithSource(ev, sourceMetaFromEvent(ev))
}

func (s *eventSink) pushWithSource(ev Event, source *EventSourceMeta) {
	if s == nil || s.broker == nil {
		return
	}
	s.broker.publish(ev, source)
}

// close seals the sink: releases blocked senders, expires unanswered
// approval requests, flushes the final drop marker, and closes the event
// channel. Idempotent.
func (s *eventSink) close() {
	if s == nil {
		return
	}
	s.expireOutstanding()
	s.broker.close()
}

func (s *eventSink) abort() {
	if s == nil {
		return
	}
	s.expireOutstanding()
	s.broker.abort()
}

func (s *eventSink) expireOutstanding() {
	s.outstandingMu.Lock()
	outstanding := s.outstanding
	s.outstanding = map[string]*ApprovalRequest{}
	s.outstandingMu.Unlock()
	for _, r := range outstanding {
		r.expire()
	}
}

func sourceMetaFromEvent(ev Event) *EventSourceMeta {
	if ev == nil {
		return nil
	}
	meta := ev.Meta()
	if meta.Source != nil {
		return cloneEventSourceMeta(meta.Source)
	}
	if meta.RunID == "" && meta.ThreadKey == "" && meta.TurnID == "" && meta.Sequence == 0 && meta.Time.IsZero() {
		return nil
	}
	return &EventSourceMeta{
		RunID: meta.RunID, ThreadID: meta.ThreadKey, TurnID: meta.TurnID,
		Sequence: meta.Sequence, Timestamp: meta.Time,
	}
}

func (s *eventSink) setPendingFailure(f *driver.RunFailure) {
	s.failMu.Lock()
	if s.failure == nil {
		s.failure = f
	}
	s.failMu.Unlock()
	s.recordOutcome(nil, f, nil, nil)
	if s.budget != nil {
		s.budget.Cancel(errApprovalAbort)
	}
}

func (s *eventSink) pendingFailure() *driver.RunFailure {
	s.failMu.Lock()
	defer s.failMu.Unlock()
	return s.failure
}
