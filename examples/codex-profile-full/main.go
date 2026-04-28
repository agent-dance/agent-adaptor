package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/examples/internal/exampleutil"
)

const (
	instructionMarker = "AGENT_ADAPTOR_PROFILE_DEMO_INSTRUCTIONS"
	mcpOKMarker       = "MCP_PROFILE_DEMO_OK"
	hookOKMarker      = "PROFILE_HOOK_DEMO_OK"
)

type providerLayout struct {
	Agent                   string
	EnvVar                  string
	ProfileLabel            string
	MCPFiles                map[string]string
	HookFiles               map[string]string
	InstructionFiles        map[string]string
	SubagentPath            string
	AuthFiles               []string
	SupportsPromptProbe     bool
	SupportsMCPInventory    bool
	MCPInventoryCommandArgs []string
	PromptProbeCommandArgs  []string
}

func main() {
	var (
		agent       = flag.String("agent", "", "Local CLI agent to use: "+exampleutil.SupportedAgents()+" (default codex, or AGENT_ADAPTOR_EXAMPLE_AGENT).")
		command     = flag.String("command", "", "Optional explicit local CLI command. Defaults by agent or CODEX_COMMAND/CLAUDE_COMMAND/CURSOR_COMMAND/PATH.")
		model       = flag.String("model", "", "Model to use. Defaults by agent or CODEX_MODEL/CLAUDE_MODEL/CURSOR_MODEL.")
		profileMode = flag.String("profile-mode", "dedicated", "Profile mode: dedicated or native. Native uses the current provider profile and may update it.")
		profileDir  = flag.String("profile", "", "Dedicated provider profile directory to materialize. Defaults to a temp dir that is kept for inspection.")
		workspace   = flag.String("workspace", "", "Workspace cwd for agent runs. Defaults to a temp dir.")
		timeout     = flag.Duration("timeout", 2*time.Minute, "Context timeout.")
		probe       = flag.Bool("probe", true, "Run lightweight local probes that show the selected CLI can read the materialized profile where the provider exposes such probes.")
		runAgent    = flag.Bool("run", false, "Also run the selected agent after syncing the profile. The Run call uses no run options.")
		prompt      = flag.String("prompt", defaultPrompt(), "Prompt used when -run=true.")
	)
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	exampleDir := currentDir()
	resolvedWorkspace := ensureDir("workspace", *workspace, "agent-adaptor-profile-full-workspace-*")
	agentCfg := exampleutil.ResolveLiveAgentConfig(*agent, *model, *command, resolvedWorkspace)
	if agentCfg.Agent == exampleutil.AgentCodex {
		agentCfg.ExtraArgs = append(agentCfg.ExtraArgs, "--skip-git-repo-check")
	}
	layout := layoutForAgent(agentCfg.Agent)

	profileOption, profileDescription, configuredProfile, artifactDir := selectProfileOption(agentCfg.Agent, layout, *profileMode, *profileDir)
	logRoot := configuredProfile
	if artifactDir != "" {
		logRoot = artifactDir
	}
	hookLog := filepath.Join(logRoot, "profile-hook.log")
	hookProbeLog := filepath.Join(logRoot, "profile-hook-probe.log")
	mcpLog := filepath.Join(logRoot, "profile-mcp.log")
	mcpProbeLog := filepath.Join(logRoot, "profile-mcp-probe.log")

	repoRoot := filepath.Dir(filepath.Dir(exampleDir))
	mcpServerPackage := "./examples/codex-profile-full/mcpserver"
	hookPackage := "./examples/codex-profile-full/hook"
	resources := profileResources(agentCfg.Agent, repoRoot, mcpServerPackage, hookPackage, hookLog, mcpLog)

	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(exampleutil.NewLiveAgentBinding(
			agentCfg,
			profileOption,
			agentadaptor.WithDefaultProfileResources(resources),
			agentadaptor.WithDefaultRunPolicy(agentadaptor.PolicyAutonomous),
		)),
		agentadaptor.WithSkillSet(agentadaptor.SkillSet{
			"profile-observer": agentadaptor.InlineSkill(
				"profile-observer",
				"---\nname: profile-observer\ndescription: Inspect the effective agent profile resources before making claims.\n---\n\nWhen active, verify the managed MCP, hooks, instructions, skills, and subagent files before claiming this demo is configured.\n",
			),
		}),
	)

	admin := sdk.Admin().Default()
	profileInfo, err := admin.GetProfile(ctx)
	exampleutil.Must(err, "resolve effective profile")
	effectiveProfile := profileInfo.Dir
	if strings.TrimSpace(effectiveProfile) == "" {
		effectiveProfile = configuredProfile
	}
	layout = withProfileRoot(layout, effectiveProfile)
	before, err := admin.ProfileSnapshot(ctx)
	exampleutil.Must(err, "snapshot before sync")
	after, err := admin.SyncProfile(ctx)
	exampleutil.Must(err, "sync profile resources")

	paths := map[string]string{
		"profile":   effectiveProfile,
		"workspace": resolvedWorkspace,
		"hook_log":  hookLog,
		"mcp_log":   mcpLog,
	}
	if artifactDir != "" {
		paths["artifact_dir"] = artifactDir
	}
	output := map[string]any{
		"demo":              "profile-full",
		"run_call_contract": "When -run=true this example calls sdk.Run(ctx, prompt) with no RunOption arguments.",
		"profile_mode":      profileDescription,
		"agent":             exampleutil.LiveAgentSummary(agentCfg),
		"paths":             paths,
		"profile_resources": map[string]any{
			"before_sync": summarizeProfile(before),
			"after_sync":  summarizeProfile(after),
		},
		"materialized_files": collectEvidence(layout, effectiveProfile, resolvedWorkspace),
	}

	if *probe {
		output["local_profile_probes"] = probeProfile(ctx, agentCfg, layout, effectiveProfile, resolvedWorkspace, repoRoot, mcpServerPackage, hookPackage, hookProbeLog, mcpProbeLog)
	}

	if *runAgent {
		result, runErr := sdk.Run(ctx, *prompt)
		if runErr != nil {
			output["run_error"] = runErr.Error()
		} else {
			output["run_result"] = runResultEvidence(result)
			output["materialized_files_after_run"] = collectEvidence(layout, effectiveProfile, resolvedWorkspace)
			output["runtime_artifacts"] = map[string]any{
				"hook_log": fileEvidence(hookLog),
				"mcp_log":  fileEvidence(mcpLog),
			}
		}
	}

	exampleutil.PrintJSON(output)
}

