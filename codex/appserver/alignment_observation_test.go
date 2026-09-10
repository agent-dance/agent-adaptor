package appserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/capability"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/capabilityobs"
	"github.com/agent-dance/agent-adaptor/todo"
)

func alignmentFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture")
	if runtime.GOOS == "windows" {
		path += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", path, "../testdata/alignment-provider/main.go")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture build: %v %s", err, b)
	}
	return path
}
func alignmentOptions(command, capture string) Options {
	return Options{Command: command, Env: []driver.EnvBinding{{Name: "ALIGNMENT_CAPTURE", Value: capture}}, Prompt: "original prompt", RunID: "run"}
}
func alignmentFacts(sink *recordingSink) ([]capability.Invocation, []todo.Snapshot) {
	var caps []capability.Invocation
	var plans []todo.Snapshot
	for _, p := range sink.streams {
		if p.Capability != nil {
			caps = append(caps, *p.Capability)
		}
		if p.Todo != nil {
			plans = append(plans, *p.Todo)
		}
	}
	return caps, plans
}

func TestAlignmentAppServerWire(t *testing.T) {
	command := alignmentFixture(t)
	for _, resident := range []bool{false, true} {
		for _, mode := range []string{"start", "resume", "fork"} {
			for _, appendText := range []string{"", "甲\n\"乙\"\\\r\n🙂 "} {
				t.Run(mode+"/"+map[bool]string{true: "resident", false: "oneshot"}[resident]+"/"+map[bool]string{true: "empty", false: "append"}[appendText == ""], func(t *testing.T) {
					capture := filepath.Join(t.TempDir(), "capture.jsonl")
					opts := alignmentOptions(command, capture)
					opts.AppendSystemPrompt = appendText
					opts.CWD = t.TempDir()
					opts.Model = "model"
					opts.ServiceTier = "fast"
					opts.Sandbox = "read-only"
					opts.Approval = "never"
					if mode == "resume" {
						opts.ResumeThreadID = "parent"
					}
					if mode == "fork" {
						opts.ForkThreadID = "parent"
					}
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					sink := &recordingSink{}
					var result driver.Response
					var err error
					if resident {
						var p *Process
						p, err = Open(ctx, opts, sink)
						if err == nil {
							result, _, err = p.RunTurn(ctx, opts, sink)
							_ = p.TerminateAndWait(ctx)
						}
					} else {
						result, err = Run(ctx, opts, sink)
					}
					if err != nil || result.Checkpoint == nil || !result.Checkpoint.Valid {
						t.Fatalf("run: %v %#v", err, result)
					}
					if result.Output != "answer" || result.RawStreams == nil || !strings.Contains(result.RawStreams.Stdout, "turn/completed") || result.RawStreams.Terminal == nil || result.Usage == nil || result.Usage.InputTokens != 0 || len(result.Transcript) == 0 {
						t.Fatalf("audit lost: %#v", result)
					}
					data, err := os.ReadFile(capture)
					if err != nil {
						t.Fatal(err)
					}
					found := false
					for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
						var frame struct {
							Method string
							Params map[string]json.RawMessage
						}
						_ = json.Unmarshal([]byte(line), &frame)
						if frame.Method == "thread/"+mode {
							found = true
							var got string
							_ = json.Unmarshal(frame.Params["developerInstructions"], &got)
							if got != appendText {
								t.Fatalf("append mismatch")
							}
							_, present := frame.Params["developerInstructions"]
							if present != (appendText != "") {
								t.Fatal("empty not omitted")
							}
							if frame.Params["baseInstructions"] != nil {
								t.Fatal("replaced base")
							}
						}
						if frame.Method == "turn/start" {
							var inputs []UserInput
							_ = json.Unmarshal(frame.Params["input"], &inputs)
							if !reflect.DeepEqual(inputs, []UserInput{TextInput(opts.Prompt)}) {
								t.Fatalf("prompt changed: %#v", inputs)
							}
						}
					}
					if !found {
						t.Fatal("missing handshake")
					}
				})
			}
		}
	}
	t.Run("large-app-server", func(t *testing.T) {
		capture := filepath.Join(t.TempDir(), "capture")
		opts := alignmentOptions(command, capture)
		opts.AppendSystemPrompt = strings.Repeat("字", 12000)
		result, err := Run(context.Background(), opts, &recordingSink{})
		if err != nil || result.Failure != nil || result.Checkpoint == nil || !result.Checkpoint.Valid || result.Output != "answer" {
			t.Fatalf("large app-server payload: %v %#v", err, result)
		}
		data, err := os.ReadFile(capture)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			var frame struct {
				Method string
				Params struct{ DeveloperInstructions string }
			}
			if err := json.Unmarshal([]byte(line), &frame); err != nil {
				t.Fatal(err)
			}
			if frame.Method == "thread/start" {
				found = true
				if frame.Params.DeveloperInstructions != opts.AppendSystemPrompt {
					t.Fatal("large app-server payload changed")
				}
			}
		}
		if !found {
			t.Fatal("missing large app-server handshake")
		}
	})
	t.Run("rpc-facts", func(t *testing.T) {
		opts := alignmentOptions(command, filepath.Join(t.TempDir(), "capture"))
		opts.Env = append(opts.Env, driver.EnvBinding{Name: "ALIGNMENT_SCENARIO", Value: "facts"})
		opts.ResolvedMCP = []driver.MCPServerSpec{{Key: "中文__server"}}
		opts.ResolvedAgents = []driver.AgentSpec{{Key: "agent/reviewer", RuntimeName: "reviewer"}}
		opts.ResolvedSkills = []driver.ResolvedSkill{{Key: "skill/review", RuntimeName: "review", SourcePath: "/skills/review"}}
		opts.SkillInputs = []UserInput{{Type: UserInputKindSkill, Name: "review", Path: "/skills/review/SKILL.md"}}
		sink := &recordingSink{}
		result, err := Run(context.Background(), opts, sink)
		if err != nil {
			t.Fatal(err)
		}
		caps, plans := alignmentFacts(sink)
		if len(caps) != 6 || len(plans) != 2 || len(plans[1].Items) != 0 {
			t.Fatalf("caps=%#v plans=%#v", caps, plans)
		}
		for i := 0; i < len(caps); i += 2 {
			if caps[i].Phase != capability.Started || caps[i+1].Phase != capability.Completed || caps[i].InvocationID != caps[i+1].InvocationID {
				t.Fatal("lifecycle")
			}
		}
		if caps[0].Evidence != capability.NativeInputAccepted || caps[2].Ref.Key != "中文__server" || caps[4].Ref.Key != "agent/reviewer" {
			t.Fatalf("refs: %#v", caps)
		}
		b, _ := json.Marshal(caps)
		if strings.Contains(string(b), "dummy-") {
			t.Fatal("unsafe observation")
		}
		if !strings.Contains(result.RawStreams.Stdout, "dummy-secret") || !strings.Contains(result.RawStreams.Stdout, "dummy-result") {
			t.Fatal("Raw was redacted")
		}
	})
	t.Run("rejected-input", func(t *testing.T) {
		opts := alignmentOptions(command, filepath.Join(t.TempDir(), "capture"))
		opts.Env = append(opts.Env, driver.EnvBinding{Name: "ALIGNMENT_SCENARIO", Value: "reject-start"})
		opts.SkillInputs = []UserInput{{Type: UserInputKindSkill, Name: "review", Path: "/skills/review/SKILL.md"}}
		opts.ResolvedSkills = []driver.ResolvedSkill{{Key: "review", RuntimeName: "review", SourcePath: "/skills/review"}}
		sink := &recordingSink{}
		_, err := Run(context.Background(), opts, sink)
		caps, _ := alignmentFacts(sink)
		if err == nil || len(caps) != 0 {
			t.Fatalf("rejected input became evidence: %v %#v", err, caps)
		}
	})
}

