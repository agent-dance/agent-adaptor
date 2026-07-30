# agent-adaptor

[English](./README.md)

`agent-adaptor` 是一个面向生产环境的 Go 库，用于把本地 coding agent 嵌入 CLI、桌面应用、服务、后台任务和工作流。

公共模型只有六个核心名词：

| 名词 | 含义 |
|---|---|
| `Agent` | 配置完整、构造后即可执行的智能体。 |
| `Thread` | 由宿主不透明 key 标识的持久对话。 |
| `Stream` | 一次正在进行的执行。 |
| `Event` | 执行过程中发生的一件 typed 事件。 |
| `Result` | 最终输出与审计记录。 |
| `Driver` | Codex、Claude、Cursor、CodeBuddy 等 provider 接入实现。 |

整个库只有一个构造入口、一条执行管线、一条 typed Event 流和一个 error 判定面。多个 Agent 就是多个 Go 变量。

## 安装

```bash
go get github.com/agent-dance/agent-adaptor
```

模块要求 Go 1.26.5 或更高版本。

## 快速开始

```go
package main

import (
	"context"
	"fmt"
	"log"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/codex"
)

func main() {
	agent := adaptor.New(
		codex.Driver(codex.Config{Model: "gpt-5.4"}),
		adaptor.WithWorkspace("/path/to/repository"),
	)

	result, err := agent.Run(context.Background(), "修复失败的测试")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(result.Text)
}
```

四个内置 Driver 使用相同的构造方式：

```go
codexAgent := adaptor.New(codex.Driver(codex.Config{}))
claudeAgent := adaptor.New(claude.Driver(claude.Config{}))
cursorAgent := adaptor.New(cursor.Driver(cursor.Config{}))
codeBuddyAgent := adaptor.New(codebuddy.Driver(codebuddy.Config{}))
```

## Run 与 Stream

`Run` 是批处理形式。`Stream` 把同一次执行暴露为一条 typed Event channel，并在最后返回 `Result`：

```go
stream := agent.Stream(ctx, "解释准备提交的补丁")
defer stream.Cancel()

for event := range stream.Events() {
	switch event := event.(type) {
	case adaptor.TextDelta:
		fmt.Print(event.Text)
	case adaptor.ToolCall:
		fmt.Printf("\n[工具：%s]\n", event.Name)
	case *adaptor.ApprovalRequest:
		_ = event.Deny(ctx, "当前未启用交互审批")
	case adaptor.Dropped:
		log.Printf("丢弃了 %d 个增量事件", event.Count)
	}
}

result, err := stream.Result()
```

`Run` 复用同一条 Stream 管线。调用方如果不再消费事件，必须调用 `Cancel`；审批、生命周期、终局、transcript 和丢弃报告事件不会被静默丢弃。

## 有状态 Thread

Agent 默认无状态。宿主需要对话连续性时，显式注入 `threadstore.Store`：

```go
agent := adaptor.New(
	claude.Driver(claude.Config{}),
	adaptor.WithThreadStore(memory.NewStore()),
)

thread := agent.Thread("tenant-42/issue-123")
result, err := thread.Run(ctx, "继续调查")

resumeOnly := agent.Thread("tenant-42/issue-123", adaptor.ResumeOnly())
branch := thread.Fork("tenant-42/issue-123/alternative")
```

Thread key 是宿主拥有的不透明字符串。新的无关对话必须使用新的宿主 key；SDK 不提供主动重绑已有 key 的入口。Driver 的 resume 标识只属于 checkpoint 细节。持久化宿主可以实现 `threadstore.Store`；`memory.NewStore()` 适合单进程使用。

Claude、CodeBuddy 和 Codex 对显式 Thread 默认跨轮复用 provider 进程。需要每轮或某一轮使用新进程时，在 Agent 构造处或调用处显式传入 `WithSpawn()`。宿主结束 Agent 生命周期时应关闭进程池：

```go
defer agent.Close(context.Background())

_, _ = thread.Run(ctx, "复用常驻 writer")
_, _ = thread.Run(ctx, "使用新进程", adaptor.WithSpawn())
```

Cursor 和无状态 Agent 调用始终逐轮启动。`Close` 开始后的 Agent/Thread 新运行返回 `ErrAgentClosed`。

## 选项与资源

选项使用一套词汇，并通过类型区分作用域：

- `Option` 只能用于构造 Agent。
- `CallOption` 只能用于 `Run` 和 `Stream`。
- `SharedOption` 可用于两处；调用处的值覆盖 Agent 默认值。

