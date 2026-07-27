package a2a

// ServerV1 drives the A2A protocol from adaptor.Runner / adaptor.Stream. The
// V1 suffix is temporary public Go naming retained until the RENAME wave;
// there is no parallel executor in this package.
//
// Design-doc target (S6):
//
//	agent := adaptor.New(codex.Driver(cfg), adaptor.WithThreadStore(store))
//	srv := a2a.NewServerV1(agent, a2a.ServerOptionsV1{
//	    AgentCard: a2a.AgentCard{Name: "Local Codex", ...},
//	    Session:   a2a.ThreadByContextID(),
//	})
//	http.Handle("/a2a", srv.Handler())
//
// Protocol semantics:
//   - AgentCard building, task-store configuration (TaskLifecycle), and
//     capability misconfiguration checks are shared protocol code;
//   - the adapter.stream.v1 extension is appended to the card;
//   - Execute projects submitted → working → per-event working DataParts →
//     exactly one terminal (completed / failed / canceled) outcome;
//   - ExposurePolicy gating and redaction ride the same sanitize/redact
//     helpers (convert.go) and the same AdapterStreamEnvelopeV1 wire;
//   - Cancel cancels the pending context and the live stream.

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"strings"
	"sync"

	a2aproto "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/agent-dance/agent-adaptor/bridges/internal/bridgekey"
	adaptor "github.com/agent-dance/agent-adaptor/next"
)

// SessionBinding decides which Runner executes one inbound A2A request.
// Build one with StatelessV1 or
// ThreadByContextID; a nil binding means stateless.
type SessionBinding interface {
	bindRunner(base adaptor.Runner, req InboundRequest) (adaptor.Runner, error)
}

type sessionBindingFunc func(base adaptor.Runner, req InboundRequest) (adaptor.Runner, error)

func (fn sessionBindingFunc) bindRunner(base adaptor.Runner, req InboundRequest) (adaptor.Runner, error) {
	return fn(base, req)
}

// StatelessV1 runs every inbound request on the configured Runner as-is:
// no thread binding, no cross-request memory. This is the default.
func StatelessV1() SessionBinding {
	return sessionBindingFunc(func(base adaptor.Runner, _ InboundRequest) (adaptor.Runner, error) {
		return base, nil
	})
}

// ThreadByContextID maps the A2A contextID onto a conversation thread: each
// distinct contextID becomes a collision-free ("a2a", contextID) Thread key,
// so follow-up
// messages in the same A2A context continue the same conversation.
//
// The configured Runner must be an *adaptor.Agent carrying a thread store
// (adaptor.WithThreadStore) — only an Agent can mint threads. A request
// without a contextID runs stateless.
func ThreadByContextID() SessionBinding {
	return sessionBindingFunc(func(base adaptor.Runner, req InboundRequest) (adaptor.Runner, error) {
		if req.ContextID == "" {
			return base, nil
		}
		agent, ok := base.(*adaptor.Agent)
		if !ok {
			return nil, fmt.Errorf("a2a bridge: ThreadByContextID requires an *adaptor.Agent runner, got %T", base)
		}
		return agent.Thread(bridgekey.Encode("a2a", req.ContextID)), nil
	})
}

// PromptBuilderV1 turns one inbound A2A request into the prompt and
// per-call options for the run — the v1 counterpart of PromptBuilder.
type PromptBuilderV1 func(ctx context.Context, req InboundRequest) (prompt string, opts []adaptor.CallOption, err error)

// ResultBuilderV1 customizes the terminal A2A artifacts and final status
// text produced from one completed run — the v1 counterpart of
// ResultBuilder, typed on the v1 Result.
type ResultBuilderV1 func(ctx context.Context, req InboundRequest, result *adaptor.Result) (BuiltResult, error)

