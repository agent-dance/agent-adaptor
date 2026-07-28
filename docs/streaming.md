# Streaming 指南

本指南面向需要实时渲染文本、thinking、工具调用、运行状态或审批请求的宿主。最终 API 只有一条 typed `Event` 流：`Agent` 与 `Thread` 都通过 `Runner.Stream` 返回 `Stream`，运行结束后再从 `Stream.Result()` 取得权威结果。

完整公共类型见 [API reference](./api-reference.md)，审批策略见 [run policy](./run-policy.md)。

## 1. 一条执行管线，一条事件流

```go
type Runner interface {
	Run(ctx context.Context, prompt string, opts ...CallOption) (*Result, error)
	Stream(ctx context.Context, prompt string, opts ...CallOption) Stream
}

type Stream interface {
	Events() <-chan Event
	Result() (*Result, error)
	RunID() string
	Cancel()
}
```

`Run` 严格等价于 `Stream`、持续 drain `Events()`、再调用 `Result()`。两者共享同一份选项合并、资源解析、Driver 调用、Thread 协调和结果归档逻辑。

调用 `Stream` 只表示宿主要观察实时事件，不等于强制某种 provider 协议。core 会根据已解析的调用、Driver 的 `StreamCapability` 和结构化输出兼容性选择 provider 传输；宿主没有额外的 streaming 开关。

## 2. 最小消费示例

```go
package main

import (
	"context"
	"errors"
	"fmt"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/codex"
)

func main() {
	ai := adaptor.New(codex.Driver(codex.Config{Model: "gpt-5.4"}))
	stream := ai.Stream(context.Background(), "Write a haiku")
	defer stream.Cancel()

	for ev := range stream.Events() {
		switch e := ev.(type) {
		case adaptor.TextDelta:
			if e.Phase == adaptor.PhaseContent {
				fmt.Print(e.Text)
			}
		case adaptor.Thinking:
			// 按需渲染 reasoning；不需要时忽略即可。
		case adaptor.ToolCall:
			if e.Phase == adaptor.PhaseStart {
				fmt.Printf("\n[tool %s]\n", e.Name)
			}
		case adaptor.Dropped:
			fmt.Printf("\n[dropped %d events]\n", e.Count)
		}
	}

	result, err := stream.Result()
	if err != nil {
		var runErr *adaptor.RunError
		if errors.As(err, &runErr) {
			fmt.Printf("run failed: %s: %s\n", runErr.Reason, runErr.Message)
			return
		}
		panic(err)
	}
	fmt.Printf("\nresult: %s\n", result.Text)
}
```

关键语义：

- `Stream` 立即返回，`RunID()` 随即可用。
- 启动前错误也通过同一个形状返回：`Events()` 关闭，`Result()` 返回错误。
- 未取消的正常运行会在所有已接受的终局事件交付且 provider、runtime service 与 workspace 已释放后关闭 `Events()`。channel 关闭表示不会再有事件，此时 `Result()` 已经可用。
- `Result()` 可并发、多次调用，返回一致结果。
- 不关心的事件类型可以在 type switch 中省略，但 channel 仍必须持续 drain。

完整可运行示例见 [`examples/streaming`](../examples/streaming/main.go)；带 Thread 的交互示例见 [`examples/streaming/chat`](../examples/streaming/chat/main.go)。

## 3. Event 词汇与顺序

一次运行的所有语义和操作信号都在同一个 `<-chan adaptor.Event` 中：

| Event | 用途 |
|---|---|
| `RunStarted` / `RunFinished` | 运行生命周期；最终成败仍以 `Stream.Result()` 为准 |
| `TextDelta` | assistant 文本，`PhaseStart` / `PhaseContent` / `PhaseEnd` |
| `Thinking` | reasoning 生命周期 |
| `ToolCall` / `ToolResult` | 工具调用参数、边界与结果 |
| `ProcessInfo` | 子进程 spawn、原始 stdout/stderr chunk |
| `Notice` | invocation、runtime、step、transcript item、审批审计信息 |
| `*ApprovalRequest` | 带 exactly-once responder 的宿主审批请求 |
| `Dropped` | 默认背压下被丢弃增量的聚合报告 |
| `SubagentUpdate` | delegation 的实时进度 |

每个事件的 `Meta()` 返回 SDK 权威信封：

- `RunID`：本次执行 ID。
- `ThreadKey`：宿主提供的不透明 Thread key。
- `Sequence`：SDK 在统一 sink 的接收顺序中分配，单次运行严格递增。
- `Time`：SDK 接收事件的时间。
- `Source`：可选的 provider/Driver 原始坐标；它不会覆盖 SDK 的权威字段。

并发 producer 也由同一个 broker 串行化，因此 channel 接收顺序与 `EventMeta.Sequence` 一致。Bridge 可使用它作为单次运行内的 wire cursor，不应把 provider 序号提升为第二个权威顺序。需要跨多个运行持久化与恢复的 recorder 必须分配自己的 host-scoped cursor（`sessionrecorder.HostSeq`），因为每个新运行的 `EventMeta.Sequence` 都会重新开始。

