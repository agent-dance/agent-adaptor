package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/memory"
)

const alignmentClaudeHelperEnv = "GO_WANT_ALIGNMENT_CLAUDE_HELPER"
const alignmentClaudeSchema = `{"type":"object","properties":{"directory":{"type":"string"}},"required":["directory"],"additionalProperties":false}`
const alignmentClaudeInit = `{"type":"system","subtype":"init","session_id":"session-c04","model":"fixture-model"}`
const alignmentClaudeQuestion = `{"type":"control_request","request_id":"question-c04","request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion","tool_use_id":"tool-question-c04","input":{"questions":[{"question":"选择目录","header":"目录","multiSelect":false,"options":[{"label":"docs","description":"文档"},{"label":"src","description":"源码"}]}]}}}`
const alignmentClaudePlan = `{"type":"control_request","request_id":"plan-c04","request":{"subtype":"can_use_tool","tool_name":"ExitPlanMode","tool_use_id":"tool-plan-c04","input":{"plan":"检查文档"}}}`
const alignmentClaudePermission = `{"type":"control_request","request_id":"permission-c04","request":{"subtype":"can_use_tool","tool_name":"Bash","tool_use_id":"tool-permission-c04","input":{"command":"pwd"}}}`
const alignmentClaudeTerminal = `{"type":"result","subtype":"success","is_error":false,"session_id":"session-c04","result":"完成","structured_output":{"directory":"docs"},"usage":{"input_tokens":0,"output_tokens":0}}`
const alignmentClaudePartial = `{"type":"assistant","message":{"model":"fixture-model","content":[{"type":"text","text":"partial assistant"}]}}`

// This child speaks NDJSON over real pipes. It cannot produce its terminal
// until the host responds on the same stdin, and records the exact wire reply.
func runAlignmentClaudeHelper() int {
	recordPersistentHelperSpawn(os.Getenv("SPAWN_FILE"), os.Getenv("PID_FILE"), os.Getenv("OVERLAP_FILE"))
	args, _ := json.Marshal(os.Args[1:])
	appendPersistentHelperLine(os.Getenv("ARGS_FILE"), string(args))
	native := false
	for _, arg := range os.Args[1:] {
		native = native || arg == "--json-schema"
	}
	appendPath := ""
	for i, arg := range os.Args[1:] {
		if arg == "--append-system-prompt-file" && i+2 < len(os.Args) {
			appendPath = os.Args[i+2]
		}
	}
	if os.Getenv("APPEND_RECORD") != "" {
		raw, err := os.ReadFile(appendPath)
		if appendPath != "" && err != nil {
			return 35
		}
		record, _ := json.Marshal(map[string]string{"path": appendPath, "text": string(raw)})
		appendPersistentHelperLine(os.Getenv("APPEND_RECORD"), string(record))
	}
	interactive := claudePersistentInputMode(os.Args[1:])
	reader := bufio.NewReader(os.Stdin)
	if os.Getenv("ALIGNMENT_PARTIAL_WRITE") == "1" {
		if _, err := reader.ReadByte(); err != nil {
			return 31
		}
		fmt.Fprint(os.Stderr, "observed diagnostic without newline")
		fmt.Fprint(os.Stdout, alignmentClaudePartial+"\n")
		_, _ = io.Copy(io.Discard, reader)
		return 0
	}
	for {
		var prompt string
		if interactive {
			line, err := reader.ReadString('\n')
			if err != nil {
				return 0
			}
			var frame struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			}
			if json.Unmarshal([]byte(line), &frame) != nil {
				return 31
			}
			prompt = frame.Message.Content
		} else {
			raw, _ := io.ReadAll(reader)
			prompt = string(raw)
		}
		if appendPath != "" {
			if _, err := os.ReadFile(appendPath); err != nil {
				return 36
			}
		}
		if os.Getenv("ADOPTION_FRAMES") != "" {
			fmt.Fprint(os.Stdout, os.Getenv("ADOPTION_FRAMES"))
		}
		fmt.Fprintln(os.Stdout, alignmentClaudeInit)
		if strings.Contains(prompt, "multiple-usage") {
			fmt.Fprint(os.Stdout, alignmentClaudeUsageFrames())
			if strings.Contains(prompt, "terminal-zero") {
				fmt.Fprintln(os.Stdout, alignmentClaudeTerminal)
				continue
			}
			fmt.Fprint(os.Stderr, "exit diagnostic without newline")
			return 23
		}
		if strings.Contains(prompt, "resident-abort") {
			fmt.Fprintln(os.Stderr, "abort stderr")
			// One atomic pipe write makes the post-request bytes available
			// before the host callback aborts. The reader must drain them.
			frames := `{"type":"stream_event","event":{"type":"message_start","message":{"id":"abort","usage":{"input_tokens":7,"output_tokens":2}}}}` + "\n" + alignmentClaudePartial + "\n" + alignmentClaudeQuestion + "\n"
			if strings.Contains(prompt, "terminal") {
				frames += alignmentClaudeTerminal + "\n"
			}
			fmt.Fprint(os.Stdout, frames)
			_, _ = io.Copy(io.Discard, reader)
			return 0
		}
		if strings.Contains(prompt, "partial-") {
			if delay := os.Getenv("ALIGNMENT_PARTIAL_OUTPUT_DELAY"); delay != "" {
				duration, err := time.ParseDuration(delay)
				if err != nil {
					return 37
				}
				time.Sleep(duration)
			}
			fmt.Fprintln(os.Stderr, "partial stderr")
			fmt.Fprintln(os.Stdout, `{"type":"stream_event","event":{"type":"message_start","message":{"id":"partial","usage":{"input_tokens":7,"output_tokens":2}}}}`)
			fmt.Fprintln(os.Stdout, alignmentClaudePartial)
			if strings.Contains(prompt, "partial-eof") {
				fmt.Fprintln(os.Stdout, `{"type":"error","message":"provider disconnected"}`)
				return 23
			}
			fmt.Fprintln(os.Stdout, `{"type":"system","subtype":"partial_ready"}`)
			_, _ = io.Copy(io.Discard, reader)
			return 0
		}
		controls := []string{}
		if strings.Contains(prompt, "permission") {
			controls = append(controls, alignmentClaudePermission)
		}
		if strings.Contains(prompt, "question") {
			controls = append(controls, alignmentClaudeQuestion)
		}
		if strings.Contains(prompt, "plan") {
			controls = append(controls, alignmentClaudePlan)
		}
		for _, control := range controls {
			fmt.Fprintln(os.Stdout, control)
			line, err := reader.ReadString('\n')
			if err != nil {
				return 0
			}
			var reply map[string]any
			if json.Unmarshal([]byte(line), &reply) != nil || reply["type"] != "control_response" {
				return 32
			}
			appendPersistentHelperLine(os.Getenv("REPLIES_FILE"), strings.TrimSpace(line))
		}
		terminal := alignmentClaudeTerminal
		if !native {
			terminal = strings.Replace(terminal, `"result":"完成"`, `"result":"{\"directory\":\"docs\"}"`, 1)
		}
		switch {
		case strings.Contains(prompt, "invalid-project-metadata"):
			terminal = strings.Replace(terminal, `"structured_output":{"directory":"docs"}`, `"structured_output":{"project_name":42}`, 1)
		case strings.Contains(prompt, "invalid"):
			terminal = strings.ReplaceAll(terminal, `"directory":"docs"`, `"directory":1`)
		case strings.Contains(prompt, "missing"):
			terminal = strings.Replace(terminal, `,"structured_output":{"directory":"docs"}`, "", 1)
		case strings.Contains(prompt, "provider-error"):
			terminal = strings.Replace(terminal, `"subtype":"success","is_error":false`, `"subtype":"error_during_execution","is_error":true`, 1)
		case strings.Contains(prompt, "malformed"):
			fmt.Fprintln(os.Stdout, `{"broken":`)
		case strings.Contains(prompt, "truncated"):
			fmt.Fprint(os.Stdout, `{"type":"result"`)
			return 0
		}
		fmt.Fprintln(os.Stdout, terminal)
		if native || !interactive || os.Getenv(alignmentClaudeHelperEnv) == "oneshot" {
			_, _ = io.Copy(io.Discard, reader)
			fmt.Fprint(os.Stdout, "\n \t\n")
			fmt.Fprint(os.Stderr, "after stdin EOF")
			if strings.Contains(prompt, "nonzero") {
				return 24
			}
			return 0
		}
	}
}

