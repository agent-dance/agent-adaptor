# 调用方使用指南

本文档提供调用 `agent-adaptor` SDK 的典型场景示例。

架构边界与 API 合同见 [`AGENTS.md`](../AGENTS.md)；`RunPolicy` 合同见 [`run-policy.md`](./run-policy.md)。

## 1. 单 Agent

```go
sdk := agentadaptor.New(
	agentadaptor.WithDefaultAgent(codex.New(agentadaptor.CodexConfig{
		Model: "gpt-5.4",
	})),
)

result, err := sdk.Run(ctx, "fix the failing tests")
```

## 2. 多 Agent

```go
sdk := agentadaptor.New(
	agentadaptor.WithDefaultAgent(codex.New(agentadaptor.CodexConfig{
		Model: "gpt-5.4",
	})),
	agentadaptor.WithAgent("review", claude.New(agentadaptor.ClaudeConfig{
		Model: "claude-sonnet-4",
	})),
)

review, err := sdk.Agent("review")
result, err := review.Run(ctx, "review the patch")
```

## 3. Session 复用

```go
sdk := agentadaptor.New(
	agentadaptor.WithDefaultAgent(codex.New(agentadaptor.CodexConfig{
		Model: "gpt-5.4",
	})),
	agentadaptor.WithSessionStore(store),
)

result, err := sdk.Run(
	ctx,
	"continue issue-123",
	agentadaptor.WithSessionKey("company-1", "issue-123"),
)
```

## 4. 绑定默认值与调用覆盖

绑定时可以设置：

- `WithDefaultIdentity`
- `WithDefaultWorkspace`
- `WithDefaultSkills`
- `WithDefaultMCP`
- `WithDefaultRunPolicy`
- `WithDefaultInstructions`
- `WithDefaultRuntimeServices`
- `WithDefaultStreaming`
- `WithDefaultMetadata`
- `WithDefaultPermissionHandler`
- `WithDefaultPlanReviewHandler`
- `WithDefaultQuestionHandler`
- `WithNativeProfile` / `WithDedicatedProfile` / `WithCloneProfile` / `WithCloneProfileFrom`

调用时可以覆盖：

- `WithSession`
- `WithSessionKey`
- `WithContinueSession`
- `WithNewSession`
- `WithForkSession`
- `WithWorkspace`
- `WithSkills`
- `WithModel`（per-run 覆盖 binding 模型，喂给内置 driver 的 `--model`，不落盘、对三种 driver 一致）
- `WithMCP`
- `WithRunPolicy`
- `WithInstructions`
- `WithRuntimeServices`
- `WithStreaming` / `WithoutStreaming`
- `WithMetadata`
- `WithAgentIdentity`
- `WithPermissionHandler`
- `WithPlanReviewHandler`
- `WithQuestionHandler`

合并顺序固定（与 `resolveInvocation` 一致）：

- 先取 `AgentBinding` 绑定默认值（含 `RunPolicy` 指针；空指针表示全字段继承）
- 再按字段合并 per-call `RunOption`（`WithRunPolicy` 对非空字段覆盖绑定同字段；未覆盖字段继承绑定）
- adapter 的 `config` 仅表达 CLI/环境级配置，**不再**承载与 `RunPolicy` 重复的权限类 toggle；策略统一由 `RunPolicy` 表达

`RunPolicy` 合同与适配器映射见 [`run-policy.md`](./run-policy.md)。

## 5. MCP 注入

MCP 和 `skills` 一样走统一的 `resolveInvocation -> adapter.Run(...)` 主路径；宿主声明 server spec，SDK 负责合并默认值与 per-run override，adapter 负责在真实生效的 profile 中物化 provider-native 配置。

