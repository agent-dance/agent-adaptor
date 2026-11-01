package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/agent-dance/agent-adaptor/v2/driver"
)

const messageInitializeID = "agent-adaptor-messages-initialize"

// Hooks are optional: after ten seconds, keep stdin's ordinary user/HITL
// transport and let core deliver pending messages through checkpoint resume.
const messageInitializeTimeout = 10 * time.Second

var messageHooks = []struct{ event, mechanism string }{
	{"UserPromptSubmit", "user_prompt_submit"},
	{"PreToolUse", "pre_tool_use"},
	{"PostToolUse", "post_tool_use"},
	{"PostToolUseFailure", "post_tool_use_failure"},
	{"Stop", "stop"},
}

func claudeMessageCapability() driver.MessageCapability {
	return driver.MessageCapability{
		Boundaries: []driver.Boundary{driver.BoundaryTool, driver.BoundaryStop},
		Continue:   driver.ContinueInRun, MinVersion: "2.1.110",
	}
}

// ResolveMessageCapability negotiates stream-json input for ordinary message
// requests. Batch native schema output retains its JSON output transport and
// continues through resume; streaming schema output can use SDK hooks.
func (a adapter) ResolveMessageCapability(req driver.Request) driver.MessageCapability {
	if req.OutputSchema != nil && req.StructuredOutputSource == driver.StructuredOutputSourceNative && !req.Streaming {
		return driver.MessageCapability{}
	}
	if req.Messages != nil || wantsInteractiveClaude(req.Policy.HumanDecision) ||
		(a.persistent != nil && !req.Spawn && persistentEligible(readConfig(req.Config), req) && persistentSessionKey(req) != "") {
		return claudeVersion(context.Background(), readConfig(req.Config)).Capability(claudeMessageCapability())
	}
	return driver.MessageCapability{}
}

// ResolveMessageCapability uses the captured configuration when resolving the
// invocation's input transport.
func (d configuredDriver) ResolveMessageCapability(req driver.Request) driver.MessageCapability {
	return d.adapter.ResolveMessageCapability(d.requestWithConfig(req))
}

var _ driver.MessageCapabilityResolver = configuredDriver{}

type messageWrite struct {
	frame     []byte
	items     []driver.MessageItem
	mechanism string
	close     bool
	done      chan error
}

// messageSession owns claimed messages until the actual pipe write completes.
// prepareHook runs synchronously in the parser; only the writer goroutine does
// pipe I/O, so a full stdin pipe cannot prevent stdout from draining.
type messageSession struct {
	ctx         context.Context
	port        driver.MessagePort
	resident    bool // set before the parser reader starts
	mu          sync.Mutex
	jobs        []messageWrite
	ended       bool
	writing     bool // guarded by mu; stdin I/O is in progress
	err         error
	writeErr    error
	open        bool // accessed only by the parser and after its reader has joined
	wake        chan struct{}
	done        chan struct{}
	initialized chan struct{}
	hooksErr    error // guarded by mu; immutable after initialized closes
	hooksReady  bool  // guarded by mu
	abort       func()
}

func newMessageSession(ctx context.Context, port driver.MessagePort) *messageSession {
	return &messageSession{ctx: ctx, port: port, wake: make(chan struct{}, 1), done: make(chan struct{}), initialized: make(chan struct{})}
}

func messageFrame(v any) []byte {
	// All frames are built from JSON primitives owned by this package.
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return append(raw, '\n')
}

func messageInitializeFrame() []byte {
	hooks := make(map[string]any, len(messageHooks))
	for _, hook := range messageHooks {
		hooks[hook.event] = []any{map[string]any{"hookCallbackIds": []string{"agent-adaptor-" + hook.event}}}
	}
	return messageFrame(map[string]any{
		"type": "control_request", "request_id": messageInitializeID,
		"request": map[string]any{"subtype": "initialize", "hooks": hooks},
	})
}

