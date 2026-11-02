// Command team-agent-workflow demonstrates event-driven plan → impl → review
// delegation with hosttools/team, one event stream and host-owned assertions.
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

	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"github.com/agent-dance/agent-adaptor/v2/bridges/sse"
	"github.com/agent-dance/agent-adaptor/v2/examples/internal/exampleutil"
	teamruntime "github.com/agent-dance/agent-adaptor/v2/hosttools/team"
	"github.com/agent-dance/agent-adaptor/v2/memory"
	"github.com/agent-dance/agent-adaptor/v2/profile"
)

const (
	defaultWebListenAddr = "127.0.0.1:8080"
	// The one-command start script reuses the maintained CopilotKit frontend
	// from examples/web-chat/copilotkit. Direct Go invocations remain locked
	// down unless the operator deliberately enables one browser origin.
	defaultWebCORSOrigin = ""

	// workflowSentinel is the leader's own completion marker.
	workflowSentinel = "TEAM_AGENT_WORKFLOW_OK"
	// reviewApprovalSentinel / reviewRejectSentinel gate the workflow on the
	// review role's verdict, read back with the persisted task Result.
	reviewApprovalSentinel = "TEAM_REVIEW_APPROVED"
	reviewRejectSentinel   = "TEAM_REVIEW_REJECTED"
	planArtifactFilename   = "PLAN.md"

	// delegateToolLiteral is the tool name the leader calls. It comes from
	// the delegation package rather than a string literal in the protocol
	// text, so prompt and sidecar can never drift apart.
	delegateToolLiteral = "team_delegate"
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
	flag.StringVar(&opts.webAddr, "web-addr", defaultWebListenAddr, "AG-UI listen address (-web-mode; explicitly set a non-loopback address for remote access)")
	flag.StringVar(&opts.webCORS, "web-cors", defaultWebCORSOrigin, "Optional Access-Control-Allow-Origin (-web-mode; disabled by default)")
	flag.Parse()

	// No default agent on purpose: every run of this showcase makes paid
	// leader and worker CLI invocations, so it never picks one for you.
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
	//    ordinary host decorator, and team.Member takes any Runner — an
	//    Agent, a Thread, or a decorator over either.
	roles := buildRoles(opts, fixture.WorkspaceDir)
	refs := make([]teamruntime.Member, 0, len(roles))
	for _, def := range roles {
		role := adaptor.New(def.Driver, append([]adaptor.Option{
			adaptor.WithProfile(profile.CloneNative(fixture.ProfileDir(def.Key),
				profile.CopySettings(), profile.LinkAuth())),
			adaptor.WithWorkspace(fixture.WorkspaceDir),
			adaptor.WithPolicy(exampleutil.NonInteractivePolicy(def.Agent, def.Sandbox)),
			adaptor.WithInstructions(def.Instructions),
			adaptor.WithIdentity(adaptor.Identity{ID: "team-" + def.Key, Tenant: "example", Name: def.DisplayName}),
			adaptor.WithMetadata("example", "team-agent-workflow"),
			adaptor.WithMetadata("workflow_role", def.Key),
		}, def.Options...)...) // role-local options come last, so they may override any shared default
		roleRunner := adaptor.Runner(role)
		if len(def.CallOptions) > 0 {
			roleRunner = withCallOptions(roleRunner, def.CallOptions...)
		}
		refs = append(refs, teamruntime.Member{Key: def.Key, Name: def.DelegationDisplayName(), Description: def.Instructions,
			Runner: observe(def.Key, roleRunner, term, audit.Record), Timeout: opts.roleTimeout})
		defer role.Close(context.Background())

		term.Logf("[role] %s = %s (%s, sandbox=%s)", def.Key, def.Agent, def.Model, def.Sandbox)
	}

	// The leader owns its sandbox; Team adds its four scoped tools.
	leaderCfg := liveConfig(opts.leader, opts.model, opts.command, fixture.WorkspaceDir)
	leaderOpts := []adaptor.Option{
		adaptor.WithProfile(profile.CloneNative(fixture.ProfileDir("leader"),
			profile.CopySettings(), profile.LinkAuth())),
		adaptor.WithWorkspace(fixture.WorkspaceDir),
		adaptor.WithPolicy(exampleutil.NonInteractivePolicy(leaderCfg.Agent, adaptor.ReadOnly)),
		adaptor.WithIdentity(adaptor.Identity{ID: "team-leader", Tenant: "example", Name: "Team leader"}),
		adaptor.WithMetadata("example", "team-agent-workflow"),
		adaptor.WithMetadata("workflow_role", "leader"),
	}
	if opts.webMode {
		leaderOpts = append(leaderOpts,
			adaptor.WithThreadStore(memory.NewStore()),                 // AG-UI threadId -> adaptor.Thread
			adaptor.WithInstructions(leaderProtocol(opts.roleTimeout)), // the browser supplies the per-turn prompt
		)
	}
	leader := adaptor.New(exampleutil.NewLiveDriver(leaderCfg), leaderOpts...)

	defer leader.Close(context.Background())
	store := teamruntime.NewMemoryStore()
	team, err := teamruntime.New(teamruntime.Config{Leader: leader, Members: refs, Store: store})
	if err != nil {
		return err
	}
	defer team.Close(context.Background())

	if opts.webMode {
		return serveAGUI(ctx, team, opts, term)
	}

	// 4. One prompt, one event stream, one for-range. No second channel with a
	//    drain obligation, no hand-rolled goroutines, no bus subscribe/clear
	//    bookkeeping: the delegated roles' progress is interleaved with the
	//    leader's own output, in order, on this very channel.
	scope := "workflow"
	stream := team.Thread(scope).Send(ctx, leaderProtocol(opts.roleTimeout))

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
			term.Live(e.Member, string(e.Kind), e.Delta)
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
	tasks, err := store.Tasks(ctx, scope)
	if err != nil {
		return err
	}
	var review *adaptor.Result
	for _, task := range tasks {
		if task.Member == "review" && task.State == teamruntime.Succeeded {
			review = task.Result
		}
	}
	if review == nil || !slices.Contains(strings.Split(strings.TrimSpace(review.Text), "\n"), reviewApprovalSentinel) {
		return fmt.Errorf("review did not approve; leader_output=%q", preview(res.Text, 800))
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
		"delegation_tool":  "team_delegate",
		"recorded_results": len(res.Subagents()),
		"review_status":    "succeeded",
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
	server := &http.Server{Addr: opts.webAddr, Handler: newTeamWebHandler(leader, opts.webCORS), ReadHeaderTimeout: 10 * time.Second}
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

func newTeamWebHandler(leader adaptor.Runner, corsOrigin string) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/agent", sse.Handler(leader, sse.Options{
		Protocol:          sse.AGUI,
		CORSAllowedOrigin: corsOrigin,
	}))
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	return mux
}

