# Workstream: Profile Resources for Agents, Hooks, Instructions, Config

> 状态：分阶段实施计划 + 当前落地记录。本文承接 [`workstream-effective-profile-materialization-plan.md`](./workstream-effective-profile-materialization-plan.md) 已拍板的 profile 管理规范，参考 skills / MCP 已落地的执行机制，补齐 `agents`、`hooks`、`instructions`、`config` 四类 profile resource 的完整落地计划。
>
> Review verdict：公共能力不按“最小交集”设计，而按“最大可努力支持的 capability envelope”设计：core 字段表达三家都能理解的 host intent；extended 字段表达至少一家有稳定 native surface、其它家有明确 fallback/unsupported 的能力；provider-native 细节通过显式 escape hatch 暴露。当前 `AgentSpec` / `HookSpec` / `InstructionsBundleRef` / `ProfileConfigPatch` 仍是 SDK desired-state 草案，进入 managed 实现前必须按本文第 3 节和 [`profile-resource-provider-matrix.md`](./profile-resource-provider-matrix.md) 修正字段语义。

## 1. 目标

把四类资源纳入与 skills / MCP 同一套 profile desired-state 语义：

- 宿主通过 binding defaults 或 per-run options 声明资源。
- SDK 在 `resolveInvocation` 中合并并规范化为 `ProfilePayload`。
- Adapter 在唯一的 `Run/Start` 主路径中，把资源物化到本次 effective profile。
- Admin 控制面可以观察和同步资源，但不执行 agent run。
- Session resume guard 使用统一 `ProfilePayload.Fingerprint`，避免 provider session 在 profile-visible 行为变化后被错误复用。

本计划不改变项目定位：`agent-adaptor` 仍是纯 Go SDK，不引入内置服务、调度器、profile store 或自动 agent routing。

## 2. 已有基础

当前代码已经具备这条路线的类型和部分执行基础：

- `ProfileResources` 已包含 `Skills`、`MCP`、`Agents`、`Hooks`、`Instructions`、`Config`。
- `ProfilePayload` 已作为 adapter-facing normalized desired state 放入 `DriverRunRequest.ProfilePayload`。
- `WithDefaultProfileResources` / `WithProfileResources` 已作为统一入口存在。
- `WithDefaultAgents` / `WithAgents`、`WithDefaultHooks` / `WithHooks`、`WithDefaultProfileConfig` / `WithProfileConfig` 已作为糖入口存在。
- `ProfileSnapshot(ctx)` / `SyncProfile(ctx)` 已存在，并且当 adapter 不支持某类资源落盘时会诚实返回 unsupported/error snapshot。
- built-in adapters 已经对 skills / MCP 证明了“宿主声明高层资源，adapter 按 effective profile 物化”的路径。

当前缺口：

- `config` 已支持 explicit provider-native JSON/TOML patch，以及 Codex / Claude Code / Cursor 的 allowlisted capability patch。
- `instructions` 已支持 Codex / Claude Code profile-native instruction files；Cursor profile sync 仍是 SDK-managed fallback，但 `Run` 时 project/local scope 会物化为 workspace `.cursor/rules/*.mdc`。
- `agents` 已支持 portable-core provider-native file materialization：Codex TOML agent、Claude/Cursor Markdown/YAML agent。
- `hooks` 已支持 command-hook core provider-native config materialization；Claude/Cursor 的 prompt 等 extended handler 按 adapter support 开放。
- 已建立 Codex / Claude Code / Cursor 调研 matrix，并把第一批 maximum-capability envelope 落到 Go struct、examples、support flag 和 adapter conformance。
- shared / host-managed 下的 ownership、conflict、manifest、lock、atomic write 还没有被四类资源复用。
- Admin snapshot 已报告四类资源的 `Support` / `Materialization`，并在 sync 后报告 managed keys；agents/instructions 已覆盖 external conflict，hooks 对 hook config target 做外部占用 hard fail。
- adapter conformance 已覆盖三家 adapters 的 `agents` / `hooks` / `instructions` / `config` 基础 managed path；剩余是 extended/native/fallback 组合的细粒度扩展。

## 3. Maximum Capability Research Gate

本节是四类资源进入 provider-native 实现前的硬门槛。目标不是把公共 API 压缩成三家最低共同字段，而是基于真实调研给出最大可努力支持的公共能力：能稳定抽象成 host intent 的进入 typed public Spec；只能原样交给某家 provider 的进入 native escape hatch；无法支持的必须被 capability report 和 snapshot 明确暴露。

当前 maximum-capability baseline 见 [`profile-resource-provider-matrix.md`](./profile-resource-provider-matrix.md)。

### 3.1 Evidence 要求

每个 resource kind 必须同时具备以下证据：

- 官方文档或本机 CLI / bundle 中可验证的 provider-native 配置表面。
- 本机真实 smoke test：在临时 profile 中写入最小配置，启动对应 CLI 或读取 provider diagnostic，确认配置被加载或至少被 provider 接受。
- Adapter capability matrix：逐 provider、逐字段标明 `portable_core` / `portable_extended` / `native_escape` / `fallback` / `unsupported`，并记录具体文件、section、event、字段映射。
- Conformance test：`ProfileSnapshot` / `SyncProfile` 不能把 unsupported 或 fallback 伪装成 native managed；extended 字段在不支持的 provider 上必须按 policy 返回 warning/error。

调研证据必须落到文档中，不能只存在于 PR 讨论或实现者记忆里。

### 3.2 Public Spec 准入规则

公共 Spec 字段按能力层级准入：

