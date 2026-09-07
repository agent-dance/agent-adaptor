package codex

import (
	"path/filepath"
	"strings"

	"github.com/agent-dance/agent-adaptor/codex/appserver"
	"github.com/agent-dance/agent-adaptor/driver"
)

// Explicit $name references select typed inputs; catalog presence alone does
// not activate a skill, and text never serves as observation evidence.
func codexExplicitSkillInputs(prompt string, skills []driver.ResolvedSkill) []appserver.UserInput {
	byName := make(map[string]driver.ResolvedSkill)
	ambiguous := make(map[string]bool)
	for _, s := range skills {
		if s.RuntimeName == "" || s.Key == "" || s.SourcePath == "" {
			continue
		}
		if old, ok := byName[s.RuntimeName]; ok && (old.Key != s.Key || old.SourcePath != s.SourcePath) {
			ambiguous[s.RuntimeName] = true
		}
		byName[s.RuntimeName] = s
	}
	seen := make(map[string]bool)
	var inputs []appserver.UserInput
	for i := 0; i < len(prompt); i++ {
		if prompt[i] != '$' || i > 0 && codexSkillNameByte(prompt[i-1]) {
			continue
		}
		end := i + 1
		for end < len(prompt) && codexSkillNameByte(prompt[end]) {
			end++
		}
		name := prompt[i+1 : end]
		s, ok := byName[name]
		if !ok || ambiguous[name] || seen[name] {
			continue
		}
		path := filepath.Clean(s.SourcePath)
		if !strings.EqualFold(filepath.Base(path), "SKILL.md") {
			path = filepath.Join(path, "SKILL.md")
		}
		if !filepath.IsAbs(path) {
			continue
		}
		inputs = append(inputs, appserver.UserInput{Type: appserver.UserInputKindSkill, Name: name, Path: path})
		seen[name] = true
		i = end - 1
	}
	return inputs
}
func codexSkillNameByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_' || b == '-' || b == '.'
}
