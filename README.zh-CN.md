# agent-adaptor

[English Version](./README.md)

一个纯 Go SDK，用一套统一方式调用本地 `coding agent`。

`agent-adaptor` 适合那些想在 `codex`、`claude` 或 `cursor` 等强大的 Agent 之上构建自己的产品的团队，`agent-adaptor` 提供了抽象的进程启动、session 复用、权限注入、skills 动态注入、运行时服务注入和结果整理等能力，你不再需要考虑如何调用它们，专注于您的产品即可。

## 核心场景

- 用最低成本将常用本地 `coding agent` 嵌入你的产品。
- 支持“Codex 实现、Claude 审查”这类多 Agent 协作场景。
- 在服务化工作流里复用 session，但不强迫所有调用都变成有状态。
- 由你的应用自己控制 workspace、skills、permissions 和运行时服务，而不是把这些细节藏在各家 agent 的私有接入代码里。
- 用同一套方式做环境检查、模型探测、配置字段展示、额度查看和 skills 管理。

## 安装

```bash
go get github.com/agent-dance/agent-adaptor
```

## 快速开始

最快的使用方式就是：绑定一个默认 Agent，然后调用 `sdk.Run(...)`。

```go
package main

import (
	"context"
	"fmt"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/codex"
)

func main() {
	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(codex.New(agentadaptor.CodexConfig{
			Model: "gpt-5.4",
		})),
	)

	result, err := sdk.Run(context.Background(), "fix the failing tests")
	if err != nil {
		panic(err)
	}

	fmt.Println(result.Output)
}
```

`New(...)` 适合应用和测试场景，会在配置错误时直接失败。若你在库或服务中嵌入 SDK，希望自己处理配置错误，请使用 `Build(...)`。

## 核心使用方式

1. 用 `WithDefaultAgent(...)` 绑定默认 Agent。
2. 默认执行路径使用 `sdk.Run(...)` 或 `sdk.Start(...)`。
3. 多 Agent 场景用 `WithAgent(name, binding)` 绑定，再通过 `sdk.Agent(name)` 获取。
4. 调用级 `RunOption` 可以覆盖绑定时的默认值。
5. 只有在确实需要会话复用时才注入 `SessionStore`。

## 完整示例

5 个 spotlight 示例，每个回答一个宿主集成决策问题。详细矩阵与走查见 [`examples/README.md`](./examples/README.md)。

- [`examples/quickstart-cli`](./examples/quickstart-cli)：绑定一个默认 agent 跑通；最小四联屏 CLI 面板。
- [`examples/web-chat-stream`](./examples/web-chat-stream)：token 级流式输出，同时支持 CLI 打字效果（`-mode=cli`）与 HTTP SSE chat 端点（`-mode=server`）。
- [`examples/multi-agent-platform`](./examples/multi-agent-platform)：默认 agent + 命名 agent（`review`、`autopilot`）配合 Admin 控制面。
- [`examples/human-in-the-loop`](./examples/human-in-the-loop)：permission / plan-review / question 三类决策，三幕话剧 + capability matrix。
- [`examples/task-recipes`](./examples/task-recipes)：基于 profile resources（skills / agents / hooks / instructions / config）+ `SyncProfile` 的宿主任务剧本。

延伸阅读（前端集成，不属于 spotlight）：

- [`examples/streaming-chat-copilotkit`](./examples/streaming-chat-copilotkit)：AG-UI + CopilotKit + HITL 卡片 demo。
- [`examples/streaming-chat-aguiclient`](./examples/streaming-chat-aguiclient)：Vite + React + `@ag-ui/client` 直连 AG-UI demo。

`examples/internal/` 下是 SDK 自身回归用例，不面向宿主。

## 常见调用方式

### 多 Agent

默认 Agent 直接走 `sdk.Run(...)`；命名 Agent 通过 `sdk.Agent(name)` 获取后再执行。

```go
package main

import (
	"context"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/claude"
	"github.com/agent-dance/agent-adaptor/codex"
)

func main() {
	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(codex.New(agentadaptor.CodexConfig{
			Model: "gpt-5.4",
		})),
		agentadaptor.WithAgent("review", claude.New(agentadaptor.ClaudeConfig{
			Model: "claude-sonnet-4",
		})),
	)

	_, err := sdk.Run(context.Background(), "implement the fix")
	if err != nil {
		panic(err)
	}

	review, err := sdk.Agent("review")
	if err != nil {
		panic(err)
	}

	_, err = review.Run(context.Background(), "review the patch")
	if err != nil {
		panic(err)
	}
}
```

### Session 复用

没有 `WithSessionStore(...)` 时，运行默认无状态。注入 store 之后，你可以用 `SessionKey` 复用稳定业务会话，也可以通过 `continue_only`、`start_new` 和 `fork` 选择更严格的模式。

```go
package main

import (
	"context"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/codex"
	"github.com/agent-dance/agent-adaptor/memory"
)

func main() {
	store := memory.NewSessionStore()

	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(codex.New(agentadaptor.CodexConfig{
			Model: "gpt-5.4",
		})),
		agentadaptor.WithSessionStore(store),
	)

	_, err := sdk.Run(
		context.Background(),
		"continue issue-123",
		agentadaptor.WithSessionKey("company-1", "issue-123"),
	)
	if err != nil {
		panic(err)
	}
}
```

