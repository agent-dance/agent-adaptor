package a2adelegation

// Local delegation loopback (P4.6, design doc §9.8 / decision D5).
//
// A Local(key, runner, policy) target is registered in the ordinary Registry
// (which is A2A-protocol-only and therefore requires an AgentCard), backed by
// a synthetic in-process card, and served by localClient: an A2AClient that
// executes the Runner directly instead of dialing anything. The runner's
// next-gen event stream is projected into A2A status updates carrying
// adapter.stream.v1 DataParts — the same high-fidelity envelope the remote
// bridge emits — so the existing eventMapper decodes local and remote targets
// through one code path and both yield identical DelegationEvent sequences
// (the Local/Remote parity gate).

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"

	bridgea2a "github.com/agent-dance/agent-adaptor/bridges/a2a"
	clienta2a "github.com/agent-dance/agent-adaptor/clients/a2a"
	"github.com/agent-dance/agent-adaptor/driver"
	adaptor "github.com/agent-dance/agent-adaptor/next"
)

// localRoleAgent mirrors the wire spelling of the A2A agent role so local
// result messages compare equal to remote ones.
const localRoleAgent = "ROLE_AGENT"

// localAgentCard synthesizes the in-process AgentCard a Local target
// registers with (Registry.Register requires a card or a card URL).
// Streaming is enabled so the Delegator always takes the streaming path.
func localAgentCard(key string) *clienta2a.AgentCard {
	return &clienta2a.AgentCard{
		Name:        key,
		Description: "in-process delegation target (delegation.Local)",
		Version:     "local",
		Capabilities: clienta2a.Capabilities{
			Streaming: true,
		},
	}
}

// localClient is the Runner-backed A2AClient loopback.
type localClient struct {
	key    string
	runner Runner

	taskSeq atomic.Uint64

	mu    sync.Mutex
	tasks map[string]clienta2a.Task
}

var _ A2AClient = (*localClient)(nil)

func newLocalClient(key string, runner Runner) *localClient {
	return &localClient{key: key, runner: runner, tasks: make(map[string]clienta2a.Task)}
}

// AgentCard returns the synthetic card; the Delegator overlays the spec's
// own AgentCard anyway, so this only needs to be non-failing.
func (c *localClient) AgentCard(ctx context.Context) (clienta2a.AgentCard, error) {
	return *localAgentCard(c.key), nil
}

// Send executes the runner to completion (non-streaming path).
func (c *localClient) Send(ctx context.Context, req clienta2a.SendRequest) (clienta2a.Task, error) {
	taskID, contextID := c.newTaskIdentity(req)
	res, runErr := c.runner.Run(ctx, promptFromMessage(req.Message))
	task := c.finalTask(taskID, contextID, res, runErr)
	c.storeTask(task)
	return task, nil
}

// SendStream executes the runner via Stream and returns an A2AStream that
// replays the run as A2A events: an initial working status, one status
// update per projected adapter.stream.v1 frame, and a terminal Task.
func (c *localClient) SendStream(ctx context.Context, req clienta2a.SendRequest) (A2AStream, error) {
	taskID, contextID := c.newTaskIdentity(req)
	// The runner stream must outlive this call but die with the delegation:
	// receiveA2AStream cancels via stream close + ctx, and the Delegator's
	// timeout context is the ctx we get here.
	runCtx, cancel := context.WithCancel(ctx)
	stream := c.runner.Stream(runCtx, promptFromMessage(req.Message))
	ls := &localA2AStream{
		client:    c,
		taskID:    taskID,
		contextID: contextID,
		stream:    stream,
		cancel:    cancel,
	}
	c.storeTask(clienta2a.Task{
		ID:        taskID,
		ContextID: contextID,
		Status:    clienta2a.TaskStatus{State: clienta2a.TaskStateWorking},
	})
	return ls, nil
}

// GetTask returns the latest snapshot recorded for the task.
func (c *localClient) GetTask(ctx context.Context, req clienta2a.GetTaskRequest) (clienta2a.Task, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	task, ok := c.tasks[req.TaskID]
	if !ok {
		return clienta2a.Task{}, fmt.Errorf("a2adelegation: local task %q not found", req.TaskID)
	}
	return task, nil
}

