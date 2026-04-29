// human-in-the-loop is the agent-adaptor spotlight for compliance / risk gates.
//
// Story: three short plays show how human-in-the-loop decisions land at the
// SDK boundary — sync rejection, async approval, and timeout abort. Three
// drivers participate as a static "capability matrix" so the host can see at
// a glance which providers actually support an Ask channel today (only Claude,
// in v1).
//
// Artifacts (every run):
//   - capability matrix table on stdout
//   - three-scene playback on stdout
//   - .spotlight/human-in-the-loop/audit/session.ndjson (one row per decision)
//   - .spotlight/human-in-the-loop/last-run.md (dynamic factual mirror)
//
// The example never panics on adapters that decline Ask: scenes that cannot
// run are tagged "skipped" with a reason, and scenes whose driver/prompt
// did not actually trigger a decision are tagged "untriggered". Both still
// make sense in walkthrough.md.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/claude"
	"github.com/agent-dance/agent-adaptor/codex"
	"github.com/agent-dance/agent-adaptor/cursor"
	"github.com/agent-dance/agent-adaptor/examples/internal/exampleutil"
)

const (
	storyText = "Three short plays show how your compliance / risk gates land at the SDK boundary."
	storyTo   = "compliance approvals · PR auto-fix · IT change control"
)

func main() {
	agentFlag := flag.String("agent", "", "Local CLI agent: codex / claude / cursor (claude has the richest HITL Ask support)")
	modelFlag := flag.String("model", "", "Model override")
	commandFlag := flag.String("command", "", "Optional explicit local CLI command")
	decisionTimeout := flag.Duration("decision-timeout", 8*time.Second, "Timeout for the timeout-abort scene")
	asyncDelay := flag.Duration("fake-front-end-delay", 2*time.Second, "Async approve delay")
	overall := flag.Duration("timeout", 4*time.Minute, "Overall example timeout")
	flag.Parse()

	cwd, err := os.Getwd()
	exampleutil.Must(err, "resolve cwd")
	agentCfg := exampleutil.ResolveLiveAgentConfig(*agentFlag, *modelFlag, *commandFlag, cwd)

	spotlightDir := filepath.Join(".spotlight", "human-in-the-loop")
	auditPath := filepath.Join(spotlightDir, "audit", "session.ndjson")
	audit := newAuditWriter(auditPath)
	defer audit.Close()

	matrix := buildCapabilityMatrix()
	matrixText := renderCapabilityMatrix(matrix, agentCfg.Agent)
	fmt.Println(matrixText)

	binding := exampleutil.NewLiveAgentBinding(agentCfg)
	caps := binding.Adapter().Descriptor().RunPolicyCaps
	chosen := pickDecisionKind(caps)

	ctx, cancel := context.WithTimeout(context.Background(), *overall)
	defer cancel()

	scenes := []sceneOutcome{
		runSyncRejectScene(ctx, agentCfg, chosen, audit),
		runAsyncApproveScene(ctx, agentCfg, chosen, *asyncDelay, audit),
		runTimeoutAbortScene(ctx, agentCfg, chosen, *decisionTimeout, audit),
	}
	scenesText := renderScenes(scenes)
	fmt.Println(scenesText)

	storyBanner := exampleutil.PrintStoryBanner(storyText, storyTo)

	artifactPaths := []string{
		auditPath,
		filepath.Join(spotlightDir, "last-run.md"),
		"examples/human-in-the-loop/walkthrough.md",
		"examples/human-in-the-loop/audit-schema.md",
	}
	artifactBanner := exampleutil.PrintArtifactsBanner(artifactPaths)

	tryNextCmd := "go run ./examples/task-recipes -agent=" + agentCfg.Agent
	tryNextBanner := exampleutil.PrintTryNextBanner(tryNextCmd)

	exampleutil.MustWriteLastRunMarkdown(filepath.Join(spotlightDir, "last-run.md"), []exampleutil.LastRunSection{
		{Title: "Story", Body: storyBanner},
		{Title: "Capability matrix (driver-truth)", Body: exampleutil.FenceCodeBlock("", matrixText)},
		{Title: "Scenes", Body: exampleutil.FenceCodeBlock("", scenesText)},
		{Title: "Artifacts", Body: artifactBanner},
		{Title: "Try next", Body: tryNextBanner},
	})
}

