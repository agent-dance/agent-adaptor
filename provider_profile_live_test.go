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
	"github.com/agent-dance/agent-adaptor/pi"
	"github.com/agent-dance/agent-adaptor/profile"
	"github.com/agent-dance/agent-adaptor/skill"
)

func TestLiveProviderProfileSkills(t *testing.T) {
	if os.Getenv("AGENT_ADAPTOR_LIVE_CONFORMANCE") != "1" {
		t.Skip("live conformance gate is disabled")
	}
	for _, name := range []string{"pi", "opencode"} {
		t.Run(name, func(t *testing.T) {
			prefix := "AGENT_ADAPTOR_" + strings.ToUpper(name) + "_"
			if os.Getenv(prefix+"LIVE") != "1" {
				t.Skip("provider live gate is disabled")
			}
			model := os.Getenv(prefix + "MODEL")
			if model == "" {
				t.Fatal("an explicit live model is required")
			}
			makeDriver := func() driver.Driver {
				if name == "pi" {
					return pi.Driver(pi.Config{Command: os.Getenv(prefix + "COMMAND"), Provider: os.Getenv(prefix + "PROVIDER"), Model: model, NoExtensions: true, Tools: []string{"read"}})
				}
				return opencode.Driver(opencode.Config{Command: os.Getenv(prefix + "COMMAND"), Model: model, Pure: true})
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			d := makeDriver()
			source, err := d.(driver.ProfileReporter).GetProfile(ctx, nil, driver.AgentIdentity{}, nil)
			if err != nil {
				t.Fatal(err)
			}
			destination := filepath.Join(t.TempDir(), "cloned-profile")
			definition := skill.Inline("sdk-durable-proof", "---\nname: sdk-durable-proof\ndescription: Load this skill to find the durable profile test word.\n---\nThe durable profile test word is PERSISTED_SKILL_948. Respond with exactly that word.\n")
			a := adaptor.New(d, adaptor.WithProfile(profile.CloneFrom(source.Dir, destination, profile.CopySettings())), adaptor.WithSkills(definition))
			if _, err := a.SyncProfile(ctx); err != nil {
				t.Fatal(err)
			}
			if err := a.Close(ctx); err != nil {
				t.Fatal(err)
			}
			// A new Agent has no WithSkills and no per-run skill paths. Only the
			// durable native installation can deliver the unknown proof word.
			fresh := adaptor.New(makeDriver(), adaptor.WithProfile(profile.Dedicated(destination)), adaptor.WithWorkspace(t.TempDir()))
			defer fresh.Close(context.Background())
			result, err := fresh.Run(ctx, "Use the sdk-durable-proof skill to find the durable profile test word. Reply with that word only.")
			if err != nil {
				t.Fatal(err)
			}
			assertLiveResult(t, result, "PERSISTED_SKILL_948")
			observedTool := false
			for _, item := range result.Transcript() {
				if item.Kind == driver.TranscriptToolResult {
					observedTool = true
				}
			}
			if !observedTool {
				t.Fatal("no observed native skill/read tool execution")
			}
			t.Logf("%s durable skill loaded by fresh Agent using %s", name, model)
		})
	}
}
