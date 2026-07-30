package e2e_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/cursor"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/memory"
	"github.com/agent-dance/agent-adaptor/profile"
	"github.com/agent-dance/agent-adaptor/tool"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	toolE2EHelperEnv      = "AGENT_ADAPTOR_TOOL_E2E_HELPER"
	toolE2EObservationEnv = "AGENT_ADAPTOR_TOOL_E2E_OBSERVATIONS"
	hostedToolMCPKey      = "agent-adaptor-tools"
)

// TestMain turns this test binary into the real child process used by the
// hermetic provider fixture. Cursor's Driver still owns process launch,
// provider-profile materialization, protocol parsing, Events, Result, and
// checkpoint creation; the child only performs the work a provider CLI would.
func TestMain(m *testing.M) {
	if os.Getenv(toolE2EHelperEnv) == "1" {
		os.Exit(runToolProviderFixture())
	}
	os.Exit(m.Run())
}

type e2eToolInput struct {
	Value string `json:"value" jsonschema:"required"`
}

type e2eToolOutput struct {
	Value string `json:"value"`
}

type toolE2EObservation struct {
	Endpoint          string   `json:"endpoint"`
	TokenHash         string   `json:"token_hash"`
	Tools             []string `json:"tools"`
	Result            string   `json:"result"`
	Unauthorized      int      `json:"unauthorized_status"`
	ResumeRequested   bool     `json:"resume_requested"`
	BearerEnvironment string   `json:"bearer_environment"`
	ProfileDir        string   `json:"profile_dir"`
}

