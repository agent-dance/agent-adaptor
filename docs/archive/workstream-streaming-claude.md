# Workstream: Claude Code Streaming

> 状态：历史实施计划。Claude token-level streaming 与 Phase 3 PlanReview / Question HITL 已落地；当前 adapter 映射见 [`claude/README-streaming.md`](../../claude/README-streaming.md)，宿主用法见 [`streaming.md`](../streaming.md) 与 [`run-policy.md`](../run-policy.md)。

本文件是 `docs/workstream-streaming-chat.md` §12.1 的落地实施计划，按 `docs/streaming-adapter-contract.md` 的硬合同组织。目标是把 Claude adapter 从"批量 stream-json"升级到"token-level partial streaming"，不改动 core SDK、不改动 `pkg/bridges/*`。

依赖引入按 `AGENTS.md` §2.4（可靠性与可持续维护优先）三条原则评估；本期的候选库清单与评估结论见 §5。结论不是"禁止引入"，是"按三条评估后，本期候选库在局部化收益不显著，倾向手写"——未来 Claude 协议复杂度上升、或 anthropic-sdk-go 出现稳定的 types-only 子模块时应重新评估。

## 0. 先把结论写死

- 官方 `claude` CLI（`@anthropic-ai/claude-code`，Sonnet 4.5+ / claude-code 2.1+）原生具备完整 token-level streaming 能力；本机以 drop-in 等价封装代跑验证，协议一致
- **不换 transport**：继续走 `--print - --output-format stream-json --verbose`，加一个 `--include-partial-messages` 就开 token-level
- **依赖选型**：本期按 `AGENTS.md` §2.4 三条评估（见 §5），候选 `anthropic-sdk-go` 的"可局部化"和"可靠性收益"未显著超过手写 ~80 行结构体的方案，因此本期手写；结论不是硬性禁止，下次 Claude 协议变动时重评
- `Reasoning: true` 打开：`thinking_delta` 在 Sonnet 4.5+ / claude-code 2.1+ 已与 partial 共存，`claude/README-streaming.md` 原有的 `Reasoning: false` 信息过期
- Streaming parser **只额外 `EmitStream`**；`Transcript` / `Output` 仍由批量路径已有的 `assistant` 全量帧产出，避免两套真相
- 任何未在 §3.2 / §4.2 列明的 wrapper `type` 或 `event.type`（含下游封装的扩展帧）**一律 Raw 透传**，源码不加 switch、不语义化；扩展帧的解释权留给宿主
- 预算：**~830 行手写 / ~0.8 工作日**（Codex app-server 的 ~50%）

## 1. 目标

让宿主通过 `sdk.Start(ctx, prompt, agentadaptor.WithStreaming())` 对 Claude agent 拿到与 Codex 等价的 `StreamPayload` 流，满足：

- 至少 3 条 `StreamTextContent`；delta 顺序拼回的文本是 `DriverRunResult.Output` 的前缀/后缀
- `StreamRunFinished.Usage.InputTokens > 0`
- tool 调用路径产出 `StreamToolCallStart` / `StreamToolCallArgs` / `StreamToolCallEnd` / `StreamToolCallResult`
- extended thinking 打开时产出 `StreamReasoningStart / Content / End`
- streaming 与非 streaming 路径产出**等价**的 `DriverCheckpoint` 与 `Output`

## 2. 非目标

- HITL 审批回路（仍然 audit-only；本期沿用 `streaming-adapter-contract.md` §2.5 的 auto-deny）
- Bedrock 模式的端到端回归（理论等价，本期只做一条冒烟；`README-streaming.md` 明示未回归）
- `ClaudeConfig` 新字段（`ExtendedThinking`、`Streaming` 专属 config）——复用 `AgentDefaults.Streaming` 已有通路
- 任何对 core SDK / bridges 的改动
- 重写现有批量 parser；streaming 只增不减

## 3. 本机验证结论摘要

### 3.1 CLI 事实（2026-04-22）

