# Examples

本目录里的 runnable examples 都走真实的本机 CLI，不再内置模拟 agent 或替身 verifier。多数示例支持在 `codex` / `claude` / `cursor` / `codebuddy` 之间切换；`codex-profile-full` 是完整 profile materialization demo，四种 agent 走同一套 agent-level resources；`session-codec-inspect` 是静态 inspection 工具，只切换 driver，不启动 CLI。

## v1 API

所有示例都已迁移到 v1 表面（见 `docs/api-v1-redesign.md`、`docs/migrating-to-v1.md`）。六个名词：**Agent / Thread / Stream / Event / Result / Driver**。

```go
import (
    adaptor "github.com/agent-dance/agent-adaptor/next"
    "github.com/agent-dance/agent-adaptor/codex"
    "github.com/agent-dance/agent-adaptor/skill"
    "github.com/agent-dance/agent-adaptor/mcp"
    "github.com/agent-dance/agent-adaptor/profile"
)

ai := adaptor.New(codex.Driver(codex.Config{Model: "gpt-5.4"}),
    adaptor.WithSkills(skill.Dir("./skills/write-proof")),
    adaptor.WithMCP(mcp.HTTP("docs", "https://example.com/mcp")),
    adaptor.WithProfile(profile.CloneNative(dir, profile.CopySettings(), profile.LinkAuth())),
)

res, err := ai.Run(ctx, "say hi")                  // 批量：一个 err 一个判决
stream := ai.Thread("chat/42").Stream(ctx, "...")  // 流式：动词就是开关
```

迁移速查：

| 旧 | 新 |
| --- | --- |
| `agentadaptor.New(WithDefaultAgent(...))` + `sdk.Agent("name")` | 多个 Agent = 多个 Go 变量（命名注册表已删除） |
| `sdk.Admin()` / `Admin().Agent(name)` | `ai.Inspect()`（只读面板）+ `ai.ProfileState` / `SyncProfile` / `SelectSkills`（变更动词留在 Agent 上） |
| `WithSessionKey` / `WithContinueSession` / `WithNewSession` / `WithForkSession` | `ai.Thread(key)` / `ai.Thread(key, adaptor.ResumeOnly())` / `ai.NewThread(key)` / `th.Fork(newKey)` |
| `WithStreaming()` + `handle.Events()` + `handle.StreamEvents()` + `handle.Wait()` | `Stream()` 一条 `Events()` 通道 + `Result()` |
| `RunResult.Failure` / `ExitCode` | 单一 `err`；业务失败是带完整 `Result` 的 `*adaptor.RunError` |
| `handle.DecisionRequests()` + `handle.ResolveDecision(id, resp)` | `adaptor.OnApproval(...)` 回调，或流上的 `*adaptor.ApprovalRequest` 事件自带 `Approve` / `Deny` / `Answer` |

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
| `AGENT_ADAPTOR_EXAMPLE_AGENT` | 默认 agent：`codex` / `claude` / `cursor` / `codebuddy` |
| `CODEX_COMMAND` / `CLAUDE_COMMAND` / `CURSOR_COMMAND` / `CODEBUDDY_COMMAND` | 覆盖本机 CLI 命令 |
| `CODEX_MODEL` / `CLAUDE_MODEL` / `CURSOR_MODEL` / `CODEBUDDY_MODEL` | 覆盖默认模型 |
| `AGUI_AGENT` / `AGUI_MODEL` | AG-UI examples 的 agent / model 覆盖 |

## Example Matrix

### `codex-basic`

v1 quickstart：`adaptor.New(driver, ...)` 构造 Agent，`ai.Run(ctx, prompt)` 取一个 `*Result`，业务失败用 `errors.As(err, &runErr)` 拿回带完整 Result 的 `*adaptor.RunError`。整个示例只用到三个名字。

`-structured` 把同一次调用换成它的类型化孪生（设计文档 S5）：`adaptor.RunAs[Ack](ctx, ai, prompt)` 从 Go 结构体推导 schema、协商 driver 支持的最强结构化输出模式、校验并解码——旧的 `WithJSONSchemaOutputFor[T]` + `DecodeStructuredOutput[T]` 两段式塌成一次调用，判决路径不变。`RunAs` 收 `Runner`，所以同一行对 Thread 也成立。

```bash
go run ./examples/codex-basic -agent=codex
go run ./examples/codex-basic -agent=claude
go run ./examples/codex-basic -agent=cursor
go run ./examples/codex-basic -agent=codex -structured
```

### `codex-stream`