func TestAlignmentPlanFenceAndAtomicSnapshots(t *testing.T) {
	sink := &recordingSink{}
	s := newRunState("run", sink)
	s.setThread("thread")
	send := func(thread, turn, plan string) {
		s.onNotification(NotifyTurnPlanUpdated, json.RawMessage(`{"threadId":"`+thread+`","turnId":"`+turn+`","plan":`+plan+`}`))
	}
	send("thread", "turn", `[]`)
	_, plans := alignmentFacts(sink)
	if len(plans) != 0 {
		t.Fatal("pre-RPC escaped")
	}
	s.setTurn("turn")
	plan := `[{"step":"第一\n步","status":"pending"},{"step":"second","status":"inProgress"},{"step":"third","status":"completed"}]`
	send("thread", "turn", plan)
	send("thread", "turn", plan)
	for _, invalid := range []string{`null`, `{}`, `[{"step":"bad","status":"unknown"}]`, `[{"step":"","status":"pending"}]`, `[{"step":1,"status":"pending"}]`, `[{"step":"ok","status":"pending"},{"step":"bad","status":"unknown"}]`} {
		send("thread", "turn", invalid)
	}
	_, plans = alignmentFacts(sink)
	if len(plans) != 2 || plans[0].Revision != 1 || plans[0].Items == nil || plans[1].Revision != 2 || !plans[1].Items[0].SyntheticID || plans[1].Items[1].Status != todo.InProgress {
		t.Fatalf("snapshots=%#v", plans)
	}
	plans[1].Items[0].Content = "mutated"
	send("thread", "turn", `[]`)
	_, plans = alignmentFacts(sink)
	if len(plans) != 3 || plans[2].Revision != 3 || plans[2].Items == nil {
		t.Fatal("clear/clone")
	}
	for _, coordinates := range [][2]string{{"other", "turn"}, {"thread", "old"}, {"", "turn"}, {"thread", ""}} {
		t.Run(coordinates[0]+coordinates[1], func(t *testing.T) {
			sink := &recordingSink{}
			s := newRunState("run", sink)
			s.setThread("thread")
			s.setTurn("turn")
			s.onNotification(NotifyTurnPlanUpdated, json.RawMessage(`{"threadId":"`+coordinates[0]+`","turnId":"`+coordinates[1]+`","plan":[]}`))
			_, got := alignmentFacts(sink)
			if len(got) != 0 || s.protocolError() == nil {
				t.Fatal("scope accepted")
			}
		})
	}
	s.onNotification(NotifyItemPlanDelta, json.RawMessage(`{"threadId":"thread","turnId":"turn","itemId":"plan","delta":"looks like a todo"}`))
	s.onNotification(NotifyTurnCompleted, json.RawMessage(`{"threadId":"thread","turn":{"id":"turn","status":"completed"}}`))
	send("thread", "turn", plan)
	_, got := alignmentFacts(sink)
	if len(got) != 3 {
		t.Fatal("terminal/delta escaped fence")
	}
	fresh := newRunState("next", &recordingSink{})
	if _, ok := fresh.observation.table.Snapshot(); ok {
		t.Fatal("new run inherited table")
	}
}

