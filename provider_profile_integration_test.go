package adaptor_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/claude"
	"github.com/agent-dance/agent-adaptor/codex"
	"github.com/agent-dance/agent-adaptor/cursor"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/mcp"
	"github.com/agent-dance/agent-adaptor/profile"
	"github.com/agent-dance/agent-adaptor/skill"
)

// These provider integration contracts preserve the v1-relevant coverage
// from the deleted root admin_profile_test.go. They intentionally exercise
// only the v1 Agent/Inspect/Profile surface and configured provider Drivers.

func TestProviderProfileStateClassifiesEffectiveProfileKind(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	t.Setenv("CURSOR_HOME", "")

	home := t.TempDir()
	canonicalClaude := filepath.Join(home, ".claude")
	if err := os.MkdirAll(canonicalClaude, 0o755); err != nil {
		t.Fatalf("mkdir canonical Claude profile: %v", err)
	}
	env := func(bindings ...driver.EnvBinding) driver.CommonConfig {
		return driver.CommonConfig{Env: append([]driver.EnvBinding{
			{Name: "HOME", Value: home},
			{Name: "USERPROFILE", Value: home},
		}, bindings...)}
	}

	cases := []struct {
		name string
		new  func() *adaptor.Agent
		want adaptor.ProfileKind
	}{
		{
			name: "claude dedicated is host managed",
			new: func() *adaptor.Agent {
				return adaptor.New(
					claude.Driver(claude.Config{CommonConfig: env()}),
					adaptor.WithProfile(profile.Dedicated(t.TempDir())),
				)
			},
			want: adaptor.ProfileKindHostManaged,
		},
		{
			name: "claude canonical env is shared",
			new: func() *adaptor.Agent {
				return adaptor.New(claude.Driver(claude.Config{CommonConfig: env(
					driver.EnvBinding{Name: "CLAUDE_CONFIG_DIR", Value: canonicalClaude},
				)}))
			},
			want: adaptor.ProfileKindShared,
		},
		{
			name: "claude noncanonical env is host managed",
			new: func() *adaptor.Agent {
				return adaptor.New(claude.Driver(claude.Config{CommonConfig: env(
					driver.EnvBinding{Name: "CLAUDE_CONFIG_DIR", Value: t.TempDir()},
				)}))
			},
			want: adaptor.ProfileKindHostManaged,
		},
		{
			name: "cursor clone is host managed",
			new: func() *adaptor.Agent {
				return adaptor.New(
					cursor.Driver(cursor.Config{CommonConfig: env()}),
					adaptor.WithProfile(profile.CloneNative(t.TempDir())),
				)
			},
			want: adaptor.ProfileKindHostManaged,
		},
		{
			name: "codex managed fallback is host managed",
			new: func() *adaptor.Agent {
				return adaptor.New(
					codex.Driver(codex.Config{CommonConfig: driver.CommonConfig{Env: []driver.EnvBinding{
						{Name: "HOME", Value: filepath.Join(t.TempDir(), "no-shared-home")},
						{Name: "USERPROFILE", Value: home},
					}}}),
					adaptor.WithIdentity(adaptor.Identity{Tenant: "tenant-a"}),
				)
			},
			want: adaptor.ProfileKindHostManaged,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snapshot, err := tc.new().ProfileState(context.Background())
			if err != nil {
				t.Fatalf("profile state: %v", err)
			}
			if snapshot.Kind != tc.want {
				t.Fatalf("profile kind = %q, want %q; snapshot: %#v", snapshot.Kind, tc.want, snapshot)
			}
		})
	}
}

