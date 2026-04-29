# Examples

5 个 spotlight examples，每个回答一个宿主集成决策问题。所有示例都通过真实的本机 CLI 运行，并支持在 `codex` / `claude` / `cursor` 之间切换。

## Prerequisites

- Go toolchain installed
- 选用的本机 CLI 已安装、已登录，并且 `--help` 能在当前 shell 中成功运行
- 默认命令：
  - `codex` -> `codex`
  - `claude` -> `claude`，找不到时也会尝试 `trpc-claudecode`
  - `cursor` -> `agent`，找不到时也会尝试 `cursor-agent`

通用选择方式：

```bash
go run ./examples/quickstart-cli -agent=claude
go run ./examples/quickstart-cli -agent=cursor -command=/absolute/path/to/agent

AGENT_ADAPTOR_EXAMPLE_AGENT=cursor go run ./examples/web-chat-stream -mode=cli
CODEX_MODEL=gpt-5.4 CLAUDE_MODEL=claude-sonnet-4 CURSOR_MODEL=gpt-5 go run ./examples/quickstart-cli
```

可用环境变量：

| Env | Purpose |
| --- | --- |
| `AGENT_ADAPTOR_EXAMPLE_AGENT` | 默认 agent：`codex` / `claude` / `cursor` |
| `CODEX_COMMAND` / `CLAUDE_COMMAND` / `CURSOR_COMMAND` | 覆盖本机 CLI 命令 |
| `CODEX_MODEL` / `CLAUDE_MODEL` / `CURSOR_MODEL` | 覆盖默认模型 |
| `AGUI_AGENT` / `AGUI_MODEL` | AG-UI examples 的 agent / model 覆盖 |

## Spotlight Matrix

| spotlight | 宿主对位场景 | 跑命令 | 走查 |
| --- | --- | --- | --- |
| [`quickstart-cli`](./quickstart-cli) | deploy-bot / CI step / postcommit hook —— 喂一个 prompt 拿一段文本就走 | `go run ./examples/quickstart-cli -agent=codex` | [`walkthrough.md`](./quickstart-cli/walkthrough.md) |
| [`web-chat-stream`](./web-chat-stream) | Web IDE / CopilotKit / 客服坐席 —— token 一个一个吐 + 同 sessionKey 续聊 | `go run ./examples/web-chat-stream -mode=cli -agent=codex` | [`walkthrough.md`](./web-chat-stream/walkthrough.md) |
| [`multi-agent-platform`](./multi-agent-platform) | 内部 dev platform / 多租户 SaaS / 团队级 AI ops 后台 | `go run ./examples/multi-agent-platform` | [`walkthrough.md`](./multi-agent-platform/walkthrough.md) |
| [`human-in-the-loop`](./human-in-the-loop) | 合规审批 / PR auto-fix / IT 变更控制 | `go run ./examples/human-in-the-loop -agent=claude` | [`walkthrough.md`](./human-in-the-loop/walkthrough.md) |
| [`task-recipes`](./task-recipes) | incident hotfix / scheduled review / 数据迁移 / 客服分流 | `go run ./examples/task-recipes -agent=codex` | [`walkthrough.md`](./task-recipes/walkthrough.md) |

每行的"走查"链接都指向 spotlight 同目录下的 `walkthrough.md`，五段结构稳定（对位场景 / 一条命令 / 终端产物 / 文件系统产物 / 落到我家产品的哪里）。

## Each Spotlight in Detail

### 1. `quickstart-cli`

- **宿主对位场景**：deploy-bot / CI step / postcommit hook / `git ai-fix` —— 喂一个 prompt 拿一段文本结果就走的脚本类产品。
- **回答的问题**：30 秒能不能跑起来？`Output` / `Summary` / `RawStreams` / `Transcript` 各长什么样？
- **跑命令**：

  ```bash
  go run ./examples/quickstart-cli -agent=codex
  ```

- **主要产物**：四联屏（Output / Summary / RawStreams / Transcript） + `.spotlight/quickstart-cli/quickstart-cli.json` + [`30-second-recipe.md`](./quickstart-cli/30-second-recipe.md)。详见 [`quickstart-cli/walkthrough.md`](./quickstart-cli/walkthrough.md) §3 / §4。
- **它故意不展示什么**：流式 / session / 多 agent / Admin 控制面 / HITL / 任务剧本——后续 spotlight 各有归属。

### 2. `web-chat-stream`

