package codebuddy

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/clihelper"
)

const controlInitializeRequestID = "agent-adaptor-initialize"

type controlState struct {
	ctx         context.Context
	sink        driver.DecisionCapableSink
	stdin       clihelper.StdinController
	runID       string
	policy      driver.HumanDecisionPolicy
	prompt      string
	configDir   string
	userStarted bool
	plan        string
}

func (p *parser) enableControl(ctx context.Context, sink driver.DecisionCapableSink, stdin clihelper.StdinController, runID string, policy driver.HumanDecisionPolicy, prompt string) {
	p.control = &controlState{ctx: ctx, sink: sink, stdin: stdin, runID: runID, policy: policy, prompt: prompt}
}

func (p *parser) enablePersistentControl(ctx context.Context, sink driver.DecisionCapableSink, runID string, policy driver.HumanDecisionPolicy, prompt, configDir string) {
	p.control = &controlState{ctx: ctx, sink: sink, runID: runID, policy: policy, prompt: prompt, configDir: configDir}
}

func (p *parser) handleControlPayload(payload map[string]any) bool {
	if p.control == nil {
		return false
	}
	switch topString(payload, "type") {
	case "control_response":
		response := topObject(payload, "response")
		if topString(response, "request_id", "requestId") == controlInitializeRequestID && !p.control.userStarted {
			p.control.userStarted = true
			_ = p.control.stdin.Write(encodeControlUser(p.control.prompt))
		}
		return true
	case "control_request":
		p.handleControlRequest(payload)
		return true
	default:
		return false
	}
}

func (p *parser) handleControlRequest(payload map[string]any) {
	requestID := topString(payload, "request_id", "requestId")
	request := topObject(payload, "request")
	if requestID == "" || request == nil || strings.ToLower(topString(request, "subtype")) != "can_use_tool" {
		return
	}
	toolName := topString(request, "tool_name", "toolName", "name")
	input := cloneMap(topObject(request, "input"))
	toolUseID := topString(request, "tool_use_id", "toolUseId", "id")
	kind := controlDecisionKind(toolName)
	decision := p.control.sinkDecision(p, requestID, toolUseID, toolName, input, kind)
	_ = p.control.stdin.Write(encodeControlResponse(requestID, toolUseID, input, decision, p.control.policy))
}

func (s *controlState) sinkDecision(p *parser, requestID, toolUseID, toolName string, input map[string]any, kind driver.HumanDecisionKind) driver.DecisionResponse {
	req := driver.DecisionRequest{
		RequestID:  requestID,
		RunID:      s.runID,
		ThreadID:   p.sessionID,
		Kind:       kind,
		Source:     controlSource(kind),
		ToolCallID: toolUseID,
		Prompt:     toolName,
		Payload:    map[string]any{"tool": toolName, "input": input},
		CreatedAt:  time.Now().UTC(),
	}
	if kind == driver.HumanDecisionPlanReview {
		req.Prompt = "ExitPlanMode"
		req.Payload["plan"] = s.plan
	}
	if kind == driver.HumanDecisionQuestion {
		req.Prompt = questionPrompt(input)
		req.Choices = questionChoices(input)
	}
	resp, err := s.sink.RequestDecision(s.ctx, req)
	if err != nil && resp.Result == "" {
		// The SDK may already classify a timeout or abort in resp. Preserve
		// that classification; only an unclassified transport error becomes
		// an aborted decision.
		resp = driver.DecisionResponse{RequestID: requestID, Result: driver.DecisionAborted, Text: err.Error()}
	}
	if resp.RequestID == "" {
		resp.RequestID = requestID
	}
	p.recordControlReject(req, resp, s.policy)
	return resp
}

func (p *parser) captureControlPlan(toolName string, input map[string]any) {
	if p.control == nil || !strings.EqualFold(toolName, "Write") {
		return
	}
	path, _ := input["file_path"].(string)
	content, _ := input["content"].(string)
	if content == "" || !isCodeBuddyPlanFile(path, p.control.configDir) {
		return
	}
	p.control.plan = content
}

// isCodeBuddyPlanFile 判断某次 Write 是否写入 CodeBuddy 的计划文件。计划位于
// <configDir>/plans/*.md；configDir 可被 CODEBUDDY_CONFIG_DIR 覆盖，因此已知时按
// 该前缀匹配，未知时回退到默认的 ~/.codebuddy/plans/ 布局。
func isCodeBuddyPlanFile(path, configDir string) bool {
	clean := filepath.ToSlash(path)
	if !strings.HasSuffix(strings.ToLower(clean), ".md") {
		return false
	}
	if dir := strings.TrimSpace(configDir); dir != "" {
		prefix := strings.TrimSuffix(filepath.ToSlash(dir), "/") + "/plans/"
		if strings.Contains(clean, prefix) {
			return true
		}
	}
	return strings.Contains(clean, ".codebuddy/plans/")
}

