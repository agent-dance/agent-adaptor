# Workstream: Effective Profile Materialization Plan

> 状态：负责人计划。本文不局限于 skills，也不局限于宿主原提案；它重新定义 SDK 如何管理 Claude / Codex / Cursor 的 effective profile 中的 MCP、skills、agents、hooks、instructions、provider config 等资源。由于 SDK 尚未正式对外发布，本文允许必要的内部 breaking changes；但 profile 公共入口保持现有 API，不做重命名。

## 1. 目标

把 profile materialization 从“各 adapter 自己顺手写几个文件”收敛成 SDK 的一套清晰语义：

- 宿主通过现有 profile API 声明 profile 选择意图：复用 native shared profile，或使用 host-managed profile。
- 宿主声明 profile desired resources：MCP、skills、agents、hooks、instructions、config。
- SDK 合并 binding defaults 与 per-run overrides，产出统一 `ProfilePayload`。
- Adapter 在同一条 `Run/Start` 内部流程中，把 `ProfilePayload` 物化到本次 effective profile。
- Admin 控制面可同步和观察 profile，但不执行 agent run。

第一期先支持两个内部 profile kind 的完整 materialization 能力：

- `shared`：provider 原生共享 profile，例如 `~/.claude` / `~/.codex` / `~/.cursor`。
- `host_managed`：宿主或 adapter 管理的隔离 profile，例如 task/role profile、clone profile、Codex managed home。

第二期再做更细的安全治理：

- 按资源维度授权。
- dry-run / diff。
- 审计记录。
- backup / rollback。
- hooks 执行路径和触发条件展示。

这里的“第二期”只推迟产品化安全治理，不推迟第一期必须具备的可靠性基础。锁、manifest、原子写、冲突检测、结构化解析都属于第一期。

## 2. 设计原则

### 2.1 可靠优先

profile 写入是会改变用户或宿主本地 agent 行为的操作，不能依赖 ad-hoc 字符串拼接或 best-effort side effect。

第一期必须做到：

- 写入前完成结构化校验。
- 写入过程持有 profile-local lock。
- JSON / TOML / provider config 使用结构化 parser / encoder。
- 文件写入使用 temp file + fsync best effort + rename。
- manifest 写入与资源写入保持可恢复。
- conflict 必须 hard fail，不允许静默 shadow 用户或外部系统的资源。

如果可靠性需要引入局部依赖，按 AGENTS §2.4 执行。比如 TOML parser、跨平台 file lock 这类基础能力，不能为了“零依赖”手写脆弱代码。

### 2.2 语义清晰

当前 `ProfileSelection.Mode` 容易被误用为运行时行为分支：

- profile 选哪里。
- 这个 profile 是 shared 还是 host-managed。
- SDK 是否有权管理它。
- clone 是否发生。

新设计必须在内部拆开：

- `ProfileSelection`：宿主通过现有 API 表达 profile 选择意图。
- `ProfileKind`：effective profile 最终是 shared 还是 host-managed。
- `ProfilePayload`：本次要物化哪些资源。
- `ProfileFingerprint`：这些资源的 provider-visible 状态指纹。

不要再通过 `ProfileSelection.Mode == dedicated` 这类 raw option 直接判断行为。所有 adapter 必须先解析 effective profile，再内部推导 `ProfileKind`。

### 2.3 可持续维护

三家 adapter 只能保留 provider-native 映射差异，不能保留三套 ownership / prune / conflict / manifest 规则。

允许不同的是：

- Claude / Codex / Cursor 的具体文件路径。
- JSON / TOML / Markdown / directory layout。
- CLI 是否还需要额外参数。

必须统一的是：

- profile classification。
- resource desired-state merge。
- manifest ownership。
- lock / atomic write。
- conflict policy。
- snapshot / sync 返回形状。
- session guard。

## 3. 当前 SDK 问题诊断

### 3.1 Profile API 语义不够精确

当前公共入口：

