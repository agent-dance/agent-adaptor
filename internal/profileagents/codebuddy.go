package profileagents

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/agent-dance/agent-adaptor/driver"
)

// Native identity must match the resolved catalog exactly. File naming is a
// separate concern: every target is a portable .md path, including SourcePath.
func codeBuddyAgentNames(spec driver.AgentSpec) (string, string, error) {
	name := strings.TrimSpace(spec.RuntimeName)
	if name == "" {
		name = strings.TrimSpace(spec.Key)
	}
	if name == "" || !utf8.ValidString(name) || strings.ContainsRune(name, 0) {
		return "", "", fmt.Errorf("CodeBuddy agent %q: invalid runtime name", spec.Key)
	}
	portable := len(name) <= 120
	for _, ch := range name {
		if !(ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '_') {
			portable = false
		}
	}
	switch name {
	case "con", "prn", "aux", "nul", "com1", "com2", "com3", "com4", "com5", "com6", "com7", "com8", "com9", "lpt1", "lpt2", "lpt3", "lpt4", "lpt5", "lpt6", "lpt7", "lpt8", "lpt9":
		portable = false
	}
	filename := name
	if !portable {
		// '~' is excluded from the unchanged simple-name branch, so encoded
		// names cannot collide with a caller-supplied simple file basename.
		filename = fmt.Sprintf("agent~%x", sha256.Sum256([]byte(name)))
	}
	return name, filename + ".md", nil
}

// CodeBuddy 2.137.1 CustomAgentsProductProvider scans <profile>/agents/*.md.
// parseAgentFile reads these frontmatter fields and uses the Markdown body as
// instructions. Keep this mapping separate from other providers' extensions.
func renderCodeBuddy(spec driver.AgentSpec, name string) (string, error) {
	if unsupported := unsupportedFields("codebuddy", spec); len(unsupported) != 0 {
		return "", fmt.Errorf("CodeBuddy agent %q: %s", spec.Key, strings.Join(unsupported, "; "))
	}
	effort := strings.TrimSpace(spec.ReasoningEffort)
	switch effort {
	case "", "minimal", "low", "medium", "high", "xhigh", "max":
	default:
		return "", fmt.Errorf("CodeBuddy agent %q: unsupported reasoning effort", spec.Key)
	}
	lines := []string{"---", "name: " + yamlString(name), "description: " + yamlString(description(spec))}
	for _, field := range []struct{ name, value string }{{"model", spec.Model}, {"permissionMode", spec.PermissionMode}, {"effort", effort}} {
		if value := strings.TrimSpace(field.value); value != "" {
			lines = append(lines, field.name+": "+yamlString(value))
		}
	}
	if spec.ToolPolicy != nil {
		if len(spec.ToolPolicy.Allow) > 0 {
			lines = append(lines, "tools: "+yamlInlineList(spec.ToolPolicy.Allow))
		}
		if len(spec.ToolPolicy.Deny) > 0 {
			lines = append(lines, "disallowedTools: "+yamlInlineList(spec.ToolPolicy.Deny))
		}
	}
	if len(spec.MCPServers) > 0 {
		lines = append(lines, yamlBlockList("mcpServers", spec.MCPServers)...)
	}
	if len(spec.Skills) > 0 {
		lines = append(lines, yamlBlockList("skills", spec.Skills)...)
	}
	lines = append(lines, "---", "", instructions(spec), "")
	return strings.Join(lines, "\n"), nil
}

func codeBuddyUnsupportedFields(spec driver.AgentSpec) []string {
	var warnings []string
	if strings.TrimSpace(spec.SandboxMode) != "" {
		warnings = append(warnings, "sandbox mode is not mapped for CodeBuddy agents")
	}
	if len(spec.Hooks) > 0 {
		warnings = append(warnings, "agent-local hooks are not mapped for CodeBuddy agents")
	}
	return warnings
}