// driverInfo is one row of the capability matrix.
type driverInfo struct {
	Name    string
	Adapter agentadaptor.DriverAdapter
}

func buildCapabilityMatrix() []driverInfo {
	return []driverInfo{
		{Name: "codex", Adapter: codex.NewAdapter()},
		{Name: "claude", Adapter: claude.NewAdapter()},
		{Name: "cursor", Adapter: cursor.NewAdapter()},
	}
}

func renderCapabilityMatrix(matrix []driverInfo, currentAgent string) string {
	var b strings.Builder
	b.WriteString("Capability matrix (driver-truth, not docs)\n")
	b.WriteString("┌─────────────┬──────────────────┬──────────────────┬──────────────────┐\n")
	b.WriteString("│ driver      │ Permission       │ PlanReview       │ Question         │\n")
	b.WriteString("├─────────────┼──────────────────┼──────────────────┼──────────────────┤\n")
	for _, di := range matrix {
		marker := " "
		if di.Name == currentAgent {
			marker = "●"
		}
		caps := di.Adapter.Descriptor().RunPolicyCaps
		b.WriteString(fmt.Sprintf("│ %s %-9s │ %-16s │ %-16s │ %-16s │\n",
			marker, di.Name,
			renderApprovalCaps(caps.Permission),
			renderApprovalCaps(caps.PlanReview),
			renderQuestionCaps(caps.Question)))
	}
	b.WriteString("└─────────────┴──────────────────┴──────────────────┴──────────────────┘\n")
	b.WriteString("Ask=adapter raises real decision request · Auto=adapter resolves silently · Retry=re-ask supported\n")
	if currentAgent != "" {
		b.WriteString(fmt.Sprintf("● = current -agent (%s)\n", currentAgent))
	}
	return b.String()
}

func renderApprovalCaps(s agentadaptor.HumanDecisionSupport) string {
	parts := []string{}
	if s.Ask {
		parts = append(parts, "Ask")
	}
	if s.AutoApprove {
		parts = append(parts, "Auto")
	}
	if s.AutoReject {
		parts = append(parts, "AutoR")
	}
	if s.Retry {
		parts = append(parts, "Retry")
	}
	if len(parts) == 0 {
		return "(none)"
	}
	return strings.Join(parts, " ")
}

func renderQuestionCaps(s agentadaptor.QuestionSupport) string {
	parts := []string{}
	if s.Ask {
		parts = append(parts, "Ask")
	}
	if s.AutoReject {
		parts = append(parts, "AutoR")
	}
	if s.Retry {
		parts = append(parts, "Retry")
	}
	if len(parts) == 0 {
		return "(none)"
	}
	return strings.Join(parts, " ")
}

// chosenKind binds the example to whichever Ask channel the current driver
// actually supports. Order of preference is Question → PlanReview → none, since
// Question is the most narrowly-scoped surface (a single clarification round)
// and is therefore easiest to demo without elaborate prompt engineering.
type chosenKind struct {
	Kind   agentadaptor.HumanDecisionKind
	Label  string
	Reason string // populated when Kind is empty: tells the host why we skipped
}

func pickDecisionKind(caps agentadaptor.RunPolicyCapabilities) chosenKind {
	if caps.Question.Ask {
		return chosenKind{Kind: agentadaptor.HumanDecisionQuestion, Label: "Question"}
	}
	if caps.PlanReview.Ask {
		return chosenKind{Kind: agentadaptor.HumanDecisionPlanReview, Label: "PlanReview"}
	}
	return chosenKind{Reason: "this adapter declares Ask=false on every decision class; see capability matrix above"}
}

// sceneOutcome captures one scene's verdict for stdout, audit, and last-run.md.
type sceneOutcome struct {
	Title      string
	Mode       string // "rejected" / "approved" / "timed-out" / "untriggered" / "skipped" / "error"
	Reason     string
	Decision   string
	Latency    time.Duration
	OutputHead string
}

