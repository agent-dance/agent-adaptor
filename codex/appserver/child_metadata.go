package appserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/sourcegraph/jsonrpc2"
)

const (
	childMetadataLimit        = 128
	childMetadataTimeout      = time.Second
	childMetadataSettlement   = 2 * time.Second
	childMetadataCancelWindow = 100 * time.Millisecond
)

// This worker only obtains official metadata. It never runs a sink/observer or
// binds a thread. Its owner joins it before freezing this turn or reusing stdio.
type childMetadataReads struct {
	ctx          context.Context
	cancel       context.CancelFunc
	client       *Client
	mu           sync.Mutex
	seen         map[string]bool
	sealed       bool
	queue        chan string
	results      chan childMetadataResult
	done         chan struct{}
	recovery     chan map[string]bool
	recoveryOnce sync.Once
	// Written before done closes, read only after joining. An abandoned call can
	// remain pending in jsonrpc2 after Wait returns, so this resident must stop
	// issuing optional reads even when the parent itself remains healthy.
	abandoned bool
}
type childMetadataResult struct {
	id  string
	raw json.RawMessage
	err error
}

func newChildMetadataReads(ctx context.Context, client *Client) *childMetadataReads {
	ctx, cancel := context.WithCancel(ctx)
	r := &childMetadataReads{ctx: ctx, cancel: cancel, client: client, seen: make(map[string]bool), queue: make(chan string, childMetadataLimit), results: make(chan childMetadataResult, childMetadataLimit), done: make(chan struct{}), recovery: make(chan map[string]bool, 1)}
	go r.run()
	return r
}
func (r *childMetadataReads) admit(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.seen[id] {
		return true
	}
	if r.sealed || len(r.seen) >= childMetadataLimit {
		return false
	}
	r.seen[id] = true
	r.queue <- id
	return true
}
func (r *childMetadataReads) seal() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.sealed {
		r.sealed = true
		close(r.queue)
	}
}
func (r *childMetadataReads) read(id string) (childMetadataResult, bool) {
	ctx, cancel := context.WithTimeout(r.ctx, childMetadataTimeout)
	defer cancel()
	var raw json.RawMessage
	err := r.client.call(ctx, "thread/read", struct {
		ThreadID     string `json:"threadId"`
		IncludeTurns bool   `json:"includeTurns"`
	}{id, false}, &raw)
	// Even cancellation immediately before dispatch conservatively pauses later
	// optional reads: a cancelled waiter cannot prove the wire response arrived.
	return childMetadataResult{id: id, raw: raw, err: err}, err != nil && ctx.Err() != nil
}

// This exact official read-store error describes unavailable SessionMeta, not
// an identity fact or proof of zero physical bytes. Other RPC errors stay final.
func childMetadataUnavailable(err error) bool {
	var rpcErr *jsonrpc2.Error
	return errors.As(err, &rpcErr) && rpcErr.Code == -32603 &&
		strings.HasPrefix(rpcErr.Message, "failed to read thread: thread-store internal error: failed to read session metadata ") &&
		strings.Contains(rpcErr.Message, ": rollout at ") && strings.HasSuffix(rpcErr.Message, " is empty")
}
func (r *childMetadataReads) run() {
	var deferred []childMetadataResult
	defer func() {
		// Exactly one result per admitted ID, including denied/unattempted recovery.
		for _, result := range deferred {
			r.results <- result
		}
		close(r.done)
	}()
firstPass:
	for {
		select {
		case <-r.ctx.Done():
			return
		case id, ok := <-r.queue:
			if !ok {
				break firstPass
			}
			if r.ctx.Err() != nil {
				return
			}
			result, abandoned := r.read(id)
			if !abandoned && childMetadataUnavailable(result.err) {
				deferred = append(deferred, result)
			} else {
				r.results <- result
			}
			if abandoned {
				r.abandoned = true
				return
			}
			if result.err != nil {
				var rpcErr *jsonrpc2.Error
				if !errors.As(result.err, &rpcErr) {
					return
				}
			}
		}
	}
	if len(deferred) == 0 {
		return
	}
	var eligible map[string]bool
	select {
	case eligible = <-r.recovery:
	case <-r.ctx.Done():
		return
	}
	for len(deferred) != 0 {
		if r.ctx.Err() != nil || r.client.closed.Load() {
			return
		}
		select {
		case <-r.client.DisconnectNotify():
			return
		default:
		}
		prior := deferred[0]
		deferred = deferred[1:]
		if !eligible[prior.id] {
			r.results <- prior
			continue
		}
		result, abandoned := r.read(prior.id)
		r.results <- result
		if abandoned {
			r.abandoned = true
			return
		}
		if result.err != nil {
			var rpcErr *jsonrpc2.Error
			if !errors.As(result.err, &rpcErr) {
				return
			}
		}
	}
}

