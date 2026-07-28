# Workstream: HITL（Human-In-The-Loop）问题记录与设计输入

> 状态：历史调查资料。当前 HITL 公共合同见 [`run-policy.md`](../run-policy.md)，当前 bridge 行为见 [`streaming.md`](../streaming.md)。本文中的 `Approvals` / `Trust` / audit-only 描述保留为问题背景，不代表当前 API。

本文件是 HITL 设计的**一手调查资料**。它不预先给结论，只负责把"当时 v1 audit-only 方案在真实交互中暴露了什么"固化下来，作为后续 HITL v2 合同/协议/UI 桥接层设计的输入。

当时范围锚点：
- `docs/streaming-adapter-contract.md` §2.5（HITL v1：audit-only，auto-deny）
- `run_types.go` `StreamHITLRequested` / `StreamHITLResolved`（v1 仅审计通道）
- `run_policy.go` `Approvals ∈ {ask, auto, off}`（当时唯一的审批旋钮）
- `pkg/bridges/agui/bridge.go`（当时 `StreamHITLRequested` 映射到 AG-UI `CustomEvent`，CopilotKit 默认不渲染）

## 0. TL;DR（一句话）

在 `Approvals=off` + headless stdio 通道 + 裸 AG-UI bridge 下，**Claude Code 的 Plan Mode（`ExitPlanMode` 工具）审批被 `trpc-claudecode` 自动合成为"用户未批准"的 `tool_result`**；Claude 因此只输出 plan 说明文字后以 `stop_reason=end_turn` 正常收尾，宿主/用户误以为"任务已完成"。这是一类**协议级的 HITL 损失**，而且**走的不是 `permission_request` 通道**，现行 v1 audit-only 的 HITL 设计完全接不住。

## 1. 问题陈述

`examples/streaming-chat-copilotkit` + `claude` adapter 在 `AGUIExampleRunPolicy`（`Approvals=off`, `Isolation=workspace_write`）下，与 Claude Code 进行多轮、涉及文件改动的任务时，用户反复感到"中断了"——任务看起来完成了（UI 出现长段 text，`RUN_FINISHED` 正常发出），但**磁盘上什么都没发生**，直到下一轮追问"完成了吗？"才真正执行。

### 1.1 用户可感知症状

- 发出改动类 prompt 后，UI 偶发 101 秒无任何事件
- 中途出现若干"空气泡"（空 text content 气泡一闪）
- 最后一条 assistant 消息是一段**听起来像是总结/分析**的文字，带 `RUN_FINISHED`
- 实际文件系统零变更
- 追问"完成了吗？"Claude 自己说"还没有，刚才只是制定了计划并等待你确认"

### 1.2 最初的两条假设（已排除）

1. **HITL permission 被协议转换丢弃** —— 部分对：`StreamHITLRequested` 确实在 `pkg/bridges/agui/bridge.go` 里被降级成 `CustomEvent` 而被 CopilotKit 忽略，但该路径在本次案例中**根本没被触发**（因为 `--dangerously-skip-permissions` bypass 了 `permission_request`）。丢失的是另一类"确认"，见 §4。
2. **Claude CLI 未登录产出 synthetic 错误** —— 存在独立 bug（见 §7.3），但用户明确该次交互不是这个原因。

## 2. 数据来源与复现信息

- 会话 ID：`96809d62-abc5-4ebb-b0d6-e0031b63e1d5`
- 产生路径：`~/.trpc-claudecode/projects/-Users-blurooo-project-agent-adaptor/96809d62-abc5-4ebb-b0d6-e0031b63e1d5.jsonl`
- 时间范围：`2026-04-22 06:19:00Z ~ 06:27:06Z`（北京时间 14:19–14:27）
- 触发路径：`examples/streaming-chat-copilotkit` 默认启动脚本，前端 CopilotKit 连 `/agent` SSE；Policy = `AGUIExampleRunPolicy()`（见 `examples/internal/exampleutil/agui_sdk.go`）
- 三条 prompt（同一会话）：
  1. "你能做什么"
  2. "帮我把 AGENTS.md 中可以渐进式披露的部分挪到 docs 下面（如果已经 docs 下已有重复的则合并）"
  3. "完成了吗？"

