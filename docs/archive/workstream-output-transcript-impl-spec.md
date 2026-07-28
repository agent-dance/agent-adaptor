# Output / Transcript 实现规格

本文档是实施规格，不再讨论“要不要做”，只定义“应该怎么做”。

目标：

- 让实现者可以直接按本文档修改代码
- 降低在实现中临时拍板字段语义的概率
- 保证 built-in adapters、tests、examples 的行为一致

命名约束：

- 不在代码类型名、函数名、字段名、文件名中引入 `V2` / `v2`
- 不保留“旧版 + 新版”双轨 API
- 允许 break，但最终仓库里只保留一套新合同

## 1. 类型规格

## 1.1 `RawStreams`

新增：

```go
type RawStreams struct {
	Stdout string
	Stderr string
}
```

语义：

- `Stdout` 是运行期捕获到的原始 stdout 完整内容
- `Stderr` 是运行期捕获到的原始 stderr 完整内容
- 不做 redaction
- 不做语义解析
- 不做截断

## 1.2 `RunResult`

最终形态要求至少包含：

```go
type RunResult struct {
	RunID           string
	DriverType      string
	Output          string
	RawStreams      *RawStreams
	Transcript      []TranscriptItem
	ExitCode        int
	Signal          string
	TimedOut        bool
	Usage           *Usage
	Session         *SessionRef
	Metadata        map[string]string
	Provider        string
	Biller          string
	Model           string
	BillingType     string
	CostUSD         *float64
	Summary         string
	Result          map[string]any
	RuntimeServices []RuntimeServiceReport
	Question        *RunQuestion
	Failure         *RunFailure
}
```

字段规则：

- `Output`: 最终 assistant 文本
- `RawStreams`: 原始 stdout/stderr
- `Transcript`: 结构化语义条目
- `Summary`: 简短摘要，不等于 `Output`
- `Result`: terminal result 原始 JSON
- `Metadata`: 仅用于简单字符串标签，不承载大块原始输出

## 1.3 `DriverRunResult`

`DriverRunResult` 与 `RunResult` 保持同构，至少也要新增：

- `RawStreams *RawStreams`

并且 built-in adapters 要把：

- `Output`
- `RawStreams`
- `Transcript`
- `Summary`
- `Result`
- `Checkpoint`

全部在同一次解析后填好。

## 1.4 `RunEvent`

最终事件类型：

```go
type RunEventType string

const (
	RunEventChunk      RunEventType = "chunk"
	RunEventItem       RunEventType = "item"
	RunEventInvocation RunEventType = "invocation"
	RunEventSpawn      RunEventType = "spawn"
	RunEventRuntime    RunEventType = "runtime"
	RunEventLifecycle  RunEventType = "lifecycle"
)

type RunEvent struct {
	Type      RunEventType
	Seq       uint64
	Timestamp time.Time

	Stream string
	Bytes  []byte

	Item *TranscriptItem

	Text     string
	Metadata map[string]string
	Data     map[string]any
}
```

字段规则：

- `Seq`: 同一 run 内单调递增
- `Stream`: 仅 `chunk` 事件使用，值只能是 `stdout` 或 `stderr`
- `Bytes`: 仅 `chunk` 事件使用
- `Item`: 仅 `item` 事件使用
- `Text/Metadata/Data`: 供 invocation/spawn/runtime/lifecycle 使用

## 1.5 `TranscriptItem`

推荐结构：

```go
type TranscriptKind string

const (
	TranscriptAssistant  TranscriptKind = "assistant"
	TranscriptThinking   TranscriptKind = "thinking"
	TranscriptUser       TranscriptKind = "user"
	TranscriptToolCall   TranscriptKind = "tool_call"
	TranscriptToolResult TranscriptKind = "tool_result"
	TranscriptInit       TranscriptKind = "init"
	TranscriptResult     TranscriptKind = "result"
	TranscriptStdout     TranscriptKind = "stdout"
	TranscriptStderr     TranscriptKind = "stderr"
	TranscriptSystem     TranscriptKind = "system"
	TranscriptSummary    TranscriptKind = "summary"
	TranscriptQuestion   TranscriptKind = "question"
	TranscriptFailure    TranscriptKind = "failure"
)

type TranscriptItem struct {
	Kind      TranscriptKind
	Text      string
	Delta     bool
	ToolUseID string
	ToolName  string
	Input     any
	IsError   bool
	Model     string
	SessionID string
	Usage     *Usage
	CostUSD   *float64
	Subtype   string
	Errors    []string
	Metadata  map[string]string
	Data      map[string]any
}
```