- `portable_core`：三家 provider 都有稳定概念，或都能通过 documented profile/rules surface 可靠表达。examples 默认使用这一层。
- `portable_extended`：host intent 是 provider-neutral，且至少一家 provider 有稳定 native surface；其它 provider 必须有明确 fallback 或 unsupported 语义。字段可以进入公共 API，但必须由 adapter capability report 决定是否生效。
- `native_escape`：宿主明确提供 provider-native file/blob/patch，adapter 只复制、引用或写入，不做跨 provider 翻译。它是公共逃生口，不是 portable capability。
- `fallback`：adapter 可通过 prompt injection、script-side filtering、CLI flag 等近似表达。必须进入 snapshot warning，不能冒充 native managed。
- `unsupported`：provider 无稳定 surface。必须按 host policy fail 或 warning，不能 silent success。

字段不会把某一家 provider 的语法直接泄漏到 core intent。例如 Claude/Codex/Cursor 的 hook event 名、matcher 语法、agent 文件格式都必须经过 adapter layout 映射；确实需要透传时只能进入 `Native` / `SourcePath` / provider-specific patch。

### 3.3 当前调研结论

| Resource | 最大公共能力 | 当前 Spec 评审 | Gate 结论 |
|---|---|---|---|
| `skills` | 可安装/可发现的 agent skill | 已有实现路径 | 已通过当前阶段 |
| `mcp` | profile/project 级 MCP server 配置 | 已有实现路径 | 已通过当前阶段 |
| `agents` | core：name/runtime name、description、instruction body、source passthrough。extended：model、effort、tool policy、permission/sandbox、MCP servers、skills、agent-local hooks。 | 现有 `AgentSpec{Key, RuntimeName, SourcePath, Content, Metadata}` 能表达一部分 desired state，但缺少 first-class `Description` / `Instructions`，也没有 extended 字段或 native escape hatch。 | ADMIT v2 envelope：先修 Spec；`Content` 迁移为 `Instructions` alias；extended 字段按 provider capability 实现。 |
| `hooks` | core：command hook、canonical event、matcher、timeout、fail policy、env、disabled。extended：prompt/http/mcp_tool/agent handler、shell/MCP/file/subagent shortcut events、provider output action。 | 现有 `HookSpec{Event, Matcher, Command, Args, Env, Disabled}` 是可用雏形，但 event/matcher/handler/fail policy 太弱，无法最大化表达 Claude/Cursor，也无法诚实表达 Codex 差异。 | ADMIT command-hook core + typed extended envelope；先实现 event mapping 和 support policy，再做 reconciler。 |
| `instructions` | core：path 或 inline content 进入 effective context，fingerprint 参与 resume guard。extended：scope、append/replace、path-scoped rules、provider rules format。 | 当前 `InstructionsBundleRef` 只有 path/id/fingerprint，不支持 inline content 和 scope/mode。 | ADMIT：扩展 source/scope/mode；snapshot 必须区分 native/file managed、prompt injected、unsupported。 |
| `config` | core：adapter-declared capability patch，例如 model、effort、sandbox、permission、env、MCP/hook policy。native escape：provider/file/section patch。 | 当前 `ProfileConfigPatch{Path, Section, Values}` 是写入机制而非公共能力；作为默认 API 过宽。 | ADMIT allowlisted capability patch；arbitrary path/section 只能作为 explicit provider-native escape hatch。 |

本表的结论是当前实现计划的 review 基线。后续如果官方文档或本机 CLI 行为变化，必须更新 matrix 和 conformance，再修改 support flag。

## 4. 复用 skills / MCP 的机制

四类资源不另起炉灶，直接复用以下机制。

### 4.1 声明与合并

与 skills / MCP 一样，资源来源只有两层：

- binding defaults：`WithDefaultProfileResources` 和各类 `WithDefaultX`。
- per-run override：`WithProfileResources` 和各类 `WithX`。

合并规则沿用总计划：

- skills：additive selected semantics，仍包含 provider required skills。
- MCP / agents / hooks / config：per-run override 替换该 resource kind 的完整 effective desired state。
- instructions：per-run override 替换 binding default；传 nil 表示清空本次 instructions。

原则：adapter 不关心资源来自 default 还是 per-run，只消费最终 `ProfilePayload`。

### 4.2 时序

四类资源必须跟 skills / MCP 一样在 adapter `Run` 内、resume guard 之后、CLI 启动之前 reconcile：

1. SDK 合并 defaults 与 per-run options。
2. SDK 解析 skills、MCP、agents、hooks、instructions、config，产出 `ProfilePayload`。
3. SDK 完成 session 协调。
4. Adapter 解析 effective profile。
5. Adapter 分类 `ProfileKind`：`shared` 或 `host_managed`。
6. Adapter 校验 resume guard，尤其是 `profile_fingerprint`。
7. Adapter 持有 profile-local lock，reconcile profile resources。
8. Adapter 启动 CLI。

禁止把 destructive reconcile 放进 `SkillAwareDriver.InjectSkills` 或其它 session 协调前 hook。

### 4.3 Profile Kind

所有资源共用已拍板的 effective profile 分类：

- `shared`：effective profile 是 provider 原生共享 profile，例如 `~/.claude` / `~/.codex` / `~/.cursor`。
- `host_managed`：宿主或 adapter 管理的隔离 profile，例如 dedicated、clone 或 Codex managed home。

分类必须基于最终 effective path，而不是直接看 `ProfileSelection.Mode`。