func TestAlignmentCapabilityFailClosedLifecycle(t *testing.T) {
	for _, scenario := range []string{"failed", "unknown", "ambiguous", "cancel", "interrupted", "no-start"} {
		t.Run(scenario, func(t *testing.T) {
			opts := Options{ResolvedMCP: []driver.MCPServerSpec{{Key: "mcp"}}}
			if scenario == "unknown" {
				opts.ResolvedMCP = nil
			}
			sink := &recordingSink{}
			s := newRunState("run", sink, opts)
			s.setThread("thread")
			s.setTurn("turn")
			if scenario == "ambiguous" {
				s.observation.catalog, _ = capabilityobs.NewCatalog([]capabilityobs.Entry{{Kind: capability.MCP, RuntimeName: "mcp", Key: "a"}, {Kind: capability.MCP, RuntimeName: "mcp", Key: "b"}})
			}
			send := func(method, status string) {
				s.onNotification(method, json.RawMessage(`{"threadId":"thread","turnId":"turn","item":{"id":"tool","type":"mcpToolCall","server":"mcp","tool":"read","status":"`+status+`","arguments":{"secret":"hidden"}}}`))
			}
			if scenario != "no-start" {
				send(NotifyItemStarted, "inProgress")
				send(NotifyItemStarted, "inProgress")
			}
			if scenario == "failed" {
				send(NotifyItemCompleted, "failed")
				send(NotifyItemCompleted, "failed")
			} else if scenario == "no-start" {
				send(NotifyItemCompleted, "completed")
			}
			var err error
			if scenario == "cancel" {
				err = context.Canceled
			}
			s.closeObservations(err)
			caps, _ := alignmentFacts(sink)
			if scenario == "unknown" || scenario == "ambiguous" || scenario == "no-start" {
				if len(caps) != 0 {
					t.Fatalf("unexpected facts %#v", caps)
				}
				return
			}
			want := capability.Interrupted
			if scenario == "failed" {
				want = capability.Failed
			}
			if scenario == "cancel" {
				want = capability.Cancelled
			}
			if len(caps) != 2 || caps[1].Phase != want {
				t.Fatalf("lifecycle %#v", caps)
			}
		})
	}
}

