package adaptertest

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/agent-dance/agent-adaptor/v2/driver"
)

// MessageScenario describes a fake-provider script requested by the MSG suite.
// Name is probe, success, interrupt, continuation, write_failure, process_exit,
// cancel, timeout, ambiguous, missing_echo, disabled, limit, batch_limit, or
// overflow. Faults occur after Claim;
// cancel and timeout are triggered by the kit immediately after ownership moves.
// Items contain already framed text: the fake must observe it verbatim.
// A probe only constructs the Driver and resolved Request; it does not run it.
type MessageScenario struct {
	// Name selects the provider script described above.
	Name string
	// Boundary is the delivery boundary the fake must reach.
	Boundary driver.Boundary
	// Items are the immutable framed inputs supplied by the kit.
	Items []driver.MessageItem
}

// MessageFixture supplies a real Driver and resolved invocation using a scripted
// fake provider. Evidence must inspect the fake's protocol observations, never
// derive success from Ack or echo the input scenario as execution evidence.
// The suite installs Request.Messages and invokes Driver.Run itself. Evidence is
// called after each Run, including a checkpoint resume for PersistsDelivered.
// Register cleanup of child processes and fixture resources with t.Cleanup.
type MessageFixture struct {
	// Driver is the implementation under test, configured to use the fake CLI.
	Driver driver.Driver
	// Request selects the resolved transport and resource shape.
	Request driver.Request
	// Evidence reads observations recorded on the provider side of the transport.
	Evidence func() MessageEvidence
}

// MessageEvidence is independently observed fake-provider evidence, cumulative
// across invocations. Received contains exact provider input payloads (a batch
// may contain several framed items). Confirmed contains payloads echoed through
// the official confirmation protocol. Ambiguous contains IDs whose delivery may
// have happened without confirmation; Rejected contains explicitly rejected IDs.
// Retained contains text observed in provider history after checkpoint resume.
// Turns describes the provider turns in the most recent Run, including text,
// observed usage and interruption; Index and CompletedAt are checked on events.
type MessageEvidence struct {
	// Received preserves provider inputs exactly, including framing.
	Received []string
	// Confirmed preserves payloads in official provider confirmation echoes.
	Confirmed []string
	// Ambiguous names attempts whose provider acceptance is unknown.
	Ambiguous []string
	// Rejected names attempts explicitly rejected before provider acceptance.
	Rejected []string
	// Retained preserves guidance found in the provider's resumed history.
	Retained []string
	// Resumed reports an observed provider resume command or reused conversation.
	Resumed bool
	// Turns records the latest invocation's observed provider turn completions.
	Turns []driver.TurnCompleted
}

type namedMessageFixture struct {
	name    string
	factory func(*testing.T, MessageScenario) MessageFixture
}

// WithMessageFixture enables hermetic MSG execution clauses for one resolved
// request shape. Register several fixtures for transport-dependent capabilities.
// The suite chooses scenarios from ResolveMessageCapability (or Descriptor when
// absent), checks every declared boundary and fault, and requires the fixtures
// together to exercise the static declaration. A missing fixture is an explicit
// skip, never evidence that message conformance passed. See MessageScenario and
// NewScriptedMessagePort for the reusable fixture-side implementation.
func WithMessageFixture(name string, factory func(*testing.T, MessageScenario) MessageFixture) Option {
	return func(c *suiteConfig) {
		c.messageFixtures = append(c.messageFixtures, namedMessageFixture{name, factory})
	}
}

// MessageBatch makes items available at one scripted boundary. Batches are
// consumed in order; a Claim at another boundary returns no items. A Stop batch
// can also be claimed at TurnEnd by an in-run Driver: a Stop hook may use one
// combined end-of-turn claim to preserve admission order across delivery timings.
// This lets a fake reach a specific boundary before a queued Soon item is exposed.
type MessageBatch struct {
	// Boundary selects where the batch becomes claimable.
	Boundary driver.Boundary
	// Items are returned in order and transferred exactly once.
	Items []driver.MessageItem
}

