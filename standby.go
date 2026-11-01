package adaptor

import "context"

// Standby is a Thread kept on standby: it keeps accepting messages until the
// context passed to Thread.Standby ends. A message sent while the Thread is
// processing joins that processing; a message sent while it is idle starts a
// new execution. Standby has no overall Result: every turn's result arrives
// as a TurnEnd event, and every execution still ends with its own Done event.
type Standby interface {
	// Send admits one message. The context bounds admission only; executions
	// started by the Standby run under the Standby's own context. Options that
	// configure an execution (for example WithModel) cannot join a running
	// execution; such a message starts a new execution after the current one.
	Send(ctx context.Context, text string, opts ...CallOption) error
	// Events returns one channel carrying every execution's events in order.
	// It closes after the Standby's context ends and the final events have
	// been delivered.
	Events() <-chan Event
	// Cancel aborts the processing in progress, if any, and fails every
	// message not yet delivered with MessageCancelled. The Standby stays
	// alive and keeps accepting messages.
	Cancel()
}

// ThreadRunner is a conversation identified by a host key that can run in
// task mode (Send returns one execution) or on standby. *Thread implements it,
// and composite runners such as teams provide their own implementations.
type ThreadRunner interface {
	Runner
	// Key returns the host's conversation key.
	Key() string
	// Standby keeps the conversation accepting messages until ctx ends. Within
	// one process a second call for the same conversation returns the live
	// Standby.
	Standby(ctx context.Context) (Standby, error)
	// Checkpoint returns the conversation's current resume checkpoint.
	Checkpoint(ctx context.Context) (*Checkpoint, error)
}
