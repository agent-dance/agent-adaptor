# Workstream: Streaming Chat / AG-UI Bridge

> 状态：历史设计记录。当前宿主用法见 [`streaming.md`](../streaming.md)，当前 adapter 合同见 [`streaming-adapter-contract.md`](../streaming-adapter-contract.md)。本文中早期 “本期不做 HITL / audit-only” 的段落是当时的设计背景，不代表当前 HITL v2 行为。

## 0. 先把结论写死

- 核心增量：`RunHandle.StreamEvents() <-chan StreamPayload` 作为第二条出事件通道
- `StreamPayload` 是协议无关的中间表示，三家 adapter（codex / claude / cursor）未来都按同一份 schema 发事件
- 默认**不**开启 streaming；`WithStreaming()` 显式开启后，adapter 自动切到各自的 token-level 通路
- `codex` 开 streaming 时自动切到 `codex app-server`（JSON-RPC over stdio），不再走 `codex exec --json`
- AG-UI 翻译层与 SSE 传输层复用官方 Go SDK `github.com/ag-ui-protocol/ag-ui/sdks/community/go`，不自造事件类型
- AG-UI / SSE 桥接包放在 `pkg/bridges/{agui,sse}`，作为主 module 的普通子包；SDK 主 `go.mod` 接入 AG-UI SDK 作为直接依赖
- 本期**不做 HITL**；HITL 请求（审批、工具入参）只发一条 audit 级 `StreamPayload`，在 adapter 侧按 policy 自动 deny
- 事件背压默认策略 `BackpressureDropStream`（slow consumer 时丢 `StreamPayload` 并发一条 marker），宿主可显式切 `BackpressureBlock`

## 1. 目标

让宿主以最低成本拿到 agent 的 token-level 流式输出，无论是直接消费 Go channel 还是起一个 SSE chat 接口。

这次 workstream 明确允许对外新增 API（`RunHandle`、`DriverAdapter.EventSink`、options 集合），但不改动现有执行语义、不引入第二条 Run 入口、不破坏 `Run/Start` 一致性。

## 2. 为什么现在做

`paperclip` 以及其它宿主系统的聊天化接入需要 token-level 流式输出，但当前 SDK 只能给出行级 `RunEvent`：

- `claude/driver.go` 已跑 `stream-json`，但没开 `--include-partial-messages`，拿不到 delta
- `cursor/driver.go` 已跑 `stream-json`，但没开 `--stream-partial-output`
- `codex/driver.go` 走 `exec --json`，协议上就没有 token-level delta

继续走 stdout 路径意味着自己做 stream 协议解析，违反 §7 "shared helper 不识别 adapter 协议"。正确做法是让每个 adapter 在自己的通路上输出 **协议无关的 `StreamPayload`**，SDK 只负责通道和顺序，桥接层负责映射到 AG-UI。

Codex 侧事实已核验（2026-04-21，`codex-cli 0.120.0`）：

- `codex app-server` 提供完整 JSON-RPC 协议：60 个 ClientRequest、52 个 ServerNotification、9 个 ServerRequest（HITL）
- token-level 事件齐全：`item/agentMessage/delta`、`item/reasoning/textDelta`、`item/commandExecution/outputDelta`、`item/fileChange/outputDelta`
- 本机端到端验证：15 条 delta，平均 5 字符/条，字符流阶段 0.33s 内完成
- 协议 schema 可导出：`codex app-server generate-json-schema --out <dir>`

## 3. 非目标

本期不做：

- Claude / Cursor 的 streaming 实施（仅预留接口与文档扩展点）
- HITL 审批回路（仅暴露 audit 级事件）
- 长驻进程池 / 高并发 chat 专用优化（每 run 一个 `codex app-server` 子进程就是终态的起点）
- `STATE_SNAPSHOT / STATE_DELTA` 的 SDK 内生成（AG-UI 规范本就规定 state 是业务侧职责）
- 自造 AG-UI 事件类型 / SSE wire format（直接复用官方 Go SDK）

## 4. 核心合同

### 4.1 `StreamPayload`（新）

`StreamPayload` 是 adapter 向宿主发射的结构化事件，schema 固定，protocol-agnostic。

```go
type StreamKind string

const (
    StreamRunStarted       StreamKind = "run.started"
    StreamRunFinished      StreamKind = "run.finished"
    StreamRunError         StreamKind = "run.error"

    StreamStepStarted      StreamKind = "step.started"
    StreamStepFinished     StreamKind = "step.finished"

    StreamTextStart        StreamKind = "text.start"
    StreamTextContent      StreamKind = "text.content"
    StreamTextEnd          StreamKind = "text.end"

    StreamToolCallStart    StreamKind = "tool_call.start"
    StreamToolCallArgs     StreamKind = "tool_call.args"
    StreamToolCallEnd      StreamKind = "tool_call.end"
    StreamToolCallResult   StreamKind = "tool_call.result"

    StreamReasoningStart   StreamKind = "reasoning.start"
    StreamReasoningContent StreamKind = "reasoning.content"
    StreamReasoningEnd     StreamKind = "reasoning.end"

    StreamHITLRequested    StreamKind = "hitl.requested"  // audit-only in v1
    StreamHITLResolved     StreamKind = "hitl.resolved"   // audit-only in v1

    StreamDropped          StreamKind = "stream.dropped"  // backpressure marker
)

type StreamPayload struct {
    Kind       StreamKind
    Sequence   uint64         // strictly monotonic within one run
    RunID      string
    ThreadID   string         // adapter-native thread / conversation id
    TurnID     string
    MessageID  string         // stable within one text message lifecycle
    ToolCallID string
    Name       string         // tool name / step name
    Delta      string         // for *.content / *.args (non-empty string chunk)
    Args       map[string]any
    Result     map[string]any
    Usage      *Usage
    Error      *RunFailure
    Timestamp  time.Time
    Role       Role           // 仅 text.* 有意义；零值 = RoleAssistant
    Raw        map[string]any // provider-specific opaque passthrough
}
```

硬约束：

