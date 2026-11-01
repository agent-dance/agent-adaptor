package agui

import (
	"context"
	"errors"
	"sync"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"github.com/agent-dance/agent-adaptor/v2/bridges/internal/bridgekey"
)

// ThreadKey maps a frontend thread ID to the same collision-free SDK identity
// used by SessionKey and UserTurnEvents.
func ThreadKey(threadID string) string { return bridgekey.Encode("agui", threadID) }

// RunRegistry routes inputs to one active Stream per Thread. Its zero value
// is ready for use and all methods are safe for concurrent callers. The host
// owns the sole Events consumer and must Forget after draining an execution.
// Hosts needing conversations that outlive individual runs can instead keep
// a Thread.Standby handle and route requests through Standby.Send.
type RunRegistry struct {
	mu     sync.Mutex
	active map[string]adaptor.Stream
}

// Active returns the active Stream for a public Thread key, or nil. Looking
// up a Stream does not subscribe to or consume its Events.
func (r *RunRegistry) Active(threadKey string) adaptor.Stream {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.active[threadKey]
}

// Send starts Thread.Send when idle and routes concurrent user text through
// the active Stream.Send. It returns that Stream and whether input supplemented
// an existing execution. Only the creator consumes Events and Result; the
// original event consumer receives message.update delivery outcomes.
//
// Options configure new runs only. Supplements preserve the latest user's ID
// and human author, and use Soon. The sender ctx is passed unchanged to
// Stream.Send: cancellation can withdraw unclaimed input even after Send
// returns nil. Claimed input is settled by the Driver. Hosts choosing a
// different HTTP lifetime must provide that context explicitly. A finishing
// run's ErrRunFinished is returned to the host
// with the original message; this method never silently starts another run.
func (r *RunRegistry) Send(ctx context.Context, thread adaptor.ThreadRunner, input *RunAgentInput, opts ...adaptor.CallOption) (adaptor.Stream, bool, error) {
	if thread == nil || input == nil || input.LastUserText() == "" {
		return nil, false, errors.New("agui: thread and nonempty user text are required")
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	attrs := []adaptor.MessageOption{adaptor.MessageID(input.LastUserMessageID()), adaptor.From(adaptor.Author{Kind: adaptor.AuthorHuman})}
	r.mu.Lock()
	stream := r.active[thread.Key()]
	if stream == nil {
		calls := append([]adaptor.CallOption(nil), opts...)
		for _, attr := range attrs {
			calls = append(calls, attr)
		}
		stream = thread.Send(ctx, input.LastUserText(), calls...)
		if r.active == nil {
			r.active = make(map[string]adaptor.Stream)
		}
		r.active[thread.Key()] = stream
		r.mu.Unlock()
		return stream, false, nil
	}
	r.mu.Unlock()
	return stream, true, stream.Send(ctx, input.LastUserText(), attrs...)
}

// SendActive adds user text only to an existing execution. If no Stream is
// registered for threadKey, it returns a MessageError wrapping ErrRunFinished;
// it never starts a run. Its sender lifetime and message attributes are the
// same as Send's supplement path.
func (r *RunRegistry) SendActive(ctx context.Context, threadKey string, input *RunAgentInput) (adaptor.Stream, error) {
	if input == nil || input.LastUserText() == "" {
		return nil, errors.New("agui: nonempty user text is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	stream := r.Active(threadKey)
	if stream == nil {
		return nil, &adaptor.MessageError{
			ID: input.LastUserMessageID(), Text: input.LastUserText(),
			Reason: adaptor.MessageRunFailed, Err: adaptor.ErrRunFinished,
		}
	}
	attrs := []adaptor.MessageOption{adaptor.MessageID(input.LastUserMessageID()), adaptor.From(adaptor.Author{Kind: adaptor.AuthorHuman})}
	return stream, stream.Send(ctx, input.LastUserText(), attrs...)
}

// Forget removes a completed execution only if it is still the registered
// Stream. Call it after Events closes and Result returns, including failures.
func (r *RunRegistry) Forget(threadKey string, stream adaptor.Stream) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if current := r.active[threadKey]; current != nil && stream != nil && current.RunID() == stream.RunID() {
		delete(r.active, threadKey)
	}
}
