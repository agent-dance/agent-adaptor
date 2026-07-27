// Command team-agent-workflow is the v1 team-collaboration showcase (design
// doc §9.7 / §9.8): one leader Agent drives three delegated roles —
// plan (read-only) -> impl (workspace-write) -> review (read-only) — through
// the host-injected delegate_to_agent MCP tool, and the whole team's progress
// arrives on the leader's single event stream.
//
// What the SDK contributes here, in three lines of construction:
//
//   - delegation.NewService(...) is the entire delegation runtime: registry,
//     event bus, delegator, per-run MCP sidecar (loopback listener, random
//     bearer token, http.Server lifecycle) and result recording. The pre-v1
//     version of this showcase hand-wrote 323 lines for exactly this.
//   - delegation.Local(key, runner, policy) registers an in-process Runner as
//     a delegatable role, so each role is one *adaptor.Agent value wrapped in
//     an ordinary host decorator — no per-role SDK instance, no A2A server,
//     no port to manage.
//   - team.Option() attaches the service to the leader in one option: the
//     sidecar is declared as a runtime service with a typed MCP endpoint, its
//     lifecycle is bound to the run, and every delegation event is folded into
//     the leader's own Events() channel as adaptor.SubagentUpdate.
//
// Everything else — the temporary workspace fixture, the terminal renderer,
// the workspace stage audit, and the protocol text handed to the leader — is
// plain host logic and lives in host.go.
//
// LIVE ONLY, and it costs real money: one leader run plus three role runs
// against the selected local CLIs. There is therefore no default agent
// (-leader is required), no fallback that picks one for you, and no Go test
// in this directory. It is not part of examples/run_examples.ps1.
//
// Usage:
//
//	go run ./examples/showcases/team-agent-workflow -leader=claude
//	go run ./examples/showcases/team-agent-workflow -leader=claude -plan=codex -review=codex
//	go run ./examples/showcases/team-agent-workflow -leader=claude -keep-workspace
//	go run ./examples/showcases/team-agent-workflow -leader=claude -web-mode -web-addr=:8080
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strings"
	"time"

	"github.com/agent-dance/agent-adaptor/bridges/sse"
	"github.com/agent-dance/agent-adaptor/examples/internal/exampleutil"
	delegation "github.com/agent-dance/agent-adaptor/hosttools/a2adelegation"
	"github.com/agent-dance/agent-adaptor/memory"
	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/profile"
)

const (
	// workflowSentinel is the leader's own completion marker.
	workflowSentinel = "TEAM_AGENT_WORKFLOW_OK"
	// reviewApprovalSentinel / reviewRejectSentinel gate the workflow on the
	// review role's verdict, read back with delegation.Result.HasLine.
	reviewApprovalSentinel = "TEAM_REVIEW_APPROVED"
	reviewRejectSentinel   = "TEAM_REVIEW_REJECTED"

	// delegateToolLiteral is the tool name the leader calls. It comes from
	// the delegation package rather than a string literal in the protocol
	// text, so prompt and sidecar can never drift apart.
	delegateToolLiteral = delegation.DelegateToolName
)

type options struct {
	leader, plan, impl, review string
	model, command             string
	timeout, roleTimeout       time.Duration
	keepWorkspace, webMode     bool
	webAddr, webCORS           string
}

