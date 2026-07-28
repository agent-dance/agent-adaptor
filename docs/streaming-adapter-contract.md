# Driver Streaming Contract

本文档定义 Driver 作者必须满足的实时事件、最终结果与协议解析合同。应用宿主应阅读 [Streaming 指南](./streaming.md)；扩展作者以 [`driver`](../driver/doc.go) 包 godoc 和 [`adaptertest`](../adaptertest/doc.go) 条款为最终可执行规范。

## 1. 分层边界

应用层始终面对同一套 `Runner`、`Stream`、`Event` 和 `Result`。Driver 不实现第二个执行入口，也不接触 bridge：

```go
type Driver interface {
	Descriptor() Descriptor
	ValidateConfig(cfg any) error
	Run(ctx context.Context, req Request, sink EventSink) (Response, error)
}
```

`ValidateConfig(nil)` 不得 panic。对 `mydriver.Driver(cfg)` 这类已捕获构造配置的 Driver，nil 表示“校验已经捕获的配置”，不能被解释成丢失配置；执行与 Inspect probes 必须观察同一份构造期配置。

职责划分：

- core：合并选项，解析 workspace、profile、skills、MCP、runtime 和 schema，协调 Thread，验证 capability，把 SPI 事件转换成一条公共 typed Event 流，并形成最终 `Result`。
- Driver：验证自己的配置，执行 provider，解析官方协议，从同一次解析形成 `StreamPayload`、`Transcript`、`Output`、`Summary`、`RawStreams`、terminal payload 与 checkpoint。
- process helper：启动/停止进程、传 stdin、捕获完整 stdout/stderr、发送 raw chunk，并把原始数据 tee 给 Driver parser；不得猜 provider 语义或 checkpoint。
- bridge：只把公开 `Runner`、`Stream`、`Event`、`Result` 翻译成 AG-UI、SSE、A2A 等外部协议，不调用 Driver。

`driver` 包本身不得反向 import 模块根包、具体 provider、bridge 或 internal implementation。第三方实现只依赖公开 `driver` SPI；内置 Driver 可以把仓库私有实现局部化在自己的包内，但公共 Config 与签名不得泄露私有类型。

## 2. Provider transport 与 `StreamCapability`

实时 Event 是 SDK 固有能力；provider 是否有原生细粒度传输是另一维度。可提供规范化实时 payload 的 Driver 实现可选接口：

```go
type StreamSupport interface {
	StreamCapability() StreamCapability
}

type StreamCapability struct {
	Native       bool
	TokenLevel   bool
	Reasoning    bool
	ToolCallArgs bool
	HITL         bool
}
```

字段必须保守、确定且跨实例稳定：

- `Native`：底层是正式事件协议，而不是从自由文本猜事件。
- `TokenLevel`：一个 assistant message 可产生多个细粒度文本 delta。
- `Reasoning`：正式协议暴露 thinking/reasoning lifecycle。
- `ToolCallArgs`：正式协议暴露增量 tool arguments；否则完整参数应放在 tool-call opening payload。
- `HITL`：正式协议暴露人机决策事件；真正的阻塞应答仍通过 `DecisionCapableSink`。

`StreamCapability` 不决定应用是否能调用 `Runner.Stream`，也不表示远端 A2A 是否支持 streaming。A2A transport 只能依据远端 Agent Card 协商。

## 3. `Request.Streaming`

`driver.Request` 是 core 已解析完毕的单次调用。与本合同最相关的字段是：

```go
type Request struct {
	RunID        string
	Prompt       string
	Config       any
	Session      *SessionContext
	OutputSchema *OutputSchema
	Streaming    bool
	// 以及已解析的 Agent、Workspace、Runtime、Skills、MCP、Profile、
	// Policy、Instructions、Metadata、ModelOverride。
}
```

已配置 Driver 收到 `Config=nil` 时继续使用构造时捕获的 Config。第三方实现不得把它当成一个新的空配置，从而让执行、capability probe 与 Inspect 观察到不同语义。

`Request.Streaming` 选择 provider-native 富事件传输。它由 core 根据 resolved invocation、`StreamCapability`、结构化输出与审批兼容性设置，不由应用调用 `Run` 还是 `Stream` 决定。两种应用动词都可能拿到 `Streaming=true` 或 `false`。

当 `Streaming=true`：

- Driver 应使用其声明的富事件 transport；只有 `Native=true` 时才能额外声称它是 provider 原生正式事件协议。
- 必须满足本文件的 `StreamPayload` lifecycle。
- `Response` 的所有结果层仍必须完整；实时事件不能取代最终响应。

当 `Streaming=false`：

- Driver 使用兼容的 provider 传输。
- 仍应通过 `EventSink.Emit` 提供 raw chunk、transcript item 和操作事件。
- 不得因为应用使用 `Runner.Stream` 就私自切到另一套 provider 协议。

