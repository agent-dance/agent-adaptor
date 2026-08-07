# agent-adaptor

[English](./README.md) | [日本語](./README.ja.md) | [한국어](./README.ko.md) | [Deutsch](./README.de.md)

`agent-adaptor` 是一个 SDK，它提供了一套简单、符合直觉的 API，统一驱动如 `Codex`、`Claude Code`、`Cursor`、`CodeBuddy` 在内的不同 Agent 形态，并提供基础调用之外的诸多增强能力。

```go
agent := adaptor.New(codex.Driver(codex.Config{Model: "gpt-5.6-sol"}))
result, err := agent.Run(ctx, "修复失败的测试")
```

改用 Claude Code 只需要换掉构造里的 Driver，其余代码不动。

## 能力概览

- **统一配置**：一套 API 控制不同 Agent 的 skills/MCP/系统提示词/模型/沙箱/工具/审批。
- **流式响应**：可选流式输出，根据场景识别思考过程、文本输出、工具调用、决策请求。
- **会话管理**：支持对话无缝续接与分叉。直接使用你的业务 ID（如工单号、用户 ID）作为会话标识，不需要考虑底层复杂的会话管理细节。
- **人工决策**：通过回调或事件轻松回答提问、拦截高危命令、确认计划，内置决策回填机制，支持将决策持久化到云端，而不局限于本地。

## 高级功能

- **结构化输出**：只需要定义 Go 结构体并调用 `RunAs[T]`，即可执行 Agent 并约束返回填好数据的对象。
- **多协议修饰**：内置 A2A/AGUI 等协议修饰，一行代码即可将 Agent 包装为支持 SSE + AGUI 流式输出的标准 Agent，搭配业务自定义前端、客户端即可提供成熟 Agent 服务（自带可运行的 CopilotKit 前端示例）。
- **Multi Agent**：支持跨 Driver 的 Team Agent 模式，如以 Codex 作为 Leader Agent 自主控制 Plan Agent（Codex）、Coding Agent（Claude）、Reviewer Agent（Cursor）协同完成工作，所有进度和输出均自动汇总到 Leader Agent 的事件流中（参考 examples/showcases/team-agent-workflow 示例）。
- **Agent 隔离**：支持复制本机 Agent 配置和登录状态到独立目录运行，使修改不影响本机在用的 Agent。因此，当你需要同时创建多个 Codex/Claude Code 实例并行开发、或扮演不同角色时，可以轻而易举地做到。

## 安装

```bash
go get github.com/agent-dance/agent-adaptor
```

需要 Go 1.26.5 及以上。

重要：**运行时需要对应的 Agent 已安装并完成登录**

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

四个内置 Driver 构造方式一致，各自带自己的 `Config`：

```go
codexAgent := adaptor.New(codex.Driver(codex.Config{}))
claudeAgent := adaptor.New(claude.Driver(claude.Config{}))
cursorAgent := adaptor.New(cursor.Driver(cursor.Config{}))
codeBuddyAgent := adaptor.New(codebuddy.Driver(codebuddy.Config{}))
```

## 流式执行

`Stream` 把一次执行展开成一条强类型事件流，结束时给出 `Result`：

```go
stream := agent.Stream(ctx, "解释准备提交的补丁")
defer stream.Cancel()

for event := range stream.Events() {
	switch event := event.(type) {
	case adaptor.TextDelta:
		fmt.Print(event.Text)
	case adaptor.Thinking:
		fmt.Fprint(os.Stderr, event.Text)
	case adaptor.ToolCall:
		if event.Phase == adaptor.PhaseStart {
			fmt.Printf("\n[调用工具：%s]\n", event.Name)
		}
	case *adaptor.ApprovalRequest:
		_ = event.Approve(ctx)
	case adaptor.Dropped:
		log.Printf("背压丢弃了 %d 个增量事件", event.Count)
	}
}

result, err := stream.Result()
```

文本、思考、工具调用与结果、进程信息、生命周期、子 Agent 进度、审批请求都在这一条流里，没有第二条通道。

