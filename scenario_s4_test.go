package adaptor_test

// Scenario S4 · background batch worker: per-job model / timeout / audit-tag
// overrides (docs/api-v1-redesign.md §3 S4). This is the core acceptance
// test for the dual-scope option system.
//
// Target shape, verbatim from the design doc:
//
//	agent := adaptor.New(codex.Driver(codex.Config{Model: "gpt-5.4"}),
//	    adaptor.WithPolicy(adaptor.Policy{Sandbox: adaptor.WorkspaceWrite, Approvals: adaptor.ApprovalsAutoDeny}),
//	)
//
//	for job := range jobs {
//	    res, err := agent.Run(ctx, job.Prompt,
//	        adaptor.WithWorkspace(job.RepoDir),
//	        adaptor.WithModel(job.Model),              // 便宜任务用小模型
//	        adaptor.WithTimeout(10*time.Minute),
//	        adaptor.WithMetadata("job", job.ID),
//	    )
//	    record(job, res, err)                          // err 一元判定
//	}
//
// Two P0 adaptations: Policy.Approvals (ApprovalsAutoDeny) joins in P1.3,
// and the fleet-default model is expressed with dual-scope WithModel until
// codex.Config moves home in P3.1.

import (
	"context"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
)

func TestScenarioS4BatchWorkerDualScope(t *testing.T) {
	ctx := context.Background()
	fake := newFakeDriver()

	// Construction scope: fleet defaults.
	agent := adaptor.New(fake,
		adaptor.WithModel("gpt-5.4"),
		adaptor.WithPolicy(adaptor.Policy{Sandbox: adaptor.WorkspaceWrite}),
		adaptor.WithWorkspace(`C:\srv\fleet\default`),
		adaptor.WithMetadata("fleet", "batch"),
	)

	jobs := []struct{ ID, Prompt, RepoDir, Model string }{
		{ID: "job-1", Prompt: "cheap task", RepoDir: `C:\srv\fleet\repo-1`, Model: "gpt-5.4-mini"},
		{ID: "", Prompt: "default task"}, // no overrides: rides the fleet defaults
	}

	// --- job-1: call-site overrides, same With* vocabulary ---
	if _, err := agent.Run(ctx, jobs[0].Prompt,
		adaptor.WithWorkspace(jobs[0].RepoDir),
		adaptor.WithModel(jobs[0].Model), // 便宜任务用小模型
		adaptor.WithTimeout(10*time.Minute),
		adaptor.WithMetadata("job", jobs[0].ID),
	); err != nil {
		t.Fatalf("Run(job-1): %v", err)
	}

	req1 := fake.request(t, 0)

	// Assertion group 1: the call site overrides the construction site.
	if req1.ModelOverride != "gpt-5.4-mini" {
		t.Errorf("job-1 model = %q, want call-site override gpt-5.4-mini", req1.ModelOverride)
	}
	if req1.Workspace.CWD != `C:\srv\fleet\repo-1` {
		t.Errorf("job-1 workspace = %q, want call-site override", req1.Workspace.CWD)
	}
	// Metadata merges per key: the job tag joins, the fleet tag survives.
	if req1.Metadata["job"] != "job-1" || req1.Metadata["fleet"] != "batch" {
		t.Errorf("job-1 metadata = %v, want fleet=batch plus job=job-1", req1.Metadata)
	}

	// --- job-2: no overrides ---
	if _, err := agent.Run(ctx, jobs[1].Prompt); err != nil {
		t.Fatalf("Run(job-2): %v", err)
	}

	req2 := fake.request(t, 1)

	// Assertion group 2: construction defaults take effect when the call
	// site is silent.
	if req2.ModelOverride != "gpt-5.4" {
		t.Errorf("job-2 model = %q, want fleet default gpt-5.4", req2.ModelOverride)
	}
	if req2.Workspace.CWD != `C:\srv\fleet\default` {
		t.Errorf("job-2 workspace = %q, want fleet default", req2.Workspace.CWD)
	}
	if req2.Policy.Isolation != adaptor.WorkspaceWrite {
		t.Errorf("job-2 isolation = %q, want fleet default workspace_write", req2.Policy.Isolation)
	}
	if req2.Metadata["fleet"] != "batch" {
		t.Errorf("job-2 metadata = %v, want fleet default intact", req2.Metadata)
	}

	// Assertion group 3: no cross-run pollution — job-1's overrides must
	// not leak into job-2.
	if got, ok := req2.Metadata["job"]; ok {
		t.Errorf("job-1 metadata leaked into job-2: job=%q", got)
	}
	if req2.ModelOverride == "gpt-5.4-mini" {
		t.Error("job-1 model override leaked into job-2")
	}
	if req2.Workspace.CWD == req1.Workspace.CWD {
		t.Error("job-1 workspace override leaked into job-2")
	}

	if fake.runCount() != 2 {
		t.Fatalf("driver saw %d runs, want 2", fake.runCount())
	}
}