## 3. 全时间线（原始 jsonl 摘录）

### 3.1 第 1 轮｜14:19:00 "你能做什么"

| 时间 | 事件 |
|---|---|
| 14:19:09 | `assistant` text（完整段）+ `stop_reason=end_turn` |

正常，不贡献异常。

### 3.2 第 2 轮｜14:20:05 "帮我把 AGENTS.md…挪到 docs 下面"（同一 Run，promptId=`70d1f431-…`）

| 时间 | 事件 | 前端可感知 |
|---|---|---|
| 14:20:10 | `assistant` content=`[text]` 但 text=`""`，stop=`tool_use` | 空气泡闪一下 |
| 14:20:13 | tool_use `Agent`（子 agent） | CopilotKit 默认不渲染 tool_use |
| 14:20:13 → 14:21:54 | **101 秒死寂**（子 agent 在跑，stream_event 不吐 delta） | 事件流空窗 |
| 14:21:54 | Agent tool_result 回来 | 无感 |
| 14:21:57 | `assistant` text=`""` + tool_use `Read` | 空气泡 |
| 14:21:57 → 14:22:05 | Read / Glob / Read 连发，均以空 text 为前缀 | 一串空气泡 |
| 14:22:09 | 首条有字的 text："现在我已经清楚了 AGENTS.md 的内容。让我分析…" | 终于有字 |
| 14:22:10 | 并发两条 tool_use `Read` | 无感 |
| 14:22:52 | text + tool_use `Agent`（第二次子 agent） | 再次空窗 |
| 14:23:28 | Agent tool_result | — |
| 14:23:36.579 | tool_use **`EnterPlanMode`** → tool_result `"Entered plan mode."` | 无感 |
| 14:23:43 | text："分析完整了。现在我来整理规划。" | — |
| 14:24:13 | tool_use `Write`（写入 `plans/floating-noodling-sedgewick.md`，即 plan 草稿） | 无感 |
| **14:24:16.449** | **tool_use `ExitPlanMode`**（`input.plan` 携带完整计划文本） | 无感 |
| **14:24:16.450** | **tool_result：`"Exit plan mode?"`，`is_error: true`，间隔 1ms** | 无感 |
| 14:24:27 | text："**分析结果如下：** AGENTS.md 里有两处「渐进式披露」候选…"，`stop_reason=end_turn` | 用户看到长段文字，**误判为已完成** |

### 3.3 第 3 轮｜14:25:14 "完成了吗？"（promptId=`ce864a20-…`）

| 时间 | 事件 |
|---|---|
| 14:25:19 | `assistant` text："**还没有，刚才只是制定了计划并等待你确认**。现在来执行：" |
| 14:25:21 | tool_use `TodoWrite` |
| 14:25:52 | tool_use `Write`（真实创建 `docs/usage-guide.md`） |
| 14:26:14 / 14:26:42 | tool_use `Edit`（真实修改 `AGENTS.md`） |
| 14:27:06 | `stop_reason=end_turn` "完成了。变更摘要：…" |

## 4. 决定性证据

第 2 轮的 `ExitPlanMode` tool_result：

```json
{
  "tool_use_id": "toolu_bdrk_01U6dYM4QmXr9LS5vhzsyagn",
  "content": "Exit plan mode?",
  "is_error": true
}
```

对照 Claude Code 的 Plan Mode 协议约定：

- `ExitPlanMode` 是一个 **client-interactive 工具**：`input.plan` 携带完整计划文本，client 应把它渲染给用户，等用户选 approve 或 reject
- 正确 approve 的 tool_result 文案为 `"User approved the plan."`，`is_error: false` → Claude 继续执行
- reject / cancel 的 tool_result 典型返回 `"Exit plan mode?"` 或等价文案，`is_error: true` → **Claude 停留在 plan 模式，不得写入任何文件**，通常以分析/说明性文本结束本轮

