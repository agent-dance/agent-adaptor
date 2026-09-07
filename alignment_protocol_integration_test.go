package adaptor_test

// T21 independent protocol QA. The provider fixtures are hand-written formal
// protocol bytes. Expected values below come from C02/C03, not production
// encoders, namespace helpers, or implementation-owner test outputs.
import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	a2aproto "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/errordetails"
	adaptor "github.com/agent-dance/agent-adaptor"
	bridge "github.com/agent-dance/agent-adaptor/bridges/a2a"
	"github.com/agent-dance/agent-adaptor/bridges/agui"
	"github.com/agent-dance/agent-adaptor/bridges/sse"
	"github.com/agent-dance/agent-adaptor/bridges/subagentstream"
	"github.com/agent-dance/agent-adaptor/capability"
	"github.com/agent-dance/agent-adaptor/claude"
	client "github.com/agent-dance/agent-adaptor/clients/a2a"
	"github.com/agent-dance/agent-adaptor/codebuddy"
	"github.com/agent-dance/agent-adaptor/codex"
	"github.com/agent-dance/agent-adaptor/cursor"
	"github.com/agent-dance/agent-adaptor/driver"
	delegation "github.com/agent-dance/agent-adaptor/hosttools/a2adelegation"
	"github.com/agent-dance/agent-adaptor/hosttools/capabilityrecorder"
	"github.com/agent-dance/agent-adaptor/hosttools/sessionrecorder"
	_ "github.com/agent-dance/agent-adaptor/internal/testutil/alignmentprotocol"
	"github.com/agent-dance/agent-adaptor/mcp"
	"github.com/agent-dance/agent-adaptor/memory"
	"github.com/agent-dance/agent-adaptor/profile"
	"github.com/agent-dance/agent-adaptor/threadstore"
	"github.com/agent-dance/agent-adaptor/todo"
	"go/parser"
	"go/token"
	"strconv"
)

func apContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}
func apJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}
func apTuple(parts ...string) string { // Independent C03 tuple oracle.
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	if err := e.Encode(parts); err != nil {
		panic(err)
	}
	return "aa1:" + base64.RawURLEncoding.EncodeToString(bytes.TrimSuffix(b.Bytes(), []byte{'\n'}))
}
func apDecodeTuple(t *testing.T, s string) []string {
	t.Helper()
	if !strings.HasPrefix(s, "aa1:") {
		t.Fatalf("not tuple: %q", s)
	}
	b, e := base64.RawURLEncoding.DecodeString(s[4:])
	if e != nil {
		t.Fatal(e)
	}
	var out []string
	if e = json.Unmarshal(b, &out); e != nil {
		t.Fatal(e)
	}
	return out
}

type apDriver struct {
	run   func(context.Context, driver.Request, driver.EventSink) (driver.Response, error)
	calls atomic.Int32
}

func (d *apDriver) Descriptor() driver.Descriptor {
	return driver.Descriptor{Type: "t21-formal-test", MCP: driver.MCPCapability{Supported: true, HTTP: true}, Observation: driver.ObservationCapabilities{Streaming: driver.ObservationSupport{Skills: true, MCP: true, Subagents: true, Todos: true}, Batch: driver.ObservationSupport{Skills: true, MCP: true, Subagents: true, Todos: true}}}
}
func (*apDriver) ValidateConfig(any) error { return nil }
func (d *apDriver) Run(c context.Context, r driver.Request, s driver.EventSink) (driver.Response, error) {
	d.calls.Add(1)
	return d.run(c, r, s)
}

type apAttachment struct {
	attach func(context.Context, string) (adaptor.RunAttachment, error)
	detach func(context.Context, string) error
}

func (p apAttachment) AttachRun(c context.Context, id string) (adaptor.RunAttachment, error) {
	if p.attach != nil {
		return p.attach(c, id)
	}
	return adaptor.RunAttachment{}, nil
}
func (p apAttachment) DetachRun(c context.Context, id string) error {
	if p.detach != nil {
		return p.detach(c, id)
	}
	return nil
}

// apRunner is an explicit third-party Runner fixture, never evidence of core
// lifecycle creation. Counters prove bridges execute only one Stream and read
// its Result after that same channel closes and its buffered tail is consumed.
type apRunner struct {
	preEvents                                                []adaptor.Event
	waitCancel                                               bool
	id                                                       string
	events                                                   []adaptor.Event
	result                                                   *adaptor.Result
	err                                                      error
	streamCalls, runCalls, resultCalls, earlyResult, cancels atomic.Int32
	cancelled                                                chan struct{}
	resultDone                                               chan struct{}
	resultInit, resultFinish                                 sync.Once
}

func (r *apRunner) resultBarrier() <-chan struct{} {
	r.resultInit.Do(func() { r.resultDone = make(chan struct{}) })
	return r.resultDone
}

func (r *apRunner) Run(context.Context, string, ...adaptor.CallOption) (*adaptor.Result, error) {
	r.runCalls.Add(1)
	return nil, errors.New("T21: parallel Run invoked")
}
func (r *apRunner) Stream(_ context.Context, _ string, _ ...adaptor.CallOption) adaptor.Stream {
	r.resultBarrier()
	r.streamCalls.Add(1)
	s := &apStream{owner: r, ch: make(chan adaptor.Event, len(r.events)), done: make(chan struct{})}
	go func() {
		for _, e := range r.preEvents {
			s.ch <- e
		}
		if r.waitCancel {
			<-r.cancelled
		}

		for _, e := range r.events {
			s.ch <- e
		}
		close(s.ch)
		close(s.done)
	}()
	return s
}

type apStream struct {
	owner *apRunner
	ch    chan adaptor.Event
	done  chan struct{}
	once  sync.Once
}

func (s *apStream) Events() <-chan adaptor.Event { return s.ch }
func (s *apStream) RunID() string                { return s.owner.id }
func (s *apStream) Cancel() {
	s.once.Do(func() {
		s.owner.cancels.Add(1)
		if s.owner.cancelled != nil {
			close(s.owner.cancelled)
		}
	})
}
func (s *apStream) Result() (*adaptor.Result, error) {
	s.owner.resultCalls.Add(1)
	defer s.owner.resultFinish.Do(func() { close(s.owner.resultDone) })
	<-s.done
	if len(s.ch) != 0 {
		s.owner.earlyResult.Add(1)
	}
	return s.owner.result, s.owner.err
}
func apMeta(e adaptor.Event, id string, n uint64) adaptor.Event {
	return adaptor.WithEventMeta(e, adaptor.EventMeta{RunID: id, Sequence: n, Time: time.Date(2026, 9, 7, 3, 0, 0, 0, time.UTC)})
}
func apFinish(meta, body, reason string, failed bool, n uint64) adaptor.Event {
	return apMeta(adaptor.RunFinished{RunID: body, Reason: adaptor.FailureReason(reason), Failed: failed}, meta, n)
}
func apDrain(s adaptor.Stream) ([]adaptor.Event, *adaptor.Result, error) {
	var events []adaptor.Event
	for e := range s.Events() {
		events = append(events, e)
	}
	r, err := s.Result()
	return events, r, err
}
func apAssertCalls(t *testing.T, r *apRunner) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	select {
	case <-r.resultBarrier():
	case <-deadline.C:
		t.Fatalf("Result did not complete: Stream/Run/Result/early=%d/%d/%d/%d", r.streamCalls.Load(), r.runCalls.Load(), r.resultCalls.Load(), r.earlyResult.Load())
	}
	if r.streamCalls.Load() != 1 || r.runCalls.Load() != 0 || r.resultCalls.Load() != 1 || r.earlyResult.Load() != 0 {
		t.Fatalf("Stream/Run/Result/early=%d/%d/%d/%d", r.streamCalls.Load(), r.runCalls.Load(), r.resultCalls.Load(), r.earlyResult.Load())
	}
}

func apServer(t *testing.T, r adaptor.Runner, ex bridge.ExposurePolicy, builder bridge.ResultBuilder) (*client.Client, string) {
	t.Helper()
	mux := http.NewServeMux()
	h := httptest.NewServer(mux)
	t.Cleanup(h.Close)
	b := bridge.NewServer(r, bridge.ServerOptions{AgentCard: bridge.AgentCard{Name: "T21", Version: "fixture", URL: h.URL + "/rpc"}, Exposure: ex, ResultBuilder: builder})
	mux.Handle("/rpc", b.Handler())
	mux.Handle("/.well-known/agent-card.json", b.AgentCardHandler())
	c := client.New(client.Options{AgentCardURL: h.URL + "/.well-known/agent-card.json", HTTPClient: h.Client()})
	t.Cleanup(func() { _ = c.Close() })
	return c, h.URL + "/.well-known/agent-card.json"
}
func apSendReq() client.SendRequest {
	return client.SendRequest{Message: client.Message{ID: "user-t21", Role: "user", Parts: []client.Part{{Kind: client.PartText, Text: "fixture task"}}}}
}
func apClientDrain(t *testing.T, c *client.Client, ctx context.Context) (client.Task, []client.Event) {
	t.Helper()
	s, e := c.SendStream(ctx, apSendReq())
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	var task client.Task
	var events []client.Event
	for {
		ev, err := s.RecvContext(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, ev)
		if ev.Task != nil {
			task = *ev.Task
		}
		if ev.Status != nil {
			task.Status = *ev.Status
			task.ID = ev.TaskID
		}
		if ev.Kind == client.EventTerminal {
			break
		}
	}
	return task, events
}
func apFailure(t *testing.T, status client.TaskStatus, want string) {
	t.Helper()
	if want == "unknown-carrier" {
		if status.State != client.TaskStateFailed || strings.Contains(apJSON(status), "agentadaptor.failure") {
			t.Fatal("unknown carrier repaired from event hint")
		}
		return
	}
	if want == "" {
		if status.State != client.TaskStateCompleted {
			t.Fatalf("not success: %+v", status)
		}
		return
	}
	state := client.TaskStateFailed
	if want == "cancelled" {
		state = client.TaskStateCanceled
	}
	if status.State != state {
		t.Fatalf("state=%s want=%s", status.State, state)
	}
	var got map[string]any
	if status.Message != nil {
		for _, p := range status.Message.Parts {
			if v, ok := p.Metadata["agentadaptor.failure"].(map[string]any); ok {
				got = v
			}
		}
	}
	if got["code"] != want {
		t.Fatalf("failure=%#v want=%s", got, want)
	}
	for k := range got {
		if k != "code" && k != "limit_ms" {
			t.Errorf("unsafe control key %s", k)
		}
	}
	if want != "active_execution_timeout" && got["limit_ms"] != nil {
		t.Errorf("secondary budget promoted: %#v", got)
	}
}

func TestAlignmentProtocolTerminalQualification(t *testing.T) {
	bare := errors.Join(&adaptor.ActiveExecutionTimeoutError{Limit: 777 * time.Second}, context.Canceled)
	valid := func(body string) []adaptor.Event {
		return []adaptor.Event{apFinish("run", ""+body, "cancelled", true, 1)}
	}
	cases := []struct {
		name, id string
		events   []adaptor.Event
		err      error
		want     string
	}{
		{"provider-id", "run", valid("provider-foreign"), bare, "cancelled"}, {"empty-body", "run", valid(""), bare, "cancelled"},
		{"foreign-meta", "run", []adaptor.Event{apFinish("foreign", "run", "cancelled", true, 1)}, bare, "active_execution_timeout"},
		{"empty-meta", "run", []adaptor.Event{apFinish("", "run", "cancelled", true, 1)}, bare, "active_execution_timeout"},
		{"empty-stream", "", valid(""), bare, "active_execution_timeout"},
		{"missing", "run", nil, bare, "active_execution_timeout"},
		{"not-failed", "run", []adaptor.Event{apFinish("run", "", "cancelled", false, 1)}, bare, "active_execution_timeout"},
		{"unknown", "run", []adaptor.Event{apFinish("run", "", "future_reason", true, 1)}, bare, "active_execution_timeout"},
		{"empty-reason", "run", []adaptor.Event{apFinish("run", "", "", true, 1)}, bare, "active_execution_timeout"},
		{"repeat", "run", append(valid("p"), apFinish("run", "p", "cancelled", true, 2)), bare, "active_execution_timeout"},
		{"conflict", "run", append(valid("p"), apFinish("run", "p", "agent_error", true, 2)), bare, "active_execution_timeout"},
		{"not-last", "run", append(valid("p"), apMeta(adaptor.Notice{Text: "after"}, "run", 2)), bare, "active_execution_timeout"},
		{"nil-error", "run", valid("p"), nil, ""},
		{"carrier-first", "run", []adaptor.Event{apFinish("run", "p", "active_execution_timeout", true, 1)}, &adaptor.RunError{Reason: adaptor.ReasonApprovalDenied, Result: &adaptor.Result{Text: "partial kept", Summary: "safe summary"}, Cause: bare}, "approval_denied"},
		{"carrier-unknown", "run", valid("p"), &adaptor.RunError{Reason: "future", Result: &adaptor.Result{Text: "partial kept", Summary: "safe summary"}, Cause: bare}, "unknown-carrier"},
		{"carrier-empty", "run", valid("p"), &adaptor.RunError{Result: &adaptor.Result{Text: "partial kept", Summary: "safe summary"}, Cause: bare}, "unknown-carrier"},
	}
	for _, tc := range cases {
		for _, path := range []string{"http-send", "http-stream", "local-send", "local-stream"} {
			t.Run(tc.name+"/"+path, func(t *testing.T) {
				ctx := apContext(t)
				r := &apRunner{id: tc.id, events: tc.events, err: tc.err}
				if tc.err == nil {
					r.result = &adaptor.Result{Text: "success"}
				}
				var status client.TaskStatus
				if strings.HasPrefix(path, "http") {
					c, _ := apServer(t, r, bridge.ExposurePolicy{}, nil)
					if path == "http-send" {
						task, err := c.Send(ctx, apSendReq())
						if err != nil {
							t.Fatal(err)
						}
						status = task.Status
					} else {
						task, _ := apClientDrain(t, c, ctx)
						status = task.Status
					}
				} else {
					svc, err := delegation.NewService(delegation.Config{Agents: []delegation.AgentRef{delegation.Local("member", r, delegation.Policy{})}})
					if err != nil {
						t.Fatal(err)
					}
					defer svc.Close()
					spec, _ := svc.Registry().Lookup("member")
					c := svc.Delegator().NewClient(spec)
					var task client.Task
					if path == "local-send" {
						task, err = c.Send(ctx, apSendReq())
					} else {
						s, e := c.SendStream(ctx, apSendReq())
						if e != nil {
							t.Fatal(e)
						}
						defer s.Close()
						for {
							ev, e := s.Recv()
							if errors.Is(e, io.EOF) {
								break
							}
							if e != nil {
								t.Fatal(e)
							}
							if ev.Kind == client.EventTerminal {
								task = *ev.Task
								break
							}
						}
					}
					if err != nil {
						t.Fatal(err)
					}
					status = task.Status
					if tc.name == "carrier-first" && (len(task.Messages) != 1 || task.Messages[0].Parts[0].Text != "partial kept") {
						t.Fatalf("partial lost: %+v", task)
					}
				}
				apFailure(t, status, tc.want)
				apAssertCalls(t, r)
			})
		}
	}
}

func apTool(id, name, parent string, input any) string {
	return apJSON(map[string]any{"type": "assistant", "parent_tool_use_id": parent, "message": map[string]any{"content": []any{map[string]any{"type": "tool_use", "id": id, "name": name, "input": input}}}}) + "\n"
}
func apToolResult(id, parent string, failed bool, result any) string {
	return apJSON(map[string]any{"type": "user", "parent_tool_use_id": parent, "tool_use_result": result, "message": map[string]any{"content": []any{map[string]any{"type": "tool_result", "tool_use_id": id, "content": "tool reply", "is_error": failed}}}}) + "\n"
}

const apTerminal = `{"type":"result","subtype":"success","session_id":"t21-session","is_error":false,"result":"final answer","usage":{"input_tokens":0,"output_tokens":3}}` + "\n"

