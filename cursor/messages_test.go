package cursor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/v2/driver"
)

const cursorMessageHelperEnv = "AGENT_ADAPTOR_CURSOR_MESSAGE_TEST"

func TestMain(m *testing.M) {
	RunHookRelayIfRequested()
	if mode := os.Getenv(cursorMessageHelperEnv); mode != "" {
		if len(os.Args) == 2 && os.Args[1] == "--version" {
			fmt.Println("2026.10.14-fixture")
			os.Exit(0)
		}
		os.Exit(runCursorMessageHelper(mode))
	}
	os.Exit(m.Run())
}

// The gate blocks on the parent's observed init event, so the helper exercises
// the real process, HTTP relay and parser without relying on scheduling sleeps.
func runCursorMessageHelper(mode string) int {
	if _, err := io.ReadAll(os.Stdin); err != nil {
		return 2
	}
	fmt.Println(`{"type":"system","subtype":"init","session_id":"conversation"}`)
	if gate := os.Getenv("CURSOR_TEST_GATE"); gate != "" {
		resp, err := http.Get(gate)
		if err != nil {
			return 3
		}
		resp.Body.Close()
	}
	if mode != "plain" {
		exe, err := os.Executable()
		if err != nil {
			return 4
		}
		cmd := exec.Command(exe, hookRelayArg)
		cmd.Stdin = strings.NewReader(`{"hook_event_name":"postToolUse","conversation_id":"conversation","tool_use_id":"call-1\nfc-1","tool_output":"never forward"}`)
		data, err := cmd.Output()
		if err != nil {
			return 5
		}
		var reply map[string]string
		if json.Unmarshal(data, &reply) != nil {
			return 6
		}
		if mode != "outstanding" {
			envelope := map[string]any{
				"toolCallId": "call-1\nfc-1", "readToolCall": map[string]any{"args": map[string]any{}, "result": map[string]any{"success": map[string]any{"content": "tool"}}},
			}
			if mode == "echo" {
				envelope["hookAdditionalContexts"] = []any{map[string]string{"content": reply["additional_context"]}}
			}
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"type": "tool_call", "subtype": "completed", "session_id": "conversation", "tool_call": envelope})
		}
	}
	fmt.Print(`{"type":"result","subtype":"success","is_error":false,"result":"done","session_id":"conversation"}`)
	return 0
}

type messageTestPort struct {
	mu      sync.Mutex
	pending []driver.MessageItem
	claims  []driver.Boundary
	acks    map[string][]driver.MessageAck
	windows []bool
}

func newMessageTestPort(items ...driver.MessageItem) *messageTestPort {
	return &messageTestPort{pending: items, acks: make(map[string][]driver.MessageAck)}
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
	items := p.pending
	p.pending = nil
	return items
}
func (p *messageTestPort) Ack(id string, ack driver.MessageAck) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.acks[id] = append(p.acks[id], ack)
}

type messageTestSink struct {
	mu       sync.Mutex
	events   []driver.RunEvent
	init     chan struct{}
	initOnce sync.Once
	failType driver.RunEventType
	err      error
}

func (s *messageTestSink) Emit(e driver.RunEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
	if e.Item != nil && e.Item.Kind == driver.TranscriptInit && s.init != nil {
		s.initOnce.Do(func() { close(s.init) })
	}
	if e.Type == s.failType {
		return s.err
	}
	return nil
}
func (*messageTestSink) EmitStream(driver.StreamPayload) error { return nil }

func managedHookRequest(t *testing.T, p driver.MessagePort) driver.Request {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Cursor hook relay is unverified on Windows")
	}
	cwd := t.TempDir()
	return driver.Request{Messages: p, Workspace: driver.WorkspaceLease{CWD: cwd, StrategyType: driver.WorkspaceStrategyGitWorktree}}
}

