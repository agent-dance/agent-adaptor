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
- `WithRunPolicy`
- `WithInstructions`
- `WithMetadata`
- `WithAgentIdentity`

合并顺序固定（与 `resolveInvocation` 一致）：

- 先取 `AgentBinding` 绑定默认值（含 `RunPolicy` 指针；空指针表示全字段继承）
- 再按字段合并 per-call `RunOption`（`WithRunPolicy` 对非空字段覆盖绑定同字段；未覆盖字段继承绑定）
- adapter 的 `config` 仅表达 CLI/环境级配置，**不再**承载与 `RunPolicy` 重复的权限类 toggle；策略统一由 `RunPolicy` 表达

`RunPolicy` 合同与适配器映射见 [`run-policy.md`](./run-policy.md)。
