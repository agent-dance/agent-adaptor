package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/examples/internal/exampleutil"
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
		artifacts := ensureDir("artifact", "", "agent-adaptor-full-profile-artifacts-*")
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
		candidate := filepath.Join(os.TempDir(), fmt.Sprintf("agent-adaptor-%s-full-profile-%d", agent, time.Now().UnixNano()))
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
