# agent-adaptor AGENTS

本文件定义当前仓库已经拍板的实现边界。后续设计、实现、重构、评审都必须以这里为准。

## 1. 项目定位

`agent-adaptor` 是一个 Go SDK。

它负责：

- 统一调用不同本地 agent
- 统一 `Run/Start` 执行语义
- 提供可选的 session、workspace、skills、runtime 注入点
- 允许宿主把它嵌入 CLI、桌面应用、HTTP/gRPC 服务、定时任务

它不负责：

- 内置 HTTP/gRPC server
- 内置队列、调度器、租户系统、鉴权系统
- 强制数据库或分布式锁依赖
- 自动决定“这次到底该用哪个 agent”

一句话：这是纯 SDK，服务化只是宿主的引入方式。

## 2. 北极星

### 2.1 只有一套执行语义

所有执行路径都必须收敛到同一条内部流程：

1. 绑定默认值与 per-call override 合并
2. 解析为统一 `resolvedInvocation`
3. session 协调
4. adapter 执行
5. checkpoint 持久化与结果归档

不允许再出现第二套执行入口、第二套默认值合并逻辑、第二套 session 语义。

### 2.2 默认 Agent 绑定优先

当前主路径不是 registry-first，也不是每次调用时再选 driver。

当前主路径是：

- 构造时绑定默认 Agent
- 调用时直接 `sdk.Run(...)` / `sdk.Start(...)`
- 多 Agent 场景通过 `sdk.Agent(name)` 取命名绑定

### 2.3 无状态默认，状态化可选

- 不注入 `SessionStore` 时：默认无状态
- 注入 `SessionStore` 时：支持 `SessionKey` / `SessionID` 复用与新建

### 2.4 可靠性与可持续维护优先

**"可靠性"和"可持续维护"是本项目最高优的技术目标，高于"零依赖"这类次要洁癖。**

判断是否引入一个外部依赖时，看三件事：

1. **可靠性**：它是否让关键职责（协议解析、IPC、schema、session）更可靠，减少手写 bug、减少协议漂移风险
2. **可持续维护**：它是否由官方/主流社区维护，版本升级、问题追踪、文档、CVE 响应都更可持续
3. **可局部化**：它是否能被隔离在明确的职责边界内（adapter 内部、bridges 子包、构建时工具），不会污染 core SDK 的公共 API / 公共语义

三条都占优或显著占优 → **优先采用**，哪怕 `go.sum` 会多出几行。

三条中与手写方案接近 → **倾向手写**，减少依赖噪音与审计面。

硬约束：

- "不引入依赖"从不是独立目标；不得以"零依赖洁癖"为唯一理由拒绝一个在可靠性/可维护性上明显占优的库
- 依赖必须**局部化**：
  - provider SDK / CLI 协议客户端 → 仅 adapter 包 import
  - AG-UI / SSE 等传输协议 → 仅 `pkg/bridges/*` import
  - 构建时工具（schema 生成器等）→ `//go:generate`，不入 runtime `go.sum`
- 每次新增顶层 `require` 都要在相关 workstream 文档的"依赖选型"段里写清楚上述三条的评估
- 已有的合理引入案例：`github.com/sourcegraph/jsonrpc2`（codex app-server，JSON-RPC 2.0 over stdio，FIFO 同步派发）、`github.com/ag-ui-protocol/ag-ui/sdks/community/go`（bridges，AG-UI 协议官方 Go SDK）、`github.com/atombender/go-jsonschema`（构建时，codex schema 生成）

## 3. 当前公共 API 心智

### 3.1 `SDK`

```go
type SDK interface {
	Run(ctx context.Context, prompt string, opts ...RunOption) (RunResult, error)
	Start(ctx context.Context, prompt string, opts ...RunOption) (RunHandle, error)

	Default() Runner
	Agent(name string) (Runner, error)

	Admin() AdminAPI
}
```

语义：

- `Run` / `Start` 永远针对默认 Agent
- `Default()` 用于组合与注入
- `Agent(name)` 用于命名 Agent
- `Admin()` 只做控制面，不做执行

### 3.2 `Runner`

```go
type Runner interface {
	Run(ctx context.Context, prompt string, opts ...RunOption) (RunResult, error)
	Start(ctx context.Context, prompt string, opts ...RunOption) (RunHandle, error)
}
```

`Runner` 是唯一执行合同。无论来自默认 Agent 还是命名 Agent，都不能分叉语义。

### 3.3 `RunHandle`

