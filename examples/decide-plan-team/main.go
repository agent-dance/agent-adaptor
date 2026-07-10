package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/claude"
	"github.com/agent-dance/agent-adaptor/memory"
	bridgea2a "github.com/agent-dance/agent-adaptor/pkg/bridges/a2a"
	clienta2a "github.com/agent-dance/agent-adaptor/pkg/clients/a2a"
	"github.com/agent-dance/agent-adaptor/pkg/hosttools/a2adelegation"
)

const (
	agentCardPath = "/.well-known/agent-card.json"
	jsonRPCPath   = "/a2a"
)

// DesignPlanRequest 直接对齐 Team Mode 文档里的 DesignPlanRequest。
type DesignPlanRequest struct {
	RequirementTitle          string `json:"requirement_title"`
	RequirementContent        string `json:"requirement_content"`
	RequirementAttachmentPath string `json:"requirement_attachment_path"`
	PrototypePath             string `json:"prototype_path"`
	WorkspacePath             string `json:"workspace_path"`
}

// DesignPlanArtifact 直接对齐 Team Mode 文档里的 DesignPlanArtifact。
type DesignPlanArtifact struct {
	PlanContent string `json:"plan_content"`
}

// DesignPlanToolResult 是 leader 看到的 delegate_to_plan_designer MCP 调用结果。
//
// 这里不只返回 DataPart，而是把制品里的两部分内容都带出来：
// - text_part：制品里的 TextPart
// - data_part：制品里的 DataPart
type DesignPlanToolResult struct {
	ArtifactName string             `json:"artifact_name"`
	TextPart     string             `json:"text_part"`
	DataPart     DesignPlanArtifact `json:"data_part"`
}

// designPlanModelOutput 是 member 内部给 Claude 约束的结构化输出。
//
// 注意：
// 1. 这不是对外 A2A 契约
// 2. 对外仍然只暴露 TextPart + DesignPlanArtifact(DataPart)
// 3. 这里多加一个 summary，只是为了让模型同时产出“摘要”和“正式 artifact”
type designPlanModelOutput struct {
	Summary  string             `json:"summary"`
	Artifact DesignPlanArtifact `json:"artifact"`
}

// ImplementRequirementRequest 直接对齐 Team Mode 文档里的 ImplementRequirementRequest。
type ImplementRequirementRequest struct {
	PlanContent   string `json:"plan_content"`
	WorkspacePath string `json:"workspace_path"`
}

// ImplementRequirementArtifact 直接对齐 Team Mode 文档里的 ImplementRequirementArtifact。
type ImplementRequirementArtifact struct {
	MRTitleDraft string   `json:"mr_title_draft"`
	MRBodyDraft  string   `json:"mr_body_draft"`
	Commit       string   `json:"commit"`
	MRLabels     []string `json:"mr_labels,omitempty"`
}

// ImplementRequirementToolResult 是 leader 看到的 delegate_to_requirement_implementer MCP 调用结果。
type ImplementRequirementToolResult struct {
	ArtifactName string                       `json:"artifact_name"`
	TextPart     string                       `json:"text_part"`
	DataPart     ImplementRequirementArtifact `json:"data_part"`
}

// implementRequirementModelOutput 是执行阶段给 Claude 的内部结构化输出。
//
// 和 design plan 一样，这里的 summary 只给 UI 和状态消息使用，
// 真正对外的 Team Mode DataPart 仍然是 ImplementRequirementArtifact。
type implementRequirementModelOutput struct {
	Summary  string                       `json:"summary"`
	Artifact ImplementRequirementArtifact `json:"artifact"`
}

// VerifyCodeRequest 直接对齐 Team Mode 文档里的 VerifyCodeRequest。
type VerifyCodeRequest struct {
	PlanContent   string `json:"plan_content"`
	WorkspacePath string `json:"workspace_path"`
}

const (
	// VerifyCodeStatePassed 表示验证通过。
	VerifyCodeStatePassed = "VERIFY_CODE_STATE_PASSED"
	// VerifyCodeStateFailed 表示验证失败。
	VerifyCodeStateFailed = "VERIFY_CODE_STATE_FAILED"
	// VerifyCodeStateSkipped 表示验证跳过。
	VerifyCodeStateSkipped = "VERIFY_CODE_STATE_SKIPPED"
)

// VerifyCodeArtifact 直接对齐 Team Mode 文档里的 VerifyCodeArtifact。
type VerifyCodeArtifact struct {
	State       string   `json:"state"`
	Description string   `json:"description"`
	ReportPaths []string `json:"report_paths,omitempty"`
}

