package pi

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/testutil"
	"github.com/agent-dance/agent-adaptor/memory"
	"github.com/agent-dance/agent-adaptor/tool"
)

type rpcFixture struct {
	agent        *adaptor.Agent
	thread       *adaptor.Thread
	pool         *rpcPool
	dir, profile string
}

func persistentRPCFixture(t *testing.T, opts ...adaptor.Option) *rpcFixture {
	t.Helper()
	t.Setenv("AGENT_ADAPTOR_PI_DYNAMIC_SESSIONS", "1")
	command, capture := testutil.ProtocolCommand(t, happy, "server stderr", 0)
	profile := t.TempDir()
	d := Driver(Config{Command: command, Model: "test/model", Env: []driver.EnvBinding{{Name: "PI_CODING_AGENT_DIR", Value: profile}}}).(configuredDriver)
	opts = append([]adaptor.Option{adaptor.WithWorkspace(t.TempDir()), adaptor.WithThreadStore(memory.NewStore())}, opts...)
	a := adaptor.New(d, opts...)
	f := &rpcFixture{agent: a, thread: a.Thread("thread"), pool: d.pool, dir: filepath.Dir(capture), profile: profile}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := a.Close(ctx); err != nil {
			t.Errorf("Close: %v", err)
		}
		for _, pid := range f.pids(t) {
			if testutil.ProcessAlive(pid) {
				t.Errorf("leaked server pid=%d", pid)
			}
		}
	})
	return f
}
func (f *rpcFixture) pids(t *testing.T) []int {
	t.Helper()
	raw, _ := os.ReadFile(filepath.Join(f.dir, "spawns.log"))
	var pids []int
	for _, line := range strings.Fields(string(raw)) {
		pid, err := strconv.Atoi(line)
		if err != nil {
			t.Fatal(err)
		}
		pids = append(pids, pid)
	}
	return pids
}
func (f *rpcFixture) run(t *testing.T, opts ...adaptor.CallOption) *adaptor.Result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r, err := f.thread.Run(ctx, "hello", opts...)
	if err != nil {
		t.Fatal(err)
	}
	if r.Text != "OK" || r.Raw().Terminal == nil || r.Raw().Stdout == "" {
		t.Fatalf("response=%+v", r)
	}
	return r
}

func TestRPCPersistentDefaultSpawnAndClose(t *testing.T) {
	t.Setenv("AGENT_ADAPTOR_PI_EXCLUSIVE_WRITER", "1")
	f := persistentRPCFixture(t)
	a, b := f.run(t), f.run(t)
	if len(f.pids(t)) != 1 || a.Metadata["pi.rpc_pid"] != b.Metadata["pi.rpc_pid"] || b.Metadata["pi.rpc_reused"] != "true" {
		t.Fatal("Thread did not reuse its server")
	}
	if strings.Count(b.Raw().Stdout, `"type":"agent_start"`) != 1 || b.Raw().Stderr != "" {
		t.Fatal("previous turn raw bytes were replayed")
	}
	f.run(t, adaptor.WithSpawn())
	if len(f.pids(t)) != 2 {
		t.Fatal("WithSpawn did not spawn")
	}
	for _, pid := range f.pids(t) {
		if testutil.ProcessAlive(pid) {
			t.Fatalf("temporary process was retained: %d", pid)
		}
	}
	f.run(t)
	if len(f.pids(t)) != 3 {
		t.Fatal("default turn reused WithSpawn writer")
	}
	if _, err := os.Stat(filepath.Join(f.dir, "overlap")); !os.IsNotExist(err) {
		t.Fatal("replacement overlapped old writer")
	}
	if err := f.agent.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := f.agent.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.thread.Run(context.Background(), "closed"); !errors.Is(err, adaptor.ErrAgentClosed) {
		t.Fatalf("after close: %v", err)
	}
}

