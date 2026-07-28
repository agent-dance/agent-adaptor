package adaptor_test

// Scenario S1 · CLI tool: one-shot task (docs/api-v1-redesign.md §3 S1).
//
// Target shape, verbatim from the design doc:
//
//	agent := adaptor.New(codex.Driver(codex.Config{Model: "gpt-5.4"}))
//
//	res, err := agent.Run(ctx, "fix the failing tests")
//	if err != nil { ... }                    // 唯一判断点
//	fmt.Println(res.Text)
//
// The hermetic fake Driver keeps this acceptance test free of paid CLI calls.

import (
	"context"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
)

func TestScenarioS1OneShotTask(t *testing.T) {
	fake := newFakeDriver()
	fake.response = driver.Response{
		Output:  "All failing tests fixed.",
		Summary: "fixed 3 tests",
		Model:   "gpt-5.4",
		Usage:   &driver.Usage{InputTokens: 120, OutputTokens: 48},
	}

	agent := adaptor.New(fake)

	res, err := agent.Run(context.Background(), "fix the failing tests")
	if err != nil { // the single verdict point — no second-layer Failure check exists
		t.Fatalf("Run: %v", err)
	}
	if res.Text != "All failing tests fixed." {
		t.Errorf("res.Text = %q, want the driver output verbatim", res.Text)
	}

	// High-frequency fields arrive flat on the Result.
	if res.Summary != "fixed 3 tests" {
		t.Errorf("res.Summary = %q", res.Summary)
	}
	if res.Model != "gpt-5.4" {
		t.Errorf("res.Model = %q", res.Model)
	}
	if res.Usage == nil || res.Usage.OutputTokens != 48 {
		t.Errorf("res.Usage = %+v, want OutputTokens=48", res.Usage)
	}
	if res.RunID == "" {
		t.Error("res.RunID must be assigned by the SDK")
	}

	// The prompt reached the driver untouched, with no accidental defaults.
	req := fake.lastRequest(t)
	if req.Prompt != "fix the failing tests" {
		t.Errorf("req.Prompt = %q", req.Prompt)
	}
	if req.ModelOverride != "" {
		t.Errorf("req.ModelOverride = %q, want empty (no WithModel given)", req.ModelOverride)
	}
	if req.Policy.Isolation != adaptor.SandboxInherit {
		t.Errorf("req.Policy.Isolation = %q, want inherit (no WithPolicy given)", req.Policy.Isolation)
	}
}
