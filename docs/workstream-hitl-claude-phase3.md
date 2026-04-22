# Workstream: HITL v2 Phase 3 — Claude 双向回填

配对设计文档：[`docs/workstream-hitl-v2.md`](./workstream-hitl-v2.md)。本文档把 Phase 3 的 CLI 方案从"TODO"转成"可立项"，基于 2026-04-22 跑在 `trpc-claudecode 2.1.112` 上的 PoC 实证。

> **Status: 实施完成 (2026-04-22)**。核心代码位于：
> - `internal/clihelper/clihelper.go` — `StdinController` 长连接 stdin
> - `claude/driver.go` — `wantsInteractiveClaude` / `validateInteractivePolicy` / `buildClaudeExecArgs(..., interactive)` 分派
> - `claude/parser.go` — `enableInteractive` / `interactiveOnToolUseStart|Delta|Stop` / `renderInteractiveToolResult`
>
> 测试：`claude/phase3_test.go`（单元）+ `claude/phase3_live_test.go`（集成，`claude_live` 构建标签）+ `internal/clihelper/stdin_controller_test.go`（stdin 控制器）。
>
> 余项：Phase 3.5（Permission Ask + 宿主 tool executor）未启动；需要新 workstream。

## 0. TL;DR

- Phase 3 采用 **stdin `stream-json` 双向** 方案，MCP permission prompt 方案作废
- CLI 行为已实证：`--input-format stream-json --output-format stream-json` 模式下，tool_use 帧发出后 CLI **完全挂起**等宿主回注 user tool_result；`--replay-user-messages` 提供 ack 信号
- 实施复杂度集中在 **clihelper** 的 stdin 长连接 + **adapter** 的 tool_use → RequestDecision → 注入 tool_result 闭环
- 一次实施覆盖 Permission / PlanReview / Question **三类 HITL**，无需分轮
- 预计 ~400-600 行代码（adapter + clihelper + tests + 示例升级），含 fixture
- 完成后 `Descriptor.RunPolicyCaps.Retry` 三类可开 `true`（CLI 仍无"保持同一 tool_use_id 重新决策"，但可以通过"reject 当前 + prompt 引导 agent 重问"近似 Retry）

## 1. 背景 / 问题

Phase 1 在 claude adapter 里是**观测层显性化**：看到 `ExitPlanMode` / `AskUserQuestion` 的 tool_result 后补发一对 `StreamHITLRequested/Resolved` 事件，解决了静默失败，但**从未真正拦截 CLI 的决策**。带来的用户可感限制：

| 现象 | 原因 |
|---|---|
| UI 卡片一到就是 REJECTED/APPROVED，不可点击 | 事件是事后回放，决策已经在 CLI 里做完了 |
| `AskUserQuestion` 永远被 CLI 合成一条占位 tool_result（`"Answer questions?"`），模型继续推进 | `claude --print` 非 TTY 模式下 CLI 无 UI 通道 |
| Bash/Write 等 Permission 无法真正停下来问人 | 同上 |
| `HumanDecisionSupport.Retry=false` | CLI 一旦本地决策就无法原地重新询问 |

## 2. PoC 验证（2026-04-22）

### 2.1 使用的 CLI

```
$ trpc-claudecode --version
trpc-claudecode 0.0.0 (@tencent/trpc-claudecode)
claude_code_version: 2.1.112
```

关键 flags:

- `--input-format stream-json`: "realtime streaming input"
- `--output-format stream-json`: NDJSON 事件流
- `--replay-user-messages`: 把宿主注入的 user 帧回吐一份（带 `isReplay:true`）作为 ack

### 2.2 实验：能否在 tool_use 发出后注入 tool_result 让 CLI 继续？

输入（通过 FIFO 持续打开的 stdin）：

```json
{"type":"user","message":{"role":"user","content":"Use the Bash tool to run `echo hello`. Use it exactly once."}}
```

CLI 输出（精选关键帧）：