func profileResources(agent, repoRoot, mcpServerPackage, hookPackage, hookLog, mcpLog string) agentadaptor.ProfileResources {
	hook := agentadaptor.HookSpec{
		Key:   "profile-session-start",
		Event: agentadaptor.HookEventSessionStart,
		Handler: agentadaptor.HookHandler{
			Type:    agentadaptor.HookHandlerCommand,
			Command: "go",
			Args:    []string{"-C", filepath.ToSlash(repoRoot), "run", hookPackage, "--log", filepath.ToSlash(hookLog), "--event", "SessionStart"},
		},
		Timeout: 10 * time.Second,
	}
	if agent == exampleutil.AgentCodex {
		hook.StatusMessage = "recording profile demo hook"
	}
	return agentadaptor.ProfileResources{
		Skills: []agentadaptor.SkillRef{
			agentadaptor.Key("profile-observer"),
		},
		MCP: &agentadaptor.MCPConfig{Servers: []agentadaptor.MCPServerSpec{{
			Key:       "profile-demo",
			Transport: agentadaptor.MCPTransportStdio,
			Command:   "go",
			Args:      []string{"-C", filepath.ToSlash(repoRoot), "run", mcpServerPackage},
			Env: map[string]string{
				"AGENT_ADAPTOR_PROFILE_DEMO":         "mcp",
				"AGENT_ADAPTOR_PROFILE_DEMO_MCP_LOG": filepath.ToSlash(mcpLog),
			},
			Required: true,
		}}},
		Agents: []agentadaptor.AgentSpec{{
			Key:          "profile-reviewer",
			RuntimeName:  "profile-reviewer",
			Description:  "Review profile resource materialization evidence.",
			Instructions: "When asked to review this demo, verify instructions, hooks, MCP, skills, and this subagent file in the effective provider profile.",
		}},
		Hooks: []agentadaptor.HookSpec{hook},
		Instructions: &agentadaptor.InstructionsBundleRef{
			ID:      "full-profile-demo",
			Content: "Profile instruction proof: mention " + instructionMarker + " if asked what profile instructions are active.",
			Scope:   agentadaptor.InstructionScopeProject,
			Mode:    agentadaptor.InstructionModeAdditive,
		},
	}
}

