package adapterutil

import (
	"strings"
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

func TestRuntimeEnvBindingsInjectsSecretEnvWithoutLeakingRuntimeJSON(t *testing.T) {
	const secret = "sk-runtime-secret"

	env, err := RuntimeEnvBindings(
		[]agentadaptor.EnvBinding{{Name: "EXISTING", Value: "1"}},
		agentadaptor.RuntimePayload{
			SecretEnv: []agentadaptor.EnvBinding{{Name: "DELEGATION_TOKEN", Value: secret}},
			Ensured: []agentadaptor.RuntimeServiceRef{{
				ID:     "svc-delegation",
				Name:   "delegation",
				URL:    "http://127.0.0.1:43127/mcp",
				Status: agentadaptor.RuntimeServiceRunning,
				Metadata: map[string]string{
					"agentadaptor.mcp.enabled":              "true",
					"agentadaptor.mcp.bearer_token_env_var": "DELEGATION_TOKEN",
				},
				SecretEnv: []agentadaptor.EnvBinding{{Name: "DELEGATION_TOKEN", Value: secret}},
			}},
		},
	)
	if err != nil {
		t.Fatalf("runtime env bindings: %v", err)
	}
	if got := lastEnvValue(env, "DELEGATION_TOKEN"); got != secret {
		t.Fatalf("expected runtime secret env to reach subprocess env, got %q in %#v", got, env)
	}
	runtimeJSON := lastEnvValue(env, "PAPERCLIP_RUNTIME_SERVICES_JSON")
	if runtimeJSON == "" {
		t.Fatalf("expected runtime services JSON in env: %#v", env)
	}
	if strings.Contains(runtimeJSON, secret) {
		t.Fatalf("runtime services JSON leaked secret: %s", runtimeJSON)
	}
	if !strings.Contains(runtimeJSON, "DELEGATION_TOKEN") {
		t.Fatalf("runtime services JSON should retain bearer env var reference, got %s", runtimeJSON)
	}
}

func TestRuntimeEnvBindingsInjectsSecretEnvWithoutEnsuredRefs(t *testing.T) {
	const secret = "sk-secret-only"

	env, err := RuntimeEnvBindings(nil, agentadaptor.RuntimePayload{
		SecretEnv: []agentadaptor.EnvBinding{{Name: "DELEGATION_TOKEN", Value: secret}},
	})
	if err != nil {
		t.Fatalf("runtime env bindings: %v", err)
	}
	if got := lastEnvValue(env, "DELEGATION_TOKEN"); got != secret {
		t.Fatalf("expected runtime secret env without ensured refs, got %q in %#v", got, env)
	}
	if got := lastEnvValue(env, "PAPERCLIP_RUNTIME_SERVICES_JSON"); got != "" {
		t.Fatalf("did not expect runtime services JSON without ensured refs, got %q", got)
	}
}

func lastEnvValue(bindings []agentadaptor.EnvBinding, name string) string {
	var out string
	for _, binding := range bindings {
		if binding.Name == name {
			out = binding.Value
		}
	}
	return out
}
