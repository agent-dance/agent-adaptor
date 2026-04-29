// multi-agent-platform is the agent-adaptor spotlight for products that need
// to host multiple drivers (codex / claude / cursor) inside one process and
// expose a SaaS-style ops dashboard on top of them.
//
// Story: three named agents, three driver types, three clone profiles, one
// Admin API surface. Each binding gets its own identity and its own profile
// directory on disk, then we sweep the entire read-only Admin API for every
// healthy agent and merge the results into one snapshot JSON. SetSelectedSkills
// is exercised on default to prove process-local overrides do not leak into
// other agents.
//
// Artifacts (every run):
//   - Agents Overview unicode table on stdout (one row per role)
//   - Same-prompt routing comparison (default vs review)
//   - Clone profile directory tree (root level per agent)
//   - .spotlight/multi-agent-platform/admin-snapshot.json (full sweep)
//   - .spotlight/multi-agent-platform/last-run.md (dynamic factual mirror)
//
// Unhealthy named agents are MARKED skipped with a reason and never panic;
// the example still produces a valid last-run.md.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/examples/internal/exampleutil"
)

const (
	storyText = "One process, three named agents, three clone profiles, one Admin API surface — your SaaS ops dashboard already has all the fields it needs."
	storyTo   = "internal dev platform · multi-tenant SaaS · team-scoped AI assistant"

	roleDefault   = "default"
	roleReview    = "review"
	roleAutopilot = "autopilot"

	tenantID = "acme"

	skillWriteProof = "write-proof"
	skillReviewNote = "review-note"
)

// roleSpec is the resolved configuration for one agent slot. When Healthy is
// false, the slot becomes a SKIPPED row in the Agents Overview table and a
// `{"status":"skipped","reason":...}` entry in admin-snapshot.json.
type roleSpec struct {
	Role       string
	Agent      string
	Model      string
	Command    string
	Healthy    bool
	SkipReason string

	Cfg      exampleutil.LiveAgentConfig
	Profile  string
	Identity agentadaptor.AgentIdentity
}