func defaultPrompt() string {
	return strings.Join([]string{
		"Confirm this selected-agent profile demo is active.",
		"Call the profile-demo MCP tool profile_effect_probe and include its exact result text.",
		"Mention " + instructionMarker + " if the profile instructions are visible.",
		"Keep the answer short.",
	}, " ")
}

func runResultEvidence(result agentadaptor.RunResult) map[string]any {
	return map[string]any{
		"driver_type": result.DriverType,
		"exit_code":   result.ExitCode,
		"timed_out":   result.TimedOut,
		"failure":     result.Failure,
		"run_id":      result.RunID,
		"session":     result.Session,
		"summary":     result.Summary,
		"output":      strings.TrimSpace(result.Output),
		"raw_stdout":  streamSnippet(result.RawStreams, true),
		"raw_stderr":  streamSnippet(result.RawStreams, false),
	}
}

func summarizeProfile(snapshot agentadaptor.ProfileSnapshot) []map[string]any {
	out := make([]map[string]any, 0, len(snapshot.Resources))
	for _, resource := range snapshot.Resources {
		out = append(out, map[string]any{
			"kind":            resource.Kind,
			"managed":         resource.Managed,
			"external":        resource.External,
			"support":         resource.Support,
			"materialization": resource.Materialization,
			"warnings":        resource.Warnings,
			"error":           resource.Error,
		})
	}
	return out
}

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

func collectEvidence(layout providerLayout, profile, workspace string) map[string]any {
	files := map[string]string{
		"manifest": filepath.Join(profile, ".agent-adaptor-profile-manifest.json"),
		"skill":    filepath.Join(profile, "skills", "profile-observer"),
		"subagent": layout.SubagentPath,
	}
	for key, path := range layout.MCPFiles {
		files["mcp_"+key] = path
	}
	for key, path := range layout.HookFiles {
		files["hooks_"+key] = path
	}
	for key, path := range layout.InstructionFiles {
		files["instructions_"+key] = path
	}
	if layout.Agent == exampleutil.AgentCursor {
		files["instructions_workspace_rule"] = filepath.Join(workspace, ".cursor", "rules", "full-profile-demo.mdc")
	}

	out := map[string]any{}
	for key, path := range files {
		out[key] = fileEvidence(path)
	}
	auth := map[string]any{}
	for _, name := range layout.AuthFiles {
		auth[name] = redactedAuthEvidence(filepath.Join(profile, name))
	}
	out["auth"] = auth
	return out
}

func fileEvidence(path string) map[string]any {
	info, err := os.Lstat(path)
	if err != nil {
		return map[string]any{"path": path, "exists": false}
	}
	evidence := map[string]any{
		"path":   path,
		"exists": true,
		"mode":   info.Mode().String(),
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		if err == nil {
			evidence["symlink_target"] = target
		}
		return evidence
	}
	if info.IsDir() {
		entries, err := os.ReadDir(path)
		if err == nil {
			names := make([]string, 0, len(entries))
			for _, entry := range entries {
				names = append(names, entry.Name())
			}
			evidence["entries"] = names
		}
		return evidence
	}
	raw, err := os.ReadFile(path)
	if err == nil {
		text := string(raw)
		if len(text) > 3000 {
			text = text[:3000] + "\n...<truncated>"
		}
		evidence["content"] = text
	}
	return evidence
}

