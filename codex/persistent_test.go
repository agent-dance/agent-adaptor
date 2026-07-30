package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/testutil"
	"github.com/agent-dance/agent-adaptor/memory"
	"github.com/agent-dance/agent-adaptor/tool"
)

const codexHelperEnv = "GO_WANT_AGENT_ADAPTOR_CODEX_HELPER"

func TestMain(m *testing.M) {
	if os.Getenv(codexHelperEnv) == "1" {
		os.Exit(runCodexHelper())
	}
	os.Exit(m.Run())
}

func TestPersistentCodexReusesAppServerAcrossTurns(t *testing.T) {
	a, req, spawnFile, _ := newPersistentCodexTest(t, "")
	defer closeCodexTestAdapter(t, a)

	first := runCodexTurn(t, a, req)
	secondReq := resumedCodexRequest(req, first)
	second := runCodexTurn(t, a, secondReq)
	third := runCodexTurn(t, a, resumedCodexRequest(secondReq, second))

	if got := helperSpawnCount(t, spawnFile); got != 1 {
		t.Fatalf("turn1 should spawn once and turn2+ should reuse it; spawns=%d", got)
	}
	if second.Output != "reply-2" || third.Output != "reply-3" {
		t.Fatalf("turns did not stay on one app-server: second=%q third=%q", second.Output, third.Output)
	}
}

func TestPersistentCodexAcceptsSharedHostedToolRuntime(t *testing.T) {
	req := driver.Request{
		Streaming: true,
		Session:   &driver.SessionContext{EngineSessionID: "engine-tools"},
		Runtime: driver.RuntimePayload{Ensured: []driver.RuntimeServiceRef{{
			ID: "agent-adaptor-tools", Lifecycle: driver.RuntimeLifecycleShared,
			MCP: &driver.MCPServerSpec{Key: "agent-adaptor-tools", Transport: driver.MCPTransportHTTP, URL: "http://127.0.0.1:12345/mcp"},
		}}},
	}
	if !persistentEligible(Config{}, req) {
		t.Fatal("shared hosted Tool runtime disabled Codex persistent reuse")
	}
	req.Runtime.Ensured[0].Lifecycle = driver.RuntimeLifecycleEphemeral
	if persistentEligible(Config{}, req) {
		t.Fatal("ephemeral runtime incorrectly remained persistent-eligible")
	}
}

func TestPersistentCodexReusesAppServerWithHostDefinedTools(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("persistent process is POSIX-only")
	}
	lowLevel, req, spawnFile, _ := newPersistentCodexTest(t, "")
	configured := configuredDriver{adapter: lowLevel.(adapter), cfg: req.Config.(Config)}
	definition := tool.Define("echo", "Echo a value.", func(context.Context, struct{}) (struct{}, error) {
		return struct{}{}, nil
	}, tool.ReadOnly(), tool.Revision("echo/v1"))
	agent := adaptor.New(configured,
		adaptor.WithThreadStore(memory.NewStore()),
		adaptor.WithTools(definition),
	)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := agent.Close(ctx); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()
	thread := agent.Thread("codex-tools-persistent")
	if _, err := thread.Run(context.Background(), "one"); err != nil {
		t.Fatalf("first turn: %v", err)
	}
	if _, err := thread.Run(context.Background(), "two"); err != nil {
		t.Fatalf("second turn: %v", err)
	}
	if got := helperSpawnCount(t, spawnFile); got != 1 {
		t.Fatalf("host-defined Tools disabled Codex persistent reuse; spawns=%d", got)
	}
}

func TestPersistentCodexSignatureDriftRebuilds(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*testing.T, *driver.Request)
	}{
		{
			name: "model",
			mutate: func(_ *testing.T, req *driver.Request) {
				cfg := readConfig(req.Config)
				cfg.Model = "gpt-drift"
				req.Config = cfg
			},
		},
		{
			name: "env",
			mutate: func(_ *testing.T, req *driver.Request) {
				cfg := readConfig(req.Config)
				cfg.Env = append(cfg.Env, driver.EnvBinding{Name: "CODEX_TEST_BINDING", Value: "changed"})
				req.Config = cfg
			},
		},
		{
			name: "settings content",
			mutate: func(t *testing.T, req *driver.Request) {
				cfg := readConfig(req.Config)
				home := codexHomeFromBindings(cfg.Env)
				if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("model = \"changed\"\n"), 0o600); err != nil {
					t.Fatalf("change settings: %v", err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, req, spawnFile, _ := newPersistentCodexTest(t, "")
			defer closeCodexTestAdapter(t, a)
			first := runCodexTurn(t, a, req)
			secondReq := resumedCodexRequest(req, first)
			tc.mutate(t, &secondReq)
			second := runCodexTurn(t, a, secondReq)
			if got := helperSpawnCount(t, spawnFile); got != 2 {
				t.Fatalf("configuration drift should rebuild app-server; spawns=%d", got)
			}
			if second.Output != "reply-1" {
				t.Fatalf("expected first turn on rebuilt process, got %q", second.Output)
			}
		})
	}
}