- `WithNativeProfile()`
- `WithDedicatedProfile(dir)`
- `WithCloneProfile(dir, opts)`
- `WithCloneProfileFrom(src, dst, opts)`

问题：

- `native` 容易被理解成“只读复用用户 profile”，但本地 profile 管理器场景又确实需要写 shared profile。
- `dedicated` 表达的是使用隔离目录，但真正执行时还要看 env、process env、managed fallback。
- `clone` 是初始化策略，不应和 profile target 混成同一层语义。
- 当前 `internal/mcpruntime.ClassifyProfile` 已经在做 effective profile 分类，但只服务 MCP，命名和复用边界都不对。

### 3.2 Materialization 分散在 adapter 内

当前实现大致是：

- MCP：`internal/mcpruntime` 有一些 shared / dedicated 规则。
- Skills：Claude / Codex / Cursor 都写入 effective profile 的 skills home；Claude prompt bundle 仅作为 legacy session guard 兼容背景。
- Instructions：主要是 prompt prefix / file path 注入，没有统一 profile resource 语义。
- Agents / hooks / provider config：没有统一公共模型。

这导致：

- shared profile 和 host-managed profile 的行为不一致。
- Claude / Codex / Cursor 的 conflict 和 prune 规则漂移。
- Admin snapshot 只能看 skills，不能看整个 profile desired state。
- session guard 统一记录 cwd / workspace / profile fingerprint；Claude 旧 checkpoint 的 prompt bundle key 继续作为 legacy guard。

### 3.3 `InjectSkills` 时序不适合 destructive reconcile

Runner 当前在 session 协调之前调用 `SkillAwareDriver.InjectSkills`。如果这里 prune profile，后面 session reject / busy / policy unsupported，profile 已经被改坏。

新设计里 destructive profile reconcile 必须发生在 adapter `Run` 内、resume guard 之后、CLI 启动之前。

## 4. 新公共心智

### 4.1 两种内部 Profile Kind

公共 API 继续保留现有四个 `AgentOption`：

```go
func WithNativeProfile() AgentOption
func WithDedicatedProfile(dir string) AgentOption
func WithCloneProfile(dir string, opts CloneProfileOptions) AgentOption
func WithCloneProfileFrom(src, dst string, opts CloneProfileOptions) AgentOption
```

内部 materialization 只看解析后的两种 `ProfileKind`：

```go
type ProfileKind string

const (
	ProfileKindShared      ProfileKind = "shared"
	ProfileKindHostManaged ProfileKind = "host_managed"
)
```

语义：

- `shared`：effective profile 是 provider 原生共享 profile，例如 `~/.claude` / `~/.codex` / `~/.cursor`。
- `host_managed`：effective profile 是宿主或 adapter 管理的隔离 profile，例如 dedicated profile、clone profile、Codex managed home。

注意：shared 不是只读模式。使用 `WithNativeProfile()` 的宿主可能就是在做本地 profile 管理器，SDK 应允许 materialize 宿主声明的 resources。若宿主只是想借用本地认证而不污染用户 profile，应使用 `WithCloneProfile(...)`。

### 4.2 Profile API 保持不变

不改公共 API 的原因：

- 现有 API 已经能表达宿主意图：native / dedicated / clone / clone-from。
- 问题出在 adapter 直接拿 `ProfileSelection.Mode` 当行为分支，而不是 API 命名本身。
- `WithNativeProfile()` 表达复用 provider native profile；它不等于只读，也不等于禁止管理。
- `WithDedicatedProfile(...)` / `WithCloneProfile(...)` 表达 host-managed profile 的常见来源。
- 第二期安全治理只能通过新增 option 或 Admin sync option 扩展，不修改这四个 profile API。

### 4.3 Effective Profile 分类

Adapter 必须统一调用内部 helper：

```go
type ProfileKind string

const (
	ProfileKindShared      ProfileKind = "shared"
	ProfileKindHostManaged ProfileKind = "host_managed"
)
```

分类规则：

