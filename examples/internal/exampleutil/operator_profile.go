package exampleutil

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/agent-dance/agent-adaptor/v2/driver"
)

// OperatorProfileEnv explicitly opts a live example into the operator's CLI
// profile. It does not change SDK Driver defaults or copy credentials. Codex
// receives CODEX_HOME (the explicit environment value, otherwise HOME/.codex),
// avoiding its intentional managed-home default. Other CLIs receive HOME;
// CLAUDE_CONFIG_DIR and CURSOR_HOME overrides are captured when present. Callers choose whether to
// append these bindings to their Config.Env; isolated hosts should not use it.
func OperatorProfileEnv(agent string) ([]driver.EnvBinding, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("operator CLI home: %w", err)
	}
	bindings := []driver.EnvBinding{{Name: "HOME", Value: home}}
	if value := os.Getenv("USERPROFILE"); value != "" {
		bindings = append(bindings, driver.EnvBinding{Name: "USERPROFILE", Value: value})
	}
	profileVar := ""
	switch agent {
	case AgentCodex:
		profileVar = "CODEX_HOME"
	case AgentClaude:
		profileVar = "CLAUDE_CONFIG_DIR"
	case AgentCursor:
		profileVar = "CURSOR_HOME"
	}
	if profileVar != "" {
		value := os.Getenv(profileVar)
		if value == "" && agent == AgentCodex {
			value = filepath.Join(home, ".codex")
		}
		if value != "" {
			bindings = append(bindings, driver.EnvBinding{Name: profileVar, Value: value})
		}
	}
	return bindings, nil
}
