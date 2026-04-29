// task-recipes is the agent-adaptor spotlight for "N fixed tasks, each is a
// recipe of skills + instructions + agents + hooks + config". It shows that
// every fixed task in your product becomes one Recipe entry (~10 lines of
// Go) plus a sample prompt.
//
// Story: bind base-coding as the default recipe, sync the profile, then run
// the same agent twice — once with the binding default, once with an
// incident-hotfix recipe layered in via WithProfileResources. Cards, a
// ProfileSnapshot diff, and a before/after directory tree make the overlay
// rules concrete.
//
// Artifacts (every run):
//   - two recipe cards (additive `+` vs replace `↻` markers)
//   - ProfileSnapshot diff (before / after SyncProfile)
//   - clone profile directory tree (before / after SyncProfile)
//   - run outcomes for base-coding and incident-hotfix
//   - .spotlight/task-recipes/last-run.md (dynamic factual mirror)
//
// recipes.go and recipes-cookbook.md sit next to this file as the host-facing
// "copy me" template for declaring new recipes.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/examples/internal/exampleutil"
)

const (
	storyText = "Two recipes: same agent, two task scripts. Each fixed task becomes ~10 lines of Go in recipes.go."
	storyTo   = "incident hotfix bot · scheduled review · data migration · nightly scan · customer triage"
)

