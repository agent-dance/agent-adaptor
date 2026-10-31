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
	"github.com/agent-dance/agent-adaptor/internal/clihelper"
	"github.com/agent-dance/agent-adaptor/internal/testutil"
	"github.com/agent-dance/agent-adaptor/opencode"
)

func TestLiveOpenCodeSavedPermissionGuard(t *testing.T) {
	if os.Getenv("AGENT_ADAPTOR_LIVE_CONFORMANCE") != "1" || os.Getenv("AGENT_ADAPTOR_OPENCODE_LIVE") != "1" {
		t.Skip("both live gates are required")
	}
	model := os.Getenv("AGENT_ADAPTOR_OPENCODE_MODEL")
	if model == "" {
		t.Fatal("an explicit live model is required")
	}
	dataHome, workspace := t.TempDir(), t.TempDir()
	storage := filepath.Join(dataHome, "opencode", "storage")
	for name, contents := range map[string]string{
		"project":    `{"worktree":"/","sandboxes":[]}`,
		"permission": `[{"permission":"*","pattern":"*","action":"allow"}]`,
	} {
		dir := filepath.Join(storage, name)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "global.json"), []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// The native startup migration creates the fixture's database and grant row;
	// the SDK only uses the official read-only query command afterward.
	bindings := []driver.EnvBinding{{Name: "XDG_DATA_HOME", Value: dataHome}, {Name: "OPENCODE_DB", Value: filepath.Join(dataHome, "opencode", "opencode.db")}}
	command := os.Getenv("AGENT_ADAPTOR_OPENCODE_COMMAND")
	if command == "" {
		command = "opencode"
	}
	d := opencode.Driver(opencode.Config{Command: command, Model: model, Pure: true, CWD: workspace, Env: bindings})
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for _, mode := range []driver.HumanDecisionMode{driver.HumanDecisionAsk, driver.HumanDecisionAutoReject} {
		req := driver.Request{Prompt: "Create denied.txt with TEST using a write tool."}
		req.Policy.HumanDecision.Permission = mode
		response, err := d.Run(ctx, req, &testutil.EventRecorder{})
		if !errors.Is(err, errors.ErrUnsupported) || !strings.Contains(err.Error(), "saved native project permissions") || response.Checkpoint != nil {
			t.Fatalf("saved grant guard: %v", err)
		}
		if response.RawStreams == nil || !strings.Contains(response.RawStreams.Stdout, "allow") || response.RawStreams.Terminal != nil {
			t.Fatal("native query audit or pre-prompt failure missing")
		}
		if _, err := os.Stat(filepath.Join(workspace, "denied.txt")); !os.IsNotExist(err) {
			t.Fatal("saved grant bypassed SDK policy")
		}
	}
	check, err := clihelper.Run(ctx, clihelper.CommandRequest{Command: command, Args: []string{"db", "SELECT data FROM permission WHERE project_id = 'global'", "--format", "json"}, CWD: workspace, Env: bindings}, &testutil.EventRecorder{})
	if err != nil || check.ExitCode != 0 || !strings.Contains(check.RawStreams.Stdout, "allow") {
		t.Fatal("native saved grant was changed")
	}
}

