package codebuddy

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/memory"
	"github.com/agent-dance/agent-adaptor/tool"
)

func TestPersistentCodeBuddyIsDefaultAndWithSpawnOptsOut(t *testing.T) {
	fx := newPersistentCodeBuddyFixture(t, nil)
	defer fx.close()

	first := fx.run(t, "one")
	second := fx.run(t, "two")
	if got := fx.spawnCount(t); got != 1 {
		t.Fatalf("default Thread turns should reuse one process; spawns=%d", got)
	}
	for i, result := range []*adaptor.Result{first, second} {
		if result.Text != "ok" || !strings.Contains(result.Raw().Stdout, `"type":"result"`) {
			t.Fatalf("turn %d output contract: %#v raw=%#v", i+1, result, result.Raw())
		}
	}
	if !strings.Contains(first.Raw().Stdout, `"type":"control_response"`) {
		t.Fatalf("first turn did not initialize control transport: %s", first.Raw().Stdout)
	}
	if strings.Contains(second.Raw().Stdout, `"type":"control_response"`) {
		t.Fatalf("reused turn repeated initialization: %s", second.Raw().Stdout)
	}

	fx.run(t, "spawned", adaptor.WithSpawn())
	if got := fx.spawnCount(t); got != 2 {
		t.Fatalf("WithSpawn should hand off to one fresh process; spawns=%d", got)
	}
	fx.run(t, "persistent again")
	if got := fx.spawnCount(t); got != 3 {
		t.Fatalf("next default turn should establish a new persistent writer; spawns=%d", got)
	}
	if got := fx.overlapCount(t); got != 0 {
		t.Fatalf("detected %d overlapping provider writers", got)
	}
}

func TestPersistentCodeBuddyReusesProcessWithHostDefinedTools(t *testing.T) {
	definition := tool.Define("echo", "Echo a value.", func(context.Context, struct{}) (struct{}, error) {
		return struct{}{}, nil
	}, tool.ReadOnly(), tool.Revision("echo/v1"))
	fx := newPersistentCodeBuddyFixtureWithOptions(t, nil, adaptor.WithTools(definition))
	defer fx.close()
	fx.run(t, "one")
	fx.run(t, "two")
	if got := fx.spawnCount(t); got != 1 {
		t.Fatalf("host-defined Tools disabled CodeBuddy persistent reuse; spawns=%d", got)
	}
}

