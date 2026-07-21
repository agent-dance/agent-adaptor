# agent-adaptor

[English](./README.md)

[![CI](https://github.com/agent-dance/agent-adaptor/actions/workflows/go.yml/badge.svg)](https://github.com/agent-dance/agent-adaptor/actions/workflows/go.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/agent-dance/agent-adaptor.svg)](https://pkg.go.dev/github.com/agent-dance/agent-adaptor)
[![Release](https://img.shields.io/github/v/release/agent-dance/agent-adaptor)](https://github.com/agent-dance/agent-adaptor/releases)
[![Go version](https://img.shields.io/badge/Go-1.26-00ADD8)](./go.mod)

`agent-adaptor` 是一个纯 SDK，它提供了一套通用语义用于控制 Codex、Claude Code 或 Cursor Agent 等不同 Agent 产品。它与 [ACP](https://agentclientprotocol.com/get-started/introduction) 最大的不同是：ACP 更聚焦于调用 Agent，`agent-adaptor` 则更聚焦于控制 Agent。

具体体现在 `agent-adaptor` 除了基础的统一调用接口之外，还提供了会话管理、工作区管理、权限和配置集成、Skills/MCP/Subagents/Hooks/Instructions 等能力扩展、AGUI/A2A/SSE 流式对接、人工决策、Multi Agent 协作等核心能力的支持。

简而言之，`agent-adaptor` 适合于希望在通用 coding agent CLI 之上构建产品的团队，不管是 CLI、桌面应用、HTTP/gRPC 服务、后台 worker 或定时自动化的产品，它都能提供最具价值的支持。

前置条件：Go 1.26+；下面的快速开始还要求 Codex CLI 已安装并配置好了认证。只有
Claude Code 或 Cursor 的环境可以从 provider-selection recipe 开始。

```bash
go get github.com/agent-dance/agent-adaptor
go run ./examples/recipes/basic-run
go run ./examples/recipes/provider-selection -agent=claude
go run ./examples/recipes/provider-selection -agent=cursor
```

## 快速开始

下面的程序与 [`examples/recipes/basic-run/main.go`](./examples/recipes/basic-run/main.go) 完全一致。它复制本机 codex 的配置和权限认证到一个隔离目录中，并在一个指定的 workspace 中进行交互，运行它不会对本机的 codex 产生任何副作用。
```go
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/codex"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	root, err := os.MkdirTemp("", "agent-adaptor-basic-*")
	if err != nil {
		return fmt.Errorf("create isolated environment: %w", err)
	}
	defer os.RemoveAll(root)
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		return fmt.Errorf("create temporary workspace: %w", err)
	}

	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(codex.New(agentadaptor.CodexConfig{
			CommonConfig: agentadaptor.CommonConfig{
				CWD: workspace,
			},
			SkipGitRepoCheck: true,
		}, agentadaptor.WithCloneProfile(
			filepath.Join(root, "profile"),
			agentadaptor.CloneProfileOptions{IncludeSettings: true, AuthMode: agentadaptor.CloneProfileAuthLink},
		))),
	)

	result, err := sdk.Run(ctx, "Reply in one sentence confirming the SDK call succeeded.")
	if err != nil {
		return fmt.Errorf("run agent: %w", err)
	}
	if result.Failure != nil {
		return fmt.Errorf("agent run failed: %s", result.Failure.Message)
	}

	fmt.Println(result.Output)
	return nil
}
```

要更换其他 Agent 可替换为 `claude.New(...)` 或 `cursor.New(...)`。

如服务需要返回配置错误而不是在启动时 fail fast 时，可使用 `Build(...)` 代替 `New(...)`。

## 按产品形态选择集成路径

| 产品形态 | 从这里开始 | 下一步 |
|---|---|---|
| 批处理自动化或 CI worker | [`basic-run`](./examples/recipes/basic-run) | 用 [`structured-output`](./examples/recipes/structured-output) 获得可校验的机器输出 |
| 交互式应用 | [`content-streaming`](./examples/recipes/content-streaming) | 加入 [`hitl-channel`](./examples/recipes/hitl-channel) 和持久化 session store |
| 受控工作站 | [`managed-profile`](./examples/showcases/managed-profile) | 加入 Admin preflight 和组织管理的 profile resources |
| React 直连 | [`web-agui`](./examples/showcases/web-agui) | 在宿主加入认证、持久化 replay、限流和审计 |
| 完整聊天和 HITL UI | [`web-copilotkit-hitl`](./examples/showcases/web-copilotkit-hitl) | 由宿主负责决策授权与持久化 |
| Agent-to-agent 服务 | [`a2a-local`](./examples/showcases/a2a-local) | 加入可信 discovery 和持久化 A2A task store |
| 混合 provider Agent 团队 | [`team-agent-workflow`](./examples/showcases/team-agent-workflow) | 增加持久化编排状态、返修循环和 tenant-scoped 角色 registry |

## 核心心智

- `SDK`：`sdk.Run(...)` 与 `sdk.Start(...)` 永远使用 `WithDefaultAgent(...)`
  提供的 Agent 进行调用；`sdk.Admin()` 只属于控制面。
- `Runner`：`sdk.Default()` 返回默认 Agent；`sdk.Agent(name)` 返回命名 Agent。
- `RunHandle`：提供运行 `Events()`、可选内容 `StreamEvents()`、`RunID()`、
  `Wait(...)`、`Cancel(...)`，以及异步 HITL 的 `DecisionRequests()` /
  `ResolveDecision(...)`。

## 能力支持情况

可移植合同很宽，但不代表 provider 行为完全相同。下表来自当前内置 descriptor
和测试。

| Capability | Codex | Claude | Cursor | 补充说明 |
|---|---:|---:|---:|---|
| Skills | ✅ | ✅ | ✅ | 无 |
| Instructions | ✅ | ✅ | ✅ | Claude 控制 home 目录下的 CLAUDE.md，Codex/Cursor 控制 home 目录下的 AGNETS.md |
| SubAgent | ✅ | ✅ | ✅ | 无 |
| Hooks | ✅ | ✅ | ✅ | 无 |
| Plugins | ✅ | ✅ | ✅ | 无 |
| MCP | stdio + HTTP | stdio + HTTP + SSE | stdio + HTTP + SSE | Codex 不支持 SSE 协议 |
| Execution lifecycle | ✅ | ✅ | ✅ | `Run`、`Start`、wait、cancel、timeout、result 和运行事件 |
| Session 管理（新建、关联、恢复、Fork） | ✅ | ✅ | ✅ | 无 |
| 流式输出 | ✅ | ✅ | ❌ | Codex/Claude 产生 token/reasoning/tool delta；Cursor 返回 transcript/result |
| HITL Ask | ❌ | PlanReview + Question | ❌ | 三个内置 adapter 都不支持 Permission `Ask` |
| 结构化输出 | ✅ | ✅ | ✅ | Cursor 不支持原生 Structured output，SDK 层面实现了 Prompt 注入 + Validate 机制以提供此能力 |
| Agent 配置目录隔离 | ✅ | ✅ | ✅ | 支持 native、dedicated 和 cloned profile；实际 workspace provisioning 由宿主负责 |

SDK 通用能力：

1. 复制配置和权限认证到隔离目录（允许多实例使用不同 Skills/MCP 等配置）
2. 协调业务 SessionKey、provider SessionID、兼容指纹、lease、fork 和有效
   checkpoint 持久化；未注入 `SessionStore` 时默认无状态。
4. 通过 `WorkspaceManager` 和 `RuntimeServiceManager` 接入 workspace、dev server、
   数据库或 sidecar 的生命周期，实际 provisioning 和进程/容器编排由宿主负责。
5. 统一解析和物化 Skills、MCP、Agents、Hooks、Instructions 与 config patches，
   并通过 profile snapshot 暴露实际生效状态。
6. 分层保留 assistant output、原始 stdout/stderr、标准化 transcript、终局 provider
   JSON 和结构化 failure，并提供 JSON Schema 输出校验。
7. 将运行事件、内容 streaming 和 HITL 保持为同一次 run 的可选通道，支持声明式
   policy、同步 typed handler 和异步 decision channel。
8. 通过 Admin 提供环境、模型、profile、schema、quota 和 skills 探测，并用 core
   之上的 AG-UI、SSE、A2A 包接入不同宿主形态。

## Session 与 ID 维度

宿主不注入 `SessionStore` 时，run 默认无状态。Session 模式包括
`continue_or_start`、`continue_only`、`start_new`、`fork` 和 `stateless`。
只有有效的 adapter checkpoint 才允许创建或更新 session 状态。

| ID | 所有者 | 含义 |
|---|---|---|
| `ThreadID` | UI 或 workflow | 业务会话 ID；bridge 知道它，core SDK 不知道 |
| `SessionKey` | 宿主 | `SessionRequest` 中概念性的 `(namespace, key)` 二元组；没有导出的 `SessionKey` 类型 |
| `SessionID` | SDK + adapter | 业务 key 背后的具体可恢复会话句柄 |
| `RunID` | SDK | 一次执行；一个 session 可以对应多次 run |

AG-UI bridge 约定把 `ThreadID` 映射为
`SessionKey = ("agui", ThreadID)`。失败 run 默认不会覆盖健康 checkpoint。

## 结果合同

始终按 `error -> RunResult.Failure -> success` 处理结果。返回 `error` 表示 run
没有可靠完成；`Failure` 表示 run 已完成，但终局结果是结构化失败。

| 字段 | 可移植语义 |
|---|---|
| `Output` | 只包含最终 assistant 文本，绝不混入原始 stream dump |
| `RawStreams` | 用于审计和调试的完整 stdout/stderr |
| `Transcript` | adapter 解析出的 assistant、reasoning、tool、result 标准化条目 |
| `Summary` | 适合列表、日志或评论的简短宿主摘要 |
| `Result` | 用于审计或深度检查的 provider-specific 终局事件 JSON |
| `StructuredOutput` | Schema 结果、校验状态、已解码 JSON 和校验细节 |
| `Failure` | run 完成后的结构化业务、策略或 provider 失败 |

`Run()` 与 `Start().Wait()` 返回相同的结果分层。

## Streaming 与人工决策

运行 `Events()` 与内容 `StreamEvents()` 面向不同消费者；使用 `Start` 时应同时
drain 两者。只有在检查 provider capability 后才启用 `WithStreaming()`。

HITL 在同一次 run 上支持三种宿主接入方式：

1. 在 `RunPolicy.HumanDecision` 中声明自动策略。
2. 挂载同步 typed permission、plan-review 或 question handler。
3. 异步消费 `DecisionRequests()`，再由服务或 UI 调用
   `ResolveDecision(requestID, response)`。

不支持的 `Ask` 组合会在 adapter 执行前失败。Timeout、reject 和 retry 行为都由
policy 显式声明，详见 [`docs/run-policy.md`](./docs/run-policy.md)。

## 受控上下文与 Bridge

Binding 默认值与 per-run override 覆盖 workspace、profile selection、skills、
MCP、instructions、policy、streaming、metadata 和 runtime services。未传 profile
option 时，adapter 解析仍可能选择进程环境或默认 native profile。会物化资源的宿主
应显式选择 dedicated 或 cloned profile；`WithNativeProfile()` 用于明确表达共享
profile 意图，但并不是 native profile 被选中的唯一途径。

`WithRuntimeServiceManager(...)` 让宿主按 `RunID` 准备并释放进程或容器；core SDK
不会变成 scheduler。`pkg/bridges` 下的 AG-UI、SSE、A2A 位于 core 之上，负责
转换统一 run。HTTP serving、TLS、auth、tenant isolation、queue 和持久化仍由宿主负责。

## Admin 与自定义 Adapter

用 [`admin-preflight`](./examples/recipes/admin-preflight) 在不执行 prompt 的情况下
检查环境、模型、effective profile、schema、quota 和 skills。`sdk.Admin()` 永不
执行 Agent。

第三方 provider 实现 `DriverAdapter`、声明真实 capability，再用 `BindTyped(...)`
绑定。先看 [`custom-adapter`](./examples/recipes/custom-adapter)，并用
[`adaptertest`](./adaptertest) 验证一致性。Provider 协议解析必须留在 adapter；
shared helper 不能猜测 session ID 或语义事件。

## Examples 与文档

双语 [examples catalog](./examples/README.zh-CN.md) 为每个条目标记 offline/live、
前置条件、预期证据和学习路径。当前指南按文档分别使用中英文，因此这里明确标注语言：

- [API reference（英文）](./docs/api-reference.md)
- [Usage guide（中文）](./docs/usage-guide.md)
- [Structured output（英文）](./docs/structured-output.md)
- [Streaming and bridges（中文）](./docs/streaming.md)
- [A2A integration（英文）](./docs/a2a.md)
- [Documentation map（中文）](./docs/README.md)

## 非目标

- 不内置 HTTP/gRPC server。
- 不内置 queue、scheduler、tenant system、authentication system 或 daemon。
- 不做自动 provider routing、broker、planner 或 Agent 选择。
- 不强制 database、distributed lock 或 stateful default。
- 不允许出现 session 或默认值合并语义不同的第二套执行入口。

Core 刻意保持为 library。服务形态 examples 展示宿主如何组合能力，而不是把 SDK
扩成半个应用框架。
