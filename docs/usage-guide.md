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
- `WithDefaultMetadata`

调用时可以覆盖：

- `WithSession`
- `WithSessionKey`
- `WithContinueSession`
- `WithNewSession`
- `WithForkSession`
- `WithWorkspace`
- `WithSkills`
- `WithMCP`
- `WithRunPolicy`
- `WithInstructions`
- `WithMetadata`
- `WithAgentIdentity`

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

MCP override 规则与 `skills` 一样简单：

- 未显式传 `WithMCP(...)` 时，继承 binding default
- 显式传 `WithMCP(...)` 时，整组 `Servers` 覆盖 binding default
- `skills/MCP` 的变化不会自动把当前 session 判为 incompatible；是否继续复用 session 仍由宿主通过 `SessionMode` 决定


## 6. 本地 profile 目录

计划中的 built-in adapter profile API 通过 profile option 指定 provider-native profile 目录：

- `WithNativeProfile()`：复用 provider 原生共享 profile。
- `WithDedicatedProfile(dir)`：使用宿主专用 profile 目录，并在需要时安全初始化基础目录。
- `WithCloneProfile(dir, opts)`：使用宿主专用 profile 目录，首次初始化时从 native profile 按白名单复制配置。

这些 option 将映射到 provider-native profile env：

- `codex` -> `CODEX_HOME`
- `claude` -> `CLAUDE_CONFIG_DIR`
- `cursor` -> `CURSOR_HOME`

`WithDedicatedProfile(dir)` 将只选择并初始化专用目录，不会自动把 `~/.codex`、`~/.claude`、`~/.cursor` 里的历史 settings、认证、cache 或 session 全量复制到新目录。需要派生配置时使用 `WithCloneProfile(dir, opts)`，复制认证必须显式 opt-in。

优先级固定为：

1. `CommonConfig.Env` 中的 provider-specific env
2. profile option
3. 进程环境中的 provider-specific env
4. adapter 默认 profile

当前设计改进计划见 [`workstream-profile-user-experience.md`](./workstream-profile-user-experience.md)。
