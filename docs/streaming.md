# Streaming Chat Guide for Hosts

本指南告诉宿主如何用最少代码拿到 token-level 流式输出。三种场景（Go channel / AG-UI / HTTP SSE）按复杂度递增排列；绝大多数宿主场景 §3 就够了。

底层设计与 roadmap：[workstream-streaming-chat.md](./workstream-streaming-chat.md)。

## 0. 何时开启 streaming

agent-adaptor 的 `Run / Start` 默认是批处理语义（行级事件 + 最终 `RunResult`）。当宿主需要：

- 聊天 UI 的打字机效果
- 工具调用 / reasoning 的实时可视化
- 审计 / 回放中的 token 粒度

就加上 `agentadaptor.WithStreaming()`。

**默认不开启**。这条路径需要更丰富的 CLI 通路（codex 切到 `codex app-server`，claude/cursor 也要追加 flag），对冷启动 TTFB 和进程资源更敏感。批处理场景保持原路径。

## 1. 场景 A：最薄 — Go channel 消费

适用于：原生 Go 应用、集成在更大 agent 框架中。

```go
import (
    "context"
    "fmt"

    agentadaptor "github.com/agent-dance/agent-adaptor"
    "github.com/agent-dance/agent-adaptor/codex"
    "github.com/agent-dance/agent-adaptor/memory"
)

func main() {
    sdk := agentadaptor.New(
        agentadaptor.WithDefaultAgent(codex.New(agentadaptor.CodexConfig{
            Model: "gpt-5.4",
        })),
        agentadaptor.WithSessionStore(memory.NewSessionStore()),
    )

    handle, err := sdk.Start(context.Background(), "Write a haiku",
        agentadaptor.WithStreaming(),
        agentadaptor.WithSessionKey("my-namespace", "thread-1"),
    )
    if err != nil { panic(err) }
    defer handle.Cancel(context.Background())

    for ev := range handle.StreamEvents() {
        switch ev.Kind {
        case agentadaptor.StreamTextContent:
            fmt.Print(ev.Delta)
        case agentadaptor.StreamRunFinished:
            fmt.Println("\n[done]", ev.Usage)
        }
    }

    result, _ := handle.Wait(context.Background())
    _ = result
}
```

关键点：

- `RunHandle.StreamEvents()` 在未开 streaming 时返回已关闭 channel，`for range` 立即退出
- `RunHandle.RunID()` 在 `Start()` 返回后立即可用，不需要等 `Wait()`
- `RunHandle.Events()` 保持不变，仍然承担 spawn / stderr / lifecycle 元事件
- Run 结束时两个 channel 都 close

完整示例：[`examples/streaming-chat/main.go`](../examples/streaming-chat/main.go)

## 2. 场景 B：AG-UI channel — 标准化事件

适用于：已有 AG-UI 客户端（CopilotKit、AG-UI Terminal 等）、需要跨语言互操作。

```go
import (
    agentadaptor "github.com/agent-dance/agent-adaptor"
    "github.com/agent-dance/agent-adaptor/pkg/bridges/agui"
)

handle, _ := sdk.Start(ctx, prompt, agentadaptor.WithStreaming())
defer handle.Cancel(ctx)

for ev := range agui.Wrap(handle) {
    // ev 是 github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events.Event
    // 可以直接 JSON 化或转到其它传输层
}
```

`agui.Wrap` 的行为：

- 状态机保证 `RUN_STARTED / TEXT_MESSAGE_* / TOOL_CALL_* / RUN_FINISHED` 的生命周期合法
- 漏发的 `TEXT_MESSAGE_START` 会自动补发
- 未关闭的 message / tool 在 `RUN_FINISHED` / `RUN_ERROR` 前被补 END
- 未知 StreamKind 透传为 AG-UI `CUSTOM` 事件

## 3. 场景 C：HTTP SSE chat 接口 — 最低成本上线

适用于：已有 Web 前端、需要暴露 RESTful chat API。

```go
import (
    "net/http"

    agentadaptor "github.com/agent-dance/agent-adaptor"
    "github.com/agent-dance/agent-adaptor/pkg/bridges/sse"
)

mux := http.NewServeMux()
mux.Handle("/v1/chat", sse.Handler(sdk, sse.Options{
    Protocol:          sse.AGUI, // 或 sse.Raw
    CORSAllowedOrigin: "*",
}))
http.ListenAndServe(":8080", mux)
```

请求格式：

```json
POST /v1/chat
{"prompt": "Write a haiku", "sessionKey": "ns/thread-id"}
```

响应：`text/event-stream`，每帧是一个 AG-UI 事件：

```
event: RUN_STARTED
data: {"type":"RUN_STARTED","threadId":"...","runId":"..."}

event: TEXT_MESSAGE_CONTENT
data: {"type":"TEXT_MESSAGE_CONTENT","messageId":"...","delta":"Hello"}

...

event: RUN_FINISHED
data: {"type":"RUN_FINISHED",...}
```

完整示例（含极简 HTML 前端）：[`examples/streaming-sse-server/main.go`](../examples/streaming-sse-server/main.go)

