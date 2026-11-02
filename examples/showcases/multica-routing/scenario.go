package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"github.com/agent-dance/agent-adaptor/v2/codex"
	"github.com/agent-dance/agent-adaptor/v2/hosttools/team"
	"github.com/agent-dance/agent-adaptor/v2/memory"
	"github.com/agent-dance/agent-adaptor/v2/profile"
)

type check struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail,omitempty"`
}
type report struct {
	Version     int           `json:"version"`
	Mode        string        `json:"mode"`
	Model       string        `json:"model"`
	Effort      string        `json:"effort"`
	Source      string        `json:"multicaSource"`
	Started     time.Time     `json:"started"`
	Ended       time.Time     `json:"ended"`
	Passed      bool          `json:"passed"`
	Checks      []check       `json:"checks"`
	Runs        []*runRecord  `json:"runs"`
	Gates       []gateArrival `json:"realWorkerGates"`
	HostInputs  []hostMessage `json:"hostInputs"`
	HostActions []hostAction  `json:"hostActions"`
	CloseErrors []string      `json:"closeErrors,omitempty"`
	Workspace   string        `json:"retainedWorkspace,omitempty"`
}
type hostAction struct {
	Action  string      `json:"action"`
	Message hostMessage `json:"message"`
	At      time.Time   `json:"at"`
	Error   string      `json:"error,omitempty"`
}
type eventRecord struct {
	Type    string            `json:"type"`
	Meta    adaptor.EventMeta `json:"meta"`
	Payload any               `json:"payload"`
}
type runRecord struct {
	Name              string              `json:"name"`
	Scope             string              `json:"scope"`
	Input             hostMessage         `json:"input"`
	RunID             string              `json:"runID"`
	Started           time.Time           `json:"started"`
	Ended             time.Time           `json:"ended"`
	ActualDone        bool                `json:"actualDone"`
	DoneMatchesResult bool                `json:"doneMatchesResult"`
	Error             string              `json:"error,omitempty"`
	AuditPath         string              `json:"auditPath"`
	Tasks             []team.Task         `json:"tasks"`
	Messages          []team.InboxMessage `json:"typedMessages"`
	Artifacts         []team.Artifact     `json:"teamArtifacts"`
	Events            []eventRecord       `json:"events"`
	result            *adaptor.Result
	err               error
}
type observed struct {
	stream    adaptor.Stream
	record    *runRecord
	completed *runRecord
	done      chan struct{}
	forwarded chan struct{}
}
type fixture struct {
	cfg        settings
	report     *report
	root       string
	workspaces map[string]string
	agents     []*adaptor.Agent
	team       *team.Team
	store      *team.MemoryStore
	gates      *gates
	secrets    []string
}

var outcomeSchema = map[string]any{"type": "object", "properties": map[string]any{"status": map[string]any{"type": "string", "enum": []string{"done"}}, "detail": map[string]any{"type": "string"}}, "required": []string{"status", "detail"}, "additionalProperties": false}

const commentMarker = "HUMAN_COMMENT_7c83"

func runScenario(ctx context.Context, cfg settings) (*report, []string) {
	r := &report{Version: 1, Mode: "real-codex-host-routing", Model: cfg.model, Effort: cfg.effort, Source: "https://github.com/multica-ai/multica/tree/0d627e54e1bd4d2521359f293d04521246495ce2", Started: time.Now().UTC()}
	f := &fixture{cfg: cfg, report: r, store: team.NewMemoryStore(), gates: newGates(), workspaces: map[string]string{}, secrets: auditSecrets(cfg.home)}
	defer func() {
		f.close()
		r.Ended = time.Now().UTC()
		r.Passed = len(r.Checks) > 0
		for _, c := range r.Checks {
			r.Passed = r.Passed && c.Passed
		}
		r.Passed = r.Passed && len(r.CloseErrors) == 0
	}()
	if err := f.setup(); err != nil {
		f.check("setup", false, err.Error())
		return r, f.secrets
	}
	f.mentionsAndNextRun(ctx)
	if ctx.Err() == nil {
		f.stopAndPriority(ctx)
	} else {
		f.check("stop-and-priority executed", false, ctx.Err().Error())
	}
	return r, f.secrets
}