func TestCodexSettingsFingerprintIgnoresProviderSystemSkills(t *testing.T) {
	root := t.TempDir()
	codexHome := filepath.Join(root, "codex-home")
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(filepath.Join(codexHome, "skills"), 0o755); err != nil {
		t.Fatalf("mkdir Codex skills: %v", err)
	}
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}

	before := codexSettingsFingerprint(codexHome, workspace)
	if err := os.MkdirAll(filepath.Join(codexHome, "skills", ".system", "provider-skill"), 0o755); err != nil {
		t.Fatalf("mkdir provider system skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "skills", ".system", "provider-skill", "SKILL.md"), []byte("provider-owned\n"), 0o644); err != nil {
		t.Fatalf("write provider system skill: %v", err)
	}
	if afterSystem := codexSettingsFingerprint(codexHome, workspace); afterSystem != before {
		t.Fatal("provider-owned skills/.system changed the persistent-process signature")
	}

	if err := os.MkdirAll(filepath.Join(codexHome, "skills", "host-skill"), 0o755); err != nil {
		t.Fatalf("mkdir host skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "skills", "host-skill", "SKILL.md"), []byte("host-owned\n"), 0o644); err != nil {
		t.Fatalf("write host skill: %v", err)
	}
	if afterHost := codexSettingsFingerprint(codexHome, workspace); afterHost == before {
		t.Fatal("host skill change did not update the persistent-process signature")
	}
}

func TestPersistentCodexFailureBeforePromptFallsBack(t *testing.T) {
	a, req, spawnFile, _ := newPersistentCodexTest(t, "fail_open_once")
	defer closeCodexTestAdapter(t, a)

	result := runCodexTurn(t, a, req)
	if result.Output != "reply-1" {
		t.Fatalf("one-shot fallback output=%q", result.Output)
	}
	if got := helperSpawnCount(t, spawnFile); got < 2 {
		t.Fatalf("expected failed persistent open plus transparent fallback, spawns=%d", got)
	}
}

func TestPersistentCodexDoesNotReplayAfterTurnStart(t *testing.T) {
	a, req, spawnFile, _ := newPersistentCodexTest(t, "fail_after_prompt_once")
	defer closeCodexTestAdapter(t, a)

	_, err := a.Run(context.Background(), req, &testutil.EventRecorder{})
	if err == nil || errors.Is(err, errPersistentFallback) {
		t.Fatalf("post-delivery disconnect must surface its original failure, got %v", err)
	}
	if got := helperSpawnCount(t, spawnFile); got != 1 {
		t.Fatalf("post-delivery failure must not replay; spawns=%d", got)
	}
}

func TestPersistentCodexOneShotHandoffWaitsForOldWriter(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process liveness probe uses POSIX signal 0")
	}
	a, req, spawnFile, _ := newPersistentCodexTest(t, "")
	overlapFile := filepath.Join(filepath.Dir(spawnFile), "overlap.log")
	defer closeCodexTestAdapter(t, a)
	first := runCodexTurn(t, a, req)

	nonStreaming := resumedCodexRequest(req, first)
	nonStreaming.Streaming = false
	result, err := a.Run(context.Background(), nonStreaming, &testutil.EventRecorder{})
	if err != nil {
		t.Fatalf("one-shot handoff: %v", err)
	}
	if result.Checkpoint == nil || !result.Checkpoint.Valid {
		t.Fatalf("one-shot result checkpoint=%#v", result.Checkpoint)
	}
	if raw, _ := os.ReadFile(overlapFile); strings.TrimSpace(string(raw)) != "" {
		t.Fatalf("old and temporary writers overlapped: %s", raw)
	}
	if got := helperSpawnCount(t, spawnFile); got < 3 {
		t.Fatalf("expected persistent + exec handoff + prewarm, spawns=%d", got)
	}
}

