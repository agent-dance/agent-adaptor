package profileagents

import (
	"fmt"
	"strings"

	"github.com/agent-dance/agent-adaptor/driver"
)

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