1. Adapter 按 provider 规则解析 effective profile。
2. 若 `ProfileSelection.Mode` 是 `dedicated` 或 `clone`，结果通常是 `host_managed`。
3. 若 adapter 返回 `AgentProfile.Managed=true`，结果是 `host_managed`。
4. 若 effective path 等于 canonical shared path，结果是 `shared`。
5. 其它自定义 path 结果是 `host_managed`。

路径比较要求：

- `filepath.Abs`
- `filepath.Clean`
- 路径存在时 `EvalSymlinks`
- Windows case-insensitive compare

将当前 `internal/mcpruntime.ClassifyProfile` 抽到 `internal/profilekind` 或 `internal/adapterprofile` 这类中性包，MCP / skills / hooks / config 全部复用。

## 5. Profile Resource 模型

### 5.1 统一 Payload

新增 adapter-facing payload：

```go
type ProfilePayload struct {
	Skills       ResolvedSkills
	MCP          MCPPayload
	Agents       AgentPayload
	Hooks        HookPayload
	Instructions *InstructionsBundleRef
	Config       ProfileConfigPayload

	Fingerprint string
	Warnings    []string
}
```

`ProfilePayload.Fingerprint` 是本次 provider-visible profile resources 的总指纹。

第一期它至少覆盖：

- skills fingerprint
- MCP fingerprint
- agents fingerprint
- hooks fingerprint
- instructions fingerprint
- config patch fingerprint

### 5.2 新资源类型

现有：

- `ResolvedSkills`
- `MCPPayload`
- `InstructionsBundleRef`

新增：

```go
type AgentSpec struct {
	Key         string
	RuntimeName string
	SourcePath  string
	Content     string
	Metadata    map[string]string
}

type AgentPayload struct {
	Agents      []AgentSpec
	Fingerprint string
	Warnings    []string
}

type HookSpec struct {
	Key       string
	Event     string
	Matcher   string
	Command   string
	Args      []string
	Env       map[string]string
	Disabled  bool
	Metadata  map[string]string
}

type HookPayload struct {
	Hooks       []HookSpec
	Fingerprint string
	Warnings    []string
}

type ProfileConfigPatch struct {
	Key      string
	FileKind ProfileConfigFileKind
	Path     string
	Section  string
	Values   map[string]any
}

type ProfileConfigPayload struct {
	Patches     []ProfileConfigPatch
	Fingerprint string
	Warnings    []string
}
```

字段可以继续收敛，但原则先定：

- key 是 SDK merge / ownership / snapshot 主键。
- runtime name 是 provider-native 文件名或配置项名。
- source / content 二选一时必须规范化。
- config patch 必须是结构化 patch，不允许让 host 传“随便拼好的 JSON/TOML 字符串”让 SDK 盲写。

### 5.3 Binding 与 Run Options

新增统一资源入口：

```go
func WithDefaultProfileResources(resources ProfileResources) AgentOption
func WithProfileResources(resources ProfileResources) RunOption
```

保留糖：

- `WithDefaultSkills`
- `WithSkills`
- `WithDefaultMCP`
- `WithMCP`
- `WithDefaultInstructions`
- `WithInstructions`

新增糖：

- `WithDefaultAgents`
- `WithAgents`
- `WithDefaultHooks`
- `WithHooks`
- `WithDefaultProfileConfig`
- `WithProfileConfig`

合并规则第一期拍板：

- skills：沿用当前 additive selected semantics，加上 provider required。
- MCP / agents / hooks / config：per-run override 替换该 resource kind 的完整 effective desired state。
- instructions：per-run override 替换 binding default。

这个规则牺牲一点灵活性，但语义清晰：除 skills 外，一个 run 的 resource override 就是“这一类资源这次完整长这样”。

## 6. Reconciler 架构

### 6.1 内部包

新增内部包：

- `internal/profilekind`：effective profile 分类。
- `internal/profilestate`：manifest、lock、atomic writer、snapshot。
- `internal/profilereconcile`：directory resource / JSON resource / TOML resource 通用 reconciler。
- `internal/profilelayout`：provider-native layout helpers。

