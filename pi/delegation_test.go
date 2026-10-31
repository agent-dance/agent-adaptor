package pi

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/adaptertest"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/testutil"
)

func delegatedRunFixture(t *testing.T, output string) (*delegationRun, *testutil.EventRecorder, string) {
	t.Helper()
	command, capture := testutil.ProtocolCommand(t, output, "child stderr", 0)
	cfg := Config{Command: command, Env: []driver.EnvBinding{{Name: "PI_CODING_AGENT_DIR", Value: t.TempDir()}}}
	d := Driver(cfg).(configuredDriver)
	t.Cleanup(func() { _ = d.CloseProcesses(context.Background()) })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	req := driver.Request{Streaming: true}
	req.ProfilePayload.Agents.Agents = []driver.AgentSpec{{Key: "worker", RuntimeName: "worker", Instructions: "isolated instructions", Model: "provider/child-model", ReasoningEffort: "low"}}
	r := &delegationRun{ctx: ctx, cancel: cancel, driver: d, config: cfg, request: req, resources: map[string]agentResources{"worker": {}}, calls: map[string]bool{}}
	sink := &testutil.EventRecorder{}
	r.sink = &invocationSink{base: sink, owner: r}
	t.Cleanup(r.stop)
	return r, sink, capture
}

func observeDelegation(r *delegationRun, id, task string) {
	_ = r.sink.Emit(driver.RunEvent{Type: driver.RunEventItem, Item: &driver.TranscriptItem{Kind: driver.TranscriptToolCall, ToolUseID: id, ToolName: subagentTool, Input: map[string]any{"agent": "worker", "task": task}}})
}

func TestDelegationParallelAuditAndAuthority(t *testing.T) {
	r, sink, capture := delegatedRunFixture(t, happy)
	_ = r.sink.EmitStream(driver.StreamPayload{Kind: driver.StreamRunStarted})
	var wg sync.WaitGroup
	for _, id := range []string{"one", "two"} {
		observeDelegation(r, id, "child task")
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			response, err := r.execute(r.ctx, id, "worker", "child task")
			if err != nil || response.Failure != nil || response.Output != "OK" {
				t.Errorf("child=%+v err=%v", response, err)
			}
		}(id)
	}
	wg.Wait()
	if _, err := r.execute(r.ctx, "one", "worker", "child task"); err == nil {
		t.Fatal("duplicate child executed")
	}
	observeDelegation(r, "different", "original")
	if _, err := r.execute(r.ctx, "different", "worker", "tampered"); err == nil {
		t.Fatal("unobserved task input executed")
	}
	if _, err := r.execute(r.ctx, "unknown", "unknown", "task"); err == nil {
		t.Fatal("unknown agent executed")
	}
	_ = r.sink.EmitStream(driver.StreamPayload{Kind: driver.StreamRunFinished, Usage: &driver.Usage{InputTokens: 7}})
	response := driver.Response{Output: "parent answer", Usage: &driver.Usage{InputTokens: 7}, RawStreams: &driver.RawStreams{Stdout: "parent stdout"}}
	r.finish(&response)
	if response.Output != "parent answer" || response.Usage.InputTokens != 17 || !strings.HasPrefix(response.RawStreams.Stdout, "parent stdout") || strings.Count(response.RawStreams.Stderr, "child stderr") != 2 {
		t.Fatalf("merged audit=%+v", response)
	}
	var reports []childReport
	if json.Unmarshal([]byte(response.Metadata["pi.child_runs"]), &reports) != nil || len(reports) != 2 || reports[0].Raw.Terminal == nil || reports[1].Raw.Terminal == nil {
		t.Fatal("child terminal audit missing")
	}
	children := map[string]bool{}
	for _, item := range response.Transcript {
		if item.Metadata["pi.agent"] == "worker" {
			children[item.Metadata["pi.parent_tool_call_id"]] = true
			if item.SessionID == "" && item.Kind != driver.TranscriptSystem {
				t.Fatal("child session audit missing")
			}
		}
	}
	if len(children) != 2 {
		t.Fatal("child transcript missing")
	}
	for _, violation := range adaptertest.VerifyStreamSequence(sink.StreamSnapshot()) {
		t.Error(violation)
	}
	events := sink.StreamSnapshot()
	if events[len(events)-1].Usage.InputTokens != response.Usage.InputTokens {
		t.Fatal("terminal usage differs from Result")
	}
	var invocation struct {
		Args   []string
		Prompt string
	}
	raw, _ := os.ReadFile(capture)
	if err := json.Unmarshal(raw, &invocation); err != nil {
		t.Fatal(err)
	}
	args := strings.Join(invocation.Args, " ")
	if !strings.Contains(args, "--provider provider --model child-model --thinking low") || !strings.Contains(args, "--no-session") || !strings.Contains(args, "--system-prompt isolated instructions") || invocation.Prompt != "child task" {
		t.Fatalf("child invocation=%+v", invocation)
	}
}

