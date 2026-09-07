package capabilityrecorder

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/capability"
)

func safeRecord() Record {
	return Record{Scope: Scope{RunID: "r"}, Sequence: 1, Time: time.Now().UTC(), Invocation: capability.Invocation{
		InvocationID: "call", Ref: capability.Ref{Kind: capability.MCP, Key: "key", Operation: "search"}, Phase: capability.Started,
		Source: capability.Provider, Evidence: capability.ProviderProtocol, OccurredAt: time.Now().UTC(),
	}}
}

func TestClosedRecordValidation(t *testing.T) {
	for name, mutate := range map[string]func(*Record){
		"run":              func(r *Record) { r.Scope.RunID = "" },
		"sequence":         func(r *Record) { r.Sequence = 0 },
		"time":             func(r *Record) { r.Time = time.Time{} },
		"occurred":         func(r *Record) { r.Invocation.OccurredAt = time.Time{} },
		"timezone":         func(r *Record) { r.Invocation.OccurredAt = r.Invocation.OccurredAt.In(time.FixedZone("offset", 3600)) },
		"kind":             func(r *Record) { r.Invocation.Ref.Kind = "unknown" },
		"empty-key":        func(r *Record) { r.Invocation.Ref.Key = "" },
		"long-key":         func(r *Record) { r.Invocation.Ref.Key = strings.Repeat("a", 513) },
		"invalid-utf8":     func(r *Record) { r.Invocation.Ref.Key = "\xff" },
		"control":          func(r *Record) { r.Invocation.Ref.Key = "key\nsecret" },
		"operation":        func(r *Record) { r.Invocation.Ref.Operation = strings.Repeat("a", 257) },
		"skill-operation":  func(r *Record) { r.Invocation.Ref.Kind = capability.Skill },
		"agent-operation":  func(r *Record) { r.Invocation.Ref.Kind = capability.Subagent },
		"id":               func(r *Record) { r.Invocation.InvocationID = "" },
		"long-id":          func(r *Record) { r.Invocation.InvocationID = strings.Repeat("a", 2049) },
		"scope":            func(r *Record) { r.Invocation.ScopeID = "bad\x00" },
		"parent-id":        func(r *Record) { r.Invocation.ParentToolCallID = "bad\n" },
		"parent-scope":     func(r *Record) { r.Invocation.ParentScopeID = "parent" },
		"self-parent":      func(r *Record) { r.Invocation.ParentToolCallID = "call" },
		"source":           func(r *Record) { r.Invocation.Source = "unknown" },
		"evidence":         func(r *Record) { r.Invocation.Evidence = "unknown" },
		"forged-evidence":  func(r *Record) { r.Invocation.Source = capability.Host },
		"phase":            func(r *Record) { r.Invocation.Phase = "unknown" },
		"started-duration": func(r *Record) { zero := time.Duration(0); r.Invocation.Duration = &zero },
		"started-error":    func(r *Record) { r.Invocation.ErrorCode = capability.ToolFailed },
		"completed-error": func(r *Record) {
			r.Invocation.Phase = capability.Completed
			r.Invocation.ErrorCode = capability.ToolFailed
		},
		"negative-duration": func(r *Record) {
			r.Invocation.Phase = capability.Failed
			negative := time.Duration(-1)
			r.Invocation.Duration = &negative
		},
		"error-text": func(r *Record) { r.Invocation.Phase = capability.Failed; r.Invocation.ErrorCode = "error-secret" },
	} {
		t.Run(name, func(t *testing.T) {
			s := NewMemoryStore()
			r := safeRecord()
			mutate(&r)
			if err := s.Append(context.Background(), r); err == nil || strings.Contains(err.Error(), "secret") {
				t.Fatal(err)
			}
			page, err := s.Query(context.Background(), Query{Scope: Scope{RunID: "r"}})
			if err != nil || len(page.Records) != 0 {
				t.Fatal(page, err)
			}
		})
	}
	for _, pair := range []struct {
		source   capability.Source
		evidence capability.Evidence
	}{{capability.Provider, capability.ProviderProtocol}, {capability.Provider, capability.NativeInputAccepted}, {capability.Host, capability.HostLifecycle}, {capability.Relay, capability.Relayed}} {
		r := safeRecord()
		r.Invocation.Source = pair.source
		r.Invocation.Evidence = pair.evidence
		r.Invocation.ParentScopeID = "parent"
		r.Invocation.ParentToolCallID = "tool"
		if err := NewMemoryStore().Append(context.Background(), r); err != nil {
			t.Fatal(pair, err)
		}
	}
}

func TestObserverProjectionRejectsEnvelopeMismatch(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	recorder, err := New(Config{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	p := recorderProvider{recorder: recorder}
	att, err := p.AttachRun(ctx, "r")
	if err != nil {
		t.Fatal(err)
	}
	r := safeRecord()
	ev := adaptor.WithEventMeta(adaptor.CapabilityInvocation{Invocation: r.Invocation}, adaptor.EventMeta{RunID: "r", Sequence: 1, Time: r.Time, ThreadKey: "thread-secret", Source: &adaptor.EventSourceMeta{RunID: "source-secret"}})
	info := adaptor.RunEventInfo{RunID: "r", ThreadKey: "thread-secret", Identity: adaptor.Identity{Name: "name-secret"}, DriverType: "type-secret"}
	wrong := info
	wrong.RunID = "other"
	if err := att.Observer(ctx, wrong, ev); err == nil {
		t.Fatal("wrong envelope accepted")
	}
	if err := att.Observer(ctx, info, adaptor.WithEventMeta(ev, adaptor.EventMeta{RunID: "wrong", Sequence: 1, Time: r.Time})); err == nil {
		t.Fatal("wrong event run accepted")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := att.Observer(cancelled, info, ev); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := att.Observer(ctx, info, adaptor.Notice{Text: "notice-secret"}); err != nil {
		t.Fatal(err)
	}
	if err := att.Observer(ctx, info, ev); err != nil {
		t.Fatal(err)
	}
	if err := p.DetachRun(ctx, "r"); err != nil {
		t.Fatal(err)
	}
	page, err := recorder.Query(ctx, Query{Scope: r.Scope})
	if err != nil || len(page.Records) != 1 {
		t.Fatal(page, err)
	}
	if page.Records[0].Scope != r.Scope {
		t.Fatal(page)
	}
}

func TestMemoryCancelledWaitCannotCommit(t *testing.T) {
	s := NewMemoryStore().(*memoryStore)
	s.mu.Lock()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Append(ctx, safeRecord()) }()
	cancel()
	s.mu.Unlock()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	page, err := s.Query(context.Background(), Query{Scope: Scope{RunID: "r"}})
	if err != nil || len(page.Records) != 0 {
		t.Fatal(page, err)
	}
}
