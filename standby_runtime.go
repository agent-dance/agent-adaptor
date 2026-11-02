package adaptor

import (
	"context"
	"errors"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/agent-dance/agent-adaptor/v2/internal/engine"
)

type standbyMessage struct {
	text    string
	opts    []CallOption
	id      string
	retried bool
	readyAt time.Time
	carried []MessageOutcome
}

type standbyStream struct {
	mu               sync.Mutex
	thread           *Thread
	runtime          *threadRuntime
	ctx              context.Context
	cancelLifetime   context.CancelFunc
	stopOwner        func() bool
	broker           *eventBroker
	done             chan struct{}
	wake             chan struct{}
	changed          chan struct{}
	queue            []*standbyMessage
	carry            []*standbyMessage
	current          *runStream
	currentMessage   *standbyMessage
	processingCtx    context.Context
	cancelProcessing context.CancelFunc
	cancelled        bool
	maxPending       int
	eventsCalled     atomic.Bool
}

var _ Standby = (*standbyStream)(nil)

// Standby gets or creates this Agent's live standby for the Thread key. Its
// lifetime belongs to the first caller's ctx, not to later callers or Send
// admission contexts. A store-validated, renewed ownership lease excludes task
// executions and other processes, including while the standby is idle.
// Shutdown releases that lease even when a checkpoint still needs persistence.
// Background recovery reacquires ownership and checks the durable version before
// writing; it never overwrites a Thread that another writer has advanced.
func (t *Thread) Standby(ctx context.Context) (Standby, error) {
	if err := t.agent.ensureOpen(); err != nil {
		return nil, err
	}
	if _, err := validateThreadDriverContract(t.agent.driver); err != nil {
		return nil, t.threadError(err)
	}
	if err := validatePolicy(t.agent.driver.Descriptor(), t.agent.defaults.policy); err != nil {
		return nil, err
	}
	if t.agent.toolConfigErr != nil {
		return nil, t.agent.toolConfigErr
	}
	if t.agent.toolThreadErr != nil {
		return nil, t.agent.toolThreadErr
	}
	runtime := t.agent.threadRuntime(t.key)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		runtime.mu.Lock()
		if existing := runtime.standby; existing != nil {
			if existing.ctx.Err() == nil {
				runtime.mu.Unlock()
				return existing, nil
			}
			runtime.mu.Unlock()
			select {
			case <-existing.done:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if runtime.task {
			runtime.mu.Unlock()
			return nil, t.threadError(ErrThreadBusy)
		}
		if err := runtime.acquireLocked(ctx); err != nil {
			runtime.mu.Unlock()
			return nil, t.threadError(err)
		}
		lifetime, cancel := context.WithCancel(ctx)
		broker := newEventBroker("", t.key, t.agent.defaults.eventBuffer, t.agent.defaults.blockingEvents)
		broker.spool, broker.discardUnsubscribed, broker.multipleRuns = true, true, true
		standby := &standbyStream{thread: t, runtime: runtime, ctx: lifetime, cancelLifetime: cancel, broker: broker, done: make(chan struct{}), wake: make(chan struct{}, 1), changed: make(chan struct{}), maxPending: effectiveMessagePolicy(t.agent.defaults.policy).MaxPending}
		standby.stopOwner = context.AfterFunc(runtime.ownerCtx, cancel)
		runtime.standby = standby
		runtime.mu.Unlock()
		go standby.run()
		return standby, nil
	}
}

func (s *standbyStream) notifyLocked() {
	close(s.changed)
	s.changed = make(chan struct{})
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *standbyStream) Send(ctx context.Context, text string, opts ...CallOption) error {
	var messageOptions []MessageOption
	settings := s.thread.agent.defaults.RunSettings.clone()
	executionOptions := false
	for _, option := range opts {
		if option == nil {
			continue
		}
		if message, ok := option.(MessageOption); ok {
			messageOptions = append(messageOptions, message)
		} else {
			executionOptions = true
		}
		option.ApplyRun(&settings)
	}
	if settings.message.id == "" {
		id, err := newRunID()
		if err != nil {
			return &MessageError{Text: text, Reason: MessageRunFailed, Err: err}
		}
		settings.message.id = "msg_" + strings.TrimPrefix(id, "run_")
		messageOptions = append(messageOptions, MessageID(settings.message.id))
	}
	id := settings.message.id
	reject := func(err error) error { return &MessageError{ID: id, Text: text, Reason: MessageCancelled, Err: err} }
	if settings.message.timing > AfterTurn || settings.message.from.Kind > AuthorSystem {
		return &MessageError{ID: id, Text: text, Reason: MessageProviderRejected, Err: errors.New("adaptor: invalid author or timing")}
	}
	// Deterministic option errors reject admission, rather than becoming
	// accepted work that can be carried into a later execution unchanged.
	if err := validatePolicy(s.thread.agent.driver.Descriptor(), settings.policy); err != nil {
		return &MessageError{ID: id, Text: text, Reason: MessageProviderRejected, Err: err}
	}
	if settings.outputSchemaErr != nil {
		return &MessageError{ID: id, Text: text, Reason: MessageProviderRejected, Err: settings.outputSchemaErr}
	}
	if _, err := engine.NormalizeOutputSchema(settings.outputSchema); err != nil {
		return &MessageError{ID: id, Text: text, Reason: MessageProviderRejected, Err: err}
	}
	for {
		if err := ctx.Err(); err != nil {
			return reject(err)
		}
		if err := s.thread.agent.ensureOpen(); err != nil {
			return reject(err)
		}
		s.mu.Lock()
		if err := s.ctx.Err(); err != nil {
			s.mu.Unlock()
			return reject(err)
		}
		for _, queued := range s.queue {
			duplicate := queued.id == id
			for _, carried := range queued.carried {
				duplicate = duplicate || carried.ID == id
			}
			if duplicate {
				s.mu.Unlock()
				return nil
			}
		}
		for index, pending := range s.carry {
			if pending.id == id {
				if s.current == nil && len(s.queue) == 0 {
					s.carry = append(s.carry[:index], s.carry[index+1:]...)
					s.queue = append(s.queue, pending)
					s.notifyLocked()
				}
				s.mu.Unlock()
				return nil
			}
		}
		if s.current != nil && !s.cancelled && !executionOptions {
			current := s.current
			s.mu.Unlock()
			err := current.hub.sendWithLifetime(ctx, s.ctx, text, messageOptions...)
			if errors.Is(err, ErrRunFinished) {
				s.mu.Lock()
				changed := s.changed
				unchanged := s.current == current
				s.mu.Unlock()
				if unchanged {
					select {
					case <-changed:
					case <-ctx.Done():
						return reject(ctx.Err())
					case <-s.ctx.Done():
						return reject(s.ctx.Err())
					}
				}
				continue
			}
			return err
		}
		if s.current == nil && len(s.queue) == 0 && len(s.carry) > 0 {
			// A fresh input wakes carried work. Claim the carry and this input
			// together as the new prompt before applying pending capacity: merely
			// ignoring carry here would overfill the unclaimed queue.
			copied := append([]CallOption(nil), opts...)
			copied = append(copied, MessageID(id))
			s.startExecutionLocked(&standbyMessage{text: text, opts: copied, id: id})
			s.mu.Unlock()
			return nil
		}
		if s.pendingLocked() < s.maxPending {
			copied := append([]CallOption(nil), opts...)
			copied = append(copied, MessageID(id))
			s.queue = append(s.queue, &standbyMessage{text: text, opts: copied, id: id})
			s.notifyLocked()
			s.mu.Unlock()
			return nil
		}
		changed := s.changed
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return reject(ctx.Err())
		case <-s.ctx.Done():
			return reject(s.ctx.Err())
		case <-changed:
		}
	}
}

