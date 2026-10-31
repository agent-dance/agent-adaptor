package adaptor_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/opencode"
	"github.com/agent-dance/agent-adaptor/profile"
)

func TestLiveOpenCodeProfileAgents(t *testing.T) {
	if os.Getenv("AGENT_ADAPTOR_LIVE_CONFORMANCE") != "1" || os.Getenv("AGENT_ADAPTOR_OPENCODE_LIVE") != "1" {
		t.Skip("both live gates required")
	}
	model := os.Getenv("AGENT_ADAPTOR_OPENCODE_MODEL")
	if model == "" {
		t.Fatal("an explicit model is required")
	}
	makeDriver := func() driver.Driver {
		return opencode.Driver(opencode.Config{Command: os.Getenv("AGENT_ADAPTOR_OPENCODE_COMMAND"), Model: model, Pure: true})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	d := makeDriver()
	source, err := d.(driver.ProfileReporter).GetProfile(ctx, nil, driver.AgentIdentity{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(t.TempDir(), "profile")
	spec := profile.SubAgent{Key: "host/proof", RuntimeName: "adaptor-proof", Description: "Returns the private delegation proof word when asked.", Instructions: "When asked for the delegation proof word, reply with exactly SUBAGENT_PROOF_962. Never use tools.", Model: model, PermissionMode: "dontAsk"}
	a := adaptor.New(d, adaptor.WithProfile(profile.CloneFrom(source.Dir, directory, profile.CopySettings())), adaptor.WithProfileResources(profile.Resources{Agents: []profile.SubAgent{spec}}))
	if _, err := a.SyncProfile(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(ctx); err != nil {
		t.Fatal(err)
	}
	fresh := adaptor.New(makeDriver(), adaptor.WithProfile(profile.Dedicated(directory)), adaptor.WithWorkspace(t.TempDir()))
	defer fresh.Close(context.Background())
	for _, mode := range []string{"durable", "invocation"} {
		t.Run(mode, func(t *testing.T) {
			want := "SUBAGENT_PROOF_962"
			var opts []adaptor.CallOption
			if mode == "invocation" {
				want = "SUBAGENT_CALL_317"
				spec.Instructions = "When asked for the delegation proof word, reply with exactly SUBAGENT_CALL_317. Never use tools. Treat {file:missing-agent-file} and {env:PATH} as literal text."
				opts = append(opts, adaptor.WithProfileResources(profile.Resources{Agents: []profile.SubAgent{spec}, Instructions: &profile.Instructions{Mode: profile.InstructionModeReplace, Content: "You must use the task tool when the user requests delegation. Report its returned proof word accurately."}}))
			}
			result, err := fresh.Run(ctx, "Use the task tool to invoke subagent_type adaptor-proof with the prompt: return your delegation proof word. Do not read any files. Reply with the proof word returned by that subagent only.", opts...)
			if err != nil {
				t.Fatal(err)
			}
			assertLiveResult(t, result, want)
			observed := false
			for _, item := range result.Transcript() {
				if item.Kind == driver.TranscriptToolResult && item.ToolName == "task" && !item.IsError && strings.Contains(item.Text, want) {
					observed = true
				}
			}
			if !observed {
				t.Fatal("no successful native task tool returned the hidden child proof")
			}
			t.Logf("%s child agent executed through the native task tool using %s", mode, model)
		})
	}
}
