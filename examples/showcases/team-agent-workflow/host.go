// Host-side scaffolding for the team-agent-workflow showcase: the temporary
// task fixture, the workspace stage audit, the terminal renderer, and the
// protocol text handed to the leader.
//
// None of this touches the SDK on purpose. It is here to keep main.go about
// the three SDK constructions that matter (Agent, delegation.Service,
// team.Option) and to make the point that everything below is host business
// the SDK deliberately does not model.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// ---- Fixture: a disposable task the team can actually finish ----

// taskFile is the brief every role reads. It is intentionally small, offline
// and unambiguous: the point of the showcase is the orchestration, not the
// difficulty of the work.
const taskFile = "TASK.md"

// solutionFile is the single deliverable. Anything else appearing in the
// workspace is a stage-boundary violation.
const solutionFile = "SOLUTION.md"

const taskContent = `# Task: release checklist

Write ` + solutionFile + ` in this directory. Nothing else in this directory may
be created, modified or deleted.

` + solutionFile + ` must contain, in this order:

1. A first line reading exactly: ` + "`# Release checklist`" + `
2. A section ` + "`## Steps`" + ` with exactly five numbered steps for shipping a
   Go library release: version bump, changelog, tag, build verification, publish.
3. A section ` + "`## Rollback`" + ` with two bullet points.
4. A final line reading exactly: ` + solutionFile + ` READY

## Acceptance checks

- ` + solutionFile + ` exists and is the only file added.
- The four numbered requirements above all hold.
- ` + taskFile + ` is unchanged.
`

type fixture struct {
	root         string
	WorkspaceDir string
	profiles     string
	keep         bool
}

// newFixture builds an isolated temp workspace plus a home for the cloned
// per-agent profiles. Nothing in the user's real project or CLI home is
// touched: profile.CloneNative copies settings and links auth into these
// directories.
func newFixture(keep bool) (*fixture, error) {
	root, err := os.MkdirTemp("", "team-agent-workflow-*")
	if err != nil {
		return nil, err
	}
	f := &fixture{root: root, WorkspaceDir: filepath.Join(root, "workspace"), profiles: filepath.Join(root, "profiles"), keep: keep}
	if err := os.MkdirAll(f.WorkspaceDir, 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(f.profiles, 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(f.WorkspaceDir, taskFile), []byte(taskContent), 0o644); err != nil {
		return nil, err
	}
	return f, nil
}

// ProfileDir is the clone target for one agent. Each role gets its own so the
// CLIs cannot collide over a shared config directory.
func (f *fixture) ProfileDir(key string) string { return filepath.Join(f.profiles, key) }

func (f *fixture) Cleanup(term *console) {
	if f.keep {
		term.Logf("[fixture] kept: %s", f.root)
		return
	}
	if err := os.RemoveAll(f.root); err != nil {
		term.Warnf("cleanup %s: %v", f.root, err)
	}
}

// Validate checks the deliverable itself — the part a human would check.
func (f *fixture) Validate() error {
	raw, err := os.ReadFile(filepath.Join(f.WorkspaceDir, solutionFile))
	if err != nil {
		return fmt.Errorf("deliverable missing: %w", err)
	}
	text := string(raw)
	for _, want := range []string{"# Release checklist", "## Steps", "## Rollback", solutionFile + " READY"} {
		if !strings.Contains(text, want) {
			return fmt.Errorf("%s missing %q", solutionFile, want)
		}
	}
	return nil
}

// ---- Stage audit: who was allowed to touch the workspace ----

// stageAudit fingerprints the workspace after each stage. The team's contract
// is that only the impl role may change files, which is exactly the kind of
// cross-role invariant a host — not the SDK — owns.
type stageAudit struct {
	dir    string
	stages []stageSnapshot
}

type stageSnapshot struct {
	Stage string            `json:"stage"`
	At    time.Time         `json:"at"`
	Files map[string]string `json:"files"`
}

func newStageAudit(dir string) *stageAudit {
	a := &stageAudit{dir: dir}
	a.Record("start")
	return a
}

// Record snapshots the workspace under a stage label. It is wired as the
// observed decorator's completion hook, so a stage is recorded the moment its
// role's run resolves — Run and Stream alike.
func (a *stageAudit) Record(stage string) {
	a.stages = append(a.stages, stageSnapshot{Stage: stage, At: time.Now(), Files: fingerprint(a.dir)})
}

// ValidateStageBoundaries enforces "only impl writes". plan and review are
// constructed with adaptor.ReadOnly, so a violation here means the sandbox
// policy did not hold — worth failing loudly rather than trusting the flag.
func (a *stageAudit) ValidateStageBoundaries() error {
	for i := 1; i < len(a.stages); i++ {
		prev, cur := a.stages[i-1], a.stages[i]
		changed := diffKeys(prev.Files, cur.Files)
		if len(changed) == 0 {
			continue
		}
		if cur.Stage != "impl" {
			return fmt.Errorf("stage %q changed the workspace (%s) but only impl may write", cur.Stage, strings.Join(changed, ", "))
		}
		if slices.Contains(changed, taskFile) {
			return fmt.Errorf("stage impl modified %s, which the task forbids", taskFile)
		}
	}
	return nil
}

func (a *stageAudit) Stages() []stageSnapshot { return a.stages }

func fingerprint(dir string) map[string]string {
	out := map[string]string{}
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // a partially readable workspace is still a usable fingerprint
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		sum := sha256.Sum256(raw)
		out[filepath.ToSlash(rel)] = hex.EncodeToString(sum[:8])
		return nil
	})
	return out
}

