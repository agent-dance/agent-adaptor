// team-agent-workflow demonstrates a Claude Code leader coordinating three
// host-curated A2A roles through one per-run MCP delegation tool:
//
//	plan (Codex) -> impl (Claude Code) -> review (Codex)
//
// The example always works in a temporary repository and cloned profiles.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/examples/internal/exampleutil"
	"github.com/agent-dance/agent-adaptor/memory"
	"github.com/agent-dance/agent-adaptor/pkg/bridges/sse"
	"github.com/agent-dance/agent-adaptor/pkg/hosttools/a2adelegation"
)

const (
	workflowSentinel       = "TEAM_AGENT_WORKFLOW_OK"
	reviewApprovalSentinel = "TEAM_REVIEW_APPROVED"
)

type options struct {
	claudeCommand string
	claudeModel   string
	codexCommand  string
	codexModel    string
	timeout       time.Duration
	roleTimeout   time.Duration
	keepWorkspace bool
	webMode       bool
	webAddr       string
	webCORS       string
}

func main() {
	var opts options
	flag.StringVar(&opts.claudeCommand, "claude-command", "", "Claude Code CLI command (or CLAUDE_COMMAND/PATH)")
	flag.StringVar(&opts.claudeModel, "claude-model", "", "Claude model override (or CLAUDE_MODEL)")
	flag.StringVar(&opts.codexCommand, "codex-command", "", "Codex CLI command (or CODEX_COMMAND/PATH)")
	flag.StringVar(&opts.codexModel, "codex-model", "", "Codex model override (or CODEX_MODEL)")
	flag.DurationVar(&opts.timeout, "timeout", 15*time.Minute, "Maximum duration for the complete workflow")
	flag.DurationVar(&opts.roleTimeout, "role-timeout", 4*time.Minute, "Maximum duration for each delegated role")
	flag.BoolVar(&opts.keepWorkspace, "keep-workspace", false, "Keep the temporary repository and cloned profiles for inspection")
	flag.BoolVar(&opts.webMode, "web-mode", false, "Serve the workflow over AG-UI so a CopilotKit frontend renders the whole run instead of running once on the CLI")
	flag.StringVar(&opts.webAddr, "web-addr", ":8080", "Address the --web-mode AG-UI server binds (POST /agent)")
	flag.StringVar(&opts.webCORS, "web-cors", "*", "Access-Control-Allow-Origin for the --web-mode AG-UI server")
	flag.Parse()

	if err := run(opts); err != nil {
		exampleutil.Fatalf("team-agent-workflow: %v", err)
	}
}

