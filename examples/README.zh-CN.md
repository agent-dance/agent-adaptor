# Examples

[English](./README.md)

Examples：

- `recipes/`：小型、可复制、一次只讲一个宿主合同；
- `showcases/`：带完整启动与清理流程的端到端产品形态；
- `tools/`：检查与诊断工具，不是推荐的第一次集成；
- `internal/`：共享 live-CLI 与确定性合同支撑。

## 前置条件

所有示例都需要 Go 工具链。标记为 **live CLI** 的条目还要求对应本地 Agent
命令已安装并完成认证：

| Agent | 默认命令 | 可选覆盖 |
|---|---|---|
| Codex | `codex` | `CODEX_COMMAND`、`CODEX_MODEL` |
| Claude Code | `claude` | `CLAUDE_COMMAND`、`CLAUDE_MODEL` |
| Cursor Agent | `agent`（随后尝试 `cursor-agent`） | `CURSOR_COMMAND`、`CURSOR_MODEL` |

多数多 provider 示例支持 `-agent`、`-command`、`-model`、`-timeout`。所有运行都有 timeout；可能写文件的示例使用临时 workspace/profile。

## 学习路径

1. **第一次运行：** `basic-run` -> `result-and-failure` -> `async-events`。
2. **自动化 worker：** `session-continuity` -> `structured-output` -> `runtime-service`。
3. **交互式产品：** `content-streaming` -> `hitl-channel` -> `web-agui` 或 `web-copilotkit-hitl`。
4. **多 Agent 工作流：** `provider-selection` -> `named-agent-review` -> `a2a-local` -> `team-agent-workflow`。
5. **受控环境：** `admin-preflight` -> `skill-injection` -> `managed-profile` -> `full-profile`。
6. **Adapter 开发：** `custom-adapter` -> `session-codec-inspect` -> [`adaptertest`](../adaptertest)。

## Recipes

| Example | 运行条件 | 主要概念 | 运行命令 | 预期结果 | 生产注意事项 |
|---|---|---|---|---|---|
| [`recipes/basic-run`](./recipes/basic-run) | live CLI，Codex | 最小 public `sdk.Run` 路径 | `go run ./examples/recipes/basic-run` | stdout 输出 assistant 确认 | 使用临时 workspace/cloned profile，不依赖 internal helper，也不硬编码 model id。 |
| [`recipes/provider-selection`](./recipes/provider-selection) | live CLI，多 provider | 显式 provider binding switch 与 preflight | `go run ./examples/recipes/provider-selection -agent=claude` | 健康 driver 与一次响应 | 路由仍由宿主负责，SDK 不自动选择 Agent。 |
| [`recipes/async-events`](./recipes/async-events) | live CLI，多 provider | `Start`、运行 `Events`、`Wait`、取消 | `go run ./examples/recipes/async-events -agent=codex` | 生命周期/事件计数与最终输出 | 因同时验证成功与取消分支而略超 120 行。 |
| [`recipes/content-streaming`](./recipes/content-streaming) | live CLI，取决于 capability | `WithStreaming` 与 `StreamEvents` | `go run ./examples/recipes/content-streaming -agent=claude` | 文本 delta 与最终结果 | 同时 drain 运行事件；Cursor 当前没有 token-level stream capability。 |
| [`recipes/session-continuity`](./recipes/session-continuity) | live CLI，多 provider | continue-or-start、continue-only、start-new、fork | `go run ./examples/recipes/session-continuity -agent=codex` | created/reused/forked SessionID | 内存 store 只演示语义，不代表生产持久化。 |
| [`recipes/named-agent-review`](./recipes/named-agent-review) | live CLI，Codex + Claude | 宿主路由的实现/评审流程 | `go run ./examples/recipes/named-agent-review` | 独立 implementer/reviewer 输出 | 需要两个 CLI，不存在自动路由。 |
| [`recipes/admin-preflight`](./recipes/admin-preflight) | 本地环境 | 不执行 prompt 的控制面发现 | `go run ./examples/recipes/admin-preflight -agent=cursor` | 环境、模型、profile、schema 摘要 | 不支持的 probe 返回真实 fallback。 |
| [`recipes/result-and-failure`](./recipes/result-and-failure) | offline | `error -> Failure -> success` 与输出分层 | `go run ./examples/recipes/result-and-failure -fail` | 结构化业务失败；去掉 `-fail` 为成功路径 | 使用仅供 examples 的确定性 contract driver。 |
| [`recipes/structured-output`](./recipes/structured-output) | live CLI，多 provider | typed JSON Schema 输出与解码 | `go run ./examples/recipes/structured-output -agent=codex` | 通过校验的 `ProjectMetadata` | Cursor 使用较弱的 prompt + 本地校验 fallback。 |
| [`recipes/hitl-handler`](./recipes/hitl-handler) | offline | 同步 typed plan-review handler | `go run ./examples/recipes/hitl-handler` | handler 读取并批准 plan | Claude 是当前支持 PlanReview/Question Ask 的内置 adapter。 |
| [`recipes/hitl-channel`](./recipes/hitl-channel) | offline | 异步 `DecisionRequests` / `ResolveDecision` | `go run ./examples/recipes/hitl-channel` | RequestID 被回填，run 继续 | 长期服务还需处理过期、取消和持久化。 |
| [`recipes/skill-injection`](./recipes/skill-injection) | live CLI，多 provider | skill selection 与隔离 profile 物化 | `go run ./examples/recipes/skill-injection -agent=codex` | 临时 proof 文件内容为 `WRITE_PROOF_OK` | 因隔离和验证 profile/workspace 写入而略超 120 行。 |
| [`recipes/runtime-service`](./recipes/runtime-service) | offline | 按 `RunID` ensure、report、release | `go run ./examples/recipes/runtime-service` | ensured service report 与匹配的 release ID | 真实进程/容器编排由宿主负责。 |
| [`recipes/custom-adapter`](./recipes/custom-adapter) | offline | 最小 `DriverAdapter` + `BindTyped` | `go run ./examples/recipes/custom-adapter` | 通过同一 `Runner` 路径返回 echo | 使用 `adaptertest`，只声明真正实现的 capability。 |

