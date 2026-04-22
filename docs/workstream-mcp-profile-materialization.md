# Workstream: MCP via Effective Profile Materialization

## 1. 目标

在不破坏当前 core SDK 边界的前提下，为 `codex`、`claude`、`cursor` 提供一套统一的 MCP 配置抽象，并把它像 `skills` 一样物化到 adapter 当前真实生效的 profile 中。

一句话：

- 宿主声明 MCP server
- SDK 统一合并 binding defaults 与 per-run override
- adapter 在运行前把 MCP 写进当前 effective profile
- CLI 按各自原生方式加载 MCP

本方案不再为 MCP 额外引入一套 `source_profile/derived_profile` 或 `ephemeral/persistent` 的 profile 规则。

## 2. 核心规则

本 workstream 最终收敛到一条简单规则：

- SDK 总是写当前 effective profile
- 如果 effective profile 是原生共享 profile，则只追加/更新，不自动清理旧 MCP
- 如果 effective profile 是宿主专用 profile，则做完整同步，允许清理旧 MCP

这里的“宿主专用 profile”包括宿主显式给出的隔离目录，以及 adapter 自己已经存在的 managed profile。

结果也很直接：

- 宿主能接受原始用户目录积累旧 MCP，就继续用共享 profile
- 宿主不能接受，就自己给 SDK 一个隔离 profile

## 3. 价值

- 宿主不必分别适配 `codex mcp add`、`claude mcp add`、`cursor-agent mcp` / `mcp.json`
- `AgentProfileDir` 继续作为统一入口，宿主不必自己处理 `CODEX_HOME` / `CLAUDE_CONFIG_DIR` / `CURSOR_HOME`
- 不需要把 provider-specific CLI 命令流程泄漏到 core 公共 API
- `skills/MCP` 的变化不自动决定 session 是否切换，session 生命周期继续由宿主控制
- 能与现有 `skills` / `runtime` 一样，走同一条 `resolveInvocation -> adapter.Run(...)` 主路径

## 4. 用户场景

- 宿主想给默认 agent 注入一组公司标准 MCP server
- 宿主想让 `codex` / `claude` / `cursor` 三个 binding 使用同一份高层 MCP 声明
- 宿主想把 MCP 配置纳入 profile / role 定义，而不是在业务代码里硬编码 provider-specific glue
- 某次运行需要临时 MCP，但宿主不希望污染用户真实 profile，于是为该次运行提供隔离 profile
- 宿主愿意复用用户原始 profile，也接受旧 MCP 会累积保留

## 5. 当前基础

当前 core 已经有两块关键基础：

- `CommonConfig.AgentProfileDir` 是 built-in adapter 的统一 profile 目录入口
- adapter 已经能够基于 profile/home 做 skills / auth / config 物化

当前已存在的相关能力：

- `AgentProfileDir` 会映射到：
  - `codex` -> `CODEX_HOME`
  - `claude` -> `CLAUDE_CONFIG_DIR`
  - `cursor` -> `CURSOR_HOME`
- `codex` 已有 managed home / shared home 的双层语义
- `skills` 已经证明“宿主声明高层资源，adapter 在 profile/home 侧物化”这条路可行
- `Run` 主路径已经有：
  - defaults + per-run override 合并
  - workspace 准备
  - runtime 准备
  - skills 准备
  - session 协调

当前缺失：

- 没有统一的 MCP 公共类型
- `DriverDescriptor` 没有 MCP capability
- `DriverRunRequest` 没有 MCP payload
- built-in adapter 缺少统一的 MCP 写入/合并逻辑
- 三家 adapter 还没有统一的 shared-profile / dedicated-profile 同步策略

## 6. 设计原则

### 6.1 不引入第二套执行入口

MCP 只能作为现有 `Runner.Run/Start` 执行语义的一部分，不允许出现：

- `RunWithMCP(...)`
- `sdk.MCP(...)`
- 独立于 `resolveInvocation` 的第二套配置合并逻辑

### 6.2 统一抽象的是 server spec，不是 CLI 命令流程

三家 CLI 都支持 MCP，但接入方式并不相同：

- `codex` / `claude` 既有 CLI 子命令，也有 profile/config 文件
- `cursor-agent` 主要是读 `mcp.json`，CLI 更偏向 login / enable / disable

因此 core 统一抽象的是：

