# Workstream: Skills via Effective Profile Materialization

> 状态：核心路径已实施。本文记录 v0.5.0 中 built-in adapters 的 skills 注入语义如何收敛到 effective profile materialization。更完整的 profile-wide 改进计划见 [`workstream-effective-profile-materialization-plan.md`](./workstream-effective-profile-materialization-plan.md)。当前公共 API 语义仍以 [`skill-api-design.md`](./skill-api-design.md) 和 [`usage-guide.md`](./usage-guide.md) 为准。

## 1. 问题

`agent-adaptor` v0.5 已经把 skills 的宿主模型收敛为：

- 宿主通过 `SkillProvider` 提供 key 到 `Skill` 的解析。
- binding 通过 `WithDefaultSkills(...)` 声明默认 selected skills。
- 单次调用通过 `WithSkills(...)` 追加临时 selected skills。
- SDK 在每次 run 前解析、去重、materialize，adapter 只消费最终 `ResolvedSkills`。

这个模型对 host 很自然，尤其适合 `agent_task_runtime` 这类 task-scoped runtime：

- 一个 task/role 创建一个隔离 profile。
- task spec 中的 skills 是该 task/role 的默认能力。
- SDK/binding 初始化时用 `WithDefaultSkills(agentadaptor.Key(...))` 声明这些默认能力。
- `Run` 时只传 prompt、session、policy、metadata；不需要每次用 `WithSkills(...)` 重复声明 task skills。

当前差异点不是单纯的 Claude bug，而是 built-in adapters 的 profile skills 语义没有统一：

- Claude 的旧实现总是为 selected skills 构造 per-run prompt bundle，并通过 `--add-dir <bundle_root>` 注入；当前实现改为写入 effective `<CLAUDE_CONFIG_DIR>/skills`，prompt bundle 只保留为 legacy guard 兼容。
- Codex 会在 `Run` 中围绕 effective `CODEX_HOME/skills` 注入，但现有规则只做部分 stale 清理，缺少 manifest ownership。
- Cursor 会在 `Run` / `SetSelectedSkills` 中围绕 effective `CURSOR_HOME/skills` 同步，但 pruning / conflict / ownership 判断与 Codex 不完全一致。
- Admin `ListSkills` 的 snapshot 语义、run checkpoint guard、shared profile 下的保守策略没有形成一套跨 adapter 的规则。

结果是 host 无法用同一套心智回答三个问题：

1. 这个 task/role 当前 selected skills 最终在哪里可见？
2. 这个 profile tree 里哪些 skill 是 SDK 管的，哪些是用户或外部系统放进去的？
3. session resume 时 selected skills 变化是否会被可靠地拒绝或触发 fresh session？

一句话：问题不是“Claude 没把 skills 写进 dedicated profile”，而是 “skills materialization 没有按 effective profile 建立跨 agent 的统一合同”。

## 2. 负责人裁决

本 workstream 采纳以下统一语义：

- SDK 仍然只负责解析高层声明：binding defaults、per-run overrides、provider required skills 合并为一个 `ResolvedSkills`。
- Adapter 负责把 `ResolvedSkills` 按 provider-native 方式物化到本次 invocation 的 effective profile 或 provider-native 临时注入位置。
- 所有 built-in adapters 都必须用同一套 effective profile 分类规则，而不是按 `ProfileSelection.Mode` 或 adapter-specific 猜测分支。
- Host-owned effective profile 下，selected skills 是 profile-local 可观察状态，adapter 应执行完整 managed reconcile。
- Native shared profile 下，adapter 默认采取保守策略：不 prune 用户已有 skills，不覆盖 external entries；如果 provider 支持临时注入，优先用临时注入避免污染用户 profile。Shared profile 不是只读禁区；宿主显式声明“我要管理这个本地 profile”时，SDK 应提供可审计的 shared-profile 管理能力，而不是逼宿主绕过 adapter 私改文件。
- Skills fingerprint 会进入统一 `ProfilePayload.Fingerprint`；所有 built-in adapters 以 `SessionParamProfileFingerprint` 作为 session guard，profile-visible resources 变化默认不应继续复用同一个 provider session。

这仍然不是第二条执行入口。`Run` / `Start` 的内部流程保持不变：解析 invocation、协调 session、adapter 执行、checkpoint 持久化与结果归档。

## 3. 职责边界

`agent-adaptor` 负责：