func main() {
	agentFlag := flag.String("agent", "", "Local CLI agent: codex / claude / cursor")
	modelFlag := flag.String("model", "", "Model override")
	commandFlag := flag.String("command", "", "Optional explicit local CLI command")
	timeoutFlag := flag.Duration("timeout", 5*time.Minute, "Overall example timeout")
	keepWorkspace := flag.Bool("keep-workspace", false, "Keep the temporary workspace/profile after the example finishes")
	flag.Parse()

	workspaceDir, err := os.MkdirTemp("", "agent-adaptor-task-recipes-*")
	exampleutil.Must(err, "create temp workspace")
	if !*keepWorkspace {
		defer func() { _ = os.RemoveAll(workspaceDir) }()
	}

	agentCfg := exampleutil.ResolveLiveAgentConfig(*agentFlag, *modelFlag, *commandFlag, workspaceDir)
	if agentCfg.Agent == exampleutil.AgentCodex {
		agentCfg.ExtraArgs = append(agentCfg.ExtraArgs, "--skip-git-repo-check")
	}
	profileDir := filepath.Join(workspaceDir, agentCfg.Agent+"-profile")
	instructionsDir := filepath.Join(workspaceDir, "instructions")
	writeText(filepath.Join(instructionsDir, "team-defaults.md"),
		"# Team defaults\n\nPrefer concise answers and mention the active task recipe.")
	writeText(filepath.Join(instructionsDir, "incident-hotfix.md"),
		"# Incident hotfix\n\nFocus on blast radius, rollback options, and customer-visible impact.")

	recipes := Recipes(agentCfg, instructionsDir)

	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(exampleutil.NewLiveAgentBinding(
			agentCfg,
			agentadaptor.WithCloneProfile(profileDir, agentadaptor.CloneProfileOptions{
				IncludeSettings: true, IncludeMCP: true, IncludeSkills: true, IncludeAuth: true,
			}),
			agentadaptor.WithDefaultIdentity(agentadaptor.AgentIdentity{
				ID: "task-recipes", TenantID: "examples", Name: "default",
			}),
			agentadaptor.WithDefaultProfileResources(recipes["base-coding"].Resources),
			agentadaptor.WithDefaultMetadata("example", "task-recipes"),
		)),
		agentadaptor.WithSkillSet(agentadaptor.SkillSet{
			"repo-map":             {Key: "repo-map", Source: agentadaptor.SkillFromInline{SkillMD: "# repo-map\n\nSummarize the repo shape before implementation."}},
			"write-proof":          {Key: "write-proof", Source: agentadaptor.SkillFromPath{Path: locateExampleSkill("write-proof")}},
			"incident-diagnostics": {Key: "incident-diagnostics", Source: agentadaptor.SkillFromInline{SkillMD: "# incident-diagnostics\n\nPrioritize blast radius, rollback, and customer impact."}},
		}),
	)

	ctx, cancel := context.WithTimeout(context.Background(), *timeoutFlag)
	defer cancel()

	admin := sdk.Admin().Default()

	// Capture before / after on both fronts: control-plane snapshot + on-disk
	// profile tree. Hosts repeatedly want to see "did SyncProfile actually
	// write files to my profile dir, or did it only update an in-memory map?"
	beforeSnap, err := admin.ProfileSnapshot(ctx)
	exampleutil.Must(err, "snapshot before")
	beforeTree := captureTree(profileDir)

	afterSnap, err := admin.SyncProfile(ctx)
	exampleutil.Must(err, "sync profile")
	afterTree := captureTree(profileDir)

	cardsText := renderRecipeCards(recipes)
	fmt.Println(cardsText)

	diffText := renderSnapshotDiff(beforeSnap, afterSnap)
	fmt.Println(diffText)

	treesText := renderProfileTrees(profileDir, beforeTree, afterTree)
	fmt.Println(treesText)

	// Run twice: binding default, then per-run override.
	baseResult, err := sdk.Run(ctx, recipes["base-coding"].Prompt,
		exampleutil.NonInteractiveRunOption(agentadaptor.IsolationWorkspaceWrite),
	)
	exampleutil.Must(err, "run base-coding")
	hotfixResult, err := sdk.Run(ctx, recipes["incident-hotfix"].Prompt,
		agentadaptor.WithProfileResources(recipes["incident-hotfix"].Resources),
		agentadaptor.WithMetadata("task_kind", "incident-hotfix"),
		agentadaptor.WithMetadata("request_id", "incident-2026-04-29"),
		exampleutil.NonInteractiveRunOption(agentadaptor.IsolationWorkspaceWrite),
	)
	exampleutil.Must(err, "run incident-hotfix")

	runsText := renderRuns(recipes, baseResult, hotfixResult)
	fmt.Println(runsText)

	storyBanner := exampleutil.PrintStoryBanner(storyText, storyTo)
	artifactPaths := []string{
		filepath.Join(".spotlight", "task-recipes", "last-run.md"),
		"examples/task-recipes/recipes.go",
		"examples/task-recipes/walkthrough.md",
		"examples/task-recipes/recipes-cookbook.md",
	}
	artifactsBanner := exampleutil.PrintArtifactsBanner(artifactPaths)
	tryNextBanner := exampleutil.PrintTryNextBanner("go run ./examples/quickstart-cli -agent=" + agentCfg.Agent)

	exampleutil.MustWriteLastRunMarkdown(filepath.Join(".spotlight", "task-recipes", "last-run.md"),
		[]exampleutil.LastRunSection{
			{Title: "Story", Body: storyBanner},
			{Title: "Recipe cards", Body: exampleutil.FenceCodeBlock("", cardsText)},
			{Title: "ProfileSnapshot diff (after SyncProfile)", Body: exampleutil.FenceCodeBlock("", diffText)},
			{Title: "Profile directory tree (before / after)", Body: exampleutil.FenceCodeBlock("", treesText)},
			{Title: "Run outcomes", Body: exampleutil.FenceCodeBlock("", runsText)},
			{Title: "Artifacts", Body: artifactsBanner},
			{Title: "Try next", Body: tryNextBanner},
		})
}

// ──────────────────────────────────────────────────────────────────────
// Recipe card rendering — the "additive vs replace" overlay made visible
// ──────────────────────────────────────────────────────────────────────

func renderRecipeCards(recipes map[string]Recipe) string {
	// Stable order: base first, then everything else alphabetically. Hosts
	// reading the cards expect "base / default" to come first.
	names := keysInOrder(recipes, "base-coding")
	var b strings.Builder
	for i, name := range names {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(renderRecipeCard(recipes[name]))
	}
	return b.String()
}

