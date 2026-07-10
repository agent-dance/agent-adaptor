package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/claude"
	"github.com/agent-dance/agent-adaptor/memory"
	bridgea2a "github.com/agent-dance/agent-adaptor/pkg/bridges/a2a"
	clienta2a "github.com/agent-dance/agent-adaptor/pkg/clients/a2a"
	"github.com/agent-dance/agent-adaptor/pkg/hosttools/a2adelegation"
)

const (
	agentCardPath = "/.well-known/agent-card.json"
	jsonRPCPath   = "/a2a"
)

func main() {
	model := flag.String("model", "", "Claude model (default claude-sonnet-4, or CLAUDE_MODEL env)")
	command := flag.String("command", "", "Explicit claude CLI command")
	leaderModel := flag.String("leader-model", "", "Override model for leader")
	writerModel := flag.String("writer-model", "", "Override model for writer")
	flag.Parse()

	exitCode := 0
	if err := run(context.Background(), model, command, leaderModel, writerModel); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		exitCode = 1
	}
	os.Exit(exitCode)
}

func run(ctx context.Context, model, command, leaderModel, writerModel *string) error {
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt)
	defer cancel()

	claudeCmd := resolveCommand(*command)
	baseModel := resolveModel(*model)
	leaderM := pickModel(*leaderModel, baseModel)
	writerM := pickModel(*writerModel, baseModel)
	workDir := resolveWorkspace()

	fmt.Fprintf(os.Stderr, "=== Leader-Writer Delegation Demo ===\n")
	fmt.Fprintf(os.Stderr, "Leader model: %s\n", leaderM)
	fmt.Fprintf(os.Stderr, "Writer model: %s\n", writerM)
	fmt.Fprintf(os.Stderr, "Claude CLI:  %s\n", claudeCmd)
	fmt.Fprintf(os.Stderr, "Workspace:   %s\n", workDir)

	writerProfileDir, err := os.MkdirTemp("", "writer-profile-*")
	if err != nil {
		return fmt.Errorf("create writer profile temp dir: %w", err)
	}
	defer os.RemoveAll(writerProfileDir)

	leaderProfileDir, err := os.MkdirTemp("", "leader-profile-*")
	if err != nil {
		return fmt.Errorf("create leader profile temp dir: %w", err)
	}
	defer os.RemoveAll(leaderProfileDir)

	writerAddr, err := startWriterServer(ctx, claudeCmd, writerM, workDir, writerProfileDir)
	if err != nil {
		return fmt.Errorf("start writer: %w", err)
	}
	fmt.Fprintf(os.Stderr, "\nWriter A2A server at %s\n", writerAddr)

	registry, err := a2adelegation.NewRegistry(
		a2adelegation.RemoteAgentSpec{
			Key:                 "writer",
			DisplayName:         "Writer Agent",
			AgentCardURL:        fmt.Sprintf("http://%s%s", writerAddr, agentCardPath),
			AcceptedOutputModes: []string{"text"},
			PreferredTransports: []clienta2a.TransportProtocol{
				clienta2a.TransportJSONRPC,
			},
		},
	)
	if err != nil {
		return fmt.Errorf("create registry: %w", err)
	}

	bus := a2adelegation.NewEventBus(64)
	delegator := a2adelegation.NewDelegator(registry, bus)

	mcpSrv := a2adelegation.NewMCPServer(delegator, a2adelegation.MCPServerOptions{
		RunID:                               "demo-run",
		AllowUnauthenticatedLoopbackForTest: true,
	})
	mcpListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("mcp listen: %w", err)
	}
	defer mcpListener.Close()
	mcpHTTP := &http.Server{Handler: mcpSrv.Handler()}
	go func() {
		<-ctx.Done()
		mcpHTTP.Close()
	}()
	go mcpHTTP.Serve(mcpListener)

	fmt.Fprintf(os.Stderr, "\n=== Starting leader agent ===\n")
	leaderSDK := agentadaptor.New(
		agentadaptor.WithDefaultAgent(newClaudeBinding(claudeCmd, leaderM, workDir, leaderProfileDir,
			agentadaptor.WithDefaultMCP(agentadaptor.MCPConfig{
				Servers: []agentadaptor.MCPServerSpec{{
					Key:       "delegate_to_agent",
					Transport: agentadaptor.MCPTransportHTTP,
					URL:       fmt.Sprintf("http://%s", mcpListener.Addr().String()),
				}},
			}),
		)),
	)

	prompt := `请分两步完成以下任务：

第一步：使用 delegate_to_agent 工具委派给 "writer" agent，问他当前日期，然后汇报结果。

第二步：再次使用 delegate_to_agent 工具委派给 "writer" agent，问他我们刚刚聊了什么，然后汇报结果。注意这次调用的时候不要透露任何对话历史

注意：你自己不要做任何事，所有工作都通过 delegate_to_agent 委派给 writer。`

	fmt.Fprintf(os.Stderr, "Leader prompt:\n%s\n\n", prompt)
	fmt.Fprintln(os.Stderr, "--- leader stream (stdout) ---")

	start := time.Now()
	handle, err := leaderSDK.Start(ctx, prompt, leaderRunOptions()...)
	if err != nil {
		return fmt.Errorf("leader start: %w", err)
	}
	defer handle.Cancel(ctx)

	fmt.Fprintf(os.Stderr, "[leader run %s]\n", handle.RunID())

	go drainRunEvents(handle.Events())
	go drainDelegationEvents(ctx, bus, "demo-run")

	printer := &leaderStreamPrinter{}
	for ev := range handle.StreamEvents() {
		printer.handle(ev)
	}

	result, err := handle.Wait(ctx)
	duration := time.Since(start)
	if err != nil {
		return fmt.Errorf("leader wait: %w", err)
	}

	fmt.Fprintf(os.Stderr, "\n--- end leader stream (took %v) ---\n", duration)
	printJSON(map[string]any{
		"run_id":  result.RunID,
		"output":  result.Output,
		"summary": result.Summary,
		"failure": result.Failure,
	})

	return nil
}