func (s *messageSession) window(open bool) {
	if s.open != open {
		s.open = open
		s.port.Window(open)
	}
}

func (s *messageSession) enqueue(job messageWrite) error {
	s.mu.Lock()
	if s.ended {
		err := s.err
		s.mu.Unlock()
		s.complete(job, err, false)
		return err
	}
	s.jobs = append(s.jobs, job)
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
	return nil
}

func (s *messageSession) write(frame []byte) error {
	done := make(chan error, 1)
	s.enqueue(messageWrite{frame: frame, done: done})
	return <-done
}

func (s *messageSession) complete(job messageWrite, err error, maybeDelivered bool) {
	for _, item := range job.items {
		ack := driver.MessageAck{Delivered: err == nil, Mechanism: job.mechanism, Err: err}
		if err != nil {
			ack.Requeue = s.ctx.Err() == nil
			ack.MaybeDelivered = maybeDelivered
			if !ack.Requeue {
				ack.Err = errors.Join(err, s.ctx.Err())
			}
		}
		s.port.Ack(item.ID, ack)
	}
	if job.done != nil {
		job.done <- err
	}
}

func (s *messageSession) start(stdin interactiveStdin, abort func()) {
	s.abort = abort
	go func() {
		defer close(s.done)
		var writeErr error
		for {
			s.mu.Lock()
			if len(s.jobs) == 0 {
				ended := s.ended
				s.mu.Unlock()
				if ended {
					return
				}
				<-s.wake
				continue
			}
			job := s.jobs[0]
			s.jobs[0] = messageWrite{}
			s.jobs = s.jobs[1:]
			ended, endErr := s.ended, s.err
			s.writing = !ended && writeErr == nil
			s.mu.Unlock()
			err := writeErr
			maybeDelivered := false
			if err == nil && ended {
				err = endErr
			}
			if err == nil {
				err = s.ctx.Err()
			}
			if err == nil {
				if job.close {
					err = stdin.Close()
				} else {
					err = stdin.Write(job.frame)
					var pipeErr *messageWriteError
					maybeDelivered = errors.As(err, &pipeErr) && pipeErr.written > 0
				}
				// Once writing starts, its result decides delivery even if ctx
				// was cancelled while the write was in flight.
			}
			s.mu.Lock()
			s.writing = false
			s.mu.Unlock()
			s.complete(job, err, maybeDelivered)
			if err != nil && writeErr == nil {
				writeErr = err
				s.mu.Lock()
				s.writeErr = err
				s.mu.Unlock()
				abort()
			}
		}
	}()
}

func (s *messageSession) failure() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeErr
}

func (s *messageSession) finish(err error) {
	if err == nil {
		err = io.ErrClosedPipe
	}
	s.mu.Lock()
	s.ended, s.err = true, err
	writing := s.writing
	s.mu.Unlock()
	// The provider has finished: an in-flight hook response no longer has
	// a live callback guaranteed to read it. Retire immediately rather than
	// wait on a pipe that the resident may leave open indefinitely. Closing
	// stdin interrupts I/O; the writer still owns every claim's actual Ack.
	if writing {
		s.abort()
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
	<-s.done
	s.window(false)
}

func (s *messageSession) handleResponse(payload map[string]any) bool {
	response := claudeTopLevelObject(payload, "response")
	if claudeTopLevelString(response, "request_id") != messageInitializeID {
		return false
	}
	var err error
	if claudeTopLevelString(response, "subtype") != "success" {
		err = fmt.Errorf("claude hooks initialization rejected: %s", claudeTopLevelString(response, "error"))
	}
	s.resolveInitialization(err)
	return true
}

// The parser resolves successful registration before it can see any callbacks.
// Timeout and late responses race under mu; the first resolution is final for
// this process, so a late success cannot reopen a degraded delivery window.
func (s *messageSession) resolveInitialization(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.hooksReady {
		s.hooksReady, s.hooksErr = true, err
		close(s.initialized)
	}
}

func (s *messageSession) hooksEnabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hooksReady && s.hooksErr == nil
}

