package opencode

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

type serverFixture struct {
	agent        *adaptor.Agent
	thread       *adaptor.Thread
	pool         *serverPool
	dir, profile string
}

func persistentServerFixture(t *testing.T, opts ...adaptor.Option) *serverFixture {
	t.Helper()
	t.Setenv("AGENT_ADAPTOR_OPENCODE_DYNAMIC_SESSIONS", "1")
	command, capture := testutil.ProtocolCommand(t, serverHappy, "server stderr", 0)
	profile := t.TempDir()
	d := Driver(Config{Command: command, Model: "test/model", Env: []driver.EnvBinding{{Name: "OPENCODE_CONFIG_DIR", Value: profile}}}).(configuredDriver)
	opts = append([]adaptor.Option{adaptor.WithWorkspace(t.TempDir()), adaptor.WithThreadStore(memory.NewStore())}, opts...)
	a := adaptor.New(d, opts...)
	f := &serverFixture{agent: a, thread: a.Thread("thread"), pool: d.pool, dir: filepath.Dir(capture), profile: profile}
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
func (f *serverFixture) pids(t *testing.T) []int {
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
func (f *serverFixture) run(t *testing.T, opts ...adaptor.CallOption) *adaptor.Result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r, err := f.thread.Run(ctx, "hello", opts...)
	if err != nil {
		t.Fatal(err)
	}
	if r.Text != "OK" || r.Raw().Terminal == nil || r.Metadata["opencode.event_stream"] == "" {
		t.Fatalf("response=%+v", r)
	}
	return r
}

func TestServerPersistentDefaultSpawnAndClose(t *testing.T) {
	t.Setenv("AGENT_ADAPTOR_OPENCODE_EXCLUSIVE_WRITER", "1")
	f := persistentServerFixture(t)
	a, b := f.run(t), f.run(t)
	if len(f.pids(t)) != 1 || a.Metadata["opencode.server_pid"] != b.Metadata["opencode.server_pid"] || b.Metadata["opencode.server_reused"] != "true" {
		t.Fatal("Thread did not reuse its server")
	}
	if b.Raw().Stdout != "" || b.Raw().Stderr != "" {
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

func TestServerPersistentDriftAndRebind(t *testing.T) {
	t.Setenv("AGENT_ADAPTOR_OPENCODE_EXCLUSIVE_WRITER", "1")
	f := persistentServerFixture(t)
	f.run(t, adaptor.WithModel("test/one"))
	f.run(t, adaptor.WithModel("test/two"))
	if len(f.pids(t)) != 2 {
		t.Fatal("model drift did not replace writer")
	}
	if err := os.WriteFile(filepath.Join(f.profile, "opencode.json"), []byte(`{"model":"test/three"}`), 0600); err != nil {
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

func TestServerPersistentInstructionsAndHostedTools(t *testing.T) {
	definition := tool.Define("echo", "Echo", func(context.Context, struct{}) (struct{}, error) { return struct{}{}, nil }, tool.ReadOnly(), tool.Revision("echo/v1"))
	f := persistentServerFixture(t, adaptor.WithInstructions("Instruction persists"), adaptor.WithTools(definition))
	f.run(t)
	f.run(t)
	if len(f.pids(t)) != 1 {
		t.Fatal("same instructions/tools restarted server")
	}
	raw, _ := os.ReadFile(filepath.Join(f.dir, "capture.json"))
	var capture struct {
		Input  map[string]any
		Config string
	}
	if err := json.Unmarshal(raw, &capture); err != nil {
		t.Fatal(err)
	}
	if capture.Input["system"] != "Instruction persists" || !strings.Contains(capture.Config, "mcp") {
		t.Fatalf("current resources absent: %+v", capture)
	}
}

func TestServerPersistentForkAndIndependentThreads(t *testing.T) {
	f := persistentServerFixture(t)
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
	if first.Metadata["opencode.server_pid"] != second.Metadata["opencode.server_pid"] || first.Metadata["opencode.server_pid"] == parent.Metadata["opencode.server_pid"] {
		t.Fatal("fork process isolation/reuse failed")
	}
	resumed := f.run(t)
	if resumed.Metadata["opencode.server_pid"] != parent.Metadata["opencode.server_pid"] {
		t.Fatal("fork replaced parent writer")
	}
	if len(f.pids(t)) != 2 {
		t.Fatal("unexpected fork spawns")
	}
}

func TestServerPersistentFallbackAndNoReplay(t *testing.T) {
	t.Run("startup", func(t *testing.T) {
		t.Setenv("AGENT_ADAPTOR_OPENCODE_FAIL_START_ONCE", "1")
		f := persistentServerFixture(t)
		r := f.run(t)
		raw, _ := os.ReadFile(filepath.Join(f.dir, "prompts.log"))
		if len(f.pids(t)) != 2 || len(strings.Fields(string(raw))) != 1 || !strings.Contains(r.Raw().Stderr, "injected pre-delivery failure") {
			t.Fatal("startup retry lost audit or replayed prompt")
		}
	})
	t.Run("after-delivery", func(t *testing.T) {
		t.Setenv("AGENT_ADAPTOR_OPENCODE_FAIL_AFTER_PROMPT", "1")
		f := persistentServerFixture(t)
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

func TestServerPersistentIdleAndCancelledAcquisition(t *testing.T) {
	f := persistentServerFixture(t)
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
	p := newServerPool()
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

func TestServerPersistentPolicyResetAndActiveClose(t *testing.T) {
	t.Run("policy-reset", func(t *testing.T) {
		t.Setenv("AGENT_ADAPTOR_OPENCODE_EXCLUSIVE_WRITER", "1")
		f := persistentServerFixture(t)
		policy := adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAsk}})
		first := f.run(t, policy)
		second := f.run(t, policy)
		if len(f.pids(t)) != 2 || first.Metadata["opencode.server_pid"] != second.Metadata["opencode.server_pid"] || second.Metadata["opencode.server_reused"] != "true" {
			t.Fatal("compatible gated turns did not reuse the verified execution server")
		}
		raw, _ := os.ReadFile(filepath.Join(f.dir, "session-permission.json"))
		if !strings.Contains(string(raw), `"action":"ask"`) {
			t.Fatal("Ask did not bind native permission gates")
		}
		third := f.run(t)
		raw, _ = os.ReadFile(filepath.Join(f.dir, "session-permissions.json"))
		var stored map[string][]permissionRule
		if err := json.Unmarshal(raw, &stored); err != nil {
			t.Fatal(err)
		}
		for _, rules := range stored {
			if permissionOutcome(rules, "read", "marker.txt") != "allow" || permissionOutcome(rules, "read", "secret.env") != "deny" {
				t.Fatalf("policy restoration lost native precedence: %s", raw)
			}
		}
		if len(f.pids(t)) != 3 || third.Metadata["opencode.server_pid"] == second.Metadata["opencode.server_pid"] {
			t.Fatal("policy reset retained per-agent permission gates")
		}
		for _, pid := range f.pids(t)[:2] {
			if testutil.ProcessAlive(pid) {
				t.Fatal("previous gated writer survived replacement")
			}
		}
		if _, err := os.Stat(filepath.Join(f.dir, "overlap")); !os.IsNotExist(err) {
			t.Fatal("policy replacement overlapped writers")
		}
	})
	t.Run("active-close", func(t *testing.T) {
		f := persistentServerFixture(t)
		partial := strings.Split(serverHappy, `{"type":"message.part.updated","properties":{"sessionID":"ses_child","part":{"id":"finish"`)[0]
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

func TestServerPersistentNativeSkillDrift(t *testing.T) {
	f := persistentServerFixture(t)
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

func TestServerPersistentNativeInstructionDrift(t *testing.T) {
	f := persistentServerFixture(t)
	f.run(t)
	path := filepath.Join(f.profile, "AGENTS.md")
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
