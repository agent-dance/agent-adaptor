package a2a

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

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/bridges/internal/bridgekey"
)

// SessionBinding decides which Runner executes an inbound A2A request. The
// package seals this interface so callers select either [Stateless] or
// [ThreadByContextID]. A nil binding is equivalent to [Stateless].
type SessionBinding interface {
	bindRunner(base adaptor.Runner, req InboundRequest) (adaptor.Runner, error)
}

type sessionBindingFunc func(base adaptor.Runner, req InboundRequest) (adaptor.Runner, error)

func (fn sessionBindingFunc) bindRunner(base adaptor.Runner, req InboundRequest) (adaptor.Runner, error) {
	return fn(base, req)
}

// Stateless runs every inbound request on the configured Runner as-is, without
// creating Thread state or carrying conversation state between requests. It is
// the default [SessionBinding].
func Stateless() SessionBinding {
	return sessionBindingFunc(func(base adaptor.Runner, _ InboundRequest) (adaptor.Runner, error) {
		return base, nil
	})
}

// ThreadByContextID maps each non-empty A2A contextID to a collision-free,
// namespace-encoded Thread key. Follow-up messages in the same A2A context
// therefore continue the same conversation.
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

// PromptBuilder turns one inbound A2A request into the prompt and per-call
// options for the run. Its options are applied after ServerOptions.Options and
// therefore provide request-specific overrides. Returning an error rejects the
// request as invalid before execution starts.
type PromptBuilder func(ctx context.Context, req InboundRequest) (prompt string, opts []adaptor.CallOption, err error)

// ResultBuilder customizes the terminal A2A artifacts and final status text
// produced from a successful run. Returning an error turns the A2A task into a
// failed terminal state.
type ResultBuilder func(ctx context.Context, req InboundRequest, result *adaptor.Result) (BuiltResult, error)

