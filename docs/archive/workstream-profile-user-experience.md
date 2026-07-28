# Workstream: Profile User Experience

> 状态：历史设计记录。`AgentProfileDir` 已移除，当前 profile 公共入口是 `WithNativeProfile` / `WithDedicatedProfile` / `WithCloneProfile` / `WithCloneProfileFrom`，当前用法见 [`usage-guide.md`](../usage-guide.md) §6。

本文档把 `AgentProfileDir` 当前暴露出的用户体验问题，整理成一份移除旧字段、改用自然 profile option 的落地计划。

核心目标不是把 profile / resolver 做进 core，也不是让 SDK 自动替宿主迁移所有本地状态，而是让 built-in adapter 的本地 profile 体验从“隐式 env 映射”升级为“可理解、可诊断、可初始化、可选择复用或隔离”的产品语义。

## 1. 背景

当前 `CommonConfig.AgentProfileDir` 是 built-in adapter 的统一 profile 目录入口，也是本 workstream 明确要移除的旧 API：

- `codex` 映射到 `CODEX_HOME`
- `claude` 映射到 `CLAUDE_CONFIG_DIR`
- `cursor` 映射到 `CURSOR_HOME`

这条抽象对 SDK 内部足够简单，但对调用方容易产生误解。用户看到 `AgentProfileDir` 时，通常会期待“这个目录就是 agent 的完整 home/profile”，包括 settings、auth、skills、MCP、session/cache 等所有内容；而当前实现只负责把目录作为 provider-native profile env 注入给 CLI，不自动迁移旧目录，也不保证 CLI 会立即创建完整目录结构。

该问题在示例中尤其明显：如果示例写 `AgentProfileDir: "~/.claudeme"`，用户自然会认为重启后 Claude 的所有配置会出现在 `~/.claudeme`。但当前实现既不展开 `~`，也不克隆 `~/.claude`。

## 2. 用户场景

### 2.1 复用本机原生 profile

用户已经通过本机 CLI 登录，希望 SDK 直接复用：

- Codex 的原生 `CODEX_HOME` / 默认 home
- Claude 的 `~/.claude` 或 `CLAUDE_CONFIG_DIR`
- Cursor 的 `~/.cursor` 或 `CURSOR_HOME`

期望：不用重新登录，SDK 能告诉我当前使用哪个目录、认证是否可用、MCP/skills 会写到哪里。

### 2.2 为桌面应用或服务使用独立 profile

宿主希望每个应用、租户、workspace 或测试环境使用隔离目录。

期望：给 SDK 一个目录后，SDK 能规范化路径、检查可写性、创建基础目录，并清晰提示“还需要登录/导入认证”。

### 2.3 从原生 profile 派生一个专用 profile

用户希望先继承默认配置，然后在专用目录中独立演化。

期望：宿主用自然的 binding option 表达“复用原生 profile”或“用这个专用目录，必要时从原生 profile 派生”。初始化应是 option 的内部语义，而不是额外暴露一套 prepare API。

### 2.4 精确管理 MCP / skills

宿主希望 SDK 写入一组确定的 MCP / skills：

- 共享 profile 下应保守追加/更新，不清理用户已有配置
- 专用 profile 下应允许完整同步和 prune

期望：SDK 能报告当前 profile kind 以及实际写入策略。

### 2.5 调试“配置为什么没生效”

用户通过旧 `AgentProfileDir` 或新 profile option 设置目录，但发现 CLI 仍然读旧配置。

常见原因：

- `CommonConfig.Env` 已显式设置 provider-specific env
- 进程环境已有 provider-specific env
- profile 目录是相对路径或包含未展开的 `~`
- 新目录没有认证文件或 settings
- provider CLI 把部分状态写到 profile 外的 cache/state 目录

期望：`CheckEnvironment` 能直接指出 effective dir、source、override、是否存在、是否可写、认证状态和下一步修复建议。

## 3. 当前问题

### 3.1 命名和实际语义不完全匹配

`AgentProfileDir` 听起来像完整 profile 根目录，但当前更准确的语义是“built-in adapter 的 provider-native profile env fallback”。

### 3.2 缺少路径规范化

当前路径只做 `filepath.Clean`。这会导致：

- `~/.claudeme` 不会展开为用户 home 下的绝对路径
- 相对路径的基准不够显式
- `GetProfile` 和 `CheckEnvironment` 返回的路径不一定能直接解释用户意图

### 3.3 初始化语义没有落在用户自然入口上

新 profile 目录为空时，当前 SDK 只把目录传给 provider CLI。目录创建、基础结构、settings 复制、认证导入等行为都没有明确归属。

