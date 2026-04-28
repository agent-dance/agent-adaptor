# Skill API Contract

本文档描述当前代码里的 skills 公共合同。历史重设计讨论已经落地；不要再按旧的 `SkillCatalog` / `SkillAssembler` 双 hook 心智实现新宿主。

## 1. 核心模型

`Skill` 是一个可被选择、物化、注入到 adapter 的能力描述：

```go
type Skill struct {
	Key      string
	Source   SkillSource
	Required bool
	Reason   string
	Metadata map[string]string
}
```

- `Key` 是 skill 身份。同一个 key 的多个候选必须结构一致，否则 SDK 返回 `ErrSkillKeyConflict`。
- `Source` 描述内容来源。内置来源包括 `SkillFromPath`、`SkillFromFS`、`SkillFromInline`、`SkillFromArchive`。
- `Required=true` 表示只要该 skill 出现在候选集里，就必须进入本次 run 的 selected 集合；per-run `WithSkills` 无法把它挤掉。
- `Metadata` 中 `_runtime_name` 和 `_display_name` 是 SDK/adapter 保留键，其它键由宿主自定义。

`Skill` 本身也是 `SkillRef`，所以可以直接传给 `WithDefaultSkills(...)` 或 `WithSkills(...)`。仅引用 store 里的 skill 时，用 `agentadaptor.Key("name")`。

## 2. Host Hooks

### `SkillProvider`

```go
type SkillProvider interface {
	GetSkills(ctx context.Context, keys []string) (map[string]Skill, error)
}
```

SDK 在每次 Run 前把本次显式引用到的 key 去重后传给 `GetSkills`。Provider 可以额外返回 required skills；SDK 会把 provider 返回的所有 skill 纳入 selected 集合。

租户 / profile / agent 身份通过 context 传递：

```go
id, _ := agentadaptor.CallerIdentityFromContext(ctx)
```

### `SkillCatalog`

```go
type SkillCatalog interface {
	SkillProvider
	Catalogue(ctx context.Context) ([]Skill, error)
}
```

`Catalogue` 只用于 Admin 路径。远程 store 无法枚举完整清单时，只实现 `SkillProvider` 即可；`Admin.ListSkills` 会诚实降级为 unsupported snapshot，而 run-time key resolution 仍然可用。

### `SkillSet`

`SkillSet` 是静态 map，实现 `SkillProvider` 和 `SkillCatalog`。小型 CLI、测试、内置 skill 集合优先用它：

```go
sdk := agentadaptor.New(
	agentadaptor.WithDefaultAgent(codex.New(
		agentadaptor.CodexConfig{Model: "gpt-5.4"},
		agentadaptor.WithDefaultSkills(agentadaptor.Key("write-proof")),
	)),
	agentadaptor.WithSkillSet(agentadaptor.SkillSet{
		"write-proof": agentadaptor.LocalSkill("./skills/write-proof"),
	}),
)
```

`WithSkillSet(set)` 等价于 `WithSkillProvider(set)`，只是表达更清楚。

## 3. Sources And Materialization

内置 source：

| Source | 用途 |
|---|---|
| `SkillFromPath{Path}` | 已存在的本地 skill 目录，目录内必须有 `SKILL.md`。 |
| `SkillFromFS{FS, Root}` | embed / 内存 fs 子树。 |
| `SkillFromInline{SkillMD}` | 只有一个 `SKILL.md` 字符串的一次性 skill。 |
| `SkillFromArchive{Archive, Format, Subpath, Fingerprint}` | zip / tar / tar.gz skill bundle。 |

便捷构造器：

- `LocalSkill(dir)`
- `FSSkill(fs, root)`
- `InlineSkill(key, skillMD)`
- `Require(skill, reason)`
- `ArchiveFromBytes`, `ArchiveFromPath`, `ArchiveFromURL`