func run(opts options) error {
	if opts.timeout <= 0 || opts.roleTimeout <= 0 {
		return errors.New("timeout and role-timeout must be positive")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, opts.timeout)
	defer cancel()

	fixture, err := newWorkflowFixture(opts.keepWorkspace)
	if err != nil {
		return err
	}
	defer fixture.Cleanup()
	term.Logf("[fixture] root=%s workspace=%s keep=%t", fixture.RootDir, fixture.WorkspaceDir, fixture.Keep)

	claudeCfg, err := exampleutil.TryResolveLiveAgentConfig(
		exampleutil.AgentClaude, opts.claudeModel, opts.claudeCommand, fixture.WorkspaceDir,
	)
	if err != nil {
		return err
	}
	codexCfg, err := exampleutil.TryResolveLiveAgentConfig(
		exampleutil.AgentCodex, opts.codexModel, opts.codexCommand, fixture.WorkspaceDir,
	)
	if err != nil {
		return err
	}

	hub, remoteAgents, err := startRoleHub(roleHubConfig{
		Fixture:     fixture,
		Claude:      claudeCfg,
		Codex:       codexCfg,
		RoleTimeout: opts.roleTimeout,
	})
	if err != nil {
		return err
	}
	defer hub.Close()

	registry, err := a2adelegation.NewRegistry(remoteAgents...)
	if err != nil {
		return fmt.Errorf("build delegation registry: %w", err)
	}
	bus := a2adelegation.NewEventBus(256)
	runtimeManager := newDelegationRuntimeManager(registry, bus)
	defer runtimeManager.Close()

	leaderCfg := withMCPToolTimeout(claudeCfg, opts.roleTimeout+30*time.Second)
	leaderOpts := []agentadaptor.AgentOption{
		fixture.CloneProfileOption("leader-claude"),
		agentadaptor.WithDefaultWorkspace(agentadaptor.SharedWorkspace{}),
		agentadaptor.WithDefaultIdentity(agentadaptor.AgentIdentity{
			ID: "team-leader", TenantID: "example", ProfileID: "leader-claude", Name: "Claude Code team leader",
		}),
		agentadaptor.WithDefaultRuntimeServices(agentadaptor.RuntimeServiceSpec{
			ID:          "team-delegation-mcp",
			Name:        "team-delegation",
			Description: "Per-run MCP tool for curated A2A role delegation",
			Lifecycle:   agentadaptor.RuntimeLifecycleEphemeral,
			Metadata:    map[string]string{"example": "team-agent-workflow"},
		}),
		agentadaptor.WithDefaultMetadata("example", "team-agent-workflow"),
		agentadaptor.WithDefaultMetadata("workflow_role", "leader"),
	}
	if opts.webMode {
		// In web mode the CopilotKit frontend supplies the per-turn user text,
		// so the fixed orchestration protocol is carried as leader instructions
		// instead of a one-shot prompt.
		leaderOpts = append(leaderOpts, agentadaptor.WithDefaultInstructions(&agentadaptor.InstructionsBundleRef{
			ID:      "team-leader-protocol",
			Content: leaderPrompt(opts.roleTimeout),
		}))
	}
	leaderBinding := exampleutil.NewLiveAgentBinding(leaderCfg, leaderOpts...)
	leaderSDK := newLeaderSDK(leaderBinding, runtimeManager, opts.webMode)

	if opts.webMode {
		return serveWeb(opts, leaderSDK, bus)
	}

	handle, err := leaderSDK.Start(
		ctx,
		leaderPrompt(opts.roleTimeout),
		exampleutil.NonInteractiveRunOption(agentadaptor.IsolationReadOnly),
		agentadaptor.WithStreaming(),
	)
	if err != nil {
		return fmt.Errorf("start Claude leader: %w", err)
	}

	trace := newWorkflowTrace()
	traceDone := make(chan struct{})
	go func() {
		trace.Collect(bus.SubscribeRun(ctx, handle.RunID()))
		close(traceDone)
	}()

	var drains sync.WaitGroup
	drains.Add(2)
	go drainRunEvents(handle.Events(), &drains)
	go renderLeaderStream(handle.StreamEvents(), &drains)

	result, waitErr := handle.Wait(ctx)
	drains.Wait()
	bus.ClearRun(handle.RunID())
	<-traceDone
	if waitErr != nil {
		return fmt.Errorf("wait for Claude leader: %w", waitErr)
	}
	if result.Failure != nil {
		return fmt.Errorf("Claude leader failed: %s", result.Failure.Message)
	}
	if err := trace.ValidateOrderedRoles([]string{"plan", "impl", "review"}); err != nil {
		return err
	}
	hub.Audit.Record("final")
	if err := hub.Audit.ValidateStageBoundaries(); err != nil {
		return err
	}
	if err := runtimeManager.RequireResultLine(handle.RunID(), "review", reviewApprovalSentinel); err != nil {
		return fmt.Errorf("%w; leader_output=%q", err, preview(result.Output, 1200))
	}
	validation, err := fixture.Validate(ctx)
	if err != nil {
		return err
	}

	exampleutil.PrintJSON(map[string]any{
		"status":   "passed",
		"sentinel": workflowSentinel,
		"leader": map[string]any{
			"agent":              exampleutil.LiveAgentSummary(leaderCfg),
			"run_id":             handle.RunID(),
			"output":             preview(result.Output, 1500),
			"requested_sentinel": strings.Contains(result.Output, workflowSentinel),
		},
		"workflow": map[string]any{
			"order":                      []string{"plan:codex", "impl:claude", "review:codex"},
			"delegations":                trace.Summary(),
			"workspace_stage_boundaries": "initial=plan; plan!=impl; impl=review=final",
		},
		"a2a": map[string]any{
			"base_url": hub.BaseURL,
			"roles":    hub.RoleEndpoints,
		},
		"workspace": map[string]any{
			"path":            fixture.WorkspaceDir,
			"cleanup_on_exit": !fixture.Keep,
			"validation":      validation,
		},
	})
	return nil
}

func newLeaderSDK(binding agentadaptor.AgentBinding, runtimeManager agentadaptor.RuntimeServiceManager, webMode bool) agentadaptor.SDK {
	sdkOpts := []agentadaptor.Option{
		agentadaptor.WithDefaultAgent(binding),
	}
	if runtimeManager != nil {
		sdkOpts = append(sdkOpts, agentadaptor.WithRuntimeServiceManager(runtimeManager))
	}
	if webMode {
		// Every AG-UI request carries a threadId. The SSE bridge maps it to
		// WithSessionKey("agui", threadId), which requires a SessionStore
		// before the adapter can start. Process-local memory is sufficient for
		// this development showcase and preserves CLI mode's stateless default.
		sdkOpts = append(sdkOpts, agentadaptor.WithSessionStore(memory.NewSessionStore()))
	}
	return agentadaptor.New(sdkOpts...)
}

