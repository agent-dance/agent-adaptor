package cursor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/capability"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/testutil"
	"github.com/agent-dance/agent-adaptor/mcp"
	"github.com/agent-dance/agent-adaptor/profile"
)

type alignmentCursorSink struct {
	mu      sync.Mutex
	streams []driver.StreamPayload
	events  []driver.RunEvent
}

func (s *alignmentCursorSink) Emit(e driver.RunEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
	return nil
}
func (s *alignmentCursorSink) EmitStream(e driver.StreamPayload) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.streams = append(s.streams, e)
	return nil
}
func (s *alignmentCursorSink) facts() []capability.Invocation {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []capability.Invocation
	for _, e := range s.streams {
		if e.Capability != nil {
			out = append(out, *e.Capability)
		}
		if e.Todo != nil {
			panic("Cursor must not emit Todo")
		}
	}
	return out
}
func alignmentCursorCatalog() driver.Request {
	return driver.Request{MCP: driver.MCPPayload{Servers: []driver.MCPServerSpec{{Key: "知识__库", Transport: driver.MCPTransportStdio, Command: "unused-fixture-server"}}}, ProfilePayload: driver.ProfilePayload{Agents: driver.AgentPayload{Agents: []driver.AgentSpec{{Key: "agent/planner", RuntimeName: "planner"}}}}}
}
func alignmentCursorFrame(id, subtype, variant, args, result string) string {
	extra := ""
	if result != "" {
		extra = `,"result":` + result
	}
	return `{"type":"tool_call","subtype":"` + subtype + `","session_id":"session-a","call_id":"` + id + `","tool_call":{"` + variant + `":{"args":` + args + extra + `}}}` + "\n"
}

const alignmentCursorMCPArgs = `{"serverIdentifier":"知识__库","toolName":"search__中文","arguments":{"secret":"DO_NOT_PROJECT","url":"https://private.invalid","header":"secret"}}`
const alignmentCursorAgentArgs = `{"subagentType":{"custom":{"name":"planner"}},"task":"DO_NOT_PROJECT"}`
const alignmentCursorTerminal = `{"type":"result","subtype":"success","session_id":"session-a","is_error":false,"result":"done","usage":{"input_tokens":123}}` + "\n"

