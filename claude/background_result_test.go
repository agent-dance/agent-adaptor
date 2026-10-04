package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/memory"
)

const backgroundInit = `{"type":"system","subtype":"init","session_id":"background-session","model":"fixture"}`
const backgroundActive = `{"type":"system","subtype":"background_tasks_changed","session_id":"background-session","tasks":[{"task_id":"task-1","task_type":"local_agent"}]}`
const backgroundIdle = `{"type":"system","subtype":"background_tasks_changed","session_id":"background-session","tasks":[]}`
const backgroundInterim = `{"type":"result","subtype":"success","is_error":false,"session_id":"background-session","result":"main turn done","structured_output":{"state":"pending"},"usage":{"input_tokens":3,"output_tokens":1}}`
const backgroundFinal = `{"type":"result","subtype":"success","is_error":false,"session_id":"background-session","result":"all done","structured_output":{"state":"done"},"usage":{"input_tokens":9,"output_tokens":4}}`

func TestMessageStopKeepsControlInput(t *testing.T) {
	for _, reason := range []string{"", "tool_use", "max_tokens", "end_turn", "stop_sequence"} {
		t.Run(reason, func(t *testing.T) {
			sink := alignmentStdinSink()
			stdin := &fakeStdin{}
			p := newClaudeParser(sink)
			p.enableStreaming("message-stop")
			p.setHITLContext("message-stop", driver.HumanDecisionPolicy{Permission: driver.HumanDecisionAsk})
			p.enableInteractive(context.Background(), sink, stdin)
			alignmentStdinFeed(t, p,
				`{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"`+reason+`"}}}`,
				`{"type":"stream_event","event":{"type":"message_stop"}}`, alignmentStdinControl)
			if stdin.closed != 0 || len(stdin.snapshot()) != 1 {
				t.Fatalf("message_stop lost control input: closes=%d responses=%d", stdin.closed, len(stdin.snapshot()))
			}
			alignmentStdinFeed(t, p, alignmentStdinNative)
			if stdin.closed != 1 || p.checkpoint(0) == nil || p.structuredOutput == nil {
				t.Fatalf("final result did not complete: closes=%d checkpoint=%#v output=%#v", stdin.closed, p.checkpoint(0), p.structuredOutput)
			}
		})
	}
}

func TestBackgroundResultWaitsForFinalResult(t *testing.T) {
	sink := alignmentStdinSink()
	stdin := &fakeStdin{}
	p := newClaudeParser(sink)
	p.enableStreaming("background")
	p.enableInteractive(context.Background(), sink, stdin)
	alignmentStdinFeed(t, p, backgroundInit, backgroundActive, backgroundInterim)
	if stdin.closed != 0 || p.terminalSeen || p.checkpoint(0) != nil {
		t.Fatalf("interim result ended invocation: closes=%d terminal=%t checkpoint=%#v", stdin.closed, p.terminalSeen, p.checkpoint(0))
	}
	alignmentStdinFeed(t, p, backgroundIdle, backgroundFinal)
	if stdin.closed != 1 || p.protocolMalformed || p.checkpoint(0) == nil || p.buildOutput() != "all done" {
		t.Fatalf("final result not accepted: closes=%d malformed=%t checkpoint=%#v output=%q", stdin.closed, p.protocolMalformed, p.checkpoint(0), p.buildOutput())
	}
	if p.usage == nil || p.usage.InputTokens != 9 || p.structuredOutput == nil || string(p.structuredOutput.RawJSON) != `{"state":"done"}` {
		t.Fatalf("final fields not authoritative: usage=%#v schema=%#v", p.usage, p.structuredOutput)
	}
	var results int
	for _, item := range p.transcript {
		if item.Kind == driver.TranscriptResult {
			results++
		}
	}
	if results != 2 {
		t.Fatalf("result transcript count=%d", results)
	}
}

type backgroundDiscardInput struct{ io.Writer }

