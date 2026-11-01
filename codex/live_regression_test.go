package codex

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"github.com/agent-dance/agent-adaptor/v2/codex/appserver"
	"github.com/agent-dance/agent-adaptor/v2/driver"
	"github.com/agent-dance/agent-adaptor/v2/internal/profileinstructions"
	"github.com/agent-dance/agent-adaptor/v2/internal/testutil"
	"github.com/agent-dance/agent-adaptor/v2/mcp"
)

func TestF2CodexCancelDuringShell(t *testing.T) {
	for _, mode := range []string{"task", "caller", "standby"} {
		t.Run(mode, func(t *testing.T) {
			low, req, _, _ := newPersistentCodexTest(t, "f2_cancel")
			configured := configuredDriver{adapter: low.(adapter), cfg: req.Config.(Config)}
			agent := adaptor.New(configured, adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAutoApprove}}))
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			callerCtx, cancelCaller := context.WithCancel(ctx)
			defer cancelCaller()
			defer agent.Close(context.Background())
			var stream adaptor.Stream
			var standby adaptor.Standby
			var events <-chan adaptor.Event
			if mode == "standby" {
				var err error
				standby, err = agent.Thread("cancel-shell").Standby(ctx)
				if err != nil {
					t.Fatal(err)
				}
				events = standby.Events()
				if err := standby.Send(ctx, "original", adaptor.MessageID("original")); err != nil {
					t.Fatal(err)
				}
			} else {
				// Task exercises the one-shot transport; caller exercises the resident path.
				if mode == "task" {
					stream = agent.Send(ctx, "original", adaptor.MessageID("original"))
				} else {
					stream = agent.Thread("caller").Send(callerCtx, "original", adaptor.MessageID("original"))
				}
				events = stream.Events()
			}
			cancelled := false
			for {
				select {
				case <-ctx.Done():
					t.Fatalf("fixture did not finish: %v", ctx.Err())
				case event, ok := <-events:
					if !ok {
						t.Fatal("events closed before Done")
					}
					if call, ok := event.(adaptor.ToolCall); ok && call.ID == "shell-running" && !cancelled {
						cancelled = true
						switch mode {
						case "task":
							stream.Cancel()
						case "caller":
							cancelCaller()
						case "standby":
							standby.Cancel()
						}
					}
					if done, ok := event.(adaptor.Done); ok {
						var business *adaptor.RunError
						if !cancelled || !errors.Is(done.Err, context.Canceled) || !errors.As(done.Err, &business) || business.Reason != adaptor.ReasonCancelled {
							t.Fatalf("cancelled=%v Done=%T %v", cancelled, done.Err, done.Err)
						}
						if stream != nil {
							result, err := stream.Result()
							if result != done.Result || err != done.Err {
								t.Fatal("Done and Result differ")
							}
						}
						failed := business.Result.Undelivered()
						if len(failed) != 1 || failed[0].Reason != adaptor.MessageCancelled || failed[0].Text != "original" {
							t.Fatalf("message outcome=%+v", failed)
						}
						return
					}
				}
			}
		})
	}
}

func TestF2CodexMCPApprovalRoundTrip(t *testing.T) {
	for _, resident := range []bool{false, true} {
		for _, action := range []string{"accept", "decline"} {
			t.Run(action+map[bool]string{true: "/resident", false: "/one-shot"}[resident], func(t *testing.T) {
				low, req, _, _ := newPersistentCodexTest(t, "f2_mcp_"+action)
				agent := adaptor.New(configuredDriver{adapter: low.(adapter), cfg: req.Config.(Config)}, adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAsk, OnReject: adaptor.FallbackContinue}}))
				defer agent.Close(context.Background())
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				called := make(chan *adaptor.ApprovalRequest, 1)
				handler := adaptor.OnApproval(func(ctx context.Context, approval *adaptor.ApprovalRequest) error {
					called <- approval
					if action == "accept" {
						return approval.Approve(ctx)
					}
					return approval.Deny(ctx, "host declined")
				})
				var stream adaptor.Stream
				if resident {
					stream = agent.Thread("mcp").Send(ctx, "delegate", handler)
				} else {
					stream = agent.Send(ctx, "delegate", handler)
				}
				result, err := stream.Result()
				if err != nil || result.Text != "mcp-action:"+action {
					t.Fatalf("result=%+v err=%v", result, err)
				}
				select {
				case approval := <-called:
					if approval.ID != "mcp-approval" || approval.Kind != adaptor.ApprovalPermission || approval.RunID != stream.RunID() || approval.Title != "Allow team_delegate?" || approval.Source != "codex.app-server.mcpServer/elicitation/request" || approval.Details["serverName"] != "agent_adaptor_tools" || approval.Details["turnId"] != "turn-1" || approval.Details["requestedSchema"] == nil || approval.Details["_meta"] == nil {
						t.Fatalf("approval fields=%+v", approval)
					}
				default:
					t.Fatal("MCP permission never reached host handler")
				}
			})
		}
	}
}

