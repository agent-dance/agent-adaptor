package claude

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/clihelper"
	"github.com/agent-dance/agent-adaptor/memory"
)

const alignmentStdinHelperEnv = "GO_WANT_ALIGNMENT_CLAUDE_STDIN_HELPER"
const alignmentStdinSuccess = `{"type":"result","subtype":"success","is_error":false,"session_id":"alignment-stdin","result":"done","usage":{"input_tokens":0,"output_tokens":1}}`
const alignmentStdinFailure = `{"type":"result","subtype":"error_during_execution","is_error":true,"session_id":"alignment-stdin","result":"provider failed"}`
const alignmentStdinNative = `{"type":"result","subtype":"success","is_error":false,"session_id":"alignment-stdin","result":"done","structured_output":{"choice":"a"}}`

// The fixture emits result before waiting for EOF. A driver which forgets to
// close interactive stdin cannot see the trailing bytes or a clean child exit.
func runAlignmentStdinHelper() int {
	reader := bufio.NewReader(os.Stdin)
	if _, err := reader.ReadString('\n'); err != nil {
		return 21
	}
	terminal := alignmentStdinSuccess
	switch os.Getenv(alignmentStdinHelperEnv) {
	case "failure":
		terminal = alignmentStdinFailure
	case "native":
		terminal = alignmentStdinNative
	}
	fmt.Fprintln(os.Stdout, terminal)
	if _, err := io.Copy(io.Discard, reader); err != nil {
		return 22
	}
	fmt.Fprint(os.Stdout, "\n \t\n")
	fmt.Fprint(os.Stderr, "after stdin EOF")
	return 0
}

func alignmentStdinConfig(t *testing.T, mode string) Config {
	t.Helper()
	command, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	return Config{CommonConfig: CommonConfig{Command: command, CWD: home, Env: []driver.EnvBinding{
		{Name: alignmentStdinHelperEnv, Value: mode},
		{Name: "CLAUDE_CONFIG_DIR", Value: home},
		{Name: "HOME", Value: home},
		{Name: "USERPROFILE", Value: home},
	}}}
}

func alignmentStdinSink() *fakeInteractiveSink {
	return newFakeInteractiveSink(func(req driver.DecisionRequest) (driver.DecisionResponse, error) {
		return driver.DecisionResponse{RequestID: req.RequestID, Result: driver.DecisionApproved}, nil
	})
}

func TestAlignmentStdinResultOnly(t *testing.T) {
	for _, mode := range []string{"success", "failure", "native"} {
		t.Run(mode, func(t *testing.T) {
			cfg := alignmentStdinConfig(t, mode)
			sink := alignmentStdinSink()
			parser := newClaudeParser(sink)
			parser.enableStreaming("alignment-stdin")
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			stdin := clihelper.NewStdinController()
			parser.enableInteractive(ctx, sink, stdin)
			// Exercise the same parser and real stdin controller as the driver.
			// Native schema + interactive negotiation is owned by T07.
			process, err := clihelper.Run(ctx, clihelper.CommandRequest{
				Command: cfg.Command, CWD: cfg.CWD, Env: cfg.Env,
				Prompt: "{}\n", Stdin: stdin, Observe: parser.onChunk,
			}, sink)
			if err != nil || ctx.Err() != nil || process.ExitCode != 0 {
				t.Fatalf("result-only child did not exit before deadline: err=%v context=%v process=%#v", err, ctx.Err(), process)
			}
			parser.finalize()
			terminal := alignmentStdinSuccess
			wantEvent := driver.StreamRunFinished
			if mode == "failure" {
				terminal, wantEvent = alignmentStdinFailure, driver.StreamRunError
			} else if mode == "native" {
				terminal = alignmentStdinNative
				if parser.structuredOutput == nil || !parser.structuredOutput.Valid || string(parser.structuredOutput.RawJSON) != `{"choice":"a"}` {
					t.Fatalf("native payload lost: %#v", parser.structuredOutput)
				}
			}
			raw := driver.RawStreams{Stdout: process.RawStreams.Stdout, Stderr: process.RawStreams.Stderr, Terminal: parser.terminal}
			req := driver.Request{}
			if mode == "native" {
				req.OutputSchema = &driver.OutputSchema{
					Format:     driver.OutputFormatJSONSchema,
					SchemaJSON: []byte(`{"type":"object","properties":{"choice":{"type":"string"}},"required":["choice"],"additionalProperties":false}`),
				}
				req.StructuredOutputSource = driver.StructuredOutputSourceNative
			}
			response, err := buildClaudeResponse(req, parser, raw, process.ExitCode, process.Signal, process.TimedOut, "", cfg.CWD, "", nil)
			if err != nil {
				t.Fatal(err)
			}
			if raw.Stdout != terminal+"\n\n \t\n" || raw.Stderr != "after stdin EOF" || raw.Terminal == nil || string(raw.Terminal.JSON) != terminal {
				t.Fatalf("terminal or tail bytes lost: %#v", raw)
			}
			if len(response.Transcript) != 2 || response.Transcript[0].Kind != driver.TranscriptResult || response.Transcript[1].Kind != driver.TranscriptStderr {
				t.Fatalf("terminal/tail transcript lost: %#v", response.Transcript)
			}
			if (response.Checkpoint != nil && response.Checkpoint.Valid) != (mode != "failure") {
				t.Fatalf("checkpoint health changed: %#v", response.Checkpoint)
			}
			if mode == "native" && (response.StructuredOutput == nil || !response.StructuredOutput.Valid || string(response.StructuredOutput.RawJSON) != `{"choice":"a"}`) {
				t.Fatalf("validated native output lost: %#v", response.StructuredOutput)
			}
			assertClaudeStreamTerminal(t, sink.events, wantEvent)
		})
	}
}

