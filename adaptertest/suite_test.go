package adaptertest

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
)

// TestReferenceDriverConformance is the suite's self-proof: the shipped
// reference driver must pass every clause with every opt-in probe enabled,
// including the "live" probes (which are in-memory for this driver, so
// they run in CI without any CLI).
func TestReferenceDriverConformance(t *testing.T) {
	cwd := t.TempDir()
	cfg := ReferenceConfig{Model: "reference-1", CWD: cwd}
	TestDriver(t, func() driver.Driver { return NewReferenceDriver(cfg) },
		WithConfig(cfg),
		WithSessionState(&driver.SessionState{
			ResumeID: "ref-session",
			Data:     map[string]string{"cwd": cwd, "workspace_id": "ws-1"},
		}),
		WithSessionKeys("cwd", "workspace_id"),
		WithGuardKeys("cwd", "workspace_id"),
		WithWorkspace(cwd),
		WithExpectedDetectedModel("reference-1"),
		WithRequiredConfigFields("model", "cwd"),
		ExpectRejectForeignConfig(),
		WithSyncSkillsProbe(),
		WithLiveRun(""),
		WithLiveStructuredOutput(),
		WithLiveRunTimeout(time.Minute),
	)
}

// TestReferenceDriverFailurePath proves the failure shape the reference
// driver models (run.error terminal frame + FailureAgentError) satisfies
// the same verifiers, so implementers can copy it for their error paths.
func TestReferenceDriverFailurePath(t *testing.T) {
	d := NewReferenceDriver(ReferenceConfig{FailRun: true})
	sink := NewRecordingSink()
	resp, err := d.Run(context.Background(), driver.Request{RunID: "fail-1", Prompt: "x", Streaming: true}, sink)
	if err != nil {
		t.Fatalf("Run returned an infrastructure error: %v (provider-level failures belong in Response.Failure)", err)
	}
	if resp.Failure == nil || resp.Failure.Code != driver.FailureAgentError {
		t.Fatalf("Failure = %+v, want Code=%q", resp.Failure, driver.FailureAgentError)
	}
	if vs := VerifyStreamSequence(sink.Stream()); len(vs) != 0 {
		t.Errorf("failure-path stream violates the timing contract: %v", vs)
	}
	if vs := VerifyRunEvents(sink.Events()); len(vs) != 0 {
		t.Errorf("failure-path run events violate the contract: %v", vs)
	}
	if vs := VerifyTranscriptMirror(sink.Events(), resp.Transcript); len(vs) != 0 {
		t.Errorf("failure-path transcript mirror violates the contract: %v", vs)
	}
	if vs := VerifyOutcome(&resp, err); len(vs) != 0 {
		t.Errorf("failure-path response violates the contract: %v", vs)
	}
	stream := sink.Stream()
	last := stream[len(stream)-1]
	if last.Kind != driver.StreamRunError || last.Error == nil {
		t.Errorf("terminal frame = %+v, want run.error carrying Error", last)
	}
}

// TestReferenceDriverSkillEchoLaws exercises the non-empty catalogue half
// of the SkillSupport invariants (the TestDriver probe uses the empty
// catalogue): Selected mirrors selected, Resolved keeps every entry.
func TestReferenceDriverSkillEchoLaws(t *testing.T) {
	d := NewReferenceDriver(ReferenceConfig{}).(driver.SkillSupport)
	payload := driver.ResolvedSkills{
		Mode: driver.SkillSyncEphemeral,
		Entries: []driver.ResolvedSkill{
			{Key: "alpha", RuntimeName: "alpha", SourcePath: "/skills/alpha"},
			{Key: "beta", RuntimeName: "beta", Required: true},
		},
		Fingerprint: "fp-1",
	}
	selected := []string{"alpha", "beta"}
	resolved := []driver.Skill{{Key: "alpha"}, {Key: "beta"}, {Key: "gamma-unselected"}}

	snapshot, err := d.ListSkills(context.Background(), nil, payload, selected, resolved, nil)
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	if got, want := len(snapshot.Selected), len(selected); got != want {
		t.Errorf("Selected has %d entries, want %d", got, want)
	}
	if len(snapshot.Resolved) != len(resolved) {
		t.Errorf("Resolved has %d entries, want %d (SkillSupport docs: MUST NOT silently drop entries)", len(snapshot.Resolved), len(resolved))
	}
	if snapshot.Fingerprint != payload.Fingerprint {
		t.Errorf("Fingerprint = %q, want %q", snapshot.Fingerprint, payload.Fingerprint)
	}
	if len(snapshot.Entries) != len(payload.Entries) {
		t.Errorf("Entries has %d rows, want %d", len(snapshot.Entries), len(payload.Entries))
	}
}

// TestReferenceDriverModelOverride pins the Request.ModelOverride
// precedence rule the reference implementation models.
func TestReferenceDriverModelOverride(t *testing.T) {
	d := NewReferenceDriver(ReferenceConfig{Model: "reference-1"})
	resp, err := d.Run(context.Background(), driver.Request{Prompt: "hi", ModelOverride: "reference-override"}, NewRecordingSink())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if resp.Model != "reference-override" {
		t.Errorf("Model = %q, want the per-run override (Request.ModelOverride docs)", resp.Model)
	}
}

// TestReferenceDriverStructuredPromptSource pins the prompt-validation half:
// the driver returns exact JSON text and leaves StructuredOutput nil
// (validation happens above the SPI).
func TestReferenceDriverStructuredPromptSource(t *testing.T) {
	d := NewReferenceDriver(ReferenceConfig{})
	resp, err := d.Run(context.Background(), driver.Request{
		Prompt:                 "structured",
		StructuredOutputSource: driver.StructuredOutputSourcePromptValidate,
		OutputSchema: &driver.OutputSchema{
			Format:     driver.OutputFormatJSONSchema,
			SchemaJSON: json.RawMessage(`{"type":"object"}`),
		},
	}, NewRecordingSink())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if resp.StructuredOutput != nil {
		t.Errorf("StructuredOutput = %+v, want nil for prompt validation at the SPI level", resp.StructuredOutput)
	}
	if !json.Valid([]byte(resp.Output)) {
		t.Errorf("Output %q is not exact JSON text", resp.Output)
	}
}