正确方向不是新增一套独立 prepare API，而是让 `WithDedicatedProfile(dir)` / `WithCloneProfile(dir, opts)` 这类宿主自然入口承载安全初始化语义，并通过 `CheckEnvironment` 诊断解释实际发生了什么。

### 3.4 覆盖优先级不够可见

目标优先级是：

1. `CommonConfig.Env` 中的 provider-specific env
2. profile option
3. 进程环境中的 provider-specific env
4. adapter 默认 profile

用户调试时很难一眼看出 profile option 是否被忽略、被谁覆盖、最终 env 注入了什么。

### 3.5 provider 差异需要由 option 和诊断承接

`CODEX_HOME`、`CLAUDE_CONFIG_DIR`、`CURSOR_HOME` 的真实覆盖范围不同。统一 option 降低了调用成本，但文档和诊断必须把差异暴露出来，避免“所有配置都会迁过去”的误解。

## 4. 设计原则

### 4.1 保持 core SDK 边界

本 workstream 不引入：

- profile 存储系统
- 自动 agent routing
- HTTP/gRPC 服务能力
- 第二套执行入口

所有执行仍然必须走 `Runner.Run/Start -> resolveInvocation -> adapter.Run(...)`。

### 4.2 不默认复制敏感凭据

profile clone / migration 可能涉及 auth token、API key、OAuth cache、企业配置。SDK 不应在用户只选择专用 profile 目录时自动复制这些内容。

### 4.3 显式优于隐式

目录是否共享、是否专用、是否克隆、是否允许 prune，都应成为可诊断的显式事实，而不是由用户猜测。

### 4.4 先定义意图，再诊断，再初始化

优先级顺序：

1. 宿主用 `WithNativeProfile()` / `WithDedicatedProfile(dir)` / `WithCloneProfile(dir, opts)` 表达意图
2. SDK 报告当前 effective profile、来源、kind、写入策略
3. profile option 在需要时创建安全的基础目录
4. clone 只在显式 `WithCloneProfile` 下按 provider 白名单执行

### 4.5 与 MCP / skills 物化策略一致

profile UX 必须服务于已有 MCP / skills 策略：

- 共享 profile：保守追加/更新
- 专用 profile：完整同步，可清理

## 5. 目标状态

### 5.1 用户能回答四个问题

每个 built-in adapter 都应让用户通过 Admin API 回答：

- 当前 effective profile 目录是什么？
- 这个目录来自哪里？
- 这个目录是否存在、可写、具备必要认证？
- SDK 会以共享 profile 还是专用 profile 的策略写 MCP / skills？

### 5.2 宿主获得自然的 profile option

主推荐入口应是更贴近宿主心智的 binding option：

- `WithNativeProfile()`：复用 provider 原生共享 profile
- `WithDedicatedProfile(dir)`：使用宿主专用 profile，并在需要时安全初始化基础目录
- `WithCloneProfile(dir, opts)`：使用宿主专用 profile，首次初始化时从 native profile 按白名单复制配置
- `WithCloneProfileFrom(src, dst, opts)`：高级场景下从指定源 profile 派生到指定目标目录

这些 option 只影响 binding 的 profile resolution，不引入第二套执行入口。

### 5.3 移除 `AgentProfileDir`

`AgentProfileDir` 不再保留为长期兼容入口。新语义直接由 profile option 表达：

- `WithDedicatedProfile(dir)` 取代 `AgentProfileDir` 的“使用专用目录”场景
- `WithCloneProfile(dir, opts)` 表达“使用专用目录并从 native profile 派生”场景
- `WithNativeProfile()` 表达“复用原生共享 profile”场景
- `CommonConfig.Env` 仍保留 provider-specific env 逃生口
- 新文档和示例禁止继续展示 `AgentProfileDir`

## 6. 执行计划

### Phase 0：文档和示例止血

目标：立刻降低误解成本，不改变公共行为。

任务：

- 在 `docs/usage-guide.md` 增加“本地 profile 目录”章节
- 从 README / usage guide 中移除 `AgentProfileDir` 推荐说明，改为 profile option 说明
- 修改示例中容易误导的 `AgentProfileDir: "~/.xxx"`，改为 `WithDedicatedProfile(...)` 或 `WithCloneProfile(...)`
- 在示例注释中说明新 profile 目录不会自动继承已有登录态
- 在 `docs/workstream-mcp-profile-materialization.md` 增加指向本文档的交叉引用

验收标准：