func (f *fixture) check(name string, passed bool, detail string) {
	f.report.Checks = append(f.report.Checks, check{name, passed, detail})
}
func (f *fixture) action(action string, msg hostMessage, err error) {
	f.report.HostActions = append(f.report.HostActions, hostAction{action, msg, time.Now().UTC(), errorText(err)})
}
func (f *fixture) setup() error {
	var err error
	f.root, err = os.MkdirTemp("", "multica-routing-")
	if err != nil {
		return err
	}
	for _, role := range []string{"leader", "alpha", "beta"} {
		workspace := filepath.Join(f.root, "workspaces", role)
		f.workspaces[role] = workspace
		for _, dir := range []string{".git/objects", ".git/refs/heads"} {
			if err = os.MkdirAll(filepath.Join(workspace, dir), 0700); err != nil {
				return err
			}
		}
		for name, value := range map[string]string{".git/HEAD": "ref: refs/heads/main\n", ".git/config": "[core]\n\trepositoryformatversion = 0\n\tbare = false\n", "README.md": "Disposable host routing acceptance workspace.\n"} {
			if err = os.WriteFile(filepath.Join(workspace, name), []byte(value), 0600); err != nil {
				return err
			}
		}
		policy := adaptor.Policy{Sandbox: adaptor.WorkspaceWrite, WebSearch: adaptor.FeatureDeny, Approvals: adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAutoApprove}, Messages: adaptor.MessagePolicy{MaxTurns: 12}}
		if role == "leader" {
			policy.Sandbox = adaptor.ReadOnly
		}
		opts := []adaptor.Option{adaptor.WithWorkspace(workspace), adaptor.WithThreadStore(memory.NewStore()), adaptor.WithIdentity(adaptor.Identity{ID: role, Name: role}), adaptor.WithPolicy(policy), adaptor.WithProfile(profile.CloneFrom(f.cfg.home, filepath.Join(f.root, "profiles", role), profile.CopySettings(), profile.LinkAuth()))}
		if role != "leader" {
			opts = append(opts, adaptor.WithTools(f.gates.toolFor(role)))
		}
		agent := adaptor.New(codex.Driver(codex.Config{CommonConfig: codex.CommonConfig{Command: f.cfg.command}, Model: f.cfg.model, ReasoningEffort: codex.ReasoningEffort(f.cfg.effort)}), opts...)
		f.agents = append(f.agents, agent)
	}
	f.team, err = team.New(team.Config{Leader: f.agents[0], Store: f.store, Members: []team.Member{{Key: "alpha", Name: "Alpha", Runner: f.agents[1], Description: "Exact key alpha; writes only alpha workspace", Output: outcomeSchema, Timeout: f.cfg.timeout}, {Key: "beta", Name: "Beta", Runner: f.agents[2], Description: "Exact key beta; writes only beta workspace", Output: outcomeSchema, Timeout: f.cfg.timeout}}, Limits: team.Limits{MaxTasks: 3, MaxLeaderTurns: 10, WakeWindow: 100 * time.Millisecond}})
	return err
}

func (f *fixture) close() {
	f.gates.releaseAll()
	closeOne := func(name string, close func(context.Context) error) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := close(ctx); err != nil {
			f.report.CloseErrors = append(f.report.CloseErrors, name+": "+err.Error())
		}
	}
	if f.team != nil {
		closeOne("team", f.team.Close)
	}
	for i, agent := range f.agents {
		closeOne(fmt.Sprintf("agent-%d", i), agent.Close)
	}
	if f.cfg.keep {
		f.report.Workspace = f.root
	} else if f.root != "" {
		if err := os.RemoveAll(f.root); err != nil {
			f.report.CloseErrors = append(f.report.CloseErrors, "temporary workspace cleanup: "+err.Error())
		}
	}
}

func human(thread, id, text, target string) hostMessage {
	return hostMessage{Thread: thread, ID: id, Text: text, Target: target, Kind: "comment", Author: adaptor.Author{Kind: adaptor.AuthorHuman, ID: "operator-17", Name: "Acceptance operator"}}
}
func framed(msg hostMessage) string {
	b, _ := json.Marshal(msg)
	return "Structured host comment (target is an exact roster key; route human input through leader only):\n" + string(b)
}

