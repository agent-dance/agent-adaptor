package claude

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/v2/driver"
)

type messageTestPort struct {
	mu      sync.Mutex
	pending []driver.MessageItem
	claims  []driver.Boundary
	windows []bool
	acks    map[string][]driver.MessageAck
	acked   chan struct{}
	onClaim func(driver.Boundary)
}

func newMessageTestPort(items ...driver.MessageItem) *messageTestPort {
	return &messageTestPort{pending: items, acks: make(map[string][]driver.MessageAck), acked: make(chan struct{}, 32)}
}
func (p *messageTestPort) Window(open bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.windows = append(p.windows, open)
}
func (*messageTestPort) Signal() <-chan struct{} { return nil }
func (p *messageTestPort) Claim(b driver.Boundary) []driver.MessageItem {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.claims = append(p.claims, b)
	if p.onClaim != nil {
		p.onClaim(b)
	}
	var items, pending []driver.MessageItem
	for _, item := range p.pending {
		if item.Timing == driver.TimingSoon || item.Timing == driver.TimingAfterTurn && b == driver.BoundaryTurnEnd {
			items = append(items, item)
		} else {
			pending = append(pending, item)
		}
	}
	p.pending = pending
	return items
}
func (p *messageTestPort) Ack(id string, ack driver.MessageAck) {
	p.mu.Lock()
	p.acks[id] = append(p.acks[id], ack)
	p.mu.Unlock()
	p.acked <- struct{}{}
}

type messageTestSink struct {
	mu          sync.Mutex
	events      []driver.RunEvent
	streams     []driver.StreamPayload
	decisions   int
	decisionErr error
}

func (s *messageTestSink) Emit(ev driver.RunEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, ev)
	return nil
}
func (s *messageTestSink) EmitStream(payload driver.StreamPayload) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.streams = append(s.streams, payload)
	return nil
}
func (s *messageTestSink) RequestDecision(_ context.Context, req driver.DecisionRequest) (driver.DecisionResponse, error) {
	s.mu.Lock()
	s.decisions++
	s.mu.Unlock()
	return driver.DecisionResponse{RequestID: req.RequestID, Result: driver.DecisionApproved}, s.decisionErr
}

type messageTestStdin struct {
	writes chan []byte
	write  func([]byte) error
}

func (w messageTestStdin) Write(b []byte) error {
	if w.write != nil {
		return w.write(b)
	}
	w.writes <- append([]byte(nil), b...)
	return nil
}
func (messageTestStdin) Close() error { return nil }

func messageHook(event, agent, parent string) map[string]any {
	return map[string]any{"type": "control_request", "request_id": "hook-" + event,
		"parent_tool_use_id": parent, "request": map[string]any{
			"subtype": "hook_callback", "callback_id": "agent-adaptor-" + event,
			"input": map[string]any{"hook_event_name": event, "agent_id": agent},
		}}
}
func messageHookOutput(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var frame map[string]any
	if err := json.Unmarshal(b, &frame); err != nil {
		t.Fatal(err)
	}
	response := claudeTopLevelObject(frame, "response")
	if claudeTopLevelString(frame, "type") != "control_response" || claudeTopLevelString(response, "subtype") != "success" {
		t.Fatalf("bad response: %s", b)
	}
	return claudeTopLevelObject(response, "response")
}

func newInitializedMessageTestSession(ctx context.Context, port driver.MessagePort) *messageSession {
	s := newMessageSession(ctx, port)
	s.resolveInitialization(nil)
	return s
}

func TestMessageToolHooks(t *testing.T) {
	for _, hook := range messageHooks[:4] {
		t.Run(hook.event, func(t *testing.T) {
			port := newMessageTestPort(driver.MessageItem{ID: "a", Text: " first\n"}, driver.MessageItem{ID: "b", Text: "second"})
			p := newClaudeParser(&messageTestSink{})
			s := newInitializedMessageTestSession(context.Background(), port)
			p.messages = s
			writes := make(chan []byte, 1)
			s.start(messageTestStdin{writes: writes}, func() {})
			defer s.finish(nil)
			p.handlePayload("", messageHook(hook.event, "", ""))
			output := messageHookOutput(t, <-writes)
			got := claudeTopLevelObject(output, "hookSpecificOutput")
			if got["hookEventName"] != hook.event || got["additionalContext"] != " first\n\n\nsecond" {
				t.Fatalf("output: %#v", output)
			}
			<-port.acked
			<-port.acked
			port.mu.Lock()
			defer port.mu.Unlock()
			for _, id := range []string{"a", "b"} {
				acks := port.acks[id]
				if len(acks) != 1 || !acks[0].Delivered || acks[0].Mechanism != "claude.hook."+hook.mechanism {
					t.Fatalf("acks=%#v", port.acks)
				}
			}
		})
	}
}

