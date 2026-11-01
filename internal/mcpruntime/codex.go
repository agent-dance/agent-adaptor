package mcpruntime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/agent-dance/agent-adaptor/v2/driver"
	"github.com/agent-dance/agent-adaptor/v2/internal/engine"
	"github.com/agent-dance/agent-adaptor/v2/internal/profilestate"
	toml "github.com/pelletier/go-toml/v2"
)

func SyncCodexProfile(codexHome string, kind ProfileKind, payload driver.MCPPayload) error {
	_, err := SyncResource(context.Background(), "codex", codexHome, kind, payload)
	return err
}

func codexServerConfig(server driver.MCPServerSpec) (map[string]any, error) {
	// Profile synchronization has no execution policy. Materialize the safe
	// prompt default; every invocation applies its resolved policy again.
	return CodexServerConfig(server, driver.RunPolicy{})
}

// CodexServerConfig renders an SDK-declared server for both materialization and
// invocation-local overrides. Explicit server defaults and exact tools policies
// belong in this rendering path, before the derived default is filled in.
func CodexServerConfig(server driver.MCPServerSpec, policy driver.RunPolicy) (map[string]any, error) {
	entry, err := codexServerTransportConfig(server)
	if err != nil {
		return nil, err
	}
	applyCodexMCPApprovalDefault(entry, policy)
	return entry, nil
}

// applyCodexMCPApprovalDefault supplies a fallback, never an exact-tool policy.
// A server's explicit default wins; tools.<exact name>.approval_mode is left
// intact and takes precedence in Codex. Filesystem isolation does not restrict
// host-owned tools, so SDK-declared servers always remain available.
func applyCodexMCPApprovalDefault(entry map[string]any, policy driver.RunPolicy) {
	if _, explicit := entry["default_tools_approval_mode"]; !explicit {
		mode := "prompt"
		if driver.EffectiveHumanDecisionPolicy(policy.HumanDecision).Permission == driver.HumanDecisionAutoApprove {
			mode = "approve"
		}
		entry["default_tools_approval_mode"] = mode
	}
	entry["enabled"] = true
}

func codexServerTransportConfig(server driver.MCPServerSpec) (map[string]any, error) {
	switch server.Transport {
	case driver.MCPTransportStdio:
		entry := map[string]any{
			"command": server.Command,
		}
		if len(server.Args) > 0 {
			entry["args"] = append([]string(nil), server.Args...)
		}
		if len(server.Env) > 0 {
			entry["env"] = cloneStringMapAny(server.Env)
		}
		return entry, nil
	case driver.MCPTransportHTTP:
		if len(server.Headers) > 0 {
			return nil, fmt.Errorf("%w: Codex MCP server %q does not support custom headers", engine.ErrInvalidMCPConfig, server.Key)
		}
		entry := map[string]any{
			"url": server.URL,
		}
		if server.BearerTokenEnvVar != "" {
			entry["bearer_token_env_var"] = server.BearerTokenEnvVar
		}
		return entry, nil
	default:
		return nil, fmt.Errorf("%w: Codex MCP server %q does not support transport %q", engine.ErrMCPTransportUnsupported, server.Key, server.Transport)
	}
}

func readTOMLObject(path string) (map[string]any, error) {
	root := map[string]any{}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return root, nil
		}
		return nil, err
	}
	if len(raw) == 0 {
		return root, nil
	}
	if err := toml.Unmarshal(raw, &root); err != nil {
		return nil, err
	}
	return root, nil
}

func writeTOMLObject(path string, root map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := toml.Marshal(root)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return profilestate.AtomicWriteFile(path, raw, 0o644)
}
