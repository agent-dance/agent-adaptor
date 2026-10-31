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
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/opencode"
	"github.com/agent-dance/agent-adaptor/profile"
)

func TestLiveOpenCodeDelegatedApprovals(t *testing.T) {
	if os.Getenv("AGENT_ADAPTOR_LIVE_CONFORMANCE") != "1" || os.Getenv("AGENT_ADAPTOR_OPENCODE_LIVE") != "1" {
		t.Skip("both live gates required")
	}
	model := os.Getenv("AGENT_ADAPTOR_OPENCODE_MODEL")
	if model == "" {
		t.Fatal("an explicit live model is required")
	}
	for _, mode := range []string{"read", "question", "deny", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			workspace := t.TempDir()
			if err := os.WriteFile(filepath.Join(workspace, "marker.txt"), []byte("CHILD_READ_736"), 0600); err != nil {
				t.Fatal(err)
			}
			spec := profile.SubAgent{Key: "worker", Description: "Performs the requested single tool operation and reports its result.", Instructions: "Perform only the requested operation using your tools. Do not delegate. Report the exact result. If denied, stop and report the denial.", ToolPolicy: &profile.ToolPolicy{Allow: []string{"read", "edit", "bash", "question"}}}
			a := adaptor.New(opencode.Driver(opencode.Config{Command: os.Getenv("AGENT_ADAPTOR_OPENCODE_COMMAND"), Model: model, Pure: true}), adaptor.WithWorkspace(workspace), adaptor.WithProfileResources(profile.Resources{Agents: []profile.SubAgent{spec}}))
			defer a.Close(context.Background())
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			var children atomic.Int32
			policy := adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAsk}
			operation, want := "Read marker.txt with the read tool and return its exact contents.", "CHILD_READ_736"
			if mode == "question" {
				policy.Question = adaptor.QuestionAsk
				operation = "Use the question tool to ask the host for the secret word, then return the word exactly."
				want = "CHILD_ANSWER_285"
			}
			if mode == "deny" || mode == "cancel" {
				operation = "Create denied.txt containing TEST using apply_patch. Do not read any files first."
			}
			respond := func(ctx context.Context, r *adaptor.ApprovalRequest) error {
				child := strings.HasPrefix(r.ToolCallID, "opencode-child:")
				if child {
					children.Add(1)
					switch mode {
					case "deny":
						return r.Deny(ctx, "child write denied")
					case "cancel":
						cancel()
						return nil
					case "question":
						if r.Kind == adaptor.ApprovalQuestion {
							return r.Answer(ctx, want)
						}
					}
				}
				return r.Approve(ctx)
			}
			opts := []adaptor.CallOption{adaptor.WithPolicy(adaptor.Policy{Approvals: policy})}
			if mode == "read" || mode == "deny" {
				opts = append(opts, adaptor.OnApproval(respond))
			}
			s := a.Stream(ctx, "Use the task tool to delegate to subagent_type worker with this request: "+operation+" You must not perform the requested operation yourself. Report only the subagent's result.", opts...)
			for event := range s.Events() {
				if r, ok := event.(*adaptor.ApprovalRequest); ok && mode != "read" && mode != "deny" {
					if err := respond(ctx, r); err != nil {
						t.Fatal(err)
					}
				}
			}
			result, err := s.Result()
			if children.Load() == 0 {
				t.Fatalf("no delegated approval observed: %v", err)
			}
			switch mode {
			case "deny":
				var failed *adaptor.RunError
				if !errors.As(err, &failed) || failed.Reason != adaptor.ReasonApprovalDenied {
					t.Fatalf("child denial: %v", err)
				}
			case "cancel":
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("child cancellation: %v", err)
				}
			default:
				if err != nil {
					t.Fatal(err)
				}
				assertLiveResult(t, result, want)
				observed := false
				for _, item := range result.Transcript() {
					if item.Kind == driver.TranscriptToolResult && item.SessionID != "" && item.ToolName != "task" && strings.Contains(item.Text, want) {
						observed = true
					}
				}
				if !observed {
					t.Fatal("child tool audit missing")
				}
			}
			if _, err := os.Stat(filepath.Join(workspace, "denied.txt")); !os.IsNotExist(err) {
				t.Fatal("denied/cancelled child write executed")
			}
		})
	}
}
