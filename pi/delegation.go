package pi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"

	"github.com/agent-dance/agent-adaptor/driver"
)

type agentScope struct {
	policy *driver.AgentToolPolicy
	skills []agentPreload
}

// Child execution is a provider tool implementation, as in Pi's official
// subagent extension. It uses the same native transports/parsers without a
// second public Runner, Driver dispatch, Thread identity or core pipeline.
type delegationRun struct {
	ctx       context.Context
	cancel    context.CancelFunc
	driver    configuredDriver
	config    Config
	request   driver.Request
	resources map[string]agentResources
	sink      *invocationSink
	mu        sync.Mutex
	closed    bool
	calls     map[string]bool
	reports   []childReport
	wg        sync.WaitGroup
}

type childReport struct {
	CallID    string             `json:"call_id"`
	Agent     string             `json:"agent"`
	SessionID string             `json:"session_id,omitempty"`
	Raw       *driver.RawStreams `json:"raw"`
	Usage     *driver.Usage      `json:"usage,omitempty"`
	Model     string             `json:"model,omitempty"`
	Provider  string             `json:"provider,omitempty"`
	ExitCode  int                `json:"exit_code"`
	Signal    string             `json:"signal,omitempty"`
	TimedOut  bool               `json:"timed_out,omitempty"`
	Failure   *driver.RunFailure `json:"failure,omitempty"`
	Error     string             `json:"error,omitempty"`
}

func (r *delegationRun) stop() {
	r.mu.Lock()
	r.closed = true
	r.cancel()
	r.mu.Unlock()
	r.wg.Wait()
}

func (r *delegationRun) execute(ctx context.Context, id, name, task string) (driver.Response, error) {
	var selected *driver.AgentSpec
	for _, spec := range r.request.ProfilePayload.Agents.Agents {
		if spec.RuntimeName == name {
			selected = &spec
			break
		}
	}
	if selected == nil || id == "" || strings.TrimSpace(task) == "" {
		return driver.Response{}, errors.New("pi: invalid subagent request")
	}
	r.mu.Lock()
	if r.closed || r.calls[id] {
		r.mu.Unlock()
		return driver.Response{}, errors.New("pi: expired or duplicate subagent call")
	}
	r.calls[id] = true
	r.wg.Add(1)
	r.mu.Unlock()
	defer r.wg.Done()
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(r.ctx, cancel)
	defer func() { stop(); cancel() }()
	if err := r.sink.awaitCall(ctx, id, name, task); err != nil {
		return driver.Response{}, err
	}
	c := cloneConfig(r.config)
	if selected.Model == "" {
		r.sink.mu.Lock()
		if r.sink.model != "" {
			c.Model = r.sink.model
		}
		if r.sink.provider != "" {
			c.Provider = r.sink.provider
		}
		r.sink.mu.Unlock()
	}
	if selected.Model != "" {
		c.Model = selected.Model
		if provider, model, found := strings.Cut(selected.Model, "/"); found {
			c.Provider, c.Model = provider, model
		}
	}
	if selected.ReasoningEffort != "" {
		c.Thinking = selected.ReasoningEffort
	}
	if selected.ToolPolicy != nil && !c.NoTools {
		c.Tools = []string{"read", "bash", "edit", "write", "grep", "find", "ls"}
	}
	req := r.request
	req.Config, req.Prompt, req.ModelOverride = c, task, ""
	req.Session, req.OutputSchema, req.StructuredOutputSource = nil, nil, ""
	// Child agents have independent context and cannot recursively acquire
	// the parent's portable delegation tool, matching Claude's subagent scope.
	req.ProfilePayload.Declared.Agents = true
	req.ProfilePayload.Agents = driver.AgentPayload{}
	resources, ok := r.resources[name]
	if !ok {
		return driver.Response{}, errors.New("pi: subagent resources were not resolved before prompting")
	}
	req.MCP = resources.MCP
	// Never reintroduce durable servers after the parent has resolved scope,
	// even when the effective inherited scope is empty.
	req.ProfilePayload.Declared.MCP = true
	req.ProfilePayload.MCP = req.MCP
	req.Instructions = nil
	if selected.Instructions != "" {
		req.Instructions = &driver.InstructionsBundleRef{Content: selected.Instructions, Mode: driver.InstructionModeReplace}
	}
	sink := &childSink{parent: r.sink, callID: id, agent: name}
	response, err := r.driver.execute(ctx, req, sink, &agentScope{policy: selected.ToolPolicy, skills: resources.Skills})
	report := childReport{CallID: id, Agent: name, SessionID: sink.session(), Raw: response.RawStreams, Usage: response.Usage, Model: response.Model, Provider: response.Provider, ExitCode: response.ExitCode, Signal: response.Signal, TimedOut: response.TimedOut, Failure: response.Failure}
	if err != nil {
		report.Error = err.Error()
	}
	r.mu.Lock()
	r.reports = append(r.reports, report)
	r.mu.Unlock()
	return response, err
}

