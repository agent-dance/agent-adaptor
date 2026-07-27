package skill

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Skill resolution and materialization errors live with the skill
// vocabulary. The engine and root package reuse these exact values and types,
// so errors.Is and errors.As never depend on an internal package identity.
var (
	ErrSkillKeyConflict           = errors.New("agentadaptor: skill key defined with conflicting sources")
	ErrSkillMaterializationFailed = errors.New("agentadaptor: skill materialization failed")
	ErrSkillSourceMissing         = errors.New("agentadaptor: skill source is required")
	ErrSkillKeyMissing            = errors.New("agentadaptor: skill key is required")
	ErrSkillNotFound              = errors.New("agentadaptor: skill not found in provider")
)

// SkillKeyConflictError reports two structurally different declarations with
// the same skill key.
type SkillKeyConflictError struct {
	Key     string
	Sources []string
	Detail  string
}

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
	Key         string
	RuntimeName string
	Cause       error
}

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
