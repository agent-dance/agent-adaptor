package appserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	ctx     context.Context
	cancel  context.CancelFunc
	client  *Client
	mu      sync.Mutex
	seen    map[string]bool
	sealed  bool
	queue   chan string
	results chan childMetadataResult
	done    chan struct{}
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
	r := &childMetadataReads{ctx: ctx, cancel: cancel, client: client, seen: make(map[string]bool), queue: make(chan string, childMetadataLimit), results: make(chan childMetadataResult, childMetadataLimit), done: make(chan struct{})}
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
func (r *childMetadataReads) run() {
	defer close(r.done)
	for {
		select {
		case <-r.ctx.Done():
			return
		case id, ok := <-r.queue:
			if !ok || r.ctx.Err() != nil {
				return
			}
			ctx, cancel := context.WithTimeout(r.ctx, childMetadataTimeout)
			var raw json.RawMessage
			err := r.client.call(ctx, "thread/read", struct {
				ThreadID     string `json:"threadId"`
				IncludeTurns bool   `json:"includeTurns"`
			}{id, false}, &raw)
			// Conservative even if cancellation won just before dispatch: never build
			// up unanswered dependency pending entries across otherwise healthy turns.
			abandoned := err != nil && ctx.Err() != nil
			cancel()
			r.results <- childMetadataResult{id: id, raw: raw, err: err}
			if abandoned {
				r.abandoned = true
				return
			}
			if err != nil {
				var rpcErr *jsonrpc2.Error
				if !errors.As(err, &rpcErr) {
					return
				}
			}
		}
	}
}

// settle does not wait indefinitely for synchronous WriteObject. On false the
// process owner MUST close transport before attempting the remaining join.
func (r *childMetadataReads) settle(healthy bool) bool {
	if r == nil {
		return true
	}
	r.seal()
	if healthy {
		timer := time.NewTimer(childMetadataSettlement - childMetadataCancelWindow)
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

func (s *runState) freezeChildMetadata() {
	s.notifyMu.Lock()
	s.metadataFrozen = true
	s.notifyMu.Unlock()
}