### kind 字段规则

#### `assistant`

- 必填：`Kind`, `Text`
- 可选：`Delta`
- 禁止：`Input`, `Usage`

#### `thinking`

- 必填：`Kind`, `Text`
- 可选：`Delta`

#### `user`

- 必填：`Kind`, `Text`

#### `tool_call`

- 必填：`Kind`, `ToolName`
- 推荐：`ToolUseID`
- 可选：`Input`

#### `tool_result`

- 必填：`Kind`, `ToolUseID`
- 推荐：`Text`
- 可选：`ToolName`, `IsError`

#### `init`

- 必填：`Kind`
- 推荐：`Model`, `SessionID`

#### `result`

- 必填：`Kind`
- 可选：`Text`, `Usage`, `CostUSD`, `Subtype`, `IsError`, `Errors`

#### `stdout` / `stderr` / `system`

- 必填：`Kind`, `Text`
- 用于 parser fallback 或系统提示

#### `summary`

- 必填：`Kind`, `Text`

#### `question`

- 必填：`Kind`, `Text`
- 选择项放 `Data["choices"]`

#### `failure`

- 必填：`Kind`, `Text`
- 错误码放 `Metadata["code"]`

### `Metadata` 与 `Data` 的使用规则

- `Metadata` 只放简短字符串标签
- `Data` 只放 provider-specific 或结构化扩展内容
- 同一个语义字段如果已经有正式 struct 字段，不再重复塞入 `Metadata` 或 `Data`

## 2. `Output` / `Summary` / `Result` 规则

## 2.1 `Output`

`Output` 只来自 `assistant` transcript。

实现规则：

- 只收集 `TranscriptAssistant`
- 按 transcript 顺序拼接
- 非空段之间使用 `"\n\n"` 连接
- 不拼接 `thinking`
- 不拼接 `tool_result`
- 不拼接 `Summary`
- 不拼接 terminal `result`

特殊情况：

- 若没有任何 assistant 文本，`Output == ""`
- mixed block 中只抽 text/assistant 部分进入 `Output`

## 2.2 `Delta`

`Delta` 只允许出现在：

- `assistant`
- `thinking`

实现规则：

- 流式阶段可以逐条 emit `Delta=true`
- 最终 `RunResult.Transcript` 不强制必须合并 delta，但 `Output` 必须基于合并后的 assistant 文本
- 如果提供 helper，helper 的职责是把连续同 kind 且 `Delta=true` 的 item 合并

## 2.3 `Summary`

`Summary` 是短摘要，不是最终答复。

实现规则：

- adapter 优先从 terminal result 事件中提取
- 若 terminal result 没有摘要字段，可退化为 adapter 自己稳定生成的短文本
- `Summary` 不得通过拼接 raw stdout 推导

## 2.4 `Result`

`Result` 是 terminal result 原始 JSON。

实现规则：

- 尽量保留 adapter 识别到的最后一条正式 result 事件原始 payload
- 若 provider 根本没有 terminal result 事件，允许为 `nil`
- 不把完整 `RawStreams.Stdout` 或 `RawStreams.Stderr` 塞进 `Result`

## 3. helper 实施规格

## 3.1 `clihelper` 职责

`internal/clihelper` 负责：

- 启动进程
- 写 stdin
- 读取 stdout/stderr
- 聚合 `RawStreams`
- 为每个 chunk emit `RunEventChunk`
- 把 chunk 回调给 adapter parser
- 返回 exit code / signal / timedOut

不负责：

- JSON 解析
- transcript 生成
- checkpoint 识别
- session 推断

## 3.2 建议接口

推荐改造为：

```go
type ChunkObserver func(stream string, chunk []byte, ts time.Time) error

type CommandRequest struct {
	Command string
	Args    []string
	CWD     string
	Env     []agentadaptor.EnvBinding
	Prompt  string
	Observe ChunkObserver
}

type CommandResult struct {
	RawStreams agentadaptor.RawStreams
	ExitCode   int
	Signal     string
	TimedOut   bool
}
```

`Observe` 规则：

- `stream` 只可能是 `stdout` 或 `stderr`
- `chunk` 可能不是完整行
- helper 必须先 emit `RunEventChunk`，再调用 `Observe`
- `Observe` 返回 error 时，本次运行直接失败返回

## 3.3 事件顺序