shared 不是只读模式。SDK 可以管理宿主声明的资源，但默认 conflict policy 仍是 hard fail，manifest 不可覆盖 external entry。

### 4.4 Ownership 与写入可靠性

四类资源全部使用同一套 profile-local 基础设施：

- `<profile>/.agent-adaptor/manifest.json`
- `<profile>/.agent-adaptor/lock`
- temp file + fsync best effort + rename 的 atomic writer
- JSON / TOML 结构化 parser / encoder
- provider-native layout helper

manifest 记录 managed resource 的 key、runtime name、文件路径或 config section、content fingerprint、source fingerprint、last applied timestamp。

manifest 禁止记录：

- token / API key / bearer value
- OAuth cache
- 带 secret 的 archive URL
- hook env 的原始 secret value

## 5. 公共资源语义

本节记录目标 public Spec envelope。它不是三家最小交集，而是三层能力：

- core：所有 built-in provider 都必须能 native 或 file-managed/fallback 地表达，并且 snapshot 要说明具体 materialization state。
- extended：公共 host intent 足够稳定，但 provider 支持不均；adapter 必须按 capability report 决定 native/fallback/unsupported。
- native escape：显式 provider-native 透传，不参与 portable 语义承诺。

当前 Go struct 可以分阶段迁移，但文档、examples、adapter capability 不应再把旧字段解释成“已经完成调研的完整公共能力”。

### 5.1 Agents

`AgentSpec` 表达 provider profile 里的子 agent / profile agent 资源，不等同于 SDK 的 named binding。目标 v2 应显式区分 portable core、extended capability 和 native escape。

```go
type AgentSpec struct {
	Key          string
	RuntimeName  string
	Description  string
	Instructions string
	SourcePath   string

	Model           string
	ReasoningEffort string
	ToolPolicy      *AgentToolPolicy
	PermissionMode  string
	SandboxMode     string
	MCPServers      []string
	Skills          []string
	Hooks           []HookSpec

	Native   map[string]any
	Metadata map[string]string
}
```

规则：

- `Key` 是 SDK merge、manifest、snapshot 主键。
- `RuntimeName` 是 provider-native 文件名或配置项名；为空时默认等于 `Key`。
- `Description` 和 `Instructions` 是 core。三家都存在“描述 + 系统提示/开发者指令/body”的 stable concept。
- 旧 `Content` 字段应迁移为 `Instructions` alias；不能同时表示 common instruction body 和 provider-native blob。
- `SourcePath` 是 native escape。它指向宿主已准备好的 provider-native agent 文件或目录，adapter 只复制/链接/引用，不猜测格式、不跨 provider 转译。
- `Model` / `ReasoningEffort` / `ToolPolicy` / `PermissionMode` / `SandboxMode` / `MCPServers` / `Skills` / `Hooks` 是 extended。adapter 能 native 映射就写入，不能映射则按 unsupported policy 返回 warning/error。
- `Native` 是 provider-scoped escape hatch，必须带 provider guard，不能在别的 adapter 上默默忽略。
- selected runtime name 被 external file / config item 占用时 hard fail。

第一期不做：

- SDK 自动决定本次 run 应该调用哪个 agent。
- 把 SDK named binding 反向生成 provider sub-agent。
- 在 core 中内置 planner / broker / router。

Provider support baseline：

- Codex：native TOML custom agent；core + model/effort/sandbox/MCP/skills 有明确 surface。
- Claude Code：native Markdown/YAML 或 `--agents` JSON；core + tools/model/permission/MCP/hooks/skills/effort/isolation 等 surface 最丰富。
- Cursor：native Markdown/YAML subagent 已由本机 bundled guide 证明；core 已成立，extended 字段需逐项 smoke test 后开放。

### 5.2 Hooks

`HookSpec` 表达 provider profile 里的 hook declaration。目标 v2 不直接暴露 provider event name，而暴露 canonical event + typed matcher + typed handler。

```go
type HookSpec struct {
	Key          string
	Event        HookEvent
	MatcherSpec  HookMatcher
	Handler      HookHandler
	Timeout      time.Duration
	FailPolicy   HookFailPolicy
	StatusMessage string
	Disabled     bool

	// v1 command-hook compatibility.
	Matcher string
	Command string
	Args    []string
	Env     map[string]string

	Native   map[string]any
	Metadata map[string]string
}
```

规则：

- `Key` 是 SDK ownership 主键。
- `Event` 使用 canonical host intent，例如 `session_start`、`prompt_submit`、`pre_tool`、`post_tool`、`permission_request`、`pre_shell`、`post_shell`、`pre_mcp`、`post_mcp`、`post_file_edit`、`subagent_start`、`subagent_stop`、`pre_compact`、`stop`。
- `Handler.Type=command` 是 core；`prompt`、`http`、`mcp_tool`、`agent` 是 extended。
- `MatcherSpec` 必须包含 subject 与 syntax，不能只是一段裸字符串。adapter 可优先使用 provider matcher；无法精确表达时可使用 script-side filtering，并在 snapshot warning 里说明。旧 `Matcher` 只作为 v1 command-hook 兼容字段保留。
- `Timeout` 和 `FailPolicy` 是 first-class 字段；不同 provider 的 fail-open/fail-closed/decision output 差异由 adapter layout 处理。
- command hook 可通过 `Handler{Type: command, Command, Args, Env}` 表达；旧 `Command` + `Args` + `Env` 字段继续兼容。若 provider 只接受 shell string，adapter 必须安全 quoting 或拒绝。
- hook env 写入 provider config 时必须按 provider 能力处理；manifest 只记录 env key 和 fingerprint，不记录 secret value。
- SDK 只写 hook declaration，不执行 hook command。
- shared profile 下同样允许管理 hooks，但 external conflict 必须 hard fail。

