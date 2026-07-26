package profile_test

import (
	"reflect"
	"testing"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/profile"
)

// Compile-time proof that every profile name is an alias for the existing
// public contract type, not a parallel declaration.
var (
	_ agentadaptor.ProfileSelection       = profile.Native()
	_ agentadaptor.ProfileMode            = profile.ModeClone
	_ agentadaptor.CloneProfileOptions    = profile.CloneOptions{}
	_ agentadaptor.CloneProfileAuthMode   = profile.AuthLink
	_ agentadaptor.ProfileResources       = profile.Resources{}
	_ agentadaptor.AgentSpec              = profile.SubAgent{}
	_ agentadaptor.AgentToolPolicy        = profile.ToolPolicy{}
	_ agentadaptor.HookSpec               = profile.Hook{}
	_ agentadaptor.HookEvent              = profile.HookEventPreTool
	_ agentadaptor.HookMatcher            = profile.HookMatcher{}
	_ agentadaptor.HookHandler            = profile.HookHandler{}
	_ agentadaptor.HookFailPolicy         = profile.HookFailPolicyClosed
	_ *agentadaptor.InstructionsBundleRef = profile.Text("x")
	_ agentadaptor.InstructionScope       = profile.InstructionScopeProject
	_ agentadaptor.InstructionMode        = profile.InstructionModeReplace
	_ agentadaptor.ProfileConfigPatch     = profile.ConfigPatch{}
	_ agentadaptor.NativeConfigPatch      = profile.NativeConfigPatch{}
	_ agentadaptor.ProfileConfigFileKind  = profile.ConfigFileJSON
)

// selectionFromOption applies a legacy binding-level profile option and
// returns the ProfileSelection it stored.
func selectionFromOption(t *testing.T, opt agentadaptor.AgentOption) agentadaptor.ProfileSelection {
	t.Helper()
	var defaults agentadaptor.AgentDefaults
	opt(&defaults)
	if defaults.Profile == nil {
		t.Fatal("legacy option did not set AgentDefaults.Profile")
	}
	return *defaults.Profile
}

func TestSelectionConstructorsMatchLegacyOptions(t *testing.T) {
	cases := []struct {
		name   string
		v1     agentadaptor.ProfileSelection
		legacy agentadaptor.AgentOption
	}{
		{
			name:   "native",
			v1:     profile.Native(),
			legacy: agentadaptor.WithNativeProfile(),
		},
		{
			name:   "dedicated",
			v1:     profile.Dedicated(`C:\profiles\tenant-a`),
			legacy: agentadaptor.WithDedicatedProfile(`C:\profiles\tenant-a`),
		},
		{
			name:   "clone native zero options",
			v1:     profile.CloneNative(`C:\profiles\clone`),
			legacy: agentadaptor.WithCloneProfile(`C:\profiles\clone`, agentadaptor.CloneProfileOptions{}),
		},
		{
			name: "clone native copy settings mcp skills",
			v1:   profile.CloneNative(`C:\profiles\clone`, profile.CopySettings(), profile.CopyMCP(), profile.CopySkills()),
			legacy: agentadaptor.WithCloneProfile(`C:\profiles\clone`, agentadaptor.CloneProfileOptions{
				IncludeSettings: true,
				IncludeMCP:      true,
				IncludeSkills:   true,
			}),
		},
		{
			name: "clone native link auth",
			v1:   profile.CloneNative(`C:\profiles\clone`, profile.LinkAuth()),
			legacy: agentadaptor.WithCloneProfile(`C:\profiles\clone`, agentadaptor.CloneProfileOptions{
				AuthMode: agentadaptor.CloneProfileAuthLink,
			}),
		},
		{
			name: "clone native copy auth",
			v1:   profile.CloneNative(`C:\profiles\clone`, profile.CopyAuth()),
			legacy: agentadaptor.WithCloneProfile(`C:\profiles\clone`, agentadaptor.CloneProfileOptions{
				AuthMode: agentadaptor.CloneProfileAuthCopy,
			}),
		},
		{
			name: "clone from template",
			v1: profile.CloneFrom(`C:\templates\golden`, `C:\profiles\job-42`,
				profile.CopySettings(), profile.CopySkills(), profile.LinkAuth()),
			legacy: agentadaptor.WithCloneProfileFrom(`C:\templates\golden`, `C:\profiles\job-42`, agentadaptor.CloneProfileOptions{
				IncludeSettings: true,
				IncludeSkills:   true,
				AuthMode:        agentadaptor.CloneProfileAuthLink,
			}),
		},
		{
			name: "clone with legacy include-auth struct via WithOptions",
			v1: profile.CloneNative(`C:\profiles\clone`, profile.WithOptions(agentadaptor.CloneProfileOptions{
				IncludeSettings: true,
				IncludeAuth:     true,
			})),
			legacy: agentadaptor.WithCloneProfile(`C:\profiles\clone`, agentadaptor.CloneProfileOptions{
				IncludeSettings: true,
				IncludeAuth:     true,
			}),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			legacy := selectionFromOption(t, tc.legacy)
			if !reflect.DeepEqual(tc.v1, legacy) {
				t.Fatalf("v1 constructor product differs from legacy option\n v1:     %+v\n legacy: %+v", tc.v1, legacy)
			}
		})
	}
}

