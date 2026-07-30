package tool

import (
	"errors"
	"strings"
)

const (
	maximumRejectionCodeLength    = 64
	maximumRejectionMessageLength = 4096
)

var (
	// ErrInvalidDefinition identifies a tool declaration that cannot be
	// validated for installation on an Agent.
	ErrInvalidDefinition = errors.New("agentadaptor: invalid tool definition")

	// ErrInvalidInput identifies arguments that do not satisfy a tool's input
	// schema or cannot be decoded into its Go input type.
	ErrInvalidInput = errors.New("agentadaptor: invalid tool input")

	// ErrInvalidOutput identifies a handler result that does not satisfy its
	// declared output schema or cannot be encoded as JSON.
	ErrInvalidOutput = errors.New("agentadaptor: invalid tool output")
)

// rejectionError is intentionally private: applications only need the
// provider-neutral Reject constructor. The exported ToolRejection method is a
// narrow capability used by the internal runtime without adding a public
// transport error type to the tool vocabulary.
type rejectionError struct {
	code    string
	message string
}

// Error implements error.
func (e *rejectionError) Error() string {
	if e == nil {
		return "tool request rejected"
	}
	if e.code == "" {
		return e.message
	}
	if e.message == "" {
		return e.code
	}
	return e.code + ": " + e.message
}

// ToolRejection exposes the already-normalized safe fields to the internal
// delivery runtime. It is not a transport-specific contract.
func (e *rejectionError) ToolRejection() (code, message string) {
	if e == nil {
		return "rejected", "tool request rejected"
	}
	return e.code, e.message
}

// Reject returns a typed, model-visible tool failure. Code should be a stable
// machine-readable identifier; message should explain how the caller can
// correct the request. Both values are trimmed; empty, malformed, or excessive
// values are replaced with safe defaults. Rejections are never interpreted as
// transport or infrastructure errors.
func Reject(code, message string) error {
	code = strings.TrimSpace(code)
	if code == "" || len(code) > maximumRejectionCodeLength || !validName.MatchString(code) {
		code = "rejected"
	}
	message = strings.TrimSpace(message)
	if message == "" || len(message) > maximumRejectionMessageLength {
		message = "tool request rejected"
	}
	return &rejectionError{
		code:    code,
		message: message,
	}
}