- server 是什么
- transport 是什么
- 启动命令/URL/headers/env 是什么

而不是：

- 具体要不要执行 `mcp add`
- OAuth 具体怎么登录
- approval list 到底落哪一个 provider-specific 文件

### 6.3 宿主决定 profile 隔离策略

SDK 不替宿主决定“该不该用隔离目录”。

SDK 只做两件事：

- 解析当前 binding 的 effective profile
- 把 MCP 物化到这个 profile

因此：

- 宿主若把 effective profile 指到 `~/.claude` / `~/.cursor` / `~/.codex` 这样的共享目录，后果由宿主接受
- 宿主若需要精确收敛、可清理、无残留，就应提供隔离 profile

### 6.4 共享 profile 保守，专用 profile 完整同步

v1 采用一条明确的行为规则：

- 原生共享 profile：只追加/更新，不自动 prune 旧 MCP
- 宿主专用 profile：完整同步，允许清理旧 MCP

这个取舍的直接含义是：

- 共享 profile 下，实际可见的工具集合可能是 `desired` 的超集
- 宿主如果不能接受这个副作用，就应改用隔离 profile

### 6.5 profile/config 文件优先于 CLI side-effect

SDK 应优先通过 profile/config 文件物化 MCP，而不是在运行前 shell out 调各家的 `mcp add`：

- 更稳定
- 更可测
- 更容易保持 `Run/Start` 语义单一
- 更不容易把 CLI 交互/OAuth/approval side-effect 泄漏进 core

## 7. 公共 API 草案

### 7.1 新类型

```go
type MCPTransport string

const (
	MCPTransportStdio MCPTransport = "stdio"
	MCPTransportHTTP  MCPTransport = "http"
	MCPTransportSSE   MCPTransport = "sse"
)

type MCPServerSpec struct {
	Key               string
	Transport         MCPTransport
	Command           string
	Args              []string
	Env               map[string]string
	URL               string
	Headers           map[string]string
	BearerTokenEnvVar string
	Required          bool
	RequiredReason    string
}

type MCPConfig struct {
	Servers []MCPServerSpec
}

type MCPPayload struct {
	Servers     []MCPServerSpec
	Fingerprint string
	Warnings    []string
}

type MCPCapability struct {
	Supported bool
	Stdio     bool
	HTTP      bool
	SSE       bool
}
```

### 7.2 `MCPServerSpec` 字段设计依据

这组字段分两类：

- server declaration：描述“一个 MCP server 是什么、怎么连”
- host annotation：描述“宿主怎么理解它”

设计目标不是把三家 provider 的配置字段做并集，也不是只保留最小交集，而是抽出一组能够稳定映射到 `codex` / `claude` / `cursor` 的公共声明。

#### `Key`

- 作用：server 的稳定主键
- 依据：
  - Codex 的 MCP 配置以命名项组织
  - Claude / Cursor 的 MCP 配置也都是按 server 名字索引
- 结论：
  - `Key` 用来做 merge、覆盖、冲突检测
  - 它不是 display label，而是 identity

#### `Transport`

- 作用：显式声明 transport，而不是靠字段形状隐式猜测
- 依据：
  - MCP 协议本身区分 `stdio` 与 remote transport
  - Claude / Cursor 明确把 `stdio/http/sse` 作为 transport 面暴露
  - Codex 当前公开 surface 至少明确支持 `stdio` 与 URL 型 remote MCP
- 结论：
  - core 必须把 transport 单独建模
  - adapter 的 capability matrix 决定自己实际支持哪些 transport

#### `Command` / `Args` / `Env`

- 作用：描述 stdio MCP server 的启动方式
- 依据：
  - stdio MCP 的本质就是“启动一个本地子进程”
  - 三家产品面最终都需要 `command + args`
  - `Env` 是 stdio server 最常见的认证与运行参数入口
- 结论：
  - 不能把它压成一个 shell string
  - 分开建模可以避免 quoting、跨平台和 merge 问题

#### `URL`

- 作用：描述 remote MCP server 的入口地址
- 依据：
  - Codex 对 remote MCP 的公开入口是 `--url`
  - Claude / Cursor 的 remote MCP 也都以 URL 为核心
- 结论：
  - remote server declaration 必须有单独的 `URL`

#### `Headers`