提前结束消费时调用 `Cancel()`，它是幂等的。

## 人工审批与沙箱

沙箱强度、联网与浏览器工具、审批模式在同一个 `Policy` 里，构造处是默认值，`Run` / `Stream` 处可以按次整体覆盖：

```go
reviewer := adaptor.New(
	claude.Driver(claude.Config{}),
	adaptor.WithPolicy(adaptor.Policy{
		Sandbox:   adaptor.ReadOnly,    // 只读工作区，适合评审、规划类角色
		WebSearch: adaptor.FeatureDeny, // 明确关掉联网搜索
		Browser:   adaptor.FeatureDeny,
		Approvals: adaptor.ApprovalPolicy{
			Permission: adaptor.ApprovalAsk, // 高危命令交给人
			PlanReview: adaptor.ApprovalAsk,
			Question:   adaptor.QuestionAsk, // 默认是自动拒绝提问
			Timeout:    2 * time.Minute,
			OnTimeout:  adaptor.FallbackAbort,
		},
	}),
)
```

沙箱有 `ReadOnly`、`WorkspaceWrite`、`Unrestricted` 三档，`PolicyReadOnly` 这类预设就是只设了 `Sandbox` 的快捷值。所选 Driver 不支持某个维度时，会在启动进程前明确报错，而不是静默降级。

审批有两种消费形态，二选一。挂了回调就是回调式，适合 CLI 与无人值守：

```go
agent := adaptor.New(
	claude.Driver(claude.Config{}),
	adaptor.OnApproval(func(ctx context.Context, req *adaptor.ApprovalRequest) error {
		switch req.Kind {
		case adaptor.ApprovalPermission:
			return req.Approve(ctx)
		case adaptor.ApprovalQuestion:
			return req.Answer(ctx, "用 PostgreSQL")
		default:
			return req.Deny(ctx, "计划需要人工确认")
		}
	}),
)
```

无人值守也可以直接用现成的 `adaptor.ApproveAll()` 和 `adaptor.DenyAll(reason)`。

不挂回调就是事件式：请求作为 `*adaptor.ApprovalRequest` 出现在事件流里，自带 responder，可以先停放、之后由任意 goroutine 或另一个 HTTP 请求回填——这正是 Web 场景需要的形态：

```go
for event := range stream.Events() {
	switch event := event.(type) {
	case *adaptor.ApprovalRequest:
		pending.Add(threadKey, event) // 停放请求，推给前端渲染
	case adaptor.Notice:
		// SDK 会广播每一个已落定的决策，含策略自动批准与超时兜底，
		// 所以待决列表不需要宿主自己对账。
		if event.Kind == adaptor.NoticeApprovalResolved {
			if id, ok := event.Data["request_id"].(string); ok {
				pending.Remove(threadKey, id)
			}
		}
	}
}
```

`pending` 是宿主自己的存储，前端拿到请求后在另一个 HTTP 请求里回填决策：

```go
func (h *host) resolveDecision(w http.ResponseWriter, r *http.Request) {
	req := h.pending.Take(threadKey, requestID)
	if err := req.Approve(r.Context()); err != nil {
		sse.WriteApprovalError(w, err) // 已落定/已过期 → 410，Kind 不匹配 → 400
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
```

应答是 exactly-once 的：重复应答、Kind 不匹配、运行已结束都返回稳定错误（`ErrApprovalResolved`、`ErrApprovalKindMismatch`、`ErrApprovalExpired`），零值请求也不会永久阻塞。没人应答时按 `Policy.Approvals` 的 `OnTimeout` 兜底，被拒之后走 `OnReject`。停放的请求存在哪里由宿主决定，不限于进程内存。

完整可跑的 Web HITL 通路见 [`web-chat/copilotkit`](./examples/web-chat/copilotkit)：`/decision/pending` 与 `/decision/resolve` 两个端点，刷新页面后未决决策仍能恢复。

## 多轮会话