- 用户读 README / usage guide 后不会再把 `AgentProfileDir` 当作主入口
- 示例不再展示 `AgentProfileDir` 或未展开的 `~` 路径
- MCP profile 文档和 profile UX 文档互相引用

### Phase 1：路径规范化

目标：让新的 profile option 路径行为符合用户直觉。

任务：

- 在 shared helper 中增加 profile path 规范化函数，供 built-in adapter 和 profile option 复用
- 支持 `~` 和 `~/...` 展开到当前用户 home
- 将相对路径转为绝对路径，基准使用当前进程工作目录，而不是 adapter run 的 `CWD`
- 清理路径中的 `.` / `..`
- 在 profile option resolution 或 adapter 侧统一调用该规范化逻辑
- 调整 `GetProfile` 返回规范化后的目录
- 增加 Codex / Claude / Cursor 单元测试

建议 helper：

```go
func NormalizeProfileDir(dir string) (string, error)
```

语义：

- 空字符串返回空字符串
- `~` / `~/x` 展开到 `os.UserHomeDir()`
- 其他包含 `~` 的路径不做特殊展开，例如 `/tmp/~x`
- 相对路径通过 `filepath.Abs` 转绝对路径
- 返回 `filepath.Clean(abs)`

验收标准：

- `WithDedicatedProfile("~/.claudeme")` 与 `WithCloneProfile("~/.claudeme", opts)` 最终都映射为 `/Users/.../.claudeme`
- `Admin().Default().Info/GetProfile/CheckEnvironment` 相关输出不再出现未展开的 `~`
- 显式 `CommonConfig.Env` 中的 provider-specific env 仍保持最高优先级

风险：

- 移除 `AgentProfileDir` 是 breaking change，应在 changelog / migration guide 中说明替代写法。

### Phase 2：profile 诊断增强

目标：把“为什么配置没生效”变成结构化诊断，而不是靠用户猜。

任务：

- 优先扩展现有 `AgentProfile`，避免新增平行 `ProfileReport` 类型
- 在 `AgentProfile` 上补足稳定字段：`Kind`、`Exists`、`Writable`、`AuthStatus`、`ConfigFiles`、`Warnings`
- 在 built-in adapter 的 `CheckEnvironment` 中输出 profile 检查项
- 检查目录是否存在、是否可创建、是否可写
- 检查 provider-specific auth 文件或登录状态
- 检查 profile option 是否被 `CommonConfig.Env` 中的 provider-specific env 覆盖
- 检查进程环境 provider-specific env 是否参与最终决策
- 报告 MCP / skills 写入策略：append/update 或 full sync/prune

建议枚举：

```go
type AgentProfileKind string

const (
	AgentProfileKindNativeShared AgentProfileKind = "native_shared"
	AgentProfileKindDedicated    AgentProfileKind = "dedicated"
	AgentProfileKindManaged      AgentProfileKind = "managed"
)

type AgentProfileAuthStatus string

const (
	AgentProfileAuthUnknown  AgentProfileAuthStatus = "unknown"
	AgentProfileAuthPresent  AgentProfileAuthStatus = "present"
	AgentProfileAuthMissing  AgentProfileAuthStatus = "missing"
	AgentProfileAuthExternal AgentProfileAuthStatus = "external"
)
```

说明：

- env override 是 `Source`，不是 `Kind`；`Kind` 表达共享、专用、托管这类写入策略分类
- provider-specific env 覆盖应通过 `Source` 和 `Warnings` 表达

验收标准：

- 新空目录会报告 `Exists=false` 或 `AuthStatus=missing`，并给出下一步建议
- 被 env 覆盖时，诊断明确指出哪个配置源成为 effective profile
- 共享 profile 和专用 profile 的 MCP/skills 同步策略可见

### Phase 3：自然的 profile binding option

目标：让宿主先用接近业务语言的方式选择 profile 模式，初始化和 clone 都挂在这些 option 的语义上。

任务：

- 设计 provider-agnostic 的 profile option，挂在 built-in `New(cfg, opts...)` 的 `AgentOption` 体系中
- 删除 `CommonConfig.AgentProfileDir`，由 profile option 承担所有 profile 选择语义
- `WithNativeProfile()` 表达“复用 provider 原生共享 profile”
- `WithDedicatedProfile(dir)` 表达“使用这个专用 profile 目录，必要时安全创建基础目录”
- `WithCloneProfile(dir, opts)` 表达“使用这个专用 profile 目录，首次初始化时从 native profile 按白名单复制配置”
- `WithCloneProfileFrom(src, dst, opts)` 表达“从指定源 profile 派生到指定目标目录”
- 保留 provider-specific env 的显式覆盖能力，`CommonConfig.Env` 仍是最低层逃生口
- clone 只允许 provider 白名单内文件，禁止递归复制整个目录

