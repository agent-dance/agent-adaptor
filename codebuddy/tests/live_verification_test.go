//go:build codebuddy_live

package tests

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/codebuddy"
	"github.com/agent-dance/agent-adaptor/memory"
)

func newLiveSDK(t *testing.T, cwd string, options ...agentadaptor.AgentOption) agentadaptor.SDK {
	t.Helper()
	command := requireCodeBuddyCLI(t)
	return newHostSDK(t, cwd, command, isolatedConfigDir(t), options...)
}

func TestLiveHostStartHeadlessStreamingAndResume(t *testing.T) {
	cwd := t.TempDir()
	sdk := newLiveSDK(t, cwd)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	permissionHandlerCalls := 0

	first := startAndObserve(t, sdk, "Remember the exact word: kumquat. Reply with only OK.",
		agentadaptor.WithStreaming(),
		agentadaptor.WithRunPolicy(headlessPolicy()),
		agentadaptor.WithPermissionHandler(func(_ context.Context, _ agentadaptor.PermissionRequest) (agentadaptor.PermissionResponse, error) {
			permissionHandlerCalls++
			return agentadaptor.PermissionResponse{Result: agentadaptor.ApprovalRejected}, nil
		}),
		agentadaptor.WithSessionKey("live-driver-verification", "resume"),
	)
	if first.result.Failure != nil || strings.TrimSpace(first.result.Output) == "" {
		t.Fatalf("first headless result = %+v", first.result)
	}
	if first.result.Usage == nil || first.result.Usage.InputTokens == 0 {
		t.Fatalf("first usage = %+v, want non-zero input tokens", first.result.Usage)
	}
	if first.result.Session == nil || first.result.Session.DisplayID == "" {
		t.Fatalf("first session = %+v", first.result.Session)
	}
	if permissionHandlerCalls != 0 {
		t.Fatalf("headless AutoApprove called permission handler %d times", permissionHandlerCalls)
	}
	handle, err := sdk.Start(ctx, "What word did I ask you to remember? Reply with only that word.",
		agentadaptor.WithStreaming(),
		agentadaptor.WithRunPolicy(headlessPolicy()),
		agentadaptor.WithSession(agentadaptor.SessionRequest{
			Namespace: first.result.Session.Namespace,
			Key:       first.result.Session.Key,
			ID:        first.result.Session.ID,
			Mode:      agentadaptor.SessionContinueOnly,
		}),
	)
	if err != nil {
		t.Fatalf("resume Start: %v", err)
	}
	second, err := handle.Wait(ctx)
	if err != nil {
		t.Fatalf("resume Wait: %v", err)
	}
	if second.Failure != nil {
		t.Fatalf("resume failure = %+v", second.Failure)
	}
	if !strings.Contains(strings.ToLower(second.Output), "kumquat") {
		t.Fatalf("resume output = %q, want remembered kumquat", second.Output)
	}
}

func TestLiveHostStartPermissionApproveUsesConfiguredCWD(t *testing.T) {
	cwd := t.TempDir()
	sdk := newLiveSDK(t, cwd)
	var (
		mu       sync.Mutex
		requests []agentadaptor.PermissionRequest
	)
	got := startAndObserve(t, sdk,
		"Create hello.txt in the current directory containing exactly hi. Use the file editing tool. Reply only DONE.",
		agentadaptor.WithStreaming(),
		agentadaptor.WithRunPolicy(agentadaptor.RunPolicy{
			HumanDecision: agentadaptor.HumanDecisionPolicy{
				Permission: agentadaptor.HumanDecisionAsk,
				PlanReview: agentadaptor.HumanDecisionAutoApprove,
			},
		}),
		agentadaptor.WithPermissionHandler(func(_ context.Context, request agentadaptor.PermissionRequest) (agentadaptor.PermissionResponse, error) {
			mu.Lock()
			requests = append(requests, request)
			mu.Unlock()
			return agentadaptor.PermissionResponse{Result: agentadaptor.ApprovalApproved}, nil
		}),
	)
	if got.result.Failure != nil {
		t.Fatalf("approved run failure = %+v", got.result.Failure)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) == 0 {
		t.Fatalf("permission handler was not called; output=%q", got.result.Output)
	}
	if requests[0].Tool == "" || requests[0].Prompt == "" {
		t.Fatalf("permission context is incomplete: %+v", requests[0])
	}
	raw, err := os.ReadFile(filepath.Join(cwd, "hello.txt"))
	if err != nil {
		t.Fatalf("hello.txt missing from configured cwd %q: %v", cwd, err)
	}
	if strings.TrimSpace(string(raw)) != "hi" {
		t.Fatalf("hello.txt = %q, want hi", raw)
	}
}

