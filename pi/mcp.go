package pi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/driverutil"
	"github.com/agent-dance/agent-adaptor/internal/processx"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Pi intentionally has no native MCP client. An official extension exposes
// the tools returned by MCP tools/list. The Go SDK owns per-run MCP framing,
// authentication and cancellation; the extension uses only Pi's registerTool.
type mcpBridge struct {
	ctx         context.Context
	cancel      context.CancelFunc
	sessions    []*mcpsdk.ClientSession
	server      *http.Server
	dir         string
	tools       []mcpTool
	token       string
	request     driver.Request
	decisions   driver.DecisionCapableSink
	abort       context.CancelCauseFunc
	definitions []extensionTool
	interactive bool
	permission  bool
	delegation  *delegationRun
	scope       *agentScope
}
type mcpTool struct {
	session *mcpsdk.ClientSession
	name    string
}
type extensionTool struct {
	Name        string `json:"name"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"`
}

func prepareExtension(ctx context.Context, req driver.Request, env []driver.EnvBinding, cwd string, allowlist *[]string, sink driver.EventSink, abort context.CancelCauseFunc) (extension string, bindings []driver.EnvBinding, closeBridge func(), err error) {
	b, err := prepareBridge(ctx, req, env, cwd, allowlist, sink, abort)
	if err != nil {
		return "", nil, nil, err
	}
	if b == nil {
		return "", env, func() {}, nil
	}
	defer func() {
		if err != nil {
			b.close()
		}
	}()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, nil, err
	}
	b.server = &http.Server{Handler: http.HandlerFunc(b.serve), ReadHeaderTimeout: 5 * time.Second, BaseContext: func(net.Listener) context.Context { return b.ctx }}
	go func() { _ = b.server.Serve(listener) }()
	b.dir, err = os.MkdirTemp("", "agent-adaptor-pi-mcp-")
	if err != nil {
		return "", nil, nil, err
	}
	urlKey := "AGENT_ADAPTOR_PI_MCP_URL_" + b.token[:16]
	tokenKey := "AGENT_ADAPTOR_PI_MCP_TOKEN_" + b.token[:16]
	source, err := b.extensionSource(urlKey, tokenKey)
	if err != nil {
		return "", nil, nil, err
	}
	extension = filepath.Join(b.dir, "mcp.mjs")
	if err = os.WriteFile(extension, []byte(source), 0600); err != nil {
		return "", nil, nil, err
	}
	bindings = append(append([]driver.EnvBinding(nil), env...), driver.EnvBinding{Name: urlKey, Value: "http://" + listener.Addr().String()}, driver.EnvBinding{Name: tokenKey, Value: b.token})
	return extension, bindings, b.close, nil
}