func TestHostDefinedToolsEndToEndThroughRealProviderProcess(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	providerProfile := filepath.Join(root, "cursor-profile")
	observationsPath := filepath.Join(root, "observations.jsonl")

	echo := tool.Define(
		"host_echo",
		"Echo a value through a host-defined Go tool.",
		func(_ context.Context, input e2eToolInput) (e2eToolOutput, error) {
			return e2eToolOutput{Value: "host:" + input.Value}, nil
		},
		tool.ReadOnly(),
		tool.Idempotent(),
		tool.Revision("host_echo/v1"),
	)
	store := memory.NewStore()
	newAgent := func() *adaptor.Agent {
		return adaptor.New(
			cursor.Driver(cursor.Config{CommonConfig: cursor.CommonConfig{
				Command: executable,
				CWD:     root,
				Env: []driver.EnvBinding{
					{Name: toolE2EHelperEnv, Value: "1"},
					{Name: toolE2EObservationEnv, Value: observationsPath},
				},
			}}),
			adaptor.WithProfile(profile.Dedicated(providerProfile)),
			adaptor.WithThreadStore(store),
			adaptor.WithTools(echo),
		)
	}
	agent := newAgent()
	closed := false
	t.Cleanup(func() {
		if !closed {
			_ = agent.Close(context.Background())
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	thread := agent.Thread("host-tools-e2e")
	first, err := thread.Run(ctx, "first")
	if err != nil {
		t.Fatalf("first Thread run: %v", err)
	}
	second, err := thread.Run(ctx, "second")
	if err != nil {
		t.Fatalf("second Thread run: %v", err)
	}
	if first.Text != "host:first" || second.Text != "host:second" {
		t.Fatalf("normal Result pipeline text = %q, %q", first.Text, second.Text)
	}
	if !hasToolTranscript(first) || !hasToolTranscript(second) {
		t.Fatal("provider tool call did not reach the normal Transcript pipeline")
	}

	observations := readToolE2EObservations(t, observationsPath)
	if len(observations) != 2 {
		t.Fatalf("provider observations = %d, want 2", len(observations))
	}
	for index, observation := range observations {
		if observation.Unauthorized != http.StatusUnauthorized {
			t.Errorf("turn %d unauthorized status = %d", index+1, observation.Unauthorized)
		}
		if len(observation.Tools) != 1 || observation.Tools[0] != "host_echo" {
			t.Errorf("turn %d tools/list = %v", index+1, observation.Tools)
		}
		if observation.BearerEnvironment == "" {
			t.Errorf("turn %d did not resolve bearer environment reference", index+1)
		}
	}
	if observations[0].Endpoint != observations[1].Endpoint ||
		observations[0].TokenHash != observations[1].TokenHash {
		t.Fatal("Agent-owned tool runtime identity changed between Thread turns")
	}
	if observations[0].ResumeRequested || !observations[1].ResumeRequested {
		t.Fatalf("provider resume flags = %v, %v", observations[0].ResumeRequested, observations[1].ResumeRequested)
	}
	if observations[0].ProfileDir == providerProfile || observations[0].ProfileDir != observations[1].ProfileDir {
		t.Fatalf("isolated profile dirs = %q, %q; source = %q", observations[0].ProfileDir, observations[1].ProfileDir, providerProfile)
	}

	if err := agent.Close(ctx); err != nil {
		t.Fatalf("Agent.Close: %v", err)
	}
	closed = true
	if _, err := os.Stat(observations[0].ProfileDir); !os.IsNotExist(err) {
		t.Fatalf("Agent.Close left isolated Tool profile %q: %v", observations[0].ProfileDir, err)
	}
	if _, err := os.Stat(filepath.Join(providerProfile, "mcp.json")); !os.IsNotExist(err) {
		t.Fatalf("source profile was polluted with hosted Tool MCP config: %v", err)
	}
	requestCtx, requestCancel := context.WithTimeout(context.Background(), time.Second)
	request, _ := http.NewRequestWithContext(requestCtx, http.MethodPost, observations[0].Endpoint, strings.NewReader(`{}`))
	if response, requestErr := http.DefaultClient.Do(request); requestErr == nil {
		response.Body.Close()
		t.Fatalf("Agent.Close left the final Tool endpoint reachable: status %d", response.StatusCode)
	}
	requestCancel()

	oldURL, err := url.Parse(observations[0].Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	portGuard, err := net.Listen("tcp", oldURL.Host)
	if err != nil {
		t.Fatalf("reserve previous Tool endpoint %q: %v", oldURL.Host, err)
	}
	defer portGuard.Close()

	secondAgent := newAgent()
	secondClosed := false
	t.Cleanup(func() {
		if !secondClosed {
			_ = secondAgent.Close(context.Background())
		}
	})
	third, err := secondAgent.Thread("host-tools-e2e").Run(ctx, "third")
	if err != nil {
		t.Fatalf("cross-Agent Thread resume: %v", err)
	}
	if third.Text != "host:third" || !hasToolTranscript(third) {
		t.Fatalf("cross-Agent Result = %#v", third)
	}
	observations = readToolE2EObservations(t, observationsPath)
	if len(observations) != 3 {
		t.Fatalf("provider observations after restart = %d, want 3", len(observations))
	}
	restarted := observations[2]
	if !restarted.ResumeRequested {
		t.Fatal("new Agent did not resume the existing Thread checkpoint")
	}
	if restarted.Endpoint == observations[0].Endpoint || restarted.TokenHash == observations[0].TokenHash {
		t.Fatalf("restarted Tool runtime identity was not renewed: before=%#v after=%#v", observations[0], restarted)
	}
	if restarted.ProfileDir == observations[0].ProfileDir || restarted.ProfileDir == providerProfile {
		t.Fatalf("restarted isolated profile = %q", restarted.ProfileDir)
	}
	if err := secondAgent.Close(ctx); err != nil {
		t.Fatalf("second Agent.Close: %v", err)
	}
	secondClosed = true
	if _, err := os.Stat(restarted.ProfileDir); !os.IsNotExist(err) {
		t.Fatalf("second Agent.Close left isolated Tool profile %q: %v", restarted.ProfileDir, err)
	}
}

func hasToolTranscript(result *adaptor.Result) bool {
	if result == nil {
		return false
	}
	for _, item := range result.Transcript() {
		if item.Kind == driver.TranscriptToolCall || item.Kind == driver.TranscriptToolResult {
			return true
		}
	}
	return false
}

func runToolProviderFixture() int {
	observationPath := os.Getenv(toolE2EObservationEnv)
	profileDir := os.Getenv("CURSOR_HOME")
	if observationPath == "" || profileDir == "" {
		return 20
	}
	entry, err := readHostedToolMCPEntry(filepath.Join(profileDir, "mcp.json"))
	if err != nil {
		return 21
	}
	authorization := entry.Headers["Authorization"]
	envName := strings.TrimSuffix(strings.TrimPrefix(authorization, "Bearer ${env:"), "}")
	if envName == authorization || envName == "" {
		return 22
	}
	token := os.Getenv(envName)
	if token == "" {
		return 23
	}

	unauthorized := 0
	request, _ := http.NewRequest(http.MethodPost, entry.URL, strings.NewReader(`{}`))
	if response, requestErr := http.DefaultClient.Do(request); requestErr == nil {
		unauthorized = response.StatusCode
		response.Body.Close()
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "agent-adaptor-e2e-provider", Version: "1"}, nil)
	httpClient := &http.Client{Transport: bearerRoundTripper{token: token, base: http.DefaultTransport}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             entry.URL,
		HTTPClient:           httpClient,
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		return 24
	}
	defer session.Close()
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		return 25
	}
	toolNames := make([]string, 0, len(listed.Tools))
	for _, definition := range listed.Tools {
		toolNames = append(toolNames, definition.Name)
	}
	sort.Strings(toolNames)

	promptBytes, err := io.ReadAll(os.Stdin)
	if err != nil {
		return 26
	}
	// Drivers may prepend their normal runtime-service context to the user
	// prompt. The fixture behaves like a provider and selects the final user
	// instruction rather than assuming a transport-specific prompt shape.
	prompt := finalNonEmptyLine(string(promptBytes))
	called, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "host_echo",
		Arguments: map[string]any{"value": prompt},
	})
	if err != nil || called.IsError {
		return 27
	}
	structured, ok := called.StructuredContent.(map[string]any)
	if !ok {
		return 28
	}
	result, _ := structured["value"].(string)
	if result == "" {
		return 29
	}
	tokenDigest := sha256.Sum256([]byte(token))
	observation := toolE2EObservation{
		Endpoint:          entry.URL,
		TokenHash:         hex.EncodeToString(tokenDigest[:]),
		Tools:             toolNames,
		Result:            result,
		Unauthorized:      unauthorized,
		ResumeRequested:   containsArgument(os.Args[1:], "--resume"),
		BearerEnvironment: envName,
		ProfileDir:        profileDir,
	}
	if err := appendToolE2EObservation(observationPath, observation); err != nil {
		return 30
	}

	emitCursorFrame(map[string]any{
		"type": "system", "subtype": "init", "session_id": "host-tools-e2e-session",
	})
	emitCursorFrame(map[string]any{
		"type": "tool_call", "subtype": "started", "session_id": "host-tools-e2e-session", "call_id": "call-1",
		"tool_call": map[string]any{"hostedToolCall": map[string]any{"args": map[string]any{"value": prompt}}},
	})
	emitCursorFrame(map[string]any{
		"type": "tool_call", "subtype": "completed", "session_id": "host-tools-e2e-session", "call_id": "call-1",
		"tool_call": map[string]any{"hostedToolCall": map[string]any{
			"args":   map[string]any{"value": prompt},
			"result": map[string]any{"success": map[string]any{"content": result}},
		}},
	})
	emitCursorFrame(map[string]any{
		"type": "assistant", "session_id": "host-tools-e2e-session",
		"message": map[string]any{"content": []any{map[string]any{"type": "text", "text": result}}},
	})
	emitCursorFrame(map[string]any{
		"type": "result", "subtype": "success", "is_error": false,
		"result": result, "session_id": "host-tools-e2e-session",
	})
	return 0
}

