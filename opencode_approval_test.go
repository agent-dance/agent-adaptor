package adaptor_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/testutil"
	"github.com/agent-dance/agent-adaptor/opencode"
)

func TestOpenCodeApprovalPipeline(t *testing.T) {
	for _, tc := range []struct {
		name, kind, reply string
		policy            adaptor.ApprovalPolicy
		failure           adaptor.FailureReason
	}{
		{"permission-approve", "permission", "approve", adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAsk}, ""},
		{"permission-deny", "permission", "deny", adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAsk}, adaptor.ReasonApprovalDenied},
		{"permission-continue", "permission", "deny", adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAsk, OnReject: adaptor.FallbackContinue}, ""},
		{"permission-auto-deny", "permission", "", adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAutoDeny}, adaptor.ReasonApprovalDenied},
		{"plan-approve", "plan", "approve", adaptor.ApprovalPolicy{PlanReview: adaptor.ApprovalAsk}, ""},
		{"plan-auto-deny", "plan", "", adaptor.ApprovalPolicy{PlanReview: adaptor.ApprovalAutoDeny}, adaptor.ReasonApprovalDenied},
		{"question-answer", "question", "BLUE", adaptor.ApprovalPolicy{Question: adaptor.QuestionAsk}, ""},
		{"question-auto-deny", "question", "", adaptor.ApprovalPolicy{Question: adaptor.QuestionAutoDeny}, adaptor.ReasonApprovalDenied},
		{"timeout", "permission", "", adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAsk, Timeout: 20 * time.Millisecond}, adaptor.ReasonApprovalTimeout},
		{"timeout-continue", "permission", "", adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAsk, Timeout: 20 * time.Millisecond, OnTimeout: adaptor.FallbackContinue}, ""},
		{"cancel", "permission", "cancel", adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAsk}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, callback := range []bool{false, true} {
				if callback && (tc.reply == "" || tc.reply == "cancel") {
					continue
				}
				t.Run(map[bool]string{false: "event", true: "callback"}[callback], func(t *testing.T) {
					raw, err := os.ReadFile(filepath.Join("opencode", "testdata", "server.jsonl"))
					if err != nil {
						t.Fatal(err)
					}
					toolName := "write"
					if tc.kind == "plan" {
						toolName = "plan_exit"
					}
					if tc.kind == "question" {
						toolName = "question"
					}
					encode := func(v any) string {
						b, err := json.Marshal(v)
						if err != nil {
							t.Fatal(err)
						}
						return string(b) + "\n"
					}
					part := map[string]any{"id": "tool", "sessionID": "ses_child", "messageID": "message", "type": "tool", "tool": toolName, "callID": "call", "state": map[string]any{"status": "running", "input": map[string]any{"path": "target"}}}
					start := encode(map[string]any{"type": "message.part.updated", "properties": map[string]any{"sessionID": "ses_child", "part": part}})
					props := map[string]any{"id": "request", "sessionID": "ses_child", "tool": map[string]any{"callID": "call", "messageID": "message"}}
					eventType := "question.asked"
					if tc.kind == "permission" {
						eventType = "permission.asked"
						props["permission"] = "edit"
						props["patterns"] = []string{"target"}
						props["metadata"] = map[string]any{"filePath": "target"}
						props["always"] = []string{"*"}
					} else {
						props["questions"] = []any{map[string]any{"question": "Choose", "header": "Choice", "options": []any{map[string]any{"label": "Yes", "description": "accept"}, map[string]any{"label": "No", "description": "reject"}}}}
					}
					decision := encode(map[string]any{"type": eventType, "properties": props})
					part["state"] = map[string]any{"status": "completed", "input": map[string]any{"path": "target"}, "output": "done", "metadata": map[string]any{}, "attachments": []any{map[string]any{"type": "file", "url": "test://attachment"}}}
					end := encode(map[string]any{"type": "message.part.updated", "properties": map[string]any{"sessionID": "ses_child", "part": part}})
					marker := `{"type":"message.part.updated","properties":{"sessionID":"ses_child","part":{"id":"start"`
					fixture := strings.Replace(string(raw), marker, start+decision+end+marker, 1)
					command, capture := testutil.ProtocolCommand(t, fixture, "audit", 0)
					workspace := t.TempDir()
					a := adaptor.New(opencode.Driver(opencode.Config{Command: command, Env: []driver.EnvBinding{{Name: "OPENCODE_CONFIG_DIR", Value: t.TempDir()}}}), adaptor.WithWorkspace(workspace))
					defer a.Close(context.Background())
					ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
					defer cancel()
					respond := func(ctx context.Context, r *adaptor.ApprovalRequest) error {
						wantKind := adaptor.ApprovalPermission
						if tc.kind == "plan" {
							wantKind = adaptor.ApprovalPlanReview
						}
						if tc.kind == "question" {
							wantKind = adaptor.ApprovalQuestion
						}
						if r.Kind != wantKind {
							t.Errorf("kind=%s want=%s", r.Kind, wantKind)
						}
						switch tc.reply {
						case "approve":
							return r.Approve(ctx)
						case "deny":
							return r.Deny(ctx, "test denial")
						case "BLUE":
							return r.Answer(ctx, "BLUE")
						case "cancel":
							cancel()
						}
						return nil
					}
					opts := []adaptor.CallOption{adaptor.WithPolicy(adaptor.Policy{Approvals: tc.policy})}
					if callback {
						opts = append(opts, adaptor.OnApproval(respond))
					}
					s := a.Stream(ctx, "test", opts...)
					for e := range s.Events() {
						if request, ok := e.(*adaptor.ApprovalRequest); ok && !callback {
							if err := respond(ctx, request); err != nil {
								t.Fatal(err)
							}
						}
					}
					result, err := s.Result()
					if tc.reply == "cancel" {
						if !errors.Is(err, context.Canceled) {
							t.Fatalf("cancel=%v", err)
						}
					} else if tc.failure != "" {
						var failed *adaptor.RunError
						if !errors.As(err, &failed) || failed.Reason != tc.failure || failed.Result == nil || failed.Result.Raw().Stdout == "" {
							t.Fatalf("err=%v", err)
						}
					} else if err != nil || result == nil {
						t.Fatalf("result=%+v err=%v", result, err)
					}
					_, statErr := os.Stat(filepath.Join(workspace, "executed"))
					if (statErr == nil) != (tc.kind == "permission" && tc.reply == "approve") {
						t.Fatalf("execution marker err=%v", statErr)
					}
					if tc.reply == "BLUE" || tc.kind == "plan" && tc.reply == "approve" {
						reply, _ := os.ReadFile(filepath.Join(filepath.Dir(capture), "reply.json"))
						want := "BLUE"
						if tc.kind == "plan" {
							want = "Yes"
						}
						if !strings.Contains(string(reply), want) {
							t.Fatalf("reply=%s", reply)
						}
					}
					if tc.policy.Permission == adaptor.ApprovalAsk || tc.policy.Permission == adaptor.ApprovalAutoDeny {
						rules, _ := os.ReadFile(filepath.Join(filepath.Dir(capture), "session-permission.json"))
						if !strings.Contains(string(rules), `"action":"deny"`) || !strings.Contains(string(rules), `"action":"ask"`) {
							t.Fatalf("native deny rule lost: %s", rules)
						}
					}
				})
			}
		})
	}
}