- **宿主对位场景**：Web IDE / Cursor-like 聊天面板 / CopilotKit / 客服坐席助手 / 内部 review 助手。
- **回答的问题**：把 SDK 暴露成给 React 前端打字的端点要写多少代码？多轮对话怎么续？
- **跑命令**：

  ```bash
  go run ./examples/web-chat-stream -mode=cli -agent=codex     # 进 smoke runner
  go run ./examples/web-chat-stream -mode=server -agent=codex  # 浏览器演示用
  ```

  `-mode=cli` 进 smoke runner；`-mode=server` 是手测 / 演示用，不进 smoke。

- **主要产物**：CLI 模式 token 逐字打字 transcript + Round 2 `[session reused: ...]` 续聊证据 + `.spotlight/web-chat-stream/sse-capture.ndjson`；server 模式浏览器三连测试（打字 / 续聊 / 关闭 tab 再开续聊）。详见 [`web-chat-stream/walkthrough.md`](./web-chat-stream/walkthrough.md) §3。
- **它故意不展示什么**：HITL 决策 / 多 driver 路由 / 任务剧本 / Admin 全套 read-only API——前后 spotlight 各有归属。

### 3. `multi-agent-platform`

- **宿主对位场景**：内部 dev platform / 多租户 SaaS / 团队级 AI ops 后台 —— 一个进程里挂多 driver、按场景路由、每个调用方独立身份独立 profile。
- **回答的问题**：多 driver 路由 + 多租户身份 + 控制面长什么样？运维后台能看到什么字段？
- **跑命令**：

  ```bash
  go run ./examples/multi-agent-platform \
      -default-agent=codex -review-agent=claude -autopilot-agent=cursor
  ```

  flag 默认值就是 `codex / claude / cursor`，不传也行；任一不 healthy 时该 named agent 自动 SKIP，不 panic。

- **主要产物**：运维报表表格（`Agents Overview`） + 同 prompt 路由对比卡 + 三个 named agent 的 clone profile 目录树 + `.spotlight/multi-agent-platform/admin-snapshot.json`（合并 `Agents` / `CheckEnvironment` / `ListModels` / `GetProfile` / `ConfigSchema` / `GetQuota` / `ListSkills`） + selection 隔离证据。详见 [`multi-agent-platform/walkthrough.md`](./multi-agent-platform/walkthrough.md) §3。
- **它故意不展示什么**：流式 / HITL / 任务剧本细节——前后 spotlight 各有归属。

### 4. `human-in-the-loop`

- **宿主对位场景**：金融 / 医疗 / 合规审批 / PR auto-fix bot / IT 变更控制 —— agent 想跑 shell / 调外部 API / 写文件，必须先问真人。
- **回答的问题**：危险操作怎么 ask user？拒 / 同意 / 超时分别长什么样？审计在哪？三家 driver 哪些 Ask 真支持？
- **跑命令**：

  ```bash
  go run ./examples/human-in-the-loop -agent=claude \
      -decision-timeout=6s -fake-front-end-delay=2s
  ```

  切到 `-agent=codex` / `-agent=cursor` 也能跑——三幕变成 SKIP，capability matrix 仍然出，理由清楚指向 driver 真值。

- **主要产物**：三幕话剧（`Sync Reject` / `Async Approve` / `Timeout Abort`） + capability matrix（三家 driver 真值表） + `.spotlight/human-in-the-loop/audit/session.ndjson`（每次决策一行）+ [`audit-schema.md`](./human-in-the-loop/audit-schema.md)（审计字段 schema 文档）。详见 [`human-in-the-loop/walkthrough.md`](./human-in-the-loop/walkthrough.md) §3。
- **它故意不展示什么**：profile resources / 流式 server / 多 named agent——前后 spotlight 各有归属。

### 5. `task-recipes`

- **宿主对位场景**：incident hotfix bot / 定期 code review / 数据迁移工作流 / 夜间安全扫描 / 客服分流 —— 产品里有 N 个固化任务，每个任务对应一组 instructions + skills + agents + hooks + config。
- **回答的问题**：怎么把"任务剧本"声明出来？默认剧本和特定剧本怎么叠加？宿主写一条新剧本要改哪几行？
- **跑命令**：

  ```bash
  go run ./examples/task-recipes -agent=codex
  ```

- **主要产物**：剧本卡片（`+` / `↻` 区分 additive vs replace）+ ProfileSnapshot diff + clone profile 目录前后对照树 + [`recipes.go`](./task-recipes/recipes.go)（直接复制走的剧本字典，仅 import `agentadaptor` 公开包）+ [`recipes-cookbook.md`](./task-recipes/recipes-cookbook.md)（6 条剧本范式）。详见 [`task-recipes/walkthrough.md`](./task-recipes/walkthrough.md) §3。
- **它故意不展示什么**：HITL / 流式 / 多 agent / Admin 全套 read-only API——前面 spotlight 各有归属。

## 延伸阅读