func (p *claudeParser) prepareMessageHook(payload map[string]any) bool {
	s := p.messages
	request := claudeTopLevelObject(payload, "request")
	if claudeTopLevelString(request, "subtype") != "hook_callback" {
		return false
	}
	input := claudeTopLevelObject(request, "input")
	event := claudeExactString(input, "hook_event_name")
	mechanism := ""
	for _, hook := range messageHooks {
		if event == hook.event && claudeExactString(request, "callback_id") == "agent-adaptor-"+hook.event {
			mechanism = "claude.hook." + hook.mechanism
			break
		}
	}
	output := map[string]any{}
	var items []driver.MessageItem
	if mechanism != "" && s.hooksEnabled() && claudeExactString(input, "agent_id") == "" &&
		claudeExactString(payload, "parent_tool_use_id") == "" && s.ctx.Err() == nil {
		p.turnComplete = false
		s.window(true)
		if event == "Stop" {
			// Stop is also the turn-end boundary. One atomic claim includes
			// Soon and AfterTurn in their original admission order.
			items = s.port.Claim(driver.BoundaryTurnEnd)
		} else {
			items = s.port.Claim(driver.BoundaryTool)
		}
		if len(items) > 0 {
			texts := make([]string, len(items))
			for i, item := range items {
				texts[i] = item.Text
			}
			text := strings.Join(texts, "\n\n")
			if event == "Stop" {
				output = map[string]any{"decision": "block", "reason": text}
				p.emitTurnCompleted(p.turnText, p.turnUsage)
				p.turnText = ""
			} else {
				output["hookSpecificOutput"] = map[string]any{"hookEventName": event, "additionalContext": text}
			}
		} else if event == "Stop" {
			s.window(false)
		}
	}
	s.enqueue(messageWrite{frame: messageFrame(map[string]any{
		"type": "control_response", "response": map[string]any{
			"subtype": "success", "request_id": claudeTopLevelString(payload, "request_id"), "response": output,
		},
	}), items: items, mechanism: mechanism})
	return true
}

func (p *claudeParser) emitTurnCompleted(text string, usage *driver.Usage) {
	if p.turnComplete {
		return
	}
	p.turnComplete = true
	if p.sink != nil {
		var copyUsage *driver.Usage
		if usage != nil {
			v := *usage
			copyUsage = &v
		}
		now := time.Now().UTC()
		_ = p.sink.Emit(driver.RunEvent{Type: driver.RunEventTurnCompleted, Timestamp: now,
			Turn: &driver.TurnCompleted{Index: p.turnIndex, Text: text, Usage: copyUsage, CompletedAt: now}})
	}
	p.turnIndex++
	p.completedUsage = addClaudeUsage(p.completedUsage, usage)
	p.turnUsage = nil
	p.turnMessageUsage = nil
}

// initialize returns an optional hook-unavailability reason separately from
// fatal transport/cancellation errors. Only the latter may fail the run.
func (s *messageSession) initialize(readerDone <-chan struct{}) (unavailable, fatal error) {
	if err := s.write(messageInitializeFrame()); err != nil {
		return nil, err
	}
	timer := time.NewTimer(messageInitializeTimeout)
	defer timer.Stop()
	select {
	case <-s.initialized:
	case <-readerDone:
		return nil, errors.New("claude process exited during hooks initialization")
	case <-s.ctx.Done():
		return nil, s.ctx.Err()
	case <-timer.C:
		s.resolveInitialization(fmt.Errorf("claude hooks initialization timed out after %s", messageInitializeTimeout))
	}
	if err := s.ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case <-readerDone:
		return nil, errors.New("claude process exited during hooks initialization")
	default:
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hooksErr, nil
}