// VerifyCodeToolResult 是 leader 看到的 delegate_to_code_verifier MCP 调用结果。
type VerifyCodeToolResult struct {
	ArtifactName string             `json:"artifact_name"`
	TextPart     string             `json:"text_part"`
	DataPart     VerifyCodeArtifact `json:"data_part"`
}

// verifyCodeModelOutput 是验证阶段给 Claude 的内部结构化输出。
type verifyCodeModelOutput struct {
	Summary  string             `json:"summary"`
	Artifact VerifyCodeArtifact `json:"artifact"`
}

// workflowGate 用 stderr 模拟最小工作流 server 的前后上报与顺序约束。
//
// 这个 demo 里只允许三步：
// 1. `design_plan` stage
// 2. `implement_requirement` stage
// 3. `verify_code` stage
//
// 如果 leader 跳阶段、重复阶段，这里会直接拦住。
type workflowGate struct {
	mu        sync.Mutex
	completed map[string]bool
}

// BeforeDelegate 上报阶段开始并做最小阶段约束。
func (h *workflowGate) BeforeDelegate(ctx context.Context, req a2adelegation.BeforeDelegation) error {
	stage := req.Request.StageContext.Stage
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.completed == nil {
		h.completed = map[string]bool{}
	}
	switch stage {
	case designPlanStage:
		if h.completed[designPlanStage] {
			return &a2adelegation.DelegationError{Code: "stage_blocked", Message: "design_plan already completed"}
		}
	case implementRequirementStage:
		if h.completed[implementRequirementStage] {
			return &a2adelegation.DelegationError{Code: "stage_blocked", Message: "implement_requirement already completed"}
		}
		if !h.completed[designPlanStage] {
			return &a2adelegation.DelegationError{Code: "stage_blocked", Message: "implement_requirement requires design_plan first"}
		}
	case verifyCodeStage:
		if h.completed[verifyCodeStage] {
			return &a2adelegation.DelegationError{Code: "stage_blocked", Message: "verify_code already completed"}
		}
		if !h.completed[implementRequirementStage] {
			return &a2adelegation.DelegationError{Code: "stage_blocked", Message: "verify_code requires implement_requirement first"}
		}
	default:
		return &a2adelegation.DelegationError{Code: "stage_blocked", Message: "unexpected stage in demo workflow: " + stage}
	}
	fmt.Fprintf(os.Stderr, "[workflow] start workflow=%s stage=%s agent=%s\n",
		req.Request.StageContext.WorkflowRunID, stage, req.AgentSpec.Key)
	return nil
}

// AfterDelegate 上报阶段完成结果。
func (h *workflowGate) AfterDelegate(ctx context.Context, req a2adelegation.AfterDelegation) error {
	h.mu.Lock()
	if h.completed == nil {
		h.completed = map[string]bool{}
	}
	if req.Result.Status == "completed" {
		h.completed[req.Request.StageContext.Stage] = true
	}
	h.mu.Unlock()
	fmt.Fprintf(os.Stderr, "[workflow] finish workflow=%s stage=%s status=%s summary=%q\n",
		req.Request.StageContext.WorkflowRunID, req.Request.StageContext.Stage, req.Result.Status, req.Result.Summary)
	return nil
}

// main 读取命令行参数并运行示例。
func main() {
	model := flag.String("model", "", "Claude model (default claude-sonnet-4, or CLAUDE_MODEL env)")
	command := flag.String("command", "", "Explicit claude CLI command")
	flag.Parse()

	exitCode := 0
	if err := run(context.Background(), model, command); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		exitCode = 1
	}
	os.Exit(exitCode)
}