func TestAlignmentHistoricalUsageIsTheOnlyTurnException(t *testing.T) {
	usage := `{"threadId":"thread","turnId":"old","tokenUsage":{"total":{"inputTokens":999,"outputTokens":999,"cachedInputTokens":0,"reasoningOutputTokens":0,"totalTokens":1998},"last":{"inputTokens":999,"outputTokens":999,"cachedInputTokens":0,"reasoningOutputTokens":0,"totalTokens":1998}}}`
	s := newRunState("run", &recordingSink{})
	s.setThread("thread")
	s.setTurn("turn")
	s.onNotification(NotifyThreadTokenUsageUpdated, json.RawMessage(usage))
	if s.protocolError() != nil || s.usage != nil {
		t.Fatal("history polluted current usage")
	}
	for _, bad := range []string{strings.Replace(usage, `"threadId":"thread"`, `"threadId":"other"`, 1), strings.Replace(usage, `"inputTokens":999`, `"inputTokens":"bad"`, 1)} {
		s := newRunState("run", &recordingSink{})
		s.setThread("thread")
		s.setTurn("turn")
		s.onNotification(NotifyThreadTokenUsageUpdated, json.RawMessage(bad))
		if s.protocolError() == nil {
			t.Fatal("loosened scope/schema")
		}
	}
	s.onNotification(NotifyItemStarted, json.RawMessage(`{"threadId":"thread","turnId":"old","item":{"id":"x","type":"mcpToolCall","server":"mcp","tool":"read","status":"inProgress"}}`))
	if s.protocolError() == nil {
		t.Fatal("non-usage crossed turn")
	}
}

func TestAlignmentSubagentRequiresCurrentCollabAndUniqueRole(t *testing.T) {
	for _, scenario := range []string{"valid", "unknown", "ambiguous", "missing-role", "no-collab", "wrong-parent", "conflict", "invalid-status"} {
		t.Run(scenario, func(t *testing.T) {
			sink := &recordingSink{}
			s := newRunState("run", sink, Options{ResolvedAgents: []driver.AgentSpec{{Key: "canonical", RuntimeName: "reviewer"}}})
			s.setThread("parent")
			s.setTurn("turn")
			if scenario == "ambiguous" {
				s.observation.catalog, _ = capabilityobs.NewCatalog([]capabilityobs.Entry{{Kind: capability.Subagent, RuntimeName: "reviewer", Key: "one"}, {Kind: capability.Subagent, RuntimeName: "reviewer", Key: "two"}})
			}
			role := "reviewer"
			parent := "parent"
			if scenario == "unknown" {
				role = "unknown"
			}
			if scenario == "missing-role" {
				role = ""
			}
			if scenario == "wrong-parent" {
				parent = "other"
			}
			child := fmt.Sprintf(`{"thread":{"id":"child","agentRole":%q,"source":{"subAgent":{"thread_spawn":{"parent_thread_id":%q,"depth":1}}}}}`, role, parent)
			if scenario != "no-collab" {
				status := "inProgress"
				if scenario == "invalid-status" {
					status = "unknown"
				}
				s.onNotification(NotifyItemStarted, json.RawMessage(fmt.Sprintf(`{"threadId":"parent","turnId":"turn","item":{"id":"spawn","type":"collabAgentToolCall","tool":"spawnAgent","senderThreadId":"parent","receiverThreadIds":["child"],"status":%q}}`, status)))
			}
			s.onNotification(NotifyThreadStarted, json.RawMessage(child))
			receiver := "child"
			if scenario == "conflict" {
				receiver = "different"
			}
			s.onNotification(NotifyItemCompleted, json.RawMessage(fmt.Sprintf(`{"threadId":"parent","turnId":"turn","item":{"id":"spawn","type":"collabAgentToolCall","tool":"spawnAgent","senderThreadId":"parent","receiverThreadIds":[%q],"status":"completed"}}`, receiver)))
			s.closeObservations(nil)
			caps, _ := alignmentFacts(sink)
			if scenario == "valid" {
				if len(caps) != 2 || caps[1].Phase != capability.Completed || caps[0].ParentToolCallID != "" || caps[0].Ref.Key != "canonical" {
					t.Fatalf("facts=%#v", caps)
				}
			} else {
				for _, v := range caps {
					if v.Phase == capability.Completed {
						t.Fatal("unproved completed")
					}
				}
				if scenario != "conflict" && len(caps) != 0 {
					t.Fatal("unproved start")
				}
			}
		})
	}
}

