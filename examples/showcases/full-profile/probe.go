package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/agent-dance/agent-adaptor/examples/internal/exampleutil"
)

func probeProfile(ctx context.Context, cfg exampleutil.LiveAgentConfig, layout providerLayout, profile, workspace, repoRoot, mcpServerPackage, hookPackage, hookProbeLog, mcpProbeLog string) map[string]any {
	probes := map[string]any{}

	if layout.SupportsMCPInventory {
		env := []string{layout.EnvVar + "=" + profile}
		probe := runProbeCommand(ctx, cfg.Command, layout.MCPInventoryCommandArgs, workspace, env, "")
		probes["provider_mcp_inventory"] = map[string]any{
			"command":               cfg.Command + " " + strings.Join(layout.MCPInventoryCommandArgs, " "),
			"exit_code":             probe.ExitCode,
			"error":                 probe.Error,
			"stdout":                limitText(probe.Stdout),
			"stderr":                stderrIfFailed(probe),
			"contains_profile_demo": strings.Contains(probe.Stdout, "profile-demo"),
		}
	}

	if layout.SupportsPromptProbe {
		probe := runProbeCommand(ctx, cfg.Command, layout.PromptProbeCommandArgs, workspace, []string{layout.EnvVar + "=" + profile}, "")
		probes["provider_prompt_input"] = map[string]any{
			"command":                    cfg.Command + " " + strings.Join(layout.PromptProbeCommandArgs, " "),
			"exit_code":                  probe.ExitCode,
			"error":                      probe.Error,
			"stderr":                     stderrIfFailed(probe),
			"contains_instruction_token": strings.Contains(probe.Stdout, instructionMarker),
			"contains_profile_skill":     strings.Contains(probe.Stdout, "profile-observer"),
		}
	}

	mcpRPC := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"profile_effect_probe","arguments":{}}}`,
		"",
	}, "\n")
	mcpProbeArgs := []string{"-C", filepath.ToSlash(repoRoot), "run", mcpServerPackage}
	mcpProbe := runProbeCommand(ctx, "go", mcpProbeArgs, workspace, []string{
		"AGENT_ADAPTOR_PROFILE_DEMO=mcp",
		"AGENT_ADAPTOR_PROFILE_DEMO_MCP_LOG=" + filepath.ToSlash(mcpProbeLog),
	}, mcpRPC)
	probes["mcp_server_rpc"] = map[string]any{
		"command":         "go " + strings.Join(mcpProbeArgs, " "),
		"exit_code":       mcpProbe.ExitCode,
		"error":           mcpProbe.Error,
		"stdout":          limitText(mcpProbe.Stdout),
		"stderr":          stderrIfFailed(mcpProbe),
		"contains_mcp_ok": strings.Contains(mcpProbe.Stdout, mcpOKMarker),
		"log":             fileEvidence(mcpProbeLog),
	}

	hookProbeArgs := []string{"-C", filepath.ToSlash(repoRoot), "run", hookPackage, "--log", filepath.ToSlash(hookProbeLog), "--event", "ProfileProbe"}
	hookProbe := runProbeCommand(ctx, "go", hookProbeArgs, workspace, nil, "")
	probes["hook_command_probe"] = map[string]any{
		"command":          "go " + strings.Join(hookProbeArgs, " "),
		"exit_code":        hookProbe.ExitCode,
		"error":            hookProbe.Error,
		"stdout":           limitText(hookProbe.Stdout),
		"stderr":           stderrIfFailed(hookProbe),
		"contains_hook_ok": strings.Contains(readText(hookProbeLog), hookOKMarker),
		"log":              fileEvidence(hookProbeLog),
	}

	return probes
}

type probeResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Error    string
}

func runProbeCommand(ctx context.Context, command string, args []string, cwd string, env []string, stdin string) probeResult {
	execPath, execArgs := exampleutil.WrapCommandForPlatform(command, args)
	cmd := exec.CommandContext(ctx, execPath, execArgs...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	cmd.Env = exampleutil.EnsureWindowsProcessEnv(append(os.Environ(), env...))
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	result := probeResult{Stdout: stdout.String(), Stderr: stderr.String()}
	if err != nil {
		result.Error = err.Error()
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
		}
		return result
	}
	return result
}

func stderrIfFailed(result probeResult) string {
	if result.ExitCode == 0 && result.Error == "" {
		return ""
	}
	return limitText(result.Stderr)
}