func TestRPCPersistentDriftAndRebind(t *testing.T) {
	t.Setenv("AGENT_ADAPTOR_PI_EXCLUSIVE_WRITER", "1")
	f := persistentRPCFixture(t)
	f.run(t, adaptor.WithModel("test/one"))
	f.run(t, adaptor.WithModel("test/two"))
	if len(f.pids(t)) != 2 {
		t.Fatal("model drift did not replace writer")
	}
	if err := os.WriteFile(filepath.Join(f.profile, "settings.json"), []byte(`{"model":"test/three"}`), 0600); err != nil {
		t.Fatal(err)
	}
	f.run(t, adaptor.WithModel("test/two"))
	if len(f.pids(t)) != 3 {
		t.Fatal("native config drift did not replace writer")
	}
	if _, err := os.Stat(filepath.Join(f.dir, "overlap")); !os.IsNotExist(err) {
		t.Fatal("rebind overlapped old writer")
	}
}

func TestRPCPersistentInstructionsAndHostedTools(t *testing.T) {
	definition := tool.Define("echo", "Echo", func(context.Context, struct{}) (struct{}, error) { return struct{}{}, nil }, tool.ReadOnly(), tool.Revision("echo/v1"))
	f := persistentRPCFixture(t, adaptor.WithInstructions("Instruction persists"), adaptor.WithTools(definition))
	f.run(t)
	f.run(t)
	if len(f.pids(t)) != 1 {
		t.Fatal("same instructions/tools restarted server")
	}
	raw, _ := os.ReadFile(filepath.Join(f.dir, "capture.json"))
	var capture struct{ Args []string }
	if err := json.Unmarshal(raw, &capture); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(capture.Args, " "), "Instruction persists") || !strings.Contains(strings.Join(capture.Args, " "), "--extension") {
		t.Fatal("resources absent")
	}

}

