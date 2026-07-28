# 调用方使用指南

本文按常见宿主场景介绍 `agent-adaptor`。完整签名见 [API 参考](./api-reference.md)，架构与发布合同见 [AGENTS.md](../AGENTS.md)。

根包的导入名是 `adaptor`：

```go
import adaptor "github.com/agent-dance/agent-adaptor"
```

## 1. 运行一个 Agent

最小程序只需要根包和一个 provider 包：

```go
package main

import (
	"context"
	"fmt"
	"log"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/codex"
)

func main() {
	agent := adaptor.New(codex.Driver(codex.Config{Model: "gpt-5.4"}))

	result, err := agent.Run(context.Background(), "fix the failing tests")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(result.Text)
}
```

`Driver(Config)` 只捕获配置，不探测环境。缺少 CLI、未登录或配置错误会在运行或 Inspect 时返回。

设置常用默认值：

```go
agent := adaptor.New(
	claude.Driver(claude.Config{Model: "claude-sonnet-4"}),
	adaptor.WithWorkspace("/work/acme-api"),
	adaptor.WithTimeout(20*time.Minute),
	adaptor.WithPolicy(adaptor.PolicyWorkspaceWrite),
	adaptor.WithIdentity(adaptor.Identity{
		ID:     "user-42",
		Tenant: "acme",
		Name:   "implementation-agent",
	}),
)
```

## 2. 多个 Agent 就是多个变量

为不同角色分别构造 Agent，可以让策略、profile 和模型在类型检查范围内清晰可见：

```go
coder := adaptor.New(
	codex.Driver(codex.Config{Model: "gpt-5.4"}),
	adaptor.WithWorkspace(repoDir),
	adaptor.WithPolicy(adaptor.PolicyWorkspaceWrite),
)

reviewer := adaptor.New(
	claude.Driver(claude.Config{Model: "claude-sonnet-4"}),
	adaptor.WithWorkspace(repoDir),
	adaptor.WithPolicy(adaptor.PolicyReadOnly),
)

patch, err := coder.Run(ctx, "implement the issue")
if err != nil {
	return err
}

review, err := reviewer.Run(ctx, "review this result:\n"+patch.Text)
if err != nil {
	return err
}
fmt.Println(review.Text)
```

需要动态选择时，宿主可以保存自己的 `map[string]*adaptor.Agent`；生命周期和路由策略仍由宿主决定。

## 3. 构造默认值与调用覆盖

返回 `SharedOption` 的函数既能用于构造，也能用于单次调用。调用处覆盖只影响本次执行：

```go
agent := adaptor.New(
	codex.Driver(codex.Config{Model: "gpt-5.4"}),
	adaptor.WithWorkspace("/repos/default"),
	adaptor.WithPolicy(adaptor.PolicyReadOnly),
	adaptor.WithMetadata("component", "triage"),
)

result, err := agent.Run(ctx, "apply the approved fix",
	adaptor.WithWorkspace("/repos/issue-123"),
	adaptor.WithModel("gpt-5.4-mini"),
	adaptor.WithPolicy(adaptor.PolicyWorkspaceWrite),
	adaptor.WithMetadata("job_id", "job-123"),
)
```

合并规则：

- `WithSkills` 追加到 Agent 默认 skills。
- `WithMetadata` 按 key 合并，调用处同名 key 覆盖。
- `WithRunServices` 追加并去重。
- `WithMCP`、`WithServices`、`WithPolicy`、workspace、instructions、identity 等整体替换对应默认值。
- `WithMCP()` 或 `WithServices()` 的空调用表示本次显式清空。

`WithThreadStore`、manager、profile 选择和事件缓冲只允许在构造时使用。`WithSchema` 只允许在调用时使用；放错位置会编译失败。