func TestDelegationCancellationAndFailureAudit(t *testing.T) {
	for _, mode := range []string{"malformed", "cancel", "unobserved"} {
		t.Run(mode, func(t *testing.T) {
			output := happy
			if mode == "malformed" {
				output = "{broken\n" + happy
			}
			r, sink, capture := delegatedRunFixture(t, output)
			if mode == "cancel" {
				t.Setenv("AGENT_ADAPTOR_PROTOCOL_HANG", "1")
			}
			if mode != "unobserved" {
				observeDelegation(r, "one", "task")
			}
			if mode == "malformed" {
				response, err := r.execute(r.ctx, "one", "worker", "task")
				if err != nil || response.Failure == nil || response.Checkpoint != nil {
					t.Fatalf("malformed: %+v %v", response, err)
				}
				return
			}
			done := make(chan error, 1)
			go func() { _, err := r.execute(r.ctx, "one", "worker", "task"); done <- err }()
			if mode == "cancel" {
				deadline := time.Now().Add(3 * time.Second)
				for time.Now().Before(deadline) {
					if _, err := os.Stat(capture); err == nil {
						break
					}
					time.Sleep(5 * time.Millisecond)
				}
				if _, err := os.Stat(capture); err != nil {
					t.Fatal("child did not start")
				}
			}
			r.stop()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) && mode == "cancel" {
					t.Fatalf("cancel=%v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("child survived cancellation")
			}
			for _, event := range sink.StreamSnapshot() {
				if event.Kind == driver.StreamRunFinished || event.Kind == driver.StreamRunStarted || event.Kind == driver.StreamRunError {
					t.Fatal("child opened a second run lifecycle")
				}
			}
		})
	}
}

func TestChildApprovalCorrelation(t *testing.T) {
	base := &interactionSink{decide: func(_ context.Context, request driver.DecisionRequest) (driver.DecisionResponse, error) {
		if request.ToolCallID != childID("parent", "native") {
			t.Errorf("tool=%s", request.ToolCallID)
		}
		return driver.DecisionResponse{Result: driver.DecisionApproved}, nil
	}}
	sink := &childSink{parent: &invocationSink{base: base}, callID: "parent"}
	if _, err := sink.RequestDecision(context.Background(), driver.DecisionRequest{ToolCallID: "native"}); err != nil {
		t.Fatal(err)
	}
}

func TestDelegationInheritsObservedParentModel(t *testing.T) {
	r, _, capture := delegatedRunFixture(t, happy)
	r.config.Provider, r.config.Model = "configured", "original"
	r.request.ProfilePayload.Agents.Agents[0].Model = ""
	p := newParser(r.sink, false)
	for _, line := range strings.Split(happy, "\n") {
		p.line([]byte(line))
	}
	observeDelegation(r, "inherit", "task")
	if _, err := r.execute(r.ctx, "inherit", "worker", "task"); err != nil {
		t.Fatal(err)
	}
	var call struct{ Args []string }
	raw, _ := os.ReadFile(capture)
	if err := json.Unmarshal(raw, &call); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(call.Args, " "), "--provider test --model test-model") {
		t.Fatalf("inherited stale construction config: %v", call.Args)
	}
}

func TestSubagentToolPolicyExtension(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node required to execute the official extension contract")
	}
	bridge := &mcpBridge{scope: &agentScope{policy: &driver.AgentToolPolicy{Allow: []string{"read", "write"}, Deny: []string{"write"}}}}
	source, err := bridge.extensionSource("url", "token")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "extension.mjs"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	harness := `import assert from "node:assert/strict";
import extension from "./extension.mjs";
const handlers = new Map(); let active = ["read","write","bash","mcp_secret","untrusted_extension_tool"];
extension({on(name,fn){const list=handlers.get(name)||[];list.push(fn);handlers.set(name,list)},registerTool(){},getActiveTools(){return active},setActiveTools(names){active=names}});
for (const handler of handlers.get("session_start")) await handler();
assert.deepEqual(active,["read"]);
for (const toolName of ["write","bash","mcp_secret","untrusted_extension_tool"]) {
  const result = await handlers.get("tool_call")[0]({toolName}); assert.equal(result.block,true);
}
assert.equal(await handlers.get("tool_call")[0]({toolName:"read"}),undefined);
`
	file := filepath.Join(root, "verify.mjs")
	if err := os.WriteFile(file, []byte(harness), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(ctx, node, file).CombinedOutput(); err != nil {
		t.Fatalf("extension: %s %v", output, err)
	}
}
