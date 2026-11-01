# Examples

这些示例使用 `github.com/agent-dance/agent-adaptor/v2`。任务从 `Send` 开始，通过 `Result()` 同步取结果，或消费 `Events()`。示例中的终端聊天采用任务模式；CopilotKit Web 示例可将运行中输入补充到当前 Stream。`showcases/issue-daemon` 展示消息投递、Standby 与团队的端到端场景。相关用法见 [streaming](../docs/streaming.md) 和 [API reference](../docs/api-reference.md)。

除 `threads/codec` 和关闭模型调用的 profile 同步模式外，运行示例通常会调用本机真实的 Codex、Claude、Cursor 或 CodeBuddy CLI，并可能产生费用。请先安装并登录对应 CLI；编译和 fake 测试不需要付费调用。

通用选择参数（提供对应 flag 的 CLI 示例）：

```bash
go run ./examples/quickstart -agent=claude
go run ./examples/quickstart -agent=cursor -command=/absolute/path/to/agent
```

也可使用 `AGENT_ADAPTOR_EXAMPLE_AGENT`、`CODEX_COMMAND`、`CLAUDE_COMMAND`、`CURSOR_COMMAND`、`CODEBUDDY_COMMAND` 及对应的 `*_MODEL` 环境变量。`web-chat/aguiclient` 和 `web-chat/copilotkit` 使用 `AGUI_AGENT` 等 AG-UI 示例环境设置，见各自 README。

## Core

| 示例 | 代码实际展示的行为 |
|---|---|
| [`quickstart`](./quickstart) | 构造 Agent，执行一次 `Send(...).Result()`，检查非空回答并打印运行摘要。 |
| [`structured-output`](./structured-output) | 用 `RunAs[T]` 推导 schema，校验并解码包含 greeting/mood 的类型化结果。 |
| [`tools`](./tools) | 给真实本机 provider 暴露一个带 Revision 的 typed `host_echo` Go 函数，要求调用一次，并关闭 Agent 资源。 |
| [`inspect`](./inspect) | 建立两个独立 profile 的 Agent，先各执行一次真实任务，再读取环境、模型、schema、quota、skills，并选择已声明的 review-note skill；不是纯只读探针示例。 |
| [`threads`](./threads) | 同 key 续接两次、ResumeOnly、缺失 key 错误、Fork，以及 checkpoint 审计；使用显式内存 store。 |
| [`threads/codec`](./threads/codec) | 不启动 CLI，检查各 Driver 的 SessionCodec 往返映射，以及工作目录变化对 guard fingerprint 的影响。 |
| [`skills`](./skills) | 发现两个 skill，选择 write-proof，追加 required skill，执行真实任务并检查 proof.txt 内容；按 Driver 支持配置 approval callback。 |
| [`profiles`](./profiles) | 同步 profile 的 skills/MCP/hooks/instructions/subagent/config 资源，展示磁盘证据；默认做本地探测、不调用模型，`-run=true` 才发送任务。`-profile-mode=native` 会使用并可能更新本机 profile。 |
| [`profiles/resources`](./profiles/resources) | 观察并同步默认 resources，执行默认任务和带单次覆盖的第二个任务，展示 skills 追加及其他资源族替换；示例声明的外部 hook 被禁用。 |
| [`streaming`](./streaming) | 消费一条 typed Event 流，统计事件，读取最终 Result；可用 `-cancel-after` 定时取消，缺少文本增量时输出最终 Text。 |
| [`streaming/chat`](./streaming/chat) | 在一个具名 Thread 上流式执行一条 prompt，显示文本、thinking、工具和用量，支持信号取消；没有交互输入循环。 |

`inspect` 的 reviewer 只声明 review-note，并按候选集合同选择该 skill。`SelectSkills` 是 Agent 上的变更操作，环境探针与 `ProfileState` 则只读。

`profiles/hook` 和 `profiles/mcpserver` 是 profiles 示例调用的辅助程序，分别处理并记录 hook 输入、提供 stdio MCP 工具；不是独立聊天应用。

Cursor 的工具边界补充消息需要宿主入口调用 `cursor.RunHookRelayIfRequested()` 并使用合适的受管理 workspace。当前这些 main 没有安装该 relay；不要把普通 Cursor 示例描述为工具边界消息示例。轮次结束的 checkpoint resume 不依赖 relay。

## Protocol bridges

| 示例 | 代码实际展示的行为 |
|---|---|
| [`web-chat`](./web-chat) | AG-UI SSE 服务和内置原生浏览器页面，按请求发送任务，内存 store 按会话续接。 |
| [`web-chat/aguiclient`](./web-chat/aguiclient) | Vite/React 的 `@ag-ui/client` 直接连接 Go SSE bridge；无 CopilotKit 代理层。 |
| [`web-chat/copilotkit`](./web-chat/copilotkit) | CopilotKit 前端、AG-UI 后端、approval responder、typed Event recorder，以及 session replay/pending/resolve 路由；可选择内存或 JSONL 恢复存储。 |
| [`a2a-server`](./a2a-server) | 在隔离 workspace/profile 中发布本机 Agent；默认通过客户端读取 AgentCard、发送一个带 contextID 的流式任务，再用 GetTask 检查终态、历史和结果 artifact；`-serve-only` 只启动服务。 |

`adapter.stream.v1` 和 `DecodeAdapterEventV1` 是版本化 wire schema 名，不是根包 v1 执行入口。A2A/团队 API 细节由对应文档单独维护。

## Showcase

[`showcases/team-agent-workflow`](./showcases/team-agent-workflow) 是 live-only 的多 Agent 规划、实现、审查示例，可在 CLI 或 CopilotKit Web 模式下观察委派进度和工作区产物。它会产生多次模型调用，不进入自动 smoke；用法由其 [README](./showcases/team-agent-workflow/README.md) 单独维护。

## Smoke

```powershell
./examples/run_examples.ps1 -Agent codex
```

脚本先执行不启动 CLI 的 `threads/codec`。若所选 CLI 的 `--help` 不健康，其余 live 示例明确跳过。健康时会执行 quickstart、structured-output、streaming、threads、inspect、skills、profiles/resources、仅同步的 profiles，以及会自行退出的 a2a-server；不执行 Tools、终端 chat、常驻 Web server 或团队 showcase。

`profiles -run=false -probe=false` 同步并检查本地 profile，不调用模型。它仍会创建/写入本地资源。其余 smoke 的 live 步骤需要真实 CLI 和认证。

在不运行示例 main 的情况下检查编译和 fake 测试：

```bash
go build ./examples/...
go test ./examples/...
```
