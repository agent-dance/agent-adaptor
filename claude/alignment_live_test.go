//go:build claude_live

package claude_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/capability"
	"github.com/agent-dance/agent-adaptor/claude"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/memory"
	"github.com/agent-dance/agent-adaptor/profile"
	"github.com/agent-dance/agent-adaptor/tool"
)

// Live runs require explicit runner credentials; no settings/auth are copied
// from the operator profile. Missing required tools/protocol fail the test.
func alignmentLiveConfig(t *testing.T, model string) claude.Config {
	t.Helper()
	home := t.TempDir()
	workspace := t.TempDir()
	return claude.Config{CommonConfig: claude.CommonConfig{Command: claudeCLIName(), CWD: workspace, Env: []driver.EnvBinding{{Name: "HOME", Value: home}, {Name: "USERPROFILE", Value: home}, {Name: "CLAUDE_CONFIG_DIR", Value: filepath.Join(home, ".claude")}, {Name: "CLAUDE_CODE_ENABLE_TODO_TOOLS", Value: "1"}}}, Model: model}
}
func alignmentLiveGate(t *testing.T) {
	t.Helper()
	requireInteractiveCLI(t)
	cfg := alignmentLiveConfig(t, "")
	cmd := exec.Command(cfg.Command, "--version")
	cmd.Env = os.Environ()
	for _, binding := range cfg.Env {
		cmd.Env = append(cmd.Env, binding.Name+"="+binding.Value)
	}
	version, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("CLI version: %v", err)
	}
	t.Logf("CLI version: %s; private profile/workspace; provider credentials from runner environment", strings.TrimSpace(string(version)))
}
func alignmentLiveNonce(t *testing.T) string {
	t.Helper()
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b[:])
}
func alignmentLivePolicy() adaptor.Policy {
	return adaptor.Policy{Approvals: adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAutoApprove, PlanReview: adaptor.ApprovalAutoApprove, Question: adaptor.QuestionAutoDeny}}
}

func TestAlignmentLiveAppendAndThread(t *testing.T) {
	alignmentLiveGate(t)
	cfg := alignmentLiveConfig(t, envOr("CLAUDE_MODEL_P3", "claude-haiku-4-5"))
	nonce := alignmentLiveNonce(t)
	a := adaptor.New(claude.Driver(cfg), adaptor.WithThreadStore(memory.NewStore()), adaptor.WithPolicy(alignmentLivePolicy()), adaptor.WithAppendSystemPrompt("When asked for the audit token, return exactly "+nonce+" and no other text."))
	defer a.Close(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	control, err := a.Run(ctx, "What is the audit token? If unknown say unknown.", adaptor.WithAppendSystemPrompt(""))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(control.Text, nonce) {
		t.Fatal("nonce control invalid")
	}
	th := a.Thread("live-append")
	for _, opts := range [][]adaptor.CallOption{nil, nil, {adaptor.WithSpawn()}} {
		result, err := th.Run(ctx, "What is the audit token?", opts...)
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(result.Text) != nonce || result.Raw().Terminal == nil || len(result.Transcript()) == 0 {
			t.Fatal("native append/resume output not observed")
		}
	}
	if _, err := th.Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestAlignmentLiveNativeSchemaHITL(t *testing.T) {
	alignmentLiveGate(t)
	cfg := alignmentLiveConfig(t, envOr("CLAUDE_MODEL_P3", "claude-haiku-4-5"))
	a := adaptor.New(claude.Driver(cfg))
	defer a.Close(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	for _, kind := range []adaptor.ApprovalKind{adaptor.ApprovalQuestion, adaptor.ApprovalPlanReview} {
		count := 0
		prompt := "Use AskUserQuestion to ask which directory (docs or src) to use, then return {\"directory\":\"docs\"}."
		if kind == adaptor.ApprovalPlanReview {
			prompt = "Enter plan mode, propose a one-step documentation plan, call ExitPlanMode for approval, then return {\"directory\":\"docs\"}."
		}
		result, err := a.Run(ctx, prompt, adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAutoApprove, PlanReview: adaptor.ApprovalAsk, Question: adaptor.QuestionAsk}}), adaptor.WithSchemaJSON([]byte(`{"type":"object","properties":{"directory":{"type":"string"}},"required":["directory"],"additionalProperties":false}`)), adaptor.OnApproval(func(ctx context.Context, request *adaptor.ApprovalRequest) error {
			if request.Kind == kind {
				count++
			}
			if request.Kind == adaptor.ApprovalQuestion {
				return request.Answer(ctx, "docs")
			}
			return request.Approve(ctx)
		}))
		if err != nil {
			t.Fatal(err)
		}
		var output map[string]string
		if err := result.Decode(&output); err != nil || output["directory"] != "docs" || count == 0 {
			t.Fatalf("schema/HITL missing: approvals=%d decode=%v", count, err)
		}
		var terminal map[string]any
		if result.Raw().Terminal == nil || json.Unmarshal(result.Raw().Terminal.JSON, &terminal) != nil || terminal["structured_output"] == nil {
			t.Fatal("native structured output missing")
		}
	}
}