// ---- The role table: host data, not an SDK concept ----

// roleDef is this host's own description of a team member. The SDK has no
// role type and wants none: "a configured agent" already has a name — Runner.
// Options and CallOptions are the two scoped escape hatches that keep the
// table honest: a role may carry construction resources or a per-invocation
// output contract without the table growing a feature-specific column.
type roleDef struct {
	Key, DisplayName string
	Driver           adaptor.Driver // root-package alias of driver.Driver
	Agent, Model     string         // resolved local CLI, for reporting only
	Sandbox          adaptor.SandboxLevel
	Instructions     string
	Options          []adaptor.Option
	CallOptions      []adaptor.CallOption
}

func (r roleDef) DelegationDisplayName() string {
	return providerDisplayName(r.Agent) + " " + r.DisplayName
}

func providerDisplayName(agent string) string {
	switch strings.ToLower(strings.TrimSpace(agent)) {
	case exampleutil.AgentClaude:
		return "Claude Code"
	case exampleutil.AgentCursor:
		return "Cursor"
	case exampleutil.AgentCodebuddy:
		return "CodeBuddy"
	default:
		return "Codex"
	}
}

// planFileArtifact is both the plan role's structured-output contract and the
// frontend attachment payload. It represents a file deliverable without
// granting the read-only planning role permission to mutate the workspace.
type planFileArtifact struct {
	Filename  string `json:"filename"`
	MediaType string `json:"media_type"`
	Summary   string `json:"summary"`
	Content   string `json:"content"`
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
			"Read TASK.md in the working directory. Do not create, modify or delete any workspace file. " +
			"Return one structured file artifact: filename must be " + planArtifactFilename + ", media_type must be text/markdown, " +
			"summary must be one sentence, and content must be Markdown containing a numbered plan of at most six steps " +
			"followed by an Acceptance checks section copied verbatim from TASK.md. Keep content under 250 words.",
		CallOptions: []adaptor.CallOption{
			adaptor.WithSchema[planFileArtifact](
				adaptor.SchemaName("team_plan_file"),
				adaptor.SchemaDescription("A downloadable Markdown plan file produced by the read-only planning role."),
			),
		},
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

// withCallOptions decorates a Runner with role-owned invocation contracts.
// The role options are appended last so a delegate cannot replace the plan
// role's required structured file schema.
type callOptionRunner struct {
	adaptor.Runner
	opts []adaptor.CallOption
}

func withCallOptions(next adaptor.Runner, opts ...adaptor.CallOption) adaptor.Runner {
	return callOptionRunner{Runner: next, opts: append([]adaptor.CallOption(nil), opts...)}
}

func (r callOptionRunner) Send(ctx context.Context, prompt string, opts ...adaptor.CallOption) adaptor.Stream {
	return r.Runner.Send(ctx, prompt, appendCallOptions(opts, r.opts)...)
}

func appendCallOptions(first, last []adaptor.CallOption) []adaptor.CallOption {
	out := make([]adaptor.CallOption, 0, len(first)+len(last))
	out = append(out, first...)
	return append(out, last...)
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

// ---- Role observation: decorate the Runner.Send entry point ----

// observed wraps a role's Runner. Embed the interface and override Send to
// observe each execution through the same Stream contract.
type observed struct {
	adaptor.Runner
	role   string
	term   *console
	record func(string)
}

func observe(role string, next adaptor.Runner, term *console, record func(string)) adaptor.Runner {
	return observed{Runner: next, role: role, term: term, record: record}
}

func (o observed) Send(ctx context.Context, prompt string, opts ...adaptor.CallOption) adaptor.Stream {
	return observedStream{Stream: o.Runner.Send(ctx, prompt, opts...), o: o, started: time.Now()}
}

// done is the single logging point for an execution: one err decides the
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
		t.order = append(t.order, e.Member)
	}
}

func (t *workflowTrace) Order() []string { return t.order }

func (t *workflowTrace) RequireOrder(want ...string) error {
	if !slices.Equal(t.order, want) {
		return fmt.Errorf("delegation order = %v, want %v", t.order, want)
	}
	return nil
}
