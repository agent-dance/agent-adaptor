package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"github.com/agent-dance/agent-adaptor/v2/hosttools/team"
	"github.com/agent-dance/agent-adaptor/v2/tool"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

type taskFixture struct {
	team.TaskStore
	mu    sync.Mutex
	tasks []team.Task
}

func (f *taskFixture) Tasks(context.Context, string) ([]team.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]team.Task(nil), f.tasks...), nil
}
func validOutput(role, spec, hash string) memberOutput {
	out := memberOutput{Role: role, SpecHash: spec, CandidateHash: hash, Requirements: requirementIDs(), Findings: []finding{}, Summary: "Bounded evidence", Manifest: []artifact{}, CheckReceipts: []string{}, RequirementEvidence: []requirementEvidence{}}
	out.Verdict = map[string]string{"planner": "plan", "engineer": "implemented", "reviewer": "approve", "verifier": "pass"}[role]
	for _, id := range requirementIDs() {
		out.RequirementEvidence = append(out.RequirementEvidence, requirementEvidence{id, "assessed", "concrete fixture evidence for " + id})
	}
	return out
}
func resultOutput(out memberOutput) *adaptor.Result {
	data, _ := json.Marshal(out)
	return &adaptor.Result{RunID: out.Role + "-run", Text: string(data)}
}

func gateFixture(t *testing.T) (*workflow, *taskFixture) {
	t.Helper()
	store := &taskFixture{}
	work, output := t.TempDir(), t.TempDir()
	w := newWorkflow(store, work, output)
	root := filepath.Join(output, "candidate")
	if err := seedProject(root); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(output, "checks"), 0700); err != nil {
		t.Fatal(err)
	}
	manifest, err := projectManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	hash := manifestHash(manifest)
	now := time.Now().UTC()
	join := now.Add(-time.Second)
	w.phase = "review"
	w.current = &candidate{Round: 1, Hash: hash, Manifest: manifest, Directory: root, Joins: map[string]joinedTask{"reviewer": {"review", join}, "verifier": {"verify", join}}, ReviewerRead: &readReceipt{"read-1", "review", hash, join.Add(time.Millisecond)}}
	w.barrier = make(chan struct{})
	close(w.barrier)
	w.barrierReleased = true
	review, verify := validOutput("reviewer", w.spec, hash), validOutput("verifier", w.spec, hash)
	review.ReviewReceipt = "read-1"
	for index, name := range []string{"test", "vet"} {
		id := fmt.Sprintf("check-%d", index)
		log := []byte("actual host log")
		rel := filepath.ToSlash(filepath.Join("checks", id+".log"))
		if err := os.WriteFile(filepath.Join(output, filepath.FromSlash(rel)), log, 0600); err != nil {
			t.Fatal(err)
		}
		w.checks = append(w.checks, checkReceipt{ID: id, Task: "verify", Candidate: hash, Check: name, Argv: []string{"go", name}, ExitCode: 0, Started: join.Add(2 * time.Millisecond), Finished: join.Add(3 * time.Millisecond), Log: rel, LogSHA256: digest(log)})
		verify.CheckReceipts = append(verify.CheckReceipts, id)
	}
	store.tasks = []team.Task{{ID: "review", Member: "reviewer", State: team.Succeeded, UpdatedAt: now, Result: resultOutput(review)}, {ID: "verify", Member: "verifier", State: team.Succeeded, UpdatedAt: now, Result: resultOutput(verify)}}
	return w, store
}

func TestGateRejectsUntrustedEvidence(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*workflow, *taskFixture)
	}{
		{"old candidate", func(w *workflow, s *taskFixture) {
			out := validOutput("verifier", w.spec, "old")
			out.CheckReceipts = []string{"check-0", "check-1"}
			s.tasks[1].Result = resultOutput(out)
		}},
		{"wrong role", func(_ *workflow, s *taskFixture) { s.tasks[1].Member = "engineer" }},
		{"forged receipt", func(w *workflow, s *taskFixture) {
			out := validOutput("verifier", w.spec, w.current.Hash)
			out.CheckReceipts = []string{"invented", "check-1"}
			s.tasks[1].Result = resultOutput(out)
		}},
		{"hidden failure", func(w *workflow, _ *taskFixture) {
			receipt := w.checks[0]
			receipt.ID = "hidden"
			receipt.ExitCode = 1
			w.checks = append(w.checks, receipt)
		}},
		{"hash drift", func(w *workflow, _ *taskFixture) {
			if err := os.WriteFile(filepath.Join(w.current.Directory, "router.go"), []byte("package mentionroute\n"), 0600); err != nil {
				t.Fatal(err)
			}
		}},
		{"not concurrent", func(w *workflow, s *taskFixture) {
			s.tasks[0].UpdatedAt = w.current.Joins["verifier"].At.Add(-time.Second)
		}},
		{"no candidate read", func(w *workflow, _ *taskFixture) { w.current.ReviewerRead = nil }},
		{"forged log", func(w *workflow, _ *taskFixture) { w.checks[0].LogSHA256 = "fake" }},
		{"wrong order", func(w *workflow, _ *taskFixture) { w.phase = "plan" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			w, store := gateFixture(t)
			test.mutate(w, store)
			if _, err := w.gate(context.Background(), gateInput{"review", "verify"}); err == nil {
				t.Fatal("accepted invalid evidence")
			}
			if w.current.Accepted || len(w.violations) == 0 {
				t.Fatal("invalid gate did not record failure")
			}
		})
	}
}

