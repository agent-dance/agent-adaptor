package appserver

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/agent-dance/agent-adaptor/v2/driver"
	"github.com/sourcegraph/jsonrpc2"
)

const messageRPCTimeout = 8 * time.Second

// TurnSteerParams supplies input guarded by the active turn identity. These
// hand-written models follow the official generate-json-schema output.
type TurnSteerParams struct {
	// ThreadID identifies the conversation containing the active turn.
	ThreadID string `json:"threadId"`
	// ExpectedTurnID is the required active-turn precondition.
	ExpectedTurnID string `json:"expectedTurnId"`
	// Input is forwarded verbatim to the provider.
	Input []UserInput `json:"input"`
}

// TurnSteerResponse identifies the turn which accepted the input.
type TurnSteerResponse struct {
	// TurnID identifies the active turn that accepted the input.
	TurnID string `json:"turnId"`
}

// TurnSteer delivers input only when ExpectedTurnID is still active.
func (c *Client) TurnSteer(ctx context.Context, params TurnSteerParams) (*TurnSteerResponse, error) {
	var resp TurnSteerResponse
	if err := c.conn.Call(ctx, "turn/steer", params, &resp); err != nil {
		return nil, wrapRPCErr("turn/steer", err)
	}
	if resp.TurnID != params.ExpectedTurnID {
		return nil, fmt.Errorf("appserver: turn/steer accepted unexpected turn %q, want %q", resp.TurnID, params.ExpectedTurnID)
	}
	return &resp, nil
}

type turnRPC interface {
	TurnStart(context.Context, TurnStartParams) (*TurnStartResponse, error)
	TurnSteer(context.Context, TurnSteerParams) (*TurnSteerResponse, error)
	TurnInterrupt(context.Context, TurnInterruptParams) error
	DisconnectNotify() <-chan struct{}
}