func main() {
	defaultAgent := flag.String("default-agent", envOr("MULTIAGENT_DEFAULT_AGENT", "codex"), "Driver hosting the default agent (codex/claude/cursor)")
	reviewAgent := flag.String("review-agent", envOr("MULTIAGENT_REVIEW_AGENT", "claude"), "Driver hosting the review agent")
	autopilotAgent := flag.String("autopilot-agent", envOr("MULTIAGENT_AUTOPILOT_AGENT", "cursor"), "Driver hosting the autopilot agent")
	defaultModel := flag.String("default-model", "", "Model override for default")
	reviewModel := flag.String("review-model", "", "Model override for review")
	autopilotModel := flag.String("autopilot-model", "", "Model override for autopilot")
	defaultCommand := flag.String("default-command", "", "Command override for default")
	reviewCommand := flag.String("review-command", "", "Command override for review")
	autopilotCommand := flag.String("autopilot-command", "", "Command override for autopilot")
	keepProfiles := flag.Bool("keep-profiles", false, "Keep the temporary clone profile directories after the example finishes")
	overall := flag.Duration("timeout", 5*time.Minute, "Overall example timeout")
	flag.Parse()

	cwd, err := os.Getwd()
	exampleutil.Must(err, "resolve cwd")

	profileRoot, err := os.MkdirTemp("", "agent-adaptor-multi-*")
	exampleutil.Must(err, "create temporary profile root")
	if !*keepProfiles {
		defer func() { _ = os.RemoveAll(profileRoot) }()
	}

	specs := []*roleSpec{
		resolveRole(roleDefault, *defaultAgent, *defaultModel, *defaultCommand, profileRoot, cwd),
		resolveRole(roleReview, *reviewAgent, *reviewModel, *reviewCommand, profileRoot, cwd),
		resolveRole(roleAutopilot, *autopilotAgent, *autopilotModel, *autopilotCommand, profileRoot, cwd),
	}

	defaultSpec := specs[0]
	if !defaultSpec.Healthy {
		exampleutil.Fatalf("default agent (%s) is required but is not healthy: %s", defaultSpec.Agent, defaultSpec.SkipReason)
	}

	skillSet := agentadaptor.SkillSet{
		skillWriteProof: {Key: skillWriteProof, Source: agentadaptor.SkillFromPath{Path: locateExampleSkill(skillWriteProof)}},
		skillReviewNote: {Key: skillReviewNote, Source: agentadaptor.SkillFromPath{Path: locateExampleSkill(skillReviewNote)}},
	}

	sdk := agentadaptor.New(buildSDKOptions(specs, skillSet)...)

	ctx, cancel := context.WithTimeout(context.Background(), *overall)
	defer cancel()

	overviewText := renderAgentsOverview(specs, sdk)
	fmt.Println(overviewText)

	comparisonText := runRoutingComparison(ctx, sdk, specs)
	fmt.Println(comparisonText)

	treesText := renderProfileTrees(profileRoot, specs)
	fmt.Println(treesText)

	snapshot := buildAdminSnapshot(ctx, sdk, specs)
	sweepText := renderAdminSweepSummary(specs, snapshot)
	fmt.Println(sweepText)

	isolationText := runSelectionIsolation(ctx, sdk, specs)
	fmt.Println(isolationText)

	spotlightDir := filepath.Join(".spotlight", "multi-agent-platform")
	snapshotPath := filepath.Join(spotlightDir, "admin-snapshot.json")
	exampleutil.Must(writeJSONFile(snapshotPath, snapshot), "write admin snapshot")

	storyBanner := exampleutil.PrintStoryBanner(storyText, storyTo)
	artifactsBanner := exampleutil.PrintArtifactsBanner([]string{
		snapshotPath,
		filepath.Join(spotlightDir, "last-run.md"),
		"examples/multi-agent-platform/walkthrough.md",
		profileRoot,
	})
	tryNextBanner := exampleutil.PrintTryNextBanner("go run ./examples/human-in-the-loop -agent=claude")

	exampleutil.MustWriteLastRunMarkdown(filepath.Join(spotlightDir, "last-run.md"), []exampleutil.LastRunSection{
		{Title: "Story", Body: storyBanner},
		{Title: "Agents overview", Body: exampleutil.FenceCodeBlock("", overviewText)},
		{Title: "Same-prompt routing comparison", Body: exampleutil.FenceCodeBlock("", comparisonText)},
		{Title: "Clone profile trees", Body: exampleutil.FenceCodeBlock("", treesText)},
		{Title: "Admin sweep summary", Body: exampleutil.FenceCodeBlock("", sweepText)},
		{Title: "Selection isolation evidence", Body: exampleutil.FenceCodeBlock("", isolationText)},
		{Title: "Artifacts", Body: artifactsBanner},
		{Title: "Try next", Body: tryNextBanner},
	})
}

// resolveRole probes one driver via DiscoverHealthyAgentCommand. If healthy, it
// builds the LiveAgentConfig + per-role profile dir + identity. If not, it
// returns a SKIPPED spec carrying a human-readable reason.
func resolveRole(role, agent, model, command, profileRoot, cwd string) *roleSpec {
	spec := &roleSpec{
		Role:    role,
		Agent:   strings.TrimSpace(agent),
		Model:   strings.TrimSpace(model),
		Command: strings.TrimSpace(command),
	}
	if spec.Agent == "" {
		spec.SkipReason = "no driver configured"
		return spec
	}

	cmd, _, ok := exampleutil.DiscoverHealthyAgentCommand(spec.Agent, spec.Command)
	if !ok {
		spec.SkipReason = fmt.Sprintf("%s CLI not in PATH (override with -%s-command or %s)", spec.Agent, role, exampleutil.CommandEnvForAgent(spec.Agent))
		return spec
	}
	spec.Healthy = true
	spec.Command = cmd
	if spec.Model == "" {
		spec.Model = exampleutil.DefaultModelForAgent(spec.Agent)
	}
	spec.Cfg = exampleutil.LiveAgentConfig{
		Agent:      spec.Agent,
		DriverType: exampleutil.DriverTypeForAgent(spec.Agent),
		Model:      spec.Model,
		Command:    cmd,
		CWD:        cwd,
	}
	spec.Profile = filepath.Join(profileRoot, role)
	spec.Identity = agentadaptor.AgentIdentity{
		ID:        role + "-" + spec.Agent,
		TenantID:  tenantID,
		ProfileID: role + "-profile",
		Name:      role,
	}
	return spec
}

