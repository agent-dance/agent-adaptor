package codebuddy

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
)

func TestAlignmentCodeBuddyQuestionOverridesHeadlessApprovals(t *testing.T) {
	for _, tc := range []struct {
		name     string
		question driver.QuestionMode
		want     bool
	}{
		{"ask", driver.QuestionAsk, true},
		{"reject", driver.QuestionAutoReject, true},
		{"unset", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := driver.HumanDecisionPolicy{
				Permission: driver.HumanDecisionAutoApprove,
				PlanReview: driver.HumanDecisionAutoApprove,
				Question:   tc.question,
			}
			if got := wantsControlTransport(policy); got != tc.want {
				t.Fatalf("Question %s with automatic permission/plan approval: control=%v, want %v", tc.question, got, tc.want)
			}
		})
	}
}

func TestAlignmentCodeBuddyQuestionWithAutomaticPermissions(t *testing.T) {
	fx := newPersistentCodeBuddyFixture(t, []driver.EnvBinding{{Name: "ALIGNMENT_CODEBUDDY_QUESTION", Value: "1"}})
	defer fx.close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var calls int
	result, err := fx.agent.Run(ctx, "ask my preferred language",
		adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalPolicy{
			Permission: adaptor.ApprovalAutoApprove, PlanReview: adaptor.ApprovalAutoApprove, Question: adaptor.QuestionAsk,
		}}), adaptor.OnApproval(func(ctx context.Context, request *adaptor.ApprovalRequest) error {
			if request.Kind != adaptor.ApprovalQuestion {
				return request.Deny(ctx, "unexpected kind")
			}
			calls++
			return request.Answer(ctx, "Go")
		}))
	if err != nil || calls != 1 {
		t.Fatalf("Question calls=%d, want 1; err=%v", calls, err)
	}
	if result.Text != "ANSWER=Go" || !strings.Contains(result.Raw().Stdout, `"tool_name":"AskUserQuestion"`) {
		t.Fatalf("formal Question/Answer round trip missing: text=%q", result.Text)
	}
}

func alignmentQuestionControlTurn(writer *bufio.Writer, scanner *bufio.Scanner) int {
	_, _ = fmt.Fprintln(writer, `{"type":"control_request","request_id":"question-1","request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion","tool_use_id":"question-tool","input":{"questions":[{"question":"Preferred language?","header":"Language","options":[{"label":"Go","description":"Go"},{"label":"Rust","description":"Rust"}],"multiSelect":false}]}}}`)
	_ = writer.Flush()
	if !scanner.Scan() {
		return 81
	}
	var frame struct {
		Response struct {
			RequestID string `json:"request_id"`
			Response  struct {
				Allowed      bool `json:"allowed"`
				UpdatedInput struct {
					Answers map[string]string `json:"answers"`
				} `json:"updatedInput"`
			} `json:"response"`
		} `json:"response"`
	}
	if json.Unmarshal(scanner.Bytes(), &frame) != nil || frame.Response.RequestID != "question-1" || !frame.Response.Response.Allowed || frame.Response.Response.UpdatedInput.Answers["Preferred language?"] != "Go" {
		return 82
	}
	_, _ = fmt.Fprintln(writer, `{"type":"result","subtype":"success","is_error":false,"session_id":"question-session","result":"ANSWER=Go"}`)
	_ = writer.Flush()
	return 0
}
