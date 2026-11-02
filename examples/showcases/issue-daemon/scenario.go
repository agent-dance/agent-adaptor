package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"github.com/agent-dance/agent-adaptor/v2/codex"
	"github.com/agent-dance/agent-adaptor/v2/driver"
	"github.com/agent-dance/agent-adaptor/v2/memory"
)

type scenario struct {
	cfg     settings
	report  *report
	order   int
	restart func(context.Context)
}
type fixture struct {
	host    *scenario
	dir     string
	agent   *adaptor.Agent
	store   *memory.Store
	control *fakeControl
	agents  []*adaptor.Agent
	profile string
}

func runScenario(ctx context.Context, cfg settings) *report {
	h := &scenario{cfg: cfg, report: &report{Version: 1, Agent: cfg.agent, Effort: cfg.effort, Started: time.Now().UTC()}}
	h.issue(ctx)
	h.busy(ctx)
	h.interrupt(ctx)
	if h.restart != nil {
		h.restart(ctx)
	}
	h.cancelPending(ctx)
	h.limit(ctx)
	h.chat(ctx, false)
	h.squad(ctx)
	h.chat(ctx, true)
	h.report.Ended = time.Now().UTC()
	h.report.Passed = len(h.report.Steps) == 15
	for _, s := range h.report.Steps {
		h.report.Passed = h.report.Passed && s.Passed
	}
	return h.report
}
func (h *scenario) step(n int, name, section string) *step {
	s := &step{Number: n, Name: name, Section: section, Started: time.Now().UTC(), Calls: []hostCall{}, Timeline: []eventRecord{}, Checks: []check{}}
	h.report.Steps = append(h.report.Steps, s)
	return s
}
func (h *scenario) newFixture(s *step) (*fixture, error) {
	dir, err := os.MkdirTemp("", "issue-daemon-")
	if err != nil {
		return nil, err
	}
	f := &fixture{host: h, dir: dir, store: memory.NewStore(), control: newControl()}
	// Initialize only a new disposable repository through files. No git command
	// changes the user's worktree or repository state.
	for _, d := range []string{".git/objects", ".git/refs/heads"} {
		if err = os.MkdirAll(filepath.Join(dir, d), 0700); err != nil {
			f.cleanup(s)
			return nil, err
		}
	}
	for name, content := range map[string]string{".git/HEAD": "ref: refs/heads/main\n", ".git/config": "[core]\n\trepositoryformatversion = 0\n\tbare = false\n", "go.mod": "module example.com/issue\n\ngo 1.23\n", "issue.go": "package issue\n"} {
		if err = os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			f.cleanup(s)
			return nil, err
		}
	}
	if h.cfg.agent == "codex" {
		f.profile = filepath.Join(dir, ".codex-profile")
		if err = os.MkdirAll(f.profile, 0700); err != nil {
			f.cleanup(s)
			return nil, err
		}
		source := os.Getenv("CODEX_HOME")
		if source == "" {
			home, e := os.UserHomeDir()
			if e != nil {
				f.cleanup(s)
				return nil, e
			}
			source = filepath.Join(home, ".codex")
		}
		for _, role := range []string{"single", "leader", "writer", "checker"} {
			if err = os.MkdirAll(filepath.Join(f.profile, role), 0700); err != nil {
				f.cleanup(s)
				return nil, err
			}
		}
		for _, name := range []string{"auth.json", "config.toml", "config.json"} {
			b, e := os.ReadFile(filepath.Join(source, name))
			if errors.Is(e, os.ErrNotExist) {
				continue
			}
			if e != nil {
				f.cleanup(s)
				return nil, e
			}
			for _, role := range []string{"single", "leader", "writer", "checker"} {
				if e = os.WriteFile(filepath.Join(f.profile, role, name), b, 0600); e != nil {
					f.cleanup(s)
					return nil, e
				}
			}
		}
	}
	f.agent = f.newAgent("single", false, s)
	s.Workspace = dir
	return f, nil
}
func (f *fixture) newAgent(role string, readOnly bool, s *step) *adaptor.Agent {
	var d driver.Driver
	if f.host.cfg.agent == "fake" {
		d = &scriptedDriver{control: f.control, role: role}
	} else {
		d = codex.Driver(codex.Config{CommonConfig: codex.CommonConfig{Env: []driver.EnvBinding{{Name: "CODEX_HOME", Value: filepath.Join(f.profile, role)}}}, ReasoningEffort: "low"})
		if role == "leader" {
			d = &auditedCodex{Driver: d, SessionCodecProvider: d.(driver.SessionCodecProvider), SessionConfigFingerprinter: d.(driver.SessionConfigFingerprinter), StreamSupport: d.(driver.StreamSupport), ProcessLifecycleDriver: d.(driver.ProcessLifecycleDriver), MessageCapabilityResolver: d.(driver.MessageCapabilityResolver), EnvironmentProbe: d.(driver.EnvironmentProbe), ProfileReporter: d.(driver.ProfileReporter), SkillSupport: d.(driver.SkillSupport), ConfigSchemaProvider: d.(driver.ConfigSchemaProvider), control: f.control}
		}
	}
	sandbox := adaptor.WorkspaceWrite
	if readOnly {
		sandbox = adaptor.ReadOnly
	}
	policy := adaptor.Policy{Sandbox: sandbox, Approvals: adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAutoApprove}}
	a := adaptor.New(d, adaptor.WithWorkspace(f.dir), adaptor.WithThreadStore(f.store), adaptor.WithPolicy(policy), adaptor.WithEventBuffer(2048))
	f.agents = append(f.agents, a)
	s.call("adaptor.New", map[string]any{"driver": f.host.cfg.agent, "role": role, "ReasoningEffort": "low", "workspace": f.dir, "thread_store": "shared memory.Store", "sandbox": sandbox, "permission": "auto_approve", "event_buffer": 2048, "CODEX_HOME": filepath.Join(f.profile, role)}, "", nil)
	return a
}
func (f *fixture) cleanup(s *step) {
	for _, a := range f.agents {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := a.Close(ctx)
		cancel()
		s.call("Agent.Close", nil, "", err)
		s.equal("Agent.Close succeeded", "none", errorClass(err))
	}
	if !f.host.cfg.keep {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := removeWorkspace(ctx, f.dir)
		cancel()
		s.call("os.RemoveAll", f.dir, "", err)
		s.equal("workspace cleanup", nil, err)
	}
}
func (f *fixture) release(s *step, gate string) {
	err := os.WriteFile(filepath.Join(f.dir, ".release-"+gate), []byte("release\n"), 0600)
	s.call("os.WriteFile", map[string]string{"path": ".release-" + gate, "text": "release\n"}, "", err)
	s.equal("release tool gate", nil, err)
	f.control.release(gate)
}
func (h *scenario) fixture(s *step) (*fixture, bool) {
	f, err := h.newFixture(s)
	s.equal("temporary git workspace and Agent created", nil, err)
	return f, err == nil
}
func (h *scenario) context(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, h.cfg.timeout)
}
func start(s *step, th adaptor.ThreadRunner, ctx context.Context, prompt, id string, order *int, opts ...adaptor.CallOption) (adaptor.Stream, *observer) {
	options := append([]adaptor.CallOption{adaptor.MessageID(id)}, opts...)
	stream := th.Send(ctx, prompt, options...)
	s.call("Thread.Send", map[string]any{"key": th.Key(), "text": prompt, "MessageID": id, "options": optionDescription(opts)}, stream.RunID(), nil)
	events := stream.Events()
	s.call("Stream.Events", nil, stream.RunID(), nil)
	return stream, observe(events, order)
}
func optionDescription(opts []adaptor.CallOption) []string {
	var names []string
	for _, o := range opts {
		names = append(names, fmt.Sprintf("%T", o))
	}
	return names
}
func send(s *step, stream adaptor.Stream, ctx context.Context, body, id string, when adaptor.Timing, from adaptor.Author) error {
	err := stream.Send(ctx, body, adaptor.MessageID(id), adaptor.When(when), adaptor.From(from))
	s.call("Stream.Send", map[string]any{"text": body, "MessageID": id, "When": []string{"Soon", "Interrupt", "AfterTurn"}[when], "From": from}, stream.RunID(), err)
	return err
}
func result(s *step, stream adaptor.Stream) (*adaptor.Result, error) {
	r, e := stream.Result()
	s.call("Stream.Result", nil, stream.RunID(), e)
	return r, e
}
func checkFile(s *step, f *fixture, name, want string) {
	b, err := os.ReadFile(filepath.Join(f.dir, name))
	s.call("os.ReadFile", name, "", err)
	s.equal(name+" contents", want, string(b))
}
func gatePrompt(gate, action string) string {
	return "GATE:" + gate + " " + action + " Use separate shell calls: first inspect go.mod, then run `i=0; while [ ! -f .release-" + gate + " ] && [ \"$i\" -lt 900 ]; do sleep 0.1; i=$((i+1)); done` (at most 90 seconds). Finish the requested changes after the wait. Do not modify go.mod or .git. Keep replies short."
}

