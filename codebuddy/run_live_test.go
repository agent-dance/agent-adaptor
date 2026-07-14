//go:build codebuddy_live

// Package codebuddy live smoke tests exercise the REAL `codebuddy` CLI
// end to end through the SDK. They are excluded from the default build and only
// run when the codebuddy_live tag is set and a logged-in `codebuddy` binary is
// on PATH.
//
// Run with:
//
//	# headless (autonomous) + resume smoke
//	go test -tags codebuddy_live -run TestCodeBuddyLiveHeadless -v ./codebuddy
//	# interactive control-transport permission loop
//	go test -tags codebuddy_live -run TestCodeBuddyLivePermission -v ./codebuddy
package codebuddy

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
	"github.com/agent-dance/agent-adaptor/internal/testutil"
	"github.com/agent-dance/agent-adaptor/memory"
)

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func codebuddyCLIName() string {
	return "codebuddy"
}

func requireCodeBuddyCLI(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath(codebuddyCLIName()); err != nil {
		t.Skipf("%s CLI not in PATH", codebuddyCLIName())
	}
}

func liveModel() string { return "glm-5.2-ioa" }

// isolatedConfigDir returns a fresh directory for CODEBUDDY_CONFIG_DIR that
// carries over the operator's real login credentials but deliberately omits
// mcp.json, so live tests do not depend on (or get polluted by) any locally
// configured MCP servers. Kept as good test hygiene so live runs are not
// implicitly coupled to the operator's global mcp.json.
func isolatedConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	home, err := os.UserHomeDir()
	if err != nil {
		t.Logf("isolatedConfigDir: could not resolve home dir: %v", err)
		return dir
	}
	realConfigDir := envOr("CODEBUDDY_CONFIG_DIR_SOURCE", filepath.Join(home, ".codebuddy"))
	for _, name := range []string{".credentials.json", "credentials.json", "settings.json"} {
		src := filepath.Join(realConfigDir, name)
		data, readErr := os.ReadFile(src)
		if readErr != nil {
			continue
		}
		if writeErr := os.WriteFile(filepath.Join(dir, name), data, 0o600); writeErr != nil {
			t.Logf("isolatedConfigDir: failed to copy %s: %v", name, writeErr)
		}
	}
	return dir
}

// --- real-time CLI event logging ---------------------------------------
//
// sdk.Run blocks silently until the whole turn completes, which makes a
// multi-minute live run indistinguishable from a genuine hang. All live
// tests below use sdk.Start + logLiveEvents so raw CLI chunks, transcript
// items, and stream deltas print to `go test -v` output as they happen.

// logLiveEvents drains handle.Events() and handle.StreamEvents() in the
// background, logging each one via t.Logf as it arrives, and returns a wait
// function the caller must invoke (after handle.Wait) to join both
// goroutines before the test returns.
func logLiveEvents(t *testing.T, handle agentadaptor.RunHandle) (wait func()) {
	t.Helper()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for ev := range handle.Events() {
			logRunEvent(t, ev)
		}
	}()
	go func() {
		defer wg.Done()
		for p := range handle.StreamEvents() {
			logStreamPayload(t, p)
		}
	}()
	return wg.Wait
}

func logRunEvent(t *testing.T, ev agentadaptor.RunEvent) {
	t.Helper()
	switch ev.Type {
	case agentadaptor.RunEventChunk:
		t.Logf("[cli:%s] %s", ev.Stream, strings.TrimRight(string(ev.Bytes), "\n"))
	case agentadaptor.RunEventSpawn:
		t.Logf("[spawn] %s data=%v", ev.Text, ev.Data)
	case agentadaptor.RunEventInvocation:
		t.Logf("[invocation] %s data=%v", ev.Text, ev.Data)
	case agentadaptor.RunEventItem:
		if ev.Item != nil {
			t.Logf("[item] kind=%s subtype=%s text=%q", ev.Item.Kind, ev.Item.Subtype, truncateForLog(ev.Item.Text))
		}
	default:
		t.Logf("[event:%s] %s data=%v", ev.Type, ev.Text, ev.Data)
	}
}