func TestRPCPersistentForkAndIndependentThreads(t *testing.T) {
	f := persistentRPCFixture(t)
	parent := f.run(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	branch := f.thread.Fork("branch")
	first, err := branch.Run(ctx, "fork")
	if err != nil {
		t.Fatal(err)
	}
	second, err := branch.Run(ctx, "resume fork")
	if err != nil {
		t.Fatal(err)
	}
	if first.Metadata["pi.rpc_pid"] != second.Metadata["pi.rpc_pid"] || first.Metadata["pi.rpc_pid"] == parent.Metadata["pi.rpc_pid"] {
		t.Fatal("fork process isolation/reuse failed")
	}
	resumed := f.run(t)
	if resumed.Metadata["pi.rpc_pid"] != parent.Metadata["pi.rpc_pid"] {
		t.Fatal("fork replaced parent writer")
	}
	if len(f.pids(t)) != 2 {
		t.Fatal("unexpected fork spawns")
	}
}

func TestRPCPersistentFallbackAndNoReplay(t *testing.T) {
	t.Run("resume-diagnostic-after-delivery", func(t *testing.T) {
		f := persistentRPCFixture(t)
		f.run(t)
		t.Setenv("AGENT_ADAPTOR_PI_FAIL_AFTER_PROMPT", "1")
		if err := os.WriteFile(filepath.Join(f.dir, "stderr"), []byte("No session found matching 'parent'"), 0600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := f.thread.Run(ctx, "must not replay"); err == nil {
			t.Fatal("failed process succeeded")
		}
		raw, _ := os.ReadFile(filepath.Join(f.dir, "prompts.log"))
		if len(strings.Fields(string(raw))) != 2 {
			t.Fatal("post-delivery diagnostic caused prompt replay")
		}
	})
	t.Run("startup", func(t *testing.T) {
		t.Setenv("AGENT_ADAPTOR_PI_FAIL_START_ONCE", "1")
		f := persistentRPCFixture(t)
		r := f.run(t)
		raw, _ := os.ReadFile(filepath.Join(f.dir, "prompts.log"))
		if len(f.pids(t)) != 2 || len(strings.Fields(string(raw))) != 1 || !strings.Contains(r.Raw().Stderr, "injected pre-delivery failure") {
			t.Fatal("startup retry lost audit or replayed prompt")
		}
	})
	t.Run("after-delivery", func(t *testing.T) {
		t.Setenv("AGENT_ADAPTOR_PI_FAIL_AFTER_PROMPT", "1")
		f := persistentRPCFixture(t)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := f.thread.Run(ctx, "do not replay"); err == nil {
			t.Fatal("process exit succeeded")
		}
		raw, _ := os.ReadFile(filepath.Join(f.dir, "prompts.log"))
		if len(f.pids(t)) != 1 || len(strings.Fields(string(raw))) != 1 {
			t.Fatal("prompt was replayed")
		}
		if checkpoint, err := f.thread.Checkpoint(ctx); err == nil && checkpoint != nil {
			t.Fatal("failed invocation persisted a checkpoint")
		}
	})
}

func TestRPCPersistentIdleAndCancelledAcquisition(t *testing.T) {
	f := persistentRPCFixture(t)
	f.pool.idleTimeout = 30 * time.Millisecond
	f.run(t)
	deadline := time.Now().Add(3 * time.Second)
	for testutil.ProcessAlive(f.pids(t)[0]) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if testutil.ProcessAlive(f.pids(t)[0]) {
		t.Fatal("idle server not reaped")
	}
	f.run(t)
	if len(f.pids(t)) != 2 {
		t.Fatal("idle writer was reused")
	}
	p := newRPCPool()
	req := driver.Request{Session: &driver.SessionContext{EngineSessionID: "same", Mode: driver.SessionContinueOrStart}}
	lease, err := p.acquire(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.release()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.acquire(ctx, req); !errors.Is(err, context.Canceled) {
		t.Fatalf("waiting writer cancellation=%v", err)
	}
}

func TestRPCPersistentActiveClose(t *testing.T) {
	t.Run("active-close", func(t *testing.T) {
		f := persistentRPCFixture(t)
		partial := strings.Split(happy, `{"type":"message_end"`)[0]
		if err := os.WriteFile(filepath.Join(f.dir, "stdout"), []byte(partial), 0600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		stream := f.thread.Stream(ctx, "wait")
		for event := range stream.Events() {
			if text, ok := event.(adaptor.TextDelta); ok && text.Text != "" {
				if err := f.agent.Close(ctx); err != nil {
					t.Fatal(err)
				}
				break
			}
		}
		for range stream.Events() {
		}
		if _, err := stream.Result(); err == nil {
			t.Fatal("active close returned success")
		}
	})
}

func TestRPCPersistentNativeSkillDrift(t *testing.T) {
	f := persistentRPCFixture(t)
	f.run(t)
	path := filepath.Join(f.profile, "skills", "proof", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("first contents"), 0600); err != nil {
		t.Fatal(err)
	}
	f.run(t)
	if len(f.pids(t)) != 2 {
		t.Fatal("new native skill did not replace stale writer")
	}
	if err := os.WriteFile(path, []byte("other contents"), 0600); err != nil {
		t.Fatal(err)
	}
	f.run(t)
	if len(f.pids(t)) != 3 {
		t.Fatal("native skill content edit did not replace stale writer")
	}
	f.run(t)
	if len(f.pids(t)) != 3 {
		t.Fatal("unchanged native skills restarted writer")
	}
}

func TestRPCPersistentNativeInstructionDrift(t *testing.T) {
	f := persistentRPCFixture(t)
	f.run(t)
	path := filepath.Join(f.profile, "APPEND_SYSTEM.md")
	if err := os.WriteFile(path, []byte("first instructions"), 0600); err != nil {
		t.Fatal(err)
	}
	f.run(t)
	if len(f.pids(t)) != 2 {
		t.Fatal("new native instructions did not replace stale writer")
	}
	if err := os.WriteFile(path, []byte("other instructions"), 0600); err != nil {
		t.Fatal(err)
	}
	f.run(t)
	if len(f.pids(t)) != 3 {
		t.Fatal("native instruction edit did not replace stale writer")
	}
	f.run(t)
	if len(f.pids(t)) != 3 {
		t.Fatal("unchanged native instructions restarted writer")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	f.run(t)
	if len(f.pids(t)) != 4 {
		t.Fatal("native instruction removal did not replace stale writer")
	}
}
