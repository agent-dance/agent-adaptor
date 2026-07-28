# Examples

这些示例只使用 v1 的最终根包 API。除 `threads/codec` 和部分 profile 同步模式外，它们都会调用本机真实的 Codex、Claude、Cursor 或 CodeBuddy CLI；请先安装并登录对应 CLI。

通用选择参数：

```bash
go run ./examples/quickstart -agent=claude
go run ./examples/quickstart -agent=cursor -command=/absolute/path/to/agent
```

也可通过 `AGENT_ADAPTOR_EXAMPLE_AGENT`、`CODEX_COMMAND`、`CLAUDE_COMMAND`、`CURSOR_COMMAND`、`CODEBUDDY_COMMAND` 及对应的 `*_MODEL` 环境变量选择 provider。

## Core

- `quickstart`：最小 `Driver → Agent → Run → Result` 路径。
- `structured-output`：用 `adaptor.RunAs[T]` 推导 schema、校验并解码类型化结果。
- `inspect`：构造多个 Agent，读取 Environment、Models、ConfigSchema、Quota、Skills 等只读探针。
- `threads`：continue-or-start、resume-only、start-new 和 fork。
- `threads/codec`：不启动 CLI，查看 Driver 的 SessionCodec 与稳定 fingerprint 输入。
- `skills`：skill 发现、选择、单次 required skill 与 approval handler。
- `profiles`：完整 profile materialization；保留 `hook/` 与 `mcpserver/` 辅助进程。
- `profiles/resources`：profile-scoped resources 与 per-call 覆盖。
- `streaming`：一条 typed Event 流、Result 收尾与取消。
- `streaming/chat`：基于 Thread 的 typed-event 终端聊天；文本粒度取决于 Driver capability。

## Protocol bridges

- `web-chat`：最小 AG-UI SSE server 与原生浏览器页面。
- `web-chat/aguiclient`：Vite + `@ag-ui/client` 直连 Go SSE bridge。
- `web-chat/copilotkit`：CopilotKit、approval responder 与 typed Event recorder。
- `a2a-server`：本机 Agent 的 A2A server、streaming client 与 contextID→Thread 映射。

`adapter.stream.v1` 是已冻结的 wire schema 名称；A2A 示例中的 `DecodeAdapterEventV1` 因此有意保留 `V1`，它不是迁移期 Go API。

## Showcase

`showcases/team-agent-workflow` 是 live-only、多 Agent 委派示例，会产生多次付费模型调用，因此不进入自动 smoke。

## Smoke

```powershell
./examples/run_examples.ps1 -Agent codex
```

runner 总会先执行不启动 CLI 的 `threads/codec`。若所选 CLI 的 `--help` 不健康，其余 live 示例会明确跳过；健康时会按同一 provider 执行所有非 server 示例。`profiles -run=false -probe=false` 只同步并检查本地 profile，不调用模型。

所有目录可统一做编译与 fake 测试：

```bash
go test ./examples/...
go test ./examples/web-chat/copilotkit
```