不要继续把 shared helper 放在 `internal/mcpruntime` 或 `internal/skillruntime` 里。MCP 和 skills 都只是 profile resources 的一种。

### 6.2 Reconciler 合同

内部合同：

```go
type ResourceReconciler interface {
	Kind() ProfileResourceKind
	Plan(ctx context.Context, req ReconcileRequest) (ResourcePlan, error)
	Apply(ctx context.Context, plan ResourcePlan) (ResourceSnapshot, error)
	Snapshot(ctx context.Context, req SnapshotRequest) (ResourceSnapshot, error)
}
```

第一期必须支持：

- directory entries：skills / agents。
- JSON object sections：Claude MCP / hooks / config。
- TOML sections：Codex config。
- JSON file layout：Cursor MCP / config。

### 6.3 Manifest

每个 profile root 下写一个 SDK manifest，建议：

```text
<profile>/.agent-adaptor/manifest.json
<profile>/.agent-adaptor/lock
```

manifest 记录：

- schema version
- driver type
- profile kind
- resource kind
- resource key
- runtime name / file path / config section
- content fingerprint
- source path fingerprint
- last applied timestamp

manifest 不记录：

- token
- API key
- bearer value
- OAuth cache
- archive URL 带 secret 的原文

### 6.4 Conflict Policy

第一期只支持两个策略：

```go
type ConflictPolicy string

const (
	ConflictFail    ConflictPolicy = "fail"
	ConflictTakeover ConflictPolicy = "takeover"
)
```

默认：

- shared：`ConflictFail`
- host-managed：`ConflictFail`

`ConflictTakeover` 只允许 host 显式设置，且只接管同 key / runtime name 的 resource，不允许全目录扫荡。

第二期再加 diff / approval / backup 后，可以让 takeover 更适合 UI 管理工具。

## 7. Run 时序

统一执行流程调整为：

1. 合并 binding defaults 与 per-run overrides。
2. 解析为 `resolvedInvocation`，其中包含 `ProfilePayload`。
3. session 协调，拿到已有 driver state。
4. adapter `Run`：
   - 解析 effective profile。
   - 分类 profile kind。
   - 校验 resume guard。
   - 持锁 reconcile profile resources。
   - 同步 runtime env。
   - 启动 CLI。
5. adapter 解析正式协议事件，产出 checkpoint / transcript / output。
6. SDK 持久化 checkpoint 与结果归档。

废弃 destructive `InjectSkills` 用法。SDK 仍会在每次 run 前调用该 hook 以保持第三方 adapter SPI contract；hook 返回错误时 run 不启动并执行 cleanup。Built-in adapters 保持 no-op，并在 adapter `Run` 内 resume guard 通过后执行真实 profile-local reconcile。

## 8. Session Guard

新增统一 session param：

```go
const SessionParamProfileFingerprint = "profile_fingerprint"
```

替代：

- `SessionParamPromptBundleKey`
- 单独的 `skills_fingerprint`

第一期 breaking 语义：

- 所有 built-in adapters 的 checkpoint 都写 `profile_fingerprint`。
- resume 时，旧 state 中 `profile_fingerprint` 与本次 `ProfilePayload.Fingerprint` 不一致，adapter 返回 `ErrResumeRejected`。
- `continue_or_start` 可 fresh start。
- `continue_only` 返回拒绝。

理由：

- MCP、skills、agents、hooks、instructions、config 都可能改变 provider-visible 行为。
- 继续复用旧 provider session 却在 profile 中换掉工具、skills 或 hooks，是最难排查的状态污染。
- 既然未正式发布，统一 guard 比继续维护 prompt bundle / skills / MCP 多套规则更清晰。

## 9. Admin 控制面

`AdminAPI` 仍然不长 `Run/Start`。

`AgentAdmin` 可以增加 profile 控制面：

