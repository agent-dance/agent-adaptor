package a2a

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"strings"

	a2aproto "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	adaptor "github.com/agent-dance/agent-adaptor/v2"
)

// messageRouter intercepts same-task input before the upstream execution
// manager, which otherwise rejects messages addressed to working tasks.
// All execution, persistence, subscriptions and cancellation remain upstream.
type messageRouter struct {
	a2asrv.RequestHandler
	executor  *executor
	streaming bool
}

func taskInProgress(id a2aproto.TaskID) error {
	return a2aproto.NewError(a2aproto.ErrInvalidRequest, fmt.Sprintf("task in progress: %s", id)).WithDetails(map[string]any{"code": "task_in_progress", "activeTaskId": string(id)})
}

func taskFinished(task *a2aproto.Task) error {
	return a2aproto.NewError(a2aproto.ErrInvalidRequest, fmt.Sprintf("task finished: %s (%s)", task.ID, task.Status.State)).WithDetails(map[string]any{"code": "task_finished", "taskId": string(task.ID), "state": string(task.Status.State)})
}

func (h *messageRouter) busyContext(contextID string) a2aproto.TaskID {
	if contextID == "" {
		return ""
	}
	h.executor.mu.Lock()
	defer h.executor.mu.Unlock()
	return h.executor.contexts[contextID]
}

func validateMessageRequest(req *a2aproto.SendMessageRequest) error {
	if req == nil || req.Message == nil {
		return a2aproto.ErrInvalidParams
	}
	msg := req.Message
	if msg.ID == "" || len(msg.Parts) == 0 || (msg.Role != a2aproto.MessageRoleUser && msg.Role != a2aproto.MessageRoleAgent) {
		return a2aproto.ErrInvalidParams
	}
	return nil
}

func (h *messageRouter) SendMessage(ctx context.Context, req *a2aproto.SendMessageRequest) (a2aproto.SendMessageResult, error) {
	if err := validateMessageRequest(req); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	msg := req.Message
	if msg.TaskID == "" {
		if busy := h.busyContext(msg.ContextID); busy != "" {
			return nil, taskInProgress(busy)
		}
		return h.RequestHandler.SendMessage(ctx, req)
	}
	task, err := h.GetTask(ctx, &a2aproto.GetTaskRequest{ID: msg.TaskID, Tenant: req.Tenant})
	if err != nil {
		return nil, err
	}
	if msg.ContextID != "" && msg.ContextID != task.ContextID {
		return nil, a2aproto.NewError(a2aproto.ErrInvalidParams, "taskId and contextId disagree")
	}
	if task.Status.State.Terminal() {
		return nil, taskFinished(task)
	}
	stream := h.executor.load(msg.TaskID)
	if stream == nil {
		return nil, taskInProgress(msg.TaskID)
	}
	converted, err := convertMessage(msg)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", a2aproto.ErrInvalidParams, err)
	}
	input := InboundRequest{TaskID: string(task.ID), ContextID: task.ContextID, Message: converted, Metadata: cloneMap(req.Metadata)}
	text, options, err := h.executor.prompt(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", a2aproto.ErrInvalidParams, err)
	}
	if strings.TrimSpace(text) == "" {
		return nil, a2aproto.NewError(a2aproto.ErrInvalidParams, "prompt is empty")
	}
	// PromptBuilder execution options cannot change an already running invocation.
	for _, option := range options {
		if _, ok := option.(adaptor.MessageOption); !ok {
			return nil, a2aproto.NewError(a2aproto.ErrInvalidParams, "supplements cannot carry execution options")
		}
	}
	author, err := messageAuthor(converted)
	if err != nil {
		return nil, err
	}
	attrs := make([]adaptor.MessageOption, 0, len(options)+2)
	for _, option := range options {
		attrs = append(attrs, option.(adaptor.MessageOption))
	}
	// Wire identity and authority take precedence over PromptBuilder options.
	attrs = append(attrs, adaptor.MessageID(msg.ID), adaptor.From(author))
	// Protocol input belongs to the existing task once dispatch starts.
	// Stream.Send's native ctx lifetime extends until Claim; it has no atomic
	// admission-only handoff. Explicitly detach here, so ending a short HTTP
	// request cannot retract accepted input. A blocked queue may therefore
	// keep waiting after disconnect, until this execution makes room or ends.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	err = stream.Send(context.WithoutCancel(ctx), text, attrs...)
	if err != nil {
		code := "message_rejected"
		message := err.Error()
		if errors.Is(err, adaptor.ErrRunFinished) {
			code = "task_finished"
			message = fmt.Sprintf("task finished: %s", task.ID)
		}
		return nil, a2aproto.NewError(a2aproto.ErrInvalidRequest, message).WithDetails(map[string]any{"code": code, "taskId": string(task.ID), "messageId": msg.ID})
	}
	// MessageUpdate on the original stream reports delivery or failure. This
	// response acknowledges admission, never provider acceptance.
	return task, nil
}