// run 启动“先设计、再实现、再验证”的最小 Team Mode 示例。
//
// 读这个函数时，可以只关注 8 个步骤：
// 1. 准备一个临时 git 工作区，给执行阶段真实修改代码和提交
// 2. 启动 design_plan member
// 3. 启动 implement_requirement member
// 4. 启动 verify_code member
// 5. 把三个 member 都注册成 leader 可调用的 MCP tool
// 6. 启动 leader
// 7. 让 leader 先调 delegate_to_plan_designer，再调 delegate_to_requirement_implementer，最后调 delegate_to_code_verifier
// 8. 最后打印临时仓库的提交结果，证明“计划 -> 执行 -> 验证”真的跑完了
func run(ctx context.Context, model, command *string) error {
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt)
	defer cancel()

	claudeCmd := resolveCommand(*command)
	selectedModel := resolveModel(*model)
	templateRepo, err := resolveDemoRepo()
	if err != nil {
		return err
	}
	// 准备临时工作区
	workRepo, err := prepareWorkRepo(templateRepo)
	if err != nil {
		return err
	}
	defer os.RemoveAll(workRepo)
	requirementAttachmentPath, prototypePath, err := resolveDemoInputs()
	if err != nil {
		return err
	}
	task := DemoTaskContext{
		RequirementTitle:          "todo list 支持 --json 输出",
		RequirementContent:        "当前 todo list 只能输出文本格式。请为它增加 --json 输出能力，并保持 summary 命令行为不变。",
		RequirementAttachmentPath: requirementAttachmentPath,
		PrototypePath:             prototypePath,
		WorkspacePath:             workRepo,
	}
	workflowState := &demoWorkflowState{}

	fmt.Fprintf(os.Stderr, "=== Design Plan + Implement + Verify Team Demo ===\n")
	fmt.Fprintf(os.Stderr, "Leader model:   %s\n", selectedModel)
	fmt.Fprintf(os.Stderr, "Member model:   %s\n", selectedModel)
	fmt.Fprintf(os.Stderr, "Claude CLI:     %s\n", claudeCmd)
	fmt.Fprintf(os.Stderr, "Template repo:  %s\n", templateRepo)
	fmt.Fprintf(os.Stderr, "Work repo:      %s\n", workRepo)

	// 1. 准备 leader 和三个 member 的隔离 profile。
	designMemberProfileDir, err := os.MkdirTemp("", "design-plan-member-profile-*")
	if err != nil {
		return fmt.Errorf("create design member profile temp dir: %w", err)
	}
	defer os.RemoveAll(designMemberProfileDir)

	implementMemberProfileDir, err := os.MkdirTemp("", "implement-member-profile-*")
	if err != nil {
		return fmt.Errorf("create implement member profile temp dir: %w", err)
	}
	defer os.RemoveAll(implementMemberProfileDir)

	verifyMemberProfileDir, err := os.MkdirTemp("", "verify-member-profile-*")
	if err != nil {
		return fmt.Errorf("create verify member profile temp dir: %w", err)
	}
	defer os.RemoveAll(verifyMemberProfileDir)

	leaderProfileDir, err := os.MkdirTemp("", "leader-profile-*")
	if err != nil {
		return fmt.Errorf("create leader profile temp dir: %w", err)
	}
	defer os.RemoveAll(leaderProfileDir)

	// 2. 启动 design_plan member。
	designMemberAddr, err := startDesignPlanMemberServer(ctx, claudeCmd, selectedModel, workRepo, designMemberProfileDir)
	if err != nil {
		return fmt.Errorf("start design plan member: %w", err)
	}
	fmt.Fprintf(os.Stderr, "\nDesign Plan member A2A server at %s\n", designMemberAddr)

	// 3. 启动 implement_requirement member。
	implementMemberAddr, err := startImplementRequirementMemberServer(ctx, claudeCmd, selectedModel, workRepo, implementMemberProfileDir)
	if err != nil {
		return fmt.Errorf("start implement member: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Implement member A2A server at %s\n", implementMemberAddr)

	// 4. 启动 verify_code member。
	verifyMemberAddr, err := startVerifyCodeMemberServer(ctx, claudeCmd, selectedModel, workRepo, verifyMemberProfileDir)
	if err != nil {
		return fmt.Errorf("start verify member: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Verify member A2A server at %s\n", verifyMemberAddr)

	// 5. 把三个 member 都注册成 leader 可调用的受控 agent。
	registry, err := a2adelegation.NewRegistry(
		a2adelegation.RemoteAgentSpec{
			Key:                 designerAgentKey,
			DisplayName:         "Plan Designer",
			AgentCardURL:        fmt.Sprintf("http://%s%s", designMemberAddr, agentCardPath),
			AcceptedOutputModes: []string{"text"},
			PreferredTransports: []clienta2a.TransportProtocol{clienta2a.TransportJSONRPC},
		},
		a2adelegation.RemoteAgentSpec{
			Key:                 implementerAgentKey,
			DisplayName:         "Requirement Implementer",
			AgentCardURL:        fmt.Sprintf("http://%s%s", implementMemberAddr, agentCardPath),
			AcceptedOutputModes: []string{"text"},
			PreferredTransports: []clienta2a.TransportProtocol{clienta2a.TransportJSONRPC},
		},
		a2adelegation.RemoteAgentSpec{
			Key:                 verifierAgentKey,
			DisplayName:         "Code Verifier",
			AgentCardURL:        fmt.Sprintf("http://%s%s", verifyMemberAddr, agentCardPath),
			AcceptedOutputModes: []string{"text"},
			PreferredTransports: []clienta2a.TransportProtocol{clienta2a.TransportJSONRPC},
		},
	)
	if err != nil {
		return fmt.Errorf("create registry: %w", err)
	}

	bus := a2adelegation.NewEventBus(64)
	delegator := a2adelegation.NewDelegator(registry, bus)
	delegator.LifecycleHook = &workflowGate{}

	// 6. 启动 MCP server，并暴露三个阶段工具。
	mcpSrv := a2adelegation.NewMCPServer(delegator, a2adelegation.MCPServerOptions{
		RunID:                               "demo-run",
		AllowUnauthenticatedLoopbackForTest: true,
		Tools: []a2adelegation.ToolSpec{
			newDesignPlanTool(task, workflowState, InvocationContext{
				WorkflowRunID: "demo-workflow",
				StepID:        "design-plan",
				Attempt:       1,
			}),
			newImplementRequirementTool(task, workflowState, InvocationContext{
				WorkflowRunID: "demo-workflow",
				StepID:        "implement-requirement",
				Attempt:       1,
			}),
			newVerifyCodeTool(task, workflowState, InvocationContext{
				WorkflowRunID: "demo-workflow",
				StepID:        "verify-code",
				Attempt:       1,
			}),
		},
		DisableDefaultTool: true,
	})
	mcpListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("mcp listen: %w", err)
	}
	defer mcpListener.Close()
	mcpHTTP := &http.Server{Handler: mcpSrv.Handler()}
	go func() {
		<-ctx.Done()
		_ = mcpHTTP.Close()
	}()
	go func() {
		_ = mcpHTTP.Serve(mcpListener)
	}()

	// 7. 启动 leader，并把固定的 Team Mode 规则放进默认 instructions。
	leaderSDK := agentadaptor.New(
		agentadaptor.WithDefaultAgent(newClaudeBinding(claudeCmd, selectedModel, workRepo, leaderProfileDir,
			agentadaptor.WithDefaultMCP(agentadaptor.MCPConfig{
				Servers: []agentadaptor.MCPServerSpec{{
					Key:       "team_mode",
					Transport: agentadaptor.MCPTransportHTTP,
					URL:       fmt.Sprintf("http://%s", mcpListener.Addr().String()),
				}},
			}),
			leaderInstructions(),
		)),
	)

	prompt := fmt.Sprintf(`请帮我在这个 demo 仓库里实现一个需求。

需求标题：%s
需求说明：%s`, task.RequirementTitle, task.RequirementContent)

	fmt.Fprintf(os.Stderr, "\nLeader prompt:\n%s\n\n", prompt)
	fmt.Fprintln(os.Stderr, "--- leader stream (stdout) ---")

	// 8. 运行 leader，让它先做计划，再做实现，最后做验证。
	start := time.Now()
	handle, err := leaderSDK.Start(ctx, prompt, leaderRunOptions()...)
	if err != nil {
		return fmt.Errorf("leader start: %w", err)
	}
	defer func() {
		_ = handle.Cancel(ctx)
	}()

	fmt.Fprintf(os.Stderr, "[leader run %s]\n", handle.RunID())
	go drainRunEvents(handle.Events())
	go drainDelegationEvents(ctx, bus, "demo-run")

	printLeaderStream(handle.StreamEvents())

	result, err := handle.Wait(ctx)
	duration := time.Since(start)
	if err != nil {
		return fmt.Errorf("leader wait: %w", err)
	}

	fmt.Fprintf(os.Stderr, "\n--- end leader stream (took %v) ---\n", duration)
	printJSON(map[string]any{
		"run_id":       result.RunID,
		"output":       result.Output,
		"summary":      result.Summary,
		"failure":      result.Failure,
		"workspace":    workRepo,
		"repo_summary": summarizeWorkRepo(workRepo),
	})
	return nil
}