hooks 是四类资源里风险最高的一类，因此实现顺序放在 config / instructions / agents 之后。第一期必须有 manifest、lock、结构化写入和 conformance 后才允许落地 hooks。

第二期再补：

- hook diff / dry-run。
- hook command path 展示。
- takeover approval。
- backup / rollback。
- audit log。

Provider support baseline：

- Codex：native command hooks，事件覆盖 session/pre-tool/permission/post-tool/prompt/stop，regex matcher，timeout，status message。
- Claude Code：native hooks 最强，支持 command/http/mcp_tool/prompt/agent handler、广泛事件、decision output、agent/skill-scoped hooks。
- Cursor：本机 bundled guide 证明 command/prompt hooks、广泛事件、JS regex matcher、timeout、failClosed、loop_limit。

第一期允许先实现 command-hook core；prompt/http/mcp_tool/agent handler 进入 extended capability，按 provider support flag 渐进开启。

### 5.3 Instructions

`InstructionsBundleRef` 是宿主提供的 instructions bundle 引用。

```go
type InstructionsBundleRef struct {
	ID          string
	Path        string
	Content     string
	Fingerprint string
	Scope       InstructionScope
	Mode        InstructionMode
	Native      map[string]any
}
```

目标语义：

- instructions 进入 `ProfilePayload.Fingerprint`。
- `Path` 与 `Content` 二选一；`Content` 支持宿主直接声明 inline instructions，不要求为了一个短 prompt bundle 先落临时文件。
- Adapter 优先把 instructions 物化为 profile-local managed resource。
- 若 provider 没有稳定 profile-level instructions 入口，adapter 可以继续使用 prompt-level injection，但必须在 snapshot 中标明 `managed` 或 `injected` 状态，不能伪装成 provider profile 文件已落盘。
- `Path` 指向宿主维护的文件时，adapter 可选择复制到 `<profile>/.agent-adaptor/instructions/<id>` 后再引用，避免 provider 直接依赖宿主临时路径。
- `Fingerprint` 为空时，SDK / adapter 必须基于 path content 或 ID 生成稳定 fingerprint；不能让 path 字符串变化成为唯一行为判断。
- `Scope` 表达 user/project/local/run intent；adapter 能映射则落 provider-native 位置，不能映射则 fallback 或 unsupported。
- `Mode` 区分 additive context 与 provider-specific instruction replacement；replacement 只能在 provider 有稳定 surface 时开启。

第一期落地状态：

- `internal/profileinstructions` 支持 path/inline source、profile manifest/lock/atomic write、stale managed instruction prune。
- Codex profile-native：写入 effective `$CODEX_HOME/AGENTS.md`；`Mode=replace` 写入 `AGENTS.override.md`。
- Claude Code profile-native：写入 effective `$CLAUDE_CONFIG_DIR/CLAUDE.md`。
- Cursor profile sync：由于公开 CLI config 只稳定暴露 project rules 和 user settings，不暴露可写 profile-level rules 文件，`SyncProfile` 保持 SDK-managed fallback。`Run` 时若 `Scope=project|local`，写入 effective workspace 的 `.cursor/rules/<id>.mdc`，其它 scope 继续 prompt fallback。
- prompt injection fallback 进入 `ProfileSnapshot.Resources` warning，不能冒充 native managed。

Provider support baseline：

- Codex：AGENTS.md / `.codex` config layers / `model_instructions_file` 等文件或配置 surface；当前实现使用 profile-root `AGENTS.md` / `AGENTS.override.md`。
- Claude Code：`CLAUDE.md`、`.claude/rules/`、settings/system-prompt flags、subagent prompt surface；当前实现使用 profile-root `CLAUDE.md`。
- Cursor：`.cursor/rules`、user rules、AGENTS.md、legacy `.cursorrules`；当前实现仅在 run 时对 project/local scope 写 `.cursor/rules/*.mdc`，profile sync fallback。

`ProfileSnapshot` 需要区分 `native_managed`、`file_managed`、`prompt_injected`、`unsupported`。只有 provider 真正读取的 profile/rules 文件才算 native/file managed。

### 5.4 Config

`ProfileConfigPatch` 是结构化 profile config intent，不是任意 JSON/TOML 文件写入。默认公共 API 应绑定 adapter-declared capability；provider/file/section 只能作为 native escape hatch。

```go
type ProfileConfigPatch struct {
	Key        string
	Capability string
	Values     map[string]any
	Native     *NativeConfigPatch
}

type NativeConfigPatch struct {
	Provider string
	FileKind ProfileConfigFileKind
	Path     string
	Section  string
	Values   map[string]any
}
```

规则：

- `Capability` 必须出现在 `AgentAdmin.ConfigSchema()` 返回的 allowlist 中，例如 model、reasoning/effort、sandbox/isolation、approval/permission rules、env policy、MCP policy、hook policy、instruction-file linkage。
- `Values` 必须是 typed map，不允许传 raw JSON/TOML 字符串。
- 同一 run 内同 `Key` 重复报错。
- 同一 capability 的多个 patch 如有字段冲突必须报错；无冲突时可按 key 顺序稳定合并。
- 空 `Values` 不等于删除；删除语义第二期另行设计。
- secret-bearing config 必须使用 env-var reference 或 host secret handle；manifest 只记录 canonical fingerprint，不记录 raw secret value。
- `Native` 是明确 provider-native escape hatch。只有 `Provider` 匹配当前 adapter 时才允许执行；不匹配必须 error 或 ignore-with-warning，不能 silent success。

