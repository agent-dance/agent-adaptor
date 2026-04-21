# Workstream: Output / Transcript Contract

## 1. 目标

把当前混杂的“原始输出 + transcript + summary”重整为一套可长期依赖的输出合同。

这次 workstream 明确允许 public API break。目标不是继续给旧语义打补丁，而是把 `RunResult`、`RunEvent`、`DriverRunResult` 的职责一次性讲清楚。

## 2. 为什么现在必须做

当前仓库在输出层有三个问题：

1. `Output` 在不同调用方心里语义冲突：
   - 有的地方把它当最终文本
   - 有的地方把它当 raw stdout
2. shared helper 会猜 stdout/stderr 里的 JSON，这和 adapter 自己识别正式协议的边界冲突
3. `Run()` 调用方很难同时拿到“最终文本”和“原始 stdout/stderr”

`paperclip` 已经验证了一件事：宿主真正需要的是三层同时成立，而不是只要其中一层：

- 原始字节流，用于审计、重放、调试
- 标准化 transcript，用于 UI 和统一消费
- 终局结果字段，用于 summary、usage、model、session、billing

## 3. 核心合同

### 3.1 `Output`

`Output` 的唯一语义是：

- 最终 assistant-facing 文本输出

硬约束：

- 没有 assistant 文本时允许为空字符串
- 不允许再承载 raw stdout dump
- 不允许自动拼接 `Summary`
- 不允许自动拼接终局 `Result`

### 3.2 `RawStreams`

`RawStreams` 代表本次运行的原始 stdout/stderr：

- `RawStreams.Stdout`
- `RawStreams.Stderr`

硬约束：

- `Run()` 与 `Start().Wait()` 都必须返回完整可用的 `RawStreams`
- `RawStreams` 是宿主做审计、重放、debug、落盘的稳定入口
- 不再通过 `Metadata["stderr"]` 这类 hack 暴露 stderr

### 3.3 `Transcript`

`Transcript` 是 host-facing 的标准化语义条目，至少覆盖：

- `assistant`
- `thinking`
- `user`
- `tool_call`
- `tool_result`
- `init`
- `result`
- `stdout`
- `stderr`
- `system`
- `summary`
- `question`
- `failure`

设计原则：

- transcript 必须来自 adapter 对正式协议的识别
- 不靠 shared helper 猜 JSON 行
- provider 无法识别的内容可以退化成 `stdout` / `stderr` / `system`

### 3.4 `Summary`

`Summary` 代表适合列表、run card、issue comment 的简短摘要。

硬约束：

- `Summary` 不等于 `Output`
- `Summary` 优先来自 adapter 识别出的 terminal result 事件
- 宿主展示可使用 `Output -> Summary -> Result` 作为 fallback，但 SDK 不自动混字段

### 3.5 `Result`

`Result` 代表 adapter 识别出的 terminal result 事件原始 JSON。

硬约束：

- 尽量填入“最后一条正式 result 事件”的原始 payload
- 允许为 `nil`
- 不把整段 stdout/stderr dump 冒充成 `Result`

## 4. `RunEvent`

`RunEvent` 需要重整为两类主信号：

- `RunEventChunk`：原始 stdout/stderr chunk
- `RunEventItem`：标准化 transcript item

同时保留：

- `RunEventInvocation`
- `RunEventSpawn`
- `RunEventRuntime`
- `RunEventLifecycle`

### 4.1 顺序合同

流式事件的稳定顺序以 `Seq` 为准。

不承诺：

- stdout 与 stderr 的 wall-clock 交错顺序
- chunk 边界稳定

必须承诺：

- `RunResult.Transcript` 等于按 `Seq` 收集到的全部 `RunEventItem`

## 5. helper 与 adapter 的职责分工

### 5.1 `internal/clihelper`

shared helper 负责：

- 启进程
- 传 stdin
- 读 stdout / stderr
- 聚合原始 stdout / stderr
- 发原始 chunk 事件
- 把原始 chunk tee 给 adapter parser callback

shared helper 不负责：

- 解析 provider/CLI 协议
- 猜 assistant / tool / result 语义
- 猜 `session_id`
- 识别正式 checkpoint

### 5.2 built-in adapters

每个 built-in adapter 都必须：

- 解析自己的正式协议
- 从同一次解析同时产出：
  - `Transcript`
  - `Output`
  - `Summary`
  - `Result`
  - `Checkpoint`
- 自己决定 `DriverCheckpoint.Valid`

## 6. 实施范围

本 workstream 进入 core 的内容：

- `RunResult` / `DriverRunResult` 的输出分层
- `RunEvent`
- `TranscriptItem`
- `internal/clihelper` 的职责重整
- built-in `codex` / `claude` / `cursor` 的 streaming parser

本 workstream 不做：

- HTTP / WS server
- run log store
- UI framework
- 对外发布新的宿主 service 层协议
- 测试专用 public SPI

## 7. 迁移策略

这次 workstream 不保留旧枚举的兼容层。

需要一次性迁移：

- core types
- built-in adapters
- examples
- tests
- 文档

但不引入第二套执行入口，也不改变 `Run/Start/Admin` 的主心智。

## 8. 命名约束

这次 workstream 虽然允许 public API break，但当前项目还没有正式用户，因此不采用带版本号的代码命名。

硬约束：

- 不在 Go 类型名里引入 `V2` / `v2`
- 不在函数名、字段名、常量名里引入 `V2` / `v2`
- 不因为允许 break 就保留“旧版 + 新版”双轨命名

允许保留版本号的地方仅限历史文档文件名；新的实现规格、代码、测试、示例都不应引入版本后缀。
## 9. 验收标准

### 9.1 输出合同

- `Run()` 与 `Start().Wait()` 都能拿到完整 `RawStreams`
- `Output` 在 built-in adapters 上都等于最终 assistant 文本
- `Summary` 与 `Result` 的职责清晰且不混用

### 9.2 流式合同

- 宿主可同时消费原始 chunk 事件与 transcript item 事件
- `RunResult.Transcript` 与按 `Seq` 收集到的 `RunEventItem` 完全一致
- 超长单行不会因 helper 的扫描器限制被截断

### 9.3 边界合同

- shared helper 不再理解 provider 协议
- checkpoint 识别仍然只属于 adapter
- 不再通过 `Metadata["stderr"]` 或 `Output` 暴露原始 stderr/stdout

### 9.4 文档合同

- `AGENTS.md`
- 本文档
- roadmap
- examples
- tests

对输出合同的说法必须一致，不能同时存在两套语义