Agent 默认无状态。需要对话连续性时，注入一个 store 即可：

```go
agent := adaptor.New(
	claude.Driver(claude.Config{}),
	adaptor.WithThreadStore(memory.NewStore()),
)
defer agent.Close(context.Background())

thread := agent.Thread("tenant-42/issue-123")        // 映射的会话已经存在则接续、不存在则创建
result, err := thread.Run(ctx, "继续排查这个问题")

only := agent.Thread("tenant-42/issue-123", adaptor.ResumeOnly()) // 只续不建
branch := thread.Fork("tenant-42/issue-123/plan-b")               // 从当前进度分叉
```

几条约定：

- **会话 key 是业务自己的字符串**，SDK 原样保存、原样比较。开一段全新对话就换一个 key，SDK 不提供把旧 key 重绑到新会话的入口。
- **同一个 Thread 同时只有一次执行**，由租约保证，过期的执行不会覆盖新状态。
- **续接前会校验兼容性**，Driver、模型、解析后的真实 workspace、配置、skills、MCP 都参与指纹计算，其中任何一项漂移都不会错误复用会话。
- **失败不污染状态**，非零退出、协议错误、取消都不产生有效 checkpoint，之前健康的会话记录保持原样。
- **常驻进程默认复用**，在 Windows、macOS 和 Linux 上，Claude、CodeBuddy、Codex 都会在显式 Thread 下跨轮复用同一个进程；某一轮或每一轮需要新进程时加 `adaptor.WithSpawn()`。Cursor 和无状态调用始终每轮启动新进程。`Close` 之后的执行返回 `ErrAgentClosed`。

单进程场景用 `memory.NewStore()`，需要持久化则实现 `threadstore.Store`。

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

Schema 从 Go 类型生成，优先走各家原生的 schema 约束。当前通道或策略不支持时自动回退到提示词约束加本地校验，两者都不可用才在执行前失败。返回值里既有 typed 值，也有完整的审计 `Result`。

细节见 [`structured-output` 示例](./examples/structured-output)和[结构化输出文档](./docs/structured-output.md)。

## 选项与资源

选项只有一套词汇，作用域由类型在编译期区分：

| 类型 | 能用在哪 |
|---|---|
| `Option` | 只能用于 `adaptor.New` |
| `CallOption` | 只能用于 `Run` / `Stream` |
| `SharedOption` | 两处都能用，调用处覆盖构造处 |

合并规则只有一条：近处覆盖远处，skills 追加，其余按各自约定替换或合并。

同一套选项覆盖各家 Agent 的主要配置面：

| 想控制什么 | 用什么 |
|---|---|
| 模型 | `WithModel` |
| 系统提示词 | `WithInstructions` |
| 工作目录 | `WithWorkspace`，隔离工作树用 `WithWorkspaceSpec` |
| skills | `WithSkills` 配 `skill.Dir` / `skill.FS` / `skill.Inline` / `skill.Key` / `skill.Require` |
| MCP | `WithMCP` 配 `mcp.Stdio` / `mcp.HTTP` / `mcp.SSE` |
| 沙箱、联网、浏览器工具、审批 | `WithPolicy`，交互式再加 `OnApproval` |
| 配置目录与资源 | `WithProfile`、`WithProfileResources` |
| 超时、审计元数据、调用方身份 | `WithTimeout`、`WithMetadata`、`WithIdentity` |
| 会话持久化 | `WithThreadStore` |

```go
agent := adaptor.New(
	codex.Driver(codex.Config{}),
	adaptor.WithModel("gpt-5.4"),
	adaptor.WithInstructions("你是这个仓库的评审者：只读代码，先给结论再给证据。"),
	adaptor.WithSkills(skill.Dir("./skills/review")),
	adaptor.WithMCP(mcp.Stdio("repo-tools", "repo-mcp", mcp.Args("serve"))),
	adaptor.WithProfile(profile.Dedicated("./profiles/reviewer")),
	adaptor.WithTimeout(10*time.Minute),
)

result, err := agent.Run(ctx, "评审这个改动",
	adaptor.WithModel("gpt-5.4-mini"),
	adaptor.WithSkills(skill.Require(skill.Dir("./skills/security"), "本次必须过安全检查")), // 追加，不顶掉默认 skills
	adaptor.WithMetadata("request_id", requestID),
)
```