type cursorMCPEntry struct {
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
}

func readHostedToolMCPEntry(path string) (cursorMCPEntry, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return cursorMCPEntry{}, err
	}
	var config struct {
		Servers map[string]cursorMCPEntry `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		return cursorMCPEntry{}, err
	}
	entry, ok := config.Servers[hostedToolMCPKey]
	if !ok || entry.URL == "" {
		return cursorMCPEntry{}, fmt.Errorf("hosted Tool MCP entry missing")
	}
	return entry, nil
}

type bearerRoundTripper struct {
	token string
	base  http.RoundTripper
}

func (r bearerRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.Header = request.Header.Clone()
	clone.Header.Set("Authorization", "Bearer "+r.token)
	return r.base.RoundTrip(clone)
}

func emitCursorFrame(value any) {
	_ = json.NewEncoder(os.Stdout).Encode(value)
}

func containsArgument(arguments []string, wanted string) bool {
	for _, argument := range arguments {
		if argument == wanted {
			return true
		}
	}
	return false
}

func finalNonEmptyLine(value string) string {
	lines := strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		if line := strings.TrimSpace(lines[index]); line != "" {
			return line
		}
	}
	return ""
}

func appendToolE2EObservation(path string, observation toolE2EObservation) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	return json.NewEncoder(file).Encode(observation)
}

func readToolE2EObservations(t *testing.T, path string) []toolE2EObservation {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var observations []toolE2EObservation
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	for {
		var observation toolE2EObservation
		if err := decoder.Decode(&observation); err != nil {
			if err == io.EOF {
				break
			}
			t.Fatal(err)
		}
		observations = append(observations, observation)
	}
	return observations
}