第一期不做：

- arbitrary file write。
- YAML / INI。
- JSON Patch RFC 6902。
- 删除 / rename / migration DSL。
- 直接覆盖整个 provider config 文件。

Provider support baseline：

- Codex：`config.toml` 支持 model、reasoning、approval、sandbox、permissions/rules、shell env、tools、skills、MCP、profiles、instruction file 等广泛配置。
- Claude Code：hierarchical JSON settings 支持 agent、permissions、env、sandbox、hook policy、MCP policy、attribution、managed settings 等；CLI flags 覆盖 per-session model/effort/system prompt。
- Cursor：CLI 明确支持 model、sandbox、mode、MCP commands、rule generation；持久 CLI config 只开放 smoke-tested allowlist，不开放 generic patch。

## 6. Adapter Layout 计划

provider-native 细节只能存在于 adapter layout helper，不能泄漏到 core 公共语义。

### 6.1 Claude

Effective profile:

- env：`CLAUDE_CONFIG_DIR`
- canonical shared：`~/.claude`

第一期映射：

- agents：Claude profile 的 provider-native agents 目录；layout helper 负责文件扩展名和 front matter。
- hooks：Claude profile 的 provider-native JSON settings / config section。
- instructions：profile-root `CLAUDE.md`；run-scoped transient bundle 继续 prompt prefix fallback 并进入 snapshot warning。
- config：Claude JSON config structured patch。

要求：

- 不再因 shared profile 自动退回纯 prompt-bundle 策略。
- hooks / config 写入必须使用 JSON parser，禁止字符串拼接。

### 6.2 Codex

Effective profile:

- env：`CODEX_HOME`
- canonical shared：`~/.codex`
- adapter managed fallback：`host_managed`

第一期映射：

- agents：Codex provider-native agents directory 或 config section；layout helper 先以 app-server schema / generated contract 可验证的入口为准。
- hooks：Codex provider-native TOML / JSON config section。
- instructions：profile-root `AGENTS.md`；`Mode=replace` 使用 `AGENTS.override.md`；run-scoped transient bundle 继续 prompt prefix fallback。
- config：`config.toml` / provider JSON config structured patch。

要求：

- TOML 写入继续使用 `github.com/pelletier/go-toml/v2`，迁移到通用 profile reconciler 后仍局部化在 profile 写入包内。
- 不手工编辑 `codex/appserver/generated.go` 或 `codex/appserver/schema/`；协议同步走既定 generate 流程。

### 6.3 Cursor

Effective profile:

- env：`CURSOR_HOME`
- canonical shared：`~/.cursor`

第一期映射：

- agents：Cursor provider-native agents directory 或 config section。
- hooks：Cursor provider-native `.cursor/hooks.json` / `~/.cursor/hooks.json`；先支持 bundled guide 已证明的 command/prompt hook surface。
- instructions：`SyncProfile` 使用 profile-local SDK-managed fallback；`Run` 对 project/local scope 写 workspace `.cursor/rules/*.mdc`，其它 scope 继续 prompt injection fallback。
- config：Cursor JSON config structured patch。

要求：

- 删除 Cursor 自己的 stale / prune 分支，改用统一 manifest ownership。
- 对 provider 不支持的资源，不降级成 silent success；在 `ProfileSnapshot` 中报告 unsupported/error。

## 7. 内部包拆分

沿用总计划的包边界：

- `internal/profilekind`：effective profile 分类。
- `internal/profilestate`：manifest、lock、atomic writer。
- `internal/profilereconcile`：directory / file / JSON / TOML resource reconciler。
- `internal/profilelayout`：provider-native layout helper。

建议子包职责：

- `profilelayout/claude`：Claude profile path 与 JSON section mapping。
- `profilelayout/codex`：Codex profile path、TOML/JSON section mapping、app-server schema alignment。
- `profilelayout/cursor`：Cursor profile path 与 JSON section mapping。
- `profilereconcile/directory`：skills / agents / instruction files。
- `profilereconcile/structured`：MCP / hooks / config JSON/TOML sections。

MCP 和 skills 迁移后也应使用这些包，不继续把通用 profile 写入逻辑留在 `internal/mcpruntime` 或 `internal/skillruntime`。

## 8. Admin 与 Snapshot

`ProfileSnapshot` 是宿主 UI / profile manager 的主要观察面。

每个 `ResourceSnapshot` 至少报告：

- `Kind`
- `Fingerprint`
- `Managed`
- `External`
- `Support`
- `Materialization`
- `Warnings`
- `Error`

四类资源的 snapshot 规则：

- desired key 已成功 reconcile：进入 `Managed`。
- profile 中存在但 manifest 不拥有：进入 `External`。
- desired runtime name 与 external 冲突：`Error` 包含 conflict detail，sync/run 失败。
- provider 不支持：`Support=unsupported`，`Error` 明确写 unsupported / not materialized。
- extended 字段被降级：`Support=portable_extended`，`Materialization=fallback`，`Warnings` 说明哪些字段没有 native 映射。
- prompt injection fallback：进入 `Managed`，但 `Materialization=prompt_injected`，`Warnings` 说明未落盘到 provider-native profile surface。
- native escape：进入 `Managed` 时必须标记 `Support=native_escape`，避免宿主误以为可跨 provider。

