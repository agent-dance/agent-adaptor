// threads demonstrates persistent, resume-only, and forked conversation
// Threads on host-owned keys.
//
// The SDK keeps exactly two consumer-visible identity layers: the thread key (the
// host's own business string) and the run ID. The provider session id is
// demoted to an audit detail reachable through th.Checkpoint(ctx).
//
// Usage:
//
//	go run ./examples/threads -agent=codex -key=support/issue-42
package main

import (
	"context"
	"errors"
	"flag"
	"os"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/examples/internal/exampleutil"
	"github.com/agent-dance/agent-adaptor/memory"
)

func main() {
	agent := flag.String("agent", "", "Local CLI agent to use: "+exampleutil.SupportedAgents()+" (default codex, or AGENT_ADAPTOR_EXAMPLE_AGENT)")
	model := flag.String("model", "", "Model to use. Defaults by agent or CODEX_MODEL/CLAUDE_MODEL/CURSOR_MODEL.")
	command := flag.String("command", "", "Optional explicit local CLI command. Defaults by agent or CODEX_COMMAND/CLAUDE_COMMAND/CURSOR_COMMAND/PATH.")
	key := flag.String("key", "examples/sessions", "Host-owned thread key (compose tenant scoping into it as needed)")
	forkKey := flag.String("fork-key", "examples/sessions-fork", "Thread key for the forked branch")
	prompt := flag.String("prompt", "Reply with a short acknowledgement for the sessions example.", "Prompt to send to the selected agent")
	timeout := flag.Duration("timeout", 5*time.Minute, "Maximum time to wait for each run")
	flag.Parse()

	cwd, err := os.Getwd()
	exampleutil.Must(err, "resolve current working directory")
	agentCfg := exampleutil.ResolveLiveAgentConfig(*agent, *model, *command, cwd)

	// WithThreadStore is construction scope only: stateful conversations are
	// part of what the Agent *is*. memory.NewStore() suits a single process;
	// a service swaps in a durable threadstore.Store implementation.
	ai := adaptor.New(
		exampleutil.NewLiveDriver(agentCfg),
		adaptor.WithThreadStore(memory.NewStore()),
		exampleutil.NonInteractive(agentCfg.Agent, adaptor.WorkspaceWrite),
	)

	// 1. continue-or-start: the everyday form. First run starts fresh.
	th := ai.Thread(*key)
	runStep(th, *timeout, *prompt, "first")
	firstCheckpoint := checkpoint(th, *timeout)

	// 2. same key again → the conversation continues; the SDK resumes the
	//    stored driver checkpoint by itself.
	runStep(th, *timeout, *prompt, "second")
	secondCheckpoint := checkpoint(th, *timeout)
	exampleutil.Check(secondCheckpoint != "", "expected the continued run to persist a checkpoint")

	// 3. resume-only: replying inside an existing conversation where starting
	//    over would be a bug. Missing key → ErrThreadNotFound instead of a
	//    silent fresh start.
	resumeOnly := ai.Thread(*key, adaptor.ResumeOnly())
	runStep(resumeOnly, *timeout, *prompt, "resume-only")

	missing := ai.Thread(*key+"/never-created", adaptor.ResumeOnly())
	_, err = runOnce(missing, *timeout, *prompt)
	exampleutil.Check(errors.Is(err, adaptor.ErrThreadNotFound),
		"expected ErrThreadNotFound for an unknown resume-only key, got %v", err)

	// 4. fork: the "try another direction" button. The parent stays intact
	//    and active under its own key.
	fork := th.Fork(*forkKey)
	runStep(fork, *timeout, *prompt, "forked")
	exampleutil.Check(fork.Key() == *forkKey, "expected fork key %q, got %q", *forkKey, fork.Key())

	exampleutil.PrintJSON(map[string]any{
		"example": "sessions",
		"agent":   exampleutil.LiveAgentSummary(agentCfg),
		"threads": map[string]any{
			"key":      th.Key(),
			"fork_key": fork.Key(),
		},
		"checkpoints": map[string]any{
			"after_first":  firstCheckpoint,
			"after_second": secondCheckpoint,
			"fork":         checkpoint(fork, *timeout),
		},
	})
}

func runOnce(r adaptor.Runner, timeout time.Duration, prompt string) (*adaptor.Result, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return r.Run(ctx, prompt)
}

func runStep(r adaptor.Runner, timeout time.Duration, prompt, label string) {
	res, err := runOnce(r, timeout, prompt)
	if err != nil {
		var runErr *adaptor.RunError
		if errors.As(err, &runErr) {
			exampleutil.Fatalf("%s run failed (%s): %s", label, runErr.Reason, runErr.Message)
		}
		exampleutil.Fatalf("%s run: %v", label, err)
	}
	exampleutil.Check(res.RunID != "", "expected a run id from the %s run", label)
}

// checkpoint reads the driver resume handle stored under the thread key. It is
// an audit/debug surface only — the SDK resumes threads without host help.
func checkpoint(th *adaptor.Thread, timeout time.Duration) string {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cp, err := th.Checkpoint(ctx)
	if errors.Is(err, adaptor.ErrThreadNotFound) {
		return ""
	}
	exampleutil.Must(err, "read checkpoint for thread %q", th.Key())
	if cp == nil || cp.State == nil {
		return ""
	}
	return cp.State.ResumeID
}