- **源码只面向官方 `claude` CLI**（npm 包 `@anthropic-ai/claude-code`），不特化任何下游封装。任何下游封装可能出现的扩展帧（例如非官方 `system.*` 子类型、非官方 `result.*` 扩展字段）由 §4.2 的"未列明帧 Raw 透传"通道兜底，源码不 switch、不语义化
- 关键 flag：`--include-partial-messages` 是一级 flag，与 `--print` + `--output-format stream-json` + `--verbose` 正交组合
- `--resume <session_id>` / `--session-id <uuid>` 支持完整；session 语义与批量路径一致
- 登录态判据源码侧按官方 `claude` 的来源（`claude auth status` / `ANTHROPIC_API_KEY` / Bedrock & Vertex 凭据）识别；本机若用等价封装（见下条）做代跑，登录方式由本机自行解决，**不进源码**
- 本机验证工具：可用官方 `claude` 或其 drop-in 等价封装（如本机的 `trpc-claudecode`，stream-json 协议与官方 1:1 对齐）。已用本机工具实测一条完整 streaming 流（`"Hi!"` prompt，~2.7s，usage.output_tokens=5），frame 结构与 Anthropic Messages API `MessageStreamEvent` 镜像关系得到正交验证，足以佐证 §3.2 映射表基线正确

### 3.2 Frame 结构（dry-run + 官方 doc + 社区双源确认）

Wrapper 帧（`.type`）：

| type | 出现时机 | 处理 |
|---|---|---|
| `system.init` | 首帧 | 取 `session_id` / `model` / `cwd` |
| `system.api_retry` | 官方 retry | Raw 透传；连续 5xx 升级 `StreamRunError` |
| `stream_event` | 仅 `--include-partial-messages` 打开 | **核心**，见 §4.2 映射表 |
| `assistant` | 每个 message 完成后全量一次 | 现有批量路径处理；streaming 下不重复 EmitStream |
| `user` | tool_result 回填 | 映射 `StreamToolCallResult` |
| `result` | 终局 | `StreamRunFinished`（usage / total_cost_usd） |
| `error` | 致命 | `StreamRunError` |

> 未在上表列出的 `type`（包括下游封装引入的扩展帧）统一走 Raw 透传兜底，源码不加 switch、不做语义化；若后续确认是官方协议新增，**先补本表再写代码**。

`stream_event.event.type` 子类型（与 Anthropic Messages API `MessageStreamEvent` 字段 1:1 对齐）：

| event.type | event.delta.type | 备注 |
|---|---|---|
| `message_start` | — | `message.id` = MessageID |
| `content_block_start` | — | content_block.type ∈ {text, tool_use, thinking}；按 index 记账 |
| `content_block_delta` | `text_delta` | `.text` 非空字符串片段 |
| `content_block_delta` | `input_json_delta` | `.partial_json`，**非合法 JSON**，原样透传 |
| `content_block_delta` | `thinking_delta` | `.thinking` |
| `content_block_delta` | `signature_delta` | 累积到 Raw，不 emit |
| `content_block_stop` | — | 配对关闭 |
| `message_delta` | — | 增量 `stop_reason` / `usage.output_tokens` |
| `message_stop` | — | 终结 message lifecycle |

## 4. 实施方案

### 4.1 文件布局

```
claude/
├── driver.go                    # [改] req.Streaming 分派；StreamCapability
├── parser.go                    # [改] handlePayload 多 case "stream_event"
├── streaming_parser.go          # [新] stream_event → StreamPayload 的状态机
├── streaming_parser_test.go     # [新] fixture 驱动 round-trip
├── run_streaming_live_test.go   # [新] build tag claude_live 的端到端
├── README-streaming.md          # [改] Reasoning=true；记录 --include-partial-messages 语义
└── testdata/
    ├── streaming-happy.jsonl         # [新] haiku partial 流
    ├── streaming-tool-use.jsonl      # [新] 带 tool_use partial_json 流
    └── streaming-thinking.jsonl      # [新] 带 thinking_delta 流
```

**禁区与必要性**：

1. **不在 `internal/clihelper` 里识别 `stream_event`**（AGENTS.md §7.2）
   - 腐化路径：一旦 clihelper 开始认 Claude 的 `stream_event.event.delta.text_delta`，它就被迫也认 codex 的 `item/agentMessage/delta`、cursor 的 partial 帧；shared helper 退化为 `switch driver {...}` 的垃圾场，"shared"失去意义
   - 更深一层：识别 stream_event 等于判断"哪个 delta 是权威的、哪个 session_id 是正式 checkpoint"，这已经是 AGENTS.md §7.3 明文属于 adapter 的职责
   - 保护对象：`clihelper` 作为"只做进程 / pipe / chunk"的薄层能长期复用

