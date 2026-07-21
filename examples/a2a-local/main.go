// a2a-local starts an in-process A2A server around a real local
// agent-adaptor Runner, then calls it through the A2A client.
//
// Usage:
//
//	go run ./examples/a2a-local -agent=codex
//	go run ./examples/a2a-local -agent=claude -prompt="Reply with one sentence"
//	go run ./examples/a2a-local -serve-only -addr=127.0.0.1:8080
//
// The example requires the selected local CLI in PATH and existing
// authentication.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/examples/internal/exampleutil"
	"github.com/agent-dance/agent-adaptor/memory"
	bridgea2a "github.com/agent-dance/agent-adaptor/pkg/bridges/a2a"
	clienta2a "github.com/agent-dance/agent-adaptor/pkg/clients/a2a"
)

const (
	agentCardPath = "/.well-known/agent-card.json"
	jsonRPCPath   = "/a2a"
)

func main() {
	agent := flag.String("agent", "", "Local CLI agent to use: "+exampleutil.SupportedAgents()+" (default codex, or AGENT_ADAPTOR_EXAMPLE_AGENT)")
	model := flag.String("model", "", "Model to use. Defaults by agent or CODEX_MODEL/CLAUDE_MODEL/CURSOR_MODEL.")
	command := flag.String("command", "", "Optional explicit local CLI command. Defaults by agent or CODEX_COMMAND/CLAUDE_COMMAND/CURSOR_COMMAND/PATH.")
	addr := flag.String("addr", "127.0.0.1:0", "HTTP listen address for the local A2A server")
	prompt := flag.String("prompt", "Reply exactly with: A2A demo OK", "Prompt to send through the A2A client")
	expect := flag.String("expect", "A2A demo OK", "Text expected in the final assistant output; empty disables output validation")
	contextID := flag.String("context", "a2a-demo/thread-1", "A2A contextId mapped to an SDK session key")
	workspace := flag.String("workspace", "", "Isolated workspace directory. Defaults to a temporary directory.")
	profile := flag.String("profile", "", "Isolated provider profile directory. Defaults to a temporary directory under the demo root.")
	keepWorkspace := flag.Bool("keep-workspace", false, "Keep the temporary workspace/profile after the example exits")
	timeout := flag.Duration("timeout", 3*time.Minute, "Maximum time for the demo; use 0 with -serve-only to run until interrupted")
	serveOnly := flag.Bool("serve-only", false, "Only serve the local A2A endpoint; do not run the built-in client demo")
	flag.Parse()

	ctx, cancel := demoContext(*timeout)
	defer cancel()

	isolation, err := newIsolation(*agent, *workspace, *profile, *keepWorkspace)
	exampleutil.Must(err, "create isolated A2A example workspace")
	defer isolation.Cleanup()

	agentCfg := exampleutil.ResolveLiveAgentConfig(*agent, *model, *command, isolation.WorkspaceDir)
	agentCfg = preferDirectlyExecutableCommand(agentCfg)
	if agentCfg.Agent == exampleutil.AgentCodex {
		agentCfg.ExtraArgs = append(agentCfg.ExtraArgs, "--skip-git-repo-check")
	}
	isolation.Agent = agentCfg.Agent

	listener, err := net.Listen("tcp", *addr)
	exampleutil.Must(err, "listen for local A2A server")
	defer listener.Close()

	baseURL := publicBaseURL(listener.Addr())
	jsonRPCURL := baseURL + jsonRPCPath
	agentCardURL := baseURL + agentCardPath

	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(exampleutil.NewLiveAgentBinding(
			agentCfg,
			agentadaptor.WithCloneProfile(isolation.ProfileDir, agentadaptor.CloneProfileOptions{
				IncludeSettings: true,
				AuthMode:        agentadaptor.CloneProfileAuthLink,
			}),
			agentadaptor.WithDefaultWorkspace(agentadaptor.SharedWorkspace{}),
			agentadaptor.WithDefaultIdentity(agentadaptor.AgentIdentity{
				ID:        "a2a-local-demo",
				TenantID:  "example",
				ProfileID: "isolated",
				Name:      "a2a-local",
			}),
			agentadaptor.WithDefaultMetadata("example", "a2a-local"),
			agentadaptor.WithDefaultMetadata("isolation", "temporary-workspace-and-profile"),
		)),
		agentadaptor.WithSessionStore(memory.NewSessionStore()),
	)

	server := bridgea2a.NewServer(sdk.Default(), bridgea2a.ServerOptions{
		AgentCard: bridgea2a.AgentCard{
			Name:        "agent-adaptor local A2A demo",
			Description: "Exposes the selected local agent-adaptor Runner through A2A JSON-RPC.",
			Version:     "1.0.0",
			URL:         jsonRPCURL,
			Provider: &bridgea2a.Provider{
				Organization: "agent-dance",
				URL:          "https://github.com/agent-dance/agent-adaptor",
			},
			Skills: []bridgea2a.Skill{{
				ID:          "local-agent",
				Name:        "Local agent",
				Description: "Runs a prompt through the configured local CLI agent.",
				Tags:        []string{"agent-adaptor", "a2a", agentCfg.Agent},
				Examples:    []string{"Reply exactly with: A2A demo OK"},
			}},
		},
		Session: bridgea2a.SessionByContextID("a2a-local"),
		RunOptions: []agentadaptor.RunOption{
			exampleutil.NonInteractiveRunOption(agentadaptor.IsolationReadOnly),
		},
		TaskLifecycle: bridgea2a.TaskLifecycleOptions{
			Ephemeral: &bridgea2a.EphemeralTaskStoreOptions{
				MaxTasks: 64,
				TTL:      30 * time.Minute,
			},
		},
	})

	httpServer := newHTTPServer(server)
	serveErr := make(chan error, 1)
	go func() {
		if err := httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()
	defer shutdownServer(httpServer)

	if *serveOnly {
		fmt.Fprintf(os.Stderr, "A2A agent card: %s\n", agentCardURL)
		fmt.Fprintf(os.Stderr, "A2A JSON-RPC:    %s\n", jsonRPCURL)
		fmt.Fprintf(os.Stderr, "Workspace:       %s\n", isolation.WorkspaceDir)
		fmt.Fprintf(os.Stderr, "Profile:         %s\n", isolation.ProfileDir)
		select {
		case <-ctx.Done():
		case err := <-serveErr:
			exampleutil.Must(err, "serve local A2A endpoint")
		}
		return
	}

	summary, err := runClientDemo(ctx, agentCardURL, *contextID, *prompt, *expect)
	exampleutil.Must(err, "run A2A client demo")

	select {
	case err := <-serveErr:
		exampleutil.Must(err, "serve local A2A endpoint")
	default:
	}

	exampleutil.PrintJSON(map[string]any{
		"example": "a2a-local",
		"agent":   exampleutil.LiveAgentSummary(agentCfg),
		"isolation": map[string]any{
			"workspace":         isolation.WorkspaceDir,
			"profile":           isolation.ProfileDir,
			"profile_mode":      "WithCloneProfile(IncludeSettings: true, AuthMode: CloneProfileAuthLink)",
			"cleanup_on_exit":   !isolation.Keep,
			"native_settings":   "copied into isolated profile",
			"native_mcp":        "not copied",
			"native_skills":     "not copied",
			"native_auth_files": "linked when present",
		},
		"server": map[string]any{
			"agent_card_url": agentCardURL,
			"jsonrpc_url":    jsonRPCURL,
		},
		"agent_card": summary.AgentCard,
		"request": map[string]any{
			"context_id": *contextID,
			"prompt":     *prompt,
			"expect":     *expect,
		},
		"stream": summary.Stream,
		"poll":   summary.Poll,
		"assistant_output": map[string]any{
			"chars":   len([]rune(summary.AssistantOutput)),
			"preview": preview(summary.AssistantOutput, 240),
		},
	})
}