// MessageCall records one MessagePort operation in reception order. Operation
// is window, signal, claim, ack or return; only the corresponding fields are populated.
type MessageCall struct {
	// Operation identifies the MessagePort method, or Run's return boundary.
	Operation string
	// Open is the state reported by Window.
	Open bool
	// Boundary is the Claim boundary.
	Boundary driver.Boundary
	// Items are copies of the items returned by Claim.
	Items []driver.MessageItem
	// ID is the acknowledged item identifier.
	ID string
	// Ack is the reported settlement.
	Ack driver.MessageAck
}

// ScriptedMessagePort is a concurrency-safe reference MessagePort for fake CLI
// fixtures. It records all operations, checks claims/acks/windows after Run, and
// signals push drivers when a scripted immediate batch is pending. It does not
// infer provider acceptance. Batches deliberately bypass admission so overflow
// handling can be fault-injected; Admit models core's framed size/timing checks.
// No testing methods or fixture callbacks run while its mutex is held.
type ScriptedMessagePort struct {
	mu         sync.Mutex
	capability driver.MessageCapability
	batches    []MessageBatch
	calls      []MessageCall
	signal     chan struct{}
	finished   bool
	afterClaim func()
}

var _ driver.MessagePort = (*ScriptedMessagePort)(nil)

// NewScriptedMessagePort returns a port with independent copies of the script.
func NewScriptedMessagePort(capability driver.MessageCapability, batches ...MessageBatch) *ScriptedMessagePort {
	capability.Boundaries = append([]driver.Boundary(nil), capability.Boundaries...)
	p := &ScriptedMessagePort{capability: capability, signal: make(chan struct{}, 1)}
	for _, b := range batches {
		p.batches = append(p.batches, MessageBatch{b.Boundary, append([]driver.MessageItem(nil), b.Items...)})
	}
	p.notifyLocked()
	return p
}

func (p *ScriptedMessagePort) notifyLocked() {
	if len(p.batches) > 0 && p.batches[0].Boundary == driver.BoundaryImmediate {
		select {
		case p.signal <- struct{}{}:
		default:
		}
	}
}

// Admit queues a framed item using the declared admission unit and timing.
// It leaves the queue unchanged on rejection. Like core admission, it applies
// the framed size limit even when the caller requests turn-end delivery.
func (p *ScriptedMessagePort) Admit(b driver.Boundary, item driver.MessageItem) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.finished {
		return fmt.Errorf("MSG-04: port used after Run")
	}
	if item.Timing == driver.TimingInterrupt && !p.capability.Interrupt {
		return fmt.Errorf("MSG-02: interrupt unsupported")
	}
	if p.capability.MaxUnits > 0 && messageUnits(item.Text, p.capability.Unit) > p.capability.MaxUnits {
		return fmt.Errorf("MSG-09: framed message exceeds %d %s", p.capability.MaxUnits, p.capability.Unit)
	}
	p.batches = append(p.batches, MessageBatch{b, []driver.MessageItem{item}})
	p.notifyLocked()
	return nil
}

// Window records provider-confirmed window transitions.
func (p *ScriptedMessagePort) Window(open bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, MessageCall{Operation: "window", Open: open})
}

// Signal returns the push notification channel.
func (p *ScriptedMessagePort) Signal() <-chan struct{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, MessageCall{Operation: "signal"})
	return p.signal
}