- 统一 `Run/Start` 执行语义。
- 合并 binding defaults 与 per-run overrides。
- 通过 `SkillProvider` 和 `SkillMaterializer` 得到最终 `ResolvedSkills`。
- 解析 built-in adapter 的 effective profile，并把高层声明物化到 CLI 能消费的位置。
- 在 `ListSkills` / `SetSelectedSkills` / run checkpoint 中报告可观察状态。
- 为 built-in adapters 提供统一 managed skills reconciler，避免 Claude / Codex / Cursor 各写一套 ownership 规则。

host 负责：

- 决定什么时候创建 SDK/binding。
- 决定使用 native、dedicated、clone，或通过 env 指向某个 profile。
- 提供 `SkillProvider`、`SkillMaterializer`、`SessionStore`。
- 持久化自己的业务配置，例如 task spec、role profile dir、用户勾选的 skill keys。

host 不应该负责：

- import `agent-adaptor/internal/skillruntime`。
- 复刻 built-in adapter 的 runtime name、profile env precedence、managed symlink 规则。
- 为 Claude/Codex/Cursor 各自写一套 skills home 注入逻辑。
- 为了修正 profile skills 可见性而篡改 `DriverRunRequest.Skills` 或绕过 adapter session guard。

这个边界与 MCP profile materialization 一致：宿主声明高层资源，adapter 按当前 effective profile 物化到 provider-native 位置。

## 4. 推荐 Host 模式

`agent_task_runtime` 这类 task runner 应把 task/role 视为 binding/profile 维度，而不是把 task skills 当成每次 run 的临时 override。

推荐装配：

```go
skillRefs := []agentadaptor.SkillRef{
	agentadaptor.Key("skill_202604271306486521000002"),
	agentadaptor.Key("skill_202604271307406521000003"),
}

binding := claude.New(
	agentadaptor.ClaudeConfig{
		Model: "claude-sonnet-4",
	},
	agentadaptor.WithDedicatedProfile("/cic/workspace/tasks/task-123/config/implementer"),
	agentadaptor.WithDefaultIdentity(agentadaptor.AgentIdentity{
		ID:       "task-123",
		TenantID: "team-456",
		Name:     "implementer",
	}),
	agentadaptor.WithDefaultSkills(skillRefs...),
)

sdk := agentadaptor.New(
	agentadaptor.WithDefaultAgent(binding),
	agentadaptor.WithSkillProvider(taskSkillProvider),
	agentadaptor.WithSkillMaterializer(taskSkillMaterializer),
	agentadaptor.WithSessionStore(taskSessionStore),
)

_, err := sdk.Run(ctx, prompt,
	agentadaptor.WithSessionKey("task", "task-123"),
	agentadaptor.WithMetadata("task_id", "task-123"),
)
```

原则：

- `WithDefaultSkills` 表达“这个 task/role 默认带哪些 skills”。
- `WithSkills` 只表达“这一次 run 额外追加哪些 skills”。
- required skills 仍由 `SkillProvider` 返回并由 SDK 自动纳入 selected 集合。
- adapter 不应关心 skill 来源是 default、run 还是 required；它只消费最终 `ResolvedSkills`。

## 5. Effective Profile 分类

本 workstream 不再使用“是否显式传了 `WithDedicatedProfile`”作为行为分支条件。判断标准必须是本次 invocation 的 effective profile 最终写到哪里。

沿用 MCP workstream 的两类 profile：

- `shared`：原生用户共享 profile。
- `host-managed`：宿主或 adapter 可安全管理的隔离 profile。

三家的 canonical shared profile 固定为：

| Adapter | Canonical shared profile | Skills home |
|---|---|---|
| Claude | `~/.claude` | `<CLAUDE_CONFIG_DIR>/skills` |
| Codex | `~/.codex` | `<CODEX_HOME>/skills` |
| Cursor | `~/.cursor` | `<CURSOR_HOME>/skills` |

统一分类规则：

1. Adapter 先按既有优先级解析 effective profile：`CommonConfig.Env`、profile option、process env、adapter default / managed fallback。
2. 如果 `AgentProfile.Managed == true`，判为 `host-managed`。
3. 否则计算 adapter 的 canonical shared profile。
4. 如果 effective profile 的真实路径等于 canonical shared profile，判为 `shared`。
5. 其它全部判为 `host-managed`。

路径比较要求：

- `filepath.Abs`
- `filepath.Clean`
- 路径存在时尽量 `EvalSymlinks`
- Windows 平台按不区分大小写比较

不要使用下面这些规则：