// buildSDKOptions assembles the SDK construction options. The default role
// becomes WithDefaultAgent; review and autopilot become WithAgent. Skipped
// roles silently drop out (rendered later as SKIPPED rows).
func buildSDKOptions(specs []*roleSpec, skillSet agentadaptor.SkillSet) []agentadaptor.Option {
	opts := []agentadaptor.Option{}
	for _, spec := range specs {
		if !spec.Healthy {
			continue
		}
		bindingOpts := []agentadaptor.AgentOption{
			agentadaptor.WithCloneProfile(spec.Profile, agentadaptor.CloneProfileOptions{
				IncludeSettings: true, IncludeMCP: true, IncludeSkills: true, IncludeAuth: true,
			}),
			agentadaptor.WithDefaultIdentity(spec.Identity),
			agentadaptor.WithDefaultSkills(initialSkillsForRole(spec.Role)...),
		}
		binding := exampleutil.NewLiveAgentBinding(spec.Cfg, bindingOpts...)
		switch spec.Role {
		case roleDefault:
			opts = append(opts, agentadaptor.WithDefaultAgent(binding))
		default:
			opts = append(opts, agentadaptor.WithAgent(spec.Role, binding))
		}
	}
	opts = append(opts, agentadaptor.WithSkillSet(skillSet))
	return opts
}

// initialSkillsForRole gives each role a deliberately distinct skill set so
// the SetSelectedSkills isolation step can show a real per-agent override.
func initialSkillsForRole(role string) []agentadaptor.SkillRef {
	switch role {
	case roleDefault:
		return []agentadaptor.SkillRef{agentadaptor.Key(skillWriteProof), agentadaptor.Key(skillReviewNote)}
	case roleReview:
		return []agentadaptor.SkillRef{agentadaptor.Key(skillReviewNote)}
	case roleAutopilot:
		return []agentadaptor.SkillRef{agentadaptor.Key(skillWriteProof)}
	}
	return nil
}

// ──────────────────────────────────────────────────────────────────────
// Agents Overview table — the SaaS ops dashboard prototype
// ──────────────────────────────────────────────────────────────────────

func renderAgentsOverview(specs []*roleSpec, sdk agentadaptor.SDK) string {
	const (
		wName   = 11
		wDriver = 18
		wTenant = 8
		wEnv    = 9
		wModels = 7
		wQuota  = 7
		wSkills = 8
	)
	type row struct {
		name, driver, tenant, env, models, quota, skills string
	}
	rows := make([]row, 0, len(specs))
	skipReasons := []string{}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for _, spec := range specs {
		if !spec.Healthy {
			rows = append(rows, row{
				name: spec.Role, driver: spec.Agent + "@-", tenant: tenantID,
				env: "skipped", models: "-", quota: "-", skills: "-",
			})
			skipReasons = append(skipReasons, fmt.Sprintf("%s: %s", spec.Role, spec.SkipReason))
			continue
		}
		var admin agentadaptor.AgentAdmin
		var lookupErr error
		if spec.Role == roleDefault {
			admin = sdk.Admin().Default()
		} else {
			admin, lookupErr = sdk.Admin().Agent(spec.Role)
		}
		envText, modelsText, quotaText, skillsText := "?", "?", "?", "?"
		if lookupErr == nil {
			env, err := admin.CheckEnvironment(ctx)
			envText = renderEnvBadge(env, err)
			models, _ := admin.ListModels(ctx)
			modelsText = strconv.Itoa(len(models))
			quota, _ := admin.GetQuota(ctx)
			quotaText = renderQuotaBadge(quota)
			skills, _ := admin.ListSkills(ctx)
			skillsText = renderSkillsBadge(skills)
		}
		rows = append(rows, row{
			name:   spec.Role,
			driver: fmt.Sprintf("%s@%s", spec.Agent, shortModel(spec.Model)),
			tenant: tenantID,
			env:    envText,
			models: modelsText,
			quota:  quotaText,
			skills: skillsText,
		})
	}

	var b strings.Builder
	b.WriteString("Agents Overview\n")
	b.WriteString(borderRow("┌", "┬", "┐", []int{wName, wDriver, wTenant, wEnv, wModels, wQuota, wSkills}))
	b.WriteString(headerRow([]string{"name", "driver@model", "tenant", "env", "models", "quota", "skills"}, []int{wName, wDriver, wTenant, wEnv, wModels, wQuota, wSkills}))
	b.WriteString(borderRow("├", "┼", "┤", []int{wName, wDriver, wTenant, wEnv, wModels, wQuota, wSkills}))
	for _, r := range rows {
		b.WriteString(headerRow([]string{r.name, r.driver, r.tenant, r.env, r.models, r.quota, r.skills}, []int{wName, wDriver, wTenant, wEnv, wModels, wQuota, wSkills}))
	}
	b.WriteString(borderRow("└", "┴", "┘", []int{wName, wDriver, wTenant, wEnv, wModels, wQuota, wSkills}))
	for _, line := range skipReasons {
		fmt.Fprintf(&b, "skipped reason · %s\n", line)
	}
	return b.String()
}