type alignmentClaudeFixture struct {
	cfg  Config
	root string
	pool *persistentPool
}

func newAlignmentClaudeFixture(t *testing.T, resident bool) *alignmentClaudeFixture {
	t.Helper()
	command, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	mode := "oneshot"
	if resident {
		mode = "resident"
	}
	env := []driver.EnvBinding{{Name: alignmentClaudeHelperEnv, Value: mode}, {Name: "HOME", Value: root}, {Name: "USERPROFILE", Value: root}, {Name: "CLAUDE_CONFIG_DIR", Value: root}}
	for _, name := range []string{"SPAWN_FILE", "PID_FILE", "OVERLAP_FILE", "ARGS_FILE", "REPLIES_FILE"} {
		env = append(env, driver.EnvBinding{Name: name, Value: filepath.Join(root, name)})
	}
	return &alignmentClaudeFixture{cfg: Config{CommonConfig: CommonConfig{Command: command, CWD: root, Env: env, GracePeriod: 50 * time.Millisecond}, Model: "fixture-model"}, root: root, pool: newPersistentPool()}
}
func (f *alignmentClaudeFixture) agent(opts ...adaptor.Option) *adaptor.Agent {
	return adaptor.New(configuredDriver{adapter: adapter{persistent: f.pool}, cfg: f.cfg}, opts...)
}
func (f *alignmentClaudeFixture) lines(t *testing.T, name string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.root, name))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}
func alignmentClaudePolicy() adaptor.Policy {
	return adaptor.Policy{Approvals: adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAutoApprove, Question: adaptor.QuestionAsk}}
}
func alignmentClaudeApprove(ctx context.Context, req *adaptor.ApprovalRequest) error {
	if req.Kind == adaptor.ApprovalQuestion {
		return req.Answer(ctx, "docs")
	}
	return req.Approve(ctx)
}
func alignmentClaudeResult(t *testing.T, result *adaptor.Result, err error) *adaptor.Result {
	t.Helper()
	if err == nil {
		if result == nil {
			t.Fatal("nil successful Result")
		}
		return result
	}
	var re *adaptor.RunError
	if result != nil || !errors.As(err, &re) || re.Result == nil {
		t.Fatalf("partial carrier lost: result=%#v err=%v", result, err)
	}
	return re.Result
}
func assertAlignmentClaudeEqual(t *testing.T, a, b *adaptor.Result) {
	t.Helper()
	if a.Text != b.Text || a.Summary != b.Summary || a.Model != b.Model || a.Provider != b.Provider || !reflect.DeepEqual(a.Raw(), b.Raw()) || !reflect.DeepEqual(a.Transcript(), b.Transcript()) || !reflect.DeepEqual(a.Usage, b.Usage) || !reflect.DeepEqual(a.Services(), b.Services()) || !reflect.DeepEqual(a.Metadata, b.Metadata) {
		t.Fatalf("Run/Stream outputs differ:\na=%#v raw=%#v\nb=%#v raw=%#v", a, a.Raw(), b, b.Raw())
	}
}