v1 事件模型：每次运行**一条**类型化事件通道，一个 `for range` + type switch 消费，`Result()` 收尾。旧的 operational `RunEvent` 通道与 `StreamPayload` 通道合并成 `adaptor.Event`，不关心的事件直接不写 case（没有 drain 义务）。也演示 `stream.Cancel()`。

```bash
go run ./examples/codex-stream -agent=claude
go run ./examples/codex-stream -agent=cursor -cancel-after=2s
```

### `codex-sessions`

Thread 的四个动作：`ai.Thread(key)`（continue-or-start）、`ai.Thread(key, adaptor.ResumeOnly())`（continue-only，缺失时返回 `ErrThreadNotFound`）、`ai.NewThread(key)`（start-new）、`th.Fork(newKey)`。SessionID 降级为 `th.Checkpoint(ctx)` 里的 resume 元数据。

```bash
go run ./examples/codex-sessions -agent=codex
```

### `codex-admin-named`

**已 re-theme（目录名保留）**：v1 删除了命名注册表，"多个 agent" 就是多个 Go 变量；`sdk.Admin()` 变成 `ai.Inspect()`。示例现在构造 writer / reviewer 两个 Agent 变量，各自跑一遍只读面板 `Inspect().Environment / Models / ConfigSchema / Quota / Skills`，再演示留在 Agent 上的变更动词 `SelectSkills` / `ProfileState`。文件头有完整的 re-theme 说明。

```bash
go run ./examples/codex-admin-named -agent=claude
```

示例会 clone 本机 profile 到临时目录，避免把示例技能写进用户真实 profile。

### `codex-skills-live`

技能词汇包实战：`skill.Dir(...)` 注入 `examples/internal/skills/write-proof`，`ai.Inspect().Skills(ctx)` 列出、`ai.SelectSkills(ctx, keys)` 选中，再用 call-scope 的 `adaptor.WithSkills(skill.Require(..., reason))` 声明这次运行必须拿到该技能（skills 是唯一 append 而非 replace 的选项），最后要求 agent 在临时 workspace 写出 proof 文件。

同时是 **approval form A** 的示例（决策 D2）：技能要写文件，所以 `Policy.Approvals.Permission = ApprovalAsk`，由 `adaptor.OnApproval(gate.decide)` 装上宿主回调。批量 `Run` 没有流，回调是这里唯一可用的 approval 形态；请求自带 responder，所以整段代码里没有任何 request-ID 记账，也没有 `ResolveDecision` 往返——回调里直接 `req.Approve(ctx)` / `req.Deny(ctx, reason)`。输出的 `approvals` 字段是宿主自己的审计流水。

```bash
go run ./examples/codex-skills-live -agent=cursor
```

Pass 条件：真实 CLI 运行成功，`Inspect().Skills` / `SelectSkills` 返回有效状态，并创建内容为 `WRITE_PROOF_OK` 的 proof 文件。

### `profile-resources`

宿主视角配置 profile-scoped resources：一个 `profile.Resources` 值同时声明 `Skills` / `Agents`（`profile.SubAgent`）/ `Hooks`（`profile.Hook`）/ `Instructions`（结构体或 `profile.Text(...)` 一行版）/ `Config`（`profile.ConfigPatch`），并演示 construction-scope 默认值与 per-call `adaptor.WithProfileResources(...)` 覆盖（replace 语义）。

```bash
go run ./examples/profile-resources -agent=codex
```

示例会：

- `profile.CloneNative(dir, CopySettings(), LinkAuth())` clone 本机 profile 到临时目录
- 写入 instructions / agent source 文件
- `ai.ProfileState(ctx)` 查看默认 desired state（只读）
- `ai.SyncProfile(ctx)` 同步默认 profile（变更）
- 用默认 resources 跑一次真实 CLI
- 用 per-call resources 覆盖后再跑一次真实 CLI

Smoke checklist：

- 已验证本机 smoke：`go run ./examples/profile-resources -agent=codex -timeout=2m`
- 预期输出：`before_sync` 里 resources 仍是 desired / not_materialized，`after_sync` 里 resources 变成 managed / native_managed / file_managed，随后两次 `Run` 都成功
- 当前环境缺口：`go run ./examples/profile-resources -agent=claude -timeout=1m` 停在 `Not logged in`
- 当前环境缺口：`go run ./examples/profile-resources -agent=cursor -timeout=1m` 停在 `no healthy local cursor CLI command found`
- 以上两个失败是环境问题，不代表 agents / hooks / instructions / config 这组资源未落地