### 流式执行

`Start(...)` 会返回 `RunHandle`，里面有 `Events()`、`StreamEvents()`、`DecisionRequests()`、`RunID()`、`Wait(...)`、`Cancel(...)` 和 `ResolveDecision(...)`；你的应用可以用同一套执行接口处理运行事件、token 级流式输出和 HITL 回填，而不用再维护第二套 API。

## 能力面

| 能力 | 说明 |
| --- | --- |
| Execution | `Run(...)` 用于同步执行，`Start(...)` 用于流式句柄。 |
| Sessions | 默认无状态；注入 `SessionStore` 后支持 `continue_or_start`、`continue_only`、`start_new` 和 `fork`。 |
| Skills | 用同一条流程处理 skills 的解析、规范化、组装和同步。 |
| MCP | 宿主声明统一的 MCP server spec，内置 adapter 会把它物化到各自真实生效的 profile。 |
| Runtime Services | 在运行前准备好需要的运行时服务，并在清理阶段按 `RunID` 释放。 |
| Admin API | 提供管理接口，用来做环境检查、模型枚举与探测、配置字段展示、额度查询和 skills 管理。 |
| Run Results | 分层返回 assistant 文本 `Output`、原始 stdout/stderr `RawStreams`、语义记录 `Transcript`、短摘要 `Summary`、provider 终局 JSON `Result`、provider/model/cost 元数据、运行时服务状态，以及结构化 question/failure。 |

## 内置包

内置包返回的是配置完成的 `AgentBinding`，而不是底层适配器。

- `github.com/agent-dance/agent-adaptor/codex`
- `github.com/agent-dance/agent-adaptor/claude`
- `github.com/agent-dance/agent-adaptor/cursor`

如果你需要更底层的扩展接口，每个内置包也都提供 `NewAdapter()`。

对于内置适配器，profile API 使用 `WithNativeProfile()`、`WithDedicatedProfile(dir)`、`WithCloneProfile(dir, opts)`、`WithCloneProfileFrom(src, dst, opts)` 统一选择或初始化本地 agent profile，而不必每次手动写各家自己的环境变量。

`WithDefaultMCP(...)` / `WithMCP(...)` 也遵循和 `skills` 相同的默认值与调用覆盖规则；`skills/MCP` 变化不会自动打断 session 复用，是否继续沿用 session 仍由宿主通过 `SessionMode` 决定。

## 管理接口

`sdk.Admin()` 只负责管理相关能力，不执行 prompt。

当你的应用需要在执行前后检查已绑定 agent 时，可以用它：

- `CheckEnvironment(...)` 用于真实环境检查。
- `ListModels(...)` 与 `DetectModel(...)` 用于模型可见性和探测。
- `ConfigSchema(...)` 用于生成配置界面需要的字段信息。
- `GetQuota(...)` 用于在支持时返回真实额度或 credit 窗口。
- `ListSkills(...)` 与 `SetSelectedSkills(...)` 用于 skill 清单与进程内 selected-skill 覆盖。

## 适配器扩展

第三方适配器实现 `DriverAdapter` 后，可以接入同一套公共执行面。

```go
binding := agentadaptor.BindTyped(myAdapter, myConfig)
sdk := agentadaptor.New(agentadaptor.WithDefaultAgent(binding))
```

共享 CLI helper 只负责进程 I/O 和原始事件传输。正式的会话快照解析仍由各适配器自己负责，这样各家 CLI 的协议差异不会被塞进共享基础设施里。

如果你正在实现自己的适配器，可复用的测试套件在 [`adaptertest`](./adaptertest)。

## 当前保证

- 对外执行路径只有一套：默认值合并、运行参数整理、session 协调、适配器执行、会话快照持久化、结果归档。
- 主路径是默认 Agent 优先，而不是每次都先从注册表挑一个 agent。
- 有状态运行只有在适配器返回有效会话快照时才会持久化 session 状态。
- 管理接口与执行接口共享同一套默认 Agent 与命名 Agent 心智模型。
- 内置包通过 `New(...)` 返回带类型的绑定对象，自定义适配器也可以用 `BindTyped(...)` 接入同样的方式。

## 非目标

- 不内置 HTTP/gRPC server。
- 不内置队列、scheduler、租户框架或编排守护进程。
- 不自动决定该选哪个 agent。
- 不允许长出第二套语义不同的执行入口。

## 深入阅读

先看当前仍作为使用入口的文档：

- [`docs/README.md`](./docs/README.md)：当前文档地图与历史 workstream 索引。
- [`docs/api-reference.md`](./docs/api-reference.md)：公共 API 面与职责归属。
- [`docs/usage-guide.md`](./docs/usage-guide.md)：常见宿主集成方式。
- [`docs/run-policy.md`](./docs/run-policy.md)：`RunPolicy` 与 HITL 合同。
- [`docs/streaming.md`](./docs/streaming.md)：token streaming、AG-UI 与 SSE 用法。
- [`docs/public-errors.md`](./docs/public-errors.md)：公开错误清单。
