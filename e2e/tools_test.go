package e2e_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/cursor"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/memory"
	"github.com/agent-dance/agent-adaptor/profile"
	"github.com/agent-dance/agent-adaptor/threadstore"
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
		// Record entry before any provider work, including unsuccessful starts.
		file, err := os.OpenFile(os.Getenv(toolE2EObservationEnv)+".spawns", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			os.Exit(40)
		}
		_, writeErr := file.WriteString("spawn\n")
		if err := errors.Join(writeErr, file.Close()); err != nil {
			os.Exit(41)
		}
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
	Endpoint                 string   `json:"endpoint"`
	TokenHash                string   `json:"token_hash"`
	Tools                    []string `json:"tools"`
	Result                   string   `json:"result"`
	Unauthorized             int      `json:"unauthorized_status"`
	SessionID                string   `json:"session_id"`
	ResumeRequested          bool     `json:"resume_requested"`
	BearerEnvironment        string   `json:"bearer_environment"`
	ProfileDir               string   `json:"profile_dir"`
	SessionTurnsBefore       int      `json:"session_turns_before"`
	PreviousCredentialStatus int      `json:"previous_credential_status"`
}

func TestHostDefinedToolsEndToEndThroughRealProviderProcess(t *testing.T) {
	for _, persistent := range []bool{true, false} {
		name := "temporary_clone"
		if persistent {
			name = "dedicated"
		}
		t.Run(name, func(t *testing.T) {
			testHostDefinedToolsProfileLifecycle(t, persistent)
		})
	}
}