func skippedScene(title, reason string) sceneOutcome {
	return sceneOutcome{Title: title, Mode: "skipped", Reason: reason}
}

func untriggeredScene(title, output string) sceneOutcome {
	return sceneOutcome{
		Title:      title,
		Mode:       "untriggered",
		Reason:     "agent did not raise a decision request — prompt may need tuning, the local CLI may be unauthenticated, or this driver may not surface this kind",
		OutputHead: firstLine(output),
	}
}

func errorScene(title string, err error) sceneOutcome {
	return sceneOutcome{Title: title, Mode: "error", Reason: err.Error()}
}

// classifyScene merges three signals into a single sceneOutcome verdict:
//
//  1. Whether a handler / channel was actually invoked (sceneTrigger > 0).
//     Without this, a CLI that exits before raising any decision would be
//     misclassified as success. We treat "no trigger" as Untriggered.
//  2. result.Failure (rejected / timed-out / other).
//  3. Wait() error (cancellation vs. environment failure).
//
// Each scene customizes only its expected outcome; the rest of the
// classification is shared so behavior stays consistent across plays.
func classifyScene(title, expected string, triggered int64, result agentadaptor.RunResult, waitErr error, latency time.Duration, audit *auditWriter, ck chosenKind, resolvedBy string) sceneOutcome {
	if waitErr != nil && !errors.Is(waitErr, context.Canceled) {
		return errorScene(title, waitErr)
	}
	if triggered == 0 {
		// The CLI exited before raising the decision. Surface a short head of
		// the captured output so the host can see what actually happened.
		head := firstLine(result.Output)
		if head == "" && result.RawStreams != nil {
			head = firstLine(result.RawStreams.Stderr)
		}
		return untriggeredScene(title, head)
	}

	// A handler or channel did fire. Now classify by result.Failure.
	switch expected {
	case "rejected":
		if result.Failure != nil && result.Failure.IsRejected() {
			audit.Append(auditEntry{RunID: result.RunID, Kind: string(ck.Kind), Decision: "reject", ResolvedBy: resolvedBy, LatencyMS: latency.Milliseconds(), Note: title})
			return sceneOutcome{Title: title, Mode: "rejected", Decision: "reject", Latency: latency, OutputHead: firstLine(result.Output)}
		}
	case "approved":
		if result.Failure == nil {
			audit.Append(auditEntry{RunID: result.RunID, Kind: string(ck.Kind), Decision: "approve", ResolvedBy: resolvedBy, LatencyMS: latency.Milliseconds(), Note: title})
			return sceneOutcome{Title: title, Mode: "approved", Decision: "approve", Latency: latency, OutputHead: firstLine(result.Output)}
		}
	case "timed-out":
		if result.Failure != nil && result.Failure.IsTimedOut() {
			audit.Append(auditEntry{RunID: result.RunID, Kind: string(ck.Kind), Decision: "timeout", ResolvedBy: resolvedBy, LatencyMS: latency.Milliseconds(), Note: title})
			return sceneOutcome{Title: title, Mode: "timed-out", Decision: "timeout", Latency: latency, OutputHead: firstLine(result.Output)}
		}
	}
	// Triggered but classification didn't match expectation. Report honestly.
	if result.Failure != nil {
		return sceneOutcome{Title: title, Mode: failureMode(result.Failure), Reason: result.Failure.Message, Latency: latency, OutputHead: firstLine(result.Output)}
	}
	return untriggeredScene(title, firstLine(result.Output))
}