```go
type AgentAdmin interface {
	// existing methods...
	GetProfile(ctx context.Context) (AgentProfile, error)
	ProfileSnapshot(ctx context.Context) (ProfileSnapshot, error)
	SyncProfile(ctx context.Context) (ProfileSnapshot, error)
}
```

语义：

- `ProfileSnapshot`：观察当前 effective profile 中 SDK 管理和外部资源的状态。
- `SyncProfile`：按 binding defaults materialize profile resources，但不执行 agent run。
- `SyncProfile` 是控制面，不是第二执行入口；它不产生 `RunID`，不触发 session，不调用 provider model。

本地 Claude 管理工具可以用：

```go
sdk.Admin().Default().SyncProfile(ctx)
```

来安装/更新 shared profile 的 MCP、skills、agents、hooks、config，而不必伪造一次 run。

## 10. Adapter 落地

### 10.1 Claude

Effective profile:

- shared canonical：`~/.claude`
- env：`CLAUDE_CONFIG_DIR`

Resource mapping 第一版：

- skills：`<CLAUDE_CONFIG_DIR>/skills/<runtime_name>`
- agents：provider-native agents directory
- MCP：provider-native JSON config section
- hooks：provider-native JSON config section
- config：provider-native JSON config patch
- instructions：profile-local managed instruction file 或 prompt-level injection，二者必须计入 `ProfileFingerprint`

变化：

- host-managed 下不再为 selected skills 构造 prompt bundle。
- shared 下也允许完整 materialization，因为 `WithNativeProfile()` 明确表示宿主要复用本地 profile；SDK 内部根据 effective path 推导为 `shared`。
- 如果未来需要“仅临时注入不落盘”的 Claude 模式，应作为另一个显式 option，而不是混在 shared profile 语义里。

### 10.2 Codex

Effective profile:

- shared canonical：`~/.codex`
- env：`CODEX_HOME`
- adapter managed fallback：host-managed

Resource mapping 第一版：

- skills：`<CODEX_HOME>/skills/<runtime_name>`
- agents：provider-native agents directory 或 config section
- MCP：`config.toml` / `config.json` 中 provider-native section
- hooks：provider-native config section
- config：`config.toml` / `config.json` structured patch
- instructions：profile-local instruction file 或 config reference

变化：

- 当前 Codex managed fallback 继续是 host-managed。
- MCP / skills 都改走同一套 profile manifest 与 lock。
- TOML 写入必须使用结构化 parser。

### 10.3 Cursor

Effective profile:

- shared canonical：`~/.cursor`
- env：`CURSOR_HOME`

Resource mapping 第一版：

- skills：`<CURSOR_HOME>/skills/<runtime_name>`
- agents：provider-native agents directory 或 config section
- MCP：provider-native JSON config
- hooks：provider-native config section
- config：provider-native JSON config patch
- instructions：profile-local instruction file 或 prompt-level injection

变化：

- 删除 Cursor 自己的一套 stale prune 分支。
- 与 Codex / Claude 共用 manifest、lock、conflict、snapshot。

## 11. Phase 1 实施计划

### P1.1 API 与类型重塑

- 保持现有 profile API：`WithNativeProfile` / `WithDedicatedProfile` / `WithCloneProfile` / `WithCloneProfileFrom`。
- 增加 `ProfilePayload`、`AgentPayload`、`HookPayload`、`ProfileConfigPayload`。
- 增加 `SessionParamProfileFingerprint`。
- 增加 `ProfileSnapshot` / `ResourceSnapshot`。
- 明确 per-run resource override 的 replace semantics。

验收：

- 旧 `ProfileModeNative/Dedicated` 不再是 adapter 分支依据。
- adapter 行为不再直接分支 `ProfileSelection.Mode`，而是基于内部 `ProfileKind`。

### P1.2 内部 profile 基础设施

- 抽 `internal/profilekind`。
- 实现 profile-local lock。
- 实现 manifest read/write。
- 实现 atomic JSON/TOML writer。
- 实现 directory entry reconciler。
- 实现 JSON object section reconciler。
- 实现 TOML section reconciler。

验收：

