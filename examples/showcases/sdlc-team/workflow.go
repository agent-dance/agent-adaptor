package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"github.com/agent-dance/agent-adaptor/v2/hosttools/team"
	"github.com/agent-dance/agent-adaptor/v2/tool"
)

type finding struct {
	Requirement string `json:"requirement"`
	Severity    string `json:"severity"`
	Message     string `json:"message"`
}

type memberOutput struct {
	Role                string                `json:"role"`
	SpecHash            string                `json:"spec_hash"`
	CandidateHash       string                `json:"candidate_hash"`
	Requirements        []string              `json:"requirements"`
	Findings            []finding             `json:"findings"`
	Verdict             string                `json:"verdict"`
	Summary             string                `json:"summary"`
	Manifest            []artifact            `json:"manifest"`
	CheckReceipts       []string              `json:"check_receipts"`
	RequirementEvidence []requirementEvidence `json:"requirement_evidence"`
	ReviewReceipt       string                `json:"review_receipt"`
}

type requirementEvidence struct {
	ID         string `json:"id"`
	Assessment string `json:"assessment"`
	Evidence   string `json:"evidence"`
}

func objectSchema(properties map[string]any) map[string]any {
	keys := make([]string, 0, len(properties))
	for key := range properties {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return map[string]any{"type": "object", "properties": properties, "required": keys, "additionalProperties": false}
}

const workflowRevision = "sdlc-workflow-v1"

func stringSchema() map[string]any { return map[string]any{"type": "string", "maxLength": 128} }
func stringArray() map[string]any {
	return map[string]any{"type": "array", "maxItems": 8, "items": stringSchema()}
}
func outputSchema(role string) map[string]any {
	return objectSchema(map[string]any{
		"role": map[string]any{"type": "string", "enum": []string{role}}, "spec_hash": stringSchema(), "candidate_hash": stringSchema(),
		"requirements": stringArray(), "verdict": map[string]any{"type": "string", "enum": []string{"plan", "implemented", "approve", "reject", "pass", "fail"}},
		"summary": map[string]any{"type": "string", "maxLength": 600}, "check_receipts": map[string]any{"type": "array", "maxItems": 2, "items": stringSchema()},
		"review_receipt":       stringSchema(),
		"requirement_evidence": map[string]any{"type": "array", "maxItems": 8, "items": objectSchema(map[string]any{"id": stringSchema(), "assessment": map[string]any{"type": "string", "maxLength": 160}, "evidence": map[string]any{"type": "string", "maxLength": 600}})},
		"manifest":             map[string]any{"type": "array", "maxItems": 3, "items": objectSchema(map[string]any{"path": stringSchema(), "sha256": stringSchema()})},
		"findings":             map[string]any{"type": "array", "maxItems": 8, "items": objectSchema(map[string]any{"requirement": map[string]any{"type": "string", "enum": append(requirementIDs(), "HOST")}, "severity": map[string]any{"type": "string", "enum": []string{"info", "error"}}, "message": map[string]any{"type": "string", "maxLength": 400}})},
	})
}

func validateOutput(out memberOutput, role, spec string) error {
	if out.Role != role || out.SpecHash != spec {
		return errors.New("wrong role or specification identity")
	}
	ids := requirementIDs()
	actual := append([]string(nil), out.Requirements...)
	slices.Sort(actual)
	slices.Sort(ids)
	if !slices.Equal(ids, actual) {
		return errors.New("output must account for every requirement exactly once")
	}
	evidenceIDs := make([]string, 0, len(out.RequirementEvidence))
	for _, item := range out.RequirementEvidence {
		if strings.TrimSpace(item.Assessment) == "" || strings.TrimSpace(item.Evidence) == "" || len([]rune(item.Assessment)) > 160 || len([]rune(item.Evidence)) > 600 {
			return errors.New("each requirement needs bounded nonempty assessment and evidence")
		}
		evidenceIDs = append(evidenceIDs, item.ID)
	}
	slices.Sort(evidenceIDs)
	if !slices.Equal(ids, evidenceIDs) {
		return errors.New("per-requirement evidence must cover every requirement exactly once")
	}
	if role != "reviewer" && out.ReviewReceipt != "" {
		return errors.New("only reviewer can claim a candidate read receipt")
	}
	if strings.TrimSpace(out.Summary) == "" || len([]rune(out.Summary)) > 600 {
		return errors.New("missing or unbounded summary")
	}
	for _, item := range out.Findings {
		if (!slices.Contains(ids, item.Requirement) && item.Requirement != "HOST") || !slices.Contains([]string{"info", "error"}, item.Severity) || strings.TrimSpace(item.Message) == "" {
			return errors.New("invalid finding")
		}
	}
	verdicts := map[string][]string{"planner": {"plan"}, "engineer": {"implemented"}, "reviewer": {"approve", "reject"}, "verifier": {"pass", "fail"}}
	if !slices.Contains(verdicts[role], out.Verdict) {
		return errors.New("verdict does not belong to this role")
	}
	if role == "planner" && (out.CandidateHash != "" || len(out.Manifest) > 0 || len(out.CheckReceipts) > 0) {
		return errors.New("planner cannot claim implementation evidence")
	}
	if role != "engineer" && len(out.Manifest) > 0 {
		return errors.New("only engineer submits an artifact manifest")
	}
	if role != "verifier" && len(out.CheckReceipts) > 0 {
		return errors.New("only verifier submits check receipts")
	}
	return nil
}

type joinedTask struct {
	Task string    `json:"task"`
	At   time.Time `json:"at"`
}
type phaseEntry struct {
	At     time.Time `json:"at"`
	Stage  string    `json:"stage"`
	Task   string    `json:"task,omitempty"`
	Detail string    `json:"detail,omitempty"`
}
type candidate struct {
	Round        int                   `json:"round"`
	Hash         string                `json:"hash"`
	Manifest     []artifact            `json:"manifest"`
	Directory    string                `json:"directory"`
	EngineerTask string                `json:"engineer_task"`
	Joins        map[string]joinedTask `json:"joins"`
	ReviewTask   string                `json:"review_task,omitempty"`
	VerifyTask   string                `json:"verify_task,omitempty"`
	Accepted     bool                  `json:"accepted"`
	ReviewerRead *readReceipt          `json:"reviewer_read,omitempty"`
}

type readReceipt struct {
	ID        string    `json:"id"`
	Task      string    `json:"task"`
	Candidate string    `json:"candidate_hash"`
	At        time.Time `json:"at"`
}
type candidateFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}
type candidateRead struct {
	CandidateHash string          `json:"candidate_hash"`
	Receipt       string          `json:"read_receipt"`
	Files         []candidateFile `json:"files"`
}