func renderRecipeCard(r Recipe) string {
	var b strings.Builder
	prefix := "+"
	if r.Trigger == "per-run via WithProfileResources" {
		prefix = "↻"
	}
	fmt.Fprintf(&b, "┌─ Recipe · %s %s\n", r.Name, lineFill(56-len(r.Name)))
	fmt.Fprintf(&b, "│ description : %s\n", r.Description)
	fmt.Fprintf(&b, "│ skills      : %s %s\n", prefix, joinSkillRefs(r.Resources.Skills))
	fmt.Fprintf(&b, "│ agents      : %s %s\n", prefix, joinAgentSpecs(r.Resources.Agents))
	fmt.Fprintf(&b, "│ hooks       : %s %s\n", prefix, joinHookSpecs(r.Resources.Hooks))
	if r.Resources.Instructions != nil {
		ref := r.Resources.Instructions
		fmt.Fprintf(&b, "│ instructions: %s %s · scope=%s · mode=%s\n", prefix, ref.ID, defaultStr(string(ref.Scope), "default"), defaultStr(string(ref.Mode), "additive"))
	} else {
		fmt.Fprintf(&b, "│ instructions: (none)\n")
	}
	fmt.Fprintf(&b, "│ config      : %s %s\n", prefix, joinConfigPatches(r.Resources.Config))
	fmt.Fprintf(&b, "│ trigger     : %s\n", r.Trigger)
	b.WriteString("└─\n")
	return b.String()
}

func joinSkillRefs(refs []agentadaptor.SkillRef) string {
	if len(refs) == 0 {
		return "(none)"
	}
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		switch v := ref.(type) {
		case agentadaptor.SkillKey:
			out = append(out, string(v))
		case agentadaptor.Skill:
			out = append(out, v.Key)
		default:
			out = append(out, "<custom>")
		}
	}
	return strings.Join(out, ", ")
}

func joinAgentSpecs(specs []agentadaptor.AgentSpec) string {
	if len(specs) == 0 {
		return "(none)"
	}
	out := make([]string, 0, len(specs))
	for _, s := range specs {
		out = append(out, s.Key)
	}
	return strings.Join(out, ", ")
}

func joinHookSpecs(specs []agentadaptor.HookSpec) string {
	if len(specs) == 0 {
		return "(none)"
	}
	out := make([]string, 0, len(specs))
	for _, s := range specs {
		label := s.Key
		if s.Disabled {
			label += " (disabled)"
		}
		out = append(out, label)
	}
	return strings.Join(out, ", ")
}

func joinConfigPatches(patches []agentadaptor.ProfileConfigPatch) string {
	if len(patches) == 0 {
		return "(none)"
	}
	out := make([]string, 0, len(patches))
	for _, p := range patches {
		out = append(out, fmt.Sprintf("%s/%s", p.Capability, p.Key))
	}
	return strings.Join(out, ", ")
}

// ──────────────────────────────────────────────────────────────────────
// ProfileSnapshot diff — what SyncProfile actually changed
// ──────────────────────────────────────────────────────────────────────

func renderSnapshotDiff(before, after agentadaptor.ProfileSnapshot) string {
	var b strings.Builder
	b.WriteString("ProfileSnapshot diff (after SyncProfile)\n")
	beforeIdx := indexResources(before.Resources)
	afterIdx := indexResources(after.Resources)
	kinds := mergeKindKeys(beforeIdx, afterIdx)
	for _, kind := range kinds {
		b.WriteString(string(kind) + "/\n")
		bRes, beforeOK := beforeIdx[kind]
		aRes, afterOK := afterIdx[kind]
		switch {
		case !beforeOK && afterOK:
			for _, m := range aRes.Managed {
				fmt.Fprintf(&b, "  + %s\n", m)
			}
			if len(aRes.Managed) == 0 && aRes.Fingerprint != "" {
				fmt.Fprintf(&b, "  + (fingerprint=%s, no managed entries)\n", aRes.Fingerprint)
			}
		case beforeOK && !afterOK:
			for _, m := range bRes.Managed {
				fmt.Fprintf(&b, "  - %s\n", m)
			}
		default:
			diffManaged(&b, bRes.Managed, aRes.Managed)
			if bRes.Fingerprint != aRes.Fingerprint {
				fmt.Fprintf(&b, "  · fingerprint: %s → %s\n", short(bRes.Fingerprint), short(aRes.Fingerprint))
			}
		}
	}
	if len(kinds) == 0 {
		b.WriteString("  (no resource categories reported)\n")
	}
	if len(after.Warnings) > 0 {
		b.WriteString("warnings:\n")
		for _, w := range after.Warnings {
			fmt.Fprintf(&b, "  ! %s\n", w)
		}
	}
	return b.String()
}

