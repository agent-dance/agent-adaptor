// Package capability defines safe, normalized observations of actual capability
// invocations. An absent observation does not prove that no invocation occurred.
package capability

import "time"

// Kind is the closed resource class of an observed invocation.
type Kind string

const (
	Skill    Kind = "skill"
	MCP      Kind = "mcp"
	Subagent Kind = "subagent"
)

// Phase is an invocation lifecycle state. Started is followed by one terminal.
type Phase string

const (
	Started     Phase = "started"
	Completed   Phase = "completed"
	Failed      Phase = "failed"
	Cancelled   Phase = "cancelled"
	Interrupted Phase = "interrupted"
)

// Evidence describes what was actually observed, rather than inferred usage.
type Evidence string

const (
	ProviderProtocol    Evidence = "provider_protocol"
	NativeInputAccepted Evidence = "native_input_accepted"
	HostLifecycle       Evidence = "host_lifecycle"
	Relayed             Evidence = "relayed"
)

// Source identifies the immediate producer of the normalized fact.
type Source string

const (
	Provider Source = "provider"
	Host     Source = "host"
	Relay    Source = "relay"
)

// ErrorCode is a closed, safe category. Empty means no classified error.
type ErrorCode string

const (
	ToolFailed       ErrorCode = "tool_failed"
	RunCancelled     ErrorCode = "run_cancelled"
	RunInterrupted   ErrorCode = "run_interrupted"
	ProtocolError    ErrorCode = "protocol_error"
	DelegationFailed ErrorCode = "delegation_failed"
)

// Ref identifies a resource from the actual resolved catalog. Key is opaque;
// Operation is activate for Skill, spawn for Subagent, or a formal MCP operation.
type Ref struct {
	Kind      Kind
	Key       string
	Operation string
}

// Invocation carries only the safe observation vocabulary. It contains no
// arguments, output, credentials, URLs or free-form error. Identity is scoped by
// run and ScopeID; parent references use ParentScopeID and ParentToolCallID.
// OccurredAt is nonzero UTC. Duration=nil means unobserved, unlike observed zero.
// Started has neither Duration nor ErrorCode; Completed has no ErrorCode.
// Duration must be nonnegative and is never inferred from wall-clock subtraction.
// Valid source/evidence pairs are Provider with ProviderProtocol or
// NativeInputAccepted, Host with HostLifecycle, and Relay with Relayed.
// Key uses 1..512 UTF-8 bytes, Operation 1..256, and required InvocationID
// 1..2048. Optional scope/parent IDs use at most 2048; controls are invalid.
// Unknown, failed or interrupted completion must not be reported as Completed.
type Invocation struct {
	InvocationID     string
	Ref              Ref
	Phase            Phase
	Evidence         Evidence
	Source           Source
	ScopeID          string
	ParentScopeID    string
	ParentToolCallID string
	OccurredAt       time.Time
	Duration         *time.Duration
	ErrorCode        ErrorCode
}