func alignmentCursorParse(t *testing.T, req driver.Request, body string, cancelled bool) (*cursorParser, *alignmentCursorSink) {
	t.Helper()
	s := &alignmentCursorSink{}
	p := newCursorParser(s)
	p.configureCapabilities(req)
	for i := 0; i < len(body); i += 7 {
		end := i + 7
		if end > len(body) {
			end = len(body)
		}
		if err := p.onChunk("stdout", []byte(body[i:end]), time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	p.finalize()
	p.closeCapabilities(cancelled)
	return p, s
}
func TestAlignmentCursorCapabilityFormalLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name, variant, args, result string
		phase                       capability.Phase
	}{
		{"MCP success", "mcpToolCall", alignmentCursorMCPArgs, `{"success":{"content":"DO_NOT_PROJECT"}}`, capability.Completed},
		{"MCP boolean success", "mcpToolCall", alignmentCursorMCPArgs, `{"success":true}`, capability.Completed},
		{"MCP failure", "mcpToolCall", alignmentCursorMCPArgs, `{"failure":{"content":"DO_NOT_PROJECT"}}`, capability.Failed},
		{"MCP oneof failure", "mcpToolCall", alignmentCursorMCPArgs, `{"result":{"case":"serverNotFound","value":{}}}`, capability.Failed},
		{"agent success", "taskToolCall", alignmentCursorAgentArgs, `{"success":true}`, capability.Completed},
		{"agent failure", "taskToolCall", alignmentCursorAgentArgs, `{"success":false}`, capability.Failed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start := alignmentCursorFrame("call-1", "started", tc.variant, tc.args, "")
			end := alignmentCursorFrame("call-1", "completed", tc.variant, tc.args, tc.result)
			p, s := alignmentCursorParse(t, alignmentCursorCatalog(), start+start+end+end+alignmentCursorTerminal, false)
			f := s.facts()
			if len(f) != 2 || f[0].Phase != capability.Started || f[1].Phase != tc.phase {
				t.Fatalf("facts=%+v", f)
			}
			for _, v := range f {
				if v.InvocationID != "call-1" || v.Evidence != capability.ProviderProtocol || v.Source != capability.Provider || v.ScopeID != "" || v.ParentToolCallID != "" || v.Duration != nil || v.OccurredAt.IsZero() {
					t.Fatalf("unsafe coordinates=%+v", v)
				}
				raw, _ := json.Marshal(v)
				if strings.Contains(string(raw), "DO_NOT_PROJECT") || strings.Contains(string(raw), "private.invalid") {
					t.Fatal("unsafe projection")
				}
			}
			if len(p.transcript) != 5 || p.buildOutput() != "done" || p.terminal == nil || p.checkpoint(0) == nil {
				t.Fatalf("lost original output=%+v", p)
			}
			if p.usage != nil {
				t.Fatal("undocumented usage must remain unobserved")
			}
		})
	}
}
func TestAlignmentCursorCapabilityUnknownAndAmbiguous(t *testing.T) {
	for _, tc := range []struct {
		name, args, result string
		req                driver.Request
	}{
		{"unknown server", `{"serverIdentifier":"unknown","toolName":"search"}`, `{"success":true}`, alignmentCursorCatalog()},
		{"missing server", `{"toolName":"search"}`, `{"success":true}`, alignmentCursorCatalog()},
		{"trim forbidden", `{"serverIdentifier":" 知识__库","toolName":"search"}`, `{"success":true}`, alignmentCursorCatalog()},
		{"conflicting operation", `{"serverIdentifier":"知识__库","toolName":"search","name":"other"}`, `{"success":true}`, alignmentCursorCatalog()},
		{"ambiguous agent", alignmentCursorAgentArgs, `{"success":true}`, driver.Request{ProfilePayload: driver.ProfilePayload{Agents: driver.AgentPayload{Agents: []driver.AgentSpec{{Key: "a", RuntimeName: "planner"}, {Key: "b", RuntimeName: "planner"}}}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			variant := "mcpToolCall"
			if tc.name == "ambiguous agent" {
				variant = "taskToolCall"
			}
			_, s := alignmentCursorParse(t, tc.req, alignmentCursorFrame("x", "started", variant, tc.args, "")+alignmentCursorFrame("x", "completed", variant, tc.args, tc.result), false)
			if len(s.facts()) != 0 {
				t.Fatalf("guessed facts=%+v", s.facts())
			}
		})
	}
}
func TestAlignmentCursorPendingNeverBecomesSuccess(t *testing.T) {
	for _, tc := range []struct {
		name, result string
		cancel       bool
		want         capability.Phase
	}{
		{"EOF", "", false, capability.Interrupted}, {"cancel", "", true, capability.Cancelled}, {"unknown result", `{"status":"success","content":"done"}`, false, capability.Interrupted}, {"unknown oneof", `{"result":{"case":"futureSuccess","value":{}}}`, false, capability.Interrupted}, {"ambiguous result", `{"success":true,"failure":{}}`, false, capability.Interrupted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := alignmentCursorFrame("x", "started", "mcpToolCall", alignmentCursorMCPArgs, "")
			if tc.result != "" {
				body += alignmentCursorFrame("x", "completed", "mcpToolCall", alignmentCursorMCPArgs, tc.result)
			}
			_, s := alignmentCursorParse(t, alignmentCursorCatalog(), body, tc.cancel)
			f := s.facts()
			if len(f) != 2 || f[1].Phase != tc.want {
				t.Fatalf("facts=%+v", f)
			}
		})
	}
	_, s := alignmentCursorParse(t, alignmentCursorCatalog(), alignmentCursorFrame("orphan", "completed", "mcpToolCall", alignmentCursorMCPArgs, `{"success":true}`), false)
	if len(s.facts()) != 0 {
		t.Fatal("invented start")
	}
}
func TestAlignmentCursorUnsupportedObservationsAndProtocolFence(t *testing.T) {
	for _, body := range []string{
		`{"type":"user","session_id":"session-a","message":{"content":[{"type":"text","text":"/review"}]}}` + "\n" + `{"type":"todo.updated","todos":["write a plan"]}` + "\n" + alignmentCursorTerminal,
		alignmentCursorTerminal + alignmentCursorFrame("late", "started", "mcpToolCall", alignmentCursorMCPArgs, ""),
		strings.ReplaceAll(alignmentCursorFrame("wrong", "started", "mcpToolCall", alignmentCursorMCPArgs, ""), "session-a", "session-b") + alignmentCursorFrame("wrong", "completed", "mcpToolCall", alignmentCursorMCPArgs, `{"success":true}`),
	} {
		_, s := alignmentCursorParse(t, alignmentCursorCatalog(), body, false)
		for _, v := range s.facts() {
			if v.Phase == capability.Completed || v.Ref.Kind == capability.Skill {
				t.Fatalf("guessed/fenced fact=%+v", v)
			}
		}
	}
}
func TestAlignmentCursorAppendPreflight(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(home, "must-not-materialize")
	cfg := Config{CommonConfig: CommonConfig{Command: filepath.Join(home, "must-not-launch")}}
	d := Driver(cfg)
	if d.Descriptor().SystemPrompt.Append {
		t.Fatal("Cursor append unsupported")
	}
	_, err := d.Run(context.Background(), driver.Request{AppendSystemPrompt: "中文\nSECRET", Profile: &driver.ProfileSelection{Mode: driver.ProfileModeDedicated, Dir: target}}, &alignmentCursorSink{})
	var typed *driver.SystemPromptUnsupportedError
	if !errors.As(err, &typed) || !errors.Is(err, driver.ErrSystemPromptUnsupported) || typed.Reason != "unsupported_driver" || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("direct error=%v", err)
	}
	a := adaptor.New(d, adaptor.WithProfile(profile.Dedicated(target)), adaptor.WithAppendSystemPrompt("SECRET"))
	defer a.Close(context.Background())
	if _, err = a.Run(context.Background(), "original"); !errors.Is(err, adaptor.ErrSystemPromptUnsupported) {
		t.Fatalf("public error=%v", err)
	}
	if _, err = a.Inspect().Environment(context.Background()); !errors.Is(err, adaptor.ErrSystemPromptUnsupported) {
		t.Fatalf("Inspect error=%v", err)
	}
	if _, err = os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("materialized=%v", err)
	}
}
func alignmentCursorCommand(t *testing.T, body string, exit int) (Config, string) {
	t.Helper()
	home := t.TempDir()
	fixture := filepath.Join(home, "fixture.jsonl")
	if err := os.WriteFile(fixture, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	command := testutil.WriteCommand(t, home, "cursor-alignment", "#!/bin/sh\ncat > \"$AA_PROMPT\"\nprintf '%s\\n' \"$@\" > \"$AA_ARGS\"\ncat \"$AA_FIXTURE\"\nprintf 'fixture stderr\\n' >&2\nexit "+string(rune('0'+exit))+"\n", "@echo off\r\nset /p X=\r\ntype \"%AA_FIXTURE%\"\r\n>&2 echo fixture stderr\r\nexit /b "+string(rune('0'+exit))+"\r\n")
	cfg := Config{CommonConfig: CommonConfig{Command: command, CWD: home, Env: []driver.EnvBinding{{Name: "HOME", Value: home}, {Name: "USERPROFILE", Value: home}, {Name: "CURSOR_HOME", Value: filepath.Join(home, "cursor")}, {Name: "AA_FIXTURE", Value: fixture}, {Name: "AA_PROMPT", Value: filepath.Join(home, "prompt")}, {Name: "AA_ARGS", Value: filepath.Join(home, "args")}}}}
	return cfg, home
}
func TestAlignmentCursorDriverTransportAndOutput(t *testing.T) {
	body := `{"type":"assistant","session_id":"session-a","message":{"content":[{"type":"text","text":"正文"}]}}` + "\n" + alignmentCursorFrame("m", "started", "mcpToolCall", alignmentCursorMCPArgs, "") + alignmentCursorFrame("m", "completed", "mcpToolCall", alignmentCursorMCPArgs, `{"success":true}`) + alignmentCursorTerminal
	cfg, _ := alignmentCursorCommand(t, body, 0)
	d := Driver(cfg)
	caps := d.Descriptor().Observation
	expected := driver.ObservationSupport{MCP: true, Subagents: true}
	if caps.Batch != expected || caps.Streaming != expected {
		t.Fatalf("actual print matrix=%+v", caps)
	}
	var outputs []driver.Response
	for _, streaming := range []bool{false, true} {
		req := alignmentCursorCatalog()
		req.Prompt = "original"
		req.Streaming = streaming
		req.Workspace = driver.WorkspaceLease{CWD: cfg.CWD}
		s := &alignmentCursorSink{}
		r, err := d.Run(context.Background(), req, s)
		if err != nil {
			t.Fatal(err)
		}
		if len(s.facts()) != 2 || r.RawStreams.Stdout != body || r.RawStreams.Stderr != "fixture stderr\n" || r.Output != "正文" || r.Summary != "" || r.RawStreams.Terminal == nil || r.Checkpoint == nil || r.Usage != nil {
			t.Fatalf("response=%+v facts=%+v", r, s.facts())
		}
		outputs = append(outputs, r)
	}
	if !reflect.DeepEqual(outputs[0], outputs[1]) {
		t.Fatal("SPI streaming flag changed output")
	}
}

type alignmentCursorObserver struct {
	mu    sync.Mutex
	facts []capability.Invocation
}

func (o *alignmentCursorObserver) AttachRun(context.Context, string) (adaptor.RunAttachment, error) {
	return adaptor.RunAttachment{Observation: adaptor.ObservationDemand{CapabilityInvocations: true, Todos: true}, Observer: func(_ context.Context, _ adaptor.RunEventInfo, e adaptor.Event) error {
		if v, ok := e.(adaptor.CapabilityInvocation); ok {
			o.mu.Lock()
			defer o.mu.Unlock()
			o.facts = append(o.facts, v.Invocation)
		}
		return nil
	}}, nil
}
func (*alignmentCursorObserver) DetachRun(context.Context, string) error { return nil }
func TestAlignmentCursorPublicRunStreamDemandAndClear(t *testing.T) {
	body := alignmentCursorFrame("m", "started", "mcpToolCall", alignmentCursorMCPArgs, "") + alignmentCursorFrame("m", "completed", "mcpToolCall", alignmentCursorMCPArgs, `{"success":true}`) + alignmentCursorTerminal
	cfg, home := alignmentCursorCommand(t, body, 0)
	var results []*adaptor.Result
	var facts [][]capability.Invocation
	for _, streaming := range []bool{false, true} {
		observer := &alignmentCursorObserver{}
		a := adaptor.New(Driver(cfg), adaptor.WithWorkspace(home), adaptor.WithAppendSystemPrompt("must-be-cleared"), adaptor.WithRunServices(observer), adaptor.WithMCP(mcp.Stdio("知识__库", "unused-fixture-server")))
		var r *adaptor.Result
		var err error
		if !streaming {
			r, err = a.Run(context.Background(), "original", adaptor.WithAppendSystemPrompt(""))
		} else {
			stream := a.Stream(context.Background(), "original", adaptor.WithAppendSystemPrompt(""))
			var unavailable []string
			for event := range stream.Events() {
				switch e := event.(type) {
				case adaptor.Notice:
					if e.Data["code"] == "observation_unavailable" {
						unavailable, _ = e.Data["capabilities"].([]string)
					}
				case adaptor.TodoUpdated:
					t.Fatal("invented Todo")
				}
			}
			r, err = stream.Result()
			if !reflect.DeepEqual(unavailable, []string{"skill", "todo"}) {
				t.Fatalf("missing demand=%+v", unavailable)
			}
		}
		if err != nil {
			t.Fatal(err)
		}
		if err = a.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(observer.facts) != 2 {
			t.Fatalf("public facts=%+v", observer.facts)
		}
		normalized := append([]capability.Invocation(nil), observer.facts...)
		for i := range normalized {
			normalized[i].OccurredAt = time.Time{}
		}
		facts = append(facts, normalized)
		results = append(results, r)
	}
	if !reflect.DeepEqual(facts[0], facts[1]) || !reflect.DeepEqual(results[0].Raw(), results[1].Raw()) || !reflect.DeepEqual(results[0].Transcript(), results[1].Transcript()) || results[0].Text != results[1].Text || results[0].Summary != "" || results[0].Usage != nil {
		t.Fatalf("Run/Stream divergence: %+v %+v", results[0], results[1])
	}
	if runtime.GOOS != "windows" {
		prompt, err := os.ReadFile(filepath.Join(home, "prompt"))
		if err != nil || string(prompt) != "original" {
			t.Fatalf("prompt changed: %q %v", prompt, err)
		}
		args, err := os.ReadFile(filepath.Join(home, "args"))
		if err != nil || !strings.Contains(string(args), "-p\n--output-format\nstream-json\n") || strings.Contains(string(args), "acp") {
			t.Fatalf("wrong transport: %q %v", args, err)
		}
	}
}
func TestAlignmentCursorCapabilityReferenceConflictAndBounds(t *testing.T) {
	body := alignmentCursorFrame("x", "started", "mcpToolCall", alignmentCursorMCPArgs, "") + alignmentCursorFrame("x", "completed", "mcpToolCall", strings.ReplaceAll(alignmentCursorMCPArgs, "search__中文", "other"), `{"success":true}`)
	_, s := alignmentCursorParse(t, alignmentCursorCatalog(), body, false)
	if f := s.facts(); len(f) != 2 || f[1].Phase != capability.Interrupted {
		t.Fatalf("mismatched terminal=%+v", f)
	}
	s = &alignmentCursorSink{}
	p := newCursorParser(s)
	p.configureCapabilities(alignmentCursorCatalog())
	var args map[string]any
	_ = json.Unmarshal([]byte(alignmentCursorMCPArgs), &args)
	for i := 0; i < cursorObservationLimit+20; i++ {
		p.observeCapability(strconv.Itoa(i), "mcpToolCall", "started", map[string]any{"args": args})
	}
	p.closeCapabilities(false)
	if len(p.capabilities.refs) != cursorObservationLimit || len(s.facts()) != cursorObservationLimit*2 {
		t.Fatalf("unbounded tracker: %d", len(p.capabilities.refs))
	}
	count := 0
	for _, e := range s.events {
		if e.Data["reason"] == "invocation_limit" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("degradation notices=%d", count)
	}
}
func TestAlignmentCursorDirectInvalidAppendOrdering(t *testing.T) {
	for _, tc := range []struct{ text, reason string }{{string([]byte{0xff}), "invalid_utf8"}, {"a\x00b", "nul_byte"}, {" ", "unsupported_driver"}} {
		_, err := Driver(Config{}).Run(context.Background(), driver.Request{AppendSystemPrompt: tc.text}, &alignmentCursorSink{})
		var typed *driver.SystemPromptUnsupportedError
		if !errors.As(err, &typed) || typed.Reason != tc.reason {
			t.Fatalf("error=%v", err)
		}
	}
}

func TestAlignmentCursorPublicPartialOutcomes(t *testing.T) {
	prefix := `{"type":"assistant","session_id":"session-a","message":{"content":[{"type":"text","text":"partial text"}]}}` + "\n"
	for _, tc := range []struct {
		name, body string
		exit       int
	}{{"nonzero", prefix + alignmentCursorTerminal, 7}, {"malformed", prefix + "{broken\n" + alignmentCursorTerminal, 0}, {"missing terminal", prefix, 0}, {"missing checkpoint", prefix + strings.ReplaceAll(alignmentCursorTerminal, `"session_id":"session-a",`, ""), 0}} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, home := alignmentCursorCommand(t, tc.body, tc.exit)
			a := adaptor.New(Driver(cfg), adaptor.WithWorkspace(home))
			defer a.Close(context.Background())
			r, err := a.Run(context.Background(), "go")
			var failure *adaptor.RunError
			if r != nil || !errors.As(err, &failure) || failure.Result == nil {
				t.Fatalf("partial error=%T", err)
			}
			partial := failure.Result
			if partial.Text != "partial text" || partial.Raw().Stdout != tc.body || !strings.Contains(partial.Raw().Stderr, "fixture stderr") || len(partial.Transcript()) == 0 {
				t.Fatalf("partial contract=%+v", partial)
			}
		})
	}
}