func TestAlignmentStdinPublicOneShot(t *testing.T) {
	for _, thread := range []bool{false, true} {
		t.Run(fmt.Sprintf("thread_spawn_%t", thread), func(t *testing.T) {
			for _, mode := range []string{"success", "failure"} {
				t.Run(mode, func(t *testing.T) {
					a := adaptor.New(Driver(alignmentStdinConfig(t, mode)), adaptor.WithThreadStore(memory.NewStore()),
						adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAsk}}))
					defer a.Close(context.Background())
					var runner adaptor.Runner = a
					if thread {
						runner = a.Thread("result-only")
					}
					ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
					defer cancel()
					var opts []adaptor.CallOption
					if thread {
						opts = append(opts, adaptor.WithSpawn())
					}
					stream := runner.Stream(ctx, "finish", opts...)
					var terminalEvents int
					for event := range stream.Events() {
						switch event.(type) {
						case adaptor.RunFinished:
							terminalEvents++
						}
					}
					result, err := stream.Result()
					if mode == "failure" {
						var runErr *adaptor.RunError
						if !errors.As(err, &runErr) {
							t.Fatalf("expected provider failure, got %v", err)
						}
						result = runErr.Result
					} else if err != nil {
						t.Fatal(err)
					}
					if ctx.Err() != nil || terminalEvents != 1 || result == nil || result.Raw().Terminal == nil || !strings.HasSuffix(result.Raw().Stdout, "\n\n \t\n") || result.Raw().Stderr != "after stdin EOF" {
						t.Fatalf("public output/tail lost: ctx=%v terminals=%d result=%#v", ctx.Err(), terminalEvents, result)
					}
					runResult, runErr := runner.Run(ctx, "finish", opts...)
					if mode == "failure" {
						var failure *adaptor.RunError
						if !errors.As(runErr, &failure) {
							t.Fatalf("Run provider failure=%v", runErr)
						}
						runResult = failure.Result
					} else if runErr != nil {
						t.Fatal(runErr)
					}
					if runResult == nil || runResult.Text != result.Text || runResult.Summary != result.Summary ||
						!reflect.DeepEqual(runResult.Raw(), result.Raw()) || !reflect.DeepEqual(runResult.Transcript(), result.Transcript()) ||
						!reflect.DeepEqual(runResult.Usage, result.Usage) || !reflect.DeepEqual(runResult.Services(), result.Services()) {
						t.Fatalf("Run and Stream.Result diverged: run=%#v stream=%#v", runResult, result)
					}
				})
			}
		})
	}
}

