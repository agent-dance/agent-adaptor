// Package hitlmock provides a simulated HITL-aware agent used by the
// streaming-chat-copilotkit example. It emits realistic StreamPayload
// sequences (RunStarted / TextStart / tool_call / HITL decision / …) and
// blocks on sink.RequestDecision so the UI can demonstrate a true
// interactive human-in-the-loop flow without depending on Phase 3 vendor
// support.
//
// The driver recognizes simple keywords in the prompt and triggers the
// matching scenario:
//
//   - "plan"        → PlanReview decision card
//   - "question"    → Question decision card
//   - "bash" / "run"→ Permission decision card (tool=Bash)
//   - otherwise     → plain text streaming
//
// The goal is not to replace a real agent; it is to give the example UI a
// reliable, deterministic backdrop to render every HITL v2 affordance.
package hitlmock

import (
	"context"
	"fmt"
	"strings"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

// DriverType identifies the adapter. Hosts may use it for log correlation.
const DriverType = "hitlmock"

// Config is the binding configuration. It has no required fields; a zero
// value is usable. Step delay is tunable so tests can run without wall-clock
// waits.
type Config struct {
	// StreamDelay controls the delay between consecutive text/tool deltas.
	// Zero disables the pacing entirely (useful in tests).
	StreamDelay time.Duration
}

// New returns an AgentBinding for the mock driver. Hosts pass this to
// agentadaptor.New(WithDefaultAgent(hitlmock.New(hitlmock.Config{}))).
func New(cfg Config, opts ...agentadaptor.AgentOption) agentadaptor.AgentBinding {
	return agentadaptor.Bind(adapter{}, cfg, opts...)
}

type adapter struct{}

func (adapter) Descriptor() agentadaptor.DriverDescriptor {
	return agentadaptor.DriverDescriptor{
		Type:        DriverType,
		DisplayName: "HITL Mock",
		RunPolicyCaps: agentadaptor.RunPolicyCapabilities{
			Permission: agentadaptor.HumanDecisionSupport{Ask: true, AutoApprove: true, AutoReject: true, Retry: true},
			PlanReview: agentadaptor.HumanDecisionSupport{Ask: true, AutoApprove: true, AutoReject: true, Retry: true},
			Question:   agentadaptor.QuestionSupport{Ask: true, AutoReject: true, Retry: true},
		},
		Sessions: agentadaptor.SessionCapability{SupportsResume: false},
	}
}

func (adapter) StreamCapability() agentadaptor.StreamCapability {
	return agentadaptor.StreamCapability{
		Native:       true,
		TokenLevel:   true,
		Reasoning:    true,
		ToolCallArgs: true,
		HITL:         true,
	}
}

func (adapter) ValidateConfig(cfg any) error {
	switch cfg.(type) {
	case Config, *Config, nil:
		return nil
	default:
		return fmt.Errorf("hitlmock: expected hitlmock.Config, got %T", cfg)
	}
}

func (adapter) Run(ctx context.Context, req agentadaptor.DriverRunRequest, sink agentadaptor.EventSink) (agentadaptor.DriverRunResult, error) {
	cfg, _ := req.Config.(Config)
	delay := cfg.StreamDelay

	prompt := strings.ToLower(req.Prompt)

	r := newRunState(sink, req.RunID, delay)
	r.emitRunStarted()

	scenario := classify(prompt)
	transcript := make([]agentadaptor.TranscriptItem, 0, 8)

	intro := r.streamText("msg-intro-"+req.RunID, fmt.Sprintf("Let me handle this as a %s scenario.", scenario.label))
	transcript = append(transcript, intro)

	// Per-scenario script. Each branch may call sink.RequestDecision to pause
	// on a HITL gate; the response drives the follow-up text.
	switch scenario.kind {
	case scenarioPlan:
		tr, err := r.playPlanReview(ctx, sink)
		transcript = append(transcript, tr...)
		if err != nil {
			return r.finish(transcript, err)
		}
	case scenarioQuestion:
		tr, err := r.playQuestion(ctx, sink)
		transcript = append(transcript, tr...)
		if err != nil {
			return r.finish(transcript, err)
		}
	case scenarioPermission:
		tr, err := r.playPermission(ctx, sink)
		transcript = append(transcript, tr...)
		if err != nil {
			return r.finish(transcript, err)
		}
	default:
		tr := r.streamText("msg-chat-"+req.RunID, "Nothing interesting to demo today; try prompts with 'plan', 'question', or 'bash'.")
		transcript = append(transcript, tr)
	}

	return r.finish(transcript, nil)
}

// -----------------------------------------------------------------------------
// Scenarios.
// -----------------------------------------------------------------------------

type scenarioKind int

const (
	scenarioChat scenarioKind = iota
	scenarioPlan
	scenarioQuestion
	scenarioPermission
)

type scenarioSpec struct {
	kind  scenarioKind
	label string
}

func classify(prompt string) scenarioSpec {
	switch {
	case strings.Contains(prompt, "plan"):
		return scenarioSpec{scenarioPlan, "plan-review"}
	case strings.Contains(prompt, "question") || strings.Contains(prompt, "ask"):
		return scenarioSpec{scenarioQuestion, "structured-question"}
	case strings.Contains(prompt, "bash") || strings.Contains(prompt, "run ") || strings.Contains(prompt, "exec"):
		return scenarioSpec{scenarioPermission, "permission-gate"}
	default:
		return scenarioSpec{scenarioChat, "chat"}
	}
}

func (r *runState) playPlanReview(ctx context.Context, sink agentadaptor.EventSink) ([]agentadaptor.TranscriptItem, error) {
	out := []agentadaptor.TranscriptItem{}

	plan := "1. Audit AGENTS.md for stale sections.\n2. Migrate the §2.4 checklist to docs/run-policy.md.\n3. Run `go test ./...` to confirm nothing breaks."

	// tool_use start (for observability on the UI tool card)
	r.emitToolCallStart("tool-plan", "ExitPlanMode")
	r.emitToolCallArgs("tool-plan", fmt.Sprintf(`{"plan":%q}`, plan))
	r.emitToolCallEnd("tool-plan")

	resp, err := r.requestDecision(ctx, sink, agentadaptor.DecisionRequest{
		Kind:       agentadaptor.HumanDecisionPlanReview,
		Source:     "hitlmock.exit_plan_mode",
		ToolCallID: "tool-plan",
		Prompt:     "Approve the following plan before I proceed?",
		Payload:    map[string]any{"plan": plan},
		Choices: []agentadaptor.DecisionChoice{
			{Key: "approve", Label: "Approve", Description: "Let me execute this plan"},
			{Key: "reject", Label: "Reject", Description: "Stop, do not apply"},
		},
	})
	if err != nil {
		return out, err
	}

	var summary string
	switch resp.Result {
	case agentadaptor.DecisionApproved:
		summary = "Plan approved. I'd now run the migration steps sequentially."
	case agentadaptor.DecisionRejected:
		summary = "Plan rejected. Aborting without any filesystem change — exactly the guardrail you asked for."
	default:
		summary = fmt.Sprintf("Plan decision: %s.", resp.Result)
	}
	out = append(out, r.streamText("msg-plan-"+r.runID, summary))
	return out, nil
}

func (r *runState) playQuestion(ctx context.Context, sink agentadaptor.EventSink) ([]agentadaptor.TranscriptItem, error) {
	out := []agentadaptor.TranscriptItem{}

	r.emitToolCallStart("tool-ask", "AskUserQuestion")
	r.emitToolCallArgs("tool-ask", `{"prompt":"Which directory should I touch?","choices":[{"key":"docs","label":"docs/","description":"Documentation only"},{"key":"examples","label":"examples/","description":"Demo code"},{"key":"core","label":"Core SDK","description":"Risky — prefer not to without review"}]}`)
	r.emitToolCallEnd("tool-ask")

	resp, err := r.requestDecision(ctx, sink, agentadaptor.DecisionRequest{
		Kind:       agentadaptor.HumanDecisionQuestion,
		Source:     "hitlmock.ask_user_question",
		ToolCallID: "tool-ask",
		Prompt:     "Which directory should I touch?",
		Payload: map[string]any{
			"schema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"directory": map[string]any{"type": "string"},
					"reason":    map[string]any{"type": "string"},
				},
			},
		},
		Choices: []agentadaptor.DecisionChoice{
			{Key: "docs", Label: "docs/", Description: "Documentation only"},
			{Key: "examples", Label: "examples/", Description: "Demo code"},
			{Key: "core", Label: "Core SDK", Description: "Risky — prefer not to without review"},
		},
	})
	if err != nil {
		return out, err
	}

	var summary string
	switch resp.Result {
	case agentadaptor.DecisionAnswered:
		dir, _ := resp.Answer["directory"].(string)
		if dir == "" {
			dir = resp.Choice
		}
		if dir == "" {
			dir = "(unspecified)"
		}
		summary = fmt.Sprintf("Got it — I will limit edits to %s.", dir)
	case agentadaptor.DecisionRejected:
		summary = "OK, I'll continue without the scope hint and fall back to read-only exploration."
	default:
		summary = fmt.Sprintf("Question decision: %s.", resp.Result)
	}
	out = append(out, r.streamText("msg-ask-"+r.runID, summary))
	return out, nil
}

