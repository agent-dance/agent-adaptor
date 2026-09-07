// Package alignmentpolicy provides independent scripted inputs for T22 QA.
// It deliberately has no dependency on adaptor or provider classification helpers.
package alignmentpolicy

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/activebudget"
	"github.com/agent-dance/agent-adaptor/memory"
	"github.com/agent-dance/agent-adaptor/threadstore"
)

type Clock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*Timer
	Stops  chan struct{}
	OnStop func()
}
type Timer struct {
	c      *Clock
	at     time.Time
	fn     func()
	active bool
}

func NewClock() *Clock {
	return &Clock{now: time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC), Stops: make(chan struct{}, 100)}
}
func (c *Clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *Clock) AfterFunc(d time.Duration, fn func()) activebudget.Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	tm := &Timer{c: c, at: c.now.Add(d), fn: fn, active: true}
	c.timers = append(c.timers, tm)
	return tm
}
func (t *Timer) Stop() bool {
	c := t.c
	c.mu.Lock()
	was := t.active
	t.active = false
	hook := c.OnStop
	c.OnStop = nil
	c.mu.Unlock()
	select {
	case c.Stops <- struct{}{}:
	default:
	}
	if hook != nil {
		hook()
	}
	return was
}
func (c *Clock) Elapse(d time.Duration) {
	if d < 0 {
		panic("negative clock step")
	}
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}
func (c *Clock) FireDue() {
	for {
		c.mu.Lock()
		var due *Timer
		for _, t := range c.timers {
			if t.active && !t.at.After(c.now) {
				due = t
				t.active = false
				break
			}
		}
		c.mu.Unlock()
		if due == nil {
			return
		}
		due.fn()
	}
}
func (c *Clock) Advance(d time.Duration) { c.Elapse(d); c.FireDue() }
func (c *Clock) FireAllStale() {
	c.mu.Lock()
	ts := append([]*Timer(nil), c.timers...)
	c.mu.Unlock()
	for _, tm := range ts {
		tm.fn()
	}
}
func (c *Clock) SetStopHook(f func()) { c.mu.Lock(); c.OnStop = f; c.mu.Unlock() }

type Driver struct {
	Desc        driver.Descriptor
	Rich        bool
	Calls       atomic.Int32
	RunFunc     func(context.Context, driver.Request, driver.EventSink) (driver.Response, error)
	ConfigError error
}

func NewDriver() *Driver {
	return &Driver{Rich: true, Desc: driver.Descriptor{Type: "t22-script", SystemPrompt: driver.SystemPromptCapability{Append: true}, Sessions: driver.SessionCapability{SupportsResume: true}, RunPolicyCaps: driver.RunPolicyCapabilities{Isolation: true, WebSearch: true, Browser: true, Permission: driver.HumanDecisionSupport{Ask: true, AutoApprove: true, AutoReject: true, Retry: true}, PlanReview: driver.HumanDecisionSupport{Ask: true, AutoApprove: true, AutoReject: true, Retry: true}, Question: driver.QuestionSupport{Ask: true, AutoReject: true, Retry: true}}, StructuredOutput: driver.StructuredOutputCapability{JSONSchemaNative: true, JSONSchemaPromptValidate: true, WorksWithRun: true, WorksWithStreaming: true, WorksWithHITL: true}}}
}
func (d *Driver) Descriptor() driver.Descriptor { return d.Desc }
func (d *Driver) ValidateConfig(any) error      { return d.ConfigError }
func (d *Driver) StreamCapability() driver.StreamCapability {
	return driver.StreamCapability{Native: d.Rich, TokenLevel: d.Rich, HITL: d.Rich}
}
func (d *Driver) SessionConfigFingerprint() (string, error) { return "t22-fixed-config-v1", nil }
func (d *Driver) SessionCodec() driver.SessionCodec         { return Codec{} }
func (d *Driver) Run(ctx context.Context, r driver.Request, s driver.EventSink) (driver.Response, error) {
	d.Calls.Add(1)
	if d.RunFunc != nil {
		return d.RunFunc(ctx, r, s)
	}
	return Response(), nil
}

type Codec struct{}

func (Codec) Name() string { return "t22-script/v1" }
func (Codec) ToParams(s *driver.SessionState) driver.SessionParams {
	if s == nil {
		return driver.SessionParams{}
	}
	return driver.SessionParams{ResumeID: s.ResumeID, DisplayID: s.DisplayID, Values: s.Data}
}
func (Codec) FromParams(p driver.SessionParams) *driver.SessionState {
	if p.ResumeID == "" && p.DisplayID == "" && len(p.Values) == 0 {
		return nil
	}
	return &driver.SessionState{ResumeID: p.ResumeID, DisplayID: p.DisplayID, Data: p.Values}
}
func (Codec) GuardFingerprint(driver.SessionParams) string { return "t22-checkpoint-encoding-v1" }
func Response() driver.Response {
	return driver.Response{Output: "audited answer", Summary: "brief", Provider: "script", Model: "known", Metadata: map[string]string{"audit": "marker"}, RawStreams: &driver.RawStreams{Stdout: "formal stdout\n", Stderr: "formal stderr\n", Terminal: &driver.TerminalPayload{Event: "completed", JSON: []byte(`{"completed":true}`)}}, Transcript: []driver.TranscriptItem{{Kind: driver.TranscriptAssistant, Text: "audited answer"}}, Usage: &driver.Usage{InputTokens: 0, OutputTokens: 3}, Checkpoint: &driver.Checkpoint{Valid: true, State: &driver.SessionState{ResumeID: "healthy-t22"}}}
}

type Store struct {
	threadstore.Store
	Finalizes      atomic.Int32
	Renews         atomic.Int32
	Releases       atomic.Int32
	BeforeFinalize func(context.Context, threadstore.FinalizeRequest) error
	AfterFinalize  func(context.Context) error
	OnRenew        func(context.Context) error
	ReleaseError   error
}

func NewStore() *Store { return &Store{Store: memory.NewStore()} }
func (s *Store) Finalize(ctx context.Context, r threadstore.FinalizeRequest) error {
	s.Finalizes.Add(1)
	if s.BeforeFinalize != nil {
		if err := s.BeforeFinalize(ctx, r); err != nil {
			return err
		}
	}
	if err := s.Store.Finalize(ctx, r); err != nil {
		return err
	}
	if s.AfterFinalize != nil {
		return s.AfterFinalize(ctx)
	}
	return nil
}
func (s *Store) RenewLease(ctx context.Context, l threadstore.Lease, ttl time.Duration) error {
	s.Renews.Add(1)
	if s.OnRenew != nil {
		if err := s.OnRenew(ctx); err != nil {
			return err
		}
	}
	return s.Store.RenewLease(ctx, l, ttl)
}
func (s *Store) ReleaseLease(ctx context.Context, l threadstore.Lease) error {
	s.Releases.Add(1)
	return errors.Join(s.Store.ReleaseLease(ctx, l), s.ReleaseError)
}
