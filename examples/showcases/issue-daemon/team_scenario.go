package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"syscall"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"github.com/agent-dance/agent-adaptor/v2/driver"
	"github.com/agent-dance/agent-adaptor/v2/hosttools/team"
)

// Embedding the Codex optional contracts preserves transport, profile and
// session negotiation. Only Run is decorated to audit the actual tools/list
// response with the leader's real capability token, never recording the token.
type auditedCodex struct {
	driver.Driver
	driver.SessionCodecProvider
	driver.SessionConfigFingerprinter
	driver.StreamSupport
	driver.ProcessLifecycleDriver
	driver.MessageCapabilityResolver
	driver.EnvironmentProbe
	driver.ProfileReporter
	driver.SkillSupport
	driver.ConfigSchemaProvider
	control *fakeControl
}

func (d *auditedCodex) Run(ctx context.Context, r driver.Request, s driver.EventSink) (driver.Response, error) {
	probe := &scriptedDriver{control: d.control}
	tools, err := probe.rpc(ctx, r, "tools/list", nil)
	if err != nil {
		return driver.Response{}, fmt.Errorf("audit leader tools/list: %w", err)
	}
	list, _ := tools["tools"].([]any)
	d.control.toolCount.Store(int32(len(list)))
	return d.Driver.Run(ctx, r, s)
}
func (f *fixture) newTeam(s *step) (*team.Team, error) {
	leader := f.newAgent("leader", false, s)
	writer := f.newAgent("writer", false, s)
	checker := f.newAgent("checker", true, s)
	// The audit uses ServeHTTP directly even in live mode; this is the same
	// authenticated handler and capability as the provider's MCP connection.
	t, err := team.New(team.Config{Leader: leader, Members: []team.Member{{Key: "writer", Name: "writer", Description: "Edit files", Runner: writer, Timeout: 2 * time.Minute}, {Key: "checker", Name: "checker", Description: "Read files only", Runner: checker, Timeout: 2 * time.Minute}}, Limits: team.Limits{WakeWindow: time.Nanosecond}})
	s.call("team.New", map[string]any{"leader": "leader", "members": []string{"writer", "checker"}, "member_timeout": "2m", "WakeWindow": "1ns"}, "", err)
	if err == nil {
		f.control.squad = t
	}
	if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
		s.EnvironmentError = err.Error()
	}
	return t, err
}
func closeTeam(s *step, t *team.Team) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := t.Close(ctx)
	s.call("Team.Close", nil, "", err)
	s.equal("Team.Close succeeded", nil, err)
}
func (h *scenario) squad(parent context.Context) {
	s := h.step(14, "event-driven squad", "§7.1–§7.4; §7.8; §9 M2/M4")
	defer s.finish()
	ctx, cancel := h.context(parent)
	defer cancel()
	f, ok := h.fixture(s)
	if !ok {
		return
	}
	defer f.cleanup(s)
	t, err := f.newTeam(s)
	s.equal("team constructed", nil, err)
	if err != nil {
		return
	}
	defer closeTeam(s, t)
	th := t.Thread("ws/issue-2")
	s.call("Team.Thread", th.Key(), "", nil)
	objective := "SQUAD_START Delegate exactly two tasks via team_delegate: writer must create squad.txt containing exactly SQUAD and one newline, checker must read go.mod without editing files. Both workers must run `while [ ! -f .release-worker ]; do sleep 0.1; done` (at most 90 seconds) before finishing. Delegate both now, then end your turn immediately with DELEGATED. Never wait or poll for workers. On host message reply HOST_ACK and end the turn immediately. On worker completions verify outcomes and finally reply SQUAD_DONE. Do not redelegate."
	stream, o := start(s, th, ctx, objective, "squad-1", &h.order)
	defer stream.Cancel()
	active := map[string]string{}
	leaderIdle := false
	o.until(ctx, s, func(e adaptor.Event) bool {
		switch e := e.(type) {
		case adaptor.TurnEnd:
			leaderIdle = true
		case adaptor.SubagentUpdate:
			active[e.Member] = e.State
		}
		return leaderIdle && active["writer"] == "running" && active["checker"] == "running"
	})
	s.equal("both workers running while leader idle", true, leaderIdle && active["writer"] == "running" && active["checker"] == "running")
	// This covers the busy-thread regression previously isolated in a separate
	// test. Never drain twice if a defective library returns the same Stream.
	second := th.Send(ctx, "Independent task; reply SHOULD_NOT_JOIN.", adaptor.MessageID("squad-busy"))
	s.call("Thread.Send", map[string]string{"text": "Independent task; reply SHOULD_NOT_JOIN.", "MessageID": "squad-busy", "key": th.Key()}, second.RunID(), nil)
	if second == stream {
		s.equal("team task does not join active execution", false, true)
	} else {
		so := observe(second.Events(), &h.order)
		so.drain(ctx, s)
		_, se := result(s, second)
		s.equal("team busy task returns ErrThreadBusy", true, errors.Is(se, adaptor.ErrThreadBusy))
	}
	at := time.Now()
	err = send(s, stream, ctx, "Reply exactly HOST_ACK now. Do not wait for workers and do not cancel them.", "squad-host", adaptor.Soon, human)
	s.equal("host input admitted", "none", errorClass(err))
	e := o.until(ctx, s, func(e adaptor.Event) bool {
		if u, ok := e.(adaptor.SubagentUpdate); ok {
			active[u.Member] = u.State
		}
		if te, ok := e.(adaptor.TurnEnd); ok {
			return strings.Contains(text(te.Result), "HOST_ACK")
		}
		return false
	})
	s.expect("host wakes idle leader before workers finish", "HOST_ACK TurnEnd with both workers running", active, e != nil && active["writer"] == "running" && active["checker"] == "running")
	s.expect("host wake bounded", "<30s", time.Since(at).String(), time.Since(at) < 30*time.Second)
	f.release(s, "worker")
	o.drain(ctx, s)
	r, re := result(s, stream)
	s.equal("squad succeeded", "none", errorClass(re))
	delegated := map[string]bool{}
	completed := map[string]bool{}
	lastWorker, completionWake := 0, false
	for i, e := range o.all {
		switch e := e.(type) {
		case adaptor.ToolCall:
			if strings.Contains(e.Name, "team_delegate") {
				if member, ok := e.Args["member"].(string); ok {
					delegated[member] = true
				}
			}
		case adaptor.SubagentUpdate:
			if e.State == "succeeded" {
				completed[e.Member] = true
				lastWorker = i
			}
		case adaptor.TurnEnd:
			if i > lastWorker && len(completed) == 2 {
				completionWake = true
			}
		}
	}
	for _, name := range []string{"writer", "checker"} {
		s.equal(name+" delegated through team_delegate", true, delegated[name])
		s.equal(name+" completed", true, completed[name])
	}
	s.equal("worker completion wakes leader", true, completionWake)
	s.equal("actual leader MCP tools/list size", 4, int(f.control.toolCount.Load()))
	tasks := map[string]string{}
	var leaderUsage adaptor.Usage
	for _, e := range o.all {
		if te, ok := e.(adaptor.TurnEnd); ok && te.Result != nil && te.Result.Usage != nil {
			sumUsage(&leaderUsage, te.Result.Usage)
		}
	}
	total := leaderUsage
	if r != nil {
		for _, sub := range r.Subagents() {
			tasks[sub.Member] = sub.State
			sumUsage(&total, sub.Usage)
		}
	}
	for _, name := range []string{"writer", "checker"} {
		s.equal("Result.Subagents "+name, "succeeded", tasks[name])
	}
	s.equal("exactly two Subagents", 2, len(r.Subagents()))
	if r != nil {
		s.equal("leader + worker Usage aggregate", &total, r.Usage)
	} else {
		s.expect("Usage available", true, false, false)
	}
	checkFile(s, f, "squad.txt", "SQUAD\n")
	checkFile(s, f, "go.mod", "module example.com/issue\n\ngo 1.23\n")
}
func sumUsage(dst *adaptor.Usage, src *adaptor.Usage) {
	if src != nil {
		dst.InputTokens += src.InputTokens
		dst.OutputTokens += src.OutputTokens
		dst.CachedInputTokens += src.CachedInputTokens
		dst.EstimatedCostMilli += src.EstimatedCostMilli
	}
}