func (r *runState) playPermission(ctx context.Context, sink agentadaptor.EventSink) ([]agentadaptor.TranscriptItem, error) {
	out := []agentadaptor.TranscriptItem{}

	command := "ls -la"

	r.emitToolCallStart("tool-bash", "Bash")
	r.emitToolCallArgs("tool-bash", fmt.Sprintf(`{"command":%q}`, command))
	r.emitToolCallEnd("tool-bash")

	resp, err := r.requestDecision(ctx, sink, agentadaptor.DecisionRequest{
		Kind:       agentadaptor.HumanDecisionPermission,
		Source:     "hitlmock.bash",
		ToolCallID: "tool-bash",
		Prompt:     "Run shell command: " + command,
		Payload: map[string]any{
			"tool":    "Bash",
			"command": command,
		},
		Choices: []agentadaptor.DecisionChoice{
			{Key: "allow", Label: "Allow once", Description: "Run this single command"},
			{Key: "deny", Label: "Deny", Description: "Do not execute"},
		},
	})
	if err != nil {
		return out, err
	}

	var summary string
	switch resp.Result {
	case agentadaptor.DecisionApproved:
		summary = "Permission granted. (Mock) Running: " + command
	case agentadaptor.DecisionRejected:
		summary = "Permission denied. Skipping the command, continuing with a safe alternative."
	default:
		summary = fmt.Sprintf("Permission decision: %s.", resp.Result)
	}
	out = append(out, r.streamText("msg-perm-"+r.runID, summary))
	return out, nil
}