// runSyncRejectScene drives a sync handler that always rejects.
func runSyncRejectScene(ctx context.Context, cfg exampleutil.LiveAgentConfig, ck chosenKind, audit *auditWriter) sceneOutcome {
	title := "Scene 1 · Sync Reject"
	if ck.Kind == "" {
		return skippedScene(title, ck.Reason)
	}

	var triggered atomic.Int64
	policy := buildPolicy(ck, agentadaptor.FailureAbort, agentadaptor.FailureAbort, 30*time.Second)
	binding := exampleutil.NewLiveAgentBinding(cfg, sceneRejectHandlers(ck, &triggered)...)
	sdk := agentadaptor.New(agentadaptor.WithDefaultAgent(binding))
	start := time.Now()

	result, err := sdk.Run(ctx, scenePrompt(ck, "reject"),
		agentadaptor.WithRunPolicy(policy),
	)
	latency := time.Since(start)
	return classifyScene(title, "rejected", triggered.Load(), result, err, latency, audit, ck, "sync-handler")
}

// runAsyncApproveScene uses sdk.Start + DecisionRequests channel and resolves
// the first decision after a fake front-end delay.
func runAsyncApproveScene(ctx context.Context, cfg exampleutil.LiveAgentConfig, ck chosenKind, delay time.Duration, audit *auditWriter) sceneOutcome {
	title := "Scene 2 · Async Approve"
	if ck.Kind == "" {
		return skippedScene(title, ck.Reason)
	}

	policy := buildPolicy(ck, agentadaptor.FailureAbort, agentadaptor.FailureAbort, 30*time.Second)
	// No handler at binding level: empty options forces dispatch to channel.
	binding := exampleutil.NewLiveAgentBinding(cfg)
	sdk := agentadaptor.New(agentadaptor.WithDefaultAgent(binding))
	start := time.Now()

	handle, err := sdk.Start(ctx, scenePrompt(ck, "approve"),
		agentadaptor.WithRunPolicy(policy),
	)
	if err != nil {
		return errorScene(title, err)
	}

	var triggered atomic.Int64
	resolved := make(chan struct{})
	go func() {
		defer close(resolved)
		select {
		case req, ok := <-handle.DecisionRequests():
			if !ok {
				return
			}
			triggered.Add(1)
			time.Sleep(delay)
			resp := agentadaptor.DecisionResponse{
				RequestID: req.RequestID,
				Result:    approvalFor(ck.Kind, true),
			}
			if err := handle.ResolveDecision(req.RequestID, resp); err != nil {
				fmt.Fprintf(os.Stderr, "[scene 2] resolve: %v\n", err)
			}
		case <-ctx.Done():
			return
		}
	}()

	result, waitErr := handle.Wait(ctx)
	<-resolved
	latency := time.Since(start)
	return classifyScene(title, "approved", triggered.Load(), result, waitErr, latency, audit, ck, "async-channel")
}

// runTimeoutAbortScene installs a sync handler that blocks past the policy
// timeout, exercising OnTimeout=Abort.
func runTimeoutAbortScene(ctx context.Context, cfg exampleutil.LiveAgentConfig, ck chosenKind, timeout time.Duration, audit *auditWriter) sceneOutcome {
	title := "Scene 3 · Timeout Abort"
	if ck.Kind == "" {
		return skippedScene(title, ck.Reason)
	}

	var triggered atomic.Int64
	policy := buildPolicy(ck, agentadaptor.FailureAbort, agentadaptor.FailureAbort, timeout)
	binding := exampleutil.NewLiveAgentBinding(cfg, sceneSilentHandlers(ck, timeout+10*time.Second, &triggered)...)
	sdk := agentadaptor.New(agentadaptor.WithDefaultAgent(binding))
	start := time.Now()

	result, err := sdk.Run(ctx, scenePrompt(ck, "timeout"),
		agentadaptor.WithRunPolicy(policy),
	)
	latency := time.Since(start)
	return classifyScene(title, "timed-out", triggered.Load(), result, err, latency, audit, ck, "policy")
}