2. **不在 `pkg/bridges/*` 里出现任何 Claude-specific 代码**（workstream-streaming-chat §7）
   - 腐化路径：Claude 的 `input_json_delta.partial_json` 是非标准 JSON 片段，最诱人的"优化"是让 bridges 帮宿主累积并 parse 成合法 JSON；这会让 bridges 同时背上 Claude / Codex / Cursor 三家的协议差异，变成另一个 switch 爆炸
   - 具体红线：bridges 只能读 `StreamPayload.Kind / Delta / Raw`，不得 `type assert` 任何 provider 结构体，不得根据 `StreamPayload.Name == "anthropic"` 走分支
   - 保护对象：`pkg/bridges/agui` 与 `pkg/bridges/sse` 对 adapter 类型盲；新增 driver 时 bridges 零改动

3. **引入外部库必须局部化到 `claude/` 包内**（AGENTS.md §2.4 第 3 条）
   - 腐化路径：若 `anthropic-sdk-go` 的类型出现在 `api.go` / `run_types.go` 的公共 API 签名里（比如 `RunResult.Raw anthropic.MessageStreamEvent`），整个 SDK 的对外语义就和 Anthropic 协议绑死；之后任何宿主都被迫 `import anthropic-sdk-go`，换 provider 时不能只换 adapter
   - 公共 API 的协议泄漏是 SDK 最难回退的腐化——一旦泄漏到 v1 签名，后续 major bump 才能清
   - 本条不否定引入依赖本身；§5.0 的评估若选中某个库，它的 import 必须严格止步于 `claude/` 子树（含 `claude/internal/*`），不出现在 `api.go` / `run_types.go` / `runner.go` / `options.go` / `pkg/bridges/*`
   - 保护对象：core SDK 保持 provider-agnostic，这是多 adapter 共存与未来替换的基石

三条合起来守护的是同一件事：**"core SDK 不认 provider、shared helper 不认协议、bridges 不认 driver"三层解耦不被本 workstream 破坏**。

### 4.2 `stream_event` → `StreamPayload` 完整映射

| Wrapper / event | StreamKind | 关键字段 | 备注 |
|---|---|---|---|
| `system.init` | `StreamRunStarted` | ThreadID=session_id | 仅首次 |
| `system.api_retry` | `""` (opaque) / `StreamRunError` | Raw=payload | 连续 ≥3 次 5xx 或 `willRetry:false` 时升级 Error |
| `stream_event` / `message_start` | (暂存) | — | 记下 message.id；text_start 延后到首个 `text_delta` |
| `stream_event` / `content_block_start` (text) | (暂存) | — | index → MessageID 表；不 emit |
| `stream_event` / `content_block_delta.text_delta` | `StreamTextContent` | MessageID, Delta=.text | 第一次到达时先补发 `StreamTextStart` |
| `stream_event` / `content_block_stop` (text) | `StreamTextEnd` | MessageID | 从 index 表取 MessageID 配对 |
| `stream_event` / `content_block_start` (tool_use) | `StreamToolCallStart` | ToolCallID=id, Name=name | 若 content_block.input 非空，作为 Args 附上 |
| `stream_event` / `content_block_delta.input_json_delta` | `StreamToolCallArgs` | ToolCallID, Delta=.partial_json | **不做 JSON 拼接**；字符串透传 |
| `stream_event` / `content_block_stop` (tool_use) | `StreamToolCallEnd` | ToolCallID | 配对关闭 |
| `stream_event` / `content_block_start` (thinking) | `StreamReasoningStart` | MessageID=thinking-{index} | 合成 id，message 仅一条 thinking 时 OK |
| `stream_event` / `content_block_delta.thinking_delta` | `StreamReasoningContent` | MessageID, Delta=.thinking | |
| `stream_event` / `content_block_delta.signature_delta` | — | Raw 累积 | 审计用，不 emit |
| `stream_event` / `content_block_stop` (thinking) | `StreamReasoningEnd` | MessageID | |
| `stream_event` / `message_delta` | (暂存) | — | 累加 usage.output_tokens / stop_reason |
| `stream_event` / `message_stop` | — | — | 不 emit；`assistant` 全量帧 / `result` 帧才是权威 |
| `assistant` (全量 message) | — | — | 批量路径已处理 `TranscriptItem`；streaming 不再 EmitStream，避免双发 |
| `user` (tool_result) | `StreamToolCallResult` | ToolCallID=tool_use_id, Result={text, is_error} | 保持批量路径的 `TranscriptToolResult` 同步 emit |
| `result` | `StreamRunFinished` | Usage, Raw={cost_usd, stop_reason, num_turns} | 终局，`state.done` 关闭 |
| `error` | `StreamRunError` | Error={Message, Code} | 终局 |