// Connections and approval responders belong to the current invocation, even
// when an RPC process and its extension listener survive across Thread turns.
func prepareBridge(ctx context.Context, req driver.Request, env []driver.EnvBinding, cwd string, allowlist *[]string, sink driver.EventSink, abort context.CancelCauseFunc, scopes ...*agentScope) (b *mcpBridge, err error) {
	interactive := wantsInteractions(req.Policy.HumanDecision)
	var scope *agentScope
	if len(scopes) > 0 {
		scope = scopes[0]
	}
	if len(req.MCP.Servers) == 0 && !interactive && len(req.ProfilePayload.Agents.Agents) == 0 && (scope == nil || (scope.policy == nil && len(scope.skills) == 0)) {
		return nil, nil
	}
	bridgeCtx, cancel := context.WithCancel(ctx)
	b = &mcpBridge{ctx: bridgeCtx, cancel: cancel, request: req, abort: abort, interactive: interactive, permission: interactive && driver.EffectiveHumanDecisionPolicy(req.Policy.HumanDecision).Permission != driver.HumanDecisionAutoApprove}
	b.scope = scope
	if len(req.ProfilePayload.Agents.Agents) > 0 && len(*allowlist) > 0 {
		*allowlist = append(*allowlist, subagentTool)
	}
	defer func(bridge *mcpBridge) {
		if err != nil {
			bridge.close()
		}
	}(b)
	if interactive {
		var ok bool
		b.decisions, ok = sink.(driver.DecisionCapableSink)
		if !ok || abort == nil {
			return nil, errors.New("pi: interactive policy requires a decision-capable sink and cancellation binding")
		}
		if len(*allowlist) > 0 {
			*allowlist = append(*allowlist, reviewPlanTool, askUserTool)
		}
	}
	random := make([]byte, 32)
	if _, err = rand.Read(random); err != nil {
		return nil, err
	}
	b.token = hex.EncodeToString(random)
	definitions := make([]extensionTool, 0)
	for _, spec := range req.MCP.Servers {
		transport, transportErr := mcpTransport(bridgeCtx, spec, env, cwd)
		if transportErr != nil {
			return nil, transportErr
		}
		client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "agent-adaptor-pi", Version: "1"}, nil)
		session, connectErr := client.Connect(bridgeCtx, transport, nil)
		if connectErr != nil {
			return nil, fmt.Errorf("pi MCP %q connect: %w", spec.Key, connectErr)
		}
		b.sessions = append(b.sessions, session)
		cursor := ""
		seenCursors := map[string]bool{}
		seenNames := map[string]bool{}
		for {
			listed, listErr := session.ListTools(bridgeCtx, &mcpsdk.ListToolsParams{Cursor: cursor})
			if listErr != nil {
				return nil, fmt.Errorf("pi MCP %q list tools: %w", spec.Key, listErr)
			}
			for _, tool := range listed.Tools {
				if tool == nil || tool.Name == "" || seenNames[tool.Name] {
					return nil, fmt.Errorf("pi MCP %q returned empty or duplicate tool name", spec.Key)
				}
				seenNames[tool.Name] = true
				schema, ok := tool.InputSchema.(map[string]any)
				if !ok || schema["type"] != "object" {
					return nil, fmt.Errorf("pi MCP %q tool %q requires an object input schema", spec.Key, tool.Name)
				}
				// Hash a length-delimited tuple rather than joining server and tool
				// names with an ambiguous separator. Native Pi tools cannot collide.
				identity, _ := json.Marshal([]string{spec.Key, tool.Name})
				sum := sha256.Sum256(identity)
				name := "mcp_" + hex.EncodeToString(sum[:16])
				if len(*allowlist) > 0 {
					*allowlist = append(*allowlist, name)
				}
				definitions = append(definitions, extensionTool{Name: name, Label: tool.Name, Description: tool.Name + " (MCP server " + spec.Key + "): " + tool.Description, Parameters: tool.InputSchema})
				b.tools = append(b.tools, mcpTool{session: session, name: tool.Name})
			}
			cursor = listed.NextCursor
			if cursor == "" {
				break
			}
			if seenCursors[cursor] {
				return nil, errors.New("pi MCP repeated tools/list cursor")
			}
			seenCursors[cursor] = true
		}
	}
	b.definitions = definitions
	return b, nil
}

func (b *mcpBridge) extensionSource(urlKey, tokenKey string) (string, error) {
	defsJSON, err := json.Marshal(b.definitions)
	if err != nil {
		return "", err
	}
	if string(defsJSON) == "null" {
		defsJSON = []byte("[]")
	}
	urlKeyJSON, _ := json.Marshal(urlKey)
	tokenKeyJSON, _ := json.Marshal(tokenKey)
	interactionConfig, _ := json.Marshal(map[string]bool{"enabled": b.interactive, "permission": b.permission})
	agents, _ := json.Marshal(b.request.ProfilePayload.Agents.Agents)
	if string(agents) == "null" {
		agents = []byte("[]")
	}
	policy := []byte("null")
	preloads := []byte("[]")
	if b.scope != nil {
		policy, _ = json.Marshal(b.scope.policy)
		if len(b.scope.skills) > 0 {
			preloads, _ = json.Marshal(b.scope.skills)
		}
	}
	return "const definitions = " + string(defsJSON) + ";\nconst urlKey = " + string(urlKeyJSON) + ";\nconst tokenKey = " + string(tokenKeyJSON) + ";\nconst interaction = " + string(interactionConfig) + ";\nconst agents = " + string(agents) + ";\nconst toolPolicy = " + string(policy) + ";\nconst skillPreloads = " + string(preloads) + ";\n" + mcpExtension + interactionExtension + delegationExtension + agentPreloadExtension, nil
}

