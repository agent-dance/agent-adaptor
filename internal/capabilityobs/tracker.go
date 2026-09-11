package capabilityobs

import (
	"errors"
	"reflect"
	"sync"
	"time"

	"github.com/agent-dance/agent-adaptor/capability"
)

type Key struct {
	ScopeID      string
	InvocationID string
}
type Tracker struct {
	mu     sync.Mutex
	values map[Key]capability.Invocation
	starts map[Key]capability.Invocation
	order  []Key
	closed bool
}

var ErrConflict = errors.New("capabilityobs: conflicting lifecycle")
var ErrClosed = errors.New("capabilityobs: closed")

func NewTracker() *Tracker {
	return &Tracker{values: map[Key]capability.Invocation{}, starts: map[Key]capability.Invocation{}}
}
func ValidParent(scope, parentScope, parentID, id string) bool {
	if !ValidText(scope, 2048, false) || !ValidText(parentScope, 2048, false) || !ValidText(parentID, 2048, false) {
		return false
	}
	if parentID == "" {
		return parentScope == ""
	}
	return scope != parentScope || id != parentID
}
func ValidTime(at time.Time) bool {
	_, off := at.Zone()
	return !at.IsZero() && at.Year() >= 1 && at.Year() <= 9999 && off == 0
}
func Validate(v capability.Invocation) error {
	if !ValidKind(v.Ref.Kind) || !ValidText(v.Ref.Key, 512, true) || !ValidText(v.Ref.Operation, 256, true) {
		return ErrInvalid
	}
	if !ValidText(v.InvocationID, 2048, true) || !ValidParent(v.ScopeID, v.ParentScopeID, v.ParentToolCallID, v.InvocationID) || !ValidTime(v.OccurredAt) {
		return ErrInvalid
	}
	if v.Ref.Kind == capability.Skill && v.Ref.Operation != "activate" || v.Ref.Kind == capability.Subagent && v.Ref.Operation != "spawn" {
		return ErrInvalid
	}
	if !(v.Source == capability.Provider && (v.Evidence == capability.ProviderProtocol || v.Evidence == capability.NativeInputAccepted) ||
		v.Source == capability.Host && v.Evidence == capability.HostLifecycle ||
		v.Source == capability.Relay && v.Evidence == capability.Relayed) {
		return ErrInvalid
	}
	switch v.Phase {
	case capability.Started:
		if v.Duration != nil || v.ErrorCode != "" {
			return ErrInvalid
		}
	case capability.Completed:
		if v.ErrorCode != "" {
			return ErrInvalid
		}
	case capability.Failed, capability.Cancelled, capability.Interrupted:
	default:
		return ErrInvalid
	}
	if !validErrorCode(v.ErrorCode) {
		return ErrInvalid
	}
	if v.Duration != nil && *v.Duration < 0 {
		return ErrInvalid
	}
	return nil
}

func validErrorCode(code capability.ErrorCode) bool {
	switch code {
	case "", capability.ToolFailed, capability.RunCancelled, capability.RunInterrupted, capability.ProtocolError, capability.DelegationFailed:
		return true
	default:
		return false
	}
}

func Clone(v capability.Invocation) capability.Invocation {
	if v.Duration != nil {
		d := *v.Duration
		v.Duration = &d
	}
	return v
}
func (t *Tracker) Start(v capability.Invocation) (*capability.Invocation, error) {
	if v.Phase != capability.Started || Validate(v) != nil {
		return nil, ErrInvalid
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil, ErrClosed
	}
	k := Key{v.ScopeID, v.InvocationID}
	if old, ok := t.starts[k]; ok {
		a, b := old, v
		a.OccurredAt = time.Time{}
		b.OccurredAt = time.Time{}
		if reflect.DeepEqual(a, b) {
			return nil, nil
		}
		return nil, ErrConflict
	}
	t.order = append(t.order, k)
	t.values[k] = Clone(v)
	t.starts[k] = Clone(v)
	out := Clone(v)
	return &out, nil
}
func (t *Tracker) Terminal(k Key, phase capability.Phase, code capability.ErrorCode, at time.Time, duration *time.Duration) (*capability.Invocation, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.terminal(k, phase, code, at, duration)
}
func (t *Tracker) terminal(k Key, phase capability.Phase, code capability.ErrorCode, at time.Time, duration *time.Duration) (*capability.Invocation, error) {
	if phase == capability.Started {
		return nil, ErrInvalid
	}
	old, ok := t.values[k]
	if !ok {
		return nil, ErrUnknown
	}
	v := old
	v.Phase = phase
	v.ErrorCode = code
	v.OccurredAt = at
	v.Duration = duration
	if Validate(v) != nil {
		return nil, ErrInvalid
	}
	if old.Phase != capability.Started {
		a, b := old, v
		a.OccurredAt = time.Time{}
		b.OccurredAt = time.Time{}
		if reflect.DeepEqual(a, b) {
			return nil, nil
		}
		return nil, ErrConflict
	}
	t.values[k] = Clone(v)
	out := Clone(v)
	return &out, nil
}
func (t *Tracker) Close(phase capability.Phase, code capability.ErrorCode, at time.Time) ([]capability.Invocation, error) {
	if phase != capability.Cancelled && phase != capability.Interrupted && phase != capability.Failed {
		return nil, ErrInvalid
	}
	if !ValidTime(at) {
		return nil, ErrInvalid
	}
	if !validErrorCode(code) {
		return nil, ErrInvalid
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil, nil
	}
	t.closed = true
	out := []capability.Invocation{}
	for _, k := range t.order {
		if t.values[k].Phase == capability.Started {
			v, err := t.terminal(k, phase, code, at, nil)
			if err != nil {
				return nil, err
			}
			out = append(out, *v)
		}
	}
	return out, nil
}