func TestF2CodexStaleNotificationsAcrossTurnsAndExecutions(t *testing.T) {
	for _, resident := range []bool{false, true} {
		t.Run(map[bool]string{true: "resident", false: "one-shot"}[resident], func(t *testing.T) {
			adapter, req, spawns, _ := newPersistentCodexTest(t, "f2_stale")
			defer closeCodexTestAdapter(t, adapter)
			req.Spawn = !resident
			port := newCodexMessageTestPort()
			port.ends = [][]driver.MessageItem{{{ID: "second", Text: "second", Timing: driver.TimingAfterTurn}}}
			req.Messages = port
			recorder := &testutil.EventRecorder{}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			response, err := adapter.Run(ctx, req, recorder)
			if err != nil || response.Failure != nil || response.Checkpoint == nil || response.Output != "reply-2" || response.Usage == nil || response.Usage.InputTokens != 10 {
				t.Fatalf("first execution response=%+v err=%v", response, err)
			}
			var tokens []int
			for _, event := range recorder.Snapshot() {
				if event.Turn != nil {
					tokens = append(tokens, event.Turn.Usage.InputTokens)
				}
			}
			if !reflect.DeepEqual(tokens, []int{5, 5}) {
				t.Fatalf("per-turn usage=%v", tokens)
			}
			req = resumedCodexRequest(req, response)
			req.Messages = nil
			response, err = adapter.Run(ctx, req, &testutil.EventRecorder{})
			if err != nil || response.Checkpoint == nil || response.Failure != nil || response.Usage.InputTokens != 5 {
				t.Fatalf("second execution response=%+v err=%v", response, err)
			}
			if resident && (response.Output != "reply-3" || helperSpawnCount(t, spawns) != 1) {
				t.Fatalf("resident reuse lost: %+v", response)
			}
		})
	}
}

func TestF2CodexMCPInvocationOverrides(t *testing.T) {
	payload := driver.MCPPayload{Servers: []driver.MCPServerSpec{{Key: "agent_adaptor_tools", Transport: driver.MCPTransportHTTP, URL: "http://127.0.0.1/mcp"}, {Key: "host.server", Transport: driver.MCPTransportStdio, Command: "server"}}}
	for _, mode := range []driver.HumanDecisionMode{driver.HumanDecisionAutoApprove, driver.HumanDecisionAsk, driver.HumanDecisionAutoReject} {
		policy := driver.RunPolicy{HumanDecision: driver.HumanDecisionPolicy{Permission: mode}}
		args, err := codexMCPPolicyArgs(payload, policy)
		if err != nil {
			t.Fatal(err)
		}
		req := driver.Request{MCP: payload, Policy: policy}
		opts, err := buildAppServerOptions(req, Config{}, "codex", nil, profileinstructions.Prepared{})
		if err != nil {
			t.Fatal(err)
		}
		for _, arg := range args {
			if !strings.Contains(strings.Join(opts.ExtraArgs, "\n"), arg) {
				t.Fatalf("missing override %q in %v", arg, opts.ExtraArgs)
			}
		}
		if !strings.Contains(strings.Join(args, "\n"), `mcp_servers."host.server".default_tools_approval_mode=`) {
			t.Fatalf("key was not quoted: %v", args)
		}
	}
}