未覆盖字段一律 `StreamPayload.Raw` 透传；不吞。

#### 4.2.1 `input_json_delta` 字符串透传决策的硬理由

§4.2 表中 `input_json_delta → StreamToolCallArgs` 一行注明"**不做 JSON 拼接**；字符串透传"。这不是实现上的简化取舍，而是同时被**跨 provider 合同**、**SDK 分层**、**可靠性责任边界**锁死的选择。四条硬理由：

1. **跨 provider 合同只存在一种说得通的语义**（AGENTS.md §7 + §4.1 禁区 2）。`StreamToolCallArgs.Delta` 是跨 provider 语义位，Codex / Cursor / Claude 都往这里灌。唯一能跨 provider 成立的定义是"**本次收到的原始字符串片段，宿主顺序追加得到完整参数**"。若 Claude adapter 在 emit 时改塞累积后的 JSON 快照，bridges 就被迫根据 `StreamPayload.Name` 走 switch 才能正确处理——直接违反"bridges 不认 driver"的硬约束。字符串透传是唯一让跨 provider 合同不崩的选择。

2. **partial JSON 解析的正确性不是 adapter 能背的包**（AGENTS.md §2.4）。Anthropic 的 `partial_json` 单片不保证合法（可能切在 `"foo":"ba`、`\u00` 转义、key 冒号前），各 partial-json 库的 recovery 策略（补 `}` / 截断 / 报错）都不同。一旦 adapter 在 emit 时 parse，adapter 就要为 parse 结果的"对错"背锅——而对错没有客观标准；库升级会表现为"同一条 Anthropic 流、SDK 小版本升级后 `StreamPayload` 变了"，v1 API 稳定性破产。透传模式下 adapter 的合同退化成"字节级原样转发"，可以字面 diff 测试。

3. **宿主需求天然分裂，SDK 不能替所有人做决定**。AG-UI 协议的 `tool_call_args.delta` 本就是 partial JSON 片段（与 Anthropic 同构，透传 0 成本）；打字机 UI 要原始字节节奏；仅要终态的宿主等 `StreamToolCallEnd` 后从 `TranscriptToolCall.Args`（批量路径已落好的完整合法 JSON）取；审计/回放宿主要字节级原样重放。**片段是所有更高抽象的必要前驱**——能从片段拼出快照，不能从快照还原片段。SDK 在最底层出片段，信息保全最大、能力损失最小。

4. **不制造两套真相**（AGENTS.md §3.4）。完整合法的 tool input 已经在 `assistant` 全量帧里被批量路径落进 `TranscriptToolCall.Args`；streaming 路径再 emit 累积 JSON 快照会让宿主拿到两份"完整参数"，且可能因 partial-json 库的补全策略导致字节不等，直接违反 `Transcript / Output / Summary / Result` 分层合同。透传模式下，streaming 出"片段"、批量出"完整"，两条信息互补、不重叠、不竞争。

**实施硬约束**（对应 §4.3 状态机）：

- `streaming_parser.go` 处理 `input_json_delta` 只做 `sink.EmitStream(StreamPayload{Kind: StreamToolCallArgs, ToolCallID: …, Delta: string(event.Delta.PartialJson)})`
- 禁止出现 `toolCallArgsBuf map[string]*bytes.Buffer` 这类累积结构
- 禁止对 `PartialJson` 调用 `json.Unmarshal` 或引入 partial-json 库
- 禁止在 `content_block_stop` 时补 emit 一条"完整参数快照"；end 事件只携带 `ToolCallID`
- 禁止任何 `// TODO: accumulate and parse` 注释——这是方向错误的坑，不是待办

### 4.3 `streaming_parser.go` 数据结构

```go
type streamingState struct {
    sink       agentadaptor.EventSink
    runID      string
    sessionID  string

    mu sync.Mutex

    // message-level
    messageID    string          // 当前 message.id
    textStarted  map[int]bool    // index → 是否已发 text.start
    blockKind    map[int]string  // index → "text" | "tool_use" | "thinking"
    toolCallID   map[int]string  // index → tool_use.id
    toolName     map[int]string  // index → tool_use.name
    thinkingID   map[int]string  // index → thinking stream MessageID
    signatures   map[int]string  // index → 累积的 signature（审计）

    // lifecycle
    runStarted   bool
    usage        *agentadaptor.Usage
    stopReason   string
    apiRetryHits int
}
```