func TestLiveHostStartPermissionRejectIsEnforced(t *testing.T) {
	cwd := t.TempDir()
	sdk := newLiveSDK(t, cwd)
	calls := 0
	got := startAndObserve(t, sdk,
		"Create blocked.txt in the current directory. Use the file editing tool.",
		agentadaptor.WithRunPolicy(agentadaptor.RunPolicy{
			HumanDecision: agentadaptor.HumanDecisionPolicy{
				Permission: agentadaptor.HumanDecisionAsk,
				OnReject:   agentadaptor.FailureAbort,
			},
		}),
		agentadaptor.WithPermissionHandler(func(_ context.Context, _ agentadaptor.PermissionRequest) (agentadaptor.PermissionResponse, error) {
			calls++
			return agentadaptor.PermissionResponse{Result: agentadaptor.ApprovalRejected}, nil
		}),
	)
	if calls == 0 {
		t.Fatal("permission handler was not called")
	}
	if got.result.Failure == nil || got.result.Failure.Code != agentadaptor.FailureReject {
		t.Fatalf("failure = %+v, want FailureReject", got.result.Failure)
	}
	if _, err := os.Stat(filepath.Join(cwd, "blocked.txt")); !os.IsNotExist(err) {
		t.Fatalf("blocked.txt exists after reject: %v", err)
	}
}

func TestLiveHostStartAutoRejectIsEnforcedWithoutHandler(t *testing.T) {
	cwd := t.TempDir()
	sdk := newLiveSDK(t, cwd)
	handlerCalls := 0
	got := startAndObserve(t, sdk,
		"Create auto-blocked.txt in the current directory. Use the file editing tool.",
		agentadaptor.WithRunPolicy(agentadaptor.RunPolicy{
			HumanDecision: agentadaptor.HumanDecisionPolicy{
				Permission: agentadaptor.HumanDecisionAutoReject,
				OnReject:   agentadaptor.FailureAbort,
			},
		}),
		agentadaptor.WithPermissionHandler(func(_ context.Context, _ agentadaptor.PermissionRequest) (agentadaptor.PermissionResponse, error) {
			handlerCalls++
			return agentadaptor.PermissionResponse{Result: agentadaptor.ApprovalApproved}, nil
		}),
	)
	if handlerCalls != 0 {
		t.Fatalf("AutoReject called permission handler %d times", handlerCalls)
	}
	if got.result.Failure == nil || got.result.Failure.Code != agentadaptor.FailureReject {
		t.Fatalf("failure = %+v, want FailureReject", got.result.Failure)
	}
	if _, err := os.Stat(filepath.Join(cwd, "auto-blocked.txt")); !os.IsNotExist(err) {
		t.Fatalf("auto-blocked.txt exists after AutoReject: %v", err)
	}
}

