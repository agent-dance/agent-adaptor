// a2a-local starts an in-process A2A server around a real local
// agent-adaptor Runner, then calls it through the A2A client.
//
// Usage:
//
//	go run ./examples/showcases/a2a-local -agent=codex
//	go run ./examples/showcases/a2a-local -agent=claude -prompt="Reply with one sentence"
//	go run ./examples/showcases/a2a-local -serve-only -addr=127.0.0.1:8080
//
// The example requires the selected local CLI in PATH and existing
// authentication.
package main

import (
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/examples/internal/exampleutil"
	"github.com/agent-dance/agent-adaptor/memory"
	bridgea2a "github.com/agent-dance/agent-adaptor/pkg/bridges/a2a"
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
		agentCfg.SkipGitRepoCheck = true
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