func TestAlignmentProviderCancellationClosesPendingFacts(t *testing.T) {
	sink := &recordingSink{}
	s := newRunState("run", sink, Options{ResolvedMCP: []driver.MCPServerSpec{{Key: "mcp"}}})
	s.setThread("thread")
	s.setTurn("turn")
	s.onNotification(NotifyItemStarted, json.RawMessage(`{"threadId":"thread","turnId":"turn","item":{"id":"tool","type":"mcpToolCall","server":"mcp","tool":"read","status":"inProgress"}}`))
	s.finishPublicResult(driver.Response{Failure: &driver.RunFailure{Code: driver.FailureCancelled}}, nil)
	caps, _ := alignmentFacts(sink)
	if len(caps) != 2 || caps[1].Phase != capability.Cancelled || caps[1].ErrorCode != capability.RunCancelled {
		t.Fatalf("provider cancellation facts=%#v", caps)
	}
}

func TestAlignmentUsageRequiresCompleteObservedCounters(t *testing.T) {
	zero := `{"cachedInputTokens":0,"inputTokens":0,"outputTokens":0,"reasoningOutputTokens":0,"totalTokens":0}`
	complete := `{"last":` + zero + `,"total":` + zero + `}`
	for _, tc := range []struct {
		name, usage string
		valid       bool
	}{
		{"explicit-zero", complete, true},
		{"empty", `{}`, false},
		{"null", `null`, false},
		{"empty-breakdowns", `{"last":{},"total":{}}`, false},
		{"missing-last", `{"total":` + zero + `}`, false},
		{"null-counter", strings.Replace(complete, `"inputTokens":0`, `"inputTokens":null`, 1), false},
		{"missing-counter", strings.Replace(complete, `"reasoningOutputTokens":0,`, ``, 1), false},
		{"negative-counter", strings.Replace(complete, `"inputTokens":0`, `"inputTokens":-1`, 1), false},
		{"fractional-counter", strings.Replace(complete, `"inputTokens":0`, `"inputTokens":1.5`, 1), false},
	} {
		for _, turn := range []string{"current", "historical"} {
			t.Run(tc.name+"/"+turn, func(t *testing.T) {
				s := newRunState("run", &recordingSink{})
				s.setThread("thread")
				s.setTurn("current")
				s.onNotification(NotifyThreadTokenUsageUpdated, json.RawMessage(fmt.Sprintf(`{"threadId":"thread","turnId":%q,"tokenUsage":%s}`, turn, tc.usage)))
				result := s.snapshot(Options{}, "thread", "audit", "", 0, "", false)
				if (result.Usage != nil) != (tc.valid && turn == "current") || (s.protocolError() == nil) != tc.valid {
					t.Fatalf("usage=%#v protocolError=%v valid=%t", result.Usage, s.protocolError(), tc.valid)
				}
			})
		}
	}
}

