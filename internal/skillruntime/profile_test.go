package skillruntime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentadaptor "github.com/agent-dance/agent-adaptor/driver"
)

func TestResolveProfileCloneLinksAuthAndCopiesSettings(t *testing.T) {
	source := t.TempDir()
	target := filepath.Join(t.TempDir(), "isolated")
	writeFile(t, filepath.Join(source, "config.toml"), "model_provider = 'codex-lb'\n")
	writeFile(t, filepath.Join(source, "auth.json"), `{"tokens":{"access_token":"native"}}`)

	resolution, err := ResolveProfile(ProfileResolveOptions{
		Selection: &agentadaptor.ProfileSelection{
			Mode: agentadaptor.ProfileModeClone,
			Dir:  target,
			Clone: &agentadaptor.CloneProfileOptions{
				IncludeSettings: true,
				AuthMode:        agentadaptor.CloneProfileAuthLink,
			},
		},
		EnvVar:          "CODEX_HOME",
		DefaultDir:      source,
		NativeSharedDir: source,
		SettingsFiles:   []string{"config.toml"},
		AuthFiles:       []string{"auth.json"},
	})
	if err != nil {
		t.Fatalf("resolve profile: %v", err)
	}
	if resolution.Profile.Dir != target {
		t.Fatalf("unexpected profile dir: %#v", resolution.Profile)
	}
	assertFileContains(t, filepath.Join(target, "config.toml"), "codex-lb")
	assertSameFile(t, filepath.Join(source, "auth.json"), filepath.Join(target, "auth.json"))
}

func TestResolveProfileCloneLinkAuthReplacesStaleCopiedAuth(t *testing.T) {
	source := t.TempDir()
	target := t.TempDir()
	writeFile(t, filepath.Join(source, "auth.json"), `{"tokens":{"access_token":"fresh"}}`)
	writeFile(t, filepath.Join(target, "auth.json"), `{"tokens":{"access_token":"stale-copy"}}`)

	_, err := ResolveProfile(ProfileResolveOptions{
		Selection: &agentadaptor.ProfileSelection{
			Mode: agentadaptor.ProfileModeClone,
			Dir:  target,
			Clone: &agentadaptor.CloneProfileOptions{
				AuthMode: agentadaptor.CloneProfileAuthLink,
			},
		},
		EnvVar:          "CODEX_HOME",
		DefaultDir:      source,
		NativeSharedDir: source,
		AuthFiles:       []string{"auth.json"},
	})
	if err != nil {
		t.Fatalf("resolve profile: %v", err)
	}
	assertSameFile(t, filepath.Join(source, "auth.json"), filepath.Join(target, "auth.json"))
	assertFileContains(t, filepath.Join(target, "auth.json"), "fresh")
}

func TestResolveProfileCloneAuthCopyDoesNotShareFile(t *testing.T) {
	source := t.TempDir()
	target := filepath.Join(t.TempDir(), "isolated")
	writeFile(t, filepath.Join(source, "auth.json"), `{"tokens":{"access_token":"copied"}}`)

	_, err := ResolveProfile(ProfileResolveOptions{
		Selection: &agentadaptor.ProfileSelection{
			Mode: agentadaptor.ProfileModeClone,
			Dir:  target,
			Clone: &agentadaptor.CloneProfileOptions{
				AuthMode: agentadaptor.CloneProfileAuthCopy,
			},
		},
		EnvVar:          "CODEX_HOME",
		DefaultDir:      source,
		NativeSharedDir: source,
		AuthFiles:       []string{"auth.json"},
	})
	if err != nil {
		t.Fatalf("resolve profile: %v", err)
	}
	assertFileContains(t, filepath.Join(target, "auth.json"), "copied")
	if sameFile(filepath.Join(source, "auth.json"), filepath.Join(target, "auth.json")) {
		t.Fatalf("expected AuthCopy to copy auth rather than share the native file")
	}
}

func TestResolveProfileRejectsUnknownCloneAuthMode(t *testing.T) {
	source := t.TempDir()
	target := filepath.Join(t.TempDir(), "isolated")
	writeFile(t, filepath.Join(source, "auth.json"), "{}")

	_, err := ResolveProfile(ProfileResolveOptions{
		Selection: &agentadaptor.ProfileSelection{
			Mode: agentadaptor.ProfileModeClone,
			Dir:  target,
			Clone: &agentadaptor.CloneProfileOptions{
				AuthMode: agentadaptor.CloneProfileAuthMode("mystery"),
			},
		},
		EnvVar:          "CODEX_HOME",
		DefaultDir:      source,
		NativeSharedDir: source,
		AuthFiles:       []string{"auth.json"},
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported clone auth mode") {
		t.Fatalf("expected unsupported auth mode error, got %v", err)
	}
}

func TestExplicitProfileSelectionOverridesConfiguredProfileBinding(t *testing.T) {
	configured := filepath.Join(t.TempDir(), "configured")
	selected := filepath.Join(t.TempDir(), "selected")
	resolution, err := ResolveProfile(ProfileResolveOptions{
		Bindings:        []agentadaptor.EnvBinding{{Name: "CURSOR_HOME", Value: configured}},
		Selection:       &agentadaptor.ProfileSelection{Mode: agentadaptor.ProfileModeDedicated, Dir: selected},
		EnvVar:          "CURSOR_HOME",
		NativeSharedDir: configured,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Profile.Dir != selected || resolution.Profile.Source != agentadaptor.AgentProfileSourceProfileOption {
		t.Fatalf("resolution = %#v, want explicit selected profile", resolution.Profile)
	}
	if got := ResolveBinding(resolution.Bindings, "CURSOR_HOME"); got != selected {
		t.Fatalf("resolved binding = %q, want %q", got, selected)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func assertFileContains(t *testing.T, path, want string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !strings.Contains(string(raw), want) {
		t.Fatalf("expected %s to contain %q, got %s", path, want, string(raw))
	}
}

func assertSameFile(t *testing.T, left, right string) {
	t.Helper()
	leftInfo, leftErr := os.Stat(left)
	rightInfo, rightErr := os.Stat(right)
	if leftErr != nil || rightErr != nil {
		t.Fatalf("stat shared files: left=%v right=%v", leftErr, rightErr)
	}
	if !os.SameFile(leftInfo, rightInfo) {
		t.Fatalf("expected %s and %s to be the same shared file", left, right)
	}
}
