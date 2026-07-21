package exampleutil

import (
	"os"
	"path/filepath"
	"strings"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

// TemporaryAgentEnvironment keeps example writes out of the caller's real
// workspace and provider profile. Authentication is linked only when the
// selected adapter supports it.
type TemporaryAgentEnvironment struct {
	RootDir      string
	WorkspaceDir string
	ProfileDir   string
}

func NewTemporaryAgentEnvironment(name string) (TemporaryAgentEnvironment, error) {
	name = strings.Trim(strings.TrimSpace(name), "-_")
	if name == "" {
		name = "run"
	}
	root, err := os.MkdirTemp("", "agent-adaptor-"+name+"-*")
	if err != nil {
		return TemporaryAgentEnvironment{}, err
	}
	environment := TemporaryAgentEnvironment{
		RootDir:      root,
		WorkspaceDir: filepath.Join(root, "workspace"),
		ProfileDir:   filepath.Join(root, "profile"),
	}
	if err := os.MkdirAll(environment.WorkspaceDir, 0o755); err != nil {
		environment.Cleanup()
		return TemporaryAgentEnvironment{}, err
	}
	return environment, nil
}

func (environment TemporaryAgentEnvironment) CloneProfileOption() agentadaptor.AgentOption {
	return agentadaptor.WithCloneProfile(environment.ProfileDir, agentadaptor.CloneProfileOptions{
		IncludeSettings: true,
		AuthMode:        agentadaptor.CloneProfileAuthLink,
	})
}

func (environment TemporaryAgentEnvironment) Configure(cfg LiveAgentConfig) LiveAgentConfig {
	cfg.CWD = environment.WorkspaceDir
	if cfg.Agent == AgentCodex {
		cfg.SkipGitRepoCheck = true
	}
	return cfg
}

func (environment TemporaryAgentEnvironment) Cleanup() {
	if environment.RootDir != "" {
		_ = os.RemoveAll(environment.RootDir)
	}
}