// Claim transfers the next batch at b, filtering by the SPI timing rules.
func (p *ScriptedMessagePort) Claim(b driver.Boundary) []driver.MessageItem {
	p.mu.Lock()
	var items []driver.MessageItem
	if len(p.batches) > 0 && (p.batches[0].Boundary == b ||
		b == driver.BoundaryTurnEnd && p.batches[0].Boundary == driver.BoundaryStop &&
			p.capability.Continue == driver.ContinueInRun && hasBoundary(p.capability.Boundaries, driver.BoundaryStop)) {
		var remaining []driver.MessageItem
		for _, item := range p.batches[0].Items {
			if item.Timing == driver.TimingAfterTurn && b != driver.BoundaryTurnEnd || item.Timing == driver.TimingInterrupt && b != driver.BoundaryImmediate {
				remaining = append(remaining, item)
			} else {
				items = append(items, item)
			}
		}
		if len(remaining) == 0 {
			p.batches = p.batches[1:]
		} else {
			p.batches[0].Items = remaining
		}
	}
	p.calls = append(p.calls, MessageCall{Operation: "claim", Boundary: b, Items: append([]driver.MessageItem(nil), items...)})
	select {
	case <-p.signal:
	default:
	}
	p.notifyLocked()
	callback := p.afterClaim
	p.mu.Unlock()
	if len(items) > 0 && callback != nil {
		callback()
	}
	return items
}

// Ack records settlement; duplicate and unknown IDs are reported by Verify.
func (p *ScriptedMessagePort) Ack(id string, ack driver.MessageAck) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, MessageCall{Operation: "ack", ID: id, Ack: ack})
}

// Finish marks the Driver.Run return boundary. All claimed items must already
// have been settled; subsequent operations are retained as MSG-04 violations.
func (p *ScriptedMessagePort) Finish() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.finished {
		p.calls = append(p.calls, MessageCall{Operation: "return"})
		p.finished = true
	}
}

// Calls returns a copy of the ordered trace, including copied item slices.
func (p *ScriptedMessagePort) Calls() []MessageCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := append([]MessageCall(nil), p.calls...)
	for i := range out {
		out[i].Items = append([]driver.MessageItem(nil), out[i].Items...)
	}
	return out
}

// Pending returns copies of scripted items not yet claimed.
func (p *ScriptedMessagePort) Pending() []driver.MessageItem {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []driver.MessageItem
	for _, b := range p.batches {
		out = append(out, b.Items...)
	}
	return out
}

func messageUnits(s string, unit driver.MessageUnit) int {
	if unit == driver.MessageUnitUTF16 {
		return len(utf16.Encode([]rune(s)))
	}
	return len(s)
}

// Verify checks the run-scoped ownership, boundary, window and Ack clauses
// against independently observed provider evidence. Call Finish at Run return.
func (p *ScriptedMessagePort) Verify(e MessageEvidence) []Violation {
	return verifyMessageCalls(p.capability, p.Calls(), e)
}

func resolvedMessageCapability(d driver.Driver, req driver.Request) driver.MessageCapability {
	if r, ok := d.(driver.MessageCapabilityResolver); ok {
		return r.ResolveMessageCapability(req)
	}
	return d.Descriptor().Messages
}

