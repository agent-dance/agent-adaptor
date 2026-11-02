package driver

import "time"

// Boundary identifies the point in a provider turn at which a Driver hands a
// queued message to the model.
//
// Core owns the message queue, ordering, idempotency, cancellation and
// accounting. A Driver only claims messages at the boundaries it declared in
// MessageCapability.Boundaries and reports each claim's outcome through
// MessagePort.Ack.
type Boundary string

const (
	// BoundaryImmediate means the provider accepted the message now and will
	// show it to the model at its next model request (for example Codex
	// app-server turn/steer or an ACP interjection extension).
	BoundaryImmediate Boundary = "immediate"
	// BoundaryTool means the message was attached around a tool call (for
	// example a PreToolUse/PostToolUse hook returning additional context).
	BoundaryTool Boundary = "tool"
	// BoundaryStop means the agent was about to finish its turn and the
	// message made the same agent loop continue (for example a Claude Stop
	// hook returning decision "block").
	BoundaryStop Boundary = "stop"
	// BoundaryTurnEnd means the message started a continuation turn after a
	// provider turn completed, inside the same execution.
	BoundaryTurnEnd Boundary = "turn_end"
)

// Timing is the delivery timing the host requested for a message.
type Timing uint8

const (
	// TimingSoon delivers at the earliest declared boundary. When no
	// mid-turn boundary is available the message is delivered as a
	// continuation turn after the current provider turn completes.
	TimingSoon Timing = iota
	// TimingInterrupt aborts the current provider turn and restarts it with
	// the message inside the same execution. Core admits it only when
	// MessageCapability.Interrupt is true.
	TimingInterrupt
	// TimingAfterTurn delivers only after the current provider turn
	// completes, as a continuation turn inside the same execution.
	TimingAfterTurn
)

// ContinueMode declares who runs continuation turns of one execution.
type ContinueMode uint8

const (
	// ContinueResume means Driver.Run returns after one provider turn. When
	// messages remain pending, core invokes Driver.Run again inside the same
	// execution with a SessionContext built from the checkpoint the previous
	// turn produced. This is the default and requires session resume support.
	ContinueResume ContinueMode = iota
	// ContinueInRun means the Driver keeps the provider turn loop alive inside
	// one Driver.Run call: at the end of every provider turn it calls
	// MessagePort.Claim(BoundaryTurnEnd) and, when items are returned, starts
	// the next provider turn in the same process. It returns only when the
	// claim comes back empty.
	ContinueInRun
)

// MessageUnit is the unit in which MessageCapability.MaxUnits is measured.
type MessageUnit string

const (
	// MessageUnitBytes measures UTF-8 bytes.
	MessageUnitBytes MessageUnit = "bytes"
	// MessageUnitUTF16 measures UTF-16 code units (JavaScript string length).
	MessageUnitUTF16 MessageUnit = "utf16"
)

// MessageCapability is the static declaration of a Driver's in-run message
// delivery support. Inspect().Environment may trim it for the local CLI
// version. The zero value means: no mid-turn boundary, no interrupt, and
// continuation through ContinueResume when the Driver supports session resume.
type MessageCapability struct {
	// Boundaries lists the mid-turn boundaries at which the Driver claims
	// messages. BoundaryTurnEnd is implied for every Driver that can continue
	// (resume support or ContinueInRun) and need not be listed.
	Boundaries []Boundary
	// Interrupt reports whether the Driver can abort the current provider
	// turn and restart it with a message inside the same execution.
	Interrupt bool
	// Continue selects who runs continuation turns.
	Continue ContinueMode
	// MaxUnits is the largest framed message the provider accepts at a
	// mid-turn boundary. Zero means no known limit. Core rejects larger
	// messages at admission instead of letting the provider drop them.
	MaxUnits int
	// Unit is the unit of MaxUnits. Empty means MessageUnitBytes.
	Unit MessageUnit
	// ConfirmedDelivery reports that a delivered Ack is backed by a provider
	// echo (for example Cursor hookAdditionalContexts) rather than by a
	// successful write.
	ConfirmedDelivery bool
	// PersistsDelivered reports that delivered message text remains in the
	// provider's conversation history after a later resume.
	PersistsDelivered bool
	// MinVersion is the oldest provider CLI version for which this
	// capability holds. Empty means no version gate.
	MinVersion string
}