- 并发 reconcile 同一个 profile 不破坏 manifest。
- crash / error 不留下半写 config。
- external conflict hard fail。

### P1.3 Runner 与 Adapter 合同

- `resolveInvocation` 产出 `ProfilePayload`。
- `DriverRunRequest` 增加 `ProfilePayload`，或把 existing `Skills/MCP/Instructions` 收进 `ProfilePayload` 后删除散字段。
- built-in adapters 在 `Run` 内统一执行 profile reconcile。
- destructive `InjectSkills` 逻辑移除；SDK 保留每次 run 前调用一次的非破坏性 hook contract。

验收：

- `Run` 与 `Start().Wait()` 仍走同一套执行语义。
- profile materialization 失败发生在 CLI 启动前。
- session reject 不会先修改 profile。

### P1.4 Resource 落地

按顺序实现：

1. MCP：迁移现有 `internal/mcpruntime` 到 profile resource reconciler。
2. Skills：迁移 Claude / Codex / Cursor 到统一 directory reconciler。
3. Config：支持 Claude JSON、Codex TOML/JSON、Cursor JSON 的 structured patch。
4. Agents：支持 provider-native directory/config layout。
5. Hooks：支持 provider-native config layout。
6. Instructions：统一计入 `ProfileFingerprint`，能落盘则落盘，不能落盘则作为 adapter injection resource 处理。

验收：

- shared profile 和 host-managed profile 都能同步上述 resource kinds。
- 三家 adapter conformance 用同一套测试矩阵。

### P1.5 Admin 控制面

- 增加 `ProfileSnapshot`。
- 增加 `SyncProfile`。
- `ListSkills` 可保留，但内部应复用 `ProfileSnapshot` 的 skills resource 状态。

验收：

- 本地 profile 管理器不需要伪造 run 即可同步 shared profile。
- `AdminAPI` 不出现 `Run/Start`。

### P1.6 测试

新增 conformance：

- profile kind classification。
- shared profile full materialization。
- host-managed profile full materialization。
- conflict fail。
- takeover policy。
- manifest prune。
- broken symlink repair。
- JSON/TOML structured update。
- session profile fingerprint reject。
- `Run` / `Start().Wait()` profile state 一致。
- Admin `SyncProfile` 不产生 run。

## 12. Phase 2 安全治理

第二期不改变第一期核心语义，只增强 shared profile 管理体验和安全性。

内容：

- resource-level authorization，例如只允许 hooks，不允许 config。
- dry-run plan。
- human-readable diff。
- backup before apply。
- rollback by manifest version。
- audit log。
- hooks 命令路径、参数、触发事件展示。
- takeover 前的 explicit approval API。
- profile health check / drift detection。

第二期可以新增：

```go
type ProfileSyncOption func(*ProfileSyncOptions)

func WithProfileDryRun() ProfileSyncOption
func WithProfileBackup() ProfileSyncOption
func WithProfileAllowedResources(...ProfileResourceKind) ProfileSyncOption
func WithProfileTakeoverApproval(handler ProfileTakeoverHandler) ProfileSyncOption
```

这些不进入第一期，是为了先把底层语义和实现收敛；但第一期的 manifest / lock / conflict / parser 不能省。

## 13. 非目标

- 不内置 HTTP/gRPC server。
- 不内置 scheduler / queue / dispatcher。
- 不做自动 agent routing。
- 不把 task / workflow / tenant store 塞进 core SDK。
- 不让 Admin 控制面执行 model run。
- 不通过 CLI side-effect 命令作为主要 materialization 机制。
- 不为 Claude / Codex / Cursor 分别设计三套 ownership 规则。

## 14. 第一期开工顺序建议

1. 先改 profile API 和 `ProfilePayload`，让类型层表达正确语义。
2. 再抽 `internal/profilekind` 和 manifest/lock/atomic writer。
3. 迁移 MCP，因为当前 MCP 已经有 shared / dedicated-like 分类，是最接近目标的资源。
4. 迁移 skills，顺手删除 Claude prompt bundle 在 host-managed 下的特殊路径。
5. 增加 config structured patch。
6. 增加 agents/hooks resource。
7. 最后接 Admin `ProfileSnapshot` / `SyncProfile`。