- `Sequence` 由 SDK 统一在 `EmitStream` 时赋值，adapter 不自己写
- `Delta` 可以是 1 字符也可以是 N 字符，但必须非空
- `Raw` 用来透传 adapter 不想 model 化的私有字段，宿主选择性消费
- 未来新增 `StreamKind` 不破坏现有消费者；宿主遇到未知 kind 应忽略
- `Role` 只在 `text.start / text.content / text.end` 上有意义；零值 =
  `RoleAssistant`，保持向后兼容。**adapter 必须保持 Role 零值**——
  `RoleUser` 是 bridge / 宿主侧合成 user-side 事件时才使用的标记（见
  `pkg/bridges/agui.RunAgentInput.UserTurnPayloads` 与
  `docs/workstream-user-message-event.md`）。

### 4.2 `StreamAwareDriver`（新）

adapter 通过实现可选接口声明支持能力：

```go
type StreamAwareDriver interface {
    StreamCapability() StreamCapability
}

type StreamCapability struct {
    Native       bool // underlying protocol is natively event-based
    TokenLevel   bool // text deltas are character-granularity
    Reasoning    bool // reasoning deltas are exposed
    ToolCallArgs bool // tool call argument streaming is exposed
    HITL         bool // human-in-the-loop requests are exposed
}
```

硬约束：

- 未实现 `StreamAwareDriver` 的 adapter 不受本次变更影响
- `StreamCapability` 仅用于 bridges / 宿主做降级；SDK 内部不据此改变行为

### 4.3 `EventSink` 扩展

`DriverAdapter.Run` 继续只接一个 `EventSink`，接口扩充：

```go
type EventSink interface {
    Emit(event RunEvent) error
    EmitStream(payload StreamPayload) error // 未开 streaming 时 no-op
}
```

硬约束：

- adapter 不感知 StreamPayload 是否最终被消费
- `EmitStream` 会自动覆盖 `Sequence / RunID / Timestamp`（如果 adapter 未填）
- `clihelper` 绝不调用 `EmitStream`，协议识别归 adapter（§7）

### 4.4 `DriverRunRequest.Streaming bool`（新）

本次 run 是否希望 adapter 以 streaming 模式执行，由 SDK 从 `WithStreaming` / `WithDefaultStreaming` 合并后填入。

硬约束：

- 这是 adapter 可选读的 hint；adapter 决定是否切内部通路
- 如果 adapter 未实现 `StreamAwareDriver`，SDK 仍传递该字段但不强求响应

### 4.5 `RunHandle` 扩展

```go
type RunHandle interface {
    Events() <-chan RunEvent           // 原有：子进程/生命周期元事件
    StreamEvents() <-chan StreamPayload // 新：结构化业务事件
    Wait(ctx context.Context) (RunResult, error)
    Cancel(ctx context.Context) error
    RunID() string                     // 新：AG-UI 需要在 RUN_STARTED 时带
}
```

硬约束：

- 未开 streaming 时 `StreamEvents()` 返回已关闭 channel，宿主可直接 range 立即退出
- `StreamEvents()` 的 producer 在 run 结束时关闭 channel
- `Events()` 与 `StreamEvents()` 互不影响，可同时消费

## 5. 宿主使用方式

### 5.1 最薄：Go channel 消费 token 流

```go
sdk := agentadaptor.New(
    agentadaptor.WithDefaultAgent(codex.New(agentadaptor.CodexConfig{
        Model: "gpt-5.4",
    })),
)

handle, err := sdk.Start(ctx, "write a haiku",
    agentadaptor.WithStreaming(), // 自动切 token-level 通路
)
if err != nil { /* ... */ }
defer handle.Cancel(ctx)

for ev := range handle.StreamEvents() {
    switch ev.Kind {
    case agentadaptor.StreamTextContent:
        fmt.Print(ev.Delta)
    case agentadaptor.StreamToolCallStart:
        fmt.Printf("\n[tool: %s]\n", ev.Name)
    case agentadaptor.StreamRunFinished:
        fmt.Printf("\n[done usage=%v]\n", ev.Usage)
    }
}
result, err := handle.Wait(ctx)
```

### 5.2 per-binding 默认开 / per-call 覆盖

```go
sdk := agentadaptor.New(
    agentadaptor.WithDefaultAgent(codex.New(
        agentadaptor.CodexConfig{Model: "gpt-5.4"},
        agentadaptor.WithDefaultStreaming(), // 该 binding 默认 streaming
    )),
)

handle, _ := sdk.Start(ctx, "hi")                              // 隐式 streaming
handle2, _ := sdk.Start(ctx, "batch", agentadaptor.WithoutStreaming()) // 显式关闭
```

覆盖顺序：

- per-call `WithStreaming` / `WithoutStreaming` 最高
- per-binding `WithDefaultStreaming` 次之
- 默认 off

### 5.3 AG-UI channel

```go
import "github.com/agent-dance/agent-adaptor/pkg/bridges/agui"

handle, _ := sdk.Start(ctx, prompt, agentadaptor.WithStreaming())
for ev := range agui.Wrap(handle) {
    json.NewEncoder(os.Stdout).Encode(ev)
}
```

`agui.Wrap` 内部读 `StreamEvents()`，翻译为 `github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events.Event`。

### 5.4 HTTP SSE chat endpoint

```go
import "github.com/agent-dance/agent-adaptor/pkg/bridges/sse"

mux.Handle("/v1/chat", sse.Handler(sdk, sse.Options{
    Protocol: sse.AGUI, // or sse.Raw (直出 StreamPayload)
}))
http.ListenAndServe(":8080", mux)
```

请求体 schema：

```json
{ "prompt": "...", "sessionKey": "...", "opts": { ... } }
```

响应：`text/event-stream`，每条帧由 AG-UI Go SDK 的 `sse.SSEWriter.WriteEvent` 写出。

## 6. 依赖策略

本次 workstream 引入以下**运行时**直接依赖：

- `github.com/ag-ui-protocol/ag-ui/sdks/community/go`（MIT，community SDK，2026-04-18 版）
  - 仅 `pkg/bridges/{agui,sse}` 使用
  - 带入间接依赖：`github.com/google/uuid`、`github.com/sirupsen/logrus`、`gopkg.in/yaml.v3`
