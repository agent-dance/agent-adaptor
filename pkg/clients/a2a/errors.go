package a2a

import (
	"errors"
	"fmt"
)

var (
	ErrInvalidAgentCard = errors.New("a2a client: invalid agent card")
	ErrProtocol         = errors.New("a2a client: protocol error")
	ErrUnauthorized     = errors.New("a2a client: unauthorized")
	ErrNotFound         = errors.New("a2a client: task not found")
	ErrUnsupported      = errors.New("a2a client: unsupported operation")
)

type ProtocolError struct {
	Op     string
	Reason string
	Cause  error
	Raw    map[string]any
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

type StreamRecoveryError struct {
	TaskID string
	Cause  error
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