## 4. Result、错误与取消

事件是进行中的观察，`Stream.Result()` 才是终局权威：

- 成功：返回 `*Result, nil`。
- 业务失败：返回 `nil, *RunError`；部分或完整结果在 `RunError.Result`。
- 基础设施失败：返回 `nil, error`，并保留 `errors.Is/As` 链。

`Result.Text`、`Summary`、`Raw()`、`Transcript()` 与 `Services()` 是彼此独立的层。不要从某个 `RunFinished` 事件重建最终结果，也不要把实时 delta 自行拼成审计用 Raw 或 Transcript。

`Cancel()` 幂等，并会解除阻塞的事件发布、审批等待和运行 context。取消后可能仍有已缓冲事件可读；继续 range 到 `Events()` 关闭，再调用 `Result()` 可获得最终取消错误。

如果消费者准备提前停止读事件，必须先调用 `Cancel()`。不能只停止 range 后直接等待 `Result()`：可靠事件或 blocking 模式可能正在等待 channel 空间。

## 5. 背压

默认普通事件 buffer 是 1024，可在构造 Agent 时调整；SDK 另行保留终局事件容量，不计入该数值：

```go
ai := adaptor.New(
	codex.Driver(codex.Config{}),
	adaptor.WithEventBuffer(256),
)
```

默认策略只允许丢弃可重放或高频增量：

- `TextDelta`、`Thinking`、`ToolCall` 的 `PhaseContent`
- stdout/stderr `ProcessInfo`
- `SubagentUpdate{Kind: SubagentDelta}`

生命周期边界、审批、终局事件、tool result、transcript item 和 `Dropped` 本身是可靠事件。当发生丢弃时，SDK 会发出聚合 `Dropped`，其中 `Count`、`ByKind`、`FirstSequence`、`LastSequence`、`Reason` 和 `Source` 描述缺口。显式取消进入 abort teardown 后，可以放弃尚未进入 channel 的增量事件与待聚合 `Dropped`，但权威 `RunFinished` 使用独立保留容量，仍会作为最后一个事件交付。

需要无损事件时使用 construction-scope 选项：

```go
ai := adaptor.New(
	codex.Driver(codex.Config{}),
	adaptor.WithEventBuffer(256),
	adaptor.WithBlockingEvents(),
)
```

未取消时，blocking 模式不会丢事件，但慢消费者会反压 Driver；取消会解除这些阻塞并进入 teardown。无论采用哪种策略，生产宿主都应持续 drain；断开或放弃消费时立即调用 `Cancel()`。

## 6. Thread 上的流式对话

`Thread` 与 `Agent` 实现同一个 `Runner`。有状态对话只多一层 Thread 协调，不会产生第二条流：

```go
import "github.com/agent-dance/agent-adaptor/memory"

ai := adaptor.New(
	codex.Driver(codex.Config{Model: "gpt-5.4"}),
	adaptor.WithThreadStore(memory.NewStore()),
)

stream := ai.Thread("tenant-7/conversation-42").Stream(ctx, "Continue")
defer stream.Cancel()
for ev := range stream.Events() {
	// 同一套 adaptor.Event。
}
result, err := stream.Result()
```

Thread key 是一个宿主提供的不透明字符串。宿主应原样持有它；不要自行拼接 provider session ID，也不要从事件中的 `Source.ThreadID` 推导消费者身份。

## 7. 审批请求

没有安装 `OnApproval` callback 时，需人工回答的请求直接出现在同一事件流：

```go
for ev := range stream.Events() {
	switch req := ev.(type) {
	case *adaptor.ApprovalRequest:
		switch req.Kind {
		case adaptor.ApprovalQuestion:
			_ = req.Answer(ctx, "yes")
		default:
			_ = req.Approve(ctx)
		}
	}
}
```

`Approve`、`Deny`、`Answer` exactly-once；重复、过期、Kind 不匹配或未绑定 responder 都会立即返回稳定错误。超时、拒绝与重试行为只由 `Policy.Approvals` 决定，bridge 不另建策略。

不方便在事件循环中交互的宿主可通过 `adaptor.OnApproval` 安装 callback。两种消费方式共享同一个请求和结果合同。

## 8. AG-UI bridge

已有 AG-UI 传输层的宿主可直接翻译 `Stream`：

```go
import "github.com/agent-dance/agent-adaptor/bridges/agui"

stream := ai.Stream(ctx, prompt)
defer stream.Cancel()

for ev := range agui.EventsContext(ctx, stream) {
	// ev 是 AG-UI events.Event，可写入 SSE、WebSocket 或 recorder。
}
```

`agui.EventsContext`：

- 保证 `RUN_STARTED` 位于输出开头。
- 补齐并去重 text、thinking、tool-call 生命周期边界。
- 在所有打开的生命周期关闭后，根据 `Stream.Result()` 生成唯一的 `RUN_FINISHED` 或 `RUN_ERROR`。
- context 结束时取消底层 `Stream`，所有向下游的发送都可取消。
- 将审批映射成可配置的 tool-call 或 custom 事件。

