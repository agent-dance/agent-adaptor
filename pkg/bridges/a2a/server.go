package a2a

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"strings"
	"sync"
	"time"

	a2aproto "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/a2aproject/a2a-go/v2/a2asrv/taskstore"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

type Server struct {
	runner agentadaptor.Runner
	card   *a2aproto.AgentCard

	handler     a2asrv.RequestHandler
	jsonHandler http.Handler
	cardHandler http.Handler
}

func NewServer(runner agentadaptor.Runner, opts ServerOptions) *Server {
	if runner == nil {
		panic("a2a bridge: nil runner")
	}
	card, err := buildAgentCard(opts.AgentCard)
	if err != nil {
		panic(err)
	}
	if opts.Session == nil {
		opts.Session = Stateless()
	}
	if opts.Prompt == nil {
		opts.Prompt = PromptBuilderFunc(defaultPrompt)
	}

	exec := &executor{
		runner: runner, session: opts.Session, prompt: opts.Prompt,
		runOptions: append([]agentadaptor.RunOption(nil), opts.RunOptions...),
		active:     map[a2aproto.TaskID]agentadaptor.RunHandle{},
	}
	requestHandler := a2asrv.NewHandler(
		exec,
		a2asrv.WithTaskStore(taskstore.NewInMemory(nil)),
		a2asrv.WithCapabilityChecks(&card.Capabilities),
	)
	return &Server{
		runner:      runner,
		card:        card,
		handler:     requestHandler,
		jsonHandler: a2asrv.NewJSONRPCHandler(requestHandler),
		cardHandler: a2asrv.NewStaticAgentCardHandler(card),
	}
}

func (s *Server) Handler() http.Handler {
	return s.jsonHandler
}

func (s *Server) AgentCardHandler() http.Handler {
	return s.cardHandler
}

func (s *Server) AgentCard() AgentCard {
	return publicCard(s.card)
}

type executor struct {
	runner     agentadaptor.Runner
	session    SessionMapper
	prompt     PromptBuilder
	runOptions []agentadaptor.RunOption

	mu     sync.Mutex
	active map[a2aproto.TaskID]agentadaptor.RunHandle
}

func (e *executor) Execute(ctx context.Context, execCtx *a2asrv.ExecutorContext) iter.Seq2[a2aproto.Event, error] {
	return func(yield func(a2aproto.Event, error) bool) {
		req := inboundFromExecCtx(execCtx)
		prompt, promptOpts, err := e.prompt.BuildPrompt(ctx, req)
		if err != nil {
			yield(nil, fmt.Errorf("%w: %v", a2aproto.ErrInvalidParams, err))
			return
		}
		if strings.TrimSpace(prompt) == "" {
			yield(nil, fmt.Errorf("%w: prompt is empty", a2aproto.ErrInvalidParams))
			return
		}
		sessionOpts, err := e.session.RunOptions(ctx, req)
		if err != nil {
			yield(nil, fmt.Errorf("%w: %v", a2aproto.ErrInvalidParams, err))
			return
		}
		runOpts := append([]agentadaptor.RunOption(nil), e.runOptions...)
		runOpts = append(runOpts, sessionOpts...)
		runOpts = append(runOpts, promptOpts...)
		runOpts = append(runOpts, agentadaptor.WithStreaming())

		if execCtx.StoredTask == nil && execCtx.Message != nil {
			if !yield(a2aproto.NewSubmittedTask(execCtx, execCtx.Message), nil) {
				return
			}
		}
		if !yield(a2aproto.NewStatusUpdateEvent(execCtx, a2aproto.TaskStateWorking, nil), nil) {
			return
		}

		handle, err := e.runner.Start(ctx, prompt, runOpts...)
		if err != nil {
			msg := failureMessage(execCtx, err.Error(), map[string]any{"layer": "start"})
			yield(a2aproto.NewStatusUpdateEvent(execCtx, a2aproto.TaskStateFailed, msg), nil)
			return
		}
		e.store(execCtx.TaskID, handle)
		defer e.delete(execCtx.TaskID)

		translator := newStreamTranslator(execCtx)
		waitCh := make(chan waitResult, 1)
		go func() {
			result, err := handle.Wait(ctx)
			waitCh <- waitResult{result: result, err: err}
		}()

		for {
			select {
			case <-ctx.Done():
				_ = cancelRunHandle(ctx, handle)
				msg := failureMessage(execCtx, ctx.Err().Error(), map[string]any{"code": string(agentadaptor.FailureCancelled)})
				yield(a2aproto.NewStatusUpdateEvent(execCtx, a2aproto.TaskStateCanceled, msg), nil)
				return
			case p, ok := <-handle.StreamEvents():
				if !ok {
					goto drained
				}
				for _, ev := range translator.Translate(p) {
					if !yield(ev, nil) {
						_ = cancelRunHandle(ctx, handle)
						return
					}
				}
			}
		}

	drained:
		out := <-waitCh
		if out.err != nil {
			state := a2aproto.TaskStateFailed
			if errors.Is(out.err, context.Canceled) {
				state = a2aproto.TaskStateCanceled
			}
			msg := failureMessage(execCtx, out.err.Error(), map[string]any{"layer": "wait"})
			yield(a2aproto.NewStatusUpdateEvent(execCtx, state, msg), nil)
			return
		}
		for _, ev := range terminalArtifacts(execCtx, out.result) {
			if !yield(ev, nil) {
				return
			}
		}
		if out.result.Failure != nil {
			msg := failureMessage(execCtx, out.result.Failure.Message, failureDetails(out.result.Failure))
			yield(a2aproto.NewStatusUpdateEvent(execCtx, a2aproto.TaskStateFailed, msg), nil)
			return
		}
		yield(a2aproto.NewStatusUpdateEvent(execCtx, a2aproto.TaskStateCompleted, agentMessage(execCtx, out.result.Output)), nil)
	}
}