// runTurns is shared by one-shot and resident transports. Only this goroutine
// claims messages and issues RPCs; the ordered notification dispatcher owns
// provider-confirmed window transitions and per-turn completion events.
func runTurns(ctx context.Context, client turnRPC, state *runState, opts Options, threadID string) (runErr error) {
	port := opts.Messages
	pending := make(map[string]driver.MessageItem)
	deferredAcks := make(map[string]driver.MessageAck)
	var deferredIDs []string
	ack := func(item driver.MessageItem, outcome driver.MessageAck) {
		delete(pending, item.ID)
		port.Ack(item.ID, outcome)
	}
	claim := func(boundary driver.Boundary) []driver.MessageItem {
		if port == nil {
			return nil
		}
		items := port.Claim(boundary)
		for _, item := range items {
			pending[item.ID] = item
		}
		return items
	}
	if port != nil {
		port.Window(false)
	}
	defer func() {
		if ctx.Err() != nil {
			state.mu.Lock()
			turnID := state.turnID
			state.mu.Unlock()
			if turnID != "" {
				interruptCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 500*time.Millisecond)
				_ = client.TurnInterrupt(interruptCtx, TurnInterruptParams{ThreadID: threadID, TurnID: turnID})
				cancel()
			}
		}
		state.notifyMu.Lock()
		state.mu.Lock()
		if state.active && port != nil {
			port.Window(false)
		}
		state.active = false
		state.messages = nil
		state.mu.Unlock()
		state.notifyMu.Unlock()
		cause := runErr
		if ctx.Err() != nil {
			cause = ctx.Err()
		}
		if cause == nil {
			cause = errors.New("codex app-server message delivery ended before acknowledgement")
		}
		for _, item := range pending {
			if outcome, ok := deferredAcks[item.ID]; ok && ctx.Err() == nil {
				ack(item, outcome)
			} else {
				ack(item, driver.MessageAck{Err: cause})
			}
		}
	}()

	input := []UserInput{TextInput(opts.Prompt)}
	var next []driver.MessageItem
	mechanism := "codex.turn_start"
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		turn, err := client.TurnStart(ctx, turnStartParams(opts, threadID, input))
		if err != nil {
			return fmt.Errorf("codex app-server turn/start: %w", err)
		}
		state.setTurn(turn.Turn.ID)
		if err := ctx.Err(); err != nil {
			return err
		}
		for _, item := range next {
			ack(item, driver.MessageAck{Delivered: true, Mechanism: mechanism})
		}
		next = nil
		var restart []driver.MessageItem
		var interruptErr error
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := state.protocolError(); err != nil {
				return err
			}
			select {
			case <-state.done:
				goto completed
			default:
			}
			var signal <-chan struct{}
			state.mu.Lock()
			active := state.active
			state.mu.Unlock()
			if port != nil && active && len(restart) == 0 {
				signal = port.Signal()
			}
			select {
			case <-ctx.Done():
				// The next iteration performs the bounded cancellation interrupt.
			case <-state.done:
				goto completed
			case <-state.wake:
			case <-client.DisconnectNotify():
				select {
				case <-state.done:
					goto completed
				default:
				}
				return errors.New("codex app-server disconnected before turn completion")
			case <-signal:
				for _, item := range claim(driver.BoundaryImmediate) {
					if err := ctx.Err(); err != nil {
						return err
					}
					if item.Timing == driver.TimingInterrupt {
						restart = append(restart, item)
						continue
					}
					state.mu.Lock()
					active := state.active
					state.mu.Unlock()
					if !active {
						deferredIDs = append(deferredIDs, item.ID)
						deferredAcks[item.ID] = driver.MessageAck{Err: errors.New("codex app-server turn is no longer active"), Requeue: true}
						continue
					}
					rpcCtx, cancel := context.WithTimeout(ctx, messageRPCTimeout)
					_, err := client.TurnSteer(rpcCtx, TurnSteerParams{ThreadID: threadID, ExpectedTurnID: turn.Turn.ID, Input: []UserInput{TextInput(item.Text)}})
					cancel()
					if ctx.Err() != nil {
						return ctx.Err()
					}
					if err == nil {
						ack(item, driver.MessageAck{Delivered: true, Mechanism: "codex.turn_steer"})
					} else {
						var providerErr *jsonrpc2.Error
						ambiguous := !errors.As(err, &providerErr)
						// Keep this claim until turn end so a level-triggered port
						// cannot immediately reclaim a failed steer in a busy loop.
						// New messages (including interrupts) remain deliverable.
						deferredIDs = append(deferredIDs, item.ID)
						deferredAcks[item.ID] = driver.MessageAck{Err: err, Requeue: true, MaybeDelivered: ambiguous}
					}
				}
				if len(restart) != 0 {
					state.mu.Lock()
					state.interrupted = true
					state.mu.Unlock()
					rpcCtx, cancel := context.WithTimeout(ctx, messageRPCTimeout)
					interruptErr = client.TurnInterrupt(rpcCtx, TurnInterruptParams{ThreadID: threadID, TurnID: turn.Turn.ID})
					cancel()
					if ctx.Err() != nil {
						return ctx.Err()
					}
					// Even an RPC error can race with turn completion or follow
					// an accepted interrupt. Keep the claims until completion;
					// restart also disables Signal while waiting, avoiding retries
					// or a busy loop on a ready message port.
				}
			}
		}
	completed:
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := state.protocolError(); err != nil {
			return err
		}
		for _, id := range deferredIDs {
			ack(pending[id], deferredAcks[id])
			delete(deferredAcks, id)
		}
		deferredIDs = nil
		state.mu.Lock()
		status := state.terminalStatus
		state.mu.Unlock()
		if status != TurnStatusCompleted && !(status == TurnStatusInterrupted && len(restart) != 0) {
			if len(restart) != 0 {
				cause := interruptErr
				if cause == nil {
					cause = fmt.Errorf("codex app-server turn %q ended with status %q before interrupt restart", turn.Turn.ID, status)
				}
				for _, item := range restart {
					ack(item, driver.MessageAck{Err: cause, Requeue: true})
				}
			}
			return nil
		}
		next = restart
		mechanism = "codex.turn_start"
		if status == TurnStatusInterrupted {
			mechanism = "codex.turn_interrupt"
		}
		if len(next) == 0 {
			next = claim(driver.BoundaryTurnEnd)
			mechanism = "codex.turn_start"
		}
		if len(next) == 0 {
			return nil
		}
		input = make([]UserInput, 0, len(next))
		for _, item := range next {
			input = append(input, TextInput(item.Text))
		}
		state.nextTurn()
	}
}

func (s *runState) nextTurn() {
	s.notifyMu.Lock()
	defer s.notifyMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.turnIndex++
	s.turnID = ""
	s.finalAgentText = ""
	s.usage = nil
	s.turnFailure = nil
	s.terminal = nil
	s.terminalStatus = ""
	s.publicClosed = false
	s.interrupted = false
	s.done = make(chan struct{})
	s.doneOnce = sync.Once{}
}

func addUsage(a, b *driver.Usage) *driver.Usage {
	if a == nil {
		return cloneUsage(b)
	}
	if b == nil {
		return cloneUsage(a)
	}
	return &driver.Usage{InputTokens: a.InputTokens + b.InputTokens, OutputTokens: a.OutputTokens + b.OutputTokens,
		CachedInputTokens: a.CachedInputTokens + b.CachedInputTokens, EstimatedCostMilli: a.EstimatedCostMilli + b.EstimatedCostMilli}
}