func addUsage(total **driver.Usage, value *driver.Usage) {
	if value == nil {
		return
	}
	if *total == nil {
		*total = &driver.Usage{}
	}
	(*total).InputTokens += value.InputTokens
	(*total).OutputTokens += value.OutputTokens
	(*total).CachedInputTokens += value.CachedInputTokens
	(*total).EstimatedCostMilli += value.EstimatedCostMilli
}

func (r *delegationRun) finish(response *driver.Response) {
	r.stop()
	r.sink.mu.Lock()
	response.Transcript = append([]driver.TranscriptItem(nil), r.sink.transcript...)
	r.sink.mu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.reports) == 0 {
		return
	}
	if response.RawStreams == nil {
		response.RawStreams = &driver.RawStreams{}
	}
	if response.Usage != nil {
		copy := *response.Usage
		response.Usage = &copy
	}
	for _, report := range r.reports {
		addUsage(&response.Usage, report.Usage)
		if report.Raw != nil {
			response.RawStreams.Stdout += report.Raw.Stdout
			response.RawStreams.Stderr += report.Raw.Stderr
		}
	}
	if response.Metadata == nil {
		response.Metadata = map[string]string{}
	}
	raw, _ := json.Marshal(r.reports)
	response.Metadata["pi.child_runs"] = string(raw)
}

// Serialize semantic records from parent and child transports. The core sink
// still owns Event sequence/time and approval policy. Result transcript follows
// the same observed order; it is never reconstructed from arbitrary JSON.
type invocationSink struct {
	base            driver.EventSink
	owner           *delegationRun
	mu              sync.Mutex
	transcript      []driver.TranscriptItem
	calls           map[string]driver.TranscriptItem
	changed         chan struct{}
	model, provider string
}

// Called only by this Driver's official message parser. A native extension
// may have changed the parent's model since its construction-time Config.
func (s *invocationSink) observeModel(model, provider string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.model, s.provider = model, provider
}