func (e *executor) Cancel(ctx context.Context, execCtx *a2asrv.ExecutorContext) iter.Seq2[a2aproto.Event, error] {
	return func(yield func(a2aproto.Event, error) bool) {
		handle := e.load(execCtx.TaskID)
		if handle != nil {
			_ = cancelRunHandle(ctx, handle)
		}
		msg := agentMessage(execCtx, "task cancelled")
		yield(a2aproto.NewStatusUpdateEvent(execCtx, a2aproto.TaskStateCanceled, msg), nil)
	}
}

const cancelRunTimeout = 5 * time.Second

func cancelRunHandle(ctx context.Context, handle agentadaptor.RunHandle) error {
	if handle == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	cancelCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cancelRunTimeout)
	defer cancel()
	return handle.Cancel(cancelCtx)
}

type waitResult struct {
	result agentadaptor.RunResult
	err    error
}

func (e *executor) store(id a2aproto.TaskID, handle agentadaptor.RunHandle) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.active[id] = handle
}

func (e *executor) load(id a2aproto.TaskID) agentadaptor.RunHandle {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.active[id]
}

func (e *executor) delete(id a2aproto.TaskID) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.active, id)
}

type execCtxAdapter struct {
	*a2asrv.ExecutorContext
}

func inboundFromExecCtx(ctx *a2asrv.ExecutorContext) InboundRequest {
	if ctx == nil {
		return InboundRequest{}
	}
	return inboundRequest(execCtxAdapter{ctx})
}

func (e execCtxAdapter) TaskIDString() string {
	return string(e.TaskID)
}

func (e execCtxAdapter) ContextIDString() string {
	return e.ContextID
}

func (e execCtxAdapter) Message() *a2aproto.Message {
	return e.ExecutorContext.Message
}

func (e execCtxAdapter) MetadataMap() map[string]any {
	return e.Metadata
}

func defaultPrompt(_ context.Context, req InboundRequest) (string, []agentadaptor.RunOption, error) {
	for i := len(req.Message.Parts) - 1; i >= 0; i-- {
		part := req.Message.Parts[i]
		if part.Kind == PartText && strings.TrimSpace(part.Text) != "" {
			return part.Text, nil, nil
		}
	}
	return "", nil, fmt.Errorf("no user text part in A2A message")
}

func publicCard(card *a2aproto.AgentCard) AgentCard {
	if card == nil {
		return AgentCard{}
	}
	out := AgentCard{
		Name: card.Name, Description: card.Description, Version: card.Version,
		DocumentationURL: card.DocumentationURL, IconURL: card.IconURL,
		DefaultInputModes:  append([]string(nil), card.DefaultInputModes...),
		DefaultOutputModes: append([]string(nil), card.DefaultOutputModes...),
		Capabilities: Capabilities{
			Streaming:         card.Capabilities.Streaming,
			PushNotifications: card.Capabilities.PushNotifications,
			ExtendedAgentCard: card.Capabilities.ExtendedAgentCard,
		},
	}
	if card.Provider != nil {
		out.Provider = &Provider{Organization: card.Provider.Org, URL: card.Provider.URL}
	}
	for _, iface := range card.SupportedInterfaces {
		if iface == nil {
			continue
		}
		if out.URL == "" {
			out.URL = iface.URL
		}
		out.Interfaces = append(out.Interfaces, AgentInterface{
			URL: iface.URL, ProtocolBinding: string(iface.ProtocolBinding), Tenant: iface.Tenant,
			ProtocolVersion: string(iface.ProtocolVersion),
		})
	}
	for _, skill := range card.Skills {
		out.Skills = append(out.Skills, Skill{ID: skill.ID, Name: skill.Name, Description: skill.Description})
	}
	return out
}