## 4. `EventSink`

```go
type EventSink interface {
	Emit(event RunEvent) error
	EmitStream(payload StreamPayload) error
}
```

- `Emit`：操作事件和逐步解析出的 transcript item。
- `EmitStream`：规范化 text、thinking、tool、step、run、HITL 与 provider drop payload。

两者最终都进入同一条公共 `Event` channel；它们不是两条宿主流。Driver 不得保存 sink，也不得在 `Run` 返回后继续调用它。

`Run` 返回前必须等待所有 parser、reader、stderr collector 和 notification goroutine 退出。context 取消时应停止 provider、解除 stdin/reader 阻塞并尽快回收 goroutine。任何 goroutine 在 `Run` 返回后继续 emit 都违反生命周期合同。

## 5. `StreamPayload` lifecycle

每次 `Request.Streaming=true` 的运行必须：

1. 第一帧恰好一个 `StreamRunStarted`。
2. 中间帧遵守各自 lifecycle 和字段要求。
3. 所有已打开 text、reasoning、tool 和 step lifecycle 在终局前关闭。
4. 最后一帧恰好一个 `StreamRunFinished` 或 `StreamRunError`。
5. 终局后不再 emit 任何 payload。

三个 `StreamRun*` payload 的 `MessageID` 与 `ToolCallID` 必须为空；run lifecycle 不能伪装成 message 或 tool lifecycle。

规范事件：

| Kind | 必填/关键字段 | 合同 |
|---|---|---|
| `StreamRunStarted` | 可选 provider `RunID` / `ThreadID` / `TurnID` | 整个运行一次且最先；SDK RunID 由 core 另行赋值 |
| `StreamRunFinished` | 可选 `Usage` | 正常终局 |
| `StreamRunError` | `Error` | 失败终局，`Error` 不得为空 |
| `StreamStepStarted` / `StreamStepFinished` | `Name` | 成对的 provider step |
| `StreamTextStart` | `MessageID` | 打开一个 assistant message |
| `StreamTextContent` | `MessageID`, 非空 `Delta` | 只可发生在已打开 message 中 |
| `StreamTextEnd` | `MessageID` | 关闭 message |
| `StreamReasoningStart` / `Content` / `End` | `MessageID`，content 的 `Delta` 非空 | 与 text 同样配对 |
| `StreamToolCallStart` | `ToolCallID`, `Name`，可选完整 `Args` | 打开 tool call |
| `StreamToolCallArgs` | `ToolCallID`, 非空 `Delta` | 仅在 `ToolCallArgs=true` 时 emit |
| `StreamToolCallEnd` | `ToolCallID`，可选 `Result` | 关闭 tool call |
| `StreamToolCallResult` | 已知 `ToolCallID`，可选 `Result` | 独立的完成结果 |
| `StreamHITLRequested` / `Resolved` | 对应的结构化 envelope | 只读审计广播，不携带 responder |
| `StreamDropped` | `Raw["dropped_count"]` 等缺口信息 | provider 自己报告的丢失 |

同一 ID 的 opening、content、closing 必须严格按顺序；不同 ID 可以交错。Capability 的硬负面合同只适用于对应事件族：`ToolCallArgs=false` 不得 emit `StreamToolCallArgs`，`Reasoning=false` 不得 emit reasoning 事件，`HITL=false` 不得 emit HITL 广播。`Native=false` 与 `TokenLevel=false` 描述传输来源和粒度，不禁止 coarse text lifecycle。

Provider 的未知正式事件可使用自定义 `StreamKind` 并把无法规范化的字段放在 `Raw`。core 会将它转换为 `Notice`，bridge 可进一步降级为外部协议的 custom event；不得静默吞掉有审计价值的事件。

### 5.1 Role

Driver 发出的每个 `StreamPayload` 都必须让 `Role` 保持零值 `RoleAssistant`。`RoleUser` 只允许 bridge 或宿主在 Driver 之上合成人类输入 lifecycle；Driver 不得回放 user prompt 冒充自己的输出。

### 5.2 Sequence 权威

Driver 必须让以下字段保持零值：

- `StreamPayload.Sequence`
- `StreamPayload.Seq`
- `StreamPayload.Timestamp`
- `RunEvent.Seq`

core 在统一 sink 接收顺序中串行化并分配公共 `EventMeta.Sequence` 与时间。Driver 可在 `RunID`、`ThreadID`、`TurnID` 等字段保留 provider 坐标，core 会把这些坐标放入 `EventMeta.Source`；它们不会覆盖 SDK 权威顺序。

不要在多个 goroutine 中预分配 sequence。只要按协议顺序调用 sink，core 会保证公共 channel 顺序与 `EventMeta.Sequence` 一致。

## 6. `RunEvent` 与 Transcript mirror