同一份配置换个 Driver 就换个 Agent；某个 Driver 不支持其中某项能力时，会在启动前明确报错而不是悄悄忽略。

```go
codexReviewer := adaptor.New(codex.Driver(codex.Config{}), reviewerOptions...)
claudeReviewer := adaptor.New(claude.Driver(claude.Config{}), reviewerOptions...)
```

## 宿主自定义 Tools

用 typed Go 函数直接给 Agent 加能力，不需要自己构造和维护 MCP server：

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

`WithTools` 只能用于构造期，整组替换 Tool 集合。schema 默认从 handler 的 Go 类型推导，也可以显式给标准 JSON Schema。`tool.Reject(code, message)` 表示可以安全展示给模型的业务失败，普通 error 和 panic 会被净化。有状态 Thread 用到的每个 Tool 都要设 `tool.Revision`，让 handler 的行为变化进入续接兼容性判断。

MCP 在这里只是内部交付机制：已有的或远程的 MCP server 仍然走 `WithMCP`；内置 Driver 会把 Tools 物化到 SDK 自有的隔离 profile，不动你配置的原生 profile。生命周期、schema、错误、安全与 Thread 语义见[宿主自定义 Tools 合同](./docs/tools.md)。

## Agent 隔离

`WithProfile` 决定这个 Agent 用哪份 provider 配置目录。`profile.CloneNative` 从本机原生配置克隆出一份独立 profile，可以选择带上 settings、MCP、skills；登录状态用共享链接而不是复制 token：

```go
worker := adaptor.New(
	claude.Driver(claude.Config{}),
	adaptor.WithProfile(profile.CloneNative("/var/agents/worker-1",
		profile.CopySettings(),
		profile.CopyMCP(),
		profile.CopySkills(),
		profile.LinkAuth(), // 软链的方式共享本机登录态，本机登录态变更会自动跟随
	)),
)
```

于是同一个 CLI 可以按角色或按任务开多份实例并行跑，各自的配置改动互不影响，也不会动到你本机在用的 `~/.claude`、`~/.codex`：

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

另外三种选择：`profile.Native()` 直接用本机原生配置；`profile.Dedicated(dir)` 钉在一个你自己管理的目录；`profile.CloneFrom(src, dst, ...)` 从模板目录派生。profile 参与会话指纹，所以它只能是构造期选项，不能按次调用切换。

声明的资源到底物化了什么、Driver 是否真的认，用 `agent.ProfileState(ctx)` 读、`agent.SyncProfile(ctx)` 物化，两者都只报告实际观察结果。完整演示见 [`profiles` 示例](./examples/profiles)。

## 结果与错误

成功返回 `*Result, nil`。失败只走 Go 的 `error` 一条路：已经执行完但业务失败的，返回带上可用 `Result` 的 `*RunError`；基础设施类失败是普通的可包装 error。

```go
result, err := agent.Run(ctx, prompt)
if err != nil {
	var runErr *adaptor.RunError
	if errors.As(err, &runErr) {
		log.Printf("执行失败：%s；已有摘要：%s", runErr.Reason, runErr.Result.Summary)
	}
	return err
}
```

`Result` 各层输出互不污染：

| 字段 | 内容 |
|---|---|
| `Text` | 最终面向用户的回答文本 |
| `Summary` | 适合列表、日志、issue 评论的简短摘要 |
| `Raw()` | 完整 stdout、stderr，以及各家正式协议的终局 payload |
| `Transcript()` | Driver 从正式协议解析出的标准化条目 |
| `Services()` | 本次执行实际观察到的 runtime services |
| `Decode()` | 已校验的结构化输出 |
| `Usage` / `Model` / `Provider` / `Metadata` | 用量与审计信息 |

