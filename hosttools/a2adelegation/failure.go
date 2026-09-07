package a2adelegation

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	clienta2a "github.com/agent-dance/agent-adaptor/clients/a2a"
)

const maxBudgetMilliseconds int64 = 9223372036855

func failureCodeKnown(code string) bool {
	switch code {
	case "active_execution_timeout", "approval_denied", "approval_timeout", "cancelled", "deadline_exceeded", "agent_error", "policy_violation", "infrastructure_error":
		return true
	}
	return false
}
func failureCause(code string) error {
	switch code {
	case "active_execution_timeout":
		return adaptor.ErrActiveExecutionTimeout
	case "approval_denied":
		return adaptor.ErrApprovalDenied
	case "approval_timeout":
		return adaptor.ErrApprovalTimeout
	case "cancelled":
		return context.Canceled
	case "deadline_exceeded":
		return context.DeadlineExceeded
	}
	return nil
}
func safeFailure(code string) string {
	switch code {
	case "active_execution_timeout":
		return "active execution budget exhausted"
	case "approval_denied":
		return "approval denied"
	case "approval_timeout":
		return "approval timed out"
	case "cancelled":
		return "execution cancelled"
	case "deadline_exceeded":
		return "execution deadline exceeded"
	case "policy_violation":
		return "execution policy violation"
	case "infrastructure_error":
		return "execution infrastructure failure"
	default:
		return "agent execution failed"
	}
}
func milliseconds(limit time.Duration) int64 {
	n := int64(limit / time.Millisecond)
	if limit%time.Millisecond != 0 {
		n++
	}
	return n
}
func parseMilliseconds(v any) (int64, bool) {
	var n int64
	switch v := v.(type) {
	case json.Number:
		return parseJSONMilliseconds(v)
	case int:
		n = int64(v)
	case int64:
		n = v
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) || v != math.Trunc(v) || v < 1 || v > float64(maxBudgetMilliseconds) {
			return 0, false
		}
		n = int64(v)
	default:
		return 0, false
	}
	return n, n >= 1 && n <= maxBudgetMilliseconds
}

// parseJSONMilliseconds checks the decimal value exactly, without float
// rounding or allocating an integer proportional to an untrusted exponent.
// Work and storage are linear in the already-received literal's length.
func parseJSONMilliseconds(value json.Number) (int64, bool) {
	raw := value.String()
	if len(raw) == 0 || raw[0] < '0' || raw[0] > '9' || !json.Valid([]byte(raw)) {
		return 0, false
	}
	mantissa := raw
	exponent := int64(0)
	if i := strings.IndexAny(raw, "eE"); i >= 0 {
		var err error
		exponent, err = strconv.ParseInt(raw[i+1:], 10, 64)
		if err != nil {
			return 0, false
		}
		mantissa = raw[:i]
	}
	// A valid nonzero value in this domain has at most thirteen digits.
	// An exponent beyond the literal's size cannot be offset by its mantissa.
	if exponent > int64(len(raw))+13 || exponent < -int64(len(raw)) {
		return 0, false
	}
	fraction := 0
	if i := strings.IndexByte(mantissa, '.'); i >= 0 {
		fraction = len(mantissa) - i - 1
	}
	digits := strings.TrimLeft(strings.ReplaceAll(mantissa, ".", ""), "0")
	if digits == "" {
		return 0, false
	}
	trimmed := strings.TrimRight(digits, "0")
	exponent += int64(len(digits) - len(trimmed) - fraction)
	digits = trimmed
	if exponent < 0 || int64(len(digits))+exponent > 13 {
		return 0, false
	}
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return 0, false
	}
	for ; exponent > 0; exponent-- {
		n *= 10
	}
	return n, n >= 1 && n <= maxBudgetMilliseconds
}

func budgetDuration(ms int64) time.Duration {
	if ms > math.MaxInt64/int64(time.Millisecond) {
		return time.Duration(math.MaxInt64)
	}
	return time.Duration(ms) * time.Millisecond
}

