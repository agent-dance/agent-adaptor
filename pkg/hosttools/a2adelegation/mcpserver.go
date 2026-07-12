package a2adelegation

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

const (
	maxToolTimeoutSeconds       = int64(1<<63-1) / int64(time.Second)
	defaultSSEKeepAliveInterval = 15 * time.Second
)

type ToolInput struct {
	Agent       string          `json:"agent"`
	Objective   string          `json:"objective"`
	Input       ToolInputBody   `json:"input,omitempty"`
	Constraints ToolConstraints `json:"constraints,omitempty"`
}

type ToolInputBody struct {
	Prompt    string          `json:"prompt,omitempty"`
	Context   string          `json:"context,omitempty"`
	Artifacts []InputArtifact `json:"artifacts,omitempty"`
}

type ToolConstraints struct {
	TimeoutSeconds    int  `json:"timeout_seconds,omitempty"`
	TimeoutSecondsSet bool `json:"-"`
	Stream            bool `json:"stream,omitempty"`
	MaxArtifacts      int  `json:"max_artifacts,omitempty"`
	MaxArtifactsSet   bool `json:"-"`
	HistoryLength     int  `json:"history_length,omitempty"`
	HistoryLengthSet  bool `json:"-"`
}

func ToolSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"agent":     map[string]any{"type": "string", "description": "Host-curated remote agent registry key."},
			"objective": map[string]any{"type": "string", "description": "Bounded task objective for the remote agent."},
			"input": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"prompt":  map[string]any{"type": "string"},
					"context": map[string]any{"type": "string"},
					"artifacts": map[string]any{
						"type": "array",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"name":      map[string]any{"type": "string"},
								"uri":       map[string]any{"type": "string"},
								"mime_type": map[string]any{"type": "string"},
							},
							"additionalProperties": false,
						},
					},
				},
				"additionalProperties": false,
			},
			"constraints": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"timeout_seconds": map[string]any{"type": "integer", "minimum": 1},
					"stream":          map[string]any{"type": "boolean"},
					"max_artifacts":   map[string]any{"type": "integer", "minimum": 0},
					"history_length":  map[string]any{"type": "integer", "minimum": 0},
				},
				"additionalProperties": false,
			},
		},
		"required":             []string{"agent", "objective"},
		"additionalProperties": false,
	}
}

func ParseToolInput(raw []byte) (ToolInput, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err == nil {
		if _, ok := envelope["endpoint_url"]; ok {
			return ToolInput{}, &DelegationError{Code: "invalid_tool_input", Message: "endpoint_url is not allowed; use a host-curated agent key"}
		}
		for key := range envelope {
			switch key {
			case "agent", "objective", "input", "constraints":
			default:
				return ToolInput{}, &DelegationError{Code: "invalid_tool_input", Message: "unknown field " + key}
			}
		}
	}
	var input ToolInput
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&input); err != nil {
		return ToolInput{}, &DelegationError{Code: "invalid_tool_input", Message: err.Error()}
	}
	if input.Agent == "" {
		return ToolInput{}, &DelegationError{Code: "invalid_tool_input", Message: "agent is required"}
	}
	if input.Objective == "" {
		return ToolInput{}, &DelegationError{Code: "invalid_tool_input", Message: "objective is required"}
	}
	if rawConstraints, ok := envelope["constraints"]; ok {
		var constraints map[string]json.RawMessage
		if err := json.Unmarshal(rawConstraints, &constraints); err == nil {
			if _, ok := constraints["timeout_seconds"]; ok {
				input.Constraints.TimeoutSecondsSet = true
			}
			if _, ok := constraints["max_artifacts"]; ok {
				input.Constraints.MaxArtifactsSet = true
			}
			if _, ok := constraints["history_length"]; ok {
				input.Constraints.HistoryLengthSet = true
			}
		}
	}
	if input.Constraints.TimeoutSecondsSet {
		if input.Constraints.TimeoutSeconds <= 0 {
			return ToolInput{}, &DelegationError{Code: "invalid_tool_input", Message: "timeout_seconds must be positive"}
		}
		if int64(input.Constraints.TimeoutSeconds) > maxToolTimeoutSeconds {
			return ToolInput{}, &DelegationError{Code: "invalid_tool_input", Message: "timeout_seconds exceeds maximum duration"}
		}
	}
	if input.Constraints.MaxArtifactsSet && input.Constraints.MaxArtifacts < 0 {
		return ToolInput{}, &DelegationError{Code: "invalid_tool_input", Message: "max_artifacts must be non-negative"}
	}
	if input.Constraints.HistoryLengthSet && input.Constraints.HistoryLength < 0 {
		return ToolInput{}, &DelegationError{Code: "invalid_tool_input", Message: "history_length must be non-negative"}
	}
	return input, nil
}

