package a2a

import (
	"errors"
	"fmt"
)

var (
	// ErrInvalidAgentCard identifies invalid or incomplete discovery documents.
	ErrInvalidAgentCard = errors.New("a2a client: invalid agent card")
	// ErrProtocol identifies malformed or unexpected A2A protocol data.
	ErrProtocol = errors.New("a2a client: protocol error")
	// ErrUnauthorized identifies authentication or authorization failures.
	ErrUnauthorized = errors.New("a2a client: unauthorized")
	// ErrNotFound identifies a remote task that does not exist.
	ErrNotFound = errors.New("a2a client: task not found")
	// ErrUnsupported identifies an operation the remote endpoint cannot perform.
	ErrUnsupported = errors.New("a2a client: unsupported operation")
	// ErrUntrustedOrigin identifies an endpoint that is not allowed to receive credentials.
	ErrUntrustedOrigin = errors.New("a2a client: untrusted origin")
)

// ProtocolError records the A2A operation, normalized reason, causal error,
// and any structured protocol details observed for a failed request.
type ProtocolError struct {
	// Op is the protocol operation that failed.
	Op string
	// Reason is the remote or validation error description.
	Reason string
	// Cause supports errors.Is and errors.As classification.
	Cause error
	// Raw preserves structured error details when the transport provides them.
	Raw map[string]any
}

func (e *ProtocolError) Error() string {
	if e == nil {
		return ""
	}
	if e.Reason != "" && e.Op != "" {
		return fmt.Sprintf("%s: %s", e.Op, e.Reason)
	}
	if e.Reason != "" {
		return e.Reason
	}
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return ErrProtocol.Error()
}

func (e *ProtocolError) Unwrap() error {
	if e == nil {
		return nil
	}
	if e.Cause != nil {
		return e.Cause
	}
	return ErrProtocol
}

// StreamRecoveryError reports a disconnected stream whose terminal task state
// could not be recovered.
type StreamRecoveryError struct {
	// TaskID is the last task identifier observed before disconnection.
	TaskID string
	// Cause is the transport or protocol error that ended the stream.
	Cause error
}

func (e *StreamRecoveryError) Error() string {
	if e == nil {
		return ""
	}
	if e.TaskID != "" {
		return fmt.Sprintf("a2a stream disconnected before terminal state for task %s: %v", e.TaskID, e.Cause)
	}
	return fmt.Sprintf("a2a stream disconnected before terminal state: %v", e.Cause)
}

func (e *StreamRecoveryError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}
