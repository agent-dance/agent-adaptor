package agentadaptor

import "errors"

var (
	ErrAgentBindingRequired          = errors.New("agentadaptor: agent binding required")
	ErrAgentNameRequired             = errors.New("agentadaptor: agent name required")
	ErrAgentNotFound                 = errors.New("agentadaptor: agent not found")
	ErrDefaultAgentAlreadyConfigured = errors.New("agentadaptor: default agent already configured")
	ErrDefaultAgentMissing           = errors.New("agentadaptor: default agent missing")
	ErrInvalidDriverConfig           = errors.New("agentadaptor: invalid driver config")
	ErrReservedAgentName             = errors.New("agentadaptor: reserved agent name")
	ErrResumeRejected                = errors.New("agentadaptor: driver rejected session resume")
	ErrSessionBusy                   = errors.New("agentadaptor: session busy")
	ErrSessionCheckpointMissing      = errors.New("agentadaptor: session checkpoint missing")
	ErrSessionIncompatible           = errors.New("agentadaptor: session incompatible")
	ErrSessionLeaseLost              = errors.New("agentadaptor: session lease lost")
	ErrSessionNotFound               = errors.New("agentadaptor: session not found")
	ErrSessionStoreRequired          = errors.New("agentadaptor: session store required")
	ErrInvalidMCPConfig              = errors.New("agentadaptor: invalid MCP configuration")
	ErrMCPUnsupported                = errors.New("agentadaptor: MCP unsupported by adapter")
	ErrMCPTransportUnsupported       = errors.New("agentadaptor: MCP transport unsupported by adapter")

	// HITL v2 (see docs/workstream-hitl-v2.md).
	ErrHumanDecisionModeUnsupported = errors.New("agentadaptor: human decision mode unsupported by adapter")
	ErrDecisionRequestExpired       = errors.New("agentadaptor: decision request expired or already resolved")
	ErrDecisionResultKindMismatch   = errors.New("agentadaptor: decision result is incompatible with request kind")
	ErrRunEnded                     = errors.New("agentadaptor: run already ended")
)

type DuplicateAgentError struct {
	Name string
}

func (e *DuplicateAgentError) Error() string {
	if e == nil || e.Name == "" {
		return "agentadaptor: duplicate agent"
	}
	return "agentadaptor: duplicate agent: " + e.Name
}

type SessionBusyError struct {
	Target string
}

func (e *SessionBusyError) Error() string {
	if e == nil || e.Target == "" {
		return ErrSessionBusy.Error()
	}
	return ErrSessionBusy.Error() + ": " + e.Target
}

func (e *SessionBusyError) Unwrap() error {
	return ErrSessionBusy
}

type SessionLeaseLostError struct {
	Target string
}

func (e *SessionLeaseLostError) Error() string {
	if e == nil || e.Target == "" {
		return ErrSessionLeaseLost.Error()
	}
	return ErrSessionLeaseLost.Error() + ": " + e.Target
}

func (e *SessionLeaseLostError) Unwrap() error {
	return ErrSessionLeaseLost
}

type SessionIncompatibleError struct {
	Reason              string
	ExpectedFingerprint string
	ActualFingerprint   string
}

func (e *SessionIncompatibleError) Error() string {
	if e == nil || e.Reason == "" {
		return ErrSessionIncompatible.Error()
	}
	return ErrSessionIncompatible.Error() + ": " + e.Reason
}

func (e *SessionIncompatibleError) Unwrap() error {
	return ErrSessionIncompatible
}

type ResumeRejectedError struct {
	Reason string
	Cause  error
}

func (e *ResumeRejectedError) Error() string {
	if e == nil || e.Reason == "" {
		return ErrResumeRejected.Error()
	}
	return ErrResumeRejected.Error() + ": " + e.Reason
}

func (e *ResumeRejectedError) Unwrap() error {
	if e == nil || e.Cause == nil {
		return ErrResumeRejected
	}
	return errors.Join(ErrResumeRejected, e.Cause)
}