建议 API 草案：

```go
sdk := agentadaptor.New(
	agentadaptor.WithDefaultAgent(claude.New(
		agentadaptor.ClaudeConfig{Model: "claude-sonnet-4"},
		agentadaptor.WithCloneProfile("~/Library/Application Support/my-app/claude", agentadaptor.CloneProfileOptions{
			IncludeSettings: true,
			IncludeMCP:      true,
			IncludeSkills:   true,
			IncludeAuth:     false,
		}),
	)),
)
```

```go
// 复用用户已经登录的原生共享 profile。
claude.New(cfg, agentadaptor.WithNativeProfile())

// 使用宿主专用目录，不从原生 profile 复制任何内容。
claude.New(cfg, agentadaptor.WithDedicatedProfile("~/Library/Application Support/my-app/claude"))

// 使用宿主专用目录；首次初始化时从原生 profile 复制白名单配置。
claude.New(cfg, agentadaptor.WithCloneProfile("~/Library/Application Support/my-app/claude", agentadaptor.CloneProfileOptions{
	IncludeSettings: true,
	IncludeMCP:      true,
}))
```

建议类型草案：

```go
type CloneProfileOptions struct {
	IncludeSettings bool
	IncludeMCP      bool
	IncludeSkills   bool
	IncludeAuth     bool
	AuthMode        CloneProfileAuthMode
}
```

设计意图：

- 宿主只传目标目录，源目录默认为 provider 的 native shared profile
- `WithDedicatedProfile` 的初始化只创建安全基础目录，不复制旧 profile 内容
- 绝大多数用户不需要关心 `FromDir` / `ToDir`
- `WithCloneProfileFrom(src, dst, opts)` 作为高级入口提供，但普通宿主优先使用 native -> dedicated 的 `WithCloneProfile(dir, opts)`
- dry-run 更适合通过 `CheckEnvironment` / 诊断模式表达，避免运行配置里混入“只预览不执行”的控制语义
- `IncludeAuth` 默认 `false`，复制认证必须显式 opt-in；OAuth CLI 推荐用 `AuthMode: CloneProfileAuthLink` 共享本机登录态，避免复制 refresh token 文件

验收标准：

- 新用户能通过 option 直接表达“复用原生 profile”还是“使用专用 profile”
- 宿主不需要调用额外的 `PrepareProfile` 方法
- 专用 profile 首次使用时能自动创建安全的基础目录
- `WithCloneProfile(dir, opts)` 不会默认复制 token / OAuth cache
- clone 诊断明确列出复制了哪些文件、跳过了哪些文件、为什么跳过
- 旧 `AgentProfileDir` 调用点完成迁移
- profile option 不引入第二套执行入口，只影响 adapter config resolution

### Phase 4：高级 profile 能力收口

目标：只提供已经拍板的高级入口，不引入复杂 `ProfileSpec` 或托管 profile 子系统。

任务：

- 提供 `WithCloneProfileFrom(src, dst, opts)`，用于从非 native 源目录派生
- 暂不把 `ProfileSpec{Mode, Dir, Clone}` 作为公共 API
- 暂不提供 `WithManagedProfile(...)`；`codex` 的 managed fallback 仍是 adapter 内部默认行为
- 所有高级 option 仍然只影响 binding 的 profile resolution，不改变 `Run/Start` 执行语义

验收标准：

- 主路径 API 保持短、自然、可读
- 高级 clone 能力可用但不污染 80% 用户场景
- 文档不要求普通宿主理解底层 provider env 或迁移源/目标模型

## 7. Adapter 细分计划

### 7.1 Codex

当前基础：

- profile option 映射到 `CODEX_HOME`
- 已有 managed home / shared home 相关语义
- MCP profile materialization 已围绕 effective profile 展开

落地重点：

- 统一 `CODEX_HOME` 的路径规范化
- 报告 config.toml、auth、managed/shared 分类
- 将 MCP 写入策略和 profile kind 一起暴露

### 7.2 Claude

当前基础：

- profile option 映射到 `CLAUDE_CONFIG_DIR`
- auth/config probe 已检查 `.credentials.json`、`credentials.json`、`settings.json`、`config.json` 等候选
- skills prompt bundle 与原生 skills home 是两套语义

落地重点：