1 毫秒（14:24:16.449 → 14:24:16.450）的间隔说明：`trpc-claudecode` 在 `bypassPermissions` + 纯 stdio 的 headless 模式下，**没有任何 UI hook 可回填 approve**，于是直接合成了等价于"取消"的 tool_result。

结果：Claude 认定 plan 没被批准，生成最终分析文字后以 `end_turn` 收尾，磁盘零变更。

## 5. 根因分层

损失发生在链路的最上游，不是 bridge/UI 层漏了；但每一层都没有机会接住：

```
[Claude 协议层]
  ExitPlanMode 语义：client 必须展示 plan、等 UI 回填 tool_result
       │
       ▼
[trpc-claudecode (bypassPermissions, stdio-only)]
  无 UI hook → 合成 "Exit plan mode?" + is_error:true → 等价默认 reject
       │
       ▼
[claude adapter：claude/parser.go + streaming_parser.go]
  看到的是普通 tool_use / tool_result 对
  不识别 ExitPlanMode / EnterPlanMode 属于 client-interactive 语义
  不会把 input.plan 上浮到 StreamHITLRequested
  StreamToolCallStart/Args/End/Result 照常发
       │
       ▼
[pkg/bridges/agui/bridge.go]
  普通 ToolCall* → AG-UI ToolCall* 事件
  即便 adapter 发了 StreamHITLRequested，也只映射为 CustomEvent（CopilotKit 默认忽略）
       │
       ▼
[CopilotKit <CopilotChat/>]
  tool_use 默认不渲染；没有审批 hook
  用户只看到最终 end_turn 的那段"分析结果如下…"
```

关键落差：**"需要人类在回路上"的工具在 Claude 协议里是通过 tool_use/tool_result 走的（`ExitPlanMode`、`AskUserQuestion` 等），而 agent-adaptor v1 HITL 只识别 codex 风格的 `permission_request` 显式事件。二者不可互换。**

## 6. 为什么 v1 HITL 没接住

对照 `docs/streaming-adapter-contract.md` §2.5：

- v1 HITL 合同假设"底层协议有 server-initiated request"（codex 的 `item/commandExecution/requestApproval` 是这种形态）
- adapter 发现后 **auto-deny** 并 `EmitStream(StreamHITLRequested)` 做审计
- 不阻塞宿主响应

但在 Claude Code 里**交互式审批不是一个独立 server request，而是一种特殊的 tool_use**：

1. adapter 没有把 `ExitPlanMode` / `AskUserQuestion` 这类工具识别为"应当上浮到 HITL 通道"——源码里它们和 `Read` / `Write` / `Glob` 在 `handleAssistantMessage` / `handleToolUseBlock` 路径上完全同构
2. 即便识别了，v1 的语义是"audit + auto-deny"。对 Claude 而言，CLI 已经替它 deny 了；adapter 再补发一条审计事件**改变不了磁盘结果，也通知不到前端**（CustomEvent 被吞）
3. `Approvals=off` 的唯一落盘是 `--dangerously-skip-permissions` 这个 CLI 一刀切开关。它同时关掉的是两个维度：
   - 文件/命令写权限弹窗（**调用方想要**）
   - Plan Mode / AskUserQuestion 的 UI 确认（**调用方并不想丢**，但被一起牵连）

## 7. 辅助/次级问题

这些不是本次"中断感"的主因，但它们叠加在一起放大了用户感知。HITL 设计时应一并考虑是否在同一版本中解决。

### 7.1 子 agent 工具的长时间死寂

`Agent` 工具执行期间（§3.2 中 14:20:13 → 14:21:54，101 秒；14:22:52 → 14:23:28，36 秒）stream_event 不吐 delta。adapter 没有 keep-alive / heartbeat。从 SSE 通道看就是"事件流空窗"。

建议（非 HITL 范畴，但相邻）：在 tool_use 发起后、tool_result 到来前，按固定节奏发送 `StreamCustom{Name: "tool.progress"}` 或等价 keep-alive；或至少让 bridge 按心跳发送 SSE comment 保持链接活性。

### 7.2 空 text content 气泡

