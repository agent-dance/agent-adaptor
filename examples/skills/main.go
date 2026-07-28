// skills proves runtime skill injection end-to-end with a real local
// CLI: the agent is given a write-proof skill and only follows it if the skill
// actually reached the provider's skill directory.
//
// The skill package provides one-line constructors that produce values
// WithSkills accepts directly:
//
//	skill.Dir("./skills/write-proof")  // a local directory
//	skill.Inline("greet", "# ...")     // literal SKILL.md content
//	skill.Key("code-review")           // resolved by the host SkillProvider
//	skill.Require(s, "reason")         // mandatory: missing => run fails
//
// WithSkills is the one append-merged option: skills passed to Run extend the
// agent defaults rather than replacing them.
//
// It doubles as a callback-form approval demo: the skill wants to
// write a file, so Permission is routed to Ask and adaptor.OnApproval installs
// the host gate. Batch Run has no stream, so the callback is the only approval
// form available here — and the request answers itself, which is why there is
// no request-ID bookkeeping and no ResolveDecision round-trip anywhere below.
//
// Usage:
//
//	go run ./examples/skills -agent=codex
package main

import (
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/examples/internal/exampleutil"
	"github.com/agent-dance/agent-adaptor/profile"
	"github.com/agent-dance/agent-adaptor/skill"
)

const (
	proofSkillName    = "write-proof"
	reviewSkillName   = "review-note"
	proofExpectedText = "WRITE_PROOF_OK"
)

func main() {
	agentName := flag.String("agent", "", "Local CLI agent to use: "+exampleutil.SupportedAgents()+" (default codex, or AGENT_ADAPTOR_EXAMPLE_AGENT)")
	model := flag.String("model", "", "Model to use. Defaults by agent or CODEX_MODEL/CLAUDE_MODEL/CURSOR_MODEL.")
	command := flag.String("command", "", "Optional explicit local CLI command. Defaults by agent or CODEX_COMMAND/CLAUDE_COMMAND/CURSOR_COMMAND/PATH.")
	timeout := flag.Duration("timeout", 5*time.Minute, "Maximum time to wait for the skills example")
	keepWorkspace := flag.Bool("keep-workspace", false, "Keep the temporary workspace after the example finishes")
	flag.Parse()

	workspaceDir, err := os.MkdirTemp("", "agent-adaptor-skills-live-*")
	exampleutil.Must(err, "create temporary workspace")
	if !*keepWorkspace {
		defer func() { _ = os.RemoveAll(workspaceDir) }()
	}

	agentCfg := exampleutil.ResolveLiveAgentConfig(*agentName, *model, *command, workspaceDir)
	proofPath := filepath.Join(workspaceDir, "proof.txt")
	clonedProfileDir := filepath.Join(workspaceDir, agentCfg.Agent+"-profile")

	gate := &approvalGate{}

	ai := adaptor.New(
		exampleutil.NewLiveDriver(agentCfg),
		adaptor.WithProfile(profile.CloneNative(clonedProfileDir,
			profile.CopySettings(), profile.CopyMCP(), profile.CopySkills(), profile.LinkAuth())),
		adaptor.WithIdentity(adaptor.Identity{ID: "skills-agent", Tenant: "examples", Name: "skills-live"}),
		// Agent-level default skills. skill.Dir takes the directory at face
		// value — no catalogue, no provider, no registration step.
		adaptor.WithSkills(
			skill.Dir(exampleSkillDir(proofSkillName)),
			skill.Dir(exampleSkillDir(reviewSkillName)),
		),
		// Ask routes every gate to the host instead of letting the driver
		// resolve it; OnApproval is what turns "ask" into an answer.
		adaptor.WithPolicy(skillsPolicy(agentCfg.Agent)),
		adaptor.OnApproval(gate.decide),
	)

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	listed, err := ai.Inspect().Skills(ctx)
	exampleutil.Must(err, "read the resolved skill set")
	exampleutil.Check(listed.Supported, "expected the %s driver to support skills", agentCfg.Agent)
	exampleutil.Check(len(listed.Selected) == 2, "expected two default skills, got %d", len(listed.Selected))

	// SelectSkills narrows the provider-side selection for subsequent runs.
	selected, err := ai.SelectSkills(ctx, []string{proofSkillName})
	exampleutil.Must(err, "narrow the skill selection")
	exampleutil.Check(len(selected.Selected) == 1 && selected.Selected[0] == proofSkillName,
		"expected the selection to be [%s], got %#v", proofSkillName, selected.Selected)

	prompt := "Use the write-proof skill. Create the file at " + filepath.ToSlash(proofPath) +
		" with exactly this content: " + proofExpectedText + ". Do not modify any other files."

	// Per-call WithSkills appends to the agent defaults; skill.Require marks
	// the skill mandatory, so a materialization failure aborts the run with
	// ErrSkillMaterialization instead of silently producing a skill-less agent.
	res, err := ai.Run(ctx, prompt,
		adaptor.WithSkills(skill.Require(skill.Dir(exampleSkillDir(proofSkillName)), "the proof depends on it")),
		adaptor.WithWorkspace(workspaceDir),
	)
	if err != nil {
		var runErr *adaptor.RunError
		if errors.As(err, &runErr) {
			exampleutil.Fatalf("skills-live run failed (%s): %s", runErr.Reason, runErr.Message)
		}
		exampleutil.Fatalf("run skills-live example: %v", err)
	}

	content, err := os.ReadFile(proofPath)
	exampleutil.Must(err, "read proof output %q; the selected real CLI must follow the write-proof skill instruction", proofPath)
	exampleutil.Check(strings.TrimSpace(string(content)) == proofExpectedText,
		"expected proof file content %q, got %q", proofExpectedText, strings.TrimSpace(string(content)))

	exampleutil.PrintJSON(map[string]any{
		"example":        "skills-live",
		"agent":          exampleutil.LiveAgentSummary(agentCfg),
		"verification":   "Confirmed runtime skill injection by asking the selected real local CLI to use write-proof and create the proof file.",
		"workspace":      workspaceDir,
		"cloned_profile": clonedProfileDir,
		"proof": map[string]any{
			"path":     proofPath,
			"contents": strings.TrimSpace(string(content)),
		},
		"default_skills":  listed.Selected,
		"selected_skills": selected.Selected,
		"approvals":       gate.decisions(),
		"run": map[string]any{
			"run_id":  res.RunID,
			"model":   res.Model,
			"summary": res.Summary,
		},
	})
}

