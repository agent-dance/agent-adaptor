// The SDK's public error catalogue (moved from the root package; the root
// package re-exports every name below so hosts keep matching the same
// error values via the historical agentadaptor identifiers).
//
// This file is the migration engine's error catalogue. Domain leaf packages
// own errors that belong to their public vocabulary (skill, MCP, structured
// output); the aliases below preserve legacy identity during the cutover.
//
// # Quick reference
//
// Hosts that wrap the SDK in an HTTP / RPC surface should consult
// docs/public-errors.md for the recommended HTTP status code, log level,
// and alert policy per error. The summary table below is normative: the
// inline comments mirror the same matrix so the godoc surface and the
// markdown reference stay in sync.
//
//	Group     | Sentinel                          | Suggested HTTP | Log  | Alert
//	----------+-----------------------------------+----------------+------+------
//	binding   | ErrAgentBindingRequired           | 400            | warn | no
//	          | ErrAgentNameRequired              | 400            | warn | no
//	          | ErrAgentNotFound                  | 404            | info | no
//	          | ErrDefaultAgentAlreadyConfigured  | (panic at New) |  -   | -
//	          | ErrDefaultAgentMissing            | (panic at New) |  -   | -
//	          | ErrInvalidDriverConfig            | 400            | warn | no
//	          | ErrReservedAgentName              | 400            | warn | no
//	session   | ErrResumeRejected                 | 409            | warn | no
//	          | ErrSessionBusy                    | 409            | info | no
//	          | ErrSessionCheckpointMissing       | 404            | info | no
//	          | ErrSessionIncompatible            | 409            | warn | no
//	          | ErrSessionLeaseLost               | 409            | warn | yes
//	          | ErrSessionNotFound                | 404            | info | no
//	          | ErrSessionStoreRequired           | 500            | err  | yes
//	mcp       | ErrInvalidMCPConfig               | 400            | warn | no
//	          | ErrMCPUnsupported                 | 400            | info | no
//	          | ErrMCPTransportUnsupported        | 400            | info | no
//	hitl      | ErrHumanDecisionModeUnsupported   | 400            | warn | no
//	          | ErrDecisionRequestExpired         | 409            | info | no
//	          | ErrDecisionResultKindMismatch     | 400            | warn | no
//	          | ErrRunEnded                       | 409            | info | no
//	structured | ErrStructuredOutputUnsupported    | 400            | warn | no
//	          | ErrInvalidOutputSchema            | 400            | warn | no
//	skill     | ErrSkillKeyConflict               | 500            | err  | yes
//	          | ErrSkillMaterializationFailed     | 500            | err  | no
//	          | ErrSkillSourceMissing             | 500            | err  | no
//	          | ErrSkillKeyMissing                | 500            | err  | no
//	          | ErrSkillNotFound                  | 404            | info | no
//
// # Predicates
//
// Six typed-error predicates are exported when hosts have a real branch
// on a single sentinel/typed-error pair: IsRunEnded, IsDecisionExpired,
// IsSessionBusy, IsSessionIncompatible, IsSkillKeyConflict, and
// IsSkillMaterializationFailed. All other errors should be inspected with
// errors.Is(err, ErrXxx) directly. The
// SDK intentionally does NOT export aggregate predicates such as
// IsExpired or IsConflict because compressing multiple sentinels into
// one boolean is irreversible: any future split (e.g. ErrRunEnded ->
// ErrRunEndedNormally + ErrRunEndedAborted) would silently drift the
// predicate's semantics. See docs/v0.5.0-host-integration-plan.md §A4.
package engine

import (
	"errors"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/mcp"
	"github.com/agent-dance/agent-adaptor/skill"
)

// --- Agent / binding -----------------------------------------------------

var (
	// ErrAgentBindingRequired is returned by WithAgent / WithDefaultAgent
	// when the binding argument is nil.
	ErrAgentBindingRequired = errors.New("agentadaptor: agent binding required")

	// ErrAgentNameRequired is returned by WithAgent when the name argument
	// is empty. Use WithDefaultAgent for the unnamed/default binding.
	ErrAgentNameRequired = errors.New("agentadaptor: agent name required")

	// ErrAgentNotFound is returned by SDK.Agent / Admin().Agent when the
	// requested name was never registered.
	ErrAgentNotFound = errors.New("agentadaptor: agent not found")

	// ErrDefaultAgentAlreadyConfigured surfaces from agentadaptor.New when
	// WithDefaultAgent is supplied more than once. New panics with this
	// error so the misconfiguration is caught at startup, not later.
	ErrDefaultAgentAlreadyConfigured = errors.New("agentadaptor: default agent already configured")

	// ErrDefaultAgentMissing surfaces from agentadaptor.New when no
	// WithDefaultAgent option is supplied. New panics so callers cannot
	// accidentally construct an SDK with no default agent and discover it
	// only on the first Run.
	ErrDefaultAgentMissing = errors.New("agentadaptor: default agent missing")

	// ErrInvalidDriverConfig wraps the adapter-side ValidateConfig error
	// with the SDK-stable sentinel so hosts can pattern-match without
	// importing the adapter package.
	ErrInvalidDriverConfig = errors.New("agentadaptor: invalid driver config")

	// ErrReservedAgentName is returned when a host attempts to register
	// the literal name "default" via WithAgent (it is reserved for the
	// default binding).
	ErrReservedAgentName = errors.New("agentadaptor: reserved agent name")
)

