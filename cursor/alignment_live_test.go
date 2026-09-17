//go:build cursor_live

package cursor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/capability"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/memory"
	"github.com/agent-dance/agent-adaptor/profile"
	"github.com/agent-dance/agent-adaptor/tool"
)

const cursorLiveBuild = true

func alignmentCursorLiveConfig(t *testing.T) Config {
	t.Helper()
	live, _ := cursorLiveGate(t)
	if !live {
		t.Skip("live provider calls disabled: require cursor_live and AGENT_ADAPTOR_LIVE_CONFORMANCE=1")
	}
	return cursorIsolatedConfig(t, true)
}
func alignmentCursorNonce(t *testing.T) string {
	t.Helper()
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	return "cursor_alignment_" + hex.EncodeToString(b[:])
}
func alignmentCursorLiveResponse(t *testing.T, r driver.Response, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("live Driver error (%T); retain provider diagnostics only in runner-approved evidence", err)
	}
	if r.ExitCode != 0 || r.Signal != "" || r.TimedOut || r.Failure != nil || r.Checkpoint == nil || !r.Checkpoint.Valid || r.RawStreams == nil || r.RawStreams.Stdout == "" || r.RawStreams.Terminal == nil || len(r.Transcript) == 0 {
		t.Fatalf("live print contract failed: exit=%d signal=%q timeout=%t failure=%t checkpoint=%t", r.ExitCode, r.Signal, r.TimedOut, r.Failure != nil, r.Checkpoint != nil)
	}
}

// TestAlignmentLiveCursorPrintResume exercises the real existing print protocol
// in both resolved Request branches and resumes a provider-created checkpoint.
func TestAlignmentLiveCursorPrintResume(t *testing.T) {
	cfg := alignmentCursorLiveConfig(t)
	d := Driver(cfg)
	nonce := alignmentCursorNonce(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	req := driver.Request{Prompt: "Remember this token for our conversation: " + nonce + ". Reply with only that token. Do not use tools.", Workspace: driver.WorkspaceLease{CWD: cfg.CWD}, Policy: driver.RunPolicy{HumanDecision: driver.HumanDecisionPolicy{Permission: driver.HumanDecisionAutoApprove}}}
	r, err := d.Run(ctx, req, &alignmentCursorSink{})
	alignmentCursorLiveResponse(t, r, err)
	if !strings.Contains(r.Output, nonce) {
		t.Fatal("provider did not return first-turn nonce")
	}
	req.Streaming = true
	req.Prompt = "Reply only with the token I asked you to remember in the prior turn. Do not use tools."
	req.Session = &driver.SessionContext{Mode: driver.SessionContinueOnly, State: r.Checkpoint.State}
	r, err = d.Run(ctx, req, &alignmentCursorSink{})
	alignmentCursorLiveResponse(t, r, err)
	if !strings.Contains(r.Output, nonce) {
		t.Fatal("provider did not retain resumed conversation")
	}
}

// TestAlignmentLiveCursorCapabilities requires real MCP and custom-agent
// evidence; lack of either is a failed supported probe, never a passing skip.
func TestAlignmentLiveCursorCapabilities(t *testing.T) {
	cfg := alignmentCursorLiveConfig(t)
	nonce := alignmentCursorNonce(t)
	var calls atomic.Int32
	echo := tool.Define("alignment_probe", "Return a fixed harmless test token.", func(context.Context, struct{}) (map[string]string, error) {
		calls.Add(1)
		return map[string]string{"token": nonce}, nil
	}, tool.ReadOnly(), tool.Idempotent(), tool.Revision("cursor-alignment/v1"))
	observer := &alignmentCursorObserver{}
	a := adaptor.New(Driver(cfg), adaptor.WithWorkspace(cfg.CWD), adaptor.WithRunServices(observer), adaptor.WithTools(echo), adaptor.WithProfileResources(profile.Resources{Agents: []profile.SubAgent{{Key: "alignment/planner", RuntimeName: "alignment-planner", Description: "Harmless live conformance helper", Instructions: "Reply with exactly PLANNER_CONFIRMED. Do not call tools."}}}), adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAutoApprove}}))
	defer a.Close(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	r, err := a.Run(ctx, "Call the alignment_probe MCP tool once. Also invoke the custom alignment-planner subagent once, wait for it, and then return the tool token and the subagent reply. Use the actual tools, not descriptions.")
	if err != nil {
		t.Fatalf("live capability run failed (%T)", err)
	}
	if calls.Load() == 0 || !strings.Contains(r.Text, nonce) {
		t.Fatal("live hosted tool was not observed by the host")
	}
	mcp, agent := false, false
	for _, v := range observer.facts {
		if v.Phase != capability.Completed || v.Evidence != capability.ProviderProtocol {
			continue
		}
		if v.Ref.Kind == capability.MCP && v.Ref.Operation == "alignment_probe" {
			mcp = true
		}
		if v.Ref.Kind == capability.Subagent && v.Ref.Key == "alignment/planner" {
			agent = true
		}
	}
	if !mcp || !agent {
		t.Fatalf("required formal evidence missing: MCP=%t subagent=%t", mcp, agent)
	}
}

func TestAlignmentLiveCursorUnsupportedAppend(t *testing.T) {
	cfg := alignmentCursorLiveConfig(t)
	d := Driver(cfg)
	_, err := d.Run(context.Background(), driver.Request{AppendSystemPrompt: alignmentCursorNonce(t)}, &alignmentCursorSink{})
	var unsupported *driver.SystemPromptUnsupportedError
	if !errors.As(err, &unsupported) || unsupported.Reason != "unsupported_driver" {
		t.Fatalf("append boundary=%v", err)
	}
	support := d.Descriptor().Observation
	if support.Batch.Skills || support.Streaming.Skills || support.Batch.Todos || support.Streaming.Todos || d.Descriptor().Process.Persistent {
		t.Fatal("unsupported Cursor surface was advertised")
	}
}

