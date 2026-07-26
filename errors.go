// Package agentadaptor's public error catalogue (re-exported).
//
// The catalogue's source of truth moved to internal/engine with the
// execution pipeline (P0.2); every sentinel below re-exports the identical
// error value and every typed error is a type alias, so errors.Is /
// errors.As behavior is byte-for-byte identical for hosts. The godoc
// contract — including the HTTP / log-level matrix in
// docs/public-errors.md — is unchanged.
package agentadaptor

import (
	"errors"

	"github.com/agent-dance/agent-adaptor/internal/engine"
)

// --- Agent / binding -------------------------------------------------------

var (
	// ErrAgentBindingRequired is returned by WithAgent / WithDefaultAgent
	// when the binding argument is nil.
	ErrAgentBindingRequired = engine.ErrAgentBindingRequired

	// ErrAgentNameRequired is returned by WithAgent when the name argument
	// is empty. Use WithDefaultAgent for the unnamed/default binding.
	ErrAgentNameRequired = engine.ErrAgentNameRequired

	// ErrAgentNotFound is returned by SDK.Agent / Admin().Agent when the
	// requested name was never registered.
	ErrAgentNotFound = engine.ErrAgentNotFound

	// ErrDefaultAgentAlreadyConfigured surfaces from agentadaptor.New when
	// WithDefaultAgent is supplied more than once.
	ErrDefaultAgentAlreadyConfigured = engine.ErrDefaultAgentAlreadyConfigured

	// ErrDefaultAgentMissing surfaces from agentadaptor.New when no
	// WithDefaultAgent option is supplied.
	ErrDefaultAgentMissing = engine.ErrDefaultAgentMissing

	// ErrInvalidDriverConfig wraps the adapter-side ValidateConfig error
	// with the SDK-stable sentinel so hosts can pattern-match without
	// importing the adapter package.
	ErrInvalidDriverConfig = engine.ErrInvalidDriverConfig

	// ErrReservedAgentName is returned when a host attempts to register
	// the literal name "default" via WithAgent.
	ErrReservedAgentName = engine.ErrReservedAgentName
)

// DuplicateAgentError is returned (wrapping nothing — match by *type with
// errors.As) when WithAgent registers a name twice.
type DuplicateAgentError = engine.DuplicateAgentError

// --- Session ---------------------------------------------------------------

var (
	// ErrResumeRejected is the base sentinel; ResumeRejectedError carries
	// the structured Reason / Cause.
	ErrResumeRejected = engine.ErrResumeRejected

	// ErrSessionBusy is the base sentinel; SessionBusyError carries the
	// structured Target.
	ErrSessionBusy = engine.ErrSessionBusy

	// ErrSessionCheckpointMissing is returned when the SessionStore
	// reports the resolved record but the driver state on it is empty.
	ErrSessionCheckpointMissing = engine.ErrSessionCheckpointMissing

	// ErrSessionIncompatible is the base sentinel; SessionIncompatibleError
	// carries the structured Reason / fingerprints.
	ErrSessionIncompatible = engine.ErrSessionIncompatible

	// ErrSessionLeaseLost is the base sentinel; SessionLeaseLostError
	// carries the structured Target.
	ErrSessionLeaseLost = engine.ErrSessionLeaseLost

	// ErrSessionNotFound is returned when SessionStore.Resolve cannot
	// locate the requested SessionID / (Namespace,Key).
	ErrSessionNotFound = engine.ErrSessionNotFound

	// ErrSessionStoreRequired is returned when a session-aware Run
	// (continue_only / fork / ...) is invoked on an SDK that has no
	// SessionStore configured.
	ErrSessionStoreRequired = engine.ErrSessionStoreRequired
)

type (
	// SessionBusyError is wrapped by ErrSessionBusy when the busy target
	// (a sessionID, a namespace+key tuple, etc.) is known.
	SessionBusyError = engine.SessionBusyError

	// SessionLeaseLostError is wrapped by ErrSessionLeaseLost when the
	// lease target is known.
	SessionLeaseLostError = engine.SessionLeaseLostError

	// SessionIncompatibleError carries the fingerprint diff that triggered
	// the rejection. Unwrap returns ErrSessionIncompatible.
	SessionIncompatibleError = engine.SessionIncompatibleError

	// ResumeRejectedError carries the adapter-supplied Reason and the
	// underlying Cause when the driver rejects a resume.
	ResumeRejectedError = engine.ResumeRejectedError
)

// --- MCP ---------------------------------------------------------------------

var (
	// ErrInvalidMCPConfig is returned when WithMCP / WithDefaultMCP is
	// given a malformed MCPConfig.
	ErrInvalidMCPConfig = engine.ErrInvalidMCPConfig

	// ErrMCPUnsupported is returned when an MCP-bound Run targets an
	// adapter that does not declare MCP support in its descriptor.
	ErrMCPUnsupported = engine.ErrMCPUnsupported

	// ErrMCPTransportUnsupported is returned when the adapter declares
	// MCP support but not for the transport the host requested.
	ErrMCPTransportUnsupported = engine.ErrMCPTransportUnsupported
)

// --- HITL (see docs/workstream-hitl-v2.md) ----------------------------------

