package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/agent-dance/agent-adaptor/v2/driver"
	"github.com/agent-dance/agent-adaptor/v2/hosttools/team"
)

// fakeControl supplies deterministic tool completion, never sleep-based test
// synchronization. It models the shell gate used by the live prompts.
type fakeControl struct {
	mu        sync.Mutex
	gates     map[string]chan struct{}
	squad     *team.Team
	toolCount atomic.Int32
}

func newControl() *fakeControl { return &fakeControl{gates: map[string]chan struct{}{}} }
func (c *fakeControl) gate(name string) <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gates[name] == nil {
		c.gates[name] = make(chan struct{})
	}
	return c.gates[name]
}
func (c *fakeControl) release(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gates[name] == nil {
		c.gates[name] = make(chan struct{})
	}
	select {
	case <-c.gates[name]:
	default:
		close(c.gates[name])
	}
}

type scriptedDriver struct {
	control *fakeControl
	role    string
	serial  atomic.Int32
}

func (d *scriptedDriver) Descriptor() driver.Descriptor {
	return driver.Descriptor{
		Type: "issue-daemon-fake", Sessions: driver.SessionCapability{SupportsResume: true}, Workspace: driver.WorkspaceCapability{Supported: true},
		MCP:           driver.MCPCapability{Supported: true, HTTP: true},
		RunPolicyCaps: driver.RunPolicyCapabilities{Isolation: true, Permission: driver.HumanDecisionSupport{Ask: true, AutoApprove: true, AutoReject: true}},
		Messages:      driver.MessageCapability{Boundaries: []driver.Boundary{driver.BoundaryImmediate}, Interrupt: true, PersistsDelivered: true},
	}
}
func (*scriptedDriver) ValidateConfig(any) error { return nil }
func (d *scriptedDriver) SessionConfigFingerprint() (string, error) {
	return "issue-daemon-script-v1/" + d.role, nil
}
func (*scriptedDriver) SessionCodec() driver.SessionCodec { return scriptCodec{} }
func (*scriptedDriver) StreamCapability() driver.StreamCapability {
	return driver.StreamCapability{Native: true}
}

type scriptCodec struct{}

func (scriptCodec) Name() string { return "issue-daemon-script-v1" }
func (scriptCodec) ToParams(s *driver.SessionState) driver.SessionParams {
	if s == nil {
		return driver.SessionParams{}
	}
	return driver.SessionParams{ResumeID: s.ResumeID, DisplayID: s.DisplayID, Values: maps.Clone(s.Data)}
}
func (scriptCodec) FromParams(p driver.SessionParams) *driver.SessionState {
	if p.ResumeID == "" && p.DisplayID == "" && len(p.Values) == 0 {
		return nil
	}
	return &driver.SessionState{ResumeID: p.ResumeID, DisplayID: p.DisplayID, Data: maps.Clone(p.Values)}
}
func (scriptCodec) GuardFingerprint(p driver.SessionParams) string { return p.ResumeID }