// The host fixture reports an observed service state separately from the
// declared request; it is included to make Services equivalence non-vacuous.
type alignmentClaudeServices struct{}

func (alignmentClaudeServices) Ensure(context.Context, adaptor.ServiceRequest) ([]adaptor.ServiceRef, error) {
	return []adaptor.ServiceRef{{ID: "fixture-service", Name: "fixture-service", Status: driver.RuntimeServiceRunning, Health: driver.RuntimeHealthHealthy, Lifecycle: driver.RuntimeLifecycleShared}}, nil
}
func (alignmentClaudeServices) ReleaseByRun(context.Context, string) error               { return nil }
func (alignmentClaudeServices) ReleaseByLabels(context.Context, map[string]string) error { return nil }

func TestAlignmentClaudeNativeHITL(t *testing.T) {
	for _, thread := range []bool{false, true} {
		for _, spawn := range []bool{false, true} {
			t.Run(fmt.Sprintf("thread_%t_spawn_%t", thread, spawn), func(t *testing.T) {
				f := newAlignmentClaudeFixture(t, true)
				a := f.agent(adaptor.WithThreadStore(memory.NewStore()), adaptor.WithPolicy(alignmentClaudePolicy()),
					adaptor.WithServiceManager(alignmentClaudeServices{}), adaptor.WithServices(adaptor.ServiceSpec{ID: "fixture-service", Lifecycle: driver.RuntimeLifecycleShared}))
				defer a.Close(context.Background())
				var runner adaptor.Runner = a
				if thread {
					runner = a.Thread("native")
					// A native turn must first stop this actual resident
					// writer; Thread identity alone does not prove reuse.
					warmCtx, warmCancel := context.WithTimeout(context.Background(), 3*time.Second)
					_, warmErr := runner.Run(warmCtx, "warm")
					warmCancel()
					if warmErr != nil {
						t.Fatal(warmErr)
					}
					if f.pool.lookup("session-c04") == nil {
						t.Fatal("warm turn did not establish a resident writer")
					}
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				opts := []adaptor.CallOption{adaptor.WithSchemaJSON([]byte(alignmentClaudeSchema))}
				if spawn {
					opts = append(opts, adaptor.WithSpawn())
				}
				count := 0
				runOpts := append(append([]adaptor.CallOption{}, opts...), adaptor.OnApproval(func(ctx context.Context, r *adaptor.ApprovalRequest) error {
					count++
					return alignmentClaudeApprove(ctx, r)
				}))
				first, err := runner.Run(ctx, "question plan", runOpts...)
				if err != nil {
					t.Fatal(err)
				}
				stream := runner.Stream(ctx, "question plan", opts...)
				for event := range stream.Events() {
					if r, ok := event.(*adaptor.ApprovalRequest); ok {
						count++
						if err := alignmentClaudeApprove(ctx, r); err != nil {
							t.Fatal(err)
						}
					}
				}
				second, err := stream.Result()
				if err != nil {
					t.Fatal(err)
				}
				assertAlignmentClaudeEqual(t, first, second)
				for _, result := range []*adaptor.Result{first, second} {
					var output map[string]string
					if err := result.Decode(&output); err != nil || output["directory"] != "docs" {
						t.Fatalf("Decode=%#v %v", output, err)
					}
					if len(result.Services()) != 1 || result.Services()[0].ID != "fixture-service" {
						t.Fatalf("observed services lost: %#v", result.Services())
					}
					if result.Text != "完成" || result.Summary != "" || result.Raw().Terminal == nil || string(result.Raw().Terminal.JSON) != alignmentClaudeTerminal || result.Raw().Stderr != "after stdin EOF" || !strings.HasSuffix(result.Raw().Stdout, "\n\n \t\n") || result.Usage == nil || result.Usage.InputTokens != 0 || result.Usage.OutputTokens != 0 {
						t.Fatalf("result-only output lost: %#v raw=%#v", result, result.Raw())
					}
				}
				if count != 4 {
					t.Fatalf("actual approvals=%d want 4", count)
				}
				replies := f.lines(t, "REPLIES_FILE")
				if len(replies) != 4 {
					t.Fatalf("replies=%q", replies)
				}
				for i, line := range replies {
					reply := decodeControlResponseFrame(t, []byte(line))
					if reply.Behavior != "allow" || reply.Interrupt {
						t.Fatalf("reply=%#v", reply)
					}
					if i%2 == 0 {
						answers, _ := reply.UpdatedInput["answers"].(map[string]any)
						if reply.RequestID != "question-c04" || reply.ToolUseID != "tool-question-c04" || answers["选择目录"] != "docs" || reply.UpdatedInput["question_type"] != nil {
							t.Fatalf("question reply=%#v", reply)
						}
					} else if reply.RequestID != "plan-c04" || reply.ToolUseID != "tool-plan-c04" || reply.UpdatedInput["plan"] != "检查文档" {
						t.Fatalf("plan reply=%#v", reply)
					}
				}
				if len(f.lines(t, "OVERLAP_FILE")) != 0 {
					t.Fatal("overlapping writers")
				}
			})
		}
	}
}

func TestAlignmentClaudeSchemaMatrix(t *testing.T) {
	for _, tc := range []struct {
		name   string
		policy adaptor.Policy
		prompt string
		native bool
		asks   int
		schema bool
	}{
		{"zero", adaptor.Policy{}, "finish", false, 0, true},
		{"question_inherits_permission", adaptor.Policy{Approvals: adaptor.ApprovalPolicy{Question: adaptor.QuestionAsk}}, "permission question plan", false, 3, true},
		{"permission", adaptor.Policy{Approvals: adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAsk}}, "permission plan", false, 2, true},
		{"native_question_and_default_plan", alignmentClaudePolicy(), "question plan", true, 2, true},
		{"native_plan", adaptor.Policy{Approvals: adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAutoApprove, PlanReview: adaptor.ApprovalAsk}}, "plan", true, 1, true},
		{"ordinary_permission", adaptor.Policy{Approvals: adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAsk}}, "permission", false, 1, false},
		{"ordinary_question_inherits_permission", adaptor.Policy{Approvals: adaptor.ApprovalPolicy{Question: adaptor.QuestionAsk}}, "permission question plan", false, 3, false},
		{"zero_no_schema", adaptor.Policy{}, "finish", false, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAlignmentClaudeFixture(t, false)
			calls := 0
			opts := []adaptor.Option{adaptor.WithPolicy(tc.policy), adaptor.OnApproval(func(ctx context.Context, r *adaptor.ApprovalRequest) error {
				calls++
				return alignmentClaudeApprove(ctx, r)
			})}
			var callOpts []adaptor.CallOption
			if tc.schema {
				callOpts = append(callOpts, adaptor.WithSchemaJSON([]byte(alignmentClaudeSchema)))
			}
			a := f.agent(opts...)
			defer a.Close(context.Background())
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			result, err := a.Run(ctx, tc.prompt, callOpts...)
			if err != nil {
				t.Fatal(err)
			}
			args := f.lines(t, "ARGS_FILE")
			if len(args) != 1 || strings.Contains(args[0], "--json-schema") != tc.native || calls != tc.asks {
				t.Fatalf("args=%q approvals=%d", args, calls)
			}
			if strings.HasPrefix(tc.name, "zero") && (strings.Contains(args[0], "--input-format") || strings.Contains(args[0], "--dangerously-skip-permissions")) {
				t.Fatalf("zero policy silently became interactive/auto-approved: %q", args)
			}
			if tc.schema {
				var got map[string]string
				if err := result.Decode(&got); err != nil || got["directory"] != "docs" {
					t.Fatalf("decode=%#v err=%v", got, err)
				}
			}
		})
	}
}