// Only the frozen Text Part control object can promote a remote error. The
// metadata container is diagnostic and never participates in primary selection.
func failureFromStatus(status clienta2a.TaskStatus) (*DelegationError, bool) {
	if status.Message == nil {
		return nil, false
	}
	var result *DelegationError
	var code string
	var limit int64
	var hasLimit bool
	for _, part := range status.Message.Parts {
		if part.Kind != clienta2a.PartText {
			continue
		}
		value, exists := part.Metadata["agentadaptor.failure"]
		if !exists {
			continue
		}
		obj, ok := value.(map[string]any)
		if !ok || obj == nil {
			return nil, true
		}
		for k := range obj {
			if k != "code" && k != "limit_ms" && k != "metadata" {
				return nil, true
			}
		}
		c, ok := obj["code"].(string)
		if !ok || !failureCodeKnown(c) {
			return nil, true
		}
		if metadata, exists := obj["metadata"]; exists {
			if _, ok := metadata.(map[string]any); !ok {
				return nil, true
			}
		}
		if (c == "cancelled" && status.State != clienta2a.TaskStateCanceled) || (c != "cancelled" && status.State != clienta2a.TaskStateFailed) {
			return nil, true
		}
		lvalue, has := obj["limit_ms"]
		var l int64
		if has {
			if c != "active_execution_timeout" {
				return nil, true
			}
			var valid bool
			l, valid = parseMilliseconds(lvalue)
			if !valid {
				return nil, true
			}
		}
		if result != nil {
			if code != c || hasLimit != has || limit != l {
				return nil, true
			}
			continue
		}
		code, limit, hasLimit = c, l, has
		result = &DelegationError{Code: c, Message: safeFailure(c), RemoteStatus: string(status.State), Retryable: c == "cancelled", Cause: failureCause(c)}
		if has {
			result.Metadata = map[string]any{"limit_ms": l}
			result.Cause = &adaptor.ActiveExecutionTimeoutError{Limit: budgetDuration(l)}
		}
	}
	return result, false
}
func rootFailureHint(err error, hint string) *DelegationError {
	if err == nil {
		return nil
	}
	var re *adaptor.RunError
	code := ""
	limitSource := err
	if errors.As(err, &re) && re != nil {
		// The carrier owns its primary limit. Outer joined causes remain evidence
		// in Cause, but cannot supply a different budget or fill a missing limit.
		limitSource = re.Cause
		code = string(re.Reason)
		if !failureCodeKnown(code) {
			code = "remote_failed"
		}
	} else if failureCodeKnown(hint) {
		code = hint
	} else {
		switch {
		case errors.Is(err, adaptor.ErrActiveExecutionTimeout):
			code = "active_execution_timeout"
		case errors.Is(err, context.DeadlineExceeded):
			code = "deadline_exceeded"
		case errors.Is(err, context.Canceled):
			code = "cancelled"
		default:
			code = "remote_failed"
		}
	}
	d := &DelegationError{Code: code, Message: safeFailure(code), Cause: err, Retryable: code == "cancelled", RemoteStatus: string(clienta2a.TaskStateFailed)}
	if code == "cancelled" {
		d.RemoteStatus = string(clienta2a.TaskStateCanceled)
	}
	if code == "active_execution_timeout" {
		var limit *adaptor.ActiveExecutionTimeoutError
		if errors.As(limitSource, &limit) && limit != nil && limit.Limit > 0 {
			d.Metadata = map[string]any{"limit_ms": milliseconds(limit.Limit)}
		}
	}
	return d
}
func failureStatus(d *DelegationError) clienta2a.TaskStatus {
	state := clienta2a.TaskStateFailed
	if d.Code == "cancelled" {
		state = clienta2a.TaskStateCanceled
	}
	obj := map[string]any{}
	if failureCodeKnown(d.Code) {
		obj["code"] = d.Code
		if ms, ok := d.Metadata["limit_ms"]; ok {
			obj["limit_ms"] = ms
		}
	}
	var metadata map[string]any
	if len(obj) > 0 {
		metadata = map[string]any{"agentadaptor.failure": obj}
	}
	return clienta2a.TaskStatus{State: state, Message: &clienta2a.Message{Role: localRoleAgent, Parts: []clienta2a.Part{{Kind: clienta2a.PartText, Text: safeFailure(d.Code), Metadata: metadata}}}}
}