func TestDefaultSelectionIsUnsetMode(t *testing.T) {
	got := profile.Default()
	if !reflect.DeepEqual(got, agentadaptor.ProfileSelection{}) {
		t.Fatalf("Default() = %+v, want zero ProfileSelection", got)
	}
	if got.Mode != agentadaptor.ProfileModeUnset {
		t.Fatalf("Default().Mode = %q, want ProfileModeUnset", got.Mode)
	}
	// The legacy API expresses this form by not applying any profile option.
	var defaults agentadaptor.AgentDefaults
	if defaults.Profile != nil {
		t.Fatal("zero AgentDefaults unexpectedly carries a profile selection")
	}
}

func TestCloneConstructorsAlwaysPinCloneOptions(t *testing.T) {
	// Legacy WithCloneProfile always stores a non-nil Clone pointer, even for
	// the zero option struct; the v1 constructors must match.
	for name, sel := range map[string]agentadaptor.ProfileSelection{
		"CloneNative": profile.CloneNative(`C:\profiles\clone`),
		"CloneFrom":   profile.CloneFrom(`C:\templates\golden`, `C:\profiles\clone`),
	} {
		if sel.Clone == nil {
			t.Fatalf("%s: Clone pointer is nil, want non-nil zero CloneOptions", name)
		}
		if *sel.Clone != (agentadaptor.CloneProfileOptions{}) {
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
		profile.WithOptions(agentadaptor.CloneProfileOptions{IncludeSettings: true, IncludeAuth: true}),
		profile.LinkAuth(),
	)
	want := agentadaptor.CloneProfileOptions{
		IncludeSettings: true,
		IncludeAuth:     true,
		AuthMode:        agentadaptor.CloneProfileAuthLink,
	}
	if *sel.Clone != want {
		t.Fatalf("composed clone options = %+v, want %+v", *sel.Clone, want)
	}
}

func TestSelectionModeConstantsMatchRoot(t *testing.T) {
	pairs := []struct {
		name      string
		got, want agentadaptor.ProfileMode
	}{
		{"unset", profile.ModeUnset, agentadaptor.ProfileModeUnset},
		{"native", profile.ModeNative, agentadaptor.ProfileModeNative},
		{"dedicated", profile.ModeDedicated, agentadaptor.ProfileModeDedicated},
		{"clone", profile.ModeClone, agentadaptor.ProfileModeClone},
	}
	for _, pair := range pairs {
		if pair.got != pair.want {
			t.Fatalf("Mode constant %s = %q, want %q", pair.name, pair.got, pair.want)
		}
	}
	auth := []struct {
		name      string
		got, want agentadaptor.CloneProfileAuthMode
	}{
		{"none", profile.AuthNone, agentadaptor.CloneProfileAuthNone},
		{"copy", profile.AuthCopy, agentadaptor.CloneProfileAuthCopy},
		{"link", profile.AuthLink, agentadaptor.CloneProfileAuthLink},
	}
	for _, pair := range auth {
		if pair.got != pair.want {
			t.Fatalf("AuthMode constant %s = %q, want %q", pair.name, pair.got, pair.want)
		}
	}
}

func TestResourceEnumConstantsMatchRoot(t *testing.T) {
	if profile.HookEventPreTool != agentadaptor.HookEventPreTool ||
		profile.HookEventStopFailure != agentadaptor.HookEventStopFailure ||
		profile.HookEventSessionStart != agentadaptor.HookEventSessionStart {
		t.Fatal("hook event constants diverge from root aliases")
	}
	if profile.HookMatcherSubjectTool != agentadaptor.HookMatcherSubjectTool ||
		profile.HookMatcherSyntaxRegex != agentadaptor.HookMatcherSyntaxRegex {
		t.Fatal("hook matcher constants diverge from root aliases")
	}
	if profile.HookHandlerMCPTool != agentadaptor.HookHandlerMCPTool ||
		profile.HookFailPolicyClosed != agentadaptor.HookFailPolicyClosed {
		t.Fatal("hook handler/fail-policy constants diverge from root aliases")
	}
	if profile.InstructionScopeProject != agentadaptor.InstructionScopeProject ||
		profile.InstructionModeReplace != agentadaptor.InstructionModeReplace {
		t.Fatal("instruction constants diverge from root aliases")
	}
	if profile.ConfigFileJSON != agentadaptor.ProfileConfigFileJSON ||
		profile.ConfigFileTOML != agentadaptor.ProfileConfigFileTOML {
		t.Fatal("config file kind constants diverge from root aliases")
	}
}

func TestTextBuildsInlineInstructions(t *testing.T) {
	got := profile.Text("Follow ACME coding standards.")
	want := &agentadaptor.InstructionsBundleRef{Content: "Follow ACME coding standards."}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Text() = %+v, want %+v", got, want)
	}
}