### `codex-profile-full`

完整 profile demo：只用 construction-scope defaults 初始化选定 agent，把 MCP、hooks、instructions、skills、subagent 全部配置进隔离 provider profile，并打印真实落盘证据。默认 `-profile-mode=dedicated` 会通过 `adaptor.WithProfile(profile.CloneNative(dir, profile.CopySettings(), profile.LinkAuth()))` 创建隔离 profile，从本机 provider profile 克隆 settings/config，并共享本机登录态；`-profile-mode=native` 走 `profile.Native()`。控制面用 `ai.ProfileState(ctx)`（只读）与 `ai.SyncProfile(ctx)`（变更）两个动词。默认 probe 会尽量用 provider CLI 读取该 profile，同时直接验证 MCP server 与 hook command；`-run=true` 时会调用 `ai.Run(ctx, prompt)`，不传任何 CallOption。

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

纯 Go 的字符级 chat UI，也是 v1 迁移里最直观的 before/after：旧版要在 goroutine 里强制 drain operational `RunEvent` 通道 **再加** `StreamPayload` 通道，最后 `Wait()`；v1 只有 `ai.Thread(key).Stream(ctx, prompt)` 一条 `Events()` 通道 + 一次 `Result()`。不处理的事件直接从 type switch 里漏过去。

```bash
go run ./examples/streaming-chat -agent=claude -prompt="Write a haiku about streaming"
go run ./examples/streaming-chat -agent=codex -thread=examples/streaming-chat
```

### `streaming-sse-server`

最小 HTTP SSE server：`sse.HandlerV1(ai, sse.OptionsV1{Protocol: sse.AGUI, ...})` 一行把 Agent 暴露成 AG-UI SSE。bridge 收的是 `adaptor.Runner`——`*Agent` 和 `*Thread` 都满足：传 Agent 时它按入站 `threadId` 自己绑到 `agent.Thread(...)`，传 Thread 则由宿主钉死会话。`OptionsV1.Options` 是 call scope，会追加到它启动的每一次 `Stream`。附带一个纯 JS 的浏览器页面。

```bash
go run ./examples/streaming-sse-server -agent=cursor -addr=:8080
# open http://localhost:8080
```

### `streaming-chat-copilotkit`

CopilotKit + AG-UI demo，也是唯一带 Go 测试的示例。宿主侧演示：一个 `for range stream.Events()` 取代旧的三条 goroutine；approval 走 D2 form B——`*adaptor.ApprovalRequest` 作为事件到达并**自带 responder**，宿主停放它、浏览器点按钮后直接 `req.Approve/Deny/Answer(ctx, ...)`，不再需要"遍历所有活跃 handle 试一遍"的兜底；历史用 `hosttools/sessionrecorder.NewEventRecorder` 按 `HostSeq` 游标重放；AG-UI 翻译用 `agui.NewEventTranslator` + `Translate` / `CloseRun(err)`。

```bash
./examples/streaming-chat-copilotkit/start-all.sh codex
./examples/streaming-chat-copilotkit/start-all.sh claude
./examples/streaming-chat-copilotkit/start-all.sh cursor

go test ./examples/streaming-chat-copilotkit   # fake Stream + 内存 recorder，不碰真实 CLI
```

See [`streaming-chat-copilotkit/README.md`](./streaming-chat-copilotkit/README.md).

### `streaming-chat-aguiclient`

Vite + React + `@ag-ui/client`，浏览器直接调用 Go backend，不经过 CopilotKit Runtime。后端在 v1 下缩成"一个 Agent 值 + 一次 bridge 调用"，与 `streaming-sse-server` 用的是同一个 `sse.HandlerV1`。

```bash
./examples/streaming-chat-aguiclient/start-all.sh codex
./examples/streaming-chat-aguiclient/start-all.sh claude
./examples/streaming-chat-aguiclient/start-all.sh cursor
```

See [`streaming-chat-aguiclient/README.md`](./streaming-chat-aguiclient/README.md).

### `a2a-local`

本地端到端 A2A demo：`a2a.NewServerV1(ai, ...)` 把真实本机 Agent 暴露为 A2A JSON-RPC；随后用 `clients/a2a` 读取 Agent Card、执行 streaming 调用，并用 `GetTask` 轮询最终任务。会话映射用 `Session: a2a.ThreadByContextID()`——每个 A2A `contextID` 成为一条自己的会话（`agent.Thread("a2a/<contextID>")`），同 context 的后续消息接着上次继续；一次性 task server 用默认的 `a2a.StatelessV1()`。A2A wire DTO 在 v1 重写中刻意保持不变，远端 peer 看到的仍是 `adapter.stream.v1` envelope。