没有 request-scoped context 的本地程序可使用 `agui.Events(stream)`；HTTP/WebSocket handler 应优先使用 `EventsContext`，避免客户端断开后 fan-out goroutine 滞留。

AG-UI 输入 helper `RunAgentInput` 会提取最后一条非空 user 文本；`UserTurnEvents` 可构造规范的 user `TextDelta` 三元组。Driver 只产生 assistant 文本，`RoleUser` 仅由 bridge 或宿主合成。

## 9. HTTP SSE bridge

`sse.Handler` 接受任意 `adaptor.Runner`：

```go
import (
	"net/http"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/bridges/sse"
)

mux := http.NewServeMux()
mux.Handle("/v1/chat", sse.Handler(ai, sse.Options{
	Protocol:          sse.AGUI,
	CORSAllowedOrigin: "*",
	Options: []adaptor.CallOption{
		adaptor.WithTimeout(2 * time.Minute),
	},
}))
```

最终 `Options` 字段：

- `Protocol`：`sse.AGUI`（零值）或 `sse.Raw`。
- `KeepAlivePing`：非零时写 SSE comment 以保持代理连接。
- `CORSAllowedOrigin`：非空时写 CORS headers。
- `WriteTimeout`：每次底层 Write/Flush 前重置 write deadline，默认 30 秒；它不是整帧或整次运行的总预算。不支持 deadline 的 `ResponseWriter` 透明回退。
- `Options`：追加到每次 `Runner.Stream` 的 `[]adaptor.CallOption`。

AG-UI 模式接收标准 `RunAgentInput`：

```json
{
  "threadId": "conversation-42",
  "runId": "browser-turn-8",
  "messages": [
    {"id": "m-8", "role": "user", "content": "Write a haiku"}
  ]
}
```

Raw 模式接收：

```json
{"prompt":"Write a haiku","sessionKey":"conversation-42"}
```

当 handler 收到 session identity 且 `Runner` 是带 Thread Store 的 `*adaptor.Agent` 时，它会绑定到 `Agent.Thread`。如果传入的本来就是 `*adaptor.Thread`，会保持宿主固定的 Thread。AG-UI 的 `threadId` 使用无碰撞 tuple 编码；Raw 的 `sessionKey` 原样保留。

客户端断开会取消 request context 和底层 `Stream`。Raw 帧使用 `EventMeta.Sequence` 作为 SSE `id`，支持读取 `Last-Event-ID` 作为后备游标；持久化 replay 仍由宿主负责。

SSE 是单向传输，审批请求只能作为信息帧发送。交互式审批应使用 `OnApproval`，或由宿主持有 live responder 并提供受鉴权的 companion endpoint。

完整服务示例见 [`examples/web-chat`](../examples/web-chat/main.go)，AG-UI client 见 [`examples/web-chat/aguiclient`](../examples/web-chat/aguiclient/main.go)，CopilotKit 集成见 [`examples/web-chat/copilotkit`](../examples/web-chat/copilotkit/server.go)。

## 10. Driver streaming fidelity

每个 `Runner` 都有 `Stream`；`driver.StreamCapability` 只描述 provider 原生事件的细粒度，不是另一个执行能力，也不是 A2A transport capability。

当前内置 Driver 的声明：

| Driver | Native | TokenLevel | Reasoning | ToolCallArgs | HITL |
|---|---:|---:|---:|---:|---:|
| Codex | ✓ | ✓ | ✓ | ✓ | — |
| Claude | ✓ | ✓ | ✓ | ✓ | ✓ |
| Cursor | — | — | — | — | — |
| CodeBuddy | ✓ | ✓ | ✓ | ✓ | ✓ |

能力为 false 时，宿主仍然使用同一个 `Stream`。事件可能只有较粗粒度的 lifecycle、process、transcript 和最终结果；宿主不应因为 fidelity 较低而创建另一条调用路径。

当结构化输出 schema 与富事件 provider 传输不兼容时，core 可以选择兼容的批量 provider 传输，同时仍通过统一 `Event` channel 暴露运行过程。这一裁决不改变 `Run` 与 `Stream` 的公共语义。

## 11. AG-UI 版本对齐

Go 侧通过 `go.mod` 固定 `github.com/ag-ui-protocol/ag-ui/sdks/community/go`；CopilotKit 示例通过 [`examples/web-chat/copilotkit/web/package-lock.json`](../examples/web-chat/copilotkit/web/package-lock.json) 固定 `@ag-ui/core`。两边版本坐标不同，升级任一侧都要重新验证事件 lifecycle 和 schema。

`go test ./internal/aguiversion/...` 会校验两个 pin。主动升级时，同步更新 `internal/aguiversion/align_test.go` 中的期望版本，并复核 `bridges/agui` fixtures。
