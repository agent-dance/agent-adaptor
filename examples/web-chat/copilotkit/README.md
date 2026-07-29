# web-chat/copilotkit

完整的 **AG-UI + CopilotKit** 前端 example，演示 `agent-adaptor` 的四条纵向通路：

1. **Streaming**：文本、thinking、tool_call 通过一条 `stream.Events()` 通道流式传递，终局由一次 `stream.Result()` 收口。若 Driver 没有发布 assistant delta 但终局 `Result.Text` 非空，后端会在 terminal 前投影一次完整 assistant message；已有流式文本时绝不重复。
2. **HITL**：没有安装 `OnApproval` 回调时，approval 以自带 responder 的 `*adaptor.ApprovalRequest` 事件出现在同一条流上。宿主停放请求后直接调用 `req.Approve(ctx)` / `req.Deny(ctx, reason)` / `req.Answer(ctx, option)`；`ErrApprovalResolved` 映射 HTTP 410，`ErrApprovalKindMismatch` 映射 400。`NoticeApprovalResolved` 会自动清掉 pending。
3. **Recovery**：每个浏览器有稳定 `thread_id`，右侧面板通过 `/session/events` + `/decision/pending` 恢复历史与未决决策。`EventMeta.Sequence` 只在单次 run 内排序；跨 run 重放使用 `hosttools/sessionrecorder` 分配的 `HostSeq`。
4. **User-prompt persistence**：user prompt 不会自动进入 SDK 持久层（thread store 只存 resume 元数据；driver 只 emit assistant 侧）。`handleAgent` 用 `userTurnEvents(lastUserMessageID(input), prompt)` 构造三段 `adaptor.TextDelta{Role: RoleUser, Phase: Start/Content/End}`，让它经过和 driver 事件相同的 fan-out。刷新页面拉 `/session/events` 即可恢复含 user 气泡的完整 transcript。<br>实现入口：`server.go::handleAgent` + `agui_run_session.go::recordUserTurn`；验证用例：`agui_run_session_test.go::TestAGUIRunSessionRecordsUserTurnBeforeAssistant`。详细设计见[归档工作流](../../../docs/archive/workstream-user-message-event.md)。

后端只调用真实本机 CLI：

| `AGUI_AGENT` | 行为 |
| --- | --- |
| `codex` | 本地 `codex`，streaming 走 codex app-server |
| `claude` | 本地 `claude` / `trpc-claudecode`，PlanReview / Question 可进入 HITL 交互 |
| `cursor` | 本地 Cursor Agent CLI（默认命令 `agent`，也会尝试 `cursor-agent`）；没有 token-level assistant delta 时仍会通过终局 `Result.Text` 显示完整回复。 |

## Quick Start

```bash
./examples/web-chat/copilotkit/start-all.sh codex
./examples/web-chat/copilotkit/start-all.sh claude
./examples/web-chat/copilotkit/start-all.sh cursor
```

打开 http://localhost:3000 。Go backend 和 Next.js dev server 默认都只绑定 `127.0.0.1`；这个示例会调用已登录的真实 CLI，不会默认暴露到局域网。

团队委托 showcase 复用同一套前端，但由它自己的脚本启动正确的 Go
backend 并切换为团队工作流页面（不显示该 backend 不提供的 HITL 恢复侧栏）：

```bash
./examples/showcases/team-agent-workflow/start-all.sh claude
```

`cursor` 在没有正式 assistant delta 协议时不会伪造 token 流；后端只把 Driver 已确认的最终 `Result.Text` 恰好投影一次，并同步写入恢复历史。

也可以只起后端：

```bash
./examples/web-chat/copilotkit/start.sh claude
```

## 架构

```
Browser
  ├─ <CopilotChat/>  (React, @copilotkit/react-ui)
  │     │  POST /api/copilotkit
  │     ▼
  │  Next.js CopilotRuntime + HttpAgent
  │     │  POST /agent  (AG-UI RunAgentInput)
  │     ▼
  │  Go backend
  │     ├─ ai.Thread(key).Stream(ctx, prompt)   →  adaptor.Stream
  │     └─ for ev := range stream.Events()      →  一条通道，两个去处
  │            ├─ recorder.Record(...)                    (历史，HostSeq 游标)
  │            ├─ translator.Translate(ev)  →  SSE        (实时)
  │            └─ *adaptor.ApprovalRequest  →  pending store (自带 responder)
  │        stream.Result()  →  Result.Text fallback（仅无 assistant delta）
  │                         →  translator.CloseResult(...) (终局判决)
  │
  └─ 直接 fetch (旁路) —— 用于 HITL & 恢复
         GET  /session/events?thread_id=T&after=N
         GET  /decision/pending?thread_id=T
         POST /decision/resolve
```

## 前置条件

- Go 1.26.5+
- Node.js 20+ + npm
- 选用的 `codex` / `claude` / `cursor` CLI 已安装、已登录，并且 `--help` 可运行