Skills 采用追加语义，其他资源族按各自合同替换或合并。

```go
agent := adaptor.New(
	codex.Driver(codex.Config{}),
	adaptor.WithModel("gpt-5.4"),
	adaptor.WithSkills(skill.Dir("./skills/review")),
	adaptor.WithMCP(mcp.Stdio("repo-tools", "repo-mcp", mcp.Args("serve"))),
	adaptor.WithProfile(profile.Dedicated("./profiles/reviewer")),
)

result, err := agent.Run(ctx, "评审这个改动",
	adaptor.WithModel("gpt-5.4-mini"),
	adaptor.WithMetadata("request_id", requestID),
)
```

workspace manager、runtime service manager、skill provider、skill materializer、profile resources 和 run-scoped host services 都是可选扩展。一次基本执行不依赖这些能力。

## 宿主自定义 Tools

应用可以直接用 typed Go 函数扩展 Agent，无需自行构造或管理 MCP server：

```go
type SearchInput struct {
	Query string `json:"query" jsonschema:"required"`
}

type SearchOutput struct {
	Files []string `json:"files"`
}

searchRepo := tool.Define(
	"search_repo",
	"Search files in the current repository.",
	func(ctx context.Context, in SearchInput) (SearchOutput, error) {
		return search(ctx, in.Query)
	},
	tool.ReadOnly(),
	tool.Idempotent(),
	tool.Revision("search_repo/v1"),
)

agent := adaptor.New(
	codex.Driver(codex.Config{}),
	adaptor.WithTools(searchRepo),
)
defer agent.Close(context.Background())
```

`WithTools` 只能用于构造期，并整体替换 Tool 集合。默认根据 handler 的 Go
类型推导 schema，也可以显式提供标准 JSON Schema。`tool.Reject(code, message)`
表示可安全展示给模型的业务失败；普通 error 与 panic 会被净化。用于有状态
Thread 的每个 Tool 都必须设置 `tool.Revision`，让 handler 行为进入续接兼容性。

MCP 只是这套 API 的内部交付机制。已有或远程 MCP server 继续使用
`WithMCP`。内置 Driver 会把 Tools 物化到 SDK 自有的隔离执行 profile，
不会改写配置的原生 profile。生命周期、schema、错误、安全和 Thread 语义见
[宿主自定义 Tools 合同](./docs/tools.md)。

## 结构化输出案例

结构化输出是单次运行合同。`RunAs[T]` 从 Go 类型生成 JSON Schema，优先使用
provider 原生约束；不支持时会自动回退到 Prompt 加本地校验，并同时返回
typed value 与普通审计 `Result`：

```go
type ReleasePlan struct {
	Filename  string `json:"filename"`
	MediaType string `json:"media_type"`
	Summary   string `json:"summary"`
	Content   string `json:"content"`
}

plan, result, err := adaptor.RunAs[ReleasePlan](ctx, agent,
	"Produce the release plan as a Markdown file artifact.")
if err != nil {
	return err
}
fmt.Printf("%s (%s)\n%s\n", plan.Filename, result.RunID, plan.Content)
```

完整可运行代码见 [`structured-output`](./examples/structured-output)，合同细节见
[结构化输出文档](./docs/structured-output.md)。

## Team agent 案例

团队形状与路由属于宿主策略。把普通 Runner 配置成本地或远程委托目标，将一个
`a2adelegation.Service` 挂到 leader，所有角色进度仍从 leader 的唯一 Event
流消费：

```go
team, err := a2adelegation.NewService(a2adelegation.Config{
	Agents: []a2adelegation.AgentRef{
		a2adelegation.LocalNamed("plan", "Codex Planner", planner, a2adelegation.Policy{}),
		a2adelegation.LocalNamed("impl", "Claude Code Implementer", implementer, a2adelegation.Policy{}),
		a2adelegation.LocalNamed("review", "Codex Reviewer", reviewer, a2adelegation.Policy{}),
	},
})
if err != nil {
	return err
}
defer team.Close()

leader := adaptor.New(leaderDriver, team.Option())
stream := leader.Stream(ctx, "Plan, implement, and review TASK.md")
for event := range stream.Events() {
	if update, ok := event.(adaptor.SubagentUpdate); ok {
		fmt.Printf("[%s] %s: %s\n", update.Agent, update.Kind, update.Delta)
	}
}
result, err := stream.Result()
```

完整的 [`team-agent-workflow`](./examples/showcases/team-agent-workflow) 还包含
角色级 sandbox、结构化 `PLAN.md` 文件制品、workspace 审计，以及带实时
subagent 卡片的 CopilotKit 页面。一条命令即可启动：

