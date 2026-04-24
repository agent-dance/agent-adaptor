package main

import (
	"context"
	"encoding/json"
	"flag"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/examples/internal/exampleutil"
	"github.com/agent-dance/agent-adaptor/examples/internal/mockkit"
)

func main() {
	timeout := flag.Duration("timeout", 30*time.Second, "Maximum time to wait for the mock skills contract run")
	flag.Parse()

	driver := mockkit.NewRecordingDriver("Mock Skills Contract")
	skillSet := agentadaptor.SkillSet{
		"write-proof":    mockkit.InlineSkill("write-proof", "# write-proof"),
		"default-unused": mockkit.InlineSkill("default-unused", "# default-unused"),
		"extra-check":    mockkit.InlineSkill("extra-check", "# extra-check"),
	}

	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(agentadaptor.BindTyped(driver, mockkit.Config{Label: "skills-contract"},
			agentadaptor.WithDefaultIdentity(agentadaptor.AgentIdentity{
				ID:       "skills-agent",
				TenantID: "examples",
				Name:     "skills-contract",
			}),
			agentadaptor.WithDefaultSkills(agentadaptor.Key("write-proof"), agentadaptor.Key("default-unused")),
		)),
		agentadaptor.WithSkillSet(skillSet),
	)

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	defaultResult, err := sdk.Run(ctx, "Capture the default skills payload for this example.")
	exampleutil.Must(err, "run mock skills contract with default skills")
	exampleutil.Check(defaultResult.ExitCode == 0, "expected default skills exit code 0, got %d", defaultResult.ExitCode)
	defaultRequest := driver.LastRequest()
	exampleutil.Check(len(defaultRequest.Skills.Entries) == 2, "expected 2 default entries, got %d", len(defaultRequest.Skills.Entries))
	exampleutil.Check(defaultRequest.Skills.Entries[0].Key == "default-unused", "expected first default entry default-unused, got %q", defaultRequest.Skills.Entries[0].Key)
	exampleutil.Check(defaultRequest.Skills.Entries[1].Key == "write-proof", "expected second default entry write-proof, got %q", defaultRequest.Skills.Entries[1].Key)
	exampleutil.Check(defaultRequest.Skills.Fingerprint != "", "expected default skills fingerprint to be populated")

	// Additive override: WithSkills merges into defaults; "write-proof"
	// already appears in the binding defaults so the final selection adds
	// only "extra-check".
	overrideResult, err := sdk.Run(ctx, "Capture the overridden skills payload for this example.",
		agentadaptor.WithSkills(agentadaptor.Key("write-proof"), agentadaptor.Key("extra-check")),
	)
	exampleutil.Must(err, "run mock skills contract with overridden skills")
	exampleutil.Check(overrideResult.ExitCode == 0, "expected overridden skills exit code 0, got %d", overrideResult.ExitCode)
	overrideRequest := driver.LastRequest()
	exampleutil.Check(len(overrideRequest.Skills.Entries) == 3, "expected 3 overridden entries, got %d", len(overrideRequest.Skills.Entries))
	exampleutil.Check(overrideRequest.Skills.Entries[0].Key == "default-unused", "expected first overridden entry default-unused, got %q", overrideRequest.Skills.Entries[0].Key)
	exampleutil.Check(overrideRequest.Skills.Entries[1].Key == "extra-check", "expected second overridden entry extra-check, got %q", overrideRequest.Skills.Entries[1].Key)
	exampleutil.Check(overrideRequest.Skills.Entries[2].Key == "write-proof", "expected third overridden entry write-proof, got %q", overrideRequest.Skills.Entries[2].Key)
	exampleutil.Check(overrideRequest.Skills.Fingerprint != "", "expected override skills fingerprint to be populated")

	exampleutil.Check(defaultResult.RawStreams != nil, "expected default RawStreams to be populated")
	exampleutil.Check(overrideResult.RawStreams != nil, "expected override RawStreams to be populated")
	var defaultOutput agentadaptor.DriverRunRequest
	err = json.Unmarshal([]byte(defaultResult.RawStreams.Stdout), &defaultOutput)
	exampleutil.Must(err, "decode default skills raw stdout")
	var overrideOutput agentadaptor.DriverRunRequest
	err = json.Unmarshal([]byte(overrideResult.RawStreams.Stdout), &overrideOutput)
	exampleutil.Must(err, "decode override skills raw stdout")

	exampleutil.PrintJSON(map[string]any{
		"example": "mock-skills-contract",
		"default": defaultOutput.Skills,
		"override": map[string]any{
			"skills": overrideOutput.Skills,
		},
	})
}