func TestAlignmentResidentWaitCause(t *testing.T) {
	command := alignmentFixture(t)
	opts := alignmentOptions(command, filepath.Join(t.TempDir(), "capture"))
	opts.Env = append(opts.Env, driver.EnvBinding{Name: "ALIGNMENT_SCENARIO", Value: "nonzero"})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p, err := Open(ctx, opts, &recordingSink{})
	if err != nil {
		t.Fatal(err)
	}
	defer p.TerminateAndWait(ctx)
	result, sent, err := p.RunTurn(ctx, opts, &recordingSink{})
	var actual, returned *exec.ExitError
	if !errors.As(p.waitErr, &actual) || actual.ExitCode() != 9 {
		t.Fatalf("fixture Wait did not observe exit 9: %v", p.waitErr)
	}
	if !sent || !errors.As(err, &returned) || returned.ExitCode() != 9 || result.Checkpoint != nil || result.Output != "answer" || result.RawStreams.Stderr != "fixture-stderr" {
		t.Fatalf("lost Wait cause/partial: err=%v result=%#v", err, result)
	}
}

type alignmentEOFSink struct {
	recordingSink
	process       *Process
	release       string
	released      chan error
	triggerKind   driver.TranscriptKind
	beforeRelease func()
}

func (s *alignmentEOFSink) Emit(event driver.RunEvent) error {
	trigger := s.triggerKind
	if trigger == "" {
		trigger = driver.TranscriptResult
	}
	if event.Item != nil && event.Item.Kind == trigger {
		select {
		case <-s.process.stream.ReadDone():
		case <-time.After(2 * time.Second):
			return errors.New("fixture never observed stdout EOF")
		}
		// Only the ordinary transcript callback is synchronized. The fake
		// releases its own process later, without mutating production state.
		go func() {
			time.Sleep(25 * time.Millisecond)
			if s.beforeRelease != nil {
				s.beforeRelease()
			}
			s.released <- os.WriteFile(s.release, []byte("release"), 0600)
		}()
	}
	return s.recordingSink.Emit(event)
}

func TestAlignmentResidentEOFHealth(t *testing.T) {
	command := alignmentFixture(t)
	for _, tc := range []struct {
		name, status string
		malformed    bool
		cancel       bool
		wantError    bool
		wantFailure  bool
	}{
		{name: "completed", status: "completed"},
		{name: "missing-terminal", status: "missing", wantError: true},
		{name: "malformed-tail", status: "completed", malformed: true, wantError: true},
		{name: "provider-failed", status: "failed", wantFailure: true},
		{name: "provider-interrupted", status: "interrupted", wantFailure: true},
		{name: "cancel-during-drain", status: "completed", cancel: true, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := alignmentOptions(command, filepath.Join(t.TempDir(), "capture"))
			sink := &alignmentEOFSink{release: filepath.Join(t.TempDir(), "release"), released: make(chan error, 1), triggerKind: driver.TranscriptAssistant}
			opts.Env = append(opts.Env,
				driver.EnvBinding{Name: "ALIGNMENT_SCENARIO", Value: "terminal-eof"},
				driver.EnvBinding{Name: "ALIGNMENT_RELEASE", Value: sink.release},
				driver.EnvBinding{Name: "ALIGNMENT_EOF_EXIT", Value: "0"},
				driver.EnvBinding{Name: "ALIGNMENT_EOF_TERMINAL", Value: tc.status},
			)
			if tc.malformed {
				opts.Env = append(opts.Env, driver.EnvBinding{Name: "ALIGNMENT_EOF_MALFORMED", Value: "1"})
			}
			deadlineCtx, deadlineCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer deadlineCancel()
			ctx, cancel := context.WithCancelCause(deadlineCtx)
			defer cancel(nil)
			p, err := Open(ctx, opts, sink)
			if err != nil {
				t.Fatal(err)
			}
			defer p.TerminateAndWait(deadlineCtx)
			sink.process = p
			cancelCause := errors.New("host canceled during EOF drain")
			if tc.cancel {
				sink.beforeRelease = func() {
					// Observe actual transport shutdown before canceling. The peer
					// stays alive until release, so this gates the bounded drain.
					for !p.client.closed.Load() {
						select {
						case <-deadlineCtx.Done():
							return
						case <-time.After(time.Millisecond):
						}
					}
					cancel(cancelCause)
				}
			}
			result, sent, err := p.RunTurn(ctx, opts, sink)
			select {
			case releaseErr := <-sink.released:
				if releaseErr != nil {
					t.Fatal(releaseErr)
				}
			case <-deadlineCtx.Done():
				t.Fatal("EOF fixture was not released")
			}
			select {
			case <-p.Done():
			default:
				t.Fatal("result preceded actual exit/drain")
			}
			if !sent || p.waitErr != nil || result.Output != "answer" || result.RawStreams == nil || result.RawStreams.Stderr != "post-eof-stderr" {
				t.Fatalf("clean peer audit lost: sent=%t wait=%v err=%v result=%#v", sent, p.waitErr, err, result)
			}
			if (err != nil) != tc.wantError {
				t.Fatalf("error=%v wantError=%t", err, tc.wantError)
			}
			if tc.wantFailure && result.Failure == nil {
				t.Fatal("provider failure was lost")
			}
			if tc.cancel && (!errors.Is(err, context.Canceled) || !errors.Is(err, cancelCause)) {
				t.Fatalf("caller cancellation cause lost: %v", err)
			}
			if tc.malformed && (!strings.Contains(result.RawStreams.Stdout, "{broken") || !strings.Contains(err.Error(), "decode JSON-RPC stdout")) {
				t.Fatalf("malformed protocol cause/audit lost: %v %#v", err, result.RawStreams)
			}
			if tc.wantError || tc.wantFailure {
				if result.Checkpoint != nil {
					t.Fatal("unhealthy turn produced checkpoint")
				}
			} else if result.Checkpoint == nil || !result.Checkpoint.Valid || result.ExitCode != 0 || result.Failure != nil || result.RawStreams.Terminal == nil {
				t.Fatalf("successful clean EOF was rejected: err=%v result=%#v", err, result)
			}
		})
	}
}