// buildPolicy constructs the full per-run RunPolicy a scene needs. It must
// also set Isolation here, because the example deliberately does NOT layer in
// exampleutil.NonInteractiveRunOption — that helper bakes
// PlanReview=AutoApprove and Question=AutoReject, which would silently undo
// the very Ask channels this spotlight is trying to demonstrate.
func buildPolicy(ck chosenKind, onTimeout, onReject agentadaptor.FailureAction, timeout time.Duration) agentadaptor.RunPolicy {
	hd := agentadaptor.HumanDecisionPolicy{
		Timeout:   timeout,
		OnTimeout: onTimeout,
		OnReject:  onReject,
		// Permission stays Unset → adapter takes its default (AutoApprove for
		// claude/codex/cursor today). The spotlight's Ask plays out on
		// PlanReview or Question only.
	}
	switch ck.Kind {
	case agentadaptor.HumanDecisionQuestion:
		hd.Question = agentadaptor.QuestionAsk
	case agentadaptor.HumanDecisionPlanReview:
		hd.PlanReview = agentadaptor.HumanDecisionAsk
	}
	return agentadaptor.RunPolicy{
		Isolation:     agentadaptor.IsolationReadOnly,
		HumanDecision: hd,
	}
}

// sceneRejectHandlers builds binding-level handler options that always reject
// the inbound decision, regardless of class. trigger increments once per
// handler call so the scene can later distinguish "agent never asked" from
// "host did reject".
func sceneRejectHandlers(ck chosenKind, trigger *atomic.Int64) []agentadaptor.AgentOption {
	switch ck.Kind {
	case agentadaptor.HumanDecisionQuestion:
		return []agentadaptor.AgentOption{agentadaptor.WithDefaultQuestionHandler(rejectQuestionHandler(trigger))}
	case agentadaptor.HumanDecisionPlanReview:
		return []agentadaptor.AgentOption{agentadaptor.WithDefaultPlanReviewHandler(rejectPlanReviewHandler(trigger))}
	}
	return nil
}

// sceneSilentHandlers builds handlers that block longer than the policy
// timeout so the runner always reaches OnTimeout.
func sceneSilentHandlers(ck chosenKind, block time.Duration, trigger *atomic.Int64) []agentadaptor.AgentOption {
	switch ck.Kind {
	case agentadaptor.HumanDecisionQuestion:
		return []agentadaptor.AgentOption{agentadaptor.WithDefaultQuestionHandler(silentQuestionHandler(block, trigger))}
	case agentadaptor.HumanDecisionPlanReview:
		return []agentadaptor.AgentOption{agentadaptor.WithDefaultPlanReviewHandler(silentPlanReviewHandler(block, trigger))}
	}
	return nil
}

func rejectQuestionHandler(trigger *atomic.Int64) agentadaptor.QuestionHandler {
	return func(_ context.Context, _ agentadaptor.QuestionRequest) (agentadaptor.QuestionResponse, error) {
		trigger.Add(1)
		return agentadaptor.QuestionResponse{Result: agentadaptor.QuestionRejected, Text: "host rejected the question"}, nil
	}
}

func rejectPlanReviewHandler(trigger *atomic.Int64) agentadaptor.PlanReviewHandler {
	return func(_ context.Context, _ agentadaptor.PlanReviewRequest) (agentadaptor.PlanReviewResponse, error) {
		trigger.Add(1)
		return agentadaptor.PlanReviewResponse{Result: agentadaptor.ApprovalRejected, Text: "host rejected the plan"}, nil
	}
}

func silentQuestionHandler(block time.Duration, trigger *atomic.Int64) agentadaptor.QuestionHandler {
	return func(ctx context.Context, _ agentadaptor.QuestionRequest) (agentadaptor.QuestionResponse, error) {
		trigger.Add(1)
		select {
		case <-time.After(block):
		case <-ctx.Done():
		}
		return agentadaptor.QuestionResponse{Result: agentadaptor.QuestionRejected}, nil
	}
}

func silentPlanReviewHandler(block time.Duration, trigger *atomic.Int64) agentadaptor.PlanReviewHandler {
	return func(ctx context.Context, _ agentadaptor.PlanReviewRequest) (agentadaptor.PlanReviewResponse, error) {
		trigger.Add(1)
		select {
		case <-time.After(block):
		case <-ctx.Done():
		}
		return agentadaptor.PlanReviewResponse{Result: agentadaptor.ApprovalRejected}, nil
	}
}