func checkMessages(t *testing.T, d driver.Driver, c *suiteConfig) {
	t.Helper()
	t.Run("MSG-01_declaration", func(t *testing.T) {
		reportViolations(t, VerifyMessageCapability(d.Descriptor(), d.Descriptor().Messages))
	})
	if len(c.messageFixtures) == 0 {
		t.Run("MSG_execution", func(t *testing.T) { t.Skip("WithMessageFixture required; MSG execution clauses have not run") })
		return
	}
	seen := driver.MessageCapability{}
	skippedFixture := false
	for _, f := range c.messageFixtures {
		t.Run(f.name, func(t *testing.T) {
			t.Cleanup(func() { skippedFixture = skippedFixture || t.Skipped() })
			if f.factory == nil {
				t.Fatal("MSG-02: nil message fixture factory")
			}
			probe := f.factory(t, MessageScenario{Name: "probe"})
			if nilDynamicValue(probe.Driver) {
				t.Fatal("MSG-02: fixture returned nil Driver")
			}
			if !reflect.DeepEqual(probe.Driver.Descriptor().Messages, d.Descriptor().Messages) {
				t.Fatal("MSG-02: fixture changed the static message declaration")
			}
			probe.Request.Messages = NewScriptedMessagePort(probe.Driver.Descriptor().Messages)
			caps := resolvedMessageCapability(probe.Driver, probe.Request)
			reportViolations(t, VerifyMessageCapability(probe.Driver.Descriptor(), caps))
			if again := resolvedMessageCapability(probe.Driver, probe.Request); !reflect.DeepEqual(caps, again) {
				t.Error("MSG-02: resolver is not deterministic for the same request")
			}
			seen.Boundaries = append(seen.Boundaries, caps.Boundaries...)
			seen.Interrupt = seen.Interrupt || caps.Interrupt
			seen.ConfirmedDelivery = seen.ConfirmedDelivery || caps.ConfirmedDelivery
			seen.PersistsDelivered = seen.PersistsDelivered || caps.PersistsDelivered
			if caps.MaxUnits == d.Descriptor().Messages.MaxUnits && normalizedMessageUnit(caps.Unit) == normalizedMessageUnit(d.Descriptor().Messages.Unit) {
				seen.MaxUnits = caps.MaxUnits
			}
			if caps.Continue == driver.ContinueInRun {
				seen.Continue = caps.Continue
			}
			t.Run("disabled", func(t *testing.T) { runMessageScenario(t, f, caps, MessageScenario{Name: "disabled"}) })
			var boundaries []driver.Boundary
			for _, b := range caps.Boundaries {
				if b != driver.BoundaryTurnEnd {
					boundaries = append(boundaries, b)
				}
			}
			if len(boundaries) == 0 {
				boundaries = append(boundaries, "")
			}
			for _, b := range boundaries {
				for _, name := range []string{"success", "write_failure", "process_exit", "cancel", "timeout"} {
					if b == "" && name != "success" {
						continue
					} // no Driver-owned claims on a resume-only transport
					scenario := MessageScenario{Name: name, Boundary: b, Items: []driver.MessageItem{{ID: "msg-1", Text: "[human msg-1]\nKeep the original goal; include café 😀 in the final answer.\nDo not acknowledge separately."}}}
					t.Run(name+"_"+string(b), func(t *testing.T) { runMessageScenario(t, f, caps, scenario) })
				}
				if b != "" && caps.ConfirmedDelivery {
					t.Run("missing_echo_"+string(b), func(t *testing.T) {
						runMessageScenario(t, f, caps, MessageScenario{Name: "missing_echo", Boundary: b, Items: []driver.MessageItem{{ID: "msg-1", Text: "[human msg-1] verbatim 😀"}}})
					})
				}
				if b == driver.BoundaryImmediate && !caps.ConfirmedDelivery {
					t.Run("ambiguous_"+string(b), func(t *testing.T) {
						runMessageScenario(t, f, caps, MessageScenario{Name: "ambiguous", Boundary: b, Items: []driver.MessageItem{{ID: "msg-1", Text: "[human msg-1] uncertain RPC acceptance"}}})
					})
				}
			}
			if caps.Interrupt {
				t.Run("interrupt", func(t *testing.T) {
					runMessageScenario(t, f, caps, MessageScenario{Name: "interrupt", Boundary: driver.BoundaryImmediate, Items: []driver.MessageItem{{ID: "msg-1", Text: "[human msg-1] interrupt guidance", Timing: driver.TimingInterrupt}}})
				})
			}
			t.Run("continuation", func(t *testing.T) {
				if caps.Continue == driver.ContinueResume && !probe.Driver.Descriptor().Sessions.SupportsResume {
					t.Skip("no continuation capability; pending input belongs to core failure settlement")
				}
				runMessageScenario(t, f, caps, MessageScenario{Name: "continuation", Boundary: driver.BoundaryTurnEnd, Items: []driver.MessageItem{{ID: "msg-1", Text: "[human msg-1] first continuation", Timing: driver.TimingAfterTurn}, {ID: "msg-2", Text: "[human msg-2] second continuation", Timing: driver.TimingAfterTurn}}})
			})
			if caps.MaxUnits > 0 {
				t.Run("MSG-09_size", func(t *testing.T) { checkMessageSize(t, f, caps) })
			}
		})
	}
	t.Run("MSG-02_fixture_coverage", func(t *testing.T) {
		caps := d.Descriptor().Messages
		for _, b := range caps.Boundaries {
			if !hasBoundary(seen.Boundaries, b) {
				if skippedFixture {
					t.Skip("MSG-02: fixture unavailable on this platform; static capability coverage has not passed")
				}
				t.Errorf("MSG-02: no request fixture exercises declared boundary %q", b)
			}
		}
		if caps.MaxUnits > 0 && seen.MaxUnits != caps.MaxUnits {
			if skippedFixture {
				t.Skip("MSG-02: size fixture unavailable")
			}
			t.Error("MSG-02: no request fixture exercises the static size envelope")
		}
		if caps.Interrupt && !seen.Interrupt || caps.Continue == driver.ContinueInRun && seen.Continue != driver.ContinueInRun || caps.ConfirmedDelivery && !seen.ConfirmedDelivery || caps.PersistsDelivered && !seen.PersistsDelivered {
			if skippedFixture {
				t.Skip("MSG-02: fixture unavailable; static capability coverage has not passed")
			}
			t.Error("MSG-02: request fixtures do not cover the static message declaration")
		}
	})
}