func TestPersistentCodexCloseCancelAndIdleReapProcesses(t *testing.T) {
	t.Run("close is idempotent", func(t *testing.T) {
		a, req, _, activeFile := newPersistentCodexTest(t, "")
		_ = runCodexTurn(t, a, req)
		closer := a.(driver.ProcessLifecycleDriver)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := closer.CloseProcesses(ctx); err != nil {
			t.Fatalf("close: %v", err)
		}
		if err := closer.CloseProcesses(ctx); err != nil {
			t.Fatalf("second close: %v", err)
		}
		assertHelperInactive(t, activeFile)
	})

	t.Run("run context cancellation kills group", func(t *testing.T) {
		a, req, spawnFile, activeFile := newPersistentCodexTest(t, "block_turn")
		defer closeCodexTestAdapter(t, a)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() {
			_, err := a.Run(ctx, req, &testutil.EventRecorder{})
			done <- err
		}()

		// Synchronize cancellation on the helper receiving turn/start. A fixed
		// startup timeout can expire before the race-instrumented Linux binary
		// has even spawned, which tests scheduler speed rather than the
		// post-prompt cancellation contract.
		turnFile := filepath.Join(filepath.Dir(spawnFile), "turns.log")
		waitForHelperRecordCount(t, turnFile, 1)
		cancel()

		var err error
		select {
		case err = <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("cancelled run did not return")
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel error=%v", err)
		}
		if got := helperSpawnCount(t, spawnFile); got != 1 {
			t.Fatalf("cancelled prompt must not replay; spawns=%d", got)
		}
		assertHelperInactive(t, activeFile)
	})

	t.Run("idle timeout", func(t *testing.T) {
		old := codexPersistentIdleTimeout
		codexPersistentIdleTimeout = 30 * time.Millisecond
		defer func() { codexPersistentIdleTimeout = old }()
		a, req, _, activeFile := newPersistentCodexTest(t, "")
		defer closeCodexTestAdapter(t, a)
		_ = runCodexTurn(t, a, req)
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if !helperActive(activeFile) {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("idle app-server was not reaped")
	})
}

func TestPersistentCodexRequiresStableSessionKey(t *testing.T) {
	a, req, spawnFile, _ := newPersistentCodexTest(t, "")
	defer closeCodexTestAdapter(t, a)
	req.Session = nil
	first := runCodexTurn(t, a, req)
	_ = first
	_ = runCodexTurn(t, a, req)
	if got := helperSpawnCount(t, spawnFile); got != 2 {
		t.Fatalf("without SessionStore-backed context every turn must remain one-shot; spawns=%d", got)
	}
}

func TestPersistentCodexNativeSchemaUsesPerTurnFieldWithoutHandoff(t *testing.T) {
	a, req, spawnFile, _ := newPersistentCodexTest(t, "")
	defer closeCodexTestAdapter(t, a)
	first := runCodexTurn(t, a, req)

	structuredReq := resumedCodexRequest(req, first)
	structuredReq.StructuredOutputSource = driver.StructuredOutputSourceNative
	structuredReq.OutputSchema = &driver.OutputSchema{
		Format:     driver.OutputFormatJSONSchema,
		SchemaJSON: json.RawMessage(`{"type":"object","properties":{"answer":{"type":"integer"}},"required":["answer"],"additionalProperties":false}`),
	}
	result := runCodexTurn(t, a, structuredReq)
	if got := helperSpawnCount(t, spawnFile); got != 1 {
		t.Fatalf("turn/start.outputSchema should reuse the existing app-server; spawns=%d", got)
	}
	if result.Output != `{"answer":42}` {
		t.Fatalf("structured output text=%q", result.Output)
	}
	if result.StructuredOutput == nil || result.StructuredOutput.Source != driver.StructuredOutputSourceNative ||
		string(result.StructuredOutput.RawJSON) != `{"answer":42}` {
		t.Fatalf("native structured output=%#v", result.StructuredOutput)
	}
}

func newPersistentCodexTest(t *testing.T, mode string) (driver.Driver, driver.Request, string, string) {
	t.Helper()
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	codexHome := filepath.Join(root, "codex-home")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		t.Fatalf("mkdir codex home: %v", err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte("# stable\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	command, err := os.Executable()
	if err != nil {
		t.Fatalf("test executable: %v", err)
	}
	spawnFile := filepath.Join(root, "spawns.log")
	activeFile := filepath.Join(root, "active.pid")
	overlapFile := filepath.Join(root, "overlap.log")
	cfg := Config{
		CommonConfig: CommonConfig{
			Command:     command,
			CWD:         workspace,
			GracePeriod: 200 * time.Millisecond,
			Env: []driver.EnvBinding{
				{Name: "HOME", Value: root},
				{Name: "USERPROFILE", Value: root},
				{Name: "CODEX_HOME", Value: codexHome},
				{Name: codexHelperEnv, Value: "1"},
				{Name: "CODEX_HELPER_MODE", Value: mode},
				{Name: "CODEX_HELPER_SPAWN_FILE", Value: spawnFile},
				{Name: "CODEX_HELPER_ACTIVE_FILE", Value: activeFile},
				{Name: "CODEX_HELPER_OVERLAP_FILE", Value: overlapFile},
				{Name: "CODEX_HELPER_TURN_FILE", Value: filepath.Join(root, "turns.log")},
			},
		},
		Model: "gpt-test",
	}
	req := driver.Request{
		RunID:          "run-1",
		Prompt:         "hello",
		Config:         cfg,
		Streaming:      true,
		Workspace:      driver.WorkspaceLease{ID: "workspace-a", CWD: workspace},
		ProfilePayload: driver.ProfilePayload{Fingerprint: "profile-a"},
		Session: &driver.SessionContext{
			EngineSessionID: "codex-engine-a",
			Mode:            driver.SessionContinueOrStart,
		},
		Policy: driver.RunPolicy{
			Isolation: driver.IsolationReadOnly,
			HumanDecision: driver.HumanDecisionPolicy{
				Permission: driver.HumanDecisionAutoApprove,
			},
		},
	}
	return adapter{persistent: newPersistentPool()}, req, spawnFile, activeFile
}

func runCodexTurn(t *testing.T, a driver.Driver, req driver.Request) driver.Response {
	t.Helper()
	result, err := a.Run(context.Background(), req, &testutil.EventRecorder{})
	if err != nil {
		t.Fatalf("codex run: %v", err)
	}
	if result.Checkpoint == nil || result.Checkpoint.State == nil || !result.Checkpoint.Valid {
		t.Fatalf("checkpoint=%#v", result.Checkpoint)
	}
	return result
}

func resumedCodexRequest(base driver.Request, previous driver.Response) driver.Request {
	next := base
	next.RunID += "-next"
	next.Session = &driver.SessionContext{
		EngineSessionID: base.Session.EngineSessionID,
		Mode:            driver.SessionContinueOrStart,
		State:           previous.Checkpoint.State,
	}
	return next
}

func closeCodexTestAdapter(t *testing.T, a driver.Driver) {
	t.Helper()
	closer, ok := a.(driver.ProcessLifecycleDriver)
	if !ok {
		t.Fatal("codex adapter does not implement ProcessLifecycleDriver")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := closer.CloseProcesses(ctx); err != nil {
		t.Fatalf("close adapter: %v", err)
	}
}

func helperSpawnCount(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read spawn log: %v", err)
	}
	count := 0
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}

func waitForHelperRecordCount(t *testing.T, path string, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(path)
		if err == nil {
			count := 0
			for _, line := range strings.Split(string(raw), "\n") {
				if strings.TrimSpace(line) != "" {
					count++
				}
			}
			if count >= want {
				return
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read helper record %s: %v", path, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("helper record %s did not reach %d entries", path, want)
}

func assertHelperInactive(t *testing.T, activeFile string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !helperActive(activeFile) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("helper process in %s is still alive", activeFile)
}

func helperActive(path string) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
}

type helperRequest struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type helperResponse struct {
	ID     json.RawMessage `json:"id"`
	Result any             `json:"result"`
}

func runCodexHelper() int {
	spawnFile := os.Getenv("CODEX_HELPER_SPAWN_FILE")
	activeFile := os.Getenv("CODEX_HELPER_ACTIVE_FILE")
	overlapFile := os.Getenv("CODEX_HELPER_OVERLAP_FILE")
	spawnNo := appendHelperRecord(spawnFile)
	markHelperActive(activeFile, overlapFile)
	defer clearHelperActive(activeFile)

	if hasProcessArg("exec") {
		_, _ = io.ReadAll(os.Stdin)
		_, _ = fmt.Fprintln(os.Stdout, `{"type":"thread.started","thread_id":"thread-1"}`)
		_, _ = fmt.Fprintln(os.Stdout, `{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}`)
		return 0
	}

	mode := os.Getenv("CODEX_HELPER_MODE")
	reader := bufio.NewScanner(os.Stdin)
	reader.Buffer(make([]byte, 4096), 4<<20)
	writer := bufio.NewWriter(os.Stdout)
	turn := 0
	for reader.Scan() {
		var req helperRequest
		if json.Unmarshal(reader.Bytes(), &req) != nil {
			continue
		}
		if mode == "fail_open_once" && spawnNo == 1 && req.Method == "initialize" {
			return 21
		}
		switch req.Method {
		case "initialize":
			writeHelperJSON(writer, helperResponse{ID: req.ID, Result: map[string]any{"userAgent": "fake-codex"}})
		case "initialized":
			// Notification: no response.
		case "thread/start":
			writeHelperJSON(writer, helperResponse{ID: req.ID, Result: map[string]any{"thread": map[string]any{"id": "thread-1"}}})
		case "thread/resume":
			var params struct {
				ThreadID string `json:"threadId"`
			}
			_ = json.Unmarshal(req.Params, &params)
			writeHelperJSON(writer, helperResponse{ID: req.ID, Result: map[string]any{"thread": map[string]any{"id": params.ThreadID}}})
		case "turn/start":
			turn++
			appendHelperRecord(os.Getenv("CODEX_HELPER_TURN_FILE"))
			if mode == "fail_after_prompt_once" && spawnNo == 1 {
				return 22
			}
			if mode == "block_turn" {
				continue
			}
			var turnParams struct {
				OutputSchema json.RawMessage `json:"outputSchema"`
			}
			_ = json.Unmarshal(req.Params, &turnParams)
			turnID := fmt.Sprintf("turn-%d", turn)
			itemID := fmt.Sprintf("item-%d", turn)
			text := fmt.Sprintf("reply-%d", turn)
			if len(turnParams.OutputSchema) > 0 {
				text = `{"answer":42}`
			}
			writeHelperJSON(writer, helperResponse{ID: req.ID, Result: map[string]any{"turn": map[string]any{"id": turnID, "status": "inProgress"}}})
			writeHelperNotification(writer, "turn/started", map[string]any{"threadId": "thread-1", "turn": map[string]any{"id": turnID, "status": "inProgress"}})
			writeHelperNotification(writer, "item/agentMessage/delta", map[string]any{"threadId": "thread-1", "turnId": turnID, "itemId": itemID, "delta": text})
			writeHelperNotification(writer, "item/completed", map[string]any{"threadId": "thread-1", "turnId": turnID, "item": map[string]any{"id": itemID, "type": "agentMessage", "text": text}})
			writeHelperNotification(writer, "turn/completed", map[string]any{"threadId": "thread-1", "turn": map[string]any{"id": turnID, "status": "completed", "usage": map[string]any{"inputTokens": 2, "outputTokens": 1, "cachedInputTokens": 0}}})
		case "turn/interrupt":
			writeHelperJSON(writer, helperResponse{ID: req.ID, Result: map[string]any{}})
		}
	}
	return 0
}

func hasProcessArg(want string) bool {
	for _, arg := range os.Args[1:] {
		if arg == want {
			return true
		}
	}
	return false
}

func writeHelperJSON(w *bufio.Writer, value any) {
	_ = json.NewEncoder(w).Encode(value)
	_ = w.Flush()
}

func writeHelperNotification(w *bufio.Writer, method string, params any) {
	writeHelperJSON(w, map[string]any{"method": method, "params": params})
}

func appendHelperRecord(path string) int {
	raw, _ := os.ReadFile(path)
	count := 0
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err == nil {
		_, _ = fmt.Fprintf(file, "%d\n", os.Getpid())
		_ = file.Close()
	}
	return count + 1
}

func markHelperActive(activeFile, overlapFile string) {
	if helperActive(activeFile) {
		file, err := os.OpenFile(overlapFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = fmt.Fprintf(file, "overlap with %s\n", strings.TrimSpace(readFileString(activeFile)))
			_ = file.Close()
		}
	}
	_ = os.WriteFile(activeFile, []byte(strconv.Itoa(os.Getpid())), 0o600)
}

func clearHelperActive(activeFile string) {
	if strings.TrimSpace(readFileString(activeFile)) == strconv.Itoa(os.Getpid()) {
		_ = os.Remove(activeFile)
	}
}

func readFileString(path string) string {
	raw, _ := os.ReadFile(path)
	return string(raw)
}
