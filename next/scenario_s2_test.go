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
// RunAs[T] lands in P3.5; until then the equivalent P0 spelling is
// reviewer.Run + res.Decode(&review). Agents are plain Go variables — the
// named registry, sdk.Agent("review") lookups, and their runtime errors are
// gone by construction.

import (
	"context"
	"strings"
	"testing"

	"github.com/agent-dance/agent-adaptor/driver"
	adaptor "github.com/agent-dance/agent-adaptor/next"
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

	res, err := reviewer.Run(ctx, "review this patch:\n"+patch.Text)
	if err != nil {
		t.Fatalf("reviewer.Run: %v", err)
	}
	var review Review
	if err := res.Decode(&review); err != nil { // P3.5: adaptor.RunAs[Review] collapses this to one line
		t.Fatalf("Decode: %v", err)
	}
	if review.Verdict != "approve" {
		t.Errorf("review.Verdict = %q, want approve", review.Verdict)
	}

	// The reviewer prompt was composed from the coder's Result.Text —
	// results flow between agents as plain values.
	if got := reviewerFake.lastRequest(t).Prompt; !strings.Contains(got, patch.Text) {
		t.Errorf("reviewer prompt %q does not embed the coder patch", got)
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
