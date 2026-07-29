package adaptor_test

// Scenario S2 · multi-agent pipeline: Codex implements, Claude reviews
// (docs/api-v1-redesign.md §3 S2).
//
// Target shape, verbatim from the design doc:
//
//	coder    := adaptor.New(codex.Driver(codex.Config{Model: "gpt-5.4"}))
//	reviewer := adaptor.New(claude.Driver(claude.Config{Model: "claude-sonnet-4"}),
//	    adaptor.WithPolicy(adaptor.Policy{Sandbox: adaptor.ReadOnly}),   // 评审者天然只读
//	)
//
//	patch, err := coder.Run(ctx, "implement the fix")
//	if err != nil { return err }
//
//	review, _, err := adaptor.RunAs[Review](ctx, reviewer,
//	    "review this patch:\n"+patch.Text)
//	if err != nil { return err }
//	if review.Verdict != "approve" { ... }
//
// Agents are plain Go variables, so results flow between them without a
// registry or name lookup.

import (
	"context"
	"strings"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
)

type Review struct {
	Verdict string   `json:"verdict"`
	Issues  []string `json:"issues"`
}

func TestScenarioS2MultiAgentPipeline(t *testing.T) {
	ctx := context.Background()

	coderFake := newFakeDriver()
	coderFake.response = driver.Response{
		Output:  "diff --git a/slug.go b/slug.go\n+fixed",
		Summary: "patch produced",
	}
	reviewerFake := newFakeDriver()
	reviewerDescriptor := reviewerFake.Descriptor()
	reviewerDescriptor.StructuredOutput = driver.StructuredOutputCapability{
		JSONSchemaPromptValidate: true,
		WorksWithRun:             true,
	}
	reviewerFake.descriptor = &reviewerDescriptor
	reviewerFake.response = driver.Response{
		Output:  `{"verdict":"approve","issues":[]}`,
		Summary: "review complete",
	}

	coder := adaptor.New(coderFake)
	reviewer := adaptor.New(reviewerFake,
		adaptor.WithPolicy(adaptor.Policy{Sandbox: adaptor.ReadOnly}), // 评审者天然只读
	)

	patch, err := coder.Run(ctx, "implement the fix")
	if err != nil {
		t.Fatalf("coder.Run: %v", err)
	}

	review, res, err := adaptor.RunAs[Review](ctx, reviewer,
		"review this patch:\n"+patch.Text,
	)
	if err != nil {
		t.Fatalf("RunAs: %v", err)
	}
	if review.Verdict != "approve" {
		t.Errorf("review.Verdict = %q, want approve", review.Verdict)
	}
	if res == nil || res.Summary != "review complete" {
		t.Errorf("result = %+v, want review summary", res)
	}

	// The reviewer prompt was composed from the coder's Result.Text —
	// results flow between agents as plain values.
	if got := reviewerFake.lastRequest(t).Prompt; !strings.Contains(got, patch.Text) {
		t.Errorf("reviewer prompt %q does not embed the coder patch", got)
	}
	if got := reviewerFake.lastRequest(t).StructuredOutputSource; got != driver.StructuredOutputSourcePromptValidate {
		t.Errorf("structured output source = %q, want automatic prompt-validation fallback", got)
	}

	// Construction-scope default took effect: the reviewer's read-only
	// policy reached its driver...
	if got := reviewerFake.lastRequest(t).Policy.Isolation; got != adaptor.ReadOnly {
		t.Errorf("reviewer isolation = %q, want %q", got, adaptor.ReadOnly)
	}
	// ...and did not bleed into the coder, which set no policy at all.
	if got := coderFake.lastRequest(t).Policy.Isolation; got != adaptor.SandboxInherit {
		t.Errorf("coder isolation = %q, want inherit", got)
	}
}