func TestAlignmentTerminalEOFWaitsForObservedExit(t *testing.T) {
	command := alignmentFixture(t)
	opts := alignmentOptions(command, filepath.Join(t.TempDir(), "capture"))
	sink := &alignmentEOFSink{release: filepath.Join(t.TempDir(), "release"), released: make(chan error, 1)}
	opts.Env = append(opts.Env, driver.EnvBinding{Name: "ALIGNMENT_SCENARIO", Value: "terminal-eof"}, driver.EnvBinding{Name: "ALIGNMENT_RELEASE", Value: sink.release})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p, err := Open(ctx, opts, sink)
	if err != nil {
		t.Fatal(err)
	}
	defer p.TerminateAndWait(ctx)
	sink.process = p
	result, sent, err := p.RunTurn(ctx, opts, sink)
	select {
	case releaseErr := <-sink.released:
		if releaseErr != nil {
			t.Fatal(releaseErr)
		}
	case <-ctx.Done():
		t.Fatal("fixture terminal/EOF barrier was not reached")
	}
	var exitErr *exec.ExitError
	if !sent || !errors.As(err, &exitErr) || exitErr.ExitCode() != 7 || result.ExitCode != 7 || result.Checkpoint != nil || result.Output != "answer" || result.RawStreams.Terminal == nil || result.RawStreams.Stderr != "post-eof-stderr" {
		t.Fatalf("known EOF returned before real exit/drain: err=%v result=%#v", err, result)
	}
}

func TestAlignmentPlanRejectsBrokenUnicode(t *testing.T) {
	for _, plan := range []string{`[{"step":"\ud800","status":"pending"}]`, `[{"step":"\udc00","status":"pending"}]`, `[{"step":"` + string([]byte{0xff}) + `","status":"pending"}]`} {
		sink := &recordingSink{}
		s := newRunState("run", sink)
		s.setThread("thread")
		s.setTurn("turn")
		s.onNotification(NotifyTurnPlanUpdated, json.RawMessage(`{"threadId":"thread","turnId":"turn","plan":`+plan+`}`))
		_, plans := alignmentFacts(sink)
		if len(plans) != 0 {
			t.Fatal("repaired malformed Unicode")
		}
	}
	if !validObservationJSON([]byte(`{"step":"\ud83d\ude42 \\ud800"}`)) {
		t.Fatal("valid Unicode rejected")
	}
}