`Text` 里不会混进原始 stdout，也不会自动拼上摘要或各家的终局 payload。`Run` 和 `Stream.Result()` 拿到的内容逐字段等价。

## 接入上层应用

**Web 前端**，一行把 Agent 包装成 `http.Handler`，走 AG-UI 协议，兼容 AG-UI 的客户端（比如 CopilotKit）可以直接接入：

```go
mux.Handle("/agent", sse.Handler(agent, sse.Options{
	Protocol: sse.AGUI,
}))
```

**A2A**，`bridges/a2a` 把任意 Runner 发布成 A2A server，宿主只负责挂路由、鉴权和 TLS：

```go
server := bridgea2a.NewServer(agent, bridgea2a.ServerOptions{
	AgentCard: bridgea2a.AgentCard{
		Name:        "Local coding agent",
		Description: "Runs coding tasks through agent-adaptor",
		Version:     "1.0.0",
		URL:         "https://host.example/a2a",
	},
	Session: bridgea2a.ThreadByContextID(), // 远端 contextID 稳定映射成本地 Thread key
	Options: []adaptor.CallOption{adaptor.WithPolicy(adaptor.PolicyWorkspaceWrite)},
})

mux.Handle("/.well-known/agent-card.json", server.AgentCardHandler())
mux.Handle("/a2a", server.Handler())
```

反向调用远端 A2A Agent 用 `clients/a2a`，它返回的是 A2A 的 task、message、artifact，不会假装远端协议任务有本地 CLI 的 stdout 或 `Result`：

```go
client := clienta2a.New(clienta2a.Options{
	AgentCardURL: "https://remote.example/.well-known/agent-card.json",
	Auth:         clienta2a.BearerTokenFromEnv("REMOTE_A2A_TOKEN"),
})
defer client.Close()

task, err := client.Send(ctx, clienta2a.SendRequest{
	Message: clienta2a.Message{
		Role:  "user",
		Parts: []clienta2a.Part{{Kind: clienta2a.PartText, Text: "评审这个改动"}},
	},
})
```

需要中间过程就用 `SendStream` / `Subscribe`。对外暴露思考过程、工具调用、审批事件还是诊断字段，由 `ExposurePolicy` 控制，默认最小暴露。

## 多 Agent 协作

`agent-adaptor` 支持以 A2A 的标准协议跨 Driver 实现多 Agent 协作（因此也支持任意远程 A2A 协议的 Agent）。

跨 Driver 协作的价值在于保住模型与其原生 `Harness` 之间的适配优势：GPT 系列模型在 Codex 上表现更好，Claude 系列模型在 Claude Code 上能力更强。因此 `agent-adaptor` 的设计取向是让每个模型都留在最适合它的 Harness 里参与协作，而不是为了开启多模型协作，就去迁就一个兼容多个模型但表现不好的通用 Harness。

