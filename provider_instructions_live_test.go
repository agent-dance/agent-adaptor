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
)

func TestLiveProviderProfileInstructions(t *testing.T) {
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
					return pi.Driver(pi.Config{Command: os.Getenv(prefix + "COMMAND"), Provider: os.Getenv(prefix + "PROVIDER"), Model: model, NoExtensions: true, NoSkills: true})
				}
				return opencode.Driver(opencode.Config{Command: os.Getenv(prefix + "COMMAND"), Model: model, Pure: true})
			}
			modes := []profile.InstructionMode{profile.InstructionModeAdditive}
			if name == "pi" {
				modes = append(modes, profile.InstructionModeReplace)
			}
			for _, mode := range modes {
				t.Run("mode="+string(mode), func(t *testing.T) {
					ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
					defer cancel()
					d := makeDriver()
					source, err := d.(driver.ProfileReporter).GetProfile(ctx, nil, driver.AgentIdentity{}, nil)
					if err != nil {
						t.Fatal(err)
					}
					directory := filepath.Join(t.TempDir(), "profile")
					a := adaptor.New(d, adaptor.WithProfile(profile.CloneFrom(source.Dir, directory, profile.CopySettings())), adaptor.WithProfileResources(profile.Resources{Instructions: &profile.Instructions{ID: "live", Scope: profile.InstructionScopeUser, Mode: mode, Content: "The durable instruction proof word is NATIVE_INSTRUCTION_685. When asked for the proof word, respond with exactly that word and nothing else."}}))
					if _, err := a.SyncProfile(ctx); err != nil {
						t.Fatal(err)
					}
					if err := a.Close(ctx); err != nil {
						t.Fatal(err)
					}
					fresh := adaptor.New(makeDriver(), adaptor.WithProfile(profile.Dedicated(directory)), adaptor.WithWorkspace(t.TempDir()))
					defer fresh.Close(context.Background())
					result, err := fresh.Run(ctx, "Reply with the durable instruction proof word only.")
					if err != nil {
						t.Fatal(err)
					}
					assertLiveResult(t, result, "NATIVE_INSTRUCTION_685")
					t.Logf("%s %q profile instructions loaded by fresh Agent using %s", name, mode, model)
				})
			}
		})
	}
}