func (d *scriptedDriver) Run(ctx context.Context, req driver.Request, sink driver.EventSink) (resp driver.Response, err error) {
	data := map[string]string{}
	if req.Session != nil && req.Session.State != nil {
		maps.Copy(data, req.Session.State.Data)
	}
	id := fmt.Sprintf("fake-session-%s-%s", d.role, req.RunID)
	if req.Session != nil && req.Session.State != nil {
		id = req.Session.State.ResumeID
	}
	emit := func(p driver.StreamPayload) error { return sink.EmitStream(p) }
	if err = emit(driver.StreamPayload{Kind: driver.StreamRunStarted, RunID: req.RunID}); err != nil {
		return resp, err
	}
	final := "OK"
	prompt := req.Prompt
	apply := func(s string) error {
		for _, f := range []struct{ name, content string }{{"base.txt", "BASE\n"}, {"human.txt", "HUMAN\n"}, {"peer.txt", "PEER\n"}, {"after.txt", "AFTER\n"}, {"squad.txt", "SQUAD\n"}} {
			if strings.Contains(s, f.name) && !strings.Contains(s, "CONTEXT_QUERY") {
				if err := os.WriteFile(filepath.Join(req.Workspace.CWD, f.name), []byte(f.content), 0600); err != nil {
					return err
				}
			}
		}
		if strings.Contains(s, "human.txt") && !strings.Contains(s, "CONTEXT_QUERY") {
			data["change"] = "HUMAN"
		}
		if strings.Contains(s, "REMEMBER_TOKEN_741") {
			data["remembered"] = "REMEMBER_TOKEN_741"
		}
		switch {
		case strings.Contains(s, "CONTEXT_QUERY"):
			final = data["remembered"] + " " + data["change"]
		case strings.Contains(s, "INTERRUPTED"):
			final = "INTERRUPTED"
		case strings.Contains(s, "CHAT_ONE"):
			final = "CHAT_ONE"
		case strings.Contains(s, "CHAT_TWO"):
			final = "CHAT_TWO"
		case strings.Contains(s, "CHAT_THREE"):
			final = "CHAT_THREE"
		case strings.Contains(s, "HOST_ACK"):
			final = "HOST_ACK"
		case strings.Contains(s, "SQUAD_DONE"):
			final = "SQUAD_DONE"
		}
		return nil
	}
	if err = apply(prompt); err != nil {
		return resp, err
	}
	if d.role == "checker" {
		raw, e := os.ReadFile(filepath.Join(req.Workspace.CWD, "go.mod"))
		if e != nil {
			return resp, e
		}
		if !strings.Contains(string(raw), "module example.com/issue") {
			return resp, fmt.Errorf("checker: unexpected go.mod")
		}
		final = "CHECKED"
	}
	gate := ""
	for _, name := range []string{"issue", "busy", "interrupt", "cancel", "limit1", "limit2", "chat1", "chatcancel", "worker", "teamchat"} {
		if strings.Contains(prompt, "GATE:"+name+" ") {
			gate = name
			break
		}
	}
	// The leader uses the public MCP HTTP handler directly, avoiding a second
	// listener or an HTTP client in the offline exercise.
	if d.role == "leader" && strings.Contains(prompt, "SQUAD_START") {
		tools, e := d.rpc(ctx, req, "tools/list", nil)
		if e != nil {
			return resp, e
		}
		list, _ := tools["tools"].([]any)
		d.control.toolCount.Store(int32(len(list)))
		for _, member := range []string{"writer", "checker"} {
			objective := "GATE:worker Read go.mod only, then reply CHECKED."
			if member == "writer" {
				objective = "GATE:worker Write squad.txt exactly SQUAD followed by a newline, then reply WRITTEN."
			}
			if e = emit(driver.StreamPayload{Kind: driver.StreamToolCallStart, ToolCallID: member, Name: "team_delegate", Args: map[string]any{"member": member, "objective": objective}}); e != nil {
				return resp, e
			}
			result, e := d.rpc(ctx, req, "tools/call", map[string]any{"name": "team_delegate", "arguments": map[string]any{"member": member, "objective": objective}})
			if e != nil {
				return resp, e
			}
			if e = emit(driver.StreamPayload{Kind: driver.StreamToolCallEnd, ToolCallID: member, Result: result}); e != nil {
				return resp, e
			}
		}
		final = "DELEGATED"
	}
	if d.role == "leader" && !strings.Contains(prompt, "SQUAD_START") && !strings.Contains(prompt, "HOST_ACK") && !strings.Contains(prompt, "CHAT_") {
		final = "SQUAD_DONE"
	}
	toolID := fmt.Sprint(d.serial.Add(1))
	if gate != "" {
		if req.Messages != nil {
			req.Messages.Window(true)
			defer req.Messages.Window(false)
		}
		if err = emit(driver.StreamPayload{Kind: driver.StreamToolCallStart, ToolCallID: toolID, Name: "shell", Args: map[string]any{"gate": gate}}); err != nil {
			return resp, err
		}
		waiting := true
		for waiting {
			var signal <-chan struct{}
			if req.Messages != nil {
				signal = req.Messages.Signal()
			}
			select {
			case <-ctx.Done():
				return resp, ctx.Err()
			case <-d.control.gate(gate):
				waiting = false
			case <-signal:
				for _, item := range req.Messages.Claim(driver.BoundaryImmediate) {
					mechanism := "fake.turn_steer"
					if item.Timing == driver.TimingInterrupt {
						mechanism = "fake.turn_interrupt"
						waiting = false
					}
					e := apply(item.Text)
					req.Messages.Ack(item.ID, driver.MessageAck{Delivered: e == nil, Mechanism: mechanism, Err: e})
					if e != nil {
						return resp, e
					}
				}
			}
		}
		if err = emit(driver.StreamPayload{Kind: driver.StreamToolCallEnd, ToolCallID: toolID}); err != nil {
			return resp, err
		}
	}
	usage := &driver.Usage{InputTokens: 2, OutputTokens: 1}
	if err = sink.Emit(driver.RunEvent{Type: driver.RunEventTurnCompleted, Turn: &driver.TurnCompleted{Text: final, Usage: usage}}); err != nil {
		return resp, err
	}
	if err = emit(driver.StreamPayload{Kind: driver.StreamRunFinished, RunID: req.RunID, Usage: usage}); err != nil {
		return resp, err
	}
	return driver.Response{Output: final, Summary: final, Usage: usage, Checkpoint: &driver.Checkpoint{Valid: true, State: &driver.SessionState{ResumeID: id, Data: data}}, Transcript: []driver.TranscriptItem{{Kind: driver.TranscriptAssistant, Text: final}}, RawStreams: &driver.RawStreams{Stdout: final}, Provider: "scripted"}, nil
}
func (d *scriptedDriver) rpc(ctx context.Context, req driver.Request, method string, params any) (map[string]any, error) {
	var token, url string
	for _, server := range req.MCP.Servers {
		if strings.HasPrefix(server.Key, "team") || server.BearerTokenEnvVar != "" {
			url = server.URL
			for _, env := range req.Runtime.SecretEnv {
				if env.Name == server.BearerTokenEnvVar {
					token = env.Value
				}
			}
		}
	}
	if token == "" {
		return nil, fmt.Errorf("fake: missing team MCP capability token")
	}
	b, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		return nil, err
	}
	r := httptest.NewRequest(http.MethodPost, url, bytes.NewReader(b)).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	d.control.squad.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		return nil, fmt.Errorf("team MCP status %d: %s", w.Code, w.Body.String())
	}
	var reply struct {
		Result map[string]any `json:"result"`
		Error  any            `json:"error"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
		return nil, err
	}
	if reply.Error != nil {
		return nil, fmt.Errorf("MCP error: %v", reply.Error)
	}
	if failed, _ := reply.Result["isError"].(bool); failed {
		return nil, fmt.Errorf("MCP tool error: %v", reply.Result)
	}
	return reply.Result, nil
}