```go
type RunHandle interface {
	Events() <-chan RunEvent
	StreamEvents() <-chan StreamPayload
	RunID() string
	Wait(ctx context.Context) (RunResult, error)
	Cancel(ctx context.Context) error
	DecisionRequests() <-chan DecisionRequest
	ResolveDecision(requestID string, resp DecisionResponse) error
}
```

### 3.4 输出合同

`RunResult` / `DriverRunResult` 的输出层必须分层表达，不能再混在一个字段里：

- `Output`：最终 assistant-facing 文本输出；没有 assistant 文本时允许为空
- `RawStreams.Stdout` / `RawStreams.Stderr`：本次运行的原始 stdout / stderr 完整内容
- `Transcript`：标准化语义条目，用于统一渲染 assistant / thinking / tool / result
- `Summary`：适合列表、日志、issue comment 的简短摘要
- `Result`：adapter 识别出的终局 result 事件原始 JSON；用于 provider-specific 细节与审计

硬约束：

- `Output` 不允许再承载原始 stdout dump
- `Output` 不允许自动拼接 `Summary` 或终局 `Result`
- `Run()` 与 `Start().Wait()` 都必须返回同样可用的 `RawStreams`
- `Transcript` 必须来自 adapter 对正式协议的解析，不能来自 shared helper 的 JSON 猜测

### 3.5 `AdminAPI`

```go
type AdminAPI interface {
	Default() AgentAdmin
	Agent(name string) (AgentAdmin, error)
	Agents() []AgentInfo
}
```

```go
type AgentAdmin interface {
	Info() AgentInfo
	CheckEnvironment(ctx context.Context) (EnvironmentReport, error)
	ListModels(ctx context.Context) ([]ModelInfo, error)
	DetectModel(ctx context.Context) (*DetectedModel, error)
	GetProfile(ctx context.Context) (AgentProfile, error)
	ConfigSchema(ctx context.Context) (*ConfigSchema, error)
	GetQuota(ctx context.Context) (QuotaReport, error)
	ListSkills(ctx context.Context) (SkillSnapshot, error)
	SetSelectedSkills(ctx context.Context, keys []string) (SkillSnapshot, error)
}
```

硬约束：

- `AdminAPI` 不允许再长出 `Run/Start`
- 控制面与执行面共享同样的“默认/命名 Agent”心智

## 4. 绑定模型

### 4.1 `AgentBinding`

```go
type AgentBinding interface {
	Adapter() DriverAdapter
	Config() any
	Defaults() AgentDefaults
}
```

### 4.2 `DriverAdapter`

```go
type DriverAdapter interface {
	Descriptor() DriverDescriptor
	ValidateConfig(cfg any) error
	Run(ctx context.Context, req DriverRunRequest, sink EventSink) (DriverRunResult, error)
}
```

### 4.3 内置构造方式

对调用方：

- `codex.New(cfg, opts...)`
- `claude.New(cfg, opts...)`
- `cursor.New(cfg, opts...)`

它们返回的是配置完成的 `AgentBinding`，不是裸 adapter。

对底层扩展：

- `codex.NewAdapter()`
- `claude.NewAdapter()`
- `cursor.NewAdapter()`

### 4.4 SDK 构造选项

当前正确用法：

```go
sdk := agentadaptor.New(
	agentadaptor.WithDefaultAgent(codex.New(agentadaptor.CodexConfig{
		Model: "gpt-5.4",
	})),
)
```

可选能力注入：

- `WithAgent(name, binding)`
- `WithSessionStore(store)`
- `WithWorkspaceManager(manager)`
- `WithSkillProvider(provider)`
- `WithSkillSet(set)`
- `WithSkillMaterializer(materializer)`
- `WithRuntimeServiceManager(manager)`
- `WithEventBuffer(runBuf, streamBuf, policy)`

约束：

- 默认 Agent 必须显式通过 `WithDefaultAgent(...)` 提供
- `"default"` 是保留名，不能作为 `WithAgent(name, ...)` 的名称
- `New(opts...)` 保持单返回值，构造失败直接 panic

### 4.5 绑定级默认值与调用级覆盖

绑定级 `AgentOption` 当前包括：

- 身份 / 工作区 / profile：`WithDefaultIdentity`、`WithDefaultWorkspace`、`WithNativeProfile`、`WithDedicatedProfile`、`WithCloneProfile`、`WithCloneProfileFrom`
- 能力注入：`WithDefaultSkills`、`WithDefaultMCP`、`WithDefaultInstructions`、`WithDefaultRuntimeServices`
- 策略与流式：`WithDefaultRunPolicy`、`WithDefaultStreaming`
- HITL handler：`WithDefaultPermissionHandler`、`WithDefaultPlanReviewHandler`、`WithDefaultQuestionHandler`
- 其它元数据：`WithDefaultMetadata`