type cancelShellSink struct {
	testutil.EventRecorder
	cancel context.CancelFunc
}

func (s *cancelShellSink) EmitStream(p driver.StreamPayload) error {
	if p.Kind == driver.StreamToolCallStart && p.ToolCallID == "shell-running" {
		s.cancel()
	}
	return s.EventRecorder.EmitStream(p)
}
func TestF2CodexDriverReturnsContextCancellation(t *testing.T) {
	for _, resident := range []bool{false, true} {
		t.Run(map[bool]string{true: "resident", false: "one-shot"}[resident], func(t *testing.T) {
			adapter, req, _, _ := newPersistentCodexTest(t, "f2_cancel")
			defer closeCodexTestAdapter(t, adapter)
			req.Spawn = !resident
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			response, err := adapter.Run(ctx, req, &cancelShellSink{cancel: cancel})
			if !errors.Is(err, context.Canceled) || response.Failure == nil || response.Failure.Code != driver.FailureCancelled || response.Checkpoint != nil || response.RawStreams == nil || !strings.Contains(response.RawStreams.Stdout, "shell-running") {
				t.Fatalf("driver cancellation: err=%v response=%+v", err, response)
			}
		})
	}
}

func TestF2CodexMCPApprovalAbort(t *testing.T) {
	low, req, _, _ := newPersistentCodexTest(t, "f2_mcp_decline")
	agent := adaptor.New(configuredDriver{adapter: low.(adapter), cfg: req.Config.(Config)}, adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAsk}}))
	defer agent.Close(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := agent.Send(ctx, "delegate", adaptor.OnApproval(adaptor.DenyAll("host declined"))).Result()
	if !errors.Is(err, adaptor.ErrApprovalDenied) || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("approval abort: %v", err)
	}
}

func TestF2CodexReadonlyMCPAutoDenyKeepsToolsVisible(t *testing.T) {
	low, req, _, _ := newPersistentCodexTest(t, "f2_policy_deny")
	agent := adaptor.New(configuredDriver{adapter: low.(adapter), cfg: req.Config.(Config)},
		adaptor.WithPolicy(adaptor.Policy{Sandbox: adaptor.ReadOnly, Approvals: adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAutoDeny, OnReject: adaptor.FallbackContinue}}),
		adaptor.WithMCP(mcp.HTTP("agent-adaptor-tools", "http://127.0.0.1:12345/mcp")))
	defer agent.Close(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	called := make(chan struct{}, 1)
	stream := agent.Send(ctx, "delegate", adaptor.OnApproval(func(ctx context.Context, request *adaptor.ApprovalRequest) error {
		called <- struct{}{}
		return request.Deny(ctx, "unexpected human callback")
	}))
	rejected, visible := false, false
	for event := range stream.Events() {
		if notice, ok := event.(adaptor.Notice); ok && notice.Kind == adaptor.NoticeApprovalResolved && notice.Data["result"] == string(driver.DecisionRejected) {
			rejected = true
		}
		if tool, ok := event.(adaptor.ToolResult); ok && tool.ID == "delegate-call" && tool.Result["status"] == "failed" && strings.Contains(fmt.Sprint(tool.Result["error"]), "rejected") {
			visible = true
		}
	}
	result, err := stream.Result()
	if err != nil || result.Text != "delegation rejected" || !rejected || !visible {
		t.Fatalf("denial was not visible: result=%+v err=%v approval=%v tool=%v", result, err, rejected, visible)
	}
	select {
	case <-called:
		t.Fatal("AutoDeny asked a human")
	default:
	}
	transcribed := false
	for _, item := range result.Transcript() {
		if item.Kind == driver.TranscriptToolResult && item.ToolUseID == "delegate-call" && item.IsError {
			transcribed = true
		}
	}
	if !transcribed {
		t.Fatal("rejected tool call missing from transcript")
	}
}