```go
sdk := agentadaptor.New(
	agentadaptor.WithDefaultAgent(codex.New(
		agentadaptor.CodexConfig{Model: "gpt-5.4"},
		agentadaptor.WithDefaultMCP(agentadaptor.MCPConfig{
			Servers: []agentadaptor.MCPServerSpec{
				{
					Key:       "docs",
					Transport: agentadaptor.MCPTransportHTTP,
					URL:       "https://example.com/mcp",
				},
			},
		}),
	)),
)

result, err := sdk.Run(
	ctx,
	"use the docs MCP",
	agentadaptor.WithMCP(agentadaptor.MCPConfig{
		Servers: []agentadaptor.MCPServerSpec{
			{
				Key:       "repo-tools",
				Transport: agentadaptor.MCPTransportStdio,
				Command:   "npx",
				Args:      []string{"repo-mcp"},
			},
		},
	}),
)
```

MCP override 规则与 `skills` 的默认值 / 调用覆盖规则相似，但有一个关键区别：

- 未显式传 `WithMCP(...)` 时，继承 binding default
- 显式传 `WithMCP(...)` 时，整组 `Servers` 覆盖 binding default
- `WithSkills(...)` 是追加合并：binding defaults、per-run refs、provider required skills 取并集
- built-in adapters 会把 skills / MCP / instructions / agents / hooks / config 合成 `ProfilePayload.Fingerprint` 并写入 checkpoint；resume 时 profile fingerprint 变化会返回 `ErrResumeRejected`，`continue_or_start` 可自动 fresh start


## 6. 本地 profile 目录

当前 built-in adapter profile API 通过 profile option 指定 provider-native profile 目录：

- `WithNativeProfile()`：复用 provider 原生共享 profile。
- `WithDedicatedProfile(dir)`：使用宿主专用 profile 目录，并在需要时安全初始化基础目录。
- `WithCloneProfile(dir, opts)`：使用宿主专用 profile 目录，从 native profile 按白名单补齐缺失的 settings/MCP/skills，并可按 `AuthMode` 共享或复制认证文件。
- `WithCloneProfileFrom(src, dst, opts)`：高级场景下从指定源 profile 派生到指定目标目录。

这些 option 将映射到 provider-native profile env：

- `codex` -> `CODEX_HOME`
- `claude` -> `CLAUDE_CONFIG_DIR`
- `cursor` -> `CURSOR_HOME`

`WithDedicatedProfile(dir)` 将只选择并初始化专用目录，不会自动把 `~/.codex`、`~/.claude`、`~/.cursor` 里的历史 settings、认证、cache 或 session 全量复制到新目录。需要派生配置时使用 `WithCloneProfile(dir, opts)`；认证必须显式 opt-in，OAuth CLI 优先用 `CloneProfileAuthLink` 共享本机登录态，只有确实需要静态副本时才用 `IncludeAuth` / `CloneProfileAuthCopy`。

优先级固定为：

1. `CommonConfig.Env` 中的 provider-specific env
2. profile option
3. 进程环境中的 provider-specific env
4. adapter 默认 profile

profile option 的设计背景见 [`workstream-profile-user-experience.md`](./workstream-profile-user-experience.md)；该文档是历史 workstream 记录，不是新的 API 入口。

## 6.1 Skills 当前合同

最小用法是直接把本地目录 / inline skill 作为 `SkillRef` 传给 binding 或 run：

```go
sdk := agentadaptor.New(
	agentadaptor.WithDefaultAgent(codex.New(
		agentadaptor.CodexConfig{Model: "gpt-5.4"},
		agentadaptor.WithDefaultSkills(agentadaptor.LocalSkill("./skills/write-proof")),
	)),
)
```

如果 skill 来自宿主自己的 store，注入 `WithSkillProvider(provider)`；如果 store 也能枚举完整清单，则同时实现 `SkillCatalog`，这样 `Admin().Default().ListSkills(ctx)` 能展示 catalogue。静态表可以直接用 `WithSkillSet(agentadaptor.SkillSet{...})`。

Claude / Codex / Cursor 都把 selected skills 物化到本次 effective profile 的 skills home：

- Claude: `<CLAUDE_CONFIG_DIR>/skills`
- Codex: `<CODEX_HOME>/skills`
- Cursor: `<CURSOR_HOME>/skills`

Claude 不再因为 selected skills 自动追加 prompt-bundle `--add-dir`；旧 checkpoint 只有 `prompt_bundle_key` 时仍按 legacy guard 处理。

