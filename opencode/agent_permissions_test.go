package opencode

import (
	"context"
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/testutil"
)

// Evaluate the native last-matching-rule contract independently of the
// normalization/rendering code, including overlapping permission wildcards.
func permissionOutcome(rules []permissionRule, permission, pattern string) string {
	match := func(glob, value string) bool {
		expression := regexp.QuoteMeta(glob)
		expression = strings.ReplaceAll(expression, `\*`, ".*")
		expression = strings.ReplaceAll(expression, `\?`, ".")
		return regexp.MustCompile("^(?:" + expression + ")$").MatchString(value)
	}
	action := "ask"
	for _, rule := range rules {
		if match(rule.Permission, permission) && match(rule.Pattern, pattern) {
			action = rule.Action
		}
	}
	return action
}

func TestAgentPermissionCanonicalizationSemantics(t *testing.T) {
	rng := rand.New(rand.NewSource(716))
	names := []string{"*", "read", "edit", "r*", "?dit"}
	patterns := []string{"*", "*.env", "secret.env", "src/*", "?"}
	actions := []string{"ask", "deny", "allow"}
	for trial := 0; trial < 250; trial++ {
		var original []permissionRule
		for count := rng.Intn(20); count > 0; count-- {
			original = append(original, permissionRule{names[rng.Intn(len(names))], patterns[rng.Intn(len(patterns))], actions[rng.Intn(len(actions))]})
		}
		normalized := effectivePermissionRules(original)
		if !reflect.DeepEqual(normalized, effectivePermissionRules(normalized)) {
			t.Fatalf("non-idempotent canonicalization: %+v", original)
		}
		for _, permission := range []string{"read", "edit", "run", "bash"} {
			for _, pattern := range []string{"a", "secret.env", "other.env", "src/main.go", "unknown"} {
				if permissionOutcome(original, permission, pattern) != permissionOutcome(normalized, permission, pattern) {
					t.Fatalf("changed %s/%s: %+v -> %+v", permission, pattern, original, normalized)
				}
			}
		}
	}
}

func TestAgentPermissionGates(t *testing.T) {
	rules := []permissionRule{{"*", "*", "allow"}, {"read", "*.env", "deny"}, {"read", "public.env", "allow"}, {"question", "*", "allow"}, {"external_directory", "/cache/*", "allow"}, {"external_directory", "/{env:SECRET}/*", "allow"}}
	raw, resolved, err := gateAgentPermissions(rules, "/cache/*")
	if err != nil {
		t.Fatal(err)
	}
	for _, sample := range []struct{ permission, path, want string }{
		{"read", "secret.env", "deny"}, {"read", "public.env", "ask"}, {"bash", "anything", "ask"}, {"question", "anything", "ask"}, {"external_directory", "/cache/output", "allow"}, {"external_directory", "/{env:SECRET}/other", "ask"},
	} {
		if got := permissionOutcome(resolved, sample.permission, sample.path); got != sample.want {
			t.Fatalf("%+v: %s", sample, got)
		}
	}
	var config map[string]json.RawMessage
	if err := json.Unmarshal(raw, &config); err != nil || string(config["question"]) != `"ask"` || strings.Contains(string(raw), "{env:") {
		t.Fatalf("native scalar/literal encoding: %s %v", raw, err)
	}
	if _, _, err := gateAgentPermissions([]permissionRule{{"read", "*", "deny"}, {"r*", "*", "ask"}, {"read", "*.env", "deny"}}, ""); err == nil {
		t.Fatal("unrepresentable interleaved permission precedence accepted")
	}
	if _, _, err := gateAgentPermissions([]any{map[string]any{"permission": "read", "pattern": "*", "action": "allow", "future": true}}, ""); err == nil {
		t.Fatal("unknown permission semantics accepted")
	}
}

func TestAgentPermissionDeliveryBeforePrompt(t *testing.T) {
	command, capture := testutil.ProtocolCommand(t, serverHappy, "", 0)
	d := Driver(Config{Command: command, Env: []driver.EnvBinding{{Name: "OPENCODE_CONFIG_DIR", Value: t.TempDir()}, {Name: "AGENT_ADAPTOR_OPENCODE_IGNORE_AGENT_PERMISSIONS", Value: "1"}}})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req := driver.Request{Prompt: "must not execute"}
	req.Policy.HumanDecision.Permission = driver.HumanDecisionAsk
	response, err := d.Run(ctx, req, &testutil.EventRecorder{})
	if err == nil || response.Checkpoint != nil || !strings.Contains(err.Error(), "build.permission") {
		t.Fatalf("ignored gate: %+v %v", response, err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(capture), "prompts.log")); !os.IsNotExist(err) {
		t.Fatal("prompt was delivered without verified agent permission gates")
	}
}