func main() {
	var opts options
	flag.StringVar(&opts.leader, "leader", "", "REQUIRED. Local CLI driving the leader: "+exampleutil.SupportedAgents())
	flag.StringVar(&opts.plan, "plan", "", "Local CLI playing the plan role (default: -leader)")
	flag.StringVar(&opts.impl, "impl", "", "Local CLI playing the impl role (default: -leader)")
	flag.StringVar(&opts.review, "review", "", "Local CLI playing the review role (default: -leader)")
	flag.StringVar(&opts.model, "model", "", "Model override for every agent. Defaults per agent or CODEX_MODEL/CLAUDE_MODEL/CURSOR_MODEL.")
	flag.StringVar(&opts.command, "command", "", "Explicit CLI command for the leader; roles resolve their own from PATH/env.")
	flag.DurationVar(&opts.timeout, "timeout", 15*time.Minute, "Deadline for the whole workflow")
	flag.DurationVar(&opts.roleTimeout, "role-timeout", 4*time.Minute, "Deadline for one delegation")
	flag.BoolVar(&opts.keepWorkspace, "keep-workspace", false, "Keep the temporary workspace and cloned profiles")
	flag.BoolVar(&opts.webMode, "web-mode", false, "Serve the leader over AG-UI SSE instead of running one CLI workflow")
	flag.StringVar(&opts.webAddr, "web-addr", ":8080", "AG-UI listen address (-web-mode)")
	flag.StringVar(&opts.webCORS, "web-cors", "*", "Access-Control-Allow-Origin (-web-mode)")
	flag.Parse()

	// No default agent on purpose: every run of this showcase makes four paid
	// CLI invocations, so it never picks one for you.
	if strings.TrimSpace(opts.leader) == "" {
		exampleutil.Fatalf("-leader is required (try -leader=claude): this showcase makes real, paid CLI calls and never picks an agent for you")
	}
	if err := run(opts); err != nil {
		exampleutil.Fatalf("team-agent-workflow: %v", err)
	}
}

