# agent-adaptor

[English](./README.md) | [日本語](./README.ja.md) | [한국어](./README.ko.md) | [Deutsch](./README.de.md)

`agent-adaptor` v2 是一个 Go SDK，通过一套简单、直观的 API 统一驱动 `Codex`、`Claude Code`、`Cursor` 和 `CodeBuddy`，并提供调用之外的会话、审批、工具和团队能力。

```go
agent := adaptor.New(codex.Driver(codex.Config{Model: "gpt-5.4"}))
result, err := agent.Send(ctx, "Fix the failing tests").Result()
```

改用 Claude Code 只需要替换构造时传入的 Driver，其余调用代码不变。

## 能力概览

- **统一配置**：一套 API 控制不同 Agent 的 skills、MCP、系统提示词、模型、沙箱、工具和审批。
- **流式响应**：按需消费流式事件，分别处理思考、文本、工具调用和决策请求。
- **会话管理**：支持续接与分叉。直接使用工单号、用户 ID 等业务字符串作为会话 key，无需管理底层 provider session。
- **人工决策**：通过回调或事件回答问题、拦截危险命令、确认计划。宿主可以保留请求中的实时 responder，再从另一个 HTTP 请求应答；审批审计数据由宿主管理。

## 高级功能

- **结构化输出**：定义 Go 结构体，调用 `RunAs[T]`，即可约束 Agent 输出并取得填好的对象。
- **多协议修饰**：内置 A2A/AGUI 适配，一行代码即可将 Agent 包装为支持 SSE + AGUI 流式输出的服务入口，再接入自定义前端或客户端；仓库附带可运行的 CopilotKit 前端。
- **团队**：由宿主选择 leader，协调不同 Driver 的本地成员或 Remote A2A 成员。例如由 Codex 带领规划成员（Codex）、实现成员（Claude）和审查成员（Cursor），任务更新与 worker 审批统一汇入 leader 的 Event 流。参见[团队指南](./docs/team.md)及 examples/showcases/team-agent-workflow 示例。
- **Agent 隔离**：将本机配置克隆到独立目录，并链接登录状态，使配置修改彼此隔离。可同时运行多个 Codex/Claude Code 实例，承担并行任务或不同角色。

## 安装

```bash
go get github.com/agent-dance/agent-adaptor/v2
```

需要 Go 1.26.5 及以上。

**运行时必须已安装并登录对应的 Agent CLI。**

## 快速开始

```go
package main

import (
	"context"
	"fmt"
	"log"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"github.com/agent-dance/agent-adaptor/v2/codex"
)

func main() {
	agent := adaptor.New(
		codex.Driver(codex.Config{Model: "gpt-5.4"}),
		adaptor.WithWorkspace("/path/to/repository"),
	)

	result, err := agent.Send(context.Background(), "Fix the failing tests").Result()
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(result.Text)
}
```

四个内置 Driver 的构造方式相同，各自拥有独立的 `Config`：

```go
codexAgent := adaptor.New(codex.Driver(codex.Config{}))
claudeAgent := adaptor.New(claude.Driver(claude.Config{}))
cursorAgent := adaptor.New(cursor.Driver(cursor.Config{}))
codeBuddyAgent := adaptor.New(codebuddy.Driver(codebuddy.Config{}))
```

## 流式执行

`Send` 开始一次执行并返回 `Stream`。读取 `Events()` 可查看实时进度，调用 `Result()` 则同步等待最终结果：

```go
stream := agent.Send(ctx, "Explain the patch that is about to be committed")
defer stream.Cancel()

for event := range stream.Events() {
	switch event := event.(type) {
	case adaptor.TextDelta:
		fmt.Print(event.Text)
	case adaptor.Thinking:
		fmt.Fprint(os.Stderr, event.Text)
	case adaptor.ToolCall:
		if event.Phase == adaptor.PhaseStart {
			fmt.Printf("\n[tool call: %s]\n", event.Name)
		}
	case *adaptor.ApprovalRequest:
		_ = event.Approve(ctx)
	case adaptor.Dropped:
		log.Printf("backpressure dropped %d incremental events", event.Count)
	}
}

result, err := stream.Result()
```

