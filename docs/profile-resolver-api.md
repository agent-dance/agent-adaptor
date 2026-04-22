# Profile / Resolver Layering Draft

本文档描述一个可选的上层 `profiles` 层，建立在当前已经拍板的 core `agent-adaptor` 语义之上。

重点先说死：

- core `agent-adaptor` 已经改为“默认 Agent 绑定优先”
- core 主路径是 `WithDefaultAgent(...) + sdk.Run(...)`
- core 不做自动 agent 选择
- profile / resolver 是可选上层，不是 core 的第二套执行入口

## 1. core 已经提供什么

当前 core 负责：

- `AgentBinding`
- `DriverAdapter`
- `SDK`
- `Runner`
- session / workspace / skills / permissions / instructions 的统一执行语义

当前 core 不负责：

- profile 存储
- profile 版本管理
- profile 选择策略
- 业务角色到具体 Agent 的映射规则

## 2. 为什么还需要 profile / resolver

虽然 core 现在已经支持：

```go
sdk := agentadaptor.New(
	agentadaptor.WithDefaultAgent(codex.New(agentadaptor.CodexConfig{
		Model: "gpt-5.4",
	})),
)

result, err := sdk.Run(ctx, "fix the failing tests")
```

但业务系统经常面对的不是“codex”这个底层名字，而是：

- `default-coding`
- `review`
- `ops`
- `safe-migration`

这些是业务角色，不是底层 adapter 名称。

因此需要一个上层把“业务 profile”解析成 core 已经支持的 `AgentBinding`。

## 3. 正确分层

### 3.1 core 层

负责：

- 怎么统一调用不同 adapter
- 怎么处理 session / workspace / skills / permissions
- 怎么把绑定默认值和 per-call override 合并

### 3.2 profile / resolver 层

负责：

- profile 存储
- profile 查找
- profile 到 `AgentBinding` 的映射
- profile 默认值注入

### 3.3 不允许的事情

profile / resolver 层不应该：

- 发明一套新的 `Execute(Request)` 执行接口
- 绕开 core 的 `Runner`
- 自己重复实现 session / workspace / skills 逻辑

## 4. 推荐类型草案

### 4.1 profile 定义

```go
package profiles

type Profile struct {
	ID          string
	TenantID    string
	Name        string
	Description string

	Adapter string
	Config  any

	Identity     agentadaptor.AgentIdentity
	Workspace    agentadaptor.WorkspaceSpec
	RunPolicy    *agentadaptor.RunPolicy
	Skills       []string
	Instructions *agentadaptor.InstructionsBundleRef
	Metadata     map[string]string
}
```

约束：

- `Adapter` 是底层 adapter 类型，例如 `codex` / `claude` / `cursor`
- `Config` 必须是对应 adapter 的 typed config
- 其余字段都是 profile 默认值

### 4.2 store

```go
package profiles

type Store interface {
	Get(ctx context.Context, tenantID, name string) (*Profile, error)
	List(ctx context.Context, tenantID string) ([]Profile, error)
}
```

### 4.3 binder

```go
package profiles

type Binder interface {
	Bind(ctx context.Context, profile *Profile) (agentadaptor.AgentBinding, error)
}
```

`Binder` 的职责是把 profile 转成 core `AgentBinding`。

### 4.4 service

```go
package profiles

type Service struct {
	store  Store
	binder Binder
}

func (s *Service) Resolve(ctx context.Context, tenantID, name string) (agentadaptor.AgentBinding, error)
```

## 5. 绑定实现方式

推荐让 `Binder` 最终调用 core 的 `BindTyped(...)` 或 built-in `New(...)`：

```go
func (b *DefaultBinder) Bind(ctx context.Context, profile *Profile) (agentadaptor.AgentBinding, error) {
	switch profile.Adapter {
	case "codex":
		cfg := profile.Config.(agentadaptor.CodexConfig)
		return codex.New(
			cfg,
			agentadaptor.WithDefaultIdentity(profile.Identity),
			agentadaptor.WithDefaultWorkspace(profile.Workspace),
			agentadaptor.WithDefaultSkills(profile.Skills...),
		), nil
	case "claude":
		cfg := profile.Config.(agentadaptor.ClaudeConfig)
		return claude.New(cfg), nil
	default:
		return nil, fmt.Errorf("unsupported adapter %q", profile.Adapter)
	}
}
```

这层的重点是：

- profile 最终还是变成一个标准 `AgentBinding`
- 一旦变成 `AgentBinding`，后续执行就完全回到 core

## 6. 两种推荐使用方式

### 6.1 宿主启动时解析 profile，构造 SDK

适合“一个服务实例只服务一个默认角色”的场景。

```go
binding, err := resolver.Resolve(ctx, "company-1", "default-coding")
if err != nil {
	return err
}

sdk := agentadaptor.New(
	agentadaptor.WithDefaultAgent(binding),
	agentadaptor.WithSessionStore(store),
)

result, err := sdk.Run(ctx, "fix the failing tests")
```

### 6.2 宿主维护多 profile，对应多个命名 Agent

适合同一个宿主里有 `default`、`review`、`ops` 多角色。

```go
defaultBinding, _ := resolver.Resolve(ctx, "company-1", "default-coding")
reviewBinding, _ := resolver.Resolve(ctx, "company-1", "review")

sdk := agentadaptor.New(
	agentadaptor.WithDefaultAgent(defaultBinding),
	agentadaptor.WithAgent("review", reviewBinding),
)

review, _ := sdk.Agent("review")
result, err := review.Run(ctx, "review the patch")
```

## 7. 为什么 profile 层不应该替 core 决定 agent

如果把“自动决定这次用哪个 agent”塞进 core，会引入：

- routing
- fallback
- cost policy
- capability matching
- prompt-aware selection

这些都不是 adaptor 的职责。

正确做法是：

- core 只负责统一执行合同
- profile / resolver 只负责角色到绑定的映射
- 真正的自动路由若以后需要，再放到更高层的 broker / router

## 8. 一句话结论

当前已经拍板的 core 模型是：

- 初始化时绑定默认 Agent
- 调用时直接 `sdk.Run(...)`

profile / resolver 层的职责，只是把“业务角色”转换成这个 core 已经接受的 `AgentBinding`，而不是重新发明另一套执行模型。
