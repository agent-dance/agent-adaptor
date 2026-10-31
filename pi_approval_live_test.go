package adaptor_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/pi"
)

func TestLivePiApprovals(t *testing.T) {
	if os.Getenv("AGENT_ADAPTOR_LIVE_CONFORMANCE") != "1" || os.Getenv("AGENT_ADAPTOR_PI_LIVE") != "1" {
		t.Skip("both live gates are required")
	}
	model := os.Getenv("AGENT_ADAPTOR_PI_MODEL")
	if model == "" {
		t.Fatal("an explicit live model is required")
	}
	for _, name := range []string{"permission", "plan", "question", "deny"} {
		t.Run(name, func(t *testing.T) {
			workspace := t.TempDir()
			if err := os.WriteFile(filepath.Join(workspace, "marker.txt"), []byte("LIVE_READ_81"), 0600); err != nil {
				t.Fatal(err)
			}
			a := adaptor.New(pi.Driver(pi.Config{Command: os.Getenv("AGENT_ADAPTOR_PI_COMMAND"), Provider: os.Getenv("AGENT_ADAPTOR_PI_PROVIDER"), Model: model, Thinking: "low", NoExtensions: true, NoSkills: true, Tools: []string{"read", "write"}}), adaptor.WithWorkspace(workspace))
			defer a.Close(context.Background())
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			var seen atomic.Int32
			policy := adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAutoApprove, PlanReview: adaptor.ApprovalAutoApprove}
			prompt, want := "", ""
			switch name {
			case "permission":
				policy.Permission = adaptor.ApprovalAsk
				prompt = "Read marker.txt with the read tool and reply with its exact content."
				want = "LIVE_READ_81"
			case "plan":
				policy.PlanReview = adaptor.ApprovalAsk
				prompt = "Call adaptor_review_plan with the plan 'Report the test result'. After approval reply PLAN_APPROVED_54."
				want = "PLAN_APPROVED_54"
			case "question":
				policy.Question = adaptor.QuestionAsk
				prompt = "Call adaptor_ask_user to ask the host for the secret word, then reply with the secret word only."
				want = "BLUE_27"
			case "deny":
				policy.Permission = adaptor.ApprovalAsk
				prompt = "Use the write tool now to create denied.txt with content TEST."
			}
			respond := func(ctx context.Context, req *adaptor.ApprovalRequest) error {
				seen.Add(1)
				if name == "question" {
					if req.Kind != adaptor.ApprovalQuestion {
						t.Errorf("kind=%s", req.Kind)
					}
					return req.Answer(ctx, want)
				}
				if name == "plan" && req.Kind != adaptor.ApprovalPlanReview {
					t.Errorf("kind=%s", req.Kind)
				}
				if name == "deny" {
					return req.Deny(ctx, "forbidden test write")
				}
				return req.Approve(ctx)
			}
			opts := []adaptor.CallOption{adaptor.WithPolicy(adaptor.Policy{Approvals: policy})}
			if name != "question" {
				opts = append(opts, adaptor.OnApproval(respond))
			}
			s := a.Stream(ctx, prompt, opts...)
			var deltas strings.Builder
			for event := range s.Events() {
				if req, ok := event.(*adaptor.ApprovalRequest); ok {
					if err := respond(ctx, req); err != nil {
						t.Fatal(err)
					}
				}
				if call, ok := event.(adaptor.ToolCall); ok {
					deltas.WriteString(call.ArgsDelta)
				}
			}
			result, err := s.Result()
			if name == "deny" {
				if !errors.Is(err, adaptor.ErrApprovalDenied) {
					t.Fatalf("denied error=%v", err)
				}
				if _, err := os.Stat(filepath.Join(workspace, "denied.txt")); !os.IsNotExist(err) {
					t.Fatal("denied write executed")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				assertLiveResult(t, result, want)
			}
			if seen.Load() == 0 {
				t.Fatal("no real approval request observed")
			}
			if deltas.Len() == 0 {
				t.Fatal("no tool argument deltas observed")
			}
		})
	}
}