func redactedAuthEvidence(path string) map[string]any {
	info, err := os.Lstat(path)
	if err != nil {
		return map[string]any{"path": path, "exists": false}
	}
	out := map[string]any{
		"path":             path,
		"exists":           true,
		"mode":             info.Mode().String(),
		"size_bytes":       info.Size(),
		"content_redacted": true,
	}
	if info.Mode()&os.ModeSymlink != 0 {
		if target, err := os.Readlink(path); err == nil {
			out["symlink_target"] = target
		}
	}
	return out
}

func layoutForAgent(agent string) providerLayout {
	switch agent {
	case exampleutil.AgentClaude:
		profile := nativeProfileHome(agent)
		return providerLayout{
			Agent:                agent,
			EnvVar:               "CLAUDE_CONFIG_DIR",
			ProfileLabel:         "Claude Code profile",
			MCPFiles:             map[string]string{"claude_json": filepath.Join(profile, ".claude.json")},
			HookFiles:            map[string]string{"settings_json": filepath.Join(profile, "settings.json")},
			InstructionFiles:     map[string]string{"claude_md": filepath.Join(profile, "CLAUDE.md")},
			SubagentPath:         filepath.Join(profile, "agents", "profile-reviewer.md"),
			AuthFiles:            []string{".credentials.json", "credentials.json"},
			SupportsMCPInventory: true,
			MCPInventoryCommandArgs: []string{
				"mcp", "list",
			},
		}
	case exampleutil.AgentCursor:
		profile := nativeProfileHome(agent)
		return providerLayout{
			Agent:                agent,
			EnvVar:               "CURSOR_HOME",
			ProfileLabel:         "Cursor profile",
			MCPFiles:             map[string]string{"mcp_json": filepath.Join(profile, "mcp.json")},
			HookFiles:            map[string]string{"hooks_json": filepath.Join(profile, "hooks.json")},
			InstructionFiles:     map[string]string{"profile_fallback": filepath.Join(profile, ".agent-adaptor", "instructions", "full-profile-demo.md")},
			SubagentPath:         filepath.Join(profile, "agents", "profile-reviewer.md"),
			AuthFiles:            []string{"cli-config.json", "auth.json", "credentials.json"},
			SupportsMCPInventory: false,
		}
	default:
		profile := nativeProfileHome(agent)
		return providerLayout{
			Agent:                exampleutil.AgentCodex,
			EnvVar:               "CODEX_HOME",
			ProfileLabel:         "Codex profile",
			MCPFiles:             map[string]string{"config_toml": filepath.Join(profile, "config.toml")},
			HookFiles:            map[string]string{"hooks_json": filepath.Join(profile, "hooks.json")},
			InstructionFiles:     map[string]string{"agents_md": filepath.Join(profile, "AGENTS.md")},
			SubagentPath:         filepath.Join(profile, "agents", "profile-reviewer.toml"),
			AuthFiles:            []string{"auth.json"},
			SupportsPromptProbe:  true,
			SupportsMCPInventory: true,
			MCPInventoryCommandArgs: []string{
				"mcp", "list",
			},
			PromptProbeCommandArgs: []string{
				"debug", "prompt-input", "Profile probe prompt",
			},
		}
	}
}

func withProfileRoot(layout providerLayout, profile string) providerLayout {
	layout.MCPFiles = replaceRoot(layout.MCPFiles, nativeProfileHome(layout.Agent), profile)
	layout.HookFiles = replaceRoot(layout.HookFiles, nativeProfileHome(layout.Agent), profile)
	layout.InstructionFiles = replaceRoot(layout.InstructionFiles, nativeProfileHome(layout.Agent), profile)
	layout.SubagentPath = replaceRootString(layout.SubagentPath, nativeProfileHome(layout.Agent), profile)
	return layout
}

func replaceRoot(values map[string]string, oldRoot, newRoot string) map[string]string {
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = replaceRootString(value, oldRoot, newRoot)
	}
	return out
}