type isolationConfig struct {
	Agent        string
	RootDir      string
	WorkspaceDir string
	ProfileDir   string
	Keep         bool
	cleanupRoot  bool
}

func newIsolation(agent, workspace, profile string, keep bool) (isolationConfig, error) {
	cfg := isolationConfig{Agent: exampleutil.ResolveLiveAgent(agent), Keep: keep}
	if strings.TrimSpace(workspace) == "" || strings.TrimSpace(profile) == "" {
		root, err := os.MkdirTemp("", "agent-adaptor-a2a-local-*")
		if err != nil {
			return isolationConfig{}, err
		}
		cfg.RootDir = root
		cfg.cleanupRoot = !keep
	}

	workspaceDir := strings.TrimSpace(workspace)
	if workspaceDir == "" {
		workspaceDir = filepath.Join(cfg.RootDir, "workspace")
	}
	profileDir := strings.TrimSpace(profile)
	if profileDir == "" {
		profileDir = filepath.Join(cfg.RootDir, cfg.Agent+"-profile")
	}

	var err error
	cfg.WorkspaceDir, err = ensureDir(workspaceDir)
	if err != nil {
		cfg.Cleanup()
		return isolationConfig{}, err
	}
	cfg.ProfileDir, err = ensureDir(profileDir)
	if err != nil {
		cfg.Cleanup()
		return isolationConfig{}, err
	}
	return cfg, nil
}

func (cfg isolationConfig) Cleanup() {
	if cfg.cleanupRoot && cfg.RootDir != "" {
		_ = os.RemoveAll(cfg.RootDir)
	}
}

