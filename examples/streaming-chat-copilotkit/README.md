# streaming-chat-copilotkit

完整的 **AG-UI + CopilotKit** 前端 example，演示 `agent-adaptor` 的四条纵向通路：

1. **Streaming**：文本、thinking、tool_call 全部 token 级流式
2. **HITL v2**：`dec.plan_review.*` / `dec.question.*` / `dec.permission.*` 在 UI 里渲染成可点击卡片，宿主通过 `POST /decision/resolve` 回填
3. **Recovery**：每个浏览器有稳定 `thread_id`，右侧面板通过 `/session/events` + `/decision/pending` 恢复历史与未决决策
4. **User-prompt persistence**：`sdk.Start` 拿到的 user prompt 不会自动进任何 SDK 持久层（`SessionStore` 只存 resume 元数据；adapter 永远 emit assistant 侧）。本 example 演示了 canonical 做法——`handleAgent` 用 `input.UserTurnPayloads(handle.RunID())` 把 user turn 构造成 `text.*{Role:RoleUser}` 三段，让它穿过和 driver 事件**完全相同**的 fan-out（`appendHistory` + `Translator` + SSE）。刷新页面拉 `/session/events` 即可拿回完整 transcript，含 user 气泡。<br>实现入口：`server.go::handleAgent` + `agui_run_session.go::recordUserTurn`；验证用例：`agui_run_session_test.go::TestAGUIRunSessionRecordsUserTurnBeforeAssistant`。详细原理与边界见 [`docs/workstream-user-message-event.md`](../../docs/workstream-user-message-event.md)。

后端只调用真实本机 CLI：

| `AGUI_AGENT` | 行为 |
| --- | --- |
| `codex` | 本地 `codex`，streaming 走 codex app-server |
| `claude` | 本地 `claude` / `trpc-claudecode`，PlanReview / Question 可进入 HITL 交互 |
| `cursor` | 本地 Cursor Agent CLI（默认命令 `agent`，也会尝试 `cursor-agent`） |

## Quick Start

```bash
./examples/streaming-chat-copilotkit/start-all.sh codex
./examples/streaming-chat-copilotkit/start-all.sh claude
./examples/streaming-chat-copilotkit/start-all.sh cursor
```

打开 http://localhost:3000 。

也可以只起后端：

```bash
./examples/streaming-chat-copilotkit/start.sh claude
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
  │     ├─ sdk.Start(...)  →  agent-adaptor SDK
  │     ├─ handle.StreamEvents()  →  AG-UI Translator  →  SSE
  │     └─ handle.DecisionRequests()  →  pending store
  │
  └─ 直接 fetch (旁路) —— 用于 HITL & 恢复
         GET  /session/events?thread_id=T&after=N
         GET  /decision/pending?thread_id=T
         POST /decision/resolve
```

## 前置条件

- Go 1.23+
- Node.js 20+ + npm
- 选用的 `codex` / `claude` / `cursor` CLI 已安装、已登录，并且 `--help` 可运行

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
| `ADDR` | `:8080` | backend 监听地址 |
| `CORS_ORIGIN` | `*` | 允许的前端 Origin |
| `THREAD_STORE_DIR` | unset | 设置后使用 JSONL 持久化 session recorder |

前端：

| 名称 | 默认 | 说明 |
| --- | --- | --- |
| `AGENT_BACKEND_URL` | `http://localhost:8080/agent` | CopilotRuntime 转发的 AG-UI 端点 |
| `NEXT_PUBLIC_AGENT_BACKEND_BASE` | `http://localhost:8080` | 浏览器旁路请求 base URL |

## Visual subagent delegation hook

The backend can render remote A2A delegation progress in the same CopilotKit
AG-UI stream by wiring `sse.Options.SubagentBus` (or the lower-level
`subagentstream.WrapAGUI`) to the run session. A host-owned `delegate_to_agent`
MCP server publishes `a2adelegation.DelegationEvent` values while the tool call
is blocked on the remote task. CopilotKit receives those updates as AG-UI
`CUSTOM` events named `subagent.started`, `subagent.text.delta`,
`subagent.artifact`, `subagent.finished`, etc.; Claude/Codex/Cursor receives only
the final structured MCP result.

This example's current main path still starts one local parent CLI. To prove the
visual delegation product path, run a Codex A2A bridge separately (see
`examples/a2a-local` and `docs/a2a.md`), register its Agent Card in an
`a2adelegation.Registry`, start an HTTP `a2adelegation.MCPServer` for each parent
run, and expose that server through runtime-backed MCP metadata:

```text
agentadaptor.mcp.enabled=true
agentadaptor.mcp.key=a2a-delegation
agentadaptor.mcp.transport=http
agentadaptor.mcp.url=http://127.0.0.1:<port>/mcp
agentadaptor.mcp.bearer_token_env_var=A2A_DELEGATION_TOKEN
```

`agentadaptor.mcp.enabled=true` is the promotion switch. Other optional keys
include `agentadaptor.mcp.headers_json`, `agentadaptor.mcp.args_json`,
`agentadaptor.mcp.env_json`, `agentadaptor.mcp.required`, and
`agentadaptor.mcp.required_reason`. The SDK validates these as ordinary MCP
config; malformed JSON or duplicate MCP keys fail the run before the parent
adapter starts.

The important boundary for UI authors: render the `subagent.*` custom events as
a nested visual group, but do not add them to the chat transcript as parent model
messages. `subagent.*` event values may include `parentToolCallId` when the host
can correlate the delegation sidecar with a parent provider tool-use ID, but UI
code should not require it. Use `delegationId` as the stable nested group key,
with `runId` as the run scope. The parent transcript should contain the normal
tool call plus the concise final delegation result only.

## 相关文档

- [`docs/workstream-user-message-event.md`](../../docs/workstream-user-message-event.md)
- [`docs/workstream-hitl-v2.md`](../../docs/workstream-hitl-v2.md)
- [`docs/workstream-session-recorder.md`](../../docs/workstream-session-recorder.md)
- [`docs/run-policy.md`](../../docs/run-policy.md)
- [`docs/streaming-adapter-contract.md`](../../docs/streaming-adapter-contract.md)