// Never + prompt is a Codex-side rejection before any elicitation. The fixture
// models the provider's failed MCP item and completed assistant response.
func TestF2CodexNeverPromptPublishesRejectedTool(t *testing.T) {
	_, req, _, _ := newPersistentCodexTest(t, "f2_policy_never")
	cfg := req.Config.(Config)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	recorder := &testutil.EventRecorder{}
	response, err := appserver.Run(ctx, appserver.Options{
		Command: cfg.Command, Env: cfg.Env, CWD: cfg.CWD, Prompt: "delegate", Sandbox: "read-only", Approval: "never",
		ExtraArgs: []string{"-c", `mcp_servers."agent-adaptor-tools".default_tools_approval_mode="prompt"`, "-c", `mcp_servers."agent-adaptor-tools".enabled=true`},
	}, recorder)
	if err != nil || response.Failure != nil || response.Output != "delegation rejected" || !hasRejectedDelegation(response.Transcript) {
		t.Fatalf("never+prompt rejection: err=%v response=%+v", err, response)
	}
	visible, ended := false, false
	for _, payload := range recorder.StreamSnapshot() {
		if payload.Kind == driver.StreamToolCallEnd && payload.ToolCallID == "delegate-call" {
			ended = true
		}
		if payload.Kind == driver.StreamToolCallResult && payload.ToolCallID == "delegate-call" && strings.Contains(fmt.Sprint(payload.Result["error"]), "approval policy is never") {
			visible = true
		}
	}
	if !visible || !ended {
		t.Fatal("provider rejection was hidden from public tool events")
	}
}

func hasRejectedDelegation(items []driver.TranscriptItem) bool {
	for _, item := range items {
		if item.Kind == driver.TranscriptToolResult && item.ToolUseID == "delegate-call" && item.IsError {
			return true
		}
	}
	return false
}

func TestF2PR23ResumeReplayAppServer(t *testing.T) {
	for _, resident := range []bool{false, true} {
		t.Run(fmt.Sprint(resident), func(t *testing.T) {
			adapter, req, _, _ := newPersistentCodexTest(t, "f2_resume_stale")
			defer closeCodexTestAdapter(t, adapter)
			req.Session.State = &driver.SessionState{ResumeID: "thread-1"}
			req.Spawn = !resident
			recorder := &testutil.EventRecorder{}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			response, err := adapter.Run(ctx, req, recorder)
			if err != nil || response.Failure != nil || response.Output != "reply-1" || response.Checkpoint == nil || response.Usage == nil || response.Usage.InputTokens != 2 || response.Usage.OutputTokens != 1 {
				t.Fatalf("resume replay: err=%v response=%+v", err, response)
			}
			if !strings.Contains(response.RawStreams.Stdout, "turn-previous") && !resident {
				t.Fatal("scripted resume replay was not captured")
			}
			for _, payload := range recorder.StreamSnapshot() {
				if payload.Name == "thread/tokenUsage/updated" {
					t.Fatal("restored usage leaked into public stream")
				}
			}
		})
	}
}

func TestF2MessageBatchAppServer(t *testing.T) {
	for _, resident := range []bool{false, true} {
		t.Run(fmt.Sprint(resident), func(t *testing.T) {
			adapter, req, _, _ := newPersistentCodexTest(t, "messages_steer")
			defer closeCodexTestAdapter(t, adapter)
			req.Spawn = !resident
			port := newCodexMessageTestPort()
			port.immediate = []driver.MessageItem{{ID: "first", Text: "first"}, {ID: "second", Text: "second"}}
			port.signal <- struct{}{}
			req.Messages = port
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			response, err := adapter.Run(ctx, req, &testutil.EventRecorder{})
			if err != nil || response.Failure != nil || response.Output != "first\n\nsecond" {
				t.Fatalf("batch response: %+v err=%v", response, err)
			}
			for _, id := range []string{"first", "second"} {
				acks := port.acks[id]
				if len(acks) != 1 || !acks[0].Delivered || acks[0].Mechanism != "codex.turn_steer" {
					t.Fatalf("Ack(%s)=%+v", id, acks)
				}
			}
		})
	}
}