var (
	// ErrHumanDecisionModeUnsupported surfaces at Start when the bound
	// adapter's RunPolicyCaps does not advertise the requested decision
	// mode (e.g. Permission=Ask on an adapter without ToolApproval).
	ErrHumanDecisionModeUnsupported = engine.ErrHumanDecisionModeUnsupported

	// ErrDecisionRequestExpired is returned by ResolveDecision when the
	// request has already been resolved or its deadline elapsed. Hosts
	// in async-bridge mode commonly map this to HTTP 409.
	ErrDecisionRequestExpired = engine.ErrDecisionRequestExpired

	// ErrDecisionResultKindMismatch is returned by ResolveDecision when
	// the response Kind does not match the request Kind (e.g. answering
	// a Permission request with a Question response).
	ErrDecisionResultKindMismatch = engine.ErrDecisionResultKindMismatch

	// ErrRunEnded is returned when an operation (Cancel, ResolveDecision,
	// Wait, ...) is invoked on a RunHandle whose run has already
	// terminated. Hosts commonly ignore it as a benign race.
	ErrRunEnded = engine.ErrRunEnded
)

// --- Structured output -------------------------------------------------------

var (
	// ErrStructuredOutputUnsupported is returned before adapter launch when
	// the requested structured-output mode cannot be honored by the bound
	// adapter or selected run mode.
	ErrStructuredOutputUnsupported = engine.ErrStructuredOutputUnsupported

	// ErrInvalidOutputSchema is returned before adapter launch when the host
	// supplies malformed JSON, an unsupported output format/mode, or a JSON
	// Schema document that cannot be compiled for local validation.
	ErrInvalidOutputSchema = engine.ErrInvalidOutputSchema
)

type (
	// StructuredOutputUnsupportedError carries diagnostic detail while
	// unwrapping to ErrStructuredOutputUnsupported.
	StructuredOutputUnsupportedError = engine.StructuredOutputUnsupportedError

	// InvalidOutputSchemaError carries diagnostic detail while unwrapping to
	// ErrInvalidOutputSchema.
	InvalidOutputSchemaError = engine.InvalidOutputSchemaError
)

// --- Skill (see docs/skill-api-design.md) -----------------------------------

var (
	// ErrSkillKeyConflict is the base sentinel; SkillKeyConflictError
	// carries the conflicting sources slice.
	ErrSkillKeyConflict = engine.ErrSkillKeyConflict

	// ErrSkillMaterializationFailed is returned when a selected skill was
	// resolved but could not be materialized into a local SKILL.md directory.
	// SkillMaterializationError carries the skill key and underlying cause.
	ErrSkillMaterializationFailed = engine.ErrSkillMaterializationFailed

	// ErrSkillSourceMissing is returned when a Skill value is registered
	// or selected without a non-nil Source.
	ErrSkillSourceMissing = engine.ErrSkillSourceMissing

	// ErrSkillKeyMissing is returned when a Skill is constructed with an
	// empty Key.
	ErrSkillKeyMissing = engine.ErrSkillKeyMissing

	// ErrSkillNotFound is returned during Run-time selection when a bare
	// SkillKey reference cannot be resolved against the configured
	// provider, default skills, or per-call skills.
	ErrSkillNotFound = engine.ErrSkillNotFound
)

// --- Predicates ---------------------------------------------------------
//
// These predicates exist only where:
//   (a) the matched error is a typed error (so the predicate is a
//       drop-in replacement for both errors.Is and errors.As), and
//   (b) hosts have a real behavioural branch on that single sentinel.
//
// Cross-sentinel aggregate predicates (e.g. IsExpired, IsConflict) are
// intentionally NOT exported. See docs/v0.5.0-host-integration-plan.md §A4.

// IsRunEnded reports whether err is or wraps ErrRunEnded. Hosts in
// async-bridge mode commonly use this to silently drop late
// ResolveDecision calls.
func IsRunEnded(err error) bool { return errors.Is(err, ErrRunEnded) }

// IsDecisionExpired reports whether err is or wraps
// ErrDecisionRequestExpired. Hosts in async-bridge mode commonly map
// this to HTTP 409 with no further logging.
func IsDecisionExpired(err error) bool { return errors.Is(err, ErrDecisionRequestExpired) }

// IsSessionBusy reports whether err is or wraps ErrSessionBusy
// (matches both the bare sentinel and *SessionBusyError).
func IsSessionBusy(err error) bool { return errors.Is(err, ErrSessionBusy) }

// IsSessionIncompatible reports whether err is or wraps
// ErrSessionIncompatible (matches both the bare sentinel and
// *SessionIncompatibleError). Hosts use this to surface "previous
// session is no longer compatible; start_new is required" in the UI.
func IsSessionIncompatible(err error) bool { return errors.Is(err, ErrSessionIncompatible) }

// IsSkillKeyConflict reports whether err is or wraps ErrSkillKeyConflict
// (matches both the bare sentinel and *SkillKeyConflictError). Hosts
// use this to detect catalogue drift between binding-default skills,
// per-call skills, and provider skills.
func IsSkillKeyConflict(err error) bool { return errors.Is(err, ErrSkillKeyConflict) }

// IsSkillMaterializationFailed reports whether err is or wraps
// ErrSkillMaterializationFailed (matches both the bare sentinel and
// *SkillMaterializationError). Hosts use this to fail tasks before the
// adapter starts when a selected skill archive/path/cache entry is invalid.
func IsSkillMaterializationFailed(err error) bool {
	return errors.Is(err, ErrSkillMaterializationFailed)
}