func TestAlignmentLiveCursorCancelPartial(t *testing.T) {
	cfg := alignmentCursorLiveConfig(t)
	// A new private workspace has no trust decision. These are explicit fixture
	// prerequisites, not a production permission-policy override.
	cfg.ExtraArgs = append(cfg.ExtraArgs, "--trust", "--stream-partial-output")
	a := adaptor.New(Driver(cfg), adaptor.WithWorkspace(cfg.CWD), adaptor.WithThreadStore(memory.NewStore()))
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := a.Close(ctx); err != nil {
			t.Errorf("bounded Close failed (%T)", err)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	th := a.Thread("cursor-live-cancel")
	if _, err := th.Run(ctx, "Remember this word: arithmetic. Reply only READY. Do not call tools."); err != nil {
		reason := "unobserved"
		textBytes, stdoutBytes, stderrBytes, transcriptItems := -1, -1, -1, -1
		resultPresent, terminalPresent := false, false
		var warmup *adaptor.RunError
		runErrorObserved := errors.As(err, &warmup) && warmup != nil
		if runErrorObserved {
			reason = "other"
			switch warmup.Reason {
			case adaptor.ReasonApprovalDenied, adaptor.ReasonApprovalTimeout, adaptor.ReasonAgentError, adaptor.ReasonCancelled, adaptor.ReasonPolicyViolation, adaptor.ReasonInfrastructure, adaptor.ReasonDeadlineExceeded, adaptor.ReasonActiveExecutionTimeout:
				reason = string(warmup.Reason)
			}
			if partial := warmup.Result; partial != nil {
				resultPresent = true
				raw := partial.Raw()
				textBytes, stdoutBytes, stderrBytes = len(partial.Text), len(raw.Stdout), len(raw.Stderr)
				transcriptItems, terminalPresent = len(partial.Transcript()), raw.Terminal != nil
			}
		}
		// Raw availability means the public audit is accessible; absent observations
		// stay -1/false. Never print provider text, error messages or credentials.
		t.Fatalf("healthy warmup failed (%T): run_error_observed=%t reason=%s cancelled=%t deadline=%t result_present=%t raw_available=%t text_bytes=%d stdout_bytes=%d stderr_bytes=%d transcript_items=%d terminal_observed=%t terminal_present=%t", err, runErrorObserved, reason, errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded), resultPresent, resultPresent, textBytes, stdoutBytes, stderrBytes, transcriptItems, resultPresent, terminalPresent)
	}
	healthy, err := th.Checkpoint(ctx)
	if err != nil {
		t.Fatal("healthy checkpoint missing")
	}
	stream := th.Stream(ctx, "Write a detailed numbered explanation of 200 elementary arithmetic facts. Do not call tools.")
	cancelled := false
	partial := ""
	started := time.Now()
	for event := range stream.Events() {
		if notice, ok := event.(adaptor.Notice); ok && notice.Meta().RunID == stream.RunID() && notice.Kind == adaptor.NoticeTranscriptItem && notice.Item != nil && notice.Item.Kind == driver.TranscriptAssistant && notice.Item.Text != "" && !cancelled {
			partial = notice.Item.Text
			cancelled = true
			started = time.Now()
			stream.Cancel()
		}
	}
	r, err := stream.Result()
	var failure *adaptor.RunError
	if !cancelled || r != nil || !errors.Is(err, context.Canceled) || !errors.As(err, &failure) || failure.Reason != adaptor.ReasonCancelled || failure.Result == nil {
		t.Fatalf("live cancel/partial contract failed: cancelled=%t errorType=%T", cancelled, err)
	}
	if time.Since(started) > 15*time.Second {
		t.Fatal("cancellation teardown exceeded 15 seconds")
	}
	raw := failure.Result.Raw()
	if raw.Stdout == "" || raw.Terminal != nil {
		t.Fatal("cancel must preserve partial stdout before a successful terminal")
	}
	observed := false
	for _, item := range failure.Result.Transcript() {
		observed = observed || item.Kind == driver.TranscriptAssistant && item.Text == partial
	}
	if !observed || !strings.Contains(failure.Result.Text, partial) {
		t.Fatal("cancel lost the actually received assistant fragment")
	}
	after, err := th.Checkpoint(ctx)
	if err != nil || !reflect.DeepEqual(healthy, after) {
		t.Fatal("cancel replaced the previous healthy checkpoint")
	}
}

// An untrusted private workspace must fail before model output. This controls
// the prerequisite independently from cancellation and never accepts an
// arbitrary RunError as proof that cancellation occurred.
func TestAlignmentLiveCursorWorkspaceTrustRequired(t *testing.T) {
	cfg := alignmentCursorLiveConfig(t)
	cfg.ExtraArgs = nil
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	r, err := Driver(cfg).Run(ctx, driver.Request{Prompt: "Reply READY. Do not use tools.", Workspace: driver.WorkspaceLease{CWD: cfg.CWD}}, &alignmentCursorSink{})
	if err != nil || r.ExitCode != 1 || r.Checkpoint != nil || r.RawStreams == nil || r.RawStreams.Stdout != "" || !strings.Contains(r.RawStreams.Stderr, "Workspace Trust Required") {
		t.Fatalf("untrusted workspace control failed: exit=%d errorType=%T", r.ExitCode, err)
	}
}
