# streaming-chat-copilotkit

完整的 **AG-UI + CopilotKit** 前端 example，演示 `agent-adaptor` 的三条纵向通路：

1. **Streaming**：文本、thinking、tool_call 全部 token 级流式
2. **HITL v2**（[`docs/workstream-hitl-v2.md`](../../docs/workstream-hitl-v2.md)）：`dec.plan_review.*` / `dec.question.*` / `dec.permission.*` 在 UI 里渲染成可点击卡片，宿主通过 `POST /decision/resolve` 回填
3. **Recovery**（§4.3.1）：每个浏览器有稳定 `thread_id`，右侧面板实时拉取 `/session/events` + `/decision/pending`，刷新页面后历史 & 未决决策都能恢复

后端支持三种 driver：

| `AGUI_AGENT` | 行为 |
|---|---|
| `mock` | 内置的 `hitlmock` adapter，**真正** 阻塞在 `sink.RequestDecision` 上，能完整演示 HITL 决策闭环（无需本地 CLI，推荐上手用） |
| `codex` | 调本地 `codex app-server`；Phase 2 未实施，所有 HITL 走 `AutoApprove` 观测路径 |
| `claude` | 调本地 `trpc-claudecode`。**Phase 3 已实施**：`ExitPlanMode` / `AskUserQuestion` 会真正暂停 CLI 等用户点卡片，点 Approve 后 claude 继续推进 (见 [`docs/workstream-hitl-claude-phase3.md`](../../docs/workstream-hitl-claude-phase3.md))。注意：其它工具 (Bash/Edit/Write) 走 Phase 1 观测路径——`Permission=AutoApprove` 让 CLI 自己执行 |

## 架构

```
Browser
  ├─ <CopilotChat/>  (React, @copilotkit/react-ui)
  │     │  POST /api/copilotkit
  │     ▼
  │  Next.js CopilotRuntime + HttpAgent
  │     │  POST /agent  (AG-UI RunAgentInput)
  │     ▼
  │  Go backend (main.go)
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

- Go 1.23+（后端）
- Node.js 20+ + npm（前端）
- 跑 `codex` / `claude` 模式时需要本地有对应 CLI

跑 `mock` 模式不需要任何 CLI——完全自包含。

## 快速开始

```bash
# 推荐：跑内置 mock adapter，能点完整 HITL 闭环
./examples/streaming-chat-copilotkit/start-all.sh mock

# 或指定真实 backend
./examples/streaming-chat-copilotkit/start-all.sh codex
./examples/streaming-chat-copilotkit/start-all.sh claude
```

打开 http://localhost:3000 。

## mock 模式能看到什么

在输入框键入关键词触发对应 HITL 场景：

| 输入关键词 | 演示的 HITL 类别 | 卡片 |
|---|---|---|
| "plan the migration" | `HumanDecisionPlanReview` | **Plan review** 卡 + Approve / Reject |
| "ask me a question" | `HumanDecisionQuestion` | **Question** 卡 + 选项按钮 / 文本输入 |
| "run bash" | `HumanDecisionPermission` | **Permission** 卡 + Allow / Deny |
| 其他 | 纯文本流 | — |

点击卡片按钮 → 浏览器 POST `/decision/resolve` → 后端调 `handle.ResolveDecision` → adapter 被唤醒继续推进 → 看到后续文本流。

右侧 **Session 面板** 实时显示：

- 当前线程的全部 `StreamPayload` 历史（按 `Seq` 升序）
- 未解决的 `DecisionRequest`（刷新页面后仍能看到）

刷一下页面 → localStorage 里的 `thread_id` 仍在 → 右侧面板从 `/session/events` 拉到完整历史，从 `/decision/pending` 拉到等你处理的决策，直接点卡片就能继续。

## 后端 HTTP 端点

| 路径 | 方法 | 用途 |
|---|---|---|
| `/agent` | POST | AG-UI RunAgentInput → SSE 流（CopilotRuntime 使用） |
| `/session/events` | GET | 历史事件重放（`?thread_id=T&after=N`） |
| `/decision/pending` | GET | 未解决的决策请求（`?thread_id=T`） |
| `/decision/resolve` | POST | `{ run_id, request_id, result, choice?, answer?, text? }` |
| `/health` | GET | 就绪检查 |

所有端点带 CORS，允许 `CORS_ORIGIN`（默认 `*`）跨域直连。

## 前端关键文件

```
web/
├── app/
│   ├── layout.tsx                 # <CopilotKit runtimeUrl="/api/copilotkit" agent="codex">
│   ├── page.tsx                   # 主页面：chat + SessionPanel + useCopilotAction 路由
│   ├── api/
│   │   └── copilotkit/route.ts    # Next.js CopilotRuntime 端点（代理到 Go 后端）
│   ├── lib/
│   │   └── backend.ts             # fetch /session/events / /decision/pending / /decision/resolve
│   └── components/
│       ├── cards.tsx              # PlanReviewCard / QuestionCard / PermissionCard / ToolCallCard
│       └── session-panel.tsx      # 右侧恢复面板
```

`useCopilotAction({ name: "*" })` 作为 **catch-all** 接住所有 tool_call。按 `name` 前缀分发：

- `dec.plan_review.*` → `PlanReviewCard`
- `dec.question.*` → `QuestionCard`
- `dec.permission.*` → `PermissionCard`
- 其他（如 `Bash` / `ExitPlanMode`） → 通用 `ToolCallCard`

## 环境变量

Backend：

| 名称 | 默认 | 说明 |
|---|---|---|
| `AGUI_AGENT` | `codex` | `codex` / `claude` / `mock` |
| `ADDR` | `:8080` | backend 监听地址 |
| `CODEX_MODEL` | `gpt-5.4` | codex 模式模型 |
| `CLAUDE_CODE_MODEL` | `claude-sonnet-4-6` | claude 模式模型 |
| `CORS_ORIGIN` | `*` | 允许的前端 Origin |

前端：

| 名称 | 默认 | 说明 |
|---|---|---|
| `AGENT_BACKEND_URL` | `http://localhost:8080/agent` | CopilotRuntime 转发的 AG-UI 端点 |
| `NEXT_PUBLIC_AGENT_BACKEND_BASE` | `http://localhost:8080` | 浏览器旁路请求（/session/events 等）的 base URL |

## 生产化 checklist

这份 example 为了易读简化了很多实现，上线前至少要关注：

- **持久化**：当前 `threadStore` 是内存 map，重启即丢。生产用 Redis / Postgres 替换 `threadStore`，`(run_id, seq)` 作主键做去重
- **sticky routing**：handle 不能跨进程。多 pod 部署时 `/decision/resolve` 必须 sticky-by-thread_id 到拥有该 handle 的 pod
- **认证**：所有端点当前对 `CORS_ORIGIN` 全开放；生产加 bearer / cookie
- **decision audit**：Session 面板只做 UX；审计链路应把所有 `StreamHITLRequested` / `StreamHITLResolved` 双写到审计存储（或单独起一个 `StreamEvents()` 消费者）

## 相关文档

- [`docs/workstream-hitl-v2.md`](../../docs/workstream-hitl-v2.md) — HITL v2 设计全集
- [`docs/run-policy.md`](../../docs/run-policy.md) — RunPolicy / HumanDecisionPolicy 合同
- [`docs/streaming-adapter-contract.md`](../../docs/streaming-adapter-contract.md) — adapter 实施契约
- CopilotKit: https://docs.copilotkit.ai
- AG-UI 协议: https://docs.ag-ui.com