func TestPersistentCodeBuddyRebuildsOnProcessSignatureDrift(t *testing.T) {
	fx := newPersistentCodeBuddyFixture(t, nil)
	defer fx.close()
	fx.run(t, "one", adaptor.WithModel("model-a"))
	fx.run(t, "two", adaptor.WithModel("model-b"))
	if got := fx.spawnCount(t); got != 2 {
		t.Fatalf("model drift should rebuild the persistent process; spawns=%d", got)
	}

	settings := filepath.Join(fx.profileDir, "settings.local.json")
	if err := os.WriteFile(settings, []byte(`{"theme":"changed"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	fx.run(t, "three", adaptor.WithModel("model-b"))
	if got := fx.spawnCount(t); got != 3 {
		t.Fatalf("settings drift should rebuild the persistent process; spawns=%d", got)
	}
	if got := fx.overlapCount(t); got != 0 {
		t.Fatalf("detected %d overlapping writers during rebuild", got)
	}
}

func TestPersistentCodeBuddyPreDeliveryFallbackAndPostDeliveryNoReplay(t *testing.T) {
	fx := newPersistentCodeBuddyFixture(t, nil)
	fx.pool.spawnProcess = func(persistentSpec, driver.EventSink) (*liveProcess, error) {
		return nil, errors.New("injected open failure")
	}
	result := fx.run(t, "fallback")
	if result.Text != "ok" || fx.spawnCount(t) != 1 {
		t.Fatalf("safe fallback result=%q spawns=%d", result.Text, fx.spawnCount(t))
	}
	fx.close()

	failing := newPersistentCodeBuddyFixture(t, []driver.EnvBinding{{Name: "FAIL_AFTER_PERSISTENT_READ", Value: "1"}})
	defer failing.close()
	_, err := failing.thread.Run(context.Background(), "unknown outcome")
	if err == nil {
		t.Fatal("post-delivery disconnect must surface an infrastructure error")
	}
	if got := failing.spawnCount(t); got != 1 {
		t.Fatalf("post-delivery failure replayed the prompt; spawns=%d", got)
	}
}

func TestPersistentCodeBuddyStructuredRelayAndPrewarm(t *testing.T) {
	fx := newPersistentCodeBuddyFixture(t, nil)
	defer fx.close()
	fx.run(t, "warm")
	result := fx.run(t, "structured", adaptor.WithSchemaJSON([]byte(`{
		"type":"object",
		"properties":{"value":{"type":"string"}},
		"required":["value"],
		"additionalProperties":false
	}`)))
	var value struct {
		Value string `json:"value"`
	}
	if err := result.Decode(&value); err != nil || value.Value != "ok" {
		t.Fatalf("structured result value=%+v err=%v", value, err)
	}
	if got := fx.waitSpawnCount(t, 3); got != 3 {
		t.Fatalf("spawns after relay=%d, want persistent + one-shot + prewarm", got)
	}
	fx.run(t, "after schema")
	if got := fx.spawnCount(t); got != 3 {
		t.Fatalf("post-schema turn did not reuse prewarm; spawns=%d", got)
	}
	if got := fx.overlapCount(t); got != 0 {
		t.Fatalf("detected %d overlapping writers during relay", got)
	}
}

func TestPersistentCodeBuddyCloseCancellationAndIdleLifecycle(t *testing.T) {
	fx := newPersistentCodeBuddyFixture(t, []driver.EnvBinding{{Name: "STAY_AFTER_EOF", Value: "1"}})
	fx.run(t, "warm")
	pid := fx.currentPID(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := fx.agent.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := fx.agent.Close(ctx); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if err := exec.Command("kill", "-0", strconv.Itoa(pid)).Run(); err == nil {
		t.Fatalf("persistent pid %d still alive after Close", pid)
	}
	if _, err := fx.thread.Run(context.Background(), "closed"); !errors.Is(err, adaptor.ErrAgentClosed) {
		t.Fatalf("run after close=%v, want ErrAgentClosed", err)
	}

	cancelled := newPersistentCodeBuddyFixture(t, []driver.EnvBinding{{Name: "BLOCK_AFTER_PERSISTENT_READ", Value: "1"}})
	defer cancelled.close()
	runCtx, cancelRun := context.WithCancel(context.Background())
	stream := cancelled.thread.Stream(runCtx, "block")
	if got := cancelled.waitSpawnCount(t, 1); got != 1 {
		t.Fatalf("cancel fixture spawns=%d, want 1", got)
	}
	blockedPID := cancelled.currentPID(t)
	cancelRun()
	if _, err := stream.Result(); !errors.Is(err, context.Canceled) {
		t.Fatalf("Result error=%v want context.Canceled", err)
	}
	if err := exec.Command("kill", "-0", strconv.Itoa(blockedPID)).Run(); err == nil {
		t.Fatalf("cancelled persistent pid %d still alive", blockedPID)
	}

	oldTimeout := codeBuddyPersistentIdleTimeout
	codeBuddyPersistentIdleTimeout = 30 * time.Millisecond
	defer func() { codeBuddyPersistentIdleTimeout = oldTimeout }()
	idle := newPersistentCodeBuddyFixture(t, nil)
	defer idle.close()
	idle.run(t, "idle")
	deadline := time.Now().Add(15 * time.Second)
	for idle.pool.lookup("codebuddy-persistent-session") != nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if idle.pool.lookup("codebuddy-persistent-session") != nil {
		t.Fatal("idle persistent process was not reaped")
	}
}

type persistentCodeBuddyFixture struct {
	agent      *adaptor.Agent
	thread     *adaptor.Thread
	pool       *persistentPool
	spawnFile  string
	pidFile    string
	overlap    string
	profileDir string
}

func newPersistentCodeBuddyFixture(t *testing.T, callerEnv []driver.EnvBinding) *persistentCodeBuddyFixture {
	return newPersistentCodeBuddyFixtureWithOptions(t, callerEnv)
}

func newPersistentCodeBuddyFixtureWithOptions(t *testing.T, callerEnv []driver.EnvBinding, opts ...adaptor.Option) *persistentCodeBuddyFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("persistent process is POSIX-only")
	}
	root := t.TempDir()
	command := filepath.Join(root, "fake-codebuddy")
	spawnFile := filepath.Join(root, "spawns")
	pidFile := filepath.Join(root, "current-pid")
	overlapFile := filepath.Join(root, "overlap")
	profileDir := filepath.Join(root, "profile")
	if err := os.MkdirAll(profileDir, 0o700); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
set -eu
printf '%s\n' "$$" >> "$SPAWN_FILE"
if [ -f "$PID_FILE" ]; then
  old_pid=$(cat "$PID_FILE")
  if [ "$old_pid" != "$$" ] && kill -0 "$old_pid" 2>/dev/null; then
    printf 'overlap:%s:%s\n' "$old_pid" "$$" >> "$OVERLAP_FILE"
  fi
fi
printf '%s' "$$" > "$PID_FILE"
control=0
structured=0
for arg in "$@"; do
  if [ "$arg" = '--input-format=stream-json' ]; then control=1; fi
  if [ "$arg" = '--json-schema' ]; then structured=1; fi
done
emit_turn() {
  printf '%s\n' '{"type":"system","subtype":"init","session_id":"codebuddy-persistent-session","model":"fake"}'
  printf '%s\n' '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"ok"}}}'
  printf '%s\n' '{"type":"assistant","session_id":"codebuddy-persistent-session","message":{"model":"fake","content":[{"type":"text","text":"ok"}]}}'
  if [ "$structured" = 1 ]; then
    printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"session_id":"codebuddy-persistent-session","result":"{\"value\":\"ok\"}","structured_output":{"value":"ok"}}'
  else
    printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"session_id":"codebuddy-persistent-session","result":"ok"}'
  fi
}
if [ "$control" = 1 ]; then
  while IFS= read -r frame; do
    case "$frame" in
      *agent-adaptor-initialize*)
        printf '%s\n' '{"type":"control_response","response":{"subtype":"success","request_id":"agent-adaptor-initialize","response":{}}}'
        ;;
      *'"type":"user"'*)
        if [ "${FAIL_AFTER_PERSISTENT_READ:-}" = 1 ]; then exit 23; fi
        if [ "${BLOCK_AFTER_PERSISTENT_READ:-}" = 1 ]; then sleep 30; fi
        emit_turn
        ;;
    esac
  done
  if [ "${STAY_AFTER_EOF:-}" = 1 ]; then
    while :; do sleep 30; done
  fi
else
  cat >/dev/null || true
  emit_turn
fi
`
	if err := os.WriteFile(command, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	env := append([]driver.EnvBinding(nil), callerEnv...)
	env = append(env,
		driver.EnvBinding{Name: "SPAWN_FILE", Value: spawnFile},
		driver.EnvBinding{Name: "PID_FILE", Value: pidFile},
		driver.EnvBinding{Name: "OVERLAP_FILE", Value: overlapFile},
		driver.EnvBinding{Name: "CODEBUDDY_CONFIG_DIR", Value: profileDir},
		driver.EnvBinding{Name: "HOME", Value: root},
	)
	pool := newPersistentPool()
	cfg := Config{CommonConfig: CommonConfig{
		Command: command, CWD: root, Env: env, GracePeriod: 50 * time.Millisecond,
	}}
	d := configuredDriver{adapter: adapter{persistent: pool}, cfg: cfg}
	agentOpts := append([]adaptor.Option{adaptor.WithThreadStore(memory.NewStore())}, opts...)
	agent := adaptor.New(d, agentOpts...)
	return &persistentCodeBuddyFixture{
		agent: agent, thread: agent.Thread("persistent-test/thread"), pool: pool,
		spawnFile: spawnFile, pidFile: pidFile, overlap: overlapFile, profileDir: profileDir,
	}
}

func (f *persistentCodeBuddyFixture) run(t *testing.T, prompt string, opts ...adaptor.CallOption) *adaptor.Result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	result, err := f.thread.Run(ctx, prompt, opts...)
	if err != nil {
		t.Fatalf("run %q: %v", prompt, err)
	}
	return result
}

func (f *persistentCodeBuddyFixture) spawnCount(t *testing.T) int {
	t.Helper()
	raw, err := os.ReadFile(f.spawnFile)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return len(strings.Fields(string(raw)))
}

func (f *persistentCodeBuddyFixture) waitSpawnCount(t *testing.T, want int) int {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	for {
		if got := f.spawnCount(t); got >= want || time.Now().After(deadline) {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (f *persistentCodeBuddyFixture) overlapCount(t *testing.T) int {
	t.Helper()
	raw, err := os.ReadFile(f.overlap)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return len(strings.Fields(string(raw)))
}

func (f *persistentCodeBuddyFixture) currentPID(t *testing.T) int {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	for {
		raw, err := os.ReadFile(f.pidFile)
		if err == nil {
			if pid, parseErr := strconv.Atoi(strings.TrimSpace(string(raw))); parseErr == nil {
				return pid
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("pid file did not become readable: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (f *persistentCodeBuddyFixture) close() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = f.agent.Close(ctx)
}