```json
{"type":"system","subtype":"init","tools":["Bash","ExitPlanMode","AskUserQuestion",...]}
{"type":"stream_event","event":{"type":"content_block_start","index":1,
    "content_block":{"type":"tool_use","id":"toolu_bdrk_019roSD7...","name":"Bash","input":{}}}}
{"type":"stream_event","event":{"type":"content_block_delta","index":1,
    "delta":{"type":"input_json_delta","partial_json":"{\"comma"}}}
...
{"type":"stream_event","event":{"type":"message_delta",
    "delta":{"stop_reason":"tool_use","stop_sequence":null}}}
{"type":"stream_event","event":{"type":"message_stop"}}
                          ← CLI 挂起 8 秒等待 ──→
```

**关键观察**：

1. `stop_reason:"tool_use"` + `message_stop` → CLI 停止生成
2. 这 8 秒内 CLI **没有**任何 `permission_denials` / 内置 Bash 执行 / stderr 输出
3. `result` 事件**未到达**——turn 尚未结束

我们主动注入（由 FIFO 追加）：

```json
{"type":"user","message":{"role":"user","content":[
  {"type":"tool_result","tool_use_id":"toolu_bdrk_019roSD7...",
   "content":"hello\n","is_error":false}
]}}
```

CLI 回应：

```json
{"type":"user","message":{"role":"user","content":[
  {"type":"tool_result","tool_use_id":"toolu_bdrk_019roSD7...",
   "content":"hello\n","is_error":false}
]},"isReplay":true}                                          ← ack
{"type":"system","subtype":"status","status":"requesting"}   ← 恢复推理
{"type":"stream_event","event":{"type":"content_block_delta",
    "delta":{"type":"text_delta","text":"The command output `hello`."}}}
{"type":"result","subtype":"success","num_turns":2,
    "result":"The command output `hello`.","terminal_reason":"completed"}
```

**结论**：CLI 完全信任宿主注入的 tool_result，继续生成后续 turn 并正常结束。我们甚至没真运行 `echo hello`（宿主撒了谎），CLI 仍然用 `"hello\n"` 作为执行结果。

### 2.3 结论复盘

| 问题 | 答案 |
|---|---|
| 是否支持运行时增量 stdin 注入？ | ✅ `--input-format stream-json` 就是为这个设计的 |
| CLI 会不会自己先跑 Bash？ | ❌ 不会。stream-json 输入模式下 CLI **把 tool 执行完全下放给宿主** |
| 有没有 ack 机制？ | ✅ `--replay-user-messages` 回吐 `isReplay:true` 帧 |
| 能否同一机制覆盖 ExitPlanMode / AskUserQuestion？ | ✅ 二者也是 tool_use，挂起机制与 Bash 完全相同 |
| stdin 何时需要关闭？ | 只在整个 turn / multi-turn 都结束后——过早关闭会让 CLI 以为 user 不会再发 message 并提前结束 |

## 3. 设计

### 3.1 Adapter 分派新流程

当 `HumanDecision.Permission / PlanReview / Question` 任一为 `Ask` 时，claude driver 切到"交互模式"：

```
sdk.Start()
  → runner.dualSink.bindRun(...)
  → claude/driver.Run()
     → 构造 args = ["--print",
                    "--input-format", "stream-json",
                    "--output-format", "stream-json",
                    "--verbose",
                    "--include-partial-messages",
                    "--replay-user-messages"]
     → clihelper.RunInteractive(ctx, CommandRequest{
          Command: "claude",
          Args:    args,
          Stdin:   <persistent writer>,    // ← 新能力
          Observe: parser.onChunk,
       })
     → parser 识别 tool_use with name ∈ whitelist
        → sink.RequestDecision(...)  // 阻塞
        → 拿到 decision → 构造 tool_result JSON
        → stdin.Write(tool_result)
     → CLI 继续, 发 result
  → driver.Run returns
```