- 不要只看 `ProfileSelection.Mode`。
- 不要把 process env 自动等价成 shared profile。
- 不要通过路径字符串里是否包含 `.claude` / `.codex` / `.cursor` 猜测。
- 不要让 skills workstream 自己再实现一份与 MCP 不同的分类 helper。

建议把当前 `internal/mcpruntime.ClassifyProfile` 抽到更中性的内部包，例如 `internal/profilekind` 或 `internal/adapterprofile`，由 MCP 与 skills 共同复用。

## 6. 目标行为

### 6.1 Host-Managed Effective Profile

当 effective profile 判为 `host-managed` 时，built-in adapter 应把最终 selected skills 物化到该 profile 的原生 skills home。

| Adapter | Host-managed skills home | 目标行为 |
|---|---|---|
| Claude | `<CLAUDE_CONFIG_DIR>/skills` | 完整 managed reconcile；运行时不再为同一批 skills 构造 prompt bundle |
| Codex | `<CODEX_HOME>/skills` | 完整 managed reconcile |
| Cursor | `<CURSOR_HOME>/skills` | 完整 managed reconcile |

Host-managed profile 下的完整 managed reconcile 含义：

- selected runtime names 必须与 `ResolvedSkills` 对齐。
- adapter 可删除上一次 manifest 中记录、但本次不再 selected 的 managed entries。
- adapter 必须保留 manifest 外的 external files。
- selected runtime name 被 external file/symlink/directory 占用时，必须在 CLI 启动前返回冲突错误。
- managed symlink 断链时应删除并重建。
- materialization 失败属于启动前失败，不能让 CLI 带着半配置状态启动。

### 6.2 Native Shared Profile

当 effective profile 判为 `shared` 时，built-in adapter 第一期同样支持完整 materialization。`WithNativeProfile()` 表达宿主复用 provider 原生共享 profile；它不等价于只读，也不禁止 SDK 管理宿主声明的 resources。

Shared profile 第一期必须满足：

- 使用 managed namespace、manifest 或 provider-native marker 记录 ownership。
- 持有 profile-local lock，所有 manifest / config 写入要原子化。
- selected runtime name 被 external entry 占用时返回冲突错误。
- 默认 hard fail on external conflict；只有宿主显式声明 takeover / replace policy 时才允许接管。
- manifest 不记录 secret、token、auth material。
- 可以 prune manifest 中由 SDK 管理、但本次 desired state 不再包含的 entries。

以下治理能力放到第二期：

- 按资源维度授权，例如 skills / MCP / agents / hooks / config 分开允许。
- dry-run / diff。
- 审计记录。
- backup / rollback。
- hooks 执行路径、参数、触发条件展示。

当前 agent-specific 传输选择：

| Adapter | Shared profile 策略 |
|---|---|
| Claude | 写入 managed skills / config / hooks；不再因为 shared profile 自动退回 prompt bundle |
| Codex | 使用统一 reconciler 写入 shared `CODEX_HOME` |
| Cursor | 使用统一 reconciler 写入 shared `CURSOR_HOME` |

这里允许 provider-native 注入方式不同，但 ownership、conflict、prune、session guard 语义必须一致。

Hooks 比 skills / MCP 更敏感，因为它会执行命令。SDK 不应禁止 shared profile hook 管理；第一期先提供 shared / host-managed 两种内部 profile kind 的完整 materialization 能力，第二期再补齐 diff、执行路径展示、审计和回滚。

## 7. 设计方案

### 7.1 公共 Managed Skills Reconciler

在 `internal/skillruntime` 增加统一 helper，供 Claude / Codex / Cursor 复用：

```go
type ManagedSkillsOptions struct {
	DriverType   string
	SkillsHome   string
	Payload      agentadaptor.ResolvedSkills
	Selected     []string
	ManagedRoots []string

	ManifestPath string
	LockPath     string
	AllowPrune   bool

	ConflictPolicy ConflictPolicy
	Sink           agentadaptor.EventSink
}
```

helper 负责：

- 为 selected `ResolvedSkill.RuntimeName` 创建或更新 symlink。
- symlink target 指向 `ResolvedSkill.SourcePath`。
- 在 symlink 不可用的平台 fallback 到 copy，并写入 `.agent-adaptor-source-path` marker。
- 写入 managed manifest，例如 `.agent-adaptor-managed-skills.json`。
- manifest 写入必须使用临时文件 + rename，避免 crash 后留下半截 JSON。
- reconcile 必须持有 profile-local lock，避免同一个 skills home 上并发 run 互相 prune。
- 只删除 manifest 中记录、且本次不再 allowed 的 managed entries。
- 保留 manifest 之外的 external files。
- selected runtime name 被 external file/symlink/directory 占用时返回冲突错误。
- managed symlink 断链时删除并重建。
- `AllowPrune=false` 时不得删除任何 non-selected entry；只能修复或更新本次 selected managed entries。