Policy 的非零值是严格意图，不是尽力而为。显式 Sandbox、WebSearch、Browser 或 approval mode 如果不在 Driver 的 `Descriptor.RunPolicyCaps` 中，会在进程启动前分别返回 `ErrPolicyCapabilityUnsupported` 或 `ErrHumanDecisionModeUnsupported`。需要跨 provider 的默认行为时保留 Inherit；需要显式策略时，应先按目标 Driver 的 capability 选择。

## 4. 处理 Result 与失败

成功时使用 `Result.Text` 作为最终 assistant 文本。原始进程输出、语义 transcript 和服务报告是独立层：

```go
result, err := agent.Run(ctx, prompt)
if err != nil {
	var runErr *adaptor.RunError
	if errors.As(err, &runErr) {
		log.Printf("run failed: reason=%s summary=%q", runErr.Reason, runErr.Result.Summary)
		log.Printf("partial stdout bytes=%d", len(runErr.Result.Raw().Stdout))
	}
	return err
}

fmt.Println(result.Text)
raw := result.Raw()
items := result.Transcript()
services := result.Services()
_, _, _ = raw, items, services
```

不要把 `Raw().Stdout` 当作 assistant 回复。`Raw().Terminal` 保存 provider 官方终局 JSON，适合审计；`Transcript()` 适合统一渲染 assistant、thinking、tool 与 result。

业务失败可用 `errors.As(err, *RunError)` 读取部分 Result，也可用 `errors.Is` 判断：

```go
switch {
case errors.Is(err, adaptor.ErrApprovalDenied):
	// 操作者或策略拒绝。
case errors.Is(err, adaptor.ErrApprovalTimeout):
	// 审批超时。
case errors.Is(err, adaptor.ErrAgentFailed):
	// provider 报告业务终局失败。
case errors.Is(err, context.Canceled):
	// 调用方取消。
}
```

## 5. Thread：四个对话动作

Thread 需要一个 `threadstore.Store`。本地工具和测试可以使用 `memory.NewStore()`：

```go
store := memory.NewStore()
agent := adaptor.New(
	claude.Driver(claude.Config{Model: "claude-sonnet-4"}),
	adaptor.WithThreadStore(store),
)
```

### 5.1 有则续、无则建

```go
thread := agent.Thread("tenant-1/issue-123")

first, err := thread.Run(ctx, "inspect the failing test")
if err != nil {
	return err
}
second, err := thread.Run(ctx, "now implement the fix")
```

第二次调用会使用第一次成功运行保存的 checkpoint。

### 5.2 只续不建

```go
thread := agent.Thread("tenant-1/issue-123", adaptor.ResumeOnly())
result, err := thread.Run(ctx, "continue the existing investigation")
if errors.Is(err, adaptor.ErrThreadNotFound) {
	return fmt.Errorf("the conversation must already exist: %w", err)
}
```

配置或运行环境与旧 checkpoint 不兼容时匹配 `ErrThreadIncompatible`，不会静默新建。

### 5.3 强制新建

```go
fresh := agent.NewThread("tenant-1/issue-123")
result, err := fresh.Run(ctx, "restart the investigation from scratch")
```

旧 active record 只会在新运行成功产生有效 checkpoint 后归档；失败不会污染旧健康对话。此 handle 后续调用会正常续接新对话。

### 5.4 分叉

```go
parent := agent.Thread("tenant-1/issue-123", adaptor.ResumeOnly())
branch := parent.Fork("tenant-1/issue-123/alternative")

result, err := branch.Run(ctx, "try a different implementation")
if errors.Is(err, adaptor.ErrThreadAlreadyExists) {
	return fmt.Errorf("choose a new branch key: %w", err)
}
```

父 Thread 保持 active，不会被分叉修改。目标 key 必须未被其他 active 对话占用。

### 5.5 查看 checkpoint

```go
checkpoint, err := thread.Checkpoint(ctx)
if err != nil {
	return err
}
fmt.Printf("valid=%v resume=%s\n", checkpoint.Valid, checkpoint.State.ResumeID)
```