// messageContext deterministically expires just after Claim, without relying on
// provider startup time or a sleep. It models both Context error identities.
type messageContext struct {
	context.Context
	done chan struct{}
	mu   sync.Mutex
	err  error
}

func (c *messageContext) Done() <-chan struct{} { return c.done }
func (c *messageContext) Err() error            { c.mu.Lock(); defer c.mu.Unlock(); return c.err }
func (c *messageContext) expire(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err == nil {
		c.err = err
		close(c.done)
	}
}

func runMessageScenario(t *testing.T, f namedMessageFixture, caps driver.MessageCapability, scenario MessageScenario) {
	t.Helper()
	fixture := f.factory(t, scenario)
	if nilDynamicValue(fixture.Driver) || fixture.Evidence == nil {
		t.Fatal("MSG-02: fixture requires Driver and independent Evidence")
	}
	var batches []MessageBatch
	if scenario.Name == "continuation" && caps.Continue == driver.ContinueInRun {
		for _, item := range scenario.Items {
			batches = append(batches, MessageBatch{scenario.Boundary, []driver.MessageItem{item}})
		}
	} else {
		batches = []MessageBatch{{scenario.Boundary, scenario.Items}}
	}
	port := NewScriptedMessagePort(caps, batches...)
	req := fixture.Request
	req.Messages = port
	if scenario.Name == "disabled" {
		req.Messages = nil
		caps = resolvedMessageCapability(fixture.Driver, req)
		reportViolations(t, VerifyMessageCapability(fixture.Driver.Descriptor(), caps))
	}
	if got := resolvedMessageCapability(fixture.Driver, req); !reflect.DeepEqual(got, caps) {
		t.Fatalf("MSG-02: fixture changed the resolved request capability: got %+v, want %+v", got, caps)
	}
	ctx := &messageContext{Context: context.Background(), done: make(chan struct{})}
	t.Cleanup(func() { ctx.expire(context.Canceled) })
	timer := time.AfterFunc(probeTimeout, func() { ctx.expire(context.DeadlineExceeded) })
	defer timer.Stop()
	if scenario.Name == "cancel" {
		port.afterClaim = func() { ctx.expire(context.Canceled) }
	}
	if scenario.Name == "timeout" {
		port.afterClaim = func() { ctx.expire(context.DeadlineExceeded) }
	}
	sink := &messageRecordingSink{NewRecordingSink()}
	resp, err := fixture.Driver.Run(ctx, req, sink)
	port.Finish()
	evidence := fixture.Evidence()
	reportViolations(t, port.Verify(evidence))
	reportViolations(t, VerifyMessageTurns(sink.Events(), evidence.Turns))
	reportViolations(t, VerifyOutcome(&resp, err))
	claims := 0
	var delivered []driver.MessageItem
	for _, call := range port.Calls() {
		if call.Operation == "claim" {
			claims += len(call.Items)
		}
		if call.Operation == "ack" && call.Ack.Delivered {
			for _, item := range scenario.Items {
				if item.ID == call.ID {
					delivered = append(delivered, item)
				}
			}
		}
	}
	success := scenario.Name == "success" || scenario.Name == "interrupt" || scenario.Name == "continuation" || scenario.Name == "limit" || scenario.Name == "disabled"
	if success && (err != nil || resp.Failure != nil || resp.ExitCode != 0 || resp.Signal != "" || resp.TimedOut) {
		t.Fatalf("MSG-02: scripted successful run failed: err=%v response=%+v", err, resp)
	}
	if success {
		if len(evidence.Turns) == 0 {
			t.Error("MSG-07: successful provider script reported no completed turns")
		} else if resp.Output != evidence.Turns[len(evidence.Turns)-1].Text {
			t.Error("MSG-07: Response.Output differs from the final provider turn")
		}
		reportViolations(t, VerifyMessageUsage(evidence.Turns, resp.Usage))
	}
	if scenario.Boundary != "" && !(scenario.Name == "continuation" && caps.Continue == driver.ContinueResume) && claims != len(scenario.Items) {
		t.Errorf("MSG-02: fixture reached %q but Driver claimed %d of %d scripted items", scenario.Boundary, claims, len(scenario.Items))
	}
	if success && scenario.Boundary != "" && !(scenario.Name == "continuation" && caps.Continue == driver.ContinueResume) && len(delivered) != len(scenario.Items) {
		t.Errorf("MSG-05: successful boundary delivered %d of %d items", len(delivered), len(scenario.Items))
	}
	if scenario.Name == "interrupt" {
		interrupted := false
		for _, turn := range evidence.Turns {
			interrupted = interrupted || turn.Interrupted
		}
		if !interrupted || len(evidence.Turns) < 2 {
			t.Error("MSG-02: Interrupt did not interrupt and restart a provider turn")
		}
	}
	if scenario.Name == "continuation" && caps.Continue == driver.ContinueInRun && len(evidence.Turns) != 1+len(scenario.Items) {
		t.Errorf("MSG-08: %d provider turns, want %d in one Run", len(evidence.Turns), 1+len(scenario.Items))
	}
	if success && scenario.Name != "disabled" && fixture.Driver.Descriptor().Sessions.SupportsResume && caps.Continue == driver.ContinueResume && (scenario.Name == "continuation" || scenario.Boundary == "") {
		if claims != 0 || len(port.Pending()) != len(scenario.Items) {
			t.Error("MSG-08: resume-only Driver consumed core-owned continuation items")
		}
		next := resumeMessageFixture(t, fixture, req, resp, scenario.Items, caps)
		if caps.PersistsDelivered {
			resumeMessageFixture(t, fixture, req, next, nil, caps)
			for _, item := range scenario.Items {
				if !containsPayload(fixture.Evidence().Retained, item.Text) {
					t.Errorf("MSG-10: core-resumed guidance %q absent from provider history", item.ID)
				}
			}
		}
	} else if success && caps.PersistsDelivered && len(delivered) > 0 {
		resumeMessageFixture(t, fixture, req, resp, nil, caps)
		retained := fixture.Evidence().Retained
		for _, item := range delivered {
			if !containsPayload(retained, item.Text) {
				t.Errorf("MSG-10: delivered %q absent from observed history after resume", item.ID)
			}
		}
	}
	if scenario.Name == "overflow" {
		for _, c := range port.Calls() {
			if c.Operation == "ack" && c.Ack.Delivered {
				t.Error("MSG-09: oversized mid-turn payload delivered")
			}
		}
	}
}