下面两个目录是前端框架接入参考，不是 spotlight：

### `streaming-chat-copilotkit`

CopilotKit + AG-UI demo。

```bash
./examples/streaming-chat-copilotkit/start-all.sh codex
./examples/streaming-chat-copilotkit/start-all.sh claude
./examples/streaming-chat-copilotkit/start-all.sh cursor
```

详见 [`streaming-chat-copilotkit/README.md`](./streaming-chat-copilotkit/README.md)。

### `streaming-chat-aguiclient`

Vite + React + `@ag-ui/client`，浏览器直连 Go backend，不经过 CopilotKit Runtime。

```bash
./examples/streaming-chat-aguiclient/start-all.sh codex
./examples/streaming-chat-aguiclient/start-all.sh claude
./examples/streaming-chat-aguiclient/start-all.sh cursor
```

详见 [`streaming-chat-aguiclient/README.md`](./streaming-chat-aguiclient/README.md)。

## 内部回归用例

[`examples/internal/`](./internal) 下是 SDK 自身的回归用例与共享工具（`exampleutil/`、`skills/`、`session-codec-inspect/`），不面向宿主，也不进入 spotlight 列表。

## Smoke Runner

PowerShell runner 会先检查选定 CLI 的 `--help`，不健康就整体 skip；健康时按同一个 agent 跑全部 5 个 spotlight 的非 server 路径，对应 `examples/run_examples.ps1` 里的：

1. `quickstart-cli`
2. `web-chat-stream -mode=cli`（server 模式不进 smoke：会 hang）
3. `multi-agent-platform`
4. `human-in-the-loop`
5. `task-recipes`

```powershell
powershell -File ./examples/run_examples.ps1 -Agent codex
powershell -File ./examples/run_examples.ps1 -Agent claude
powershell -File ./examples/run_examples.ps1 -Agent cursor -Command "C:\path\to\agent.exe"
```

## Changelog

本轮重构（2026-04-29）把 14 个旧 examples 收敛成 5 个 spotlight + 2 个延伸阅读 + `internal/` 回归用例。下表是迁移速查；如果你有内部脚本 / IDE task / blog 链接写死了旧路径，按这张表改。

| 旧路径 | 新归属 | 备注 |
| --- | --- | --- |
| `examples/codex-basic` | → `examples/quickstart-cli` | 重命名 + 扩展为四联屏输出层演示 |
| `examples/codex-stream` | → `examples/web-chat-stream` (`-mode=cli`) | 合并：流式 CLI 子模式 |
| `examples/streaming-chat` | → `examples/web-chat-stream` (`-mode=cli`) | 合并：同上，去 `codex-` 前缀，统一名为 `web-chat-stream` |
| `examples/codex-sessions` | → `examples/web-chat-stream` (`-mode=cli`) | 合并：用同 sessionKey 两轮续聊承接；语义保留在 [`docs/usage-guide.md`](../docs/usage-guide.md) 的 sessions 段 |
| `examples/streaming-sse-server` | → `examples/web-chat-stream` (`-mode=server`) | 合并：HTTP SSE 子模式（极简内联 HTML 前端）|
| `examples/codex-admin-named` | → `examples/multi-agent-platform` | 重命名 + 收紧叙事到"治理面"（运维报表 / Admin 全套 read-only / clone profile 隔离）|
| `examples/codex-skills-live` | → `examples/task-recipes` | 折入：`write-proof` skill 作为最小可验证 skill 资源 |
| `examples/profile-resources` | → `examples/task-recipes` | 重构：从"功能字段拼盘"改为"任务剧本"叙事（`recipes.go` + cookbook）|
| `examples/session-codec-inspect` | → `examples/internal/session-codec-inspect` | 移出 spotlight，归档为 SDK 自身 inspection 工具 |
| `examples/mock-runtime-admin` | → `examples/internal/mock-runtime-admin` | 同上 |
| `examples/mock-adapter-playground` | → `examples/internal/mock-adapter-playground` | 同上 |
| `examples/mock-skills-contract` | → `examples/internal/mock-skills-contract` | 同上 |
| `examples/streaming-chat-copilotkit` | (保留，降级为延伸阅读) | 不再列入 spotlight；前端框架接入参考 |
| `examples/streaming-chat-aguiclient` | (保留，降级为延伸阅读) | 同上 |
| **新增** `examples/human-in-the-loop` | (新 spotlight) | 堵 HITL 治理叙事盲区（permission / plan-review / question 三类决策 + capability matrix + audit ndjson）|

`run_examples.ps1` 也按上面的迁移收敛到 5 行；旧入口（`basic` / `stream` / `sessions` / `admin-named` / `skills-live` / `profile-resources`）已删除。
