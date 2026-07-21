package main

import (
	"context"
	"flag"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/examples/internal/exampleutil"
	"github.com/agent-dance/agent-adaptor/memory"
)

func main() {
	agent := flag.String("agent", "", "Local CLI agent to use: "+exampleutil.SupportedAgents()+" (default codex, or AGENT_ADAPTOR_EXAMPLE_AGENT)")
	model := flag.String("model", "", "Model to use. Defaults by agent or CODEX_MODEL/CLAUDE_MODEL/CURSOR_MODEL.")
	command := flag.String("command", "", "Optional explicit local CLI command. Defaults by agent or CODEX_COMMAND/CLAUDE_COMMAND/CURSOR_COMMAND/PATH.")
	namespace := flag.String("namespace", "examples", "Logical session namespace")
	key := flag.String("key", "sessions", "Logical session key")
	forkKey := flag.String("fork-key", "sessions-fork", "Logical session key for the forked branch")
	prompt := flag.String("prompt", "Reply with a short acknowledgement for the sessions example.", "Prompt to send to the selected agent")
	timeout := flag.Duration("timeout", 5*time.Minute, "Maximum time to wait for each run")
	flag.Parse()

	environment, err := exampleutil.NewTemporaryAgentEnvironment("session-continuity")
	exampleutil.Must(err, "create isolated agent environment")
	defer environment.Cleanup()
	agentCfg := exampleutil.ResolveLiveAgentConfig(*agent, *model, *command, environment.WorkspaceDir)
	agentCfg = environment.Configure(agentCfg)

	store := memory.NewSessionStore()
	sdk, err := agentadaptor.Build(
		agentadaptor.WithDefaultAgent(exampleutil.NewLiveAgentBinding(agentCfg, environment.CloneProfileOption())),
		agentadaptor.WithSessionStore(store),
	)
	exampleutil.Must(err, "build sessions example")

	first := runWithTimeout(sdk, agentCfg, *timeout, *prompt, agentadaptor.WithSessionKey(*namespace, *key))
	exampleutil.Check(first.Session != nil, "expected a session from the first run")
	exampleutil.Check(first.Session.Created && !first.Session.Reused, "expected first run to create a new session, got %#v", first.Session)
	exampleutil.Check(first.Session.ID != "", "expected first run session id to be populated")

	second := runWithTimeout(sdk, agentCfg, *timeout, *prompt, agentadaptor.WithSessionKey(*namespace, *key))
	exampleutil.Check(second.Session != nil, "expected a session from the second run")
	exampleutil.Check(second.Session.Reused, "expected second run to reuse the logical session, got %#v", second.Session)
	exampleutil.Check(second.Session.ID == first.Session.ID, "expected second run to reuse session %q, got %q", first.Session.ID, second.Session.ID)

	continued := runWithTimeout(sdk, agentCfg, *timeout, *prompt, agentadaptor.WithContinueSession(first.Session.ID))
	exampleutil.Check(continued.Session != nil && continued.Session.Reused, "expected continue_session to reuse the exact session")
	exampleutil.Check(continued.Session.ID == first.Session.ID, "expected continue_session to use %q, got %q", first.Session.ID, continued.Session.ID)

	restarted := runWithTimeout(sdk, agentCfg, *timeout, *prompt, agentadaptor.WithNewSession(*namespace, *key))
	exampleutil.Check(restarted.Session != nil && restarted.Session.Created, "expected start_new to create a session")
	exampleutil.Check(restarted.Session.ID != first.Session.ID, "expected start_new to create a new session id, got same id %q", restarted.Session.ID)
	exampleutil.Check(restarted.Session.PreviousID == first.Session.ID, "expected start_new previous id %q, got %q", first.Session.ID, restarted.Session.PreviousID)

	forked := runWithTimeout(sdk, agentCfg, *timeout, *prompt, agentadaptor.WithForkSession(first.Session.ID, *namespace, *forkKey))
	exampleutil.Check(forked.Session != nil && forked.Session.Created, "expected fork to create a new session")
	exampleutil.Check(forked.Session.ID != "" && forked.Session.ID != first.Session.ID, "expected fork to create a distinct session id, got %q", forked.Session.ID)
	exampleutil.Check(forked.Session.Key == *forkKey, "expected forked session key %q, got %q", *forkKey, forked.Session.Key)

	exampleutil.PrintJSON(map[string]any{
		"example": "sessions",
		"agent":   exampleutil.LiveAgentSummary(agentCfg),
		"sessions": map[string]any{
			"first": map[string]any{
				"id":      first.Session.ID,
				"created": first.Session.Created,
				"reused":  first.Session.Reused,
			},
			"second": map[string]any{
				"id":      second.Session.ID,
				"created": second.Session.Created,
				"reused":  second.Session.Reused,
			},
			"continued": map[string]any{
				"id":      continued.Session.ID,
				"created": continued.Session.Created,
				"reused":  continued.Session.Reused,
			},
			"restarted": map[string]any{
				"id":          restarted.Session.ID,
				"previous_id": restarted.Session.PreviousID,
			},
			"forked": map[string]any{
				"id":  forked.Session.ID,
				"key": forked.Session.Key,
			},
		},
	})
}

func runWithTimeout(sdk agentadaptor.SDK, agentCfg exampleutil.LiveAgentConfig, timeout time.Duration, prompt string, opts ...agentadaptor.RunOption) agentadaptor.RunResult {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	opts = append(opts, exampleutil.NonInteractiveRunOption(agentadaptor.IsolationReadOnly))
	result, err := sdk.Run(ctx, prompt, opts...)
	exampleutil.Must(err, "run session step")
	exampleutil.Check(result.Failure == nil, "session run failed: %#v", result.Failure)
	exampleutil.Check(result.DriverType == agentCfg.DriverType, "expected driver type %q, got %q", agentCfg.DriverType, result.DriverType)
	exampleutil.Check(result.ExitCode == 0, "expected exit code 0, got %d", result.ExitCode)
	return result
}