// scenePrompt nudges the agent toward emitting the desired decision class.
// All three scenes share the same prompt — the difference between them lives
// entirely in RunPolicy + handler / channel wiring, not in what we ask the
// agent. Adapters and models vary in how literally they comply, so spotlight
// scenes gracefully degrade to "untriggered" when the agent answers directly.
func scenePrompt(ck chosenKind, _ string) string {
	switch ck.Kind {
	case agentadaptor.HumanDecisionQuestion:
		return "I have a small task in mind for you, but I'd like you to ask me one short clarifying question through the official question protocol first to confirm the scope."
	case agentadaptor.HumanDecisionPlanReview:
		return "Help me plan a small task. Please submit a short plan for review before suggesting anything else."
	}
	return "Reply with a short acknowledgement."
}

// approvalFor builds the channel-mode DecisionResult that maps to "approve"
// for whichever decision class is in play.
func approvalFor(kind agentadaptor.HumanDecisionKind, approve bool) agentadaptor.DecisionResult {
	if kind == agentadaptor.HumanDecisionQuestion {
		if approve {
			return agentadaptor.DecisionAnswered
		}
		return agentadaptor.DecisionRejected
	}
	if approve {
		return agentadaptor.DecisionApproved
	}
	return agentadaptor.DecisionRejected
}

// failureMode maps RunFailure to the sceneOutcome mode vocabulary.
func failureMode(f *agentadaptor.RunFailure) string {
	switch {
	case f == nil:
		return ""
	case f.IsRejected():
		return "rejected"
	case f.IsTimedOut():
		return "timed-out"
	default:
		return "error"
	}
}

// renderScenes prints all three plays as a single block so a screenshot
// captures the whole story.
func renderScenes(scenes []sceneOutcome) string {
	var b bytes.Buffer
	for i, s := range scenes {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "━━━ %s ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n", s.Title)
		fmt.Fprintf(&b, "  status   = %s\n", s.Mode)
		if s.Reason != "" {
			fmt.Fprintf(&b, "  reason   = %s\n", s.Reason)
		}
		if s.Decision != "" {
			fmt.Fprintf(&b, "  decision = %s\n", s.Decision)
		}
		if s.Latency > 0 {
			fmt.Fprintf(&b, "  latency  = %s\n", s.Latency.Round(time.Millisecond))
		}
		if s.OutputHead != "" {
			fmt.Fprintf(&b, "  output   = %s\n", clip(s.OutputHead, 96))
		}
	}
	return b.String()
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		return s[:idx]
	}
	return s
}

func clip(s string, max int) string {
	if len([]rune(s)) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max]) + "…"
}

// ──────────────────────────────────────────────────────────────────────
// audit ndjson appender (host-facing schema; see audit-schema.md)
// ──────────────────────────────────────────────────────────────────────

type auditEntry struct {
	Timestamp  time.Time `json:"ts"`
	RunID      string    `json:"run_id,omitempty"`
	Kind       string    `json:"kind"`
	Tool       string    `json:"tool,omitempty"`
	Payload    string    `json:"payload,omitempty"`
	Decision   string    `json:"decision"`
	ResolvedBy string    `json:"resolved_by"`
	LatencyMS  int64     `json:"latency_ms"`
	Note       string    `json:"note,omitempty"`
}

type auditWriter struct {
	path string
	f    *os.File
	mu   sync.Mutex
}

func newAuditWriter(path string) *auditWriter {
	exampleutil.Must(os.MkdirAll(filepath.Dir(path), 0o755), "create audit dir")
	f, err := os.Create(path)
	exampleutil.Must(err, "create audit file %s", path)
	return &auditWriter{path: path, f: f}
}

func (a *auditWriter) Append(entry auditEntry) {
	if a == nil {
		return
	}
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now().UTC()
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	payload, err := json.Marshal(entry)
	exampleutil.Must(err, "marshal audit entry")
	_, err = a.f.Write(append(payload, '\n'))
	exampleutil.Must(err, "write audit entry")
}

func (a *auditWriter) Close() {
	if a == nil || a.f == nil {
		return
	}
	_ = a.f.Close()
}