func (f *fixture) start(ctx context.Context, name, scope string, msg hostMessage) *observed {
	r := &runRecord{Name: name, Scope: scope, Input: msg, Started: time.Now().UTC()}
	f.report.HostInputs = append(f.report.HostInputs, msg)
	rawSchema, _ := json.Marshal(outcomeSchema)
	s := f.team.Thread(scope).Send(ctx, framed(msg), adaptor.From(msg.Author), adaptor.MessageID(msg.ID), adaptor.WithSchemaJSON(rawSchema))
	r.RunID = s.RunID()
	f.report.Runs = append(f.report.Runs, r)
	return observeStream(s, r)
}

// The collector owns a detached record. A timed-out collector cannot mutate
// the report while the host serializes its bounded-cleanup failure.
func observeStream(s adaptor.Stream, record *runRecord) *observed {
	copy := *record
	r := &copy
	o := &observed{stream: s, record: record, done: make(chan struct{}), forwarded: make(chan struct{})}
	events := s.Events()
	go func() {
		defer close(o.done)
		forwardCalls := map[string]bool{}
		forwarded := false
		var terminal adaptor.Done
		for ev := range events {
			payload := any(ev)
			switch e := ev.(type) {
			case adaptor.Done:
				r.ActualDone = true
				terminal = e
				payload = map[string]any{"error": errorText(e.Err), "resultRunID": resultID(e.Result)}
			case adaptor.ToolCall:
				if strings.HasSuffix(e.Name, "team_send") && strings.Contains(fmt.Sprint(e.Args["text"]), commentMarker) {
					forwardCalls[e.ID] = true
				}
			case adaptor.ToolResult:
				if forwardCalls[e.ID] && !forwarded {
					close(o.forwarded)
					forwarded = true
				}
			}
			r.Events = append(r.Events, eventRecord{fmt.Sprintf("%T", ev), ev.Meta(), payload})
		}
		r.result, r.err = s.Result()
		r.DoneMatchesResult = r.ActualDone && terminal.Result == r.result && terminal.Err == r.err
		if r.result == nil {
			var runErr *adaptor.RunError
			if errors.As(r.err, &runErr) {
				r.result = runErr.Result
			}
		}
		r.Error = errorText(r.err)
		r.Ended = time.Now().UTC()
		o.completed = r
	}()
	return o
}
func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
func resultID(r *adaptor.Result) string {
	if r == nil {
		return ""
	}
	return r.RunID
}

func (f *fixture) finish(ctx context.Context, o *observed, wantCancelled bool) {
	select {
	case <-o.done:
	case <-ctx.Done():
		o.stream.Cancel()
		select {
		case <-o.done:
		case <-time.After(20 * time.Second):
			f.check(o.record.Name+" bounded terminal", false, "Done timed out after Cancel")
			return
		}
	}
	*o.record = *o.completed
	r := o.record
	f.check(r.Name+" authoritative Done", r.ActualDone && r.DoneMatchesResult, "Done and Stream.Result must agree")
	if wantCancelled {
		f.check(r.Name+" cancellation retained", errors.Is(r.err, context.Canceled) && r.result != nil, r.Error)
	} else {
		f.check(r.Name+" execution succeeded", r.err == nil, r.Error)
		var decoded any
		decodeErr := errors.New("missing Result")
		if r.result != nil {
			decodeErr = r.result.Decode(&decoded)
		}
		f.check(r.Name+" schema decoded", decodeErr == nil, errorText(decodeErr))
	}
	auditCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var err error
	r.Tasks, err = f.store.Tasks(auditCtx, r.Scope)
	f.check(r.Name+" TaskStore.Tasks", err == nil, errorText(err))
	r.Messages, err = f.store.Messages(auditCtx, r.Scope)
	f.check(r.Name+" inbox audit", err == nil, errorText(err))
	r.Artifacts, err = f.store.Artifacts(auditCtx, r.Scope)
	f.check(r.Name+" artifact audit", err == nil, errorText(err))
	var workers []map[string]any
	for _, task := range r.Tasks {
		workers = append(workers, map[string]any{"task": task, "result": resultAudit(task.Result, nil)})
		f.check(r.Name+" worker "+task.Member+" Result retained", task.Result != nil, string(task.State))
		if !wantCancelled {
			f.check(r.Name+" worker "+task.Member+" succeeded", task.State == team.Succeeded, "task="+task.ID+" state="+string(task.State)+" reason="+task.Reason)
		}
		if wantCancelled && task.Result != nil {
			f.check(r.Name+" worker "+task.Member+" cancellation audit retained", task.Result.Raw().Stdout != "" && len(task.Result.Transcript()) > 0, "the observed worker reached its gate; cancellation may omit a terminal payload and usage")
		}
		if task.State == team.Succeeded && task.Result != nil {
			var decoded any
			decodeErr := task.Result.Decode(&decoded)
			raw := task.Result.Raw()
			f.check(r.Name+" worker "+task.Member+" complete audit", decodeErr == nil && raw.Stdout != "" && raw.Terminal != nil && len(task.Result.Transcript()) > 0 && task.Result.Usage != nil, errorText(decodeErr))
		}
	}
	r.AuditPath = filepath.Join(f.cfg.output, r.Name+".json")
	err = writeJSON(r.AuditPath, map[string]any{"run": r, "teamResult": resultAudit(r.result, r.err), "workerResults": workers, "workspaceArtifacts": f.readArtifacts()}, f.secrets)
	f.check(r.Name+" full audit saved", err == nil, errorText(err))
}