func indexResources(resources []agentadaptor.ResourceSnapshot) map[agentadaptor.ProfileResourceKind]agentadaptor.ResourceSnapshot {
	out := map[agentadaptor.ProfileResourceKind]agentadaptor.ResourceSnapshot{}
	for _, r := range resources {
		out[r.Kind] = r
	}
	return out
}

func mergeKindKeys(a, b map[agentadaptor.ProfileResourceKind]agentadaptor.ResourceSnapshot) []agentadaptor.ProfileResourceKind {
	seen := map[agentadaptor.ProfileResourceKind]struct{}{}
	for k := range a {
		seen[k] = struct{}{}
	}
	for k := range b {
		seen[k] = struct{}{}
	}
	out := make([]agentadaptor.ProfileResourceKind, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return string(out[i]) < string(out[j]) })
	return out
}

func diffManaged(b *strings.Builder, before, after []string) {
	beforeSet := map[string]struct{}{}
	for _, v := range before {
		beforeSet[v] = struct{}{}
	}
	afterSet := map[string]struct{}{}
	for _, v := range after {
		afterSet[v] = struct{}{}
	}
	all := append([]string{}, before...)
	for _, v := range after {
		if _, ok := beforeSet[v]; !ok {
			all = append(all, v)
		}
	}
	sort.Strings(all)
	for _, v := range all {
		_, hadBefore := beforeSet[v]
		_, hasAfter := afterSet[v]
		switch {
		case !hadBefore && hasAfter:
			fmt.Fprintf(b, "  + %s\n", v)
		case hadBefore && !hasAfter:
			fmt.Fprintf(b, "  - %s\n", v)
		default:
			fmt.Fprintf(b, "  · %s\n", v)
		}
	}
}

func short(fp string) string {
	if len(fp) <= 12 {
		return fp
	}
	return fp[:12] + "…"
}

// ──────────────────────────────────────────────────────────────────────
// Profile directory tree — show that recipes are real files on disk
// ──────────────────────────────────────────────────────────────────────

type treeEntry struct {
	Path  string // relative path
	IsDir bool
}

