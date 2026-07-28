package profile_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/mcp"
	"github.com/agent-dance/agent-adaptor/profile"
	"github.com/agent-dance/agent-adaptor/skill"
)

func TestSelectionConstructors(t *testing.T) {
	cases := []struct {
		name string
		got  profile.Selection
		want profile.Selection
	}{
		{
			name: "native",
			got:  profile.Native(),
			want: profile.Selection{Mode: profile.ModeNative},
		},
		{
			name: "dedicated",
			got:  profile.Dedicated(`C:\profiles\tenant-a`),
			want: profile.Selection{Mode: profile.ModeDedicated, Dir: `C:\profiles\tenant-a`},
		},
		{
			name: "clone native zero options",
			got:  profile.CloneNative(`C:\profiles\clone`),
			want: profile.Selection{Mode: profile.ModeClone, Dir: `C:\profiles\clone`, Clone: &profile.CloneOptions{}},
		},
		{
			name: "clone native copy settings mcp skills",
			got:  profile.CloneNative(`C:\profiles\clone`, profile.CopySettings(), profile.CopyMCP(), profile.CopySkills()),
			want: profile.Selection{Mode: profile.ModeClone, Dir: `C:\profiles\clone`, Clone: &profile.CloneOptions{
				IncludeSettings: true, IncludeMCP: true, IncludeSkills: true,
			}},
		},
		{
			name: "clone native link auth",
			got:  profile.CloneNative(`C:\profiles\clone`, profile.LinkAuth()),
			want: profile.Selection{Mode: profile.ModeClone, Dir: `C:\profiles\clone`, Clone: &profile.CloneOptions{AuthMode: profile.AuthLink}},
		},
		{
			name: "clone native copy auth",
			got:  profile.CloneNative(`C:\profiles\clone`, profile.CopyAuth()),
			want: profile.Selection{Mode: profile.ModeClone, Dir: `C:\profiles\clone`, Clone: &profile.CloneOptions{AuthMode: profile.AuthCopy}},
		},
		{
			name: "clone from template",
			got: profile.CloneFrom(`C:\templates\golden`, `C:\profiles\job-42`,
				profile.CopySettings(), profile.CopySkills(), profile.LinkAuth()),
			want: profile.Selection{Mode: profile.ModeClone, From: `C:\templates\golden`, Dir: `C:\profiles\job-42`, Clone: &profile.CloneOptions{
				IncludeSettings: true, IncludeSkills: true, AuthMode: profile.AuthLink,
			}},
		},
		{
			name: "clone with prebuilt options",
			got: profile.CloneNative(`C:\profiles\clone`, profile.WithOptions(profile.CloneOptions{
				IncludeSettings: true,
				AuthMode:        profile.AuthCopy,
			})),
			want: profile.Selection{Mode: profile.ModeClone, Dir: `C:\profiles\clone`, Clone: &profile.CloneOptions{
				IncludeSettings: true,
				AuthMode:        profile.AuthCopy,
			}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !reflect.DeepEqual(tc.got, tc.want) {
				t.Fatalf("constructor product differs\n got:  %+v\n want: %+v", tc.got, tc.want)
			}
		})
	}
}

func TestDefaultSelectionIsUnsetMode(t *testing.T) {
	got := profile.Default()
	if !reflect.DeepEqual(got, profile.Selection{}) {
		t.Fatalf("Default() = %+v, want zero ProfileSelection", got)
	}
	if got.Mode != profile.ModeUnset {
		t.Fatalf("Default().Mode = %q, want ProfileModeUnset", got.Mode)
	}
}

func TestCloneConstructorsAlwaysPinCloneOptions(t *testing.T) {
	for name, sel := range map[string]profile.Selection{
		"CloneNative": profile.CloneNative(`C:\profiles\clone`),
		"CloneFrom":   profile.CloneFrom(`C:\templates\golden`, `C:\profiles\clone`),
	} {
		if sel.Clone == nil {
			t.Fatalf("%s: Clone pointer is nil, want non-nil zero CloneOptions", name)
		}
		if *sel.Clone != (profile.CloneOptions{}) {
			t.Fatalf("%s: Clone = %+v, want zero CloneOptions", name, *sel.Clone)
		}
	}
}

func TestCloneOptionProducts(t *testing.T) {
	cases := []struct {
		name string
		opt  profile.CloneOption
		want profile.CloneOptions
	}{
		{"CopySettings", profile.CopySettings(), profile.CloneOptions{IncludeSettings: true}},
		{"CopyMCP", profile.CopyMCP(), profile.CloneOptions{IncludeMCP: true}},
		{"CopySkills", profile.CopySkills(), profile.CloneOptions{IncludeSkills: true}},
		{"CopyAuth", profile.CopyAuth(), profile.CloneOptions{AuthMode: profile.AuthCopy}},
		{"LinkAuth", profile.LinkAuth(), profile.CloneOptions{AuthMode: profile.AuthLink}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got profile.CloneOptions
			tc.opt(&got)
			if got != tc.want {
				t.Fatalf("%s product = %+v, want %+v", tc.name, got, tc.want)
			}
		})
	}
}