前端依赖固定使用 CopilotKit 1.63.2 与 `next@16.3.0-preview.8`。选择该
Next preview 是因为截至冻结日最新 stable 16.2.12 仍落在已公开的 high
advisory 范围内；同时以局部 `overrides` 将 Hono、PostCSS、fast-uri 提升到
兼容的安全补丁版本，并更新 MCP SDK、DOMPurify、Mermaid 等仍在兼容范围内的
传递依赖。lockfile 只保存 npm 官方 registry URL，CI 会执行 fresh `npm ci`、
lint、build 与 `npm audit --omit=dev --audit-level=high`。冻结时 production tree
没有 high/critical；剩余 moderate/low 来自 CopilotKit Runtime 尚未接受的
`@hono/node-server`/`uuid` major 以及暂无已发布修复的上游链路，因此没有用
`npm audit fix --force` 或不兼容的全局 override 掩盖风险。

## 后端 HTTP 端点

| 路径 | 方法 | 用途 |
| --- | --- | --- |
| `/agent` | POST | AG-UI RunAgentInput -> SSE 流 |
| `/session/events` | GET | 历史事件重放 |
| `/decision/pending` | GET | 未解决的决策请求 |
| `/decision/resolve` | POST | 宿主回填 HITL 决策 |
| `/health` | GET | 就绪检查 |

## 环境变量

| 名称 | 默认 | 说明 |
| --- | --- | --- |
| `AGUI_AGENT` | `codex` | `codex` / `claude` / `cursor` |
| `AGUI_MODEL` | agent 默认模型 | 覆盖 AG-UI 后端模型 |
| `CODEX_COMMAND` / `CLAUDE_COMMAND` / `CURSOR_COMMAND` | 自动探测 | 覆盖本机 CLI 命令 |
| `CODEX_MODEL` / `CLAUDE_MODEL` / `CURSOR_MODEL` | agent 默认模型 | 覆盖对应 agent 模型 |
| `ADDR` | `127.0.0.1:8080` | backend 监听地址；远程部署必须显式覆盖 |
| `CORS_ORIGIN` | `http://localhost:3000` | 仅允许本机 CopilotKit UI；可显式覆盖为部署后的准确 Origin |
| `THREAD_STORE_DIR` | unset | 设置后使用 typed Event JSONL 持久化；目录创建、写入或同步失败会显式终止启动/本次请求，绝不静默退回内存。未设置时明确使用 `NewMemoryEventBackend()` |

前端：

| 名称 | 默认 | 说明 |
| --- | --- | --- |
| `AGENT_BACKEND_URL` | `http://localhost:8080/agent` | CopilotRuntime 转发的 AG-UI 端点 |
| `NEXT_PUBLIC_AGENT_BACKEND_BASE` | `http://localhost:8080` | 浏览器旁路请求 base URL |

如需从其他机器访问，必须显式设置监听地址与准确 Origin，例如 `ADDR=0.0.0.0:8080 CORS_ORIGIN=https://chat.example.com`；不要在不受信网络中使用 `CORS_ORIGIN=*`，因为每个请求都可能触发真实模型调用。

## Visual subagent delegation hook

The backend can render remote A2A delegation progress in the same CopilotKit
AG-UI stream. Create an `a2adelegation.Service` from the remote Agent Cards and
pass `team.Option()` to `adaptor.New` (or pass the service through
`adaptor.WithRunServices(team)`). The service creates an authenticated,
run-scoped `delegate_to_agent` MCP sidecar, exposes it through the typed MCP
contract, binds its cleanup to the run, and folds progress into the Agent's
single event stream as `adaptor.SubagentUpdate`. The AG-UI bridge projects those
updates into one incrementally patched AG-UI Activity message per delegation
(`activityType="subagent"`, `messageId=delegationId`); Claude/Codex/Cursor
receives only the final structured MCP result. In team-workflow mode the
CopilotKit frontend renders these Activity messages as live cards in the right
sidebar. A role's human-facing name carries its provider base, so the card tag
shows `Claude Code`, `Codex`, `Cursor`, or `CodeBuddy` instead of the common
A2A transport. Structured plan output shaped as a file value is rendered as a
previewable and downloadable `PLAN.md` attachment.

This example's default main path starts one local parent CLI. Hosts that enable
delegation should run an A2A bridge for each remote agent (see
`examples/a2a-server` and `docs/a2a.md`), construct the service once, keep it
alive for the equipped Agent, and close it during host shutdown. No stringly
runtime metadata or bridge-specific run handle is involved.

The important boundary for UI authors: render `activityType="subagent"` as a
nested visual group, but do not turn it into a parent assistant message. The
Activity content may include `parentToolCallId` when the host can correlate the
delegation sidecar with a parent provider tool-use ID, but UI code should not
require it. `subagentId` is the stable nested group key and `runId` is its run
scope. The parent transcript should contain the normal tool call plus the
concise final delegation result only.

## 相关文档

- [`docs/workstream-user-message-event.md`](../../../docs/archive/workstream-user-message-event.md)
- [`docs/workstream-hitl-v2.md`](../../../docs/archive/workstream-hitl-v2.md)
- [`docs/workstream-session-recorder.md`](../../../docs/archive/workstream-session-recorder.md)
- [`docs/run-policy.md`](../../../docs/run-policy.md)
- [`docs/streaming-adapter-contract.md`](../../../docs/streaming-adapter-contract.md)