§3.2 中至少 9 处 `content=[text]` 但 text=`""`。Claude 常在 tool_use 前后夹一个空 text block 作为分段标记。这些目前会走完 `StreamTextStart → (0 token) → StreamTextEnd`，UI 上表现为短暂的空泡。

建议：在 `claude/streaming_parser.go` 的 text 输出闭合处，若内容为空则不发 `Start/End` 对，或下沉为一个 `StreamCustom{Name:"text.empty"}`。

### 7.3 synthetic 错误未进入 stream（独立 bug，已知，未修）

当 Claude CLI 未登录时，返回 `message.model="<synthetic>"` + `error="authentication_failed"` + text "Not logged in…"，没有前置 stream_event delta。`claude/parser.go` 的 `handleAssistantMessage` 只把它塞进 `Transcript`，不发到 stream；UI 看到空 bubble + 正常 `RUN_FINISHED`。

修复方向（与本次 HITL 议题独立，可并行）：
- `claude/parser.go`：检测到 `isApiErrorMessage=true` 或 `model="<synthetic>"` 时，emit `StreamRunError`；兜底合成 `StreamTextStart/Content/End`
- `claude/streaming_parser.go::handleResultTerminal`：`result.subtype` 表示错误（或 `is_error=true`）时，emit `StreamRunError` 而非 `StreamRunFinished`

### 7.4 UI 默认不渲染 tool_use

CopilotKit 的 `<CopilotChat/>` 在没注册 `useCopilotAction` 的工具上默认静默。这放大了 §7.1 的死寂感。HITL 设计时需要约定：哪些工具应强制在 UI 上可见（至少显示"正在执行 XXX…"的状态块）。

## 8. 问题分类：三类"需要人在回路上"的工具

HITL 设计必须首先承认"审批"不是一种均质事件。至少应区分：

| 类别 | 代表工具 | 目前协议形态 | 若不正确处理的后果 |
|---|---|---|---|
| A：**前置权限询问** | codex 的 `requestApproval`；Claude 的 bash/file 权限弹窗（已被 `--dangerously-skip-permissions` bypass） | server-initiated request | 被用户阻塞或被 auto-deny，任务失败但行为可预期 |
| B：**Plan 审批** | Claude `ExitPlanMode` | client-interactive tool_use，`input.plan` 携带待审阅文本 | **本次案例**：被静默 reject，UI 误判为已完成，磁盘零变更 |
| C：**执行中澄清** | Claude `AskUserQuestion`；各 adapter 的交互式 prompt | client-interactive tool_use，等待结构化回答 | 被静默 cancel / 默认值，agent 自行臆测或停住 |

**v1 只覆盖了 A 的一个子集**（codex 的 `requestApproval`），而且仍然是 audit-only。B 和 C 在 Claude 上完全没被识别；在其他 adapter 上也没有统一抽象。

## 9. HITL v2 必须回答的问题

设计时建议至少给每条一个显式答案（不是隐含默认）：

### 9.1 合同层

1. HITL 事件的**统一抽象**是什么？是"审批请求"（布尔 approve/reject）还是更广义的"结构化问答"（可返回任意 JSON）？B/C 两类能不能塞进同一个事件模型？
2. Adapter 如何**识别** "这一次 tool_use 其实是 HITL"？是白名单工具名（`ExitPlanMode`、`AskUserQuestion`、…），还是 schema 驱动（工具签名里有"interactive: true"之类标记），还是两者都要？
3. `StreamHITLRequested` 是否需要新增子类型（`kind: approval | question | plan_review`）？
4. **响应通道**在哪里？adapter 需要一个 `ResolveHITL(requestID, response)` 入口；它与现有 `CheckpointResume` / `sink.Emit` 怎么不互相打架？
5. 阻塞还是非阻塞？若 UI 没回应，**兜底策略**是：timeout reject / auto-approve / 保持挂起？不同工具类别应当允许不同默认。

### 9.2 Policy 层

1. `Approvals ∈ {ask, auto, off}` 是否仍然够用？当前 `off` 同时关闭 A 和 B/C，是本次案例的直接诱因之一。
2. 是否引入细粒度子开关？候选：
   - `Approvals.FilePermissions`（A 子集）
   - `Approvals.PlanReview`（B）
   - `Approvals.InteractiveQuestions`（C）
