package claude

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

// fakeInteractiveSink records RequestDecision invocations and returns a
// canned response. The parser's interactive flow MUST call RequestDecision
// exactly once per whitelisted tool_use, so tests use this recorder to
// verify both the invocation and the DecisionRequest shape.
type fakeInteractiveSink struct {
	mu       sync.Mutex
	events   []agentadaptor.StreamPayload
	requests []agentadaptor.DecisionRequest
	respond  func(agentadaptor.DecisionRequest) (agentadaptor.DecisionResponse, error)
}

func newFakeInteractiveSink(respond func(agentadaptor.DecisionRequest) (agentadaptor.DecisionResponse, error)) *fakeInteractiveSink {
	return &fakeInteractiveSink{respond: respond}
}

func (f *fakeInteractiveSink) Emit(agentadaptor.RunEvent) error { return nil }
func (f *fakeInteractiveSink) EmitStream(p agentadaptor.StreamPayload) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, p)
	return nil
}

func (f *fakeInteractiveSink) RequestDecision(ctx context.Context, req agentadaptor.DecisionRequest) (agentadaptor.DecisionResponse, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	f.mu.Unlock()
	return f.respond(req)
}

// fakeStdin captures frames written back to the CLI so tests can verify the
// exact control_response envelope shape.
type fakeStdin struct {
	mu     sync.Mutex
	frames [][]byte
	closed int
}

func (f *fakeStdin) Write(frame []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.frames = append(f.frames, append([]byte(nil), frame...))
	return nil
}

func (f *fakeStdin) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed++
	return nil
}

func (f *fakeStdin) snapshot() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]byte, len(f.frames))
	for i, v := range f.frames {
		out[i] = append([]byte(nil), v...)
	}
	return out
}