Selected skill 物化失败是启动前错误，而不是 best-effort warning：坏 zip、缺少 `SKILL.md`、本地路径不可用或自定义 materializer 报错时，`Run` / `Start().Wait()` 会返回匹配 `ErrSkillMaterializationFailed` 的错误，adapter 不会启动。

`Admin().Default().ProfileSnapshot(ctx)` 报告 desired / observed profile resource 状态；`SyncProfile(ctx)` 现在覆盖 skills、MCP、agents、hooks、instructions 和 config capability patches。未支持的字段仍会以 warning / error 暴露，不会被伪装成 managed。

`examples/showcases/managed-profile` 是最直接的 profile-resources live smoke。运行前应先执行 `examples/recipes/admin-preflight`；命令缺失或认证失败属于环境门禁，不能当成 profile resource 能力通过，也不能当成实现回归。当前 provider 支持状态见 [`capabilities.md`](./capabilities.md)。

Admin 里的 `SetSelectedSkills(ctx, keys)` 只是**进程内 selection override**，不会替宿主持久化用户偏好。需要长期保存勾选状态时，宿主应该写入自己的数据库，并在构造 binding 或调用时通过 `WithDefaultSkills` / `WithSkills` 重新声明。

## 7. 宿主集成 — 维度对齐

宿主集成 SDK 时常踩的第一个坑：**ID 命名层级混淆**。SDK 自己有 SessionKey / SessionID / RunID 三层；AG-UI 协议引入 ThreadID 第四层。下表把四层对齐给出一个权威坐标，避免宿主用错：

| 层 | ID | 来源 | 值示例 | 跨层关系 |
|---|---|---|---|---|
| ① 业务/UI | `ThreadID` | Web IDE / Workflow / 业务方下发 | `"task-req-12345"` | 一个 Thread 可对应 N 个 SessionID（fork / start_new 触发新 SessionID） |
| ② SDK API 入参 | `SessionKey = (Namespace, Key)` | 宿主，或 agui bridge 自动派生 `("agui", ThreadID)` | `("agui", "task-req-12345")` | 1 ThreadID : 1 SessionKey（命名空间隔离）|
| ③ SDK driver 句柄 | `SessionID` | SDK 自身（fingerprint based） | `"claude-9c22b132-..."` | 1 SessionKey : N SessionID（fingerprint 漂移会 mint 新 ID）|
| ④ 执行实例 | `RunID` | SDK 在 `Start()` 内分配 | `"run-2026-04-26-xx"` | 1 SessionID : N RunID |

> **重要**：`SessionKey` 是**术语概念**，SDK 公共 API 里**不存在 `SessionKey` 类型**。它在 `SessionRequest{Namespace, Key, ...}` 里以二元组形式出现。新人 grep `SessionKey` 找不到对应类型不是 bug，是术语 / 类型分层。完整图示见 [`AGENTS.md`](../AGENTS.md) §6.1。

### Fork 场景流转示例

用户在同一个 chat 里点了"重新生成"按钮，AG-UI bridge 把这个动作翻译成 `SessionFork`：

```
Run 1 (initial):
  ThreadID    = "thread-A"
  SessionKey  = ("agui", "thread-A")
  SessionID   = "sess-001"  # 首次创建
  RunID       = "run-001"

Run 2 (continue):
  ThreadID    = "thread-A"  # 不变
  SessionKey  = ("agui", "thread-A")  # 不变
  SessionID   = "sess-001"  # 复用
  RunID       = "run-002"

Run 3 (fork from run-001's checkpoint):
  ThreadID    = "thread-A"  # 不变
  SessionKey  = ("agui", "thread-A")  # 不变
  SessionID   = "sess-002"  # NEW！fork 切出新 driver session
  RunID       = "run-003"
```

宿主侧的 `sessionrecorder` 如果用 `sessionKey = ThreadID` 风格，Run 1/2/3 的 stream history 全部累积在同一个 `sessionrecorder` 文件下；如果用 `sessionKey = SessionID` 风格，Run 3 会单独起一份。两种都合法，按你的 UI 模型选。