func ensureDir(path string) (string, error) {
	cleaned := filepath.Clean(path)
	absolute, err := filepath.Abs(cleaned)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(absolute, 0o755); err != nil {
		return "", err
	}
	return absolute, nil
}

func preferDirectlyExecutableCommand(cfg exampleutil.LiveAgentConfig) exampleutil.LiveAgentConfig {
	if runtime.GOOS != "windows" || !strings.EqualFold(filepath.Ext(cfg.Command), ".ps1") {
		return cfg
	}
	base := strings.TrimSuffix(cfg.Command, filepath.Ext(cfg.Command))
	for _, candidate := range []string{base + ".cmd", base + ".exe"} {
		if exampleutil.ProbeAgentCommand(candidate) {
			cfg.Command = candidate
			cfg.CommandNote += " Streaming examples execute the provider command directly; using the Windows executable shim instead of the PowerShell shim."
			return cfg
		}
	}
	return cfg
}

type demoSummary struct {
	AgentCard       map[string]any
	Stream          streamSummary
	Poll            map[string]any
	AssistantOutput string
}

type streamSummary struct {
	TaskID             string   `json:"task_id"`
	ContextID          string   `json:"context_id"`
	States             []string `json:"states"`
	ArtifactChunks     int      `json:"artifact_chunks"`
	ResultArtifactSeen bool     `json:"result_artifact_seen"`
	TerminalState      string   `json:"terminal_state"`
	TerminalMessage    string   `json:"terminal_message,omitempty"`
	RecoveredState     bool     `json:"recovered_state"`
}

func runClientDemo(ctx context.Context, agentCardURL, contextID, prompt, expect string) (demoSummary, error) {
	client := clienta2a.New(clienta2a.Options{
		AgentCardURL:        agentCardURL,
		PreferredTransports: []clienta2a.TransportProtocol{clienta2a.TransportJSONRPC},
	})
	defer client.Close()

	card, err := client.AgentCard(ctx)
	if err != nil {
		return demoSummary{}, err
	}
	if !card.Capabilities.Streaming {
		return demoSummary{}, fmt.Errorf("expected demo agent card to advertise streaming")
	}

	stream, err := client.SendStream(ctx, clienta2a.SendRequest{
		ContextID:           contextID,
		AcceptedOutputModes: card.DefaultOutputModes,
		Message: clienta2a.Message{
			Role: "user",
			Parts: []clienta2a.Part{{
				Kind:      clienta2a.PartText,
				Text:      prompt,
				MediaType: "text/plain",
			}},
		},
		Metadata: map[string]any{
			"example": "a2a-local",
		},
	})
	if err != nil {
		return demoSummary{}, err
	}
	defer stream.Close()

	streamOut, assistantOutput, err := consumeStream(stream)
	if err != nil {
		return demoSummary{}, err
	}
	if streamOut.TaskID == "" {
		return demoSummary{}, fmt.Errorf("A2A stream did not return a task id")
	}
	if streamOut.TerminalState != string(clienta2a.TaskStateCompleted) {
		return demoSummary{}, fmt.Errorf("A2A task ended in %s: %s", streamOut.TerminalState, defaultString(streamOut.TerminalMessage, "no terminal message"))
	}

	historyLength := 4
	task, err := client.GetTask(ctx, clienta2a.GetTaskRequest{
		TaskID:        streamOut.TaskID,
		HistoryLength: &historyLength,
	})
	if err != nil {
		return demoSummary{}, err
	}
	if task.Status.State != clienta2a.TaskStateCompleted {
		return demoSummary{}, fmt.Errorf("GetTask returned state %s: %s", task.Status.State, statusMessage(task.Status))
	}
	if strings.TrimSpace(assistantOutput) == "" && task.Status.Message != nil {
		assistantOutput = partsText(task.Status.Message.Parts)
	}
	if expected := strings.TrimSpace(expect); expected != "" && !strings.Contains(assistantOutput, expected) {
		return demoSummary{}, fmt.Errorf("A2A task completed but assistant output did not contain %q: %q", expected, preview(assistantOutput, 240))
	}

	return demoSummary{
		AgentCard: map[string]any{
			"name":                 card.Name,
			"url":                  card.URL,
			"fingerprint":          card.Fingerprint,
			"streaming":            card.Capabilities.Streaming,
			"default_output_modes": card.DefaultOutputModes,
			"skills":               len(card.Skills),
		},
		Stream:          streamOut,
		AssistantOutput: assistantOutput,
		Poll: map[string]any{
			"task_id":              task.ID,
			"context_id":           task.ContextID,
			"state":                task.Status.State,
			"history_messages":     len(task.Messages),
			"artifacts":            len(task.Artifacts),
			"result_artifact_seen": taskHasArtifact(task, bridgea2a.ArtifactAgentAdaptorResult),
		},
	}, nil
}