func TestCursorMessageCapability(t *testing.T) {
	d := Driver(Config{CommonConfig: CommonConfig{Command: versionCommand(t, "2026.10.14-fixture", "exit 99")}, HookRelay: []string{"relay"}})
	caps := d.Descriptor().Messages
	if !reflect.DeepEqual(caps.Boundaries, []driver.Boundary{driver.BoundaryTool}) || caps.Interrupt || caps.Continue != driver.ContinueResume || !caps.ConfirmedDelivery || caps.PersistsDelivered || caps.Unit != driver.MessageUnitUTF16 || caps.MinVersion != "2026.10.14" || caps.MaxUnits != hookContextLimit-hookTagOverhead {
		t.Fatalf("capability: %+v", caps)
	}
	resolved := d.(driver.MessageCapabilityResolver)
	req := managedHookRequest(t, newMessageTestPort())
	if got := resolved.ResolveMessageCapability(req); !reflect.DeepEqual(got, caps) {
		t.Fatalf("managed: %+v", got)
	}
	req.Workspace.StrategyType = driver.WorkspaceStrategyProjectPrimary
	if got := resolved.ResolveMessageCapability(req); len(got.Boundaries) != 0 {
		t.Fatalf("user workspace: %+v", got)
	}
	argv := []string{"original"}
	driverCfg := Driver(Config{HookRelay: argv}).(configuredDriver)
	argv[0] = "changed"
	if driverCfg.cfg.HookRelay[0] != "original" {
		t.Fatal("relay argv was not captured")
	}
	copyCfg := driverCfg.requestWithConfig(driver.Request{}).Config.(Config)
	copyCfg.HookRelay[0] = "changed again"
	if driverCfg.cfg.HookRelay[0] != "original" {
		t.Fatal("request leaked captured argv")
	}
}