// CancelTask marks the task canceled; the in-flight runner context is owned
// by the stream and is canceled through A2AStream.Close.
func (c *localClient) CancelTask(ctx context.Context, req clienta2a.CancelTaskRequest) (clienta2a.Task, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	task, ok := c.tasks[req.TaskID]
	if !ok {
		return clienta2a.Task{}, fmt.Errorf("a2adelegation: local task %q not found", req.TaskID)
	}
	if !task.Status.State.Terminal() {
		task.Status = clienta2a.TaskStatus{State: clienta2a.TaskStateCanceled}
		c.tasks[req.TaskID] = task
	}
	return task, nil
}

func (c *localClient) newTaskIdentity(req clienta2a.SendRequest) (taskID, contextID string) {
	taskID = fmt.Sprintf("local-%s-%d", c.key, c.taskSeq.Add(1))
	contextID = req.ContextID
	if contextID == "" {
		contextID = taskID + "-ctx"
	}
	return taskID, contextID
}

func (c *localClient) storeTask(task clienta2a.Task) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tasks[task.ID] = task
}

// finalTask folds the runner outcome into a terminal A2A task. Business
// failures (*adaptor.RunError) and infrastructure errors map to failed;
// context cancellation maps to canceled; success carries the final text as
// a single agent message so resultFromTask lifts it into Summary/Messages.
func (c *localClient) finalTask(taskID, contextID string, res *adaptor.Result, runErr error) clienta2a.Task {
	task := clienta2a.Task{ID: taskID, ContextID: contextID}
	var finalText string
	switch {
	case runErr == nil:
		task.Status = clienta2a.TaskStatus{State: clienta2a.TaskStateCompleted}
		if res != nil {
			finalText = res.Text
		}
	case errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded):
		task.Status = clienta2a.TaskStatus{State: clienta2a.TaskStateCanceled}
	default:
		task.Status = clienta2a.TaskStatus{State: clienta2a.TaskStateFailed}
		var runFail *adaptor.RunError
		if errors.As(runErr, &runFail) && runFail.Result != nil {
			finalText = runFail.Result.Text
		}
	}
	if finalText != "" {
		task.Messages = []clienta2a.Message{{
			ID:        taskID + ":final",
			Role:      localRoleAgent,
			TaskID:    taskID,
			ContextID: contextID,
			Parts:     []clienta2a.Part{{Kind: clienta2a.PartText, Text: finalText}},
		}}
	}
	c.storeTask(task)
	return task
}

// promptFromMessage extracts the objective text the MCP tool packed into the
// outgoing A2A message.
func promptFromMessage(msg clienta2a.Message) string {
	var b strings.Builder
	for _, part := range msg.Parts {
		if part.Kind == clienta2a.PartText && part.Text != "" {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(part.Text)
		}
	}
	return b.String()
}

// localA2AStream adapts a next-gen Stream to the A2AStream contract.
type localA2AStream struct {
	client    *localClient
	taskID    string
	contextID string
	stream    adaptor.Stream
	cancel    context.CancelFunc

	seq         uint64
	sentInitial bool
	sentFinal   bool
	closeOnce   sync.Once
}

var _ A2AStream = (*localA2AStream)(nil)

// Recv satisfies the minimal A2AStream contract; the Delegator prefers
// RecvContext when available.
func (s *localA2AStream) Recv() (clienta2a.Event, error) {
	return s.RecvContext(context.Background())
}