// startDesignPlanMemberServer 启动默认 Claude member 的 Design Plan A2A server。
func startDesignPlanMemberServer(ctx context.Context, cliCommand, model, workDir, profileDir string) (string, error) {
	binding := newClaudeBinding(cliCommand, model, workDir, profileDir)
	memberSDK := agentadaptor.New(
		agentadaptor.WithDefaultAgent(binding),
		agentadaptor.WithSessionStore(memory.NewSessionStore()),
	)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("member listen: %w", err)
	}

	addr := listener.Addr().String()
	jsonRPCURL := fmt.Sprintf("http://%s%s", addr, jsonRPCPath)
	server := bridgea2a.NewServer(memberSDK.Default(), designPlannerServerOptions(jsonRPCURL))

	mux := http.NewServeMux()
	mux.Handle(agentCardPath, server.AgentCardHandler())
	mux.Handle(jsonRPCPath, server.Handler())

	httpSrv := &http.Server{Handler: mux}
	go func() {
		<-ctx.Done()
		_ = httpSrv.Close()
	}()
	go func() {
		_ = httpSrv.Serve(listener)
	}()
	return addr, nil
}

// startImplementRequirementMemberServer 启动默认 Claude member 的执行阶段 A2A server。
func startImplementRequirementMemberServer(ctx context.Context, cliCommand, model, workDir, profileDir string) (string, error) {
	binding := newClaudeBinding(cliCommand, model, workDir, profileDir)
	memberSDK := agentadaptor.New(
		agentadaptor.WithDefaultAgent(binding),
		agentadaptor.WithSessionStore(memory.NewSessionStore()),
	)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("implement member listen: %w", err)
	}

	addr := listener.Addr().String()
	jsonRPCURL := fmt.Sprintf("http://%s%s", addr, jsonRPCPath)
	server := bridgea2a.NewServer(memberSDK.Default(), implementRequirementServerOptions(jsonRPCURL))

	mux := http.NewServeMux()
	mux.Handle(agentCardPath, server.AgentCardHandler())
	mux.Handle(jsonRPCPath, server.Handler())

	httpSrv := &http.Server{Handler: mux}
	go func() {
		<-ctx.Done()
		_ = httpSrv.Close()
	}()
	go func() {
		_ = httpSrv.Serve(listener)
	}()
	return addr, nil
}