func (s *standbyStream) Events() <-chan Event {
	events := s.broker.subscribe()
	s.eventsCalled.Store(true)
	return events
}

func (s *standbyStream) pendingLocked() int {
	pending := len(s.carry) + len(s.queue)
	for _, message := range s.queue {
		pending += len(message.carried)
	}
	return pending
}

func (s *standbyStream) startExecutionLocked(message *standbyMessage) {
	for _, pending := range s.carry {
		attributes := MessageAttributes(pending.opts...)
		message.carried = append(message.carried, MessageOutcome{ID: pending.id, Text: pending.text, From: attributes.From, Timing: attributes.Timing})
	}
	s.carry = nil
	mode, parent := s.thread.planMode()
	s.processingCtx, s.cancelProcessing = context.WithCancel(s.ctx)
	s.current = s.thread.agent.startInvocation(s.processingCtx, message.text, message.opts, &invocationTarget{thread: s.thread, mode: mode, forkFromKey: parent, runtime: s.runtime, standby: true, carried: message.carried, hostEventsCalled: &s.eventsCalled}).(*runStream)
	s.currentMessage = message
	s.cancelled = false
	s.notifyLocked()
}

func (s *standbyStream) Cancel() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancelled = true
	if s.cancelProcessing != nil {
		s.cancelProcessing()
	}
	if s.current != nil {
		s.current.Cancel()
	}
	s.failQueuedLocked()
	s.notifyLocked()
}

func (s *standbyStream) failQueuedLocked() {
	failed := make(map[string]bool)
	fail := func(id, text string) {
		if !failed[id] {
			s.broker.publish(MessageUpdate{ID: id, State: MessageFailed, Reason: MessageCancelled, Text: text}, nil)
			failed[id] = true
		}
	}
	for _, pending := range append(s.carry, s.queue...) {
		fail(pending.id, pending.text)
		for _, carried := range pending.carried {
			fail(carried.ID, carried.Text)
		}
	}
	s.queue, s.carry = nil, nil
}

