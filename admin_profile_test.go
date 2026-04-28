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
	instructionsPath := filepath.Join(home, "AGENTS.md")
	if err := os.WriteFile(instructionsPath, []byte("# Team instructions\n"), 0o644); err != nil {
		t.Fatalf("write instructions source: %v", err)
	}
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
		agentadaptor.WithDefaultInstructions(&agentadaptor.InstructionsBundleRef{ID: "team-instructions", Path: instructionsPath}),
		agentadaptor.WithDefaultProfileConfig(agentadaptor.ProfileConfigPatch{
			Key:        "sandbox",
			Capability: "sandbox",
			Values:     map[string]any{"mode": "workspace-write"},
		}),
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
	if !strings.Contains(string(rawConfig), "sandbox_mode = 'workspace-write'") {
		t.Fatalf("expected capability config patch in config.toml, got %s", string(rawConfig))
	}
	if _, err := os.Stat(filepath.Join(profileDir, "AGENTS.md")); err != nil {
		t.Fatalf("expected Codex native instructions materialized in profile: %v", err)
	}
	if _, err := os.Stat(filepath.Join(profileDir, "agents", "reviewer.toml")); err != nil {
		t.Fatalf("expected agent materialized in profile: %v", err)
	}
	if _, err := os.Stat(filepath.Join(profileDir, "hooks.json")); err != nil {
		t.Fatalf("expected hooks materialized in profile: %v", err)
	}

	assertResourceManaged(t, snapshot, agentadaptor.ProfileResourceSkills, "main")
	assertResourceManaged(t, snapshot, agentadaptor.ProfileResourceMCP, "local")
	assertResourceManaged(t, snapshot, agentadaptor.ProfileResourceAgents, "reviewer")
	assertResourceManaged(t, snapshot, agentadaptor.ProfileResourceHooks, "pre")
	assertResourceManaged(t, snapshot, agentadaptor.ProfileResourceInstructions, "team-instructions")
	assertResourceManaged(t, snapshot, agentadaptor.ProfileResourceConfig, "sandbox")
}

func TestAdminConfigSchemaExposesProfileConfigCapabilities(t *testing.T) {
	cases := []struct {
		name       string
		binding    agentadaptor.AgentBinding
		fieldNames []string
	}{
		{
			name:       "codex",
			binding:    codex.New(agentadaptor.CodexConfig{}),
			fieldNames: []string{"profile_config.model", "profile_config.reasoning_effort", "profile_config.sandbox", "profile_config.approval"},
		},
		{
			name:       "claude",
			binding:    claude.New(agentadaptor.ClaudeConfig{}),
			fieldNames: []string{"profile_config.model", "profile_config.effort", "profile_config.permission", "profile_config.env"},
		},
		{
			name:       "cursor",
			binding:    cursor.New(agentadaptor.CursorConfig{}),
			fieldNames: []string{"profile_config.sandbox", "profile_config.approval", "profile_config.permissions", "profile_config.display"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sdk := agentadaptor.New(agentadaptor.WithDefaultAgent(tc.binding))
			schema, err := sdk.Admin().Default().ConfigSchema(context.Background())
			if err != nil {
				t.Fatalf("config schema: %v", err)
			}
			for _, name := range tc.fieldNames {
				field := findConfigField(t, schema, name)
				if field.Group != "profile_config" || field.Meta["profile_resource"] != "config" || field.Meta["capability"] == "" {
					t.Fatalf("profile config field %q missing capability metadata: %#v", name, field)
				}
			}
		})
	}
}

func assertResourceManaged(t *testing.T, snapshot agentadaptor.ProfileSnapshot, kind agentadaptor.ProfileResourceKind, key string) {
	t.Helper()
	resource := findResource(t, snapshot, kind)
	wantSupport := agentadaptor.ProfileResourceSupportPortableCore
	switch kind {
	case agentadaptor.ProfileResourceConfig:
		if resource.Support == agentadaptor.ProfileResourceSupportNativeEscape ||
			resource.Support == agentadaptor.ProfileResourceSupportPortableExtended {
			wantSupport = resource.Support
		} else {
			wantSupport = agentadaptor.ProfileResourceSupportPortableExtended
		}
	}
	if resource.Support != wantSupport {
		t.Fatalf("expected %s resource to report %s support, got %#v", kind, wantSupport, resource)
	}
	if resource.Materialization == agentadaptor.ProfileResourceMaterializationNotMaterialized {
		t.Fatalf("expected %s resource to report materialization, got %#v", kind, resource)
	}
	for _, managed := range resource.Managed {
		if managed == key {
			return
		}
	}
	t.Fatalf("expected %s resource to manage %q, got %#v", kind, key, resource)
}

func findConfigField(t *testing.T, schema *agentadaptor.ConfigSchema, name string) agentadaptor.ConfigField {
	t.Helper()
	if schema == nil {
		t.Fatalf("nil config schema")
	}
	for _, field := range schema.Fields {
		if field.Name == name {
			return field
		}
	}
	t.Fatalf("missing config field %q in %#v", name, schema.Fields)
	return agentadaptor.ConfigField{}
}

func assertResourceUnsupported(t *testing.T, snapshot agentadaptor.ProfileSnapshot, kind agentadaptor.ProfileResourceKind) {
	t.Helper()
	resource := findResource(t, snapshot, kind)
	if resource.Support != agentadaptor.ProfileResourceSupportUnsupported {
		t.Fatalf("expected %s resource to report unsupported support status, got %#v", kind, resource)
	}
	if resource.Materialization != agentadaptor.ProfileResourceMaterializationNotMaterialized {
		t.Fatalf("expected %s resource to report no materialization, got %#v", kind, resource)
	}
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
