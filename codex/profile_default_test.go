package codex

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/agent-dance/agent-adaptor/v2/driver"
)

// Without an explicit profile or tenant, the Driver must use the CLI's own
// home, which carries the user's login and provider configuration. A managed
// empty home made every default Agent fail authentication (seen live: 401
// against the built-in provider although ~/.codex configured a custom one).
func TestDefaultProfileUsesNativeCodexHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CODEX_HOME", "")
	native := filepath.Join(home, ".codex")
	if err := os.MkdirAll(native, 0o700); err != nil {
		t.Fatal(err)
	}
	profile, err := (adapter{}).GetProfile(context.Background(), Config{}, driver.AgentIdentity{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Dir != native || profile.Source != driver.AgentProfileSourceDefault || profile.Managed {
		t.Fatalf("default profile = %#v, want native %q", profile, native)
	}
	bindings, err := effectiveCodexBindingsNoInitialize(CommonConfig{}, nil, driver.AgentIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range bindings {
		if b.Name == "CODEX_HOME" && b.Value != native {
			t.Fatalf("process CODEX_HOME = %q, want %q", b.Value, native)
		}
	}
}

func TestDefaultProfileFallsBackToManagedHomeWithoutNativeHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CODEX_HOME", "")
	profile, err := (adapter{}).GetProfile(context.Background(), Config{}, driver.AgentIdentity{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !profile.Managed || profile.Dir != resolveManagedCodexHome(driver.AgentIdentity{}) {
		t.Fatalf("profile without a native Codex home = %#v, want managed", profile)
	}
}