func TestAlignmentClaudePersistentPartial(t *testing.T) {
	for _, tc := range []struct {
		name, mode  string
		outputDelay time.Duration
	}{
		{name: "partial-eof", mode: "partial-eof"},
		{name: "partial-cancel", mode: "partial-cancel"},
		{name: "partial-deadline", mode: "partial-deadline"},
		// Deliberately exceed the former shared 600ms caller deadline before
		// emitting partial frames. Host scheduling must not decide EOF/cancel.
		{name: "partial-eof-delayed-output", mode: "partial-eof", outputDelay: 750 * time.Millisecond},
		{name: "partial-cancel-delayed-output", mode: "partial-cancel", outputDelay: 750 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mode := tc.mode
			f := newAlignmentClaudeFixture(t, true)
			if tc.outputDelay != 0 {
				f.cfg.Env = append(f.cfg.Env, driver.EnvBinding{Name: "ALIGNMENT_PARTIAL_OUTPUT_DELAY", Value: tc.outputDelay.String()})
			}
			store := memory.NewStore()
			a := f.agent(adaptor.WithThreadStore(store))
			defer a.Close(context.Background())
			th := a.Thread("partial")
			if _, err := th.Run(context.Background(), "warm"); err != nil {
				t.Fatal(err)
			}
			before, err := th.Checkpoint(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			// EOF comes from provider exit; cancellation comes from the observed
			// ready frame below. This watchdog only bounds a broken test and must
			// never count as either expected outcome.
			watchdogExpired := make(chan struct{})
			watchdog := time.AfterFunc(30*time.Second, func() {
				close(watchdogExpired)
				cancel()
			})
			defer watchdog.Stop()
			if mode == "partial-deadline" {
				// Only this case exercises a real caller deadline. Leave room for
				// fixture frames on a busy host; readiness remains mandatory.
				deadlineCtx, deadlineCancel := context.WithTimeout(ctx, 5*time.Second)
				defer deadlineCancel()
				ctx = deadlineCtx
			}
			stream := th.Stream(ctx, mode)
			sawReady := false
			var stdout strings.Builder
			for event := range stream.Events() {
				p, ok := event.(adaptor.ProcessInfo)
				if !ok || p.Kind != adaptor.ProcessStdout {
					continue
				}
				// Process chunks need not align with the complete readiness frame.
				stdout.Write(p.Bytes)
				if !sawReady && strings.Contains(stdout.String(), `{"type":"system","subtype":"partial_ready"}`+"\n") {
					sawReady = true
					if mode == "partial-cancel" {
						stream.Cancel()
						stream.Cancel()
					}
				}
			}
			result, err := stream.Result()
			select {
			case <-watchdogExpired:
				t.Fatalf("partial fixture watchdog expired: mode=%s ready=%t err=%v", mode, sawReady, err)
			default:
			}
			partial := alignmentClaudeResult(t, result, err)
			if err == nil {
				t.Fatal("interrupted provider succeeded")
			}
			if mode == "partial-cancel" && (!sawReady || ctx.Err() != nil || !errors.Is(err, context.Canceled)) {
				t.Fatalf("cancel cause lost: %v", err)
			}
			if mode == "partial-deadline" && (!sawReady || ctx.Err() != context.DeadlineExceeded || !errors.Is(err, context.DeadlineExceeded)) {
				t.Fatalf("deadline cause lost: %v", err)
			}
			if mode == "partial-eof" && (ctx.Err() != nil || !errors.Is(err, io.EOF) || partial.Raw().Terminal == nil) {
				t.Fatalf("EOF/terminal lost: %v raw=%#v", err, partial.Raw())
			}
			if !strings.Contains(partial.Raw().Stdout, alignmentClaudePartial) || partial.Raw().Stderr != "partial stderr\n" || len(partial.Transcript()) < 3 || partial.Usage == nil || partial.Usage.InputTokens != 7 || partial.Text != "" {
				t.Fatalf("partial protocol lost: %#v raw=%#v transcript=%#v", partial, partial.Raw(), partial.Transcript())
			}
			after, err := th.Checkpoint(context.Background())
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("healthy checkpoint changed: before=%#v after=%#v err=%v", before, after, err)
			}
			if len(f.lines(t, "SPAWN_FILE")) != 1 {
				t.Fatal("post-delivery prompt replayed")
			}
		})
	}
}

