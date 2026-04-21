# agent-adaptor AGENTS

本文件定义当前仓库已经拍板的实现边界。后续设计、实现、重构、评审都必须以这里为准。

## 1. 项目定位

`agent-adaptor` 是一个纯粹的 Go SDK。

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
	Wait(ctx context.Context) (RunResult, error)
	Cancel(ctx context.Context) error
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
	ListSkills(ctx context.Context) (SkillSnapshot, error)
	SyncSkills(ctx context.Context, desired []string) (SkillSnapshot, error)
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
- `WithSkillCatalog(catalog)`
- `WithSkillAssembler(assembler)`
- `WithRuntimeServiceManager(manager)`

约束：

- 默认 Agent 必须显式通过 `WithDefaultAgent(...)` 提供
- `"default"` 是保留名，不能作为 `WithAgent(name, ...)` 的名称
- `New(opts...)` 保持单返回值，构造失败直接 panic

## 5. 调用方推荐使用方式

### 5.1 单 Agent

```go
sdk := agentadaptor.New(
	agentadaptor.WithDefaultAgent(codex.New(agentadaptor.CodexConfig{
		Model: "gpt-5.4",
	})),
)

result, err := sdk.Run(ctx, "fix the failing tests")
```

### 5.2 多 Agent

```go
sdk := agentadaptor.New(
	agentadaptor.WithDefaultAgent(codex.New(agentadaptor.CodexConfig{
		Model: "gpt-5.4",
	})),
	agentadaptor.WithAgent("review", claude.New(agentadaptor.ClaudeConfig{
		Model: "claude-sonnet-4",
	})),
)

review, err := sdk.Agent("review")
result, err := review.Run(ctx, "review the patch")
```

### 5.3 session 复用

```go
sdk := agentadaptor.New(
	agentadaptor.WithDefaultAgent(codex.New(agentadaptor.CodexConfig{
		Model: "gpt-5.4",
	})),
	agentadaptor.WithSessionStore(store),
)

result, err := sdk.Run(
	ctx,
	"continue issue-123",
	agentadaptor.WithSessionKey("company-1", "issue-123"),
)
```

### 5.4 绑定默认值 + 调用覆盖

绑定时可以设置：

- `WithDefaultIdentity`
- `WithDefaultWorkspace`
- `WithDefaultSkills`
- `WithDefaultPermissions`
- `WithDefaultInstructions`
- `WithDefaultMetadata`

调用时可以覆盖：

- `WithSession`
- `WithSessionKey`
- `WithContinueSession`
- `WithNewSession`
- `WithForkSession`
- `WithWorkspace`
- `WithSkills`
- `WithPermissions`
- `WithInstructions`
- `WithMetadata`
- `WithAgentIdentity`

覆盖顺序固定：

- per-call `RunOption`
- `AgentBinding` defaults
- config/internal defaults

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

## 10. Streaming 是第二条可选通道

`RunHandle` 在原有 `Events()` 之外提供了 `StreamEvents() <-chan StreamPayload`。这条通道的职责边界是硬的：

- `Run / Start` 语义不变；未开 `WithStreaming()` 时 SDK 行为与历史完全一致
- streaming 不是第二条 Run 入口；所有执行仍然走同一份 `Runner.Run/Start` + `adapter.Run(ctx, req, sink)`
- 结构化业务事件只通过 `sink.EmitStream(StreamPayload)` 发射；不得混进 `RunEvent.Data`
- `clihelper` 不感知 streaming；streaming-aware adapter 自行选择协议通路（codex 切 `codex app-server`，claude / cursor 追加各自 CLI flag）
- `pkg/bridges/agui` 和 `pkg/bridges/sse` 是主 module 下的可选子包；它们只读 `StreamEvents()`，不得调用 adapter 内部、不得重新进入 Run 路径、不得污染 core
- `StreamPayload.Sequence / Timestamp` 由 SDK 在 `EmitStream` 时统一赋值，adapter 不自己写
- HITL 在 v1 是 audit-only：adapter 自动 deny 并通过 `StreamHITLRequested` 透传，不阻塞

与 streaming 相关的完整规划：`docs/workstream-streaming-chat.md`；adapter 实施规则：`docs/streaming-adapter-contract.md`；宿主集成指南：`docs/streaming.md`。