func testHostDefinedToolsProfileLifecycle(t *testing.T, persistent bool) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	providerProfile := filepath.Join(root, "cursor-profile")
	observationsPath := filepath.Join(root, "observations.jsonl")
	if err := os.Mkdir(providerProfile, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(providerProfile, "source-marker"), []byte("host-owned source"), 0o600); err != nil {
		t.Fatal(err)
	}
	selection := profile.Dedicated(providerProfile)
	if !persistent {
		// CloneFrom exercises the temporary hosted selection using only test
		// directories. It never discovers the user's native Cursor profile.
		selection = profile.CloneFrom(providerProfile, filepath.Join(root, "selected-profile"))
	}

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
	store := &toolE2EStore{Store: memory.NewStore()}
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
			adaptor.WithProfile(selection),
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
	assertToolE2EResult(t, first, "first")
	assertToolE2EResult(t, second, "second")
	healthy, err := store.Resolve(ctx, threadstore.Query{Key: "host-tools-e2e"})
	if err != nil || healthy == nil || healthy.State == nil {
		t.Fatalf("healthy stored record: %v", err)
	}
	healthyCheckpoint, err := thread.Checkpoint(ctx)
	if err != nil || !healthyCheckpoint.Valid || !reflect.DeepEqual(healthy.State, healthyCheckpoint.State) {
		t.Fatalf("healthy checkpoint: %v", err)
	}

	observations := readToolE2EObservations(t, observationsPath)
	if len(observations) != 2 {
		t.Fatalf("provider observations = %d, want 2", len(observations))
	}
	for index, observation := range observations {
		assertToolE2EObservation(t, index+1, observation, []string{"host:first", "host:second"}[index])
		if observation.SessionTurnsBefore != index {
			t.Errorf("turn %d read %d prior session turns, want %d", index+1, observation.SessionTurnsBefore, index)
		}
	}
	if observations[0].Endpoint != observations[1].Endpoint ||
		observations[0].TokenHash != observations[1].TokenHash {
		t.Fatal("Agent-owned tool runtime identity changed between Thread turns")
	}
	if observations[0].SessionID == "" || observations[0].SessionID != observations[1].SessionID || observations[1].SessionID != healthy.State.ResumeID {
		t.Fatal("same-Agent turns did not retain provider session identity")
	}
	assertToolE2ESpawns(t, observationsPath, 2)
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
	assertClosedToolE2EProfile(t, observations[0].ProfileDir, persistent, "first\nsecond\n")
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
	if !persistent {
		// A deleted temporary profile cannot recover its provider files from a
		// stored resume ID. Prove rejection before any replacement child starts.
		rejected, resumeErr := secondAgent.Thread("host-tools-e2e", adaptor.ResumeOnly()).Run(ctx, "must not dispatch")
		if rejected != nil || !errors.Is(resumeErr, adaptor.ErrResumeRejected) {
			t.Fatalf("temporary ResumeOnly must reject the missing profile before spawn: %v", resumeErr)
		}
		assertToolE2ESpawns(t, observationsPath, 2)
		if got := len(readToolE2EObservations(t, observationsPath)); got != 2 {
			t.Fatalf("ResumeOnly delivered provider work: %d observations", got)
		}
		after, err := store.Resolve(ctx, threadstore.Query{Key: "host-tools-e2e"})
		if err != nil || !reflect.DeepEqual(healthy, after) {
			t.Fatalf("ResumeOnly changed the healthy record: %v", err)
		}
		checkpoint, err := secondAgent.Thread("host-tools-e2e").Checkpoint(ctx)
		if err != nil || !reflect.DeepEqual(healthyCheckpoint, checkpoint) {
			t.Fatalf("ResumeOnly changed the healthy checkpoint: %v", err)
		}
		if len(store.committed()) != 2 {
			t.Fatal("ResumeOnly finalized a record")
		}
	}
	third, err := secondAgent.Thread("host-tools-e2e").Run(ctx, "third")
	if err != nil {
		t.Fatalf("cross-Agent Thread resume: %v", err)
	}
	assertToolE2EResult(t, third, "third")
	assertToolE2ESpawns(t, observationsPath, 3)
	observations = readToolE2EObservations(t, observationsPath)
	if len(observations) != 3 {
		t.Fatalf("provider observations after restart = %d, want 3", len(observations))
	}
	restarted := observations[2]
	if restarted.ResumeRequested != persistent {
		t.Fatalf("cross-Agent resume = %v, want %v", restarted.ResumeRequested, persistent)
	}
	current, err := store.Resolve(ctx, threadstore.Query{Key: "host-tools-e2e"})
	checkpoint, checkpointErr := secondAgent.Thread("host-tools-e2e").Checkpoint(ctx)
	if err != nil || checkpointErr != nil || current == nil || current.Status != threadstore.StatusActive || current.State == nil || !checkpoint.Valid || !reflect.DeepEqual(current.State, checkpoint.State) || current.State.ResumeID != restarted.SessionID {
		t.Fatalf("third checkpoint not persisted: %v / %v", err, checkpointErr)
	}
	commits := store.committed()
	if len(commits) != 3 {
		t.Fatalf("successful turns finalized %d times, want 3", len(commits))
	}
	last := commits[2]
	if !reflect.DeepEqual(last.before, healthy) || !reflect.DeepEqual(last.after, current) {
		t.Fatal("old healthy record changed before the single final commit")
	}
	if persistent {
		if restarted.SessionID != healthy.State.ResumeID || current.ID != healthy.ID || last.archiveOld {
			t.Fatal("Dedicated did not resume the same healthy session/record")
		}
	} else {
		if restarted.SessionID == healthy.State.ResumeID || current.ID == healthy.ID || !last.archiveOld || !last.rebindActive || last.previousID != healthy.ID || last.archived == nil || last.archived.Status != threadstore.StatusArchived || !reflect.DeepEqual(last.archived.State, healthy.State) {
			t.Fatal("temporary fresh checkpoint did not atomically save/archive/rebind the old healthy record")
		}
	}
	if restarted.Endpoint == observations[0].Endpoint || restarted.TokenHash == observations[0].TokenHash {
		t.Fatalf("restarted Tool runtime identity was not renewed: before=%#v after=%#v", observations[0], restarted)
	}
	assertToolE2EObservation(t, 3, restarted, "host:third")
	if restarted.BearerEnvironment == observations[0].BearerEnvironment {
		t.Fatal("restarted Tool bearer environment carrier was not renewed")
	}
	if restarted.PreviousCredentialStatus != http.StatusUnauthorized {
		t.Fatalf("new Tool endpoint accepted the old bearer: status %d", restarted.PreviousCredentialStatus)
	}
	if restarted.ProfileDir == providerProfile || (restarted.ProfileDir == observations[0].ProfileDir) != persistent {
		t.Fatalf("restarted isolated profile = %q, prior = %q, persistent = %v", restarted.ProfileDir, observations[0].ProfileDir, persistent)
	}
	wantPriorTurns := 0
	if persistent {
		wantPriorTurns = 2
	}
	if restarted.SessionTurnsBefore != wantPriorTurns {
		t.Fatalf("restarted provider read %d prior session turns, want %d", restarted.SessionTurnsBefore, wantPriorTurns)
	}
	if err := secondAgent.Close(ctx); err != nil {
		t.Fatalf("second Agent.Close: %v", err)
	}
	secondClosed = true
	assertClosedToolE2EProfile(t, restarted.ProfileDir, persistent, "first\nsecond\nthird\n")
	entries, err := os.ReadDir(providerProfile)
	if err != nil || len(entries) != 1 || entries[0].Name() != "source-marker" {
		t.Fatalf("source profile was polluted: entries=%v, err=%v", entries, err)
	}
	marker, err := os.ReadFile(filepath.Join(providerProfile, "source-marker"))
	if err != nil || string(marker) != "host-owned source" {
		t.Fatalf("source marker changed: %q, %v", marker, err)
	}
}

