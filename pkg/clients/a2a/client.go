package a2a

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"

	a2aproto "github.com/a2aproject/a2a-go/v2/a2a"
	upclient "github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/a2aproject/a2a-go/v2/a2aclient/agentcard"
)

type Options struct {
	AgentCardURL        string
	AgentCardPath       string
	Auth                Auth
	HTTPClient          *http.Client
	AcceptedOutputModes []string
	PreferredTransports []TransportProtocol
}

type Client struct {
	opts       Options
	httpClient *http.Client

	mu         sync.Mutex
	card       *a2aproto.AgentCard
	publicCard AgentCard
	upstream   *upclient.Client
}

func New(opts Options) *Client {
	return &Client{opts: opts, httpClient: httpClientWithAuth(opts.HTTPClient, opts.Auth)}
}

func (c *Client) AgentCard(ctx context.Context) (AgentCard, error) {
	if err := c.ensure(ctx); err != nil {
		return AgentCard{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.publicCard, nil
}

func (c *Client) Send(ctx context.Context, req SendRequest) (Task, error) {
	up, err := c.ensureClient(ctx)
	if err != nil {
		return Task{}, err
	}
	result, err := up.SendMessage(ctx, upstreamSendRequest(req))
	if err != nil {
		return Task{}, classifyError("SendMessage", err)
	}
	switch v := result.(type) {
	case *a2aproto.Task:
		return convertTask(v), nil
	case *a2aproto.Message:
		msg := convertMessage(v)
		return Task{
			ID: msg.TaskID, ContextID: msg.ContextID,
			Status:   TaskStatus{State: TaskStateCompleted, Message: &msg},
			Messages: []Message{msg},
			Raw:      map[string]any{"message": msg.Raw},
		}, nil
	default:
		return Task{}, &ProtocolError{Op: "SendMessage", Reason: fmt.Sprintf("unexpected result %T", result), Cause: ErrProtocol}
	}
}

func (c *Client) SendStream(ctx context.Context, req SendRequest) (*Stream, error) {
	up, err := c.ensureClient(ctx)
	if err != nil {
		return nil, err
	}
	taskID := req.TaskID
	streamCtx, cancel := context.WithCancel(ctx)
	seq := up.SendStreamingMessage(streamCtx, upstreamSendRequest(req))
	return c.startStream(streamCtx, cancel, taskID, seq), nil
}

func (c *Client) Subscribe(ctx context.Context, req SubscribeRequest) (*Stream, error) {
	if req.TaskID == "" {
		return nil, fmt.Errorf("%w: task id is required", ErrProtocol)
	}
	up, err := c.ensureClient(ctx)
	if err != nil {
		return nil, err
	}
	streamCtx, cancel := context.WithCancel(ctx)
	streamReq := &a2aproto.SubscribeToTaskRequest{ID: a2aproto.TaskID(req.TaskID), Tenant: req.Tenant}
	seq := up.SubscribeToTask(streamCtx, streamReq)
	return c.startStream(streamCtx, cancel, req.TaskID, seq), nil
}

func (c *Client) GetTask(ctx context.Context, taskID string) (Task, error) {
	if taskID == "" {
		return Task{}, fmt.Errorf("%w: task id is required", ErrProtocol)
	}
	up, err := c.ensureClient(ctx)
	if err != nil {
		return Task{}, err
	}
	task, err := up.GetTask(ctx, &a2aproto.GetTaskRequest{ID: a2aproto.TaskID(taskID)})
	if err != nil {
		return Task{}, classifyError("GetTask", err)
	}
	return convertTask(task), nil
}

func (c *Client) CancelTask(ctx context.Context, taskID string) (Task, error) {
	if taskID == "" {
		return Task{}, fmt.Errorf("%w: task id is required", ErrProtocol)
	}
	up, err := c.ensureClient(ctx)
	if err != nil {
		return Task{}, err
	}
	task, err := up.CancelTask(ctx, &a2aproto.CancelTaskRequest{ID: a2aproto.TaskID(taskID)})
	if err != nil {
		return Task{}, classifyError("CancelTask", err)
	}
	return convertTask(task), nil
}

func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.upstream == nil {
		return nil
	}
	return c.upstream.Destroy()
}

func (c *Client) ensureClient(ctx context.Context) (*upclient.Client, error) {
	if err := c.ensure(ctx); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.upstream, nil
}

func (c *Client) ensure(ctx context.Context) error {
	c.mu.Lock()
	if c.upstream != nil {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()

	card, public, err := c.fetchCard(ctx)
	if err != nil {
		return err
	}
	cfg := upclient.Config{AcceptedOutputModes: append([]string(nil), c.opts.AcceptedOutputModes...)}
	for _, tr := range c.opts.PreferredTransports {
		cfg.PreferredTransports = append(cfg.PreferredTransports, a2aproto.TransportProtocol(tr))
	}
	up, err := upclient.NewFromCard(ctx, card, upclient.WithJSONRPCTransport(c.httpClient), upclient.WithRESTTransport(c.httpClient), upclient.WithConfig(cfg))
	if err != nil {
		return classifyError("NewFromCard", err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.upstream != nil {
		_ = up.Destroy()
		return nil
	}
	c.card = card
	c.publicCard = public
	c.upstream = up
	return nil
}

func (c *Client) fetchCard(ctx context.Context) (*a2aproto.AgentCard, AgentCard, error) {
	base, path, err := splitAgentCardURL(c.opts.AgentCardURL, c.opts.AgentCardPath)
	if err != nil {
		return nil, AgentCard{}, err
	}
	resolver := agentcard.NewResolver(c.httpClient)
	var resolveOpts []agentcard.ResolveOption
	if path != "" {
		resolveOpts = append(resolveOpts, agentcard.WithPath(path))
	}
	for k, v := range c.authHeaders() {
		resolveOpts = append(resolveOpts, agentcard.WithRequestHeader(k, v))
	}
	card, err := resolver.Resolve(ctx, base, resolveOpts...)
	if err != nil {
		return nil, AgentCard{}, classifyError("AgentCard", err)
	}
	if err := validateAgentCard(card); err != nil {
		return nil, AgentCard{}, err
	}
	public, err := convertAgentCard(card)
	if err != nil {
		return nil, AgentCard{}, err
	}
	return card, public, nil
}

func (c *Client) authHeaders() map[string]string {
	if c.opts.Auth == nil {
		return nil
	}
	return c.opts.Auth.Headers()
}

func upstreamSendRequest(req SendRequest) *a2aproto.SendMessageRequest {
	msg := upstreamMessage(req.Message)
	if req.ContextID != "" {
		msg.ContextID = req.ContextID
	}
	if req.TaskID != "" {
		msg.TaskID = a2aproto.TaskID(req.TaskID)
	}
	return &a2aproto.SendMessageRequest{
		Tenant:   req.Tenant,
		Message:  msg,
		Metadata: cloneMap(req.Metadata),
		Config: &a2aproto.SendMessageConfig{
			AcceptedOutputModes: append([]string(nil), req.AcceptedOutputModes...),
			ReturnImmediately:   req.ReturnImmediately,
			HistoryLength:       req.HistoryLength,
		},
	}
}

func httpClientWithAuth(base *http.Client, auth Auth) *http.Client {
	if base == nil {
		base = http.DefaultClient
	}
	copy := *base
	if auth != nil {
		copy.Transport = auth.Wrap(base.Transport)
	}
	return &copy
}

func splitAgentCardURL(raw, overridePath string) (base, path string, err error) {
	if raw == "" {
		return "", "", fmt.Errorf("%w: AgentCardURL is required", ErrInvalidAgentCard)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", fmt.Errorf("%w: invalid AgentCardURL: %v", ErrInvalidAgentCard, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", "", fmt.Errorf("%w: AgentCardURL must be absolute", ErrInvalidAgentCard)
	}
	path = overridePath
	if path == "" && u.Path != "" && u.Path != "/" {
		path = u.Path
	}
	u.Path, u.RawPath, u.RawQuery, u.Fragment = "", "", "", ""
	return u.String(), path, nil
}

func classifyError(op string, err error) error {
	if err == nil {
		return nil
	}
	raw := protocolErrorRaw(err)
	switch {
	case errors.Is(err, a2aproto.ErrTaskNotFound):
		return &ProtocolError{Op: op, Reason: err.Error(), Cause: ErrNotFound, Raw: raw}
	case errors.Is(err, a2aproto.ErrUnauthenticated), errors.Is(err, a2aproto.ErrUnauthorized):
		return &ProtocolError{Op: op, Reason: err.Error(), Cause: ErrUnauthorized, Raw: raw}
	case errors.Is(err, a2aproto.ErrUnsupportedOperation), errors.Is(err, a2aproto.ErrUnsupportedContentType):
		return &ProtocolError{Op: op, Reason: err.Error(), Cause: ErrUnsupported, Raw: raw}
	default:
		return &ProtocolError{Op: op, Reason: err.Error(), Cause: err, Raw: raw}
	}
}

func protocolErrorRaw(err error) map[string]any {
	var a2aErr *a2aproto.Error
	if !errors.As(err, &a2aErr) {
		return nil
	}
	raw := map[string]any{}
	if a2aErr.Message != "" {
		raw["message"] = a2aErr.Message
	}
	if a2aErr.Err != nil {
		raw["cause"] = a2aErr.Err.Error()
		raw["reason"] = a2aproto.ErrorReason(a2aErr.Err)
	}
	if len(a2aErr.Details) > 0 {
		raw["details"] = cloneMap(a2aErr.Details)
	}
	if len(a2aErr.TypedDetails) > 0 {
		typed := make([]map[string]any, 0, len(a2aErr.TypedDetails))
		for _, detail := range a2aErr.TypedDetails {
			if detail == nil {
				continue
			}
			value := map[string]any{}
			for k, v := range detail.Value {
				value[k] = v
			}
			value["@type"] = detail.TypeURL
			typed = append(typed, value)
		}
		if len(typed) > 0 {
			raw["typed_details"] = typed
		}
	}
	if len(raw) == 0 {
		return nil
	}
	return raw
}

type Stream struct {
	cancel context.CancelFunc
	events <-chan streamItem
}

type streamItem struct {
	event Event
	err   error
}

func (s *Stream) Recv() (Event, error) {
	item, ok := <-s.events
	if !ok {
		return Event{}, io.EOF
	}
	return item.event, item.err
}

func (s *Stream) Close() error {
	if s.cancel != nil {
		s.cancel()
	}
	for range s.events {
	}
	return nil
}

func (c *Client) startStream(streamCtx context.Context, cancel context.CancelFunc, taskID string, seq func(func(a2aproto.Event, error) bool)) *Stream {
	out := make(chan streamItem, 32)
	go func() {
		defer close(out)
		lastTaskID := taskID
		terminal := false
		seq(func(ev a2aproto.Event, err error) bool {
			if err != nil {
				if !terminal {
					if recovered, ok := c.tryRecover(streamCtx, lastTaskID); ok {
						out <- streamItem{event: recovered}
						return false
					}
				}
				out <- streamItem{err: &StreamRecoveryError{TaskID: lastTaskID, Cause: err}}
				return false
			}
			event, convErr := eventFromUpstream(ev)
			if convErr != nil {
				out <- streamItem{err: convErr}
				return false
			}
			if event.TaskID != "" {
				lastTaskID = event.TaskID
			}
			if event.Kind == EventTerminal {
				terminal = true
			}
			select {
			case <-streamCtx.Done():
				out <- streamItem{err: streamCtx.Err()}
				return false
			case out <- streamItem{event: event}:
				return true
			}
		})
	}()
	return &Stream{cancel: cancel, events: out}
}

func (c *Client) tryRecover(ctx context.Context, taskID string) (Event, bool) {
	if taskID == "" {
		return Event{}, false
	}
	task, err := c.GetTask(ctx, taskID)
	if err != nil || !task.Status.State.Terminal() {
		return Event{}, false
	}
	return Event{Kind: EventTerminal, Task: &task, TaskID: task.ID, ContextID: task.ContextID, RecoveredState: true, Raw: task.Raw}, true
}
