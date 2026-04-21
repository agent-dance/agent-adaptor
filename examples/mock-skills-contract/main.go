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
	catalog := mockkit.StaticSkillCatalog{
		Entries: map[string]agentadaptor.Skill{
			"write-proof": {
				Key:      "write-proof",
				Runtime:  "write-proof",
				PathHint: "/virtual/skills/write-proof",
			},
		},
	}
	assembler := mockkit.StaticSkillAssembler{
		Mode:          agentadaptor.SkillSyncPersistent,
		RuntimePrefix: "runtime/",
	}

	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(agentadaptor.BindTyped(driver, mockkit.Config{Label: "skills-contract"},
			agentadaptor.WithDefaultIdentity(agentadaptor.AgentIdentity{
				ID:       "skills-agent",
				TenantID: "examples",
				Name:     "skills-contract",
			}),
			agentadaptor.WithDefaultSkills("write-proof", "default-unused"),
		)),
		agentadaptor.WithSkillCatalog(catalog),
		agentadaptor.WithSkillAssembler(assembler),
	)

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	defaultResult, err := sdk.Run(ctx, "Capture the default skills payload for this example.")
	exampleutil.Must(err, "run mock skills contract with default skills")
	exampleutil.Check(defaultResult.ExitCode == 0, "expected default skills exit code 0, got %d", defaultResult.ExitCode)
	defaultRequest := driver.LastRequest()
	exampleutil.Check(len(defaultRequest.Skills.Requested) == 2, "expected 2 default requested skills, got %d", len(defaultRequest.Skills.Requested))
	exampleutil.Check(defaultRequest.Skills.Requested[0] == "write-proof", "expected first default skill to be write-proof, got %q", defaultRequest.Skills.Requested[0])
	exampleutil.Check(defaultRequest.Skills.Mode == agentadaptor.SkillSyncPersistent, "expected persistent default skills mode, got %q", defaultRequest.Skills.Mode)
	exampleutil.Check(len(defaultRequest.Skills.Resolved) == 2, "expected 2 default resolved skills, got %d", len(defaultRequest.Skills.Resolved))
	exampleutil.Check(defaultRequest.Skills.Resolved[0].Runtime == "runtime/write-proof", "expected resolved runtime prefix for write-proof, got %#v", defaultRequest.Skills.Resolved[0])
	exampleutil.Check(defaultRequest.Skills.Fingerprint != "", "expected default skills fingerprint to be populated")

	overrideResult, err := sdk.Run(ctx, "Capture the overridden skills payload for this example.",
		agentadaptor.WithSkills("write-proof", "extra-check"),
	)
	exampleutil.Must(err, "run mock skills contract with overridden skills")
	exampleutil.Check(overrideResult.ExitCode == 0, "expected overridden skills exit code 0, got %d", overrideResult.ExitCode)
	overrideRequest := driver.LastRequest()
	exampleutil.Check(len(overrideRequest.Skills.Requested) == 2, "expected 2 overridden requested skills, got %d", len(overrideRequest.Skills.Requested))
	exampleutil.Check(overrideRequest.Skills.Requested[0] == "write-proof" && overrideRequest.Skills.Requested[1] == "extra-check", "expected overridden requested skills [write-proof extra-check], got %#v", overrideRequest.Skills.Requested)
	exampleutil.Check(len(overrideRequest.Skills.Resolved) == 2, "expected 2 overridden resolved skills, got %d", len(overrideRequest.Skills.Resolved))
	exampleutil.Check(overrideRequest.Skills.Resolved[0].Runtime == "runtime/write-proof", "expected runtime prefix for write-proof, got %#v", overrideRequest.Skills.Resolved[0])
	exampleutil.Check(overrideRequest.Skills.Resolved[1].Runtime == "runtime/extra-check", "expected runtime prefix for extra-check, got %#v", overrideRequest.Skills.Resolved[1])
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