// skillsPolicy demonstrates interactive approvals only where the Driver
// advertises them. Codex and Cursor stay on the portable unattended policy;
// Claude can ask for plan/question input but cannot route permission prompts;
// CodeBuddy supports all three approval kinds.
func skillsPolicy(agent string) adaptor.Policy {
	policy := exampleutil.NonInteractivePolicy(agent, adaptor.WorkspaceWrite)
	switch agent {
	case exampleutil.AgentClaude:
		policy.Approvals.PlanReview = adaptor.ApprovalAsk
		policy.Approvals.Question = adaptor.QuestionAsk
	case exampleutil.AgentCodebuddy:
		policy.Approvals.Permission = adaptor.ApprovalAsk
		policy.Approvals.PlanReview = adaptor.ApprovalAsk
		policy.Approvals.Question = adaptor.QuestionAsk
	}
	return policy
}

// approvalGate is the host side of form A. The handler may be invoked from
// the SDK's goroutine, so the audit trail is guarded; the decision itself is
// a plain method call on the request.
type approvalGate struct {
	mu  sync.Mutex
	log []map[string]any
}

func (g *approvalGate) decide(ctx context.Context, req *adaptor.ApprovalRequest) error {
	outcome := "approved"
	var err error
	switch req.Kind {
	case adaptor.ApprovalQuestion:
		// Answer(ctx, choice) is the Question responder; this example runs
		// unattended, so there is nobody to pick a choice.
		outcome = "denied"
		err = req.Deny(ctx, "the skills example runs unattended")
	default:
		// The run is sandboxed to WorkspaceWrite and the workspace is a
		// throwaway temp dir, so the write the skill needs is safe to grant.
		err = req.Approve(ctx)
	}

	g.mu.Lock()
	g.log = append(g.log, map[string]any{
		"id":       req.ID,
		"kind":     string(req.Kind),
		"title":    req.Title,
		"source":   req.Source,
		"decision": outcome,
	})
	g.mu.Unlock()
	return err
}

func (g *approvalGate) decisions() []map[string]any {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]map[string]any(nil), g.log...)
}

func exampleSkillDir(name string) string {
	_, file, _, ok := runtime.Caller(0)
	exampleutil.Check(ok, "locate current example source")
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "internal", "skills", name))
}