调用级 `RunOption` 当前包括：

- session：`WithSession`、`WithSessionKey`、`WithContinueSession`、`WithNewSession`、`WithForkSession`
- 运行上下文：`WithWorkspace`、`WithRuntimeServices`、`WithSkills`、`WithMCP`、`WithInstructions`
- 策略与流式：`WithRunPolicy`、`WithStreaming`、`WithoutStreaming`
- HITL handler：`WithPermissionHandler`、`WithPlanReviewHandler`、`WithQuestionHandler`
- 其它元数据：`WithMetadata`、`WithAgentIdentity`

## 5. 调用方推荐使用方式

完整使用示例（单 Agent、多 Agent、session 复用、绑定默认值与调用覆盖）见 [`docs/usage-guide.md`](./docs/usage-guide.md)。

## 6. Session 语义硬约束

必须保留并清晰支持：

- `continue_or_start`
- `continue_only`
- `start_new`
- `fork`
- `stateless`

当前约束：

- `SessionKey` 是业务稳定键
- `SessionID` 是具体会话句柄
- 失败运行不能默认污染健康 session
- 只有有效 checkpoint 才允许持久化新 session 状态
- 非零退出默认不产生有效 checkpoint

### 6.1 维度对齐

宿主在 SDK + AG-UI bridge + 自己业务三层之间反复踩 ID 命名混乱。下图把四层 ID 在同一坐标里对齐：

```
            Web IDE / Workflow
                   │
                   ▼
   ┌────────── ThreadID ──────────┐   ① 业务/UI 层
   │   AG-UI 协议字段              │
   │   "task-req-12345"           │
   └──────────────┬───────────────┘
                  │ agui bridge: ("agui", ThreadID)
                  ▼
   ┌──────── SessionKey ──────────┐   ② SDK API 入参
   │   (Namespace, Key) 二元组术语 │
   │   ("agui", "task-req-12345") │
   │   注：SDK 没有 SessionKey 类型 │
   └──────────────┬───────────────┘
                  │ SessionStore.Resolve()
                  ▼
   ┌──────── SessionID ───────────┐   ③ SDK driver 句柄
   │   "claude-9c22b132-7f3a4e..."│
   └──────────────┬───────────────┘
                  │ 多次 sdk.Start(...)
                  ▼
   ┌─────────── RunID ────────────┐   ④ 执行实例
   │   "run-2026-04-26-xx"        │
   └──────────────────────────────┘
```

| 层 | ID | 来源 | 容易混淆点 |
|---|---|---|---|
| ① 业务/UI 层 | `ThreadID` | 业务方 / Web IDE / Workflow 下发 | AG-UI 协议字段；SDK core 不认识它，只 `pkg/bridges/agui` 知道 |
| ② SDK API 入参 | `SessionKey = (Namespace, Key)` | 宿主，或 agui bridge 自动派生 `("agui", ThreadID)` | **是个二元组术语，不是单一字段名 — SDK 公共 API 里不存在 `SessionKey` 类型**；它在 `SessionRequest` 里以 `(Namespace, Key)` 二元组形式出现 |
| ③ SDK driver 句柄 | `SessionID` | SDK 自己（fingerprint based） | adapter 用它做 resume；同一 SessionKey 在不同时间点可对应不同 SessionID |
| ④ 执行实例 | `RunID` | SDK 在 `Start()` 内分配 | 一个 SessionID 上跑 N 个 RunID |

完整 fork / continue 流转示例与命名陷阱速查见 [`docs/usage-guide.md`](./docs/usage-guide.md) "宿主集成 — 维度对齐" 与 "命名陷阱" 段。

## 7. adapter 与 helper 的职责边界

### 7.1 shared helper 负责什么

`internal/clihelper` 只负责：

- 启进程
- 传 stdin
- 收 stdout / stderr
- 聚合原始 stdout / stderr
- 发送原始 chunk 事件
- 把原始 chunk tee 给 adapter parser callback

### 7.2 shared helper 不负责什么

它不负责：

- 解析 provider/CLI 协议
- 把 stdout/stderr 解释成 assistant / tool / result 语义
- 递归扫描 JSON 猜 `session_id`
- 判断哪个字段是正式 checkpoint
- 推断失败后 session 是否仍可恢复

### 7.3 checkpoint 必须由各 adapter 自己解析

每个内置 adapter 都必须：

- 解析自己的 stdout/stderr 正式协议事件
- 从同一次解析同时产出 `Transcript`、`Output`、`Summary`、`Result`
- 只识别自己的正式协议事件
- 只接受顶层、明确的 checkpoint 字段
- 自己决定 `DriverCheckpoint.Valid`