- 解决 `~/.claudeme` 不展开问题
- 明确 `CLAUDE_CONFIG_DIR` 不等同于复制 `~/.claude` 全部状态
- 报告 settings/auth 缺失时的下一步建议
- 文档说明 prompt bundle cache 不等于 Claude 原生 profile

### 7.3 Cursor

当前基础：

- profile option 映射到 `CURSOR_HOME`
- MCP 主要围绕 `mcp.json` 或 Cursor 原生 profile 文件

落地重点：

- 明确 Cursor profile 中哪些文件由 SDK 管理，哪些由 Cursor CLI 管理
- 报告 mcp.json 写入策略
- 检查 CLI 对 `CURSOR_HOME` 的实际支持边界

## 8. 公共文档更新清单

必须更新：

- `README.md`
- `docs/usage-guide.md`
- `docs/workstream-mcp-profile-materialization.md`
- built-in adapter 示例

建议新增或补充：

- provider profile matrix：Codex / Claude / Cursor 各自 profile env、默认目录、认证文件、settings 文件、MCP 文件、skills 目录
- migration guide：从 `AgentProfileDir` 迁移到 profile option
- migration guide：如何从共享 profile 切到专用 profile

## 9. 测试计划

### 9.1 路径规范化测试

覆盖：

- 空路径
- 绝对路径
- 相对路径
- `~`
- `~/x`
- 路径中间包含 `~` 的情况
- Windows 风格路径，如果当前平台可合理测试

### 9.2 优先级测试

覆盖：

- `CommonConfig.Env` provider-specific env 优先于 profile option
- profile option 优先于进程 env
- 进程 env 优先于 adapter default
- 未设置任何项时回落默认 profile

### 9.3 诊断测试

覆盖：

- 目录不存在
- 目录存在但不可写
- 目录存在且可写
- auth present / missing
- shared profile / dedicated profile 分类
- MCP/skills 写入策略输出

### 9.4 Profile option 测试

覆盖：

- `WithNativeProfile()` 解析到 provider 原生共享 profile
- `WithDedicatedProfile(dir)` 解析到规范化后的专用目录
- `WithDedicatedProfile(dir)` 首次使用只创建允许的基础目录
- `WithCloneProfile(dir, opts)` 的源目录默认为 native shared profile
- 诊断 dry-run 不写文件
- clone 默认不复制认证
- clone include auth 必须显式开启
- provider-specific 白名单生效

## 10. 迁移与兼容策略

### 10.1 移除 `AgentProfileDir`

删除 `CommonConfig.AgentProfileDir`。这是有意的 breaking change，用更明确的 profile option 取代旧字段。

### 10.2 明确 provider-specific env 最高优先级

`CommonConfig.Env` 中的 provider-specific env 继续是最高优先级，因为它表达了调用方最明确、最底层的覆盖意图。

### 10.3 `~` 展开视为 bug fix

虽然这会改变曾经错误使用字面 `~` 目录的行为，但符合绝大多数用户预期，应作为 bug fix 处理。

### 10.4 不自动迁移

任何自动复制旧 profile 的行为都必须通过 `WithCloneProfile(dir, opts)` 显式 opt-in，不能挂在普通专用目录选择上。

## 11. 决策点

需要实现前拍板：

- profile option 相对路径基准是否固定为进程工作目录
- profile option 是全部放在 root `agentadaptor` 包，还是由 built-in adapter 各自暴露 provider-specific wrapper

建议决策：

- 相对路径基准固定为进程工作目录
- `CommonConfig.Env` > profile option > process env > default 的优先级作为新规则
- 扩展现有 `AgentProfile` 和 `CheckEnvironment`，避免新增平行 `ProfileReport`
- dry-run 只通过诊断表达，不放入 `CloneProfileOptions`
- profile option 在 Phase 3 一次性引入，初始化和 clone 是 option 语义，不拆独立 prepare API
- 不把 `ProfileSpec` 作为主路径；公共入口固定为 `WithNativeProfile()` / `WithDedicatedProfile(dir)` / `WithCloneProfile(dir, opts)` / `WithCloneProfileFrom(src, dst, opts)`

## 12. 非目标

本 workstream 不做：

- 内置 profile database
- 多租户 profile 存储
- 自动选择 agent
- 自动迁移所有 provider cache/state
- 自动复制认证
- 修改 `Run/Start` 执行语义
- 为 profile 新增第二套执行入口

## 13. 推荐落地顺序

1. 文档和示例止血
2. 路径规范化
3. profile 诊断增强
4. 自然的 profile binding option
5. 高级 profile 能力后置

其中 1-3 应作为近期必做，4-5 可以按用户反馈和宿主集成需求逐步推进。