Checkpoint 是诊断面；正常业务只需要保存 Thread key。多进程服务必须提供持久化、支持原子 Finalize 和 lease token 校验的 Store，不能使用 `memory.Store` 做跨进程协调。

## 6. Stream：一条 typed Event 流

```go
stream := agent.Stream(ctx, "explain and fix the failure")
fmt.Printf("run=%s\n", stream.RunID())

for ev := range stream.Events() {
	switch e := ev.(type) {
	case adaptor.TextDelta:
		if e.Phase == adaptor.PhaseContent {
			fmt.Print(e.Text)
		}
	case adaptor.Thinking:
		if e.Phase == adaptor.PhaseContent {
			log.Printf("thinking: %s", e.Text)
		}
	case adaptor.ToolCall:
		if e.Phase == adaptor.PhaseStart {
			log.Printf("tool %s (%s)", e.Name, e.ID)
		}
	case adaptor.ToolResult:
		log.Printf("tool result %s", e.ID)
	case adaptor.Dropped:
		log.Printf("dropped %d events: %v", e.Count, e.ByKind)
	case adaptor.RunFinished:
		log.Printf("terminal hint failed=%v", e.Failed)
	}
}

result, err := stream.Result()
```

`RunFinished` 是流上的提示，最终成功、类型化业务失败和完整 Result 以 `stream.Result()` 为准。

默认缓冲大小为 1024。消费者跟不上时，可丢事件会聚合成 `Dropped`。只有确实需要无丢弃审计且能持续消费时才使用：

```go
agent := adaptor.New(driver,
	adaptor.WithEventBuffer(4096),
	adaptor.WithBlockingEvents(),
)
```

blocking 模式会把慢消费者的压力传回执行端。提前停止消费时应调用 `stream.Cancel()`，然后继续收口 `Events()` 与 `Result()`。

## 7. HITL 审批

### 7.1 回调方式

终端程序或自动策略可在构造或调用处安装 handler：

```go
agent := adaptor.New(
	claude.Driver(claude.Config{Model: "claude-sonnet-4"}),
	adaptor.WithPolicy(adaptor.Policy{
		Sandbox: adaptor.WorkspaceWrite,
		Approvals: adaptor.ApprovalPolicy{
			Permission: adaptor.ApprovalAsk,
			PlanReview: adaptor.ApprovalAsk,
			Question:   adaptor.QuestionAsk,
			Timeout:    2 * time.Minute,
		},
	}),
	adaptor.OnApproval(func(ctx context.Context, req *adaptor.ApprovalRequest) error {
		switch req.Kind {
		case adaptor.ApprovalQuestion:
			if len(req.Choices) > 0 {
				return req.Answer(ctx, req.Choices[0].Key)
			}
			return req.Answer(ctx, "please continue with the safest option")
		case adaptor.ApprovalPermission, adaptor.ApprovalPlanReview:
			return req.Approve(ctx)
		default:
			return req.Deny(ctx, "unsupported request")
		}
	}),
)
```

Question 不一定有 Choices；开放问题可以用自由文本调用 `Answer`。

`ApprovalsAutoDeny` 只有在 Driver 对 Permission、PlanReview、Question 三类都声明 auto-reject 支持时才可用，不是跨 provider 的通用无人值守配置。Question 保持 `QuestionInherit` 时会采用保守默认 auto-deny；这与显式 `QuestionAutoDeny` 的严格 capability 要求不同。`DenyAll(reason)` 与 `ApproveAll()` 只处理已经按 capability 路由成 Ask 的请求，其中 `ApproveAll()` 会拒绝 Question，因为问题没有可安全合成的答案。

### 7.2 事件方式

Web UI 可以保存请求对象，在另一个 goroutine 或 HTTP handler 中回答：

