package cursor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/agent-dance/agent-adaptor/v2/adaptertest"
	"github.com/agent-dance/agent-adaptor/v2/driver"
)

// The real HTTP server accepts net.Pipe connections. A FIFO carries its reply
// to the fake CLI; neither the Driver nor its authenticated HTTP handler is
// replaced. This exercises Run/parser/relay without a sandbox TCP permission.
type conformanceHookListener struct {
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
}

func (l *conformanceHookListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}
func (l *conformanceHookListener) Close() error { l.once.Do(func() { close(l.done) }); return nil }
func (*conformanceHookListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1234}
}

type cursorConformanceDriver struct {
	configuredDriver
	ready    chan *conformanceHookListener
	path     string
	scenario adaptertest.MessageScenario
	hooks    bool
}

func (d cursorConformanceDriver) Run(ctx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
	claimed := make(chan struct{})
	messagesEnabled := req.Messages != nil
	if messagesEnabled {
		req.Messages = &cursorConformancePort{MessagePort: req.Messages, claimed: claimed}
	}
	observedSink := &cursorConformanceSink{EventSink: sink, run: func() error {
		if !d.hooks || !messagesEnabled {
			return nil
		}
		var info struct{ Endpoint, Token string }
		b, err := os.ReadFile(d.path + ".request")
		if err != nil {
			return err
		}
		if err = json.Unmarshal(b, &info); err != nil {
			return err
		}
		listener := <-d.ready
		client, server := net.Pipe()
		defer client.Close()
		select {
		case listener.conns <- server:
		case <-ctx.Done():
			server.Close()
			return ctx.Err()
		}
		request, err := http.NewRequest(http.MethodPost, info.Endpoint, strings.NewReader(`{"hook_event_name":"postToolUse","conversation_id":"conversation","tool_use_id":"call-1"}`))
		if err != nil {
			return err
		}
		request.Header.Set("Authorization", "Bearer "+info.Token)
		if err = request.Write(client); err != nil {
			return err
		}
		var output []byte
		if d.scenario.Name == "write_failure" {
			select {
			case <-claimed:
			case <-ctx.Done():
				return ctx.Err()
			}
			_ = client.Close() // deterministic HTTP response write failure after Claim
			output = []byte(`{}`)
		} else {
			response, err := http.ReadResponse(bufio.NewReader(client), request)
			if err != nil {
				return err
			}
			output, err = io.ReadAll(response.Body)
			response.Body.Close()
			if err != nil {
				return err
			}
		}
		if d.scenario.Name == "cancel" || d.scenario.Name == "timeout" {
			return nil
		}
		// O_RDWR avoids a blocking open when the child has already exited. The
		// bounded reply is read concurrently by the fake CLI, before terminal JSON.
		fifo, err := os.OpenFile(d.path, os.O_RDWR, 0)
		if err != nil {
			return err
		}
		defer fifo.Close()
		_, err = fifo.Write(append(output, '\n'))
		return err
	}}
	response, err := d.configuredDriver.Run(ctx, req, observedSink)
	return response, errors.Join(err, observedSink.failure())
}

type cursorConformancePort struct {
	driver.MessagePort
	claimed chan struct{}
	once    sync.Once
}

func (p *cursorConformancePort) Claim(b driver.Boundary) []driver.MessageItem {
	items := p.MessagePort.Claim(b)
	p.once.Do(func() { close(p.claimed) })
	return items
}

type cursorConformanceSink struct {
	driver.EventSink
	once sync.Once
	run  func() error
	err  error
	mu   sync.Mutex
}

func (s *cursorConformanceSink) Emit(e driver.RunEvent) error {
	if err := s.EventSink.Emit(e); err != nil {
		return err
	}
	if e.Item != nil && e.Item.Kind == driver.TranscriptInit {
		s.once.Do(func() { err := s.run(); s.mu.Lock(); s.err = err; s.mu.Unlock() })
	}
	return s.failure()
}
func (s *cursorConformanceSink) failure() error { s.mu.Lock(); defer s.mu.Unlock(); return s.err }