- 作用：表达远程 HTTP/SSE server 需要的自定义请求头
- 依据：
  - Claude 明确支持 header
  - Cursor 的 MCP 配置也支持 header 类远程配置
  - 真实世界 remote MCP 不一定是 Bearer auth，也经常用 `X-API-Key` 或内网路由头
- 结论：
  - 不能只靠 `BearerTokenEnvVar`
  - `Headers` 是通用 remote auth/config 的最小补充

#### `BearerTokenEnvVar`

- 作用：用“环境变量名引用 secret”，而不是把 token 值写进 profile
- 依据：
  - Codex 当前公开 surface 明确有 `--bearer-token-env-var`
  - Bearer token 是 remote MCP 最常见的认证方式
- 结论：
  - 这是兼顾 Codex 配置面与 secret hygiene 的专用字段
  - 它和 `Headers` 不重复：`Headers` 负责通用 header，`BearerTokenEnvVar` 负责最常见的 secret-by-reference 场景

#### `Required` / `RequiredReason`

- 作用：表达宿主是否把某个 MCP 当成硬依赖
- 依据：
  - 这两个字段不是 provider config，而是宿主执行语义
  - 当前仓库的 `skills` 已经有同样的心智与字段组合
- 结论：
  - 它们让宿主能区分“可选增强 MCP”和“没有就不完整的 MCP”
  - 失败时也能给出明确原因，而不是只有一个黑箱式 `required=true`

### 7.3 接入点

新增：

- `AgentDefaults.MCP *MCPConfig`
- `runOptions.mcp *MCPConfig`
- `DriverRunRequest.MCP MCPPayload`
- `DriverDescriptor.MCP MCPCapability`

新增 option：

- `WithDefaultMCP(cfg MCPConfig)`
- `WithMCP(cfg MCPConfig)`

### 7.4 v1 不进入控制面

v1 暂不新增：

- `Admin().ListMCPServers()`
- `Admin().SyncMCPServers()`
- `GetEffectiveMCPConfig()`

理由：

- 先把执行面打通
- 先验证 profile-side 写入模型稳定
- 避免在执行合同未稳定前抢先拍板第二层控制面 API

## 8. 执行语义

### 8.1 合并规则

MCP 的合并规则和现有 defaults/override 模式保持一致：

- binding-level `WithDefaultMCP(...)`
- per-run `WithMCP(...)`

字段规则：

- per-run 若显式给出 `Servers`，则覆盖 binding default 的 server 列表
- 未显式覆盖时，继承 binding default

### 8.2 session 复用由宿主决定

这次设计在 session 上明确采用一条更简单的规则：

- `skills` 变了，不自动影响 session 复用
- `MCP` 变了，也不自动影响 session 复用
- 是否新开 session，由宿主自己决定

也就是说，SDK 不因为 `WithSkills(...)` / `WithMCP(...)` 的变化，自动把旧 session 判成 incompatible。

宿主如果想保留原 session，就继续使用：

- `continue_or_start`
- `continue_only`

宿主如果想明确切出新线程，就显式使用：

- `start_new`
- `fork`

这条规则的含义是：

- `skills/MCP` 是本次 run 的执行配置
- session 是否延续，是宿主的业务决策
- 这两件事不要在 SDK 里被强行绑定

因此，本 workstream 不要求：

- 把 `skills/MCP` 的 payload fingerprint 纳入 session compatibility 判断
- 因为 `skills/MCP` 变化自动拒绝复用旧 session

是否因为工具集合变化而主动新开 session，交给宿主在调用层决定。

### 8.3 失败语义

MCP 物化失败属于启动前失败，直接阻断 run：

- effective profile 解析失败
- 配置文件写入或合并失败
- adapter 不支持某个 transport

这些错误应在 `adapter.Run` 之前暴露，不允许让子进程带着半配置状态启动。

## 9. Effective Profile 语义

### 9.1 沿用现有 profile 解析规则

MCP v1 不引入额外的 MCP-specific profile 语义。

仍沿用 built-in adapter 当前优先级：

1. `CommonConfig.Env` 中显式声明的 adapter-specific env
2. `CommonConfig.AgentProfileDir`
3. 进程环境与 adapter 默认路径
4. adapter managed fallback

### 9.2 两类 effective profile

实现上把 effective profile 分成两类：

- 原生共享 profile
- 宿主专用 profile

`原生共享 profile` 的判定标准，不看“宿主是不是显式传了路径”，而看：