// declaredResources builds a Resources value exercising every sub-resource
// family using only profile-package vocabulary.
func declaredResources() profile.Resources {
	return profile.Resources{
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

	// profile.Resources is the root ProfileResources — assignment needs no
	// conversion and preserves every field.
	var asRoot agentadaptor.ProfileResources = declared
	if !reflect.DeepEqual(asRoot, declared) {
		t.Fatal("assigning profile.Resources to root ProfileResources changed its contents")
	}

	// Feeding it through the existing binding-level option must land each
	// sub-resource family on AgentDefaults unchanged.
	var defaults agentadaptor.AgentDefaults
	agentadaptor.WithDefaultProfileResources(declared)(&defaults)

	if !reflect.DeepEqual(defaults.Agents, declared.Agents) {
		t.Fatalf("agents not passed through\n got:  %+v\n want: %+v", defaults.Agents, declared.Agents)
	}
	if !reflect.DeepEqual(defaults.Hooks, declared.Hooks) {
		t.Fatalf("hooks not passed through\n got:  %+v\n want: %+v", defaults.Hooks, declared.Hooks)
	}
	if !reflect.DeepEqual(defaults.Instructions, declared.Instructions) {
		t.Fatalf("instructions not passed through\n got:  %+v\n want: %+v", defaults.Instructions, declared.Instructions)
	}
	if !reflect.DeepEqual(defaults.ProfileConfig, declared.Config) {
		t.Fatalf("config patches not passed through\n got:  %+v\n want: %+v", defaults.ProfileConfig, declared.Config)
	}

	// The option clones inputs, so later host mutation of the declared value
	// must not leak into the binding defaults.
	declared.Agents[0].Key = "mutated"
	declared.Hooks[0].Handler.Env["AUDIT"] = "0"
	if defaults.Agents[0].Key != "tester" {
		t.Fatal("binding defaults alias the caller's agent slice")
	}
	if defaults.Hooks[0].Handler.Env["AUDIT"] != "1" {
		t.Fatal("binding defaults alias the caller's hook env map")
	}
}