func cursorMessageFixture(hooks bool) func(*testing.T, adaptertest.MessageScenario) adaptertest.MessageFixture {
	return func(t *testing.T, scenario adaptertest.MessageScenario) adaptertest.MessageFixture {
		t.Helper()
		if runtime.GOOS == "windows" {
			t.Skip("named-pipe fake and Cursor relay are unverified on Windows")
		}
		home := t.TempDir()
		path := filepath.Join(home, "hook-reply")
		if b, err := exec.Command("mkfifo", path).CombinedOutput(); err != nil {
			t.Fatalf("create fake relay FIFO: %v: %s", err, b)
		}
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		script, err := json.Marshal(scenario)
		if err != nil {
			t.Fatal(err)
		}
		ready := make(chan *conformanceHookListener, 1)
		previous := cursorHookListen
		cursorHookListen = func(string, string) (net.Listener, error) {
			l := &conformanceHookListener{conns: make(chan net.Conn), done: make(chan struct{})}
			ready <- l
			return l, nil
		}
		t.Cleanup(func() { cursorHookListen = previous })
		cfg := Config{CommonConfig: CommonConfig{Command: exe, CWD: home, Env: []driver.EnvBinding{{Name: cursorMessageHelperEnv, Value: "conformance"}, {Name: "CURSOR_MSG_PATH", Value: path}, {Name: "CURSOR_MSG_SCENARIO", Value: string(script)}, {Name: "CURSOR_HOME", Value: home}}}, HookRelay: []string{exe, hookRelayArg}}
		d := cursorConformanceDriver{configuredDriver: Driver(cfg).(configuredDriver), ready: ready, path: path, scenario: scenario, hooks: hooks}
		req := driver.Request{Prompt: "initial", Workspace: driver.WorkspaceLease{CWD: home, StrategyType: driver.WorkspaceStrategyGitWorktree}}
		if !hooks {
			req.Workspace.StrategyType = driver.WorkspaceStrategyProjectPrimary
		}
		return adaptertest.MessageFixture{Driver: d, Request: req, Evidence: func() adaptertest.MessageEvidence {
			var e adaptertest.MessageEvidence
			b, err := os.ReadFile(path + ".evidence")
			if err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(b, &e); err != nil {
				t.Fatal(err)
			}
			return e
		}}
	}
}

func runCursorMessageConformanceHelper() int {
	path := os.Getenv("CURSOR_MSG_PATH")
	var scenario adaptertest.MessageScenario
	if json.Unmarshal([]byte(os.Getenv("CURSOR_MSG_SCENARIO")), &scenario) != nil {
		return 80
	}
	var e adaptertest.MessageEvidence
	if b, err := os.ReadFile(path + ".evidence"); err == nil {
		_ = json.Unmarshal(b, &e)
	}
	save := func() {
		b, _ := json.Marshal(e)
		_ = os.WriteFile(path+".next", b, 0o600)
		_ = os.Rename(path+".next", path+".evidence")
	}
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		return 81
	}
	e.Resumed = false
	for i, arg := range os.Args {
		if arg == "--resume" && i+1 < len(os.Args) && os.Args[i+1] == "conversation" {
			e.Resumed = true
		}
	}
	e.Received = append(e.Received, string(input))
	e.Turns = nil
	initial := strings.HasPrefix(string(input), "initial")
	endpoint, token := os.Getenv(hookURLEnv), os.Getenv(hookTokenEnv)
	useHook := endpoint != "" && initial
	// Resumed runs still install hooks and send an empty callback. This proves
	// no buffered input is accidentally consumed from the prior invocation.
	if endpoint != "" {
		b, _ := json.Marshal(struct{ Endpoint, Token string }{endpoint, token})
		if os.WriteFile(path+".request", b, 0o600) != nil {
			return 82
		}
	}
	e.Turns = []driver.TurnCompleted{{Text: "done"}}
	if scenario.Name == "process_exit" || scenario.Name == "cancel" || scenario.Name == "timeout" {
		e.Turns = []driver.TurnCompleted{{}}
	}
	save()
	enc := json.NewEncoder(os.Stdout)
	_ = enc.Encode(map[string]any{"type": "system", "subtype": "init", "session_id": "conversation"})
	if endpoint != "" {
		fifo, err := os.Open(path)
		if err != nil {
			return 83
		}
		var reply map[string]string
		err = json.NewDecoder(fifo).Decode(&reply)
		fifo.Close()
		if err != nil {
			return 84
		}
		text := reply["additional_context"]
		if text != "" {
			e.Received = append(e.Received, text)
			save()
		}
		if useHook && scenario.Name == "process_exit" {
			return 85
		}
		if text != "" && scenario.Name != "write_failure" {
			call := map[string]any{"toolCallId": "call-1", "readToolCall": map[string]any{"args": map[string]any{}, "result": map[string]any{"success": "tool"}}}
			if scenario.Name != "missing_echo" {
				call["hookAdditionalContexts"] = []any{map[string]string{"content": text}}
				e.Confirmed = append(e.Confirmed, text)
				save()
			}
			_ = enc.Encode(map[string]any{"type": "tool_call", "subtype": "completed", "session_id": "conversation", "tool_call": call})
		}
	}
	e.Turns = []driver.TurnCompleted{{Text: "done"}}
	save()
	_ = enc.Encode(map[string]any{"type": "result", "subtype": "success", "is_error": false, "result": "done", "session_id": "conversation"})
	return 0
}

var _ driver.Driver = cursorConformanceDriver{}