func (s *standbyStream) run() {
	defer func() {
		s.stopOwner()
		s.runtime.mu.Lock()
		s.runtime.standby = nil
		releaseErr := s.runtime.releaseLocked()
		if releaseErr == nil {
			if err := s.runtime.startReleasedRecoveryLocked(s.ctx); err != nil && !errors.Is(err, ErrAgentClosed) {
				releaseErr = err
			}
		}
		s.runtime.signal()
		s.runtime.mu.Unlock()
		if releaseErr != nil {
			s.broker.publish(Notice{Kind: "thread.ownership", Text: "Releasing Thread ownership failed: " + releaseErr.Error()}, nil)
		}
		s.broker.close()
		close(s.done)
	}()
	var events <-chan Event
	for {
		s.mu.Lock()
		ending := s.ctx.Err() != nil
		if ending {
			if s.current == nil {
				s.failQueuedLocked()
				s.notifyLocked()
				s.mu.Unlock()
				return
			}
			s.cancelled = true
			s.cancelProcessing()
			s.current.Cancel()
		}
		var delay time.Duration
		if s.current == nil && len(s.queue) > 0 && s.ctx.Err() == nil {
			message := s.queue[0]
			delay = time.Until(message.readyAt)
			if delay <= 0 {
				s.queue = s.queue[1:]
				s.startExecutionLocked(message)
			}
		}
		if events == nil && s.current != nil {
			// Internal forwarding subscribes for transport/backpressure only.
			// Approval observation follows the host's Standby.Events call.
			events = s.current.sink.broker.subscribe()
		}
		processingCtx := s.processingCtx
		lifetimeDone := s.ctx.Done()
		if ending {
			lifetimeDone = nil
		}
		s.mu.Unlock()
		var timer *time.Timer
		var timerC <-chan time.Time
		if delay > 0 {
			timer = time.NewTimer(delay)
			timerC = timer.C
		}
		select {
		case event, open := <-events:
			if open {
				s.broker.publishContext(processingCtx, event, nil)
			} else {
				s.completeExecution()
				events = nil
			}
		case <-s.wake:
		case <-lifetimeDone:
		case <-timerC:
		}
		if timer != nil {
			timer.Stop()
		}
	}
}

func (s *standbyStream) completeExecution() {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.current.Result()
	if err != nil {
		outcomes := s.current.hub.outcomes()
		if !s.cancelled && s.ctx.Err() == nil {
			var pending []*standbyMessage
			for _, outcome := range outcomes {
				if outcome.State == MessageFailed && outcome.Reason == MessageRunFailed {
					options := []CallOption{MessageID(outcome.ID), From(outcome.From), When(outcome.Timing)}
					for _, option := range s.currentMessage.opts {
						if _, messageOption := option.(MessageOption); !messageOption {
							options = append(options, option)
						}
					}
					if outcome.ID == s.currentMessage.id {
						options = s.currentMessage.opts
					}
					pending = append(pending, &standbyMessage{text: outcome.Text, id: outcome.ID, opts: options, retried: s.currentMessage.retried})
				}
			}
			if delay, retryable := standbyRetryDelay(err); retryable && !s.currentMessage.retried && len(pending) > 0 {
				for _, message := range pending {
					message.retried = true
				}
				pending[0].readyAt = time.Now().Add(delay)
				for _, extra := range pending[1:] {
					for _, outcome := range outcomes {
						if outcome.ID == extra.id {
							pending[0].carried = append(pending[0].carried, outcome)
						}
					}
				}
				s.queue = append([]*standbyMessage{pending[0]}, s.queue...)
			} else {
				s.carry = append(s.carry, pending...)
			}
		}
	}
	s.cancelProcessing()
	s.current, s.currentMessage = nil, nil
	s.notifyLocked()
}

func standbyRetryDelay(err error) (time.Duration, bool) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return 0, false
	}
	var failure *RunError
	retryable := false
	if errors.As(err, &failure) {
		switch failure.Reason {
		case "rate_limit", "rate_limited", "network_error", "server_error", "provider_server_error":
			retryable = true
		}
		if status, ok := failure.Details["status_code"].(int); ok && (status == 429 || status >= 500 && status <= 599) {
			retryable = true
		}
		if retryable {
			if hint, ok := failure.Details["retry_after"]; ok {
				if delay, valid := standbyRetryAfter(hint); valid {
					return delay, true
				}
			}
		}
	}
	var network net.Error
	if errors.As(err, &network) {
		retryable = true
	}
	return 250 * time.Millisecond, retryable
}

func standbyRetryAfter(hint any) (time.Duration, bool) {
	var seconds float64
	switch value := hint.(type) {
	case time.Duration:
		return max(0, value), true
	case int:
		seconds = float64(value)
	case float64:
		seconds = value
	case string:
		if duration, err := time.ParseDuration(value); err == nil {
			return max(0, duration), true
		}
		if date, err := http.ParseTime(value); err == nil {
			return max(0, time.Until(date)), true
		}
		var err error
		seconds, err = strconv.ParseFloat(value, 64)
		if err != nil {
			return 0, false
		}
	default:
		return 0, false
	}
	if math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		return 0, false
	}
	if seconds >= float64(math.MaxInt64)/float64(time.Second) {
		return time.Duration(math.MaxInt64), true
	}
	return time.Duration(max(0, seconds) * float64(time.Second)), true
}