func TestAlignmentClaudeInteractiveNativeArgs(t *testing.T) {
	req := driver.Request{OutputSchema: &driver.OutputSchema{SchemaJSON: []byte(alignmentClaudeSchema)}, StructuredOutputSource: driver.StructuredOutputSourceNative}
	for _, streaming := range []bool{false, true} {
		req.Streaming = streaming
		args, err := buildClaudeExecArgs(Config{CommonConfig: CommonConfig{ExtraArgs: []string{"--output-format=json", "--json-schema", "{}", "--permission-prompt-tool=other", "--custom"}}}, req, true)
		if err != nil {
			t.Fatal(err)
		}
		joined := " " + strings.Join(args, " ") + " "
		for _, want := range []string{" --output-format stream-json ", " --json-schema ", " --input-format stream-json ", " --include-partial-messages ", " --replay-user-messages ", " --permission-prompt-tool stdio ", " --custom "} {
			if !strings.Contains(joined, want) {
				t.Fatalf("missing %q in %q", want, joined)
			}
		}
		if strings.Count(joined, "--output-format") != 1 || strings.Count(joined, "--json-schema") != 1 || strings.Contains(joined, "other") {
			t.Fatalf("conflicting transport args %q", joined)
		}
	}
}

func TestAlignmentClaudeApprovalFailures(t *testing.T) {
	for _, kind := range []string{"question", "plan"} {
		for _, action := range []string{"deny", "timeout", "cancel", "deny-continue", "timeout-continue"} {
			t.Run(kind+"/"+action, func(t *testing.T) {
				f := newAlignmentClaudeFixture(t, false)
				policy := alignmentClaudePolicy()
				policy.Approvals.Timeout = 40 * time.Millisecond
				if action == "deny-continue" {
					policy.Approvals.OnReject = adaptor.FallbackContinue
				}
				if action == "timeout-continue" {
					policy.Approvals.OnTimeout = adaptor.FallbackContinue
				}
				a := f.agent(adaptor.WithThreadStore(memory.NewStore()), adaptor.WithPolicy(policy))
				defer a.Close(context.Background())
				th := a.Thread("approval-errors")
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				schema := adaptor.WithSchemaJSON([]byte(alignmentClaudeSchema))
				// Establish a healthy record with the identical schema/policy fingerprint.
				if _, err := th.Run(ctx, "finish", schema, adaptor.WithSpawn()); err != nil {
					t.Fatal(err)
				}
				before, err := th.Checkpoint(ctx)
				if err != nil {
					t.Fatal(err)
				}
				stream := th.Stream(ctx, kind, schema, adaptor.WithSpawn())
				var request *adaptor.ApprovalRequest
				for event := range stream.Events() {
					if r, ok := event.(*adaptor.ApprovalRequest); ok {
						request = r
						if strings.HasPrefix(action, "deny") {
							if err := r.Deny(ctx, "try a smaller plan"); err != nil {
								t.Fatal(err)
							}
						}
						if action == "cancel" {
							stream.Cancel()
						}
					}
				}
				result, err := stream.Result()
				result = alignmentClaudeResult(t, result, err)
				if request == nil {
					t.Fatal("provider never requested actual approval")
				}
				if !errors.Is(request.Deny(ctx, "late"), adaptor.ErrApprovalResolved) {
					t.Fatal("expired responder accepted a duplicate")
				}
				if strings.HasSuffix(action, "continue") {
					if err != nil || result.Text != "完成" {
						t.Fatalf("continue failed: %v %#v", err, result)
					}
					replies := f.lines(t, "REPLIES_FILE")
					if len(replies) != 1 {
						t.Fatalf("deny wire missing %q", replies)
					}
					response := decodeControlResponseFrame(t, []byte(replies[0]))
					if response.Behavior != "deny" || response.Interrupt {
						t.Fatalf("wrong continue control=%#v", response)
					}
				} else {
					var re *adaptor.RunError
					if !errors.As(err, &re) {
						t.Fatalf("missing RunError: %v", err)
					}
					want := adaptor.ReasonApprovalDenied
					if action == "timeout" {
						want = adaptor.ReasonApprovalTimeout
					}
					if action == "cancel" {
						want = adaptor.ReasonCancelled
					}
					if re.Reason != want {
						t.Fatalf("reason=%v want %v (cause=%v)", re.Reason, want, re.Cause)
					}
					if !strings.Contains(result.Raw().Stdout, `"type":"control_request"`) || len(result.Transcript()) == 0 {
						t.Fatalf("approval partial output lost %#v", result.Raw())
					}
					after, checkpointErr := th.Checkpoint(ctx)
					if checkpointErr != nil || !reflect.DeepEqual(before, after) {
						t.Fatalf("aborted approval changed checkpoint: %v", checkpointErr)
					}
				}
			})
		}
	}
}