- 当前 effective profile 的真实目录，是否等于该 adapter 的 canonical user-default profile 路径

换句话说，判断标准是“这次真正写到哪”，不是“这个路径最初从哪里来”。

三家的 canonical shared profile 固定为：

- `~/.claude`
- `~/.cursor`
- `~/.codex`

因此，建议实现规则是：

1. 如果 `AgentProfile.Managed=true`，直接判为 `宿主专用 profile`
2. 否则计算该 adapter 的 canonical shared profile 路径
3. 若 `effective profile` 与该 canonical 路径相同，则判为 `原生共享 profile`
4. 否则一律判为 `宿主专用 profile`

这里的“路径相同”建议按规范化后的真实路径比较：

- `filepath.Abs`
- `filepath.Clean`
- 路径存在时尽量 `EvalSymlinks`
- Windows 平台按不区分大小写比较

不要采用下面这些不稳定规则：

- 不要只看 `AgentProfile.Source`
- 不要把 `process env` 自动等价成 shared profile
- 不要通过“路径里是否包含 `.claude/.cursor/.codex`”做字符串猜测

`宿主专用 profile` 指宿主明确交给 SDK 使用的目录，或 adapter 自己已经合成出来的隔离目录，例如：

- `AgentProfileDir`
- binding env 显式指定的自定义 profile
- `codex` 当前已有的 managed `CODEX_HOME`

### 9.3 Codex 的特殊点

对 `codex` 来说，built-in adapter 在没有显式 profile 输入时，当前默认走的是 managed `CODEX_HOME`，不是直接写 `~/.codex`。

因此：

- “共享 profile 只追加不清理”这条语义，只有在 effective profile 最终真的等于共享 `~/.codex` 时才成立
- 默认 built-in `codex` 绑定更接近“宿主专用 profile，可完整同步”

## 10. Profile Materialization 策略

### 10.1 通用步骤

每个 adapter 在运行前执行：

1. 解析 current effective profile
2. 判断它是原生共享 profile 还是宿主专用 profile
3. 把 MCP server 配置按 provider-native 方式写入该 profile
4. 根据 profile 类型决定是否 prune 旧 MCP
5. 启动 CLI，并把 `CODEX_HOME` / `CLAUDE_CONFIG_DIR` / `CURSOR_HOME` 指向该 effective profile

### 10.2 原生共享 profile 规则

原生共享 profile 下：

- 允许追加新的 MCP server
- 允许更新同名 MCP server 的 provider-managed 配置
- 不自动删除“这次 desired 集合里没有”的旧 MCP server

后果必须文档化：

- 共享 profile 的实际工具集合可能比当前 `desired` 更大
- 同一份 invocation 配置在不同历史 profile 状态下，实际暴露的工具可能不同
- 宿主若要求精确工具集，不能依赖共享 profile

### 10.3 宿主专用 profile 规则

宿主专用 profile 下：

- 可以把 MCP 配置视为当前 binding/run 的完整期望集合
- 可以对 provider-managed MCP section 做完整同步
- 可以删除本次 `desired` 中不存在的旧 MCP server

这条规则成立的前提不是 SDK 神奇地知道 ownership，而是宿主已经通过隔离 profile 给了 SDK 一个可安全收敛的空间。

### 10.4 v1 不做 sidecar ownership

v1 不要求为 MCP 引入 `managed/external` 或 sidecar manifest。

原因不是这件事不重要，而是当前方案已经把选择权交给宿主：

- 共享 profile：保守，不清理
- 专用 profile：完整同步

只有当未来必须在“同一个共享 profile 里既保留用户自配 MCP，又精确清理 SDK 自己写的 MCP”时，才需要重新引入 ownership 记录机制。

## 11. 各 adapter 的落地方案

### 11.1 Codex

现状：

- 已有 `shared home` / `managed home`
- 已有 auth/config copy/symlink 逻辑
- 最适合作为第一阶段实现对象

实施：

- 复用当前 `CODEX_HOME` 解析与 managed home 准备逻辑
- 在 effective `CODEX_HOME/config.toml` 中写 MCP 配置
- 保留：
  - `auth.json` symlink/copy
  - 非 MCP 配置对现有 `config.toml` / `config.json` 的兼容读取
- `config.toml` 是 MCP 的唯一 source of truth
- 不把 MCP 写入 `config.json`
- 若 effective profile 是 managed/custom home，允许完整同步
- 若宿主显式把 effective profile 指到共享 `~/.codex`，改为追加/更新，不做 prune