func TestLiveHostStartPlanReviewIsRoutedToPlanHandler(t *testing.T) {
	sdk := newLiveSDK(t, t.TempDir())
	var (
		mu    sync.Mutex
		plans []agentadaptor.PlanReviewRequest
	)
	got := startAndObserve(t, sdk,
		"Create a one-step plan to add hello.txt, then submit it with ExitPlanMode. Do not write hello.txt.",
		agentadaptor.WithRunPolicy(agentadaptor.RunPolicy{
			HumanDecision: agentadaptor.HumanDecisionPolicy{
				Permission: agentadaptor.HumanDecisionAsk,
				PlanReview: agentadaptor.HumanDecisionAsk,
			},
		}),
		agentadaptor.WithPermissionHandler(func(_ context.Context, _ agentadaptor.PermissionRequest) (agentadaptor.PermissionResponse, error) {
			return agentadaptor.PermissionResponse{Result: agentadaptor.ApprovalApproved}, nil
		}),
		agentadaptor.WithPlanReviewHandler(func(_ context.Context, request agentadaptor.PlanReviewRequest) (agentadaptor.PlanReviewResponse, error) {
			mu.Lock()
			plans = append(plans, request)
			mu.Unlock()
			return agentadaptor.PlanReviewResponse{Result: agentadaptor.ApprovalApproved}, nil
		}),
	)
	if got.result.Failure != nil {
		t.Fatalf("plan review run failure = %+v", got.result.Failure)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(plans) == 0 {
		t.Fatalf("plan handler was not called; output=%q", got.result.Output)
	}
	if plans[0].Prompt == "" {
		t.Fatalf("plan request has empty prompt: %+v", plans[0])
	}
	if strings.TrimSpace(plans[0].Plan) == "" {
		t.Fatalf("plan request has empty plan content: %+v", plans[0])
	}
}

func TestLiveHostMCPIsMaterializedAndUsed(t *testing.T) {
	cwd := t.TempDir()
	configDir := isolatedConfigDir(t)
	server := buildMCPServer(t)
	mcp := agentadaptor.MCPConfig{Servers: []agentadaptor.MCPServerSpec{{
		Key:       "codebuddy-driver-test-mcp",
		Transport: agentadaptor.MCPTransportStdio,
		Command:   server,
		Required:  true,
	}}}
	sdk := newHostSDK(t, cwd, requireCodeBuddyCLI(t), configDir, agentadaptor.WithDefaultMCP(mcp))

	got := startAndObserve(t, sdk,
		"Call the echo_marker MCP tool and reply with its exact response only.",
		agentadaptor.WithStreaming(),
		agentadaptor.WithRunPolicy(headlessPolicy()),
	)
	if got.result.Failure != nil {
		t.Fatalf("MCP run failure = %+v", got.result.Failure)
	}
	if !strings.Contains(got.result.Output, "CODEBUDDY_DRIVER_MCP_MARKER") {
		t.Fatalf("MCP marker absent from output %q", got.result.Output)
	}
	raw, err := os.ReadFile(filepath.Join(configDir, "mcp.json"))
	if err != nil {
		t.Fatalf("read materialized mcp.json: %v", err)
	}
	if !strings.Contains(string(raw), `"codebuddy-driver-test-mcp"`) {
		t.Fatalf("mcp.json does not contain managed server:\n%s", raw)
	}
	snapshot, err := sdk.Admin().Default().SyncProfile(context.Background())
	if err != nil {
		t.Fatalf("SyncProfile: %v", err)
	}
	if !snapshotHasManaged(snapshot, agentadaptor.ProfileResourceMCP, "codebuddy-driver-test-mcp") {
		t.Fatalf("MCP snapshot does not report the managed server: %+v", snapshot.Resources)
	}
}

func TestLiveHostCleanProfileDoesNotReportTestResources(t *testing.T) {
	sdk := newLiveSDK(t, t.TempDir())
	snapshot, err := sdk.Admin().Default().SyncProfile(context.Background())
	if err != nil {
		t.Fatalf("SyncProfile: %v", err)
	}
	for _, resource := range snapshot.Resources {
		for _, key := range append(append([]string(nil), resource.Managed...), resource.External...) {
			if key == "codebuddy-driver-test-mcp" || key == "codebuddy-driver-test-skill" {
				t.Fatalf("clean profile unexpectedly reports test resource %q: %+v", key, resource)
			}
		}
	}
}

func snapshotHasManaged(snapshot agentadaptor.ProfileSnapshot, kind agentadaptor.ProfileResourceKind, key string) bool {
	for _, resource := range snapshot.Resources {
		if resource.Kind != kind {
			continue
		}
		for _, managed := range resource.Managed {
			if managed == key {
				return true
			}
		}
	}
	return false
}

func TestLiveHostSkillIsInstalledAndUsed(t *testing.T) {
	cwd := t.TempDir()
	configDir := isolatedConfigDir(t)
	skill := agentadaptor.InlineSkill("codebuddy-driver-test-skill", `---
name: codebuddy-driver-test-skill
description: Return the CodeBuddy driver skill marker when explicitly invoked.
---
# CodeBuddy Driver Test Skill

When the user asks to use this skill, reply with exactly:
CODEBUDDY_DRIVER_SKILL_MARKER
`)
	cfg := agentadaptor.CodeBuddyConfig{
		CommonConfig: agentadaptor.CommonConfig{
			CWD:     cwd,
			Command: requireCodeBuddyCLI(t),
			Env:     []agentadaptor.EnvBinding{{Name: "CODEBUDDY_CONFIG_DIR", Value: configDir}},
		},
		Model: liveModel,
	}
	sdk := agentadaptor.New(
		agentadaptor.WithSkillSet(agentadaptor.SkillSet{skill.Key: skill}),
		agentadaptor.WithDefaultAgent(codebuddy.New(cfg, agentadaptor.WithDefaultSkills(skill))),
		agentadaptor.WithSessionStore(memory.NewSessionStore()),
	)

	got := startAndObserve(t, sdk,
		"Use the codebuddy-driver-test-skill skill now. Follow its instruction exactly.",
		agentadaptor.WithRunPolicy(headlessPolicy()),
	)
	if got.result.Failure != nil {
		t.Fatalf("skill run failure = %+v", got.result.Failure)
	}
	if !strings.Contains(got.result.Output, "CODEBUDDY_DRIVER_SKILL_MARKER") {
		t.Fatalf("skill marker absent from output %q", got.result.Output)
	}
	snapshot, err := sdk.Admin().Default().ListSkills(context.Background())
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	for _, entry := range snapshot.Entries {
		if entry.Key == skill.Key && entry.State == agentadaptor.SkillStateInstalled {
			return
		}
	}
	t.Fatalf("skill %q was not installed in snapshot: %+v", skill.Key, snapshot.Entries)
}