func TestAlignmentClaudeInvalidNativeCheckpoint(t *testing.T) {
	for _, mode := range []string{"invalid", "missing", "provider-error", "nonzero", "malformed", "truncated"} {
		t.Run(mode, func(t *testing.T) {
			f := newAlignmentClaudeFixture(t, false)
			a := f.agent(adaptor.WithThreadStore(memory.NewStore()), adaptor.WithPolicy(alignmentClaudePolicy()), adaptor.OnApproval(alignmentClaudeApprove))
			defer a.Close(context.Background())
			th := a.Thread("schema-checkpoint")
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			opts := []adaptor.CallOption{adaptor.WithSchemaJSON([]byte(alignmentClaudeSchema)), adaptor.WithSpawn()}
			if _, err := th.Run(ctx, "question plan", opts...); err != nil {
				t.Fatal(err)
			}
			before, err := th.Checkpoint(ctx)
			if err != nil {
				t.Fatal(err)
			}
			result, err := th.Run(ctx, "question plan "+mode, opts...)
			result = alignmentClaudeResult(t, result, err)
			if err == nil {
				t.Fatal("invalid terminal succeeded")
			}
			if result.Raw().Stdout == "" || len(result.Transcript()) == 0 || (mode != "truncated" && result.Raw().Terminal == nil) {
				t.Fatalf("invalid terminal lost partial output: %#v", result.Raw())
			}
			var out map[string]string
			if mode == "invalid" || mode == "missing" {
				if result.Decode(&out) == nil {
					t.Fatalf("invalid native output was validated: %#v", out)
				}
			}
			after, err := th.Checkpoint(ctx)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("invalid checkpoint overwrote healthy state: %v", err)
			}
		})
	}
}

func TestAlignmentClaudePartialResponseCause(t *testing.T) {
	cause := errors.New("transport disconnected after terminal")
	sink := alignmentStdinSink()
	p := newClaudeParser(sink)
	p.enableStreaming("partial")
	alignmentStdinFeed(t, p, alignmentClaudeInit, alignmentClaudePartial, alignmentClaudeTerminal)
	raw := driver.RawStreams{Stdout: alignmentClaudeInit + "\n" + alignmentClaudePartial + "\n" + alignmentClaudeTerminal + "\n", Stderr: "captured stderr", Terminal: p.terminal}
	req := driver.Request{OutputSchema: &driver.OutputSchema{SchemaJSON: []byte(alignmentClaudeSchema)}, StructuredOutputSource: driver.StructuredOutputSourceNative,
		Runtime: driver.RuntimePayload{Ensured: []driver.RuntimeServiceRef{{ID: "observed", Status: driver.RuntimeServiceRunning, Health: driver.RuntimeHealthHealthy}}}}
	result, err := buildClaudeResponse(req, p, raw, 0, "", false, "fixture-model", "/workspace", "profile", cause)
	if !errors.Is(err, cause) || result.Output != "完成" || !reflect.DeepEqual(*result.RawStreams, raw) || len(result.Transcript) != 3 || result.Usage == nil || result.Usage.InputTokens != 0 || len(result.RuntimeServices) != 1 || result.Checkpoint != nil || result.StructuredOutput != nil {
		t.Fatalf("error cleared/validated parser data: %#v err=%v", result, err)
	}
}