// drainRunEvents 消费 leader 的运行期事件，避免阻塞内部 channel。
func drainRunEvents(events <-chan agentadaptor.RunEvent) {
	for range events {
	}
}

// drainDelegationEvents 把 writer 委派的流式输出打到 stderr，便于对照 leader。
func drainDelegationEvents(ctx context.Context, bus *a2adelegation.EventBus, runID string) {
	if bus == nil {
		return
	}
	ch := bus.SubscribeRun(ctx, runID)
	for ev := range ch {
		switch ev.Kind {
		case a2adelegation.DelegationStarted:
			fmt.Fprintf(os.Stderr, "\n[writer|%s] started agent=%s\n", ev.DelegationID, ev.AgentKey)
		case a2adelegation.DelegationTextStart:
			fmt.Fprintf(os.Stderr, "[writer|%s] ", ev.DelegationID)
		case a2adelegation.DelegationTextDelta:
			fmt.Fprintf(os.Stderr, "%s", ev.Delta)
		case a2adelegation.DelegationTextEnd:
			fmt.Fprintln(os.Stderr)
		case a2adelegation.DelegationFinished:
			fmt.Fprintf(os.Stderr, "\n[writer|%s] finished status=%s\n", ev.DelegationID, ev.Status)
		case a2adelegation.DelegationFailed:
			msg := ""
			if ev.Error != nil {
				msg = ev.Error.Message
			}
			fmt.Fprintf(os.Stderr, "\n[writer|%s] failed: %s\n", ev.DelegationID, msg)
		case a2adelegation.DelegationCancelled:
			fmt.Fprintf(os.Stderr, "\n[writer|%s] cancelled\n", ev.DelegationID)
		}
	}
}

// leaderStreamPrinter 流式打印 leader 输出，并汇总 tool 调用的输入/输出。
type leaderStreamPrinter struct {
	toolInputs      map[string]*strings.Builder
	toolStartArgs   map[string]map[string]any
	toolHasArgDelta map[string]bool
}