// startVerifyCodeMemberServer 启动默认 Claude member 的代码验证阶段 A2A server。
func startVerifyCodeMemberServer(ctx context.Context, cliCommand, model, workDir, profileDir string) (string, error) {
	binding := newClaudeBinding(cliCommand, model, workDir, profileDir)
	memberSDK := agentadaptor.New(
		agentadaptor.WithDefaultAgent(binding),
		agentadaptor.WithSessionStore(memory.NewSessionStore()),
	)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("verify member listen: %w", err)
	}

	addr := listener.Addr().String()
	jsonRPCURL := fmt.Sprintf("http://%s%s", addr, jsonRPCPath)
	server := bridgea2a.NewServer(memberSDK.Default(), verifyCodeServerOptions(jsonRPCURL))

	mux := http.NewServeMux()
	mux.Handle(agentCardPath, server.AgentCardHandler())
	mux.Handle(jsonRPCPath, server.Handler())

	httpSrv := &http.Server{Handler: mux}
	go func() {
		<-ctx.Done()
		_ = httpSrv.Close()
	}()
	go func() {
		_ = httpSrv.Serve(listener)
	}()
	return addr, nil
}

// prepareWorkRepo 复制 demo 模板仓库并初始化本地 git 仓库。
//
// 这样执行阶段就能在一个隔离目录里真实改代码、真实提交，而不会污染模板仓库。
func prepareWorkRepo(templateRepo string) (string, error) {
	workRepo, err := os.MkdirTemp("", "team-mode-work-repo-*")
	if err != nil {
		return "", fmt.Errorf("create work repo temp dir: %w", err)
	}
	if err := runCommand("", "cp", "-R", templateRepo+"/.", workRepo); err != nil {
		return "", fmt.Errorf("copy demo repo: %w", err)
	}
	if err := runCommand(workRepo, "git", "init"); err != nil {
		return "", fmt.Errorf("git init: %w", err)
	}
	if err := runCommand(workRepo, "git", "config", "user.name", "Team Mode Demo"); err != nil {
		return "", fmt.Errorf("git config user.name: %w", err)
	}
	if err := runCommand(workRepo, "git", "config", "user.email", "team-mode@example.com"); err != nil {
		return "", fmt.Errorf("git config user.email: %w", err)
	}
	if err := runCommand(workRepo, "git", "add", "."); err != nil {
		return "", fmt.Errorf("git add initial files: %w", err)
	}
	if err := runCommand(workRepo, "git", "commit", "-m", "chore: seed demo repo"); err != nil {
		return "", fmt.Errorf("git initial commit: %w", err)
	}
	return workRepo, nil
}