不要先从 hooks 开刀。hooks 最敏感，但不是基础设施最难；先把 manifest/lock/parser/session guard 站稳，再让 hooks 复用同一套底座。

## 15. 依赖选型

本轮新增依赖只为第一期基础可靠性服务，不为第二期 diff / audit / backup / rollback 预埋库。

### 15.1 决策摘要

第一期 runtime 依赖拍板：

- `github.com/pelletier/go-toml/v2`：保留并作为 direct dependency；当前代码已在 `internal/mcpruntime` 使用，后续迁移到 `internal/profilereconcile` / `internal/profilelayout/codex`。
- `github.com/gofrs/flock`：新增 direct dependency；仅用于 `internal/profilestate` 的 profile-local exclusive lock。

第一期不引入：

- atomic write 第三方库：不新增。
- JSON patch / diff 库：不新增。
- YAML 库：不新增。
- fs watcher：不新增。
- Claude / Codex / Cursor provider SDK：不新增。

### 15.2 `github.com/pelletier/go-toml/v2`

用途：

- 读写 Codex `config.toml`。
- 物化 MCP / hooks / config 等 TOML section。
- 对已有 TOML 做结构化解析和编码，避免字符串拼接。

可靠性：

- Codex profile materialization 会更新嵌套 TOML section；手写字符串拼接容易破坏数组、表、转义和已有字段。
- `go-toml/v2` 支持 TOML v1.0.0，提供 `Marshal` / `Unmarshal` / `Encoder` / `Decoder`，并提供带位置的 decode error，适合把配置错误反馈给宿主。
- 当前仓库已经在 `internal/mcpruntime/codex.go` 使用它；继续使用比再换一个 TOML 库更少漂移。

可持续维护：

- v2 模块有明确 semver 说明，README 明确未标注 unstable 的 API 遵循 SemVer。
- 当前版本 `v2.3.0` 已在本仓库 `go.sum` 中存在，发布时间为 2026-03-24。
- 与 `github.com/BurntSushi/toml` 相比，两者都可用；本仓库已引入 `go-toml/v2`，继续使用可减少迁移和双 TOML 栈维护成本。

可局部化：

- 只允许由 `internal/profilereconcile` / `internal/profilelayout/codex` 这类 profile 写入包 import。
- 不暴露到 core 公共 API。
- 不允许 adapter 外的业务类型依赖 TOML library 类型。

结论：

- 采用。
- `go.mod` 中应从 indirect 调整为 direct require。

### 15.3 `github.com/gofrs/flock`

用途：

- 对 `<profile>/.agent-adaptor/lock` 做 profile-local exclusive lock。
- 串行化同一 profile 下的 manifest、directory resource、JSON/TOML config 写入。

可靠性：

- profile materialization 可能由多个 SDK 实例、宿主进程、Admin sync、run 前 reconcile 并发触发；只用进程内 mutex 不够。
- Go 标准库没有跨平台 file lock API。手写 Unix `flock` / Windows `LockFileEx` 会把平台细节带进 SDK。
- `gofrs/flock` 提供 `Lock` / `TryLock` / `TryLockContext` / `Unlock`，并有 Unix 和 Windows 实现。

可持续维护：

- `gofrs/flock` 由 Gofrs 维护，项目历史清楚，release 到 `v0.13.0`，发布时间为 2025-10-09。
- 依赖面小，主要引入 `golang.org/x/sys`；这是 Go 生态处理平台 syscall 的主流依赖。
- 它仍是 `v0`，因此 SDK 内部必须做一层极薄 wrapper，避免未来 API 变化扩散。

可局部化：