func TestMessageInitializeFrame(t *testing.T) {
	var frame struct {
		Type      string `json:"type"`
		RequestID string `json:"request_id"`
		Request   struct {
			Subtype string `json:"subtype"`
			Hooks   map[string][]struct {
				CallbackIDs []string `json:"hookCallbackIds"`
			} `json:"hooks"`
		} `json:"request"`
	}
	if err := json.Unmarshal(messageInitializeFrame(), &frame); err != nil {
		t.Fatal(err)
	}
	if frame.Type != "control_request" || frame.RequestID != "agent-adaptor-messages-initialize" || frame.Request.Subtype != "initialize" || len(frame.Request.Hooks) != 5 {
		t.Fatalf("initialize=%+v", frame)
	}
	for _, event := range []string{"UserPromptSubmit", "PreToolUse", "PostToolUse", "PostToolUseFailure", "Stop"} {
		hooks := frame.Request.Hooks[event]
		if len(hooks) != 1 || !reflect.DeepEqual(hooks[0].CallbackIDs, []string{"agent-adaptor-" + event}) {
			t.Fatalf("%s registration=%+v", event, hooks)
		}
	}
}

func TestMessageQueuedClaimsDoNotBlockStdoutAndAllFail(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "write-failure", true: "cancelled"}[cancelled], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			port := newMessageTestPort(driver.MessageItem{ID: "first", Text: "first"})
			p := newClaudeParser(&messageTestSink{})
			p.messages = newInitializedMessageTestSession(ctx, port)
			entered, release := make(chan struct{}), make(chan struct{})
			writes := 0
			p.messages.start(messageTestStdin{write: func([]byte) error { writes++; close(entered); <-release; return io.ErrClosedPipe }}, func() {})
			if err := p.onChunk("stdout", messageFrame(messageHook("PreToolUse", "", "")), time.Time{}); err != nil {
				t.Fatal(err)
			}
			<-entered
			port.mu.Lock()
			port.pending = append(port.pending, driver.MessageItem{ID: "second", Text: "second"})
			port.mu.Unlock()
			// The first pipe write is blocked. Parsing and claiming the next
			// callback must still finish synchronously on this goroutine.
			if err := p.onChunk("stdout", messageFrame(messageHook("PostToolUse", "", "")), time.Time{}); err != nil {
				t.Fatal(err)
			}
			port.mu.Lock()
			pending, acked := len(port.pending), len(port.acks)
			port.mu.Unlock()
			if pending != 0 || acked != 0 {
				t.Fatalf("pending=%d acked=%d", pending, acked)
			}
			if cancelled {
				cancel()
			}
			close(release)
			<-port.acked
			<-port.acked
			p.messages.finish(io.EOF)
			if writes != 1 {
				t.Fatalf("wrote %d times after pipe failure", writes)
			}
			for _, id := range []string{"first", "second"} {
				acks := port.acks[id]
				if len(acks) != 1 || acks[0].Delivered || acks[0].Err == nil || acks[0].Requeue == cancelled {
					t.Fatalf("%s ack=%+v", id, acks)
				}
			}
		})
	}
}

func TestMessageUnregisteredCallbackClaimsNothing(t *testing.T) {
	port := newMessageTestPort(driver.MessageItem{ID: "pending", Text: "pending"})
	p := newClaudeParser(&messageTestSink{})
	p.messages = newInitializedMessageTestSession(context.Background(), port)
	writes := make(chan []byte, 1)
	p.messages.start(messageTestStdin{writes: writes}, func() {})
	defer p.messages.finish(nil)
	frame := messageHook("PreToolUse", "", "")
	claudeTopLevelObject(frame, "request")["callback_id"] = "another-integration"
	p.handlePayload("", frame)
	if out := messageHookOutput(t, <-writes); len(out) != 0 {
		t.Fatalf("output=%v", out)
	}
	if len(port.pending) != 1 || len(port.claims) != 0 || len(port.windows) != 0 {
		t.Fatalf("unexpected ownership: %+v", port)
	}
}