func captureTree(root string) []treeEntry {
	var out []treeEntry
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return out
	}
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if path == root {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		out = append(out, treeEntry{Path: rel, IsDir: d.IsDir()})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func renderProfileTrees(root string, before, after []treeEntry) string {
	var b strings.Builder
	b.WriteString("Profile directory tree (clone profile · diff view)\n")
	fmt.Fprintf(&b, "root: %s\n\n", root)
	b.WriteString("before SyncProfile (root level only):\n")
	writeRootOnly(&b, before)
	b.WriteString("\nafter SyncProfile (+ = added by recipe):\n")
	writeDiffTree(&b, before, after)
	return b.String()
}

// writeRootOnly prints just the depth-0 entries, with subtree counts. The
// idea is to acknowledge "the clone profile starts non-empty" without
// drowning the reader in user-local skill catalogs.
func writeRootOnly(b *strings.Builder, entries []treeEntry) {
	if len(entries) == 0 {
		b.WriteString("  (empty)\n")
		return
	}
	const sep = string(filepath.Separator)
	subtreeCount := map[string]int{}
	rootEntries := []treeEntry{}
	for _, e := range entries {
		depth := strings.Count(e.Path, sep)
		if depth == 0 {
			rootEntries = append(rootEntries, e)
			continue
		}
		// attribute every nested entry to its top-level ancestor
		top := strings.SplitN(e.Path, sep, 2)[0]
		subtreeCount[top]++
	}
	for _, e := range rootEntries {
		name := filepath.Base(e.Path)
		if e.IsDir {
			fmt.Fprintf(b, "  %s/  [%d nested entries]\n", name, subtreeCount[e.Path])
		} else {
			fmt.Fprintf(b, "  %s\n", name)
		}
	}
}

// writeDiffTree renders the post-sync tree but only the parts that changed.
// Pre-existing directories are kept as anchors (so the user can see context),
// pre-existing leaves are collapsed into a "[N pre-existing files]" trailer,
// and recipe-driven additions are explicitly marked with `+`.
func writeDiffTree(b *strings.Builder, before, after []treeEntry) {
	if len(after) == 0 {
		b.WriteString("  (empty)\n")
		return
	}
	const (
		maxDepth = 3
		sep      = string(filepath.Separator)
	)
	beforeSet := map[string]struct{}{}
	for _, e := range before {
		beforeSet[e.Path] = struct{}{}
	}
	parentOf := func(p string) string {
		dir := filepath.Dir(p)
		if dir == "." || dir == "" {
			return ""
		}
		return dir
	}

	addedFiles := map[string]int{}
	preExistingFiles := map[string]int{}
	rootAdded := 0
	rootPre := 0
	for _, e := range after {
		if e.IsDir {
			continue
		}
		parent := parentOf(e.Path)
		_, existed := beforeSet[e.Path]
		if existed {
			if parent == "" {
				rootPre++
			} else {
				preExistingFiles[parent]++
			}
		} else {
			if parent == "" {
				rootAdded++
			} else {
				addedFiles[parent]++
			}
		}
	}

	if rootAdded > 0 {
		fmt.Fprintf(b, "+ [%d new files at root]\n", rootAdded)
	}
	if rootPre > 0 {
		fmt.Fprintf(b, "  [%d pre-existing files at root]\n", rootPre)
	}

	// Compute "subtree contains any added entry" so we can collapse the
	// entirely-pre-existing siblings into a single line per parent.
	subtreeHasAdded := map[string]bool{}
	for _, e := range after {
		if _, existed := beforeSet[e.Path]; existed {
			continue
		}
		cur := e.Path
		for cur != "" && cur != "." {
			subtreeHasAdded[cur] = true
			cur = parentOf(cur)
		}
		subtreeHasAdded[""] = true
	}

	collapsedSiblings := map[string]int{} // parent → count of fully-pre-existing subdirs collapsed under it
	collapsedAncestors := map[string]bool{}

	emitParentTrailer := func(parent string, depth int) {
		if collapsedSiblings[parent] == 0 {
			return
		}
		indent := strings.Repeat("  ", depth)
		fmt.Fprintf(b, "%s· [%d pre-existing subdirectories collapsed]\n", indent, collapsedSiblings[parent])
		// Mark "done" so we don't double-emit if we revisit the parent.
		collapsedSiblings[parent] = 0
	}

	// First pass: identify pre-existing-only siblings.
	for _, e := range after {
		if !e.IsDir {
			continue
		}
		_, existed := beforeSet[e.Path]
		if existed && !subtreeHasAdded[e.Path] {
			collapsedSiblings[parentOf(e.Path)]++
		}
	}

	// Second pass: render. For each visited parent, emit the trailer once
	// when we transition out of its scope.
	var lastParent string
	for _, e := range after {
		if !e.IsDir {
			continue
		}
		depth := strings.Count(e.Path, sep)
		if depth >= maxDepth {
			continue
		}
		// Skip rendering if any ancestor was already collapsed.
		anc := parentOf(e.Path)
		skipped := false
		for anc != "" {
			if collapsedAncestors[anc] {
				skipped = true
				break
			}
			anc = parentOf(anc)
		}
		if skipped {
			continue
		}

		parent := parentOf(e.Path)
		if parent != lastParent {
			// Switched parent — flush the trailer of the previous one.
			emitParentTrailer(lastParent, depth)
			lastParent = parent
		}

		_, existed := beforeSet[e.Path]
		// If this directory is fully pre-existing (no recipe-driven additions
		// inside it), it has been counted into collapsedSiblings; do not
		// render the directory itself.
		if existed && !subtreeHasAdded[e.Path] {
			collapsedAncestors[e.Path] = true
			continue
		}

		indent := strings.Repeat("  ", depth)
		name := filepath.Base(e.Path)
		marker := "  "
		if !existed {
			marker = "+ "
		}
		added := addedFiles[e.Path]
		pre := preExistingFiles[e.Path]
		switch {
		case added > 0 && pre > 0:
			fmt.Fprintf(b, "%s%s%s/  [+%d new, %d pre-existing files]\n", indent, marker, name, added, pre)
		case added > 0:
			fmt.Fprintf(b, "%s%s%s/  [+%d new files]\n", indent, marker, name, added)
		case pre > 0:
			fmt.Fprintf(b, "%s%s%s/  [%d pre-existing files]\n", indent, marker, name, pre)
		default:
			fmt.Fprintf(b, "%s%s%s/\n", indent, marker, name)
		}
	}
	emitParentTrailer(lastParent, 1) // flush the last group at indent depth=1
}

// ──────────────────────────────────────────────────────────────────────
// Run outcomes
// ──────────────────────────────────────────────────────────────────────

func renderRuns(recipes map[string]Recipe, base, hotfix agentadaptor.RunResult) string {
	var b strings.Builder
	b.WriteString("Run outcomes\n")
	for _, row := range []struct {
		recipe string
		result agentadaptor.RunResult
	}{
		{"base-coding", base},
		{"incident-hotfix", hotfix},
	} {
		r := row.result
		fmt.Fprintf(&b, "─ %s\n", row.recipe)
		fmt.Fprintf(&b, "  driver_type = %s\n", r.DriverType)
		fmt.Fprintf(&b, "  exit_code   = %d\n", r.ExitCode)
		out := strings.TrimSpace(r.Output)
		if out != "" {
			fmt.Fprintf(&b, "  output      = %s\n", clip(out, 96))
		}
		if r.Failure != nil {
			fmt.Fprintf(&b, "  failure     = %s · %s\n", string(r.Failure.Code), r.Failure.Message)
		}
		// Surface stderr head when the run looks broken so reviewers see WHY
		// the recipe failed without grepping the raw streams.
		if (r.ExitCode != 0 || out == "") && r.RawStreams != nil {
			if head := firstNonBlank(r.RawStreams.Stderr); head != "" {
				fmt.Fprintf(&b, "  stderr_head = %s\n", clip(head, 96))
			}
		}
	}
	return b.String()
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

// ──────────────────────────────────────────────────────────────────────
// helpers
// ──────────────────────────────────────────────────────────────────────

func writeText(path, content string) {
	exampleutil.Must(os.MkdirAll(filepath.Dir(path), 0o755), "create %q", path)
	exampleutil.Must(os.WriteFile(path, []byte(content+"\n"), 0o644), "write %q", path)
}

func locateExampleSkill(name string) string {
	_, file, _, ok := runtime.Caller(0)
	exampleutil.Check(ok, "locate current example source")
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "internal", "skills", name))
}

func keysInOrder(m map[string]Recipe, first ...string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(m))
	for _, k := range first {
		if _, ok := m[k]; ok {
			out = append(out, k)
			seen[k] = struct{}{}
		}
	}
	rest := make([]string, 0, len(m))
	for k := range m {
		if _, ok := seen[k]; !ok {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	return append(out, rest...)
}

func defaultStr(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
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

func lineFill(n int) string {
	if n < 0 {
		n = 0
	}
	return strings.Repeat("─", n)
}