func TestLiveOpenCodeApprovals(t *testing.T) {
	if os.Getenv("AGENT_ADAPTOR_LIVE_CONFORMANCE") != "1" || os.Getenv("AGENT_ADAPTOR_OPENCODE_LIVE") != "1" {
		t.Skip("both live gates are required")
	}
	model := os.Getenv("AGENT_ADAPTOR_OPENCODE_MODEL")
	if model == "" {
		t.Fatal("an explicit live model is required")
	}
	for _, name := range []string{"permission", "plan", "question", "deny", "native-schema", "schema-with-approval"} {
		t.Run(name, func(t *testing.T) {
			workspace := t.TempDir()
			if err := os.WriteFile(filepath.Join(workspace, "marker.txt"), []byte("OPEN_READ_81"), 0600); err != nil {
				t.Fatal(err)
			}
			cfg := opencode.Config{Command: os.Getenv("AGENT_ADAPTOR_OPENCODE_COMMAND"), Model: model, Pure: true}
			if name == "plan" {
				cfg.Agent = "plan"
			}
			a := adaptor.New(opencode.Driver(cfg), adaptor.WithWorkspace(workspace))
			defer a.Close(context.Background())
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			var seen atomic.Int32
			policy := adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAutoApprove}
			prompt, want := "", ""
			type answer struct {
				Word string `json:"word"`
			}
			var opts []adaptor.CallOption
			switch name {
			case "plan":
				policy.PlanReview = adaptor.ApprovalAsk
				prompt = "Write a minimal plan to report PLAN_OK, then call plan_exit to request approval to execute it. After approval report PLAN_OK."
				want = "PLAN_OK"
			case "permission":
				policy.Permission = adaptor.ApprovalAsk
				prompt = "Read marker.txt with the read tool and reply with its exact content."
				want = "OPEN_READ_81"
			case "question":
				policy.Question = adaptor.QuestionAsk
				prompt = "Use the question tool to ask the host for a secret word, then reply with that word only."
				want = "VIOLET_27"
			case "deny":
				policy.Permission = adaptor.ApprovalAsk
				prompt = "Create denied.txt with content TEST using a file writing tool."
			case "native-schema":
				prompt = "Set the word field to the exact string SCHEMA_OK."
				opts = append(opts, adaptor.WithSchema[answer]())
			case "schema-with-approval":
				policy.Permission = adaptor.ApprovalAsk
				prompt = "Read marker.txt with the read tool. Return its exact content as the word field."
				opts = append(opts, adaptor.WithSchema[answer](adaptor.SchemaInlineReferences()))
			}
			respond := func(ctx context.Context, r *adaptor.ApprovalRequest) error {
				seen.Add(1)
				if name == "plan" && r.Kind != adaptor.ApprovalPlanReview {
					t.Errorf("kind=%s", r.Kind)
				}
				if name == "question" {
					if r.Kind != adaptor.ApprovalQuestion {
						t.Errorf("kind=%s", r.Kind)
					}
					return r.Answer(ctx, want)
				}
				if name == "deny" {
					return r.Deny(ctx, "test denial")
				}
				return r.Approve(ctx)
			}
			opts = append(opts, adaptor.WithPolicy(adaptor.Policy{Approvals: policy}))
			if name != "question" {
				opts = append(opts, adaptor.OnApproval(respond))
			}
			s := a.Stream(ctx, prompt, opts...)
			for event := range s.Events() {
				if r, ok := event.(*adaptor.ApprovalRequest); ok && name == "question" {
					if err := respond(ctx, r); err != nil {
						t.Fatal(err)
					}
				}
			}
			result, err := s.Result()
			if name == "deny" {
				var failure *adaptor.RunError
				if !errors.As(err, &failure) || failure.Reason != adaptor.ReasonApprovalDenied {
					t.Fatalf("deny=%v", err)
				}
				if _, err := os.Stat(filepath.Join(workspace, "denied.txt")); !os.IsNotExist(err) {
					t.Fatal("denied write executed")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(name, "schema") {
					var decoded answer
					if err := result.Decode(&decoded); err != nil {
						t.Fatal(err)
					}
					word := "SCHEMA_OK"
					if name == "schema-with-approval" {
						word = "OPEN_READ_81"
					}
					if decoded.Word != word {
						t.Fatalf("decoded=%+v terminal=%s", decoded, result.Raw().Terminal.JSON)
					}
				} else if !strings.Contains(result.Text, want) {
					t.Fatalf("text=%q", result.Text)
				}
				if result.Raw().Terminal == nil || result.Metadata["opencode.event_stream"] == "" {
					t.Fatal("server audit missing")
				}
			}
			if name != "native-schema" && seen.Load() == 0 {
				t.Fatal("no actual approval request")
			}
		})
	}
}