type MCPServerOptions struct {
	RunID              string
	ParentToolCallID   string
	Tenant             string
	BearerToken        string
	Tools              []ToolSpec
	DisableDefaultTool bool
	// SSEKeepAliveInterval controls the interval between Streamable HTTP
	// keepalives for long-running tools. Zero uses a conservative default.
	SSEKeepAliveInterval time.Duration

	// AllowUnauthenticatedLoopbackForTest permits an otherwise unprotected
	// loopback-only server for tests and local probes. Production HTTP sidecars
	// should always use a per-run bearer token.
	AllowUnauthenticatedLoopbackForTest bool
}

type MCPServer struct {
	Delegator *Delegator
	Options   MCPServerOptions
}

func NewMCPServer(delegator *Delegator, opts MCPServerOptions) *MCPServer {
	return &MCPServer{Delegator: delegator, Options: opts}
}

func (s *MCPServer) Handler() http.Handler {
	return http.HandlerFunc(s.ServeHTTP)
}

func (s *MCPServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := s.authorize(r); err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	defer r.Body.Close()
	var req rpcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeRPC(w, rpcError(nil, -32700, "parse error: "+err.Error()))
		return
	}
	if len(req.ID) == 0 && strings.HasPrefix(req.Method, "notifications/") {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if req.Method == "tools/call" && acceptsEventStream(r.Header.Get("Accept")) {
		writeMCPToolCallEventStream(w, req, s.sseKeepAliveInterval(), func() map[string]any {
			return s.handle(r, req)
		})
		return
	}
	writeMCPRPCResponse(w, func() map[string]any { return s.handle(r, req) })
}

// writeMCPRPCResponse 写入一次性 MCP JSON-RPC 响应。
func writeMCPRPCResponse(w http.ResponseWriter, buildPayload func() map[string]any) {
	writeRPC(w, buildPayload())
}

// writeMCPToolCallEventStream 以 Streamable HTTP 返回长时间工具调用。
//
// 客户端通过 Accept: text/event-stream 明确声明支持后，服务端在执行前先发送
// progress 或 SSE 注释，并在执行期间持续保活。最终 JSON-RPC 响应仍在同一 SSE
// 流内以 event: message 返回，因此不会改变工具调用的最终结果语义。
func writeMCPToolCallEventStream(w http.ResponseWriter, req rpcRequest, keepAliveInterval time.Duration, buildPayload func() map[string]any) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeMCPRPCResponse(w, buildPayload)
		return
	}
	if keepAliveInterval <= 0 {
		keepAliveInterval = defaultSSEKeepAliveInterval
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	progressToken, hasProgressToken := toolCallProgressToken(req.Params)
	if !writeMCPStreamStatus(w, flusher, progressToken, hasProgressToken, "delegation accepted") {
		return
	}

	payloads := make(chan map[string]any, 1)
	go func() {
		payloads <- buildPayload()
	}()

	ticker := time.NewTicker(keepAliveInterval)
	defer ticker.Stop()
	for {
		select {
		case payload := <-payloads:
			_ = writeMCPStreamMessage(w, flusher, payload)
			return
		case <-ticker.C:
			if !writeMCPStreamStatus(w, flusher, progressToken, hasProgressToken, "delegation running") {
				return
			}
		}
	}
}

func (s *MCPServer) sseKeepAliveInterval() time.Duration {
	if s != nil && s.Options.SSEKeepAliveInterval > 0 {
		return s.Options.SSEKeepAliveInterval
	}
	return defaultSSEKeepAliveInterval
}

func acceptsEventStream(accept string) bool {
	for _, value := range strings.Split(accept, ",") {
		mediaType := strings.TrimSpace(strings.SplitN(value, ";", 2)[0])
		if strings.EqualFold(mediaType, "text/event-stream") {
			return true
		}
	}
	return false
}