manifest 只记录 adapter 管理的 entries，不记录 archive URL、token、auth、provider-specific secret。

建议 manifest 记录：

- schema version
- driver type
- skills home
- runtime name
- skill key
- source path
- payload fingerprint 或 entry fingerprint
- last reconciled timestamp

首次接管旧目录时：

- 如果已存在 entry 与本次 selected `SourcePath` 精确匹配，可把它纳入 manifest。
- 如果 entry 指向 SDK managed cache root 或带 `.agent-adaptor-source-path` marker，可按 managed entry 处理。
- 其它情况一律视为 external；若 runtime name 与 selected 冲突则 hard fail。

### 7.2 Adapter Run 时序

`SkillAwareDriver.InjectSkills` 仍保留为可选 hook，但不应承担 destructive reconcile。

原因：SDK 当前在 session 协调之前调用 `InjectSkills`。如果 hook 里已经 prune profile，随后 session reject / busy / unsupported policy 失败，profile 就被错误修改了。

统一时序应放在 adapter `Run` 内：

1. 解析 adapter config 与 effective profile。
2. 计算 profile kind：`shared` 或 `host-managed`。
3. 计算本次 profile guard：`req.ProfilePayload.Fingerprint`。
4. 如果请求携带 session state，先校验 cwd / workspace / profile fingerprint 等 resume guard。
5. 按 profile kind reconcile skills。
6. 同步 MCP / instructions / runtime env 等其它 profile-local 配置。
7. 启动 CLI。
8. checkpoint 中写入 cwd / workspace / profile fingerprint。

`InjectSkills` 可以继续用于非破坏性预热或 no-op；built-in adapters 不应在这里 prune profile。

### 7.3 Agent-Specific 落地

#### Claude

Claude adapter 的旧 prompt-bundle 逻辑是：

- `InjectSkills` no-op。
- `Run` 内调用 `prepareClaudePromptBundle(req.Agent, req.Skills)`。
- `buildClaudeExecArgs` 在有 bundle root 时追加 `--add-dir`。
- checkpoint 使用 prompt bundle key 做 resume guard。

调整为：

1. `Run` 先解析 effective `CLAUDE_CONFIG_DIR` 与 profile kind。
2. 若 profile kind 为 `shared` 或 `host-managed`：
   - 使用统一 reconciler 写入 `<CLAUDE_CONFIG_DIR>/skills`。
   - 不为本次 `req.Skills` 构造 prompt bundle。
   - `buildClaudeExecArgs` 不追加本次 skills 对应的 `--add-dir <claude-prompt-cache/...>`。
3. per-run prompt bundle 只作为历史实现说明或未来显式临时注入模式，不再由 shared profile 自动触发。
4. 新 checkpoint 写 `SessionParamProfileFingerprint`；旧 checkpoint 只有 `prompt_bundle_key` 时继续按 legacy guard 拒绝不兼容 resume。

#### Codex

Codex 当前已经在 `Run` 内对 effective `CODEX_HOME/skills` 注入。

调整为：

1. 复用统一 profile kind helper。
2. profile kind 为 `host-managed` 时，使用统一 reconciler，`AllowPrune=true`。
3. profile kind 为 `shared` 时，使用统一 reconciler，`AllowPrune=false`。
4. selected runtime name 与 external entry 冲突时 hard fail。
5. checkpoint 写入 `SessionParamProfileFingerprint`，resume 时 profile fingerprint 变化则拒绝。

Codex 的 managed fallback `CODEX_HOME` 仍属于 `host-managed`，因为 `AgentProfile.Managed=true`。

#### Cursor

Cursor 当前已经在 `Run` / `SetSelectedSkills` 中对 effective `CURSOR_HOME/skills` 同步。

调整为：

1. 复用统一 profile kind helper。
2. profile kind 为 `host-managed` 时，使用统一 reconciler，`AllowPrune=true`。
3. profile kind 为 `shared` 时，使用统一 reconciler，`AllowPrune=false`。
4. 删除现有与 Codex 不一致的 stale 判断分支，统一交给 manifest ownership。
5. checkpoint 写入 `SessionParamProfileFingerprint`，resume 时 profile fingerprint 变化则拒绝。

### 7.4 Session Guard

