package codex

import (
	"errors"
	"reflect"
	"testing"

	"github.com/agent-dance/agent-adaptor/driver"
)

func TestCodexPolicyArgsMatrix(t *testing.T) {
	tests := []struct {
		name   string
		policy driver.RunPolicy
		want   []string
	}{
		{name: "inherit", want: []string{"--ask-for-approval", "on-request"}},
		{name: "read only", policy: driver.RunPolicy{Isolation: driver.IsolationReadOnly}, want: []string{"--sandbox", "read-only", "--ask-for-approval", "on-request"}},
		{name: "workspace write", policy: driver.RunPolicy{Isolation: driver.IsolationWorkspaceWrite}, want: []string{"--sandbox", "workspace-write", "--ask-for-approval", "on-request"}},
		{name: "unrestricted", policy: driver.RunPolicy{Isolation: driver.IsolationUnrestricted}, want: []string{"--sandbox", "danger-full-access", "--ask-for-approval", "on-request"}},
		{name: "web allow", policy: driver.RunPolicy{WebSearch: driver.FeatureAllow}, want: []string{"--search", "--ask-for-approval", "on-request"}},
		{name: "web deny", policy: driver.RunPolicy{WebSearch: driver.FeatureDeny}, want: []string{"-c", `web_search="disabled"`, "--ask-for-approval", "on-request"}},
		{name: "permission auto approve", policy: driver.RunPolicy{HumanDecision: driver.HumanDecisionPolicy{Permission: driver.HumanDecisionAutoApprove}}, want: []string{"--ask-for-approval", "never"}},
		{
			name: "independent dimensions",
			policy: driver.RunPolicy{
				Isolation: driver.IsolationUnrestricted,
				WebSearch: driver.FeatureDeny,
			},
			want: []string{"-c", `web_search="disabled"`, "--sandbox", "danger-full-access", "--ask-for-approval", "on-request"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := codexPolicyArgs(tt.policy); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("codexPolicyArgs() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestCodexAppServerExtraArgsProjection(t *testing.T) {
	extra := []string{"-c", `model_provider="openai"`, "--enable=fast_mode", "--strict-config"}
	policy := driver.RunPolicy{WebSearch: driver.FeatureDeny}
	want := []string{"-c", `web_search="disabled"`, "-c", `model_provider="openai"`, "--enable=fast_mode", "--strict-config"}
	got, err := codexAppServerExtraArgs(extra, policy)
	if err != nil {
		t.Fatalf("projection: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("app-server args = %#v, want %#v", got, want)
	}
}

func TestCodexAppServerExtraArgsRejectExecOnlyFlags(t *testing.T) {
	for _, extra := range [][]string{{"--output-schema", "schema.json"}, {"--model", "gpt"}, {"-c"}, {"-c", "--strict-config"}} {
		_, err := codexAppServerExtraArgs(extra, driver.RunPolicy{})
		if !errors.Is(err, driver.ErrInvalidDriverConfig) {
			t.Fatalf("extra %#v error = %v, want ErrInvalidDriverConfig", extra, err)
		}
	}
}

func TestCodexAppServerExtraArgsRemovesSDKOwnedPolicyFlags(t *testing.T) {
	extra := []string{
		"--search", "--sandbox", "danger-full-access", "--ask-for-approval", "never",
		"--dangerously-bypass-approvals-and-sandbox", "--full-auto", "--strict-config",
	}
	want := []string{"--strict-config"}
	got, err := codexAppServerExtraArgs(extra, driver.RunPolicy{})
	if err != nil {
		t.Fatalf("projection: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("app-server args = %#v, want %#v", got, want)
	}
}

func TestFilterCodexPolicyExtraArgsDoesNotConsumeFollowingFlagAsValue(t *testing.T) {
	extra := []string{"--sandbox", "--strict-config", "-a", "--analytics-default-enabled", "-c", "--enable", "fast_mode"}
	policy := driver.RunPolicy{
		Isolation: driver.IsolationReadOnly,
		WebSearch: driver.FeatureDeny,
		HumanDecision: driver.HumanDecisionPolicy{
			Permission: driver.HumanDecisionAutoApprove,
		},
	}
	want := []string{"--strict-config", "--analytics-default-enabled", "-c", "--enable", "fast_mode"}
	if got := filterCodexPolicyExtraArgs(extra, policy); !reflect.DeepEqual(got, want) {
		t.Fatalf("filterCodexPolicyExtraArgs() = %#v, want %#v", got, want)
	}
}

func TestFilterCodexPolicyExtraArgsCallPolicyWins(t *testing.T) {
	extra := []string{
		"--search",
		"--sandbox", "read-only",
		"-a", "on-request",
		"--dangerously-bypass-approvals-and-sandbox",
		"--full-auto",
		"-c", `web_search="live"`,
		"--config=sandbox_mode=\"read-only\"",
		"-c", `approval_policy="on-request"`,
		"-c", `model_reasoning_effort="high"`,
		"--custom-flag",
	}
	policy := driver.RunPolicy{
		Isolation: driver.IsolationWorkspaceWrite,
		WebSearch: driver.FeatureDeny,
		HumanDecision: driver.HumanDecisionPolicy{
			Permission: driver.HumanDecisionAutoApprove,
		},
	}
	want := []string{"-c", `model_reasoning_effort="high"`, "--custom-flag"}
	if got := filterCodexPolicyExtraArgs(extra, policy); !reflect.DeepEqual(got, want) {
		t.Fatalf("filterCodexPolicyExtraArgs() = %#v, want %#v", got, want)
	}
}

func TestFilterCodexPolicyExtraArgsInheritStillRemovesSDKOwnedArgs(t *testing.T) {
	extra := []string{
		"--search", "--sandbox", "danger-full-access", "--ask-for-approval", "never",
		"--dangerously-bypass-approvals-and-sandbox", "--full-auto",
		"-c", `web_search="live"`, `--config=sandbox_mode="danger-full-access"`,
		"-c", `approval_policy="never"`, "-sdanger-full-access", "-anever",
		`-cweb_search="live"`, `-csandbox_mode="danger-full-access"`,
		`-capproval_policy="never"`, "--enable", "web_search_request",
		"--disable=web_search_request", "--enable", "fast_mode", "--custom-flag",
	}
	want := []string{"--enable", "fast_mode", "--custom-flag"}
	if got := filterCodexPolicyExtraArgs(extra, driver.RunPolicy{}); !reflect.DeepEqual(got, want) {
		t.Fatalf("inherited policy allowed constructor override: %#v", got)
	}
}

func TestCodexAppServerExtraArgsAcceptsAttachedUnmanagedConfig(t *testing.T) {
	extra := []string{`-cmodel_provider="openai"`}
	got, err := codexAppServerExtraArgs(extra, driver.RunPolicy{})
	if err != nil {
		t.Fatalf("projection: %v", err)
	}
	if !reflect.DeepEqual(got, extra) {
		t.Fatalf("app-server args = %#v, want %#v", got, extra)
	}
}

func TestCodexAppServerPolicyArgsMatrix(t *testing.T) {
	tests := []struct {
		name string
		web  driver.FeatureLevel
		want []string
	}{
		{name: "inherit"},
		{name: "allow", web: driver.FeatureAllow, want: []string{"-c", `web_search="live"`}},
		{name: "deny", web: driver.FeatureDeny, want: []string{"-c", `web_search="disabled"`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := codexAppServerPolicyArgs(driver.RunPolicy{WebSearch: tt.web}); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("codexAppServerPolicyArgs() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestCodexAppServerPolicyDimensionsRemainIndependent(t *testing.T) {
	if got := mapApprovalPolicy(driver.RunPolicy{Isolation: driver.IsolationUnrestricted}); got != "on-request" {
		t.Fatalf("default approval policy = %q, want on-request", got)
	}
	if got := mapApprovalPolicy(driver.RunPolicy{HumanDecision: driver.HumanDecisionPolicy{Permission: driver.HumanDecisionAutoApprove}}); got != "never" {
		t.Fatalf("auto approve mapped to %q, want never", got)
	}
	if got := mapSandbox(driver.RunPolicy{}); got != "" {
		t.Fatalf("inherited sandbox mapped to %q", got)
	}
	if got := mapSandbox(driver.RunPolicy{Isolation: driver.IsolationUnrestricted}); got != "danger-full-access" {
		t.Fatalf("unrestricted sandbox mapped to %q", got)
	}
}