// RecvContext returns the next A2A event: first a plain working status, then
// one adapter.stream.v1 status update per projected runner event, and finally
// the terminal Task built from Stream.Result(). After the terminal event it
// returns io.EOF.
func (s *localA2AStream) RecvContext(ctx context.Context) (clienta2a.Event, error) {
	if !s.sentInitial {
		s.sentInitial = true
		status := clienta2a.TaskStatus{State: clienta2a.TaskStateWorking}
		return clienta2a.Event{
			Kind:      clienta2a.EventStatus,
			TaskID:    s.taskID,
			ContextID: s.contextID,
			Status:    &status,
		}, nil
	}
	if s.sentFinal {
		return clienta2a.Event{}, io.EOF
	}
	for {
		select {
		case <-ctx.Done():
			return clienta2a.Event{}, ctx.Err()
		case ev, ok := <-s.stream.Events():
			if !ok {
				return s.finalEvent()
			}
			frame, matched := adapterStreamFrame(ev)
			if !matched {
				continue
			}
			s.seq++
			frame["sequence"] = s.seq
			envelope := map[string]any{
				"schema": bridgea2a.AdapterStreamSchemaV1,
				"event":  frame,
			}
			status := clienta2a.TaskStatus{
				State: clienta2a.TaskStateWorking,
				Message: &clienta2a.Message{
					ID:        fmt.Sprintf("sm-%d", s.seq),
					Role:      localRoleAgent,
					TaskID:    s.taskID,
					ContextID: s.contextID,
					Parts:     []clienta2a.Part{{Kind: clienta2a.PartData, Data: envelope}},
				},
			}
			return clienta2a.Event{
				Kind:      clienta2a.EventStatus,
				TaskID:    s.taskID,
				ContextID: s.contextID,
				Status:    &status,
			}, nil
		}
	}
}

// finalEvent folds Stream.Result into the terminal Task event.
func (s *localA2AStream) finalEvent() (clienta2a.Event, error) {
	s.sentFinal = true
	res, runErr := s.stream.Result()
	task := s.client.finalTask(s.taskID, s.contextID, res, runErr)
	return clienta2a.Event{
		Kind:      clienta2a.EventTerminal,
		TaskID:    s.taskID,
		ContextID: s.contextID,
		Task:      &task,
	}, nil
}

// Close cancels the underlying runner stream. Idempotent.
func (s *localA2AStream) Close() error {
	s.closeOnce.Do(func() {
		s.cancel()
		s.stream.Cancel()
	})
	return nil
}

// adapterStreamFrame projects one next-gen event into an adapter.stream.v1
// event object (without sequence, which the caller stamps). The kind strings
// are the driver SPI StreamKind values — exactly what the remote bridge emits
// and what adapterStreamStatusDecoder understands, which is what makes Local
// and Remote event sequences comparable.
func adapterStreamFrame(ev adaptor.Event) (map[string]any, bool) {
	switch e := ev.(type) {
	case adaptor.TextDelta:
		if e.Role == adaptor.RoleUser {
			return nil, false
		}
		frame := map[string]any{"message_id": e.MessageID}
		switch e.Phase {
		case adaptor.PhaseStart:
			frame["kind"] = string(driver.StreamTextStart)
		case adaptor.PhaseEnd:
			frame["kind"] = string(driver.StreamTextEnd)
		default:
			if e.Text == "" {
				return nil, false
			}
			frame["kind"] = string(driver.StreamTextContent)
			frame["delta"] = e.Text
		}
		return frame, true
	case adaptor.Thinking:
		frame := map[string]any{"message_id": e.MessageID}
		switch e.Phase {
		case adaptor.PhaseStart:
			frame["kind"] = string(driver.StreamReasoningStart)
		case adaptor.PhaseEnd:
			frame["kind"] = string(driver.StreamReasoningEnd)
		default:
			if e.Text == "" {
				return nil, false
			}
			frame["kind"] = string(driver.StreamReasoningContent)
			frame["delta"] = e.Text
		}
		return frame, true
	case adaptor.ToolCall:
		frame := map[string]any{"tool_call_id": e.ID}
		switch e.Phase {
		case adaptor.PhaseStart:
			frame["kind"] = string(driver.StreamToolCallStart)
			frame["name"] = e.Name
			if len(e.Args) > 0 {
				frame["args"] = e.Args
			}
		case adaptor.PhaseEnd:
			frame["kind"] = string(driver.StreamToolCallEnd)
			if len(e.Result) > 0 {
				frame["result"] = e.Result
			}
		default:
			if e.ArgsDelta == "" {
				return nil, false
			}
			frame["kind"] = string(driver.StreamToolCallArgs)
			frame["delta"] = e.ArgsDelta
		}
		return frame, true
	case adaptor.ToolResult:
		frame := map[string]any{"tool_call_id": e.ID, "kind": string(driver.StreamToolCallResult)}
		if len(e.Result) > 0 {
			frame["result"] = e.Result
		}
		return frame, true
	default:
		// Lifecycle, process, notice, approval, dropped, and nested
		// subagent events stay local: the delegation channel carries the
		// semantic stream only.
		return nil, false
	}
}