// ServerOptionsV1 configures NewServerV1. AgentCard is required; everything
// else uses stateless sessions, the last text part as prompt, an in-memory
// ephemeral task store, and minimal exposure.
type ServerOptionsV1 struct {
	// AgentCard is the public A2A identity of this server.
	AgentCard AgentCard

	// Session decides thread binding per inbound request:
	// StatelessV1() (default) or ThreadByContextID().
	Session SessionBinding

	// Prompt, when set, replaces the default prompt extraction (the last
	// non-blank text part of the inbound message).
	Prompt PromptBuilderV1

	// Options are appended to every run (call scope, decision D7).
	Options []adaptor.CallOption

	// ResultBuilder, when set, customizes terminal artifacts and status.
	ResultBuilder ResultBuilderV1

	// TaskLifecycle configures task retention (default: in-memory ephemeral
	// store, 256 tasks / 1h).
	TaskLifecycle TaskLifecycleOptions

	// PushNotifications must be set exactly when the card enables the
	// capability; configuration is validated at construction.
	PushNotifications *PushNotificationSupport

	// ExtendedAgentCard must be set exactly when the card enables the
	// capability; configuration is validated at construction.
	ExtendedAgentCard *ExtendedAgentCardSupport

	// Exposure controls how much intermediate detail crosses the A2A
	// boundary. The zero value hides reasoning, tool calls, HITL traffic,
	// and all diagnostics.
	Exposure ExposurePolicy
}

// ServerV1 is the assembled v1 A2A server. Mount Handler() on an HTTP mux;
// AgentCardHandler() serves the public card document.
type ServerV1 struct {
	runner adaptor.Runner
	card   *a2aproto.AgentCard

	handler     a2asrv.RequestHandler
	jsonHandler http.Handler
	cardHandler http.Handler
}

// NewServerV1 assembles an A2A server around a Runner (Agent or Thread).
// It panics on construction-time misconfiguration (nil runner, invalid
// card, invalid task lifecycle, capability support mismatch).
func NewServerV1(runner adaptor.Runner, opts ServerOptionsV1) *ServerV1 {
	if runner == nil {
		panic("a2a bridge: nil runner")
	}
	opts.AgentCard.Capabilities.Extensions = append(opts.AgentCard.Capabilities.Extensions, Extension{
		URI:         AdapterStreamExtensionURI,
		Description: "Streams agent-adaptor intermediate events through TaskStatusUpdateEvent DataParts.",
		Required:    false,
		Params:      map[string]any{"schema": AdapterStreamSchemaV1},
	})
	card, err := buildAgentCard(opts.AgentCard)
	if err != nil {
		panic(err)
	}
	taskStore, err := newConfiguredTaskStore(opts.TaskLifecycle)
	if err != nil {
		panic(err)
	}
	if opts.Session == nil {
		opts.Session = StatelessV1()
	}
	prompt := opts.Prompt
	if prompt == nil {
		prompt = defaultPromptV1
	}

	exec := &executorV1{
		runner:        runner,
		session:       opts.Session,
		prompt:        prompt,
		options:       append([]adaptor.CallOption(nil), opts.Options...),
		resultBuilder: opts.ResultBuilder,
		exposure:      opts.Exposure,
		active:        map[a2aproto.TaskID]adaptor.Stream{},
		pending:       map[a2aproto.TaskID]context.CancelFunc{},
	}
	handlerOpts := []a2asrv.RequestHandlerOption{a2asrv.WithTaskStore(taskStore)}
	capabilityOpts, err := requestHandlerCapabilityOptions(card, opts.PushNotifications, opts.ExtendedAgentCard)
	if err != nil {
		panic(err)
	}
	handlerOpts = append(handlerOpts, capabilityOpts...)
	handlerOpts = append(handlerOpts, a2asrv.WithCapabilityChecks(&card.Capabilities))

	requestHandler := a2asrv.NewHandler(exec, handlerOpts...)
	return &ServerV1{
		runner:      runner,
		card:        card,
		handler:     requestHandler,
		jsonHandler: a2asrv.NewJSONRPCHandler(requestHandler),
		cardHandler: a2asrv.NewStaticAgentCardHandler(card),
	}
}

// Handler returns the JSON-RPC endpoint handler.
func (s *ServerV1) Handler() http.Handler {
	return s.jsonHandler
}

