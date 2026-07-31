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

// rejectionError is intentionally private: only Reject can mint a rejection
// trusted for model-visible delivery.
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

// AsRejection reports whether err, or an error it wraps, was created by
// [Reject]. Only the package-private rejection type is recognized; an ordinary
// application error cannot opt into model-visible delivery by implementing a
// public method with the same shape.
func AsRejection(err error) (code, message string, ok bool) {
	// Walk only standard unwrap contracts. Using errors.As here would let an
	// unrelated error's custom As method manufacture the private target and
	// cross the safe-delivery boundary.
	pending := []error{err}
	for inspected := 0; len(pending) > 0 && inspected < 100; inspected++ {
		current := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if current == nil {
			continue
		}
		if rejection, trusted := current.(*rejectionError); trusted && rejection != nil {
			return rejection.code, rejection.message, true
		}
		switch wrapped := current.(type) {
		case interface{ Unwrap() []error }:
			pending = append(pending, wrapped.Unwrap()...)
		case interface{ Unwrap() error }:
			pending = append(pending, wrapped.Unwrap())
		}
	}
	return "", "", false
}