func apClaudeFrames() string {
	var s strings.Builder
	s.WriteString(`{"type":"system","subtype":"init","session_id":"t21-session","model":"fixture-model"}` + "\n")
	for _, p := range []string{"parent:A", "parent/B"} {
		s.WriteString(apTool(p, "Agent", "", map[string]any{}))
		s.WriteString(apTool("same", "mcp__知识_库__search", p, map[string]any{"secret": "T21_ARG_SECRET"}))
		s.WriteString(apTool("same", "mcp__知识_库__search", p, map[string]any{"secret": "T21_ARG_SECRET"}))
	}
	s.WriteString(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"same","content":"bare result"}]}}` + "\n") // No parent selector: the bare result is ambiguous.
	for _, p := range []string{"parent:A", "parent/B"} {
		s.WriteString(apToolResult("same", p, false, nil))
		s.WriteString(apToolResult("same", p, false, nil))
	}
	s.WriteString(apTool("unknown", "mcp__absent__search", "", map[string]any{}))
	s.WriteString(apTool("ambiguous", "mcp__a_b__search", "", map[string]any{}))
	s.WriteString(apTool("orphan", "Read", "unknown-parent", map[string]any{}))
	s.WriteString(apTool("", "Read", "", map[string]any{}))
	s.WriteString(apTool("create", "TaskCreate", "", map[string]any{"subject": "检查中文"}))
	s.WriteString(apToolResult("create", "", false, map[string]any{"task": map[string]any{"id": "real-73"}}))
	s.WriteString(apTool("update", "TaskUpdate", "", map[string]any{"taskId": "real-73", "status": "in_progress"}))
	s.WriteString(apToolResult("update", "", false, nil))
	s.WriteString(apTool("bad-update", "TaskUpdate", "", map[string]any{"taskId": "1", "status": "completed"}))
	s.WriteString(apToolResult("bad-update", "", false, nil))
	s.WriteString(apTool("failed-clear", "TodoWrite", "", map[string]any{"todos": []any{}}))
	s.WriteString(apToolResult("failed-clear", "", true, nil))
	s.WriteString(apTool("invalid", "TodoWrite", "", map[string]any{"todos": []any{map[string]any{"content": "bad", "status": "unknown"}}}))
	s.WriteString(apToolResult("invalid", "", false, nil))
	s.WriteString(apTool("clear", "TodoWrite", "", map[string]any{"todos": []any{}}))
	s.WriteString(apToolResult("clear", "", false, nil))
	s.WriteString(apTool("clear-again", "TodoWrite", "", map[string]any{"todos": []any{}}))
	s.WriteString(apToolResult("clear-again", "", false, nil))
	s.WriteString(apTool("child-clear", "TodoWrite", "parent:A", map[string]any{"todos": []any{}}))
	s.WriteString(apToolResult("child-clear", "parent:A", false, nil))
	s.WriteString(`{"type":"assistant","message":{"content":[{"type":"text","text":"assistant fragment"}]}}` + "\n")
	s.WriteString(apTerminal)
	return s.String()
}
func apProvider(t *testing.T, kind, frames string, options ...adaptor.Option) *adaptor.Agent {
	t.Helper()
	dir := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	protocol := filepath.Join(dir, "protocol.jsonl")
	if err = os.WriteFile(protocol, []byte(frames), 0600); err != nil {
		t.Fatal(err)
	}
	// Only generated absolute paths reach this shim; shell quoting is explicit.
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	command := filepath.Join(dir, "provider.sh")
	if err = os.WriteFile(command, []byte("#!/bin/sh\nexec "+quote(exe)+" \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	env := []driver.EnvBinding{{Name: "AGENT_ADAPTOR_T21_PRIVATE_CHILD", Value: "fixture-v1"}, {Name: "T21_PROVIDER", Value: kind}, {Name: "T21_PROTOCOL", Value: protocol}, {Name: "HOME", Value: dir}, {Name: "USERPROFILE", Value: dir}, {Name: "XDG_CONFIG_HOME", Value: dir}, {Name: "GORACE", Value: "atexit_sleep_ms=0"}}
	common := driver.CommonConfig{Command: command, CWD: dir, Env: env}
	var d driver.Driver
	switch kind {
	case "claude":
		d = claude.Driver(claude.Config{CommonConfig: common, Model: "fixture-model"})
	case "codebuddy":
		d = codebuddy.Driver(codebuddy.Config{CommonConfig: common, Model: "fixture-model"})
	case "cursor":
		d = cursor.Driver(cursor.Config{CommonConfig: common, Model: "fixture-model"})
	case "codex":
		d = codex.Driver(codex.Config{CommonConfig: common, Model: "fixture-model"})
	default:
		t.Fatalf("bad provider %s", kind)
	}
	opts := []adaptor.Option{adaptor.WithWorkspace(dir), adaptor.WithProfile(profile.Dedicated(dir)), adaptor.WithMCP(mcp.Stdio("知识_库", "fixture-unused"), mcp.Stdio("a.b", "fixture-unused"), mcp.Stdio("a_b", "fixture-unused")), adaptor.WithRunServices(apAttachment{attach: func(context.Context, string) (adaptor.RunAttachment, error) {
		return adaptor.RunAttachment{Observation: adaptor.ObservationDemand{CapabilityInvocations: true, Todos: true}}, nil
	}})}
	opts = append(opts, options...)
	a := adaptor.New(d, opts...)
	t.Cleanup(func() {
		ctx, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		if err := a.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return a
}
func apFacts(events []adaptor.Event) ([]adaptor.CapabilityInvocation, []adaptor.TodoUpdated) {
	var caps []adaptor.CapabilityInvocation
	var todos []adaptor.TodoUpdated
	for _, e := range events {
		switch v := e.(type) {
		case adaptor.CapabilityInvocation:
			caps = append(caps, v)
		case adaptor.TodoUpdated:
			todos = append(todos, v)
		}
	}
	return caps, todos
}
func apLifecycle(t *testing.T, events []adaptor.Event, err error) {
	t.Helper()
	starts, ends := 0, 0
	var last uint64
	for i, e := range events {
		m := e.Meta()
		if m.RunID == "" || m.Sequence <= last || m.Time.IsZero() {
			t.Fatalf("invalid authoritative meta: %+v", m)
		}
		last = m.Sequence
		switch v := e.(type) {
		case adaptor.RunStarted:
			starts++
		case adaptor.RunFinished:
			ends++
			if i != len(events)-1 {
				t.Error("terminal not last")
			}
			if v.Failed != (err != nil) {
				t.Errorf("terminal failure=%v error=%v", v.Failed, err)
			}
		}
	}
	if starts != 1 || ends != 1 {
		t.Fatalf("core lifecycle %d/%d", starts, ends)
	}
}
func apDecodeEvents(t *testing.T, events []client.Event) []adaptor.Event {
	t.Helper()
	var out []adaptor.Event
	for _, ev := range events {
		if ev.Status == nil || ev.Status.Message == nil {
			continue
		}
		for _, p := range ev.Status.Message.Parts {
			if p.Kind == client.PartData {
				e, matched, err := bridge.DecodeAdapterEventV1(p.Data)
				if err != nil {
					t.Fatal(err)
				}
				if matched {
					out = append(out, e)
				}
			}
		}
	}
	return out
}

func TestAlignmentProtocolFormalClaude(t *testing.T) {
	ctx := apContext(t)
	a := apProvider(t, "claude", apClaudeFrames())
	events, r, err := apDrain(a.Stream(ctx, "first"))
	if err != nil {
		t.Fatal(err)
	}
	apLifecycle(t, events, err)
	caps, todos := apFacts(events)
	if len(caps) != 4 || len(todos) != 4 {
		t.Fatalf("facts/todos=%d/%d events=%s", len(caps), len(todos), apJSON(events))
	}
	for i, c := range caps {
		if c.Invocation.Ref != (capability.Ref{Kind: capability.MCP, Key: "知识_库", Operation: "search"}) || c.Invocation.Source != capability.Provider || c.Invocation.Evidence != capability.ProviderProtocol {
			t.Fatalf("fact=%+v", c)
		}
		if c.Invocation.ScopeID == "" || c.Invocation.ParentToolCallID == "" {
			t.Fatal("parent missing")
		}
		if i < 2 && c.Invocation.Phase != capability.Started || i >= 2 && c.Invocation.Phase != capability.Completed {
			t.Fatal("phase order")
		}
	}
	if caps[0].Invocation.ScopeID == caps[1].Invocation.ScopeID || caps[0].Invocation.InvocationID != caps[2].Invocation.InvocationID || caps[1].Invocation.InvocationID != caps[3].Invocation.InvocationID {
		t.Fatal("sibling/replay identity")
	}
	if len(todos[0].Snapshot.Items) != 1 || todos[0].Snapshot.Items[0].ID != "real-73" || todos[0].Snapshot.Items[0].SyntheticID || todos[1].Snapshot.Items[0].Status != todo.InProgress || todos[2].Snapshot.Items == nil || len(todos[2].Snapshot.Items) != 0 || todos[2].Snapshot.Revision != 3 || todos[3].Snapshot.ParentToolCallID != "parent:A" || todos[3].Snapshot.Revision != 1 {
		t.Fatalf("todo snapshots=%+v", todos)
	}
	counts := map[string][3]int{}
	notices := map[string]bool{}
	for _, e := range events {
		switch v := e.(type) {
		case adaptor.ToolCall:
			if v.ID == "same" {
				n := counts[v.ScopeID]
				switch v.Phase {
				case adaptor.PhaseStart:
					n[0]++
					if v.Args["secret"] != "T21_ARG_SECRET" {
						t.Fatal("full args lost")
					}
				case adaptor.PhaseEnd:
					n[1]++
				default:
					t.Fatal("duplicate args delta for full wrapper")
				}
				counts[v.ScopeID] = n
			}
			if v.ID == "orphan" || v.ID == "" {
				t.Fatalf("invented lifecycle %+v", v)
			}
		case adaptor.ToolResult:
			if v.ID == "same" {
				n := counts[v.ScopeID]
				n[2]++
				counts[v.ScopeID] = n
			}
		case adaptor.Notice:
			if c, ok := v.Data["code"].(string); ok {
				notices[c] = true
			}
		}
	}
	for scope, n := range counts {
		if n != [3]int{1, 1, 1} {
			t.Fatalf("scope %s lifecycle %v", scope, n)
		}
	}
	if len(counts) != 2 || !notices["tool_result_ambiguous"] || !notices["parent_unresolved"] || !notices["todo_unknown_id"] || !notices["todo_invalid"] {
		t.Fatalf("negative evidence=%v", notices)
	}
	if r.Text != "final answer" || r.Raw().Stdout != apClaudeFrames() || r.Raw().Stderr != "t21 private stderr" || r.Raw().Terminal == nil || !strings.Contains(string(r.Raw().Terminal.JSON), `"result":"final answer"`) || r.Usage == nil || r.Usage.OutputTokens != 3 {
		t.Fatalf("layers=%+v raw=%+v", r, r.Raw())
	}
	transcriptTools := map[string]int{}
	for _, item := range r.Transcript() {
		if item.ToolUseID == "same" && item.Kind == driver.TranscriptToolCall {
			transcriptTools[item.ScopeID]++
			if item.ParentToolCallID == "" {
				t.Fatal("transcript parent lost")
			}
		}
	}
	if len(transcriptTools) != 2 {
		t.Fatal("transcript siblings lost")
	}
	run, err := a.Run(ctx, "second")
	if err != nil {
		t.Fatal(err)
	}
	if run.Text != r.Text || run.Summary != r.Summary || !reflect.DeepEqual(run.Raw(), r.Raw()) || !reflect.DeepEqual(run.Usage, r.Usage) {
		t.Fatal("Run/Stream layers differ")
	}
	apOtherBridges(t, events, r)
	for _, ex := range []struct {
		name string
		p    bridge.ExposurePolicy
		want int
	}{{"default", bridge.ExposurePolicy{}, 0}, {"tools-only", bridge.ExposurePolicy{IncludeToolCalls: true}, 0}, {"opt-in", bridge.ExposurePolicy{IncludeCapabilityInvocations: true, IncludeTodos: true, IncludeToolCalls: true}, 4}} {
		t.Run(ex.name, func(t *testing.T) {
			c, _ := apServer(t, a, ex.p, nil)
			task, wire := apClientDrain(t, c, ctx)
			if task.Status.State != client.TaskStateCompleted {
				t.Fatal(task.Status)
			}
			got := apDecodeEvents(t, wire)
			caps, todos := apFacts(got)
			if len(caps) != ex.want || len(todos) != ex.want {
				t.Fatalf("exposure facts=%d/%d", len(caps), len(todos))
			}
			if ex.want > 0 {
				if todos[2].Snapshot.Items == nil || len(todos[2].Snapshot.Items) != 0 || caps[0].Invocation.ParentToolCallID != "parent:A" {
					t.Fatal("wire parent/clear")
				}
				if strings.Contains(apJSON(caps), "T21_ARG_SECRET") {
					t.Fatal("capability secret")
				}
			}
		})
	}
}
func apOtherBridges(t *testing.T, events []adaptor.Event, r *adaptor.Result) {
	t.Helper()
	id := events[0].Meta().RunID
	archive := t.TempDir()
	backend, err := sessionrecorder.NewJSONLEventBackend(archive)
	if err != nil {
		t.Fatal(err)
	}
	rec := sessionrecorder.NewEventRecorder(backend)
	for _, e := range events {
		if _, err := rec.Record(context.Background(), "t21", e); err != nil {
			t.Fatal(err)
		}
	}
	if err := rec.Close(); err != nil {
		t.Fatal(err)
	}
	backend, err = sessionrecorder.NewJSONLEventBackend(archive)
	if err != nil {
		t.Fatal(err)
	}
	rec = sessionrecorder.NewEventRecorder(backend)
	rows, err := rec.Since(context.Background(), "t21", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(events) {
		t.Fatal("recorder lost events")
	}
	for i, row := range rows {
		// Opaque JSON data decodes arrays as []any; typed observations and
		// authoritative metadata retain exact values across a fresh disk read.
		if reflect.TypeOf(row.Event) != reflect.TypeOf(events[i]) || apJSON(row.Event) != apJSON(events[i]) || !reflect.DeepEqual(row.Event.Meta(), events[i].Meta()) {
			t.Fatalf("recorder event %d %T differs after reopening disk", i, events[i])
		}
		switch events[i].(type) {
		case adaptor.CapabilityInvocation, adaptor.TodoUpdated:
			if !reflect.DeepEqual(row.Event, events[i]) {
				t.Fatal("typed observation changed on disk")
			}
		}
	}
	if err := rec.Close(); err != nil {
		t.Fatal(err)
	}
	replay := &apRunner{id: id, events: events, result: r}
	var ag []map[string]any
	for e := range agui.Events(replay.Stream(context.Background(), "replay")) {
		var v map[string]any
		if err := json.Unmarshal([]byte(apJSON(e)), &v); err != nil {
			t.Fatal(err)
		}
		ag = append(ag, v)
	}
	encoded := apJSON(ag)
	if !strings.Contains(encoded, "adapter.capability.invocation") || !strings.Contains(encoded, "adapter.todo.updated") || !strings.Contains(encoded, `"items":[]`) {
		t.Fatal("AGUI capability/todo projection missing")
	}
	directTools := false
	for _, e := range events {
		if _, ok := e.(adaptor.ToolCall); ok {
			directTools = true
		}
	}
	if directTools && !strings.Contains(encoded, "adapter.tool.parent") {
		t.Fatal("AGUI direct tool parent missing")
	}
	if !directTools && !strings.Contains(encoded, `"parentToolCallId":"host-tool"`) {
		t.Fatal("AGUI delegation parent missing")
	}
	// Keep each public translation result alive while translating the next
	// real core events. Earlier output must remain the snapshot it represented,
	// even when the consumer serializes it after the run has finished.
	translator := agui.NewEventTranslator()
	type snapshot struct {
		event any
		json  string
	}
	var snapshots []snapshot
	for _, e := range events {
		for _, translated := range translator.Translate(e) {
			snapshots = append(snapshots, snapshot{event: translated, json: apJSON(translated)})
		}
	}
	translator.CloseResult(r, nil)
	for i, snapshot := range snapshots {
		if got := apJSON(snapshot.event); got != snapshot.json {
			t.Errorf("AGUI published snapshot %d mutated: before=%s after=%s", i, snapshot.json, got)
		}
	}
	raw := httptest.NewRecorder()
	sse.Handler(&apRunner{id: id, events: events, result: r}, sse.Options{Protocol: sse.Raw}).ServeHTTP(raw, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"prompt":"replay"}`)))
	body := raw.Body.String()
	if raw.Code != 200 || !strings.Contains(body, "event: capability.invocation") || !strings.Contains(body, "event: todo.updated") || !strings.Contains(body, `"items":[]`) {
		t.Fatal("SSE capability/todo projection missing")
	}
	if directTools && !strings.Contains(body, `"parent_tool_call_id":"parent:A"`) {
		t.Fatal("SSE direct parent missing")
	}
}

