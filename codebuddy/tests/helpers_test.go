package tests

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/codebuddy"
	"github.com/agent-dance/agent-adaptor/memory"
)

const liveModel = "glm-5.2-ioa"

func requireCodeBuddyCLI(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("codebuddy")
	if err != nil {
		t.Skip("codebuddy CLI not in PATH")
	}
	return path
}

func isolatedConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	source := strings.TrimSpace(os.Getenv("CODEBUDDY_CONFIG_DIR_SOURCE"))
	if source == "" {
		source = filepath.Join(home, ".codebuddy")
	}
	for _, name := range []string{".credentials.json", "credentials.json", "settings.json"} {
		data, err := os.ReadFile(filepath.Join(source, name))
		if err == nil {
			if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
				t.Fatalf("copy %s: %v", name, err)
			}
		}
	}
	return dir
}

func newHostSDK(t *testing.T, cwd, command string, configDir string, opts ...agentadaptor.AgentOption) agentadaptor.SDK {
	t.Helper()
	if command == "" {
		command = "codebuddy"
	}
	if configDir == "" {
		configDir = t.TempDir()
	}
	cfg := agentadaptor.CodeBuddyConfig{
		CommonConfig: agentadaptor.CommonConfig{
			CWD:     cwd,
			Command: command,
			Env: []agentadaptor.EnvBinding{
				{Name: "CODEBUDDY_CONFIG_DIR", Value: configDir},
			},
		},
		Model: liveModel,
	}
	return agentadaptor.New(
		agentadaptor.WithDefaultAgent(codebuddy.New(cfg, opts...)),
		agentadaptor.WithSessionStore(memory.NewSessionStore()),
	)
}

func buildMCPServer(t *testing.T) string {
	t.Helper()
	return buildTool(t, "codebuddy-driver-test-mcp", "./mcpserver")
}

func buildTool(t *testing.T, name, source string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), name)
	cmd := exec.Command("go", "build", "-o", out, source)
	cmd.Dir = "."
	if data, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", name, err, data)
	}
	return out
}

type observedRun struct {
	result agentadaptor.RunResult
	events []agentadaptor.RunEvent
	stream []agentadaptor.StreamPayload
}

func startAndObserve(t *testing.T, sdk agentadaptor.SDK, prompt string, opts ...agentadaptor.RunOption) observedRun {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	handle, err := sdk.Start(ctx, prompt, opts...)
	if err != nil {
		t.Fatalf("sdk.Start: %v", err)
	}

	var (
		mu     sync.Mutex
		events []agentadaptor.RunEvent
		stream []agentadaptor.StreamPayload
		wg     sync.WaitGroup
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		for event := range handle.Events() {
			mu.Lock()
			events = append(events, event)
			mu.Unlock()
		}
	}()
	go func() {
		defer wg.Done()
		for payload := range handle.StreamEvents() {
			mu.Lock()
			stream = append(stream, payload)
			mu.Unlock()
		}
	}()
	result, err := handle.Wait(ctx)
	if err != nil {
		t.Fatalf("run wait: %v", err)
	}
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	return observedRun{result: result, events: append([]agentadaptor.RunEvent(nil), events...), stream: append([]agentadaptor.StreamPayload(nil), stream...)}
}

func headlessPolicy() agentadaptor.RunPolicy {
	return agentadaptor.RunPolicy{HumanDecision: agentadaptor.HumanDecisionPolicy{
		Permission: agentadaptor.HumanDecisionAutoApprove,
		PlanReview: agentadaptor.HumanDecisionAutoApprove,
	}}
}