- `github.com/sourcegraph/jsonrpc2`（MIT，v0.2.1）
  - 仅 `codex/appserver/` 使用
  - 提供 JSON-RPC 2.0 客户端、server push（对应 Codex 的 ServerRequest）、
    以及 **同步 Handler 派发模型**（每条 inbound 帧必须等 `Handler.Handle`
    返回才会读下一条）——这是保证 token 严格 FIFO 的关键
  - 已本机端到端验证（见 §17.2）
  - 无间接运行时依赖
  - 不需要 tolerant codec：Request/Response 的 UnmarshalJSON 都不强校验
    `"jsonrpc":"2.0"` 字段
  - 选型说明见 §16.3（为什么不用 creachadair/jrpc2）

**构建时**直接依赖（不进入运行时 `go.sum`）：

- `github.com/atombender/go-jsonschema`（MIT，v0.23+）
  - 通过 `go:generate` 从 `codex app-server generate-json-schema` 导出的 schema 生成 Go 结构体
  - 仅生成扁平 envelope；`oneOf`/discriminated union 由 `codex/appserver/union.go` 手写（见 §7 与 §16）

依赖 pin：三个第三方依赖都 pin 到明确版本（AG-UI SDK 使用 pseudo-version），升级由本 workstream 维护者统一 bump，并跑 §14 的端到端验证集。

## 7. 架构分层

```
agent-adaptor/                       # 主 module
├── api.go                           # + StreamAwareDriver / StreamCapability / EventSink.EmitStream
├── run_types.go                     # + StreamKind / StreamPayload / DriverRunRequest.Streaming
├── options.go                       # + WithStreaming / WithoutStreaming / WithDefaultStreaming
├── binding.go                       # + AgentDefaults.Streaming *bool
├── runner.go                        # channelEventSink → dualSink (RunEvent + StreamPayload)
├── sdk.go                           # + WithEventBuffer(runSize, streamSize, policy)
├── codex/
│   ├── driver.go                    # 保留 exec --json 入口；Streaming=true 分派到 appserver
│   └── appserver/                   # 新子包
│       ├── schema/                  #  codex app-server generate-json-schema 导出的 SoT
│       │   └── v2/*.json
│       ├── generate.go              #  //go:generate go-jsonschema ...（扁平 envelope 生成）
│       ├── generated.go             #  自动生成（do not edit）；覆盖扁平 notification/params/response
│       ├── union.go                 #  手写 discriminated union helper：ThreadItem / UserInput /
│                                    #  SandboxPolicy / CommandAction / WebSearchAction 的 decoder +
│                                    #  消费到的 ~6 个 ThreadItem 子类型（agentMessage / reasoning /
│                                    #  commandExecution / fileChange / mcpToolCall / webSearch）
│       ├── codec.go                 #  stdioStream：把子进程 stdin/stdout 包成 jsonrpc2.ObjectStream
│       ├── client.go                #  sourcegraph/jsonrpc2 客户端封装 + Codex-specific method wrappers
│       ├── run.go                   #  initialize → thread/start → turn/start → 消费到 turn/completed
│       └── translate.go             #  notification → StreamPayload
├── claude/                          # 不动，预留扩展点（见 §12）
├── cursor/                          # 不动，预留扩展点（见 §12）
└── pkg/
    └── bridges/
        ├── agui/                    # StreamPayload → agui.Event
        │   ├── translator.go
        │   ├── wrap.go
        │   └── wrap_test.go
        └── sse/                     # http.Handler，复用官方 sse.SSEWriter
            ├── handler.go
            ├── options.go
            └── handler_test.go
```

分层契约：

| 层 | 职责 | 禁区 |
|---|---|---|
| core SDK | 协议无关的 `StreamPayload` 定义、双通道管线、streaming 开关传递 | 不识别任何 driver 协议 |
| `codex/appserver` | 处理 codex app-server 的 JSON-RPC + 翻译到 StreamPayload | 不感知 AG-UI / SSE |
| `clihelper` | 不改 | 不参与 streaming |
| `pkg/bridges/agui` | StreamPayload → AG-UI event；纯映射 | 不认具体 driver 类型 |
| `pkg/bridges/sse` | AG-UI / Raw 两种 wire + HTTP handler | 不进入 core 主路径 |

这份分层是未来 Claude / Cursor 接入的核心保障：**`pkg/bridges/*` 对 adapter 类型盲，新增 driver 只需在自己包内发 `StreamPayload`**。

## 8. Codex app-server 集成详细

### 8.1 进程模型

**每 Run 一个 `codex app-server` 子进程**，run 结束即退出。

理由：

- 与现有 `DriverAdapter.Run` 的"一次 run"合同一致
- 失败隔离天然
- auth / `codex_home` / mcp 配置不共享，不必处理租户边界
- 实测 TTFB ≈ 5s（含 mcp init），聊天场景可接受

长驻进程池是未来增量：预留 `codex.WithAppServerPool(size)` 的 option 位置，但本期不实现。

### 8.2 会话驱动流程

```
codex app-server --listen stdio://
        │
        ▼
1.  → initialize { clientInfo, capabilities }
    ← initialize result
2.  → initialized  (notification)
3.  → thread/start { cwd, ephemeral, sandbox, model, ... }
    ← thread/start result (contains threadId)
      OR if resuming:
    → thread/resume { threadId }
4.  → turn/start { threadId, input, approvalPolicy: "never", sandboxPolicy }
    ← turn/start result
    ← turn/started             (notification)
    ← item/started             (agent_message | reasoning | commandExecution | ...)
    ← item/agentMessage/delta  ×N
    ← item/reasoning/textDelta ×N
    ← item/commandExecution/outputDelta ×N
    ← item/completed
    ← turn/completed
5.  关闭 stdin / 结束进程
```

### 8.3 Notification → StreamPayload 映射