func renderEnvBadge(env agentadaptor.EnvironmentReport, err error) string {
	switch {
	case err != nil:
		return "error"
	case env.Healthy, env.Status == agentadaptor.EnvironmentPass:
		return "healthy"
	case env.Status == "":
		return "unknown"
	default:
		return string(env.Status)
	}
}

func renderQuotaBadge(q agentadaptor.QuotaReport) string {
	if !q.Available {
		return "n/a"
	}
	for _, w := range q.Windows {
		if w.UsedPercent != nil && *w.UsedPercent >= 90 {
			return fmt.Sprintf("%d%%!", *w.UsedPercent)
		}
	}
	return "ok"
}

func renderSkillsBadge(s agentadaptor.SkillSnapshot) string {
	if !s.Supported {
		return "n/a"
	}
	return fmt.Sprintf("%d sel", len(s.Selected))
}

// shortModel collapses long provider model IDs into a host-readable badge
// (`claude-sonnet-4` → `sonnet-4`) so the table column stays aligned.
func shortModel(model string) string {
	if model == "" {
		return "-"
	}
	if strings.HasPrefix(model, "claude-") {
		return strings.TrimPrefix(model, "claude-")
	}
	if r := []rune(model); len(r) > 14 {
		return string(r[:14])
	}
	return model
}

func borderRow(left, mid, right string, widths []int) string {
	var b strings.Builder
	b.WriteString(left)
	for i, w := range widths {
		b.WriteString(strings.Repeat("─", w+2))
		if i < len(widths)-1 {
			b.WriteString(mid)
		}
	}
	b.WriteString(right + "\n")
	return b.String()
}

func headerRow(cells []string, widths []int) string {
	var b strings.Builder
	b.WriteString("│")
	for i, c := range cells {
		b.WriteString(" " + padRight(c, widths[i]) + " │")
	}
	b.WriteString("\n")
	return b.String()
}

func padRight(s string, w int) string {
	rs := []rune(s)
	if len(rs) >= w {
		return string(rs[:w])
	}
	return s + strings.Repeat(" ", w-len(rs))
}

// ──────────────────────────────────────────────────────────────────────
// Same-prompt routing comparison
// ──────────────────────────────────────────────────────────────────────

const routingPrompt = "Reply with one short sentence acknowledging this request."

