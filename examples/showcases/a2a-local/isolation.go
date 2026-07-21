package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/agent-dance/agent-adaptor/examples/internal/exampleutil"
)

type isolationConfig struct {
	Agent        string
	RootDir      string
	WorkspaceDir string
	ProfileDir   string
	Keep         bool
	cleanupRoot  bool
}

func newIsolation(agent, workspace, profile string, keep bool) (isolationConfig, error) {
	cfg := isolationConfig{Agent: exampleutil.ResolveLiveAgent(agent), Keep: keep}
	if strings.TrimSpace(workspace) == "" || strings.TrimSpace(profile) == "" {
		root, err := os.MkdirTemp("", "agent-adaptor-a2a-local-*")
		if err != nil {
			return isolationConfig{}, err
		}
		cfg.RootDir = root
		cfg.cleanupRoot = !keep
	}

	workspaceDir := strings.TrimSpace(workspace)
	if workspaceDir == "" {
		workspaceDir = filepath.Join(cfg.RootDir, "workspace")
	}
	profileDir := strings.TrimSpace(profile)
	if profileDir == "" {
		profileDir = filepath.Join(cfg.RootDir, cfg.Agent+"-profile")
	}

	var err error
	cfg.WorkspaceDir, err = ensureDir(workspaceDir)
	if err != nil {
		cfg.Cleanup()
		return isolationConfig{}, err
	}
	cfg.ProfileDir, err = ensureDir(profileDir)
	if err != nil {
		cfg.Cleanup()
		return isolationConfig{}, err
	}
	return cfg, nil
}

func (cfg isolationConfig) Cleanup() {
	if cfg.cleanupRoot && cfg.RootDir != "" {
		_ = os.RemoveAll(cfg.RootDir)
	}
}

func ensureDir(path string) (string, error) {
	cleaned := filepath.Clean(path)
	absolute, err := filepath.Abs(cleaned)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(absolute, 0o755); err != nil {
		return "", err
	}
	return absolute, nil
}

func preferDirectlyExecutableCommand(cfg exampleutil.LiveAgentConfig) exampleutil.LiveAgentConfig {
	if runtime.GOOS != "windows" || !strings.EqualFold(filepath.Ext(cfg.Command), ".ps1") {
		return cfg
	}
	base := strings.TrimSuffix(cfg.Command, filepath.Ext(cfg.Command))
	for _, candidate := range []string{base + ".cmd", base + ".exe"} {
		if exampleutil.ProbeAgentCommand(candidate) {
			cfg.Command = candidate
			cfg.CommandNote += " Streaming examples execute the provider command directly; using the Windows executable shim instead of the PowerShell shim."
			return cfg
		}
	}
	return cfg
}
