package adaptor_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/testutil"
	"github.com/agent-dance/agent-adaptor/pi"
)

// A hermetic CLI exercises the actual private extension transport and root
// approval state machine. The execution marker is written only after approval.
func TestPiApprovalProcess(t *testing.T) {
	marker := os.Getenv("ADAPTOR_PI_APPROVAL_MARKER")
	if marker == "" {
		return
	}
	_, _ = io.ReadAll(os.Stdin)
	toolName := os.Getenv("ADAPTOR_PI_APPROVAL_TOOL")
	var endpoint, token string
	for _, binding := range os.Environ() {
		key, value, _ := strings.Cut(binding, "=")
		if strings.HasPrefix(key, "AGENT_ADAPTOR_PI_MCP_URL_") {
			endpoint = value
		}
		if strings.HasPrefix(key, "AGENT_ADAPTOR_PI_MCP_TOKEN_") {
			token = value
		}
	}
	write := func(v any) { _ = json.NewEncoder(os.Stdout).Encode(v) }
	write(map[string]any{"type": "session", "id": "approval-session"})
	write(map[string]any{"type": "agent_start"})
	write(map[string]any{"type": "tool_execution_start", "toolCallId": "call-1", "toolName": toolName, "args": map[string]any{}})
	body, _ := json.Marshal(map[string]any{"toolCallId": "call-1", "toolName": toolName, "input": map[string]any{"plan": "Review the plan", "question": "Which value?", "options": []string{"A", "B"}}})
	request, _ := http.NewRequest(http.MethodPost, endpoint+"/decision", strings.NewReader(string(body)))
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		os.Exit(3)
	}
	var decision struct{ Result, Text string }
	if err := json.NewDecoder(response.Body).Decode(&decision); err != nil {
		os.Exit(4)
	}
	response.Body.Close()
	text := "DENIED"
	if decision.Result == "approved" || decision.Result == "answered" {
		_ = os.WriteFile(marker, []byte(decision.Text), 0600)
		text = "APPROVED " + decision.Text
	}
	write(map[string]any{"type": "tool_execution_end", "toolCallId": "call-1", "toolName": toolName, "result": map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}}, "isError": text == "DENIED"})
	write(map[string]any{"type": "message_start", "message": map[string]any{"role": "assistant", "content": []any{}}})
	message := map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": text}}, "stopReason": "stop"}
	write(map[string]any{"type": "message_end", "message": message})
	write(map[string]any{"type": "agent_end", "messages": []any{message}})
	write(map[string]any{"type": "agent_settled"})
	os.Exit(0)
}

func TestPiApprovalPipeline(t *testing.T) {
	for _, tc := range []struct {
		name, tool string
		policy     adaptor.ApprovalPolicy
		reply      string
		failure    adaptor.FailureReason
	}{
		{"permission-approve", "write", adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAsk}, "approve", ""},
		{"permission-deny-abort", "write", adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAsk}, "deny", adaptor.ReasonApprovalDenied},
		{"permission-deny-continue", "write", adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAsk, OnReject: adaptor.FallbackContinue}, "deny", ""},
		{"permission-auto-deny", "write", adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAutoDeny}, "", adaptor.ReasonApprovalDenied},
		{"plan-approve", "adaptor_review_plan", adaptor.ApprovalPolicy{PlanReview: adaptor.ApprovalAsk}, "approve", ""},
		{"plan-auto-deny", "adaptor_review_plan", adaptor.ApprovalPolicy{PlanReview: adaptor.ApprovalAutoDeny}, "", adaptor.ReasonApprovalDenied},
		{"question-answer", "adaptor_ask_user", adaptor.ApprovalPolicy{Question: adaptor.QuestionAsk}, "B", ""},
		{"question-auto-deny", "adaptor_ask_user", adaptor.ApprovalPolicy{Question: adaptor.QuestionAutoDeny}, "", adaptor.ReasonApprovalDenied},
		{"permission-timeout-abort", "write", adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAsk, Timeout: 20 * time.Millisecond}, "", adaptor.ReasonApprovalTimeout},
		{"permission-timeout-continue", "write", adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAsk, Timeout: 20 * time.Millisecond, OnTimeout: adaptor.FallbackContinue}, "", ""},
		{"permission-cancel", "write", adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAsk}, "cancel", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, callback := range []bool{false, true} {
				if callback && (tc.reply == "" || tc.reply == "cancel") {
					continue
				}
				t.Run(map[bool]string{false: "event", true: "callback"}[callback], func(t *testing.T) {
					marker := filepath.Join(t.TempDir(), "executed")
					t.Setenv("ADAPTOR_PI_APPROVAL_MARKER", marker)
					t.Setenv("ADAPTOR_PI_APPROVAL_TOOL", tc.tool)
					executable, err := os.Executable()
					if err != nil {
						t.Fatal(err)
					}
					quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
					command := testutil.WriteCommand(t, t.TempDir(), "pi-approval", "#!/bin/sh\nexec "+quote(executable)+" -test.run=^TestPiApprovalProcess$\n", "@echo off\r\n\""+executable+"\" -test.run=TestPiApprovalProcess\r\n")
					a := adaptor.New(pi.Driver(pi.Config{NoExtensions: true, Command: command, Env: []driver.EnvBinding{{Name: "PI_CODING_AGENT_DIR", Value: t.TempDir()}}}))
					defer a.Close(context.Background())
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					respond := func(ctx context.Context, req *adaptor.ApprovalRequest) error {
						switch tc.reply {
						case "approve":
							return req.Approve(ctx)
						case "deny":
							return req.Deny(ctx, "test rejected")
						case "B":
							return req.Answer(ctx, "B")
						case "cancel":
							cancel()
						}
						return nil
					}
					opts := []adaptor.CallOption{adaptor.WithPolicy(adaptor.Policy{Approvals: tc.policy})}
					if callback {
						opts = append(opts, adaptor.OnApproval(respond))
					}
					stream := a.Stream(ctx, "test", opts...)
					for event := range stream.Events() {
						if request, ok := event.(*adaptor.ApprovalRequest); ok && !callback {
							if err := respond(ctx, request); err != nil {
								t.Fatal(err)
							}
						}
					}
					result, err := stream.Result()
					if tc.reply == "cancel" {
						if !errors.Is(err, context.Canceled) {
							t.Fatalf("cancel=%v", err)
						}
					} else if tc.failure != "" {
						var runError *adaptor.RunError
						if !errors.As(err, &runError) || runError.Reason != tc.failure || runError.Result == nil || runError.Result.Raw().Stdout == "" {
							t.Fatalf("error=%+v", err)
						}
					} else if err != nil || result == nil {
						t.Fatalf("result=%+v error=%v", result, err)
					}
					_, statErr := os.Stat(marker)
					wantExecuted := tc.reply == "approve" || tc.reply == "B"
					if (statErr == nil) != wantExecuted {
						t.Fatalf("tool executed=%v, want %v", statErr == nil, wantExecuted)
					}
				})
			}
		})
	}
}