3. 如何与现有 CLI 旋钮对齐？`--dangerously-skip-permissions` 在 Claude 上是 A+B+C 一起关；在 codex 上 `bypass_approvals_and_sandbox` 只影响 A。Policy 到 driver flag 的映射矩阵需要写死。

### 9.3 Bridge / UI 层

1. AG-UI 是否需要**一条新的 event type**（例如 `HITL_REQUESTED` / `HITL_RESOLVED`），而不是降级成 `CustomEvent`？（CopilotKit 目前的 `useCopilotAction` 约定是否可以直接承载？）
2. 宿主如何把 UI 侧的 approve 回填到 adapter 的 `ResolveHITL`？需要一个跨进程/跨 SSE 连接的**关联 ID 机制**（`requestID` 必须在 bridge 里被保留并可寻址）。
3. 如果 UI 没接入 HITL 通道（例如 `examples/streaming-sse-server` 这类最小示例），默认行为是什么？**现在的"静默 reject"是最坏默认**，应当改为更显性的失败或至少一条 banner 级别的 `RunError` / `RunFinished.Reason` 字段。

### 9.4 adapter 专属（Claude）

1. `ExitPlanMode` 的正确 approve tool_result 文案、格式（是否可自定义 `response_text`？）需要验证并写进 `docs/workstream-streaming-claude.md`。
2. `AskUserQuestion` 的 `input` / 期望 tool_result 结构需要抓一份真实样本固化。
3. `EnterPlanMode` / `ExitPlanMode` / `AskUserQuestion` 这三个工具名是否稳定？是否应该用 metadata 而不是硬编码名字做识别？

## 10. 临时缓解（可选，不是最终设计）

在 HITL v2 落地前，如果想先把本次用户感知问题压下去，有三条互不冲突的短期修补：

1. **parser 层 early-warning**：`claude/parser.go` 在检测到本次 Run 内出现 `ExitPlanMode` tool_use 且对应 tool_result `is_error=true` 时，`sink.Emit(RunFinished{Reason: "plan_unapproved"})` 或 emit 一条 `StreamCustom{Name:"claude.plan_unapproved", Raw: {plan}}`，让宿主/UI 至少"知情"而不是被 `end_turn` 骗。
2. **bridge 层显式上浮**：在 `pkg/bridges/agui/bridge.go` 里为 `StreamHITLRequested` 增加一条 opt-in 映射（配置或工具名白名单），映射成 CopilotKit 可识别的 action / message，而非 CustomEvent。
3. **policy 描述对齐**：在 `docs/run-policy.md` 的 `Approvals=off` 说明里明确标注"同时会绕过 Plan Mode / AskUserQuestion 审批；当前版本这些节点会被 CLI 默认拒绝"，把**已知限制**显式化。

以上任何一条单独做都不是 HITL 的设计终态，但会把用户体验的"静默失败"先变成"显性失败 + 可观测"。

## 11. 决策原材料清单（供 HITL 设计文档引用）

- 会话 jsonl：`~/.trpc-claudecode/projects/-Users-blurooo-project-agent-adaptor/96809d62-abc5-4ebb-b0d6-e0031b63e1d5.jsonl`
- 决定性帧：`ExitPlanMode` tool_use（14:24:16.449）+ 1ms 后的 tool_result `"Exit plan mode?"`/`is_error:true`（14:24:16.450）
- 当时 HITL v1 合同：`docs/streaming-adapter-contract.md` §2.5
- 当时 Policy 合同：`docs/run-policy.md`、`run_policy.go`
- 当时 bridge 映射：`pkg/bridges/agui/bridge.go`（`StreamHITLRequested → CustomEvent`）
- 当时 Claude driver 现状：`claude/driver.go`（`HITL: false`）、`claude/parser.go`（`permission_request → StreamHITLRequested`，未覆盖 `ExitPlanMode` 等）
- 参考示例：`examples/streaming-chat-copilotkit/main.go` + `examples/internal/exampleutil/agui_sdk.go`