func (s *invocationSink) awaitCall(ctx context.Context, id, name, task string) error {
	for {
		s.mu.Lock()
		call, exists := s.calls[id]
		if s.changed == nil {
			s.changed = make(chan struct{})
		}
		changed := s.changed
		s.mu.Unlock()
		if exists {
			input, _ := call.Input.(map[string]any)
			if call.ToolName != subagentTool || input["agent"] != name || input["task"] != task {
				return errors.New("pi: subagent request disagrees with its observed native tool call")
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}

func (s *invocationSink) Emit(event driver.RunEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if event.Type == driver.RunEventItem && event.Item != nil {
		s.transcript = append(s.transcript, *event.Item)
		if event.Item.Kind == driver.TranscriptToolCall {
			if s.calls == nil {
				s.calls = map[string]driver.TranscriptItem{}
			}
			s.calls[event.Item.ToolUseID] = *event.Item
			if s.changed != nil {
				close(s.changed)
				s.changed = nil
			}
		}
	}
	return s.base.Emit(event)
}

func (s *invocationSink) EmitStream(event driver.StreamPayload) error {
	if event.Kind == driver.StreamRunFinished || event.Kind == driver.StreamRunError {
		s.owner.stop()
		if event.Usage != nil {
			copy := *event.Usage
			event.Usage = &copy
		}
		s.owner.mu.Lock()
		for _, report := range s.owner.reports {
			addUsage(&event.Usage, report.Usage)
		}
		s.owner.mu.Unlock()
	}
	return s.base.EmitStream(event)
}

func (s *invocationSink) RequestDecision(ctx context.Context, req driver.DecisionRequest) (driver.DecisionResponse, error) {
	if sink, ok := s.base.(driver.DecisionCapableSink); ok {
		return sink.RequestDecision(ctx, req)
	}
	return driver.DecisionResponse{}, errors.New("pi: approval requires a decision-capable sink")
}

type childSink struct {
	parent        *invocationSink
	callID, agent string
	mu            sync.Mutex
	sessionID     string
}

func childID(callID, id string) string {
	if id == "" {
		return ""
	}
	raw, _ := json.Marshal([2]string{callID, id})
	return "pi-child:" + string(raw)
}
func (s *childSink) session() string { s.mu.Lock(); defer s.mu.Unlock(); return s.sessionID }
func (s *childSink) Emit(event driver.RunEvent) error {
	if event.Type == driver.RunEventItem && event.Item != nil {
		item := *event.Item
		if item.Kind == driver.TranscriptInit && item.SessionID != "" {
			s.mu.Lock()
			s.sessionID = item.SessionID
			s.mu.Unlock()
		}
		item.SessionID = s.session()
		item.ToolUseID = childID(s.callID, item.ToolUseID)
		item.Metadata = cloneMetadata(item.Metadata)
		item.Metadata["pi.parent_tool_call_id"], item.Metadata["pi.agent"] = s.callID, s.agent
		event.Item = &item
	}
	return s.parent.Emit(event)
}
func cloneMetadata(values map[string]string) map[string]string {
	copy := map[string]string{}
	for key, value := range values {
		copy[key] = value
	}
	return copy
}
func (s *childSink) EmitStream(event driver.StreamPayload) error {
	switch event.Kind {
	case driver.StreamRunStarted, driver.StreamRunFinished, driver.StreamRunError:
		return nil
	}
	event.ThreadID = s.session()
	event.MessageID, event.ToolCallID = childID(s.callID, event.MessageID), childID(s.callID, event.ToolCallID)
	if event.Kind == driver.StreamStepStarted || event.Kind == driver.StreamStepFinished {
		event.Name = childID(s.callID, event.Name)
	}
	return s.parent.EmitStream(event)
}
func (s *childSink) RequestDecision(ctx context.Context, request driver.DecisionRequest) (driver.DecisionResponse, error) {
	request.ToolCallID = childID(s.callID, request.ToolCallID)
	return s.parent.RequestDecision(ctx, request)
}

func (b *mcpBridge) serveDelegation(w http.ResponseWriter, req *http.Request) {
	if b.delegation == nil {
		http.NotFound(w, req)
		return
	}
	var call struct {
		ToolCallID string `json:"toolCallId"`
		Agent      string `json:"agent"`
		Task       string `json:"task"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, req.Body, 4<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&call); err != nil {
		http.Error(w, "invalid subagent request", 400)
		return
	}
	response, err := b.delegation.execute(req.Context(), call.ToolCallID, call.Agent, call.Task)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if response.Failure != nil {
		http.Error(w, response.Failure.Message, http.StatusUnprocessableEntity)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"text": response.Output})
}

const delegationExtension = `
function installDelegation(pi) {
  if (!agents.length) return;
  pi.registerTool({
    name: "adaptor_subagent", label: "Delegate to subagent",
    description: "Delegate a focused task to an isolated child Pi agent. Available agents:\n" + agents.map(a => a.RuntimeName + ": " + a.Description).join("\n"),
    parameters: {type:"object", properties:{agent:{type:"string",enum:agents.map(a => a.RuntimeName)},task:{type:"string",minLength:1}},required:["agent","task"],additionalProperties:false},
    async execute(toolCallId, args, signal) {
      const response = await fetch(process.env[urlKey] + "/delegate", {method:"POST", signal, headers:{"Authorization":"Bearer " + process.env[tokenKey],"Content-Type":"application/json"},body:JSON.stringify({toolCallId,...args})});
      if (!response.ok) throw new Error(await response.text());
      const result = await response.json();
      return {content:[{type:"text",text:result.text}],details:{agent:args.agent}};
    }
  });
}
`

var _ driver.EventSink = (*invocationSink)(nil)