`SyncProfile(ctx)` 只使用 binding defaults，不读取 per-run options，不启动 CLI，不分配 `RunID`，不参与 session。

## 9. Session Guard

所有 built-in adapters 必须在 checkpoint 中写：

```go
SessionParamProfileFingerprint = "profile_fingerprint"
```

resume 规则：

- 旧 state 有 `profile_fingerprint` 且与本次 `ProfilePayload.Fingerprint` 不一致：adapter 返回 `ErrResumeRejected`。
- `continue_or_start` 由 runner fresh start。
- `continue_only` 向 host 暴露拒绝。
- 老 checkpoint 的 legacy guard 只做兼容读取，不再新增。

理由：agents、hooks、instructions、config 都会改变 provider-visible 行为，错误复用旧 provider session 会造成难排查的状态污染。

## 10. 实施顺序

### Phase 0：文档与测试基线

- 本文档落地。
- 建立 `docs/profile-resource-provider-matrix.md`，记录 Codex / Claude Code / Cursor 对 agents、hooks、instructions、config 的官方资料、本机 CLI 版本、本机 smoke test 命令、support level、field mapping 和 unsupported/fallback 原因。
- 在总计划和文档地图中加入链接。
- 补齐 `api-reference.md` 中 profile resource options 的当前入口说明。
- 为 unsupported/fallback behavior 增加/保留测试，确保尚未实现的字段级能力不会被报告为 native managed。

验收：

- `SyncProfile` 对已支持的 skills / MCP 报 managed。
- `SyncProfile` 对已实现的 agents / hooks / instructions / native config patch 报告真实 materialization；对尚未实现的 config capability patch 和 unsupported extended 字段报 warning/error，不能假装 managed。
- matrix 明确哪些 Spec 字段是 `portable_core`、`portable_extended`、`native_escape`、`fallback`、`unsupported`。
- 每个后续 phase 必须引用 matrix 中对应 resource kind 的字段级 support 判定。

### Phase 0.5：公共 Spec envelope 修正

- 扩展 `AgentSpec`：增加 `Description`、`Instructions`、extended fields、`Native`；明确旧 `Content` 迁移策略。
- 扩展 `HookSpec`：增加 canonical `HookEvent`、`MatcherSpec HookMatcher`、`HookHandler`、`Timeout`、`FailPolicy`、`StatusMessage`、`Native`，保留旧 `Matcher` / `Command` / `Args` / `Env` 兼容入口。
- 扩展 `InstructionsBundleRef`：增加 `Content`、`Scope`、`Mode`、`Native`。
- 重塑 `ProfileConfigPatch`：默认走 `Capability + Values`，把 `Path` / `Section` 移入 `NativeConfigPatch`。
- 为 unsupported policy 增加统一约定：core 字段必须支持或 fallback；extended 字段按 adapter capability native/fallback/unsupported；native escape 必须 provider guard。

验收：

- `api-reference.md` 明确新字段支持层级和旧字段迁移语义。
- examples 只使用 portable core 和明确的 extended 字段，不再用 provider-native path/section 冒充 common config。
- payload fingerprint 覆盖新增字段，并且 unsupported/fallback 选择不会改变 fingerprint 稳定性。

### Phase 1：通用 profile state 基础设施

- 抽出 `internal/profilekind`，让 MCP / skills / snapshot 共用。
- 实现 `internal/profilestate`：lock、manifest、atomic writer。
- 实现 JSON / TOML structured writer。
- 实现 directory entry reconciler。
- 当前落地状态：`internal/profilestate` 已提供 profile-local lock、manifest、atomic writer；`internal/profilereconcile` 已提供 directory entry reconciler 与 JSON/TOML section patch writer。后续 phase 接入 adapter-native layout 时复用这些基础设施。

验收：

- 同一 profile 并发 sync/run 不破坏 manifest。
- crash/error 不留下半写 JSON/TOML。
- external conflict hard fail。
- 已覆盖测试：profile lock exclusivity、manifest round-trip、atomic write temp cleanup、directory write/update/prune、external conflict、source file copy、JSON/TOML section patch preservation。

### Phase 2：Config Capability Patch

- 实现 adapter-declared `ProfileConfigPatch{Capability, Values}` reconciler。
- `AgentAdmin.ConfigSchema()` 返回每个 adapter 支持的 capability、字段 schema、support level、materialization strategy。
- `NativeConfigPatch` 作为 explicit provider-native escape hatch，支持 JSON / TOML，限制 path 不得逃出 effective profile root。
- 增加 capability overlap conflict 检测；native patch 增加 file/section overlap conflict 检测。
- 当前落地状态：explicit native JSON/TOML patch 已接入 Codex / Claude Code / Cursor 的 `SyncProfile` 与 run 前 profile reconcile；`Capability + Values` allowlist/reconciler 已开放一批已调研表面：Codex `model` / `reasoning_effort` / `sandbox` / `approval`，Claude Code `model` / `effort` / `permission` / `env`，Cursor `sandbox` / `approval` / `permissions` / `display`。这些 allowlist 已作为 `ConfigSchema` 的 `profile_config.*` 字段暴露给 Admin 控制面。provider guard、path escape、unsupported capability snapshot 已有测试。

验收：

- Codex / Claude / Cursor 至少各有一个 smoke-tested capability patch。
- unsupported capability、unsupported native provider、file kind、path escape、overlapping patch 返回错误或 warning，符合 unsupported policy。
- 不再把 arbitrary JSON/TOML path/section 暴露为默认公共示例。
- 已覆盖测试：native patch materialization、provider mismatch、path escape、unsupported capability snapshot、Codex sandbox capability materialization、Claude env capability materialization、Cursor sandbox capability materialization、Codex Admin `SyncProfile` capability config materialization、三家 `ConfigSchema` profile config capability field metadata。

