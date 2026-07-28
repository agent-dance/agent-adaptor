package claude

import (
	"strings"
	"unicode"

	"github.com/agent-dance/agent-adaptor/driver"
)

// claudeInteractiveTools is the whitelist of tool names whose tool_use /
// tool_result frames the claude parser recognizes as HITL decisions.
var claudeInteractiveTools = map[string]driver.HumanDecisionKind{
	"ExitPlanMode":    driver.HumanDecisionPlanReview,
	"AskUserQuestion": driver.HumanDecisionQuestion,
}

// sourceForTool produces the canonical Source label for a given claude tool
// name, e.g. "ExitPlanMode" → "claude.exit_plan_mode".
func sourceForTool(toolName string) string {
	return "claude." + toSnakeCase(toolName)
}

// toSnakeCase converts an UpperCamelCase tool name into snake_case.
// It is deliberately tiny — the tool names are short and fully controlled
// by the vendor, so a general-purpose library would be overkill.
func toSnakeCase(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s) + 4)
	for i, r := range s {
		if unicode.IsUpper(r) {
			if i > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(unicode.ToLower(r))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// interpretClaudeToolResult maps the content of a claude tool_result frame
// for one of the whitelisted interactive tools into a DecisionResult.
//
// Returns ok=false when the tool_result is a CLI-side validation / execution
// failure (wrapped in <tool_use_error>…</tool_use_error>) that happened before
// the UI could even be shown — those are model-authored bugs, not human
// decisions, and must not be reported through the HITL channel.
func interpretClaudeToolResult(content string, isError bool) (driver.DecisionResult, bool) {
	if isError && looksLikeToolUseError(content) {
		// Not a HITL outcome at all: the CLI rejected the model's tool call
		// on schema / validation grounds. The regular tool_call.result
		// stream already shows the error to the host; surfacing a HITL
		// "rejected" card would mis-attribute the failure to the user.
		return "", false
	}
	if isError {
		return driver.DecisionRejected, true
	}
	lower := strings.ToLower(strings.TrimSpace(content))
	switch {
	case strings.HasPrefix(lower, "user approved the plan"),
		strings.HasPrefix(lower, "user approved"),
		strings.Contains(lower, "plan approved"):
		return driver.DecisionApproved, true
	}
	// No explicit error / rejection signal: treat as approved. Callers may
	// log unrecognised content for future-CLI drift.
	return driver.DecisionApproved, true
}

// looksLikeToolUseError detects the wrapper Claude's CLI uses when it
// rejects a tool invocation on validation grounds (schema, option count,
// parameter shape). Historically the CLI prefixes these with
// <tool_use_error> and closes with </tool_use_error>; we match the open
// tag defensively so partial / truncated content still matches.
func looksLikeToolUseError(content string) bool {
	trimmed := strings.TrimSpace(content)
	if strings.HasPrefix(trimmed, "<tool_use_error>") {
		return true
	}
	// Additional loose markers seen in older CLI versions.
	if strings.Contains(trimmed, "InputValidationError") ||
		strings.Contains(trimmed, "ToolUseError") {
		return true
	}
	return false
}