// summarizeWorkRepo 返回临时工作区最关键的结果摘要。
func summarizeWorkRepo(repo string) map[string]any {
	head, _ := commandOutput(repo, "git", "rev-parse", "HEAD")
	show, _ := commandOutput(repo, "git", "show", "--stat", "--oneline", "--name-only", "HEAD")
	status, _ := commandOutput(repo, "git", "status", "--short")
	return map[string]any{
		"head_commit": strings.TrimSpace(head),
		"head_show":   strings.TrimSpace(show),
		"status":      strings.TrimSpace(status),
	}
}

// runCommand 运行一个简单命令，失败时把 stderr 一起带出来。
func runCommand(dir string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// commandOutput 返回命令 stdout。
func commandOutput(dir string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// drainRunEvents 消费 leader 的运行期事件，避免阻塞内部 channel。
func drainRunEvents(events <-chan agentadaptor.RunEvent) {
	for range events {
	}
}

// drainDelegationEvents 打印阶段 agent 的委派事件，便于观察 Team Mode 行为。
func drainDelegationEvents(ctx context.Context, bus *a2adelegation.EventBus, runID string) {
	if bus == nil {
		return
	}
	ch := bus.SubscribeRun(ctx, runID)
	for ev := range ch {
		switch ev.Kind {
		case a2adelegation.DelegationStarted:
			fmt.Fprintf(os.Stderr, "\n[member|%s] started agent=%s stage=%s\n", ev.DelegationID, ev.AgentKey, ev.AgentKey)
		case a2adelegation.DelegationArtifactCreated:
			if ev.Artifact != nil {
				fmt.Fprintf(os.Stderr, "[member|%s] artifact name=%s uri=%s\n", ev.DelegationID, ev.Artifact.Name, ev.Artifact.URI)
			}
		case a2adelegation.DelegationTextStart:
			fmt.Fprintf(os.Stderr, "[member|%s] ", ev.DelegationID)
		case a2adelegation.DelegationTextDelta:
			fmt.Fprintf(os.Stderr, "%s", ev.Delta)
		case a2adelegation.DelegationTextEnd:
			fmt.Fprintln(os.Stderr)
		case a2adelegation.DelegationFinished:
			fmt.Fprintf(os.Stderr, "\n[member|%s] finished status=%s\n", ev.DelegationID, ev.Status)
		case a2adelegation.DelegationFailed:
			msg := ""
			if ev.Error != nil {
				msg = ev.Error.Message
			}
			fmt.Fprintf(os.Stderr, "\n[member|%s] failed: %s\n", ev.DelegationID, msg)
		case a2adelegation.DelegationCancelled:
			fmt.Fprintf(os.Stderr, "\n[member|%s] cancelled\n", ev.DelegationID)
		}
	}
}

// leaderStreamPrinter 流式打印 leader 输出，并汇总 tool 调用的输入/输出。
type leaderStreamPrinter struct {
	toolInputs      map[string]*strings.Builder
	toolStartArgs   map[string]map[string]any
	toolHasArgDelta map[string]bool
}

// handle 处理单条 leader 流式事件。
func (p *leaderStreamPrinter) handle(ev agentadaptor.StreamPayload) {
	switch ev.Kind {
	case agentadaptor.StreamTextContent:
		fmt.Print(ev.Delta)
	case agentadaptor.StreamToolCallStart:
		if p.toolInputs == nil {
			p.toolInputs = map[string]*strings.Builder{}
		}
		if p.toolStartArgs == nil {
			p.toolStartArgs = map[string]map[string]any{}
		}
		if p.toolHasArgDelta == nil {
			p.toolHasArgDelta = map[string]bool{}
		}
		p.toolArgBuffer(ev.ToolCallID)
		fmt.Fprintf(os.Stderr, "\n[leader tool:%s id=%s]\n", ev.Name, ev.ToolCallID)
		if len(ev.Args) > 0 {
			p.toolStartArgs[ev.ToolCallID] = ev.Args
		}
	case agentadaptor.StreamToolCallArgs:
		if ev.Delta == "" {
			return
		}
		p.toolHasArgDelta[ev.ToolCallID] = true
		buf := p.toolArgBuffer(ev.ToolCallID)
		buf.WriteString(ev.Delta)
	case agentadaptor.StreamToolCallEnd:
		input := p.formatToolInput(ev.ToolCallID)
		if input != "" {
			fmt.Fprintf(os.Stderr, "[leader tool input] %s\n", input)
		}
		delete(p.toolInputs, ev.ToolCallID)
		delete(p.toolStartArgs, ev.ToolCallID)
		delete(p.toolHasArgDelta, ev.ToolCallID)
	case agentadaptor.StreamToolCallResult:
		fmt.Fprintf(os.Stderr, "[leader tool output id=%s]\n%s\n", ev.ToolCallID, formatStreamValue(ev.Result))
	case agentadaptor.StreamRunFinished:
		fmt.Println()
	case agentadaptor.StreamRunError:
		msg := "unknown"
		if ev.Error != nil {
			msg = ev.Error.Message
		}
		fmt.Fprintln(os.Stderr, "[leader run error]:", msg)
	}
}

func (p *leaderStreamPrinter) toolArgBuffer(toolCallID string) *strings.Builder {
	if p.toolInputs == nil {
		p.toolInputs = map[string]*strings.Builder{}
	}
	buf, ok := p.toolInputs[toolCallID]
	if !ok {
		buf = &strings.Builder{}
		p.toolInputs[toolCallID] = buf
	}
	return buf
}

// formatToolInput 优先使用流式 args delta 拼接结果；无 delta 时回退到 start 事件里的 Args。
func (p *leaderStreamPrinter) formatToolInput(toolCallID string) string {
	if p.toolHasArgDelta[toolCallID] {
		if buf := p.toolInputs[toolCallID]; buf != nil && buf.Len() > 0 {
			return formatStreamJSON(buf.String())
		}
	}
	if args := p.toolStartArgs[toolCallID]; len(args) > 0 {
		if input, ok := args["input"]; ok {
			return formatStreamValue(input)
		}
		return formatStreamValue(args)
	}
	return ""
}

// printLeaderStream 打印 leader 的最少必要流式信息。
//
// 除了 leader 文本输出外，这里也会打印 tool 调用的输入/输出，方便调试
// Team Mode 的 delegation 参数。
func printLeaderStream(events <-chan agentadaptor.StreamPayload) {
	printer := &leaderStreamPrinter{}
	for ev := range events {
		printer.handle(ev)
	}
}

// leaderRunOptions 返回 leader 的执行选项。
func leaderRunOptions() []agentadaptor.RunOption {
	return []agentadaptor.RunOption{
		autoPolicy(),
		agentadaptor.WithStreaming(),
	}
}

// autoPolicy 为 demo 关闭交互式人工确认。
func autoPolicy() agentadaptor.RunOption {
	return agentadaptor.WithRunPolicy(agentadaptor.RunPolicy{
		Isolation: agentadaptor.IsolationWorkspaceWrite,
		HumanDecision: agentadaptor.HumanDecisionPolicy{
			Permission: agentadaptor.HumanDecisionAutoApprove,
			PlanReview: agentadaptor.HumanDecisionAutoApprove,
			Question:   agentadaptor.QuestionAutoReject,
		},
	})
}

// leaderInstructions 返回 leader 默认写入 profile 的固定规则。
func leaderInstructions() agentadaptor.AgentOption {
	return agentadaptor.WithDefaultInstructions(&agentadaptor.InstructionsBundleRef{
		ID: "team-mode-leader",
		Content: `你是一个 Team Mode leader。
你不能直接修改代码或自己实现需求，必须通过 MCP 工具把工作委托给阶段 agent。

你现在可以使用三个工具：
1. "delegate_to_plan_designer"
2. "delegate_to_requirement_implementer"
3. "delegate_to_code_verifier"

工作规则：
1. 先调用 "delegate_to_plan_designer" 生成一个很小的方案，再调用 "delegate_to_requirement_implementer" 去执行，最后调用 "delegate_to_code_verifier" 做验证。
2. 调工具时，需要填写一段文本 instruction，说明这一阶段想让对应 agent 做什么。
3. 真正发给 member 的 TextPart 直接使用 leader 提供的 instruction；结构化 request 仍然由 host 固定生成。host 会自动补齐当前需求、工作区、附件、原型以及上一阶段产出的 plan_content。
4. code verifier 只验证两件事："go build ./..." 和 "go test ./..."。
5. 最终回答至少包含：方案摘要、commit SHA、验证结论、修改文件列表。`,
	})
}

// newClaudeBinding 创建一个隔离 profile 的 Claude 默认 binding。
func newClaudeBinding(cliCommand, model, workDir, profileDir string, opts ...agentadaptor.AgentOption) agentadaptor.AgentBinding {
	agentOpts := []agentadaptor.AgentOption{
		agentadaptor.WithCloneProfile(profileDir, agentadaptor.CloneProfileOptions{
			IncludeSettings: true,
			AuthMode:        agentadaptor.CloneProfileAuthLink,
		}),
	}
	agentOpts = append(agentOpts, opts...)
	return claude.New(agentadaptor.ClaudeConfig{
		CommonConfig: agentadaptor.CommonConfig{
			Command: cliCommand,
			CWD:     workDir,
		},
		Model:          model,
		MaxTurnsPerRun: 50,
	}, agentOpts...)
}

// resolveDemoRepo 返回示例里 tiny repo 的绝对路径。
func resolveDemoRepo() (string, error) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("resolve demo repo: runtime.Caller failed")
	}
	repoPath := filepath.Join(filepath.Dir(currentFile), "demo-repo")
	abs, err := filepath.Abs(repoPath)
	if err != nil {
		return "", fmt.Errorf("resolve demo repo abs path: %w", err)
	}
	return abs, nil
}