const webLandingPage = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>team-agent-workflow · AG-UI backend</title>
<style>
  body { font-family: system-ui, sans-serif; max-width: 720px; margin: 3rem auto; padding: 0 1rem; color: #1f2937; line-height: 1.55; }
  code { background: #f3f4f6; padding: 0.1rem 0.35rem; border-radius: 4px; }
  pre { background: #f3f4f6; padding: 0.75rem 1rem; border-radius: 8px; overflow: auto; }
  a { color: #2563eb; }
  .note { color: #6b7280; font-size: 0.9rem; }
</style>
</head>
<body>
<h1>team-agent-workflow — AG-UI backend</h1>
<p>This process is the <strong>backend</strong>, not the UI. It exposes a single
AG-UI endpoint at <code>POST /agent</code> for a CopilotKit frontend to call.</p>
<p>Open the CopilotKit UI (a separate Next.js app) at
<a href="http://localhost:3000">http://localhost:3000</a>. Start it with:</p>
<pre>cd examples/showcases/web-copilotkit-hitl/web
npm install   # first run only
npm run dev   # serves http://localhost:3000</pre>
<p class="note">Its <code>AGENT_BACKEND_URL</code> already defaults to
<code>http://localhost:8080/agent</code>, so no extra configuration is needed.
Health check: <a href="/health">/health</a>.</p>
</body>
</html>
`

// serveWeb exposes the leader over an AG-UI SSE endpoint so a CopilotKit
// frontend renders the whole run. The delegation EventBus is overlaid via
// SubagentBus, so each delegated role's progress (started, tool calls, status,
// completion) streams to the browser as AG-UI Activity events
// (ACTIVITY_SNAPSHOT / ACTIVITY_DELTA, activityType "subagent") alongside the
// leader's own text, reasoning, and delegate_to_agent TOOL_CALL_* cards. The
// parent delegate_to_agent TOOL_CALL cards are always present; Activity cards
// carry the per-role execution detail for the frontend SubagentCard.
func serveWeb(opts options, sdk agentadaptor.SDK, bus *a2adelegation.EventBus) error {
	handler := sse.Handler(sdk, sse.Options{
		Protocol:          sse.AGUI,
		CORSAllowedOrigin: opts.webCORS,
		KeepAlivePing:     15 * time.Second,
		SubagentBus:       bus,
		RunOptions: []agentadaptor.RunOption{
			exampleutil.NonInteractiveRunOption(agentadaptor.IsolationReadOnly),
		},
	})
	mux := http.NewServeMux()
	mux.Handle("/agent", exampleutil.WithRequestTimeout(handler, opts.timeout))
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})
	// This server is the AG-UI backend, not the UI. Opening it in a browser
	// otherwise shows a bare "404 page not found"; serve a short landing page
	// that points at the CopilotKit frontend instead.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, webLandingPage)
	})
	// The reused web-copilotkit-hitl frontend polls these host-recovery
	// endpoints directly from the browser. This minimal team backend does not
	// persist a session recorder, so serve empty-but-valid responses (with CORS)
	// to keep that frontend's side panel error-free; the live CopilotChat still
	// renders the whole run.
	corsStub := func(payload func(*http.Request) any) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if opts.webCORS != "" {
				w.Header().Set("Access-Control-Allow-Origin", opts.webCORS)
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			}
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(payload(r))
		}
	}
	mux.HandleFunc("/session/events", corsStub(func(r *http.Request) any {
		return map[string]any{
			"thread_id": r.URL.Query().Get("thread_id"), "after": 0,
			"events": []any{}, "last_seq": 0, "run_active": false,
		}
	}))
	mux.HandleFunc("/decision/pending", corsStub(func(*http.Request) any {
		return map[string]any{"pending": []any{}}
	}))
	server := &http.Server{Addr: opts.webAddr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	term.Logf("[web] AG-UI server listening on %s", opts.webAddr)
	term.Logf("[web] POST %s (point a CopilotKit HttpAgent here)", exampleutil.HTTPURL(opts.webAddr, "/agent"))
	term.Logf("[web] reuse the frontend in examples/showcases/web-copilotkit-hitl/web; AGENT_BACKEND_URL already defaults to this endpoint")
	return exampleutil.ServeUntilSignal(server)
}

func withMCPToolTimeout(cfg exampleutil.LiveAgentConfig, timeout time.Duration) exampleutil.LiveAgentConfig {
	value := strconv.FormatInt(timeout.Milliseconds(), 10)
	env := make([]agentadaptor.EnvBinding, 0, len(cfg.Env)+1)
	for _, binding := range cfg.Env {
		if binding.Name != "MCP_TOOL_TIMEOUT" {
			env = append(env, binding)
		}
	}
	cfg.Env = append(env, agentadaptor.EnvBinding{Name: "MCP_TOOL_TIMEOUT", Value: value})
	return cfg
}

func leaderPrompt(roleTimeout time.Duration) string {
	seconds := int(roleTimeout / time.Second)
	return fmt.Sprintf(`You are the leader of a three-stage software team. You coordinate work only through the MCP tool delegate_to_agent. Do not edit files directly and do not use any unregistered agent key.

Execute exactly this sequence, waiting for each structured result before continuing:

1. Call agent "plan" (Codex). Ask it to inspect TASK.md, the current code, and tests, then return a bounded implementation plan. Use stream=true and timeout_seconds=%d.
2. Call agent "impl" (Claude Code). Pass the plan result in input.context. Ask it to implement the task in the shared workspace, modify only slug.go, run tests, and not commit. Use stream=true and timeout_seconds=%d.
3. Call agent "review" (Codex). Pass both prior results in input.context. Ask it to review the current git diff against TASK.md using the host-generated test/diff evidence attached by the role boundary. It must end with a line containing exactly %s only when the implementation is correct; otherwise it must end with TEAM_REVIEW_REJECTED. Use stream=true and timeout_seconds=%d.

Do not reorder, skip, repeat, or parallelize stages. If any delegation fails or review does not return the exact approval marker, explain the failure and do not claim success. After an approved review, summarize the plan, implementation, and review evidence, then end with the exact sentinel %s.`, seconds, seconds, reviewApprovalSentinel, seconds, workflowSentinel)
}

func drainRunEvents(events <-chan agentadaptor.RunEvent, wg *sync.WaitGroup) {
	defer wg.Done()
	for range events {
	}
}

// renderLeaderStream surfaces the Claude leader's live output so operators can
// watch it reason, invoke the delegate_to_agent tool, and narrate delegations
// instead of waiting for the final JSON. Delegated role text is streamed
// separately by workflowTrace.
func renderLeaderStream(events <-chan agentadaptor.StreamPayload, wg *sync.WaitGroup) {
	defer wg.Done()
	for ev := range events {
		switch ev.Kind {
		case agentadaptor.StreamTextContent:
			term.Stream("leader (claude)", ev.Delta)
		case agentadaptor.StreamReasoningContent:
			term.Stream("leader (claude) \u00b7 thinking", ev.Delta)
		case agentadaptor.StreamToolCallStart:
			term.Logf("[leader] tool_call.start %s%s", toolLabel(ev.Name, ev.ToolCallID), toolArgsHint(ev.Args))
		case agentadaptor.StreamToolCallArgs:
			term.Stream("leader (claude) \u00b7 tool args", ev.Delta)
		case agentadaptor.StreamToolCallResult:
			term.Logf("[leader] tool_call.result %s", toolLabel(ev.Name, ev.ToolCallID))
		case agentadaptor.StreamSubagentStart:
			if ev.Subagent != nil {
				term.Logf("[leader] subagent.start  %-7s id=%s kind=%s", ev.Subagent.Name, ev.Subagent.ID, ev.Subagent.Kind)
			}
		case agentadaptor.StreamSubagentStatus:
			if ev.Subagent != nil && ev.Delta != "" {
				term.Stream("  \u21b3 "+ev.Subagent.Name, ev.Delta)
			}
		case agentadaptor.StreamSubagentEnd:
			if ev.Subagent != nil {
				term.Logf("[leader] subagent.end    %-7s id=%s", ev.Subagent.Name, ev.Subagent.ID)
			}
		case agentadaptor.StreamRunError:
			if ev.Error != nil {
				term.Logf("[leader] stream error: %s", ev.Error.Message)
			}
		}
	}
}

func toolLabel(name, id string) string {
	if name != "" {
		return name
	}
	return id
}

// toolArgsHint condenses a tool-call argument snapshot into one readable line.
// delegate_to_agent carries a large prompt, so the delegated role key is
// surfaced directly and any other arguments are previewed as compact JSON.
func toolArgsHint(args map[string]any) string {
	if len(args) == 0 {
		return ""
	}
	if agent, ok := args["agent"].(string); ok && strings.TrimSpace(agent) != "" {
		return " agent=" + agent
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return ""
	}
	return " args=" + preview(string(raw), 160)
}

func preview(value string, maxRunes int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	if maxRunes <= 3 {
		return string(runes[:maxRunes])
	}
	return string(runes[:maxRunes-3]) + "..."
}