func alignmentStdinFeed(t *testing.T, p *claudeParser, lines ...string) {
	t.Helper()
	for _, line := range lines {
		if err := p.onChunk("stdout", []byte(line+"\n"), time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAlignmentStdinCloseExactlyOnce(t *testing.T) {
	for _, order := range []string{"result-only", "message-stop-first", "result-first", "duplicate-result", "decision-error-first"} {
		t.Run(order, func(t *testing.T) {
			sink := alignmentStdinSink()
			stdin := &fakeStdin{}
			p := newClaudeParser(sink)
			p.enableStreaming("alignment-close")
			p.enableInteractive(context.Background(), sink, stdin)
			if order == "message-stop-first" {
				alignmentStdinFeed(t, p,
					`{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"end_turn"}}}`,
					`{"type":"stream_event","event":{"type":"message_stop"}}`)
			}
			if order == "decision-error-first" {
				sink.respond = func(driver.DecisionRequest) (driver.DecisionResponse, error) {
					return driver.DecisionResponse{}, context.Canceled
				}
				p.setHITLContext("alignment-close", driver.HumanDecisionPolicy{Permission: driver.HumanDecisionAsk})
				alignmentStdinFeed(t, p, alignmentStdinControl)
			}
			alignmentStdinFeed(t, p, alignmentStdinSuccess)
			if order == "result-first" {
				alignmentStdinFeed(t, p, `{"type":"stream_event","event":{"type":"message_stop"}}`)
			} else if order == "duplicate-result" {
				alignmentStdinFeed(t, p, alignmentStdinSuccess)
			}
			if stdin.closed != 1 {
				t.Fatalf("Close calls=%d, want exactly one", stdin.closed)
			}
			// Extra frames after a result still invalidate the protocol; closing
			// stdin must not relax the existing checkpoint safety fence.
			if (order == "result-first" || order == "duplicate-result") && !p.protocolMalformed {
				t.Fatal("post-terminal protocol fence was lost")
			}
		})
	}
}

const alignmentStdinControl = `{"type":"control_request","request_id":"alignment-control","request":{"subtype":"can_use_tool","tool_name":"Bash","tool_use_id":"outer-tool","input":{"command":"pwd"}}}`

func TestAlignmentStdinIntermediateAndNestedRemainAnswerable(t *testing.T) {
	sink := alignmentStdinSink()
	stdin := &fakeStdin{}
	p := newClaudeParser(sink)
	p.enableStreaming("alignment-tool")
	p.setHITLContext("alignment-tool", driver.HumanDecisionPolicy{Permission: driver.HumanDecisionAsk})
	p.enableInteractive(context.Background(), sink, stdin)
	alignmentStdinFeed(t, p,
		`{"type":"stream_event","event":{"type":"message_start","message":{"id":"root"}}}`,
		`{"type":"stream_event","event":{"type":"message_stop"}}`,
		`{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"tool_use"}}}`,
		`{"type":"stream_event","parent_tool_use_id":"outer-tool","event":{"type":"message_start","message":{"id":"child"}}}`,
		`{"type":"stream_event","parent_tool_use_id":"outer-tool","event":{"type":"message_delta","delta":{"stop_reason":"end_turn"}}}`,
		`{"type":"stream_event","parent_tool_use_id":"outer-tool","event":{"type":"message_stop"}}`,
		`{"type":"stream_event","event":{"type":"message_stop"}}`,
		`{"type":"assistant","parent_tool_use_id":"outer-tool","message":{"content":[{"type":"text","text":"child done"}],"stop_reason":"end_turn"}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"child-tool","content":[{"type":"result","subtype":"success","result":"child result"}]}]}}`)
	if stdin.closed != 0 || p.terminalSeen {
		t.Fatalf("intermediate/child frames ended root: closes=%d terminal=%t", stdin.closed, p.terminalSeen)
	}
	alignmentStdinFeed(t, p, alignmentStdinControl)
	frames := stdin.snapshot()
	if len(frames) != 1 || len(sink.requests) != 1 {
		t.Fatalf("tool_use did not remain answerable: frames=%q requests=%#v", frames, sink.requests)
	}
	if response := decodeControlResponseFrame(t, frames[0]); response.RequestID != "alignment-control" || response.Behavior != "allow" {
		t.Fatalf("control response=%#v", response)
	}
	alignmentStdinFeed(t, p, alignmentStdinSuccess)
	if stdin.closed != 1 {
		t.Fatalf("root result Close calls=%d", stdin.closed)
	}
}

func TestAlignmentStdinPersistentSecondTurn(t *testing.T) {
	fx := newPersistentClaudeFixtureWithOptions(t, nil, adaptor.WithPolicy(adaptor.Policy{
		Approvals: adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAsk},
	}))
	defer fx.close()
	for _, prompt := range []string{"first", "second"} {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		result, err := fx.thread.Run(ctx, prompt)
		cancel()
		if err != nil || result == nil || result.Text != "ok" || result.Raw().Terminal == nil {
			t.Fatalf("persistent turn %q: result=%#v err=%v", prompt, result, err)
		}
	}
	if fx.spawnCount(t) != 1 || fx.overlapCount(t) != 0 {
		t.Fatal("interactive result closed/replaced the resident writer")
	}
}

func TestAlignmentStdinCancelRacesTerminal(t *testing.T) {
	for i := 0; i < 5; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			cfg := alignmentStdinConfig(t, "success")
			a := adaptor.New(Driver(cfg), adaptor.WithPolicy(adaptor.Policy{
				Approvals: adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAsk},
			}))
			defer a.Close(context.Background())
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			stream := a.Stream(ctx, "finish")
			sawResult := false
			for event := range stream.Events() {
				if process, ok := event.(adaptor.ProcessInfo); ok && strings.Contains(string(process.Bytes), `"type":"result"`) {
					sawResult = true
					stream.Cancel()
					stream.Cancel()
				}
			}
			_, err := stream.Result()
			if !sawResult || ctx.Err() != nil {
				t.Fatalf("cancel/terminal did not complete within bound: result=%t context=%v err=%v", sawResult, ctx.Err(), err)
			}
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Fatalf("unexpected cancellation result: %v", err)
			}
		})
	}
}
