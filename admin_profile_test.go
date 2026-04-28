package agentadaptor_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/claude"
	"github.com/agent-dance/agent-adaptor/codex"
	"github.com/agent-dance/agent-adaptor/cursor"
)

func TestAdminProfileSnapshotClassifiesEffectiveProfileKind(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	t.Setenv("CURSOR_HOME", "")

	home := t.TempDir()
	canonicalClaude := filepath.Join(home, ".claude")
	if err := os.MkdirAll(canonicalClaude, 0o755); err != nil {
		t.Fatalf("mkdir canonical Claude profile: %v", err)
	}

	cases := []struct {
		name    string
		binding agentadaptor.AgentBinding
		want    agentadaptor.ProfileKind
	}{
		{
			name: "claude dedicated is host managed",
			binding: claude.New(agentadaptor.ClaudeConfig{CommonConfig: agentadaptor.CommonConfig{
				Env: []agentadaptor.EnvBinding{{Name: "HOME", Value: home}, {Name: "USERPROFILE", Value: home}},
			}}, agentadaptor.WithDedicatedProfile(t.TempDir())),
			want: agentadaptor.ProfileKindHostManaged,
		},
		{
			name: "claude canonical env is shared",
			binding: claude.New(agentadaptor.ClaudeConfig{CommonConfig: agentadaptor.CommonConfig{
				Env: []agentadaptor.EnvBinding{{Name: "HOME", Value: home}, {Name: "USERPROFILE", Value: home}, {Name: "CLAUDE_CONFIG_DIR", Value: canonicalClaude}},
			}}),
			want: agentadaptor.ProfileKindShared,
		},
		{
			name: "claude noncanonical env is host managed",
			binding: claude.New(agentadaptor.ClaudeConfig{CommonConfig: agentadaptor.CommonConfig{
				Env: []agentadaptor.EnvBinding{{Name: "HOME", Value: home}, {Name: "USERPROFILE", Value: home}, {Name: "CLAUDE_CONFIG_DIR", Value: t.TempDir()}},
			}}),
			want: agentadaptor.ProfileKindHostManaged,
		},
		{
			name: "cursor clone is host managed",
			binding: cursor.New(agentadaptor.CursorConfig{CommonConfig: agentadaptor.CommonConfig{
				Env: []agentadaptor.EnvBinding{{Name: "HOME", Value: home}, {Name: "USERPROFILE", Value: home}},
			}}, agentadaptor.WithCloneProfile(t.TempDir(), agentadaptor.CloneProfileOptions{})),
			want: agentadaptor.ProfileKindHostManaged,
		},
		{
			name: "codex managed fallback is host managed",
			binding: codex.New(agentadaptor.CodexConfig{CommonConfig: agentadaptor.CommonConfig{
				Env: []agentadaptor.EnvBinding{{Name: "HOME", Value: filepath.Join(t.TempDir(), "no-shared-home")}, {Name: "USERPROFILE", Value: home}},
			}}, agentadaptor.WithDefaultIdentity(agentadaptor.AgentIdentity{TenantID: "tenant-a"})),
			want: agentadaptor.ProfileKindHostManaged,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sdk := agentadaptor.New(agentadaptor.WithDefaultAgent(tc.binding))
			snapshot, err := sdk.Admin().Default().ProfileSnapshot(context.Background())
			if err != nil {
				t.Fatalf("profile snapshot: %v", err)
			}
			if snapshot.Kind != tc.want {
				t.Fatalf("expected kind %q, got %#v", tc.want, snapshot)
			}
		})
	}
}