func (f *fixture) readArtifacts() []map[string]any {
	var artifacts []map[string]any
	for _, role := range []string{"alpha", "beta"} {
		for _, name := range []string{"base.txt", "comment.txt", "cancel-start.txt", "restart.txt"} {
			b, err := os.ReadFile(filepath.Join(f.workspaces[role], name))
			if os.IsNotExist(err) {
				continue
			}
			entry := map[string]any{"role": role, "path": name, "error": errorText(err)}
			if err == nil {
				sum := sha256.Sum256(b)
				entry["text"] = string(b)
				entry["sha256"] = hex.EncodeToString(sum[:])
			}
			artifacts = append(artifacts, entry)
		}
	}
	return artifacts
}
func (f *fixture) fileEquals(role, name, want string) bool {
	b, err := os.ReadFile(filepath.Join(f.workspaces[role], name))
	return err == nil && string(b) == want
}

func (f *fixture) mentionsAndNextRun(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, f.cfg.timeout)
	defer cancel()
	scope := encodeThreadKey("workspace:λ", "issue/17")
	q := newHostQueue([]string{"alpha", "beta"})
	initial := human(scope, "same-id-across-threads", `Delegate exactly ONE task to alpha (never beta). Alpha must write base.txt with exact bytes BASE, then call host_gate with gate="comment-running" and wait for its response. A human comment with additional instructions will arrive via leader while the tool is blocked. After release, apply that pending leader message to comment.txt; its exact content is not known yet. Do not infer a comment before receiving it. Then return {"status":"done","detail":"alpha artifacts written"}. Leader: after delegation immediately end your turn so host comments can wake you. On a human comment targeting alpha, use team_send with the EXISTING alpha task handle and the exact requested comment, when="soon"; never delegate a second task. On task completion finish with the required JSON outcome. Do not poll, sleep, or write files as leader.`, "alpha")
	o := f.start(ctx, "mention-running-comment", scope, initial)
	arrival, err := f.gates.wait(ctx, "alpha", "comment-running")
	f.check("actual alpha worker gate before comment", err == nil, errorText(err))
	if err == nil {
		f.report.Gates = append(f.report.Gates, arrival)
	}
	// These are held only by the host until authoritative Done, not SDK AfterTurn.
	for _, id := range []string{"after-1", "after-2"} {
		msg := human(scope, id, "Do not delegate. Reply with the required JSON outcome; detail must acknowledge original ID "+id+" and text ORDER_"+id, "leader")
		ok, e := q.Admit(msg)
		f.action("queue-until-Done", msg, e)
		f.check("queue "+id, ok && e == nil, errorText(e))
	}
	unknown := human(scope, "unknown-target", "Do not deliver this", "not-in-roster")
	_, unknownErr := q.Admit(unknown)
	f.action("reject-unknown-target", unknown, unknownErr)
	f.check("unknown target rejected before SDK send", unknownErr != nil, "exact roster validation")
	comment := human(scope, "comment-dedupe-1", "Forward this exact instruction to the EXISTING alpha task via team_send, soon: write comment.txt with exact bytes "+commentMarker+" after host_gate releases. No other delegation.", "alpha")
	if err == nil {
		f.report.HostInputs = append(f.report.HostInputs, comment, comment)
		for i := 0; i < 2; i++ {
			sendErr := o.stream.Send(ctx, framed(comment), adaptor.From(comment.Author), adaptor.MessageID(comment.ID), adaptor.When(adaptor.Soon))
			f.action("send-runtime-comment", comment, sendErr)
			f.check(fmt.Sprintf("same-ID runtime comment admission %d", i+1), sendErr == nil, errorText(sendErr))
		}
		select {
		case <-o.forwarded:
			f.check("leader forwarded comment before gate release", true, "")
		case <-o.done:
			f.check("leader forwarded comment before gate release", false, "team ended before forwarding")
		case <-ctx.Done():
			f.check("leader forwarded comment before gate release", false, ctx.Err().Error())
		}
	}
	f.gates.release("alpha", "comment-running")
	f.finish(ctx, o, false)
	f.check("one exact alpha delegation", len(o.record.Tasks) == 1 && o.record.Tasks[0].Member == "alpha" && o.record.Tasks[0].State == team.Succeeded, "TaskStore, not leader prose")
	f.check("worker wrote both observed artifacts", f.fileEquals("alpha", "base.txt", "BASE") && f.fileEquals("alpha", "comment.txt", commentMarker), "exact bytes from alpha workspace")
	count := 0
	for _, message := range o.record.Messages {
		if message.ID == comment.ID {
			count++
			f.check("comment receipt author and original text", message.Host && message.From == comment.Author && message.Text == framed(comment) && message.State == team.Delivered, "durable original human receipt")
		}
	}
	f.check("same-thread duplicate has one durable receipt", count == 1, fmt.Sprintf("receipts=%d", count))
	peerDelivered := false
	for _, task := range o.record.Tasks {
		if task.Result == nil {
			continue
		}
		for _, message := range task.Result.Messages() {
			if strings.Contains(message.Text, commentMarker) && message.From.Kind == adaptor.AuthorAgent && message.State == adaptor.MessageDelivered {
				peerDelivered = true
			}
		}
	}
	f.check("worker observed forwarded peer authority", peerDelivered, "worker Result.Messages, not leader claim")
	previous := o.record
	for i := 0; i < 2; i++ {
		msg, ok := q.Pop(scope)
		if ok {
			f.action("dispatch-after-Done", msg, nil)
		}
		f.check("host FIFO dispatch", ok && msg.ID == fmt.Sprintf("after-%d", i+1), msg.ID)
		if !ok {
			continue
		}
		nextCtx, nextCancel := context.WithTimeout(parent, f.cfg.timeout)
		next := f.start(nextCtx, "start-after-"+msg.ID, scope, msg)
		f.finish(nextCtx, next, false)
		nextCancel()
		f.check(next.record.Name+" starts after previous Done", previous.ActualDone && !next.record.Started.Before(previous.Ended) && next.record.RunID != previous.RunID, "host starts a new Thread.Send after completion")
		f.check(next.record.Name+" original ID and text", hasReceipt(next.record.Messages, msg), "TaskStore receipt preserves full structured host envelope")
		f.check(next.record.Name+" no extra delegation", len(next.record.Tasks) == 1, "leader-only queued acknowledgment")
		previous = next.record
	}
}