所有方法：
- `handleSystemInit(payload)` → `StreamRunStarted`
- `handleStreamEvent(event)` → 分派到子 handler
- `handleUserToolResult(block)` → `StreamToolCallResult`
- `handleResult(payload)` → `StreamRunFinished`
- `handleError(payload)` → `StreamRunError`
- `finalize()` → 补齐未关闭的 text/tool/reasoning lifecycle，发 `StreamRunFinished` 兜底

### 4.4 `driver.go` 改动

```go
func (adapter) StreamCapability() agentadaptor.StreamCapability {
    return agentadaptor.StreamCapability{
        Native:       true,
        TokenLevel:   true,
        Reasoning:    true,  // 修正 README-streaming 的过期信息
        ToolCallArgs: true,  // partial_json 片段，以字符串透传
        HITL:         false,
    }
}

// 在 Run() 内：
args := []string{"--print", "-", "--output-format", "stream-json", "--verbose"}
if req.Streaming {
    args = append(args, "--include-partial-messages")
}
// ... 其余 args 不变

parser := newClaudeParser(sink)
if req.Streaming {
    parser.enableStreaming(req.RunID)
}
```

`parser.enableStreaming(runID)` 在 parser 内部挂上 `streamingState`；非 streaming 路径 `streamingState == nil`，`handlePayload` 里的 `case "stream_event"` 直接忽略该帧（不会出现，因为没传 flag）。

### 4.5 Checkpoint 等价性

`streamingState` 不参与 checkpoint 构造。`parser.checkpoint(exitCode)` 继续用 `sessionID` + `exitCode` + `session_codec` 三件套：

- streaming 下 `sessionID` 来自 `system.init` + 每条 `stream_event.session_id` + `result.session_id`（三源冗余取最后一个非空）
- `SessionParamCWD` / `SessionParamWorkspaceID` / `SessionParamPromptBundleKey` 在 `driver.go` 内统一 stamp，与 streaming 解耦

streaming / 批量产出的 `DriverCheckpoint.State.Data` 严格等价。

### 4.6 Output / Transcript 等价性

- `assistant` 全量帧在 streaming 下**依然会发**（实测确认：claude-code 在 message_stop 之后还会发一条 `type:"assistant"` 承载完整 content）
- 因此 `buildOutput()` 仍从 `assistantText[]` 拼；streaming 通道不污染
- 若某次 run 被 ctx cancel，`assistant` 帧可能缺失：
  - 兜底：parser 内额外维护 `deltaBuffer[messageID] → strings.Builder`
  - `buildOutput()` 在 `assistantText == nil && deltaBuffer != nil` 时使用 delta 累积作为 Output，打 metadata `{"output_source":"reconstructed_from_deltas"}`
  - 这是**降级**，不是主路径

## 5. 实施阶段

### 5.0 依赖选型评估

按 `AGENTS.md` §2.4 三条原则，对可能引入的外部库逐个打分。评估结论必须落到这里，未来升级或重评估时直接改本表。