核心代码示例如下：

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
```

完整的 [`team-agent-workflow`](./examples/showcases/team-agent-workflow) 里有角色级沙箱、结构化 `PLAN.md` 产物、workspace 审计，以及带实时子 Agent 卡片的 CopilotKit 页面，一条命令启动：

```bash
./examples/showcases/team-agent-workflow/start-all.sh claude
```

## 环境探针

`Agent.Inspect()` 是只读探针，用来做启动前检查、环境诊断和模型选择。不支持的探针会明确返回 unsupported，不会编造数据：

```go
environment, err := agent.Inspect().Environment(ctx) // 健康状态与逐项诊断，可直接渲染
models, err := agent.Inspect().Models(ctx)
quota, err := agent.Inspect().Quota(ctx)
state, err := agent.ProfileState(ctx)                // 只报告期望与实际，不做变更
synced, err := agent.SyncProfile(ctx)                // 显式物化配置资源
```

## 六个名词

整个库的公共模型只有六个名词：

| 名词 | 含义 |
|---|---|
| `Agent` | 配置完整、构造后即可执行的智能体 |
| `Thread` | 由业务 key 标识、可续接可分叉的一段对话 |
| `Stream` | 一次正在进行的执行 |
| `Event` | 执行过程中发生的一件强类型事件 |
| `Result` | 一次执行的最终结果与审计信息 |
| `Driver` | 某个 Agent CLI 的接入实现，扩展方才需要关心 |

配套的约束是：一个构造入口、一套选项合并规则、一条执行管线、一条事件流、一个失败判定入口。

## 包一览

| 包 | 用途 |
|---|---|
| [`driver`](./driver) | Driver SPI，接入新 Agent 时用 |
| [`codex`](./codex)、[`claude`](./claude)、[`cursor`](./cursor)、[`codebuddy`](./codebuddy) | 内置 Driver 与各自的 Config |
| [`tool`](./tool)、[`skill`](./skill)、[`mcp`](./mcp)、[`profile`](./profile) | 面向调用方的能力与资源词汇 |
| [`threadstore`](./threadstore)、[`memory`](./memory) | Thread 持久化接口与内存实现 |
| [`bridges`](./bridges) | SSE、AG-UI、A2A、subagent-stream 协议桥 |
| [`clients/a2a`](./clients/a2a) | A2A 客户端 |
| [`hosttools`](./hosttools) | 可选的委托编排与事件录制组件 |
| [`adaptertest`](./adaptertest) | Driver 一致性测试套件 |

接入自家的 Agent CLI：实现 `driver.Driver`，跑通 `adaptertest`，之后它和内置 Driver 有同样的上层能力。

## 示例

- [`quickstart`](./examples/quickstart)：构造 Agent 跑一次 prompt。
- [`streaming`](./examples/streaming)：事件消费与取消。
- [`threads`](./examples/threads)：续接、只续不建、分叉与 checkpoint 审计。
- [`structured-output`](./examples/structured-output)：typed JSON 输出。
- [`tools`](./examples/tools)：向真实本地 provider 暴露 typed Go 函数，不用自己管 MCP。
- [`skills`](./examples/skills) / [`profiles`](./examples/profiles)：skill 解析物化、配置资源与同步。
- [`inspect`](./examples/inspect)：环境、模型、配额、schema、skills 与配置状态。
- [`web-chat`](./examples/web-chat)：SSE/AG-UI 服务端，配 [`aguiclient`](./examples/web-chat/aguiclient) 与 [`copilotkit`](./examples/web-chat/copilotkit) 两个前端。
- [`a2a-server`](./examples/a2a-server)：通过 A2A 发布并调用 Agent。
- [`showcases/team-agent-workflow`](./examples/showcases/team-agent-workflow)：规划、实现、评审串成一条流水线。

需要真实调用的示例依赖对应 CLI 与登录状态。仓库的常规测试不会产生付费调用。

## 边界

核心库不提供 HTTP/gRPC server、队列、调度器、多租户、鉴权、数据库，也不替调用方决定一个任务该派给哪个 Agent。协议服务留给 bridges 和上层应用，团队角色与流程策略留给业务侧。

## 文档

- [文档地图](./docs/README.md)
- [API reference](./docs/api-reference.md)
- [宿主自定义 Tools](./docs/tools.md)
- [流式指南](./docs/streaming.md)
- [结构化输出](./docs/structured-output.md)
- [执行策略：沙箱、审批、超时](./docs/run-policy.md)
- [A2A 集成](./docs/a2a.md)
- [公开错误](./docs/public-errors.md)

## 许可证

除另有说明外，本仓库采用 [Apache License 2.0](./LICENSE) 授权。第三方材料
保留其各自的许可证与署名，详见[第三方声明](./THIRD_PARTY_NOTICES.md)。授权条款
以 `LICENSE` 中的英文正文为准。

Codex、Claude、Cursor、CodeBuddy 及其他产品名称均为其各自权利人的商标。
本项目仅使用这些名称标识所支持的集成，与相关权利人不存在隶属或背书关系。