// The owner decides only after a parsed healthy parent terminal. This immutable
// bounded snapshot cannot promote seal(), foreign frames or failed terminals
// into permission to issue another read. Later identity merges remain strict.
func (s *runState) childMetadataRecovery(allowed bool, client *Client, stream *stdioStream) map[string]bool {
	if !allowed || client.closed.Load() {
		return nil
	}
	select {
	case <-stream.ReadDone():
		return nil
	default:
	}
	select {
	case <-client.DisconnectNotify():
		return nil
	default:
	}
	s.notifyMu.Lock()
	defer s.notifyMu.Unlock()
	s.mu.Lock()
	healthy := s.terminal != nil && s.terminalStatus == TurnStatusCompleted && s.turnFailure == nil && s.protocolErr == nil
	s.mu.Unlock()
	if !healthy {
		return nil
	}
	var eligible map[string]bool
	for id := range s.observation.metadataPending {
		child, known := s.observation.children[id]
		if child.rejected || child.conflict || (known && child.role != "") {
			continue
		}
		if eligible == nil {
			eligible = make(map[string]bool)
		}
		eligible[id] = true
	}
	return eligible
}

// settle does not wait indefinitely for synchronous WriteObject. On false the
// process owner MUST close transport before attempting the remaining join.
func (r *childMetadataReads) settle(healthy bool, recovery map[string]bool) bool {
	if r == nil {
		return true
	}
	r.seal()
	if healthy {
		timer := time.NewTimer(childMetadataSettlement - childMetadataCancelWindow)
		r.recoveryOnce.Do(func() { r.recovery <- recovery })
		select {
		case <-r.done:
			timer.Stop()
			r.cancel()
			return true
		case <-r.ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
	r.recoveryOnce.Do(func() { r.recovery <- nil })
	r.cancel()
	timer := time.NewTimer(childMetadataCancelWindow)
	defer timer.Stop()
	select {
	case <-r.done:
		return true
	case <-timer.C:
		return false
	}
}
func (r *childMetadataReads) join(ctx context.Context) error {
	if r == nil {
		return nil
	}
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("codex child metadata worker cleanup: %w", ctx.Err())
	}
}

func (s *runState) applyChildMetadataResults() error {
	if s.metadataReads == nil {
		return nil
	}
	s.notifyMu.Lock()
	defer s.notifyMu.Unlock()
	var fatal error
	for {
		select {
		case result := <-s.metadataReads.results:
			s.observation.metadataPending[result.id] = false
			if result.err != nil {
				var rpcErr *jsonrpc2.Error
				if errors.As(result.err, &rpcErr) || childMetadataContextOnly(result.err) {
					s.observationNotice("capability_unresolved")
					for _, id := range s.observation.collabOrder {
						s.observeCollab(s.observation.collab[id])
					}
				} else {
					fatal = errors.Join(fatal, fmt.Errorf("codex child metadata call: %w", result.err))
				}
				continue
			}
			s.mergeChildMetadata(result.raw, result.id)
		default:
			return fatal
		}
	}
}

// Cancellation only makes metadata optional when every leaf is a context
// sentinel. A joined write/EOF/close error remains evidence of a failed call.
func childMetadataContextOnly(err error) bool {
	switch e := err.(type) {
	case interface{ Unwrap() []error }:
		children := e.Unwrap()
		if len(children) == 0 {
			return false
		}
		for _, child := range children {
			if !childMetadataContextOnly(child) {
				return false
			}
		}
		return true
	case interface{ Unwrap() error }:
		return childMetadataContextOnly(e.Unwrap())
	default:
		return err == context.Canceled || err == context.DeadlineExceeded
	}
}

// Only these official Thread fields can provide the second half of the
// current parent spawn's receiver/role proof. This is not recursive JSON search.
type childMetadata struct {
	Thread struct {
		ID        string `json:"id"`
		AgentRole string `json:"agentRole"`
		Source    struct {
			SubAgent struct {
				Spawn struct {
					Parent string `json:"parent_thread_id"`
					Role   string `json:"agent_role"`
				} `json:"thread_spawn"`
			} `json:"subAgent"`
		} `json:"source"`
	} `json:"thread"`
}

func (s *runState) freezeNotifications() {
	s.notifyMu.Lock()
	s.notificationsFrozen = true
	s.notifyMu.Unlock()
}
