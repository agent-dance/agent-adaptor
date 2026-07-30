// Package engine owns only the internal thread-coordination sentinels.
// Domain errors remain owned by their public leaf packages so errors.Is and
// errors.As identities do not depend on this internal package.
package engine

import (
	"errors"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/mcp"
	"github.com/agent-dance/agent-adaptor/skill"
)

var ErrInvalidDriverConfig = driver.ErrInvalidDriverConfig

// --- Session -------------------------------------------------------------

var (
	// ErrResumeRejected is the base sentinel; ResumeRejectedError below
	// carries the structured Reason / Cause.
	ErrResumeRejected = errors.New("agentadaptor: driver rejected session resume")

	// ErrSessionBusy is the base sentinel; SessionBusyError below carries
	// the structured Target.
	ErrSessionBusy = errors.New("agentadaptor: session busy")

	// ErrSessionCheckpointMissing is returned when the SessionStore
	// reports the resolved record but the driver state on it is empty.
	ErrSessionCheckpointMissing = errors.New("agentadaptor: session checkpoint missing")

	// ErrSessionIncompatible is the base sentinel; SessionIncompatibleError
	// below carries the structured Reason / fingerprints.
	ErrSessionIncompatible = errors.New("agentadaptor: session incompatible")

	// ErrSessionLeaseLost is the base sentinel; SessionLeaseLostError below
	// carries the structured Target.
	ErrSessionLeaseLost = errors.New("agentadaptor: session lease lost")

	// ErrSessionNotFound is returned when SessionStore.Resolve cannot
	// locate the requested SessionID / (Namespace,Key).
	ErrSessionNotFound = errors.New("agentadaptor: session not found")

	// ErrSessionStoreRequired is returned when a session-aware Run
	// (continue_only / fork / ...) is invoked on an SDK that has no
	// SessionStore configured.
	ErrSessionStoreRequired = errors.New("agentadaptor: session store required")

	// ErrThreadAlreadyExists is returned by structured fork coordination when
	// the requested child key already has an active record. The parent and
	// existing target remain unchanged.
	ErrThreadAlreadyExists = errors.New("agentadaptor: fork target thread already exists")

	// ErrInvalidSessionRequest is returned before store access when selectors
	// are incomplete, contradictory, or invalid for the requested mode.
	ErrInvalidSessionRequest = errors.New("agentadaptor: invalid session request")
)

// SessionBusyError is wrapped by ErrSessionBusy when the busy target
// (a sessionID, a namespace+key tuple, etc.) is known.
type SessionBusyError struct {
	Target string
}

// Error reports the busy session target when present.
func (e *SessionBusyError) Error() string {
	if e == nil || e.Target == "" {
		return ErrSessionBusy.Error()
	}
	return ErrSessionBusy.Error() + ": " + e.Target
}

// Unwrap returns ErrSessionBusy so errors.Is(err, ErrSessionBusy) holds.
func (e *SessionBusyError) Unwrap() error { return ErrSessionBusy }

// SessionLeaseLostError is wrapped by ErrSessionLeaseLost when the lease
// target is known.
type SessionLeaseLostError struct {
	Target string
}

// Error reports the lost lease target when present.
func (e *SessionLeaseLostError) Error() string {
	if e == nil || e.Target == "" {
		return ErrSessionLeaseLost.Error()
	}
	return ErrSessionLeaseLost.Error() + ": " + e.Target
}

// Unwrap returns ErrSessionLeaseLost so errors.Is(err, ErrSessionLeaseLost)
// holds.
func (e *SessionLeaseLostError) Unwrap() error { return ErrSessionLeaseLost }

// SessionIncompatibleError carries the fingerprint diff that triggered
// the rejection. Unwrap returns ErrSessionIncompatible.
type SessionIncompatibleError struct {
	Reason              string
	ExpectedFingerprint string
	ActualFingerprint   string
}

// Error reports the incompatibility reason when present.
func (e *SessionIncompatibleError) Error() string {
	if e == nil || e.Reason == "" {
		return ErrSessionIncompatible.Error()
	}
	return ErrSessionIncompatible.Error() + ": " + e.Reason
}

