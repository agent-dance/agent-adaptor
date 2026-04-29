package exampleutil

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// bannerWidth is the rune width every spotlight banner pads to. Wide enough for
// a 4-line term in 80 cols without wrap, narrow enough to render fine on
// laptop terminals.
const bannerWidth = 70

// PrintStoryBanner writes the "Story" section to stdout and returns the verbatim
// banner text. Callers forward the return value into last-run.md so the static
// walkthrough.md and the dynamic last-run.md share identical phrasing.
func PrintStoryBanner(story, alignedTo string) string {
	body := strings.TrimSpace(story)
	if alignedTo = strings.TrimSpace(alignedTo); alignedTo != "" {
		body += "\n对位：" + alignedTo
	}
	return emitBanner(os.Stdout, "Story", body)
}

// PrintArtifactsBanner writes the "Artifacts" section to stdout and returns the
// banner text. Each item becomes a markdown-style bullet so the same payload is
// readable both on screen and inside last-run.md.
func PrintArtifactsBanner(items []string) string {
	body := bulletize(items)
	return emitBanner(os.Stdout, "Artifacts", body)
}

// PrintTryNextBanner writes the "Try next" section to stdout and returns the
// banner text.
func PrintTryNextBanner(command string) string {
	command = strings.TrimSpace(command)
	body := "(no follow-up)"
	if command != "" {
		body = "$ " + command
	}
	return emitBanner(os.Stdout, "Try next", body)
}

// Bulletize is exported so spotlights can build a body string from a list when
// they need to embed an "Artifacts" snapshot inside a different surface
// (last-run.md, walkthrough.md sample, etc).
func Bulletize(items []string) string {
	return bulletize(items)
}

func bulletize(items []string) string {
	cleaned := make([]string, 0, len(items))
	for _, item := range items {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" {
			continue
		}
		cleaned = append(cleaned, "- "+trimmed)
	}
	if len(cleaned) == 0 {
		return "(none)"
	}
	return strings.Join(cleaned, "\n")
}

func emitBanner(w io.Writer, title, body string) string {
	head := bannerHead(title)
	rendered := head + "\n" + body + "\n"
	if w == nil {
		w = os.Stdout
	}
	fmt.Fprintln(w)
	fmt.Fprint(w, rendered)
	return rendered
}

func bannerHead(title string) string {
	prefix := "━━━ " + title + " "
	width := bannerWidth - utf8RuneLen(prefix)
	if width < 4 {
		width = 4
	}
	return prefix + strings.Repeat("━", width)
}

func utf8RuneLen(s string) int {
	return len([]rune(s))
}
