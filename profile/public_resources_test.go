package profile_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/mcp"
	"github.com/agent-dance/agent-adaptor/profile"
	"github.com/agent-dance/agent-adaptor/skill"
)

func TestResourceContractsExcludeRemovedFields(t *testing.T) {
	tests := []struct {
		name      string
		typeOf    reflect.Type
		forbidden []string
	}{
		{name: "profile.SubAgent", typeOf: reflect.TypeOf(profile.SubAgent{}), forbidden: []string{"Content"}},
		{name: "profile.Hook", typeOf: reflect.TypeOf(profile.Hook{}), forbidden: []string{"Matcher", "Command", "Args", "Env"}},
		{name: "profile.ConfigPatch", typeOf: reflect.TypeOf(profile.ConfigPatch{}), forbidden: []string{"FileKind", "Path", "Section"}},
		{name: "profile.CloneOptions", typeOf: reflect.TypeOf(profile.CloneOptions{}), forbidden: []string{"IncludeAuth"}},
	}
	for _, test := range tests {
		for _, field := range test.forbidden {
			if _, ok := test.typeOf.FieldByName(field); ok {
				t.Errorf("%s still exposes removed field %s", test.name, field)
			}
		}
	}
}

// TestResourcesPublicVocabulary is an external-package compile boundary: a
// host can fill every resource family using only the three consumer
// vocabulary packages, without importing driver or internal/engine.
func TestResourcesPublicVocabulary(t *testing.T) {
	resources := profile.Resources{
		Skills: []skill.Ref{skill.Inline("review", "# review")},
		MCP: []mcp.Server{
			mcp.Stdio("repo", "repo-server", mcp.Args("--stdio"), mcp.Env(map[string]string{"MODE": "strict"})),
		},
		Agents: []profile.SubAgent{{
			Key:        "reviewer",
			ToolPolicy: &profile.ToolPolicy{Allow: []string{"read"}},
		}},
		Hooks: []profile.Hook{{
			Key:      "audit",
			Event:    profile.HookEventPreTool,
			Handler:  profile.HookHandler{Type: profile.HookHandlerCommand, Command: "audit"},
			Timeout:  time.Second,
			Disabled: false,
		}},
		Instructions: profile.Text("Follow repository policy."),
		Config: []profile.ConfigPatch{{
			Key:        "telemetry",
			Capability: "telemetry.opt_out",
			Values:     map[string]any{"enabled": false},
		}},
	}

	if len(resources.Skills) != 1 || len(resources.MCP) != 1 || len(resources.Agents) != 1 || len(resources.Hooks) != 1 || len(resources.Config) != 1 {
		t.Fatalf("complete resource declaration = %+v", resources)
	}
	if resources.Instructions == nil || resources.Instructions.Content != "Follow repository policy." {
		t.Fatalf("instructions = %+v", resources.Instructions)
	}
}