// Unwrap returns ErrSessionIncompatible.
func (e *SessionIncompatibleError) Unwrap() error { return ErrSessionIncompatible }

// ResumeRejectedError carries the adapter-supplied Reason and the
// underlying Cause when the driver rejects a resume. Unwrap joins
// ErrResumeRejected with Cause.
type ResumeRejectedError struct {
	Reason string
	Cause  error
}

// Error reports the resume rejection reason when present.
func (e *ResumeRejectedError) Error() string {
	if e == nil || e.Reason == "" {
		return ErrResumeRejected.Error()
	}
	return ErrResumeRejected.Error() + ": " + e.Reason
}

// Unwrap joins ErrResumeRejected with the adapter-supplied Cause when set.
func (e *ResumeRejectedError) Unwrap() error {
	if e == nil || e.Cause == nil {
		return ErrResumeRejected
	}
	return errors.Join(ErrResumeRejected, e.Cause)
}

// --- MCP -----------------------------------------------------------------

var (
	// ErrInvalidMCPConfig is returned when WithMCP is given a malformed
	// MCPConfig.
	ErrInvalidMCPConfig = mcp.ErrInvalidConfig

	// ErrMCPUnsupported is returned when an MCP-bound Run targets an
	// adapter that does not declare MCP support in its descriptor.
	ErrMCPUnsupported = mcp.ErrUnsupported

	// ErrMCPTransportUnsupported is returned when the adapter declares
	// MCP support but not for the transport the host requested.
	ErrMCPTransportUnsupported = mcp.ErrTransportUnsupported
)

// --- Structured output --------------------------------------------------

var (
	// ErrStructuredOutputUnsupported is returned before Driver launch when
	// structured output cannot be honored by the bound Driver or selected
	// provider transport.
	ErrStructuredOutputUnsupported = driver.ErrStructuredOutputUnsupported

	// ErrInvalidOutputSchema is returned before Driver launch when the host
	// supplies malformed JSON, an unsupported output format, or a JSON
	// Schema document that cannot be compiled for local validation.
	ErrInvalidOutputSchema = driver.ErrInvalidOutputSchema
)

type (
	// StructuredOutputUnsupportedError is owned by package driver.
	StructuredOutputUnsupportedError = driver.StructuredOutputUnsupportedError
	// InvalidOutputSchemaError is owned by package driver.
	InvalidOutputSchemaError = driver.InvalidOutputSchemaError
)

// --- Skill --------------------------------------------------------------

var (
	// ErrSkillKeyConflict is the base sentinel; SkillKeyConflictError in
	// skill_types.go carries the conflicting sources slice.
	ErrSkillKeyConflict = skill.ErrSkillKeyConflict

	// ErrSkillMaterializationFailed is returned when a selected skill was
	// resolved but could not be materialized into a local SKILL.md directory.
	// SkillMaterializationError carries the skill key and underlying cause.
	ErrSkillMaterializationFailed = skill.ErrSkillMaterializationFailed

	// (v0.4 ErrSkillsNotEnumerable removed in v0.5 PR4: non-enumerable
	// providers now simply do not implement SkillCatalog. Hosts that
	// previously matched on this sentinel should switch to checking
	// SkillSnapshot.Mode == SkillSyncUnsupported, or rely on
	// ErrSkillNotFound for unresolved bare key references.)

	// ErrSkillSourceMissing is returned when a Skill value is registered
	// or selected without a non-nil Source.
	ErrSkillSourceMissing = skill.ErrSkillSourceMissing

	// ErrSkillKeyMissing is returned when a Skill is constructed with an
	// empty Key.
	ErrSkillKeyMissing = skill.ErrSkillKeyMissing

	// ErrSkillNotFound is returned during Run-time selection when a bare
	// SkillKey reference cannot be resolved against the configured
	// provider, default skills, or per-call skills.
	ErrSkillNotFound = skill.ErrSkillNotFound
)
