package codex

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
)

// Called only after the build-tag and environment gates. The authorized B06
// runner must provide an isolated authentication seed explicitly; never infer
// the operator's native profile. Only auth is copied into a fresh profile.
func alignmentLiveConfig(t *testing.T) Config {
	t.Helper()
	if !codexLiveCompiled || os.Getenv("AGENT_ADAPTOR_LIVE_CONFORMANCE") != "1" {
		t.Fatal("live profile requested without both live gates")
	}
	source := os.Getenv("AGENT_ADAPTOR_CODEX_LIVE_PROFILE")
	if source == "" {
		t.Fatal("AGENT_ADAPTOR_CODEX_LIVE_PROFILE must name an isolated authorized auth seed")
	}
	root := t.TempDir()
	profile := filepath.Join(root, "profile")
	workspace := filepath.Join(root, "workspace")
	for _, dir := range []string{profile, workspace} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	auth, err := os.ReadFile(filepath.Join(source, "auth.json"))
	if err != nil {
		t.Fatal("live auth seed is unavailable")
	}
	if err := os.WriteFile(filepath.Join(profile, "auth.json"), auth, 0600); err != nil {
		t.Fatal("cannot copy isolated live auth")
	}
	command, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal("authorized live runner requires codex CLI")
	}
	model := os.Getenv("AGENT_ADAPTOR_CODEX_LIVE_MODEL")
	if model == "" {
		model = "gpt-5.4"
	}
	cfg := Config{CommonConfig: CommonConfig{Command: command, CWD: workspace, Env: []driver.EnvBinding{{Name: "CODEX_HOME", Value: profile}, {Name: "HOME", Value: root}, {Name: "USERPROFILE", Value: root}}}, Model: model}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, command, "--version")
	cmd.Env = append(os.Environ(), "CODEX_HOME="+profile, "HOME="+root, "USERPROFILE="+root)
	version, err := cmd.Output()
	if err != nil {
		t.Fatal("live CLI version unavailable")
	}
	t.Logf("CLI=%s; isolated profile/workspace; model=%s", strings.TrimSpace(string(version)), model)
	return cfg
}
func TestAlignmentLiveGateDisabledWithoutBuildTag(t *testing.T) {
	if codexLiveCompiled {
		return
	}
	t.Setenv("AGENT_ADAPTOR_LIVE_CONFORMANCE", "1")
	live, _ := codexLiveGate(t)
	if live {
		t.Fatal("environment bypassed build tag")
	}
}

// A discoverable CLI must remain unexecuted when either live gate is absent.
// The canary has no provider behavior and cannot read an authentication seed.
func TestAlignmentLiveGateCanary(t *testing.T) {
	bin := t.TempDir()
	name := "codex"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	build := exec.CommandContext(ctx, "go", "build", "-o", filepath.Join(bin, name), "./testdata/live-gate-canary")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build gate canary: %v\n%s", err, output)
	}
	marker := filepath.Join(bin, "invoked")
	t.Setenv("PATH", bin)
	t.Setenv("AGENT_ADAPTOR_CODEX_CANARY_FILE", marker)
	t.Setenv("AGENT_ADAPTOR_CODEX_LIVE_PROFILE", filepath.Join(bin, "absent-auth-seed"))
	if _, err := exec.LookPath("codex"); err != nil {
		t.Fatal("gate canary is not discoverable")
	}
	t.Setenv("AGENT_ADAPTOR_LIVE_CONFORMANCE", "0")
	if live, _ := codexLiveGate(t); live {
		t.Fatal("disabled environment enabled live")
	}
	if !codexLiveCompiled {
		t.Setenv("AGENT_ADAPTOR_LIVE_CONFORMANCE", "1")
		if live, _ := codexLiveGate(t); live {
			t.Fatal("environment bypassed absent build tag")
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("disabled live gate invoked a CLI")
	}
}