func TestCursorHookInstallAndRefusals(t *testing.T) {
	useMemoryHookListener(t)
	for _, reason := range []string{"none", "unmanaged", "concurrent", "windows", "primary", "probe", "bad directory"} {
		t.Run(reason, func(t *testing.T) {
			p := newMessageTestPort()
			req := managedHookRequest(t, p)
			path := filepath.Join(req.Workspace.CWD, ".cursor", "hooks.json")
			cfg := Config{}
			var first *cursorHooks
			switch reason {
			case "unmanaged":
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(`{"hooks":{}}`), 0o600); err != nil {
					t.Fatal(err)
				}
			case "concurrent":
				var err error
				first, err = startCursorHooks(context.Background(), req, cfg, req.Workspace.CWD, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer first.close()
			case "windows":
				old := cursorHookGOOS
				cursorHookGOOS = func() string { return "windows" }
				t.Cleanup(func() { cursorHookGOOS = old })
			case "primary":
				req.Workspace.StrategyType = driver.WorkspaceStrategyProjectPrimary
			case "probe":
				cfg.HookRelay = []string{filepath.Join(t.TempDir(), "missing-relay")}
			case "bad directory":
				if err := os.WriteFile(filepath.Dir(path), []byte("file"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			sink := &messageTestSink{}
			hooks, err := prepareCursorHooks(context.Background(), req, cfg, req.Workspace.CWD, nil, sink)
			if err != nil {
				t.Fatal(err)
			}
			if reason != "none" {
				if hooks != nil || len(sink.events) != 1 || sink.events[0].Type != driver.RunEventLifecycle || !strings.Contains(sink.events[0].Text, "turn end via resume") {
					t.Fatalf("hooks=%v events=%+v", hooks, sink.events)
				}
				if reason != "probe" && len((adapter{}).ResolveMessageCapability(req).Boundaries) != 0 {
					t.Fatal("resolver advertised unavailable hooks")
				}
				if reason == "unmanaged" {
					data, err := os.ReadFile(path)
					if err != nil || string(data) != `{"hooks":{}}` {
						t.Fatalf("unmanaged file changed: %s %v", data, err)
					}
				}
				return
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var doc struct {
				Hooks map[string][]struct {
					Command string `json:"command"`
					Timeout int    `json:"timeout"`
				} `json:"hooks"`
			}
			if json.Unmarshal(data, &doc) != nil || len(doc.Hooks) != 2 {
				t.Fatalf("hooks: %s", data)
			}
			for _, event := range []string{"postToolUse", "postToolUseFailure"} {
				entries := doc.Hooks[event]
				if len(entries) != 1 || entries[0].Timeout != 10 || !strings.Contains(entries[0].Command, hookRelayArg) || !strings.Contains(entries[0].Command, hookManagedMarker) {
					t.Fatalf("event %s: %+v", event, entries)
				}
			}
			if err := hooks.close(); err != nil {
				t.Fatal(err)
			}
			if err := hooks.close(); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("cleanup: %v", err)
			}
		})
	}
}

func TestCursorHookCleanupPreservesReplacement(t *testing.T) {
	useMemoryHookListener(t)
	req := managedHookRequest(t, newMessageTestPort())
	hooks, err := startCursorHooks(context.Background(), req, Config{}, req.Workspace.CWD, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hooks.path, []byte(`{"user":"hooks"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := hooks.close(); err == nil {
		t.Fatal("cleanup must report the modified file")
	}
	data, err := os.ReadFile(hooks.path)
	if err != nil || string(data) != `{"user":"hooks"}` {
		t.Fatalf("replacement lost: %s %v", data, err)
	}
}

func TestCursorHookConcurrentInstallation(t *testing.T) {
	useMemoryHookListener(t)
	req := managedHookRequest(t, newMessageTestPort())
	start := make(chan struct{})
	type result struct {
		hooks *cursorHooks
		err   error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			<-start
			h, err := startCursorHooks(context.Background(), req, Config{}, req.Workspace.CWD, nil)
			results <- result{h, err}
		}()
	}
	close(start)
	a, b := <-results, <-results
	if (a.err == nil) == (b.err == nil) {
		t.Fatalf("want exactly one owner: %v %v", a.err, b.err)
	}
	for _, r := range []result{a, b} {
		if r.hooks != nil {
			if err := r.hooks.close(); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestCursorHookDeliveryAndTurnCompleted(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cross-process relay requires loopback sockets: %v", err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"echo", "missing", "outstanding", "plain"} {
		t.Run(mode, func(t *testing.T) {
			p := newMessageTestPort(driver.MessageItem{ID: "one", Text: "verbatim 😀 guidance"})
			req := managedHookRequest(t, p)
			sink := &messageTestSink{init: make(chan struct{})}
			gate := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				select {
				case <-sink.init:
					w.WriteHeader(http.StatusOK)
				case <-r.Context().Done():
				}
			}))
			defer gate.Close()
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			req.Prompt = "task"
			req.Config = Config{CommonConfig: CommonConfig{Command: exe, Env: []driver.EnvBinding{
				{Name: cursorMessageHelperEnv, Value: mode}, {Name: "CURSOR_TEST_GATE", Value: gate.URL}, {Name: "CURSOR_HOME", Value: t.TempDir()},
			}}}
			if mode == "plain" {
				cfg := req.Config.(Config)
				cfg.HookRelay = []string{"/missing-relay"}
				req.Config = cfg
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			res, err := (adapter{}).Run(ctx, req, sink)
			if err != nil || res.Output != "done" || res.Checkpoint == nil {
				t.Fatalf("response=%+v err=%v", res, err)
			}
			var turns int
			for _, event := range sink.events {
				if event.Type == driver.RunEventTurnCompleted {
					turns++
					if event.Turn == nil || event.Turn.Text != res.Output || event.Turn.Index != 0 || event.Turn.CompletedAt.IsZero() || event.Turn.Interrupted {
						t.Fatalf("turn: %+v", event.Turn)
					}
				}
			}
			if turns != 1 {
				t.Fatalf("turns=%d", turns)
			}
			if mode == "plain" {
				if len(p.claims) != 0 || len(p.windows) != 0 || len(p.acks) != 0 {
					t.Fatalf("failed probe opened window: %+v", p)
				}
			} else {
				if !reflect.DeepEqual(p.claims, []driver.Boundary{driver.BoundaryTool}) || !reflect.DeepEqual(p.windows, []bool{true, false}) {
					t.Fatalf("port: %+v", p)
				}
				acks := p.acks["one"]
				if len(acks) != 1 {
					t.Fatalf("acks: %+v", acks)
				}
				if mode == "echo" {
					if !acks[0].Delivered || acks[0].Mechanism != "cursor.hook.post_tool_use" {
						t.Fatalf("ack: %+v", acks[0])
					}
				} else if acks[0].Delivered || acks[0].Err == nil || !acks[0].Requeue {
					t.Fatalf("ack: %+v", acks[0])
				}
			}
			if _, err := os.Stat(filepath.Join(req.Workspace.CWD, ".cursor", "hooks.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("hooks left behind: %v", err)
			}
		})
	}
}

// File ownership, server lifecycle and bounded probe tests also run in sandboxes
// that prohibit socket creation. HTTP request semantics are tested separately.
type memoryHookListener struct {
	closed chan struct{}
	once   sync.Once
}

func (l *memoryHookListener) Accept() (net.Conn, error) { <-l.closed; return nil, net.ErrClosed }
func (l *memoryHookListener) Close() error              { l.once.Do(func() { close(l.closed) }); return nil }
func (*memoryHookListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1234}
}
func useMemoryHookListener(t *testing.T) {
	t.Helper()
	previous := cursorHookListen
	cursorHookListen = func(string, string) (net.Listener, error) {
		return &memoryHookListener{closed: make(chan struct{})}, nil
	}
	t.Cleanup(func() { cursorHookListen = previous })
}

func TestCursorParserConfirmsHookDelivery(t *testing.T) {
	for _, echo := range []bool{true, false} {
		p := newMessageTestPort(driver.MessageItem{ID: "one", Text: "guidance"})
		h := bareHooks(p)
		h.sessionID = ""
		parser := newCursorParser(nil)
		parser.hooks = h
		if err := parser.onChunk("stdout", []byte(`{"type":"system","subtype":"init","session_id":"conversation"}`+"\n"), time.Time{}); err != nil {
			t.Fatal(err)
		}
		text := h.claim(ownHook())["additional_context"]
		envelope := map[string]any{"toolCallId": "call\nfc-call", "readToolCall": map[string]any{"args": map[string]any{}, "result": map[string]any{"success": "done"}}}
		if echo {
			envelope["hookAdditionalContexts"] = []any{map[string]string{"content": text}}
		}
		payload := map[string]any{"type": "tool_call", "subtype": "completed", "session_id": "conversation", "tool_call": envelope}
		data, _ := json.Marshal(payload)
		if err := parser.onChunk("stdout", append(data, '\n'), time.Time{}); err != nil {
			t.Fatal(err)
		}
		if err := parser.onChunk("stdout", []byte(`{"type":"result","subtype":"success","is_error":false,"result":"done","session_id":"conversation"}`+"\n"), time.Time{}); err != nil {
			t.Fatal(err)
		}
		parser.finalize()
		h.end()
		acks := p.acks["one"]
		if parser.protocolMalformed || len(acks) != 1 || acks[0].Delivered != echo || acks[0].Requeue == echo {
			t.Fatalf("echo=%t parser=%+v ack=%+v", echo, parser, acks)
		}
		if !reflect.DeepEqual(p.windows, []bool{true, false}) {
			t.Fatalf("windows=%v", p.windows)
		}
	}
}

func bareHooks(p driver.MessagePort) *cursorHooks {
	return &cursorHooks{ctx: context.Background(), port: p, nonce: strings.Repeat("a", 2*hookNonceBytes), token: "secret", sessionID: "conversation", inflight: make(map[string]hookClaim)}
}
func ownHook() hookRequest {
	return hookRequest{Event: "postToolUse", ConversationID: "conversation", ToolUseID: "call"}
}

func TestCursorHookOwnershipAndConfirmation(t *testing.T) {
	p := newMessageTestPort(driver.MessageItem{ID: "one", Text: "guidance"})
	s := bareHooks(p)
	for _, modify := range []func(*hookRequest){
		func(r *hookRequest) { r.ParentToolCallID = "parent" },
		func(r *hookRequest) { r.ConversationID = "other" },
		func(r *hookRequest) { r.Event = "stop" },
		func(r *hookRequest) { r.ToolUseID = "" },
	} {
		r := ownHook()
		modify(&r)
		if got := s.claim(r); len(got) != 0 {
			t.Fatalf("foreign hook: %+v", got)
		}
	}
	if len(p.claims) != 0 || len(p.windows) != 0 {
		t.Fatal("foreign hook opened a window")
	}
	output := s.claim(ownHook())["additional_context"]
	if output != s.tag()+"\nguidance" || !strings.Contains(s.promptNote(), s.tag()) {
		t.Fatalf("nonce framing: %q", output)
	}
	if len(p.acks) != 0 {
		t.Fatal("hook response was acknowledged without echo")
	}
	if len(s.claim(ownHook())) != 0 || len(p.claims) != 1 {
		t.Fatal("duplicate hook claimed twice")
	}
	s.receipt("other", map[string]any{"hookAdditionalContexts": []any{map[string]any{"content": output}}})
	if len(p.acks) != 0 {
		t.Fatal("wrong tool claimed delivery")
	}
	// The exact tag in a tool result, or a truncated echo, cannot confirm delivery.
	s.receipt("call", map[string]any{"result": map[string]any{"hookAdditionalContexts": output}, "hookAdditionalContexts": []any{map[string]any{"content": s.tag()}}})
	if a := p.acks["one"]; len(a) != 1 || a[0].Delivered || !a[0].Requeue {
		t.Fatalf("forged delivery: %+v", a)
	}
	s.end()
	s.end()
	if len(p.acks["one"]) != 1 || len(s.claim(ownHook())) != 0 {
		t.Fatal("claim was settled more than once")
	}
}

func TestCursorHookUTF16BatchLimit(t *testing.T) {
	caps := cursorMessageCapability()
	max := strings.Repeat("😀", caps.MaxUnits/2) + strings.Repeat("x", caps.MaxUnits%2)
	p := newMessageTestPort(driver.MessageItem{ID: "max", Text: max}, driver.MessageItem{ID: "later", Text: "next"}, driver.MessageItem{ID: "last", Text: ""})
	s := bareHooks(p)
	output := s.claim(ownHook())["additional_context"]
	if utf16Units(output) != 10000 || !strings.HasSuffix(output, max) {
		t.Fatalf("units=%d", utf16Units(output))
	}
	for _, id := range []string{"later", "last"} {
		a := p.acks[id]
		if len(a) != 1 || !a[0].Requeue {
			t.Fatalf("overflow %s: %+v", id, a)
		}
	}
	s.receipt("call", map[string]any{"hookAdditionalContexts": []any{map[string]any{"content": output}}})
	if len(p.acks["max"]) != 1 || !p.acks["max"][0].Delivered {
		t.Fatal("max size not delivered")
	}
	p = newMessageTestPort(driver.MessageItem{ID: "over", Text: max + "x"})
	s = bareHooks(p)
	if len(s.claim(ownHook())) != 0 || !p.acks["over"][0].Requeue {
		t.Fatal("oversize context was returned")
	}
	p = newMessageTestPort(driver.MessageItem{ID: "a", Text: "one"}, driver.MessageItem{ID: "b", Text: "two"})
	s = bareHooks(p)
	if text := s.claim(ownHook())["additional_context"]; text != s.tag()+"\none\n\ntwo" {
		t.Fatalf("batch: %q", text)
	}
	s.end()
	if len(p.acks["a"]) != 1 || len(p.acks["b"]) != 1 {
		t.Fatal("outstanding batch not requeued")
	}
}

func TestCursorHookHTTPValidation(t *testing.T) {
	p := newMessageTestPort(driver.MessageItem{ID: "one", Text: "hello"})
	s := bareHooks(p)
	for _, tc := range []struct {
		name, method, path, token, body string
		status                          int
	}{
		{"token", "POST", "/hook", "wrong", `{}`, 401},
		{"method", "GET", "/hook", "secret", `{}`, 404},
		{"path", "POST", "/other", "secret", `{}`, 404},
		{"invalid", "POST", "/hook", "secret", `{`, 400},
		{"null", "POST", "/hook", "secret", `null`, 400},
		{"trailing", "POST", "/hook", "secret", `{} {}`, 400},
		{"large", "POST", "/hook", "secret", `{"tool_use_id":"` + strings.Repeat("x", hookRequestLimit) + `"}`, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			r.Header.Set("Authorization", "Bearer "+tc.token)
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if w.Code != tc.status || !json.Valid(w.Body.Bytes()) {
				t.Fatalf("status=%d body=%s", w.Code, w.Body)
			}
		})
	}
	if len(p.claims) != 0 {
		t.Fatal("invalid request claimed messages")
	}
	data, _ := json.Marshal(ownHook())
	r := httptest.NewRequest("POST", "/hook", bytes.NewReader(data))
	r.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "additional_context") || len(p.acks) != 0 {
		t.Fatalf("response: %s", w.Body)
	}
	s.end()
}

func TestCursorHookCancellationClosesClaims(t *testing.T) {
	p := newMessageTestPort(driver.MessageItem{ID: "one", Text: "hello"})
	s := bareHooks(p)
	ctx, cancel := context.WithCancel(context.Background())
	s.ctx = ctx
	s.claim(ownHook())
	cancel()
	if len(s.claim(hookRequest{Event: "postToolUse", ConversationID: "conversation", ToolUseID: "next"})) != 0 {
		t.Fatal("claim after cancellation")
	}
	s.end()
	if len(p.acks["one"]) != 1 || !p.acks["one"][0].Requeue {
		t.Fatal("cancelled claim lost")
	}
}

func TestCursorHookNoticeSinkFailure(t *testing.T) {
	errWant := errors.New("sink closed")
	req := managedHookRequest(t, newMessageTestPort())
	req.Workspace.StrategyType = driver.WorkspaceStrategyProjectPrimary
	_, err := prepareCursorHooks(context.Background(), req, Config{}, req.Workspace.CWD, nil, &messageTestSink{failType: driver.RunEventLifecycle, err: errWant})
	if !errors.Is(err, errWant) {
		t.Fatalf("error: %v", err)
	}
}

func TestCursorTurnCompletedWithResumeFallback(t *testing.T) {
	for _, failSink := range []bool{false, true} {
		req := managedHookRequest(t, newMessageTestPort())
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		req.Config = Config{HookRelay: []string{"/missing-relay"}, CommonConfig: CommonConfig{
			Command: exe, Env: []driver.EnvBinding{{Name: cursorMessageHelperEnv, Value: "plain"}, {Name: "CURSOR_HOME", Value: t.TempDir()}},
		}}
		want := errors.New("turn sink closed")
		sink := &messageTestSink{}
		if failSink {
			sink.failType = driver.RunEventTurnCompleted
			sink.err = want
		}
		res, err := (adapter{}).Run(context.Background(), req, sink)
		if failSink {
			if !errors.Is(err, want) {
				t.Fatalf("turn sink error: %v", err)
			}
		} else if err != nil || res.Output != "done" {
			t.Fatalf("response=%+v err=%v", res, err)
		}
		var notices, turns int
		for _, event := range sink.events {
			if event.Type == driver.RunEventLifecycle {
				notices++
			}
			if event.Type == driver.RunEventTurnCompleted {
				turns++
				if event.Turn == nil || event.Turn.Text != "done" || event.Turn.Index != 0 || event.Turn.CompletedAt.IsZero() {
					t.Fatalf("turn=%+v", event.Turn)
				}
			}
		}
		if notices != 1 || turns != 1 {
			t.Fatalf("notices=%d turns=%d", notices, turns)
		}
	}
}

func TestCursorTurnCompletedWithInstalledHooks(t *testing.T) {
	useMemoryHookListener(t)
	p := newMessageTestPort()
	req := managedHookRequest(t, p)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	req.Config = Config{HookRelay: []string{exe, hookRelayArg}, CommonConfig: CommonConfig{
		Command: exe, Env: []driver.EnvBinding{{Name: cursorMessageHelperEnv, Value: "plain"}, {Name: "CURSOR_HOME", Value: t.TempDir()}},
	}}
	sink := &messageTestSink{}
	res, err := (adapter{}).Run(context.Background(), req, sink)
	if err != nil || res.Output != "done" {
		t.Fatalf("response=%+v err=%v", res, err)
	}
	turns := 0
	for _, event := range sink.events {
		if event.Type == driver.RunEventLifecycle {
			t.Fatalf("unexpected fallback: %s", event.Text)
		}
		if event.Type == driver.RunEventTurnCompleted {
			turns++
		}
	}
	if turns != 1 || len(p.windows) != 0 {
		t.Fatalf("turns=%d windows=%v", turns, p.windows)
	}
	if _, err := os.Stat(filepath.Join(req.Workspace.CWD, ".cursor", "hooks.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cleanup: %v", err)
	}
}

func TestCursorHookClaimReceiptEndConcurrent(t *testing.T) {
	for range 100 {
		p := newMessageTestPort(driver.MessageItem{ID: "one", Text: "guidance"})
		s := bareHooks(p)
		text := s.claim(ownHook())["additional_context"]
		start := make(chan struct{})
		var done sync.WaitGroup
		for _, work := range []func(){
			func() {
				s.receipt("call", map[string]any{"hookAdditionalContexts": []any{map[string]any{"content": text}}})
			},
			func() { s.end() },
			func() { s.claim(ownHook()) },
		} {
			done.Add(1)
			go func() { defer done.Done(); <-start; work() }()
		}
		close(start)
		done.Wait()
		if len(p.acks["one"]) != 1 {
			t.Fatalf("concurrent settlement: %+v", p.acks)
		}
	}
}