文本、思考、工具调用与结果、进程信息、生命周期、消息回执和审批请求都走这一条事件流。provider 每轮产生一个 `TurnEnd`。最后一个事件是 `Done{Result, Err}`，在校验、checkpoint 处理和清理完成后发布，与 `stream.Result()` 返回相同的结果指针和错误。`RunFinished` 仅供参考。

提前停止消费时调用 `Cancel()`；该方法幂等。

## 运行中补充消息

向返回的 Stream 追加指导，并使用稳定的宿主消息 ID 去重：

```go
stream := agent.Send(ctx, "Fix the failing tests")
err := stream.Send(ctx, "Keep the public API unchanged",
	adaptor.MessageID("review-comment-42"),
	adaptor.From(adaptor.Author{Kind: adaptor.AuthorHuman, ID: "reviewer"}),
	adaptor.When(adaptor.Soon),
)
```

默认时机 `Soon` 在最早支持的边界交付，否则在轮次结束后续跑。`AfterTurn` 等待后续轮次。`Interrupt` 中止当前轮次，并在同一次执行中继续；不支持的 Driver 返回 `ErrMessageUnsupported`。参见 [Driver 能力表](./docs/streaming.md#12-message-delivery-capabilities)。

Send 返回 nil 仅表示消息已接纳。`MessageUpdate` 报告 accepted、delivered 或 failed，失败时保留原文。`Result.Messages()` 记录最终回执，`Undelivered()` 返回失败项。接纳错误为 `*MessageError`，包含 ID、原文、原因及可解包的错误；执行已结束时可匹配 `ErrRunFinished`。Stream.Send 的 context 可以撤回尚未被领取的消息；Driver 领取后，由其交付确认决定结果。

`MessageID` 在 Thread 内去重，保留最近最多 1000 个已交付 ID，最长七天。进程重启后恢复需要持久化 Thread store。若 provider 已接收消息而回执尚未落盘，此时崩溃仍可能在恢复后重投；去重不能消除这个窗口。

## 待命会话 Standby

每个请求需要一次有界执行结果时，使用任务模式 `Thread.Send`。聊天或 IM bot 需要持续接收消息和回复时，使用 Standby：

```go
func openChat(serverCtx context.Context, agent *adaptor.Agent) (adaptor.Standby, error) {
	sb, err := agent.Thread("chat-42").Standby(serverCtx)
	if err != nil {
		return nil, err
	}
	events := sb.Events() // subscribe before accepting input
	go func() {
		for event := range events {
			switch e := event.(type) {
			case adaptor.TurnEnd:
				if e.Err == nil && e.Result != nil {
					fmt.Println(e.Result.Text)
				}
			case adaptor.Done:
				if e.Err != nil {
					log.Print(e.Err)
				}
			}
		}
	}()
	return sb, nil
}
```

使用服务生命周期的 context 打开 Standby。HTTP handler 可调用 `sb.Send(r.Context(), text, adaptor.MessageID(id))`：请求 context 只限制接纳，接纳后的工作由 `serverCtx` 管理。忙碌时输入并入当前执行，空闲时开始新执行。带 `WithModel` 等执行级选项的输入会排到当前工作之后，单独执行。

Standby 提供 `Send`、`Events` 和 `Cancel`，没有整体 `Result()`。provider 每轮产生 `TurnEnd`，每次执行产生 `Done`。`sb.Cancel()` 中止当前处理，将排队消息以 `MessageCancelled` 结算，随后继续接收输入。取消 `serverCtx` 才结束 Standby 并关闭事件流。第一次调用 `Events()` 前的事件不缓存。

存活的 Standby 即使空闲也持有 Thread，任务模式 Send 返回 `ErrThreadBusy`。同一个 Agent 再次打开相同 key 时返回已有句柄，其生命周期仍属于第一次调用的 context。

执行失败不会结束 Standby。只有已分类的限流、网络和 provider 服务端故障可自动重试，且最多一次。未交付的 `run_failed` 消息沿用原 ID 携带到下一次执行，观察者可能先看到 failed，再看到 delivered。未重试时，携带消息等待下一条宿主输入唤醒。交付与恢复限制见[流式指南](./docs/streaming.md)。

## 人工审批与沙箱

沙箱、搜索和浏览器控制、审批模式、消息上限都配置在同一个 `Policy` 中。构造时设置默认值，`Send` 可以按次替换整个 policy：

```go
reviewer := adaptor.New(
	codex.Driver(codex.Config{}),
	adaptor.WithPolicy(adaptor.Policy{
		Sandbox:   adaptor.ReadOnly,
		WebSearch: adaptor.FeatureDeny,
		Approvals: adaptor.ApprovalPolicy{
			Permission: adaptor.ApprovalAutoApprove,
		},
	}),
)
```

沙箱有 `ReadOnly`、`WorkspaceWrite`、`Unrestricted` 三档。`PolicyReadOnly` 等预设只是设置 `Sandbox` 字段的便捷值。所选 Driver 不支持某个显式控制维度时，会在进程启动前返回明确错误。

审批有回调和事件两种消费方式，选择其中一种。回调适合 CLI 与无人值守场景：

```go
agent := adaptor.New(
	claude.Driver(claude.Config{}),
	adaptor.WithPolicy(adaptor.Policy{
		Approvals: adaptor.ApprovalPolicy{Question: adaptor.QuestionAsk},
	}),
	adaptor.OnApproval(func(ctx context.Context, req *adaptor.ApprovalRequest) error {
		switch req.Kind {
		case adaptor.ApprovalPermission:
			return req.Approve(ctx)
		case adaptor.ApprovalQuestion:
			return req.Answer(ctx, "Use PostgreSQL")
		default:
			return req.Deny(ctx, "the plan needs human confirmation")
		}
	}),
)
```

回调可直接使用 `adaptor.ApproveAll()` 或 `adaptor.DenyAll(reason)`。它们只处理已路由到回调的请求，不会开启 Driver 不支持的审批模式。Claude 和 CodeBuddy 支持交互请求。Codex 通过 app-server 支持 Permission 审批，包括 MCP 工具审批；不支持 PlanReview 或需要输入的 Question。Cursor 不声明支持 `Ask`。

未安装回调时，请求以携带 responder 的 `*adaptor.ApprovalRequest` 出现在事件流中。宿主可以保留请求，稍后从其他 goroutine 或另一个 HTTP 请求应答，适合 Web UI：

```go
func consumeApprovals(ctx context.Context, stream adaptor.Stream,
	publish func(*adaptor.ApprovalRequest)) (*adaptor.Result, error) {
	for event := range stream.Events() {
		if req, ok := event.(*adaptor.ApprovalRequest); ok {
			publish(req) // the host retains the live responder for its UI
		}
	}
	return stream.Result()
}
```

宿主可在配套接口中调用 `req.Approve(r.Context())`、`req.Deny(r.Context(), reason)` 或 `req.Answer(r.Context(), text)`。`NoticeApprovalResolved` 的 `Data["request_id"]` 可供 UI 移除已处理的卡片。存储审计记录不会序列化实时 responder。

请求只能应答一次。重复应答、Kind 不匹配、执行结束分别返回稳定错误 `ErrApprovalResolved`、`ErrApprovalKindMismatch`、`ErrApprovalExpired`；零值请求也不会永久阻塞。请求到达时若宿主既未调用 `Events()`，也未安装 `OnApproval`，会立即采用超时兜底并发布生命周期 Notice。否则超时后由 `Policy.Approvals.OnTimeout` 接管，拒绝则遵循 `OnReject`。保留请求的位置由宿主决定，不限于进程内存。

[`web-chat/copilotkit`](./examples/web-chat/copilotkit) 提供完整可运行的 Web HITL 示例，包含 `/decision/pending` 和 `/decision/resolve` 两个接口，页面刷新后仍可取得待处理审批。

## 多轮会话

直接调用 `Agent.Send` 时，各次执行相互独立，相当于每次使用新的匿名 Thread。具名 Thread 默认使用 Agent 自己的内存 store。注入 `WithThreadStore` 可共享 store 或实现持久化：

```go
agent := adaptor.New(
	claude.Driver(claude.Config{}),
	adaptor.WithThreadStore(memory.NewStore()),
)
defer agent.Close(context.Background())

thread := agent.Thread("tenant-42/issue-123")        // continue the mapped conversation if it exists, otherwise create it
result, err := thread.Send(ctx, "Keep investigating this problem").Result()

only := agent.Thread("tenant-42/issue-123", adaptor.ResumeOnly()) // resume only, never create
branch := thread.Fork("tenant-42/issue-123/plan-b")               // fork from the current progress
```

使用约定：

- **会话 key 由宿主提供**：SDK 逐字保存和比较。开启全新会话时使用新 key；SDK 不提供将旧 key 主动重绑为新会话的入口。
- **一个 Thread 只有一个 writer**：由 lease 保证。另一次执行或存活的 Standby 占用 Thread 时，任务模式 `thread.Send` 通过其 Stream 返回 `ErrThreadBusy`。补充输入应发送给已有 Stream 或 Standby。
- **续接前校验兼容性**：Driver、模型、已解析 workspace、配置、skills 和 MCP 都参与 fingerprint，发生漂移时防止错误复用。
- **失败不污染状态**：非零退出、协议错误和取消不产生有效 checkpoint，之前健康的会话记录保持不变。
- **默认复用常驻进程**：Windows、macOS 和 Linux 上，Claude、CodeBuddy、Codex 的显式 Thread 默认跨轮次复用进程。使用 `adaptor.WithSpawn()` 可让本轮或每轮启动新进程。Cursor 与无状态调用逐轮启动。`Agent.Close` 开始后，新 Send 返回 `ErrAgentClosed`。

单进程场景可使用 `memory.NewStore()`；需要持久化时实现 `threadstore.Store`。

## 结构化输出

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

schema 从 Go 类型推导，优先使用 provider 原生 JSON Schema 约束。当前 transport 或 policy 不支持时，自动退回提示词约束加本地校验；两种机制都不可用才在启动前失败。返回值同时包含类型化对象和完整审计 `Result`。

详见 [`structured-output` 示例](./examples/structured-output)和[结构化输出文档](./docs/structured-output.md)。

## 选项与资源

选项使用统一词汇，并通过类型在编译时区分作用域：

| 类型 | 适用位置 |
|---|---|
| `Option` | 仅 `adaptor.New` |
| `CallOption` | `Agent.Send`、`Thread.Send`、`Standby.Send` |
| `SharedOption` | 构造默认值与单次执行；调用处覆盖构造值 |
| `MessageOption` | `From`、`MessageID`、`When`；也属于 `CallOption`，且是 `Stream.Send` 唯一接受的选项类型 |

合并规则只有一条：近处覆盖远处，skills 追加，其余配置按各自合同替换或合并。

同一套选项覆盖各 Agent 的主要配置：

| 要控制的内容 | 使用方式 |
|---|---|
| 模型 | `WithModel` |
| 系统提示词 | `WithInstructions` |
| 工作目录 | `WithWorkspace`；隔离工作树使用 `WithWorkspaceSpec` |
| skills | `WithSkills`，配合 `skill.Dir` / `skill.FS` / `skill.Inline` / `skill.Key` / `skill.Require` |
| MCP | `WithMCP`，配合 `mcp.Stdio` / `mcp.HTTP` / `mcp.SSE` |
| 沙箱、搜索、浏览器工具、审批、消息上限 | `WithPolicy`；交互应答配合 `OnApproval` |
| 配置目录与资源 | `WithProfile`、`WithProfileResources` |
| 超时、审计元数据、调用方身份 | `WithTimeout`、`WithMetadata`、`WithIdentity` |
| 会话持久化 | `WithThreadStore` |

```go
agent := adaptor.New(
	codex.Driver(codex.Config{}),
	adaptor.WithModel("gpt-5.4"),
	adaptor.WithInstructions("You are this repository's reviewer: read the code only, state the conclusion before the evidence."),
	adaptor.WithSkills(skill.Dir("./skills/review")),
	adaptor.WithMCP(mcp.Stdio("repo-tools", "repo-mcp", mcp.Args("serve"))),
	adaptor.WithProfile(profile.Dedicated("./profiles/reviewer")),
	adaptor.WithTimeout(10*time.Minute),
)

result, err := agent.Send(ctx, "Review this change",
	adaptor.WithModel("gpt-5.4-mini"),
	adaptor.WithSkills(skill.Require(skill.Dir("./skills/security"), "this run must pass the security review")), // appends, does not displace the default skills
	adaptor.WithMetadata("request_id", "review-42"),
).Result()
```

相同配置配合不同 Driver 会构造出不同 Agent。Driver 不支持某个能力时，启动前返回明确错误。

```go
codexReviewer := adaptor.New(codex.Driver(codex.Config{}), reviewerOptions...)
claudeReviewer := adaptor.New(claude.Driver(claude.Config{}), reviewerOptions...)
```

## 宿主定义的 Tools

直接用类型化 Go 函数扩展 Agent，无需自行构造或维护 MCP server：

```go
type SearchInput struct {
	Query string `json:"query" jsonschema:"required"`
}

type SearchOutput struct {
	Files []string `json:"files"`
}

searchRepo := tool.Define(
	"search_repo",
	"Match files using a repository-relative glob.",
	func(ctx context.Context, in SearchInput) (SearchOutput, error) {
		files, err := filepath.Glob(in.Query)
		return SearchOutput{Files: files}, err
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

`WithTools` 仅用于构造，并整体替换 Tool 集合。默认从 handler 的 Go 类型推导 schema，也可显式提供标准 JSON Schema。`tool.Reject(code, message)` 返回可安全展示给模型的业务失败；只有 tool 包创建的拒绝值可通过这一边界，普通错误、伪装的错误和 panic 都会脱敏。用于有状态 Thread 的每个 Tool 都必须有 `tool.Revision`，让 handler 行为变化参与续接兼容性判断。

此处 MCP 是内部交付机制：已有或远程 MCP server 仍使用 `WithMCP`。内置 Driver 将 Tools 物化到 SDK 自有的隔离 profile，不修改已配置的本机 profile。每个 Agent 使用不可预测的 bearer token 环境变量名，其他 MCP server 不能占用该凭据载体。生命周期、schema、错误、安全和 Thread 语义见[宿主 Tools 合同](./docs/tools.md)。

## Agent 隔离

`WithProfile` 决定 Agent 使用的 provider 配置目录。`profile.CloneNative` 从本机配置克隆独立 profile，可选择带入 settings、MCP 和 skills；登录状态通过链接共享，不复制 token：

```go
worker := adaptor.New(
	claude.Driver(claude.Config{}),
	adaptor.WithProfile(profile.CloneNative("/var/agents/worker-1",
		profile.CopySettings(),
		profile.CopyMCP(),
		profile.CopySkills(),
		profile.LinkAuth(), // share the machine's login state via a symlink, so local re-logins are picked up automatically
	)),
)
```

因此，同一种 CLI 可按角色或任务同时运行多个实例，配置修改彼此隔离，也不影响平时使用的 `~/.claude` 和 `~/.codex`：

```go
isolated := func(dir string) adaptor.Option {
	return adaptor.WithProfile(profile.CloneNative(dir,
		profile.CopySettings(), profile.LinkAuth()))
}

planner := adaptor.New(codex.Driver(codex.Config{}),
	isolated("/var/agents/planner"),
	adaptor.WithPolicy(adaptor.PolicyReadOnly),
)
implementer := adaptor.New(claude.Driver(claude.Config{}),
	isolated("/var/agents/implementer"),
	adaptor.WithWorkspace("/repo/worktrees/feature-x"),
)
```

另有三种选择：`profile.Native()` 直接使用本机配置，`profile.Dedicated(dir)` 固定到宿主管理的目录，`profile.CloneFrom(src, dst, ...)` 从模板派生。profile 参与会话 fingerprint，因此只能在构造时设置，不能按次切换。

使用 `agent.ProfileState(ctx)` 查看声明资源的实际物化状态，使用 `agent.SyncProfile(ctx)` 执行物化；两者都区分 desired 资源与 observed 结果。完整用法见 [`profiles` 示例](./examples/profiles)。

## 结果与错误

成功返回 `*Result, nil`。失败只通过 Go 的 `error` 表达：业务失败返回携带可获得结果的 `*RunError`，基础设施失败则返回可包装、可解包的普通错误。

```go
result, err := agent.Send(ctx, prompt).Result()
if err != nil {
	var runErr *adaptor.RunError
	if errors.As(err, &runErr) {
		log.Printf("run failed: %s; available summary: %s", runErr.Reason, runErr.Result.Summary)
	}
	return err
}
```

`Result` 各输出层保持独立语义：

| 字段 | 内容 |
|---|---|
| `Text` | provider 最后一轮面向用户的回答文本 |
| `Summary` | 适合列表、日志和 issue comment 的简短摘要 |
| `Raw()` | 完整 stdout、stderr 和最后识别到的官方终局 payload |
| `Transcript()` | 各轮正式解析的 provider 条目，加 core 记录的补充消息交付 |
| `Services()` | 本次执行实际观察到的 runtime service 报告 |
| `Decode()` | 已校验的结构化输出 |
| `Turns` / `Usage` | provider 轮数及已观察用量总和 |
| `Messages()` / `Undelivered()` | 按接纳顺序排列的最终回执 / 含原文的失败消息 |
| `Model` / `Provider` / `Metadata` | provider 与审计信息 |

`Text` 不混入原始 stdout，也不会自动追加 Summary 或 provider 终局 payload。`Done` 与 `Stream.Result()` 表达相同结果。业务失败时，可获得的结果在 `RunError.Result` 中，直接返回的 result 为 nil。

## 集成到应用

**Web 前端**：一行代码将 Agent 包装为提供 AG-UI 协议的 `http.Handler`，CopilotKit 等兼容客户端可直接连接：

```go
mux.Handle("/agent", sse.Handler(agent, sse.Options{
	Protocol: sse.AGUI,
}))
```

聊天 UI 可使用 `agui.RunRegistry`：空闲时在 Thread 上开始执行，忙碌时向活动 Stream 追加输入；它保留原生 Stream.Send 的 context 语义，取消可撤回尚未被领取的输入。CopilotKit 示例显式将 `POST /message` 派送与补充请求的 context 脱离；派送开始后，HTTP 断开不会撤回输入或终止队列等待，交付回执仍在原 SSE 流中返回。可选的 `sessionrecorder` 默认只记录终局安全字段，额外诊断数据需通过 `WithExposure` 显式启用。路由与记录用法见[流式指南](./docs/streaming.md#8-ag-ui-bridge)。

**A2A**：`bridges/a2a` 将任意 Runner 发布为 A2A 服务，路由、认证和 TLS 由宿主负责：

```go
server := bridgea2a.NewServer(agent, bridgea2a.ServerOptions{
	AgentCard: bridgea2a.AgentCard{
		Name:        "Local coding agent",
		Description: "Runs coding tasks through agent-adaptor",
		Version:     "2.0.0",
		URL:         "https://host.example/a2a",
	},
	Session: bridgea2a.ThreadByContextID(), // the remote contextID maps stably to a local Thread key
	Options: []adaptor.CallOption{adaptor.WithPolicy(adaptor.PolicyWorkspaceWrite)},
})

mux.Handle("/.well-known/agent-card.json", server.AgentCardHandler())
mux.Handle("/a2a", server.Handler())
```

使用 `clients/a2a` 调用远程 A2A Agent。它返回 A2A task、message 和 artifact，不会将远程协议任务伪装成本地 CLI stdout 或 `Result`：

```go
client := clienta2a.New(clienta2a.Options{
	AgentCardURL: "https://remote.example/.well-known/agent-card.json",
	Auth:         clienta2a.BearerTokenFromEnv("REMOTE_A2A_TOKEN"),
})
defer client.Close()

task, err := client.Send(ctx, clienta2a.SendRequest{
	Message: clienta2a.Message{
		Role:  "user",
		Parts: []clienta2a.Part{{Kind: clienta2a.PartText, Text: "Review this change"}},
	},
})
```

需要中间进度时使用 `SendStream` / `Subscribe`。对外是否暴露思考、工具调用、审批事件或诊断字段，由默认最小暴露的 `ExposurePolicy` 控制。暴露控制、Remote 成员和协议限制见 [A2A 集成](./docs/a2a.md)。

## 多 Agent 协作

[`hosttools/team`](./hosttools/team) 将以委派为主的 leader 与宿主选定的本地 Agent、嵌套 Team 或 Remote A2A 成员组合起来。本地成员可使用不同 Driver。leader 获得四个固定工具，worker 向 leader 汇报；任务、成员更新与本地 worker 审批共用一条 Event 流。

```go
func Coordinate(ctx context.Context, leader, planner, implementer *adaptor.Agent) (*adaptor.Result, error) {
	t, err := team.New(team.Config{
		Leader: leader,
		Members: []team.Member{
			{Key: "plan", Description: "Plan the change", Runner: planner},
			{Key: "impl", Description: "Implement and test", Runner: implementer},
		},
	})
	if err != nil {
		return nil, err
	}
	defer t.Close(context.Background())
	return t.Thread("issue-123").Send(ctx, "Plan and implement TASK.md").Result()
}
```

导入 `context`、`adaptor "github.com/agent-dance/agent-adaptor/v2"` 和 `"github.com/agent-dance/agent-adaptor/v2/hosttools/team"`。Agent 的生命周期仍由宿主管理。持续输入使用 `Thread(key).Standby(ctx)`；忙碌 scope 上的 `Thread.Send` 返回 `ErrThreadBusy`。[团队指南](./docs/team.md)涵盖 Remote 成员、工具、审批、上限、持久恢复和惰性 Fork。

[真实运行示例](./examples/showcases/team-agent-workflow) 展示本地规划 → 实现 → 审查流程，包含角色沙箱和 CopilotKit 页面：

```bash
./examples/showcases/team-agent-workflow/start-all.sh claude
```

## 环境探针

`Agent.Inspect()` 是只读探针入口，用于启动前检查、环境诊断和模型选择。不支持的探针会明确返回 unsupported：

```go
environment, err := agent.Inspect().Environment(ctx) // health, CLI version, and effective message capability
models, err := agent.Inspect().Models(ctx)
quota, err := agent.Inspect().Quota(ctx)
state, err := agent.ProfileState(ctx)                // reports desired versus observed only, changes nothing
synced, err := agent.SyncProfile(ctx)                // explicitly materializes configuration resources
```

内置环境探针报告 `Version` 和 `Messages`。检测到的 CLI 版本低于消息能力所需最低版本时，禁用运行中交付，待处理消息通过 checkpoint resume 续轮，并在本次执行中发布一条 Notice。未知版本保留声明能力，依靠运行时兜底。参见 [Driver 能力表](./docs/streaming.md#12-message-delivery-capabilities)。

## 六个核心名词

整个公共模型只有六个名词：

| 名词 | 含义 |
|---|---|
| `Agent` | 配置完整、构造后即可执行的智能体 |
| `Thread` | 由宿主 key 标识、可续接和分叉的对话 |
| `Stream` | 一次正在进行的执行 |
| `Event` | 执行过程中发生的一件 typed 事件 |
| `Result` | 一次执行的最终结果与审计信息 |
| `Driver` | 一种 Agent CLI 的接入实现，供扩展作者使用 |

相应约束是：一个构造入口、一套选项合并、一条执行管线、一条事件流和一个失败判定面。

## 包布局

| 包 | 用途 |
|---|---|
| [`driver`](./driver) | 接入新 Agent 时使用的 Driver SPI |
| [`codex`](./codex)、[`claude`](./claude)、[`cursor`](./cursor)、[`codebuddy`](./codebuddy) | 内置 Driver 及各自的 Config |
| [`tool`](./tool)、[`skill`](./skill)、[`mcp`](./mcp)、[`profile`](./profile) | 面向应用开发者的能力与资源词汇 |
| [`threadstore`](./threadstore)、[`memory`](./memory) | Thread 持久化合同及内存实现 |
| [`bridges`](./bridges) | SSE、AG-UI、A2A 和 subagent-stream 协议桥 |
| [`clients/a2a`](./clients/a2a) | A2A 客户端 |
| [`hosttools`](./hosttools) | 可选的团队委派和事件记录组件 |
| [`adaptertest`](./adaptertest) | Driver 一致性测试套件 |

接入自定义 Agent CLI 时，实现 `driver.Driver` 并通过 `adaptertest`，即可使用与内置 Driver 相同的上层能力。

## 示例

- [`quickstart`](./examples/quickstart)：构造 Agent 并执行一个 prompt。
- [`streaming`](./examples/streaming)：消费事件、按需定时取消、最终文本兜底；[`streaming/chat`](./examples/streaming/chat) 在具名 Thread 上流式执行一个任务。
- [`threads`](./examples/threads)：续接、只续不建、分叉和 checkpoint 审计。
- [`structured-output`](./examples/structured-output)：类型化 JSON 输出。
- [`tools`](./examples/tools)：将类型化 Go 函数交付给真实本机 provider，无需自行管理 MCP。
- [`skills`](./examples/skills)：选择 skill 并检查证明文件；[`profiles`](./examples/profiles)：同步 profile 资源，可选本地探针或发送任务。
- [`inspect`](./examples/inspect)：两次真实调用后读取环境和模型探针，选择已声明的 reviewer skill，再观察 profile。
- [`web-chat`](./examples/web-chat)：SSE/AG-UI 服务，附带 [`aguiclient`](./examples/web-chat/aguiclient) 与 [`copilotkit`](./examples/web-chat/copilotkit) 两个前端。
- [`a2a-server`](./examples/a2a-server)：通过 A2A 发布和调用 Agent。
- [`showcases/team-agent-workflow`](./examples/showcases/team-agent-workflow)：串联规划、实现和审查。

真实调用示例依赖对应 CLI 及其登录状态。仓库的普通测试不会产生付费调用。

## 边界

core 不提供通用 HTTP/gRPC 服务框架、宿主任务队列、调度器、多租户、鉴权或数据库，也不决定一次任务应派给哪个 Agent。协议服务由 bridges 与宿主应用提供，团队角色和业务流程由宿主定义。

## 文档

- [文档地图](./docs/README.md)
- [API 参考](./docs/api-reference.md)
- [宿主定义的 Tools](./docs/tools.md)
- [流式指南](./docs/streaming.md)
- [结构化输出](./docs/structured-output.md)
- [运行策略：沙箱、审批、超时](./docs/run-policy.md)
- [A2A 集成](./docs/a2a.md)
- [公共错误](./docs/public-errors.md)

## 从 v1 迁移到 v2

所有 SDK import 与依赖均使用 `/v2` 模块路径。

| v1 | v2 |
|---|---|
| `agent.Run(ctx, prompt, opts...)` | `agent.Send(ctx, prompt, opts...).Result()` |
| `agent.Stream(ctx, prompt, opts...)` | `agent.Send(ctx, prompt, opts...)` |
| `thread.Run` / `thread.Stream` | `thread.Send`，再调用 `Result()` 或 `Events()` |
| 有两个执行方法的 `Runner` | 只有 `Send` 的 `Runner` |
| 用 `RunFinished` 判定完成 | 读取权威终局 `Done` 或调用 `Result()` |
| Thread 必须显式注入 store | 默认使用 Agent 自有内存；重启恢复时注入持久 store |

`RunAs[T]` 保留为同步类型化 helper，内部调用 `Send`。需要普通进度时先订阅 `Events()`；只调用 Result 也会保留关键事件与 drop 汇总，供稍后读取。补充输入使用 `Stream.Send`，持续聊天使用 `Thread.Standby(serverCtx)`。`Agent.Close(ctx)` 仍负责关闭时释放 Agent 自有资源。

## 许可证

除另有说明外，本仓库采用 [Apache License, Version 2.0](./LICENSE)。第三方材料保留其原有许可证与归属声明，详见[第三方声明](./THIRD_PARTY_NOTICES.md)。

Codex、Claude、Cursor、CodeBuddy 等产品名称是其各自所有者的商标。它们仅用于标识支持的集成，本项目不隶属于这些所有者，也未获得其背书。