func TestAlignmentProtocolServiceRelay(t *testing.T) {
	for _, path := range []string{"local", "remote", "nested-remote"} {
		t.Run(path, func(t *testing.T) {
			ctx := apContext(t)
			leaf := apProvider(t, "claude", apClaudeFrames())
			var target adaptor.Runner = leaf
			if path == "nested-remote" {
				_, url := apServer(t, leaf, bridge.ExposurePolicy{IncludeCapabilityInvocations: true, IncludeTodos: true, IncludeToolCalls: true, Diagnostics: bridge.DiagnosticsPolicy{IncludeMetadata: true}}, nil)
				nested, err := delegation.NewService(delegation.Config{Agents: []delegation.AgentRef{delegation.Remote("inner", url, delegation.Policy{})}, NewID: func() string { return "inner-delegation" }})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = nested.Close() })
				nd := &apDriver{run: func(c context.Context, r driver.Request, s driver.EventSink) (driver.Response, error) {
					_, err := nested.Delegate(c, delegation.DelegationRequest{RunID: r.RunID, Agent: "inner", Objective: "nested", ScopeID: "middle", ParentScopeID: "middle-parent", ParentToolCallID: "middle-tool"})
					return driver.Response{Output: "nested done"}, err
				}}
				na := adaptor.New(nd, nested.Option())
				t.Cleanup(func() { _ = na.Close(context.Background()) })
				target = na
			}
			ref := delegation.Local("member", target, delegation.Policy{})
			if path != "local" {
				_, url := apServer(t, target, bridge.ExposurePolicy{IncludeCapabilityInvocations: true, IncludeTodos: true, IncludeToolCalls: true, Diagnostics: bridge.DiagnosticsPolicy{IncludeMetadata: true}}, nil)
				ref = delegation.Remote("member", url, delegation.Policy{})
			}
			var ids atomic.Int32
			svc, err := delegation.NewService(delegation.Config{Agents: []delegation.AgentRef{ref}, NewID: func() string { return fmt.Sprintf("domain/%d", ids.Add(1)) }, ReplayLimit: 1})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = svc.Close() })
			rec, err := capabilityrecorder.New(capabilityrecorder.Config{Store: capabilityrecorder.NewMemoryStore()})
			if err != nil {
				t.Fatal(err)
			}
			var observed []adaptor.Event
			var slowEvents []delegation.Event
			var observerMu sync.Mutex
			observer := apAttachment{attach: func(context.Context, string) (adaptor.RunAttachment, error) {
				return adaptor.RunAttachment{Observer: func(c context.Context, info adaptor.RunEventInfo, e adaptor.Event) error {
					observerMu.Lock()
					observed = append(observed, e)
					observerMu.Unlock()
					// A fresh replay at this instant cannot already contain this fact: the
					// publisher must finish observer delivery before EventBus.Publish.
					q, cancel := context.WithCancel(c)
					bus := svc.Bus().SubscribeRun(q, info.RunID)
					for len(bus) > 0 {
						v := <-bus
						if fact, ok := e.(adaptor.CapabilityInvocation); ok && v.Capability != nil && v.Capability.InvocationID == fact.Invocation.InvocationID && v.Capability.Phase == fact.Invocation.Phase {
							t.Error("bus published before observer")
						}
					}
					cancel()
					return nil
				}}, nil
			}}
			var runID string
			d := &apDriver{run: func(c context.Context, r driver.Request, s driver.EventSink) (driver.Response, error) {
				runID = r.RunID
				busCtx, cancel := context.WithCancel(c)
				defer cancel()
				slow := svc.Bus().SubscribeRun(busCtx, r.RunID)
				for i := 0; i < 2; i++ {
					result, err := svc.Delegate(c, delegation.DelegationRequest{RunID: r.RunID, ScopeID: "caller", ParentScopeID: "actual-parent", ParentToolCallID: "host-tool", Agent: "member", Objective: "roundtrip", IncludeRemoteArtifacts: true})
					if err != nil {
						return driver.Response{}, err
					}
					if !result.HasLine("final answer") && path != "nested-remote" {
						return driver.Response{}, errors.New("delegation text lost")
					}
				}
				for len(slow) > 0 {
					slowEvents = append(slowEvents, <-slow)
				}
				page, err := rec.Query(c, capabilityrecorder.Query{Scope: capabilityrecorder.Scope{RunID: r.RunID}, Limit: 100})
				if err != nil || len(page.Records) == 0 {
					t.Errorf("live query empty %v", err)
				}
				return driver.Response{Output: "parent done"}, nil
			}}
			a := adaptor.New(d, adaptor.WithRunServices(observer), svc.Option(), rec.Option())
			defer a.Close(context.Background())
			original := a.Stream(ctx, "parent")
			merged := subagentstream.Merge(ctx, original, svc.Bus())
			if merged.RunID() != original.RunID() {
				t.Fatal("Merge RunID changed")
			}
			events, r, err := apDrain(merged)
			if err != nil {
				t.Fatal(err)
			}
			if r.Text != "parent done" {
				t.Fatal("parent result changed")
			}
			apLifecycle(t, events, err)
			caps, todos := apFacts(events)
			want := 12
			if path == "nested-remote" {
				want = 16
			}
			if len(caps) != want || len(todos) != 8 {
				t.Fatalf("relay facts/todos %d/%d want %d/8", len(caps), len(todos), want)
			}
			observerMu.Lock()
			obsCaps, obsTodos := apFacts(observed)
			observerMu.Unlock()
			if len(obsCaps) != len(caps) || len(obsTodos) != len(todos) {
				t.Fatal("observer lost facts")
			}
			for i, c := range caps {
				if c.Meta().Sequence != obsCaps[i].Meta().Sequence {
					t.Fatal("observer order differs")
				}
			}
			seen := map[string]map[capability.Phase]int{}
			outer := 0
			for _, c := range caps {
				v := c.Invocation
				if seen[v.InvocationID] == nil {
					seen[v.InvocationID] = map[capability.Phase]int{}
				}
				seen[v.InvocationID][v.Phase]++
				if v.Source == capability.Host {
					outer++
					if v.Ref != (capability.Ref{Kind: capability.Subagent, Key: "member", Operation: "spawn"}) || v.ScopeID != "caller" || v.ParentScopeID != "actual-parent" || v.ParentToolCallID != "host-tool" {
						t.Fatalf("outer fields=%+v", v)
					}
					if v.InvocationID != apTuple("delegation", fmt.Sprintf("domain/%d", (outer+1)/2)) {
						t.Error("outer namespace differs")
					}
				} else {
					if v.Source != capability.Relay || v.Evidence != capability.Relayed || c.Meta().Source == nil || c.Meta().Source.RunID == "" || c.Meta().Source.Sequence == 0 {
						t.Fatalf("source missing %+v", c)
					}
					tuple := apDecodeTuple(t, v.InvocationID)
					if len(tuple) != 5 || tuple[0] != "invocation" || !strings.HasPrefix(tuple[1], "domain/") {
						t.Fatalf("wrong tuple %v", tuple)
					}
					if path == "nested-remote" && v.Ref.Kind == capability.MCP && c.Meta().Source.Upstream == nil {
						t.Fatal("nested source chain lost")
					}
				}
			}
			for id, phases := range seen {
				if phases[capability.Started] != 1 || phases[capability.Completed] != 1 || len(phases) != 2 {
					t.Fatalf("duplicate lifecycle %s: %v", id, phases)
				}
			}
			page, err := rec.Query(ctx, capabilityrecorder.Query{Scope: capabilityrecorder.Scope{RunID: runID}, Limit: 1})
			if err != nil || len(page.Records) != 1 || page.NextSequence != page.Records[0].Sequence {
				t.Fatal("pagination")
			}
			first := page.Records[0]
			first.Invocation.Ref.Key = "mutated"
			again, _ := rec.Query(ctx, capabilityrecorder.Query{Scope: capabilityrecorder.Scope{RunID: runID}, Limit: 1})
			if again.Records[0].Invocation.Ref.Key == "mutated" {
				t.Fatal("query aliases store")
			}
			other, _ := rec.Query(ctx, capabilityrecorder.Query{Scope: capabilityrecorder.Scope{RunID: runID, IdentityID: "other"}})
			if len(other.Records) != 0 {
				t.Fatal("identity leak")
			}
			drops := 0
			for _, e := range slowEvents {
				if e.Kind == delegation.DelegationStreamDropped {
					drops++
				}
			}
			if path != "nested-remote" && drops == 0 {
				t.Fatal("slow bus did not exercise drop")
			}
			if !svc.Bus().RunEventsBound(runID) {
				t.Fatal("detach removed historical binding proof")
			}
			if path == "local" {
				apOtherBridges(t, events, r)
			}
		})
	}
}

func TestAlignmentProtocolObserverIsolationAndDrop(t *testing.T) {
	for _, mode := range []string{"error", "panic", "timeout", "reentry"} {
		t.Run(mode, func(t *testing.T) {
			ctx := apContext(t)
			var calls atomic.Int32
			release := make(chan struct{})
			var publisher adaptor.RunEventPublisher
			service := apAttachment{attach: func(context.Context, string) (adaptor.RunAttachment, error) {
				return adaptor.RunAttachment{BindEvents: func(p adaptor.RunEventPublisher) error { publisher = p; return nil }, Observer: func(c context.Context, _ adaptor.RunEventInfo, e adaptor.Event) error {
					calls.Add(1)
					switch mode {
					case "error":
						return errors.New("T21_OBSERVER_SECRET")
					case "panic":
						panic("T21_OBSERVER_SECRET")
					case "timeout":
						<-release
						return errors.New("late-secret")
					case "reentry":
						return publisher(c, e)
					}
					return nil
				}}, nil
			}}
			a := apProvider(t, "claude", apClaudeFrames(), adaptor.WithRunServices(service))
			events, r, err := apDrain(a.Stream(ctx, "observe"))
			close(release)
			if err != nil || r == nil || r.Text != "final answer" {
				t.Fatalf("observer changed result: %v", err)
			}
			if calls.Load() != 1 {
				t.Fatalf("calls=%d", calls.Load())
			}
			disabled := 0
			for _, e := range events {
				if n, ok := e.(adaptor.Notice); ok && n.Data["code"] == "observation_disabled" {
					disabled++
					if strings.Contains(apJSON(n), "SECRET") || strings.Contains(apJSON(n), "late-secret") {
						t.Fatal("observer secret leaked")
					}
				}
			}
			if disabled != 1 {
				t.Fatalf("disabled notices=%d", disabled)
			}
			apLifecycle(t, events, nil)
		})
	}
	for _, eventKind := range []string{"capability.invocation", "todo.updated"} {
		t.Run("cancel-"+eventKind, func(t *testing.T) {
			ctx := apContext(t)
			accepted := make(chan adaptor.Event, 1)
			releaseObserver := make(chan struct{})
			service := apAttachment{attach: func(context.Context, string) (adaptor.RunAttachment, error) {
				return adaptor.RunAttachment{Observer: func(_ context.Context, _ adaptor.RunEventInfo, e adaptor.Event) error {
					if eventKind == "todo.updated" {
						if _, ok := e.(adaptor.TodoUpdated); !ok {
							return nil
						}
					} else {
						if _, ok := e.(adaptor.CapabilityInvocation); !ok {
							return nil
						}
					}
					select {
					case accepted <- e:
						<-releaseObserver
					default:
					}
					return nil
				}}, nil
			}}
			a := apProvider(t, "claude", apClaudeFrames(), adaptor.WithEventBuffer(1), adaptor.WithBlockingEvents(), adaptor.WithRunServices(service))
			s := a.Stream(ctx, "cancel")
			var fact adaptor.Event
			// Drain through process/tool markers until a fact is accepted, then stop
			// consuming. Observer and cancellation barriers avoid wall-clock guesses.
		loop:
			for {
				select {
				case fact = <-accepted:
					break loop
				case _, ok := <-s.Events():
					if !ok {
						t.Fatal("closed before fact")
					}
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			s.Cancel()
			s.Cancel()
			close(releaseObserver)
			// Keep the buffer saturated until cancellation completes. Result is
			// allowed concurrently and Cancel must release every blocked sender.
			finished := make(chan struct{})
			go func() { _, _ = s.Result(); close(finished) }()
			select {
			case <-finished:
			case <-ctx.Done():
				t.Fatal("cancel did not unblock saturated stream")
			}
			events, r, err := apDrain(s)
			if r != nil || err == nil {
				t.Fatalf("cancel outcome %v/%v", r, err)
			}
			if fact.Meta().Sequence == 0 {
				t.Fatal("observer before stamp")
			}
			var re *adaptor.RunError
			if !errors.As(err, &re) || re.Result == nil {
				t.Fatal("partial carrier missing")
			}
			if len(events) == 0 {
				t.Fatal("cancel tail lost")
			}
			terminal, ok := events[len(events)-1].(adaptor.RunFinished)
			if !ok || !terminal.Failed {
				t.Fatal("cancel terminal lost")
			}
			dropCount := 0
			for _, e := range events {
				if d, ok := e.(adaptor.Dropped); ok {
					dropCount += d.ByKind[eventKind]
					sum := 0
					for _, n := range d.ByKind {
						sum += n
					}
					if sum != d.Count || d.FirstSequence == 0 || d.LastSequence < d.FirstSequence {
						t.Fatal("incomplete drop accounting")
					}
				}
			}
			if dropCount == 0 {
				t.Fatal("accepted critical fact was not accounted after cancel barrier")
			}
		})
	}
}

func TestAlignmentProtocolEveryDrain(t *testing.T) {
	for _, mode := range []string{"normal", "server-cancel", "translation-error", "builder-error"} {
		t.Run(mode, func(t *testing.T) {
			ctx := apContext(t)
			original := errors.Join(context.Canceled, &adaptor.ActiveExecutionTimeoutError{Limit: 777 * time.Second})
			r := &apRunner{id: "drain-run", events: []adaptor.Event{apFinish("drain-run", "provider-body", "cancelled", true, 9)}, err: original, cancelled: make(chan struct{})}
			var builder bridge.ResultBuilder
			switch mode {
			case "server-cancel":
				// Cancel through the public protocol; the server intentionally detaches execution from HTTP request contexts.
				r.waitCancel = true
			case "translation-error":
				v := adaptor.CapabilityInvocation{Invocation: capability.Invocation{InvocationID: "i", Ref: capability.Ref{Kind: capability.MCP, Key: "catalog", Operation: "search"}, Phase: capability.Started, Evidence: capability.ProviderProtocol, Source: capability.Provider, OccurredAt: time.Now().UTC()}}
				r.preEvents = []adaptor.Event{adaptor.WithEventMeta(v, adaptor.EventMeta{RunID: r.id, Sequence: 1, Time: time.Now().UTC(), ThreadKey: strings.Repeat("opaque/", 12000)})}
				r.waitCancel = true
			case "builder-error":
				r.err = nil
				r.result = &adaptor.Result{Text: "builder original", Summary: "summary"}
				builder = func(_ context.Context, _ bridge.InboundRequest, res *adaptor.Result) (bridge.BuiltResult, error) {
					if res != r.result {
						t.Error("builder got wrong Result")
					}
					return bridge.BuiltResult{}, errors.New("T21_BUILDER_SECRET")
				}
			}
			c, _ := apServer(t, r, bridge.ExposurePolicy{IncludeCapabilityInvocations: true}, builder)
			s, err := c.SendStream(ctx, apSendReq())
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			var last client.TaskStatus
			var translationEvents []client.Event
			var transportError error
			cancelSent := false
			for {
				e, err := s.RecvContext(ctx)
				if err != nil {
					transportError = err
					break
				}
				if mode == "translation-error" {
					translationEvents = append(translationEvents, e)
				}
				if e.Task != nil {
					last = e.Task.Status
					if mode == "server-cancel" && !cancelSent {
						for r.streamCalls.Load() == 0 {
							select {
							case <-ctx.Done():
								t.Fatal(ctx.Err())
							case <-time.After(time.Millisecond):
							}
						}
						ack, ce := c.CancelTask(ctx, client.CancelTaskRequest{TaskID: e.Task.ID})
						if ce != nil {
							t.Fatal(ce)
						}
						if ack.Status.State != client.TaskStateCanceled || strings.Contains(apJSON(ack.Status), "limit") {
							t.Fatal("unsafe cancel acknowledgment")
						}
						cancelSent = true
					}
				}
				if e.Status != nil {
					last = *e.Status
				}
			}
			if mode != "translation-error" && !errors.Is(transportError, io.EOF) {
				t.Fatal(transportError)
			}
			// A canceled HTTP context may close the response before the executor's
			// terminal write. Only completion/drain is observable on that branch.
			if mode == "server-cancel" {
				if last.State == client.TaskStateCompleted {
					t.Fatal("cancellation fabricated success")
				}
			} else if mode == "translation-error" {
				if !apTranslationOutcome(translationEvents, transportError, apTranslationEncodeError) {
					t.Fatalf("unexpected translation outcome: state=%s status=%s error=%T %v", last.State, apJSON(last), transportError, transportError)
				}
				t.Logf("translation HTTP outcome: eof=%v error=%T", transportError == io.EOF, transportError)
			} else {
				want := "cancelled"
				if mode == "builder-error" {
					want = "infrastructure_error"
				}
				apFailure(t, last, want)
			}
			apAssertCalls(t, r)
			if mode == "server-cancel" || mode == "translation-error" {
				if r.cancels.Load() != 1 {
					t.Fatal("Cancel not idempotent")
				}
			}
		})
	}
}

func TestAlignmentProtocolDrainOracle(t *testing.T) {
	for _, consume := range []bool{false, true} {
		t.Run(fmt.Sprintf("consume-tail=%v", consume), func(t *testing.T) {
			ctx := apContext(t)
			result := &adaptor.Result{Text: "partial"}
			r := &apRunner{id: "drain-oracle", events: []adaptor.Event{
				apMeta(adaptor.TextDelta{Text: "tail"}, "drain-oracle", 1),
				apFinish("drain-oracle", "provider", "cancelled", true, 2),
			}, result: result, err: context.Canceled}
			stream := r.Stream(ctx, "oracle").(*apStream)
			select {
			case <-stream.done:
			case <-ctx.Done():
				t.Fatal("producer did not complete")
			}
			if len(stream.ch) != 2 {
				t.Fatal("fixture did not retain its buffered tail")
			}
			if consume {
				for range stream.Events() {
				}
			}
			got, err := stream.Result()
			if got != result || err != context.Canceled {
				t.Fatal("drain audit changed original Result/error")
			}
			select {
			case <-r.resultBarrier():
			default:
				t.Fatal("Result audit completion barrier missing")
			}
			wantEarly := int32(1)
			if consume {
				wantEarly = 0
			}
			if r.earlyResult.Load() != wantEarly || r.streamCalls.Load() != 1 || r.runCalls.Load() != 0 || r.resultCalls.Load() != 1 {
				t.Fatalf("buffered tail audit: early=%d want=%d", r.earlyResult.Load(), wantEarly)
			}
			for range stream.Events() {
			}
			if consume {
				apAssertCalls(t, r)
			}
		})
	}
}

const (
	apTranslationEncodeError  = "encode adapter stream status: payload_too_large"
	apTranslationInvalidError = "encode adapter stream status: invalid_payload"
)

// The real oversized ThreadKey cannot fit either the original event or its
// loss projection. JSON-RPC maps that encoder error to an A2A internal error.
// Recovery can observe the stored failed Task or race its persistence and
// surface that exact typed error; neither outcome may adopt the drained hint.
func apTranslationOutcome(observed []client.Event, transportError error, wantCause string) bool {
	if wantCause != apTranslationEncodeError && wantCause != apTranslationInvalidError {
		return false
	}
	var last client.TaskStatus
	var taskID string
	var liveTerminal bool
	for _, event := range observed {
		// Inspect every public projection, including every Part and the Raw
		// mirrors, so an earlier control cannot disappear behind a later status.
		raw, err := json.Marshal(event)
		if err != nil {
			return false
		}
		var value any
		if json.Unmarshal(raw, &value) != nil || apTranslationControl(value) {
			return false
		}
		if event.Task != nil {
			if event.Task.ID != "" && event.TaskID != "" && event.Task.ID != event.TaskID {
				return false
			}
			if event.Task.ID != "" {
				taskID = event.Task.ID
			}
			last = event.Task.Status
			if !apTranslationState(last.State) {
				return false
			}
			if event.RecoveredState && last.State == client.TaskStateFailed {
				liveTerminal = true
			}
		}
		if event.TaskID != "" {
			taskID = event.TaskID
		}
		if event.Status != nil {
			last = *event.Status
			if !apTranslationState(last.State) {
				return false
			}
			if last.State == client.TaskStateFailed {
				liveTerminal = true
			}
		}
	}
	if transportError == io.EOF {
		return last.State == client.TaskStateFailed && taskID != ""
	}
	// A full Task is historical unless explicitly recovered. Its failed state
	// cannot turn a later exact recovery error into an incompatible live outcome.
	if liveTerminal || (last.State != client.TaskStateUnspecified && !apTranslationState(last.State)) {
		return false
	}
	var recovery *client.StreamRecoveryError
	if !errors.As(transportError, &recovery) || recovery == nil || transportError != recovery || recovery.TaskID != taskID || recovery.Cause == nil {
		return false
	}
	var remote *a2aproto.Error
	if !errors.As(recovery.Cause, &remote) || remote == nil || recovery.Cause != remote || remote.Err != a2aproto.ErrInternalError || remote.Message != wantCause || len(remote.Details) != 0 {
		return false
	}
	// The pinned JSON-RPC transport may attach its standard ErrorInfo timestamp,
	// but cannot smuggle an adaptor failure code or limit through typed details.
	if len(remote.TypedDetails) > 1 {
		return false
	}
	for _, detail := range remote.TypedDetails {
		if detail == nil || detail.TypeURL != "type.googleapis.com/google.rpc.ErrorInfo" || len(detail.Value) != 3 || detail.Value["reason"] != "INTERNAL_ERROR" || detail.Value["domain"] != "a2a-protocol.org" {
			return false
		}
		metadata, ok := detail.Value["metadata"].(map[string]string)
		if !ok || len(metadata) != 1 {
			return false
		}
		if _, err := time.Parse(time.RFC3339, metadata["timestamp"]); err != nil {
			return false
		}
	}
	return true
}

func apTranslationState(state client.TaskState) bool {
	return state == client.TaskStateSubmitted || state == client.TaskStateWorking || state == client.TaskStateFailed
}

func apTranslationControl(value any) bool {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			switch key {
			case "agentadaptor.failure", "code", "limit", "limit_ms":
				return true
			}
			if apTranslationControl(child) {
				return true
			}
		}
	case []any:
		for _, child := range v {
			if apTranslationControl(child) {
				return true
			}
		}
	}
	return false
}