func resumeMessageFixture(t *testing.T, f MessageFixture, req driver.Request, resp driver.Response, items []driver.MessageItem, caps driver.MessageCapability) driver.Response {
	t.Helper()
	if resp.Checkpoint == nil || !resp.Checkpoint.Valid || resp.Checkpoint.State == nil {
		t.Fatal("MSG-08: core continuation requires a healthy checkpoint")
	}
	req.Session = &driver.SessionContext{Mode: driver.SessionContinueOnly, EngineSessionID: "adaptertest-thread", State: resp.Checkpoint.State}
	req.Prompt = "Report retained guidance."
	if len(items) > 0 {
		texts := make([]string, len(items))
		for i, item := range items {
			texts[i] = item.Text
		}
		req.Prompt = strings.Join(texts, "\n\n")
	}
	port := NewScriptedMessagePort(caps)
	req.Messages = port
	sink := &messageRecordingSink{NewRecordingSink()}
	next, err := f.Driver.Run(probeContext(t), req, sink)
	port.Finish()
	e := f.Evidence()
	if !e.Resumed {
		t.Error("MSG-08: fake provider did not observe checkpoint resume or a reused conversation")
	}
	reportViolations(t, VerifyOutcome(&next, err))
	reportViolations(t, port.Verify(e))
	reportViolations(t, VerifyMessageTurns(sink.Events(), e.Turns))
	reportViolations(t, VerifyMessageUsage(e.Turns, next.Usage))
	if err != nil || next.Failure != nil || next.ExitCode != 0 {
		t.Fatalf("MSG-08: checkpoint resume failed: %v %+v", err, next)
	}
	for _, item := range items {
		if !containsPayload(e.Received, item.Text) {
			t.Errorf("MSG-08: resumed provider did not receive %q verbatim", item.ID)
		}
	}
	return next
}