- 只允许 `internal/profilestate` import。
- 对外暴露 SDK 自己的 lock error，例如 `ErrProfileLocked` / `ErrProfileLockTimeout`，不暴露 `flock` 类型。
- 只使用 exclusive lock，不使用 shared lock，避免跨平台 shared/exclusive 升降级语义差异。

结论：

- 采用。
- 建议版本：`v0.13.0`。

### 15.4 Atomic Writer 不新增依赖

用途：

- manifest、JSON、TOML、provider config 写入都必须避免半写文件。

候选调研：

- `github.com/google/renameio/v2`：POSIX 侧很强，明确关注 atomic create/replace、同文件系统 temp file、fsync 等细节；但 README 明确 Windows 上无法提供正确实现且不导出函数，不适合作为 SDK 默认 runtime 依赖。
- `github.com/natefinch/atomic`：支持 Windows `MoveFileEx`，但 release 停在 2021 年，API 很小但维护活跃度弱。
- `github.com/creachadair/atomicfile`：API 清晰，发布时间新，但仍是 pre-v1，文档只承诺在 rename 本身 atomic 时成立，跨平台替换语义不够强。

决策：

- 不新增 atomic write 第三方库。
- 在 `internal/profilestate` 写最小 atomic writer：
  - temp file 必须建在目标文件同目录。
  - 写入后 `Sync` best effort。
  - `Close` 后再 replace。
  - POSIX 使用 `os.Rename`。
  - Windows 若要声明强替换语义，使用 build-tag 封装 `MoveFileEx(MOVEFILE_REPLACE_EXISTING|MOVEFILE_WRITE_THROUGH)`；该实现仍留在 `internal/profilestate`，不暴露公共 API。
  - 所有平台都要有 crash/error cleanup 测试。

理由：

- 这块逻辑很小，且候选库没有同时满足跨平台、维护活跃、API 稳定三项。
- 手写实现必须局部化并以测试兜底；这属于 AGENTS §2.4 中“三条与手写方案接近时倾向手写”的情况，不是零依赖洁癖。

### 15.5 标准库足够的部分

以下部分第一期用标准库：

- manifest：`encoding/json`。
- Claude / Cursor JSON config：`encoding/json`。
- directory resource reconcile：`os` / `io/fs` / `path/filepath`。
- fingerprint：现有 stable hash helper 或 `crypto/sha256`。
- backup / rollback：第二期再设计，不提前引入 archive / diff 依赖。

### 15.6 不引入 Provider SDK

第一期 profile materialization 不引入 Claude / Codex / Cursor provider SDK：

- 本 workstream 的目标是写 provider-native profile 文件，不是调用 provider API。
- CLI protocol 解析仍属于各 adapter 职责，不能因为 profile 写入再引入一层 provider SDK。
- Codex app-server 已有 `sourcegraph/jsonrpc2`，与本轮 profile resource 写入无关。

### 15.7 实施约束

- 每个新增 direct dependency 必须在 `go.mod` 中保持 direct require，不允许因为间接依赖存在就留作 `// indirect`。
- `go mod tidy` 后若 `github.com/gofrs/flock` 带来新的 indirect 依赖，只能由 `internal/profilestate` 的 direct import 触发。
- 若后续第二期需要 diff / audit 依赖，必须另开依赖选型，不沿用本节结论。

### 15.8 调研来源

- [`github.com/pelletier/go-toml/v2`](https://pkg.go.dev/github.com/pelletier/go-toml/v2) 与 [项目 README](https://github.com/pelletier/go-toml)。
- [`github.com/gofrs/flock`](https://pkg.go.dev/github.com/gofrs/flock) 与 [项目 README](https://github.com/gofrs/flock)。
- [`github.com/google/renameio/v2`](https://pkg.go.dev/github.com/google/renameio/v2) 与 [项目 README](https://github.com/google/renameio)。
- [`github.com/natefinch/atomic`](https://github.com/natefinch/atomic)。
- [`github.com/creachadair/atomicfile`](https://pkg.go.dev/github.com/creachadair/atomicfile)。
- [`os.Rename`](https://pkg.go.dev/os#Rename)。