func TestCodexSyncProfileMaterializesAllSupportedResources(t *testing.T) {
	t.Setenv("CODEX_HOME", "")
	t.Setenv(skill.SkillCacheRootEnv, filepath.Join(t.TempDir(), "skill-cache"))

	home := t.TempDir()
	profileDir := t.TempDir()
	instructionsPath := filepath.Join(home, "AGENTS.md")
	if err := os.WriteFile(instructionsPath, []byte("# Team instructions\n"), 0o644); err != nil {
		t.Fatalf("write instructions source: %v", err)
	}

	agent := adaptor.New(
		codex.Driver(codex.Config{CommonConfig: driver.CommonConfig{Env: []driver.EnvBinding{
			{Name: "HOME", Value: home},
			{Name: "USERPROFILE", Value: home},
		}}}),
		adaptor.WithProfile(profile.Dedicated(profileDir)),
		adaptor.WithProfileResources(profile.Resources{
			Skills: []skill.Ref{skill.Inline("main", "# Main")},
			MCP:    []mcp.Server{mcp.Stdio("local", "echo")},
			Agents: []profile.SubAgent{{
				Key:          "reviewer",
				Instructions: "review things",
			}},
			Hooks: []profile.Hook{{
				Key:   "pre",
				Event: profile.HookEventPreTool,
				Handler: profile.HookHandler{
					Type:    profile.HookHandlerCommand,
					Command: "echo",
				},
			}},
			Instructions: &profile.Instructions{
				ID:   "team-instructions",
				Path: instructionsPath,
			},
			Config: []profile.ConfigPatch{{
				Key:        "sandbox",
				Capability: "sandbox",
				Values:     map[string]any{"mode": "workspace-write"},
			}},
		}),
	)

	snapshot, err := agent.SyncProfile(context.Background())
	if err != nil {
		t.Fatalf("sync profile: %v", err)
	}
	if _, err := os.Stat(filepath.Join(profileDir, "skills", "main")); err != nil {
		t.Fatalf("expected skill materialized in profile: %v", err)
	}
	rawConfig, err := os.ReadFile(filepath.Join(profileDir, "config.toml"))
	if err != nil {
		t.Fatalf("expected Codex config.toml: %v", err)
	}
	configText := string(rawConfig)
	if !strings.Contains(configText, "local") {
		t.Fatalf("expected MCP server in config.toml, got %s", configText)
	}
	if !strings.Contains(configText, "sandbox_mode = 'workspace-write'") {
		t.Fatalf("expected capability config patch in config.toml, got %s", configText)
	}
	for _, path := range []string{
		filepath.Join(profileDir, "AGENTS.md"),
		filepath.Join(profileDir, "agents", "reviewer.toml"),
		filepath.Join(profileDir, "hooks.json"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected profile resource %s: %v", path, err)
		}
	}

	assertProviderProfileResourceManaged(t, snapshot, adaptor.ProfileResourceSkills, "main")
	assertProviderProfileResourceManaged(t, snapshot, adaptor.ProfileResourceMCP, "local")
	assertProviderProfileResourceManaged(t, snapshot, adaptor.ProfileResourceAgents, "reviewer")
	assertProviderProfileResourceManaged(t, snapshot, adaptor.ProfileResourceHooks, "pre")
	assertProviderProfileResourceManaged(t, snapshot, adaptor.ProfileResourceInstructions, "team-instructions")
	assertProviderProfileResourceManaged(t, snapshot, adaptor.ProfileResourceConfig, "sandbox")
}

func TestProviderConfigSchemaExposesProfileConfigCapabilities(t *testing.T) {
	cases := []struct {
		name       string
		driver     driver.Driver
		fieldNames []string
	}{
		{
			name:       "codex",
			driver:     codex.Driver(codex.Config{}),
			fieldNames: []string{"profile_config.model", "profile_config.reasoning_effort", "profile_config.sandbox", "profile_config.approval"},
		},
		{
			name:       "claude",
			driver:     claude.Driver(claude.Config{}),
			fieldNames: []string{"profile_config.model", "profile_config.effort", "profile_config.permission", "profile_config.env"},
		},
		{
			name:       "cursor",
			driver:     cursor.Driver(cursor.Config{}),
			fieldNames: []string{"profile_config.sandbox", "profile_config.approval", "profile_config.permissions", "profile_config.display"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			schema, err := adaptor.New(tc.driver).Inspect().ConfigSchema(context.Background())
			if err != nil {
				t.Fatalf("config schema: %v", err)
			}
			for _, name := range tc.fieldNames {
				field := findProviderProfileConfigField(t, schema, name)
				if field.Group != "profile_config" || field.Meta["profile_resource"] != "config" || field.Meta["capability"] == "" {
					t.Fatalf("profile config field %q missing capability metadata: %#v", name, field)
				}
			}
		})
	}
}

func assertProviderProfileResourceManaged(t *testing.T, snapshot adaptor.ProfileSnapshot, kind adaptor.ProfileResourceKind, key string) {
	t.Helper()
	resource := findProviderProfileResource(t, snapshot, kind)
	wantSupport := adaptor.ProfileResourceSupportPortableCore
	if kind == adaptor.ProfileResourceConfig {
		if resource.Support == adaptor.ProfileResourceSupportNativeEscape || resource.Support == adaptor.ProfileResourceSupportPortableExtended {
			wantSupport = resource.Support
		} else {
			wantSupport = adaptor.ProfileResourceSupportPortableExtended
		}
	}
	if resource.Support != wantSupport {
		t.Fatalf("%s support = %s, want %s; resource: %#v", kind, resource.Support, wantSupport, resource)
	}
	if resource.Materialization == adaptor.ProfileResourceMaterializationNotMaterialized {
		t.Fatalf("expected %s to report materialization, got %#v", kind, resource)
	}
	for _, managed := range resource.Managed {
		if managed == key {
			return
		}
	}
	t.Fatalf("expected %s to manage %q, got %#v", kind, key, resource)
}

func findProviderProfileResource(t *testing.T, snapshot adaptor.ProfileSnapshot, kind adaptor.ProfileResourceKind) adaptor.ResourceSnapshot {
	t.Helper()
	for _, resource := range snapshot.Resources {
		if resource.Kind == kind {
			return resource
		}
	}
	t.Fatalf("missing %s resource in %#v", kind, snapshot.Resources)
	return adaptor.ResourceSnapshot{}
}

func findProviderProfileConfigField(t *testing.T, schema *adaptor.ConfigSchema, name string) driver.ConfigField {
	t.Helper()
	if schema == nil {
		t.Fatal("nil config schema")
	}
	for _, field := range schema.Fields {
		if field.Name == name {
			return field
		}
	}
	t.Fatalf("missing config field %q in %#v", name, schema.Fields)
	return driver.ConfigField{}
}