func TestWithOptionsComposesWithLaterOptions(t *testing.T) {
	sel := profile.CloneNative(`C:\profiles\clone`,
		profile.WithOptions(profile.CloneOptions{IncludeSettings: true, AuthMode: profile.AuthCopy}),
		profile.LinkAuth(),
	)
	want := profile.CloneOptions{
		IncludeSettings: true,
		AuthMode:        profile.AuthLink,
	}
	if *sel.Clone != want {
		t.Fatalf("composed clone options = %+v, want %+v", *sel.Clone, want)
	}
}

func TestResourceEnumWireValues(t *testing.T) {
	values := map[string]string{
		"hook event":      string(profile.HookEventPreTool),
		"matcher subject": string(profile.HookMatcherSubjectTool),
		"matcher syntax":  string(profile.HookMatcherSyntaxRegex),
		"handler":         string(profile.HookHandlerMCPTool),
		"fail policy":     string(profile.HookFailPolicyClosed),
		"scope":           string(profile.InstructionScopeProject),
		"mode":            string(profile.InstructionModeReplace),
		"config kind":     string(profile.ConfigFileTOML),
	}
	want := map[string]string{
		"hook event":      "pre_tool",
		"matcher subject": "tool",
		"matcher syntax":  "regex",
		"handler":         "mcp_tool",
		"fail policy":     "closed",
		"scope":           "project",
		"mode":            "replace",
		"config kind":     "toml",
	}
	if !reflect.DeepEqual(values, want) {
		t.Fatalf("resource enum wire values = %#v, want %#v", values, want)
	}
}

func TestTextBuildsInlineInstructions(t *testing.T) {
	got := profile.Text("Follow ACME coding standards.")
	want := &profile.Instructions{Content: "Follow ACME coding standards."}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Text() = %+v, want %+v", got, want)
	}
}

// declaredResources exercises every resource family using only public
// consumer vocabulary packages.
func declaredResources() profile.Resources {
	return profile.Resources{
		Skills: []skill.Ref{skill.Inline("triage", "# triage")},
		MCP: []mcp.Server{
			mcp.Stdio("docs", "docs-server", mcp.Args("--stdio")),
		},
		Agents: []profile.SubAgent{{
			Key:             "tester",
			RuntimeName:     "acme-tester",
			Description:     "runs the test suite",
			Instructions:    "Run go test and summarize failures.",
			Model:           "sonnet",
			ReasoningEffort: "high",
			ToolPolicy:      &profile.ToolPolicy{Allow: []string{"Bash"}, Deny: []string{"WebSearch"}},
			PermissionMode:  "auto",
			SandboxMode:     "workspace-write",
			MCPServers:      []string{"docs"},
			Skills:          []string{"triage"},
			Hooks: []profile.Hook{{
				Key:     "tester-guard",
				Event:   profile.HookEventPreTool,
				Handler: profile.HookHandler{Type: profile.HookHandlerCommand, Command: "guard.exe"},
			}},
			Native:   map[string]any{"x-provider": "value"},
			Metadata: map[string]string{"team": "qa"},
		}},
		Hooks: []profile.Hook{{
			Key:   "audit-shell",
			Event: profile.HookEventPreShell,
			MatcherSpec: profile.HookMatcher{
				Subject: profile.HookMatcherSubjectCommand,
				Syntax:  profile.HookMatcherSyntaxPrefix,
				Pattern: "rm ",
			},
			Handler: profile.HookHandler{
				Type:    profile.HookHandlerCommand,
				Command: "audit.exe",
				Args:    []string{"--strict"},
				Env:     map[string]string{"AUDIT": "1"},
			},
			Timeout:       5 * time.Second,
			FailPolicy:    profile.HookFailPolicyClosed,
			StatusMessage: "auditing shell command",
			Native:        map[string]any{"provider": map[string]any{"k": "v"}},
			Metadata:      map[string]string{"owner": "sec"},
		}},
		Instructions: profile.Text("Follow ACME coding standards."),
		Config: []profile.ConfigPatch{
			{
				Key:        "telemetry",
				Capability: "telemetry.opt_out",
				Values:     map[string]any{"enabled": false},
			},
			{
				Key: "native-toml",
				Native: &profile.NativeConfigPatch{
					Provider: "codex",
					FileKind: profile.ConfigFileTOML,
					Path:     "config.toml",
					Section:  "tools",
					Values:   map[string]any{"web_search": true},
				},
			},
		},
	}
}

func TestResourcesSubResourceFieldsPassThrough(t *testing.T) {
	declared := declaredResources()
	if len(declared.Skills) != 1 || len(declared.MCP) != 1 || len(declared.Agents) != 1 || len(declared.Hooks) != 1 || len(declared.Config) != 2 {
		t.Fatalf("complete profile.Resources declaration lost a resource family: %+v", declared)
	}
	if declared.MCP[0].Key != "docs" || declared.Instructions.Content == "" {
		t.Fatalf("public resource vocabulary did not retain values: %+v", declared)
	}
}