```go
var pending sync.Map // request ID -> *adaptor.ApprovalRequest

stream := agent.Stream(ctx, prompt)
for ev := range stream.Events() {
	if req, ok := ev.(*adaptor.ApprovalRequest); ok {
		pending.Store(req.ID, req)
		pushApprovalCard(req)
	}
}
```

回答端：

```go
value, ok := pending.LoadAndDelete(requestID)
if !ok {
	return adaptor.ErrApprovalExpired
}
req := value.(*adaptor.ApprovalRequest)
if err := req.Approve(ctx); err != nil {
	return err
}
```

重复或迟到回答匹配 `ErrApprovalResolved`，过期匹配 `ErrApprovalExpired`，错误应答方法匹配 `ErrApprovalKindMismatch`。`bridges/sse.MapApprovalError` 可把这些错误映射为稳定 HTTP 状态和 JSON body。

## 8. Skills、MCP 与 profile

### 8.1 本地和 catalogue skills

直接声明本地或 inline skill：

```go
agent := adaptor.New(driver,
	adaptor.WithSkills(
		skill.Dir("./skills/code-review"),
		skill.Inline("release-check", "# Release check\nVerify changelog and tests."),
	),
)
```

使用静态 catalogue 和 key：

```go
catalogue := skill.Set{
	"code-review": skill.Dir("./skills/code-review"),
	"deploy":      skill.Require(skill.Dir("./skills/deploy"), "required for releases"),
}

agent := adaptor.New(driver,
	adaptor.WithSkillProvider(catalogue),
	adaptor.WithSkills(skill.Key("code-review")),
)
```

`skill.Key` 必须由 provider 解析；未知 key 匹配 `ErrSkillNotFound`。坏 archive、缺少 `SKILL.md` 或 materializer 失败都会在 Driver 启动前失败。

### 8.2 MCP

```go
agent := adaptor.New(driver,
	adaptor.WithMCP(
		mcp.HTTP("docs", "https://example.com/mcp",
			mcp.WithBearerTokenEnv("DOCS_MCP_TOKEN"),
			mcp.Required("documentation lookup is required"),
		),
		mcp.Stdio("repo-tools", "npx",
			mcp.Args("repo-mcp", "--readonly"),
			mcp.Env(map[string]string{"LOG_LEVEL": "warn"}),
		),
	),
)
```

调用处 `WithMCP(...)` 替换 Agent 默认集合，不是追加。需要同时保留默认 server 时，应在调用处传入完整集合。MCP transport 不受 Driver 支持时匹配 `ErrMCPTransportUnsupported`。

### 8.3 Profile 选择

复用 provider 原生 profile：

```go
adaptor.WithProfile(profile.Native())
```

使用宿主专用目录：

```go
adaptor.WithProfile(profile.Dedicated(profileDir))
```

从本机原生 profile 派生，并共享 OAuth 登录态：

```go
adaptor.WithProfile(profile.CloneNative(
	profileDir,
	profile.CopySettings(),
	profile.CopyMCP(),
	profile.CopySkills(),
	profile.LinkAuth(),
))
```

`LinkAuth` 优先于复制 OAuth token 文件；无法创建共享链接时会失败，不会静默复制。

provider-specific profile 环境变量如果已经在 Driver 的 `CommonConfig.Env` 中显式设置，会优先于 profile selection；之后才依次考虑 selection、进程环境和 provider 默认目录。

### 8.4 Profile resources

```go
agent := adaptor.New(driver,
	adaptor.WithSkillProvider(catalogue),
	adaptor.WithProfile(profile.Dedicated(profileDir)),
	adaptor.WithProfileResources(profile.Resources{
		Skills: []skill.Ref{skill.Key("code-review")},
		MCP: []mcp.Server{
			mcp.HTTP("docs", "https://example.com/mcp"),
		},
		Instructions: profile.Text("Follow the ACME engineering standard."),
	}),
)
```

