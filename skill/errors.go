package skill

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

var (
	// ErrSkillKeyConflict identifies conflicting declarations of one skill key.
	ErrSkillKeyConflict = errors.New("agentadaptor: skill key defined with conflicting sources")
	// ErrSkillMaterializationFailed identifies a failure to stage a skill.
	ErrSkillMaterializationFailed = errors.New("agentadaptor: skill materialization failed")
	// ErrSkillSourceMissing identifies a skill declaration without a source.
	ErrSkillSourceMissing = errors.New("agentadaptor: skill source is required")
	// ErrSkillKeyMissing identifies a skill declaration without a key.
	ErrSkillKeyMissing = errors.New("agentadaptor: skill key is required")
	// ErrSkillNotFound identifies a catalogue key that a Provider cannot resolve.
	ErrSkillNotFound = errors.New("agentadaptor: skill not found in provider")
)

// SkillKeyConflictError reports two structurally different declarations with
// the same skill key.
type SkillKeyConflictError struct {
	// Key is the conflicting skill key.
	Key string
	// Sources describes the declarations that conflict.
	Sources []string
	// Detail contains optional diagnostic context.
	Detail string
}

// Error implements error.
func (e *SkillKeyConflictError) Error() string {
	if e == nil {
		return ErrSkillKeyConflict.Error()
	}
	parts := append([]string(nil), e.Sources...)
	sort.Strings(parts)
	msg := fmt.Sprintf("agentadaptor: skill key %q is defined by multiple sources with different content", e.Key)
	if len(parts) > 0 {
		msg += " [" + strings.Join(parts, ", ") + "]"
	}
	if strings.TrimSpace(e.Detail) != "" {
		msg += ": " + e.Detail
	}
	return msg
}

// Unwrap exposes [ErrSkillKeyConflict] for errors.Is.
func (e *SkillKeyConflictError) Unwrap() error { return ErrSkillKeyConflict }

// SkillMaterializationError reports a failure to stage a resolved skill into
// a provider-visible directory.
type SkillMaterializationError struct {
	// Key is the logical skill key.
	Key string
	// RuntimeName is the provider-visible directory name, when known.
	RuntimeName string
	// Cause is the underlying materialization failure.
	Cause error
}

// Error implements error.
func (e *SkillMaterializationError) Error() string {
	if e == nil {
		return ErrSkillMaterializationFailed.Error()
	}
	msg := fmt.Sprintf("agentadaptor: skill %q materialization failed", e.Key)
	if strings.TrimSpace(e.RuntimeName) != "" && e.RuntimeName != e.Key {
		msg += fmt.Sprintf(" (runtime name %q)", e.RuntimeName)
	}
	if e.Cause != nil {
		msg += ": " + e.Cause.Error()
	}
	return msg
}

// Unwrap preserves the underlying materializer cause.
func (e *SkillMaterializationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// Is also classifies the error as [ErrSkillMaterializationFailed].
func (e *SkillMaterializationError) Is(target error) bool {
	return target == ErrSkillMaterializationFailed
}