// resolveDemoInputs 返回 Design Plan request 里会用到的两个占位输入文件。
//
// 文档里要求这里通常是“需求附件 zip”和“原型文件”的绝对路径。
// 这个示例只关注字段形状，所以这里提供两个本地占位文件路径。
func resolveDemoInputs() (string, string, error) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		return "", "", fmt.Errorf("resolve demo inputs: runtime.Caller failed")
	}
	baseDir := filepath.Dir(currentFile)
	attachmentPath, err := filepath.Abs(filepath.Join(baseDir, "inputs", "requirement-attachment.zip"))
	if err != nil {
		return "", "", fmt.Errorf("resolve requirement attachment path: %w", err)
	}
	prototypePath, err := filepath.Abs(filepath.Join(baseDir, "inputs", "prototype.md"))
	if err != nil {
		return "", "", fmt.Errorf("resolve prototype path: %w", err)
	}
	return attachmentPath, prototypePath, nil
}

// resolveCommand 推断 Claude CLI 命令。
func resolveCommand(override string) string {
	if strings.TrimSpace(override) != "" {
		return strings.TrimSpace(override)
	}
	if env := os.Getenv("CLAUDE_COMMAND"); strings.TrimSpace(env) != "" {
		return strings.TrimSpace(env)
	}
	if path, err := exec.LookPath("claude"); err == nil {
		return path
	}
	for _, name := range []string{"claude.ps1", "claude.cmd", "claude", "trpc-claudecode.ps1", "trpc-claudecode.cmd", "trpc-claudecode"} {
		var cmd string
		if filepath.IsAbs(name) {
			cmd = name
		} else if path, err := exec.LookPath(name); err == nil {
			cmd = path
		}
		if cmd != "" {
			return cmd
		}
	}
	return "claude"
}

