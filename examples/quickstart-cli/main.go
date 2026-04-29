// quickstart-cli is the agent-adaptor spotlight for the smallest possible
// integration: one prompt in, one assistant text out. The whole story fits
// into 12 lines of host code (see 30-second-recipe.md).
//
// Story: render the same RunResult through four panels — Output / Summary /
// RawStreams / Transcript — so the host can see at a glance that §3.4's
// output layering is concrete. Each panel maps to a different surface in a
// host product:
//
//   - Output      → user-facing chat / popup
//   - Summary     → notification feed / Slack title
//   - RawStreams  → audit ingestion (raw protocol bytes)
//   - Transcript  → timeline rendering input (parsed semantic items)
//
// Artifacts (every run):
//   - four-quadrant view on stdout
//   - .spotlight/quickstart-cli/quickstart-cli.json
//   - .spotlight/quickstart-cli/last-run.md
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/examples/internal/exampleutil"
)

const (
	storyText = "Your deploy-bot just got a one-shot answer; pick the layer your product needs to render."
	storyTo   = "deploy-bot · CI step · postcommit hook · git ai-fix"
)

func main() {
	agentFlag := flag.String("agent", "", "Local CLI agent: "+exampleutil.SupportedAgents()+" (default codex, or AGENT_ADAPTOR_EXAMPLE_AGENT)")
	modelFlag := flag.String("model", "", "Model override (default: agent-specific or CODEX_MODEL/CLAUDE_MODEL/CURSOR_MODEL)")
	commandFlag := flag.String("command", "", "Optional explicit local CLI command")
	promptFlag := flag.String("prompt", "Reply with a short acknowledgement for the quickstart example.", "Prompt to send")
	timeoutFlag := flag.Duration("timeout", 3*time.Minute, "Maximum time to wait for the run")
	flag.Parse()

	cwd, err := os.Getwd()
	exampleutil.Must(err, "resolve current working directory")
	agentCfg := exampleutil.ResolveLiveAgentConfig(*agentFlag, *modelFlag, *commandFlag, cwd)

	ctx, cancel := context.WithTimeout(context.Background(), *timeoutFlag)
	defer cancel()

	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(exampleutil.NewLiveAgentBinding(agentCfg)),
	)

	result, err := sdk.Run(ctx, *promptFlag)
	exampleutil.Must(err, "run quickstart")
	exampleutil.Check(result.ExitCode == 0, "expected exit code 0, got %d", result.ExitCode)
	exampleutil.Check(strings.TrimSpace(result.Output) != "", "expected non-empty output from %s", agentCfg.Agent)
	exampleutil.Check(len(result.Transcript) > 0, "expected non-empty transcript")

	spotlightDir := filepath.Join(".spotlight", "quickstart-cli")
	jsonPath := filepath.Join(spotlightDir, "quickstart-cli.json")
	lastRunPath := filepath.Join(spotlightDir, "last-run.md")

	quadView := renderQuadView(result)
	fmt.Print(quadView)

	exampleutil.Must(writeJSONSummary(jsonPath, result), "write JSON summary")

	storyBanner := exampleutil.PrintStoryBanner(storyText, storyTo)
	artifactPaths := []string{
		jsonPath,
		lastRunPath,
		"examples/quickstart-cli/30-second-recipe.md",
		"examples/quickstart-cli/walkthrough.md",
	}
	artifactsBanner := exampleutil.PrintArtifactsBanner(artifactPaths)
	tryNextCmd := "go run ./examples/web-chat-stream -mode=cli -agent=" + agentCfg.Agent
	tryNextBanner := exampleutil.PrintTryNextBanner(tryNextCmd)

	exampleutil.MustWriteLastRunMarkdown(lastRunPath, []exampleutil.LastRunSection{
		{Title: "Story", Body: storyBanner},
		{Title: "Quad view", Body: exampleutil.FenceCodeBlock("", quadView)},
		{Title: "Artifacts", Body: artifactsBanner},
		{Title: "Try next", Body: tryNextBanner},
	})
}