func replaceRootString(value, oldRoot, newRoot string) string {
	rel, err := filepath.Rel(oldRoot, value)
	if err != nil || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return value
	}
	return filepath.Join(newRoot, rel)
}

func streamSnippet(streams *agentadaptor.RawStreams, stdout bool) string {
	if streams == nil {
		return ""
	}
	value := streams.Stderr
	if stdout {
		value = streams.Stdout
	}
	value = strings.TrimSpace(value)
	if len(value) > 3000 {
		return value[:3000] + "\n...<truncated>"
	}
	return value
}

func limitText(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 3000 {
		return value[:3000] + "\n...<truncated>"
	}
	return value
}

func readText(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(raw)
}

func currentDir() string {
	_, file, _, ok := runtime.Caller(0)
	exampleutil.Check(ok, "resolve current example directory")
	return filepath.Dir(file)
}

func ensureDir(label, requested, pattern string) string {
	var dir string
	if strings.TrimSpace(requested) == "" {
		created, err := os.MkdirTemp("", pattern)
		exampleutil.Must(err, "create temp %s", label)
		dir = created
	} else {
		dir = filepath.Clean(requested)
		exampleutil.Must(os.MkdirAll(dir, 0o755), "create %s", label)
	}
	abs, err := filepath.Abs(dir)
	exampleutil.Must(err, "resolve %s", label)
	return abs
}

func selectProfileOption(agent string, layout providerLayout, mode, requested string) (agentadaptor.AgentOption, string, string, string) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "dedicated":
		profile := chooseProfileTarget(agent, requested)
		return agentadaptor.WithCloneProfile(profile, agentadaptor.CloneProfileOptions{
				IncludeSettings: true,
				AuthMode:        agentadaptor.CloneProfileAuthLink,
			}),
			fmt.Sprintf("dedicated: WithCloneProfile(..., IncludeSettings:true, AuthMode:CloneProfileAuthLink) creates an isolated %s seeded from native settings and shared native login state.", layout.EnvVar),
			profile,
			""
	case "native":
		if strings.TrimSpace(requested) != "" {
			exampleutil.Fatalf("-profile cannot be combined with -profile-mode=native")
		}
		artifacts := ensureDir("artifact", "", "agent-adaptor-profile-full-artifacts-*")
		return agentadaptor.WithNativeProfile(),
			fmt.Sprintf("native: WithNativeProfile() uses the current %s, so model runs reuse the same auth as the local CLI. This may write managed demo resources to that profile.", layout.ProfileLabel),
			nativeProfileHome(agent),
			artifacts
	default:
		exampleutil.Fatalf("unsupported -profile-mode %q; use dedicated or native", mode)
		return nil, "", "", ""
	}
}

func chooseProfileTarget(agent, requested string) string {
	if strings.TrimSpace(requested) != "" {
		abs, err := filepath.Abs(filepath.Clean(requested))
		exampleutil.Must(err, "resolve profile")
		return abs
	}
	for attempt := 0; attempt < 100; attempt++ {
		candidate := filepath.Join(os.TempDir(), fmt.Sprintf("agent-adaptor-%s-profile-full-%d", agent, time.Now().UnixNano()))
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
		time.Sleep(time.Millisecond)
	}
	exampleutil.Fatalf("choose profile: exhausted unique temp path attempts")
	return ""
}

func nativeProfileHome(agent string) string {
	envVar := "CODEX_HOME"
	dirName := ".codex"
	switch agent {
	case exampleutil.AgentClaude:
		envVar = "CLAUDE_CONFIG_DIR"
		dirName = ".claude"
	case exampleutil.AgentCursor:
		envVar = "CURSOR_HOME"
		dirName = ".cursor"
	}
	if configured := strings.TrimSpace(os.Getenv(envVar)); configured != "" {
		abs, err := filepath.Abs(filepath.Clean(configured))
		if err == nil {
			return abs
		}
		return filepath.Clean(configured)
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return filepath.Clean(dirName)
	}
	return filepath.Join(home, dirName)
}
