//go:build codex_live

package codex

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/capability"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/testutil"
	"github.com/agent-dance/agent-adaptor/memory"
	"github.com/agent-dance/agent-adaptor/profile"
	"github.com/agent-dance/agent-adaptor/skill"
	"github.com/agent-dance/agent-adaptor/tool"
)

func alignmentLiveGate(t *testing.T) Config {
	t.Helper()
	if os.Getenv("AGENT_ADAPTOR_LIVE_CONFORMANCE") != "1" {
		t.Skip("requires codex_live and AGENT_ADAPTOR_LIVE_CONFORMANCE=1")
	}
	return alignmentLiveConfig(t)
}
func alignmentNonce(t *testing.T) string {
	t.Helper()
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b[:])
}

// Both actual transports, with a nonce never sent through user input. No fake
// argv/RPC capture can satisfy these provider-response checks.
func TestAlignmentLiveNativeAppend(t *testing.T) {
	cfg := alignmentLiveGate(t)
	nonce := alignmentNonce(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	prompt := "Reply only with the required response marker from your developer instructions, or NONE if absent."
	for _, streaming := range []bool{false, true} {
		t.Run(map[bool]string{false: "exec", true: "appserver"}[streaming], func(t *testing.T) {
			req := driver.Request{Config: cfg, Prompt: prompt, AppendSystemPrompt: "The required response marker is " + nonce + ". When asked for the marker reply with it exactly.", Streaming: streaming, Policy: driver.RunPolicy{HumanDecision: driver.HumanDecisionPolicy{Permission: driver.HumanDecisionAutoApprove}}}
			result, err := (adapter{}).Run(ctx, req, &testutil.EventRecorder{})
			if err != nil || result.Failure != nil || strings.TrimSpace(result.Output) != nonce || result.RawStreams == nil || result.RawStreams.Terminal == nil || result.Checkpoint == nil {
				t.Fatal("native append response/checkpoint evidence missing")
			}
			req.Session = &driver.SessionContext{Mode: driver.SessionContinueOnly, State: result.Checkpoint.State}
			if next, err := (adapter{}).Run(ctx, req, &testutil.EventRecorder{}); err != nil || strings.TrimSpace(next.Output) != nonce {
				t.Fatal("native append resume evidence missing")
			}
			if streaming {
				req.Session.Mode = driver.SessionFork
				if branch, err := (adapter{}).Run(ctx, req, &testutil.EventRecorder{}); err != nil || strings.TrimSpace(branch.Output) != nonce || branch.Checkpoint == nil || branch.Checkpoint.State.ResumeID == result.Checkpoint.State.ResumeID {
					t.Fatal("native append fork evidence missing")
				}
			}
			req.AppendSystemPrompt = ""
			req.Session = nil
			if control, err := (adapter{}).Run(ctx, req, &testutil.EventRecorder{}); err != nil || strings.Contains(control.Output, nonce) {
				t.Fatal("unappended control failed")
			}
		})
	}
}

func TestAlignmentLiveObservationAndPersistent(t *testing.T) {
	cfg := alignmentLiveGate(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	nonce := alignmentNonce(t)
	probe := tool.Define("alignment_probe", "Return the alignment marker.", func(context.Context, struct{}) (string, error) { return nonce, nil }, tool.ReadOnly(), tool.Revision("alignment-probe/v1"))
	a := adaptor.New(Driver(cfg), adaptor.WithThreadStore(memory.NewStore()), adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAutoApprove}}), adaptor.WithSkills(skill.Inline("review", "---\nname: review\ndescription: Alignment skill.\n---\nUse the alignment_probe tool when asked.")), adaptor.WithTools(probe))
	defer a.Close(context.Background())
	thread := a.Thread("alignment-live")
	spawns := 0
	for round := 0; round < 2; round++ {
		stream := thread.Stream(ctx, "Use $review. First call update_plan with two steps, then call alignment_probe exactly once, mark the plan completed and return its marker.")
		skills, mcp, plan := false, false, false
		for event := range stream.Events() {
			switch e := event.(type) {
			case adaptor.ProcessInfo:
				if e.Kind == adaptor.ProcessSpawn {
					spawns++
				}
			case adaptor.CapabilityInvocation:
				if e.Invocation.Ref.Kind == capability.Skill && e.Invocation.Evidence == capability.NativeInputAccepted {
					skills = true
				}
				if e.Invocation.Ref.Kind == capability.MCP && e.Invocation.Phase == capability.Completed {
					mcp = true
				}
			case adaptor.TodoUpdated:
				plan = true
			}
		}
		result, err := stream.Result()
		if err != nil || !strings.Contains(result.Text, nonce) || !skills || !mcp || !plan || result.Raw().Terminal == nil || len(result.Transcript()) == 0 {
			t.Fatal("required formal skill/MCP/plan/output evidence missing")
		}
	}
	if spawns != 1 {
		t.Fatalf("persistent turns spawned %d processes", spawns)
	}
}

