# Examples

本目录里的 runnable examples 都走真实的本机 CLI，不再内置模拟 agent 或替身 verifier。所有示例都支持在 `codex` / `claude` / `cursor` 之间切换；`session-codec-inspect` 是静态 inspection 工具，只切换 adapter，不启动 CLI。

## Prerequisites

- Go toolchain installed
- 选用的本机 CLI 已安装、已登录，并且 `--help` 能在当前 shell 中成功运行
- 默认命令：
  - `codex` -> `codex`
  - `claude` -> `claude`，找不到时也会尝试 `trpc-claudecode`
  - `cursor` -> `agent`，找不到时也会尝试 `cursor-agent`

通用选择方式：

```bash
go run ./examples/codex-basic -agent=claude
go run ./examples/codex-basic -agent=cursor -command=/absolute/path/to/agent

AGENT_ADAPTOR_EXAMPLE_AGENT=cursor go run ./examples/codex-stream
CODEX_MODEL=gpt-5.4 CLAUDE_MODEL=claude-sonnet-4 CURSOR_MODEL=gpt-5 go run ./examples/codex-basic
```

可用环境变量：

| Env | Purpose |
| --- | --- |
| `AGENT_ADAPTOR_EXAMPLE_AGENT` | 默认 agent：`codex` / `claude` / `cursor` |
| `CODEX_COMMAND` / `CLAUDE_COMMAND` / `CURSOR_COMMAND` | 覆盖本机 CLI 命令 |
| `CODEX_MODEL` / `CLAUDE_MODEL` / `CURSOR_MODEL` | 覆盖默认模型 |
| `AGUI_AGENT` / `AGUI_MODEL` | AG-UI examples 的 agent / model 覆盖 |

## Example Matrix

### `codex-basic`

最短路径：构造默认 agent，然后 `sdk.Run(...)`。

```bash
go run ./examples/codex-basic -agent=codex
go run ./examples/codex-basic -agent=claude
go run ./examples/codex-basic -agent=cursor
```

### `codex-stream`

异步执行、`RunHandle.Events()` 消费，以及可选取消。

```bash
go run ./examples/codex-stream -agent=claude
go run ./examples/codex-stream -agent=cursor -cancel-after=2s
```

### `codex-sessions`

验证 `WithSessionKey` / `WithContinueSession` / `WithNewSession` / `WithForkSession` 的服务宿主语义。

```bash
go run ./examples/codex-sessions -agent=codex
```

### `codex-admin-named`

默认 agent + 命名 `review` agent，并跑 Admin 控制面：`Agents`、`CheckEnvironment`、`ListModels`、`GetProfile`、`ConfigSchema`、`GetQuota`、`ListSkills`、`SetSelectedSkills`。

```bash
go run ./examples/codex-admin-named -agent=claude
```

示例会 clone 本机 profile 到临时目录，避免把示例技能写进用户真实 profile。

### `codex-skills-live`

真实技能注入体验：把 `examples/internal/skills/write-proof` 注入选定 CLI 的临时 cloned profile，然后要求 agent 在临时 workspace 写出 proof 文件。

```bash
go run ./examples/codex-skills-live -agent=cursor
```

Pass 条件：真实 CLI 运行成功，`ListSkills` / `SetSelectedSkills` 返回有效状态，并创建内容为 `WRITE_PROOF_OK` 的 proof 文件。

### `profile-resources`

展示宿主视角如何配置 profile-scoped resources：`skills`、`agents`、`hooks`、`instructions`、`config`，并演示 binding-level 默认值与 per-run `WithProfileResources(...)` 覆盖。

```bash
go run ./examples/profile-resources -agent=codex
```

示例会：

- clone 本机 profile 到临时目录
- 写入 instructions / agent source 文件
- `Admin().Default().ProfileSnapshot()` 查看默认 desired state
- `Admin().Default().SyncProfile()` 同步默认 profile
- 用默认 resources 跑一次真实 CLI
- 用 per-run resources 覆盖后再跑一次真实 CLI

### `streaming-chat`

纯 Go 消费 `RunHandle.StreamEvents()`。

```bash
go run ./examples/streaming-chat -agent=claude -prompt="Write a haiku about streaming"
```

### `streaming-sse-server`

最小 HTTP SSE server，把 SDK streaming surface 暴露为 AG-UI SSE。

```bash
go run ./examples/streaming-sse-server -agent=cursor -addr=:8080
# open http://localhost:8080
```

### `streaming-chat-copilotkit`

CopilotKit + AG-UI demo。

```bash
./examples/streaming-chat-copilotkit/start-all.sh codex
./examples/streaming-chat-copilotkit/start-all.sh claude
./examples/streaming-chat-copilotkit/start-all.sh cursor
```

See [`streaming-chat-copilotkit/README.md`](./streaming-chat-copilotkit/README.md).

### `streaming-chat-aguiclient`

Vite + React + `@ag-ui/client`，浏览器直接调用 Go backend，不经过 CopilotKit Runtime。

```bash
./examples/streaming-chat-aguiclient/start-all.sh codex
./examples/streaming-chat-aguiclient/start-all.sh claude
./examples/streaming-chat-aguiclient/start-all.sh cursor
```

See [`streaming-chat-aguiclient/README.md`](./streaming-chat-aguiclient/README.md).

### `session-codec-inspect`

静态 inspection utility，用来查看某个 adapter 的 session codec 参数形状；它不启动本机 CLI。

```bash
go run ./examples/session-codec-inspect -agent=cursor
```

## Smoke Runner

PowerShell runner 会先检查选定 CLI 的 `--help`，不健康就整体 skip；健康时按同一个 agent 跑所有非 server examples。

```powershell
powershell -File ./examples/run_examples.ps1 -Agent codex
powershell -File ./examples/run_examples.ps1 -Agent claude
powershell -File ./examples/run_examples.ps1 -Agent cursor -Command "C:\path\to\agent.exe"
```