`ProfileState(ctx)` 只读取 desired/observed 状态；`SyncProfile(ctx)` 才执行物化。不支持的资源会出现在 `ResourceSnapshot` 的 warning/error 中，不会伪装成已管理。

`SelectSkills(ctx, keys)` 是进程内选择覆盖。长期用户偏好应由宿主存储，并在下次构造 Agent 时重新声明。

## 9. Workspace 与 runtime services

### 9.1 直接工作目录

```go
result, err := agent.Run(ctx, prompt, adaptor.WithWorkspace(repoDir))
```

### 9.2 WorkspaceSpec

```go
agent := adaptor.New(driver,
	adaptor.WithWorkspace(repoDir),
	adaptor.WithWorkspaceSpec(adaptor.GitWorktreeWorkspace{
		BaseRef:           "main",
		BranchTemplate:    "agent/{run_id}",
		WorktreeParentDir: worktreeRoot,
	}),
	adaptor.WithWorkspaceManager(workspaceManager),
)
```

`WorkspaceManager.Resolve` 返回实际 `WorkspaceLease`，Driver 使用 lease 的 `CWD`。`Release` 必须并发安全并按 `WorkspaceReleaseMode` 清理。未提供 manager 时只是 passthrough，不会替宿主创建 git worktree。

### 9.3 声明式服务

```go
agent := adaptor.New(driver,
	adaptor.WithServices(
		adaptor.ServiceSpec{
			ID:      "preview",
			Name:    "preview server",
			Command: "npm run dev",
			Port:    4317,
		},
	),
	adaptor.WithServiceManager(serviceManager),
)
```

`ServiceManager.Ensure` 在 Driver 前运行并返回 `[]ServiceRef`；执行结束后调用 `ReleaseByRun`。如果 ServiceRef 带 `MCP`，该 server 会追加到本次 MCP 集合。`SecretEnv` 只进入 Driver 子进程，不会出现在 `Result.Services()`。

没有 `WithServiceManager` 时，`WithServices` 声明不会自动启动进程或虚构 endpoint。

生态包提供的动态 sidecar 使用 `RunServiceProvider`。它的 `RunAttachment.Events` 会直接并入当前 Stream；调用方不需要再合并第二条 channel。

## 10. Inspect 与 profile 运维

环境体检：

```go
report, err := agent.Inspect().Environment(ctx)
if err != nil {
	return err
}
if !report.Healthy {
	for _, check := range report.Checks {
		log.Printf("%s: %s", check.Code, check.Message)
	}
}
```

其余只读检查：

```go
models, err := agent.Inspect().Models(ctx)
quota, err := agent.Inspect().Quota(ctx)
schema, err := agent.Inspect().ConfigSchema(ctx)
skills, err := agent.Inspect().Skills(ctx)
_, _, _, _ = models, quota, schema, skills
```

Driver 不提供动态 probe 时会返回静态 descriptor fallback 或明确不可用报告，不应把 `Available=false` 当作调用失败。

Profile 运维：

```go
before, err := agent.ProfileState(ctx)
if err != nil {
	return err
}
after, err := agent.SyncProfile(ctx)
_, _ = before, after
```

设置页面可用 `Models`、`ConfigSchema`、`Quota` 和 `Skills`；onboarding 可用 `Environment`；desired/observed 资源页使用 `ProfileState`。

## 11. 结构化输出

最简方式是 `RunAs[T]`：

```go
type Triage struct {
	Severity  string   `json:"severity"`
	Component string   `json:"component"`
	Labels    []string `json:"labels"`
}

triage, result, err := adaptor.RunAs[Triage](
	ctx,
	agent,
	"classify this issue",
	adaptor.WithWorkspace(repoDir),
)
if err != nil {
	return err
}
fmt.Printf("%s: %s\n", triage.Severity, result.Summary)
```

`RunAs` 同样接受 Thread：

```go
triage, result, err := adaptor.RunAs[Triage](ctx, thread, prompt)
```