func TestAlignmentProtocolTranslationOutcomeOracle(t *testing.T) {
	const taskID = "translation-task"
	failed := client.TaskStatus{State: client.TaskStateFailed}
	working := client.TaskStatus{State: client.TaskStateWorking}
	cause := func() *a2aproto.Error {
		return &a2aproto.Error{Err: a2aproto.ErrInternalError, Message: apTranslationEncodeError}
	}
	recovery := func(err error) *client.StreamRecoveryError {
		return &client.StreamRecoveryError{TaskID: taskID, Cause: err}
	}
	typedCause := cause()
	typedCause.TypedDetails = []*errordetails.Typed{{TypeURL: "type.googleapis.com/google.rpc.ErrorInfo", Value: map[string]any{
		"reason": "INTERNAL_ERROR", "domain": "a2a-protocol.org", "metadata": map[string]string{"timestamp": "2026-09-08T00:00:00Z"},
	}}}
	wrongCode := cause()
	wrongCode.Err = a2aproto.ErrInvalidParams
	wrongMessage := cause()
	wrongMessage.Message = "encode adapter stream status: invalid_payload"
	extraMessage := cause()
	extraMessage.Message += ": unrelated failure"
	detailsControl := cause()
	detailsControl.Details = map[string]any{"code": "active_execution_timeout", "limit_ms": 777000}
	typedControl := cause()
	typedControl.TypedDetails = []*errordetails.Typed{{TypeURL: "type.googleapis.com/google.rpc.ErrorInfo", Value: map[string]any{
		"reason": "INTERNAL_ERROR", "domain": "a2a-protocol.org", "metadata": map[string]string{"timestamp": "2026-09-08T00:00:00Z", "limit_ms": "777000"},
	}}}
	statusControl := failed
	statusControl.Message = &client.Message{Parts: []client.Part{{Kind: client.PartText, Text: "failure", Metadata: map[string]any{"agentadaptor.failure": map[string]any{"code": "cancelled"}}}}}
	workingControl := working
	workingControl.Message = statusControl.Message
	var nilRemote *a2aproto.Error
	for _, tc := range []struct {
		name string
		last client.TaskStatus
		err  error
		id   string
		want bool
	}{
		{"failed-task-eof", failed, io.EOF, taskID, true},
		{"typed-recovery", working, recovery(cause()), taskID, true},
		{"typed-recovery-errorinfo", working, recovery(typedCause), taskID, true},
		{"empty-task-recovery", client.TaskStatus{}, &client.StreamRecoveryError{Cause: cause()}, "", true},
		{"wrapped-recovery", working, fmt.Errorf("read: %w", recovery(cause())), taskID, false},
		{"nil-error", failed, nil, taskID, false},
		{"bare-eof", working, io.EOF, taskID, false},
		{"no-event-eof", client.TaskStatus{}, io.EOF, "", false},
		{"io-timeout", working, os.ErrDeadlineExceeded, taskID, false},
		{"same-text-ordinary-error", working, errors.New(recovery(cause()).Error()), taskID, false},
		{"bare-protocol-error", working, cause(), taskID, false},
		{"same-text-ordinary-cause", working, recovery(errors.New(apTranslationEncodeError)), taskID, false},
		{"wrong-cause", working, recovery(wrongMessage), taskID, false},
		{"cause-message-suffix", working, recovery(extraMessage), taskID, false},
		{"wrong-protocol-code", working, recovery(wrongCode), taskID, false},
		{"nil-cause", working, recovery(nil), taskID, false},
		{"typed-nil-cause", working, recovery(nilRemote), taskID, false},
		{"wrong-task-id", working, recovery(cause()), "another-task", false},
		{"no-observed-task-id", failed, io.EOF, "", false},
		{"failed-task-with-recovery", failed, recovery(cause()), taskID, true},
		{"status-control", statusControl, io.EOF, taskID, false},
		{"recovery-status-control", workingControl, recovery(cause()), taskID, false},
		{"cause-control", working, recovery(detailsControl), taskID, false},
		{"typed-cause-control", working, recovery(typedControl), taskID, false},
		{"deadline", working, context.DeadlineExceeded, taskID, false},
		{"cancelled", working, context.Canceled, taskID, false},
		{"recovery-deadline-cause", working, recovery(context.DeadlineExceeded), taskID, false},
		{"joined-deadline", working, errors.Join(recovery(cause()), context.DeadlineExceeded), taskID, false},
		{"joined-eof-deadline", failed, errors.Join(io.EOF, context.DeadlineExceeded), taskID, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var observed []client.Event
			if tc.last.State != client.TaskStateUnspecified || tc.id != "" {
				observed = []client.Event{{Task: &client.Task{ID: tc.id, Status: tc.last}}}
			}
			if got := apTranslationOutcome(observed, tc.err, apTranslationEncodeError); got != tc.want {
				t.Fatalf("accepted=%v want=%v state=%s error=%T %v", got, tc.want, tc.last.State, tc.err, tc.err)
			}
		})
	}
	for _, state := range []client.TaskState{client.TaskStateCompleted, client.TaskStateCanceled, client.TaskStateInputRequired, client.TaskStateRejected, client.TaskStateUnspecified, "invented"} {
		t.Run("other-state-"+string(state), func(t *testing.T) {
			last := client.TaskStatus{State: state}
			observed := []client.Event{{Task: &client.Task{ID: taskID, Status: last}}}
			if apTranslationOutcome(observed, io.EOF, apTranslationEncodeError) || apTranslationOutcome(observed, recovery(cause()), apTranslationEncodeError) {
				t.Fatal("other terminal/unknown state accepted as the expected translation failure")
			}
		})
	}
	for _, placement := range []string{"task-metadata", "task-raw", "second-status-part", "status-message-metadata", "status-raw", "earlier-status"} {
		t.Run("observed-control-"+placement, func(t *testing.T) {
			control := map[string]any{"agentadaptor.failure": map[string]any{"code": "active_execution_timeout", "limit_ms": 777000}}
			safe := client.Part{Kind: client.PartText, Text: "safe"}
			bad := client.Part{Kind: client.PartText, Text: "failure", Metadata: control}
			event := client.Event{Task: &client.Task{ID: taskID, Status: working}}
			switch placement {
			case "task-metadata":
				event.Task.Metadata = control
			case "task-raw":
				event.Task.Raw = map[string]any{"metadata": control}
			case "second-status-part", "earlier-status":
				event.Task.Status.Message = &client.Message{Parts: []client.Part{safe, bad}}
			case "status-message-metadata":
				event.Status = &client.TaskStatus{State: client.TaskStateWorking, Message: &client.Message{Metadata: control}}
			case "status-raw":
				event.Status = &working
				event.Raw = map[string]any{"status": map[string]any{"message": map[string]any{"parts": []any{map[string]any{"text": "safe"}, map[string]any{"metadata": control}}}}}
			}
			observed := []client.Event{event}
			if apTranslationOutcome(observed, recovery(cause()), apTranslationEncodeError) {
				t.Fatal("error branch accepted an observed control")
			}
			observed = append(observed, client.Event{TaskID: taskID, Status: &failed})
			if apTranslationOutcome(observed, io.EOF, apTranslationEncodeError) {
				t.Fatal("later failed status hid an earlier control")
			}
		})
	}
	t.Run("last-observed-task-id", func(t *testing.T) {
		observed := []client.Event{{Task: &client.Task{ID: "earlier", Status: working}}, {TaskID: taskID, Status: &working}}
		if !apTranslationOutcome(observed, recovery(cause()), apTranslationEncodeError) || apTranslationOutcome(observed, &client.StreamRecoveryError{TaskID: "earlier", Cause: cause()}, apTranslationEncodeError) {
			t.Fatal("recovery error was not matched to the last observed task ID")
		}
	})
	t.Run("earlier-other-terminal", func(t *testing.T) {
		for _, state := range []client.TaskState{client.TaskStateCompleted, client.TaskStateCanceled, client.TaskStateInputRequired} {
			observed := []client.Event{{Task: &client.Task{ID: taskID, Status: client.TaskStatus{State: state}}}, {TaskID: taskID, Status: &failed}}
			if apTranslationOutcome(observed, io.EOF, apTranslationEncodeError) || apTranslationOutcome(observed[:1], recovery(cause()), apTranslationEncodeError) {
				t.Fatal("expected failure hid an earlier replacement terminal")
			}
		}
	})

	t.Run("fixture-specific-cause", func(t *testing.T) {
		observed := []client.Event{{Task: &client.Task{ID: taskID, Status: working}}}
		invalid := &a2aproto.Error{Err: a2aproto.ErrInternalError, Message: apTranslationInvalidError}
		if !apTranslationOutcome(observed, recovery(invalid), apTranslationInvalidError) || apTranslationOutcome(observed, recovery(cause()), apTranslationInvalidError) || apTranslationOutcome(observed, recovery(invalid), apTranslationEncodeError) {
			t.Fatal("oversized ThreadKey and unsafe Sequence causes became interchangeable")
		}
	})
	t.Run("live-failed-is-terminal", func(t *testing.T) {
		for _, event := range []client.Event{
			{TaskID: taskID, Status: &failed},
			{Task: &client.Task{ID: taskID, Status: failed}, RecoveredState: true},
		} {
			if !apTranslationOutcome([]client.Event{event}, io.EOF, apTranslationEncodeError) || apTranslationOutcome([]client.Event{event}, recovery(cause()), apTranslationEncodeError) {
				t.Fatal("live failed Status/recovered Task was treated as a historical snapshot")
			}
		}
	})

}

