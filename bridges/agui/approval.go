package agui

import (
	"strings"

	adaptor "github.com/agent-dance/agent-adaptor/next"
)

// DecisionMode selects how adaptor approval events are represented on the
// AG-UI wire. Tool-call lifecycles are the interoperable default; CustomEvent
// remains an explicit wire projection for hosts with a custom renderer.
type DecisionMode int

const (
	DecisionAsToolCall DecisionMode = iota
	DecisionAsCustom
)

const decisionToolPrefix = "dec-"

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func hitlToolName(kind, source string) string {
	parts := []string{"dec", kind}
	if source = strings.TrimSpace(source); source != "" {
		parts = append(parts, source)
	}
	return strings.Join(parts, ".")
}

func choicesToJSON(choices []adaptor.Choice) []map[string]string {
	if len(choices) == 0 {
		return nil
	}
	out := make([]map[string]string, 0, len(choices))
	for _, choice := range choices {
		out = append(out, map[string]string{
			"key":         choice.Key,
			"label":       choice.Label,
			"description": choice.Description,
		})
	}
	return out
}
