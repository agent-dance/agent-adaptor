package driver

import (
	"errors"
	"fmt"
)

// Stable pre-launch error categories shared by the root runner and Driver
// implementations. Keeping these identities in package driver lets both
// sides of the SPI wrap and match the same errors without depending on an
// internal package.
var (
	// ErrInvalidDriverConfig reports that Driver.ValidateConfig rejected the
	// configured Driver before an invocation was launched.
	ErrInvalidDriverConfig = errors.New("agentadaptor: invalid driver config")
	// ErrInvalidPolicy reports an out-of-domain RunPolicy value. Capability
	// misses use one of the dedicated unsupported sentinels instead.
	ErrInvalidPolicy = errors.New("agentadaptor: invalid run policy")
	// ErrPolicyCapabilityUnsupported reports that a valid, explicitly
	// selected non-approval policy value is unsupported by the Driver.
	ErrPolicyCapabilityUnsupported = errors.New("agentadaptor: policy capability unsupported by driver")
	// ErrHumanDecisionModeUnsupported reports that an explicitly selected
	// human-decision mode is absent from Descriptor.RunPolicyCaps.
	ErrHumanDecisionModeUnsupported = errors.New("agentadaptor: human decision mode unsupported by driver")
)

// InvalidDriverConfigError identifies the configured Driver and preserves
// its validation error while unwrapping to ErrInvalidDriverConfig.
type InvalidDriverConfigError struct {
	Driver string
	Cause  error
}

// Error implements error.
func (e *InvalidDriverConfigError) Error() string {
	if e == nil {
		return ErrInvalidDriverConfig.Error()
	}
	message := ErrInvalidDriverConfig.Error()
	if e.Driver != "" {
		message += ": driver=" + e.Driver
	}
	if e.Cause != nil {
		message += ": " + e.Cause.Error()
	}
	return message
}

// Unwrap preserves the stable category and the Driver-provided cause.
func (e *InvalidDriverConfigError) Unwrap() error {
	if e == nil || e.Cause == nil {
		return ErrInvalidDriverConfig
	}
	return errors.Join(ErrInvalidDriverConfig, e.Cause)
}

// InvalidPolicyError identifies one out-of-domain RunPolicy field. Value is
// formatted as text because the policy contains several distinct string
// enumerations plus MaxRetries.
type InvalidPolicyError struct {
	Driver string
	Field  string
	Value  string
}

// Error implements error.
func (e *InvalidPolicyError) Error() string {
	if e == nil {
		return ErrInvalidPolicy.Error()
	}
	message := ErrInvalidPolicy.Error()
	if e.Driver != "" {
		message += ": driver=" + e.Driver
	}
	if e.Field != "" {
		message += " field=" + e.Field
	}
	if e.Value != "" {
		message += " value=" + fmt.Sprintf("%q", e.Value)
	}
	return message
}

// Unwrap exposes ErrInvalidPolicy for errors.Is.
func (e *InvalidPolicyError) Unwrap() error { return ErrInvalidPolicy }

// PolicyCapabilityUnsupportedError identifies one valid policy dimension
// which the Driver cannot honor. Approval modes use the more specific
// HumanDecisionModeUnsupportedError.
type PolicyCapabilityUnsupportedError struct {
	Driver    string
	Dimension string
	Value     string
}

// Error implements error.
func (e *PolicyCapabilityUnsupportedError) Error() string {
	if e == nil {
		return ErrPolicyCapabilityUnsupported.Error()
	}
	message := ErrPolicyCapabilityUnsupported.Error()
	if e.Driver != "" {
		message += ": driver=" + e.Driver
	}
	if e.Dimension != "" {
		message += " dimension=" + e.Dimension
	}
	if e.Value != "" {
		message += " value=" + e.Value
	}
	return message
}

// Unwrap exposes ErrPolicyCapabilityUnsupported for errors.Is.
func (e *PolicyCapabilityUnsupportedError) Unwrap() error {
	return ErrPolicyCapabilityUnsupported
}

// HumanDecisionModeUnsupportedError identifies the exact capability miss.
// Mode is textual because Permission/PlanReview and Question deliberately use
// different mode types.
type HumanDecisionModeUnsupportedError struct {
	Driver string
	Kind   HumanDecisionKind
	Mode   string
}

// Error implements error.
func (e *HumanDecisionModeUnsupportedError) Error() string {
	if e == nil {
		return ErrHumanDecisionModeUnsupported.Error()
	}
	message := ErrHumanDecisionModeUnsupported.Error()
	if e.Driver != "" {
		message += ": driver=" + e.Driver
	}
	if e.Kind != "" {
		message += " kind=" + string(e.Kind)
	}
	if e.Mode != "" {
		message += " mode=" + e.Mode
	}
	return message
}

// Unwrap exposes ErrHumanDecisionModeUnsupported for errors.Is.
func (e *HumanDecisionModeUnsupportedError) Unwrap() error {
	return ErrHumanDecisionModeUnsupported
}