func consumeStream(stream *clienta2a.Stream) (streamSummary, string, error) {
	var summary streamSummary
	var output strings.Builder

	for {
		event, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return summary, "", err
		}

		if event.TaskID != "" {
			summary.TaskID = event.TaskID
		}
		if event.ContextID != "" {
			summary.ContextID = event.ContextID
		}

		switch event.Kind {
		case clienta2a.EventTask:
			if event.Task != nil {
				rememberState(&summary, event.Task.Status.State)
			}
		case clienta2a.EventStatus:
			if event.Status != nil {
				rememberState(&summary, event.Status.State)
				if event.Status.Message != nil {
					for _, part := range event.Status.Message.Parts {
						if part.Kind != clienta2a.PartData {
							continue
						}
						payload, matched, err := bridgea2a.DecodeAdapterStreamStatus(part.Data)
						if err == nil && matched && payload.Kind == agentadaptor.StreamTextContent {
							output.WriteString(payload.Delta)
						}
					}
				}
			}
		case clienta2a.EventArtifact:
			summary.ArtifactChunks++
			if event.Artifact == nil {
				continue
			}
			if event.Artifact.Name == bridgea2a.ArtifactAgentAdaptorResult {
				summary.ResultArtifactSeen = true
			}
		case clienta2a.EventTerminal:
			applyTerminal(&summary, &output, event)
			return summary, output.String(), nil
		}
	}

	return summary, output.String(), nil
}

func applyTerminal(summary *streamSummary, output *strings.Builder, event clienta2a.Event) {
	summary.RecoveredState = event.RecoveredState
	if event.Task != nil {
		rememberState(summary, event.Task.Status.State)
		summary.TerminalState = string(event.Task.Status.State)
		if output.Len() == 0 && event.Task.Status.Message != nil {
			output.WriteString(partsText(event.Task.Status.Message.Parts))
		}
		summary.TerminalMessage = statusMessage(event.Task.Status)
		return
	}
	if event.Status != nil {
		rememberState(summary, event.Status.State)
		summary.TerminalState = string(event.Status.State)
		if output.Len() == 0 && event.Status.Message != nil {
			output.WriteString(partsText(event.Status.Message.Parts))
		}
		summary.TerminalMessage = statusMessage(*event.Status)
		return
	}
	if event.Message != nil {
		summary.TerminalState = string(clienta2a.TaskStateCompleted)
		if output.Len() == 0 {
			output.WriteString(partsText(event.Message.Parts))
		}
		summary.TerminalMessage = partsText(event.Message.Parts)
	}
}

func rememberState(summary *streamSummary, state clienta2a.TaskState) {
	if state == "" {
		return
	}
	value := string(state)
	for _, existing := range summary.States {
		if existing == value {
			return
		}
	}
	summary.States = append(summary.States, value)
}

func taskHasArtifact(task clienta2a.Task, name string) bool {
	for _, artifact := range task.Artifacts {
		if artifact.Name == name {
			return true
		}
	}
	return false
}

func partsText(parts []clienta2a.Part) string {
	var out strings.Builder
	for _, part := range parts {
		if part.Kind == clienta2a.PartText {
			out.WriteString(part.Text)
		}
	}
	return out.String()
}

func statusMessage(status clienta2a.TaskStatus) string {
	if status.Message == nil {
		return ""
	}
	return partsText(status.Message.Parts)
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func newHTTPServer(server *bridgea2a.Server) *http.Server {
	mux := http.NewServeMux()
	mux.Handle(agentCardPath, server.AgentCardHandler())
	mux.Handle(jsonRPCPath, server.Handler())
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "agent-adaptor A2A demo\nagent card: %s\njson-rpc: %s\n", agentCardPath, jsonRPCPath)
	})
	return &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
}

func shutdownServer(server *http.Server) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = server.Shutdown(ctx)
}

func demoContext(timeout time.Duration) (context.Context, context.CancelFunc) {
	base, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	if timeout <= 0 {
		return base, stop
	}
	ctx, cancel := context.WithTimeout(base, timeout)
	return ctx, func() {
		cancel()
		stop()
	}
}

func publicBaseURL(addr net.Addr) string {
	host, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return "http://" + addr.String()
	}
	switch host {
	case "", "::", "0.0.0.0":
		host = "localhost"
	}
	return (&url.URL{Scheme: "http", Host: net.JoinHostPort(host, port)}).String()
}

func preview(value string, maxRunes int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	if maxRunes <= 3 {
		return string(runes[:maxRunes])
	}
	return string(runes[:maxRunes-3]) + "..."
}