// handle 处理单条 leader 流式事件。
func (p *leaderStreamPrinter) handle(ev agentadaptor.StreamPayload) {
	switch ev.Kind {
	case agentadaptor.StreamTextContent:
		fmt.Print(ev.Delta)
	case agentadaptor.StreamReasoningContent:
		fmt.Fprint(os.Stderr, ev.Delta)
	case agentadaptor.StreamToolCallStart:
		if p.toolInputs == nil {
			p.toolInputs = map[string]*strings.Builder{}
		}
		if p.toolStartArgs == nil {
			p.toolStartArgs = map[string]map[string]any{}
		}
		if p.toolHasArgDelta == nil {
			p.toolHasArgDelta = map[string]bool{}
		}
		p.toolArgBuffer(ev.ToolCallID)
		fmt.Fprintf(os.Stderr, "\n[leader tool:%s id=%s]\n", ev.Name, ev.ToolCallID)
		if len(ev.Args) > 0 {
			p.toolStartArgs[ev.ToolCallID] = ev.Args
		}
	case agentadaptor.StreamToolCallArgs:
		if ev.Delta == "" {
			return
		}
		p.toolHasArgDelta[ev.ToolCallID] = true
		buf := p.toolArgBuffer(ev.ToolCallID)
		buf.WriteString(ev.Delta)
	case agentadaptor.StreamToolCallEnd:
		input := p.formatToolInput(ev.ToolCallID)
		if input != "" {
			fmt.Fprintf(os.Stderr, "[leader tool input] %s\n", input)
		}
		delete(p.toolInputs, ev.ToolCallID)
		delete(p.toolStartArgs, ev.ToolCallID)
		delete(p.toolHasArgDelta, ev.ToolCallID)
	case agentadaptor.StreamToolCallResult:
		fmt.Fprintf(os.Stderr, "[leader tool output id=%s]\n%s\n", ev.ToolCallID, formatStreamValue(ev.Result))
	case agentadaptor.StreamRunFinished:
		fmt.Println()
		if ev.Usage != nil {
			fmt.Fprintf(os.Stderr, "[leader usage in=%d out=%d cached=%d]\n",
				ev.Usage.InputTokens, ev.Usage.OutputTokens, ev.Usage.CachedInputTokens)
		}
	case agentadaptor.StreamRunError:
		msg := "unknown"
		if ev.Error != nil {
			msg = ev.Error.Message
		}
		fmt.Fprintln(os.Stderr, "[leader run error]:", msg)
	}
}

func (p *leaderStreamPrinter) toolArgBuffer(toolCallID string) *strings.Builder {
	if p.toolInputs == nil {
		p.toolInputs = map[string]*strings.Builder{}
	}
	buf, ok := p.toolInputs[toolCallID]
	if !ok {
		buf = &strings.Builder{}
		p.toolInputs[toolCallID] = buf
	}
	return buf
}

// formatToolInput 优先使用流式 args delta 拼接结果；无 delta 时回退到 start 事件里的 Args。
func (p *leaderStreamPrinter) formatToolInput(toolCallID string) string {
	if p.toolHasArgDelta[toolCallID] {
		if buf := p.toolInputs[toolCallID]; buf != nil && buf.Len() > 0 {
			return formatStreamJSON(buf.String())
		}
	}
	if args := p.toolStartArgs[toolCallID]; len(args) > 0 {
		if input, ok := args["input"]; ok {
			return formatStreamValue(input)
		}
		return formatStreamValue(args)
	}
	return ""
}

func formatStreamValue(v any) string {
	if v == nil {
		return ""
	}
	switch typed := v.(type) {
	case string:
		return formatStreamJSON(typed)
	case map[string]any:
		raw, err := json.MarshalIndent(typed, "", "  ")
		if err != nil {
			return fmt.Sprintf("%v", typed)
		}
		return string(raw)
	default:
		raw, err := json.MarshalIndent(typed, "", "  ")
		if err != nil {
			return fmt.Sprintf("%v", typed)
		}
		return string(raw)
	}
}

func formatStreamJSON(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	var parsed any
	if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
		return trimmed
	}
	out, err := json.MarshalIndent(parsed, "", "  ")
	if err != nil {
		return trimmed
	}
	return string(out)
}