func TestAlignmentClaudeDescriptorSnapshot(t *testing.T) {
	d := Driver(Config{Model: "fixture-model"})
	first, second := d.Descriptor(), d.Descriptor()
	native, prompt := first.StructuredOutput.NativeHITL, first.StructuredOutput.PromptValidateHITL
	if native == nil || prompt == nil || native.Permission || !native.Question || !native.PlanReview || !prompt.Permission || !prompt.Question || !prompt.PlanReview || first.StructuredOutput.WorksWithHITL {
		t.Fatalf("false schema capability: %#v", first.StructuredOutput)
	}
	native.Question = false
	prompt.Permission = false
	if !second.StructuredOutput.NativeHITL.Question || !d.Descriptor().StructuredOutput.PromptValidateHITL.Permission {
		t.Fatal("descriptor leaked shared mutable matrix")
	}
}

func TestAlignmentClaudePublicFailureEquivalence(t *testing.T) {
	for _, mode := range []string{"provider-error", "nonzero"} {
		t.Run(mode, func(t *testing.T) {
			f := newAlignmentClaudeFixture(t, false)
			a := f.agent(adaptor.WithPolicy(alignmentClaudePolicy()), adaptor.OnApproval(alignmentClaudeApprove))
			defer a.Close(context.Background())
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			schema := adaptor.WithSchemaJSON([]byte(alignmentClaudeSchema))
			result, err := a.Run(ctx, "question plan "+mode, schema)
			run := alignmentClaudeResult(t, result, err)
			if err == nil {
				t.Fatal("failure returned success")
			}
			stream := a.Stream(ctx, "question plan "+mode, schema)
			for range stream.Events() {
			}
			result, err = stream.Result()
			streamed := alignmentClaudeResult(t, result, err)
			if err == nil {
				t.Fatal("stream failure returned success")
			}
			assertAlignmentClaudeEqual(t, run, streamed)
		})
	}
}