func run(opts options) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, opts.timeout)
	defer cancel()

	term := newConsole()

	fixture, err := newFixture(opts.keepWorkspace) // temp workspace + TASK.md (host logic)
	if err != nil {
		return err
	}
	defer fixture.Cleanup(term)
	audit := newStageAudit(fixture.WorkspaceDir)
	term.Logf("[fixture] workspace=%s", fixture.WorkspaceDir)

	// 1. The roles. Each one is a single *adaptor.Agent value: its sandbox,
	//    instructions and profile are construction-scope properties of what
	//    the role *is*, not per-call decoration. observe(...) wraps it in an
	//    ordinary host decorator, and delegation.Local takes any Runner — an
	//    Agent, a Thread, or a decorator over either.
	roles := buildRoles(opts, fixture.WorkspaceDir)
	refs := make([]delegation.AgentRef, 0, len(roles))
	for _, def := range roles {
		role := adaptor.New(def.Driver, append([]adaptor.Option{
			adaptor.WithProfile(profile.CloneNative(fixture.ProfileDir(def.Key),
				profile.CopySettings(), profile.LinkAuth())),
			adaptor.WithWorkspace(fixture.WorkspaceDir),
			adaptor.WithPolicy(exampleutil.NonInteractivePolicy(def.Sandbox)),
			adaptor.WithInstructions(def.Instructions),
			adaptor.WithIdentity(adaptor.Identity{ID: "team-" + def.Key, Tenant: "example", Name: def.DisplayName}),
			adaptor.WithMetadata("example", "team-agent-workflow"),
			adaptor.WithMetadata("workflow_role", def.Key),
		}, def.Options...)...) // role-local options come last, so they may override any shared default
		refs = append(refs, delegation.Local(def.Key,
			observe(def.Key, role, term, audit.Record),
			delegation.Policy{MaxTimeout: opts.roleTimeout, RequireStreaming: true, MaxArtifactBytes: 1 << 20}))
		term.Logf("[role] %s = %s (%s, sandbox=%s)", def.Key, def.Agent, def.Model, def.Sandbox)
	}

	// 2. The delegation service: one configuration call replaces the registry,
	//    event bus, per-run MCP sidecar and result bookkeeping a host used to
	//    write by hand. Config.Observe is available as an out-of-band tap, but
	//    this example does not need it: the same events reach the leader's own
	//    stream below.
	team, err := delegation.NewService(delegation.Config{
		Agents:      refs,
		ToolTimeout: opts.roleTimeout + 30*time.Second, // replaces the old MCP_TOOL_TIMEOUT env side channel
		Tenant:      "example",
	})
	if err != nil {
		return err
	}
	defer team.Close()

	// 3. The leader: one Agent value again, with team.Option() as the single
	//    line that gives it a team. The leader is read-only for its whole
	//    life — it delegates instead of editing — so the sandbox belongs on
	//    the constructor, not on every call.
	leaderCfg := liveConfig(opts.leader, opts.model, opts.command, fixture.WorkspaceDir)
	leaderOpts := []adaptor.Option{
		adaptor.WithProfile(profile.CloneNative(fixture.ProfileDir("leader"),
			profile.CopySettings(), profile.LinkAuth())),
		adaptor.WithWorkspace(fixture.WorkspaceDir),
		adaptor.WithPolicy(exampleutil.NonInteractivePolicy(adaptor.ReadOnly)),
		adaptor.WithIdentity(adaptor.Identity{ID: "team-leader", Tenant: "example", Name: "Team leader"}),
		adaptor.WithMetadata("example", "team-agent-workflow"),
		adaptor.WithMetadata("workflow_role", "leader"),
		team.Option(), // sidecar declaration + run-scoped lifecycle + SubagentUpdate confluence
	}
	if opts.webMode {
		leaderOpts = append(leaderOpts,
			adaptor.WithThreadStore(memory.NewStore()),                 // AG-UI threadId -> adaptor.Thread
			adaptor.WithInstructions(leaderProtocol(opts.roleTimeout)), // the browser supplies the per-turn prompt
		)
	}
	// R9 note — resident leader process. The leader runs the whole workflow in
	// one long invocation, so it is the one agent that benefits from CLI
	// process reuse:
	//
	//	claude.Driver(claude.Config{
	//	    Model: leaderCfg.Model,
	//	    // PersistentProcess: true,
	//	})
	//
	// PersistentProcess is out of v1.0.0 scope (implementation plan R9). It
	// returns as an additive claude.Config field once cl/opt_examples merges
	// into main — the v1 API shape does not change when it does — so the line
	// is kept here as a comment switch to flip on after that merge.
	// exampleutil.NewLiveDriver carries the same note on its claude branch.
	leader := adaptor.New(exampleutil.NewLiveDriver(leaderCfg), leaderOpts...)

	if opts.webMode {
		return serveAGUI(ctx, leader, opts, term)
	}

	// 4. One prompt, one event stream, one for-range. No second channel with a
	//    drain obligation, no hand-rolled goroutines, no bus subscribe/clear
	//    bookkeeping: the delegated roles' progress is interleaved with the
	//    leader's own output, in order, on this very channel.
	stream := leader.Stream(ctx, leaderProtocol(opts.roleTimeout))

	trace := &workflowTrace{}
	for ev := range stream.Events() {
		switch e := ev.(type) {
		case adaptor.TextDelta:
			term.Print(e.Text)
		case adaptor.Thinking:
			term.Reasoning(e.Text)
		case adaptor.ToolCall:
			if e.Phase == adaptor.PhaseStart {
				term.Tool(e.Name, e.Args)
			}
		case adaptor.SubagentUpdate:
			term.Live(e.Agent, string(e.Kind), e.Delta)
			trace.Add(e)
		case adaptor.Dropped:
			term.Warnf("dropped %d events", e.Count)
		}
	}

	// One err, one judgement. A business failure is a *adaptor.RunError that
	// still carries the complete Result; infrastructure failures travel the
	// same return.
	res, err := stream.Result()
	if err != nil {
		var runErr *adaptor.RunError
		if errors.As(err, &runErr) {
			return fmt.Errorf("leader failed (%s): %s", runErr.Reason, runErr.Message)
		}
		return fmt.Errorf("leader: %w", err)
	}

	// 5. The orchestration verdict: delegation order, workspace stage
	//    boundaries, the review role's own sentinel, and the fixture artifact.
	if err := trace.RequireOrder("plan", "impl", "review"); err != nil {
		return err
	}
	audit.Record("final")
	if err := audit.ValidateStageBoundaries(); err != nil {
		return err
	}
	review, ok := team.Result(stream.RunID(), "review")
	if !ok {
		return fmt.Errorf("no delegation result recorded for review in run %s", stream.RunID())
	}
	if !review.HasLine(reviewApprovalSentinel) {
		return fmt.Errorf("review did not approve (status=%s); leader_output=%q", review.Status, preview(res.Text, 800))
	}
	if err := fixture.Validate(); err != nil {
		return err
	}

	exampleutil.PrintJSON(map[string]any{
		"example":          "team-agent-workflow",
		"run_id":           stream.RunID(),
		"leader":           exampleutil.LiveAgentSummary(leaderCfg),
		"roles":            roleSummaries(roles),
		"delegation_order": trace.Order(),
		"delegation_tool":  delegation.DelegateToolName,
		"recorded_results": len(team.Results(stream.RunID())),
		"review_status":    review.Status,
		"leader_sentinel":  strings.Contains(res.Text, workflowSentinel),
		"workspace":        fixture.WorkspaceDir,
		"stages":           audit.Stages(),
		"console":          term.Stats(),
	})
	return nil
}