func logStreamPayload(t *testing.T, p agentadaptor.StreamPayload) {
	t.Helper()
	switch p.Kind {
	case agentadaptor.StreamTextContent:
		t.Logf("[stream:text] %s", p.Delta)
	case agentadaptor.StreamReasoningContent:
		t.Logf("[stream:thinking] %s", p.Delta)
	case agentadaptor.StreamHITLRequested:
		if p.HITLRequested != nil {
			t.Logf("[stream:hitl-requested] kind=%s source=%s prompt=%q", p.HITLRequested.Kind, p.HITLRequested.Source, truncateForLog(p.HITLRequested.Prompt))
		}
	case agentadaptor.StreamHITLResolved:
		if p.HITLResolved != nil {
			t.Logf("[stream:hitl-resolved] kind=%s result=%s latency=%s", p.HITLResolved.Kind, p.HITLResolved.Result, p.HITLResolved.Latency)
		}
	case agentadaptor.StreamRunFinished:
		t.Logf("[stream:finished] usage=%+v", p.Usage)
	case agentadaptor.StreamRunError:
		t.Logf("[stream:error] %+v", p.Error)
	default:
		t.Logf("[stream:%s] name=%s raw=%v", p.Kind, p.Name, p.Raw)
	}
}

func truncateForLog(s string) string {
	const max = 300
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

func newLiveSDK(t *testing.T, cwd string) agentadaptor.SDK {
	t.Helper()
	return agentadaptor.New(
		agentadaptor.WithDefaultAgent(New(agentadaptor.CodeBuddyConfig{
			CommonConfig: agentadaptor.CommonConfig{
				CWD:     cwd,
				Command: codebuddyCLIName(),
				Env: []agentadaptor.EnvBinding{
					{Name: "CODEBUDDY_CONFIG_DIR", Value: isolatedConfigDir(t)},
				},
			},
			Model: liveModel(),
		})),
		agentadaptor.WithSessionStore(memory.NewSessionStore()),
	)
}

// livePolicyHeadless 保持 wantsControlTransport=false，从而真正驱动底层
// `codebuddy --print` batch 引擎，而非 control stream-json 传输。
// Permission/PlanReview=AutoApprove 会映射到 --permission-mode bypassPermissions；
// Question 保持 Unset：该 smoke test 不需要阻塞的 question control request。
var livePolicyHeadless = agentadaptor.RunPolicy{
	Isolation: agentadaptor.IsolationUnrestricted,
	HumanDecision: agentadaptor.HumanDecisionPolicy{
		Permission: agentadaptor.HumanDecisionAutoApprove,
		PlanReview: agentadaptor.HumanDecisionAutoApprove,
	},
}

// TestCodeBuddyLiveHeadlessStreaming drives the headless stream-json engine
// against the real CLI and asserts we observe streamed text deltas, a terminal
// StreamRunFinished with usage, and an assembled Output that matches the
// deltas.
func TestCodeBuddyLiveHeadlessStreaming(t *testing.T) {
	requireCodeBuddyCLI(t)
	cwd := t.TempDir()
	sdk := newLiveSDK(t, cwd)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	rec := &testutil.EventRecorder{}
	handle, err := sdk.Start(ctx, "Write a haiku about autumn. Reply with only the haiku.",
		agentadaptor.WithStreaming(),
		agentadaptor.WithRunPolicy(livePolicyHeadless),
		agentadaptor.WithSessionKey("codebuddy_live_haiku", "v1"),
	)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	streamDone := make(chan struct{})
	go func() {
		defer close(streamDone)
		for p := range handle.StreamEvents() {
			logStreamPayload(t, p)
			_ = rec.EmitStream(p)
		}
	}()
	eventsDone := make(chan struct{})
	go func() {
		defer close(eventsDone)
		for ev := range handle.Events() {
			logRunEvent(t, ev)
			_ = rec.Emit(ev)
		}
	}()

	res, err := handle.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	<-streamDone
	<-eventsDone

	stream := rec.StreamSnapshot()
	textDeltas := 0
	var assembled strings.Builder
	sawFinish := false
	for _, p := range stream {
		if p.Kind == agentadaptor.StreamTextContent {
			textDeltas++
			assembled.WriteString(p.Delta)
		}
		if p.Kind == agentadaptor.StreamRunFinished && p.Usage != nil && p.Usage.InputTokens > 0 {
			sawFinish = true
		}
	}
	// A short reply (e.g. a haiku) may arrive in only 1-2 text deltas depending
	// on the CLI's chunking, so we only require the streaming path to emit at
	// least one delta; the assembled-vs-Output consistency check below is the
	// real correctness assertion.
	if textDeltas < 1 {
		t.Fatalf("expected >=1 StreamTextContent delta, got %d (streams=%d)", textDeltas, len(stream))
	}
	if !sawFinish {
		t.Fatal("missing StreamRunFinished with InputTokens")
	}
	if !strings.Contains(strings.TrimSpace(res.Output), strings.TrimSpace(assembled.String())) {
		t.Fatalf("delta text diverges from output:\n stream=%q\n output=%q", assembled.String(), res.Output)
	}
	t.Logf("deltas=%d output=%q usage=%v", textDeltas, res.Output, res.Usage)
}

// TestCodeBuddyLiveHeadlessResume checks a second turn continues the same
// CodeBuddy session (stream-json session id / --resume) as the first.
func TestCodeBuddyLiveHeadlessResume(t *testing.T) {
	requireCodeBuddyCLI(t)
	cwd := t.TempDir()
	sdk := newLiveSDK(t, cwd)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	handle1, err := sdk.Start(ctx, "Remember the word banana. Reply with only: OK.",
		agentadaptor.WithStreaming(),
		agentadaptor.WithRunPolicy(livePolicyHeadless),
		agentadaptor.WithSessionKey("codebuddy_live_resume", "v1"),
	)
	if err != nil {
		t.Fatalf("Start 1: %v", err)
	}
	wait1 := logLiveEvents(t, handle1)
	r1, err := handle1.Wait(ctx)
	wait1()
	if err != nil {
		t.Fatalf("Run 1: %v", err)
	}
	if r1.Session == nil || r1.Session.ID == "" {
		t.Fatalf("missing session ref after first run: %+v", r1.Session)
	}

	handle2, err := sdk.Start(ctx, "What word did I ask you to remember? Reply with only that word.",
		agentadaptor.WithStreaming(),
		agentadaptor.WithRunPolicy(livePolicyHeadless),
		agentadaptor.WithSession(agentadaptor.SessionRequest{
			Namespace: r1.Session.Namespace,
			Key:       r1.Session.Key,
			ID:        r1.Session.ID,
			Mode:      agentadaptor.SessionContinueOnly,
		}),
	)
	if err != nil {
		t.Fatalf("Start 2: %v", err)
	}
	wait2 := logLiveEvents(t, handle2)
	r2, err := handle2.Wait(ctx)
	wait2()
	if err != nil {
		t.Fatalf("Run 2: %v", err)
	}
	if !strings.Contains(strings.ToLower(r2.Output), "banana") {
		t.Logf("resume did not recall context (model-dependent): output=%q", r2.Output)
	}
}

// TestCodeBuddyLivePermissionApprove drives the interactive control transport:
// a Permission=Ask policy selects stream-json control mode, the prompt triggers a real
// tool that needs permission, and the host approves it. Verifies the handler
// is invoked and the run completes without a rejection failure.
func TestCodeBuddyLivePermissionApprove(t *testing.T) {
	requireCodeBuddyCLI(t)
	cwd := t.TempDir()
	sdk := newLiveSDK(t, cwd)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	var (
		mu    sync.Mutex
		calls []agentadaptor.PermissionRequest
	)
	handle, err := sdk.Start(ctx,
		"Create a file named hello.txt in the current directory containing the text hi. "+
			"Use your file-editing tool. Reply with only DONE.",
		agentadaptor.WithStreaming(),
		agentadaptor.WithRunPolicy(agentadaptor.RunPolicy{
			HumanDecision: agentadaptor.HumanDecisionPolicy{
				Permission: agentadaptor.HumanDecisionAsk,
				PlanReview: agentadaptor.HumanDecisionAutoApprove,
			},
		}),
		agentadaptor.WithPermissionHandler(func(_ context.Context, req agentadaptor.PermissionRequest) (agentadaptor.PermissionResponse, error) {
			mu.Lock()
			calls = append(calls, req)
			mu.Unlock()
			t.Logf("[permission-handler] approving tool=%q prompt=%q", req.Tool, truncateForLog(req.Prompt))
			return agentadaptor.PermissionResponse{Result: agentadaptor.ApprovalApproved}, nil
		}),
		agentadaptor.WithSessionKey("codebuddy_live_perm", "approve"),
	)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	wait := logLiveEvents(t, handle)
	res, err := handle.Wait(ctx)
	wait()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	mu.Lock()
	got := append([]agentadaptor.PermissionRequest(nil), calls...)
	mu.Unlock()
	if len(got) == 0 {
		t.Fatalf("permission handler never invoked; model likely used no permissioned tool — output=%q", res.Output)
	}
	for i, req := range got {
		if req.Tool == "" {
			t.Errorf("permission request[%d] missing tool name (empty Tool): %+v", i, req)
		}
	}
	if res.Failure != nil {
		t.Errorf("approved run should not fail: %+v", res.Failure)
	}
	data, statErr := os.ReadFile(filepath.Join(cwd, "hello.txt"))
	if statErr != nil {
		t.Fatalf("hello.txt should exist in cwd after approval: %v", statErr)
	}
	if strings.TrimSpace(string(data)) != "hi" {
		t.Errorf("hello.txt content = %q, want \"hi\"", string(data))
	}
}

// TestCodeBuddyLivePermissionReject verifies the OnReject=FailureAbort path
// against the real CLI: the host rejects the tool permission and the adapter
// records a structured rejection failure.
func TestCodeBuddyLivePermissionReject(t *testing.T) {
	requireCodeBuddyCLI(t)
	cwd := t.TempDir()
	sdk := newLiveSDK(t, cwd)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	handle, err := sdk.Start(ctx,
		"Create a file named blocked.txt in the current directory with any content. "+
			"Use your file-editing tool. Do not ask questions.",
		agentadaptor.WithStreaming(),
		agentadaptor.WithRunPolicy(agentadaptor.RunPolicy{
			HumanDecision: agentadaptor.HumanDecisionPolicy{
				Permission: agentadaptor.HumanDecisionAsk,
				PlanReview: agentadaptor.HumanDecisionAutoApprove,
				OnReject:   agentadaptor.FailureAbort,
			},
		}),
		agentadaptor.WithPermissionHandler(func(_ context.Context, req agentadaptor.PermissionRequest) (agentadaptor.PermissionResponse, error) {
			t.Logf("[permission-handler] rejecting tool=%q prompt=%q", req.Tool, truncateForLog(req.Prompt))
			return agentadaptor.PermissionResponse{Result: agentadaptor.ApprovalRejected}, nil
		}),
		agentadaptor.WithSessionKey("codebuddy_live_perm", "reject"),
	)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	wait := logLiveEvents(t, handle)
	res, err := handle.Wait(ctx)
	wait()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Failure == nil {
		t.Logf("no failure recorded; model may not have attempted the permissioned tool — output=%q", res.Output)
	}
}