func (b *mcpBridge) close() {
	b.cancel()
	if b.server != nil {
		_ = b.server.Close()
	}
	for _, session := range b.sessions {
		_ = session.Close()
	}
	if b.dir != "" {
		_ = os.RemoveAll(b.dir)
	}
}
func (b *mcpBridge) serve(w http.ResponseWriter, r *http.Request) {
	if !validBridgeRequest(w, r, b.token) {
		return
	}
	b.handle(w, r)
}
func validBridgeRequest(w http.ResponseWriter, r *http.Request, token string) bool {
	if r.Method != http.MethodPost || r.Header.Get("Origin") != "" {
		http.Error(w, "unsupported request", http.StatusMethodNotAllowed)
		return false
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	return true
}
func (b *mcpBridge) handle(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/delegate" {
		b.serveDelegation(w, r)
		return
	}
	if r.URL.Path == "/decision" {
		b.serveDecision(w, r)
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/tool/") {
		http.NotFound(w, r)
		return
	}
	index, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/tool/"))
	if err != nil || index < 0 || index >= len(b.tools) {
		http.NotFound(w, r)
		return
	}
	var args map[string]any
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20))
	if err := decoder.Decode(&args); err != nil {
		http.Error(w, "invalid arguments", http.StatusBadRequest)
		return
	}
	tool := b.tools[index]
	result, err := tool.session.CallTool(r.Context(), &mcpsdk.CallToolParams{Name: tool.name, Arguments: args})
	if err != nil {
		http.Error(w, "MCP tool call failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	if result.NeedsInput() {
		http.Error(w, "MCP input requests are unsupported by the Pi tool bridge", http.StatusUnprocessableEntity)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

type mcpHeaders struct {
	headers http.Header
	base    http.RoundTripper
}

func (t mcpHeaders) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	for key, values := range t.headers {
		clone.Header[key] = append([]string(nil), values...)
	}
	return t.base.RoundTrip(clone)
}
func mcpTransport(ctx context.Context, spec driver.MCPServerSpec, env []driver.EnvBinding, cwd string) (mcpsdk.Transport, error) {
	if spec.Transport == driver.MCPTransportStdio {
		command, args, err := processx.PrepareCommand(spec.Command, spec.Args)
		if err != nil {
			return nil, err
		}
		cmd := exec.CommandContext(ctx, command, args...)
		cmd.Dir = cwd
		cmd.Env = os.Environ()
		processx.ConfigureCancellation(cmd)
		for _, binding := range env {
			cmd.Env = append(cmd.Env, binding.Name+"="+binding.Value)
		}
		for key, value := range spec.Env {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
		return &mcpsdk.CommandTransport{Command: cmd, TerminateDuration: time.Second}, nil
	}
	headers := http.Header{}
	for key, value := range spec.Headers {
		headers.Set(key, value)
	}
	if spec.BearerTokenEnvVar != "" {
		token, _ := driverutil.ResolvedEnvValue(env, spec.BearerTokenEnvVar)
		if token == "" {
			return nil, fmt.Errorf("pi MCP bearer environment %q is unset", spec.BearerTokenEnvVar)
		}
		headers.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Transport: mcpHeaders{headers: headers, base: http.DefaultTransport}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	switch spec.Transport {
	case driver.MCPTransportHTTP:
		return &mcpsdk.StreamableClientTransport{Endpoint: spec.URL, HTTPClient: client, MaxRetries: -1}, nil
	case driver.MCPTransportSSE:
		return &mcpsdk.SSEClientTransport{Endpoint: spec.URL, HTTPClient: client}, nil
	default:
		return nil, fmt.Errorf("pi: unsupported MCP transport %q", spec.Transport)
	}
}

const mcpExtension = `
export default function (pi) {
  installInteractions(pi);
  installDelegation(pi);
  installSkillPreloads(pi);
  definitions.forEach((tool, index) => pi.registerTool({
    ...tool,
    async execute(_toolCallId, params, signal) {
      const response = await fetch(process.env[urlKey] + "/tool/" + index, {
        method: "POST",
        headers: { "Authorization": "Bearer " + process.env[tokenKey], "Content-Type": "application/json" },
        body: JSON.stringify(params), signal
      });
      if (!response.ok) throw new Error(await response.text());
      const result = await response.json();
      // Pi accepts text and image content. Preserve other MCP content and the
      // complete structured result in details rather than silently dropping it.
      const content = (result.content || []).map(item =>
        item.type === "text" || item.type === "image" ? item : {type: "text", text: JSON.stringify(item)});
      if (result.isError) throw new Error(JSON.stringify(result));
      return { content, details: result };
    }
  }));
}
`