能力声明：

- `Stdio=true`
- `HTTP=true`
- `SSE=false`

### 11.2 Claude

现状：

- 已能解析 `CLAUDE_CONFIG_DIR`
- 已有围绕 profile 的 skills/auth 物化经验

实施：

- 直接在 effective `CLAUDE_CONFIG_DIR` 写 MCP 配置
- 若 effective profile 是共享 `~/.claude`，只追加/更新，不做 prune
- 若 effective profile 是宿主给出的隔离目录，允许完整同步

v1 不做：

- `claude mcp login`
- OAuth secret prompt 的统一封装

能力声明：

- `Stdio=true`
- `HTTP=true`
- `SSE=true`

### 11.3 Cursor

现状：

- CLI 本身更偏向读取 `mcp.json`
- `cursor-agent mcp` 主要提供 login/list/enable/disable，不提供通用 add

实施：

- 在 effective `CURSOR_HOME` 写入 `mcp.json`
- 若 effective profile 是共享 `~/.cursor`，只追加/更新，不做 prune
- 若 effective profile 是宿主给出的隔离目录，允许完整同步
- SDK 只管理 server declaration，不管理 approval/login state

v1 明确不做：

- `cursor-agent mcp enable`
- `cursor-agent mcp disable`
- `cursor-agent mcp login`
- 任何“approved list”或 operator-local state 的声明式文件化管理

运行语义：

- 如果 server declaration 正确，但当前 operator 侧仍需要额外 approval/login，SDK 不替宿主自动完成
- 这类问题视为 operator/preflight 问题，而不是 profile materialization 的一部分
- 文档与错误提示必须诚实暴露这一限制，不伪装成“已经 fully managed”

能力声明：

- `Stdio=true`
- `HTTP=true`
- `SSE=true`

## 12. 内部包与职责划分

建议新增：

- `internal/mcpruntime`

职责：

- `MCPConfig -> MCPPayload` 规范化
- effective profile 分类
- vendor-specific 配置文件写入与合并

建议文件：

- `internal/mcpruntime/types.go`
- `internal/mcpruntime/payload.go`
- `internal/mcpruntime/profile_kind.go`
- `internal/mcpruntime/codex.go`
- `internal/mcpruntime/claude.go`
- `internal/mcpruntime/cursor.go`

边界：

- 不进入 `internal/clihelper`
- 不让 shared helper 处理 provider-specific MCP 协议
- 不让 bridge 层参与 MCP 配置物化

## 13. `ConfigSchema()` 与配置展示

v1 不建议把整组 `MCPServerSpec` 暴露成 built-in `ConfigSchema()` 的静态字段：

- 结构太复杂，不适合现有 `ConfigField` 扁平模型
- 三家 provider-specific 选项差异还没完全收敛

v1 建议在文档中说明：

- 高层 MCP 配置走 `WithDefaultMCP` / `WithMCP`
- 不是 built-in adapter typed config 的简单 text/select 字段

未来如果要扩 MCP 控制面，也应沿用现有 `AdminAPI -> AgentAdmin` 心智：

- 不做 `Admin().MCP()`
- 若需要控制面能力，增量挂到 `AgentAdmin`
  - 例如 `ListMCPServers()` / `SyncMCPServers()`

## 14. 依赖选型

### 14.1 需要新增的候选依赖

对于 Codex 的 TOML 配置读写，建议引入局部化 TOML 库，而不是手写字符串拼接。

候选：

- `github.com/pelletier/go-toml/v2`

### 14.2 评估

#### 可靠性

- Codex profile 物化涉及 TOML 合并/写入
- 手写字符串拼接容易破坏已有配置、转义、数组、嵌套表结构
- 使用成熟 TOML 库显著降低配置破坏风险

结论：外部库明显占优。

#### 可持续维护

- TOML 是稳定格式
- 主流 TOML 库比仓内临时手写 parser 更容易维护和升级

结论：外部库占优。

#### 可局部化

- 只需要在 `internal/mcpruntime/codex.go` 使用
- 不污染 core 公共 API

结论：完全可局部化。

### 14.3 决策

若进入实现阶段，建议引入 TOML 库，不要以“零依赖”为唯一理由拒绝。

Claude / Cursor 的 JSON 配置写入仍可使用标准库。