func assertToolE2EObservation(t *testing.T, turn int, observation toolE2EObservation, wantResult string) {
	t.Helper()
	if observation.Unauthorized != http.StatusUnauthorized {
		t.Errorf("turn %d unauthorized status = %d", turn, observation.Unauthorized)
	}
	if len(observation.Tools) != 1 || observation.Tools[0] != "host_echo" {
		t.Errorf("turn %d tools/list = %v", turn, observation.Tools)
	}
	if observation.BearerEnvironment == "" {
		t.Errorf("turn %d did not resolve bearer environment reference", turn)
	}
	if observation.Result != wantResult {
		t.Errorf("turn %d tools/call result = %q, want %q", turn, observation.Result, wantResult)
	}
}

func assertClosedToolE2EProfile(t *testing.T, dir string, persistent bool, wantSession string) {
	t.Helper()
	if !persistent {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("Agent.Close left temporary Tool profile %q: %v", dir, err)
		}
		return
	}
	data, err := os.ReadFile(filepath.Join(dir, "projects", "host-tools-e2e-session.jsonl"))
	if err != nil || string(data) != wantSession {
		t.Fatalf("Agent.Close did not retain provider session: %q, %v", data, err)
	}
	data, err = os.ReadFile(filepath.Join(dir, "mcp.json"))
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Servers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if _, exists := config.Servers[hostedToolMCPKey]; exists {
		t.Fatal("Agent.Close retained the owned hosted Tool MCP projection")
	}
}

func hasToolTranscript(result *adaptor.Result) bool {
	if result == nil {
		return false
	}
	call, response := false, false
	for _, item := range result.Transcript() {
		call = call || item.Kind == driver.TranscriptToolCall
		response = response || item.Kind == driver.TranscriptToolResult
	}
	return call && response
}

func assertToolE2EResult(t *testing.T, result *adaptor.Result, prompt string) {
	t.Helper()
	if result == nil || result.Text != "host:"+prompt || !hasToolTranscript(result) {
		t.Fatalf("incomplete normal Result/Transcript for %s", prompt)
	}
	raw := result.Raw()
	if raw.Terminal == nil || !strings.Contains(raw.Stdout, `"type":"tool_call"`) || !strings.Contains(raw.Stdout, `"result":"host:`+prompt+`"`) || raw.Stderr != "host-tools-e2e provider: "+prompt+"\n" {
		t.Fatalf("incomplete Raw/terminal for %s", prompt)
	}
}

func assertToolE2ESpawns(t *testing.T, path string, want int) {
	t.Helper()
	data, err := os.ReadFile(path + ".spawns")
	if err != nil || string(data) != strings.Repeat("spawn\n", want) {
		t.Fatalf("provider spawn count = %d, want %d (%v)", strings.Count(string(data), "\n"), want, err)
	}
}

// Observe the public atomic Finalize boundary while delegating all persistence
// and lease validation to the real memory store, without a second store model.
type toolE2EStore struct {
	*memory.Store
	mu      sync.Mutex
	commits []toolE2ECommit
}
type toolE2ECommit struct {
	before, after, archived  *threadstore.Record
	previousID               string
	archiveOld, rebindActive bool
}

