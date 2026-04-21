package adapterutil

import (
	"testing"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

func TestResolvedEnvValuePrefersBindingOverProcessEnv(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "process-value")
	value, source := ResolvedEnvValue([]agentadaptor.EnvBinding{{
		Name:  "OPENAI_API_KEY",
		Value: "binding-value",
	}}, "OPENAI_API_KEY")
	if value != "binding-value" || source != "binding_env" {
		t.Fatalf("unexpected resolved env: %q %q", value, source)
	}
}

func TestResolvedTruthyEnvRecognizesTrueValues(t *testing.T) {
	ok, source := ResolvedTruthyEnv([]agentadaptor.EnvBinding{{
		Name:  "CLAUDE_CODE_USE_BEDROCK",
		Value: "true",
	}}, "CLAUDE_CODE_USE_BEDROCK")
	if !ok || source != "binding_env" {
		t.Fatalf("unexpected truthy env result: %v %q", ok, source)
	}
}