type alignmentCursorCancelSink struct {
	alignmentCursorSink
	cancel context.CancelFunc
}

func (s *alignmentCursorCancelSink) EmitStream(e driver.StreamPayload) error {
	err := s.alignmentCursorSink.EmitStream(e)
	if e.Capability != nil && e.Capability.Phase == capability.Started {
		s.cancel()
	}
	return err
}
func TestAlignmentCursorCancelClosesObservedCallAndRetainsPartial(t *testing.T) {
	body := `{"type":"assistant","session_id":"session-a","message":{"content":[{"type":"text","text":"partial"}]}}` + "\n" + alignmentCursorFrame("c", "started", "mcpToolCall", alignmentCursorMCPArgs, "")
	cfg, home := alignmentCursorCommand(t, body, 0)
	cfg.Command = testutil.WriteCommand(t, home, "cursor-cancel", "#!/bin/sh\ncat >/dev/null\ncat \"$AA_FIXTURE\"\nexec sleep 30\n", "@echo off\r\nset /p X=\r\ntype \"%AA_FIXTURE%\"\r\nping -n 31 127.0.0.1 >nul\r\n")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &alignmentCursorCancelSink{cancel: cancel}
	req := alignmentCursorCatalog()
	req.Prompt = "go"
	req.Workspace = driver.WorkspaceLease{CWD: home}
	r, err := Driver(cfg).Run(ctx, req, s)
	if err != nil || r.Checkpoint != nil || r.RawStreams == nil || r.RawStreams.Stdout != body || r.Output != "partial" || !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("cancel response=%+v err=%v", r, err)
	}
	f := s.facts()
	if len(f) != 2 || f[1].Phase != capability.Cancelled || f[1].ErrorCode != capability.RunCancelled {
		t.Fatalf("cancel facts=%+v", f)
	}
}