// -----------------------------------------------------------------------------
// run state helpers.
// -----------------------------------------------------------------------------

type runState struct {
	sink   agentadaptor.EventSink
	runID  string
	delay  time.Duration
	usage  *agentadaptor.Usage
}

func newRunState(sink agentadaptor.EventSink, runID string, delay time.Duration) *runState {
	return &runState{sink: sink, runID: runID, delay: delay, usage: &agentadaptor.Usage{}}
}

func (r *runState) base() agentadaptor.StreamPayload {
	return agentadaptor.StreamPayload{RunID: r.runID, ThreadID: r.runID}
}

func (r *runState) emit(p agentadaptor.StreamPayload) {
	_ = r.sink.EmitStream(p)
}

func (r *runState) emitRunStarted() {
	pl := r.base()
	pl.Kind = agentadaptor.StreamRunStarted
	r.emit(pl)
}

func (r *runState) emitToolCallStart(id, name string) {
	pl := r.base()
	pl.Kind = agentadaptor.StreamToolCallStart
	pl.ToolCallID = id
	pl.Name = name
	r.emit(pl)
}

func (r *runState) emitToolCallArgs(id, args string) {
	pl := r.base()
	pl.Kind = agentadaptor.StreamToolCallArgs
	pl.ToolCallID = id
	pl.Delta = args
	r.emit(pl)
}

func (r *runState) emitToolCallEnd(id string) {
	pl := r.base()
	pl.Kind = agentadaptor.StreamToolCallEnd
	pl.ToolCallID = id
	r.emit(pl)
}

// streamText splits s into short chunks so the UI sees genuine token-level
// deltas. Returns the corresponding TranscriptItem for the transcript array.
func (r *runState) streamText(messageID, text string) agentadaptor.TranscriptItem {
	start := r.base()
	start.Kind = agentadaptor.StreamTextStart
	start.MessageID = messageID
	r.emit(start)

	words := strings.Fields(text)
	for i, word := range words {
		delta := word
		if i < len(words)-1 {
			delta += " "
		}
		mid := r.base()
		mid.Kind = agentadaptor.StreamTextContent
		mid.MessageID = messageID
		mid.Delta = delta
		r.emit(mid)
		if r.delay > 0 {
			time.Sleep(r.delay)
		}
	}

	end := r.base()
	end.Kind = agentadaptor.StreamTextEnd
	end.MessageID = messageID
	r.emit(end)

	r.usage.OutputTokens += len(words)
	return agentadaptor.TranscriptItem{
		Kind: agentadaptor.TranscriptAssistant,
		Text: text,
	}
}

func (r *runState) requestDecision(ctx context.Context, sink agentadaptor.EventSink, req agentadaptor.DecisionRequest) (agentadaptor.DecisionResponse, error) {
	ic, ok := sink.(agentadaptor.DecisionCapableSink)
	if !ok {
		// Without a decision-capable sink we fall back to "auto-reject" so
		// the scenario still produces text output. The UI will not see a
		// card in that case (no streaming sink either).
		return agentadaptor.DecisionResponse{Result: agentadaptor.DecisionRejected}, nil
	}
	return ic.RequestDecision(ctx, req)
}

func (r *runState) finish(transcript []agentadaptor.TranscriptItem, err error) (agentadaptor.DriverRunResult, error) {
	done := r.base()
	done.Kind = agentadaptor.StreamRunFinished
	if r.usage != nil {
		u := *r.usage
		done.Usage = &u
	}
	r.emit(done)

	output := ""
	summary := ""
	if n := len(transcript); n > 0 {
		output = transcript[n-1].Text
		summary = transcript[n-1].Text
	}

	result := agentadaptor.DriverRunResult{
		Output:     output,
		RawStreams: &agentadaptor.RawStreams{Stdout: "", Stderr: ""},
		Transcript: transcript,
		ExitCode:   0,
		Usage:      r.usage,
		Provider:   DriverType,
		Model:      "hitlmock-v1",
		Summary:    summary,
	}
	if err != nil {
		// The runner stashes a structured RunFailure (FailureReject /
		// FailureTimeout / FailureCancelled with HumanDecision) via the
		// DecisionCapableSink before returning the abort sentinel. Leave
		// DriverRunResult.Failure nil so the runner overlays its attribution
		// instead of ours — a generic FailureAgentError would hide the
		// HumanDecision metadata the host uses for routing alerts.
		result.ExitCode = 1
	}
	return result, nil
}