```bash
go run ./examples/a2a-local -agent=codex
go run ./examples/a2a-local -agent=claude -prompt="Reply with one sentence"
go run ./examples/a2a-local -serve-only -addr=127.0.0.1:8080
```

默认使用临时 workspace + 临时 cloned provider profile，并把 native settings 复制到临时 profile 以支持 custom API key / base URL；auth files 通过 `CloneProfileAuthLink` 共享，避免复制 OAuth refresh token。示例不会写入宿主正在使用的 profile，不会复制 native skills/MCP 目录。默认会校验最终输出包含 `A2A demo OK`，避免把未登录提示误判为成功；可用 `-expect=` 关闭该校验。默认输出包含隔离目录、Agent Card fingerprint、streaming 状态、bridge artifact 统计、最终 task state 与 assistant 输出预览。`-serve-only` 只启动 server，方便外部 A2A client 连接 `/.well-known/agent-card.json` 与 `/a2a`。需要排查时加 `-keep-workspace` 保留临时 workspace/profile；该目录可能包含复制出的 provider settings。

### `session-codec-inspect`

v1 里消费者只看得到两层身份：宿主自己选的 thread key，和 SDK 分配的 run ID；provider session id 已经降级成 SDK 自己持久化/回放的实现细节。这个示例是那个"确实需要往下看一眼"的稽核口：`codex.Driver(...)` 等返回的 driver 值会 promote 底层 adapter 的所有可选能力接口，所以一次 `driver.SessionCodecProvider` 类型断言就能拿到 codec，查看它如何把 session 归一成稳定参数、如何推导 resume guard fingerprint。不启动任何 CLI 进程，因此可在任意环境运行（smoke runner 里排第一个）。

```bash
go run ./examples/session-codec-inspect -agent=cursor
```

### `showcases/team-agent-workflow`

**live-only、手动跑、会产生付费模型调用**（一次 leader run + 三次 role run），因此不进 `run_examples.ps1` 冒烟清单、目录下不带任何 `_test.go`、且必须显式给 `-leader=`（没有默认 agent、没有兜底）。

设计文档 §9.7 / §9.8 的团队协作形态：一个 leader Agent 带 plan（read-only）→ impl（workspace-write）→ review（read-only）三个角色，全程只有三处 SDK 构造。`delegation.NewService(...)` 一次配置就是整个委派运行时（registry + event bus + delegator + per-run MCP sidecar + 结果记录）；`delegation.Local(key, runner, policy)` 把进程内的任意 `Runner` 注册成可委派角色，所以每个角色就是一个 `*adaptor.Agent` 值加一层普通 Go decorator，不起 A2A server、不占端口；`team.Option()` 一行把服务挂到 leader 上——sidecar 作为 runtime service 带类型化 MCP 声明、生命周期绑定到本次 run、委派事件并入 leader 自己的 `Events()` 通道成为 `adaptor.SubagentUpdate`。消费端仍然是一个 `for range stream.Events()` + `Result()`，团队进度与 leader 自己的输出在同一条通道上按序到达；跑完用 `team.Result(stream.RunID(), "review")` + `HasLine(...)` 读回 review 角色的裁决。临时 workspace/TASK.md、终端渲染、workspace 阶段审计等宿主逻辑放在 `host.go`。

```bash
go run ./examples/showcases/team-agent-workflow -leader=claude
go run ./examples/showcases/team-agent-workflow -leader=claude -plan=codex -review=codex
go run ./examples/showcases/team-agent-workflow -leader=claude -keep-workspace
go run ./examples/showcases/team-agent-workflow -leader=claude -web-mode -web-addr=:8080
```

## Smoke Runner

PowerShell runner 会先检查选定 CLI 的 `--help`，不健康就整体 skip；健康时按同一个 agent 跑所有非 server examples。顺序上先跑不启动 CLI 的 `session-codec-inspect` 作为廉价 wiring smoke，最后的 `codex-profile-full` 用 `-run=false -probe=false` 只做 profile materialization，不触发付费模型调用。

```powershell
powershell -File ./examples/run_examples.ps1 -Agent codex
powershell -File ./examples/run_examples.ps1 -Agent claude
powershell -File ./examples/run_examples.ps1 -Agent cursor -Command "C:\path\to\agent.exe"
powershell -File ./examples/run_examples.ps1 -Agent codebuddy
```