| app-server notification | StreamKind | 备注 |
|---|---|---|
| `thread/started` | — | 仅记录 threadId，用于填充 StreamPayload.ThreadID |
| `turn/started` | `StreamRunStarted` | TurnID 填入 |
| `item/started` (agentMessage) | `StreamTextStart` | 用 itemId 当 MessageID |
| `item/agentMessage/delta` | `StreamTextContent` | Delta 透传 |
| `item/completed` (agentMessage) | `StreamTextEnd` | |
| `item/started` (reasoning) | `StreamReasoningStart` | |
| `item/reasoning/textDelta` | `StreamReasoningContent` | |
| `item/reasoning/summaryTextDelta` | `StreamReasoningContent` | 并入同 stream |
| `item/completed` (reasoning) | `StreamReasoningEnd` | |
| `item/started` (commandExecution / mcpToolCall / fileChange / webSearch) | `StreamToolCallStart` | `Name` 字段按 item.type 填 |
| `item/commandExecution/outputDelta` | `StreamToolCallArgs` | Delta 放命令输出 |
| `item/fileChange/outputDelta` | `StreamToolCallArgs` | |
| `item/completed` (tool items) | `StreamToolCallEnd` + `StreamToolCallResult` | Result 带 exitCode / result payload |
| `item/plan/delta` | — | 透传到 `Raw`，未来可以提升为独立 Kind |
| `thread/tokenUsage/updated` | — | 暂存到内存；`turn/completed` 时一并带上 |
| `turn/completed` (`status=completed`) | `StreamRunFinished` | Usage / cost 填入 |
| `turn/completed` (`status=failed/interrupted`) | `StreamRunError` | Error 填入；这是唯一正式终局通知 |
| `error` | — | 可重试且非终局，以 `StreamKind=""` 的 Raw notice 透传 |
| ServerRequest (approval / userInput) | `StreamHITLRequested` | audit-only，adapter 自动 deny |

未列出的 notification 降级为 `StreamKind=""` 的 Raw 透传，存进 `StreamPayload.Raw`。

### 8.4 Checkpoint 处理

仅在 `turn/completed.status=completed`、进程退出 0、协议完整且没有业务失败时，用 `threadId` 构造 `DriverCheckpoint`：

```go
&DriverCheckpoint{
    State: &DriverSessionState{
        ResumeID:  threadID,
        DisplayID: threadID,
        Data: map[string]string{
            SessionParamCWD:         effectiveCWD,
            SessionParamWorkspaceID: req.Workspace.ID,
        },
    },
    Valid: true,
}
```

resume 时检查 `State.Data[SessionParamCWD]` 与 `req.Workspace.CWD` 一致，不一致返回 `ResumeRejectedError`（与现有 `codex/exec` 路径一致）。

## 9. 背压与顺序语义

### 9.1 背压策略

```go
type EventBackpressure int

const (
    BackpressureDropStream EventBackpressure = iota // 默认：丢 StreamPayload + 发 StreamDropped 标记
    BackpressureBlock                                // 严格：sink 阻塞至 consumer 消费
)

func WithEventBuffer(runBufSize, streamBufSize int, policy EventBackpressure) SDKOption
```

默认值：

- `runBufSize = 64`（与当前一致）
- `streamBufSize = 1024`
- `policy = BackpressureDropStream`

`BackpressureDropStream` 触发时，`dualSink` 累积 drop 计数，下一次 `EmitStream` 成功进入 channel 前先插入一条：

```go
StreamPayload{
    Kind: StreamDropped,
    Raw: map[string]any{"dropped_count": N},
}
```

宿主文档必须明示：**消费 `StreamEvents()` 不能阻塞太久**；长时间 consumer 应自行开 buffer。

### 9.2 顺序保证

`dualSink` 内部用 `atomic.Uint64` 为 `StreamPayload.Sequence` 赋值，严格单调自增。

AG-UI 翻译层依赖该 Sequence 做事件顺序保障（AG-UI 协议无序号字段，但事件顺序必须严格）。

### 9.3 生命周期

- `Start()` 创建 `dualSink`，两 channel 同时创建
- run 结束（Wait 内部 goroutine 返回）时，`dualSink` 先 emit 最后一条 `StreamRunFinished` 或 `StreamRunError`，然后关闭两 channel
- `Cancel()` 触发 ctx cancel → `codex app-server` 子进程接收到 EOF/SIGTERM → 正常退出路径收敛

## 10. 实施阶段

### Phase 1：SDK 核心抽象（协议无关）

| # | 产出 | 行数 | 天 |
|---|---|---|---|
| T1 | `run_types.go` 新增 `StreamKind` / `StreamPayload` | ~150 | |
| T2 | `api.go` 新增 `StreamAwareDriver` / `StreamCapability`；扩 `EventSink.EmitStream` | ~80 | |
| T3 | `options.go` 新增 `WithStreaming` / `WithoutStreaming` / `WithDefaultStreaming`；`runOptions` / `AgentDefaults` tri-state 合并 | ~120 | |
| T4 | `runner.go` `channelEventSink` → `dualSink`；`Sequence` 单调；背压策略分派 | ~300 | |
| T5 | `sdk.go` `WithEventBuffer(runSize, streamSize, policy)` | ~60 | |
| T6 | `RunHandle.StreamEvents() / RunID()`；未开 streaming 返 closed channel | ~100 | |
| T7 | `DriverRunRequest.Streaming bool`；`runner.executeWithSessionPlan` 传递 | ~40 | |
| 小计 | | **~850** | **1** |

验收：

- 现有 `go test ./...` 全部通过，无修改
- 新增覆盖：`sdk_stream_internal_test.go` 含 tri-state 合并矩阵、Sequence 单调、drop marker、channel 生命周期

### Phase 2：Codex app-server 集成

