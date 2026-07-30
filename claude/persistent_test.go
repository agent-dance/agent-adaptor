package claude

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/memory"
	"github.com/agent-dance/agent-adaptor/tool"
)

func TestPersistentClaudeIsDefaultAndWithSpawnOptsOut(t *testing.T) {
	fx := newPersistentClaudeFixture(t, nil)
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

	fx.run(t, "spawned", adaptor.WithSpawn())
	if got := fx.spawnCount(t); got != 2 {
		t.Fatalf("WithSpawn should terminate the writer and use one fresh process; spawns=%d", got)
	}
	fx.run(t, "persistent again")
	if got := fx.spawnCount(t); got != 3 {
		t.Fatalf("next default turn should establish a new persistent writer; spawns=%d", got)
	}
	if got := fx.overlapCount(t); got != 0 {
		t.Fatalf("detected %d overlapping provider writers", got)
	}
}

func TestPersistentClaudeReusesProcessWithHostDefinedTools(t *testing.T) {
	definition := tool.Define("echo", "Echo a value.", func(context.Context, struct{}) (struct{}, error) {
		return struct{}{}, nil
	}, tool.ReadOnly(), tool.Revision("echo/v1"))
	fx := newPersistentClaudeFixtureWithOptions(t, nil, adaptor.WithTools(definition))
	defer fx.close()
	fx.run(t, "one")
	fx.run(t, "two")
	if got := fx.spawnCount(t); got != 1 {
		t.Fatalf("host-defined Tools disabled Claude persistent reuse; spawns=%d", got)
	}
}

func TestPersistentClaudeRebuildsOnProcessSignatureDrift(t *testing.T) {
	fx := newPersistentClaudeFixture(t, nil)
	defer fx.close()
	fx.run(t, "one", adaptor.WithModel("model-a"))
	fx.run(t, "two", adaptor.WithModel("model-b"))
	if got := fx.spawnCount(t); got != 2 {
		t.Fatalf("model drift should rebuild the persistent process; spawns=%d", got)
	}

	settings := filepath.Join(fx.profileDir, "settings.local.json")
	if err := os.WriteFile(settings, []byte(`{"env":{"DRIFT":"yes"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	fx.run(t, "three", adaptor.WithModel("model-b"))
	if got := fx.spawnCount(t); got != 3 {
		t.Fatalf("settings drift should rebuild the persistent process; spawns=%d", got)
	}
}

func TestPersistentClaudePreDeliveryFallbackAndPostDeliveryNoReplay(t *testing.T) {
	fx := newPersistentClaudeFixture(t, nil)
	fx.pool.spawnProcess = func(persistentSpec, driver.EventSink) (*liveProcess, error) {
		return nil, errors.New("injected open failure")
	}
	result := fx.run(t, "fallback")
	if result.Text != "ok" || fx.spawnCount(t) != 1 {
		t.Fatalf("safe fallback result=%q spawns=%d", result.Text, fx.spawnCount(t))
	}
	fx.close()

	failing := newPersistentClaudeFixture(t, []driver.EnvBinding{{Name: "FAIL_AFTER_PERSISTENT_READ", Value: "1"}})
	defer failing.close()
	_, err := failing.thread.Run(context.Background(), "unknown outcome")
	if err == nil {
		t.Fatal("post-delivery disconnect must surface an infrastructure error")
	}
	if got := failing.spawnCount(t); got != 1 {
		t.Fatalf("post-delivery failure replayed the prompt; spawns=%d", got)
	}
}

func TestPersistentClaudeCloseAndIdleLifecycle(t *testing.T) {
	fx := newPersistentClaudeFixture(t, nil)
	fx.run(t, "warm")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := fx.agent.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := fx.agent.Close(ctx); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := fx.thread.Run(context.Background(), "closed"); !errors.Is(err, adaptor.ErrAgentClosed) {
		t.Fatalf("run after close=%v, want ErrAgentClosed", err)
	}

	oldTimeout := persistentIdleTimeout
	persistentIdleTimeout = 30 * time.Millisecond
	defer func() { persistentIdleTimeout = oldTimeout }()
	idle := newPersistentClaudeFixture(t, nil)
	defer idle.close()
	idle.run(t, "idle")
	deadline := time.Now().Add(2 * time.Second)
	for idle.pool.lookup("claude-persistent-session") != nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if idle.pool.lookup("claude-persistent-session") != nil {
		t.Fatal("idle persistent process was not reaped")
	}
}

type persistentClaudeFixture struct {
	agent      *adaptor.Agent
	thread     *adaptor.Thread
	pool       *persistentPool
	spawnFile  string
	overlap    string
	profileDir string
}

func newPersistentClaudeFixture(t *testing.T, callerEnv []driver.EnvBinding) *persistentClaudeFixture {
	return newPersistentClaudeFixtureWithOptions(t, callerEnv)
}

func newPersistentClaudeFixtureWithOptions(t *testing.T, callerEnv []driver.EnvBinding, opts ...adaptor.Option) *persistentClaudeFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("persistent process is POSIX-only")
	}
	root := t.TempDir()
	command := filepath.Join(root, "fake-claude")
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
persistent=0
previous=''
for arg in "$@"; do
  if [ "$previous" = '--input-format' ] && [ "$arg" = 'stream-json' ]; then persistent=1; fi
  previous="$arg"
done
emit_turn() {
  printf '%s\n' '{"type":"system","subtype":"init","session_id":"claude-persistent-session","model":"fake"}'
  printf '%s\n' '{"type":"assistant","session_id":"claude-persistent-session","message":{"model":"fake","content":[{"type":"text","text":"ok"}]}}'
  printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"session_id":"claude-persistent-session","result":"ok"}'
}
if [ "$persistent" = 1 ]; then
  while IFS= read -r frame; do
    if [ "${FAIL_AFTER_PERSISTENT_READ:-}" = 1 ]; then exit 23; fi
    emit_turn
  done
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
		driver.EnvBinding{Name: "CLAUDE_CONFIG_DIR", Value: profileDir},
		driver.EnvBinding{Name: "HOME", Value: root},
	)
	pool := newPersistentPool()
	cfg := Config{CommonConfig: CommonConfig{
		Command: command, CWD: root, Env: env, GracePeriod: 50 * time.Millisecond,
	}}
	d := configuredDriver{adapter: adapter{persistent: pool}, cfg: cfg}
	agentOpts := append([]adaptor.Option{adaptor.WithThreadStore(memory.NewStore())}, opts...)
	agent := adaptor.New(d, agentOpts...)
	return &persistentClaudeFixture{
		agent: agent, thread: agent.Thread("persistent-test/thread"), pool: pool,
		spawnFile: spawnFile, overlap: overlapFile, profileDir: profileDir,
	}
}

func (f *persistentClaudeFixture) run(t *testing.T, prompt string, opts ...adaptor.CallOption) *adaptor.Result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	result, err := f.thread.Run(ctx, prompt, opts...)
	if err != nil {
		t.Fatalf("run %q: %v", prompt, err)
	}
	return result
}

func (f *persistentClaudeFixture) spawnCount(t *testing.T) int {
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

func (f *persistentClaudeFixture) overlapCount(t *testing.T) int {
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

func (f *persistentClaudeFixture) close() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = f.agent.Close(ctx)
}