func (s *toolE2EStore) Finalize(ctx context.Context, req threadstore.FinalizeRequest) error {
	before, err := s.Store.Resolve(ctx, threadstore.Query{Key: req.Key})
	if err != nil {
		return err
	}
	if err := s.Store.Finalize(ctx, req); err != nil {
		return err
	}
	after, err := s.Store.Resolve(ctx, threadstore.Query{Key: req.Key})
	if err != nil {
		return err
	}
	var archived *threadstore.Record
	if req.ArchiveOld {
		archived, err = s.Store.Resolve(ctx, threadstore.Query{ID: req.PreviousID, IncludeArchived: true})
		if err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.commits = append(s.commits, toolE2ECommit{before: before, after: after, archived: archived, previousID: req.PreviousID, archiveOld: req.ArchiveOld, rebindActive: req.RebindActive})
	s.mu.Unlock()
	return nil
}
func (s *toolE2EStore) committed() []toolE2ECommit {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]toolE2ECommit(nil), s.commits...)
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
	// The fixture's session artifact is deliberately outside configuration
	// roots. Dedicated reconstruction must read both prior turns; the temporary
	// selection intentionally starts with no retained local session file.
	projectDir := filepath.Join(profileDir, "projects")
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		return 31
	}
	sessionPath := filepath.Join(projectDir, "host-tools-e2e-session.jsonl")
	previousSession, err := os.ReadFile(sessionPath)
	if err != nil && !os.IsNotExist(err) {
		return 32
	}
	profileDigest := sha256.Sum256([]byte(profileDir))
	sessionID := "host-tools-e2e-" + hex.EncodeToString(profileDigest[:16])
	resumeID := ""
	for i, argument := range os.Args[1:] {
		if argument == "--resume" && i+2 < len(os.Args) {
			resumeID = os.Args[i+2]
		}
	}
	if (resumeID != "") != (len(previousSession) > 0) || resumeID != "" && resumeID != sessionID {
		return 37
	}
	if err := os.WriteFile(sessionPath, append(previousSession, []byte(prompt+"\n")...), 0o600); err != nil {
		return 33
	}
	// Keep the first credential only inside the isolated test directory so
	// the replacement child can prove the new gateway rejects it. Neither the
	// credential nor its carrier value is emitted in observations or logs.
	previousCredentialStatus := 0
	credentialPath := observationPath + ".bearer"
	previousCredential, err := os.ReadFile(credentialPath)
	if os.IsNotExist(err) {
		if err := os.WriteFile(credentialPath, []byte(token), 0o600); err != nil {
			return 34
		}
	} else if err != nil {
		return 35
	} else if string(previousCredential) != token {
		request, _ := http.NewRequestWithContext(ctx, http.MethodPost, entry.URL, strings.NewReader(`{}`))
		request.Header.Set("Authorization", "Bearer "+string(previousCredential))
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			return 36
		}
		previousCredentialStatus = response.StatusCode
		response.Body.Close()
	}
	tokenDigest := sha256.Sum256([]byte(token))
	observation := toolE2EObservation{
		Endpoint:                 entry.URL,
		TokenHash:                hex.EncodeToString(tokenDigest[:]),
		Tools:                    toolNames,
		Result:                   result,
		Unauthorized:             unauthorized,
		SessionID:                sessionID,
		ResumeRequested:          containsArgument(os.Args[1:], "--resume"),
		BearerEnvironment:        envName,
		ProfileDir:               profileDir,
		SessionTurnsBefore:       strings.Count(string(previousSession), "\n"),
		PreviousCredentialStatus: previousCredentialStatus,
	}
	if err := appendToolE2EObservation(observationPath, observation); err != nil {
		return 30
	}

	fmt.Fprintln(os.Stderr, "host-tools-e2e provider: "+prompt)
	emitCursorFrame(map[string]any{
		"type": "system", "subtype": "init", "session_id": sessionID,
	})
	emitCursorFrame(map[string]any{
		"type": "tool_call", "subtype": "started", "session_id": sessionID, "call_id": "call-1",
		"tool_call": map[string]any{"hostedToolCall": map[string]any{"args": map[string]any{"value": prompt}}},
	})
	emitCursorFrame(map[string]any{
		"type": "tool_call", "subtype": "completed", "session_id": sessionID, "call_id": "call-1",
		"tool_call": map[string]any{"hostedToolCall": map[string]any{
			"args":   map[string]any{"value": prompt},
			"result": map[string]any{"success": map[string]any{"content": result}},
		}},
	})
	emitCursorFrame(map[string]any{
		"type": "assistant", "session_id": sessionID,
		"message": map[string]any{"content": []any{map[string]any{"type": "text", "text": result}}},
	})
	emitCursorFrame(map[string]any{
		"type": "result", "subtype": "success", "is_error": false,
		"result": result, "session_id": sessionID,
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