```bash
./examples/showcases/team-agent-workflow/start-all.sh claude
```

## Result 与错误

成功执行返回 `*Result, nil`。已经完成但业务失败的执行返回携带可用 `Result` 的 typed `*RunError`；基础设施失败保持为普通可包装的 Go error。

```go
result, err := agent.Run(ctx, prompt)
if err != nil {
	var runErr *adaptor.RunError
	if errors.As(err, &runErr) {
		log.Printf("执行失败：%s；部分摘要：%s", runErr.Reason, runErr.Result.Summary)
	}
	return err
}
```

`Result` 将不同输出层明确分开：

- `Text` 是最终面向 assistant 的文本。
- `Summary` 是适合宿主列表与日志的简短摘要。
- `Raw()` 包含完整 stdout、stderr，以及观察到的 provider 正式终局 payload。
- `Transcript()` 包含 Driver 从正式协议解析的标准化条目。
- `Services()` 报告本次执行实际观察到的 runtime services。
- `Decode()` 读取已经校验的结构化输出。

结构化输出直接使用 `RunAs[T]`；高级 schema 定制只在专项合同文档中展开。

## Inspect 与 Profile

`Agent.Inspect()` 提供只读的环境、模型、配额、配置 schema 和 skill 探针。不支持的探针会如实报告。Profile 操作保持显式：

```go
environment, err := agent.Inspect().Environment(ctx)
models, err := agent.Inspect().Models(ctx)
state, err := agent.ProfileState(ctx)
synced, err := agent.SyncProfile(ctx)
```

## 包布局

| 包 | 用途 |
|---|---|
| [`driver`](./driver) | Driver SPI 与 provider-facing 合同。 |
| [`codex`](./codex)、[`claude`](./claude)、[`cursor`](./cursor)、[`codebuddy`](./codebuddy) | 内置 Driver 及其 Config 类型。 |
| [`tool`](./tool)、[`skill`](./skill)、[`mcp`](./mcp)、[`profile`](./profile) | 面向调用方的能力与资源词汇。 |
| [`threadstore`](./threadstore)、[`memory`](./memory) | Thread 持久化合同与内存实现。 |
| [`bridges`](./bridges) | SSE、AG-UI、A2A 与 subagent-stream 协议桥。 |
| [`clients/a2a`](./clients/a2a) | 面向宿主的 A2A 客户端。 |
| [`hosttools`](./hosttools) | 可选的 delegation 与事件记录组件。 |
| [`adaptertest`](./adaptertest) | Driver 一致性测试套件。 |

## Examples

- [`quickstart`](./examples/quickstart)：构造 Agent 并执行一次 prompt。
- [`inspect`](./examples/inspect)：环境、模型、配额、schema、skills 与 profile 状态。
- [`threads`](./examples/threads)：续接、只续不建、分叉和 checkpoint 审计。
- [`tools`](./examples/tools)：向真实本地 provider 暴露 typed Go 函数，无需自行管理 MCP。
- [`skills`](./examples/skills)：实时 skill 解析与物化。
- [`profiles`](./examples/profiles)：provider profile resources 与同步。
- [`streaming`](./examples/streaming)：typed Event 消费与取消。
- [`structured-output`](./examples/structured-output)：typed JSON 输出。
- [`web-chat`](./examples/web-chat)：SSE/AG-UI server，以及 [`aguiclient`](./examples/web-chat/aguiclient) 和 [`copilotkit`](./examples/web-chat/copilotkit) 前端。
- [`a2a-server`](./examples/a2a-server)：通过 A2A 发布 Agent，并使用 A2A client 调用。
- [`showcases/team-agent-workflow`](./examples/showcases/team-agent-workflow)：leader → plan → implementation → review，并提供一条命令启动的 CopilotKit 验收入口。

会调用 provider 的 examples 需要对应的本地 CLI 和登录状态。普通仓库测试不会产生付费调用。

## 边界

Core 不提供 HTTP 或 gRPC server、队列、调度器、租户系统、鉴权系统、数据库或自动 Agent router。协议服务属于 bridges 和宿主应用；团队角色与业务流程策略属于宿主。

## 文档

- [文档地图](./docs/README.md)
- [API reference](./docs/api-reference.md)
- [Streaming 指南](./docs/streaming.md)
- [宿主自定义 Tools](./docs/tools.md)
- [结构化输出](./docs/structured-output.md)
- [A2A 集成](./docs/a2a.md)
- [公开错误](./docs/public-errors.md)