var human = adaptor.Author{Kind: adaptor.AuthorHuman, ID: "human-1", Name: "maintainer"}
var reviewer = adaptor.Author{Kind: adaptor.AuthorAgent, Name: "reviewer"}

const comment = "Write human.txt with exactly HUMAN followed by one newline."

func (h *scenario) issue(parent context.Context) {
	s1 := h.step(1, "M1 task start", "§8; §9 M1")
	ctx, cancel := h.context(parent)
	defer cancel()
	f, ok := h.fixture(s1)
	if !ok {
		s1.finish()
		for i := 2; i <= 7; i++ {
			s := h.step(i, "issue continuation unavailable", "§9 M1")
			s.expect("fixture available", true, false, false)
			s.finish()
		}
		h.restart = func(context.Context) {
			s := h.step(10, "daemon restart unavailable", "§9 M7")
			s.expect("fixture available", true, false, false)
			s.finish()
		}
		return
	}

	th := f.thread(s1, "ws/issue-1/agent-a")
	task := gatePrompt("issue", "Remember REMEMBER_TOKEN_741 in conversation; never put that token on disk. Create base.txt with exactly BASE and one newline. Run several separate shell steps.")
	stream, o := start(s1, th, ctx, task, "task-1", &h.order)
	defer stream.Cancel()
	ev := o.until(ctx, s1, isTool)
	s1.expect("task emits first tool event", "ToolCall", fmt.Sprintf("%T", ev), ev != nil)
	s1.finish()
	s2 := h.step(2, "M1 human comment mid-run", "§5.2; §6.2; §9 M1")
	s2.Workspace = f.dir
	err := send(s2, stream, ctx, comment, "c1", adaptor.Soon, human)
	s2.equal("comment accepted", "none", errorClass(err))
	if err == nil {
		e := o.until(ctx, s2, delivered("c1"))
		m, _ := e.(adaptor.MessageUpdate)
		mech := "codex.turn_steer"
		if h.cfg.agent == "fake" {
			mech = "fake.turn_steer"
		}
		valid := m.Boundary == "immediate" && m.Mechanism == mech || m.Boundary == "turn_end" && m.Mechanism == "core.resume"
		s2.expect("delivery boundary and mechanism", mech+"/immediate or core.resume/turn_end", m, valid)
	}
	s2.finish()
	s3 := h.step(3, "duplicate comment", "§5.2-3")
	s3.Workspace = f.dir
	err = send(s3, stream, ctx, comment, "c1", adaptor.Soon, human)
	s3.equal("duplicate admitted without error", "none", errorClass(err))
	s3.finish()
	s4 := h.step(4, "agent-authored comment", "§4.4; §4.6; §9 M3/M4")
	s4.Workspace = f.dir
	err = send(s4, stream, ctx, "Write peer.txt with exactly PEER and one newline.", "peer-1", adaptor.Soon, reviewer)
	s4.equal("peer comment admitted", "none", errorClass(err))
	if err == nil {
		o.until(ctx, s4, delivered("peer-1"))
	}
	s4.finish()
	s5 := h.step(5, "AfterTurn continuation", "§4.5; §5.1")
	s5.Workspace = f.dir
	err = send(s5, stream, ctx, "Write after.txt with exactly AFTER and one newline. Reply AFTER_DONE.", "after-1", adaptor.AfterTurn, human)
	s5.equal("AfterTurn admitted", "none", errorClass(err))
	f.release(s5, "issue")
	o.drain(ctx, s5)
	r, runErr := result(s5, stream)
	s5.equal("execution succeeded", "none", errorClass(runErr))
	turns := 0
	for _, e := range o.all {
		if _, ok := e.(adaptor.TurnEnd); ok {
			turns++
		}
	}
	s5.expect("at least two TurnEnd events", ">=2", turns, turns >= 2)
	rt := 0
	if r != nil {
		rt = r.Turns
	}
	s5.expect("Result.Turns", ">=2", rt, rt >= 2)
	checkFile(s5, f, "after.txt", "AFTER\n")
	checkFile(s1, f, "base.txt", "BASE\n")
	checkFile(s2, f, "human.txt", "HUMAN\n")
	checkFile(s4, f, "peer.txt", "PEER\n")
	ms := o.outcomes("c1")
	accepted, deliveries := 0, 0
	for _, m := range ms {
		if m.State == adaptor.MessageAccepted {
			accepted++
		}
		if m.State == adaptor.MessageDelivered && m.Mechanism != "core.dedupe" {
			deliveries++
		}
	}
	s2.equal("Accepted precedes Delivered", true, len(ms) >= 2 && ms[0].State == adaptor.MessageAccepted && ms[1].State == adaptor.MessageDelivered)
	s3.equal("one provider delivery for c1", 1, deliveries)
	s3.equal("one admission for repeated c1", 1, accepted)
	peer := false
	if r != nil {
		for _, item := range r.Transcript() {
			if item.Message != nil && item.Message.ID == "peer-1" {
				peer = item.Message.From == reviewer
			}
		}
	}
	s4.equal("TranscriptMessage retains peer authorship", true, peer)
	s1.finish()
	s2.finish()
	s3.finish()
	s4.finish()
	s5.finish()
	s6 := h.step(6, "Done / Result equivalence", "§4.5; §5.6")
	s6.Workspace = f.dir
	again, againErr := result(s6, stream)
	same := o.done != nil && o.done.Result == again && o.done.Err == againErr
	s6.equal("Done and Result pointer/error identity", true, same)
	s6.equal("repeated Result identity", true, again == r && againErr == runErr)
	if o.done != nil && o.done.Result != nil && again != nil {
		s6.equal("Text", o.done.Result.Text, again.Text)
		s6.equal("Turns", o.done.Result.Turns, again.Turns)
		s6.equal("Usage", o.done.Result.Usage, again.Usage)
	}
	s6.finish()
	s7 := h.step(7, "comment after end routes to new task", "§4.2; §8; §9 M1")
	s7.Workspace = f.dir
	body := "CONTEXT_QUERY Without reading files, reply with the exact REMEMBER token from earlier, then a space and the token you wrote in human.txt."
	err = send(s7, stream, ctx, body, "after-end", adaptor.Soon, human)
	s7.equal("post-Done Send returns ErrRunFinished", true, errors.Is(err, adaptor.ErrRunFinished))
	var me *adaptor.MessageError
	retained := errors.As(err, &me) && me.Text == body
	s7.equal("MessageError retains original text", true, retained)
	routed := body
	if me != nil {
		routed = me.Text
	}
	next, no := start(s7, th, ctx, routed, "followup-1", &h.order)
	no.drain(ctx, s7)
	nr, ne := result(s7, next)
	s7.equal("followup succeeds", "none", errorClass(ne))
	s7.equal("conversation continuity", "REMEMBER_TOKEN_741 HUMAN", strings.TrimSpace(text(nr)))
	s7.finish()
	h.restart = func(parent context.Context) {
		s10 := h.step(10, "daemon restart and Thread-level dedupe", "§5.2-3; §5.7; §9 M7")
		s10.Workspace = f.dir
		ctx, cancel := h.context(parent)
		defer cancel()
		defer func() { f.cleanup(s10); s10.finish() }()
		closeCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		err = f.agent.Close(closeCtx)
		stop()
		s10.call("Agent.Close", nil, "", err)
		s10.equal("old daemon Agent closed", nil, err)
		replacement := f.newAgent("single", false, s10)
		thread := replacement.Thread(th.Key())
		s10.call("Agent.Thread", thread.Key(), "", nil)
		// The original delivered ID is replayed as the initial message. This also
		// tests dedupe before any provider inference; a new execution still exists.
		dedup, do := start(s10, thread, ctx, comment, "c1", &h.order)
		do.drain(ctx, s10)
		_, de := result(s10, dedup)
		s10.equal("replayed comment succeeds", "none", errorClass(de))
		deduped := false
		for _, m := range do.outcomes("c1") {
			if m.State == adaptor.MessageDelivered && m.Mechanism == "core.dedupe" {
				deduped = true
			}
		}
		s10.equal("replayed c1 uses core.dedupe", true, deduped)
		resumed, ro := start(s10, thread, ctx, body, "restart-query", &h.order)
		ro.drain(ctx, s10)
		rr, re := result(s10, resumed)
		s10.equal("restart resume succeeds", "none", errorClass(re))
		s10.equal("restart continuity", "REMEMBER_TOKEN_741 HUMAN", strings.TrimSpace(text(rr)))
		s10.finish()
	}
}
func (h *scenario) busy(parent context.Context) {
	s := h.step(8, "task-mode busy Thread", "§4.10; §9 M1")
	defer s.finish()
	ctx, cancel := h.context(parent)
	defer cancel()
	f, ok := h.fixture(s)
	if !ok {
		return
	}
	defer f.cleanup(s)
	th := f.thread(s, "busy")
	first, o := start(s, th, ctx, gatePrompt("busy", "Reply BUSY_DONE."), "busy-1", &h.order)
	defer first.Cancel()
	o.until(ctx, s, isTool)
	second, so := start(s, th, ctx, "Reply SECOND.", "busy-2", &h.order)
	if second == first {
		s.equal("second task has distinct failed Stream", true, false)
	} else {
		so.drain(ctx, s)
		_, err := result(s, second)
		s.equal("second task rejected with ErrThreadBusy", true, errors.Is(err, adaptor.ErrThreadBusy))
	}
	first.Cancel()
	s.call("Stream.Cancel", nil, first.RunID(), nil)
	o.drain(ctx, s)
}
func (h *scenario) interrupt(parent context.Context) {
	s := h.step(9, "interrupt long task", "§4.4; §6.2")
	defer s.finish()
	ctx, cancel := h.context(parent)
	defer cancel()
	f, ok := h.fixture(s)
	if !ok {
		return
	}
	defer f.cleanup(s)
	stream, o := start(s, f.thread(s, "interrupt"), ctx, "GATE:interrupt Run separate shell commands: inspect go.mod, then sleep 120, then sleep 120, then reply TOO_LATE. Obey a later explicit stop instruction immediately.", "interrupt-1", &h.order)
	defer stream.Cancel()
	o.until(ctx, s, isTool)
	at := time.Now()
	err := send(s, stream, ctx, "Stop the original task and reply exactly INTERRUPTED.", "stop-1", adaptor.Interrupt, human)
	s.equal("interrupt admitted", "none", errorClass(err))
	o.drain(ctx, s)
	r, re := result(s, stream)
	s.equal("interrupted execution succeeds", "none", errorClass(re))
	s.expect("interrupt text", "contains INTERRUPTED", text(r), strings.Contains(text(r), "INTERRUPTED"))
	elapsed := time.Since(at)
	s.expect("finishes before two long sleeps", "<60s", elapsed.String(), elapsed < 60*time.Second)
	mechanism := "codex.turn_interrupt"
	if h.cfg.agent == "fake" {
		mechanism = "fake.turn_interrupt"
	}
	found := false
	for _, m := range o.outcomes("stop-1") {
		if m.State == adaptor.MessageDelivered && m.Mechanism == mechanism {
			found = true
		}
	}
	s.equal("interrupt mechanism "+mechanism, true, found)
}
func (h *scenario) cancelPending(parent context.Context) {
	s := h.step(11, "Cancel returns pending original input", "§5.2-7")
	defer s.finish()
	ctx, cancel := h.context(parent)
	defer cancel()
	f, ok := h.fixture(s)
	if !ok {
		return
	}
	defer f.cleanup(s)
	stream, o := start(s, f.thread(s, "cancel"), ctx, gatePrompt("cancel", "Reply CANCEL_DONE."), "cancel-1", &h.order)
	defer stream.Cancel()
	o.until(ctx, s, isTool)
	body := "Pending cancellation comment; reply NEVER_DELIVERED."
	err := send(s, stream, ctx, body, "pending-1", adaptor.AfterTurn, human)
	s.equal("pending admitted", "none", errorClass(err))
	stream.Cancel()
	s.call("Stream.Cancel", nil, stream.RunID(), nil)
	o.drain(ctx, s)
	_, err = result(s, stream)
	s.equal("cancelled Result", true, errors.Is(err, context.Canceled))
	s.equal("cancelled Done", true, o.done != nil && errors.Is(o.done.Err, context.Canceled))
	found := false
	for _, m := range o.outcomes("pending-1") {
		if m.State == adaptor.MessageFailed && m.Reason == adaptor.MessageCancelled && m.Text == body {
			found = true
		}
	}
	s.equal("Failed/cancelled retains original text", true, found)
}
func (h *scenario) limit(parent context.Context) {
	s := h.step(12, "turn limit", "§5.4; §9 M8")
	defer s.finish()
	ctx, cancel := h.context(parent)
	defer cancel()
	f, ok := h.fixture(s)
	if !ok {
		return
	}
	defer f.cleanup(s)
	policy := adaptor.Policy{Sandbox: adaptor.WorkspaceWrite, Approvals: adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAutoApprove}, Messages: adaptor.MessagePolicy{MaxTurns: 2}}
	s.call("WithPolicy", map[string]int{"Messages.MaxTurns": 2}, "", nil)
	stream, o := start(s, f.thread(s, "limit"), ctx, gatePrompt("limit1", "Reply LIMIT_ONE."), "limit-0", &h.order, adaptor.WithPolicy(policy))
	defer stream.Cancel()
	o.until(ctx, s, isTool)
	err := send(s, stream, ctx, gatePrompt("limit2", "Reply LIMIT_TWO."), "limit-1", adaptor.AfterTurn, human)
	s.equal("first AfterTurn admitted", "none", errorClass(err))
	f.release(s, "limit1")
	// Wait for the continuation's tool call before queuing messages two and
	// three. Sending all three in the first turn legitimately batches them.
	o.until(ctx, s, turnEnd)
	o.until(ctx, s, isTool)
	for _, id := range []string{"limit-2", "limit-3"} {
		err = send(s, stream, ctx, "Reply "+id+".", id, adaptor.AfterTurn, human)
		s.equal(id+" admitted", "none", errorClass(err))
	}
	f.release(s, "limit2")
	o.drain(ctx, s)
	r, _ := result(s, stream)
	noticed := false
	for _, e := range o.all {
		if n, ok := e.(adaptor.Notice); ok && n.Kind == "limit.approaching" {
			noticed = true
		}
	}
	s.equal("limit.approaching notice", true, noticed)
	got := map[string]string{}
	originals := map[string]string{}
	if r != nil {
		for _, m := range r.Undelivered() {
			got[m.ID] = string(m.Reason)
			originals[m.ID] = m.Text
		}
	}
	for _, id := range []string{"limit-2", "limit-3"} {
		s.equal(id+" Undelivered reason", "limit_exceeded", got[id])
		s.equal(id+" Undelivered text", "Reply "+id+".", originals[id])
	}
	s.equal("exactly two turns", true, r != nil && r.Turns == 2)
}

func (f *fixture) thread(s *step, key string) *adaptor.Thread {
	th := f.agent.Thread(key)
	s.call("Agent.Thread", key, "", nil)
	return th
}