## Showcases

| Example | 运行条件 | 产品形态 | 启动命令 | 预期证据 | 生产注意事项 |
|---|---|---|---|---|---|
| [`showcases/managed-profile`](./showcases/managed-profile) | live CLI，多 provider | binding defaults + per-run profile resources | `go run ./examples/showcases/managed-profile -agent=codex` | 前后 snapshot 与两次成功 run | 使用临时 workspace 与 cloned profile。 |
| [`showcases/full-profile`](./showcases/full-profile) | live profile probes；模型调用可选 | skills、MCP、hooks、instructions、agents、config | `go run ./examples/showcases/full-profile -agent=codex -run=false` | 物化文件与本地 probe 证据 | auth 链接到隔离 profile；只有 `-run=true` 才调用模型。 |
| [`showcases/web-sse`](./showcases/web-sse) | live CLI，多 provider | 宿主持有的 HTTP server + AG-UI SSE | `go run ./examples/showcases/web-sse -agent=codex -addr=:8080` | 浏览器收到 lifecycle 与 text 事件 | auth、TLS、tenant、持久化仍由宿主负责。 |
| [`showcases/web-agui`](./showcases/web-agui) | live CLI + Node 20 | React 直连 Go AG-UI backend | `./examples/showcases/web-agui/start-all.sh codex` | 流式消息与稳定 ThreadID 映射 | setup、cleanup、限制见该 showcase README。 |
| [`showcases/web-copilotkit-hitl`](./showcases/web-copilotkit-hitl) | live CLI + Node 20 | CopilotKit、session、replay、HITL 卡片 | `./examples/showcases/web-copilotkit-hitl/start-all.sh claude` | plan/question 卡片回填后 run 继续 | Claude 当前提供最完整的内置 HITL 路径。 |
| [`showcases/a2a-local`](./showcases/a2a-local) | live CLI，多 provider | 本地 A2A server + client + task polling | `go run ./examples/showcases/a2a-local -agent=codex` | Agent Card、streaming artifact、最终 task | serving、auth、task 持久化、路由由宿主负责。 |
| [`showcases/team-agent-workflow`](./showcases/team-agent-workflow) | live CLI，Claude Code + Codex | Claude leader 通过 MCP 向受控 A2A 角色派发 plan/impl/review | `go run ./examples/showcases/team-agent-workflow` | 有序 delegation 事件、通过的 workspace 检查与 `TEAM_AGENT_WORKFLOW_OK` | 在临时仓库中顺序调用四次模型；生产编排仍由宿主负责。 |

## Tools

| Tool | 运行条件 | 用途 | 运行命令 | 输出 | 注意事项 |
|---|---|---|---|---|---|
| [`tools/session-codec-inspect`](./tools/session-codec-inspect) | offline | 检查 adapter 的公共 session 参数形状 | `go run ./examples/tools/session-codec-inspect -agent=cursor` | Session codec JSON | 诊断工具，不启动 Agent CLI。 |
| [`tools/live-smoke`](./tools/live-smoke) | live CLI，多 provider | 跨平台认证 sentinel smoke | `go run ./examples/tools/live-smoke -agent=codex` | `passed`、`skipped`、`environment_failed` 或 `run_failed` JSON | 使用隔离 workspace/profile；对应 exit code 为 0、0、2、3。 |

## 验证

确定性验证：

```bash
go test ./examples/...
go run ./examples/recipes/result-and-failure
go run ./examples/recipes/hitl-handler
go run ./examples/recipes/hitl-channel
go run ./examples/recipes/runtime-service
go run ./examples/recipes/custom-adapter
```

跨平台 live smoke 会报告 `passed`、`skipped`、`environment_failed` 或
`run_failed`，不会把未登录当成通过：

```bash
go run ./examples/tools/live-smoke -agent=codex
go run ./examples/tools/live-smoke -agent=claude
go run ./examples/tools/live-smoke -agent=cursor
```

只有调用方明确禁用 live validation 时才使用 `-skip`。命令缺失、未登录或额度问题
属于 `environment_failed`，不是 `skipped`。