func (backgroundDiscardInput) Close() error { return nil }

func TestBackgroundResidentReaderConsumesWholeInvocation(t *testing.T) {
	first := strings.Join([]string{backgroundInit, backgroundActive, backgroundInterim, backgroundIdle, backgroundFinal, ""}, "\n")
	second := strings.ReplaceAll(backgroundFinal, "all done", "next turn") + "\n"
	lp := &liveProcess{
		stdin: backgroundDiscardInput{io.Discard}, stdout: bufio.NewReader(strings.NewReader(first + second)), stderr: &lockedBuffer{},
	}
	for i, want := range []string{"all done", "next turn"} {
		p := newClaudeParser(nil)
		raw, sent, err := lp.turn(context.Background(), "prompt", nil, p, nil)
		if err != nil || !sent || p.buildOutput() != want || p.checkpoint(0) == nil {
			t.Fatalf("turn %d: err=%v sent=%t output=%q checkpoint=%#v", i, err, sent, p.buildOutput(), p.checkpoint(0))
		}
		if i == 0 && raw.Stdout != first || i == 1 && raw.Stdout != second {
			t.Fatalf("turn %d raw crossed invocation boundary: %q", i, raw.Stdout)
		}
	}
}

func TestBackgroundFinalResultWithoutNewline(t *testing.T) {
	rawInput := strings.Join([]string{backgroundInit, backgroundActive, backgroundInterim, backgroundIdle, backgroundFinal}, "\n")
	lp := &liveProcess{stdin: backgroundDiscardInput{io.Discard}, stdout: bufio.NewReader(strings.NewReader(rawInput)), stderr: &lockedBuffer{}}
	p := newClaudeParser(nil)
	raw, _, err := lp.turn(context.Background(), "prompt", nil, p, nil)
	if err != nil || raw.Stdout != rawInput || p.checkpoint(0) == nil || p.buildOutput() != "all done" {
		t.Fatalf("unterminated final record lost: raw=%q err=%v checkpoint=%#v", raw.Stdout, err, p.checkpoint(0))
	}
}

func TestBackgroundFailureResultClosesInput(t *testing.T) {
	sink := alignmentStdinSink()
	stdin := &fakeStdin{}
	p := newClaudeParser(sink)
	p.enableInteractive(context.Background(), sink, stdin)
	alignmentStdinFeed(t, p, backgroundInit, backgroundActive, backgroundInterim,
		`{"type":"result","subtype":"error_during_execution","is_error":true,"session_id":"background-session","result":"background failed"}`)
	if stdin.closed != 1 || !p.hasTerminalResult() || p.checkpoint(0) != nil || p.errorMessage != "background failed" {
		t.Fatalf("failed result hidden behind background work: closes=%d checkpoint=%#v failure=%q", stdin.closed, p.checkpoint(0), p.errorMessage)
	}
}