func toolCallProgressToken(params json.RawMessage) (any, bool) {
	var envelope struct {
		Meta struct {
			ProgressToken json.RawMessage `json:"progressToken"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal(params, &envelope); err != nil || len(envelope.Meta.ProgressToken) == 0 {
		return nil, false
	}
	decoder := json.NewDecoder(bytes.NewReader(envelope.Meta.ProgressToken))
	decoder.UseNumber()
	var token any
	if err := decoder.Decode(&token); err != nil {
		return nil, false
	}
	switch token.(type) {
	case string, json.Number:
		return token, true
	default:
		return nil, false
	}
}

func writeMCPStreamStatus(w http.ResponseWriter, flusher http.Flusher, progressToken any, hasProgressToken bool, message string) bool {
	if hasProgressToken {
		return writeMCPStreamMessage(w, flusher, map[string]any{
			"jsonrpc": "2.0",
			"method":  "notifications/progress",
			"params": map[string]any{
				"progressToken": progressToken,
				"progress":      0,
				"total":         1,
				"message":       message,
			},
		})
	}
	if _, err := fmt.Fprintf(w, ": %s\n\n", message); err != nil {
		return false
	}
	flusher.Flush()
	return true
}

func writeMCPStreamMessage(w http.ResponseWriter, flusher http.Flusher, payload map[string]any) bool {
	raw, err := json.Marshal(payload)
	if err != nil {
		return false
	}
	if _, err := fmt.Fprintf(w, "event: message\ndata: %s\n\n", raw); err != nil {
		return false
	}
	flusher.Flush()
	return true
}

func (s *MCPServer) authorize(r *http.Request) error {
	if s == nil {
		return errors.New("delegation MCP server is not configured")
	}
	if s.Options.BearerToken == "" {
		if s.Options.AllowUnauthenticatedLoopbackForTest && isLoopbackRequest(r) {
			return nil
		}
		return errors.New("delegation MCP bearer token is required")
	}
	got := strings.TrimSpace(r.Header.Get("Authorization"))
	want := "Bearer " + s.Options.BearerToken
	if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		return errors.New("unauthorized")
	}
	return nil
}

func isLoopbackRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *MCPServer) handle(r *http.Request, req rpcRequest) map[string]any {
	switch req.Method {
	case "initialize":
		return result(req.ID, map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "agent-adaptor-a2a-delegation", "version": "0.1.0"},
		})
	case "ping":
		return result(req.ID, map[string]any{})
	case "tools/list":
		return result(req.ID, map[string]any{"tools": s.listTools()})
	case "tools/call":
		return s.handleToolCall(r, req)
	default:
		return rpcError(req.ID, -32601, "method not found")
	}
}

func (s *MCPServer) handleToolCall(r *http.Request, req rpcRequest) map[string]any {
	if s == nil || s.Delegator == nil {
		return rpcError(req.ID, -32000, "delegator is not configured")
	}
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return rpcError(req.ID, -32602, "invalid tools/call params: "+err.Error())
	}
	if tool, ok := s.findTool(params.Name); ok {
		return s.handleCustomToolCall(r, req, tool, params.Arguments)
	}
	if params.Name != DelegateToolName {
		return rpcError(req.ID, -32602, "unknown tool "+params.Name)
	}
	input, err := ParseToolInput(params.Arguments)
	if err != nil {
		return toolResult(req.ID, DelegationResult{Status: "failed", Error: delegationErr(err)}, true)
	}
	runID := strings.TrimSpace(s.Options.RunID)
	if runID == "" {
		derr := &DelegationError{Code: "configuration_error", Message: "run context is required for delegation MCP tool"}
		return toolResult(req.ID, DelegationResult{Agent: input.Agent, RemoteProtocol: ProtocolA2A, Status: "failed", Error: derr}, true)
	}
	request := DelegationRequest{
		RunID:            runID,
		ParentToolCallID: s.Options.ParentToolCallID,
		Agent:            input.Agent,
		Objective:        input.Objective,
		Prompt:           input.Input.Prompt,
		Context:          input.Input.Context,
		Artifacts:        append([]InputArtifact(nil), input.Input.Artifacts...),
		Stream:           input.Constraints.Stream,
		Tenant:           s.Options.Tenant,
	}
	if input.Constraints.MaxArtifactsSet {
		request.MaxArtifacts = &input.Constraints.MaxArtifacts
	}
	if input.Constraints.HistoryLengthSet {
		request.HistoryLength = &input.Constraints.HistoryLength
	}
	if input.Constraints.TimeoutSecondsSet {
		request.Timeout = time.Duration(input.Constraints.TimeoutSeconds) * time.Second
	}
	out, err := s.Delegator.Delegate(r.Context(), request)
	if err != nil {
		out = ensureDelegationError(out, err)
	}
	return toolResult(req.ID, out, err != nil)
}

func (s *MCPServer) handleCustomToolCall(r *http.Request, req rpcRequest, tool ToolSpec, raw json.RawMessage) map[string]any {
	if tool.BuildRequest == nil {
		return rpcError(req.ID, -32602, "tool "+tool.Name+" does not define a request builder")
	}
	request, err := tool.BuildRequest(r.Context(), raw, ToolContext{
		RunID:            strings.TrimSpace(s.Options.RunID),
		ParentToolCallID: s.Options.ParentToolCallID,
		Tenant:           s.Options.Tenant,
	})
	if err != nil {
		return jsonToolResult(req.ID, map[string]any{
			"error": map[string]any{
				"code":    "tool_request_build_failed",
				"message": err.Error(),
			},
		}, true)
	}
	if request.RunID == "" {
		request.RunID = strings.TrimSpace(s.Options.RunID)
	}
	if request.ParentToolCallID == "" {
		request.ParentToolCallID = s.Options.ParentToolCallID
	}
	if request.Tenant == "" {
		request.Tenant = s.Options.Tenant
	}
	if strings.TrimSpace(request.RunID) == "" {
		derr := &DelegationError{Code: "configuration_error", Message: "run context is required for delegation MCP tool"}
		return toolResult(req.ID, DelegationResult{Agent: request.Agent, RemoteProtocol: ProtocolA2A, Status: "failed", Error: derr}, true)
	}
	out, err := s.Delegator.Delegate(r.Context(), request)
	if err != nil {
		out = ensureDelegationError(out, err)
	}
	value := any(out)
	if err == nil && tool.BuildResult != nil {
		built, buildErr := tool.BuildResult(r.Context(), out)
		if buildErr != nil {
			return jsonToolResult(req.ID, map[string]any{
				"delegation": out,
				"error": map[string]any{
					"code":    "tool_result_build_failed",
					"message": buildErr.Error(),
				},
			}, true)
		}
		value = built
	}
	return jsonToolResult(req.ID, value, err != nil)
}

func ensureDelegationError(out DelegationResult, err error) DelegationResult {
	if out.Status == "" {
		out.Status = "failed"
	}
	if out.RemoteProtocol == "" {
		out.RemoteProtocol = ProtocolA2A
	}
	if out.Error == nil {
		out.Error = delegationErr(err)
	}
	return out
}

func delegationErr(err error) *DelegationError {
	var derr *DelegationError
	if errors.As(err, &derr) {
		return derr
	}
	return &DelegationError{Code: "delegation_error", Message: err.Error()}
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc,omitempty"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

func toolResult(id json.RawMessage, value DelegationResult, isError bool) map[string]any {
	return jsonToolResult(id, value, isError)
}

func jsonToolResult(id json.RawMessage, value any, isError bool) map[string]any {
	raw, err := json.Marshal(value)
	if err != nil {
		return rpcError(id, -32603, "marshal tool result: "+err.Error())
	}
	return result(id, map[string]any{
		"content": []map[string]any{{"type": "text", "text": string(raw)}},
		"isError": isError,
	})
}

func (s *MCPServer) listTools() []map[string]any {
	tools := []map[string]any{}
	if !s.Options.DisableDefaultTool {
		tools = append(tools, map[string]any{
			"name":        DelegateToolName,
			"description": "Delegate a bounded task to a host-curated remote A2A agent and return a structured final result.",
			"inputSchema": ToolSchema(),
		})
	}
	for _, tool := range s.Options.Tools {
		if strings.TrimSpace(tool.Name) == "" {
			continue
		}
		schema := tool.InputSchema
		if schema == nil {
			schema = map[string]any{"type": "object"}
		}
		tools = append(tools, map[string]any{
			"name":        tool.Name,
			"description": tool.Description,
			"inputSchema": schema,
		})
	}
	return tools
}

func (s *MCPServer) findTool(name string) (ToolSpec, bool) {
	for _, tool := range s.Options.Tools {
		if tool.Name == name {
			return tool, true
		}
	}
	return ToolSpec{}, false
}

func result(id json.RawMessage, value any) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "result": value}
}

func rpcError(id json.RawMessage, code int, message string) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "error": map[string]any{"code": code, "message": message}}
}

func writeRPC(w http.ResponseWriter, payload map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	if payload == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		http.Error(w, fmt.Sprintf("encode rpc response: %v", err), http.StatusInternalServerError)
	}
}