// TestPhase3_PlanApproved_InjectsApproveToolResult drives the full
// stream-json interactive path with a canned approval. It verifies:
//
//  1. The parser invokes RequestDecision exactly once with the right Kind
//     + payload
//  2. A control_response allow frame is written back to stdin
//  3. No pendingFailure is recorded
func TestPhase3_PlanApproved_InjectsApproveToolResult(t *testing.T) {
	data := mustReadFixture(t, "streaming-phase3-plan.jsonl")

	sink := newFakeInteractiveSink(func(req agentadaptor.DecisionRequest) (agentadaptor.DecisionResponse, error) {
		return agentadaptor.DecisionResponse{
			RequestID: req.RequestID,
			Result:    agentadaptor.DecisionApproved,
		}, nil
	})
	stdin := &fakeStdin{}

	p := newClaudeParser(sink)
	p.setHITLContext("run-p3-plan-ok", agentadaptor.HumanDecisionPolicy{
		PlanReview: agentadaptor.HumanDecisionAsk,
	})
	p.enableStreaming("run-p3-plan-ok")
	p.enableInteractive(context.Background(), sink, stdin)

	if err := p.onChunk("stdout", data, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	p.finalize()

	if got := len(sink.requests); got != 1 {
		t.Fatalf("want 1 RequestDecision call, got %d: %+v", got, sink.requests)
	}
	req := sink.requests[0]
	if req.Kind != agentadaptor.HumanDecisionPlanReview {
		t.Errorf("Kind: %q want plan_review", req.Kind)
	}
	if req.Source != "claude.exit_plan_mode" {
		t.Errorf("Source: %q", req.Source)
	}
	plan, _ := req.Payload["plan"].(string)
	if !strings.Contains(plan, "Audit AGENTS.md") {
		t.Errorf("plan payload missing expected content: %q", plan)
	}
	if req.ToolCallID != "toolu_plan_phase3" {
		t.Errorf("ToolCallID: %q", req.ToolCallID)
	}

	frames := stdin.snapshot()
	if len(frames) != 1 {
		t.Fatalf("want 1 stdin frame, got %d", len(frames))
	}
	cr := decodeControlResponseFrame(t, frames[0])
	if cr.RequestID != "req-plan-phase3" {
		t.Errorf("request_id: %q", cr.RequestID)
	}
	if cr.Behavior != "allow" {
		t.Fatalf("approved plan should produce allow, got %q", cr.Behavior)
	}
	if cr.ToolUseID != "toolu_plan_phase3" {
		t.Errorf("toolUseID: %q", cr.ToolUseID)
	}
	planOut, _ := cr.UpdatedInput["plan"].(string)
	if !strings.Contains(planOut, "Audit AGENTS.md") {
		t.Errorf("approved updatedInput.plan: %q", planOut)
	}

	if p.pendingFailure != nil {
		t.Errorf("approved plan should not set pendingFailure, got %+v", p.pendingFailure)
	}
}

// TestPhase3_PlanRejected_SetsFailureAndDenies covers OnReject=FailureAbort:
// a rejected decision injects a deny control_response and records a structured
// RunFailure the driver can surface to the host.
func TestPhase3_PlanRejected_SetsFailureAndDenies(t *testing.T) {
	data := mustReadFixture(t, "streaming-phase3-plan.jsonl")

	sink := newFakeInteractiveSink(func(req agentadaptor.DecisionRequest) (agentadaptor.DecisionResponse, error) {
		return agentadaptor.DecisionResponse{
			RequestID: req.RequestID,
			Result:    agentadaptor.DecisionRejected,
			Text:      "Be more careful with AGENTS.md root section.",
		}, nil
	})
	stdin := &fakeStdin{}

	p := newClaudeParser(sink)
	p.setHITLContext("run-p3-plan-rej", agentadaptor.HumanDecisionPolicy{
		PlanReview: agentadaptor.HumanDecisionAsk,
		OnReject:   agentadaptor.FailureAbort,
	})
	p.enableStreaming("run-p3-plan-rej")
	p.enableInteractive(context.Background(), sink, stdin)

	if err := p.onChunk("stdout", data, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	p.finalize()

	if p.pendingFailure == nil {
		t.Fatal("rejected plan must set pendingFailure")
	}
	if p.pendingFailure.Code != agentadaptor.FailureReject {
		t.Errorf("Failure.Code: %q", p.pendingFailure.Code)
	}
	hd := p.pendingFailure.HumanDecision
	if hd == nil || hd.Kind != agentadaptor.HumanDecisionPlanReview {
		t.Errorf("HumanDecision attribution missing: %+v", hd)
	}

	frames := stdin.snapshot()
	if len(frames) != 1 {
		t.Fatalf("want 1 stdin frame, got %d", len(frames))
	}
	cr := decodeControlResponseFrame(t, frames[0])
	if cr.Behavior != "deny" {
		t.Fatalf("rejected plan must produce deny, got %q", cr.Behavior)
	}
	if !cr.Interrupt {
		t.Fatal("rejected plan with OnReject=Abort must set interrupt=true")
	}
	if !strings.Contains(cr.Message, "User rejected") {
		t.Errorf("rejected message: %q", cr.Message)
	}
	if !strings.Contains(cr.Message, "Be more careful") {
		t.Errorf("rejection Text should be forwarded to CLI: %q", cr.Message)
	}
	if err := p.onChunk("stdout", frames[0], time.Now().UTC()); err != nil {
		t.Fatalf("echo control_response ack: %v", err)
	}
	if stdin.closed != 1 {
		t.Fatalf("rejected plan should close stdin after control_response ack, got %d", stdin.closed)
	}
}

// TestPhase3_QuestionAnswered_MultipleChoice verifies the multipleChoice
// path: parser should normalise claude's nested questions[].options shape
// into choices, and a selected Choice round-trips into the CLI updatedInput.
func TestPhase3_QuestionAnswered_MultipleChoice(t *testing.T) {
	data := mustReadFixture(t, "streaming-phase3-question.jsonl")

	sink := newFakeInteractiveSink(func(req agentadaptor.DecisionRequest) (agentadaptor.DecisionResponse, error) {
		if req.Prompt != "Pick a directory" {
			t.Errorf("prompt not extracted from questions[0].question: %q", req.Prompt)
		}
		if len(req.Choices) != 2 {
			t.Errorf("options not decoded into Choices: %+v", req.Choices)
		}
		if qt, _ := req.Payload["question_type"].(string); qt != "multipleChoice" {
			t.Errorf("payload.question_type: %q want multipleChoice", qt)
		}
		return agentadaptor.DecisionResponse{
			RequestID: req.RequestID,
			Result:    agentadaptor.DecisionAnswered,
			Choice:    "docs",
		}, nil
	})
	stdin := &fakeStdin{}

	p := newClaudeParser(sink)
	p.setHITLContext("run-p3-q", agentadaptor.HumanDecisionPolicy{
		Question: agentadaptor.QuestionAsk,
	})
	p.enableStreaming("run-p3-q")
	p.enableInteractive(context.Background(), sink, stdin)

	if err := p.onChunk("stdout", data, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	p.finalize()

	if got := len(sink.requests); got != 1 {
		t.Fatalf("want 1 RequestDecision, got %d", got)
	}

	frames := stdin.snapshot()
	if len(frames) != 1 {
		t.Fatalf("want 1 stdin frame, got %d", len(frames))
	}
	cr := decodeControlResponseFrame(t, frames[0])
	if cr.Behavior != "allow" {
		t.Fatalf("answered question must produce allow, got %q", cr.Behavior)
	}
	if _, ok := cr.UpdatedInput["question_type"]; ok {
		t.Fatalf("updatedInput must not leak synthetic question_type: %+v", cr.UpdatedInput)
	}
	answers, ok := cr.UpdatedInput["answers"].(map[string]any)
	if !ok {
		t.Fatalf("updatedInput.answers missing: %+v", cr.UpdatedInput)
	}
	if got, _ := answers["Pick a directory"].(string); got != "docs" {
		t.Fatalf("answer round-trip: got %q want docs", got)
	}
}

// TestPhase3_QuestionAnswered_FreeText verifies the freeText path: there
// are no choices, the prompt comes from questions[0].question, and the
// card's free-text answer round-trips into the CLI updatedInput.
func TestPhase3_QuestionAnswered_FreeText(t *testing.T) {
	data := mustReadFixture(t, "streaming-phase3-question-freetext.jsonl")

	sink := newFakeInteractiveSink(func(req agentadaptor.DecisionRequest) (agentadaptor.DecisionResponse, error) {
		if req.Prompt != "What problem would you like me to tackle?" {
			t.Errorf("prompt: %q", req.Prompt)
		}
		if len(req.Choices) != 0 {
			t.Errorf("freeText must have no choices, got %+v", req.Choices)
		}
		if qt, _ := req.Payload["question_type"].(string); qt != "freeText" {
			t.Errorf("question_type: %q", qt)
		}
		return agentadaptor.DecisionResponse{
			RequestID: req.RequestID,
			Result:    agentadaptor.DecisionAnswered,
			Text:      "Add a rate limit on /api/checkout.",
			Answer:    map[string]any{"text": "Add a rate limit on /api/checkout."},
		}, nil
	})
	stdin := &fakeStdin{}

	p := newClaudeParser(sink)
	p.setHITLContext("run-p3-qft", agentadaptor.HumanDecisionPolicy{
		Question: agentadaptor.QuestionAsk,
	})
	p.enableStreaming("run-p3-qft")
	p.enableInteractive(context.Background(), sink, stdin)

	if err := p.onChunk("stdout", data, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	p.finalize()

	frames := stdin.snapshot()
	if len(frames) != 1 {
		t.Fatalf("want 1 stdin frame, got %d", len(frames))
	}
	cr := decodeControlResponseFrame(t, frames[0])
	if cr.Behavior != "allow" {
		t.Fatalf("answered freeText must produce allow, got %q", cr.Behavior)
	}
	answers, ok := cr.UpdatedInput["answers"].(map[string]any)
	if !ok {
		t.Fatalf("updatedInput.answers missing: %+v", cr.UpdatedInput)
	}
	if got, _ := answers["What problem would you like me to tackle?"].(string); got != "Add a rate limit on /api/checkout." {
		t.Fatalf("freeText round-trip: got %q", got)
	}
}

// TestExtractInteractivePayload_Shapes locks in the parser's schema
// normalisation so future claude CLI drift is caught.
func TestExtractInteractivePayload_Shapes(t *testing.T) {
	t.Run("plan", func(t *testing.T) {
		prompt, choices, payload := extractInteractivePayload(
			agentadaptor.HumanDecisionPlanReview,
			map[string]any{"plan": "do the thing"},
		)
		if prompt != "" {
			t.Errorf("plan prompt should be empty: %q", prompt)
		}
		if len(choices) != 0 {
			t.Errorf("plan has no choices, got %+v", choices)
		}
		if payload["plan"] != "do the thing" {
			t.Errorf("payload.plan: %v", payload["plan"])
		}
	})

	t.Run("question multipleChoice", func(t *testing.T) {
		prompt, choices, payload := extractInteractivePayload(
			agentadaptor.HumanDecisionQuestion,
			map[string]any{
				"questions": []any{
					map[string]any{
						"type":     "multipleChoice",
						"question": "Which env?",
						"options":  []any{"dev", "staging", "prod"},
					},
				},
			},
		)
		if prompt != "Which env?" {
			t.Errorf("prompt: %q", prompt)
		}
		if len(choices) != 3 || choices[0].Key != "dev" {
			t.Errorf("choices: %+v", choices)
		}
		if payload["question_type"] != "multipleChoice" {
			t.Errorf("question_type: %v", payload["question_type"])
		}
	})

	t.Run("question multipleChoice label-only options", func(t *testing.T) {
		prompt, choices, payload := extractInteractivePayload(
			agentadaptor.HumanDecisionQuestion,
			map[string]any{
				"questions": []any{
					map[string]any{
						"type":     "multipleChoice",
						"question": "请选择性别？",
						"options": []any{
							map[string]any{"label": "男性", "description": "男"},
							map[string]any{"label": "女性", "description": "女"},
						},
					},
				},
			},
		)
		if prompt != "请选择性别？" {
			t.Errorf("prompt: %q", prompt)
		}
		if len(choices) != 2 {
			t.Fatalf("choices: %+v", choices)
		}
		if choices[0].Key != "男性" || choices[0].Label != "男性" {
			t.Errorf("choice[0]: %+v", choices[0])
		}
		if choices[0].Description != "男" {
			t.Errorf("choice[0].Description: %q", choices[0].Description)
		}
		if payload["question_type"] != "multipleChoice" {
			t.Errorf("question_type: %v", payload["question_type"])
		}
	})

	t.Run("question freeText", func(t *testing.T) {
		prompt, choices, _ := extractInteractivePayload(
			agentadaptor.HumanDecisionQuestion,
			map[string]any{
				"questions": []any{
					map[string]any{"type": "freeText", "question": "What next?"},
				},
			},
		)
		if prompt != "What next?" {
			t.Errorf("prompt: %q", prompt)
		}
		if len(choices) != 0 {
			t.Errorf("freeText must have no choices: %+v", choices)
		}
	})

	t.Run("question empty (model sent no questions)", func(t *testing.T) {
		prompt, _, _ := extractInteractivePayload(
			agentadaptor.HumanDecisionQuestion,
			map[string]any{"questions": []any{}},
		)
		if prompt != "" {
			t.Errorf("empty questions should produce empty prompt, got %q", prompt)
		}
	})

	t.Run("question legacy prompt fallback", func(t *testing.T) {
		prompt, _, _ := extractInteractivePayload(
			agentadaptor.HumanDecisionQuestion,
			map[string]any{"prompt": "Direct prompt"},
		)
		if prompt != "Direct prompt" {
			t.Errorf("prompt: %q", prompt)
		}
	})
}

// TestPhase3_NonWhitelistedTool_AutoApprove_WritesControlResponse verifies
// that non-whitelisted tools in interactive mode are also resolved through
// can_use_tool control_request, not by a synthetic tool_result denial.
func TestPhase3_NonWhitelistedTool_AutoApprove_WritesControlResponse(t *testing.T) {
	data := mustReadFixture(t, "streaming-phase3-bash.jsonl")

	sink := newFakeInteractiveSink(func(agentadaptor.DecisionRequest) (agentadaptor.DecisionResponse, error) {
		t.Fatal("RequestDecision must not be called for non-whitelisted tools in Phase 3")
		return agentadaptor.DecisionResponse{}, nil
	})
	stdin := &fakeStdin{}

	p := newClaudeParser(sink)
	p.setHITLContext("run-p3-bash", agentadaptor.HumanDecisionPolicy{
		Permission: agentadaptor.HumanDecisionAutoApprove,
		PlanReview: agentadaptor.HumanDecisionAsk,
	})
	p.enableStreaming("run-p3-bash")
	p.enableInteractive(context.Background(), sink, stdin)

	if err := p.onChunk("stdout", data, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	p.finalize()

	if got := len(sink.requests); got != 0 {
		t.Fatalf("RequestDecision must not be invoked for Bash, got %d calls", got)
	}

	frames := stdin.snapshot()
	if len(frames) != 1 {
		t.Fatalf("want exactly one control_response frame, got %d", len(frames))
	}
	cr := decodeControlResponseFrame(t, frames[0])
	if cr.RequestID != "req-bash-phase3" {
		t.Errorf("request_id: %q", cr.RequestID)
	}
	if cr.Behavior != "allow" {
		t.Fatalf("Bash auto-approve must produce allow, got %q", cr.Behavior)
	}
	if cr.ToolUseID != "toolu_bash_phase3" {
		t.Errorf("toolUseID: %q", cr.ToolUseID)
	}
	if got, _ := cr.UpdatedInput["command"].(string); got != "ls" {
		t.Errorf("updatedInput.command: %q", got)
	}
}

// TestPhase3_IsReplayFrame_Ignored covers the ack stream: the CLI echoes
// every user frame we inject as {"isReplay":true,…}. Those must be dropped
// so the transcript doesn't gain phantom tool_result entries.
func TestPhase3_IsReplayFrame_Ignored(t *testing.T) {
	// Synthetic NDJSON: one real assistant message + one isReplay echo of a
	// prior user frame. Feed them into the parser with interactive mode
	// enabled and confirm the transcript has no tool_result item.
	replay := `{"type":"user","isReplay":true,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_x","content":"whatever","is_error":false}]}}` + "\n"
	realAssistant := `{"type":"assistant","message":{"model":"claude-fixture","content":[{"type":"text","text":"done"}]}}` + "\n"

	sink := newFakeInteractiveSink(func(agentadaptor.DecisionRequest) (agentadaptor.DecisionResponse, error) {
		return agentadaptor.DecisionResponse{}, nil
	})
	stdin := &fakeStdin{}

	p := newClaudeParser(sink)
	p.setHITLContext("run-p3-replay", agentadaptor.HumanDecisionPolicy{PlanReview: agentadaptor.HumanDecisionAsk})
	p.enableStreaming("run-p3-replay")
	p.enableInteractive(context.Background(), sink, stdin)

	if err := p.onChunk("stdout", []byte(replay+realAssistant), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	p.finalize()

	for _, item := range p.transcript {
		if item.Kind == agentadaptor.TranscriptToolResult {
			t.Errorf("isReplay frame should NOT produce TranscriptToolResult, got %+v", item)
		}
	}
	if got := len(stdin.snapshot()); got != 0 {
		t.Errorf("isReplay frame must not trigger outbound stdin writes, got %d", got)
	}
}

// TestRenderInteractiveToolResult covers the per-Kind content mapping.
func TestRenderInteractiveToolResult(t *testing.T) {
	t.Run("plan approved", func(t *testing.T) {
		content, isError := renderInteractiveToolResult(
			agentadaptor.DecisionRequest{Kind: agentadaptor.HumanDecisionPlanReview},
			agentadaptor.DecisionResponse{Result: agentadaptor.DecisionApproved},
		)
		if isError {
			t.Fatal("plan approval must not be an error")
		}
		if !strings.Contains(content, "User approved the plan") {
			t.Fatalf("content: %q", content)
		}
	})

	t.Run("plan rejected with hint", func(t *testing.T) {
		content, isError := renderInteractiveToolResult(
			agentadaptor.DecisionRequest{Kind: agentadaptor.HumanDecisionPlanReview},
			agentadaptor.DecisionResponse{Result: agentadaptor.DecisionRejected, Text: "try smaller batches"},
		)
		if !isError {
			t.Fatal("plan rejection must be an error")
		}
		if !strings.Contains(content, "try smaller batches") {
			t.Fatalf("content: %q", content)
		}
	})

	t.Run("question answered - choice resolves to label with question context", func(t *testing.T) {
		req := agentadaptor.DecisionRequest{
			Kind:   agentadaptor.HumanDecisionQuestion,
			Prompt: "请问您的性别是?",
			Choices: []agentadaptor.DecisionChoice{
				{Key: "male", Label: "男性"},
				{Key: "female", Label: "女性"},
			},
			Payload: map[string]any{
				"questions": []any{
					map[string]any{
						"question": "请问您的性别是?",
					},
				},
			},
		}
		resp := agentadaptor.DecisionResponse{
			Result: agentadaptor.DecisionAnswered,
			Choice: "male",
		}
		content, isError := renderInteractiveToolResult(req, resp)
		if isError {
			t.Fatal("answered question must not be an error")
		}
		if !strings.Contains(content, `"请问您的性别是?"="男性"`) {
			t.Fatalf("content: %q", content)
		}
		if strings.Contains(content, `"请问您的性别是?"="male"`) {
			t.Fatalf("content should use display label, got %q", content)
		}
	})

	t.Run("question answered - structured answers and annotations", func(t *testing.T) {
		req := agentadaptor.DecisionRequest{
			Kind: agentadaptor.HumanDecisionQuestion,
			Payload: map[string]any{
				"questions": []any{
					map[string]any{"question": "Which env?"},
					map[string]any{"question": "Any notes?"},
				},
			},
		}
		resp := agentadaptor.DecisionResponse{
			Result: agentadaptor.DecisionAnswered,
			Answer: map[string]any{
				"answers": map[string]any{
					"Which env?": "staging",
					"Any notes?": "deploy after 5pm",
				},
				"annotations": map[string]any{
					"Which env?": map[string]any{
						"preview": "<div>staging preview</div>",
						"notes":   "matches release candidate",
					},
				},
			},
		}
		content, isError := renderInteractiveToolResult(req, resp)
		if isError {
			t.Fatal("answered question must not be an error")
		}
		for _, want := range []string{
			`"Which env?"="staging"`,
			`"Any notes?"="deploy after 5pm"`,
			"selected preview:\n<div>staging preview</div>",
			"user notes: matches release candidate",
		} {
			if !strings.Contains(content, want) {
				t.Fatalf("content %q missing %q", content, want)
			}
		}
	})

	t.Run("question answered - answer map text key", func(t *testing.T) {
		req := agentadaptor.DecisionRequest{
			Kind:   agentadaptor.HumanDecisionQuestion,
			Prompt: "What next?",
		}
		content, isError := renderInteractiveToolResult(
			req,
			agentadaptor.DecisionResponse{
				Result: agentadaptor.DecisionAnswered,
				Answer: map[string]any{"text": "my reply"},
			},
		)
		if isError {
			t.Fatal("answered question must not be an error")
		}
		if !strings.Contains(content, `"What next?"="my reply"`) {
			t.Fatalf("content: %q", content)
		}
	})

	t.Run("question rejected", func(t *testing.T) {
		content, isError := renderInteractiveToolResult(
			agentadaptor.DecisionRequest{Kind: agentadaptor.HumanDecisionQuestion},
			agentadaptor.DecisionResponse{Result: agentadaptor.DecisionRejected},
		)
		if !isError {
			t.Fatal("rejected question must be an error")
		}
		if !strings.Contains(content, "User declined") {
			t.Fatalf("content: %q", content)
		}
	})
}

func TestBuildInteractiveControlResponse_QuestionAnswered_UsesDisplayLabel(t *testing.T) {
	req := agentadaptor.DecisionRequest{
		Kind:       agentadaptor.HumanDecisionQuestion,
		ToolCallID: "toolu_gender",
		Prompt:     "请问您的性别是?",
		Choices: []agentadaptor.DecisionChoice{
			{Key: "男性", Label: "男性"},
			{Key: "女性", Label: "女性"},
		},
		Payload: map[string]any{
			"questions": []any{
				map[string]any{
					"type":     "multipleChoice",
					"question": "请问您的性别是?",
					"options": []any{
						map[string]any{"label": "男性", "description": "男"},
						map[string]any{"label": "女性", "description": "女"},
					},
				},
			},
			"question_type": "multipleChoice",
		},
	}
	resp := agentadaptor.DecisionResponse{
		Result: agentadaptor.DecisionAnswered,
		Choice: "男性",
	}

	got := buildInteractiveControlResponse(req, resp)
	if got["behavior"] != "allow" {
		t.Fatalf("behavior: %v", got["behavior"])
	}
	if got["toolUseID"] != "toolu_gender" {
		t.Fatalf("toolUseID: %v", got["toolUseID"])
	}
	updatedInput, _ := got["updatedInput"].(map[string]any)
	if updatedInput == nil {
		t.Fatalf("updatedInput missing: %+v", got)
	}
	if _, ok := updatedInput["question_type"]; ok {
		t.Fatalf("question_type must be stripped: %+v", updatedInput)
	}
	answers, _ := updatedInput["answers"].(map[string]any)
	if answers == nil {
		t.Fatalf("answers missing: %+v", updatedInput)
	}
	if gotAnswer, _ := answers["请问您的性别是?"].(string); gotAnswer != "男性" {
		t.Fatalf("answer: got %q want 男性", gotAnswer)
	}
}

// TestEncodeInteractiveUserFrame sanity-checks the wire shape expected by
// --input-format stream-json.
func TestEncodeInteractiveUserFrame(t *testing.T) {
	raw, err := encodeInteractiveUserFrame("hello")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(raw, "\n") {
		t.Error("frame must terminate with newline (NDJSON)")
	}
	var decoded struct {
		Type    string `json:"type"`
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(strings.TrimRight(raw, "\n")), &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Type != "user" || decoded.Message.Role != "user" || decoded.Message.Content != "hello" {
		t.Errorf("decoded: %+v", decoded)
	}
}

// TestValidateInteractivePolicy covers the "Permission=Ask not supported"
// guard: Phase 3 explicitly rejects that combo. Importantly the guard only
// fires on the RAW field — not effective defaults — so users with a zero
// policy are not blocked.
func TestValidateInteractivePolicy(t *testing.T) {
	cases := []struct {
		name    string
		policy  agentadaptor.HumanDecisionPolicy
		wantErr bool
	}{
		{"explicit permission ask rejected", agentadaptor.HumanDecisionPolicy{
			Permission: agentadaptor.HumanDecisionAsk,
			PlanReview: agentadaptor.HumanDecisionAsk,
		}, true},
		{"plan ask only (permission unset)", agentadaptor.HumanDecisionPolicy{
			PlanReview: agentadaptor.HumanDecisionAsk,
		}, false},
		{"plan ask + permission auto", agentadaptor.HumanDecisionPolicy{
			Permission: agentadaptor.HumanDecisionAutoApprove,
			PlanReview: agentadaptor.HumanDecisionAsk,
		}, false},
		{"question ask + permission auto", agentadaptor.HumanDecisionPolicy{
			Permission: agentadaptor.HumanDecisionAutoApprove,
			Question:   agentadaptor.QuestionAsk,
		}, false},
	}
	for _, c := range cases {
		err := validateInteractivePolicy(c.policy)
		if (err != nil) != c.wantErr {
			t.Errorf("%s: got err=%v wantErr=%v", c.name, err, c.wantErr)
		}
	}
}

// TestWantsInteractiveClaude covers the mode-selection helper. The helper
// inspects raw policy fields: a zero-valued policy stays in Phase 1 mode
// (no interactive engagement) even though the effective defaults are Ask.
func TestWantsInteractiveClaude(t *testing.T) {
	cases := []struct {
		name   string
		policy agentadaptor.HumanDecisionPolicy
		want   bool
	}{
		{"zero policy stays Phase 1", agentadaptor.HumanDecisionPolicy{}, false},
		{"autonomous policy", agentadaptor.HumanDecisionPolicy{
			Permission: agentadaptor.HumanDecisionAutoApprove,
			PlanReview: agentadaptor.HumanDecisionAutoApprove,
			Question:   agentadaptor.QuestionAutoReject,
		}, false},
		{"explicit plan ask", agentadaptor.HumanDecisionPolicy{
			Permission: agentadaptor.HumanDecisionAutoApprove,
			PlanReview: agentadaptor.HumanDecisionAsk,
		}, true},
		{"explicit question ask", agentadaptor.HumanDecisionPolicy{
			Permission: agentadaptor.HumanDecisionAutoApprove,
			Question:   agentadaptor.QuestionAsk,
		}, true},
		{"AutoReject stays Phase 1", agentadaptor.HumanDecisionPolicy{
			Permission: agentadaptor.HumanDecisionAutoApprove,
			PlanReview: agentadaptor.HumanDecisionAutoReject,
		}, false},
	}
	for _, c := range cases {
		if got := wantsInteractiveClaude(c.policy); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

// TestPhase3_OnAssistantMessageStop_Stdin closes the interactive stdin on
// end_turn (and similar) so the host run can finish; tool_use turns must not
// close before the model has received the injected tool_result.
func TestPhase3_OnAssistantMessageStop_Stdin(t *testing.T) {
	sink := newFakeInteractiveSink(func(req agentadaptor.DecisionRequest) (agentadaptor.DecisionResponse, error) {
		return agentadaptor.DecisionResponse{RequestID: req.RequestID, Result: agentadaptor.DecisionApproved}, nil
	})
	stdin := &fakeStdin{}
	p := newClaudeParser(sink)
	p.setHITLContext("r1", agentadaptor.HumanDecisionPolicy{PlanReview: agentadaptor.HumanDecisionAsk})
	p.enableStreaming("r1")
	p.enableInteractive(context.Background(), sink, stdin)

	p.onAssistantMessageStop("tool_use")
	if stdin.closed != 0 {
		t.Fatalf("tool_use: should not close stdin, closed=%d", stdin.closed)
	}
	p.onAssistantMessageStop("end_turn")
	if stdin.closed != 1 {
		t.Fatalf("end_turn: want 1 Close, got %d", stdin.closed)
	}
	p.onAssistantMessageStop("end_turn")
	if stdin.closed != 1 {
		t.Fatalf("duplicate end_turn: idempotent, want still 1 Close, got %d", stdin.closed)
	}
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

type toolResultFrame struct {
	ToolUseID string
	Content   string
	IsError   bool
}

type controlResponseFrame struct {
	RequestID    string
	Behavior     string
	Message      string
	Interrupt    bool
	ToolUseID    string
	UpdatedInput map[string]any
}

func decodeToolResultFrame(t *testing.T, raw []byte) toolResultFrame {
	t.Helper()
	var decoded struct {
		Message struct {
			Content []struct {
				Type      string `json:"type"`
				ToolUseID string `json:"tool_use_id"`
				Content   string `json:"content"`
				IsError   bool   `json:"is_error"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode tool_result frame: %v (raw=%q)", err, raw)
	}
	if n := len(decoded.Message.Content); n != 1 {
		t.Fatalf("frame content length: want 1, got %d (raw=%q)", n, raw)
	}
	c := decoded.Message.Content[0]
	if c.Type != "tool_result" {
		t.Fatalf("frame content[0].type: want tool_result, got %q", c.Type)
	}
	return toolResultFrame{ToolUseID: c.ToolUseID, Content: c.Content, IsError: c.IsError}
}

func decodeControlResponseFrame(t *testing.T, raw []byte) controlResponseFrame {
	t.Helper()
	var decoded struct {
		Type     string `json:"type"`
		Response struct {
			Subtype   string `json:"subtype"`
			RequestID string `json:"request_id"`
			Response  struct {
				Behavior     string         `json:"behavior"`
				Message      string         `json:"message"`
				Interrupt    bool           `json:"interrupt"`
				ToolUseID    string         `json:"toolUseID"`
				UpdatedInput map[string]any `json:"updatedInput"`
			} `json:"response"`
		} `json:"response"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode control_response frame: %v (raw=%q)", err, raw)
	}
	if decoded.Type != "control_response" {
		t.Fatalf("type: want control_response, got %q", decoded.Type)
	}
	if decoded.Response.Subtype != "success" {
		t.Fatalf("subtype: want success, got %q", decoded.Response.Subtype)
	}
	return controlResponseFrame{
		RequestID:    decoded.Response.RequestID,
		Behavior:     decoded.Response.Response.Behavior,
		Message:      decoded.Response.Response.Message,
		Interrupt:    decoded.Response.Response.Interrupt,
		ToolUseID:    decoded.Response.Response.ToolUseID,
		UpdatedInput: decoded.Response.Response.UpdatedInput,
	}
}