func TestAlignmentProtocolCoreTerminal(t *testing.T) {
	for _, sources := range []bool{false, true} {
		for _, cleanupFail := range []bool{false, true} {
			t.Run(fmt.Sprintf("sources=%v/cleanup=%v", sources, cleanupFail), func(t *testing.T) {
				ctx := apContext(t)
				cleanupErr := errors.New("T21_CLEANUP_PRIVATE")
				release := make(chan struct{})
				inCleanup := make(chan struct{})
				provider := apAttachment{attach: func(context.Context, string) (adaptor.RunAttachment, error) {
					a := adaptor.RunAttachment{}
					if sources {
						a.Events = func(c context.Context, _ string) <-chan adaptor.Event {
							ch := make(chan adaptor.Event)
							go func() { <-c.Done(); close(ch) }()
							return ch
						}
					}
					return a, nil
				}, detach: func(context.Context, string) error {
					close(inCleanup)
					<-release
					if cleanupFail {
						return cleanupErr
					}
					return nil
				}}
				a := apProvider(t, "claude", apClaudeFrames(), adaptor.WithRunServices(provider))
				s := a.Stream(ctx, "cleanup")
				var before []adaptor.Event
			waiting:
				for {
					select {
					case <-inCleanup:
						break waiting
					case e, ok := <-s.Events():
						if !ok {
							t.Fatal("closed before cleanup")
						}
						before = append(before, e)
						if _, ok := e.(adaptor.RunFinished); ok {
							t.Fatal("provider success escaped before cleanup")
						}
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
				}
				close(release)
				tail, r, err := apDrain(s)
				events := append(before, tail...)
				apLifecycle(t, events, err)
				if cleanupFail {
					var re *adaptor.RunError
					if r != nil || !errors.As(err, &re) || re.Result == nil || re.Reason != adaptor.ReasonInfrastructure || !errors.Is(err, cleanupErr) {
						t.Fatalf("cleanup carrier %v", err)
					}
					r = re.Result
					if events[len(events)-1].(adaptor.RunFinished).Reason != adaptor.ReasonInfrastructure {
						t.Fatal("terminal wrong cleanup reason")
					}
				} else if err != nil {
					t.Fatal(err)
				}
				if r.Raw().Terminal == nil || r.Raw().Stdout != apClaudeFrames() || len(r.Transcript()) == 0 {
					t.Fatal("provider audit clipped")
				}
			})
		}
	}
	t.Run("static-rejection", func(t *testing.T) {
		d := &apDriver{run: func(context.Context, driver.Request, driver.EventSink) (driver.Response, error) {
			return driver.Response{}, nil
		}}
		var attached atomic.Int32
		a := adaptor.New(d, adaptor.WithRunServices(apAttachment{attach: func(context.Context, string) (adaptor.RunAttachment, error) {
			attached.Add(1)
			return adaptor.RunAttachment{}, nil
		}}))
		defer a.Close(context.Background())
		events, r, err := apDrain(a.Stream(apContext(t), "invalid", adaptor.WithPolicy(adaptor.Policy{ActiveExecutionTimeout: -1})))
		if len(events) != 0 || r != nil || !errors.Is(err, adaptor.ErrInvalidPolicy) || attached.Load() != 0 || d.calls.Load() != 0 {
			t.Fatal("static reject fabricated lifecycle/resources")
		}
	})
	t.Run("admitted-pre-driver-failure", func(t *testing.T) {
		fault := errors.New("prepare-failure")
		d := &apDriver{run: func(context.Context, driver.Request, driver.EventSink) (driver.Response, error) {
			return driver.Response{}, nil
		}}
		a := adaptor.New(d, adaptor.WithRunServices(apAttachment{attach: func(context.Context, string) (adaptor.RunAttachment, error) { return adaptor.RunAttachment{}, fault }}))
		defer a.Close(context.Background())
		events, r, err := apDrain(a.Stream(apContext(t), "prepare"))
		apLifecycle(t, events, err)
		var re *adaptor.RunError
		if r != nil || !errors.Is(err, fault) || errors.As(err, &re) || d.calls.Load() != 0 {
			t.Fatal("pre-driver invented Result")
		}
	})
}

func TestAlignmentProtocolOtherProviders(t *testing.T) {
	cb := `{"type":"system","subtype":"init","session_id":"t21-session"}` + "\n" + apTool("same", "mcp__知识_库__search", "", map[string]any{"secret": "PRIVATE"}) + apTool("same", "mcp__知识_库__search", "", map[string]any{"secret": "PRIVATE"}) + apToolResult("same", "", false, nil) + apToolResult("same", "", false, nil) + apTool("unknown", "mcp__absent__search", "", map[string]any{}) + apTool("clear", "TodoWrite", "", map[string]any{"newTodos": []any{}}) + strings.Replace(apToolResult("clear", "", false, nil), "tool reply", "Todo list updated successfully", 1) + apTerminal
	cursorFrames := `{"type":"system","subtype":"init","session_id":"t21-session"}` + "\n"
	for _, sub := range []string{"started", "started", "completed", "completed"} {
		result := ""
		if sub == "completed" {
			result = `,"result":{"success":{"content":"PRIVATE"}}`
		}
		cursorFrames += `{"type":"tool_call","subtype":"` + sub + `","session_id":"t21-session","call_id":"same","tool_call":{"mcpToolCall":{"args":{"serverIdentifier":"知识_库","toolName":"search","arguments":{"secret":"PRIVATE"}}` + result + `}}}` + "\n"
	}
	cursorFrames += `{"type":"tool_call","subtype":"started","session_id":"t21-session","call_id":"unknown","tool_call":{"mcpToolCall":{"args":{"serverIdentifier":"missing","toolName":"search"}}}}` + "\n" + apTerminal
	rpc := func(method string, p any) string { return apJSON(map[string]any{"method": method, "params": p}) + "\n" }
	scoped := func(thread, turn string, k string, v any) map[string]any {
		return map[string]any{"threadId": thread, "turnId": turn, k: v}
	}
	badCodexFrames := rpc("turn/plan/updated", scoped("foreign-thread", "t21-turn", "plan", []any{map[string]any{"step": "WRONG_THREAD", "status": "pending"}})) + rpc("turn/plan/updated", scoped("t21-thread", "old-turn", "plan", []any{map[string]any{"step": "WRONG_TURN", "status": "pending"}}))
	item := map[string]any{"type": "mcpToolCall", "id": "same", "server": "知识_库", "tool": "search", "arguments": map[string]any{"secret": "PRIVATE"}, "status": "inProgress"}
	badCodexFrames += rpc("item/started", scoped("foreign-thread", "t21-turn", "item", item)) + rpc("item/started", scoped("t21-thread", "old-turn", "item", item))
	codexFrames := ""
	for i := 0; i < 2; i++ {
		codexFrames += rpc("item/started", scoped("t21-thread", "t21-turn", "item", item))
	}
	item["status"] = "completed"
	item["durationMs"] = 0
	item["result"] = map[string]any{"content": []any{}}
	for i := 0; i < 2; i++ {
		codexFrames += rpc("item/completed", scoped("t21-thread", "t21-turn", "item", item))
	}
	codexFrames += rpc("turn/plan/updated", scoped("t21-thread", "t21-turn", "plan", []any{map[string]any{"step": "checked", "status": "pending"}})) + rpc("turn/plan/updated", scoped("t21-thread", "t21-turn", "plan", []any{})) + rpc("turn/plan/updated", scoped("t21-thread", "t21-turn", "plan", []any{})) + rpc("item/completed", scoped("t21-thread", "t21-turn", "item", map[string]any{"type": "agentMessage", "id": "answer", "text": "final answer"})) + rpc("turn/completed", map[string]any{"threadId": "t21-thread", "turn": map[string]any{"id": "t21-turn", "status": "completed"}})
	t.Run("codex-foreign-fence", func(t *testing.T) {
		a := apProvider(t, "codex", badCodexFrames)
		events, r, err := apDrain(a.Stream(apContext(t), "foreign"))
		var re *adaptor.RunError
		if r != nil || !errors.As(err, &re) || re.Result == nil {
			t.Fatal("foreign scoped protocol not rejected")
		}
		caps, todos := apFacts(events)
		if len(caps) != 0 || len(todos) != 0 || !strings.Contains(re.Result.Raw().Stdout, "WRONG_THREAD") {
			t.Fatal("foreign notification became a fact or raw lost")
		}
	})
	for _, tc := range []struct {
		kind, frames string
		todos        int
	}{{"codebuddy", cb, 1}, {"cursor", cursorFrames, 0}, {"codex", codexFrames, 2}} {
		t.Run(tc.kind, func(t *testing.T) {
			ctx := apContext(t)
			a := apProvider(t, tc.kind, tc.frames)
			events, r, err := apDrain(a.Stream(ctx, "formal"))
			if err != nil {
				var re *adaptor.RunError
				if errors.As(err, &re) {
					t.Fatalf("%v cause=%+v raw=%+v", err, re.Cause, re.Result.Raw())
				}
				t.Fatal(err)
			}
			apLifecycle(t, events, nil)
			facts, todos := apFacts(events)
			if len(facts) != 2 || len(todos) != tc.todos {
				t.Fatalf("%s caps/todos=%d/%d", tc.kind, len(facts), len(todos))
			}
			for i, v := range facts {
				if v.Invocation.Ref != (capability.Ref{Kind: capability.MCP, Key: "知识_库", Operation: "search"}) || v.Invocation.Source != capability.Provider || v.Invocation.Evidence != capability.ProviderProtocol {
					t.Fatalf("fact=%+v", v)
				}
				want := capability.Started
				if i == 1 {
					want = capability.Completed
				}
				if v.Invocation.Phase != want {
					t.Fatal("duplicate/wrong phase")
				}
			}
			if tc.kind == "codex" && (facts[1].Invocation.Duration == nil || *facts[1].Invocation.Duration != 0) {
				t.Fatal("observed zero duration lost")
			}
			for _, v := range todos {
				if strings.Contains(apJSON(v), "WRONG_") {
					t.Fatal("Codex scope fence failed")
				}
			}
			if tc.todos > 0 && (todos[len(todos)-1].Snapshot.Items == nil || len(todos[len(todos)-1].Snapshot.Items) != 0) {
				t.Fatal("empty clear lost")
			}
			if r.Text != "final answer" || r.Raw().Terminal == nil || !strings.Contains(r.Raw().Stdout, "PRIVATE") {
				t.Fatal("formal output lost")
			}
			c, _ := apServer(t, a, bridge.ExposurePolicy{IncludeCapabilityInvocations: true, IncludeTodos: true}, nil)
			task, wire := apClientDrain(t, c, ctx)
			if task.Status.State != client.TaskStateCompleted {
				t.Fatal(task.Status)
			}
			decoded := apDecodeEvents(t, wire)
			wc, wt := apFacts(decoded)
			if len(wc) != 2 || len(wt) != tc.todos {
				t.Fatal("wire facts lost")
			}
			if strings.Contains(apJSON(wc), "PRIVATE") {
				t.Fatal("private args relayed")
			}
			if tc.kind == "cursor" {
				notices := 0
				for _, e := range events {
					if n, ok := e.(adaptor.Notice); ok && n.Data["code"] == "observation_unavailable" {
						notices++
					}
				}
				if notices != 1 {
					t.Fatal("Cursor unsupported todo not explicit")
				}
			}
		})
	}
}

// Hand-written closed wire payload: no production encoder supplies expected
// values. Raw-byte invalid cases are tested at the public decoder boundary;
// representable structural negatives also cross real HTTP and delegation below.
const apCapabilityWire = `{"schema":"adapter.stream.v1","event":{"kind":"capability.invocation","meta":{"run_id":"wire-run","sequence":1,"time":"2026-09-07T00:00:00Z"},"capability":{"invocation_id":"i","kind":"mcp","key":"safe","operation":"read","phase":"started","evidence":"provider_protocol","source":"provider","occurred_at":"2026-09-07T00:00:00Z"}}}`
const apTodoWire = `{"schema":"adapter.stream.v1","event":{"kind":"todo.updated","meta":{"run_id":"wire-run","sequence":2,"time":"2026-09-07T00:00:00Z"},"todo":{"items":[],"source":"plan_update","revision":1,"occurred_at":"2026-09-07T00:00:00Z"}}}`

func TestAlignmentProtocolWireValidation(t *testing.T) {
	cases := []struct {
		name, wire string
		valid      bool
	}{
		{"capability", apCapabilityWire, true}, {"empty-todo", apTodoWire, true},
		{"unknown-kind", strings.Replace(apCapabilityWire, "capability.invocation", "new.secret.kind", 1), false},
		{"unknown-field", strings.Replace(apCapabilityWire, `"key":"safe"`, `"key":"safe","token":"PRIVATE"`, 1), false},
		{"duplicate-key", strings.Replace(apCapabilityWire, `"key":"safe"`, `"key":"safe","key":"PRIVATE"`, 1), false},
		{"invalid-utf8", strings.Replace(apCapabilityWire, "safe", string([]byte{0xff}), 1), false},
		{"surrogate", strings.Replace(apCapabilityWire, "safe", `\ud800`, 1), false},
		{"bad-phase", strings.Replace(apCapabilityWire, "started", "future", 1), false},
		{"source-evidence", strings.Replace(apCapabilityWire, `"source":"provider"`, `"source":"host"`, 1), false},
		{"unsafe-sequence", strings.Replace(apCapabilityWire, `"sequence":1`, `"sequence":9007199254740992`, 1), false},
		{"null-items", strings.Replace(apTodoWire, `"items":[]`, `"items":null`, 1), false},
		{"no-items", strings.Replace(apTodoWire, `"items":[],`, "", 1), false},
		{"bad-status", strings.Replace(apTodoWire, `"items":[]`, `"items":[{"id":"x","content":"a","status":"unknown","synthetic_id":false}]`, 1), false},
		{"duplicate-id", strings.Replace(apTodoWire, `"items":[]`, `"items":[{"id":"x","content":"a","status":"pending","synthetic_id":false},{"id":"x","content":"b","status":"pending","synthetic_id":false}]`, 1), false},
		{"oversize-key", strings.Replace(apCapabilityWire, "safe", strings.Repeat("界", 171), 1), false},
		{"extra-value", apCapabilityWire + "{}", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, matched, err := bridge.DecodeAdapterEventV1(json.RawMessage(tc.wire))
			if !matched || (err == nil) != tc.valid {
				t.Fatalf("matched=%v err=%v valid=%v", matched, err, tc.valid)
			}
			if !tc.valid && e != nil {
				t.Fatal("invalid partial event returned")
			}
			if tc.valid {
				if e.Meta().RunID != "wire-run" {
					t.Fatal("meta lost")
				}
				if todo, ok := e.(adaptor.TodoUpdated); ok && todo.Snapshot.Items == nil {
					t.Fatal("clear nil")
				}
			}
		})
	}
	for _, size := range []int{65536, 65537} {
		t.Run(fmt.Sprintf("envelope-%d", size), func(t *testing.T) {
			base := strings.Replace(apCapabilityWire, `"sequence":1`, `"sequence":1,"thread_key":""`, 1)
			wire := strings.Replace(base, `"thread_key":""`, `"thread_key":"`+strings.Repeat("x", size-len(base))+`"`, 1)
			if len(wire) != size {
				t.Fatal("oracle length")
			}
			_, matched, err := bridge.DecodeAdapterEventV1(json.RawMessage(wire))
			if !matched || (err == nil) != (size == 65536) {
				t.Fatalf("boundary len=%d matched=%v err=%v", size, matched, err)
			}
		})
	}
}

// This standards-shaped HTTP fixture tests third-party A2A continuation and
// recovery independently of our server implementation. The real bridge server
// is exercised separately above; this fixture cannot invent core events.
type apWirePeer struct {
	url                                           string
	streamCalls, getCalls, cancelCalls, sendCalls atomic.Int32
	mu                                            sync.Mutex
	requests                                      []map[string]any
	frames                                        [][]any
	task                                          map[string]any
	syncTask                                      map[string]any
	broken                                        bool
	streaming                                     bool
	cancelErr                                     bool
	methodHook                                    func(context.Context, string)
	streamEnd                                     func(context.Context)
}