func TestAlignmentCursorLaunchErrorKeepsInfrastructureCause(t *testing.T) {
	cfg := Config{CommonConfig: CommonConfig{Command: filepath.Join(t.TempDir(), "missing-command"), Env: []driver.EnvBinding{{Name: "CURSOR_HOME", Value: t.TempDir()}}}}
	r, err := Driver(cfg).Run(context.Background(), driver.Request{}, &alignmentCursorSink{})
	if !errors.Is(err, os.ErrNotExist) || r.Failure != nil || r.Checkpoint != nil {
		t.Fatalf("launch outcome=%+v err=%v", r, err)
	}
}
func TestAlignmentCursorInvalidUTF8CannotBecomeCatalogEvidence(t *testing.T) {
	req := alignmentCursorCatalog()
	req.MCP.Servers[0].Key = "�"
	body := strings.ReplaceAll(alignmentCursorFrame("x", "started", "mcpToolCall", alignmentCursorMCPArgs, ""), "知识__库", string([]byte{0xff})) + alignmentCursorTerminal
	p, s := alignmentCursorParse(t, req, body, false)
	if len(s.facts()) != 0 || p.checkpoint(0) != nil {
		t.Fatal("invalid UTF-8 was repaired into valid evidence")
	}
}

func TestAlignmentCursorFormalSpellingsAndStartConflict(t *testing.T) {
	for _, args := range []string{`{"providerIdentifier":"知识__库","toolName":"search__中文"}`, `{"serverIdentifier":"知识__库","providerIdentifier":"user-other-alias","name":"search__中文"}`} {
		_, s := alignmentCursorParse(t, alignmentCursorCatalog(), alignmentCursorFrame("x", "started", "mcpToolCall", args, "")+alignmentCursorFrame("x", "completed", "mcpToolCall", args, `{"success":true}`), false)
		f := s.facts()
		if len(f) != 2 || f[1].Phase != capability.Completed || f[0].Ref.Key != "知识__库" || f[0].Ref.Operation != "search__中文" {
			t.Fatalf("formal spelling=%+v", f)
		}
	}
	changed := strings.ReplaceAll(alignmentCursorMCPArgs, "DO_NOT_PROJECT", "changed arguments")
	_, s := alignmentCursorParse(t, alignmentCursorCatalog(), alignmentCursorFrame("x", "started", "mcpToolCall", alignmentCursorMCPArgs, "")+alignmentCursorFrame("x", "started", "mcpToolCall", changed, ""), false)
	if len(s.facts()) != 2 {
		t.Fatalf("conflicting start republished: %+v", s.facts())
	}
	conflicts := 0
	for _, e := range s.events {
		if e.Data["reason"] == "lifecycle_conflict" {
			conflicts++
		}
	}
	if conflicts != 1 {
		t.Fatalf("conflicting arguments were silently accepted: %d", conflicts)
	}
}

