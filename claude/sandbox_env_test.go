package claude

import (
	"testing"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

func withGeteuid(t *testing.T, uid int) {
	t.Helper()
	prev := geteuid
	geteuid = func() int { return uid }
	t.Cleanup(func() { geteuid = prev })
}

func envContains(env []agentadaptor.EnvBinding, name, value string) bool {
	for _, b := range env {
		if b.Name == name && b.Value == value {
			return true
		}
	}
	return false
}

func envHas(env []agentadaptor.EnvBinding, name string) bool {
	for _, b := range env {
		if b.Name == name {
			return true
		}
	}
	return false
}

func TestEnsureRootSandboxEnvInjectsUnderRootWithSkipPermissions(t *testing.T) {
	withGeteuid(t, 0)
	env := []agentadaptor.EnvBinding{{Name: "HOME", Value: "/root"}}
	out := ensureRootSandboxEnv([]string{"--print", "--dangerously-skip-permissions"}, env)
	if !envContains(out, "IS_SANDBOX", "1") {
		t.Fatalf("expected IS_SANDBOX=1 to be injected, got %#v", out)
	}
	if len(env) != 1 || env[0].Name != "HOME" {
		t.Fatalf("input env must not be mutated, got %#v", env)
	}
}

func TestEnsureRootSandboxEnvNoOpWhenNotRoot(t *testing.T) {
	withGeteuid(t, 1000)
	env := []agentadaptor.EnvBinding{{Name: "HOME", Value: "/home/dev"}}
	out := ensureRootSandboxEnv([]string{"--dangerously-skip-permissions"}, env)
	if envHas(out, "IS_SANDBOX") {
		t.Fatalf("non-root should not receive IS_SANDBOX, got %#v", out)
	}
}

func TestEnsureRootSandboxEnvNoOpWithoutSkipPermissions(t *testing.T) {
	withGeteuid(t, 0)
	env := []agentadaptor.EnvBinding{{Name: "HOME", Value: "/root"}}
	out := ensureRootSandboxEnv([]string{"--print", "--output-format", "stream-json"}, env)
	if envHas(out, "IS_SANDBOX") {
		t.Fatalf("no skip-permissions flag, no injection expected, got %#v", out)
	}
}

func TestEnsureRootSandboxEnvRespectsHostIntent(t *testing.T) {
	withGeteuid(t, 0)
	cases := []struct {
		name string
		env  []agentadaptor.EnvBinding
	}{
		{
			name: "host set IS_SANDBOX=0 keeps it",
			env:  []agentadaptor.EnvBinding{{Name: "IS_SANDBOX", Value: "0"}},
		},
		{
			name: "host set IS_SANDBOX=1 keeps it",
			env:  []agentadaptor.EnvBinding{{Name: "IS_SANDBOX", Value: "1"}},
		},
		{
			name: "host set CLAUDE_CODE_BUBBLEWRAP takes precedence",
			env:  []agentadaptor.EnvBinding{{Name: "CLAUDE_CODE_BUBBLEWRAP", Value: "1"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := ensureRootSandboxEnv([]string{"--dangerously-skip-permissions"}, tc.env)
			isSandboxCount := 0
			bubblewrapCount := 0
			for _, b := range out {
				if b.Name == "IS_SANDBOX" {
					isSandboxCount++
				}
				if b.Name == "CLAUDE_CODE_BUBBLEWRAP" {
					bubblewrapCount++
				}
			}
			if isSandboxCount > 1 {
				t.Fatalf("IS_SANDBOX duplicated, got %#v", out)
			}
			if bubblewrapCount > 1 {
				t.Fatalf("CLAUDE_CODE_BUBBLEWRAP duplicated, got %#v", out)
			}
		})
	}
}