// DuplicateAgentError is returned (wrapping nothing — match by *type with
// errors.As) when WithAgent registers a name twice.
type DuplicateAgentError struct {
	Name string
}

// Error reports the duplicate registration with the offending Name.
func (e *DuplicateAgentError) Error() string {
	if e == nil || e.Name == "" {
		return "agentadaptor: duplicate agent"
	}
	return "agentadaptor: duplicate agent: " + e.Name
}

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
	// ErrInvalidMCPConfig is returned when WithMCP / WithDefaultMCP is
	// given a malformed MCPConfig.
	ErrInvalidMCPConfig = mcp.ErrInvalidConfig

	// ErrMCPUnsupported is returned when an MCP-bound Run targets an
	// adapter that does not declare MCP support in its descriptor.
	ErrMCPUnsupported = mcp.ErrUnsupported

	// ErrMCPTransportUnsupported is returned when the adapter declares
	// MCP support but not for the transport the host requested.
	ErrMCPTransportUnsupported = mcp.ErrTransportUnsupported
)

// --- HITL (see docs/workstream-hitl-v2.md) ------------------------------

var (
	// ErrHumanDecisionModeUnsupported surfaces at Start when the bound
	// adapter's RunPolicyCaps does not advertise the requested decision
	// mode (e.g. Permission=Ask on an adapter without ToolApproval).
	ErrHumanDecisionModeUnsupported = errors.New("agentadaptor: human decision mode unsupported by adapter")

	// ErrDecisionRequestExpired is returned by ResolveDecision when the
	// request has already been resolved or its deadline elapsed. Hosts
	// in async-bridge mode commonly map this to HTTP 409.
	ErrDecisionRequestExpired = errors.New("agentadaptor: decision request expired or already resolved")

	// ErrDecisionResultKindMismatch is returned by ResolveDecision when
	// the response Kind does not match the request Kind (e.g. answering
	// a Permission request with a Question response).
	ErrDecisionResultKindMismatch = errors.New("agentadaptor: decision result is incompatible with request kind")

	// ErrRunEnded is returned when an operation (Cancel, ResolveDecision,
	// Wait, ...) is invoked on a RunHandle whose run has already
	// terminated. Hosts commonly ignore it as a benign race.
	ErrRunEnded = errors.New("agentadaptor: run already ended")
)

// --- Structured output --------------------------------------------------

var (
	// ErrStructuredOutputUnsupported is returned before adapter launch when
	// the requested structured-output mode cannot be honored by the bound
	// adapter or selected run mode.
	ErrStructuredOutputUnsupported = driver.ErrStructuredOutputUnsupported

	// ErrInvalidOutputSchema is returned before adapter launch when the host
	// supplies malformed JSON, an unsupported output format/mode, or a JSON
	// Schema document that cannot be compiled for local validation.
	ErrInvalidOutputSchema = driver.ErrInvalidOutputSchema
)

type (
	// StructuredOutputUnsupportedError is retained as a migration alias. The
	// concrete type and sentinel identity are owned by package driver.
	StructuredOutputUnsupportedError = driver.StructuredOutputUnsupportedError
	// InvalidOutputSchemaError is retained as a migration alias. The concrete
	// type and sentinel identity are owned by package driver.
	InvalidOutputSchemaError = driver.InvalidOutputSchemaError
)

// --- Skill (see docs/skill-api-design.md) -------------------------------

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

// --- Predicates ---------------------------------------------------------
//
// These predicates exist only where:
//   (a) the matched error is a typed error (so the predicate is a
//       drop-in replacement for both errors.Is and errors.As), and
//   (b) hosts have a real behavioural branch on that single sentinel.
//
// Cross-sentinel aggregate predicates (e.g. IsExpired, IsConflict) are
// intentionally NOT exported. Aggregating multiple sentinels into one
// bool is irreversible: a future split (ErrRunEnded -> Normally +
// Aborted) would silently drift the predicate's meaning even when the
// new sentinel is harmless to the host. Use errors.Is(err, ErrXxx)
// directly when you want a generic check.

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