### Phase 3：Instructions

- 实现 managed instruction file reconciler。
- 支持 `Path` 与 `Content` source。
- 为三家 adapter 接入 provider-native rules/instruction reference 或 prompt injection fallback。
- snapshot 明确区分落盘 managed 与 injection fallback warning。
- 当前落地状态：`internal/profileinstructions` 已支持 path/inline source、profile-local manifest/lock/atomic write、stale prune、run 前 prompt prefix fallback。Codex 写 profile-root `AGENTS.md` / `AGENTS.override.md`，Claude Code 写 profile-root `CLAUDE.md`，Cursor profile sync 写 SDK fallback；Cursor `Run` 对 project/local scope 写 workspace `.cursor/rules/*.mdc`。

验收：

- instructions fingerprint 变化会触发 resume reject。
- managed instruction file 不依赖宿主临时路径。
- prompt injection fallback 不被报告成 provider-native config 已落盘；Codex/Claude native file 不再被报告成 fallback。
- 已覆盖测试：inline fallback sync、source path prepare、stale managed instruction prune、Codex native `AGENTS.md`、Claude native `CLAUDE.md`、Cursor project `.cursor/rules/*.mdc`、Admin `SyncProfile` Codex native materialization、全量 `go test ./...`。

### Phase 4：Agents

- 实现 agents directory/config resource reconciler。
- `Description` + `Instructions` 渲染为 provider-native agent file。
- extended 字段按 capability report native/fallback/unsupported。
- `SourcePath` 复制/链接/引用 provider-native source。
- 三家 adapter 各自补 layout helper。
- 当前落地状态：`internal/profileagents` 已接入 shared directory reconciler。Codex 渲染 `agents/*.toml`，Claude Code / Cursor 渲染 `agents/*.md` YAML frontmatter；`SourcePath` 按 native escape 复制到 provider-native agents directory；不支持的 extended 字段进入 snapshot warning。

验收：

- host-managed profile 可完整同步 portable core agents。
- supported extended agent fields 被正确物化；unsupported extended fields 按 policy 返回。
- shared profile 可管理 manifest-owned agents，external conflict hard fail。
- 删除 stale managed agent 不删除 external agent。
- 已覆盖测试：Codex TOML agent materialization、Claude Markdown/YAML agent materialization、external conflict hard fail、Admin `SyncProfile` managed agents。
- 剩余扩展：字段级 conformance 还需继续覆盖 Codex skills/MCP agent config、Claude agent-local hooks、Cursor extended fields。

### Phase 5：Hooks

- 在 config reconciler 已稳定后实现 hooks。
- 先实现 command-hook core：canonical event、matcher、handler command、timeout、fail policy。
- 三家 adapter 分别接 provider-native hooks config；没有稳定 surface 的字段必须 fallback/unsupported。
- prompt/http/mcp_tool/agent handlers 作为 extended capability 分别开启。
- hook env secret value 不进入 manifest。
- 当前落地状态：`internal/profilehooks` 已将 canonical events 映射到 Codex / Claude Code / Cursor provider event 名；Codex 写 `hooks.json` command hooks，Claude Code 写 `settings.json` hooks，Cursor 写 `hooks.json`。Claude Code 开放 prompt/http/mcp_tool/agent handler，Cursor 开放 prompt handler，Codex 仅 command handler。hook env values 不写入 provider config 或 manifest value。

验收：

- disabled hook 允许无 handler。
- enabled hook 无 handler 报错。
- command / args 保持结构化；provider shell string 由 adapter quoting 或拒绝。
- unsupported event / matcher / handler type 按 capability policy 返回。
- shared profile external conflict hard fail。
- 已覆盖测试：Codex command hook JSON、Claude prompt hook settings JSON、Cursor unsupported HTTP handler error、external hooks config conflict、Admin `SyncProfile` managed hooks。
- 剩余扩展：当前 hooks config target 采用保守 whole-file ownership；对已有 provider settings 的细粒度 merge/takeover、diff/dry-run 和 backup/rollback 仍属于第二期安全治理。

### Phase 6：统一迁移 skills / MCP

- MCP 从 `internal/mcpruntime` 迁到 `profilereconcile` + `profilelayout`。
- Skills 从 adapter-specific stale/prune 逻辑迁到 directory reconciler。
- 保留 `internal/mcpruntime` 兼容薄层或删除，取决于迁移范围。

验收：

- skills / MCP 既有测试继续通过。
- 新 profile manifest 能同时记录 skills、MCP、agents、hooks、instructions、config。

### Phase 7：Conformance 与文档

- `adaptertest` 增加 profile resource matrix。
- `usage-guide.md` 增加完整 profile resources 示例。
- `api-reference.md` 标明字段级 support level、adapter-specific unsupported/fallback 行为和 native escape 限制。
- release notes 标明 shared profile 管理、conflict fail、session guard 行为。

验收：

- conformance matrix 覆盖 Codex / Claude Code / Cursor 三个 adapter 的 agents、hooks、instructions、config 基础 managed path；extended 字段组合继续增量补齐。
- docs、examples、API reference 与 Go struct 字段名一致，不保留已删除 API 或把 fallback 写成 native managed。
- examples 能通过 `-agent codex|claude|cursor` 真实调用本机 CLI；缺失 CLI 时给出清晰跳过/错误，不使用 mock。