func TestAdminSyncProfileMaterializesSupportedResourcesHonestly(t *testing.T) {
	t.Setenv("CODEX_HOME", "")
	t.Setenv("AGENT_ADAPTOR_SKILL_CACHE_ROOT", filepath.Join(t.TempDir(), "skill-cache"))

	home := t.TempDir()
	profileDir := t.TempDir()
	sdk := agentadaptor.New(agentadaptor.WithDefaultAgent(codex.New(
		agentadaptor.CodexConfig{CommonConfig: agentadaptor.CommonConfig{
			Env: []agentadaptor.EnvBinding{{Name: "HOME", Value: home}, {Name: "USERPROFILE", Value: home}},
		}},
		agentadaptor.WithDedicatedProfile(profileDir),
		agentadaptor.WithDefaultSkills(agentadaptor.InlineSkill("main", "# Main")),
		agentadaptor.WithDefaultMCP(agentadaptor.MCPConfig{Servers: []agentadaptor.MCPServerSpec{{
			Key:       "local",
			Transport: agentadaptor.MCPTransportStdio,
			Command:   "echo",
		}}}),
		agentadaptor.WithDefaultAgents(agentadaptor.AgentSpec{Key: "reviewer", Content: "review things"}),
		agentadaptor.WithDefaultHooks(agentadaptor.HookSpec{Key: "pre", Event: "PreToolUse", Command: "echo"}),
		agentadaptor.WithDefaultInstructions(&agentadaptor.InstructionsBundleRef{ID: "team-instructions", Path: filepath.Join(home, "AGENTS.md")}),
		agentadaptor.WithDefaultProfileConfig(agentadaptor.ProfileConfigPatch{Key: "sandbox", FileKind: agentadaptor.ProfileConfigFileTOML, Path: "config.toml"}),
	)))

	snapshot, err := sdk.Admin().Default().SyncProfile(context.Background())
	if err != nil {
		t.Fatalf("sync profile: %v", err)
	}
	if _, err := os.Stat(filepath.Join(profileDir, "skills", "main")); err != nil {
		t.Fatalf("expected skill materialized in profile: %v", err)
	}
	rawConfig, err := os.ReadFile(filepath.Join(profileDir, "config.toml"))
	if err != nil {
		t.Fatalf("expected codex config.toml: %v", err)
	}
	if !strings.Contains(string(rawConfig), "local") {
		t.Fatalf("expected MCP server in config.toml, got %s", string(rawConfig))
	}

	assertResourceManaged(t, snapshot, agentadaptor.ProfileResourceSkills, "main")
	assertResourceManaged(t, snapshot, agentadaptor.ProfileResourceMCP, "local")
	assertResourceUnsupported(t, snapshot, agentadaptor.ProfileResourceAgents)
	assertResourceUnsupported(t, snapshot, agentadaptor.ProfileResourceHooks)
	assertResourceUnsupported(t, snapshot, agentadaptor.ProfileResourceInstructions)
	assertResourceUnsupported(t, snapshot, agentadaptor.ProfileResourceConfig)
}

func assertResourceManaged(t *testing.T, snapshot agentadaptor.ProfileSnapshot, kind agentadaptor.ProfileResourceKind, key string) {
	t.Helper()
	resource := findResource(t, snapshot, kind)
	for _, managed := range resource.Managed {
		if managed == key {
			return
		}
	}
	t.Fatalf("expected %s resource to manage %q, got %#v", kind, key, resource)
}

func assertResourceUnsupported(t *testing.T, snapshot agentadaptor.ProfileSnapshot, kind agentadaptor.ProfileResourceKind) {
	t.Helper()
	resource := findResource(t, snapshot, kind)
	if len(resource.Managed) != 0 {
		t.Fatalf("expected %s resource not to report managed keys, got %#v", kind, resource)
	}
	if resource.Error == "" || !strings.Contains(resource.Error, "not materialized") {
		t.Fatalf("expected %s resource to report unsupported materialization, got %#v", kind, resource)
	}
}

func findResource(t *testing.T, snapshot agentadaptor.ProfileSnapshot, kind agentadaptor.ProfileResourceKind) agentadaptor.ResourceSnapshot {
	t.Helper()
	for _, resource := range snapshot.Resources {
		if resource.Kind == kind {
			return resource
		}
	}
	t.Fatalf("missing %s resource in %#v", kind, snapshot.Resources)
	return agentadaptor.ResourceSnapshot{}
}
