# web-chat/copilotkit

完整的 **AG-UI + CopilotKit** 前端 example，演示 `agent-adaptor` 的四条纵向通路：

1. **Streaming**：文本、thinking、tool_call 通过一条 `stream.Events()` 通道流式传递，终局由一次 `stream.Result()` 收口。若 Driver 没有发布 assistant delta 但终局 `Result.Text` 非空，后端会在 terminal 前投影一次完整 assistant message；已有流式文本时绝不重复。
2. **HITL**：没有安装 `OnApproval` 回调时，approval 以自带 responder 的 `*adaptor.ApprovalRequest` 事件出现在同一条流上。宿主停放请求后直接调用 `req.Approve(ctx)` / `req.Deny(ctx, reason)` / `req.Answer(ctx, option)`；`ErrApprovalResolved` 映射 HTTP 410，`ErrApprovalKindMismatch` 映射 400。`NoticeApprovalResolved` 会自动清掉 pending。
3. **Recovery**：每个浏览器有稳定 `thread_id`，右侧面板通过 `/session/events` + `/decision/pending` 恢复历史与未决决策。`EventMeta.Sequence` 只在单次 run 内排序；跨 run 重放使用 `hosttools/sessionrecorder` 分配的 `HostSeq`。
4. **User-prompt persistence**：`bridges/agui/aguisession` records the input user turn before source events and projects replayable history. The POST response omits that user turn because the browser already owns it. `/session/events` keeps its existing typed-record response shape; storage uses `aguisession.RecorderKey(recordingKey(threadID))`. Result fallback and approval observation use the same single drain.

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

前端依赖固定使用 CopilotKit 1.83.0、`@ag-ui/client` 1.0.2（与 CopilotKit
内部精确依赖的版本一致，避免出现两份 `HttpAgent`/`AbstractAgent`）以及
stable `next@16.4.3`。CopilotKit 1.83 自带 AG-UI 1.0 客户端，会按 1.0 schema
校验事件流（例如 `CUSTOM.value` 必填），因此后端必须使用发送完整 1.0 事件的
`bridges/agui`。

上游仍未修复的链路通过 `overrides` 提升，并都验证过运行时兼容：

- `zod` → 4.6.7（GHSA-mrhx-c7xc-m3w5 没有 3.x 修复）。`@ag-ui/client` 已改用
  `zod/v4` 子路径，`@ag-ui/core` 声明支持 `^4`，CopilotKit 的
  `schemaToJsonSchema` 优先走 zod 4 原生 `toJSONSchema()`；默认解析下顶层
  zod 本来就是 4.x。override 只影响 `@ag-ui/proto`（运行时不 import zod）、
  `@copilotkit/shared`（`describe`/`any` 等 zod 4 仍保留的 API）以及 A2UI
  链路 `@a2ui/web_core`/`@copilotkit/a2ui-renderer` 的嵌套副本；本示例的
  Runtime 未启用 A2UI（`/info` 返回 `a2uiEnabled: false`）。
- `undici` → 6.29.1：`@ai-sdk/provider-utils@3`（经 `@ai-sdk/google-vertex@3`）
  声明 `^5.29.0`，5.x 没有修复；它只用 `Agent` + `fetch({ dispatcher })`，与
  `provider-utils@4` 搭配 undici 6 的用法相同。同时移除了 undici 5 带入的
  `@fastify/busboy` 2.x。
- `graphql-scalars` → 2.0.1（GHSA-x8rm-f576-gq67）：Runtime 与 `type-graphql`
  只使用 `GraphQLJSON`、`GraphQLJSONObject`、`GraphQLTimestamp`、
  `GraphQLDateTimeISO`，2.0.1 仍导出这四个 scalar。

lockfile 只保存 npm 官方 registry URL，CI 会执行 fresh `npm ci`、lint、build 与
`npm audit --omit=dev --audit-level=high`。冻结时 production tree 没有
high/critical；剩余项低于 CI 阈值：

- `@hono/node-server` 1.19.17（moderate，GHSA-rmxm-3fg6-px4f）：修复只在 2.x，
  CopilotKit Runtime 与 MCP SDK 仍要求 1.x；该漏洞位于 `serveStatic`，本示例
  只通过 Next.js route handler 使用 Runtime，不走这条路径。
- `katex` 0.16.x（low，GHSA-238p-pmpm-9mq7）及其 `streamdown`/`mermaid`/
  `remark-math` 依赖链：修复在 0.18.2，`@copilotkit/react-core` 仍锁 `^0.16`；
  它只在浏览器中渲染本地 agent 输出的数学公式。

因此没有用 `npm audit fix --force`（它会把 CopilotKit 降到 1.54）掩盖风险。

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

- [`docs/streaming.md`](../../../docs/streaming.md)
- [`docs/run-policy.md`](../../../docs/run-policy.md)
- [`docs/streaming-adapter-contract.md`](../../../docs/streaming-adapter-contract.md)

The local example permits session access by default. Production hosts must supply
`aguisession.Options.Resolve` to authorize and map `OpStart` and `OpRead` using
the authenticated principal; browser thread IDs do not grant access. Protect
`/session/events` and `/decision/*` with the same authorization and key mapping:
these example-specific companion handlers use the local default mapping and do
not pass through the Session handlers or its Resolve hook.


The backend uses `bridges/agui/aguisession` for the sole drain, record-before-wire
projection, user capture, Result.Text fallback and authoritative termination.
It enables `InputOptions.HistoryFallback`: subsequent turns rehydrate recorded
text/call summaries into the prompt. `THREAD_STORE_DIR` persists UI audit records,
not provider checkpoints. A host requiring provider-native continuation must
inject a persistent ThreadStore separately.

`/session/events` retains `{thread_id, after, events, last_seq, run_active}`.
Each event is the recorder envelope `{host_seq, recorded_at, kind, meta, event}`;
the panel reads that typed envelope. Invalid cursors return 400, storage read
failures return 500, and an empty suffix retains the supplied cursor. Pending
cards use live ApprovalRequest fields (`ID`, `Title`, `Details`, `Attempt`), and
`Observe`/`OnRunEnd` maintain and clear the original responders. Invalid answers
do not discard a still-pending request. Questions accept `choice` or free `text`;
a structured `answer` without either is rejected explicitly.

Projection version 2 uses a RUN_STARTED input seed for the submitted user and
run-scoped provider message/tool IDs. Existing draft version-1 records require
an explicit migration; see [the AG-UI contract](../../../docs/agui.md).


For direct assistant-ui, persistent Thread checkpoints and opt-in Snapshot/Observe
joining, see [`../assistant-ui`](../assistant-ui). This example retains its
CopilotKit-owned AG-UI client/core 0.0.57 pins: forcing the direct client 1.0.3
transitively is not a trivial compatible upgrade. The Go session endpoint is
shared; the direct examples exercise the published AG-UI 1.0 client separately.
