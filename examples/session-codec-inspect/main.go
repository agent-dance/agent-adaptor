// session-codec-inspect looks underneath the v1 Thread abstraction at the
// driver-owned session codec.
//
// In v1 there are only two consumer-visible identity layers: the thread key the
// host chose, and the run ID the SDK assigned. The provider session id is an
// implementation detail the SDK persists and replays on its own — hosts never
// have to hold it. This example is the audit hatch for the rare case where you
// *do* need to look: a driver that implements driver.SessionCodecProvider
// exposes how it normalizes a session into stable parameters and how it derives
// the resume guard fingerprint.
//
// The v1 driver value returned by codex.Driver / claude.Driver / cursor.Driver /
// codebuddy.Driver promotes every optional capability interface of the
// underlying adapter, so a plain type assertion is all it takes.
//
// No CLI process is started: the codec is pure data mapping, so this example
// runs anywhere.
//
// Usage:
//
//	go run ./examples/session-codec-inspect -agent=codex
package main

import (
	"flag"
	"path/filepath"

	"github.com/agent-dance/agent-adaptor/claude"
	"github.com/agent-dance/agent-adaptor/codebuddy"
	"github.com/agent-dance/agent-adaptor/codex"
	"github.com/agent-dance/agent-adaptor/cursor"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/examples/internal/exampleutil"
)

// Session parameter keys the built-in drivers agree on. They are part of the
// driver SPI contract; spelled as literals here because the v1 driver package
// does not re-export the key constants yet.
const (
	paramCWD                = "cwd"
	paramWorkspaceID        = "workspace_id"
	paramProfileFingerprint = "profile_fingerprint"
)

func main() {
	agent := flag.String("agent", "", "Driver to inspect: "+exampleutil.SupportedAgents()+" (default codex, or AGENT_ADAPTOR_EXAMPLE_AGENT)")
	flag.Parse()

	name := exampleutil.ResolveLiveAgent(*agent)

	// A driver value is a plain value; no Agent, no process, no CLI needed to
	// interrogate its static capabilities.
	provider, ok := driverFor(name).(driver.SessionCodecProvider)
	exampleutil.Check(ok, "driver %q does not expose a session codec", name)
	codec := provider.SessionCodec()

	// driver.SessionState is what a driver hands back as Checkpoint.State after
	// a run — the same shape th.Checkpoint(ctx) surfaces to a host.
	state := &driver.SessionState{
		ResumeID: name + "-session-42",
		Data: map[string]string{
			paramCWD:                filepath.Join("workspace", "repo"),
			paramWorkspaceID:        "workspace-a",
			paramProfileFingerprint: "profile-a",
		},
	}

	params := codec.ToParams(state)
	roundTrip := codec.FromParams(params)

	exampleutil.Check(roundTrip != nil, "expected FromParams to reconstruct the session state")
	exampleutil.Check(roundTrip.ResumeID == state.ResumeID,
		"expected the round trip to preserve the resume id, got %q", roundTrip.ResumeID)

	// The guard fingerprint is what lets a driver reject a resume whose
	// workspace or effective profile drifted since the session was captured.
	guard := codec.GuardFingerprint(params)

	drifted := codec.ToParams(&driver.SessionState{
		ResumeID: state.ResumeID,
		Data: map[string]string{
			paramCWD:                filepath.Join("workspace", "other-repo"),
			paramWorkspaceID:        "workspace-a",
			paramProfileFingerprint: "profile-a",
		},
	})
	exampleutil.Check(codec.GuardFingerprint(drifted) != guard,
		"expected a different guard fingerprint after the workspace changed")

	exampleutil.PrintJSON(map[string]any{
		"example":    "session-codec-inspect",
		"agent":      name,
		"codec":      codec.Name(),
		"resume_id":  params.ResumeID,
		"display_id": params.DisplayID,
		"values":     params.Values,
		"guard": map[string]any{
			"fingerprint":      guard,
			"after_cwd_change": codec.GuardFingerprint(drifted),
		},
	})
}

func driverFor(agent string) driver.Driver {
	switch agent {
	case exampleutil.AgentClaude:
		return claude.Driver(claude.Config{})
	case exampleutil.AgentCursor:
		return cursor.Driver(cursor.Config{})
	case exampleutil.AgentCodebuddy:
		return codebuddy.Driver(codebuddy.Config{})
	default:
		return codex.Driver(codex.Config{})
	}
}