// serveAGUI is the web half of the same leader. It needs no delegation-aware
// bridge option: SubagentUpdate is already on the stream the handler consumes,
// so CLI and browser render one shape.
func serveAGUI(ctx context.Context, leader adaptor.Runner, opts options, term *console) error {
	mux := http.NewServeMux()
	mux.Handle("/agent", sse.HandlerV1(leader, sse.OptionsV1{
		Protocol:          sse.AGUI,
		CORSAllowedOrigin: opts.webCORS,
	}))
	server := &http.Server{Addr: opts.webAddr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	term.Logf("[web] AG-UI on %s (POST /agent) — every request delegates and bills the CLIs", opts.webAddr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// ---- The role table: host data, not an SDK concept ----

// roleDef is this host's own description of a team member. The SDK has no
// role type and wants none: "a configured agent" already has a name — Runner.
// The Options field is the escape hatch that keeps the table honest: any role
// may carry any construction option (skills, MCP, profile resources, its own
// thread store, an approval handler, ...) without the table growing a column.
type roleDef struct {
	Key, DisplayName string
	Driver           adaptor.Driver // root-package alias of driver.Driver
	Agent, Model     string         // resolved local CLI, for reporting only
	Sandbox          adaptor.SandboxLevel
	Instructions     string
	Options          []adaptor.Option
}

func buildRoles(opts options, cwd string) []roleDef {
	plan := liveConfig(pick(opts.plan, opts.leader), opts.model, "", cwd)
	impl := liveConfig(pick(opts.impl, opts.leader), opts.model, "", cwd)
	review := liveConfig(pick(opts.review, opts.leader), opts.model, "", cwd)
	return []roleDef{{
		Key: "plan", DisplayName: "Planner",
		Driver: exampleutil.NewLiveDriver(plan), Agent: plan.Agent, Model: plan.Model,
		Sandbox: adaptor.ReadOnly,
		Instructions: "You are the planning stage of a three-agent team, and only that stage. " +
			"Read TASK.md in the working directory. Do not create, modify or delete any file. " +
			"Answer with a numbered plan of at most six steps, followed by the acceptance checks " +
			"copied verbatim from TASK.md. Keep the whole answer under 250 words.",
	}, {
		Key: "impl", DisplayName: "Implementer",
		Driver: exampleutil.NewLiveDriver(impl), Agent: impl.Agent, Model: impl.Model,
		Sandbox: adaptor.WorkspaceWrite,
		Instructions: "You are the implementation stage of a three-agent team, and only that stage. " +
			"Use the plan quoted in your objective plus TASK.md, and produce exactly the file TASK.md " +
			"asks for. Do not modify TASK.md, do not create any other file, do not run a package " +
			"manager or a network command. Answer with the file path and a one-line summary.",
		Options: []adaptor.Option{
			// Escape hatch in action: only this role gets a sub-agent, and the
			// table did not have to learn what a sub-agent is.
			adaptor.WithProfileResources(profile.Resources{
				Agents: []profile.SubAgent{{
					Key:          "proofreader",
					Description:  "Re-reads the produced file against TASK.md before the stage answers.",
					Instructions: "Compare the produced file with TASK.md section by section and list anything missing. Never edit files.",
				}},
			}),
		},
	}, {
		Key: "review", DisplayName: "Reviewer",
		Driver: exampleutil.NewLiveDriver(review), Agent: review.Agent, Model: review.Model,
		Sandbox: adaptor.ReadOnly,
		Instructions: "You are the review stage of a three-agent team, and only that stage. " +
			"Read TASK.md and the produced file. Do not create, modify or delete any file. " +
			"State each acceptance check and whether it passes. End your answer with a final line " +
			"containing exactly " + reviewApprovalSentinel + " when every check passes, otherwise a " +
			"final line containing exactly " + reviewRejectSentinel + ".",
	}}
}

// liveConfig resolves one local CLI (PATH / *_COMMAND / -command) and probes
// its --help before the workflow starts. Probing is free; the model calls that
// follow are not.
func liveConfig(agent, model, command, cwd string) exampleutil.LiveAgentConfig {
	cfg := exampleutil.ResolveLiveAgentConfig(agent, model, command, cwd)
	if cfg.Agent == exampleutil.AgentCodex {
		// The fixture workspace is a bare temp directory, not a git repo.
		cfg.ExtraArgs = append(cfg.ExtraArgs, "--skip-git-repo-check")
	}
	return cfg
}

func roleSummaries(roles []roleDef) []map[string]any {
	out := make([]map[string]any, 0, len(roles))
	for _, def := range roles {
		out = append(out, map[string]any{
			"key": def.Key, "agent": def.Agent, "model": def.Model, "sandbox": string(def.Sandbox),
		})
	}
	return out
}

// ---- Role observation: decorate two methods, not three interception points ----

// observed wraps a role's Runner. Runner has exactly Run and Stream, so a
// decorator is plain Go: embed the interface, override what you care about.
type observed struct {
	adaptor.Runner
	role   string
	term   *console
	record func(string)
}

func observe(role string, next adaptor.Runner, term *console, record func(string)) adaptor.Runner {
	return observed{Runner: next, role: role, term: term, record: record}
}

func (o observed) Run(ctx context.Context, prompt string, opts ...adaptor.CallOption) (*adaptor.Result, error) {
	started := time.Now()
	res, err := o.Runner.Run(ctx, prompt, opts...)
	o.done(started, res, err)
	return res, err
}

func (o observed) Stream(ctx context.Context, prompt string, opts ...adaptor.CallOption) adaptor.Stream {
	return observedStream{Stream: o.Runner.Stream(ctx, prompt, opts...), o: o, started: time.Now()}
}

// done is the single logging point for both verbs: one err decides the
// outcome, and the business detail lives in *adaptor.RunError.Result.
func (o observed) done(started time.Time, res *adaptor.Result, err error) {
	elapsed := time.Since(started).Round(time.Millisecond)
	if err != nil {
		o.term.Logf("[role] %s failed in %s: %v", o.role, elapsed, err)
	} else {
		o.term.Logf("[role] %s done in %s: %s", o.role, elapsed, preview(pick(res.Summary, res.Text), 120))
	}
	o.record(o.role) // workspace stage snapshot (host audit logic)
}

type observedStream struct {
	adaptor.Stream
	o       observed
	started time.Time
}

func (s observedStream) Result() (*adaptor.Result, error) {
	res, err := s.Stream.Result()
	s.o.done(s.started, res, err)
	return res, err
}

// ---- Delegation order check ----

// workflowTrace reconstructs the delegation order from the leader's own event
// stream: a SubagentStarted is a role picking up work.
type workflowTrace struct{ order []string }

func (t *workflowTrace) Add(e adaptor.SubagentUpdate) {
	if e.Kind == adaptor.SubagentStarted {
		t.order = append(t.order, e.Agent)
	}
}

func (t *workflowTrace) Order() []string { return t.order }

func (t *workflowTrace) RequireOrder(want ...string) error {
	if !slices.Equal(t.order, want) {
		return fmt.Errorf("delegation order = %v, want %v", t.order, want)
	}
	return nil
}