`EventSink.Emit` 支持：

- `RunEventChunk`：`Stream` 只能是 `stdout` 或 `stderr`，`Bytes` 可为任意 chunk 边界。
- `RunEventItem`：`Item` 必须是有效 `TranscriptItem`。
- `RunEventInvocation`、`RunEventSpawn`、`RunEventRuntime`、`RunEventLifecycle`：使用 `Text`、`Metadata`、`Data`。

`RunEventItem` 的接收顺序必须逐项、完整地等于最终 `Response.Transcript`。不能在结束时用另一套 heuristic 重算 transcript，也不能 emit 一个版本、返回另一个版本。

Transcript 只能来自 Driver 对 provider 正式协议的解析。文本、thinking、tool call/result、init、terminal result、question 和 failure 必须使用正确的 `TranscriptKind` 与字段；shared helper 不得扫描任意 JSON 猜 semantic item。

## 7. `Response` 合同

```go
type Response struct {
	Output           string
	Summary          string
	RawStreams       *RawStreams
	Transcript       []TranscriptItem
	Usage            *Usage
	Checkpoint       *Checkpoint
	StructuredOutput *StructuredOutput
	RuntimeServices  []RuntimeServiceReport
	Failure          *RunFailure
	ExitCode         int
	Signal           string
	TimedOut         bool
	// 以及 provider/model/metadata 字段。
}
```

同一次官方协议解析必须同时形成这些层：

- `Output`：最终 assistant-facing 文本。不得放 raw stdout/stderr、Summary 或 terminal JSON。
- `Summary`：可选、短小的宿主摘要。没有正式摘要时保持空，不能回退为完整 `Output`。
- `RawStreams`：完整、未截断的 stdout 与 stderr，以及可选的官方 terminal payload。
- `Transcript`：正式 parser 产出的标准化语义条目。
- `Usage`：仅填 provider 实际观察到的 normalized accounting。
- `RuntimeServices`：Driver 实际观察到的服务报告，不回显宿主声明冒充成功证据。
- `Failure`：结构化业务失败；终局仍通过 core 的 Go error 路径返回。

`Run` 返回 non-nil error 时，任何 `Checkpoint.Valid=true` 都会被视为无效。当前 core 把这种情况作为基础设施/执行错误返回，不把同时返回的 partial `Response` 暴露成应用 `Result`；必要诊断必须保留在可包装 error 和已经发布的事件中，Driver 不得依赖 partial `Response` 作为错误审计面。结构化业务失败应使用 `Response.Failure`，由 core 形成携带 `Result` 的 `RunError`。

## 8. Raw 与 provider terminal

```go
type RawStreams struct {
	Stdout   string
	Stderr   string
	Terminal *TerminalPayload
}

type TerminalPayload struct {
	Event string
	JSON  json.RawMessage
}
```

硬约束：

- `Stdout` / `Stderr` 是子进程本次运行写出的完整、未截断字节内容，不做语义替换或逐行改写。
- JSON-RPC/app-server reader 必须先 tee 原始 stdout，再 decode；结束时等待 reader、process wait 与 stderr collector 完成后才能 snapshot。
- `Terminal` 只来自 Driver 识别到的 provider 官方终局事件。
- `Terminal.Event` 是 provider 原生 event/method 名。
- `Terminal.JSON` 保留 parser 识别到的精确 JSON value；不得从 `Output`、`Summary`、`Transcript` 或任意嵌套 JSON 合成。
- 没有观察到官方终局事件时 `Terminal` 为 nil。

应用 `Run` 与 `Stream.Result()` 必须拿到等价的 `Result.Raw()`。实时 delta 不能替代完整 Raw。

## 9. Checkpoint 安全

`Checkpoint.Valid=true` 只允许在以下条件全部成立时出现：

- provider 进程成功退出，`ExitCode == 0`。
- 没有 signal、timeout、context cancellation、Driver error 或 `Response.Failure`。
- 官方 parser 观察到明确的成功终局事件。
- 该正式协议事件提供顶层、明确的 resume/session identifier。
- `State` 非 nil，`State.ResumeID` 非空。
- 同一 Driver 的 `SessionCodec` 接受并能无损 round-trip 该状态。
- codec 为该状态生成非空、确定的 guard fingerprint。

init/session announcement、partial output、嵌套或猜测出的 ID、畸形协议、缺失 terminal、非零退出和终局错误事件都不足以形成有效 checkpoint。