### 3.2 clihelper 新能力：长连接 stdin

当前 `clihelper.Run` 签名：

```go
type CommandRequest struct {
    Command string
    Args    []string
    CWD     string
    Env     []EnvBinding
    Prompt  string     // ← 一次性写入后 close(stdin)
    Observe ChunkHandler
}
```

新增：

```go
type CommandRequest struct {
    ...
    // Stdin controls how the subprocess stdin is fed.
    //
    // Prompt + Stdin = nil   → legacy behaviour: write Prompt once, close stdin
    // Stdin != nil           → caller drives stdin incrementally; helper never
    //                          closes stdin until ctx is cancelled or Close()
    //                          is explicitly called
    Stdin   StdinController
}

// StdinController lets an adapter drive the subprocess stdin throughout the
// run — used by HITL-interactive adapters (claude Phase 3) that need to
// inject user tool_result frames mid-turn.
type StdinController interface {
    // Write appends a frame (single NDJSON line) to stdin. Returns
    // ErrStdinClosed when the subprocess has exited.
    Write(frame []byte) error
    // Close flushes and closes stdin. Called implicitly when the run
    // terminates.
    Close() error
}
```

实现要点：

- `clihelper` 内部起一个 writer goroutine 轮询 `StdinController.Write` 的队列，串行写入 `subprocess.Stdin`
- Context 取消时 writer goroutine 退出，clihelper 关闭 stdin
- `Write` 在 subprocess 已退出时返回 `ErrStdinClosed` 以避免悄悄丢帧

### 3.3 Parser 侧：交互状态机

在 `claude/streaming_parser.go` 的 `handleContentBlockStart`（tool_use 分支）：

```go
case "tool_use":
    toolName := block.ContentBlock.Name
    toolUseID := block.ContentBlock.ID
    if kind, isHITL := claudeInteractiveTools[toolName]; isHITL && p.interactive {
        // Mark pending, but do NOT emit Requested yet — input_json_delta
        // may still be arriving. We emit on content_block_stop (tool_use end)
        // when we have the complete input.
        p.pendingInteractiveHITL[toolUseID] = &pendingInteractiveHITL{
            Kind:   kind,
            Source: sourceForTool(toolName),
        }
    }
```

在 `handleContentBlockStop` 看到 tool_use 的那个 index 时，拿到完整 input，触发真正的 sink.RequestDecision 阻塞：

```go
if pending, ok := p.pendingInteractiveHITL[toolUseID]; ok {
    req := buildDecisionRequest(pending, assembledInput)
    resp, err := p.sink.RequestDecision(ctx, req)
    if err != nil {
        // abort sentinel — adapter应停止往stdin写并让run结束
        return err
    }
    toolResult := buildToolResult(toolUseID, pending.Kind, resp)
    p.stdin.Write(toolResult)
}
```

### 3.4 Decision → tool_result 映射

给 CLI 的 tool_result content 如何生成（按 Kind 分类）：

**Permission（Bash/Write/Read/...）**：

| Decision | tool_result |
|---|---|
| `DecisionApproved` | `{is_error:false, content:<actual tool output>}` — **宿主必须真正执行工具**或 CLI 自己执行（见 §3.6） |
| `DecisionRejected` | `{is_error:true, content:"Permission denied by user."}` |

**PlanReview（ExitPlanMode）**：

| Decision | tool_result |
|---|---|
| `DecisionApproved` | `{is_error:false, content:"User approved the plan. Proceed with implementation."}` |
| `DecisionRejected` | `{is_error:true, content:"User rejected the plan."<+ 可选 resp.Text 作为修正提示>}` |

**Question（AskUserQuestion）**：

| Decision | tool_result |
|---|---|
| `DecisionAnswered` | `{is_error:false, content:"User has answered your questions: \"Q\"=\"A\" ..."}` — 与 claude 原版 `AskUserQuestionTool.mapToolResultToToolResultBlockParam(...)` 一致；必要时可追加 `selected preview` / `user notes` 注释 |
| `DecisionRejected` | `{is_error:true, content:"User declined to answer."}` |