| # | 产出 | 行数 | 天 |
|---|---|---|---|
| T8a | `codex/appserver/schema/` 拷贝 SoT；`generate.go` 配置 `//go:generate go-jsonschema -p appserver schema/v2/*.json -o generated.go` | ~20 | |
| T8b | `codex/appserver/union.go` — 手写 `ThreadItem` / `UserInput` / `SandboxPolicy` / `CommandAction` / `WebSearchAction` 的 discriminated union；6 个消费中的 ThreadItem 子类型 | ~250 | |
| T8c | `codex/appserver/codec.go` — `stdioStream`：把 subprocess 的 stdin/stdout 包成 `jsonrpc2.ObjectStream` | ~60 | |
| T9 | `codex/appserver/client.go` — `sourcegraph/jsonrpc2.NewConn` 封装 + Codex method wrappers（`Initialize` / `ThreadStart` / `ThreadResume` / `TurnStart` / `TurnInterrupt`）+ 同步 `Handler` 派发 | ~220 | |
| T10 | `codex/appserver/translate.go` — notification → StreamPayload（§8.3 的完整映射表） | ~300 | |
| T11 | `codex/appserver/run.go` — 完整会话驱动；HITL ServerRequest 自动 deny + 发 audit 事件 | ~200 | |
| T12 | `codex/driver.go` — `req.Streaming` 分派 exec / appserver；实现 `StreamAwareDriver` | ~100 | |
| T13 | `codex/binding` 关联 — `AgentDefaults.Streaming` 透传 | ~40 | |
| 小计 | | **~1200** | **~1.4** |

验收：

- `go test ./codex/... -short` 保持绿
- `go generate ./codex/appserver/...` 可重复执行且产出无 diff
- 新增 `codex/appserver/run_live_test.go` 在 `go test -tags=codex_live -run TestAppServerHaiku` 下跑真实 `codex` 二进制，断言：
  - 至少 3 条 `StreamTextContent`
  - 有 `StreamRunStarted` / `StreamRunFinished`
  - `StreamRunFinished.Usage.InputTokens > 0`
  - **delta 顺序重构最终文本**：按接收顺序拼接的 `StreamTextContent.Delta` 必须是 `res.Output` 的前缀 / 后缀（两向 `strings.Contains`），作为 ordering 回归锚点（历史 bug 见 §17.2）

### Phase 3：AG-UI 桥接（`pkg/bridges/agui`）

| # | 产出 | 行数 | 天 |
|---|---|---|---|
| T14 | `pkg/bridges/agui/translator.go` — `StreamPayload → events.Event` 状态机 | ~250 | |
| T15 | `pkg/bridges/agui/wrap.go` — `Wrap(handle) <-chan events.Event`；run 结束补发 `RUN_FINISHED`；错误补发 `RUN_ERROR` | ~100 | |
| 小计 | | **~350** | **0.5** |

验收：

- `go test ./pkg/bridges/agui/...` 绿
- 表驱动测试：Phase 2 真实 StreamPayload fixture → AG-UI event 序列；手工断言合法 message 生命周期

### Phase 4：SSE Handler（`pkg/bridges/sse`）

| # | 产出 | 行数 | 天 |
|---|---|---|---|
| T16 | `pkg/bridges/sse/handler.go` — `Handler(sdk, Options) http.Handler` | ~150 | |
| T17 | `pkg/bridges/sse/options.go` — Options / KeepAlive / CORS | ~80 | |
| 小计 | | **~230** | **0.5** |

验收：

- `go test ./pkg/bridges/sse/...` 绿
- `httptest` 集成测试：`POST /chat`，`text/event-stream` 响应含 `RUN_STARTED` + `TEXT_MESSAGE_CONTENT` + `RUN_FINISHED`

### Phase 5：扩展点文档（不写代码）

| # | 产出 | 天 |
|---|---|---|
| T18 | `docs/streaming-adapter-contract.md` — adapter 实施 streaming 的契约指南（参数、事件映射、容错） | |
| T19 | `claude/README-streaming.md` — Claude `--include-partial-messages` → StreamPayload 映射表 | |
| T20 | `cursor/README-streaming.md` — Cursor `--stream-partial-output` → StreamPayload 映射表 | |
| 小计 | | **0.5** |

### Phase 6：示例与文档