## 8. 宿主集成 — 命名陷阱

下面 5 种是真实生产宿主反复踩过的命名错误，每条给出"应该怎么写"的修正示例：

### 陷阱 1：把宿主自己的 `ThreadHistoryStore` 叫 `SessionStore`

```go
// 错：与 SDK 撞名，且语义完全不同
type SessionStore interface {
    SaveHistory(threadID string, payloads []StreamPayload) error
    LoadHistory(threadID string) ([]StreamPayload, error)
}

// 对：用语义清晰的名字，与 SDK 的 SessionStore 区分
type ThreadHistoryStore interface {
    SaveHistory(threadID string, payloads []StreamPayload) error
    LoadHistory(threadID string) ([]StreamPayload, error)
}
```

SDK 的 `SessionStore` 索引的是 driver-level resume tokens / lease / fingerprint（按 SessionID 索引），跟用户面 thread history 是两个 ontology 维度。撞名会让 review 时所有人都误以为它实现了 `agentadaptor.SessionStore` 接口。

### 陷阱 2：把 `RunID` 当 `SessionID` 使用

```go
// 错：把 RunID 灌进 SessionStore 的 SessionID 槽位
sdk.WithContinueSession(handle.RunID())  // 永远找不到对应 SessionRecord

// 对：从 RunResult.Session 取 SessionID
result, _ := handle.Wait(ctx)
sdk.WithContinueSession(result.Session.ID)
```

RunID 的生命周期是单次 Run；SessionID 跨多次 Run。混用会让 fork / continue 立刻 `ErrSessionNotFound`。

### 陷阱 3：把 `sessionrecorder` 的 `sessionKey` 当 `SessionID` 使用

```go
// 错：用 SessionID 当 sessionKey
recorder.Record(ctx, result.Session.ID, payload)
// fork 之后 SessionID 换了，新 Run 写入新文件，无法跨 fork 累积同 thread 历史

// 对：用 ThreadID（或一个跨 fork 稳定的业务键）
recorder.Record(ctx, threadID, payload)
```

`sessionrecorder.sessionKey` 是**宿主 ontology 中立聚合键**，与 SDK 的 SessionID 是两层。详见 [`pkg/hosttools/sessionrecorder/doc.go`](../pkg/hosttools/sessionrecorder/doc.go)。

### 陷阱 4：在 outbound API 里把 `task_id` 和 `thread_id` 混用

```go
// 错：业务侧 1 个 task 在 UI 上可重试 3 次，每次 1 个 thread
// task_id 和 thread_id 是 1:N 关系，但代码里当成同一个字段
type Response struct { TaskID string `json:"thread_id"` }

// 对：明确两者是不同维度
type Response struct {
    TaskID   string `json:"task_id"`
    ThreadID string `json:"thread_id"`
}
```

混用会让"重试"语义全错（task 维度的状态被 thread 状态覆盖）。

### 陷阱 5：给 `SessionStore` 加 `SaveHistory / LoadHistory` 方法

```go
// 错：违反 SessionStore 的 ontology 边界
type MySessionStore struct { ... }
func (s *MySessionStore) Resolve(...)
func (s *MySessionStore) Finalize(...)
func (s *MySessionStore) SaveHistory(...) // ← 不在 SessionStore 接口里
func (s *MySessionStore) LoadHistory(...) // ← 不在 SessionStore 接口里

// 对：独立组合 sessionrecorder
type MyHostState struct {
    SessionStore agentadaptor.SessionStore         // SDK 的；只管 driver resume
    History      sessionrecorder.Recorder           // hosttools 的；只管 thread history
    Tasks        *MyTaskStore                       // 你的；只管业务 task
}
```

SDK 不会在 `SessionStore` 接口上加任何 history 相关方法；如果你的 `MySessionStore` 实现了它们，是死方法，永远不会被 SDK 调用。详见 [`session_types.go`](../session_types.go) 上 `SessionStore` 的 doc comment。