## 8. 当前阶段的明确非目标

默认禁止进入 core SDK：

- 内置 HTTP/gRPC server
- 内置 scheduler / queue / dispatcher
- 自动 agent routing
- profile 存储系统
- broker / planner / router 逻辑

如果未来需要 profile/resolver 或服务示例，应放在 core 之上，而不是把 core 改成半个服务框架。

## 9. 对未来修改者的硬要求

- 不允许复活 `WithDriver(...)`、`sdk.Codex(...)`、`sdk.Driver(...)`、`Registry`
- 不允许再引入第二套执行入口
- 不允许让 shared helper 偷吃 adapter 的协议职责
- 不允许再把原始 stdout/stderr 混塞进 `Output`
- 不允许把宿主服务能力直接塞回 core SDK
- 文档必须和代码的当前公共语义保持一致，不能保留已删除 API 的示例
- 不允许手工编辑 `codex/appserver/generated.go` 或 `codex/appserver/schema/` 下的 JSON；协议同步必须走 `codex app-server generate-json-schema` + `go generate`
- 不允许以"零依赖"为唯一理由拒绝一个在可靠性或可持续维护上明显占优的外部库；依赖引入按 §2.4 的三条评估，评估结论必须落到 workstream 文档

## 10. Streaming 是第二条可选通道

`RunHandle` 在原有 `Events()` 之外提供了 `StreamEvents() <-chan StreamPayload`。核心约束：

- `Run / Start` 语义不变；未开 `WithStreaming()` 时 SDK 行为与历史完全一致
- streaming 不是第二条 Run 入口；所有执行仍然走同一份 `Runner.Run/Start` + `adapter.Run(ctx, req, sink)`
- `StreamPayload.Role` 是可选的方向维度，零值（`RoleAssistant`）= v0.8 行为完全一致。**adapter 必须保持 Role 零值**；`RoleUser` 是 bridge / 宿主合成 user-side text 事件的专属标记（见 [`docs/workstream-user-message-event.md`](./docs/workstream-user-message-event.md)）

完整实施规则、adapter 合同、宿主集成指南见：[`docs/workstream-streaming-chat.md`](./docs/workstream-streaming-chat.md) / [`docs/streaming-adapter-contract.md`](./docs/streaming-adapter-contract.md) / [`docs/streaming.md`](./docs/streaming.md) / [`docs/workstream-user-message-event.md`](./docs/workstream-user-message-event.md)。

## 11. HITL 是第三条可选通道（host-intent 单维度合同）

`RunHandle` 在 `Events()` / `StreamEvents()` 之外提供了两个 HITL 相关方法：

- `DecisionRequests() <-chan DecisionRequest`——异步模式下宿主消费决策请求
- `ResolveDecision(requestID, resp)`——异步模式下宿主回填决策

`RunPolicy.HumanDecision` 是唯一的宿主意图合同（`HumanDecisionPolicy{Permission, PlanReview, Question, Timeout, OnTimeout, OnReject, MaxRetries}`），三类决策共享。Adapter 端通过 `DecisionCapableSink.RequestDecision(ctx, DecisionRequest)` 阻塞获取决策，SDK 负责按 Kind 分派到 typed handler（同步模式）或 `DecisionRequests()` channel（异步模式）。

核心约束：

- `Run / Start` 语义不变；`HumanDecisionPolicy{}` 零值直接跑 = 保守默认（Permission/PlanReview `Ask`，Question `AutoReject`，Timeout 30s，OnTimeout/OnReject `Abort`）
- HITL 不是第四条 Run 入口；三类事件（Permission / PlanReview / Question）共享统一的 `DecisionRequest/Response`、`StreamHITLRequested/Resolved` 协议形状
- 失败归因结构化（`RunResult.Failure.HumanDecision`），`(*RunFailure).IsHumanDecision()` / `IsRejected()` / `IsTimedOut()` 为粗粒度辨别的 nil-safe 语法糖
- Adapter 必须在 `Descriptor.RunPolicyCaps.Permission/PlanReview/Question` 上如实声明 `{Ask, AutoApprove, AutoReject, Retry}` 能力矩阵；宿主写不支持的 `Ask` 时 SDK 在 Start 前返回 `ErrHumanDecisionModeUnsupported`

完整设计、分派规则、宿主三种接入模式（声明式 / 同步 handler / 异步 channel）、bridge 层映射见 [`docs/workstream-hitl-v2.md`](./docs/workstream-hitl-v2.md) 与 [`docs/run-policy.md`](./docs/run-policy.md)。
