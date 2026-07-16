---
scope: claude-streaming
status: approved
references:
  - AGENTS.md
  - claude/parser.go
  - claude/streaming_parser.go
  - claude/streaming_parser_test.go
---

# 补齐 Claude 工具调用生命周期、参数与 SubAgent 标题

## 目标

让所有经 Claude 输出的工具调用都向 `StreamPayload` 输出完整的生命周期和参数，使下游前端能直接聚合卡片；同时让 FlowX Viewer 根据真实参数显示具体 SubAgent 名称。Explore 是当前已确认的触发场景。

## 背景

Claude CLI 对普通工具调用会输出 `stream_event.content_block_start` 和 `input_json_delta`，adapter 已将其转换为完整工具调用生命周期。

Explore 内部工具调用会以非流式 `assistant.message.content[].tool_use` 出现。任何 Claude 工具调用出现这种形态时，当前 `handleAssistantMessage` 都只写 `TranscriptToolCall`，没有调用 `EmitStream`。后续 `user.message.content[].tool_result` 仍会被 `handleUserMessage` 转为 `StreamToolCallResult`，因此前端只有结果，没有名称和入参。

下游 FlowX 会原样转发 `StreamPayload`，但 Viewer 当前不读取 `tool_call.start.Args`，也只会把 SubAgent 显示成 Claude 的原始工具名 `Agent`。AG-UI translator 同样会忽略 start 上的完整 Args，因此需要补齐转换降级逻辑。

## 终态方案

在 `agent-adaptor` 的 Claude streaming parser 中补齐非流式 tool_use fallback：

1. `handleAssistantMessage` 发现 `tool_use` 且 streaming 已启用时，调用 streaming state 的 fallback 方法。
2. fallback 以完整 `input` 发出 `StreamToolCallStart`，设置 `ToolCallID`、`Name` 和 `Args`，紧接着发出 `StreamToolCallEnd`。
3. 既有 `handleUserToolResult` 不变，继续发出同一 `ToolCallID` 的 `StreamToolCallResult`。
4. streaming state 记录已见 tool call ID，普通 `stream_event` 与 fallback 共享这一去重状态，保证同一工具调用不会重复发 start/end。
5. `input_json_delta` 参数事件带上原始工具名；完整 `assistant.tool_use.input` 原样写入 `StreamToolCallStart.Args`，不额外包装 `input`。
6. AG-UI translator 暂存 start 上的完整 Args：若后续没有 delta，则在 end 前补发一次 `TOOL_CALL_ARGS`；若收到 delta，则保持既有 delta 流并丢弃快照，避免把完整 JSON 与参数/命令输出 delta 拼成无效 AG-UI args。
7. FlowX Viewer 同时消费 start 快照和 args 增量；有完整 start Args 时把后续 delta 单独显示为 output。参数 JSON 完整后，根据 `subagent_type` 把 `Agent` / `Task` 卡片标题显示为具体 SubAgent 名称，事件中的真实工具名保持不变。

## 验收场景

Given Claude 输出非流式 `assistant.tool_use(name=Read, input={file_path: ...})`，随后输出 `user.tool_result`

When adapter 处于 streaming 模式

Then stream 按顺序得到同一 ToolCallID 的 start、end、result；start 带 `Name=Read` 与完整 Args。

Given 同一 ToolCallID 已通过 `stream_event` 输出 start、args、end

When terminal assistant message再次携带该 tool_use

Then fallback 不重复输出 start 或 end，原有 args 和 result 语义不变。

Given 下游转发上述 payload 并由前端重放

When 渲染 Explore 或其他非流式 Claude 工具调用卡片

Then 卡片无需结果反推，直接展示名称、入参和结果。

对应 adapter 测试：`claude/streaming_parser_test.go`。下游 FlowX 升级后应补充自身的转发和回放验收。

## 变更边界

允许修改：

- `agent-adaptor/claude/parser.go`、`streaming_parser.go` 和测试
- `agent-adaptor/pkg/bridges/agui/bridge.go` 和测试
- `flowx-agent/cmd/flowx-agent/web/app.js` 和 `viewer_test.go`
- `go.mod`、`go.sum`（仅版本发布所需变更）
- 本 Plan

禁止修改：

- 外部前端的结果反推、cursor 兼容逻辑
- Claude CLI 子 agent transcript 文件 tail
- checkpoint、artifact、HITL 和 Team Mode 状态机
- Rainbow、部署配置和业务运行时下发字段

## 实施步骤

- [x] 在 Claude streaming state 中实现非流式 tool_use 的 lifecycle fallback 与去重状态
- [x] 在 parser 中接入 fallback
- [x] 增加 Explore result-only fixture 与正常流式去重 fixture
- [x] 增加 MCP 参数增量 fixture
- [x] 增加 AG-UI 完整 Args 降级与去重测试
- [x] 更新 FlowX Viewer 的参数聚合与 SubAgent 标题
- [ ] 发布 adapter 修复版本
- [ ] 向下游说明升级版本与验收条件
- [x] 运行 Claude adapter、AG-UI bridge、FlowX 全仓测试和定向 race 测试
- [x] 自检 Plan 与实现一致

## 风险与回滚

- Claude CLI 可能在不同版本中同时输出流式与非流式 tool_use；必须按 ToolCallID 去重。
- fallback Args 来自完整 input，不生成伪造的增量 args 事件。
- 可通过回退 `agent-adaptor` 版本恢复当前只显示结果的行为。