func TestAlignmentLiveSubagentCatalog(t *testing.T) {
	cfg := alignmentLiveGate(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	nonce := alignmentNonce(t)
	a := adaptor.New(Driver(cfg), adaptor.WithThreadStore(memory.NewStore()), adaptor.WithProfileResources(profile.Resources{Agents: []profile.SubAgent{{
		Key: "alignment/canonical-reviewer", RuntimeName: "alignment_reviewer", Description: "Answer alignment review requests.",
		Instructions: "When given an alignment review request, reply with " + nonce + ".", Model: cfg.Model,
	}}}))
	defer a.Close(context.Background())
	stream := a.Thread("alignment-live-subagent").Stream(ctx, "Spawn an alignment_reviewer agent for an alignment review request. Wait for its answer, then return that answer.")
	completed := false
	for event := range stream.Events() {
		if e, ok := event.(adaptor.CapabilityInvocation); ok && e.Invocation.Ref.Kind == capability.Subagent && e.Invocation.Phase == capability.Completed {
			if e.Invocation.Ref.Key != "alignment/canonical-reviewer" || e.Invocation.ParentToolCallID != "" {
				t.Fatal("subagent identity was not the resolved canonical key")
			}
			completed = true
		}
	}
	result, err := stream.Result()
	if err != nil || !completed || !strings.Contains(result.Text, nonce) || result.Raw().Terminal == nil {
		t.Fatal("required collab and formally associated child-role evidence missing")
	}
}

func TestAlignmentLiveCancellationAndAppendRebind(t *testing.T) {
	cfg := alignmentLiveGate(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	nonce := alignmentNonce(t)
	a := adaptor.New(Driver(cfg), adaptor.WithThreadStore(memory.NewStore()), adaptor.WithAppendSystemPrompt("When asked for the marker reply only with "+nonce+"."))
	defer a.Close(context.Background())
	thread := a.Thread("alignment-live-rebind")
	result, err := thread.Run(ctx, "Return the marker.")
	if err != nil || strings.TrimSpace(result.Text) != nonce {
		t.Fatal("initial developer instructions were not accepted")
	}
	healthy, err := thread.Checkpoint(ctx)
	if err != nil {
		t.Fatal(err)
	}
	runctx, stop := context.WithCancel(ctx)
	stream := thread.Stream(runctx, "Write a long numbered list of 2000 separate ideas, one per line.")
	observed := false
	for event := range stream.Events() {
		if e, ok := event.(adaptor.TextDelta); ok && e.Text != "" {
			observed = true
			stop()
		}
	}
	stop()
	_, err = stream.Result()
	var runErr *adaptor.RunError
	if !observed || !errors.Is(err, context.Canceled) || !errors.As(err, &runErr) || runErr.Result == nil || runErr.Result.Raw().Stdout == "" || len(runErr.Result.Transcript()) == 0 {
		t.Fatal("cancelled output or context cause was lost")
	}
	after, err := thread.Checkpoint(ctx)
	if err != nil || !reflect.DeepEqual(healthy, after) {
		t.Fatal("cancelled turn replaced the healthy checkpoint")
	}
	if _, err := a.Thread("alignment-live-rebind", adaptor.ResumeOnly()).Run(ctx, "Return the marker.", adaptor.WithAppendSystemPrompt("")); err == nil {
		t.Fatal("ResumeOnly accepted changed developer instructions")
	}
	control, err := thread.Run(ctx, "Reply NONE if no developer instruction provides a marker; otherwise return that marker.", adaptor.WithAppendSystemPrompt(""))
	if err != nil || strings.Contains(control.Text, nonce) {
		t.Fatal("append clear did not rebuild the incompatible conversation")
	}
}