func TestBackgroundSnapshotSafety(t *testing.T) {
	for name, frame := range map[string]string{
		"missing":      `{"type":"system","subtype":"background_tasks_changed","session_id":"background-session"}`,
		"null":         `{"type":"system","subtype":"background_tasks_changed","session_id":"background-session","tasks":null}`,
		"object":       `{"type":"system","subtype":"background_tasks_changed","session_id":"background-session","tasks":{}}`,
		"bad-entry":    `{"type":"system","subtype":"background_tasks_changed","session_id":"background-session","tasks":[null]}`,
		"missing-id":   `{"type":"system","subtype":"background_tasks_changed","session_id":"background-session","tasks":[{"task_type":"local_agent"}]}`,
		"missing-kind": `{"type":"system","subtype":"background_tasks_changed","session_id":"background-session","tasks":[{"task_id":"a"}]}`,
		"duplicate":    `{"type":"system","subtype":"background_tasks_changed","session_id":"background-session","tasks":[{"task_id":"a","task_type":"local_agent"},{"task_id":"a","task_type":"local_agent"}]}`,
		"foreign":      strings.ReplaceAll(backgroundIdle, "background-session", "foreign"),
		"no-session":   `{"type":"system","subtype":"background_tasks_changed","tasks":[]}`,
		"bad-parent":   `{"type":"system","subtype":"background_tasks_changed","session_id":"background-session","parent_tool_use_id":42,"tasks":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			p := newClaudeParser(nil)
			alignmentStdinFeed(t, p, backgroundInit, backgroundActive, backgroundInterim, frame)
			if !p.protocolMalformed || !p.backgroundTasksPending {
				t.Fatalf("invalid snapshot cleared or healed state: malformed=%t pending=%t", p.protocolMalformed, p.backgroundTasksPending)
			}
			alignmentStdinFeed(t, p, backgroundIdle, backgroundFinal)
			if p.checkpoint(0) != nil || p.failureForOutcome(0, "", false) == nil {
				t.Fatal("later valid snapshot/result healed malformed invocation")
			}
		})
	}
	t.Run("nested-does-not-clear", func(t *testing.T) {
		p := newClaudeParser(nil)
		alignmentStdinFeed(t, p, backgroundInit, backgroundActive,
			`{"type":"system","subtype":"background_tasks_changed","session_id":"background-session","parent_tool_use_id":"child","tasks":[]}`,
			backgroundInterim)
		if p.protocolMalformed || !p.backgroundTasksPending || p.terminalSeen || p.checkpoint(0) != nil {
			t.Fatal("nested task snapshot changed the root boundary")
		}
	})
}

func TestBackgroundFinalitySafety(t *testing.T) {
	for name, tail := range map[string][]string{
		"missing-final":      {},
		"idle-without-final": {backgroundIdle},
		"provider-failure":   {`{"type":"result","subtype":"error_during_execution","is_error":true,"session_id":"background-session","result":"background failed"}`},
		"provider-error":     {`{"type":"error","session_id":"background-session","message":"background failed"}`},
		"malformed-success":  {`{"type":"result","subtype":"success","session_id":"background-session","result":"bad"}`},
		"nested-result":      {backgroundIdle, strings.Replace(backgroundFinal, `"type":"result"`, `"type":"result","parent_tool_use_id":"child"`, 1)},
		"duplicate-final":    {backgroundIdle, backgroundFinal, backgroundFinal},
		"after-final-error":  {backgroundIdle, backgroundFinal, `{"type":"error","message":"late"}`},
		"after-final-tasks":  {backgroundIdle, backgroundFinal, backgroundActive},
	} {
		t.Run(name, func(t *testing.T) {
			p := newClaudeParser(nil)
			alignmentStdinFeed(t, p, backgroundInit, backgroundActive, backgroundInterim)
			alignmentStdinFeed(t, p, tail...)
			p.finalize()
			if p.checkpoint(0) != nil || p.failureForOutcome(0, "", false) == nil {
				t.Fatalf("incomplete or invalid invocation became healthy: %#v", p.checkpoint(0))
			}
			if len(p.transcript) < 3 || p.terminal == nil {
				t.Fatal("partial result audit lost")
			}
		})
	}
	t.Run("optional-fields-do-not-carry", func(t *testing.T) {
		p := newClaudeParser(nil)
		alignmentStdinFeed(t, p, backgroundInit, backgroundActive, backgroundInterim, backgroundIdle,
			`{"type":"result","subtype":"success","is_error":false,"session_id":"background-session","result":"done"}`)
		if p.checkpoint(0) == nil || p.structuredOutput != nil || p.usage != nil || p.cost != nil {
			t.Fatal("final result inherited intermediate optional fields")
		}
	})
}

const backgroundHelperEnv = "GO_WANT_CLAUDE_BACKGROUND_HELPER"

func runBackgroundHelper() int {
	reader := bufio.NewReader(os.Stdin)
	mode := os.Getenv(backgroundHelperEnv)
	for turn := 0; ; turn++ {
		if _, err := reader.ReadString('\n'); err != nil {
			if err == io.EOF {
				return 0
			}
			return 31
		}
		if turn > 0 {
			fmt.Fprintln(os.Stdout, strings.ReplaceAll(backgroundFinal, "all done", "next turn"))
			continue
		}
		fmt.Fprintln(os.Stdout, backgroundInit)
		fmt.Fprintln(os.Stdout, backgroundActive)
		for i := 0; i < 2; i++ {
			if i == 0 {
				fmt.Fprintln(os.Stdout, `{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"max_tokens"}}}`)
				fmt.Fprintln(os.Stdout, `{"type":"stream_event","event":{"type":"message_stop"}}`)
			} else {
				fmt.Fprintln(os.Stdout, backgroundInterim)
				if mode == "exit" {
					fmt.Fprint(os.Stderr, "background process failed")
					return 17
				}
				if mode == "eof" {
					return 0
				}
				if mode == "cancel" {
					_, _ = io.Copy(io.Discard, reader)
					return 0
				}
			}
			fmt.Fprintln(os.Stdout, strings.ReplaceAll(alignmentStdinControl, "alignment-control", fmt.Sprintf("background-control-%d", i)))
			line, err := reader.ReadString('\n')
			if err != nil {
				return 32
			}
			var response struct {
				Type     string `json:"type"`
				Response struct {
					RequestID string `json:"request_id"`
				} `json:"response"`
			}
			if json.Unmarshal([]byte(line), &response) != nil || response.Type != "control_response" || response.Response.RequestID != fmt.Sprintf("background-control-%d", i) {
				return 33
			}
		}
		fmt.Fprintln(os.Stdout, backgroundIdle)
		fmt.Fprintln(os.Stdout, backgroundFinal)
		if mode == "one-shot" {
			if _, err := io.Copy(io.Discard, reader); err != nil {
				return 34
			}
			fmt.Fprint(os.Stderr, "after final EOF")
			return 0
		}
	}
}

func backgroundAgent(t *testing.T, mode string, approve adaptor.ApprovalHandler) *adaptor.Agent {
	t.Helper()
	command, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	return adaptor.New(Driver(Config{CommonConfig: CommonConfig{Command: command, CWD: home, GracePeriod: 50 * time.Millisecond, Env: []driver.EnvBinding{
		{Name: backgroundHelperEnv, Value: mode}, {Name: "HOME", Value: home}, {Name: "CLAUDE_CONFIG_DIR", Value: home},
	}}}), adaptor.WithThreadStore(memory.NewStore()),
		adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAsk}}), adaptor.OnApproval(approve))
}

func TestBackgroundPublicProcessBoundary(t *testing.T) {
	for _, resident := range []bool{false, true} {
		t.Run(fmt.Sprintf("resident_%t", resident), func(t *testing.T) {
			mode := "one-shot"
			if resident {
				mode = "resident"
			}
			var decisions int
			a := backgroundAgent(t, mode, func(ctx context.Context, r *adaptor.ApprovalRequest) error { decisions++; return r.Approve(ctx) })
			defer a.Close(context.Background())
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var runner adaptor.Runner = a
			if resident {
				runner = a.Thread("background")
			}
			stream := runner.Stream(ctx, "start")
			var terminals, spawns int
			for event := range stream.Events() {
				switch event := event.(type) {
				case adaptor.RunFinished:
					terminals++
				case adaptor.ProcessInfo:
					if event.Kind == adaptor.ProcessSpawn {
						spawns++
					}
				}
			}
			result, err := stream.Result()
			if err != nil || result == nil || result.Text != "all done" || decisions != 2 {
				t.Fatalf("first turn: result=%#v err=%v decisions=%d", result, err, decisions)
			}
			if terminals != 1 || spawns != 1 {
				t.Fatalf("terminal/spawn counts=%d/%d", terminals, spawns)
			}
			if result.Raw().Terminal == nil || string(result.Raw().Terminal.JSON) != backgroundFinal || !strings.Contains(result.Raw().Stdout, backgroundInterim) {
				t.Fatal("intermediate raw or final terminal lost")
			}
			if !resident && result.Raw().Stderr != "after final EOF" {
				t.Fatal("one-shot did not drain stderr")
			}
			if resident {
				second, err := runner.Run(ctx, "continue")
				if err != nil || second == nil || second.Text != "next turn" || decisions != 2 || strings.Contains(second.Raw().Stdout, backgroundInterim) {
					t.Fatalf("resident boundary/reuse failed: result=%#v err=%v decisions=%d", second, err, decisions)
				}
			}
		})
	}
}

func TestBackgroundPublicFailureKeepsAudit(t *testing.T) {
	for _, mode := range []string{"exit", "eof", "decision-error"} {
		t.Run(mode, func(t *testing.T) {
			wantErr := errors.New("background decision failed")
			var decisions int
			a := backgroundAgent(t, mode, func(ctx context.Context, r *adaptor.ApprovalRequest) error {
				decisions++
				if mode == "decision-error" && decisions == 2 {
					return wantErr
				}
				return r.Approve(ctx)
			})
			defer a.Close(context.Background())
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			thread := a.Thread("background")
			result, err := thread.Run(ctx, "fail")
			var runErr *adaptor.RunError
			if result != nil || !errors.As(err, &runErr) || runErr.Result == nil || !strings.Contains(runErr.Result.Raw().Stdout, backgroundInterim) {
				t.Fatalf("failure audit lost: result=%#v err=%v", result, err)
			}
			if mode == "decision-error" && !errors.Is(err, wantErr) {
				t.Fatalf("decision cause lost: %v", err)
			}
			if ctx.Err() != nil {
				t.Fatalf("failure needed outer timeout: %v", err)
			}
			if checkpoint, err := thread.Checkpoint(ctx); checkpoint != nil || !errors.Is(err, adaptor.ErrThreadNotFound) {
				t.Fatalf("failed background invocation saved a checkpoint: %#v %v", checkpoint, err)
			}
		})
	}
}

func TestBackgroundPublicCancellationKeepsPartialAudit(t *testing.T) {
	for _, resident := range []bool{false, true} {
		t.Run(fmt.Sprintf("resident_%t", resident), func(t *testing.T) {
			a := backgroundAgent(t, "cancel", func(ctx context.Context, r *adaptor.ApprovalRequest) error { return r.Approve(ctx) })
			defer a.Close(context.Background())
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var runner adaptor.Runner = a
			if resident {
				runner = a.Thread("cancel-background")
			}
			stream := runner.Stream(ctx, "cancel after interim")
			var stdout strings.Builder
			var cancelled bool
			for event := range stream.Events() {
				if event, ok := event.(adaptor.ProcessInfo); ok && event.Kind == adaptor.ProcessStdout {
					stdout.Write(event.Bytes)
					if !cancelled && strings.Contains(stdout.String(), backgroundInterim) {
						stream.Cancel()
						cancelled = true
					}
				}
			}
			result, err := stream.Result()
			var runErr *adaptor.RunError
			if !cancelled || result != nil || !errors.Is(err, context.Canceled) || !errors.As(err, &runErr) || runErr.Result == nil || !strings.Contains(runErr.Result.Raw().Stdout, backgroundInterim) || ctx.Err() != nil {
				t.Fatalf("cancel lost cause/partial result or needed deadline: cancelled=%t result=%#v err=%v ctx=%v", cancelled, result, err, ctx.Err())
			}
			if resident {
				if checkpoint, err := a.Thread("cancel-background").Checkpoint(ctx); checkpoint != nil || !errors.Is(err, adaptor.ErrThreadNotFound) {
					t.Fatalf("cancel saved a healthy checkpoint: %#v %v", checkpoint, err)
				}
			}
		})
	}
}