// ServerOptions configures [NewServer]. AgentCard is required. The zero values
// select stateless execution, the last non-blank text part as prompt, the
// bounded in-memory task store, and conservative exposure.
type ServerOptions struct {
	// AgentCard is the public A2A identity of this server.
	AgentCard AgentCard

	// Session decides thread binding per inbound request. Nil uses Stateless.
	Session SessionBinding

	// Prompt, when set, replaces the default prompt extraction (the last
	// non-blank text part of the inbound message).
	Prompt PromptBuilder

	// Options are applied to every run at call scope. PromptBuilder options are
	// applied afterward.
	Options []adaptor.CallOption

	// ResultBuilder, when set, customizes successful terminal artifacts and
	// status text.
	ResultBuilder ResultBuilder

	// TaskLifecycle configures protocol task persistence and retention. Its zero
	// value uses an in-memory store retaining at most
	// DefaultEphemeralTaskLimit tasks for DefaultEphemeralTaskTTL.
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

// Server exposes a Runner through the A2A JSON-RPC protocol. Mount [Server.Handler]
// and [Server.AgentCardHandler] on host-selected routes; the host remains
// responsible for authentication middleware, TLS, and HTTP lifecycle.
type Server struct {
	runner adaptor.Runner
	card   *a2aproto.AgentCard

	handler     a2asrv.RequestHandler
	jsonHandler http.Handler
	cardHandler http.Handler
}

// NewServer assembles an A2A server around a Runner, normally an Agent or
// Thread. It panics for construction-time programming errors: a nil runner, an
// invalid Agent Card or task lifecycle, or support that disagrees with the
// advertised capability flags.
func NewServer(runner adaptor.Runner, opts ServerOptions) *Server {
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
		opts.Session = Stateless()
	}
	prompt := opts.Prompt
	if prompt == nil {
		prompt = defaultPrompt
	}

	exec := &executor{
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
	return &Server{
		runner:      runner,
		card:        card,
		handler:     requestHandler,
		jsonHandler: a2asrv.NewJSONRPCHandler(requestHandler),
		cardHandler: a2asrv.NewStaticAgentCardHandler(card),
	}
}

// Handler returns the A2A JSON-RPC endpoint handler.
func (s *Server) Handler() http.Handler {
	return s.jsonHandler
}

// AgentCardHandler returns an HTTP handler that serves the effective public
// Agent Card.
func (s *Server) AgentCardHandler() http.Handler {
	return s.cardHandler
}

// AgentCard returns a copy of the effective public card after bridge-owned
// extensions and defaults have been applied.
func (s *Server) AgentCard() AgentCard {
	return publicCard(s.card)
}

type executor struct {
	runner        adaptor.Runner
	session       SessionBinding
	prompt        PromptBuilder
	options       []adaptor.CallOption
	resultBuilder ResultBuilder
	exposure      ExposurePolicy

	mu      sync.Mutex
	active  map[a2aproto.TaskID]adaptor.Stream
	pending map[a2aproto.TaskID]context.CancelFunc
}

func (e *executor) Execute(ctx context.Context, execCtx *a2asrv.ExecutorContext) iter.Seq2[a2aproto.Event, error] {
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
			yield(canceledStatus(execCtx, runCtx.Err()), nil)
			return
		}

		// Stream never fails to start: startup problems close the event
		// channel immediately and surface through Result() below.
		stream := target.Stream(runCtx, prompt, callOpts...)
		defer stream.Cancel()
		e.store(execCtx.TaskID, stream)
		defer e.delete(execCtx.TaskID)

		// 3. Forward unified events as working-status DataParts.
		translator := newStreamTranslator(execCtx, e.exposure)
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
		// 4. Project exactly one terminal outcome. Business failures carry
		// their Result in *RunError; cancellation and infrastructure failures
		// are plain wrapped errors.
		res, runErr := stream.Result()
		if runErr != nil {
			if errors.Is(runErr, context.Canceled) {
				yield(canceledStatus(execCtx, runErr), nil)
				return
			}
			var re *adaptor.RunError
			if errors.As(runErr, &re) {
				for _, ev := range defaultTerminalArtifacts(execCtx, re.Result, e.exposure) {
					if !yield(ev, nil) {
						return
					}
				}
				msg := failureMessage(execCtx, defaultString(re.Message, re.Error()), failureDetails(re, e.exposure))
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
		artifacts, err := terminalArtifacts(execCtx, res, e.exposure, built)
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
		yield(a2aproto.NewStatusUpdateEvent(execCtx, a2aproto.TaskStateCompleted, agentMessage(execCtx, completedStatusText(res, built))), nil)
	}
}

func (e *executor) buildResult(ctx context.Context, req InboundRequest, result *adaptor.Result) (BuiltResult, error) {
	if e == nil || e.resultBuilder == nil {
		return BuiltResult{}, nil
	}
	return e.resultBuilder(ctx, req, result)
}

func (e *executor) Cancel(ctx context.Context, execCtx *a2asrv.ExecutorContext) iter.Seq2[a2aproto.Event, error] {
	return func(yield func(a2aproto.Event, error) bool) {
		e.cancelPending(execCtx.TaskID)
		if stream := e.load(execCtx.TaskID); stream != nil {
			stream.Cancel()
		}
		msg := agentMessage(execCtx, "task cancelled")
		yield(a2aproto.NewStatusUpdateEvent(execCtx, a2aproto.TaskStateCanceled, msg), nil)
	}
}

func (e *executor) store(id a2aproto.TaskID, stream adaptor.Stream) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.active[id] = stream
}

func (e *executor) load(id a2aproto.TaskID) adaptor.Stream {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.active[id]
}

func (e *executor) delete(id a2aproto.TaskID) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.active, id)
}

func (e *executor) storePending(id a2aproto.TaskID, cancel context.CancelFunc) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.pending[id] = cancel
}

func (e *executor) cancelPending(id a2aproto.TaskID) {
	e.mu.Lock()
	cancel := e.pending[id]
	e.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (e *executor) deletePending(id a2aproto.TaskID) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.pending, id)
}

func completedStatusText(result *adaptor.Result, built BuiltResult) string {
	if built.StatusText != nil {
		return *built.StatusText
	}
	if result == nil {
		return ""
	}
	return result.Text
}

func canceledStatus(info a2aproto.TaskInfoProvider, cause error) *a2aproto.TaskStatusUpdateEvent {
	msg := "task cancelled"
	if cause != nil {
		msg = cause.Error()
	}
	return a2aproto.NewStatusUpdateEvent(info, a2aproto.TaskStateCanceled, failureMessage(info, msg, map[string]any{"code": string(adaptor.ReasonCancelled)}))
}

func defaultPrompt(_ context.Context, req InboundRequest) (string, []adaptor.CallOption, error) {
	for i := len(req.Message.Parts) - 1; i >= 0; i-- {
		part := req.Message.Parts[i]
		if part.Kind == PartText && strings.TrimSpace(part.Text) != "" {
			return part.Text, nil, nil
		}
	}
	return "", nil, fmt.Errorf("no user text part in A2A message")
}