func runRoutingComparison(ctx context.Context, sdk agentadaptor.SDK, specs []*roleSpec) string {
	type outcome struct {
		role, agent, line string
	}
	results := []outcome{}

	if specs[0].Healthy {
		line := executeRoutingProbe(ctx, sdk.Default(), specs[0].Cfg.Agent)
		results = append(results, outcome{role: roleDefault, agent: specs[0].Agent, line: line})
	}
	if specs[1].Healthy {
		runner, err := sdk.Agent(roleReview)
		if err == nil {
			line := executeRoutingProbe(ctx, runner, specs[1].Cfg.Agent)
			results = append(results, outcome{role: roleReview, agent: specs[1].Agent, line: line})
		}
	}

	var b strings.Builder
	b.WriteString("Same-prompt routing comparison\n")
	fmt.Fprintf(&b, "prompt: %q\n", routingPrompt)
	if len(results) == 0 {
		b.WriteString("(no healthy agents available — comparison skipped)\n")
		return b.String()
	}
	for _, r := range results {
		fmt.Fprintf(&b, "─ %-9s (%s)  ── %s\n", r.role, r.agent, clip(r.line, 90))
	}
	if len(results) == 1 {
		b.WriteString("(review agent skipped — only one row shown; routing visibility limited)\n")
	}
	return b.String()
}

// executeRoutingProbe runs the routing prompt against a Runner and returns the
// first line worth showing the host. If Output is empty (common for an
// unauthenticated CLI), it falls back to stderr_head so the table shows real
// evidence the binary was actually invoked.
func executeRoutingProbe(ctx context.Context, runner agentadaptor.Runner, agent string) string {
	probeCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	result, err := runner.Run(probeCtx, routingPrompt,
		exampleutil.NonInteractiveRunOption(agentadaptor.IsolationReadOnly))
	if err != nil {
		return fmt.Sprintf("[run error: %s]", clip(err.Error(), 80))
	}
	if line := firstLine(result.Output); line != "" {
		return line
	}
	if result.RawStreams != nil {
		if head := firstNonBlank(result.RawStreams.Stderr); head != "" {
			return fmt.Sprintf("[stderr_head] %s", head)
		}
	}
	if result.Failure != nil {
		return fmt.Sprintf("[failure %s] %s", string(result.Failure.Code), result.Failure.Message)
	}
	return fmt.Sprintf("[empty output · exit=%d]", result.ExitCode)
}

// ──────────────────────────────────────────────────────────────────────
// Clone profile directory tree
// ──────────────────────────────────────────────────────────────────────