新增公开常量：

```go
const SessionParamProfileFingerprint = "profile_fingerprint"
```

语义：

- 所有 built-in adapters 在 checkpoint 中写入本次 `req.ProfilePayload.Fingerprint`。
- resume 时，如果旧 session state 中存在 `profile_fingerprint` 且与本次不同，adapter 返回 `ErrResumeRejected`。
- `continue_or_start` 模式下，runner 可按既有逻辑 fresh start；`continue_only` 模式下向 host 暴露拒绝。
- Claude 迁移期同时识别旧 `prompt_bundle_key`。
- 新 session 不再把 `prompt_bundle_key` 作为主要 guard 字段。

这不是 SDK-level session compatibility fingerprint。宿主仍然通过 `WithSessionKey` / `WithNewSession` / `WithForkSession` 决定 session 策略；adapter 只负责拒绝对 provider session 不安全的 resume。

### 7.5 Admin Snapshot

`ListSkills` / `SetSelectedSkills` 必须报告 profile kind 下的真实状态：

- `Selected`：SDK 传入的最终 selected keys。
- `Resolved`：SDK 传入的完整 merged catalogue，不能被 adapter 静默丢弃。
- `Entries`：adapter 观察到的 installed / configured / missing / external / conflict 状态。
- `Fingerprint`：`ResolvedSkills.Fingerprint`。

Host-managed profile 下：

- selected managed entries 已写入 profile 后，应报告 `installed`。
- manifest 中存在但本次不再 selected 的 managed entries，在 reconcile 后应消失。
- unmanaged entries 报告为 `external`。
- selected runtime name 被 external 占用时，`SetSelectedSkills` / `Run` 返回错误；`ListSkills` 可报告 conflict detail。

Shared profile 下：

- Claude selected skills 可报告为 `installed` 或 `configured`，detail 说明来自 profile-local managed skills home。
- Codex / Cursor selected managed entries 如果已在 shared profile 中存在，可报告 `installed`。
- 不应把 shared profile 中未 selected 的 external entries 当成 stale managed entries。

`DriverDescriptor.Skills.Mode` 是静态能力提示，不能完整表达 profile-kind-dependent 行为。宿主 UI 应以 `SkillSnapshot.Mode` 和 `SkillSnapshot.Entries` 为准。

## 8. API 影响

首选方案不新增 host 必须调用的新 API。

沿用现有公开合同：

- `WithDedicatedProfile(dir)`
- `WithNativeProfile()`
- `WithCloneProfile(...)`
- `WithDefaultSkills(refs...)`
- `WithSkills(refs...)`
- `WithSkillProvider(provider)`
- `WithSkillMaterializer(materializer)`
- `SkillAwareDriver.ListSkills`
- `SkillAwareDriver.SyncSkills`
- `ResolvedSkills.Fingerprint`

新增公开常量：

```go
const SessionParamProfileFingerprint = "profile_fingerprint"
```

不新增：

- `RunWithSkillsProfile`
- `PrepareProfileSkills`
- host-facing manifest API
- profile ownership store
- task / workflow / tenant 概念

## 9. 验收场景

### 9.1 Cross-Agent Host-Managed Profile

Given host 使用 `SkillProvider + WithDefaultSkills + WithDedicatedProfile` 构造任意 built-in binding，When run 启动，Then：

- SDK 调用 `SkillProvider.GetSkills` 并 materialize selected skills。
- adapter 解析本次 effective profile。
- profile kind 被判为 `host-managed`。
- `<skills_home>/<runtime_name>` 指向 materialized source path，或在 symlink 不可用时是带 marker 的 copy。
- managed symlink / marker ownership 足以证明 SDK 管理的 entries；无法证明 ownership 的 entry 仍报告为 external。
- checkpoint 记录 `profile_fingerprint`。
- resume 时 profile fingerprint 变化会被拒绝。

### 9.2 Claude Host-Managed Profile

Given host 使用 Claude host-managed profile，When run 携带 selected skills，Then：

- `<CLAUDE_CONFIG_DIR>/skills/<runtime_name>` 可见。
- Claude CLI argv 不包含本次 skills 对应的 `--add-dir <claude-prompt-cache/...>`。
- session checkpoint 写 `profile_fingerprint`。

### 9.3 Shared Profile Materialization

Given host 使用 native shared profile，When run 携带 selected skills，Then：