// AgentCardHandler returns the public agent-card document handler.
func (s *ServerV1) AgentCardHandler() http.Handler {
	return s.cardHandler
}

// AgentCard returns the effective public card (after bridge extensions).
func (s *ServerV1) AgentCard() AgentCard {
	return publicCard(s.card)
}

type executorV1 struct {
	runner        adaptor.Runner
	session       SessionBinding
	prompt        PromptBuilderV1
	options       []adaptor.CallOption
	resultBuilder ResultBuilderV1
	exposure      ExposurePolicy

	mu      sync.Mutex
	active  map[a2aproto.TaskID]adaptor.Stream
	pending map[a2aproto.TaskID]context.CancelFunc
}

func (e *executorV1) Execute(ctx context.Context, execCtx *a2asrv.ExecutorContext) iter.Seq2[a2aproto.Event, error] {
	return func(yield func(a2aproto.Event, error) bool) {
		// 1. Resolve the inbound request into prompt + runner + options.
		req, err := inboundFromExecCtx(execCtx)
		if err != nil {
			yield(nil, fmt.Errorf("%w: %v", a2aproto.ErrInvalidParams, err))
			return
		}
		prompt, promptOpts, err := e.prompt(ctx, req)
		if err != nil {
			yield(nil, fmt.Errorf("%w: %v", a2aproto.ErrInvalidParams, err))
			return
		}
		if strings.TrimSpace(prompt) == "" {
			yield(nil, fmt.Errorf("%w: prompt is empty", a2aproto.ErrInvalidParams))
			return
		}
		target, err := e.session.bindRunner(e.runner, req)
		if err != nil {
			yield(nil, fmt.Errorf("%w: %v", a2aproto.ErrInvalidParams, err))
			return
		}
		callOpts := append([]adaptor.CallOption(nil), e.options...)
		callOpts = append(callOpts, promptOpts...)

		// 2. Register cancellation before starting the run.
		runCtx, cancelRunCtx := context.WithCancel(ctx)
		e.storePending(execCtx.TaskID, cancelRunCtx)
		defer func() {
			e.deletePending(execCtx.TaskID)
			cancelRunCtx()
		}()

		if execCtx.StoredTask == nil && execCtx.Message != nil {
			if !yield(a2aproto.NewSubmittedTask(execCtx, execCtx.Message), nil) {
				return
			}
		}
		if !yield(a2aproto.NewStatusUpdateEvent(execCtx, a2aproto.TaskStateWorking, nil), nil) {
			return
		}
		if runCtx.Err() != nil {
			yield(canceledStatusV1(execCtx, runCtx.Err()), nil)
			return
		}

		// Stream never fails to start: startup problems close the event
		// channel immediately and surface through Result() below.
		stream := target.Stream(runCtx, prompt, callOpts...)
		defer stream.Cancel()
		e.store(execCtx.TaskID, stream)
		defer e.delete(execCtx.TaskID)

		// 3. Forward unified events as working-status DataParts.
		translator := newStreamTranslatorV1(execCtx, e.exposure)
		for {
			select {
			case <-runCtx.Done():
				stream.Cancel()
				// Cancellation is a request to stop, not a second terminal
				// authority. Drain the now-cancelled stream and let Result()
				// classify the one terminal status below.
				for range stream.Events() {
				}
				goto drained
			case ev, ok := <-stream.Events():
				if !ok {
					goto drained
				}
				for _, out := range translator.Translate(ev) {
					if !yield(out, nil) {
						stream.Cancel()
						return
					}
				}
			}
		}

	drained:
		// 4. Project exactly one terminal outcome (D1 error contract:
		// business failure = *RunError carrying the Result; cancellation
		// and infrastructure failures are plain wrapped errors).
		res, runErr := stream.Result()
		if runErr != nil {
			if errors.Is(runErr, context.Canceled) {
				yield(canceledStatusV1(execCtx, runErr), nil)
				return
			}
			var re *adaptor.RunError
			if errors.As(runErr, &re) {
				for _, ev := range defaultTerminalArtifactsV1(execCtx, re.Result, e.exposure) {
					if !yield(ev, nil) {
						return
					}
				}
				msg := failureMessage(execCtx, defaultString(re.Message, re.Error()), failureDetailsV1(re, e.exposure))
				yield(a2aproto.NewStatusUpdateEvent(execCtx, a2aproto.TaskStateFailed, msg), nil)
				return
			}
			msg := failureMessage(execCtx, runErr.Error(), map[string]any{"layer": "wait"})
			yield(a2aproto.NewStatusUpdateEvent(execCtx, a2aproto.TaskStateFailed, msg), nil)
			return
		}
		built, err := e.buildResult(ctx, req, res)
		if err != nil {
			msg := failureMessage(execCtx, err.Error(), map[string]any{"layer": "result_builder"})
			yield(a2aproto.NewStatusUpdateEvent(execCtx, a2aproto.TaskStateFailed, msg), nil)
			return
		}
		artifacts, err := terminalArtifactsV1(execCtx, res, e.exposure, built)
		if err != nil {
			msg := failureMessage(execCtx, err.Error(), map[string]any{"layer": "result_builder"})
			yield(a2aproto.NewStatusUpdateEvent(execCtx, a2aproto.TaskStateFailed, msg), nil)
			return
		}
		for _, ev := range artifacts {
			if !yield(ev, nil) {
				return
			}
		}
		yield(a2aproto.NewStatusUpdateEvent(execCtx, a2aproto.TaskStateCompleted, agentMessage(execCtx, completedStatusTextV1(res, built))), nil)
	}
}