- `Seq` 由 `EventSink.Emit` 统一分配
- 任何通过 sink 发出的事件都走同一序列
- transcript 一致性验收以 `Seq` 为准，而不是 timestamp

## 4. built-in adapter 映射表

## 4.1 codex

当前代码位置：

- `codex/driver.go`
- 现有辅助：`parseCodexJSONL`

terminal 语义来源：

- `thread.started` -> `init`
- `item.started` / `item.completed` -> `assistant` / `thinking` / `tool_call` / `tool_result`
- `turn.completed` -> `result`
- `turn.failed` -> `result` with error

字段映射：

- `Output`: 所有 `agent_message` / assistant 文本拼接
- `Summary`: `turn.completed.result` 或 parser 已提取的 `summary`
- `Result`: `turn.completed` 或 `turn.failed` 原始 payload
- `Usage`: 来自 terminal event `usage`
- `Checkpoint`: 继续沿用当前 codex 顶层 checkpoint 识别规则

## 4.2 claude

当前代码位置：

- `claude/driver.go`
- 现有辅助：`parseCheckpoint`

terminal 语义来源：

- `type=system, subtype=init` -> `init`
- `type=assistant` -> `assistant` / `thinking` / `tool_call`
- `type=user` with `tool_result` -> `tool_result`
- `type=result` -> `result`

字段映射：

- `Output`: assistant content 中 `text` block 拼接
- `Summary`: 优先 parser 提取的 summary；其次 terminal `result` 字段
- `Result`: 最后一条 `type=result` 原始 payload
- `Usage`: 来自 `type=result.usage`
- `Checkpoint`: 继续遵循当前 claude 顶层 checkpoint 识别逻辑

## 4.3 cursor

当前代码位置：

- `cursor/driver.go`
- 现有辅助：`parseCheckpoint`

terminal 语义来源：

- 以 cursor stream-json 正式事件为准
- 若存在 delta 事件，映射为 `assistant` / `thinking` 且 `Delta=true`

字段映射：

- `Output`: 合并后的 assistant 文本
- `Summary`: terminal result 摘要
- `Result`: terminal result 原始 payload
- `Usage`: 从 terminal result 提取
- `Checkpoint`: 继续遵循当前 cursor 顶层 checkpoint 识别逻辑

## 5. fixture 与测试规格

## 5.1 fixture 命名

每个 built-in adapter 至少提供：

- `happy-assistant.jsonl`
- `with-thinking.jsonl`
- `with-tool.jsonl`
- `multi-message.jsonl`
- `long-line.jsonl`
- `failure.jsonl`

建议放在：

- `codex/testdata/`
- `claude/testdata/`
- `cursor/testdata/`

## 5.2 golden 文件

每个 fixture 对应两类 golden：

- `*.transcript.json`：`[]TranscriptItem`
- `*.result.json`：最终 `DriverRunResult` 关键字段快照

`*.result.json` 至少包含：

- `output`
- `summary`
- `has_result`
- `usage`
- `checkpoint_valid`

## 5.3 测试清单

每个 built-in adapter 至少要有：

1. fixture transcript 对比
2. fixture result 对比
3. chunk boundary 测试
4. long line 不截断测试
5. failure fixture 测试
6. raw streams 完整性测试

其中：

- chunk boundary 测试：把同一 fixture 切成不同粒度喂入，最终 transcript 必须相同
- long line 测试：验证不会受 scanner 默认上限影响

## 5.4 集成测试

根目录需要至少两个集成测试：

### A. `Events()` 与 `Transcript` 一致

- 启动 fake CLI
- 收集所有 `RunEventItem`
- `Wait()` 返回 `RunResult`
- 断言 `RunResult.Transcript` 等于按 `Seq` 收集到的 item 序列

### B. `Run()` 也有 `RawStreams`

- 直接调用 `sdk.Run()`
- 断言 `result.RawStreams.Stdout/Stderr` 可用于审计

## 6. 实施顺序

建议顺序：

1. 更新 types 与注释
2. 改 `clihelper`
3. 删除 helper 里的猜解析
4. 先落 `codex`
5. 再落 `claude`
6. 最后落 `cursor`
7. 迁移 examples
8. 完成 parser tests 与 integration tests
9. 删除旧 enum / 旧 helper

## 7. 明确不做

本轮不做：

- public test SPI
- 新的 HTTP/WS 服务协议
- provider 全量抽象统一
- transcript 的双轨兼容 API