// MessageItem is one queued message handed to a Driver at a boundary. Text is
// already framed by core (author, authority and "do not acknowledge
// separately" guidance) and must be delivered verbatim.
type MessageItem struct {
	// ID is the core-assigned, execution-unique message identifier.
	ID string
	// Text is the framed text to deliver verbatim.
	Text string
	// Timing is the requested delivery timing.
	Timing Timing
	// MaybeDuplicate reports that an earlier delivery attempt of this item
	// had an ambiguous outcome; Text already carries a note asking the model
	// to ignore it when it was already received.
	MaybeDuplicate bool
}

// MessageAck reports the outcome of one claimed MessageItem.
type MessageAck struct {
	// Delivered reports that the provider accepted the message at the
	// claiming boundary.
	Delivered bool
	// Mechanism names the concrete provider mechanism, for example
	// "codex.turn_steer", "claude.hook.post_tool_use" or
	// "cursor.hook.post_tool_use". It is reported on MessageUpdate.
	Mechanism string
	// Err describes a delivery failure. It is required when Delivered is
	// false and ignored otherwise.
	Err error
	// Requeue asks core to keep an undelivered item pending so it is
	// delivered at a later boundary or as a continuation turn, instead of
	// failing it. Drivers set it when the failure was specific to this
	// boundary (RPC timeout, turn ended concurrently, missing echo).
	Requeue bool
	// MaybeDelivered marks a requeued item whose first attempt may have
	// reached the model (for example an RPC timeout). Core then marks the
	// retry as a possible duplicate.
	MaybeDelivered bool
}

// MessagePort is the run-scoped interface through which a Driver receives
// queued messages. Core supplies it on Request.Messages when message delivery
// is enabled for the execution; it is nil otherwise. All methods are safe for
// concurrent use and must not be retained after Driver.Run returns.
type MessagePort interface {
	// Window reports provider-confirmed changes of the delivery window: open
	// once the provider has an active turn that can accept a message at a
	// declared boundary, closed when that is no longer true.
	Window(open bool)
	// Signal returns a channel that becomes ready when new items are queued.
	// Push-style Drivers (BoundaryImmediate, Interrupt) watch it.
	Signal() <-chan struct{}
	// Claim atomically takes every item deliverable at boundary b, in
	// admission order. TimingSoon items are claimable at any boundary;
	// TimingAfterTurn items only when the turn ends (BoundaryTurnEnd, or a
	// BoundaryStop that closes the turn); TimingInterrupt items only at
	// BoundaryImmediate. A Stop claim, and an immediate claim containing an
	// Interrupt item, reserves the successor turn: when the execution has no
	// turn left the claim returns nothing and the input stays pending. Every
	// claimed item MUST later be passed to Ack exactly once.
	Claim(b Boundary) []MessageItem
	// Ack reports the outcome of one claimed item.
	Ack(id string, ack MessageAck)
}

// TurnCompleted is the payload of a RunEventTurnCompleted event. A Driver
// emits one at the end of every provider turn, including the last one, so
// core can publish per-turn results. Index starts at zero for the first turn
// of a Driver.Run call.
type TurnCompleted struct {
	// Index is the zero-based turn index within this Driver.Run call.
	Index int
	// Text is the final assistant text of the turn.
	Text string
	// Usage is the token accounting of the turn, when reported.
	Usage *Usage
	// Interrupted reports that the turn ended because of a TimingInterrupt
	// message rather than by natural completion.
	Interrupted bool
	// CompletedAt is when the provider reported completion.
	CompletedAt time.Time
}

// MessageCapabilityResolver is implemented by Drivers whose message support
// depends on the resolved invocation, for example on the selected provider
// transport. Core calls it once per invocation after resolution and uses the
// result for admission checks (interrupt support, size limit) instead of the
// static Descriptor.Messages. The result must be a subset of
// Descriptor.Messages.
type MessageCapabilityResolver interface {
	ResolveMessageCapability(req Request) MessageCapability
}