func renderProfileTrees(profileRoot string, specs []*roleSpec) string {
	var b strings.Builder
	b.WriteString("Clone profile directory trees (tree -L 2)\n")
	fmt.Fprintf(&b, "root: %s\n\n", profileRoot)
	for _, spec := range specs {
		if !spec.Healthy {
			fmt.Fprintf(&b, "%s/  (skipped — no clone profile created)\n\n", spec.Role)
			continue
		}
		fmt.Fprintf(&b, "%s/  (id=%s · profile=%s)\n", spec.Role, spec.Identity.ID, spec.Profile)
		writeTwoLevel(&b, spec.Profile)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// writeTwoLevel walks the dir to depth 2 and prints a compact listing. We cap
// per-directory output to keep the visual budget tight (a clone profile may
// contain a user's full skills catalogue, which would drown the spotlight
// signal otherwise).
func writeTwoLevel(b *strings.Builder, root string) {
	if _, err := os.Stat(root); err != nil {
		fmt.Fprintf(b, "  (unavailable: %v)\n", err)
		return
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		fmt.Fprintf(b, "  (read error: %v)\n", err)
		return
	}
	if len(entries) == 0 {
		b.WriteString("  (empty)\n")
		return
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	const maxChildren = 6
	for _, e := range entries {
		if e.IsDir() {
			child, _ := os.ReadDir(filepath.Join(root, e.Name()))
			if len(child) == 0 {
				fmt.Fprintf(b, "  %s/\n", e.Name())
				continue
			}
			fmt.Fprintf(b, "  %s/\n", e.Name())
			sort.Slice(child, func(i, j int) bool { return child[i].Name() < child[j].Name() })
			shown := 0
			for _, c := range child {
				if shown >= maxChildren {
					fmt.Fprintf(b, "    · …%d more entries\n", len(child)-shown)
					break
				}
				suffix := ""
				if c.IsDir() {
					suffix = "/"
				}
				fmt.Fprintf(b, "    %s%s\n", c.Name(), suffix)
				shown++
			}
		} else {
			fmt.Fprintf(b, "  %s\n", e.Name())
		}
	}
}

// ──────────────────────────────────────────────────────────────────────
// Admin sweep — full read-only API per healthy agent
// ──────────────────────────────────────────────────────────────────────

// agentAdminSnapshot is the per-agent payload landed inside admin-snapshot.json.
// Skipped roles use the Status / Reason fields; healthy roles fill the rest.
type agentAdminSnapshot struct {
	Status       string                          `json:"status,omitempty"`
	Reason       string                          `json:"reason,omitempty"`
	Info         *agentadaptor.AgentInfo         `json:"info,omitempty"`
	Environment  *agentadaptor.EnvironmentReport `json:"environment,omitempty"`
	ModelCount   int                             `json:"model_count,omitempty"`
	Models       []agentadaptor.ModelInfo        `json:"models,omitempty"`
	Profile      *agentadaptor.AgentProfile      `json:"profile,omitempty"`
	ConfigSchema *agentadaptor.ConfigSchema      `json:"config_schema,omitempty"`
	Quota        *agentadaptor.QuotaReport       `json:"quota,omitempty"`
	Skills       *agentadaptor.SkillSnapshot     `json:"skills,omitempty"`
}

func buildAdminSnapshot(ctx context.Context, sdk agentadaptor.SDK, specs []*roleSpec) map[string]agentAdminSnapshot {
	out := map[string]agentAdminSnapshot{}
	for _, spec := range specs {
		if !spec.Healthy {
			out[spec.Role] = agentAdminSnapshot{Status: "skipped", Reason: spec.SkipReason}
			continue
		}
		var admin agentadaptor.AgentAdmin
		var err error
		if spec.Role == roleDefault {
			admin = sdk.Admin().Default()
		} else {
			admin, err = sdk.Admin().Agent(spec.Role)
		}
		if err != nil {
			out[spec.Role] = agentAdminSnapshot{Status: "error", Reason: err.Error()}
			continue
		}
		snap := agentAdminSnapshot{}
		info := admin.Info()
		snap.Info = &info
		if env, err := admin.CheckEnvironment(ctx); err == nil {
			snap.Environment = &env
		}
		if models, err := admin.ListModels(ctx); err == nil {
			snap.Models = models
			snap.ModelCount = len(models)
		}
		if profile, err := admin.GetProfile(ctx); err == nil {
			snap.Profile = &profile
		}
		if schema, err := admin.ConfigSchema(ctx); err == nil {
			snap.ConfigSchema = schema
		}
		if quota, err := admin.GetQuota(ctx); err == nil {
			snap.Quota = &quota
		}
		if skills, err := admin.ListSkills(ctx); err == nil {
			snap.Skills = &skills
		}
		out[spec.Role] = snap
	}
	return out
}

func renderAdminSweepSummary(specs []*roleSpec, snap map[string]agentAdminSnapshot) string {
	var b strings.Builder
	b.WriteString("Admin sweep summary (read-only API surface · per role)\n")
	for _, spec := range specs {
		s := snap[spec.Role]
		if s.Status == "skipped" {
			fmt.Fprintf(&b, "─ %s · skipped · %s\n", spec.Role, s.Reason)
			continue
		}
		if s.Status == "error" {
			fmt.Fprintf(&b, "─ %s · error · %s\n", spec.Role, s.Reason)
			continue
		}
		fmt.Fprintf(&b, "─ %s\n", spec.Role)
		envText := "(unavailable)"
		if s.Environment != nil {
			envText = fmt.Sprintf("status=%s · healthy=%v · checks=%d", s.Environment.Status, s.Environment.Healthy, len(s.Environment.Checks))
		}
		fmt.Fprintf(&b, "  environment  : %s\n", envText)
		fmt.Fprintf(&b, "  models       : %d listed\n", s.ModelCount)
		profileText := "(unavailable)"
		if s.Profile != nil {
			profileText = fmt.Sprintf("supported=%v · dir=%s · source=%s", s.Profile.Supported, clip(s.Profile.Dir, 60), s.Profile.Source)
		}
		fmt.Fprintf(&b, "  profile      : %s\n", profileText)
		schemaText := "(unavailable)"
		if s.ConfigSchema != nil {
			schemaText = fmt.Sprintf("%d fields", len(s.ConfigSchema.Fields))
		}
		fmt.Fprintf(&b, "  config_schema: %s\n", schemaText)
		quotaText := "(unavailable)"
		if s.Quota != nil {
			quotaText = fmt.Sprintf("available=%v · provider=%s · windows=%d", s.Quota.Available, s.Quota.Provider, len(s.Quota.Windows))
		}
		fmt.Fprintf(&b, "  quota        : %s\n", quotaText)
		skillsText := "(unavailable)"
		if s.Skills != nil {
			skillsText = fmt.Sprintf("supported=%v · selected=%v", s.Skills.Supported, s.Skills.Selected)
		}
		fmt.Fprintf(&b, "  skills       : %s\n", skillsText)
	}
	return b.String()
}

// ──────────────────────────────────────────────────────────────────────
// SetSelectedSkills isolation evidence
// ──────────────────────────────────────────────────────────────────────

func runSelectionIsolation(ctx context.Context, sdk agentadaptor.SDK, specs []*roleSpec) string {
	var b strings.Builder
	b.WriteString("Selection isolation evidence (process-local override is per-agent)\n")

	defaultAdmin := sdk.Admin().Default()
	defaultBefore, err := defaultAdmin.ListSkills(ctx)
	if err != nil || !defaultBefore.Supported {
		fmt.Fprintf(&b, "(default skills unsupported — isolation step skipped: %v)\n", err)
		return b.String()
	}

	overrideKey := skillWriteProof
	defaultAfter, err := defaultAdmin.SetSelectedSkills(ctx, []string{overrideKey})
	if err != nil {
		fmt.Fprintf(&b, "(SetSelectedSkills failed: %v)\n", err)
		return b.String()
	}

	fmt.Fprintf(&b, "default.skills.selected (before override): %v\n", defaultBefore.Selected)
	fmt.Fprintf(&b, "default.skills.selected (after  override): %v\n", defaultAfter.Selected)
	if !equalStrings(defaultBefore.Selected, defaultAfter.Selected) {
		b.WriteString("+ default skills changed\n")
	} else {
		b.WriteString("· default skills unchanged (override request did not alter selection)\n")
	}

	if specs[1].Healthy {
		reviewAdmin, err := sdk.Admin().Agent(roleReview)
		if err == nil {
			reviewSkills, err := reviewAdmin.ListSkills(ctx)
			if err == nil && reviewSkills.Supported {
				fmt.Fprintf(&b, "review.skills.selected  (unchanged):       %v\n", reviewSkills.Selected)
				if !sliceContains(reviewSkills.Selected, overrideKey) || equalStrings(reviewSkills.Selected, defaultBefore.Selected) {
					b.WriteString("+ review unchanged · override on default did not bleed across\n")
				}
			}
		}
	} else {
		fmt.Fprintf(&b, "(review agent skipped — cross-agent isolation evidence not shown: %s)\n", specs[1].SkipReason)
	}
	return b.String()
}

// ──────────────────────────────────────────────────────────────────────
// helpers
// ──────────────────────────────────────────────────────────────────────

func envOr(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return fallback
}

func writeJSONFile(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	payload, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	payload = append(payload, '\n')
	return os.WriteFile(path, payload, 0o644)
}

func locateExampleSkill(name string) string {
	_, file, _, ok := runtime.Caller(0)
	exampleutil.Check(ok, "locate current example source")
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "internal", "skills", name))
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

func firstNonBlank(s string) string {
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func clip(s string, max int) string {
	if len([]rune(s)) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max]) + "…"
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sliceContains(items []string, target string) bool {
	for _, v := range items {
		if v == target {
			return true
		}
	}
	return false
}