- adapter 写入 SDK managed skills。
- adapter 可以 prune manifest 中由 SDK 管理、但本次 desired state 不再包含的 skills。
- selected runtime name 与 external entry 冲突时启动前失败。
- Claude 不因为 shared profile 自动退回 prompt bundle + `--add-dir`。
- Codex / Cursor 使用同一套 managed manifest / lock / conflict 规则。

### 9.4 Stale Managed Prune

Given host-managed 或 shared profile 中 manifest 记录了旧 managed skill，When 新 run 的 selected skills 不再包含它，Then：

- 旧 managed entry 被删除。
- manifest 外的 unmanaged entries 保留。
- 如果同名 external entry 存在，adapter 返回冲突错误，不静默 shadow。

### 9.5 Broken Symlink Repair

Given manifest-managed symlink 断链，When run 启动且该 skill 仍 selected，Then：

- adapter 删除断链并重建。
- manifest 被原子更新。
- CLI 只在 reconcile 成功后启动。

### 9.6 Concurrency

Given 同一个 skills home 上有两个并发 run，When 两次 run 同时 reconcile，Then：

- profile-local lock 保证 manifest 与 symlink 状态不会交叉写坏。
- `AllowPrune=true` 时，每次 prune 基于锁内读取的最新 manifest。
- 任意失败不会留下半写 manifest。

### 9.7 Admin

Given `Admin.ListSkills` / `Admin.SetSelectedSkills` 被调用，Then：

- snapshot `Resolved` 包含 SDK 传入的完整 merged catalogue。
- host-managed / shared profile 下 installed / external / conflict 状态与磁盘一致。
- shared profile 下不会把用户已有 skills 标成 stale managed。
- `SetSelectedSkills` 仍然只是进程内 selected-key override，不变成跨进程持久化能力。

### 9.8 Adaptertest

adapter conformance suite 应新增 shared / host-managed profile skills materialization case：

- Claude / Codex / Cursor 都必须通过。
- 覆盖 managed ownership、conflict、stale prune、broken symlink repair。
- 覆盖 session guard 中 `profile_fingerprint` 的 codec 行为。

## 10. 非目标

- 不把 task、workflow、role、tenant store 等 host 业务概念加入 core SDK。
- 不新增第二执行入口。
- 不要求 host 直接管理 adapter skills home。
- 不改变 `WithSkills` 的追加语义。
- 不让 `Admin.SetSelectedSkills` 变成跨进程持久化能力。
- 不清理 manifest 无法证明属于 SDK 管理的用户已有 skills。
- 不强制三家 adapter 使用同一种 CLI 参数；只强制同一套 ownership / profile kind / session guard 语义。

## 11. 迁移与兼容

兼容策略：

- Claude shared / host-managed profile 都切到 profile-local skills home；prompt bundle 不再作为 shared profile 默认策略。
- Codex / Cursor 切到统一 managed reconciler 后，external conflict 可能从“skipped 后继续启动”变成启动前错误；这是可靠性修正，应在 release notes 中列为 breaking-adjacent behavior change。
- 老 Claude session 如果 checkpoint 里只有 `prompt_bundle_key`，继续按旧规则处理。
- 新 session 统一写 `profile_fingerprint`。
- 首次引入 manifest 时，adapter 可安全接管与本次 selected source 精确匹配的旧 managed links；无法证明 ownership 的 entry 保持 external。

文档更新：

- `skill-api-design.md`：补充 effective profile 下 adapter 应如何物化 selected skills。
- `usage-guide.md`：加入 task-scoped runtime 推荐模式：`SkillProvider + WithDefaultSkills + host-managed profile`。
- `workstream-profile-user-experience.md`：更新 Claude / Codex / Cursor skills 可见性。
- `api-reference.md`：说明 `WithDefaultSkills` 适合绑定级默认能力，`WithSkills` 适合 per-run 追加能力。
- `session_codec.go` 文档：新增 `SessionParamProfileFingerprint`，说明 `SessionParamPromptBundleKey` 的 legacy 地位。

## 12. 开放问题

- 统一 profile kind helper 的包名：`internal/profilekind`、`internal/adapterprofile`，还是继续放在 `internal/mcpruntime` 并接受命名不精确。
- `DriverDescriptor.Skills.Mode` 是否需要未来新增 `hybrid` / `profile_scoped`，还是继续让 UI 以 `SkillSnapshot.Mode` 为准。
- Claude CLI 对 `CLAUDE_CONFIG_DIR/skills` 与 `--add-dir` 同时存在时的 precedence 是否需要作为 smoke test 固化；SDK-managed skills 不应同时使用两者，但用户自定义 extra args 仍可能碰到。