| # | 产出 | 行数 | 天 |
|---|---|---|---|
| T21 | `examples/streaming_chat/main.go` — Go channel 消费 demo | ~80 | |
| T22 | `examples/streaming_sse_server/main.go` — SSE chat server + 极简 HTML 页面（宿主使用样板） | ~180 | |
| T23 | `docs/streaming.md` — 宿主使用指南（三场景） | | |
| T24 | `AGENTS.md` 增量：声明 streaming 是第二通道、bridges/* 的边界 | | |
| 小计 | | **~260** | **0.5** |

### 总计

| Phase | 总手写行数 | 生成代码 | 工作日 |
|---|---|---|---|
| 1. SDK 骨架 | 850 | — | 1.0 |
| 2. Codex app-server | 1200 | ~500（go-jsonschema 生成的扁平类型，不计入手写） | 1.4 |
| 3. AG-UI 桥 | 350 | — | 0.5 |
| 4. SSE handler | 230 | — | 0.5 |
| 5. 扩展点文档 | 0 | — | 0.5 |
| 6. 示例 + 文档 | 260 | — | 0.5 |
| **合计** | **~2890** | **~500** | **~4.4** |

> 注：与原计划（全手写 3530 行 / 4 工作日）相比，采用 sourcegraph/jsonrpc2 + go-jsonschema 净省 ~760 行手写代码（实测后切换 jsonrpc2 库又省掉 ~120 行 `tolerantChannel`）。工期大头仍在 union 手写与 codex 协议对接，实际 ~4 天；这是基于 §17 实际验证后的如实估算。

## 11. 风险与应对

| 风险 | 触发条件 | 应对 |
|---|---|---|
| `codex app-server` 协议字段演进 | OpenAI 升级 `codex-cli` | 只 model 化消费的 method / notification；未覆盖字段降级到 `Raw`；每次 bump codex 版本跑 `codex_live` 测试 + `go generate` |
| `codex app-server` 未来补上 `"jsonrpc":"2.0"` 字段 | codex-cli 修协议 bug | `sourcegraph/jsonrpc2` 不校验该字段；无论 codex 带/不带都直接透传，零干预 |
| JSON-RPC 客户端 notification 乱序 | 客户端用 goroutine-per-message 派发 + 非 FIFO mutex 抢锁（`creachadair/jrpc2` v1.3.5 实测） | 已切换到 `sourcegraph/jsonrpc2`：`Handler` 同步派发，读-派发-读 严格串行；`run_live_test.go` 加了 "delta 顺序重构最终文本" 的硬 assert |
| app-server 启动 ~5s TTFB | 聊天首字符慢 | 本期接受；`WithAppServerPool` option 位已预留 |
| 高并发 = 多进程 | 超过数十并发 | 宿主侧做 worker pool；v2 提供原生 pool |
| AG-UI Go SDK 仍是 pseudo-version | 升级可能引入破坏 | pin 到明确 pseudo-version；升级走 PR review |
| `logrus` 引入，和项目其它地方 `log/slog` 风格不一 | 依赖图观感 | 隔离在 bridges 包；核心不用 |
| `go-jsonschema` 对 `oneOf` 退化为 `interface{}` | codex schema 新增 discriminated union | 在 `union.go` 里手写 decoder；`union_test.go` 对所有已 model 的 subtype 做 round-trip 校验 |
| `go-jsonschema` 输出命名约定（如 `*Json` 后缀）与仓库风格不一致 | 观感 | 在 `generate.go` 的 build tag / 参数层面统一配置；必要时通过 renaming 后缀修正 |
| Claude partial 与 thinking 互斥 | 未来接 Claude streaming | adapter 侧在 `StreamCapability.Reasoning` 按配置降级，并在 `ValidateConfig` 报明显错误 |
| Cursor tool_call args 非流式 | 未来接 Cursor streaming | `StreamToolCallArgs` 允许一次发完；`StreamCapability.ToolCallArgs=false` 标明 |
| 宿主忘记 drain `StreamEvents()` | slow consumer | 默认 `BackpressureDropStream` + `StreamDropped` marker，不会卡 codex 子进程；文档在最显眼处提醒 drain |
| 事件顺序在高吞吐下错乱 | 并发 emit | `Sequence` 由 `atomic.Uint64` 单调赋值；翻译层据此校验 |

## 12. Claude / Cursor 跟进的零改动契约

保证未来接入 Claude / Cursor 时：

- core SDK 不动
- `pkg/bridges/agui` 不动
- `pkg/bridges/sse` 不动
- 只在各自 adapter 包内新增实现

### 12.1 Claude

**已完成**，实施说明与映射表见 [`workstream-streaming-claude.md`](./workstream-streaming-claude.md)、[`claude/README-streaming.md`](../../claude/README-streaming.md)。

- `claude/driver.go` 实现 `StreamAwareDriver`，`StreamCapability{Native:true, TokenLevel:true, ToolCallArgs:true, Reasoning:true}`
- `req.Streaming=true` 时追加 `--include-partial-messages`
- `claude/streaming_parser.go`：解析 `stream_event` → `StreamPayload`；`assistant` 全量帧仍走批量路径，避免双发

### 12.2 Cursor

- `cursor/driver.go` 实现 `StreamAwareDriver`，返回 `StreamCapability{Native:true, TokenLevel:true, ToolCallArgs:false, Reasoning:false}`
- 当 `req.Streaming=true`：追加 CLI flag `--stream-partial-output`
- 新增 `cursor/partial_parser.go`：
  - `system.init` → 记录 runID / threadId
  - `assistant`（partial 模式多条） → `StreamTextStart` / `StreamTextContent` / `StreamTextEnd`
  - `tool_call{started}` → `StreamToolCallStart`（args 一次性带入 `Args`）
  - `tool_call{completed}` → `StreamToolCallEnd` + `StreamToolCallResult`
  - `result` → `StreamRunFinished`
- `thinking` 事件在 print 模式被 Cursor 主动抑制，不产生 `StreamReasoning*`

### 12.3 能力降级协议

bridges 层不应该硬编码 "必须有 delta"：

- `agui.Wrap` 看到 `StreamToolCallStart` 时，如果后续没有 `StreamToolCallArgs`，只发 `TOOL_CALL_START` 带完整 `args`，不发 `TOOL_CALL_ARGS`
- AG-UI 协议本身允许这种退化

## 13. 对 AGENTS.md 的边界声明

本次 workstream 在 `AGENTS.md` 追加一段硬约束：

> Streaming 是 core SDK 的**第二条可选通道**，不是第二套执行入口。
>
> - `Run/Start` 语义不变
> - 未开启 `WithStreaming` 时 SDK 行为完全与现在一致
> - `pkg/bridges/*` 是主 module 的子包，但职责仅限于把 `StreamPayload` 翻译到宿主需要的传输协议
> - bridges 不得读取 `RunEvent`、不得调用 adapter 内部、不得重新进入 Run 路径
> - adapter 协议识别仍归 adapter 自己；`clihelper` 不感知 streaming

## 14. 验收清单

本 workstream 合并需同时满足：

- [ ] 现有 `go test ./...` 在 `WithStreaming` 未开启的默认路径下 100% 绿
- [ ] `go generate ./codex/appserver/...` 可重复执行；产出经 `git diff` 无变化
- [ ] `codex/appserver/union_test.go`：所有手写 ThreadItem 子类型对 §17 采集到的 fixture 做 round-trip 校验
- [ ] `codex_live` 集成测试：本机跑 `codex app-server`，Haiku prompt 看到 ≥ 3 条 `StreamTextContent`、合法 `StreamRunFinished.Usage.InputTokens > 0`
- [ ] `pkg/bridges/agui` fixture 测试：合法 AG-UI message / tool_call 生命周期
- [ ] `pkg/bridges/sse` httptest：SSE 帧格式符合 AG-UI Go SDK `SSEWriter` 输出
- [ ] `examples/streaming_chat` 与 `examples/streaming_sse_server` 本机单命令跑通
- [ ] `docs/streaming.md` 涵盖 5.1 / 5.3 / 5.4 三场景可复制代码
- [ ] `AGENTS.md` 已追加 §13 边界声明

## 15. 开放议题

以下不在本期交付，但在合并前需明确下期跟进计划：

- HITL 回路：本期仅 audit；下期需设计 `HITLRequestHandler` + 超时策略 + per-turn approval policy override
- AG-UI `STATE_SNAPSHOT / STATE_DELTA` 的宿主钩子（例如 `WithStateMaintainer`）
- Codex `codex app-server` 从 `[experimental]` 转正后的 session 持久化联动
- Claude / Cursor 的 streaming 落地排期

## 16. 协议兼容性与升级策略

本期引入的三个 codex 相关组件都需要"随 codex-cli 升级"的维护动作。本节把责任边界和动作 checklist 写死。

### 16.1 `codex app-server` 升级流程

当需要升级 `codex-cli` 时，维护者按以下步骤推进：

1. 安装新版 `codex-cli`
2. 跑 `codex app-server generate-json-schema --out codex/appserver/schema/` 覆盖 SoT
3. 跑 `go generate ./codex/appserver/...` 重新产出 `generated.go`
4. 跑 `go build ./...` 修复编译错误（字段改名 / 删除 / 新增）
5. 跑 `go test -tags=codex_live ./codex/appserver/...` 做端到端验证
6. 若失败，优先修 `union.go` / `translate.go`，最后才考虑改 `StreamPayload` 契约
7. 升级 PR 必须带上新老版本的 `codex --version`，以及端到端测试输出 diff

### 16.2 `"jsonrpc":"2.0"` marker 宽容策略

历史上（2026-04-21 之前）我们通过 `tolerantChannel` 往缺失 marker 的
inbound 帧里手工注入 `"jsonrpc":"2.0"`，因为 `creachadair/jrpc2` 严格
校验该字段。

切换到 `sourcegraph/jsonrpc2` 后这一层**不再需要**：

- `jsonrpc2.Request.UnmarshalJSON` 只从解析结果里 `delete "jsonrpc"`
  字段（作为未知扩展），不校验值
- `jsonrpc2.Response.UnmarshalJSON` 直接走 `json.Unmarshal` 到内部
  tmpType，Go 的 JSON 默认忽略未知字段
- 因此 codex app-server 不论是否补上 marker，客户端都无感

由此带来的精简：

- 删除 `tolerantChannel`、`TolerantStats`、`injectJSONRPCMarker`
- `codex/appserver/codec.go` 只剩下 `stdioStream`：一个把
  `io.WriteCloser`（stdin）+ `io.Reader`（stdout）包成
  `jsonrpc2.ObjectStream` 的 ~60 行适配器
- 维护者无需关心 codex 后续是否补 marker

### 16.3 `go-jsonschema` 的 discriminated union 边界

`go-jsonschema` 对 `oneOf` 会退化为 `type X interface{}`。本项目的规则：

- 生成代码中任何 `interface{}` 类型都**不直接使用**
- 所有需要类型化的 union 在 `union.go` 手写：
  - 提供 `DecodeX(raw json.RawMessage) (X, error)` 函数
  - `X` 是一个 `type X interface { isX() }` 之类的 marker interface
  - 每个子类型实现 `isX()`，并有明确字段
- 当 codex 新增 union 子类型而我们不消费它时：
  - `DecodeX` 对未知 `type` 字段返回 `UnknownX{Raw: raw}`，不 panic
  - `translate.go` 对 `UnknownX` 发 `StreamPayload{Raw: map[...]}` 透传

### 16.4 上游 schema 与本仓 SoT 的同步原则

- `codex/appserver/schema/` 是**本仓 SoT**，而不是 `go generate` 时去远程拉
- 升级人负责同步；CI 检测 schema 目录 diff 与 `generated.go` diff 是否一致
- `AGENTS.md` §9 "对未来修改者的硬要求" 增补一条：**schema 目录不允许手工编辑**

## 17. 附录：本工作流立项前的事实验证

本节记录立项前跑过的真实验证，作为实施时的 baseline 和回归锚点。

### 17.1 codex 本机事实（2026-04-21）

- `codex-cli 0.120.0`
- `codex app-server` 子命令存在，标记 `[experimental]`
- `codex app-server --listen stdio://` 默认启动 stdio 模式
- `codex app-server generate-json-schema --out DIR` 可用，导出 162 个 v2 schema + 2 个 v1 schema

### 17.2 JSON-RPC 客户端选型与端到端验证

**用途**：给 codex app-server 选一个 Go JSON-RPC 2.0 over stdio 的客户端基础设施。

**初选：`github.com/creachadair/jrpc2` v1.3.5**

1. 严格校验 inbound 帧 `"jsonrpc":"2.0"` 字段；codex 不带 → 需要 40 行 `tolerantChannel` 适配
2. 初次 haiku 测试通过（15 条 delta）

**退选原因：notification 乱序**

后续长 prompt 压测发现 `item/agentMessage/delta` 被严重乱序投递。
对 `jrpc2` 客户端源码定位：

- `client.accept()` 每读一条 inbound 消息就 `go` 一个新 goroutine 去
  派发 notification（`jrpc2/client.go:accept`）
- 这些 goroutine 竞争同一把 `sync.Mutex` 抢分发；Go 的 sync.Mutex
  在 **非饥饿模式下不保证 FIFO 唤醒顺序**，所以 notification 到达
  回调时相对于 wire 顺序已经乱了
- 对 codex 这种 token-level 推流场景（同一 turn 内会有几十到几百条
  delta），表现就是诗句字符被拆乱
- 这不是配置可关的行为，是 `jrpc2` 客户端的架构决定

**换用：`github.com/sourcegraph/jsonrpc2` v0.2.1**

1. `Handler.Handle` 被 **同步** 调用——`Conn` 在读下一条 inbound 前
   必须等 handler 返回，天然 FIFO
2. 不强校验 `"jsonrpc":"2.0"` marker，`tolerantChannel` 直接删除
3. `ObjectStream` 是一个简单的 3 方法接口（`ReadObject` / `WriteObject`
   / `Close`），自写一个 stdin/stdout 适配器只需 60 行
4. 同步 handler 会回压 codex 子进程——代价可接受，因为我们的
   translator + dualSink 做的是纯内存状态机和 channel send，单次
   派发 &lt; 几 µs

**实测指标（2026-04-21）**：

```
deltas:          17
text:            "Words bloom line by line  \nA river of waking light  \nSilence learns to speak"
assembled order: 严格等于最终输出（live test 直接校验）
usage:           input=13192 output=33 cached=9728
wall:            6.72s（包含模型首 token 延迟）
```

**结论**：改用 `sourcegraph/jsonrpc2` 后，所有顺序敏感的 bug 消失，
同时代码总量反而减少（删除 tolerantChannel + 精简 client wrapper
≈ -120 行）。`codex/appserver/run_live_test.go` 已补上 "delta 顺序
必须重构最终文本" 的硬 assert，作为 ordering 回归锚点。

### 17.3 go-jsonschema 对 codex schema 的生成验证

**用途**：确认 `github.com/atombender/go-jsonschema` 能否从 codex schema 生成可用的 Go 类型。

**发现**：

1. 扁平 schema 如 `AgentMessageDeltaNotification` 生成完美：Go struct + `UnmarshalJSON` 带 required 字段校验
2. 复杂 `oneOf` schema 如 `ItemStartedNotification.item`（15 种 `ThreadItem` 子类型）退化为 `type ThreadItem interface{}`
3. 同样退化的还有 `UserInput`、`CommandAction`、`SandboxPolicy`、`WebSearchAction`、`DynamicToolCallOutputContentItem`、`PatchChangeKind`、`MessagePhase`
4. 其他 enum、嵌套 object、required 字段校验均正确

**结论**：采用混合方案——

- `generated.go`：所有扁平 envelope / params / response / notification 的顶层结构
- `union.go`：手写 6 个 `ThreadItem` 子类型（`agentMessage` / `reasoning` / `commandExecution` / `fileChange` / `mcpToolCall` / `webSearch`）+ `UserInput` 3 个子类型（`text` / `image` / `localImage`）+ `SandboxPolicy` 4 个子类型
- 未来新增 union 子类型时，若非必要不立刻手写；先按 `UnknownX{Raw}` 透传到 `StreamPayload.Raw`

### 17.4 实测采集的 notification fixture（用于回归）

用于 `codex/appserver/union_test.go` 和 `translate_test.go` 的 round-trip 测试。完整 sample 在 git 中以 `codex/appserver/testdata/` 形式提交，这里只记录采集清单：

- `thread/started` — 344 bytes，含 `thread` 对象
- `turn/started` — 210 bytes，含 `turn` 对象
- `item/started(agentMessage)` — 247 bytes
- `item/agentMessage/delta` × 15
- `item/completed(agentMessage)` — 330 bytes
- `thread/tokenUsage/updated` — 381 bytes
- `turn/completed` — 含 usage 对象
- `thread/status/changed` × 2
- `mcpServer/startupStatus/updated` × 2
- `account/rateLimits/updated` × 2

以上 fixture 覆盖了 §8.3 映射表里标为 ✅ 的所有 notification。新增消费的 notification 时，应在 live probe 重新抓取。

## 18. AG-UI 输入覆盖边界与 TODO

`pkg/bridges/agui.DecodeHTTPRequest` + `RunAgentInput` 解码器当前做的是
**"AG-UI → 单轮 text prompt"** 的降级翻译，仅投递以下信息给 adapter：

- 最新 `role=user` message 的文本 content
- `ThreadID` → `("agui", threadId)` session key 映射

其它 AG-UI 字段在类型上保留（不报错、不破坏 JSON round-trip），但
**bridge 层不会把它们投递到 `DriverRunRequest`**。清单：

### 18.1 被静默吃掉的输入

| 字段 / 能力 | 现状 | 业务影响 | 优先级 |
|---|---|---|---|
| `messages[].content` 中 `image` / `audio` / `file` / `document` part | `contentAsString` 内 `continue` 跳过，无日志 | 多模态消息的文字部分通过，图/音/文件静默丢失 | P1 |
| `tools[]`（frontend tools 声明） | 未读 | CopilotKit "frontend actions" 能力失效，agent 不知道前端能做什么 | P1 |
| assistant 的 `tool_calls` + role=tool 的 result | `LastUserText` 只收 role=user，其它跳过 | 前端工具调用往返链路打断 | P1 |
| `state`（CopilotKit shared state，双向 sync） | 未读 | 前端状态同步失效 | P2 |
| `context[]`（system-level 上下文：当前路由 / locale / tz） | 未读 | system context 缺失，agent 无法感知前端环境 | P2 |
| `forwardedProps`（任意透传 props） | 未读 | 自定义透传 channel 不可用 | P3 |
| `Message.Name`（tool name） | 解码但未使用 | — | P3 |
| 非最新 user message 的历史 | 忽略 | **故意**：agent-adaptor 的 SessionStore 已持有 transcript，避免双写 | — |

### 18.2 补齐计划

按优先级分 3 步推进（此处只占位，不在本 workstream 落地）：

1. **P1-防静默（下一迭代）**
   - 非 text content part 产生 WARN 日志（`log/slog`，每 run 聚合一次，不要每 part 一条）
   - `pkg/bridges/agui/input.go` godoc 明确声明 v1 text-only 边界
   - `RunAgentInput.LastUserText` 返回值是**空串但存在非 text part**时，返回明确的 sentinel error 而非 "no user message"

2. **P1-透传（下个 workstream）**
   - 扩 `DriverRunRequest` 新增 `Extras map[string]any` 或专门的 `AGUIInput *RunAgentInput`
   - bridge 把整个 decoded input 塞进去，adapter 选择性消费
   - Codex adapter v1 仍 ignore，但字段不再是 bridge 层吃

3. **P2-协议双向（未来 workstream）**
   - AG-UI frontend tools：需要 `TOOL_CALL_*` event + tool result 回注机制；涉及 `StreamPayload` tool-call 往返语义、`RunHandle.PushToolResult()` 入口；设计时注意 AGENTS.md §2.1 "只有一套执行语义"
   - 多模态 input 下行：`DriverRunRequest.Prompt string` → `Prompt Prompt`，adapter 决定如何映射到本地协议（Codex app-server 已支持 `UserInput.image` / `localImage`）
   - `state` 双向 sync：需要在事件流里补 `STATE_DELTA` 语义

### 18.3 守门规则

在补齐之前，**不允许**在 `agui/input.go` 外暗地读 `RunAgentInput` 的
`Tools` / `State` / `Context` / `ForwardedProps` 字段——避免形成"有的
路径读、有的路径不读"的碎片状态。所有扩展必须走 §18.2 的统一入口。
