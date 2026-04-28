# Examples

本目录里的 runnable examples 都走真实的本机 CLI，不再内置模拟 agent 或替身 verifier。多数示例支持在 `codex` / `claude` / `cursor` 之间切换；`codex-profile-full` 是完整 profile materialization demo，三种 agent 走同一套 SDK binding-level resources；`session-codec-inspect` 是静态 inspection 工具，只切换 adapter，不启动 CLI。

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

Smoke checklist：

- 已验证本机 smoke：`go run ./examples/profile-resources -agent=codex -timeout=2m`
- 预期输出：`before_sync` 里 resources 仍是 desired / not_materialized，`after_sync` 里 resources 变成 managed / native_managed / file_managed，随后两次 `Run` 都成功
- 当前环境缺口：`go run ./examples/profile-resources -agent=claude -timeout=1m` 停在 `Not logged in`
- 当前环境缺口：`go run ./examples/profile-resources -agent=cursor -timeout=1m` 停在 `no healthy local cursor CLI command found`
- 以上两个失败是环境问题，不代表 agents / hooks / instructions / config 这组资源未落地

### `codex-profile-full`

完整 profile demo：只用 binding-level defaults 初始化选定 agent，把 MCP、hooks、instructions、skills、subagent 全部配置进隔离 provider profile，并打印真实落盘证据。默认 `-profile-mode=dedicated` 会通过 `WithCloneProfile(..., CloneProfileOptions{IncludeSettings:true, AuthMode:CloneProfileAuthLink})` 创建隔离 profile，从本机 provider profile 克隆 settings/config，并共享本机登录态；默认 probe 会尽量用 provider CLI 读取该 profile，同时直接验证 MCP server 与 hook command；`-run=true` 时会调用 `sdk.Run(ctx, prompt)`，不传任何 run option。

```bash
go run ./examples/codex-profile-full -agent=codex -run=false
go run ./examples/codex-profile-full -agent=codex -run=true -timeout=3m
go run ./examples/codex-profile-full -agent=claude -run=false -probe=true
go run ./examples/codex-profile-full -agent=cursor -run=false -probe=true
```

注意：`CloneProfileAuthLink` 优先创建 symlink；如果平台不允许 symlink，会退到 hardlink；两者都失败时 SDK 会直接报错，避免静默复制 OAuth refresh token 副本。`-run=true` 需要选定的 provider CLI 已安装且本机已登录；否则 sync/probe 可以证明 profile 落地，但真实模型运行会停在 provider 自己的登录或 CLI 可用性错误。

Provider profile 文件布局：

- Codex：`config.toml` 写 MCP，`hooks.json` 写 hook，`AGENTS.md` 写 instructions，`agents/profile-reviewer.toml` 写 subagent
- Claude Code：`.claude.json` 写 MCP，`settings.json` 写 hook，`CLAUDE.md` 写 instructions，`agents/profile-reviewer.md` 写 subagent
- Cursor：`mcp.json` 写 MCP，`hooks.json` 写 hook，`agents/profile-reviewer.md` 写 subagent；instructions 先落到 profile fallback `.agent-adaptor/instructions/full-profile-demo.md`，真实 `Run` 时同步到 workspace `.cursor/rules/full-profile-demo.mdc`

Sync-only 输出应能看到：

- provider 对应 MCP config 里的 `profile-demo`
- provider 对应 hooks config 里的 `SessionStart` hook
- provider 对应 instructions 文件里的 `AGENT_ADAPTOR_PROFILE_DEMO_INSTRUCTIONS`
- provider 对应 agents 文件里的 `profile-reviewer` subagent
- `skills/profile-observer` skill link/copy
- `materialized_files.auth` 的 redacted shared-auth evidence
- `.agent-adaptor-profile-manifest.json` 里的 managed ownership
- `local_profile_probes.provider_mcp_inventory.contains_profile_demo = true`（provider CLI 支持 inventory probe 时）
- `local_profile_probes.provider_prompt_input.contains_instruction_token = true`（Codex 当前支持 prompt-input probe）
- `local_profile_probes.mcp_server_rpc.contains_mcp_ok = true`
- `local_profile_probes.hook_command_probe.log.content` 里的 `PROFILE_HOOK_DEMO_OK`
- `-run=true` 后的 `runtime_artifacts.mcp_log` / `hook_log` 能证明模型会话实际触发了 MCP 和 hook

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
