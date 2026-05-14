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

## 相关文档

- [`docs/workstream-user-message-event.md`](../../docs/workstream-user-message-event.md)
- [`docs/workstream-hitl-v2.md`](../../docs/workstream-hitl-v2.md)
- [`docs/workstream-session-recorder.md`](../../docs/workstream-session-recorder.md)
- [`docs/run-policy.md`](../../docs/run-policy.md)
- [`docs/streaming-adapter-contract.md`](../../docs/streaming-adapter-contract.md)
