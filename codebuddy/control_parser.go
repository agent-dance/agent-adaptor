package codebuddy

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/internal/clihelper"
)

const controlInitializeRequestID = "agent-adaptor-initialize"

type controlState struct {
	ctx         context.Context
	sink        agentadaptor.DecisionCapableSink
	stdin       clihelper.StdinController
	runID       string
	policy      agentadaptor.HumanDecisionPolicy
	prompt      string
	userStarted bool
	plan        string
}

func (p *parser) enableControl(ctx context.Context, sink agentadaptor.DecisionCapableSink, stdin clihelper.StdinController, runID string, policy agentadaptor.HumanDecisionPolicy, prompt string) {
	p.control = &controlState{ctx: ctx, sink: sink, stdin: stdin, runID: runID, policy: policy, prompt: prompt}
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
	_ = p.control.stdin.Write(encodeControlResponse(requestID, toolUseID, input, decision))
}

func (s *controlState) sinkDecision(p *parser, requestID, toolUseID, toolName string, input map[string]any, kind agentadaptor.HumanDecisionKind) agentadaptor.DecisionResponse {
	req := agentadaptor.DecisionRequest{
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
	if kind == agentadaptor.HumanDecisionPlanReview {
		req.Prompt = "ExitPlanMode"
		req.Payload["plan"] = s.plan
	}
	if kind == agentadaptor.HumanDecisionQuestion {
		req.Prompt = questionPrompt(input)
		req.Choices = questionChoices(input)
	}
	resp, err := s.sink.RequestDecision(s.ctx, req)
	if err != nil && resp.Result == "" {
		// The SDK may already classify a timeout or abort in resp. Preserve
		// that classification; only an unclassified transport error becomes
		// an aborted decision.
		resp = agentadaptor.DecisionResponse{RequestID: requestID, Result: agentadaptor.DecisionAborted, Text: err.Error()}
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
	if content == "" || !isCodeBuddyPlanFile(path) {
		return
	}
	p.control.plan = content
}

func isCodeBuddyPlanFile(path string) bool {
	clean := filepath.ToSlash(path)
	return strings.Contains(clean, ".codebuddy/plans/") && strings.HasSuffix(strings.ToLower(clean), ".md")
}

func controlDecisionKind(toolName string) agentadaptor.HumanDecisionKind {
	switch toolName {
	case "AskUserQuestion":
		return agentadaptor.HumanDecisionQuestion
	case "ExitPlanMode":
		return agentadaptor.HumanDecisionPlanReview
	default:
		return agentadaptor.HumanDecisionPermission
	}
}

func controlSource(kind agentadaptor.HumanDecisionKind) string {
	switch kind {
	case agentadaptor.HumanDecisionQuestion:
		return "AskUserQuestion"
	case agentadaptor.HumanDecisionPlanReview:
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

func encodeControlResponse(requestID, toolUseID string, input map[string]any, decision agentadaptor.DecisionResponse) []byte {
	allowed := decision.Result == agentadaptor.DecisionApproved || decision.Result == agentadaptor.DecisionAnswered
	response := map[string]any{"allowed": allowed, "tool_use_id": toolUseID}
	if allowed {
		updated := cloneMap(input)
		if decision.Result == agentadaptor.DecisionAnswered {
			updated["answers"] = decision.Answer
			if decision.Choice != "" {
				updated["choice"] = decision.Choice
			}
		}
		response["updatedInput"] = updated
	} else {
		response["reason"] = decision.Text
		response["interrupt"] = true
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

func (p *parser) recordControlReject(req agentadaptor.DecisionRequest, resp agentadaptor.DecisionResponse, policy agentadaptor.HumanDecisionPolicy) {
	if resp.Result != agentadaptor.DecisionRejected || p.pendingFailure != nil {
		return
	}
	effective := agentadaptor.EffectiveHumanDecisionPolicy(policy)
	if effective.OnReject != agentadaptor.FailureAbort && effective.OnReject != agentadaptor.FailureActionUnset {
		return
	}
	snapshot := req
	p.pendingFailure = &agentadaptor.RunFailure{
		Code:    agentadaptor.FailureReject,
		Message: "CodeBuddy control request was rejected.",
		HumanDecision: &agentadaptor.HumanDecisionFailure{
			Kind: req.Kind, Source: req.Source, Decision: agentadaptor.DecisionRejected, Request: &snapshot, Attempts: 1,
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

func questionChoices(input map[string]any) []agentadaptor.DecisionChoice {
	questions, _ := input["questions"].([]any)
	if len(questions) == 0 {
		return nil
	}
	question, _ := questions[0].(map[string]any)
	options, _ := question["options"].([]any)
	choices := make([]agentadaptor.DecisionChoice, 0, len(options))
	for _, option := range options {
		entry, _ := option.(map[string]any)
		label := topString(entry, "label")
		choices = append(choices, agentadaptor.DecisionChoice{Key: label, Label: label, Description: topString(entry, "description")})
	}
	return choices
}
