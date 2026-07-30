package adaptor_test

// Scenario S5 · issue triager: structured output decoded straight into a
// business struct with one generic RunAs call.

import (
	"context"
	"strings"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor"
)

type Triage struct {
	Severity  string  `json:"severity"`
	Component string  `json:"component"`
	Duplicate *string `json:"duplicate_of"`
}

func TestScenarioS5IssueTriage(t *testing.T) {
	ctx := context.Background()
	fake := &structuredFake{
		caps:   fullStructuredCaps(),
		output: `{"severity":"high","component":"auth","duplicate_of":"ISSUE-17"}`,
	}
	agent := adaptor.New(fake)
	issueBody := "Login fails with 500 after the latest deploy."

	// The generic call derives the schema, runs the Agent, and decodes the result.
	triage, _, err := adaptor.RunAs[Triage](ctx, agent, "triage this issue:\n"+issueBody)
	if err != nil {
		t.Fatalf("RunAs: %v", err)
	}
	if triage.Severity != "high" || triage.Component != "auth" {
		t.Errorf("triage = %+v, want severity high / component auth", triage)
	}
	if triage.Duplicate == nil || *triage.Duplicate != "ISSUE-17" {
		t.Errorf("duplicate = %v, want ISSUE-17", triage.Duplicate)
	}

	req := fake.lastRequest(t)
	if req.OutputSchema == nil || !strings.Contains(string(req.OutputSchema.SchemaJSON), `"duplicate_of"`) {
		t.Errorf("output schema = %#v, want the Triage-derived JSON Schema on the wire", req.OutputSchema)
	}
	if !strings.HasSuffix(req.Prompt, issueBody) {
		t.Errorf("prompt = %q, want it to end with the issue body", req.Prompt)
	}
}