// startWriterServer 启动 writer A2A HTTP server
func startWriterServer(ctx context.Context, claudeCmd, model, workDir, profileDir string) (string, error) {
	binding := newClaudeBinding(claudeCmd, model, workDir, profileDir)

	writerSDK := agentadaptor.New(
		agentadaptor.WithDefaultAgent(binding),
		agentadaptor.WithSessionStore(memory.NewSessionStore()),
	)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("writer listen: %w", err)
	}

	addr := listener.Addr().String()
	jsonRPCURL := fmt.Sprintf("http://%s%s", addr, jsonRPCPath)

	server := bridgea2a.NewServer(writerSDK.Default(), bridgea2a.ServerOptions{
		Session: bridgea2a.SessionByContextID("a2a-demo"),
		AgentCard: bridgea2a.AgentCard{
			Name:        "writer-agent",
			Description: "Claude Code agent that can write files to the local filesystem",
			Version:     "1.0.0",
			URL:         jsonRPCURL,
			Capabilities: bridgea2a.Capabilities{
				Streaming: bridgea2a.CapabilityDefault,
			},
			Skills: []bridgea2a.Skill{{
				ID:          "file-writer",
				Name:        "File Writer",
				Description: "Writes files to the local filesystem",
				Tags:        []string{"writer", "file"},
			}},
		},
		TaskLifecycle: bridgea2a.TaskLifecycleOptions{
			Ephemeral: &bridgea2a.EphemeralTaskStoreOptions{
				MaxTasks: 64,
				TTL:      30 * time.Minute,
			},
		},
	})

	mux := http.NewServeMux()
	mux.Handle(agentCardPath, server.AgentCardHandler())
	mux.Handle(jsonRPCPath, server.Handler())

	httpSrv := &http.Server{Handler: mux}
	go func() {
		<-ctx.Done()
		httpSrv.Close()
	}()
	go httpSrv.Serve(listener)

	return addr, nil
}

func newClaudeBinding(cliCommand, model, workDir, profileDir string, opts ...agentadaptor.AgentOption) agentadaptor.AgentBinding {
	agentOpts := []agentadaptor.AgentOption{
		agentadaptor.WithCloneProfile(profileDir, agentadaptor.CloneProfileOptions{
			IncludeSettings: true,
			AuthMode:        agentadaptor.CloneProfileAuthLink,
		}),
	}
	agentOpts = append(agentOpts, opts...)
	return claude.New(agentadaptor.ClaudeConfig{
		CommonConfig: agentadaptor.CommonConfig{
			Command: cliCommand,
			CWD:     workDir,
		},
		Model:          model,
		MaxTurnsPerRun: 50,
	}, agentOpts...)
}

func leaderRunOptions() []agentadaptor.RunOption {
	return []agentadaptor.RunOption{
		autoPolicy(),
		agentadaptor.WithStreaming(),
	}
}

func autoPolicy() agentadaptor.RunOption {
	return agentadaptor.WithRunPolicy(agentadaptor.RunPolicy{
		Isolation: agentadaptor.IsolationWorkspaceWrite,
		HumanDecision: agentadaptor.HumanDecisionPolicy{
			Permission: agentadaptor.HumanDecisionAutoApprove,
			PlanReview: agentadaptor.HumanDecisionAutoApprove,
			Question:   agentadaptor.QuestionAutoReject,
		},
	})
}

func resolveCommand(override string) string {
	if strings.TrimSpace(override) != "" {
		return strings.TrimSpace(override)
	}
	if env := os.Getenv("CLAUDE_COMMAND"); strings.TrimSpace(env) != "" {
		return strings.TrimSpace(env)
	}
	if path, err := exec.LookPath("claude"); err == nil {
		return path
	}
	for _, name := range []string{"claude.ps1", "claude.cmd", "claude", "trpc-claudecode.ps1", "trpc-claudecode.cmd", "trpc-claudecode"} {
		var cmd string
		if filepath.IsAbs(name) {
			cmd = name
		} else if path, err := exec.LookPath(name); err == nil {
			cmd = path
		}
		if cmd != "" {
			return cmd
		}
	}
	return "claude"
}

func resolveModel(override string) string {
	if strings.TrimSpace(override) != "" {
		return strings.TrimSpace(override)
	}
	if env := os.Getenv("CLAUDE_MODEL"); strings.TrimSpace(env) != "" {
		return strings.TrimSpace(env)
	}
	return "claude-sonnet-4"
}

func pickModel(override, fallback string) string {
	if strings.TrimSpace(override) != "" {
		return strings.TrimSpace(override)
	}
	return fallback
}

func resolveWorkspace() string {
	cwd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return cwd
}

func printJSON(value any) {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		fmt.Fprintf(os.Stderr, "encode JSON: %v\n", err)
	}
}

func init() {
	if runtime.GOOS == "windows" {
		fmt.Fprintf(os.Stderr, "Windows support requires WSL or Linux.\n")
		os.Exit(1)
	}
}