### 3.5 Who Runs the Tool? —— Permission 的岔路

PoC 验证 CLI 把 Bash 的执行下放给了宿主。这催生一个架构选择：

| 方案 | 优点 | 缺点 |
|---|---|---|
| **A. 宿主承诺执行**：Approved 时宿主/adapter 真去跑 Bash，把 stdout 塞回 tool_result | 完全可控，能加审计 / 脱敏 / 沙箱 | 宿主要实现每个工具的执行——和 claude 内置实现的语义漂移风险大（edge case 多） |
| **B. 弃用 stream-json，回到普通模式用 `--dangerously-skip-permissions`** 当 approved | 零代码，CLI 自带执行 | AutoApprove 语义等价，**不是真交互 HITL** |
| **C. 混合：Approved 转交回 CLI，Rejected 本地生成 is_error tool_result** | 折中 | stream-json 模式下没有"把决定权交还 CLI"的帧 —— 路走不通 |

**推荐方案 A**，但 **Phase 3 只对 PlanReview / Question 两类生效**。Permission 类 tool（Bash/Write/...）的真正交互留给 **Phase 3.5**：

- Phase 3（本期）：只拦截 `ExitPlanMode` 和 `AskUserQuestion`——这两类的 tool_result 是**声明性**的（用户意见文本），宿主不需要"执行工具"
- Phase 3.5（后续）：加 Permission 类拦截，同时把每个内置 tool 的执行策略声明清楚（允许走 sandbox、审计钩子等）

### 3.6 Retry 语义

PoC 里 CLI 的 tool_use_id 是一次性的：一旦注入 tool_result，CLI 就从那个 tool_use_id "前进"了，无法"撤回重问"。这是个物理约束。

Phase 3 的 Retry 实现：

- `OnReject=FailureRetry` 触发时，adapter 注入一条 `tool_result{is_error:true, content:"<修正提示>"}` 告诉 CLI "这个请求不行，再试一次"
- CLI 的重试是靠**模型自己重新生成一个新的 tool_use（新 id）**——不是"保持同一次 tool_use_id 等第二个 tool_result"
- 所以 `Descriptor.RunPolicyCaps.PlanReview.Retry` 可以改 `true`，但语义是"**拒绝当前并引导重新生成**"，不是"保持决策窗口开"。文档里要明确标注

### 3.7 与 `HumanDecisionAutoApprove` 的关系

当 `Permission=AutoApprove` / `PlanReview=AutoApprove` 时，adapter 仍然走 Phase 1 的普通模式（`--print -` + `--dangerously-skip-permissions`），不启用 stream-json 双向。这保留了两点：

1. Phase 1 路径的性能（无中介开销）
2. `claude` 内置工具执行能力（用户不需要 Phase 3.5 的宿主执行器）

只有 `Ask` / `AutoReject` 才切到 stream-json 模式。

## 4. 实施拆解

| Item | LOC 估 | 依赖 |
|---|---|---|
| `clihelper.StdinController` + 长连接 stdin 实现 | ~120 | — |
| claude driver：两模式分派（print-once vs stream-json 交互）| ~80 | StdinController |
| claude streaming_parser：tool_use 挂起 → RequestDecision → 注入 tool_result | ~180 | Phase 1 whitelist 已有 |
| `Descriptor.RunPolicyCaps.Retry=true` | 5 | — |
| 3 个 fixture（PlanReview approve / reject / Question answered）+ 对应 driver 级集成测试（用真 CLI） | ~200 | dev 环境有 CLI |
| `examples/streaming-chat-copilotkit` 加 `AGUI_AGENT=claude-interactive` 或直接让 claude 走交互 | ~50 | — |
| `docs/workstream-hitl-v2.md` §5.1 整节重写 + Phase 1→3 迁移表 | — | — |