func diffKeys(before, after map[string]string) []string {
	var changed []string
	for name, sum := range after {
		if before[name] != sum {
			changed = append(changed, name)
		}
	}
	for name := range before {
		if _, ok := after[name]; !ok {
			changed = append(changed, name+" (deleted)")
		}
	}
	slices.Sort(changed)
	return changed
}

// ---- The leader's protocol ----

// leaderProtocol is the whole orchestration instruction. Note what it does not
// contain: no endpoint, no token, no tool schema, no timeout plumbing. The
// leader discovers delegate_to_agent through the MCP sidecar that team.Option()
// declared, and the Service enforces the budget on its side.
func leaderProtocol(roleTimeout time.Duration) string {
	return fmt.Sprintf(`You lead a three-agent team through one task. You must not create, modify or
delete any file yourself: your only way to change the workspace is the %s tool.

Read %s in your working directory, then delegate three times, in this order,
waiting for each result before the next:

1. %s(agent="plan", objective="<the task, restated, plus: return a numbered plan>")
2. %s(agent="impl", objective="<the plan from step 1, verbatim, plus the task>")
3. %s(agent="review", objective="<the task plus what impl reported>")

Each delegation may take up to %s. Do not delegate to the same agent twice and
do not invent other agent names.

When the review agent approves, reply with a summary of at most 120 words that
contains, on its own final line, exactly: %s
If the review agent rejects, say why instead and do not print that line.`,
		delegateToolLiteral, taskFile,
		delegateToolLiteral, delegateToolLiteral, delegateToolLiteral,
		roleTimeout.Round(time.Second), workflowSentinel)
}

// ---- Terminal rendering ----

// console keeps the interleaved leader/role output readable: leader text
// streams inline, role deltas are prefixed and truncated, and reasoning is
// counted rather than printed so the transcript stays about the work.
type console struct {
	lineOpen  bool
	thinking  int
	tools     int
	subagents map[string]int
}

func newConsole() *console { return &console{subagents: map[string]int{}} }

func (c *console) Print(text string) {
	if text == "" {
		return
	}
	c.lineOpen = !strings.HasSuffix(text, "\n")
	fmt.Print(text)
}

func (c *console) Reasoning(string) { c.thinking++ }

func (c *console) Tool(name string, args map[string]any) {
	c.tools++
	c.Logf("[tool] %s %s", name, preview(fmt.Sprint(args), 160))
}

// Live renders one SubagentUpdate. Started/finished are announced; deltas are
// sampled, because three CLIs streaming at once would otherwise drown the
// leader's own output.
func (c *console) Live(agent, kind, delta string) {
	c.subagents[agent]++
	switch kind {
	case "started":
		c.Logf("[%s] started", agent)
	case "finished":
		c.Logf("[%s] finished (%d events)", agent, c.subagents[agent])
	default:
		if n := c.subagents[agent]; n%25 == 0 && strings.TrimSpace(delta) != "" {
			c.Logf("[%s] %s", agent, preview(delta, 100))
		}
	}
}

func (c *console) Logf(format string, args ...any) {
	c.newline()
	fmt.Printf(format+"\n", args...)
}

func (c *console) Warnf(format string, args ...any) {
	c.newline()
	fmt.Fprintf(os.Stderr, "warn: "+format+"\n", args...)
}

func (c *console) Stats() map[string]any {
	return map[string]any{"thinking_events": c.thinking, "tool_calls": c.tools, "subagent_events": c.subagents}
}

func (c *console) newline() {
	if c.lineOpen {
		fmt.Println()
		c.lineOpen = false
	}
}

// ---- Small helpers ----

func pick(preferred, fallback string) string {
	if strings.TrimSpace(preferred) != "" {
		return preferred
	}
	return fallback
}

func preview(value string, maxRunes int) string {
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes]) + "..."
}