func apWireMessage(id, text string) map[string]any {
	return map[string]any{"messageId": id, "role": "ROLE_AGENT", "taskId": "task-21", "contextId": "context-21", "parts": []any{map[string]any{"text": text}}}
}
func apWireTask(state, id, text string, parts []any) map[string]any {
	return map[string]any{"id": "task-21", "contextId": "context-21", "status": map[string]any{"state": state, "message": apWireMessage(id, text)}, "artifacts": []any{map[string]any{"artifactId": "artifact-21", "name": "report", "parts": parts}}}
}
func apWireStatus(state, id, text string) any {
	return map[string]any{"statusUpdate": map[string]any{"taskId": "task-21", "contextId": "context-21", "status": map[string]any{"state": state, "message": apWireMessage(id, text)}}}
}
func apArtifact(parts []any, appendValue, last bool) any {
	return map[string]any{"artifactUpdate": map[string]any{"taskId": "task-21", "contextId": "context-21", "artifact": map[string]any{"artifactId": "artifact-21", "name": "report", "parts": parts}, "append": appendValue, "lastChunk": last}}
}
func apPeer(t *testing.T, p *apWirePeer) string {
	t.Helper()
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "agent-card") {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, apJSON(map[string]any{"name": "T21 peer", "description": "independent canonical fixture", "version": "1", "supportedInterfaces": []any{map[string]any{"url": p.url + "/rpc", "protocolBinding": "JSONRPC", "protocolVersion": "1.0"}}, "capabilities": map[string]any{"streaming": p.streaming}, "defaultInputModes": []string{"text/plain"}, "defaultOutputModes": []string{"text/plain"}, "skills": []any{map[string]any{"id": "t21", "name": "fixture", "description": "fixture", "tags": []string{"test"}}}}))
			return
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		p.mu.Lock()
		p.requests = append(p.requests, req)
		p.mu.Unlock()
		send := func(result any) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, apJSON(map[string]any{"jsonrpc": "2.0", "id": req["id"], "result": result}))
		}
		if p.methodHook != nil {
			p.methodHook(r.Context(), req["method"].(string))
		}
		switch req["method"] {
		case "SendStreamingMessage":
			n := int(p.streamCalls.Add(1)) - 1
			if n >= len(p.frames) {
				n = len(p.frames) - 1
			}
			w.Header().Set("Content-Type", "text/event-stream")
			for _, f := range p.frames[n] {
				fmt.Fprintf(w, "data: %s\n\n", apJSON(map[string]any{"jsonrpc": "2.0", "id": req["id"], "result": f}))
				w.(http.Flusher).Flush()
			}
			if p.streamEnd != nil {
				p.streamEnd(r.Context())
			}
			if p.broken {
				fmt.Fprint(w, "data: {broken\n\n")
				w.(http.Flusher).Flush()
			}
		case "GetTask":
			p.getCalls.Add(1)
			send(p.task)
		case "SendMessage":
			p.sendCalls.Add(1)
			task := p.syncTask
			if task == nil {
				task = p.task
			}
			send(map[string]any{"task": task})
		case "CancelTask":
			p.cancelCalls.Add(1)
			if p.cancelErr {
				w.WriteHeader(503)
				fmt.Fprint(w, "fixture cancellation unavailable")
			} else {
				send(apWireTask("TASK_STATE_CANCELED", "cancel", "cancel", nil))
			}
		default:
			t.Errorf("unexpected method %v", req["method"])
			w.WriteHeader(400)
		}
	}))
	p.url = h.URL
	t.Cleanup(h.Close)
	return h.URL + "/.well-known/agent-card.json"
}
func apPeerDelegate(t *testing.T, p *apWirePeer, req delegation.DelegationRequest, policy delegation.Policy) (delegation.DelegationResult, error, []delegation.Event) {
	t.Helper()
	url := apPeer(t, p)
	var events []delegation.Event
	svc, err := delegation.NewService(delegation.Config{Agents: []delegation.AgentRef{delegation.Remote("peer", url, policy)}, Observe: func(e delegation.Event) { events = append(events, e) }})
	if err != nil {
		t.Fatal(err)
	}
	req.RunID = "fixture-parent"
	req.Agent = "peer"
	req.Objective = "answer"
	if req.Message == nil {
		req.Message = &client.Message{ID: "answer-21", Role: "user", TaskID: "task-21", ContextID: "context-21", Parts: []client.Part{{Kind: client.PartText, Text: "answer to previous question"}}}
	}
	result, err := svc.Delegate(apContext(t), req)
	if e := svc.Close(); e != nil {
		t.Fatal(e)
	}
	return result, err, events
}
func TestAlignmentProtocolContinuationArtifacts(t *testing.T) {
	oldParts := []any{map[string]any{"text": "old"}}
	liveParts := []any{map[string]any{"text": "alpha"}, map[string]any{"data": map[string]any{"n": 7}}, map[string]any{"raw": "AQID", "filename": "piece.bin", "mediaType": "application/octet-stream"}, map[string]any{"url": "https://fixture.invalid/file", "filename": "remote.bin"}}
	tail := []any{map[string]any{"text": "omega"}}
	for _, old := range []string{"TASK_STATE_INPUT_REQUIRED", "TASK_STATE_COMPLETED", "TASK_STATE_FAILED"} {
		for _, end := range []string{"complete", "question", "EOF", "recover", "stale-recover"} {
			t.Run(old+"/"+end, func(t *testing.T) {
				snapshot := apWireTask(old, "already-answered", "OLD QUESTION", oldParts)
				finalState := "TASK_STATE_COMPLETED"
				finalID, finalText := "new-answer", "finished"
				if end == "question" {
					finalState = "TASK_STATE_INPUT_REQUIRED"
					finalID = "new-question"
					finalText = "NEW QUESTION"
				}
				frames := []any{map[string]any{"task": snapshot}, apWireStatus("TASK_STATE_WORKING", "working", "working"), apArtifact(liveParts, false, false), apArtifact(tail, true, true)}
				if end == "complete" || end == "question" {
					frames = append(frames, apWireStatus(finalState, finalID, finalText))
				}
				recovered := apWireTask(finalState, finalID, finalText, append(append([]any{}, liveParts...), tail...))
				if end == "stale-recover" {
					recovered = snapshot
				}
				p := &apWirePeer{frames: [][]any{frames}, task: recovered, streaming: true, broken: end == "recover" || end == "stale-recover"}
				result, err, events := apPeerDelegate(t, p, delegation.DelegationRequest{IncludeRemoteArtifacts: true}, delegation.Policy{AllowInputRequired: true})
				interrupted := end == "EOF" || end == "stale-recover"
				if interrupted {
					var de *delegation.DelegationError
					if !errors.As(err, &de) || de.Code != "stream_interrupted" {
						t.Fatalf("interruption=%+v %v", result, err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
				questions := 0
				updates := []delegation.Event{}
				for _, e := range events {
					if e.Kind == delegation.DelegationInputRequired {
						questions++
						if strings.Contains(apJSON(e), "OLD QUESTION") {
							t.Fatal("old question resurrected")
						}
					}
					if e.Kind == delegation.DelegationArtifactCreated {
						updates = append(updates, e)
					}
				}
				wantQ := 0
				if end == "question" {
					wantQ = 1
				}
				if questions != wantQ {
					t.Fatalf("questions=%d want=%d", questions, wantQ)
				}
				if len(result.RemoteArtifacts) != 1 {
					t.Fatalf("final artifact count=%d", len(result.RemoteArtifacts))
				}
				parts := result.RemoteArtifacts[0].Parts
				if len(parts) != 5 || parts[0].Text != "alpha" || parts[1].Data.(map[string]any)["n"] != float64(7) || !bytes.Equal(parts[2].Raw, []byte{1, 2, 3}) || parts[3].URL != "https://fixture.invalid/file" || parts[4].Text != "omega" {
					t.Fatalf("ordered parts=%+v", parts)
				}
				foundLive, foundTail := false, false
				for _, e := range updates {
					if e.Artifact == nil {
						continue
					}
					if len(e.Artifact.Parts) == 4 {
						foundLive = true
						if e.Append || e.LastChunk {
							t.Fatal("first update flags")
						}
					}
					if len(e.Artifact.Parts) == 1 && e.Artifact.Parts[0].Text == "omega" {
						foundTail = true
						if !e.Append || !e.LastChunk {
							t.Fatal("append/last flags")
						}
					}
				}
				if !foundLive || !foundTail {
					t.Fatal("per-update full Parts lost")
				}
				if !interrupted && p.cancelCalls.Load() != 0 {
					t.Fatal("successful continuation canceled")
				}
				if p.streamCalls.Load() != 1 {
					t.Fatal("continuation replayed execution")
				}
				if end == "recover" && p.getCalls.Load() == 0 {
					t.Fatal("no real GetTask recovery")
				}
			})
		}
	}
	t.Run("opt-out", func(t *testing.T) {
		p := &apWirePeer{streaming: true, frames: [][]any{{apArtifact(liveParts, false, true), apWireStatus("TASK_STATE_COMPLETED", "done", "done")}}, task: apWireTask("TASK_STATE_COMPLETED", "done", "done", liveParts)}
		r, err, events := apPeerDelegate(t, p, delegation.DelegationRequest{}, delegation.Policy{})
		if err != nil {
			t.Fatal(err)
		}
		if len(r.RemoteArtifacts) != 0 {
			t.Fatal("opt-out final Parts leaked")
		}
		for _, e := range events {
			if e.Artifact != nil && len(e.Artifact.Parts) != 0 {
				t.Fatal("opt-out live Parts leaked")
			}
		}
	})
	t.Run("limit", func(t *testing.T) {
		large := []any{map[string]any{"text": strings.Repeat("T21_PRIVATE", 200)}}
		p := &apWirePeer{streaming: true, frames: [][]any{{apArtifact(large, false, true), apWireStatus("TASK_STATE_COMPLETED", "done", "done")}}, task: apWireTask("TASK_STATE_COMPLETED", "done", "done", large)}
		_, err, events := apPeerDelegate(t, p, delegation.DelegationRequest{IncludeRemoteArtifacts: true}, delegation.Policy{MaxArtifactBytes: 100})
		if err == nil {
			t.Fatal("oversize artifact accepted")
		}
		dropped := false
		for _, e := range events {
			if e.Kind == delegation.DelegationStreamDropped {
				dropped = true
				if strings.Contains(apJSON(e), "T21_PRIVATE") {
					t.Fatal("drop leaked invalid payload")
				}
			}
		}
		if !dropped {
			t.Fatal("artifact limit silently lost")
		}
	})
	t.Run("sync-polling", func(t *testing.T) {
		p := &apWirePeer{streaming: false, syncTask: apWireTask("TASK_STATE_WORKING", "working", "working", oldParts), task: apWireTask("TASK_STATE_COMPLETED", "done", "done", liveParts)}
		r, err, _ := apPeerDelegate(t, p, delegation.DelegationRequest{IncludeRemoteArtifacts: true}, delegation.Policy{PollInterval: time.Millisecond, MaxPolls: 2})
		if err != nil || r.Status != "completed" || p.sendCalls.Load() != 1 || p.getCalls.Load() != 1 || p.streamCalls.Load() != 0 {
			t.Fatalf("polling %v %+v", err, r)
		}
	})
}

// A transparent public Runner decorator records the original Stream outcome.
// It never changes Events, Result, IDs, cancellation, or the Driver boundary.
type apCaptureRunner struct {
	agent  *adaptor.Agent
	parent func(context.Context) context.Context
	mu     sync.Mutex
	result *adaptor.Result
	err    error
	calls  atomic.Int32
}
type apCaptureStream struct {
	adaptor.Stream
	owner *apCaptureRunner
}

func (r *apCaptureRunner) Run(context.Context, string, ...adaptor.CallOption) (*adaptor.Result, error) {
	panic("T21 bridge must use Stream")
}
func (r *apCaptureRunner) Stream(c context.Context, p string, o ...adaptor.CallOption) adaptor.Stream {
	r.calls.Add(1)
	if r.parent != nil {
		c = r.parent(c)
	}
	return &apCaptureStream{Stream: r.agent.Stream(c, p, o...), owner: r}
}
func (s *apCaptureStream) Result() (*adaptor.Result, error) {
	r, e := s.Stream.Result()
	s.owner.mu.Lock()
	s.owner.result = r
	s.owner.err = e
	s.owner.mu.Unlock()
	return r, e
}

func TestAlignmentProtocolRealPreparationCauses(t *testing.T) {
	for _, scenario := range []string{"parent-no-budget", "parent-with-budget", "deadline-with-budget", "own-active-first"} {
		for _, path := range []string{"http-send", "http-stream", "local", "remote"} {
			t.Run(scenario+"/"+path, func(t *testing.T) {
				ctx := apContext(t)
				parentCause := &adaptor.ActiveExecutionTimeoutError{Limit: 777 * time.Second}
				var cancelParent context.CancelCauseFunc
				ownLimit := time.Second
				if scenario == "parent-no-budget" {
					ownLimit = 0
				}
				if scenario == "own-active-first" {
					ownLimit = 100 * time.Millisecond
				}
				d := &apDriver{}
				attachment := apAttachment{attach: func(c context.Context, _ string) (adaptor.RunAttachment, error) {
					if scenario == "parent-no-budget" || scenario == "parent-with-budget" {
						cancelParent(parentCause)
					}
					<-c.Done()
					if scenario == "own-active-first" {
						cancelParent(parentCause)
					}
					return adaptor.RunAttachment{}, context.Cause(c)
				}}
				a := adaptor.New(d, adaptor.WithRunServices(attachment), adaptor.WithPolicy(adaptor.Policy{ActiveExecutionTimeout: ownLimit}))
				defer a.Close(context.Background())
				capture := &apCaptureRunner{agent: a, parent: func(c context.Context) context.Context {
					if scenario == "deadline-with-budget" {
						dc, stop := context.WithDeadlineCause(c, time.Now().Add(50*time.Millisecond), parentCause)
						t.Cleanup(stop)
						return dc
					}
					cc, stop := context.WithCancelCause(c)
					cancelParent = stop
					t.Cleanup(func() { stop(context.Canceled) })
					return cc
				}}
				want := "cancelled"
				if scenario == "deadline-with-budget" {
					want = "deadline_exceeded"
				}
				if scenario == "own-active-first" {
					want = "active_execution_timeout"
				}
				if strings.HasPrefix(path, "http") {
					c, _ := apServer(t, capture, bridge.ExposurePolicy{}, nil)
					var task client.Task
					var err error
					if path == "http-send" {
						task, err = c.Send(ctx, apSendReq())
					} else {
						task, _ = apClientDrain(t, c, ctx)
					}
					if err != nil {
						t.Fatal(err)
					}
					apFailure(t, task.Status, want)
					wire := apJSON(task.Status)
					if want == "active_execution_timeout" {
						if !strings.Contains(wire, `"limit_ms":100`) {
							t.Fatal("own limit lost")
						}
					} else if strings.Contains(wire, "limit_ms") {
						t.Fatal("parent budget was promoted")
					}
				} else {
					var ref delegation.AgentRef
					if path == "local" {
						ref = delegation.Local("member", capture, delegation.Policy{})
					} else {
						_, url := apServer(t, capture, bridge.ExposurePolicy{}, nil)
						ref = delegation.Remote("member", url, delegation.Policy{})
					}
					svc, e := delegation.NewService(delegation.Config{Agents: []delegation.AgentRef{ref}})
					if e != nil {
						t.Fatal(e)
					}
					defer svc.Close()
					out, e := svc.Delegate(ctx, delegation.DelegationRequest{RunID: "caller", Agent: "member", Objective: "prepare", Stream: true})
					if e == nil || out.Error == nil || out.Error.Code != want {
						t.Fatalf("delegation=%+v err=%v", out.Error, e)
					}
					if want != "active_execution_timeout" && strings.Contains(apJSON(out.Error), "limit_ms") {
						t.Fatal("parent limit leaked")
					}
				}
				capture.mu.Lock()
				defer capture.mu.Unlock()
				var carrier *adaptor.RunError
				if capture.calls.Load() != 1 || d.calls.Load() != 0 || capture.result != nil || capture.err == nil || errors.As(capture.err, &carrier) {
					t.Fatalf("not real pre-Driver shape calls=%d driver=%d result=%v err=%T", capture.calls.Load(), d.calls.Load(), capture.result, capture.err)
				}
				var typed *adaptor.ActiveExecutionTimeoutError
				if !errors.As(capture.err, &typed) {
					t.Fatal("typed cause lost")
				}
				expected := parentCause.Limit
				if scenario == "own-active-first" {
					expected = 100 * time.Millisecond
				}
				if typed.Limit != expected {
					t.Fatal("typed cause replaced")
				}
			})
		}
	}
}

type apFailingStore struct {
	capabilityrecorder.Store
	mu           sync.Mutex
	attempts     map[string]int
	failIdentity string
	closes       atomic.Int32
}

func (s *apFailingStore) Append(c context.Context, r capabilityrecorder.Record) error {
	s.mu.Lock()
	s.attempts[r.Scope.RunID]++
	s.mu.Unlock()
	if r.Scope.IdentityID == s.failIdentity {
		return errors.New("T21_STORE_PRIVATE")
	}
	return s.Store.Append(c, r)
}
func (s *apFailingStore) Close() error { s.closes.Add(1); return nil }
func TestAlignmentProtocolRecorderOwnership(t *testing.T) {
	store := &apFailingStore{Store: capabilityrecorder.NewMemoryStore(), attempts: map[string]int{}, failIdentity: "fails"}
	rec, err := capabilityrecorder.New(capabilityrecorder.Config{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	for _, identity := range []string{"fails", "healthy", "fails"} {
		a := apProvider(t, "claude", apClaudeFrames(), rec.Option(), adaptor.WithIdentity(adaptor.Identity{ID: identity, Tenant: "tenant", Profile: "profile"}))
		stream := a.Stream(apContext(t), "store fault")
		events, result, e := apDrain(stream)
		if e != nil || result == nil || result.Text != "final answer" || result.Raw().Terminal == nil {
			t.Fatal("observer changed successful result")
		}
		disabled := 0
		for _, ev := range events {
			if n, ok := ev.(adaptor.Notice); ok && n.Data["code"] == "observation_disabled" {
				disabled++
				if strings.Contains(apJSON(n), "T21_STORE_PRIVATE") {
					t.Fatal("store secret in notice")
				}
			}
		}
		store.mu.Lock()
		attempts := store.attempts[stream.RunID()]
		store.mu.Unlock()
		want := 4
		if identity == "fails" {
			want = 1
		}
		if attempts != want || disabled != map[bool]int{true: 1, false: 0}[identity == "fails"] {
			t.Fatalf("attempts=%d disabled=%d", attempts, disabled)
		}
		page, e := rec.Query(context.Background(), capabilityrecorder.Query{Scope: capabilityrecorder.Scope{IdentityID: identity, Tenant: "tenant", Profile: "profile", RunID: stream.RunID()}})
		if e != nil {
			t.Fatal(e)
		}
		wantRecords := 4
		if identity == "fails" {
			wantRecords = 0
		}
		if len(page.Records) != wantRecords {
			t.Fatal("silent fallback or cross-run store loss")
		}
		if e := a.Close(context.Background()); e != nil {
			t.Fatal(e)
		}
		if store.closes.Load() != 0 {
			t.Fatal("Agent closed shared host Store")
		}
	}
	if err := store.Close(); err != nil || store.closes.Load() != 1 {
		t.Fatal("host ownership")
	}
}

func TestAlignmentProtocolLocalCancelDrain(t *testing.T) {
	for _, carrier := range []bool{false, true} {
		t.Run(fmt.Sprintf("carrier=%v", carrier), func(t *testing.T) {
			cause := errors.Join(context.Canceled, &adaptor.ActiveExecutionTimeoutError{Limit: 777 * time.Second})
			original := cause
			partial := &adaptor.Result{Text: "partial local", Summary: "safe partial"}
			want := "cancelled"
			if carrier {
				original = &adaptor.RunError{Reason: adaptor.ReasonApprovalDenied, Result: partial, Cause: cause}
				want = "approval_denied"
			}
			r := &apRunner{id: "local-cancel", waitCancel: true, cancelled: make(chan struct{}), events: []adaptor.Event{apFinish("local-cancel", "provider", "cancelled", true, 1)}, err: original, result: partial}
			svc, e := delegation.NewService(delegation.Config{Agents: []delegation.AgentRef{delegation.Local("member", r, delegation.Policy{})}})
			if e != nil {
				t.Fatal(e)
			}
			defer svc.Close()
			spec, _ := svc.Registry().Lookup("member")
			c := svc.Delegator().NewClient(spec)
			s, e := c.SendStream(apContext(t), apSendReq())
			if e != nil {
				t.Fatal(e)
			}
			if e = s.Close(); e != nil {
				t.Fatal(e)
			}
			_ = s.Close()
			var task client.Task
			for {
				ev, e := s.Recv()
				if errors.Is(e, io.EOF) {
					break
				}
				if e != nil {
					t.Fatal(e)
				}
				if ev.Task != nil {
					task = *ev.Task
				}
			}
			apFailure(t, task.Status, want)
			apAssertCalls(t, r)
			if r.err != original || r.result != partial || !errors.Is(r.err, context.Canceled) || r.cancels.Load() != 1 {
				t.Fatal("drain changed original outcome")
			}
			if len(task.Messages) != 1 || task.Messages[0].Parts[0].Text != "partial local" {
				t.Fatal("partial text lost")
			}
		})
	}
}

func apWireDataStatus(raw string) any {
	return map[string]any{"statusUpdate": map[string]any{"taskId": "task-21", "contextId": "context-21", "status": map[string]any{"state": "TASK_STATE_WORKING", "message": map[string]any{"messageId": "data", "role": "ROLE_AGENT", "parts": []any{map[string]any{"data": json.RawMessage(raw)}}}}}}
}
func TestAlignmentProtocolInboundNegativeRelay(t *testing.T) {
	for _, tc := range []struct{ name, wire string }{
		{"cap-unknown-field", strings.Replace(apCapabilityWire, `"key":"safe"`, `"key":"safe","secret":"T21_WIRE_PRIVATE"`, 1)},
		{"cap-unknown-kind", strings.Replace(apCapabilityWire, "capability.invocation", "future.kind", 1)},
		{"cap-invalid-phase", strings.Replace(apCapabilityWire, "started", "future_phase", 1)},
		{"todo-null", strings.Replace(apTodoWire, `"items":[]`, `"items":null`, 1)},
		{"todo-invalid-state", strings.Replace(apTodoWire, `"items":[]`, `"items":[{"id":"i","content":"T21_WIRE_PRIVATE","status":"invalid","synthetic_id":false}]`, 1)},
		{"todo-oversized-content", strings.Replace(apTodoWire, `"items":[]`, `"items":[{"id":"i","content":"`+strings.Repeat("界", 1366)+`","status":"pending","synthetic_id":false}]`, 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &apWirePeer{streaming: true, frames: [][]any{{apWireDataStatus(tc.wire), apWireStatus("TASK_STATE_COMPLETED", "done", "safe")}}}
			out, err, events := apPeerDelegate(t, p, delegation.DelegationRequest{}, delegation.Policy{})
			if err != nil || out.Error != nil {
				t.Fatalf("bad fact changed business outcome: %v", err)
			}
			drops, inner := 0, 0
			for _, ev := range events {
				if ev.Kind == delegation.DelegationStreamDropped {
					drops++
					if strings.Contains(apJSON(ev.Raw), "T21_WIRE_PRIVATE") {
						t.Fatal("invalid field leaked in drop")
					}
				}
				if ev.Capability != nil && ev.Capability.Evidence == capability.Relayed {
					inner++
				}
				if ev.Todo != nil {
					inner++
				}
			}
			if drops != 1 || inner != 0 {
				t.Fatalf("drops=%d accepted invalid facts=%d", drops, inner)
			}
		})
	}
}

func TestAlignmentProtocolContinuationRoundsAndClones(t *testing.T) {
	parts := []any{map[string]any{"text": "round"}, map[string]any{"data": map[string]any{"values": []any{map[string]any{"n": float64(7)}}}}, map[string]any{"raw": "AQID", "mediaType": "application/octet-stream"}}
	p := &apWirePeer{streaming: true, frames: [][]any{
		{apArtifact(parts, false, true), apWireStatus("TASK_STATE_INPUT_REQUIRED", "q1", "first question")},
		{map[string]any{"task": apWireTask("TASK_STATE_INPUT_REQUIRED", "q1", "first question", parts)}, apArtifact([]any{map[string]any{"text": "second"}}, false, true), apWireStatus("TASK_STATE_INPUT_REQUIRED", "q2", "second question")},
		{map[string]any{"task": apWireTask("TASK_STATE_INPUT_REQUIRED", "q2", "second question", nil)}, apArtifact(parts, false, true), apWireStatus("TASK_STATE_COMPLETED", "done", "complete")},
	}}
	url := apPeer(t, p)
	svc, e := delegation.NewService(delegation.Config{Agents: []delegation.AgentRef{delegation.Remote("member", url, delegation.Policy{AllowInputRequired: true})}})
	if e != nil {
		t.Fatal(e)
	}
	defer svc.Close()
	ctx := apContext(t)
	for round := 0; round < 3; round++ {
		runID := fmt.Sprintf("round-%d", round)
		bus := svc.Bus().SubscribeRun(ctx, runID)
		message := client.Message{ID: fmt.Sprintf("answer-%d", round), Role: "user", Parts: []client.Part{{Kind: client.PartText, Text: "answer"}}}
		if round > 0 {
			message.TaskID = "task-21"
			message.ContextID = "context-21"
		}
		out, err := svc.Delegate(ctx, delegation.DelegationRequest{RunID: runID, Agent: "member", Message: &message, Stream: true, IncludeRemoteArtifacts: true})
		if err != nil || out.RemoteTaskID != "task-21" {
			t.Fatalf("round %d: %v", round, err)
		}
		q := 0
		var artifactEvent delegation.Event
		for len(bus) > 0 {
			ev := <-bus
			if ev.Kind == delegation.DelegationInputRequired {
				q++
				expected := []string{"first question", "second question", ""}[round]
				if !strings.Contains(apJSON(ev), expected) {
					t.Fatal("replayed old questionnaire")
				}
			}
			if ev.Artifact != nil {
				artifactEvent = ev
			}
		}
		expectedQ := 1
		if round == 2 {
			expectedQ = 0
		}
		if q != expectedQ {
			t.Fatalf("round %d questions=%d", round, q)
		}
		if round != 1 {
			if len(out.RemoteArtifacts) != 1 || len(out.RemoteArtifacts[0].Parts) != 3 {
				t.Fatal("multipart lost")
			}
			mutate := func(a []delegation.RemotePart) {
				a[1].Data.(map[string]any)["values"].([]any)[0].(map[string]any)["n"] = float64(99)
				a[2].Raw[0] = 9
			}
			mutate(out.RemoteArtifacts[0].Parts)
			if artifactEvent.Artifact == nil || artifactEvent.Artifact.Parts[2].Raw[0] != 1 {
				t.Fatal("result/event alias")
			}
			mutate(artifactEvent.Artifact.Parts)
			stored, ok := svc.Result(runID, "member")
			if !ok || stored.RemoteArtifacts[0].Parts[2].Raw[0] != 1 || stored.RemoteArtifacts[0].Parts[1].Data.(map[string]any)["values"].([]any)[0].(map[string]any)["n"] != float64(7) {
				t.Fatal("Service retained aliases")
			}
			mutate(stored.RemoteArtifacts[0].Parts)
			all := svc.Results(runID)
			mutate(all["member"].RemoteArtifacts[0].Parts)
			history := svc.Delegations(runID)
			if len(history) != 1 || history[0].RemoteArtifacts[0].Parts[2].Raw[0] != 1 {
				t.Fatal("Service query clone lost")
			}
			replay := svc.Bus().SubscribeRun(ctx, runID)
			found := false
			for len(replay) > 0 {
				ev := <-replay
				if ev.Artifact != nil && len(ev.Artifact.Parts) == 3 {
					found = true
					if ev.Artifact.Parts[2].Raw[0] != 1 {
						t.Fatal("EventBus replay alias")
					}
				}
			}
			if !found {
				t.Fatal("artifact replay missing")
			}
		}
	}
	if p.streamCalls.Load() != 3 || p.cancelCalls.Load() != 0 {
		t.Fatalf("continuation stream=%d cancel=%d get=%d", p.streamCalls.Load(), p.cancelCalls.Load(), p.getCalls.Load())
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	requests := 0
	for _, req := range p.requests {
		if req["method"] != "SendStreamingMessage" {
			continue
		}
		if requests > 0 {
			params := req["params"].(map[string]any)
			message := params["message"].(map[string]any)
			if message["taskId"] != "task-21" || message["contextId"] != "context-21" {
				t.Fatal("continuation task identity lost")
			}
		}
		requests++
	}
}

// Only the public A2A interface is decorated to inspect cancellation context;
// all discovery, streaming, recovery and cancellation still cross real HTTP.
type apBudgetClient struct {
	client       *client.Client
	cancelChecks *atomic.Int32
	t            *testing.T
}

func (c *apBudgetClient) AgentCard(ctx context.Context) (client.AgentCard, error) {
	return c.client.AgentCard(ctx)
}
func (c *apBudgetClient) Send(ctx context.Context, r client.SendRequest) (client.Task, error) {
	return c.client.Send(ctx, r)
}
func (c *apBudgetClient) SendStream(ctx context.Context, r client.SendRequest) (delegation.A2AStream, error) {
	return c.client.SendStream(ctx, r)
}
func (c *apBudgetClient) GetTask(ctx context.Context, r client.GetTaskRequest) (client.Task, error) {
	return c.client.GetTask(ctx, r)
}
func (c *apBudgetClient) CancelTask(ctx context.Context, r client.CancelTaskRequest) (client.Task, error) {
	d, ok := ctx.Deadline()
	if ctx.Err() != nil || !ok || time.Until(d) <= 0 || time.Until(d) > 5*time.Second || r.TaskID != "task-21" {
		c.t.Error("known-task cancellation context is not detached and bounded")
	}
	c.cancelChecks.Add(1)
	return c.client.CancelTask(ctx, r)
}
func TestAlignmentProtocolDelegationBudgets(t *testing.T) {
	for _, mode := range []string{"active", "wall", "recovery", "new-rounds"} {
		t.Run(mode, func(t *testing.T) {
			p := &apWirePeer{streaming: true, cancelErr: true, frames: [][]any{{apArtifact([]any{map[string]any{"text": "partial before timeout"}}, false, true)}}}
			switch mode {
			case "active", "wall":
				p.streamEnd = func(ctx context.Context) { <-ctx.Done() }
			case "recovery":
				p.broken = true
				p.streamEnd = func(ctx context.Context) {
					select {
					case <-ctx.Done():
					case <-time.After(250 * time.Millisecond):
					}
				}
				p.methodHook = func(ctx context.Context, method string) {
					if method == "GetTask" {
						select {
						case <-ctx.Done():
						case <-time.After(350 * time.Millisecond):
						}
					}
				}
				p.task = apWireTask("TASK_STATE_COMPLETED", "done", "too late", nil)
			case "new-rounds":
				p.cancelErr = false
				p.frames = [][]any{{apWireStatus("TASK_STATE_COMPLETED", "done", "complete")}}
			}
			url := apPeer(t, p)
			var checks atomic.Int32
			var clients []*client.Client
			svc, err := delegation.NewService(delegation.Config{Agents: []delegation.AgentRef{delegation.Remote("member", url, delegation.Policy{})}, NewClient: func(spec delegation.RemoteAgentSpec) delegation.A2AClient {
				c := client.New(client.Options{AgentCardURL: spec.AgentCardURL})
				clients = append(clients, c)
				return &apBudgetClient{client: c, cancelChecks: &checks, t: t}
			}, Hook: delegation.DelegationLifecycleHookFuncs{BeforeFunc: func(ctx context.Context, _ delegation.BeforeDelegation) error {
				if mode == "new-rounds" {
					select {
					case <-ctx.Done():
						return ctx.Err()
					case <-time.After(200 * time.Millisecond):
					}
				}
				return nil
			}}})
			if err != nil {
				t.Fatal(err)
			}
			defer svc.Close()
			defer func() {
				for _, c := range clients {
					_ = c.Close()
				}
			}()
			rounds := 1
			if mode == "new-rounds" {
				rounds = 3
			}
			for round := 0; round < rounds; round++ {
				req := delegation.DelegationRequest{RunID: "budget-parent", Agent: "member", Message: &client.Message{ID: fmt.Sprintf("round-%d", round), Role: "user", TaskID: "task-21", Parts: []client.Part{{Kind: client.PartText, Text: "continue"}}}, Stream: true, IncludeRemoteArtifacts: true, ActiveExecutionTimeout: 100 * time.Millisecond}
				want := "active_execution_timeout"
				if mode == "wall" {
					req.Timeout = 100 * time.Millisecond
					req.ActiveExecutionTimeout = time.Second
					want = "deadline_exceeded"
				}
				if mode == "recovery" || mode == "new-rounds" {
					req.ActiveExecutionTimeout = 500 * time.Millisecond
				}
				out, e := svc.Delegate(apContext(t), req)
				if mode == "new-rounds" {
					if e != nil || out.Error != nil {
						t.Fatalf("fresh budget round %d failed: %v", round, e)
					}
					continue
				}
				if e == nil || out.Error == nil || out.Error.Code != want || out.RemoteTaskID != "task-21" {
					t.Fatalf("main failure=%+v err=%v", out.Error, e)
				}
				if out.Metadata["remote_cancel"] != "failed" || checks.Load() != 1 || p.cancelCalls.Load() != 1 {
					t.Fatal("bounded cancel failure was not a diagnostic")
				}
				if len(out.RemoteArtifacts) != 1 || out.RemoteArtifacts[0].Parts[0].Text != "partial before timeout" {
					t.Fatal("budget lost partial artifact")
				}
				if want == "active_execution_timeout" {
					var typed *adaptor.ActiveExecutionTimeoutError
					if !errors.Is(e, adaptor.ErrActiveExecutionTimeout) || !errors.As(e, &typed) || typed.Limit != req.ActiveExecutionTimeout {
						t.Fatal("budget cause/limit lost")
					}
				} else if strings.Contains(apJSON(out.Error), "limit_ms") {
					t.Fatal("wall timeout promoted active limit")
				}
				if mode == "recovery" && p.getCalls.Load() == 0 {
					t.Fatal("recovery not entered")
				}
			}
			if mode == "new-rounds" && (p.streamCalls.Load() != 3 || checks.Load() != 0) {
				t.Fatal("new rounds retried or cancelled")
			}
		})
	}
}

func TestAlignmentProtocolHookAndDuplicateDomains(t *testing.T) {
	for _, mode := range []string{"accepted", "rejected", "observer-error"} {
		t.Run(mode, func(t *testing.T) {
			var before, started, ioCount atomic.Int32
			p := &apWirePeer{streaming: true, frames: [][]any{{apWireDataStatus(apCapabilityWire), apWireDataStatus(apTodoWire), apWireStatus("TASK_STATE_COMPLETED", "done", "complete")}}, methodHook: func(_ context.Context, method string) {
				if method == "SendStreamingMessage" {
					ioCount.Add(1)
					if before.Load() == 0 || started.Load() == 0 {
						t.Error("remote I/O preceded hook acceptance/host fact")
					}
				}
			}}
			url := apPeer(t, p)
			hookErr := errors.New("T21_HOOK_PRIVATE")
			var domain atomic.Int32
			svc, e := delegation.NewService(delegation.Config{Agents: []delegation.AgentRef{delegation.Remote("member", url, delegation.Policy{})}, NewID: func() string { return fmt.Sprintf("domain/%d", domain.Add(1)) }, Hook: delegation.DelegationLifecycleHookFuncs{BeforeFunc: func(context.Context, delegation.BeforeDelegation) error {
				if mode == "rejected" {
					return hookErr
				}
				before.Add(1)
				return nil
			}}})
			if e != nil {
				t.Fatal(e)
			}
			defer svc.Close()
			observer := apAttachment{attach: func(context.Context, string) (adaptor.RunAttachment, error) {
				return adaptor.RunAttachment{Observer: func(_ context.Context, _ adaptor.RunEventInfo, e adaptor.Event) error {
					if f, ok := e.(adaptor.CapabilityInvocation); ok && f.Invocation.Source == capability.Host && f.Invocation.Phase == capability.Started {
						started.Add(1)
					}
					if mode == "observer-error" {
						return errors.New("T21_OBSERVER_PRIVATE")
					}
					return nil
				}}, nil
			}}
			d := &apDriver{run: func(ctx context.Context, r driver.Request, _ driver.EventSink) (driver.Response, error) {
				for i := 0; i < 2; i++ {
					out, e := svc.Delegate(ctx, delegation.DelegationRequest{RunID: r.RunID, Agent: "member", Objective: "domain", Stream: true, ScopeID: "caller", ParentScopeID: "actual-parent", ParentToolCallID: "tool"})
					if mode == "rejected" {
						if e == nil || out.Error.Code != "workflow_before_failed" || !errors.Is(e, hookErr) {
							t.Error("hook rejection lost")
						}
						return driver.Response{Output: "rejection handled"}, nil
					}
					if e != nil {
						return driver.Response{}, e
					}
				}
				return driver.Response{Output: "both complete"}, nil
			}}
			a := adaptor.New(d, svc.Option(), adaptor.WithRunServices(observer))
			defer a.Close(context.Background())
			events, result, err := apDrain(a.Stream(apContext(t), "delegate"))
			if err != nil || result == nil {
				t.Fatal("observation/hook handling changed parent outcome")
			}
			caps, todos := apFacts(events)
			if mode == "rejected" {
				if len(caps) != 0 || len(todos) != 0 || ioCount.Load() != 0 {
					t.Fatal("rejected hook forged invocation or IO")
				}
				return
			}
			if ioCount.Load() != 2 || before.Load() != 2 || len(caps) != 6 || len(todos) != 2 {
				t.Fatalf("accepted counts %d/%d caps=%d todos=%d", ioCount.Load(), before.Load(), len(caps), len(todos))
			}
			inner := map[string]bool{}
			for _, f := range caps {
				if f.Invocation.Source != capability.Relay {
					continue
				}
				tuple := apDecodeTuple(t, f.Invocation.InvocationID)
				if len(tuple) != 5 || tuple[0] != "invocation" || tuple[2] != "wire-run" || tuple[3] != "" || tuple[4] != "i" {
					t.Fatal("relay ID does not use complete tuple")
				}
				inner[f.Invocation.InvocationID] = true
				if f.Meta().Source == nil || f.Meta().Source.RunID != "wire-run" || f.Meta().Source.Sequence != 1 || f.Invocation.OccurredAt.Format(time.RFC3339) != "2026-09-07T00:00:00Z" {
					t.Fatal("remote source/time lost")
				}
			}
			if len(inner) != 2 {
				t.Fatal("identical remote run/invocation collided across delegations")
			}
		})
	}
}

type apReleaseFailure struct {
	threadstore.Store
	cause    error
	releases atomic.Int32
}

func (s *apReleaseFailure) ReleaseLease(ctx context.Context, l threadstore.Lease) error {
	s.releases.Add(1)
	if err := s.Store.ReleaseLease(ctx, l); err != nil {
		return err
	}
	return s.cause
}
func TestAlignmentProtocolLeaseTerminal(t *testing.T) {
	for _, sources := range []bool{false, true} {
		t.Run(fmt.Sprintf("sources=%v", sources), func(t *testing.T) {
			cause := errors.New("T21_LEASE_RELEASE_PRIVATE")
			store := &apReleaseFailure{Store: memory.NewStore(), cause: cause}
			attachment := apAttachment{attach: func(context.Context, string) (adaptor.RunAttachment, error) {
				a := adaptor.RunAttachment{}
				if sources {
					a.Events = func(ctx context.Context, _ string) <-chan adaptor.Event {
						ch := make(chan adaptor.Event)
						go func() { <-ctx.Done(); close(ch) }()
						return ch
					}
				}
				return a, nil
			}}
			a := apProvider(t, "claude", apClaudeFrames(), adaptor.WithThreadStore(store), adaptor.WithSpawn(), adaptor.WithRunServices(attachment))
			s := a.Thread("opaque/thread:21").Stream(apContext(t), "lease release")
			events, r, e := apDrain(s)
			var re *adaptor.RunError
			if r != nil || !errors.As(e, &re) || re.Reason != adaptor.ReasonInfrastructure || !errors.Is(e, cause) || store.releases.Load() == 0 {
				t.Fatalf("lease outcome %T %v", e, e)
			}
			apLifecycle(t, events, e)
			last := events[len(events)-1].(adaptor.RunFinished)
			if last.Reason != re.Reason || re.Result == nil || re.Result.Raw().Terminal == nil || len(re.Result.Transcript()) == 0 || re.Result.Text != "final answer" {
				t.Fatal("lease cleanup split final authority or lost provider audit")
			}
		})
	}
}

func TestAlignmentProtocolNeutralVocabulary(t *testing.T) {
	for _, dir := range []string{"capability", "todo", "driver", "internal/capabilityobs", "internal/todoobs"} {
		entries, e := os.ReadDir(dir)
		if e != nil {
			t.Fatal(e)
		}
		count := 0
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
				continue
			}
			count++
			f, e := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, entry.Name()), nil, parser.ImportsOnly)
			if e != nil {
				t.Fatal(e)
			}
			for _, imp := range f.Imports {
				path, e := strconv.Unquote(imp.Path.Value)
				if e != nil {
					t.Fatal(e)
				}
				if path == "github.com/agent-dance/agent-adaptor" || strings.Contains(path, "/claude") || strings.Contains(path, "/codex") || strings.Contains(path, "/codebuddy") || strings.Contains(path, "/cursor") || strings.Contains(path, "/internal/engine") {
					t.Fatalf("neutral vocabulary imports %s", path)
				}
				if strings.HasPrefix(dir, "internal/") && path == "encoding/json" {
					t.Fatal("neutral tracker interprets JSON")
				}
			}
		}
		if count == 0 {
			t.Fatal("empty architecture check")
		}
	}
	// Compile-time vocabulary boundaries are exercised by typed public values.
	_ = adaptor.CapabilityInvocation{Invocation: capability.Invocation{Ref: capability.Ref{Kind: capability.Skill}}}
	_ = driver.StreamPayload{Kind: driver.StreamTodoUpdated, Todo: &todo.Snapshot{Items: []todo.Item{}}}
}

func TestAlignmentProtocolPartialWrappers(t *testing.T) {
	for _, provider := range []string{"claude", "codebuddy"} {
		for _, abnormal := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/abnormal=%v", provider, abnormal), func(t *testing.T) {
				stream := func(event any) string {
					return apJSON(map[string]any{"type": "stream_event", "parent_tool_use_id": "", "event": event}) + "\n"
				}
				frames := `{"type":"system","subtype":"init","session_id":"t21-session"}` + "\n"
				frames += stream(map[string]any{"type": "message_start", "message": map[string]any{"id": "message-1", "role": "assistant", "usage": map[string]any{"input_tokens": 1, "output_tokens": 0}}})
				frames += stream(map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "tool_use", "id": "partial-id", "name": "mcp__知识_库__search", "input": map[string]any{}}})
				frames += stream(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": `{"query":`}})
				frames += stream(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": `"PRIVATE"}`}})
				frames += stream(map[string]any{"type": "content_block_stop", "index": 0})
				frames += apTool("partial-id", "mcp__知识_库__search", "", map[string]any{"query": "PRIVATE"})
				if abnormal {
					frames += `{"type":"error","message":"T21_FORMAL_FAILURE"}` + "\n"
				} else {
					frames += apToolResult("partial-id", "", false, nil) + apToolResult("partial-id", "", false, nil) + apTerminal
				}
				a := apProvider(t, provider, frames)
				events, r, err := apDrain(a.Stream(apContext(t), "partial wrappers"))
				caps, _ := apFacts(events)
				if len(caps) != 2 {
					t.Fatalf("partial/wrapper capability count=%d", len(caps))
				}
				if caps[0].Invocation.Phase != capability.Started {
					t.Fatal("missing start")
				}
				want := capability.Completed
				if abnormal {
					want = capability.Interrupted
				}
				if caps[1].Invocation.Phase != want {
					t.Fatalf("terminal phase=%s want=%s", caps[1].Invocation.Phase, want)
				}
				starts, ends, results := 0, 0, 0
				var deltas strings.Builder
				var args map[string]any
				for _, event := range events {
					switch v := event.(type) {
					case adaptor.ToolCall:
						if v.ID != "partial-id" {
							continue
						}
						if v.Phase == adaptor.PhaseStart {
							starts++
						}
						if v.Phase == adaptor.PhaseEnd {
							ends++
						}
						if v.Args != nil {
							args = v.Args
						}
						deltas.WriteString(v.ArgsDelta)
					case adaptor.ToolResult:
						if v.ID == "partial-id" {
							results++
						}
					}
				}
				if starts != 1 || ends != 1 {
					t.Fatalf("partial lifecycle %d/%d", starts, ends)
				}
				if args != nil || deltas.String() != `{"query":"PRIVATE"}` {
					t.Fatalf("Args and delta changed: args=%v deltas=%q", args, deltas.String())
				}
				if abnormal {
					var re *adaptor.RunError
					if r != nil || !errors.As(err, &re) || re.Result.Raw().Terminal == nil || results != 0 {
						t.Fatal("formal failure lost partial audit or invented result")
					}
				} else if err != nil || r == nil || results != 1 {
					t.Fatalf("wrapper success=%v results=%d", err, results)
				}
				apLifecycle(t, events, err)
			})
		}
	}
}