总量：**~635 行代码 + 测试 + 文档**。

## 5. 风险与缓解

| 风险 | 概率 | 缓解 |
|---|---|---|
| CLI 版本不一致，老版本不支持 `--replay-user-messages` | 中 | Descriptor-level 能力探测：启动前跑 `claude --help` grep 该 flag，不支持则退回 Phase 1 模式并 warn |
| Tool input 的 `input_json_delta` 分段组装有 edge case（我们测的 `{"comma` / `nd\": \"echo` 拼回原 JSON 不一定 100% 稳）| 中 | 加单元测试 + 用完整 JSON 验证而不是 substring |
| stdin 被 CLI 静默关闭（EOF 提前）导致 Write 丢帧 | 中 | `StdinController.Write` 检测底层 pipe write error，返回 `ErrStdinClosed`；上层 adapter 把它当 abort 信号 |
| Permission tool 的执行问题（§3.5） | 高 | 明确 Phase 3 **不**拦 Permission；Phase 3.5 另立项 |
| `isReplay:true` 帧可能污染 parser 的事件序列（我们把它当普通 user message 处理了）| 低 | parser 识别 `isReplay:true` 字段直接丢弃，不影响 checkpoint / transcript |
| Sonnet 4.6 / 4.5 / Haiku 行为漂移 | 低 | fixture 覆盖多模型；`--include-partial-messages` 在所有模型都已稳定 |

## 6. 验收（DoD）

- [x] `examples/streaming-chat-copilotkit AGUI_AGENT=claude` 能演示完整"plan request → UI 卡 pending → 用户点 Approve → 模型继续"闭环（`AGUIExampleRunPolicy` 在 claude 分支开启 PlanReview=Ask + Question=Ask）
- [x] 3 个 fixture 覆盖 plan approve/reject + question answered（`claude/testdata/streaming-phase3-{plan,question,bash}.jsonl` + `claude/phase3_test.go`）
- [x] 真 CLI 集成测试（`claude/phase3_live_test.go`，`//go:build claude_live`，需本地有 `trpc-claudecode 2.1.112+` 且 CLI 支持 `--replay-user-messages`）
- [ ] ~~`Descriptor.RunPolicyCaps.PlanReview.Retry = true`、`Question.Retry = true`~~ — **改为 false**。stream-json 模式下 CLI 无法对同一个 tool_use_id 重开决策窗口；FailureRetry 语义需要"让模型重新发新 tool_use"，这在 Phase 3 没实现。保留 `Retry:false`，后续若真需要再评估
- [x] `Permission.Ask` 明确声明为 `false`（stream-json 模式下 CLI 不自己执行 tool，需要 Phase 3.5 的宿主 executor）
- [x] `workstream-hitl-v2.md` §5.1.8 更新（指向本文档的实施）
- [x] `hitlmock` adapter 继续保留（作为无外部依赖的 demo 场景）
- [x] `handlePayload` 识别 `isReplay:true` 帧并跳过（防止 tool_result echo 污染 transcript）
- [x] `claudeInteractiveSinkRequired` 守卫 + `validateInteractivePolicy` 对 `Permission=Ask` 的拒绝

## 7. 时间预估

- Week 1：clihelper 重构 + 单元测试
- Week 2：claude adapter 两模式分派 + parser 状态机 + 单元测试
- Week 3：集成测试 + 示例联调 + 文档
- Week 4：code review / 小改

约 3-4 周 engineering 时间（不含 review）。

## 8. 后续

- **Phase 3.5**：Permission 类拦截（Bash/Write/Edit/...），需配合宿主侧 tool executor（可复用 workspace manager）
- **Phase 2**：codex `requestApproval` 双向（Phase 3 落地后代码模式已经走熟，Phase 2 主要是 JSON-RPC client）
- **Phase 4**：cursor——等 vendor CLI 加入流式 approval 协议

三个 Phase 之间无硬依赖，可并行。
