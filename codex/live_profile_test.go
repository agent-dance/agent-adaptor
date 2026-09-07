package codex

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
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
