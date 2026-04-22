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

- [`examples/codex-basic`](./examples/codex-basic)：最小默认 Agent 执行。
- [`examples/codex-stream`](./examples/codex-stream)：`Start(...)` 与事件流。
- [`examples/codex-sessions`](./examples/codex-sessions)：服务化 session 复用。
- [`examples/codex-admin-named`](./examples/codex-admin-named)：命名 Agent 与管理接口。
- [`examples/codex-skills-live`](./examples/codex-skills-live)：skills 实时注入与同步。
- [`examples/mock-runtime-admin`](./examples/mock-runtime-admin)：运行时服务与管理信息输出。
- [`examples/session-codec-inspect`](./examples/session-codec-inspect)：安全检查适配器 session 参数。
- [`examples/mock-adapter-playground`](./examples/mock-adapter-playground)：自定义适配器 playground。

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

`Start(...)` 会返回 `RunHandle`，里面有 `Events()`、`Wait(...)` 和 `Cancel(...)`；你的应用可以用同一套执行接口处理流式输出，而不用再维护第二套 API。

## 能力面

| 能力 | 说明 |
| --- | --- |
| Execution | `Run(...)` 用于同步执行，`Start(...)` 用于流式句柄。 |
| Sessions | 默认无状态；注入 `SessionStore` 后支持 `continue_or_start`、`continue_only`、`start_new` 和 `fork`。 |
| Skills | 用同一条流程处理 skills 的解析、规范化、组装和同步。 |
| Runtime Services | 在运行前准备好需要的运行时服务，并在清理阶段按 `RunID` 释放。 |
| Admin API | 提供管理接口，用来做环境检查、模型枚举与探测、配置字段展示、额度查询和 skills 管理。 |
| Run Results | 返回统一输出、执行记录、provider/model/cost 元数据、运行时服务状态，以及结构化 question/failure。 |

## 内置包

内置包返回的是配置完成的 `AgentBinding`，而不是底层适配器。

- `github.com/agent-dance/agent-adaptor/codex`
- `github.com/agent-dance/agent-adaptor/claude`
- `github.com/agent-dance/agent-adaptor/cursor`

如果你需要更底层的扩展接口，每个内置包也都提供 `NewAdapter()`。

对于内置适配器，`CommonConfig.AgentProfileDir` 可以让你统一指定本地 agent 的配置目录，而不必每次手动写各家自己的环境变量。

## 管理接口

`sdk.Admin()` 只负责管理相关能力，不执行 prompt。

当你的应用需要在执行前后检查已绑定 agent 时，可以用它：

- `CheckEnvironment(...)` 用于真实环境检查。
- `ListModels(...)` 与 `DetectModel(...)` 用于模型可见性和探测。
- `ConfigSchema(...)` 用于生成配置界面需要的字段信息。
- `GetQuota(...)` 用于在支持时返回真实额度或 credit 窗口。
- `ListSkills(...)` 与 `SyncSkills(...)` 用于 skill 清单与期望 skill 同步。

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

更深入的协议说明、草案和工作流文档放在 [`docs/`](./docs) 下。

- [`docs/profile-resolver-api.md`](./docs/profile-resolver-api.md)
- [`docs/paperclip-alignment-roadmap.md`](./docs/paperclip-alignment-roadmap.md)
- [`docs/workstream-adapter-conformance-kit.md`](./docs/workstream-adapter-conformance-kit.md)
- [`docs/workstream-session-codec.md`](./docs/workstream-session-codec.md)
- [`docs/workstream-runtime-service-lifecycle-v2.md`](./docs/workstream-runtime-service-lifecycle-v2.md)
- [`docs/workstream-mcp-profile-materialization.md`](./docs/workstream-mcp-profile-materialization.md)
- [`docs/workstream-builtin-probes.md`](./docs/workstream-builtin-probes.md)
- [`docs/workstream-transcript-contract.md`](./docs/workstream-transcript-contract.md)
- [`docs/workstream-bridges-profiles-host.md`](./docs/workstream-bridges-profiles-host.md)