func (h *scenario) chat(parent context.Context, teamMode bool) {
	number, name, section := 13, "standby chat", "§4.3; §8; §9 M5"
	if teamMode {
		number, name, section = 15, "team standby", "§7.1; §7.3; §8"
	}
	s := h.step(number, name, section)
	defer s.finish()
	ctx, cancel := h.context(parent)
	defer cancel()
	f, ok := h.fixture(s)
	if !ok {
		return
	}
	defer f.cleanup(s)
	var th adaptor.ThreadRunner = f.thread(s, "chat-1")
	if teamMode {
		t, err := f.newTeam(s)
		s.equal("team constructed", nil, err)
		if err != nil {
			return
		}
		defer closeTeam(s, t)
		th = t.Thread("ws/issue-3")
		s.call("Team.Thread", th.Key(), "", nil)
	}
	serverCtx, serverStop := context.WithCancel(ctx)
	defer serverStop()
	sb, err := th.Standby(serverCtx)
	s.call("Thread.Standby", map[string]string{"key": th.Key(), "context": "serverCtx"}, "", err)
	s.equal("Standby opened", nil, err)
	if err != nil {
		return
	}
	o := observe(sb.Events(), &h.order)
	s.call("Standby.Events", nil, "", nil)
	again, err := th.Standby(ctx)
	s.call("Thread.Standby", th.Key(), "", err)
	s.equal("same Standby handle", true, err == nil && again == sb)
	sendChat := func(body, id string, when adaptor.Timing) {
		e := sb.Send(ctx, body, adaptor.MessageID(id), adaptor.When(when), adaptor.From(human))
		s.call("Standby.Send", map[string]any{"text": body, "MessageID": id, "When": []string{"Soon", "Interrupt", "AfterTurn"}[when], "From": human}, "", e)
		s.equal(id+" admitted", "none", errorClass(e))
	}
	sendChat(gatePrompt("chat1", "Reply CHAT_ONE."), "m1", adaptor.Soon)
	o.until(ctx, s, isTool)
	// When(AfterTurn) still joins the current execution, while forcing a
	// separate response turn whose MessageIDs can be checked deterministically.
	sendChat("Reply CHAT_TWO.", "m2", adaptor.AfterTurn)
	f.release(s, "chat1")
	firstDone, _ := o.until(ctx, s, isDone).(adaptor.Done)
	firstRun := ""
	ids := map[string]int{}
	replies := map[string]string{}
	turns := 0
	for _, e := range o.all {
		if te, ok := e.(adaptor.TurnEnd); ok {
			turns++
			if firstRun == "" {
				firstRun = te.Meta().RunID
			}
			for _, id := range te.MessageIDs {
				ids[id] = turns
				replies[id] = strings.TrimSpace(text(te.Result))
			}
			s.equal("joined chat turns share RunID", firstRun, te.Meta().RunID)
		}
	}
	s.equal("chat execution completed", nil, firstDone.Err)
	for _, reply := range []struct{ id, token string }{{"m1", "CHAT_ONE"}, {"m2", "CHAT_TWO"}} {
		if teamMode {
			// A leader may first report delegation progress, then answer after
			// a worker notification. Keep the actual message turn and final text
			// in the report rather than attributing that final turn to this ID.
			s.expect(reply.id+" reply", reply.token, map[string]string{
				"message_turn": replies[reply.id], "final": text(firstDone.Result),
			}, chatReplyComplete(replies[reply.id], firstDone, reply.token))
		} else {
			s.equal(reply.id+" reply", reply.token, replies[reply.id])
		}
	}
	s.expect("m1 then m2 TurnEnd ordering", "m1 before m2", ids, ids["m1"] > 0 && ids["m2"] > ids["m1"])
	busy := th.Send(ctx, "Task mode must be rejected.", adaptor.MessageID("chat-busy"))
	s.call("Thread.Send", map[string]string{"key": th.Key(), "text": "Task mode must be rejected.", "MessageID": "chat-busy"}, busy.RunID(), nil)
	// A broken team might route this into Standby. Bound it by cancelling it;
	// a correct failed Stream is already complete and Cancel is harmless.
	busy.Cancel()
	s.call("Stream.Cancel", nil, busy.RunID(), nil)
	_, be := result(s, busy)
	s.equal("task Send while standby returns ErrThreadBusy", true, errors.Is(be, adaptor.ErrThreadBusy))
	sendChat(gatePrompt("chatcancel", "Reply SHOULD_CANCEL."), "m-cancel", adaptor.Soon)
	o.until(ctx, s, isTool)
	sb.Cancel()
	s.call("Standby.Cancel", nil, "", nil)
	de := o.until(ctx, s, isDone)
	d, _ := de.(adaptor.Done)
	s.equal("Cancel aborts current processing", true, errors.Is(d.Err, context.Canceled))
	sendChat("Reply exactly CHAT_THREE.", "m3", adaptor.Soon)
	o.until(ctx, s, func(e adaptor.Event) bool {
		d, ok := e.(adaptor.Done)
		return ok && d.Err == nil && strings.Contains(text(d.Result), "CHAT_THREE")
	})
	m3 := false
	for _, e := range o.all {
		if te, ok := e.(adaptor.TurnEnd); ok {
			for _, id := range te.MessageIDs {
				if id == "m3" && te.Meta().RunID != firstRun {
					m3 = true
				}
			}
		}
	}
	s.equal("m3 starts a new execution after Cancel", true, m3)
	if teamMode {
		s.equal("team host messages reached leader", true, m3 && ids["m1"] > 0 && ids["m2"] > 0)
	}
	serverStop()
	s.call("serverCtx.cancel", nil, "", nil)
	o.drain(ctx, s)
	s.equal("server cancellation closes event stream", true, o.closed)
}

// chatReplyComplete accepts an exact reply in the message's own turn or a
// complete line in the successful final answer. Mentioning the token in a
// progress sentence does not satisfy the reply check.
func chatReplyComplete(messageTurn string, done adaptor.Done, token string) bool {
	if done.Err != nil || done.Result == nil {
		return false
	}
	if strings.TrimSpace(messageTurn) == token {
		return true
	}
	for _, line := range strings.Split(done.Result.Text, "\n") {
		if strings.TrimSpace(line) == token {
			return true
		}
	}
	return false
}
