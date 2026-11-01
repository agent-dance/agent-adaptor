package team

import (
	"context"
	"errors"
	"sync"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
)

type standbyMessage struct {
	text    string
	options []adaptor.CallOption
	order   uint64
}

type standby struct {
	thread        *Thread
	ctx           context.Context
	cancel        context.CancelFunc
	mu            sync.Mutex
	current       adaptor.Stream
	streams       []adaptor.Stream
	observed      map[string]bool
	lease         ScopeLease
	closeErr      error
	queue         []standbyMessage
	carry         []standbyMessage
	retries       int
	wake          chan struct{}
	done          chan struct{}
	log           *eventLog
	subscribed    chan struct{}
	subscribeOnce sync.Once
	senders       sync.WaitGroup
	forwardTail   <-chan struct{}
	admissions    uint64
	admission     chan struct{}
	generation    uint64
	cancelling    bool
	ready         chan struct{}
	initErr       error
}

// Standby gets or creates the process-local standby for this scope. Its first
// context owns the lifetime. Cancel affects only the current execution and
// pending messages; subsequent Send calls remain usable. Events are not
// buffered until Events is first called. Send contexts bound admission only;
// successful inbox persistence admits an input, including supplements to a busy
// execution. Admitted inputs belong to the standby lifetime. Later provider
// admission failures are reported through MessageUpdate, not the Send return.
func (thread *Thread) Standby(ctx context.Context) (adaptor.Standby, error) {
	team := thread.team
	team.mu.Lock()
	if team.closed {
		team.mu.Unlock()
		return nil, adaptor.ErrAgentClosed
	}
	if current := team.standbys[thread.scope]; current != nil {
		select {
		case <-current.done:
			delete(team.standbys, thread.scope)
		default:
			team.mu.Unlock()
			select {
			case <-current.ready:
				if current.initErr != nil {
					return nil, current.initErr
				}
				return current, nil
			default:
			}
			select {
			case <-current.ready:
				if current.initErr != nil {
					return nil, current.initErr
				}
				return current, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}
	if err := ctx.Err(); err != nil {
		team.mu.Unlock()
		return nil, err
	}
	if current := team.active[thread.scope]; current != nil {
		select {
		case <-current.done:
			delete(team.active, thread.scope)
		default:
			team.mu.Unlock()
			return nil, ErrThreadBusy
		}
	}
	lifetime, cancel := context.WithCancel(ctx)
	waiting := &standby{thread: thread, ctx: lifetime, cancel: cancel, wake: make(chan struct{}, 1), done: make(chan struct{}), log: newEventLog("", thread.scope, true), observed: make(map[string]bool), ready: make(chan struct{})}
	waiting.admission = make(chan struct{}, 1)
	team.standbys[thread.scope] = waiting
	waiting.subscribed = make(chan struct{})
	team.mu.Unlock()
	lease, err := team.config.Store.AcquireScope(lifetime, thread.scope, "standby-"+randomID(), scopeTTL)
	if err != nil {
		cancel()
		waiting.initErr = err
		team.mu.Lock()
		if team.standbys[thread.scope] == waiting {
			delete(team.standbys, thread.scope)
		}
		team.mu.Unlock()
		close(waiting.ready)
		waiting.log.finish()
		close(waiting.done)
		return nil, err
	}
	waiting.lease = lease
	close(waiting.ready)
	go waiting.loop()
	go waiting.renew()
	return waiting, nil
}

func (waiting *standby) renew() {
	ticker := time.NewTicker(scopeTTL / 3)
	defer ticker.Stop()
	for {
		select {
		case <-waiting.ctx.Done():
			return
		case <-ticker.C:
			waiting.mu.Lock()
			lease := waiting.lease
			waiting.mu.Unlock()
			renewed, err := waiting.thread.team.config.Store.RenewScope(waiting.ctx, lease, scopeTTL)
			if err != nil {
				if waiting.ctx.Err() == nil {
					waiting.log.push(adaptor.Notice{Kind: "team.lease_failed", Text: "Standby lost its scope lease."})
					waiting.mu.Lock()
					waiting.closeErr = errors.Join(waiting.closeErr, err)
					waiting.mu.Unlock()
					waiting.cancel()
				}
				return
			}
			waiting.mu.Lock()
			waiting.lease = renewed
			waiting.mu.Unlock()
		}
	}
}

func (waiting *standby) attach(stream adaptor.Stream) {
	waiting.mu.Lock()
	if !waiting.observed[stream.RunID()] {
		waiting.observed[stream.RunID()] = true
		waiting.streams = append(waiting.streams, stream)
	}
	waiting.mu.Unlock()
	waiting.signal()
}

func (waiting *standby) Events() <-chan adaptor.Event {
	waiting.subscribeOnce.Do(func() { close(waiting.subscribed) })
	return waiting.log.events()
}

func (waiting *standby) signal() {
	select {
	case waiting.wake <- struct{}{}:
	default:
	}
}

func (waiting *standby) Send(ctx context.Context, text string, options ...adaptor.CallOption) error {
	if adaptor.MessageAttributes(options...).ID == "" {
		options = append(append([]adaptor.CallOption(nil), options...), adaptor.MessageID(randomID()))
	}
	attributes := adaptor.MessageAttributes(options...)
	reject := func(err error) error {
		reason := adaptor.MessageRunFailed
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			reason = adaptor.MessageCancelled
		}
		return &adaptor.MessageError{ID: adaptor.MessageAttributes(options...).ID, Text: text, Reason: reason, Err: err}
	}
	if err := ctx.Err(); err != nil {
		return reject(err)
	}
	if attributes.Timing > adaptor.AfterTurn || attributes.From.Kind > adaptor.AuthorSystem {
		return &adaptor.MessageError{ID: attributes.ID, Text: text, Reason: adaptor.MessageProviderRejected, Err: errors.New("team: invalid author or timing")}
	}
	// PutMessage may block while holding mu. Bound waiting for another
	// admission by the caller's context before acquiring that mutex.
	select {
	case waiting.admission <- struct{}{}:
		defer func() { <-waiting.admission }()
	case <-ctx.Done():
		return reject(ctx.Err())
	case <-waiting.ctx.Done():
		return reject(waiting.ctx.Err())
	}
	_, onlyMessages := asMessageOptions(options)
	waiting.mu.Lock()
	if err := ctx.Err(); err != nil {
		waiting.mu.Unlock()
		return reject(err)
	}
	if waiting.ctx.Err() != nil {
		waiting.mu.Unlock()
		return reject(waiting.ctx.Err())
	}
	current := waiting.current
	if current == nil || waiting.cancelling || !onlyMessages || len(waiting.queue) > 0 {
		if err := waiting.persistQueued(ctx, text, options); err != nil {
			waiting.mu.Unlock()
			return reject(err)
		}
		waiting.admissions++
		waiting.queue = append(waiting.queue, standbyMessage{text: text, options: append([]adaptor.CallOption(nil), options...), order: waiting.admissions})
		waiting.mu.Unlock()
		waiting.signal()
		return nil
	}
	if err := waiting.persistQueued(ctx, text, options); err != nil {
		waiting.mu.Unlock()
		return reject(err)
	}
	// Persistence is the Standby's acceptance boundary. Forward under its
	// lifetime so the request context cannot retract an already accepted input.
	waiting.admissions++
	message := standbyMessage{text: text, options: append([]adaptor.CallOption(nil), options...), order: waiting.admissions}
	waiting.forwardLocked(current, []standbyMessage{message}, waiting.generation)
	waiting.mu.Unlock()
	return nil
}

func (waiting *standby) Cancel() {
	waiting.mu.Lock()
	current := waiting.current
	waiting.generation++
	waiting.cancelling = current != nil
	queued := waiting.queue
	queued = append(queued, waiting.carry...)
	waiting.queue = nil
	waiting.carry = nil
	waiting.mu.Unlock()
	if current != nil {
		current.Cancel()
	}
	waiting.failQueued(queued)
}

func (waiting *standby) failQueued(messages []standbyMessage) {
	waiting.failQueuedWithReason(messages, adaptor.MessageCancelled)
}

func (waiting *standby) failQueuedWithReason(messages []standbyMessage, reason adaptor.MessageFailure) {
	waiting.mu.Lock()
	lease := waiting.lease
	waiting.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ids := make(map[string]bool)
	for _, message := range messages {
		ids[adaptor.MessageAttributes(message.options...).ID] = true
		if e := waiting.thread.team.config.Store.PutMessage(ctx, lease, hostRecord(waiting.thread.scope, message.text, message.options)); e != nil {
			waiting.log.push(adaptor.Notice{Kind: "team.inbox_failed", Text: "Cannot activate queued message for settlement."})
		}
	}
	claimed, err := waiting.thread.team.config.Store.ClaimMessages(ctx, lease, true, scopeTTL)
	for _, item := range claimed {
		if ids[item.ID] {
			err = errors.Join(err, waiting.thread.team.config.Store.FinishMessage(ctx, lease, item.ID, item.ClaimToken, DeliveryFailed, string(reason)))
		} else {
			err = errors.Join(err, waiting.thread.team.config.Store.RequeueMessage(ctx, lease, item.ID, item.ClaimToken))
		}
	}
	if err != nil {
		waiting.log.push(adaptor.Notice{Kind: "team.inbox_failed", Text: "Could not persist queued message failure."})
	}
	for _, message := range messages {
		attributes := adaptor.MessageAttributes(message.options...)
		waiting.log.push(adaptor.MessageUpdate{ID: attributes.ID, State: adaptor.MessageFailed, Text: message.text, Reason: reason})
	}
}

func (waiting *standby) loop() {
	defer func() {
		waiting.cancel()
		// Admissions register forwarding work while holding mu. Fence them
		// before Wait; later Send calls observe the cancelled lifetime.
		waiting.mu.Lock()
		waiting.mu.Unlock()
		waiting.senders.Wait()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		waiting.mu.Lock()
		lease := waiting.lease
		waiting.mu.Unlock()
		err := waiting.thread.team.config.Store.ReleaseScope(ctx, lease)
		cancel()
		if err != nil {
			waiting.mu.Lock()
			waiting.closeErr = errors.Join(waiting.closeErr, err)
			waiting.mu.Unlock()
			waiting.log.push(adaptor.Notice{Kind: "team.lease_release_failed", Text: "Standby could not release its scope lease."})
		}
		waiting.log.finish()
		close(waiting.done)
		waiting.thread.team.mu.Lock()
		if waiting.thread.team.standbys[waiting.thread.scope] == waiting {
			delete(waiting.thread.team.standbys, waiting.thread.scope)
		}
		waiting.thread.team.mu.Unlock()
	}()
	for {
		waiting.mu.Lock()
		if waiting.current == nil && len(waiting.streams) > 0 {
			waiting.current = waiting.streams[0]
			waiting.streams = waiting.streams[1:]
		}
		if waiting.current == nil && len(waiting.queue) > 0 && waiting.ctx.Err() == nil {
			message := waiting.queue[0]
			waiting.queue = waiting.queue[1:]
			waiting.mu.Unlock()
			stream := waiting.thread.team.startOwned(waiting.ctx, waiting.thread.scope, true, message.text, message.options, waiting)
			waiting.attach(stream)
			continue
		}
		current := waiting.current
		generation := waiting.generation
		var supplements []standbyMessage
		if current != nil {
			supplements = append(supplements, waiting.carry...)
			waiting.carry = nil
			for len(waiting.queue) > 0 {
				if _, messages := asMessageOptions(waiting.queue[0].options); !messages {
					break
				}
				supplements = append(supplements, waiting.queue[0])
				waiting.queue = waiting.queue[1:]
			}
		}
		if len(supplements) > 0 {
			waiting.forwardLocked(current, supplements, generation)
		}
		waiting.mu.Unlock()
		if current == nil {
			if waiting.ctx.Err() != nil {
				waiting.Cancel()
				return
			}
			select {
			case <-waiting.ctx.Done():
				waiting.Cancel()
				return
			case <-waiting.wake:
			}
			continue
		}
		stopCancellation := context.AfterFunc(waiting.ctx, current.Cancel)
		ready := make(chan struct{})
		var result *adaptor.Result
		var runErr error
		go func() { result, runErr = current.Result(); close(ready) }()
		select {
		case <-waiting.subscribed:
		case <-ready:
		}
		for event := range current.Events() {
			waiting.log.push(event)
			if waiting.ctx.Err() != nil {
				current.Cancel()
			}
		}
		stopCancellation()
		<-ready
		waiting.mu.Lock()
		if waiting.current == current {
			waiting.current = nil
			waiting.cancelling = false
		}
		delete(waiting.observed, current.RunID())
		if runErr != nil && result != nil && waiting.ctx.Err() == nil && !errors.Is(runErr, context.Canceled) && !errors.Is(runErr, ErrLimitExceeded) {
			for _, outcome := range result.Undelivered() {
				if outcome.Reason != adaptor.MessageCancelled && outcome.Reason != adaptor.MessageLimitExceeded {
					waiting.carry = append(waiting.carry, standbyMessage{text: outcome.Text, options: []adaptor.CallOption{adaptor.MessageID(outcome.ID), adaptor.From(outcome.From), adaptor.When(outcome.Timing)}})
				}
			}
		}
		var delay time.Duration
		retry := false
		var retryable RetryableError
		if runErr != nil && errors.As(runErr, &retryable) && retryable.RetryAfter() >= 0 && waiting.retries < 1 && len(waiting.carry) > 0 {
			waiting.retries++
			retry = true
			delay = max(retryable.RetryAfter(), waiting.thread.team.config.Limits.RetryBackoff)
			message := waiting.carry[0]
			waiting.carry = waiting.carry[1:]
			waiting.queue = append([]standbyMessage{message}, waiting.queue...)
		} else if runErr == nil {
			waiting.retries = 0
		}
		// No later Send can target this completed stream after current was
		// cleared under mu. Let all earlier transfers requeue before starting
		// a later execution-option input.
		transferred := waiting.forwardTail
		waiting.mu.Unlock()
		if transferred != nil {
			<-transferred
		}
		if waiting.ctx.Err() != nil {
			waiting.Cancel()
			return
		}
		if retry {
			timer := time.NewTimer(delay)
			select {
			case <-timer.C:
			case <-waiting.ctx.Done():
				timer.Stop()
				waiting.Cancel()
				return
			}
		}
	}
}

// forwardLocked preserves admission order across asynchronous transfers. The
// caller holds mu, which also fences sender registration against shutdown.
func (waiting *standby) forwardLocked(stream adaptor.Stream, messages []standbyMessage, generation uint64) {
	previous := waiting.forwardTail
	finished := make(chan struct{})
	waiting.forwardTail = finished
	waiting.senders.Add(1)
	go func() {
		defer waiting.senders.Done()
		defer close(finished)
		if previous != nil {
			<-previous
		}
		waiting.supplement(stream, messages, generation)
	}()
}

func (waiting *standby) supplement(stream adaptor.Stream, messages []standbyMessage, generation uint64) {
	for index, message := range messages {
		waiting.mu.Lock()
		valid := waiting.generation == generation && waiting.ctx.Err() == nil
		waiting.mu.Unlock()
		if !valid {
			waiting.failQueued(messages[index:])
			return
		}
		options, _ := asMessageOptions(message.options)
		err := stream.Send(waiting.ctx, message.text, options...)
		if errors.Is(err, adaptor.ErrRunFinished) && waiting.ctx.Err() == nil {
			waiting.mu.Lock()
			valid := waiting.generation == generation
			if valid {
				index := len(waiting.queue)
				for index > 0 && waiting.queue[index-1].order > message.order {
					index--
				}
				waiting.queue = append(waiting.queue, standbyMessage{})
				copy(waiting.queue[index+1:], waiting.queue[index:])
				waiting.queue[index] = message
			}
			waiting.mu.Unlock()
			if !valid {
				waiting.failQueued([]standbyMessage{message})
			}
			waiting.signal()
		} else if err != nil {
			reason := adaptor.MessageProviderRejected
			var rejected *adaptor.MessageError
			if errors.As(err, &rejected) && rejected.Reason != "" {
				reason = rejected.Reason
			} else if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				reason = adaptor.MessageCancelled
			}
			waiting.failQueuedWithReason([]standbyMessage{message}, reason)
		}
	}
}

func (waiting *standby) persistQueued(ctx context.Context, text string, options []adaptor.CallOption) error {
	message := hostRecord(waiting.thread.scope, text, options)
	message.Deferred = true
	admission, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(waiting.ctx, cancel)
	defer stop()
	defer cancel()
	return waiting.thread.team.config.Store.PutMessage(admission, waiting.lease, message)
}