| 候选库 | 用途 | 可靠性 | 可持续维护 | 可局部化 | 结论 |
|---|---|---|---|---|---|
| `github.com/anthropics/anthropic-sdk-go` | 复用 `MessageStreamEvent` / `ContentBlockDeltaEvent` / `TextDelta` / `InputJSONDelta` / `ThinkingDelta` 结构体 + `Message.Accumulate` 累积器 | 🟢 高：官方维护，与 Anthropic API 1:1 对齐，协议升级自动跟随 | 🟢 高：Anthropic 官方 Go SDK，版本节奏稳定 | 🟡 **中**：SDK 本体是"HTTP client + types + options + retry + auth"的一体化包，没有独立的 types-only 子模块；import 会把 HTTP client / google/uuid / tidwall/* 一起拖进 `go.sum` | ⚠️ **本期不引入**：可靠性/可维护性收益成立，但可局部化不占优（会让 `claude/` 子树依赖一个完整的 HTTP SDK 只为用几个类型），且手写 ~80 行结构体即可覆盖本期消费的 6 种 event + 3 种 delta。下次 Claude 协议字段增加或 Anthropic 拆出 types-only 子包时重评 |
| `github.com/tidwall/partial-json-go` / `gjson` | 累积并解析 `input_json_delta.partial_json` 片段 | 🟢 高 | 🟢 高 | 🔴 **低**：应由宿主决定用哪个 partial-JSON 库，SDK 层不该替宿主选。`StreamToolCallArgs.Delta` 的协议合同就是"原始字符串片段" | ❌ **不引入**：违反可局部化（会把 partial-JSON 语义推到 `pkg/bridges/*`，让 bridges 认 driver） |
| stdlib `encoding/json` + `bufio.Scanner` | NDJSON 逐行解析；手写 6 个 event + 3 个 delta 结构体 | 🟢 高 | 🟢 高 | 🟢 高：零新增 | ✅ **采用**：与 codex appserver 风格一致，审计面最小 |

评估准则（照搬 AGENTS.md §2.4）：

- 三条都占优或显著占优 → 优先采用
- 三条与手写接近 → 倾向手写
- 任意一条明显劣势 → 本期拒绝，文档里写清楚，下次重评

本期结论：**零新增顶层 `require`**，但这是评估结果而非立项前提。若本节未来某次升级改为采用 `anthropic-sdk-go`，`go.mod` `require` 段里新增条目不会违反任何硬约束——只需同时更新本节与 §8 验收清单。

### Phase A：streaming parser（核心）

| # | 产出 | 行数 |
|---|---|---|
| A1 | `claude/streaming_parser.go` — `streamingState` + 所有 handler | ~260 |
| A2 | `claude/parser.go` — 增加 `enableStreaming(runID)` / `case "stream_event"` 分派 + user tool_result 额外 EmitStream | ~60 |
| A3 | `claude/testdata/streaming-*.jsonl` — 三个 fixture | ~150 |
| A4 | `claude/streaming_parser_test.go` — table-driven + round-trip | ~180 |
| 小计 | | **~650** |

验收：
- `go test ./claude/... -short` 全绿（包括未开 streaming 的回归）
- fixture 回放时 delta 顺序拼回的文本与 `assistant` 全量帧字节级一致
- `StreamToolCallArgs.Delta` 累积起来与 `assistant` 里 `tool_use.input` 的 `json.Marshal` 等价（允许 whitespace 差异）

### Phase B：driver 分派

| # | 产出 | 行数 |
|---|---|---|
| B1 | `claude/driver.go` — `StreamCapability()` 实现 | ~15 |
| B2 | `claude/driver.go` — `req.Streaming` 分派 args + parser 开关 | ~15 |
| B3 | `claude/driver_test.go` — 断言 streaming=true 时 args 包含 `--include-partial-messages` | ~30 |
| 小计 | | **~60** |

验收：
- `StreamCapability()` 返回值满足 §4.4
- `req.Streaming=false` 时 CLI args 不变（回归保护）

### Phase C：端到端 live test

| # | 产出 | 行数 |
|---|---|---|
| C1 | `claude/run_streaming_live_test.go`（build tag `claude_live`） | ~120 |
| 小计 | | **~120** |

用例：
- `TestClaudeStreamingHaiku`（haiku prompt）：
  - ≥ 3 条 `StreamTextContent`
  - `StreamRunFinished.Usage.InputTokens > 0`
  - delta 顺序拼回文本 == `result.Output`
- `TestClaudeStreamingToolUse`（让 Claude 写一个小文件）：
  - 至少一对 `StreamToolCallStart` / `StreamToolCallEnd`
  - `StreamToolCallArgs.Delta` 累积后解析为合法 JSON，等价于 `assistant` 帧的 `tool_use.input`
  - `StreamToolCallResult` 出现，`Result.is_error == false`
- `TestClaudeStreamingResume`：
  - 第一次 run 拿到 `sessionID`
  - 第二次 run 用 `WithSession(SessionKey("claude-live", sessionID))` 可复活，`StreamRunStarted.ThreadID == sessionID`

前置条件：本机 `claude` CLI（或 drop-in 等价封装）可调用并已登录；运行时脚本 `make claude-live` 调用 `claude/auth_probe.go` 检查登录态才放行。

### Phase D：文档

| # | 产出 |
|---|---|
| D1 | 更新 `claude/README-streaming.md`：Reasoning=true；补 `--include-partial-messages` 语义与映射表脚注 |
| D2 | 更新 `docs/streaming.md`：支持矩阵 claude 从 🚧 → ✅ |
| D3 | 更新 `docs/workstream-streaming-chat.md` §12.1：标注已完成，指向本文档 |

### 总计

| Phase | 手写代码 | 工作日 |
|---|---|---|
| A. parser + fixture | 650 | 0.5 |
| B. driver 分派 | 60 | 0.1 |
| C. live test | 120 | 0.1 |
| D. 文档 | — | 0.1 |
| **合计** | **~830** | **~0.8** |

比原先估算（640 行）略高，是因为加上了 fixture 与 driver_test，这两个应当上 CI。核心逻辑代码仍然只有 ~320 行。

## 6. 风险与应对

| 风险 | 触发条件 | 应对 |
|---|---|---|
| 本机验证工具（官方 `claude` 或 drop-in 封装）对 stream_event 有字段差异 | 版本漂移 / 下游 fork | 首次 live test 先 dump 一整条 haiku 到 `testdata/streaming-happy.jsonl`，**以官方协议为准**；未识别字段一律 Raw 透传 |
| `content_block_start` 带预填 input | Claude 偶尔会在 tool_use 启动时就带完整 input | Start 事件 `Args` 字段填上 content_block.input；后续 `input_json_delta` 追加即可 |
| `input_json_delta.partial_json` 不是合法 JSON | Anthropic 协议设计如此 | 协议合同：`StreamToolCallArgs.Delta` 是原始字符串片段，宿主需自行用 partial-json 库累积解析。文档 `streaming-adapter-contract.md` §4.3 补一条脚注 |
| `message_delta.usage` 只给 output_tokens | Anthropic 协议：input_tokens 在 message_start 就给全 | 同步累加；最终以 `result.usage` 为准，message_delta 只兜底 |
| ctx cancel 时 assistant 帧缺失 | 用户主动 Cancel | §4.6 的降级路径（deltaBuffer 兜底） |
| Bedrock 模式 streaming 未回归 | 本期不测 | README-streaming 明示，issue 追踪 |
| Extended thinking + 极长 reasoning 超出 stream buffer | 单 run 数千条 delta | 默认 `BackpressureDropStream` + `StreamDropped` marker 兜住；严格宿主自行 `WithEventBuffer(_, 8192, BackpressureBlock)` |
| 官方 `claude` CLI 协议升级 | 版本升级 | live test 每次升级 CLI 必跑；fixture diff 会暴露协议改动 |
| parser 同时跑 streaming + batch 产出不一致 Output | 实现 bug | 测试：同一 fixture 在 `req.Streaming=true` 与 `false` 两路下 `DriverRunResult.Output` 必须字节一致 |

## 7. 与既有硬合同的对照

| 合同 | 遵守证据 |
|---|---|
| AGENTS.md §2.1 只有一套执行语义 | streaming 与 batch 共用同一 `Run()` 入口；parser 唯一，streaming 仅增量 EmitStream |
| AGENTS.md §3.4 Output 不接原始 stdout | `Output` 从 `assistantText` 拼；降级才用 delta |
| AGENTS.md §7.2 clihelper 不识别协议 | 所有 `stream_event` 解析在 `claude/streaming_parser.go` 内 |
| AGENTS.md §7.3 checkpoint 由 adapter 自解析 | `parser.checkpoint` 沿用批量路径；sessionID 来自顶层字段，不递归扫描 |
| AGENTS.md §2.4 可靠性与可持续维护优先 | §5.0 已按三条原则评估候选库；本期评估结论为手写，结论与理由留档可追溯 |
| AGENTS.md §10 streaming 第二通道 | 只调 `sink.EmitStream`；`RunEvent` 通道不变 |
| streaming-adapter-contract §1.1 StreamAwareDriver | §4.4 实现 |
| streaming-adapter-contract §1.2 req.Streaming 分派 | §4.4 实现 |
| streaming-adapter-contract §2.1 goroutine 生命周期 | parser 同步运行在 `clihelper.Run` 的 `Observe` 回调里，无额外 goroutine |
| streaming-adapter-contract §2.2 事件顺序 | 同一 messageID / toolCallID 的 start / content / end 严格按协议顺序 |
| streaming-adapter-contract §2.3 Session 等价 | §4.5 明示 |
| streaming-adapter-contract §2.4 不污染 RunEvent | 结构化事件只走 EmitStream |
| streaming-adapter-contract §2.5 HITL v1 auto-deny | Claude CLI 的 permission 已被 `--dangerously-skip-permissions` 绕过；若遇到 `type:"permission_request"` 帧走 `StreamHITLRequested` audit-only |

## 8. 验收清单

合并前必须全部满足：

- [x] `go test ./...` 默认路径 100% 绿（streaming 未开时所有既有用例不回归）
- [x] `go test ./claude/... -short` 覆盖 streaming fixture 的 round-trip
- [ ] `go test -tags=claude_live ./claude/...` 本机通过（需要本机 `claude` CLI 已登录）
- [x] streaming 与 batch 对同一 fixture 产出的 `DriverRunResult.Output` 字节一致
- [x] `claude/README-streaming.md` 已更新 `Reasoning: true` 并补 `--include-partial-messages` 语义说明
- [x] `docs/streaming.md` 支持矩阵 claude 标记为 ✅
- [x] `docs/workstream-streaming-chat.md` §12.1 标注"已完成，参见 workstream-streaming-claude.md"
- [x] `go.mod` `require` 段的变化与 §5.0 "依赖选型评估"结论一致（本期评估结论为手写，因此无新增；若实施中改为采用候选库，§5.0 与本项同步更新即可）

## 9. 开放议题（下期跟进）

- Bedrock 模式 streaming 端到端回归（AWS SigV4 + Anthropic beta header 组合）
- `input_json_delta` 的 bridges 侧 partial-JSON 累积（是否在 `pkg/bridges/agui` 提供可选 helper？需要评估是否打破 "bridges 不认 driver" 的硬约束）
- 多 turn 对话下 `--input-format stream-json` 的可行性（当前方案每 run 起一个 CLI 进程，TTFB 有 300-1200ms 的壳层开销；若要做长连接 chat，需要评估 stream-json 双向输入模式）
- `claude/auth_probe.go` 的登录态判据按官方 `claude` 多来源补全：当前仅看 `claude auth status` 的 `loggedIn` 字段，对纯环境变量登录（`ANTHROPIC_API_KEY`、`ANTHROPIC_AUTH_TOKEN`、Bedrock/Vertex 凭据等）会误判。判据建议：`auth status.loggedIn==true` 或 `ANTHROPIC_API_KEY`/`ANTHROPIC_AUTH_TOKEN` 非空或 Bedrock/Vertex 凭据命中任一时视为已登录。**源码不识别任何下游封装的专属变量名**——由下游封装在自己的场景里把自己的变量映射到官方变量（这是 drop-in 封装的职责）

## 10. 本机前置动作清单（动工前的人工步骤）

> 源码面向官方 `claude` CLI；本机验证可用官方 `claude` 或其 drop-in 等价封装（如 `trpc-claudecode`）。本机登录方式与变量名由本机自行解决，**不进源码、不进 fixture**。
>
> 下文命令以 `$CLAUDE_BIN` 代指"本机用来跑的可执行文件"。

1. 确认本机 `$CLAUDE_BIN` 可调用且已登录。登录方式自行负责（官方 `claude auth login` 或任意等价环境变量）。
2. 冒烟验证：
   ```
   echo "Say 'hi' in one word." | \
     $CLAUDE_BIN --print - --output-format stream-json --verbose \
       --include-partial-messages --dangerously-skip-permissions
   ```
   期望：`result.is_error == false`、至少 1 条 `stream_event.content_block_delta.text_delta`。
3. 正式 fixture 采集：
   ```
   cd /tmp/claude-streaming-probe
   echo "Write a haiku about autumn. Keep it under 30 words." | \
     $CLAUDE_BIN --print - --output-format stream-json --verbose \
       --include-partial-messages --dangerously-skip-permissions \
       > claude-haiku-streaming.jsonl
   ```
4. 把 `claude-haiku-streaming.jsonl` 作为 fixture 种子，裁剪/脱敏后落到 `claude/testdata/streaming-happy.jsonl`；**必须脱敏**：
   - 剔除 uuid、session_id、真实 cost、真实 model 路由明细
   - 剔除任何下游封装的扩展帧（`system.status` / `result.modelUsage` 等未在 §3.2 / §4.2 列明的字段），只保留官方 stream-json 基线帧
5. 确认 fixture 里至少出现：`system.init`、`message_start`、≥3 条 `content_block_delta.text_delta`、`content_block_stop`、`message_delta`、`message_stop`、`assistant` 全量帧、`result`
6. 若 fixture 里出现任何映射表未覆盖的 wrapper type 或 event.type：默认走 §3.2 末的"Raw 透传兜底"路径；只有在**官方文档确认该字段是原生 `claude` CLI 协议成员**时，才补进本文档 §4.2 并写入 switch case。下游封装的扩展帧一律不写进源码 switch。