func TestAlignmentLiveToolsTodosAndNestedParent(t *testing.T) {
	alignmentLiveGate(t)
	cfg := alignmentLiveConfig(t, envOr("CLAUDE_MODEL_P3", "claude-haiku-4-5"))
	echo := tool.Define("alignment_echo", "Return the supplied text", func(_ context.Context, in struct {
		Text string `json:"text"`
	}) (struct {
		Text string `json:"text"`
	}, error) {
		return in, nil
	}, tool.ReadOnly())
	a := adaptor.New(claude.Driver(cfg), adaptor.WithPolicy(alignmentLivePolicy()), adaptor.WithTools(echo), adaptor.WithProfile(profile.Dedicated(t.TempDir())))
	defer a.Close(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	st := a.Stream(ctx, "Call alignment_echo with text hello. Create a task using TaskCreate with subject alignment probe, update that exact task to completed with TaskUpdate, and read the full list with TaskList. Also launch an Agent subagent to use Read on a local file and report its result. Perform these tool calls, then reply done.")
	var mcp, confirmed, nested bool
	for event := range st.Events() {
		switch e := event.(type) {
		case adaptor.CapabilityInvocation:
			if e.Invocation.Ref.Kind == capability.MCP && e.Invocation.Phase == capability.Completed {
				mcp = true
			}
		case adaptor.TodoUpdated:
			for _, item := range e.Snapshot.Items {
				if item.Content == "alignment probe" && !item.SyntheticID {
					confirmed = true
				}
			}
		case adaptor.ToolCall:
			if e.ParentToolCallID != "" && e.ScopeID != "" {
				nested = true
			}
		}
	}
	if _, err := st.Result(); err != nil {
		t.Fatal(err)
	}
	if !mcp || !confirmed || !nested {
		t.Fatalf("required provider facts missing: mcp=%t todo=%t nested=%t", mcp, confirmed, nested)
	}
}

// Preserve earlier live scenarios under T27's TestAlignmentLive selector.
func TestAlignmentLiveExistingStreamingAndApproval(t *testing.T) {
	alignmentLiveGate(t)
	t.Run("stream", TestClaudeStreamingHaiku)
	t.Run("permission", TestClaudeInteractive_PermissionAsk)
}

func TestAlignmentLiveDedicatedToolResumeAfterClose(t *testing.T) {
	alignmentLiveGate(t)
	cfg := alignmentLiveConfig(t, envOr("CLAUDE_MODEL_P3", "claude-haiku-4-5"))
	store := memory.NewStore()
	dir := t.TempDir()
	nonce := alignmentLiveNonce(t)
	echo := tool.Define("alignment_echo", "Return text", func(_ context.Context, in struct {
		Text string `json:"text"`
	}) (struct {
		Text string `json:"text"`
	}, error) { return in, nil }, tool.ReadOnly())
	makeAgent := func() *adaptor.Agent {
		return adaptor.New(claude.Driver(cfg), adaptor.WithThreadStore(store), adaptor.WithProfile(profile.Dedicated(dir)), adaptor.WithTools(echo), adaptor.WithPolicy(alignmentLivePolicy()))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	first := makeAgent()
	_, err := first.Thread("durable-live").Run(ctx, "Call alignment_echo with text "+nonce+". Remember that value as the durable token; reply saved.")
	if err != nil {
		_ = first.Close(context.Background())
		t.Fatal(err)
	}
	if err := first.Close(ctx); err != nil {
		t.Fatal(err)
	}
	second := makeAgent()
	defer second.Close(context.Background())
	result, err := second.Thread("durable-live", adaptor.ResumeOnly()).Run(ctx, "What durable token did I give you in the previous turn? Reply only with that value.")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(result.Text) != nonce {
		t.Fatal("dedicated hosted-tool conversation did not survive Agent.Close")
	}
}

func TestAlignmentLiveCancellationPreservesPartialResult(t *testing.T) {
	alignmentLiveGate(t)
	cfg := alignmentLiveConfig(t, envOr("CLAUDE_MODEL_P3", "claude-haiku-4-5"))
	a := adaptor.New(claude.Driver(cfg), adaptor.WithThreadStore(memory.NewStore()), adaptor.WithPolicy(alignmentLivePolicy()))
	defer a.Close(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	th := a.Thread("cancel-live")
	if _, err := th.Run(ctx, "Reply ready"); err != nil {
		t.Fatal(err)
	}
	before, err := th.Checkpoint(ctx)
	if err != nil {
		t.Fatal(err)
	}
	stream := th.Stream(ctx, "Write a long detailed explanation of how parsers work, at least 3000 words.")
	cancelled := false
	for event := range stream.Events() {
		if _, ok := event.(adaptor.TextDelta); ok && !cancelled {
			cancelled = true
			stream.Cancel()
		}
	}
	_, err = stream.Result()
	var runErr *adaptor.RunError
	if !cancelled || !errors.Is(err, context.Canceled) || !errors.As(err, &runErr) || runErr.Result == nil || runErr.Result.Raw().Stdout == "" {
		t.Fatalf("cancel/partial evidence missing: %v", err)
	}
	after, err := th.Checkpoint(ctx)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("cancel replaced healthy checkpoint")
	}
}