func TestGateReworkAndIntegration(t *testing.T) {
	t.Run("integrate requires gate", func(t *testing.T) {
		w, _ := gateFixture(t)
		if _, err := w.integrate(context.Background(), integrateInput{w.current.Hash}); err == nil {
			t.Fatal("integration bypassed gate")
		}
	})
	t.Run("accepted frozen bytes", func(t *testing.T) {
		w, _ := gateFixture(t)
		if _, err := w.gate(context.Background(), gateInput{"review", "verify"}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.integrate(context.Background(), integrateInput{w.current.Hash}); err != nil {
			t.Fatal(err)
		}
		if err := verifyManifest(filepath.Join(w.output, "integrated"), w.current.Manifest); err != nil {
			t.Fatal(err)
		}
	})
	for _, round := range []int{1, 2, 3} {
		t.Run(fmt.Sprint("failed check round ", round), func(t *testing.T) {
			w, _ := gateFixture(t)
			w.round = round
			w.checks[0].ExitCode = 1
			view, err := w.gate(context.Background(), gateInput{"review", "verify"})
			if err != nil {
				t.Fatal(err)
			}
			expected := "implement"
			if round == 3 {
				expected = "failed"
			}
			if view.Stage != expected || w.current.Accepted {
				t.Fatalf("stage=%s accepted=%v", view.Stage, w.current.Accepted)
			}
		})
	}
}

func TestOutputSchemaRejectsMissingAndAdditionalFields(t *testing.T) {
	compiler := jsonschema.NewCompiler()
	schema := outputSchema("reviewer")
	schemaBytes, _ := json.Marshal(schema)
	var normalized any
	if err := json.Unmarshal(schemaBytes, &normalized); err != nil {
		t.Fatal(err)
	}
	if err := compiler.AddResource("https://example.invalid/output", normalized); err != nil {
		t.Fatal(err)
	}
	compiled, err := compiler.Compile("https://example.invalid/output")
	if err != nil {
		t.Fatal(err)
	}
	out := validOutput("reviewer", "spec", "candidate")
	out.ReviewReceipt = "read"
	data, _ := json.Marshal(out)
	var value map[string]any
	if err = json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	if err = compiled.Validate(value); err != nil {
		t.Fatal(err)
	}
	value["findings"] = []any{map[string]any{"requirement": "R1-R8", "severity": "error", "message": "checks unavailable"}}
	if err = compiled.Validate(value); err == nil {
		t.Fatal("requirement range passed native output schema")
	}
	value["findings"] = []any{map[string]any{"requirement": "HOST", "severity": "error", "message": "checks unavailable"}}
	if err = compiled.Validate(value); err != nil {
		t.Fatalf("host infrastructure finding rejected: %v", err)
	}
	out.Findings = []finding{{Requirement: "HOST", Severity: "error", Message: "checks unavailable"}}
	if err = validateOutput(out, "reviewer", "spec"); err != nil {
		t.Fatal(err)
	}
	value["invented_proof"] = true
	if err = compiled.Validate(value); err == nil {
		t.Fatal("unknown field passed strict schema")
	}
	delete(value, "invented_proof")
	delete(value, "requirement_evidence")
	if err = compiled.Validate(value); err == nil {
		t.Fatal("missing evidence passed schema")
	}
	out.RequirementEvidence = out.RequirementEvidence[:7]
	if err = validateOutput(out, "reviewer", "spec"); err == nil {
		t.Fatal("omitted requirement evidence passed host validation")
	}
}

func TestReviewRendezvousNeedsBothRolesAndCancels(t *testing.T) {
	w, s := gateFixture(t)
	w.current.Joins = map[string]joinedTask{}
	w.barrier = make(chan struct{})
	w.barrierReleased = false
	w.launched["reviewer"] = true
	w.launched["verifier"] = true
	for index := range s.tasks {
		s.tasks[index].State = team.Running
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	results := make(chan error, 2)
	go func() { _, err := w.begin(ctx, "reviewer"); results <- err }()
	go func() { _, err := w.begin(ctx, "verifier"); results <- err }()
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if !w.barrierReleased || len(w.current.Joins) != 2 {
		t.Fatal("missing actual role rendezvous")
	}
	w.current.Joins = map[string]joinedTask{}
	w.barrier = make(chan struct{})
	w.barrierReleased = false
	cancelled, stop := context.WithCancel(context.Background())
	stop()
	if _, err := w.begin(cancelled, "reviewer"); !errors.Is(err, context.Canceled) {
		t.Fatalf("barrier did not cancel: %v", err)
	}
}

type recordingRunner struct {
	ctxErr error
	calls  int
}

func (r *recordingRunner) Send(ctx context.Context, _ string, _ ...adaptor.CallOption) adaptor.Stream {
	r.ctxErr = ctx.Err()
	r.calls++
	return nil
}
func TestRoleLaunchRejectsWrongOrderThroughUnderlyingRunner(t *testing.T) {
	w := newWorkflow(&taskFixture{}, t.TempDir(), t.TempDir())
	underlying := &recordingRunner{}
	runner := &roleRunner{agent: underlying, role: "engineer", host: w}
	_ = runner.Send(context.Background(), "work")
	if underlying.calls != 1 || !errors.Is(underlying.ctxErr, context.Canceled) || len(w.violations) != 1 {
		t.Fatal("invalid launch did not forward a cancelled invocation")
	}
	w.phase = "implement"
	_ = runner.Send(context.Background(), "work")
	if underlying.ctxErr != nil || underlying.calls != 2 {
		t.Fatal("valid launch did not forward live context")
	}
	_ = runner.Send(context.Background(), "duplicate")
	if !errors.Is(underlying.ctxErr, context.Canceled) {
		t.Fatal("duplicate launch was admitted")
	}
}

func TestFailedLeaderPartialIsRetainedForEvidence(t *testing.T) {
	partial := &adaptor.Result{RunID: "failed-leader", Text: "partial"}
	err := fmt.Errorf("wrapped: %w", &adaptor.RunError{Result: partial})
	if evidenceResult(nil, err) != partial {
		t.Fatal("failed leader partial audit result lost")
	}
	if evidenceResult(nil, context.Canceled) != nil {
		t.Fatal("invented a result for cancellation")
	}
}

func TestHostToolsValidateTheirPublicSchemas(t *testing.T) {
	w, s := gateFixture(t)
	for _, definitions := range [][]tool.Definition{w.leaderTools(), w.memberTools("planner"), w.memberTools("engineer"), w.memberTools("reviewer"), w.memberTools("verifier")} {
		for _, definition := range definitions {
			descriptor, err := definition.Descriptor()
			if err != nil {
				t.Fatal(err)
			}
			if descriptor.Revision != workflowRevision {
				t.Fatalf("host tool %s has no stable Thread semantic revision", descriptor.Name)
			}
		}
	}
	inspect := w.leaderTools()[0]
	if _, err := inspect.Invoke(context.Background(), json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	s.tasks[0].State = team.Running
	read := w.memberTools("reviewer")[1]
	args, _ := json.Marshal(integrateInput{w.current.Hash})
	response, err := read.Invoke(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	var observed candidateRead
	if err = json.Unmarshal(response, &observed); err != nil {
		t.Fatal(err)
	}
	if observed.Receipt == "" || len(observed.Files) != len(projectFiles) || w.current.ReviewerRead.Task != "review" {
		t.Fatal("read did not bind complete candidate content to actual reviewer task")
	}
}

func TestHostVetWithoutExplicitBuildCache(t *testing.T) {
	// Windows Go locates its default cache through LOCALAPPDATA. The model
	// check's restricted environment must retain that non-secret build setting.
	t.Setenv("GOCACHE", "")
	root := t.TempDir()
	if err := seedProject(root); err != nil {
		t.Fatal(err)
	}
	output := t.TempDir()
	if err := os.Mkdir(filepath.Join(output, "checks"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	receipt, err := runCheck(ctx, root, output, "verifier", "candidate", "vet", 1)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.ExitCode != 0 {
		log, _ := os.ReadFile(filepath.Join(output, filepath.FromSlash(receipt.Log)))
		t.Fatalf("host Go environment is unusable: exit=%d log=%s", receipt.ExitCode, log)
	}
}

func TestCheckCancellationKillsPipeHoldingChildren(t *testing.T) {
	if mode := os.Getenv("SDLC_CHECK_CHILD"); mode != "" {
		if mode == "parent" {
			cmd := exec.Command(os.Args[0], "-test.run=^TestCheckCancellationKillsPipeHoldingChildren$")
			cmd.Env = append(os.Environ(), "SDLC_CHECK_CHILD=grandchild")
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			if err := cmd.Start(); err != nil {
				os.Exit(2)
			}
		} else {
			fmt.Println("grandchild-ready")
		}
		for {
			time.Sleep(time.Hour)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCheckCancellationKillsPipeHoldingChildren$")
	command.Env = append(os.Environ(), "SDLC_CHECK_CHILD=parent")
	configureCommand(command)
	pipe, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(pipe)
	if !scanner.Scan() || !strings.Contains(scanner.Text(), "grandchild-ready") {
		cancel()
		_ = command.Wait()
		t.Fatal("child did not start")
	}
	cancel()
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled host check retained a pipe-holding child")
	}
}