func TestMessageWindowAndSubagents(t *testing.T) {
	port := newMessageTestPort()
	p := newClaudeParser(&messageTestSink{})
	s := newInitializedMessageTestSession(context.Background(), port)
	p.messages = s
	writes := make(chan []byte, 8)
	s.start(messageTestStdin{writes: writes}, func() {})
	defer s.finish(nil)
	for _, frame := range []map[string]any{messageHook("PreToolUse", "worker", ""), messageHook("Stop", "", "parent"), messageHook("PreToolUse", " ", ""), messageHook("Stop", "", " ")} {
		p.handlePayload("", frame)
		if out := messageHookOutput(t, <-writes); len(out) != 0 {
			t.Fatalf("subagent output=%v", out)
		}
	}
	if len(port.claims) != 0 || len(port.windows) != 0 {
		t.Fatalf("subagent claimed or opened window: %#v", port)
	}
	p.handlePayload("", messageHook("UserPromptSubmit", "", ""))
	<-writes
	p.handlePayload("", messageHook("PreToolUse", "", ""))
	<-writes
	p.handlePayload("", messageHook("Stop", "", ""))
	<-writes
	if !reflect.DeepEqual(port.windows, []bool{true, false}) {
		t.Fatalf("windows=%v", port.windows)
	}
	if !reflect.DeepEqual(port.claims, []driver.Boundary{driver.BoundaryTool, driver.BoundaryTool, driver.BoundaryStop, driver.BoundaryTurnEnd}) {
		t.Fatalf("claims=%v", port.claims)
	}
}

func TestMessageClaimsAwaitWriteAndFailExactlyOnce(t *testing.T) {
	for _, mode := range []string{"failure", "cancel", "cancel-write-wins", "exit-before-write"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			port := newMessageTestPort(driver.MessageItem{ID: "a", Text: "first"})
			p := newClaudeParser(&messageTestSink{})
			s := newInitializedMessageTestSession(ctx, port)
			p.messages = s
			entered, release := make(chan struct{}), make(chan struct{})
			want := errors.New("pipe write failed")
			// Claim before starting the writer also covers process death before
			// an owned frame ever reaches the pipe.
			p.handlePayload("", messageHook("PreToolUse", "", ""))
			if len(port.acks) != 0 || len(port.pending) != 0 {
				t.Fatal("claim did not synchronously transfer ownership")
			}
			if mode == "exit-before-write" {
				s.mu.Lock()
				s.ended = true
				s.err = io.EOF
				s.mu.Unlock()
			}
			s.start(messageTestStdin{write: func([]byte) error {
				close(entered)
				<-release
				if mode == "cancel-write-wins" {
					return nil
				}
				return want
			}}, func() {})
			if mode != "exit-before-write" {
				<-entered
				if len(port.acks) != 0 {
					t.Fatal("acked before write completed")
				}
				if strings.HasPrefix(mode, "cancel") {
					cancel()
				}
				close(release)
			}
			<-port.acked
			s.finish(io.EOF)
			acks := port.acks["a"]
			if len(acks) != 1 {
				t.Fatalf("acks=%v", acks)
			}
			ack := acks[0]
			if mode == "cancel-write-wins" {
				if !ack.Delivered {
					t.Fatalf("write won: %+v", ack)
				}
				return
			}
			if ack.Delivered || ack.Err == nil || ack.Requeue != (mode == "failure" || mode == "exit-before-write") {
				t.Fatalf("ack=%+v", ack)
			}
		})
	}
}

func TestMessageResolverTransport(t *testing.T) {
	port := newMessageTestPort()
	cfg := Config{CommonConfig: CommonConfig{Command: versionCommand(t, "2.1.110 (Claude Code)", "exit 99")}}
	for _, tc := range []struct {
		name    string
		a       adapter
		req     driver.Request
		enabled bool
	}{
		{name: "plain"},
		{name: "streaming-output-only", req: driver.Request{Streaming: true}},
		{name: "message-switch", req: driver.Request{Messages: port}, enabled: true},
		{name: "hitl", req: driver.Request{Policy: driver.RunPolicy{HumanDecision: driver.HumanDecisionPolicy{Permission: driver.HumanDecisionAsk}}}, enabled: true},
		{name: "resident", a: adapter{persistent: newPersistentPool()}, req: driver.Request{Session: &driver.SessionContext{EngineSessionID: "record"}}, enabled: true},
		{name: "spawn", a: adapter{persistent: newPersistentPool()}, req: driver.Request{Spawn: true, Session: &driver.SessionContext{EngineSessionID: "record"}}},
		{name: "native", req: driver.Request{Messages: port, OutputSchema: &driver.OutputSchema{}, StructuredOutputSource: driver.StructuredOutputSourceNative}},
		{name: "native-streaming", req: driver.Request{Messages: port, Streaming: true, OutputSchema: &driver.OutputSchema{}, StructuredOutputSource: driver.StructuredOutputSourceNative}, enabled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.req.Config = cfg
			got := tc.a.ResolveMessageCapability(tc.req)
			want := driver.MessageCapability{}
			if tc.enabled {
				want = claudeMessageCapability()
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("capability=%+v want=%+v", got, want)
			}
		})
	}
	d := Driver(Config{MaxTurnsPerRun: 1}).(driver.MessageCapabilityResolver)
	if got := d.ResolveMessageCapability(driver.Request{Session: &driver.SessionContext{EngineSessionID: "record"}}); got.Continue != driver.ContinueResume {
		t.Fatalf("lost captured config: %+v", got)
	}
	if got := (adapter{}).Descriptor().Messages; !reflect.DeepEqual(got, claudeMessageCapability()) {
		t.Fatalf("descriptor=%+v", got)
	}
}