func (w *workflow) readCandidate(ctx context.Context, in integrateInput) (candidateRead, error) {
	task, err := w.activeTask(ctx, "reviewer")
	if err != nil {
		return candidateRead{}, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.phase != "review" || w.current == nil || in.CandidateHash != w.current.Hash || !w.barrierReleased || w.current.Joins["reviewer"].Task != task.ID {
		return candidateRead{}, w.violation(errors.New("candidate read has stale hash, wrong role/stage, or missing rendezvous"))
	}
	if err = verifyManifest(w.current.Directory, w.current.Manifest); err != nil {
		return candidateRead{}, w.violation(err)
	}
	view := candidateRead{CandidateHash: in.CandidateHash, Receipt: fmt.Sprintf("read-round-%02d-%s", w.round, task.ID), Files: []candidateFile{}}
	for _, file := range w.current.Manifest {
		data, err := safeRead(w.current.Directory, file.Path)
		if err != nil {
			return candidateRead{}, w.violation(err)
		}
		view.Files = append(view.Files, candidateFile{file.Path, string(data)})
	}
	w.current.ReviewerRead = &readReceipt{view.Receipt, task.ID, in.CandidateHash, time.Now().UTC()}
	w.record("candidate_read", task.ID, view.Receipt)
	return view, nil
}

type workflow struct {
	mu                        sync.Mutex
	store                     team.TaskStore
	scope, work, output, spec string
	phase                     string
	round                     int
	launched                  map[string]bool
	began                     map[string]string
	current                   *candidate
	candidates                []*candidate
	barrier                   chan struct{}
	barrierReleased           bool
	checks                    []checkReceipt
	checkActive               bool
	violations                []string
	history                   []phaseEntry
}

func newWorkflow(store team.TaskStore, work, output string) *workflow {
	return &workflow{store: store, scope: "sdlc", work: work, output: output, spec: digest([]byte(fixtureSpec)), phase: "plan", round: 1, launched: map[string]bool{}, began: map[string]string{}}
}
func (w *workflow) record(stage, task, detail string) {
	w.history = append(w.history, phaseEntry{time.Now().UTC(), stage, task, detail})
}
func (w *workflow) violation(err error) error {
	w.violations = append(w.violations, err.Error())
	w.record("violation", "", err.Error())
	return err
}
func (w *workflow) workspace(role string) string {
	if role == "engineer" {
		return filepath.Join(w.work, "engineer", "project")
	}
	if role == "reviewer" || role == "verifier" {
		if w.current != nil {
			return w.current.Directory
		}
	}
	return filepath.Join(w.work, role)
}
func (w *workflow) canLaunch(role string) bool {
	return !w.launched[role] && ((role == "planner" && w.phase == "plan") || (role == "engineer" && w.phase == "implement") || ((role == "reviewer" || role == "verifier") && w.phase == "review"))
}

// This decorator remains a plain public Runner. Rejected launches still enter
// the real Agent's single pipeline, using an already cancelled context.
type roleRunner struct {
	agent adaptor.Runner
	role  string
	host  *workflow
}

func (r *roleRunner) Send(ctx context.Context, text string, opts ...adaptor.CallOption) adaptor.Stream {
	r.host.mu.Lock()
	valid := r.host.canLaunch(r.role)
	workspace := r.host.workspace(r.role)
	if valid {
		r.host.launched[r.role] = true
		r.host.record("launch", r.role, r.host.phase)
	} else {
		_ = r.host.violation(fmt.Errorf("out-of-order or duplicate launch: %s in %s", r.role, r.host.phase))
	}
	r.host.mu.Unlock()
	if !valid {
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		ctx = cancelled
	}
	forward := append([]adaptor.CallOption(nil), opts...)
	forward = append(forward, adaptor.WithWorkspace(workspace))
	return r.agent.Send(ctx, text, forward...)
}

func (w *workflow) task(ctx context.Context, id, role string) (team.Task, memberOutput, error) {
	tasks, err := w.store.Tasks(ctx, w.scope)
	if err != nil {
		return team.Task{}, memberOutput{}, err
	}
	for _, task := range tasks {
		if task.ID == id {
			if task.Member != role || task.State != team.Succeeded || task.Result == nil {
				return task, memberOutput{}, errors.New("task is not a succeeded result from the required member")
			}
			var out memberOutput
			if err := task.Result.Decode(&out); err != nil {
				return task, out, fmt.Errorf("decode task output: %w", err)
			}
			if err := validateOutput(out, role, w.spec); err != nil {
				return task, out, err
			}
			return task, out, nil
		}
	}
	return team.Task{}, memberOutput{}, errors.New("unknown task handle")
}

func (w *workflow) activeTask(ctx context.Context, role string) (team.Task, error) {
	tasks, err := w.store.Tasks(ctx, w.scope)
	if err != nil {
		return team.Task{}, err
	}
	var found []team.Task
	for _, task := range tasks {
		if task.Member == role && task.State == team.Running {
			found = append(found, task)
		}
	}
	if len(found) != 1 {
		return team.Task{}, fmt.Errorf("expected exactly one running %s task", role)
	}
	return found[0], nil
}

type emptyInput struct{}
type hostView struct {
	Stage         string         `json:"stage"`
	SpecHash      string         `json:"spec_hash"`
	CandidateHash string         `json:"candidate_hash"`
	Round         int            `json:"round"`
	Task          string         `json:"task,omitempty"`
	Project       string         `json:"project,omitempty"`
	Manifest      []artifact     `json:"manifest"`
	CheckReceipts []checkReceipt `json:"check_receipts"`
	Violations    []string       `json:"violations"`
}

func (w *workflow) viewLocked() hostView {
	v := hostView{Stage: w.phase, SpecHash: w.spec, Round: w.round, Manifest: []artifact{}, CheckReceipts: append([]checkReceipt{}, w.checks...), Violations: append([]string{}, w.violations...)}
	if w.current != nil {
		v.CandidateHash = w.current.Hash
		v.Manifest = append([]artifact(nil), w.current.Manifest...)
		v.Project = w.current.Directory
	}
	return v
}
func (w *workflow) inspect(context.Context, emptyInput) (hostView, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.viewLocked(), nil
}

func (w *workflow) begin(ctx context.Context, role string) (hostView, error) {
	task, err := w.activeTask(ctx, role)
	if err != nil {
		return hostView{}, err
	}
	w.mu.Lock()
	allowed := w.launched[role] && ((role == "planner" && w.phase == "plan") || (role == "engineer" && w.phase == "implement") || ((role == "reviewer" || role == "verifier") && w.phase == "review"))
	if !allowed {
		err = w.violation(errors.New("begin called by wrong role/stage"))
		w.mu.Unlock()
		return hostView{}, err
	}
	var barrier <-chan struct{}
	if role == "reviewer" || role == "verifier" {
		if _, exists := w.current.Joins[role]; !exists {
			w.current.Joins[role] = joinedTask{task.ID, time.Now().UTC()}
			w.record("review_join", task.ID, role)
		}
		if len(w.current.Joins) == 2 && !w.barrierReleased {
			close(w.barrier)
			w.barrierReleased = true
			w.record("review_barrier_released", "", w.current.Hash)
		}
		barrier = w.barrier
	} else {
		w.began[role] = task.ID
		w.record("member_begin", task.ID, role)
	}
	w.mu.Unlock()
	if barrier != nil {
		bounded, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		select {
		case <-barrier:
		case <-bounded.Done():
			return hostView{}, fmt.Errorf("review/verifier rendezvous: %w", bounded.Err())
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	view := w.viewLocked()
	view.Task = task.ID
	view.Project = w.workspace(role)
	return view, nil
}

type checkpointInput struct {
	Stage string `json:"stage"`
	Task  string `json:"task"`
}

func (w *workflow) checkpoint(ctx context.Context, in checkpointInput) (hostView, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	role := "planner"
	expected := "plan"
	if in.Stage == "implementation" {
		role = "engineer"
		expected = "implement"
	} else if in.Stage != "plan" {
		return hostView{}, w.violation(errors.New("unknown checkpoint stage"))
	}
	if w.phase != expected {
		return hostView{}, w.violation(errors.New("checkpoint out of order"))
	}
	_, out, err := w.task(ctx, in.Task, role)
	if err != nil {
		return hostView{}, w.violation(err)
	}
	if w.began[role] != in.Task {
		return hostView{}, w.violation(errors.New("task skipped its required workflow_begin"))
	}
	if role == "planner" {
		w.phase = "implement"
		w.record("plan_accepted", in.Task, out.Summary)
		return w.viewLocked(), nil
	}
	root := filepath.Join(w.work, "engineer", "project")
	if err = verifyManifest(root, out.Manifest); err != nil {
		return hostView{}, w.violation(err)
	}
	if out.CandidateHash != manifestHash(out.Manifest) {
		return hostView{}, w.violation(errors.New("engineer forged candidate identity"))
	}
	if err = hostFilesUnchanged(root); err != nil {
		return hostView{}, w.violation(err)
	}
	if err = validateRouter(root); err != nil {
		return hostView{}, w.violation(err)
	}
	if w.current != nil && w.current.Hash == out.CandidateHash {
		return hostView{}, w.violation(errors.New("rework did not change the rejected candidate"))
	}
	directory := filepath.Join(w.output, "candidates", fmt.Sprintf("%02d", w.round))
	if err = copyCandidate(root, directory, out.Manifest); err != nil {
		return hostView{}, w.violation(err)
	}
	w.current = &candidate{Round: w.round, Hash: out.CandidateHash, Manifest: append([]artifact(nil), out.Manifest...), Directory: directory, EngineerTask: in.Task, Joins: map[string]joinedTask{}}
	w.candidates = append(w.candidates, w.current)
	w.barrier = make(chan struct{})
	w.barrierReleased = false
	w.phase = "review"
	if err = writeJSON(directory+".manifest.json", w.current.Manifest); err != nil {
		return hostView{}, w.violation(err)
	}
	w.record("candidate_frozen", in.Task, w.current.Hash)
	return w.viewLocked(), nil
}

func hostFilesUnchanged(root string) error {
	for path, content := range map[string]string{"go.mod": fixtureModule, "acceptance_test.go": fixtureAcceptance} {
		data, err := safeRead(root, path)
		if err != nil {
			return err
		}
		if string(data) != content {
			return fmt.Errorf("host fixture changed: %s", path)
		}
	}
	return nil
}

func (w *workflow) manifest(ctx context.Context, _ emptyInput) (hostView, error) {
	if _, err := w.activeTask(ctx, "engineer"); err != nil {
		return hostView{}, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.phase != "implement" {
		return hostView{}, w.violation(errors.New("manifest outside implementation stage"))
	}
	root := w.workspace("engineer")
	if err := hostFilesUnchanged(root); err != nil {
		return hostView{}, w.violation(err)
	}
	files, err := projectManifest(root)
	if err != nil {
		return hostView{}, err
	}
	view := w.viewLocked()
	view.Manifest = files
	view.CandidateHash = manifestHash(files)
	view.Project = root
	return view, nil
}

type checkInput struct {
	CandidateHash string `json:"candidate_hash"`
	Check         string `json:"check"`
}

func (w *workflow) runChecks(ctx context.Context, in checkInput) (checkReceipt, error) {
	task, err := w.activeTask(ctx, "verifier")
	if err != nil {
		return checkReceipt{}, err
	}
	w.mu.Lock()
	if w.phase != "review" || w.current == nil || in.CandidateHash != w.current.Hash || !w.barrierReleased || w.current.Joins["verifier"].Task != task.ID || w.checkActive {
		err = w.violation(errors.New("check request has stale candidate, wrong stage, missing rendezvous, or concurrent invocation"))
		w.mu.Unlock()
		return checkReceipt{}, err
	}
	if _, ok := checkCommands[in.Check]; !ok {
		err = w.violation(errors.New("check command is not allowlisted"))
		w.mu.Unlock()
		return checkReceipt{}, err
	}
	for _, receipt := range w.checks {
		if receipt.Task == task.ID && receipt.Candidate == in.CandidateHash && receipt.Check == in.Check {
			w.mu.Unlock()
			return checkReceipt{}, errors.New("this check already ran; retain and report its original receipt")
		}
	}
	if err = verifyManifest(w.current.Directory, w.current.Manifest); err != nil {
		err = w.violation(err)
		w.mu.Unlock()
		return checkReceipt{}, err
	}
	root := w.current.Directory
	if err = validateRouter(root); err != nil {
		err = w.violation(err)
		w.mu.Unlock()
		return checkReceipt{}, err
	}
	expected := append([]artifact(nil), w.current.Manifest...)
	w.checkActive = true
	ordinal := len(w.checks) + 1
	w.mu.Unlock()
	receipt, runErr := runCheck(ctx, root, w.output, task.ID, in.CandidateHash, in.Check, ordinal)
	w.mu.Lock()
	defer w.mu.Unlock()
	w.checkActive = false
	if runErr != nil {
		return receipt, w.violation(runErr)
	}
	w.checks = append(w.checks, receipt)
	w.record("check_finished", task.ID, receipt.ID)
	if err = verifyManifest(root, expected); err != nil {
		return receipt, w.violation(err)
	}
	return receipt, nil
}

type gateInput struct {
	ReviewTask   string `json:"review_task"`
	VerifierTask string `json:"verifier_task"`
}

func (w *workflow) gate(ctx context.Context, in gateInput) (hostView, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.phase != "review" || w.current == nil || w.checkActive {
		return hostView{}, w.violation(errors.New("gate out of order"))
	}
	review, reviewOut, err := w.task(ctx, in.ReviewTask, "reviewer")
	if err != nil {
		return hostView{}, w.violation(err)
	}
	verify, verifyOut, err := w.task(ctx, in.VerifierTask, "verifier")
	if err != nil {
		return hostView{}, w.violation(err)
	}
	current := w.current
	read := current.ReviewerRead
	if read == nil || read.Task != review.ID || read.Candidate != current.Hash || read.ID != reviewOut.ReviewReceipt || read.At.Before(current.Joins["reviewer"].At) || read.At.After(review.UpdatedAt) {
		return hostView{}, w.violation(errors.New("reviewer did not read the current frozen candidate through the host"))
	}
	if reviewOut.CandidateHash != current.Hash || verifyOut.CandidateHash != current.Hash || current.Joins["reviewer"].Task != review.ID || current.Joins["verifier"].Task != verify.ID || !w.barrierReleased {
		return hostView{}, w.violation(errors.New("gate evidence belongs to another candidate or unobserved task"))
	}
	if maxTime(current.Joins["reviewer"].At, current.Joins["verifier"].At).After(minTime(review.UpdatedAt, verify.UpdatedAt)) {
		return hostView{}, w.violation(errors.New("review and verification did not overlap"))
	}
	if err = verifyManifest(current.Directory, current.Manifest); err != nil {
		return hostView{}, w.violation(err)
	}
	passed := reviewOut.Verdict == "approve" && verifyOut.Verdict == "pass"
	for _, out := range []memberOutput{reviewOut, verifyOut} {
		for _, item := range out.Findings {
			if item.Severity == "error" {
				passed = false
			}
		}
	}
	seen := map[string]bool{}
	receiptIDs := map[string]bool{}
	for _, id := range verifyOut.CheckReceipts {
		if receiptIDs[id] {
			return hostView{}, w.violation(errors.New("duplicate check receipt"))
		}
		receiptIDs[id] = true
		found := false
		for _, receipt := range w.checks {
			if receipt.ID == id {
				if receipt.Task != verify.ID || receipt.Candidate != current.Hash {
					return hostView{}, w.violation(errors.New("receipt belongs to another task or candidate"))
				}
				data, readErr := os.ReadFile(filepath.Join(w.output, filepath.FromSlash(receipt.Log)))
				if readErr != nil || digest(data) != receipt.LogSHA256 {
					return hostView{}, w.violation(errors.New("check log hash mismatch"))
				}
				if !receipt.Started.Before(receipt.Finished) || receipt.Started.Before(current.Joins["verifier"].At) || receipt.Finished.After(verify.UpdatedAt) {
					return hostView{}, w.violation(errors.New("check receipt outside verifier lifecycle"))
				}
				seen[receipt.Check] = true
				if receipt.ExitCode != 0 {
					passed = false
				}
				found = true
				break
			}
		}
		if !found {
			return hostView{}, w.violation(errors.New("forged check receipt"))
		}
	}
	if !seen["test"] || !seen["vet"] {
		return hostView{}, w.violation(errors.New("verifier omitted required host checks"))
	}
	for _, receipt := range w.checks {
		if receipt.Task == verify.ID && receipt.Candidate == current.Hash && !receiptIDs[receipt.ID] {
			return hostView{}, w.violation(errors.New("verifier hid an actual host check receipt"))
		}
	}
	current.ReviewTask = review.ID
	current.VerifyTask = verify.ID
	current.Accepted = passed
	if passed {
		w.phase = "gate_passed"
		w.record("gate_passed", "", current.Hash)
	} else if w.round < 3 {
		w.round++
		w.phase = "implement"
		w.launched["engineer"] = false
		w.launched["reviewer"] = false
		w.launched["verifier"] = false
		w.record("rework_required", "", current.Hash)
	} else {
		w.phase = "failed"
		w.record("rework_exhausted", "", current.Hash)
	}
	return w.viewLocked(), nil
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

type integrateInput struct {
	CandidateHash string `json:"candidate_hash"`
}

func (w *workflow) integrate(_ context.Context, in integrateInput) (hostView, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.phase != "gate_passed" || w.current == nil || !w.current.Accepted || in.CandidateHash != w.current.Hash || len(w.violations) > 0 {
		return hostView{}, w.violation(errors.New("integration attempted without a clean current gate"))
	}
	if err := copyCandidate(w.current.Directory, filepath.Join(w.output, "integrated"), w.current.Manifest); err != nil {
		return hostView{}, w.violation(err)
	}
	w.phase = "integrated"
	w.record("integrated", "", w.current.Hash)
	return w.viewLocked(), nil
}

func (w *workflow) leaderTools() []tool.Definition {
	return []tool.Definition{
		tool.Define("workflow_inspect", "Read the authoritative workflow stage, current hash and host evidence.", w.inspect, tool.ReadOnly(), tool.Revision(workflowRevision)),
		tool.Define("workflow_checkpoint", "After the indicated task succeeds: stage=plan accepts planner output; stage=implementation independently validates the engineer manifest and freezes its complete candidate.", w.checkpoint, tool.NonDestructive(), tool.Revision(workflowRevision)),
		tool.Define("workflow_gate", "After both current reviewer and verifier tasks succeed, independently inspect their structured outputs, actual overlapping lifecycles and host check receipts. Return gate_passed, implement (rework), or failed.", w.gate, tool.NonDestructive(), tool.Revision(workflowRevision)),
		tool.Define("workflow_integrate", "Only after gate_passed: copy the verified frozen candidate into the final integrated artifact.", w.integrate, tool.NonDestructive(), tool.Revision(workflowRevision)),
	}
}
func (w *workflow) memberTools(role string) []tool.Definition {
	definitions := []tool.Definition{tool.Define("workflow_begin", "Call FIRST. Identify your actual running task and read host stage/spec/candidate. Reviewer and verifier rendezvous here until both real agents have called, with a bounded cancellation-safe wait.", func(ctx context.Context, _ emptyInput) (hostView, error) { return w.begin(ctx, role) }, tool.ReadOnly(), tool.Revision(workflowRevision))}
	if role == "engineer" {
		definitions = append(definitions, tool.Define("workflow_manifest", "After editing only router.go, ask the host for the complete actual project manifest and candidate hash; use these exact values in your final output.", w.manifest, tool.ReadOnly(), tool.Revision(workflowRevision)))
	}
	if role == "verifier" {
		// Only this member receives the check-execution capability.
		definitions = append(definitions, tool.Define("workflow_run_checks", "Verifier only: execute check=test and check=vet separately against the current frozen candidate; host returns an actual argv/exit/log-hash receipt. Report all IDs, including failed checks.", w.runChecks, tool.ReadOnly(), tool.Revision(workflowRevision)))
	}
	if role == "reviewer" {
		definitions = append(definitions, tool.Define("workflow_read_candidate", "Reviewer only: read every exact frozen candidate file by current candidate_hash after workflow_begin. Return the host read_receipt in your structured output and cite specific implementation evidence per requirement.", w.readCandidate, tool.ReadOnly(), tool.Revision(workflowRevision)))
	}
	return definitions
}

func (w *workflow) finalError(ctx context.Context, streamErr error) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	err := streamErr
	if w.phase != "integrated" {
		err = errors.Join(err, fmt.Errorf("workflow ended in stage %q", w.phase))
	}
	if len(w.violations) > 0 {
		err = errors.Join(err, fmt.Errorf("host recorded %d protocol violations", len(w.violations)))
	}
	if w.current != nil {
		err = errors.Join(err, verifyManifest(w.current.Directory, w.current.Manifest))
		if w.phase == "integrated" {
			err = errors.Join(err, verifyManifest(filepath.Join(w.output, "integrated"), w.current.Manifest))
		}
	}
	tasks, storeErr := w.store.Tasks(ctx, w.scope)
	err = errors.Join(err, storeErr)
	for _, task := range tasks {
		if task.State != team.Succeeded {
			err = errors.Join(err, fmt.Errorf("task %s/%s ended %s", task.ID, task.Member, task.State))
		}
	}
	return err
}

var _ adaptor.Runner = (*roleRunner)(nil)