// renderQuadView paints four labelled blocks (Output / Summary / RawStreams /
// Transcript) and returns the captured text so callers can also embed it in
// last-run.md verbatim.
func renderQuadView(result agentadaptor.RunResult) string {
	var b strings.Builder
	b.WriteString(renderBlock("Output", "what your end-user sees", linesFrom(result.Output)))
	b.WriteString("\n")
	b.WriteString(renderBlock("Summary", "what your notification feed shows", linesFrom(result.Summary)))
	b.WriteString("\n")
	b.WriteString(renderRawStreamsBlock(result.RawStreams))
	b.WriteString("\n")
	b.WriteString(renderTranscriptBlock(result.Transcript))
	return b.String()
}

// renderBlock draws one unicode-bordered panel. Each panel has only a top and
// bottom border; the right side is intentionally open so long content does
// not need to be re-wrapped to a fixed column.
func renderBlock(title, caption string, lines []string) string {
	const totalWidth = 70
	header := fmt.Sprintf("┌─ %s ─ %s ", title, caption)
	pad := totalWidth - utf8RuneLen(header)
	if pad < 4 {
		pad = 4
	}
	var b strings.Builder
	b.WriteString(header + strings.Repeat("─", pad) + "\n")
	emitted := 0
	for _, line := range lines {
		if line == "" {
			continue
		}
		for _, sub := range strings.Split(line, "\n") {
			fmt.Fprintf(&b, "│ %s\n", clip(sub, 96))
			emitted++
		}
	}
	if emitted == 0 {
		b.WriteString("│ (empty)\n")
	}
	b.WriteString("└─\n")
	return b.String()
}

func renderRawStreamsBlock(raw *agentadaptor.RawStreams) string {
	if raw == nil {
		return renderBlock("RawStreams.Stdout", "raw protocol bytes (head 3 lines / 0B)", nil)
	}
	headLines, total := head3NonBlank(raw.Stdout)
	caption := fmt.Sprintf("raw protocol bytes (head 3 lines / %dB)", len(raw.Stdout))
	display := append([]string{}, headLines...)
	if total > len(headLines) {
		display = append(display, fmt.Sprintf("... +%d lines", total-len(headLines)))
	}
	return renderBlock("RawStreams.Stdout", caption, display)
}

func renderTranscriptBlock(items []agentadaptor.TranscriptItem) string {
	counts := map[string]int{}
	order := make([]string, 0, len(items))
	for _, it := range items {
		k := string(it.Kind)
		if k == "" {
			k = "(unknown)"
		}
		if _, seen := counts[k]; !seen {
			order = append(order, k)
		}
		counts[k]++
	}
	lines := make([]string, 0, len(order))
	for _, k := range order {
		lines = append(lines, fmt.Sprintf("%s × %d", k, counts[k]))
	}
	return renderBlock("Transcript", "parsed semantic items", lines)
}

func head3NonBlank(s string) ([]string, int) {
	var head []string
	total := 0
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		total++
		if len(head) < 3 {
			head = append(head, line)
		}
	}
	return head, total
}

func writeJSONSummary(path string, r agentadaptor.RunResult) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	transcriptKinds := map[string]int{}
	for _, it := range r.Transcript {
		k := string(it.Kind)
		if k == "" {
			k = "(unknown)"
		}
		transcriptKinds[k]++
	}
	rawStdoutBytes := 0
	if r.RawStreams != nil {
		rawStdoutBytes = len(r.RawStreams.Stdout)
	}
	payload := map[string]any{
		"output":           r.Output,
		"summary":          r.Summary,
		"raw_stdout_bytes": rawStdoutBytes,
		"transcript_kinds": transcriptKinds,
		"driver_type":      r.DriverType,
		"exit_code":        r.ExitCode,
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}

func linesFrom(s string) []string {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return nil
	}
	return []string{trimmed}
}

func clip(s string, max int) string {
	if len([]rune(s)) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max]) + "…"
}

func utf8RuneLen(s string) int {
	return len([]rune(s))
}
