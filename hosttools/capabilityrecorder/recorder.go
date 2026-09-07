package capabilityrecorder

import (
	"context"
	"errors"
	"reflect"
	"time"
	"unicode"
	"unicode/utf8"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/capability"
)

// Recorder attaches a safe capability projection through the public run-service
// observer. It is safe to share across Agents and runs. It has no Close method:
// shared Store lifetime belongs to the host.
type Recorder struct{ store Store }

// New builds an optional recorder using the explicit host-owned Store.
func New(cfg Config) (*Recorder, error) {
	if cfg.Store == nil {
		return nil, ErrStoreRequired
	}
	v := reflect.ValueOf(cfg.Store)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		if v.IsNil() {
			return nil, ErrStoreRequired
		}
	}
	return &Recorder{store: cfg.Store}, nil
}

// Option attaches this recorder at construction or for one Run/Stream call.
// It requests formal capability observations independently of consumer method
// and collects accepted facts before user backpressure. Unavailable provider
// observations remain unavailable, never synthesized as zero calls.
func (r *Recorder) Option() adaptor.SharedOption {
	return adaptor.WithRunServices(recorderProvider{recorder: r})
}

// Query reads one exact scope, including while the run is active. It rejects
// out-of-scope, unordered or invalid custom Store pages without returning a
// partial page, and returns a deep copy. Store errors remain observable to the
// host; query failures do not disable observation or change run outcomes.
func (r *Recorder) Query(ctx context.Context, q Query) (Page, error) {
	q, err := normalizeQuery(q)
	if err != nil {
		return Page{}, err
	}
	if r == nil || r.store == nil {
		return Page{}, ErrStoreRequired
	}
	if err := ctx.Err(); err != nil {
		return Page{}, err
	}
	page, err := r.store.Query(ctx, q)
	if err != nil {
		return Page{}, err
	}
	if err := ctx.Err(); err != nil {
		return Page{}, err
	}
	if len(page.Records) > q.Limit {
		return Page{}, errInvalidPage
	}
	previous := q.AfterSequence
	for _, record := range page.Records {
		if record.Scope != q.Scope || record.Sequence <= previous || !validRecord(record) {
			return Page{}, errInvalidPage
		}
		previous = record.Sequence
	}
	if page.NextSequence != 0 && (len(page.Records) == 0 || page.NextSequence != previous) {
		return Page{}, errInvalidPage
	}
	return clonePage(page), nil
}

type recorderProvider struct{ recorder *Recorder }

func (p recorderProvider) AttachRun(ctx context.Context, runID string) (adaptor.RunAttachment, error) {
	if p.recorder == nil || p.recorder.store == nil {
		return adaptor.RunAttachment{}, ErrStoreRequired
	}
	if err := ctx.Err(); err != nil {
		return adaptor.RunAttachment{}, err
	}
	return adaptor.RunAttachment{
		Observation: adaptor.ObservationDemand{CapabilityInvocations: true},
		Observer: func(ctx context.Context, info adaptor.RunEventInfo, ev adaptor.Event) error {
			fact, ok := ev.(adaptor.CapabilityInvocation)
			if !ok {
				return nil
			}
			meta := ev.Meta()
			record := Record{
				Scope:    Scope{IdentityID: info.Identity.ID, Tenant: info.Identity.Tenant, Profile: info.Identity.Profile, RunID: info.RunID},
				Sequence: meta.Sequence, Time: meta.Time, Invocation: fact.Invocation,
			}
			if info.RunID != runID || meta.RunID != runID || !validRecord(record) {
				return errInvalidRecord
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			// Core owns ordering, first-failure isolation, the bounded callback
			// context and late-return fence. No second worker or error policy.
			return p.recorder.store.Append(ctx, cloneRecord(record))
		},
	}, nil
}

func (recorderProvider) DetachRun(context.Context, string) error {
	// No local per-run state or resources are retained. Core revokes and drops
	// the observer closure; the shared Store is deliberately not closed.
	return nil
}

var (
	errInvalidRecord = errors.New("capabilityrecorder: invalid record")
	errInvalidPage   = errors.New("capabilityrecorder: invalid store page")
	errConflict      = errors.New("capabilityrecorder: conflicting record")
)

func normalizeQuery(q Query) (Query, error) {
	if q.Scope.RunID == "" || q.Limit < 0 || q.Limit > 1000 {
		return Query{}, ErrInvalidQuery
	}
	if q.Limit == 0 {
		q.Limit = 100
	}
	return q, nil
}

// Validation repeats only the closed public value contract. It never parses
// provider bytes, discovers catalogs or infers trust from resource/tool names.
func validRecord(r Record) bool {
	v := r.Invocation
	if r.Scope.RunID == "" || r.Sequence == 0 || !validTime(r.Time) || !validTime(v.OccurredAt) ||
		!validText(v.InvocationID, 2048, true) || !validText(v.Ref.Key, 512, true) || !validText(v.Ref.Operation, 256, true) ||
		!validText(v.ScopeID, 2048, false) || !validText(v.ParentScopeID, 2048, false) || !validText(v.ParentToolCallID, 2048, false) ||
		(v.ParentToolCallID == "" && v.ParentScopeID != "") ||
		(v.ParentToolCallID != "" && v.ScopeID == v.ParentScopeID && v.InvocationID == v.ParentToolCallID) {
		return false
	}
	switch v.Ref.Kind {
	case capability.Skill:
		if v.Ref.Operation != "activate" {
			return false
		}
	case capability.Subagent:
		if v.Ref.Operation != "spawn" {
			return false
		}
	case capability.MCP:
	default:
		return false
	}
	if !(v.Source == capability.Provider && (v.Evidence == capability.ProviderProtocol || v.Evidence == capability.NativeInputAccepted) ||
		v.Source == capability.Host && v.Evidence == capability.HostLifecycle || v.Source == capability.Relay && v.Evidence == capability.Relayed) {
		return false
	}
	switch v.Phase {
	case capability.Started:
		if v.Duration != nil || v.ErrorCode != "" {
			return false
		}
	case capability.Completed:
		if v.ErrorCode != "" {
			return false
		}
	case capability.Failed, capability.Cancelled, capability.Interrupted:
	default:
		return false
	}
	switch v.ErrorCode {
	case "", capability.ToolFailed, capability.RunCancelled, capability.RunInterrupted, capability.ProtocolError, capability.DelegationFailed:
	default:
		return false
	}
	return v.Duration == nil || *v.Duration >= 0
}

func validTime(t time.Time) bool {
	_, offset := t.Zone()
	return !t.IsZero() && t.Year() >= 1 && t.Year() <= 9999 && offset == 0
}

func validText(s string, limit int, required bool) bool {
	if (required && s == "") || len(s) > limit || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func cloneRecord(r Record) Record {
	if r.Invocation.Duration != nil {
		duration := *r.Invocation.Duration
		r.Invocation.Duration = &duration
	}
	return r
}

func clonePage(page Page) Page {
	out := Page{Records: make([]Record, len(page.Records)), NextSequence: page.NextSequence}
	for i, r := range page.Records {
		out.Records[i] = cloneRecord(r)
	}
	return out
}