// Every present known alias must be valid independently; a valid second name
// cannot repair a malformed first one. Unknown additive fields remain opaque.
func TestAlignmentCursorMCPOperationAliases(t *testing.T) {
	for _, tc := range []struct {
		name, fields string
		observe      bool
	}{
		{"name only", `"name":"read"`, true},
		{"toolName only", `"toolName":"read"`, true},
		{"same aliases", `"toolName":"read","name":"read"`, true},
		{"different aliases", `"toolName":"write","name":"read"`, false},
		{"numeric toolName", `"toolName":17,"name":"read"`, false},
		{"empty toolName", `"toolName":"","name":"read"`, false},
		{"null toolName", `"toolName":null,"name":"read"`, false},
		{"object toolName", `"toolName":{},"name":"read"`, false},
		{"boolean toolName", `"toolName":true,"name":"read"`, false},
		{"numeric name", `"toolName":"read","name":17`, false},
		{"empty name", `"toolName":"read","name":""`, false},
		{"null name", `"toolName":"read","name":null`, false},
		{"unknown additive field", `"toolName":"read","futureOperation":17`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := `{"serverIdentifier":"知识__库",` + tc.fields + `}`
			body := alignmentCursorFrame("alias-call", "started", "mcpToolCall", args, "") + alignmentCursorFrame("alias-call", "completed", "mcpToolCall", args, `{"success":true}`) + alignmentCursorTerminal
			cfg, home := alignmentCursorCommand(t, body, 0)
			for _, streaming := range []bool{false, true} {
				req := alignmentCursorCatalog()
				req.Streaming = streaming
				req.Prompt = "unchanged"
				req.Workspace = driver.WorkspaceLease{CWD: home}
				sink := &alignmentCursorSink{}
				response, err := Driver(cfg).Run(context.Background(), req, sink)
				if err != nil || response.Failure != nil || response.Checkpoint == nil || !response.Checkpoint.Valid || response.RawStreams == nil || response.RawStreams.Stdout != body || response.RawStreams.Terminal == nil || response.Output != "done" || len(response.Transcript) != 4 {
					t.Fatalf("streaming=%t changed unrelated output: response=%+v err=%v", streaming, response, err)
				}
				facts := sink.facts()
				if tc.observe {
					if len(facts) != 2 || facts[0].Phase != capability.Started || facts[1].Phase != capability.Completed || facts[0].Ref.Operation != "read" {
						t.Fatalf("streaming=%t valid aliases=%+v", streaming, facts)
					}
				} else {
					if len(facts) != 0 {
						t.Fatalf("streaming=%t malformed alias accepted: %+v", streaming, facts)
					}
					notices := 0
					for _, e := range sink.events {
						if e.Data["reason"] == "invalid_reference" {
							notices++
						}
					}
					if notices != 1 {
						t.Fatalf("streaming=%t invalid alias notice count=%d", streaming, notices)
					}
				}
			}
		})
	}
	valid := `{"serverIdentifier":"知识__库","toolName":"read"}`
	invalid := `{"serverIdentifier":"知识__库","toolName":null,"name":"read"}`
	_, sink := alignmentCursorParse(t, alignmentCursorCatalog(), alignmentCursorFrame("pending", "started", "mcpToolCall", valid, "")+alignmentCursorFrame("pending", "completed", "mcpToolCall", invalid, `{"success":true}`), false)
	facts := sink.facts()
	if len(facts) != 2 || facts[1].Phase != capability.Interrupted {
		t.Fatalf("malformed terminal alias closed pending call successfully: %+v", facts)
	}
}