func (e *executorV1) buildResult(ctx context.Context, req InboundRequest, result *adaptor.Result) (BuiltResult, error) {
	if e == nil || e.resultBuilder == nil {
		return BuiltResult{}, nil
	}
	return e.resultBuilder(ctx, req, result)
}

func (e *executorV1) Cancel(ctx context.Context, execCtx *a2asrv.ExecutorContext) iter.Seq2[a2aproto.Event, error] {
	return func(yield func(a2aproto.Event, error) bool) {
		e.cancelPending(execCtx.TaskID)
		if stream := e.load(execCtx.TaskID); stream != nil {
			stream.Cancel()
		}
		msg := agentMessage(execCtx, "task cancelled")
		yield(a2aproto.NewStatusUpdateEvent(execCtx, a2aproto.TaskStateCanceled, msg), nil)
	}
}

func (e *executorV1) store(id a2aproto.TaskID, stream adaptor.Stream) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.active[id] = stream
}

func (e *executorV1) load(id a2aproto.TaskID) adaptor.Stream {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.active[id]
}

func (e *executorV1) delete(id a2aproto.TaskID) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.active, id)
}

func (e *executorV1) storePending(id a2aproto.TaskID, cancel context.CancelFunc) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.pending[id] = cancel
}

func (e *executorV1) cancelPending(id a2aproto.TaskID) {
	e.mu.Lock()
	cancel := e.pending[id]
	e.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (e *executorV1) deletePending(id a2aproto.TaskID) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.pending, id)
}

func completedStatusTextV1(result *adaptor.Result, built BuiltResult) string {
	if built.StatusText != nil {
		return *built.StatusText
	}
	if result == nil {
		return ""
	}
	return result.Text
}

func canceledStatusV1(info a2aproto.TaskInfoProvider, cause error) *a2aproto.TaskStatusUpdateEvent {
	msg := "task cancelled"
	if cause != nil {
		msg = cause.Error()
	}
	return a2aproto.NewStatusUpdateEvent(info, a2aproto.TaskStateCanceled, failureMessage(info, msg, map[string]any{"code": string(adaptor.ReasonCancelled)}))
}

func defaultPromptV1(_ context.Context, req InboundRequest) (string, []adaptor.CallOption, error) {
	for i := len(req.Message.Parts) - 1; i >= 0; i-- {
		part := req.Message.Parts[i]
		if part.Kind == PartText && strings.TrimSpace(part.Text) != "" {
			return part.Text, nil, nil
		}
	}
	return "", nil, fmt.Errorf("no user text part in A2A message")
}