const messageHelperEnv = "AGENT_ADAPTOR_CLAUDE_MESSAGE_HELPER"

func messageHelper() int {
	if os.Getenv("MESSAGE_MODE") == "empty" && !claudePersistentInputMode(os.Args[1:]) {
		_, _ = io.Copy(io.Discard, os.Stdin)
		emitEmptyMessageTurn(json.NewEncoder(os.Stdout))
		return 0
	}
	dec := json.NewDecoder(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	read := func() (map[string]any, bool) { var v map[string]any; err := dec.Decode(&v); return v, err == nil }
	var initialized bool
	for {
		frame, ok := read()
		if !ok {
			return 0
		}
		if claudeTopLevelString(frame, "type") == "control_request" {
			request := claudeTopLevelObject(frame, "request")
			hooks := claudeTopLevelObject(request, "hooks")
			if len(hooks) != 5 || initialized {
				return 20
			}
			for _, hook := range messageHooks {
				if _, ok := hooks[hook.event]; !ok {
					return 21
				}
			}
			if os.Getenv("MESSAGE_MODE") == "reject-init" {
				_ = enc.Encode(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "error", "request_id": messageInitializeID, "error": "hooks disabled"}})
			} else if os.Getenv("MESSAGE_MODE") == "exit-init" {
				return 29
			} else if os.Getenv("MESSAGE_MODE") != "ignore-init" {
				if _, err := acknowledgeClaudeInitialize(frame, enc); err != nil {
					return 22
				}
			}
			initialized = true
			continue
		}
		if claudeTopLevelString(frame, "type") != "user" {
			return 23
		}
		if !initialized && os.Getenv("MESSAGE_MODE") != "empty" {
			_ = enc.Encode(map[string]any{"type": "result", "subtype": "success", "is_error": false, "session_id": "messages-session", "result": "disabled"})
			return 0
		}
		mode := os.Getenv("MESSAGE_MODE")
		switch mode {
		case "reject-init", "ignore-init", "empty":
			if initialized {
				// An unanswered registration may still respond after timeout.
				// Neither that success nor a stale callback may claim messages
				// once the driver has chosen its fallback for this process.
				if mode == "ignore-init" {
					_ = enc.Encode(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": messageInitializeID, "response": map[string]any{}}})
				}
				_ = enc.Encode(messageHook("PreToolUse", "", ""))
				v, ok := read()
				if !ok || len(claudeTopLevelObject(claudeTopLevelObject(v, "response"), "response")) != 0 {
					return 30
				}
			}
			if os.Getenv("MESSAGE_HITL") == "1" {
				_ = enc.Encode(map[string]any{"type": "control_request", "request_id": "permission", "request": map[string]any{"subtype": "can_use_tool", "tool_name": "Bash", "tool_use_id": "tool", "input": map[string]any{"command": "pwd"}}})
				v, ok := read()
				if !ok || claudeTopLevelObject(claudeTopLevelObject(v, "response"), "response")["behavior"] != "allow" {
					return 26
				}
			}
			emitEmptyMessageTurn(enc)
			continue
		case "cancel":
			_ = enc.Encode(messageHook("PreToolUse", "", ""))
			_, _ = io.Copy(io.Discard, os.Stdin)
			return 0
		case "exit":
			_, _ = io.WriteString(os.Stderr, "process failed\n")
			return 7
		case "malformed":
			_, _ = io.WriteString(os.Stdout, "{invalid json}\n")
			return 0
		case "missing":
			_ = enc.Encode(map[string]any{"type": "system", "subtype": "init", "session_id": "init-only"})
			return 0
		case "error":
			_ = enc.Encode(map[string]any{"type": "error", "message": "provider failure"})
			_, _ = io.Copy(io.Discard, os.Stdin)
			return 0
		case "late-exit", "no-checkpoint":
			terminal := map[string]any{"type": "result", "subtype": "success", "is_error": false, "result": "partial"}
			if mode == "late-exit" {
				terminal["session_id"] = "unsafe"
			}
			_ = enc.Encode(terminal)
			if mode == "late-exit" {
				return 8
			}
			return 0
		}
		hook := func(event, agent, parent string) map[string]any {
			_ = enc.Encode(messageHook(event, agent, parent))
			v, ok := read()
			if !ok {
				return nil
			}
			return claudeTopLevelObject(claudeTopLevelObject(v, "response"), "response")
		}
		for _, ids := range [][2]string{{"worker", ""}, {"", "parent"}} {
			if output := hook("PreToolUse", ids[0], ids[1]); output == nil || len(output) != 0 {
				return 24
			}
		}
		if output := hook("UserPromptSubmit", "", ""); claudeTopLevelObject(output, "hookSpecificOutput")["additionalContext"] != "soon" {
			return 25
		}
		if os.Getenv("MESSAGE_HITL") == "1" {
			_ = enc.Encode(map[string]any{"type": "control_request", "request_id": "permission", "request": map[string]any{"subtype": "can_use_tool", "tool_name": "Bash", "tool_use_id": "tool", "input": map[string]any{"command": "pwd"}}})
			response, ok := read()
			if !ok || claudeTopLevelObject(claudeTopLevelObject(response, "response"), "response")["behavior"] != "allow" {
				return 26
			}
		}
		_ = enc.Encode(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{map[string]any{"type": "text", "text": "first segment"}}}})
		// A terminal model message_stop precedes the Stop callback. The hook
		// transport must keep stdin open at this point.
		_ = enc.Encode(map[string]any{"type": "stream_event", "event": map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn"}}})
		_ = enc.Encode(map[string]any{"type": "stream_event", "event": map[string]any{"type": "message_stop"}})
		if output := hook("Stop", "", ""); output["decision"] != "block" || output["reason"] != "after" {
			return 27
		}
		_ = enc.Encode(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{map[string]any{"type": "text", "text": "last segment"}}}})
		if output := hook("Stop", "", ""); output == nil || len(output) != 0 {
			return 28
		}
		terminal := map[string]any{"type": "result", "subtype": "success", "is_error": false, "session_id": "messages-session", "result": "last segment", "usage": map[string]any{"input_tokens": 7, "output_tokens": 9}}
		if mode == "schema" {
			terminal["structured_output"] = map[string]any{"ok": true}
		}
		_ = enc.Encode(terminal)
	}
}