func TestAlignmentClaudeSafeFallback(t *testing.T) {
	f := newAlignmentClaudeFixture(t, false)
	a := f.agent(adaptor.WithThreadStore(memory.NewStore()))
	defer a.Close(context.Background())
	attempts := 0
	f.pool.spawnProcess = func(spec persistentSpec, _ driver.EventSink) (*liveProcess, error) {
		if spec.prompt != "" {
			attempts++
		}
		return nil, errors.New("before prompt")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := a.Thread("fallback").Run(ctx, "finish")
	if err != nil || result.Raw().Terminal == nil || len(f.lines(t, "SPAWN_FILE")) != 1 || attempts != 1 {
		t.Fatalf("safe fallback: attempts=%d result=%#v err=%v", attempts, result, err)
	}
}

func TestAlignmentClaudeNativeDecisionSinkDenial(t *testing.T) {
	for _, action := range []driver.FailureAction{driver.FailureAbort, driver.FailureContinue} {
		t.Run(string(action), func(t *testing.T) {
			f := newAlignmentClaudeFixture(t, false)
			sink := newFakeInteractiveSink(func(request driver.DecisionRequest) (driver.DecisionResponse, error) {
				return driver.DecisionResponse{RequestID: request.RequestID, Result: driver.DecisionRejected, Text: "smaller plan"}, nil
			})
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			response, err := (adapter{}).Run(ctx, driver.Request{RunID: "denied", Config: f.cfg, Prompt: "plan", Workspace: driver.WorkspaceLease{CWD: f.root},
				Policy:       driver.RunPolicy{HumanDecision: driver.HumanDecisionPolicy{Permission: driver.HumanDecisionAutoApprove, PlanReview: driver.HumanDecisionAsk, OnReject: action}},
				OutputSchema: &driver.OutputSchema{SchemaJSON: []byte(alignmentClaudeSchema)}, StructuredOutputSource: driver.StructuredOutputSourceNative}, sink)
			if err != nil || len(sink.requests) != 1 || response.RawStreams == nil || response.RawStreams.Terminal == nil {
				t.Fatalf("native denial failed: %#v %v", response, err)
			}
			replies := f.lines(t, "REPLIES_FILE")
			if len(replies) != 1 {
				t.Fatalf("missing real denial: %q", replies)
			}
			reply := decodeControlResponseFrame(t, []byte(replies[0]))
			if reply.Behavior != "deny" || reply.Message != "User rejected the plan. Correction hint: smaller plan" || reply.Interrupt != (action == driver.FailureAbort) {
				t.Fatalf("wrong denial wire: %#v", reply)
			}
			if action == driver.FailureAbort && (response.Failure == nil || response.Failure.Code != driver.FailureReject || response.Checkpoint != nil) {
				t.Fatalf("aborted denial became healthy: %#v", response)
			}
			if action == driver.FailureContinue && (response.Failure != nil || response.Checkpoint == nil || !response.Checkpoint.Valid) {
				t.Fatalf("continued denial lost healthy terminal: %#v", response)
			}
		})
	}
}

// This is a legal DecisionCapableSink: a returned error aborts the invocation,
// but it does not own (and need not cancel) the caller's context.
type alignmentClaudeAbortSink struct {
	*fakeInteractiveSink
	onRaw func(driver.RunEvent)
}

func (s *alignmentClaudeAbortSink) Emit(event driver.RunEvent) error {
	if s.onRaw != nil {
		s.onRaw(event)
	}
	return nil
}

type alignmentClaudeDecisionError struct{}

func (*alignmentClaudeDecisionError) Error() string { return "host decision aborted" }

func TestAlignmentClaudeResidentDecisionAbort(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		modes := []string{"no-context-cancel", "cancel-in-decision"}
		if terminal {
			modes = append(modes, "cancel-during-drain")
		}
		for _, mode := range modes {
			t.Run(fmt.Sprintf("terminal_%t/%s", terminal, mode), func(t *testing.T) {
				f := newAlignmentClaudeFixture(t, true)
				d := configuredDriver{adapter: adapter{persistent: f.pool}, cfg: f.cfg}
				defer d.CloseProcesses(context.Background())
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				cause := &alignmentClaudeDecisionError{}
				asked := make(chan struct{})
				sink := &alignmentClaudeAbortSink{fakeInteractiveSink: newFakeInteractiveSink(func(driver.DecisionRequest) (driver.DecisionResponse, error) {
					close(asked)
					if mode == "cancel-in-decision" {
						cancel()
					}
					return driver.DecisionResponse{}, cause
				})}
				if mode == "cancel-during-drain" {
					sink.onRaw = func(event driver.RunEvent) {
						if strings.Contains(string(event.Bytes), `"type":"result"`) {
							cancel()
						}
					}
				}
				type outcome struct {
					response driver.Response
					err      error
				}
				done := make(chan outcome, 1)
				prompt := "resident-abort"
				if terminal {
					prompt += "-terminal"
				}
				go func() {
					response, err := d.Run(ctx, driver.Request{RunID: "resident-abort", Prompt: prompt, Streaming: true,
						Workspace: driver.WorkspaceLease{CWD: f.root}, Session: &driver.SessionContext{EngineSessionID: "abort-thread"},
						Policy: driver.RunPolicy{HumanDecision: driver.HumanDecisionPolicy{Permission: driver.HumanDecisionAutoApprove, Question: driver.QuestionAsk}}}, sink)
					done <- outcome{response, err}
				}()
				select {
				case <-asked:
				case got := <-done:
					t.Fatalf("no real decision: %v", got.err)
				case <-time.After(5 * time.Second):
					cancel()
					t.Fatal("provider never requested decision")
				}
				var got outcome
				blocked := false
				select {
				case got = <-done:
				case <-time.After(1500 * time.Millisecond):
					blocked = true
					cancel()
					select {
					case got = <-done:
					case <-time.After(5 * time.Second):
						t.Fatal("resident failed to exit even after cleanup cancellation")
					}
				}
				if blocked {
					t.Fatalf("resident blocked after DecisionSink error until caller cancellation: original=%t err=%v", errors.Is(got.err, cause), got.err)
				}
				if mode == "no-context-cancel" && (ctx.Err() != nil || errors.Is(got.err, context.Canceled)) {
					t.Fatalf("abort invented caller cancellation: ctx=%v err=%v", ctx.Err(), got.err)
				}
				if mode != "no-context-cancel" && !errors.Is(got.err, context.Canceled) {
					t.Fatalf("context race cause lost: %v", got.err)
				}
				var typed *alignmentClaudeDecisionError
				if !errors.Is(got.err, cause) || !errors.As(got.err, &typed) || typed != cause {
					t.Fatalf("original decision cause lost: %v", got.err)
				}
				response := got.response
				if response.Checkpoint != nil || response.RawStreams == nil || !strings.Contains(response.RawStreams.Stdout, alignmentClaudePartial) || !strings.Contains(response.RawStreams.Stdout, alignmentClaudeQuestion) || response.RawStreams.Stderr != "abort stderr\n" || len(response.Transcript) < 3 || response.Usage == nil {
					t.Fatalf("abort lost partial response or accepted checkpoint: %#v", response)
				}
				if terminal {
					if response.RawStreams.Terminal == nil || string(response.RawStreams.Terminal.JSON) != alignmentClaudeTerminal || response.Output != "完成" || response.Usage.InputTokens != 0 {
						t.Fatalf("buffered terminal was not drained: %#v", response)
					}
				} else if response.RawStreams.Terminal != nil || response.Output != "" || response.Usage.InputTokens != 7 {
					t.Fatalf("missing terminal was fabricated: %#v", response)
				}
				if len(sink.requests) != 1 || len(f.lines(t, "SPAWN_FILE")) != 1 || len(f.lines(t, "REPLIES_FILE")) != 0 || f.pool.lookup("session-c04") != nil {
					t.Fatal("aborted resident was replayed, answered or reused")
				}
			})
		}
	}
}

func TestAlignmentClaudeResidentDecisionSuccessKeepsInput(t *testing.T) {
	f := newAlignmentClaudeFixture(t, true)
	a := f.agent(adaptor.WithThreadStore(memory.NewStore()), adaptor.WithPolicy(alignmentClaudePolicy()), adaptor.OnApproval(alignmentClaudeApprove))
	defer a.Close(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for i := 0; i < 2; i++ {
		result, err := a.Thread("resident-success").Run(ctx, "question plan")
		if err != nil || result.Raw().Terminal == nil {
			t.Fatalf("resident round %d: %#v %v", i, result, err)
		}
	}
	if len(f.lines(t, "SPAWN_FILE")) != 1 || len(f.lines(t, "REPLIES_FILE")) != 4 || f.pool.lookup("session-c04") == nil {
		t.Fatal("normal terminal closed/replaced resident stdin")
	}
}