`Descriptor.Sessions.SupportsResume=true` 当且仅当 Driver 同时满足两项构造期合同：实现 `SessionCodecProvider` 并稳定返回 non-nil、稳定命名的 codec；实现 `SessionConfigFingerprinter` 并稳定返回 non-empty、跨进程确定的构造配置 fingerprint。后者必须覆盖 provider 可见的全部构造期配置及 codec/version 合同；无法稳定表达的值必须报错，不能静默遗漏。缺失任一合同的公共 Thread 调用会在获取 workspace/runtime/store lease 或调用 `Driver.Run` 前以 `adaptor.ErrThreadIncompatible` 拒绝。Codec 的 nil/zero 映射、guard fingerprint、round-trip 与 config fingerprint 真话性必须满足 `adaptertest` 的 `CAP-*` / `SES-*` 条款。

失败运行没有 checkpoint 例外。没有健康 checkpoint 时 core 保留 Thread 之前的 active record，不允许失败运行污染续接状态。

## 10. HITL

需要阻塞等待宿主决策时，Driver 对 sink 做可选接口断言：

```go
if decisionSink, ok := sink.(driver.DecisionCapableSink); ok {
	resp, err := decisionSink.RequestDecision(ctx, driver.DecisionRequest{
		Kind:   driver.HumanDecisionPermission,
		Source: "mydriver.tool",
		Prompt: "Allow this operation?",
		Payload: map[string]any{"tool": "shell"},
	})
	// 按 resp.Result 或 err 推进/停止 provider 协议。
}
```

core 负责分配缺失的 request ID、时间和 deadline，执行 `Policy.Approvals`，并把可回答请求投影成公共 `*ApprovalRequest` 或交给 `OnApproval` callback。Driver 不得自己维护第二套 host channel、HTTP endpoint 或 retry policy。

Driver emit 的 `StreamHITLRequested` / `StreamHITLResolved` 是 provider 审计广播，core 将其投影为 `Notice`；它们没有 responder，不能替代 `RequestDecision`。`Descriptor.RunPolicyCaps` 与 `StreamCapability.HITL` 必须分别如实表达决策能力和协议可见性。

## 11. Backpressure 与取消

Driver 不选择宿主背压策略。默认 broker 可以丢高频 delta，但在未取消的正常运行中，审批、lifecycle、terminal、transcript、tool result 和 drop report 是可靠事件；因此即使默认模式，sink 调用也可能等待消费者空间。显式取消会进入 abort teardown，不承诺继续投递尚未进入 channel 的关键事件。

在 `WithBlockingEvents` 下所有事件都可能反压 Driver。Driver 必须让进程 I/O、parser callback 和 event emit 对 context cancellation 敏感，避免 reader、process wait 和 sink 形成闭环等待。

规则很简单：

- 持续检查 `ctx.Done()`。
- context 结束时停止 provider 和 stdin writer。
- 等待所有 I/O goroutine 退出后再返回。
- 不关闭 sink；channel lifecycle 由 core 独占。
- 不忽略可能表示 cancellation 的 helper/sink error。

## 12. `adaptertest` 验收

每个 Driver 都必须直接运行最终 conformance suite：

```go
func TestMyDriverConformance(t *testing.T) {
	adaptertest.TestDriver(t, func() driver.Driver {
		return mydriver.Driver(mydriver.Config{Model: "m-1"})
	},
		adaptertest.WithSessionState(&driver.SessionState{
			ResumeID: "session-1",
			Data: map[string]string{"cwd": "/repo"},
		}),
		adaptertest.WithSessionKeys("cwd"),
		adaptertest.WithGuardKeys("cwd"),
	)
}
```

所有适用于该 Descriptor、已实现可选接口与显式 opt-in 的 hermetic clauses 都会运行，覆盖 Driver/config、capability truthfulness、structured-output matrix、SessionCodec 与 SessionConfigFingerprinter；不适用的 optional capability 会明确 skip。真实 provider 执行通过 `WithLiveRun` 显式启用，并由 provider 包的 CLI-availability 与环境变量双门保护，普通 CI 不得产生付费调用。

与本合同直接相关的 clause groups：

- `CAP-10`：`StreamCapability` 确定性。
- `EVT-*`：run/text/reasoning/tool/step/HITL lifecycle、Role、zero sequence、terminal-last 与 capability negatives。
- `RUN-*`：raw chunk、transcript item、core-owned `RunEvent.Seq` 与 transcript mirror。
- `TRN-*`：各 `TranscriptKind` 的字段合法性。
- `RSP-*`：checkpoint cleanliness、Failure invariant、Output 分层、codec round-trip 与 official terminal JSON。

除套件外，Driver 自己还必须为每种正式协议路径覆盖：成功、非零退出、畸形协议、缺失 checkpoint、缺失 terminal、取消，以及 provider 特有的 parser lifecycle。Codex CLI 与 app-server 等不同传输必须各自有合同测试，不能用其中一个路径替另一个背书。

发布门禁至少包括：

```text
go test -count=1 ./adaptertest ./yourdriver/...
go vet ./adaptertest ./yourdriver/...
```

Linux CI 还必须运行 race；关键 parser 与 archive parser 按仓库约定执行 fuzz。