func emitEmptyMessageTurn(enc *json.Encoder) {
	_ = enc.Encode(map[string]any{"type": "system", "subtype": "init", "session_id": "messages-session", "model": "fake"})
	_ = enc.Encode(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{map[string]any{"type": "text", "text": "unchanged"}}}})
	_ = enc.Encode(map[string]any{"type": "stream_event", "event": map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn"}}})
	_ = enc.Encode(map[string]any{"type": "stream_event", "event": map[string]any{"type": "message_stop"}})
	_ = enc.Encode(map[string]any{"type": "result", "subtype": "success", "is_error": false, "session_id": "messages-session", "result": "unchanged", "usage": map[string]any{"input_tokens": 7, "output_tokens": 9}})
}

func TestMessageProcessLifecycleEquivalence(t *testing.T) {
	for _, resident := range []bool{false, true} {
		for _, hitl := range []bool{false, true} {
			t.Run(strings.Join([]string{map[bool]string{false: "per-run", true: "resident"}[resident], map[bool]string{false: "hooks", true: "hitl"}[hitl]}, "/"), func(t *testing.T) {
				home := t.TempDir()
				command, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				env := []driver.EnvBinding{{Name: messageHelperEnv, Value: "1"}, {Name: "CLAUDE_CONFIG_DIR", Value: home}}
				if hitl {
					env = append(env, driver.EnvBinding{Name: "MESSAGE_HITL", Value: "1"})
				}
				d := Driver(Config{CommonConfig: CommonConfig{Command: command, CWD: home, Env: env}}).(configuredDriver)
				defer d.CloseProcesses(context.Background())
				var resume string
				for i := 0; i < 2; i++ {
					port := newMessageTestPort(driver.MessageItem{ID: "soon", Text: "soon"}, driver.MessageItem{ID: "after", Text: "after", Timing: driver.TimingAfterTurn})
					sink := &messageTestSink{}
					req := driver.Request{RunID: "messages-run", Prompt: "initial", Messages: port, Streaming: hitl, Workspace: driver.WorkspaceLease{CWD: home}}
					if resident {
						req.Session = &driver.SessionContext{EngineSessionID: "record", State: &driver.SessionState{ResumeID: resume}}
					}
					if hitl {
						req.Policy.HumanDecision.Permission = driver.HumanDecisionAsk
					}
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					response, err := d.Run(ctx, req, sink)
					cancel()
					if err != nil {
						t.Fatalf("Run: %v", err)
					}
					if response.Output != "last segment" || response.Failure != nil || response.ExitCode != 0 || response.Checkpoint == nil || !response.Checkpoint.Valid {
						t.Fatalf("response=%+v raw=%+v", response, response.RawStreams)
					}
					resume = response.Checkpoint.State.ResumeID
					if response.Usage == nil || response.Usage.InputTokens != 7 || response.Usage.OutputTokens != 9 {
						t.Fatalf("usage=%+v", response.Usage)
					}
					if response.RawStreams.Terminal == nil || !strings.Contains(response.RawStreams.Stdout, "first segment") || !strings.Contains(response.RawStreams.Stdout, "last segment") {
						t.Fatalf("raw=%+v", response.RawStreams)
					}
					var turns []*driver.TurnCompleted
					var assistants []string
					spawns := 0
					for _, ev := range sink.events {
						if ev.Type == driver.RunEventSpawn {
							spawns++
						}
						if ev.Turn != nil {
							turns = append(turns, ev.Turn)
						}
					}
					wantSpawns := 1
					if resident && i == 1 {
						wantSpawns = 0
					}
					if spawns != wantSpawns {
						t.Fatalf("spawns=%d want %d", spawns, wantSpawns)
					}
					for _, item := range response.Transcript {
						if item.Kind == driver.TranscriptAssistant {
							assistants = append(assistants, item.Text)
						}
					}
					if len(turns) != 2 || turns[0].Index != 0 || turns[0].Text != "first segment" || turns[1].Index != 1 || turns[1].Text != "last segment" || turns[1].Usage == nil {
						t.Fatalf("turns=%+v", turns)
					}
					if !reflect.DeepEqual(assistants, []string{"first segment", "last segment"}) {
						t.Fatalf("transcript=%v", assistants)
					}
					if !reflect.DeepEqual(port.windows, []bool{true, false}) {
						t.Fatalf("windows=%v", port.windows)
					}
					for _, id := range []string{"soon", "after"} {
						if len(port.acks[id]) != 1 || !port.acks[id][0].Delivered {
							t.Fatalf("acks=%+v", port.acks)
						}
					}
					if port.acks["after"][0].Mechanism != "claude.hook.stop" {
						t.Fatalf("stop ack=%+v", port.acks)
					}
					if hitl && sink.decisions != 1 {
						t.Fatalf("decisions=%d", sink.decisions)
					}
				}
			})
		}
	}
}

func TestMessageDisabledDoesNotInitialize(t *testing.T) {
	home := t.TempDir()
	command, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	sink := &messageTestSink{}
	req := driver.Request{Prompt: "plain", Config: Config{CommonConfig: CommonConfig{Command: command, CWD: home, Env: []driver.EnvBinding{{Name: messageHelperEnv, Value: "1"}, {Name: "CLAUDE_CONFIG_DIR", Value: home}}}}}
	// Explicit HITL keeps stream-json input enabled while message hooks are off.
	req.Policy.HumanDecision.Permission = driver.HumanDecisionAsk
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	response, err := (adapter{}).Run(ctx, req, sink)
	if err != nil || response.Output != "disabled" {
		t.Fatalf("response=%+v error=%v", response, err)
	}
	var turns int
	for _, ev := range sink.events {
		if ev.Turn != nil {
			turns++
			if ev.Turn.Index != 0 || ev.Turn.Text != "disabled" {
				t.Fatalf("turn=%+v", ev.Turn)
			}
		}
	}
	if turns != 1 {
		t.Fatalf("turn count=%d", turns)
	}
}

func TestMessageProcessFailureOutputAndCheckpoint(t *testing.T) {
	for _, mode := range []string{"exit", "malformed", "missing", "no-checkpoint", "late-exit", "error", "exit-init"} {
		t.Run(mode, func(t *testing.T) {
			home := t.TempDir()
			command, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			req := driver.Request{Prompt: "failure", Messages: newMessageTestPort(), Config: Config{CommonConfig: CommonConfig{Command: command, CWD: home, Env: []driver.EnvBinding{{Name: messageHelperEnv, Value: "1"}, {Name: "CLAUDE_CONFIG_DIR", Value: home}, {Name: "MESSAGE_MODE", Value: mode}}}}}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			response, err := (adapter{}).Run(ctx, req, &messageTestSink{})
			if mode == "exit-init" {
				if err == nil || !strings.Contains(err.Error(), "process exited during hooks initialization") {
					t.Fatalf("error=%v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if response.Checkpoint != nil && response.Checkpoint.Valid {
				t.Fatalf("unsafe checkpoint: %+v", response.Checkpoint)
			}
			if response.RawStreams == nil || !strings.Contains(response.RawStreams.Stdout, "control_response") {
				t.Fatalf("lost raw output: %+v", response.RawStreams)
			}
			if mode == "exit" {
				if response.ExitCode != 7 || !strings.Contains(response.RawStreams.Stderr, "process failed") {
					t.Fatalf("exit response=%+v", response)
				}
				return
			}
			if mode == "late-exit" {
				if response.ExitCode != 8 || response.Output != "partial" || response.RawStreams.Terminal == nil {
					t.Fatalf("exit response=%+v", response)
				}
				return
			}
			if response.Failure == nil {
				t.Fatalf("missing protocol failure: %+v", response)
			}
		})
	}
}

func TestMessageStreamingSchemaRetainsNativeOutput(t *testing.T) {
	home := t.TempDir()
	command, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	req := driver.Request{Prompt: "schema", Streaming: true,
		Messages:     newMessageTestPort(driver.MessageItem{ID: "soon", Text: "soon"}, driver.MessageItem{ID: "after", Text: "after", Timing: driver.TimingAfterTurn}),
		OutputSchema: &driver.OutputSchema{Format: driver.OutputFormatJSONSchema, SchemaJSON: json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"],"additionalProperties":false}`)}, StructuredOutputSource: driver.StructuredOutputSourceNative,
		Config: Config{CommonConfig: CommonConfig{Command: command, CWD: home, Env: []driver.EnvBinding{{Name: messageHelperEnv, Value: "1"}, {Name: "CLAUDE_CONFIG_DIR", Value: home}, {Name: "MESSAGE_MODE", Value: "schema"}}}}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	response, err := (adapter{}).Run(ctx, req, &messageTestSink{})
	if err != nil || response.Failure != nil || response.StructuredOutput == nil || !response.StructuredOutput.Valid || response.StructuredOutput.Source != driver.StructuredOutputSourceNative {
		t.Fatalf("response=%+v error=%v", response, err)
	}
}

func TestMessageHITLDecisionErrorPreserved(t *testing.T) {
	for _, resident := range []bool{false, true} {
		t.Run(map[bool]string{false: "per-run", true: "resident"}[resident], func(t *testing.T) {
			home := t.TempDir()
			command, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			d := Driver(Config{CommonConfig: CommonConfig{Command: command, CWD: home, Env: []driver.EnvBinding{{Name: messageHelperEnv, Value: "1"}, {Name: "CLAUDE_CONFIG_DIR", Value: home}, {Name: "MESSAGE_HITL", Value: "1"}}}}).(configuredDriver)
			defer d.CloseProcesses(context.Background())
			port := newMessageTestPort(driver.MessageItem{ID: "soon", Text: "soon"})
			req := driver.Request{Prompt: "decision error", Messages: port, Policy: driver.RunPolicy{HumanDecision: driver.HumanDecisionPolicy{Permission: driver.HumanDecisionAsk}}}
			if resident {
				req.Session = &driver.SessionContext{EngineSessionID: "record"}
			}
			wantErr := errors.New("approval callback failed")
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			sink := &messageTestSink{decisionErr: wantErr}
			_, err = d.Run(ctx, req, sink)
			if !errors.Is(err, wantErr) {
				t.Fatalf("error=%v want %v", err, wantErr)
			}
			var terminals int
			for _, ev := range sink.streams {
				if ev.Kind == driver.StreamRunError {
					terminals++
				}
			}
			if terminals != 1 {
				t.Fatalf("error terminal count=%d", terminals)
			}
		})
	}
}

func TestMessageProcessCancellation(t *testing.T) {
	for _, resident := range []bool{false, true} {
		t.Run(map[bool]string{false: "per-run", true: "resident"}[resident], func(t *testing.T) {
			home := t.TempDir()
			command, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			d := Driver(Config{CommonConfig: CommonConfig{Command: command, CWD: home, Env: []driver.EnvBinding{{Name: messageHelperEnv, Value: "1"}, {Name: "CLAUDE_CONFIG_DIR", Value: home}, {Name: "MESSAGE_MODE", Value: "cancel"}}}}).(configuredDriver)
			defer d.CloseProcesses(context.Background())
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			port := newMessageTestPort(driver.MessageItem{ID: "cancelled", Text: "pending"})
			port.onClaim = func(driver.Boundary) { cancel() }
			req := driver.Request{Prompt: "cancel", Messages: port}
			if resident {
				req.Session = &driver.SessionContext{EngineSessionID: "record"}
			}
			response, err := d.Run(ctx, req, &messageTestSink{})
			if resident {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("error=%v", err)
				}
			} else {
				if err != nil || response.RawStreams == nil || response.ExitCode == 0 || response.Checkpoint != nil {
					t.Fatalf("response=%+v error=%v", response, err)
				}
			}
			acks := port.acks["cancelled"]
			if len(acks) != 1 || acks[0].Delivered || acks[0].Requeue || !errors.Is(acks[0].Err, context.Canceled) {
				t.Fatalf("acks=%+v", acks)
			}
			if !reflect.DeepEqual(port.windows, []bool{true, false}) {
				t.Fatalf("window=%v", port.windows)
			}
		})
	}
}

func TestMessageStopCombinesBoundariesAndCompletesOnce(t *testing.T) {
	port := newMessageTestPort(driver.MessageItem{ID: "soon", Text: "soon"}, driver.MessageItem{ID: "after", Text: "after", Timing: driver.TimingAfterTurn})
	sink := &messageTestSink{}
	p := newClaudeParser(sink)
	p.messages = newInitializedMessageTestSession(context.Background(), port)
	writes := make(chan []byte, 1)
	p.messages.start(messageTestStdin{writes: writes}, func() {})
	defer p.messages.finish(nil)
	p.handlePayload("", map[string]any{"type": "assistant", "message": map[string]any{"content": []any{map[string]any{"type": "text", "text": "first"}}}})
	p.handlePayload("", messageHook("Stop", "", ""))
	if out := messageHookOutput(t, <-writes); out["decision"] != "block" || out["reason"] != "soon\n\nafter" {
		t.Fatalf("output=%v", out)
	}
	<-port.acked
	<-port.acked
	terminal := map[string]any{"type": "result", "subtype": "success", "is_error": false, "session_id": "s", "result": "final"}
	p.handlePayload(string(messageFrame(terminal)), terminal)
	p.completeStream(nil, 0, "", false)
	var turns []*driver.TurnCompleted
	for _, ev := range sink.events {
		if ev.Turn != nil {
			turns = append(turns, ev.Turn)
		}
	}
	if len(turns) != 2 || turns[0].Text != "first" || turns[1].Text != "final" {
		t.Fatalf("turns=%+v", turns)
	}
	if !reflect.DeepEqual(port.claims, []driver.Boundary{driver.BoundaryStop, driver.BoundaryTurnEnd}) {
		t.Fatalf("claims=%v", port.claims)
	}
}

func TestMessageTransportNegotiationPreservesPolicy(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, hitl := range []bool{false, true} {
			req := driver.Request{Streaming: streaming, Messages: newMessageTestPort(), Policy: driver.RunPolicy{HumanDecision: driver.HumanDecisionPolicy{Permission: driver.HumanDecisionAutoApprove}}}
			if hitl {
				req.Policy.HumanDecision.Permission = driver.HumanDecisionAsk
			}
			args, err := buildClaudeExecArgs(Config{}, req, hitl)
			if err != nil {
				t.Fatal(err)
			}
			joined := strings.Join(args, " ")
			if !strings.Contains(joined, "--input-format stream-json") || strings.Contains(joined, "--permission-prompt-tool stdio") != hitl || strings.Contains(joined, "--include-partial-messages") != (hitl || streaming) || strings.Contains(joined, "--dangerously-skip-permissions") == hitl {
				t.Fatalf("args=%v", args)
			}
		}
	}
}