func hasReceipt(messages []team.InboxMessage, msg hostMessage) bool {
	for _, m := range messages {
		if m.ID == msg.ID && m.Text == framed(msg) && m.From == msg.Author && m.State == team.Delivered {
			return true
		}
	}
	return false
}

func (f *fixture) stopAndPriority(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, f.cfg.timeout)
	defer cancel()
	scope := encodeThreadKey("workspace:λ", "chat/17")
	initial := human(scope, "same-id-across-threads", `Delegate exactly one task to beta. Beta must write cancel-start.txt with exact bytes STARTED, then call host_gate with gate="cancel-running" and wait. Never finish that task before the gate returns. Leader ends its provider turn immediately after delegation; no polling or additional delegation.`, "beta")
	o := f.start(ctx, "stop-original", scope, initial)
	arrival, err := f.gates.wait(ctx, "beta", "cancel-running")
	f.check("actual beta worker before cancellation", err == nil, errorText(err))
	if err == nil {
		f.report.Gates = append(f.report.Gates, arrival)
	}
	q := newHostQueue([]string{"alpha", "beta"})
	normal := human(scope, "normal-queued", "Do not delegate. Return required JSON with detail ORDINARY_AFTER_PRIORITY.", "leader")
	withdrawn := human(scope, "withdrawn", "This must never reach the SDK", "leader")
	priority := human(scope, "priority-restart", `Start a NEW objective: delegate exactly one task to beta, which must write restart.txt with exact bytes NEW_GOAL and return the required JSON. Do not resume, continue, or restart the cancelled task. Leader ends its turn immediately after delegation and finalizes after beta completes.`, "beta")
	priority.Priority = true
	for _, msg := range []hostMessage{normal, withdrawn, priority} {
		ok, e := q.Admit(msg)
		f.action("queue-during-active-run", msg, e)
		f.check("host enqueue "+msg.ID, ok && e == nil, errorText(e))
	}
	f.check("host withdrawal before dispatch", q.Withdraw(scope, withdrawn.ID), "")
	f.action("withdraw-before-dispatch", withdrawn, nil)
	o.stream.Cancel()
	f.finish(ctx, o, true)
	f.check("old cancellation and artifact kept", len(o.record.Tasks) == 1 && o.record.Tasks[0].Member == "beta" && o.record.Tasks[0].State == team.Cancelled && f.fileEquals("beta", "cancel-start.txt", "STARTED"), "cancelled TaskStore record and partial Result retained")
	f.gates.release("beta", "cancel-running")
	newScope := encodeThreadKey("workspace:λ", "chat/17/restarted")
	previous := o.record
	var restartedTasks []team.Task
	for i := 0; i < 2; i++ {
		msg, ok := q.Pop(scope)
		if ok {
			f.action("dispatch-after-cancelled-Done", msg, nil)
		}
		expected := priority.ID
		if i == 1 {
			expected = normal.ID
		}
		f.check("priority then ordinary host order", ok && msg.ID == expected, msg.ID)
		if !ok {
			continue
		}
		// Queue identity remains the original thread and envelope. The new TeamThread
		// is an explicit host restart destination, never an Interrupt delivery.
		nextCtx, nextCancel := context.WithTimeout(parent, f.cfg.timeout)
		next := f.start(nextCtx, fmt.Sprintf("restart-%d-%s", i+1, msg.ID), newScope, msg)
		f.finish(nextCtx, next, false)
		nextCancel()
		taskErr := validateRestartTasks(next.record.Tasks, restartedTasks, i == 1)
		f.check(next.record.Name+" exact successful beta task", taskErr == nil, errorText(taskErr))
		if i == 0 {
			restartedTasks = append([]team.Task(nil), next.record.Tasks...)
		}
		f.check(next.record.Name+" waits for Done and new RunID", previous.ActualDone && !next.record.Started.Before(previous.Ended) && next.record.RunID != previous.RunID, "")
		f.check(next.record.Name+" original queued input retained", hasReceipt(next.record.Messages, msg), "original ID, original text, original human author")
		f.check(next.record.Name+" withdrawn message absent", !hasID(next.record.Messages, withdrawn.ID), "")
		previous = next.record
	}
	f.check("new goal independent of cancelled task", f.fileEquals("beta", "restart.txt", "NEW_GOAL"), "new TeamThread beta artifact")
	f.check("same ID cross-thread independent", scope != f.report.Runs[0].Scope && hasReceipt(o.record.Messages, initial), "same opaque starting ID delivered in both isolated scopes")
	_, remaining := q.Pop(scope)
	f.check("withdrawn queue stays empty", !remaining, "")
}

// A successful leader response and a written artifact do not prove successful
// delegation. The ordinary queued run must retain exactly the restart's task,
// rather than silently adding or replacing work.
func validateRestartTasks(tasks, previous []team.Task, requireUnchanged bool) error {
	if len(tasks) != 1 {
		return fmt.Errorf("restart requires exactly one beta task, observed %d tasks", len(tasks))
	}
	task := tasks[0]
	if task.ID == "" || task.Member != "beta" || task.State != team.Succeeded {
		return fmt.Errorf("restart task must be beta and succeeded: task=%q member=%q state=%q", task.ID, task.Member, task.State)
	}
	if requireUnchanged {
		if len(previous) != 1 {
			return fmt.Errorf("restart baseline must contain exactly one beta task, observed %d", len(previous))
		}
		before := previous[0]
		if task.ID != before.ID || task.Member != before.Member || task.Scope != before.Scope || task.Objective != before.Objective || task.Conversation != before.Conversation {
			return errors.New("ordinary queued run changed the restart task identity or delegated objective")
		}
	}
	return nil
}
func hasID(messages []team.InboxMessage, id string) bool {
	for _, m := range messages {
		if m.ID == id {
			return true
		}
	}
	return false
}