func controlDecisionKind(toolName string) driver.HumanDecisionKind {
	switch toolName {
	case "AskUserQuestion":
		return driver.HumanDecisionQuestion
	case "ExitPlanMode":
		return driver.HumanDecisionPlanReview
	default:
		return driver.HumanDecisionPermission
	}
}

func controlSource(kind driver.HumanDecisionKind) string {
	switch kind {
	case driver.HumanDecisionQuestion:
		return "AskUserQuestion"
	case driver.HumanDecisionPlanReview:
		return "ExitPlanMode"
	default:
		return "permission"
	}
}

func encodeControlUser(prompt string) []byte {
	frame, _ := json.Marshal(map[string]any{
		"type":       "user",
		"session_id": "",
		"message": map[string]any{
			"role":    "user",
			"content": prompt,
		},
		"parent_tool_use_id": nil,
	})
	return append(frame, '\n')
}

func encodeControlResponse(requestID, toolUseID string, input map[string]any, decision driver.DecisionResponse,
	policy driver.HumanDecisionPolicy) []byte {
	allowed := decision.Result == driver.DecisionApproved || decision.Result == driver.DecisionAnswered
	response := map[string]any{"allowed": allowed, "tool_use_id": toolUseID}
	if allowed {
		updated := cloneMap(input)
		if decision.Result == driver.DecisionAnswered {
			updated["answers"] = questionAnswers(input, decision)
			if decision.Choice != "" {
				updated["choice"] = decision.Choice
			}
		}
		response["updatedInput"] = updated
	} else {
		response["reason"] = decision.Text
		response["interrupt"] = controlDenyInterrupts(decision.Result, policy)
	}
	frame, _ := json.Marshal(map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "success",
			"request_id": requestID,
			"response":   response,
		},
	})
	return append(frame, '\n')
}

// controlDenyInterrupts reports whether a denied control decision should carry
// interrupt=true, i.e. abort the CLI run. Rejected/Aborted honor OnReject and
// TimedOut honors OnTimeout; both default (Unset) to Abort. Approved/Answered
// never reach this path. 与 claude 的 interrupt 门控保持一致。
func controlDenyInterrupts(result driver.DecisionResult, policy driver.HumanDecisionPolicy) bool {
	effective := driver.EffectiveHumanDecisionPolicy(policy)
	switch result {
	case driver.DecisionTimedOut:
		return effective.OnTimeout == driver.FailureAbort
	case driver.DecisionRejected, driver.DecisionAborted:
		return effective.OnReject == driver.FailureAbort
	default:
		return false
	}
}

func (p *parser) recordControlReject(req driver.DecisionRequest, resp driver.DecisionResponse, policy driver.HumanDecisionPolicy) {
	if resp.Result != driver.DecisionRejected || p.pendingFailure != nil {
		return
	}
	effective := driver.EffectiveHumanDecisionPolicy(policy)
	if effective.OnReject != driver.FailureAbort && effective.OnReject != driver.FailureActionUnset {
		return
	}
	snapshot := req
	p.pendingFailure = &driver.RunFailure{
		Code:    driver.FailureReject,
		Message: "CodeBuddy control request was rejected.",
		HumanDecision: &driver.HumanDecisionFailure{
			Kind: req.Kind, Source: req.Source, Decision: driver.DecisionRejected, Request: &snapshot, Attempts: 1,
		},
	}
}

func questionPrompt(input map[string]any) string {
	questions, _ := input["questions"].([]any)
	if len(questions) == 0 {
		return "AskUserQuestion"
	}
	question, _ := questions[0].(map[string]any)
	return topString(question, "question", "header")
}

// questionAnswers 将宿主的显式 Answer 原样写入 CodeBuddy 控制响应。
// 未提供 Answer 时，使用 SDK 的 Text 或 Choice 回填首题。
func questionAnswers(input map[string]any, decision driver.DecisionResponse) map[string]any {
	if len(decision.Answer) > 0 {
		return cloneMap(decision.Answer)
	}
	answer := strings.TrimSpace(decision.Text)
	if answer == "" {
		answer = strings.TrimSpace(decision.Choice)
	}
	question := firstQuestionText(input)
	if answer == "" || question == "" {
		return nil
	}
	return map[string]any{question: answer}
}

func firstQuestionText(input map[string]any) string {
	questions, _ := input["questions"].([]any)
	if len(questions) == 0 {
		return ""
	}
	question, _ := questions[0].(map[string]any)
	return topString(question, "question")
}

func questionChoices(input map[string]any) []driver.DecisionChoice {
	questions, _ := input["questions"].([]any)
	if len(questions) == 0 {
		return nil
	}
	question, _ := questions[0].(map[string]any)
	options, _ := question["options"].([]any)
	choices := make([]driver.DecisionChoice, 0, len(options))
	for _, option := range options {
		entry, _ := option.(map[string]any)
		label := topString(entry, "label")
		choices = append(choices, driver.DecisionChoice{Key: label, Label: label, Description: topString(entry, "description")})
	}
	return choices
}