流式消费后解码：

```go
stream := agent.Stream(ctx, prompt,
	adaptor.WithSchema[Triage](
		adaptor.SchemaStrict(),
		adaptor.SchemaName("issue_triage"),
	),
)
for ev := range stream.Events() {
	render(ev)
}
result, err := stream.Result()
if err != nil {
	return err
}
var triage Triage
if err := result.Decode(&triage); err != nil {
	return err
}
```

选择模式：

- `SchemaStrict()`：要求 provider 原生约束，能力不足会在进程前失败。
- `SchemaFlexible()`：原生优先，可回退到 prompt + 本地校验。
- `SchemaPromptOnly()`：明确接受较弱的 prompt + 本地校验合同。

来自外部 registry 的 schema 使用 `WithSchemaJSON(schemaBytes, ...)`。schema 名称、描述、enum、pattern 和 examples 可能传给 provider，不要写入秘密。

## 12. Event recorder 与 Thread store 的边界

二者保存完全不同的数据：

| 组件 | 保存内容 | 典型 key |
|---|---|---|
| `threadstore.Store` | provider resume checkpoint、兼容 fingerprint、active/archive 状态和 lease | Thread key |
| `sessionrecorder.EventRecorder` | 宿主需要回放的 typed Event 和跨 run `HostSeq` | RunID 或宿主稳定会话 key |

Thread store 不是 UI 聊天历史。Event recorder 也不参与 resume、fork 或并发 lease。

### 12.1 内存 recorder

```go
recorder := sessionrecorder.NewEventRecorder(
	sessionrecorder.NewMemoryEventBackend(),
)
defer recorder.Close()

stream := thread.Stream(ctx, prompt)
for ev := range stream.Events() {
	if _, err := recorder.Record(ctx, "thread_issue_123", ev); err != nil {
		stream.Cancel()
		return err
	}
	render(ev)
}
result, err := stream.Result()
```

### 12.2 JSONL recorder

```go
backend, err := sessionrecorder.NewJSONLEventBackend(eventDir)
if err != nil {
	return err
}
recorder := sessionrecorder.NewEventRecorder(backend)
defer recorder.Close()
```

构造失败会直接返回错误，不会退回内存。默认每次 append 同步到存储；明确接受 buffered durability 时可使用 `WithoutJSONLEventSyncOnAppend()` 并自行调用 `Flush()`。

`EventRecorder.Since(ctx, key, afterHostSeq)` 使用在一个 recorder key 内严格递增的 `HostSeq` 恢复事件。默认 key validator 只接受单个跨平台安全文件名片段；Thread key 含 `/` 时，应由宿主映射为稳定且无碰撞的 recorder key，而不是直接拿来当 JSONL 文件名。

单个 recorder 实例在进程内分配 HostSeq。多进程宿主需要 sticky routing，或实现由数据库/协调器原子分配序号的 `EventBackend`。

## 13. 生产集成检查

- 为 Agent 设置明确 workspace、Policy、timeout 和 Identity。
- 无状态任务直接使用 Agent；只有需要 provider 续接时才使用 Thread。
- 多进程 Thread 使用持久化 Store，并实现原子 Finalize 与 token lease。
- 始终消费 Stream 到关闭并读取 `Result()`；提前退出时先 `Cancel()`。
- Web HITL 保存 `*ApprovalRequest` 本身，并处理重复、过期和 Kind 不匹配错误。
- 对 `RunError` 保留的 Result 做审计，但不要把部分 Text 当作成功。
- skill、MCP、schema、profile 和 runtime 失败都按启动前错误处理，不做静默降级。
- 用 `ProfileState` 区分 desired 与 observed；需要物化时显式调用 `SyncProfile`。
- 将 Thread checkpoint 与 Event 历史放在不同存储边界。
- 自定义 Driver 必须运行 `adaptertest.TestDriver`，并让 Descriptor 与真实能力一致。