### Options

- `Protocol`: `sse.AGUI`（默认，推荐）或 `sse.Raw`（直出 `StreamPayload` JSON）
- `KeepAlivePing`: 非零时发 SSE 注释帧防代理断连
- `CORSAllowedOrigin`: 浏览器前端必须；空字符串不加 CORS 头
- `WriteTimeout`: 单帧写超时，默认 30s
- `RunOptions`: 应用到每次请求的 RunOption 列表（例如 `WithRunPolicy`）
- `DecodeRequest`: 自定义请求体解码

## 4. 配置

### 4.1 per-binding 默认开

```go
sdk := agentadaptor.New(
    agentadaptor.WithDefaultAgent(codex.New(
        agentadaptor.CodexConfig{Model: "gpt-5.4"},
        agentadaptor.WithDefaultStreaming(), // 该 binding 默认 streaming
    )),
)

// 隐式 streaming
handle, _ := sdk.Start(ctx, "hi")

// 显式关闭本次
handle2, _ := sdk.Start(ctx, "batch",
    agentadaptor.WithoutStreaming())
```

覆盖顺序：per-call `WithStreaming` / `WithoutStreaming` > per-binding `WithDefaultStreaming` > 默认 off。

### 4.2 背压

```go
sdk := agentadaptor.New(
    // ...
    agentadaptor.WithEventBuffer(64, 1024, agentadaptor.BackpressureDropStream),
)
```

- `BackpressureDropStream`（默认）：stream channel 满时丢 `StreamPayload` 并发 `StreamDropped` marker（`Raw["dropped_count"]` 报告丢失计数）。**adapter 子进程永不阻塞**。
- `BackpressureBlock`：严格无丢失；slow consumer 会让 adapter goroutine 阻塞，适合 AG-UI 一致性要求高的下游。

**宿主务必及时 drain `StreamEvents()`**，否则 Block 模式可能挂起，Drop 模式会丢事件。

## 5. 能力与降级

每个 streaming-aware adapter 通过 `StreamAwareDriver.StreamCapability()` 声明自己能提供什么：

| adapter | Native | TokenLevel | Reasoning | ToolCallArgs | HITL |
|---|---|---|---|---|---|
| codex | ✓ | ✓ | ✓ | ✓ | — |
| claude | ✓ | ✓ | ✓ | ✓ | ✓（PlanReview / Question） |
| cursor | — | — | — | — | — |

`pkg/bridges/agui` 会按 capability 做合理降级：

- `ToolCallArgs=false` → `StreamToolCallStart` 带完整 `Args`，不发 `StreamToolCallArgs`
- `Reasoning=false` → 不发 `REASONING_*`
- HITL 事件默认映射成 AG-UI tool-call lifecycle，便于 CopilotKit 这类客户端渲染审批卡片；需要旧行为时使用 `agui.WithDecisionMode(agui.DecisionAsCustom)`

## 6. 常见问题

**Q: 开了 `WithStreaming` 但 `StreamEvents()` 一直没有 payload？**
A: 检查 adapter 是否实现了 `StreamAwareDriver`。未实现的 adapter 会走普通路径，`StreamEvents()` 返回 closed channel。

**Q: `Wait()` 阻塞但 `StreamEvents()` 已经 range 完成？**
A: 这是预期的：stream channel 可以比 adapter.Run 更早结束；`Wait()` 等 run goroutine 完成并返回归一化 `RunResult`。

**Q: SSE 帧里看到 `CUSTOM` 事件是什么意思？**
A: 可能是 codex 的 `thread/tokenUsage/updated` 或未识别的 provider 事件。AG-UI 协议允许 CUSTOM 做透传，`data` 字段里有原始 payload。

**Q: 多个并发 chat 安全吗？**
A: 安全。每个 `Start()` 创建独立 handle / sink / codex app-server 子进程。但目前没有进程池，并发 100 个 chat = 100 个 codex 子进程。高并发场景参考 [workstream-streaming-chat.md](./workstream-streaming-chat.md) §11 的 risk 表。

**Q: SSE 断连后 run 还在跑吗？**
A: 不在。SSE handler 在 client 断连时会调 `handle.Cancel()`，ctx cancellation 穿透到 codex 子进程。

## 7. AG-UI 前后端版本对齐

Go 侧通过 `go.mod` 固定 `github.com/ag-ui-protocol/ag-ui/sdks/community/go`；`examples/streaming-chat-copilotkit/web` 通过 `package-lock.json` 固定 `@ag-ui/core`。两边不是同一个坐标系，升级任一侧时都可能出现 Zod/Go `Validate` 行为漂移。

**回归守门**：`go test ./internal/aguiversion/...` 会检查 `go.mod` 的模块 pin 子串，以及 `package-lock.json` 中 `@ag-ui/core` 的版本号常量。有意的版本升级时，需同步更新 `internal/aguiversion/align_test.go` 里的 `expectedAGUICoreNPM` 与 `expectedGoAGUIModuleSubstr`（并复查 `pkg/bridges/agui` 的 fixture 与 `literals.go`）。