func (h *messageRouter) SendStreamingMessage(ctx context.Context, req *a2aproto.SendMessageRequest) iter.Seq2[a2aproto.Event, error] {
	return func(yield func(a2aproto.Event, error) bool) {
		if !h.streaming {
			yield(nil, a2aproto.ErrUnsupportedOperation)
			return
		}
		if err := validateMessageRequest(req); err != nil {
			yield(nil, err)
			return
		}
		if req.Message.TaskID == "" {
			if busy := h.busyContext(req.Message.ContextID); busy != "" {
				yield(nil, taskInProgress(busy))
				return
			}
		}
		if req.Message.TaskID == "" {
			for event, err := range h.RequestHandler.SendStreamingMessage(ctx, req) {
				if !yield(event, err) {
					return
				}
			}
			return
		}
		// Upstream subscriptions are lazy and start from a task snapshot.
		// Pull that snapshot before admission, while its reader is registered,
		// so a fast Claim/Ack cannot outrun this request's event subscription.
		next, stop := iter.Pull2(h.SubscribeToTask(ctx, &a2aproto.SubscribeToTaskRequest{ID: req.Message.TaskID, Tenant: req.Tenant}))
		defer stop()
		first, subscribeErr, open := next()
		if subscribeErr != nil || !open {
			if latest, getErr := h.GetTask(ctx, &a2aproto.GetTaskRequest{ID: req.Message.TaskID, Tenant: req.Tenant}); getErr == nil && latest.Status.State.Terminal() {
				yield(nil, taskFinished(latest))
				return
			}
			if subscribeErr == nil {
				subscribeErr = a2aproto.NewError(a2aproto.ErrInvalidAgentResponse, "task subscription ended before message admission")
			}
			yield(nil, subscribeErr)
			return
		}
		result, err := h.SendMessage(ctx, req)
		if err != nil {
			yield(nil, err)
			return
		}
		task := result.(*a2aproto.Task)
		if !yield(task, nil) {
			return
		}
		if _, snapshot := first.(*a2aproto.Task); !snapshot {
			if !yield(first, nil) {
				return
			}
		}
		for {
			event, err, open := next()
			if !open {
				return
			}
			if err != nil {
				if latest, getErr := h.GetTask(ctx, &a2aproto.GetTaskRequest{ID: task.ID, Tenant: req.Tenant}); getErr == nil && latest.Status.State.Terminal() {
					yield(latest, nil)
					return
				}
			}
			if !yield(event, err) {
				return
			}
		}
	}
}

// messageAuthor uses the protocol role as authority. Optional
// agentadaptor.author metadata supplies id/name and can lower a user message
// to peer authority; remote input cannot claim host-system authority.
func messageAuthor(msg Message) (adaptor.Author, error) {
	author := adaptor.Author{Kind: adaptor.AuthorHuman}
	if msg.Role == string(a2aproto.MessageRoleAgent) {
		author.Kind = adaptor.AuthorAgent
	}
	if raw, present := msg.Metadata["agentadaptor.author"]; present {
		fields, ok := raw.(map[string]any)
		if !ok {
			return adaptor.Author{}, a2aproto.NewError(a2aproto.ErrInvalidParams, "author metadata must be an object")
		}
		for key, target := range map[string]*string{"id": &author.ID, "name": &author.Name} {
			if value, present := fields[key]; present {
				text, ok := value.(string)
				if !ok {
					return adaptor.Author{}, a2aproto.NewError(a2aproto.ErrInvalidParams, "author "+key+" must be a string")
				}
				*target = text
			}
		}
		if value, present := fields["kind"]; present {
			kind, ok := value.(string)
			if !ok {
				return adaptor.Author{}, a2aproto.NewError(a2aproto.ErrInvalidParams, "author kind must be a string")
			}
			switch kind {
			case "", "human":
				if kind == "human" && author.Kind == adaptor.AuthorAgent {
					return adaptor.Author{}, a2aproto.NewError(a2aproto.ErrInvalidParams, "agent role cannot claim human authority")
				}
			case "agent":
				author.Kind = adaptor.AuthorAgent
			default:
				return adaptor.Author{}, a2aproto.NewError(a2aproto.ErrInvalidParams, "remote author kind must be human or agent; host authority cannot be supplied remotely")
			}
		}
	}
	return author, nil
}