func TestAlignmentProtocolTodoWholeSnapshots(t *testing.T) {
	frames := `{"type":"system","subtype":"init","session_id":"t21-session"}` + "\n"
	for _, v := range []struct{ call, id, subject string }{{"c1", "real-B", "second alphabetically"}, {"c2", "real-A", "first alphabetically"}, {"c3", "", "synthetic"}} {
		frames += apTool(v.call, "TaskCreate", "", map[string]any{"subject": v.subject})
		var reply any
		if v.id != "" {
			reply = map[string]any{"task": map[string]any{"id": v.id}}
		}
		frames += apToolResult(v.call, "", false, reply)
	}
	frames += apTool("update-B", "TaskUpdate", "", map[string]any{"taskId": "real-B", "status": "completed"}) + apToolResult("update-B", "", false, nil)
	frames += apTool("reject-local-index", "TaskUpdate", "", map[string]any{"taskId": "3", "status": "completed"}) + apToolResult("reject-local-index", "", false, nil)
	frames += apTool("clear", "TodoWrite", "", map[string]any{"todos": []any{}}) + apToolResult("clear", "", false, nil) + apTerminal
	a := apProvider(t, "claude", frames)
	events, r, e := apDrain(a.Stream(apContext(t), "ordered todo"))
	if e != nil || r == nil {
		t.Fatal(e)
	}
	_, snapshots := apFacts(events)
	if len(snapshots) != 5 {
		t.Fatalf("snapshots=%d", len(snapshots))
	}
	for n, v := range snapshots {
		if v.Snapshot.Revision != uint64(n+1) || v.Snapshot.Items == nil {
			t.Fatal("revision or clear shape")
		}
		if n < 4 {
			wantCount := n + 1
			if wantCount > 3 {
				wantCount = 3
			}
			if len(v.Snapshot.Items) != wantCount || v.Snapshot.Items[0].ID != "real-B" {
				t.Fatal("snapshot not full or order changed")
			}
			if n > 0 && v.Snapshot.Items[1].ID != "real-A" {
				t.Fatal("creation order sorted")
			}
		}
	}
	synthetic := snapshots[2].Snapshot.Items[2]
	if !synthetic.SyntheticID || synthetic.ID != "synthetic:"+apTuple(events[0].Meta().RunID, "", "c3") {
		t.Fatalf("synthetic ID=%q", synthetic.ID)
	}
	if snapshots[3].Snapshot.Items[0].Status != todo.Completed || snapshots[3].Snapshot.Items[1].Status != todo.Pending || snapshots[3].Snapshot.Items[2].Status != todo.Pending || len(snapshots[4].Snapshot.Items) != 0 {
		t.Fatal("update changed unrelated items or clear lost")
	}
	c, _ := apServer(t, a, bridge.ExposurePolicy{IncludeTodos: true}, nil)
	task, wire := apClientDrain(t, c, apContext(t))
	if task.Status.State != client.TaskStateCompleted {
		t.Fatal(task.Status)
	}
	_, roundtrip := apFacts(apDecodeEvents(t, wire))
	if len(roundtrip) != 5 || len(roundtrip[3].Snapshot.Items) != 3 || roundtrip[3].Snapshot.Items[0].Status != todo.Completed || len(roundtrip[4].Snapshot.Items) != 0 {
		t.Fatal("A2A full snapshots truncated")
	}
}