### Phase 8：完整性验收

- 汇总 Phase 0-7 的验收证据，逐项对照 `profile-resource-provider-matrix.md` 的 support level、materialization strategy、fallback/unsupported 原因。
- 运行完整测试：`go test ./...`、`git diff --check`，并检查 examples 可编译。
- 抽样运行至少一个本机 CLI smoke example；若某 CLI 不存在，记录为环境缺口而不是功能通过。
- 审计 `ProfileSnapshot.Resources`：每个 resource kind 必须同时报告 `Support` 和 `Materialization`，禁止 unknown/empty 状态被误解为 managed。
- 审计 session guard：新增 profile resource 字段必须进入 payload fingerprint；fallback/native materialization 改变必须能触发 session resume reject。
- 审计文档地图：`README`、`api-reference`、usage guide、workstream、provider matrix 互相链接且措辞一致。

验收：

- 所有 phase 的 acceptance checklist 都有测试、文档或 smoke evidence。
- 没有 `unsupported` resource 被报告为 `managed`；没有 fallback 被报告为 native file/config 已落盘。
- Go tests 与 diff checks 通过；未通过项必须列为明确 remaining risk。

当前完整性验收记录：

- `go test ./...` 已覆盖 root、Codex、Claude Code、Cursor、profile state/reconcile/config/instructions/agents/hooks 包和 examples 编译。
- `examples/profile-resources` 已改为真实本机 CLI agent switching（`-agent codex|claude|cursor`），并避免 mock；Codex smoke 已在本机认证环境通过，Claude/Cursor 在本机 auth 缺失时能真实到达 CLI 并暴露认证错误。
- `ProfileSnapshot.Resources` 现在对 skills / MCP / agents / hooks / instructions / config 都报告 `Support` 与 `Materialization`。
- instructions 当前 materialization：Codex `native_managed` -> `AGENTS.md` / `AGENTS.override.md`；Claude Code `native_managed` -> `CLAUDE.md`；Cursor `SyncProfile` fallback，`Run` project/local scope -> workspace `.cursor/rules/*.mdc`。
- `adaptertest` conformance 已覆盖三家 adapter 的 profile resources 基础 managed path：agents、hooks、instructions、config。
- `ProfilePayload.Fingerprint` 覆盖 agents、hooks、instructions、config，三家 adapter checkpoint 都写 `profile_fingerprint`，resume guard 对 profile resource 变化返回 resume rejected。
- Remaining risks：hooks 当前采用保守 ownership，细粒度 settings merge/takeover/diff/backup 仍待第二期；skills/MCP 尚未完全迁到统一 manifest reconciler；conformance 还需继续扩展到所有 extended/native/fallback 字段组合。

## 11. 测试矩阵

通用测试：

- payload normalization：key trim、runtime default、duplicate fail。
- source/instructions exclusive。
- config capability schema validation；native file kind / path / section validation。
- hook disabled / handler validation。
- hook event mapping validation。
- unsupported policy behavior。
- fingerprint stability。
- manifest read/write。
- lock concurrency。
- atomic writer cleanup。
- path escape rejection。

Reconciler 测试：

- host-managed full sync。
- shared profile managed update。
- stale managed prune。
- external preserve。
- external conflict。
- takeover policy reserved for explicit future option。
- broken symlink repair。
- JSON structured update。
- TOML structured update。

Adapter 测试：

- Claude / Codex / Cursor profile kind classification。
- adaptertest profile resource conformance：agents、hooks、instructions、config 基础 managed path。
- `Run` 和 `Start().Wait()` materialization 一致。
- session `profile_fingerprint` reject。
- `SyncProfile` 不产生 run。
- unsupported resource honestly reports error.
- extended field fallback/unsupported is reported at field/resource level.

Host-facing 测试：

- `ProfileSnapshot` managed/external/error fields match disk state。
- `ListSkills` 继续兼容。
- `SetSelectedSkills` 仍是 process-local override，不变成 profile store。

## 12. 非目标

- 不把 provider hooks 的执行结果纳入 SDK 事件流。
- 不做 hook approval UI。
- 不做 provider agent routing。
- 不做 profile diff / rollback / backup，留到第二期。
- 不接受 raw JSON/TOML string patch。
- 不把 secret value 写入 manifest。
- 不让 bridge 层参与 profile materialization。
- 不新增 `RunWithAgents`、`RunWithHooks`、`RunWithConfig` 这类第二执行入口。

## 13. 开放问题

- `DriverDescriptor` 是否需要新增静态 `ProfileResources` capability，还是继续以 `ProfileSnapshot` / `SyncProfile` 作为真实能力来源。
- unsupported policy 应作为 `RunPolicy` 的一部分，还是 profile resource option 的一部分。
- `HookMatcher` 是否需要 `ScriptSideFilter` 显式字段，还是由 adapter 自动 fallback。
- hooks 的 env 是否需要 first-class secret-by-env-var 字段，避免宿主误传 raw secret。
- provider-native agents layout 是否需要先做 smoke test 固化，再开放所有 built-in adapters 的 support flag。

## 14. 推荐开工顺序

先做 config，再做 instructions，再做 agents，最后做 hooks。

原因：

- config 让 JSON/TOML structured writer、lock、manifest 先站稳。
- instructions 风险较低，但会验证 profile fingerprint 与 injection fallback。
- agents 复用 directory resource 能力，风险中等。
- hooks 会执行命令，必须等 manifest、snapshot、conflict、parser、conformance 全部稳定后再启用。
