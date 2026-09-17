package codex

import (
	"path/filepath"
	"testing"

	"github.com/agent-dance/agent-adaptor/driver"
)

func TestExplicitSkillReferenceSentenceBoundary(t *testing.T) {
	root := t.TempDir()
	skills := []driver.ResolvedSkill{
		{Key: "review", RuntimeName: "review", SourcePath: filepath.Join(root, "review")},
		{Key: "review.deep", RuntimeName: "review.deep", SourcePath: filepath.Join(root, "deep")},
		{Key: "literal-dot", RuntimeName: "literal.", SourcePath: filepath.Join(root, "literal")},
	}
	for _, tt := range []struct {
		name, prompt, want string
	}{
		{"sentence", "Use $review. First call update_plan.", "review"},
		{"trailing period", "$review.", "review"},
		{"ellipsis", "Use $review...", "review"},
		{"dotted name", "$review.deep", "review.deep"},
		{"dotted sentence", "$review.deep.", "review.deep"},
		{"literal trailing dot", "$literal.", "literal."},
		{"literal dotted sentence", "$literal..", "literal."},
		{"duplicate", "$review. Then $review!", "review"},
		{"unknown dotted name", "$review.unknown", ""},
		{"unknown suffix", "$review-unknown.", ""},
		{"word prefix", "x$review.", ""},
		{"catalog only", "Use review.", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			inputs := codexExplicitSkillInputs(tt.prompt, skills)
			if tt.want == "" {
				if len(inputs) != 0 {
					t.Fatalf("unexpected native skill activation: %#v", inputs)
				}
				return
			}
			if len(inputs) != 1 || inputs[0].Name != tt.want || filepath.Base(inputs[0].Path) != "SKILL.md" {
				t.Fatalf("inputs = %#v, want one %q native input", inputs, tt.want)
			}
		})
	}
}

func TestExplicitSkillReferenceExactNamePrecedesSentenceBoundary(t *testing.T) {
	root := t.TempDir()
	skills := []driver.ResolvedSkill{
		{Key: "short", RuntimeName: "review", SourcePath: filepath.Join(root, "short")},
		{Key: "exact", RuntimeName: "review.", SourcePath: filepath.Join(root, "exact")},
	}
	inputs := codexExplicitSkillInputs("$review.", skills)
	if len(inputs) != 1 || inputs[0].Name != "review." || inputs[0].Path != filepath.Join(root, "exact", "SKILL.md") {
		t.Fatalf("exact dotted name lost precedence: %#v", inputs)
	}
	skills = append(skills, driver.ResolvedSkill{Key: "conflict", RuntimeName: "review.", SourcePath: filepath.Join(root, "conflict")})
	if inputs := codexExplicitSkillInputs("$review.", skills); len(inputs) != 0 {
		t.Fatalf("ambiguous exact name fell back to a different skill: %#v", inputs)
	}
}