func TestAlignmentProtocolOutgoingLoss(t *testing.T) {
	at := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	for _, kind := range []string{"capability-key", "todo-count", "sequence"} {
		t.Run(kind, func(t *testing.T) {
			var ev adaptor.Event = adaptor.CapabilityInvocation{Invocation: capability.Invocation{InvocationID: "i", Ref: capability.Ref{Kind: capability.MCP, Key: "safe", Operation: "read"}, Phase: capability.Started, Source: capability.Provider, Evidence: capability.ProviderProtocol, OccurredAt: at}}
			seq := uint64(1)
			if kind == "capability-key" {
				v := ev.(adaptor.CapabilityInvocation)
				v.Invocation.Ref.Key = strings.Repeat("T21_PRIVATE", 60)
				ev = v
			}
			if kind == "todo-count" {
				items := make([]todo.Item, 129)
				for i := range items {
					items[i] = todo.Item{ID: fmt.Sprint(i), Content: "T21_PRIVATE", Status: todo.Pending}
				}
				ev = adaptor.TodoUpdated{Snapshot: todo.Snapshot{Items: items, Source: todo.PlanUpdate, Revision: 1, OccurredAt: at}}
			}
			if kind == "sequence" {
				seq = 9007199254740992
			}
			ev = apMeta(ev, "wire", seq)
			r := &apRunner{id: "wire", events: []adaptor.Event{ev}, result: &adaptor.Result{Text: "safe"}}
			c, _ := apServer(t, r, bridge.ExposurePolicy{IncludeCapabilityInvocations: true, IncludeTodos: true}, nil)
			if kind == "sequence" {
				ctx := apContext(t)
				stream, err := c.SendStream(ctx, apSendReq())
				if err != nil {
					t.Fatal(err)
				}
				defer stream.Close()
				var observed []client.Event
				for {
					event, recvErr := stream.RecvContext(ctx)
					if recvErr != nil {
						err = recvErr
						break
					}
					observed = append(observed, event)
				}
				if !apTranslationOutcome(observed, err, apTranslationInvalidError) {
					t.Fatalf("unencodable Sequence outcome: events=%s error=%T %v", apJSON(observed), err, err)
				}
				t.Logf("translation HTTP outcome: eof=%v error=%T", err == io.EOF, err)
				apAssertCalls(t, r)
				if r.cancels.Load() != 1 {
					t.Fatal("translation Cancel not idempotent")
				}
				return
			}
			task, wire := apClientDrain(t, c, apContext(t))
			if task.Status.State != client.TaskStateCompleted {
				t.Fatal("semantic loss changed business success")
			}
			count := 0
			for _, e := range wire {
				if e.Status == nil || e.Status.Message == nil {
					continue
				}
				for _, p := range e.Status.Message.Parts {
					if p.Kind != client.PartData {
						continue
					}
					payload := apJSON(p.Data)
					if strings.Contains(payload, "T21_PRIVATE") {
						t.Fatal("invalid payload exposed")
					}
					if strings.Contains(payload, `"kind":"stream.dropped"`) {
						count++
						if !strings.Contains(payload, `"dropped_count":1`) || !strings.Contains(payload, `"event_kind"`) {
							t.Fatal("unsafe or incomplete wire loss")
						}
					}
				}
			}
			if count != 1 {
				t.Fatalf("loss count=%d", count)
			}
			apAssertCalls(t, r)
		})
	}
}

func TestAlignmentProtocolCodeBuddyControl(t *testing.T) {
	frames := `{"type":"system","subtype":"init","session_id":"t21-session"}` + "\n" + apTool("control-id", "mcp__知识_库__search", "", map[string]any{"query": "fixture"})
	frames += apJSON(map[string]any{"type": "control_request", "request_id": "permission-21", "request": map[string]any{"subtype": "can_use_tool", "tool_name": "mcp__知识_库__search", "tool_use_id": "control-id", "input": map[string]any{"query": "fixture"}}}) + "\n"
	frames += apToolResult("control-id", "", false, nil) + apToolResult("control-id", "", false, nil) + apTerminal
	var approvals atomic.Int32
	a := apProvider(t, "codebuddy", frames, adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAsk}}), adaptor.OnApproval(func(ctx context.Context, r *adaptor.ApprovalRequest) error { approvals.Add(1); return r.Approve(ctx) }))
	events, result, e := apDrain(a.Stream(apContext(t), "control protocol"))
	if e != nil || result == nil || approvals.Load() != 1 {
		t.Fatalf("real control response: count=%d err=%v", approvals.Load(), e)
	}
	caps, _ := apFacts(events)
	if len(caps) != 2 || caps[0].Invocation.Phase != capability.Started || caps[1].Invocation.Phase != capability.Completed || caps[1].Invocation.Evidence != capability.ProviderProtocol {
		t.Fatal("control/wrapper replay duplicated observation")
	}
	if result.Raw().Stdout != `{"response":{"request_id":"agent-adaptor-initialize","response":{},"subtype":"success"},"type":"control_response"}`+"\n"+frames || result.Raw().Terminal == nil {
		t.Fatal("control raw/terminal lost")
	}
	apLifecycle(t, events, nil)
}

func TestAlignmentProtocolArtifactRecoveryReconciliation(t *testing.T) {
	for _, tc := range []struct {
		name, query, want string
		conflict          bool
	}{{"extension", "alphabet", "alphabet", false}, {"lagging", "a", "alpha", false}, {"conflict", "different", "different", true}} {
		t.Run(tc.name, func(t *testing.T) {
			p := &apWirePeer{streaming: true, broken: true, frames: [][]any{{apArtifact([]any{map[string]any{"text": "alpha"}}, false, true)}}, task: apWireTask("TASK_STATE_COMPLETED", "new-terminal", "complete", []any{map[string]any{"text": tc.query}})}
			out, e, events := apPeerDelegate(t, p, delegation.DelegationRequest{IncludeRemoteArtifacts: true}, delegation.Policy{})
			if e != nil || len(out.RemoteArtifacts) != 1 || out.RemoteArtifacts[0].Parts[0].Text != tc.want {
				t.Fatalf("reconciliation: %v artifacts=%v", e, out.RemoteArtifacts)
			}
			conflicts := 0
			live := 0
			for _, event := range events {
				if event.Artifact != nil && len(event.Artifact.Parts) > 0 && event.Artifact.Parts[0].Text == "alpha" {
					live++
				}
				if event.Kind == delegation.DelegationStreamDropped && event.Raw["reason"] == "artifact_recovery_conflict" {
					conflicts++
					if len(event.Raw) != 2 || event.Raw["resolution"] != "recovered_snapshot" {
						t.Fatal("conflict leaked content or lacked resolution")
					}
				}
			}
			expected := 0
			if tc.conflict {
				expected = 1
			}
			if conflicts != expected || live == 0 || p.streamCalls.Load() != 1 || p.getCalls.Load() == 0 {
				t.Fatalf("conflicts=%d live=%d", conflicts, live)
			}
		})
	}
}
