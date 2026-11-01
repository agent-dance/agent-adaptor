# web-chat/copilotkit

完整的 **AG-UI + CopilotKit** 前端 example，演示 `agent-adaptor` 的四条纵向通路：

1. **Streaming**：文本、thinking、tool_call 通过一条 `stream.Events()` 通道流式传递，终局由一次 `stream.Result()` 收口。若 Driver 没有发布 assistant delta 但终局 `Result.Text` 非空，后端会在 terminal 前投影一次完整 assistant message；已有流式文本时绝不重复。
2. **HITL**：没有安装 `OnApproval` 回调时，approval 以自带 responder 的 `*adaptor.ApprovalRequest` 事件出现在同一条流上。宿主停放请求后直接调用 `req.Approve(ctx)` / `req.Deny(ctx, reason)` / `req.Answer(ctx, option)`；`ErrApprovalResolved` 映射 HTTP 410，`ErrApprovalKindMismatch` 映射 400。`NoticeApprovalResolved` 会自动清掉 pending。
3. **Recovery**：每个浏览器有稳定 `thread_id`，右侧面板通过 `/session/events` + `/decision/pending` 恢复历史与未决决策。`EventMeta.Sequence` 只在单次 run 内排序；跨 run 重放使用 `hosttools/sessionrecorder` 分配的 `HostSeq`。
4. **User-prompt persistence**：SDK 的消息回执不等于 UI transcript；示例仍需显式记录 user prompt 气泡。`handleAgent` 用 `userTurnEvents(lastUserMessageID(input), prompt)` 构造三段 `adaptor.TextDelta{Role: RoleUser, Phase: Start/Content/End}`，让它经过和 driver 事件相同的 fan-out。刷新页面拉 `/session/events` 即可恢复含 user 气泡的完整 transcript。<br>实现入口：`server.go::handleAgent` + `agui_run_session.go::recordUserTurn`；验证用例：`agui_run_session_test.go::TestAGUIRunSessionRecordsUserTurnBeforeAssistant`。

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
  │     ├─ ai.Thread(key).Send(ctx, prompt)   →  adaptor.Stream
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

## Visual team updates

The team showcase uses this frontend with a `hosttools/team.Team` as the
bridge's Runner. Team contains a host-selected leader and local `Member.Runner`
or Remote A2A `Member.Remote` entries. The default backend in this directory
still starts one local Agent; the showcase has its own backend and launch script:

```bash
./examples/showcases/team-agent-workflow/start-all.sh claude
```

Team owns one capability-token-protected loopback MCP listener. Its leader has
four tools (delegate/send/status/cancel); local workers have send/status. The
leader relays messages, while bound local worker approvals carry
`Origin{Member, Task}` to the host. Remote input-required follows remote policy,
with no portable local approval responder. The showcase uses noninteractive
member policies and does not provide this backend's HITL/recovery endpoints.

The single Team Event stream includes typed `SubagentUpdate` values. AG-UI
projects their lifecycles into Activity messages with
`activityType="subagent"`; team-workflow mode renders them as sidebar cards.
Use the task handle for task identity and `Member` for roster identity. There
is no separate delegation bus or extra subagent merge to attach.

The frontend can render provider names, worker text and structured `PLAN.md`
attachments when those fields are supplied. Current Team updates carry typed
state/delta/summary but do not populate the older Activity detail fields used
by those rich renderers. Do not assume provider badges, live text or plan
attachments are available from this showcase. Full local worker results and
Remote protocol artifacts are available through the host's Team TaskStore;
this backend does not expose a TaskStore download endpoint.

The maintained backend here uses task-mode `Thread.Send`. The showcase publishes
a Team Runner through request-scoped SSE. Neither example is a Standby UI or
an endpoint for supplementing an active execution. A host adding ongoing chat
should create `Team.Thread(key).Standby(serverCtx)`, subscribe once and broadcast
its events; route user input through `Standby.Send` and a stop button through
`Standby.Cancel`. Task-mode sends to a busy named scope report `ErrThreadBusy`.

For Remote members, the host provides Agent Card URL, trusted-origin auth and
policy in `RemoteSpec`; no per-member A2A server is needed for local members.
Close the Team during shutdown, then close the host-owned Agents. See the
[team guide](../../../docs/team.md) and [A2A guide](../../../docs/a2a.md) for
recovery, Fork and Remote timing/schema/approval limits.

UI authors should keep worker Activities in their own visual groups, without
turning them into parent assistant messages. `parentToolCallId` is optional;
`subagentId` identifies an Activity within its `runId`. A delegated tool result
is a task handle/admission result; worker completion reaches the leader later
as a notification. It is not a synchronous final MCP result.

## 相关文档

- [`docs/streaming.md`](../../../docs/streaming.md)
- [`docs/run-policy.md`](../../../docs/run-policy.md)
- [`docs/streaming-adapter-contract.md`](../../../docs/streaming-adapter-contract.md)