`SkillFromFS`、`SkillFromInline`、`SkillFromArchive` 会在 run 前被 materializer 写成一个含 `SKILL.md` 的目录。selected skill 物化失败会让本次 run 在 adapter 启动前失败，`Run` 或 `Start().Wait()` 返回匹配 `ErrSkillMaterializationFailed` 的 `*SkillMaterializationError`。默认 materializer 使用 SDK cache root；宿主需要多租户隔离、审计卷或自定义缓存策略时，用：

```go
agentadaptor.WithSkillMaterializer(myMaterializer)
```

自定义 `SkillSource` 必须配套自定义 `SkillMaterializer`，因为 SDK 默认 materializer 只认识内置 source。

## 4. Merge Rules

最终 selected skill 集合来自：

1. binding 上的 `WithDefaultSkills(...)`
2. per-run `WithSkills(...)`
3. `SkillProvider.GetSkills` 返回的 required / injected skills

合并语义是并集，不是覆盖。per-run `WithSkills(...)` 只追加；如果宿主需要一个“更少 skill”的 agent，应该注册另一个 named binding，而不是期待 per-run option 做减法。

冲突规则：

- 同 key 且结构一致：去重。
- 同 key 但结构不同：返回 `ErrSkillKeyConflict`。
- bare key 未被 provider 解析到：返回 `ErrSkillNotFound`。
- selected skill 物化失败：返回 `ErrSkillMaterializationFailed`，可用 `errors.As` 取 `*SkillMaterializationError`。
- `Skill.Source == nil`：返回 `ErrSkillSourceMissing`。
- `Skill.Key == ""` 且无法从 map key / path 推导：返回 `ErrSkillKeyMissing`。

## 5. Admin Semantics

`AgentAdmin.ListSkills(ctx)` 返回 `SkillSnapshot`：

- `Selected`：当前绑定默认 / process-local override 选中的 key。
- `Resolved`：SDK 传给 adapter 的完整 merged catalogue。
- `Entries`：adapter 对每个 skill 的状态分类。
- `Fingerprint`：用于审计与变更检测。

`AgentAdmin.SetSelectedSkills(ctx, keys)` 是**进程内** selected-key override：

- 它替换该 agent 后续 run 的 binding `WithDefaultSkills` selection。
- Provider 返回的 `Required` skills 仍然会自动出现。
- 它不会写数据库、不会写宿主配置、不会跨进程持久化。

因此宿主若有用户勾选 UI，应把选择保存到自己的 store，并在下次构造 SDK / binding 时重新声明。

## 6. Adapter Contract

Adapter 不直接看 `SkillProvider`。SDK 解析、去重、物化后，把 `ResolvedSkills` 放进 `DriverRunRequest.Skills`：

```go
type ResolvedSkills struct {
	Mode        SkillSyncMode
	Entries     []ResolvedSkill
	Warnings    []string
	Fingerprint string
}
```

当 `ResolvedSkills` 传到 adapter 时，`Entries` 已包含所有 selected skills；缺失或损坏的 archive/path 不会被降级成 warning 继续执行。

`SkillAwareDriver` 的三个方法分别用于：

- `ListSkills`：Admin snapshot。
- `InjectSkills`：每次 Run 前注入 skill。
- `SyncSkills`：`SetSelectedSkills` 后让 adapter 更新自身可见状态。

`SyncSkills` 是 adapter SPI 名称；宿主公共 API 名称是 `SetSelectedSkills`。

## 7. Migration Notes

旧写法与当前写法：

| 旧概念 | 当前写法 |
|---|---|
| `WithSkillCatalog(catalog)` | `WithSkillProvider(provider)` 或 `WithSkillSet(set)` |
| `WithSkillAssembler(assembler)` | `WithSkillMaterializer(materializer)` |
| `SkillProvider.List(ctx, tenantID)` | `SkillProvider.GetSkills(ctx, keys)`，租户从 `CallerIdentityFromContext(ctx)` 取 |
| Admin `SyncSkills(desired)` | Admin `SetSelectedSkills(keys)` |
| `Desired` / `Requested` 术语 | `Selected` |

完整 v0.5 迁移背景见 [`v0.5.0-release-notes.md`](./v0.5.0-release-notes.md)。