## 15. 测试计划

### 15.1 core 合并测试

- `WithDefaultMCP` 与 `WithMCP` 的合并
- MCP payload 变化不会自动触发 session incompatible
- session 是否切换继续由宿主传入的 `SessionMode` 决定

### 15.2 profile 行为测试

- `AgentProfileDir` 优先级保持不变
- 原生共享 profile 不自动 prune 旧 MCP
- 宿主专用 profile 允许完整同步与清理
- `codex` managed fallback 继续可完整同步
- canonical shared profile 判定 helper 在 symlink / Windows 场景下稳定

### 15.3 adapter 单测

- Codex/Claude/Cursor 写出的配置文件形状正确
- run 时 env 指向 effective profile
- transport 不支持时及时报错
- 共享 profile 与专用 profile 下的写入策略符合预期
- Codex 只写 `config.toml`，不把 MCP 落到 `config.json`
- Cursor 不尝试管理 approval/login state

### 15.4 integration/live

- 至少一个 stdio 假 MCP server
- 至少一个 HTTP 假 MCP server
- 三个 adapter 各跑一次
- 确认 agent 能实际看到并调用工具

### 15.5 回归测试

- 无 MCP 时现有 run 行为不变
- skills/session/runtime 路径不被 MCP 实现污染

## 16. 分阶段实施

### Phase 0: 设计冻结

目标：

- 敲定公共类型
- 固化 shared profile / dedicated profile 的分类规则
- 固化 Cursor approval/login 不纳入声明式配置的边界

产出：

- 本文档

### Phase 1: Core + Codex

目标：

- 新增公共类型与 option
- `runner` 接入 MCP payload
- Codex 完成 profile-side 写入

产出：

- type contract
- core 测试
- codex adapter 单测

### Phase 2: Claude + Cursor

目标：

- Claude config writer
- Cursor `mcp.json` writer
- integration 假 server 测试打通

### Phase 3: 文档 + Example + 收口

目标：

- README / usage guide / examples
- capability 文档
- 回归测试

## 17. 边界与非目标

不进入 core：

- MCP marketplace / catalog
- OAuth secret storage
- provider-specific `mcp login` 工作流统一封装
- AG-UI frontend tools / tool result 回注
- sub-agent / delegation orchestration
- 宿主级自动路由

特别强调：

- MCP 不是 `skills`
- MCP 也不是 `runtime services`
- 它是独立的一类 agent-native tool provider config
- 但它和 `skills` 一样，落在 effective profile 上由 adapter 物化
- Cursor 的 approval/login state 不属于 v1 的 declarative MCP config

## 18. 已拍板决策

- Cursor:
  - SDK 只写 `mcp.json` 里的 server declaration
  - 不管理 `enable/disable/login`
  - approval/login 属于 operator-local state
- Codex:
  - MCP 只写 `config.toml`
  - `config.toml` 是 MCP 的唯一 source of truth
  - 不把 MCP 写入 `config.json`
- Shared profile 判定:
  - 若 `AgentProfile.Managed=true`，直接视为宿主专用 profile
  - 否则把 effective profile 与 canonical shared profile 路径比较
  - 相同则是原生共享 profile，否则是宿主专用 profile
- canonical path helper:
  - `Abs + Clean`
  - 路径存在时优先 `EvalSymlinks`
  - Windows 按不区分大小写比较
- 控制面:
  - v1 不新增 MCP control-plane API
  - 未来若要扩展，挂到 `AgentAdmin`
  - 不做 `Admin().MCP()`

## 19. 验收标准

- 宿主可以用统一抽象声明 MCP servers
- `codex` / `claude` / `cursor` 都能通过 effective profile 加载 MCP
- 原生共享 profile 采用追加/更新、不自动清理的保守策略
- 宿主专用 profile 采用完整同步策略
- `skills/MCP` 的变化不自动打断 session 复用
- 无 MCP 场景下现有行为零回归
- Cursor 对 approval/login 限制必须诚实暴露，不能伪装“全支持”

## 20. 一句话结论

这版方案的核心不是让 SDK 替宿主做 profile 策略判断，而是把边界讲清楚：

- SDK 只写 effective profile
- 共享 profile 只追加/更新，不自动清理
- 宿主专用 profile 做完整同步
- 宿主若需要精确、可清理、无残留的 MCP 集合，就自己提供隔离 profile