func checkMessageSize(t *testing.T, f namedMessageFixture, caps driver.MessageCapability) {
	t.Helper()
	// Astral and BMP characters distinguish UTF-8 bytes from UTF-16 units.
	var text string
	if caps.Unit == driver.MessageUnitUTF16 {
		text = strings.Repeat("😀", caps.MaxUnits/2) + strings.Repeat("x", caps.MaxUnits%2)
	} else {
		text = strings.Repeat("😀", caps.MaxUnits/4) + strings.Repeat("x", caps.MaxUnits%4)
	}
	port := NewScriptedMessagePort(caps)
	b := driver.BoundaryTool
	if len(caps.Boundaries) > 0 {
		b = caps.Boundaries[0]
	}
	if err := port.Admit(b, driver.MessageItem{ID: "max", Text: text}); err != nil {
		t.Fatalf("MSG-09: exact limit rejected: %v", err)
	}
	if err := port.Admit(b, driver.MessageItem{ID: "over", Text: text + "x"}); err == nil {
		t.Error("MSG-09: limit+1 admitted")
	}
	if len(caps.Boundaries) == 0 {
		return
	}
	t.Run("limit", func(t *testing.T) {
		runMessageScenario(t, f, caps, MessageScenario{Name: "limit", Boundary: b, Items: []driver.MessageItem{{ID: "msg-1", Text: text}}})
	})
	t.Run("batch_limit", func(t *testing.T) {
		runMessageScenario(t, f, caps, MessageScenario{Name: "batch_limit", Boundary: b, Items: []driver.MessageItem{{ID: "msg-1", Text: text}, {ID: "msg-2", Text: "[human msg-2] overflow guidance"}}})
	})
	t.Run("overflow", func(t *testing.T) {
		runMessageScenario(t, f, caps, MessageScenario{Name: "overflow", Boundary: b, Items: []driver.MessageItem{{ID: "msg-1", Text: text + "x"}}})
	})
}

// Approval policy belongs to core; these hermetic scripts never ask a real
// question. Providing this interface lets fixtures select a control transport.
type messageRecordingSink struct{ *RecordingSink }

func (*messageRecordingSink) RequestDecision(_ context.Context, req driver.DecisionRequest) (driver.DecisionResponse, error) {
	return driver.DecisionResponse{RequestID: req.RequestID, Result: driver.DecisionApproved}, nil
}