// resolveModel 返回默认 Claude 模型。
func resolveModel(override string) string {
	if strings.TrimSpace(override) != "" {
		return strings.TrimSpace(override)
	}
	if env := os.Getenv("CLAUDE_MODEL"); strings.TrimSpace(env) != "" {
		return strings.TrimSpace(env)
	}
	return "claude-sonnet-4"
}

// printJSON 打印最终结果 JSON。
func printJSON(value any) {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		fmt.Fprintf(os.Stderr, "encode JSON: %v\n", err)
	}
}

// formatStreamValue 把流式 tool 结果格式化成可读 JSON。
func formatStreamValue(v any) string {
	if v == nil {
		return ""
	}
	switch typed := v.(type) {
	case string:
		return formatStreamJSON(typed)
	case map[string]any:
		raw, err := json.MarshalIndent(typed, "", "  ")
		if err != nil {
			return fmt.Sprintf("%v", typed)
		}
		return string(raw)
	default:
		raw, err := json.MarshalIndent(typed, "", "  ")
		if err != nil {
			return fmt.Sprintf("%v", typed)
		}
		return string(raw)
	}
}

// formatStreamJSON 尝试把原始 JSON 字符串缩进输出。
func formatStreamJSON(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	var parsed any
	if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
		return trimmed
	}
	out, err := json.MarshalIndent(parsed, "", "  ")
	if err != nil {
		return trimmed
	}
	return string(out)
}

// defaultString 返回非空值，否则回退到默认值。
func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

// init 在不支持的平台上直接退出，避免示例行为不一致。
func init() {
	if runtime.GOOS == "windows" {
		fmt.Fprintf(os.Stderr, "Windows support requires WSL or Linux.\n")
		os.Exit(1)
	}
}
