# Skill API 重设计（break change，评审稿）

本文档是 agent-adaptor 对 `Skill` 公共合同的**重设计提案**，允许对现有 API 做 break，不保留兼容层。

目标读者：SDK 维护者、预期的 3 类典型宿主（本地 CLI / 桌面应用 / 企业 SaaS）。

前置约束：

- 遵守 [`AGENTS.md`](../AGENTS.md) §2.1 的"只有一套执行语义"、§2.4 的"依赖局部化"和 §7 的"adapter 与 helper 职责边界"。
- 不改 `Run/Start/RunHandle` 合同，不改 HITL/Streaming/Session 合同。
- `SkillAwareDriver` 是 adapter-internal 合同，所有对宿主无用的结构都要从 SDK 公共 API 里沉下去。

## 1. 为什么要重做

当前 `Skill` 相关 API 是渐进长出来的：

- 宿主需要两个 hook（`SkillCatalog` + `SkillAssembler`），但只有前者是真的业务差异。
- `Skill` 值对象的来源三种（`PathHint` / `Content` / `Files`）互相覆盖，优先级写在 helper 里而不是类型里。
- `SkillPayload` 既在宿主面暴露又在 adapter 面暴露，字段语义混杂。
- `SkillCatalogInventory` 是 optional interface，Admin 的核心功能静默依赖它——最小化实现能跑通 `Run`，但 `ListSkills/SyncSkills/required-auto-injection` 都会不起作用，宿主踩坑无声。
- `WithSkills("key")` 只接受字符串引用；一次性/热加载 skill 要先反注册到 Catalog 再引用，绕路。
- per-run `WithSkills` **覆盖** binding `WithDefaultSkills`，迫使"必装"这件事要么由 caller 每次记得加、要么引入第二个并行 Option（`WithRequiredSkills`），两边都会把 policy 责任泄漏到错误的层。

这些问题单独看都小，合起来让"第一次用 agent-adaptor 的宿主"在 skill 这个子系统上花的时间远超它的业务权重。

本次重做的底线：**API 层至少砍掉一半表面积，且宿主场景表达力不退反升**，关键是让合并规则收敛到**一条**，让"policy vs selection"的困惑在 API 形状层面消失。

## 2. 宿主场景画像

下面这三类画像覆盖当前所有已知宿主需求（含 paperclip）。每一条都明确"它要什么、它愿意付出什么、它不能接受什么"。

### 2.1 画像 A：CLI 小工具 / 自动化脚本

谁：在某个 Go 写的 CLI 里想调用 codex / claude，顺手带上自己写的 `./skills/` 目录。

它要什么：

- 一行代码告诉 SDK"这个目录里的 SKILL.md 这次用上"。
- 不理解 tenant、不理解 required、不理解 SkillSnapshot。
- 最多维护 3–5 个 skill，全是磁盘目录。

它愿意付出什么：

- 一个 `WithSkills(...)` 参数，有默认的 `LocalSkill("./path")` helper。
- 不需要读 API doc 超过 30 秒。

它不能接受的：

- 必须实现一个接口（`SkillCatalog`）才能塞一个本地目录。
- 必须理解 `Runtime / PathHint / Content` 这些字段的含义。

### 2.2 画像 B：桌面应用 / IDE 插件 / Paperclip 客户端

谁：Electron / Tauri / IntelliJ plugin，给最终用户提供 agent 能力。skill 来源多：

- 应用自带的内置 skill（打包在二进制 / 资源包里）
- 用户本地 `~/.myapp/skills/` 下的 skill
- 从 skill marketplace 临时拉下来的 skill bundle（zip / tar / inline）
- 会话内临时生成的 skill（"一次性人设"）

它要什么：

- 多种 source 类型统一接入：磁盘、embed、内存 fs、inline 字符串。
- 能在 UI 上看到"当前 agent 开启了哪些 skill"、能让用户勾选或取消。
- 能在一次 `Run` 里临时加一个不进入长期 catalog 的 skill。
- 能在 skill 发生变更时不用重启整个 SDK。

它愿意付出什么：

- 实现一个 `SkillProvider`（如果 skill 来自外部可变源）。
- 为 inline skill 物化到磁盘这件事提供一个缓存根（比如 `~/.myapp/cache/`）。

它不能接受的：

- 临时 skill 必须先注册到 catalog 再用字符串引用。
- 自己去实现 skill 物化、缓存、`.ready` 文件写入这些基础设施。
- 为了支持 `ListSkills` 必须偶然发现 "还有一个 optional interface 要实现"。

### 2.3 画像 C：企业 SaaS / 多租户后端 / Paperclip 服务端

谁：多租户 SaaS，每个租户一组 skill，部分 skill 是 tenant-required、其它由用户勾选；skill 存储在数据库/对象存储里；请求级别能感知 tenant。

它要什么：

- `List(tenantID)` 可以对接数据库。
- Required skill 列表**按 tenant 动态**，且 caller 无法通过 per-run 绕过。
- 拒绝"进程级 override"当作"持久化"——持久化是宿主自己的数据库职责。
- 审计 / 观测：每次 run 用了哪些 skill、fingerprint 是什么。
- skill 体积可能很大，希望按需物化、有缓存复用。

它愿意付出什么：

- 实现 `SkillProvider`，在 `List()` 里按 tenant 把必装 skill 标记 `Required=true`。
- 可能自己实现 `SkillMaterializer`，把物化根挂到自家托管的存储。

它不能接受的：

- Admin 的 `SyncSkills` 假装帮忙"持久化"，实际只是 process-local override——和数据库中存储的真实 selection 冲突。
- 字段名 `Desired / Requested / Selected` 三套术语并存。
- adapter 暴露的 `SkillPayload` 字段被宿主误用为 API。
- caller 能在 per-run 里通过某种写法"挤掉"必装 skill。

### 2.4 画像小结

| 画像 | 需要的最小 API 表面 | 可选能力 |
|---|---|---|
| A（CLI） | `WithDefaultSkills(Skill...)` + `LocalSkill(path)` | — |
| B（桌面） | A + `InlineSkill/FSSkill` + `WithSkillProvider`（可变源时） | `WithSkillMaterializer`、`Admin().SetSelectedSkills` |
| C（SaaS） | `WithSkillProvider`（必需）+ Provider 内部用 `Skill.Required=true` 表达必装 | `WithSkillMaterializer`、fingerprint 审计字段 |

上限是 C，下限是 A；API 必须让 A **完全不感知** B/C 的复杂度。

## 3. 现状诊断（对照 §2 的画像）

### 3.1 双 hook 分工不直觉

```go
// 现状
type SkillCatalog interface {
    Resolve(ctx, tenantID string, refs []string) ([]Skill, error)
}
type SkillCatalogInventory interface { // optional
    List(ctx, tenantID string) ([]Skill, error)
}
type SkillAssembler interface {
    Prepare(ctx, SkillAssemblyRequest) (SkillPayload, error)
}
```

- 画像 A：两个都要不需要，但文档/示例把它们放在前排，逼 A 读懂才敢用。
- 画像 B/C：Catalog 需要，Assembler 不需要（默认 `prepareSkillPayload` 就够）。
- `Inventory` 是 optional interface，Admin 功能静默依赖它——B/C 的最小实现会踩陷阱。

### 3.2 `Skill` 值对象戴三顶帽子

```go
// 现状
type Skill struct {
    Key, Runtime, Content, PathHint string
    Metadata      map[string]string
    Files         []SkillFile
    Required      bool
    RequiredReason string
}
```

来源三选一但没类型约束，优先级写在 `materializeSkillSource` 里；`Runtime` 字段名和 `RuntimeServiceSpec` 撞得像孪生兄弟；`SkillFile.Kind` 的分类法（primary/markdown/reference/script/asset/other）是 SDK 内部概念，却在宿主合同里出现。

### 3.3 `SkillPayload` 是半公开半内部的混合体

```go
// 现状
type SkillPayload struct {
    Mode           SkillSyncMode
    Requested      []string
    Resolved       []Skill
    RuntimeEntries []SkillRuntimeEntry // 含 SourcePath，adapter-only
    Warnings       []string
    Fingerprint    string
}
```

`RuntimeEntries.SourcePath` 是 adapter 物化之后才有的字段，宿主既不填也不读，却出现在公共包的结构里。宿主要读 `Requested/Warnings/Fingerprint` 又没有清晰入口（当前靠 `SkillSnapshot` 间接返回，术语又不一致）。

### 3.4 术语漂移

| 概念 | 宿主面 | 运行面 | 管理面 |
|---|---|---|---|
| 这次 run 选定的 skill 列表 | `[]string` in `WithSkills` | `SkillPayload.Requested` | `SkillSnapshot.Desired` |
| 解析后的 skill 条目 | — | `SkillPayload.Resolved` | `SkillSnapshot.Resolved` |
| 物化后的条目 | — | `SkillPayload.RuntimeEntries` | `SkillSnapshot.Entries` |

同一件事三种叫法，术语统一成本很低，收益很高。

### 3.5 字符串引用一等公民导致热插拔别扭

```go
// 现状
agentadaptor.WithDefaultSkills("write-proof", "shadow-unused")
```

宿主手里已经有一个 `Skill` 值（画像 B 的 marketplace 拉取 / 画像 C 的一次性任务）时，必须先把它塞进 Catalog，再用字符串引用回去，或者替换整个 Catalog 实现。

### 3.6 Inline skill 物化策略黑盒

`~/.cache/agent-adaptor/skill-cache/` 是硬编码的；宿主想换缓存根、内存 fs 测试、或审计缓存写入行为，唯一办法是替换 `SkillAssembler` 再抄一遍 `prepareSkillPayload` 的所有逻辑。

### 3.7 Admin `SyncSkills` 语义模糊

`SyncSkills(desired)` 当前只修改 SDK 进程内的 per-agent selection，不触达任何持久化存储。字面量（"sync"）暗示持久化，实际是 process-local override；再加上 `SkillSyncMode = persistent/ephemeral` 是 adapter 行为字段，两个 "persistent" 挨得太近。

### 3.8 per-run `WithSkills` 覆盖语义是"policy vs selection"困惑的根源

```go
// 现状：per-run WithSkills 完全替换 binding WithDefaultSkills
WithDefaultSkills("compliance", "write-proof")
sdk.Run(..., WithSkills("adhoc"))   // Selected = {adhoc}，compliance 丢了
```

为了表达"必装"，任何"覆盖语义 + 单列表"的设计都会被迫引入第二个并行列表（`WithRequiredSkills`），从而产生"两个 Option 长得像、合并语义不同"的根源困惑。覆盖语义不变，任何命名优化都是治标不治本。

## 4. 设计原则

1. **单一主 hook**：宿主只要看见一个接口（`SkillProvider`），其它都是 SDK 内部。
2. **类型即文档**：skill 的来源用 sum type 表达，不用"同时设了以谁为准"这种运行时规则。
3. **值即引用**：`WithSkills/WithDefaultSkills` 同时接受 key 和 skill 值，画像 A/B 的一次性用法不再强依赖 Catalog。
4. **术语一把梳**：宿主/运行/管理三面用同一套词（`Selected / Entries / Source`）。
5. **公共面最小化**：`SkillPayload` 及其派生结构从公共 API 沉到 adapter 合同包，宿主永远看不见。
6. **可插入点窄且可选**：`SkillMaterializer` 替代原来的 `SkillAssembler`，只做"给我一个 `SourcePath`"这一件事。
7. **合并是并集，不是覆盖**：per-run `WithSkills` **追加**到 binding `WithDefaultSkills`，不允许替换。"必装"由 `Skill.Required` 这一个字段表达，不需要平行 Option。
8. **Key 是 skill 身份**：任何同 Key 的两个 Skill 值必须结构相等，否则 SDK 拒绝并报错。消除"哪方赢"的偏序。

## 5. 新 API 形状

以下 API 草案只展示**公共 API 表面**；内部辅助函数名可后续调整。

### 5.1 Skill 值对象

```go
type Skill struct {
    Key      string           // 业务标识；必须
    Source   SkillSource      // 来源，三选一，必须
    Required bool             // 只要出现在候选集里就必装；per-run 无法绕过
    Reason   string           // Required 时的人类可读原因
    Metadata map[string]string // 扩展字段；SDK 保留若干下划线前缀键
}

// SkillSource 是 sum type。adapter 侧能看到的 SourcePath 由 SDK 物化给出，
// 宿主只负责声明"从哪来"。
type SkillSource interface{ isSkillSource() }

type SkillFromPath   struct{ Path string }        // 磁盘目录（含 SKILL.md）
type SkillFromFS     struct{ FS fs.FS; Root string } // 嵌入 / 内存 fs
type SkillFromInline struct{ SkillMD string }     // 仅 SKILL.md 字符串
```

约束：

- `Source == nil` 直接返回 `ErrSkillSourceMissing`。
- `SkillFromPath.Path` 必须能 `Stat` 到一个含 `SKILL.md` 的目录，否则 Provider.List 阶段就报错。
- `SkillFromFS.Root` 默认 `"."`，子目录形式允许嵌入一个大 fs 里多个 skill。
- `SkillFromInline.SkillMD` 只支持单文件 SKILL.md；需要 references 的走 `SkillFromFS`。

`Required` 语义：

- 是 Skill 的**属性**，不是某个 Option 的参数。这个属性在任何来源（`WithDefaultSkills` 里的值、`WithSkills` 里的值、`SkillProvider.List()` 返回的值、`SkillSet` 里的值）上都能打。
- 只要一个 Skill 在候选集里出现且 `Required=true`，它就被 SDK 加进本次 run 的最终 Selected 集合。caller 无论在 per-run `WithSkills` 里塞什么都无法把它挤掉（见 §5.5）。

Metadata 保留键（SDK 语义化读取）：

| Key | 含义 |
|---|---|
| `_runtime_name` | 覆盖 adapter 物化目录名；默认 `slug(Key)` |
| `_display_name` | Admin UI 友好名 |

其它键 SDK 不解释。

### 5.2 Provider（唯一宿主 hook）

```go
type SkillProvider interface {
    // List 返回该 tenant 可见的完整 skill 清单。
    // 宿主后端完全无法枚举时返回 ErrSkillsNotEnumerable，Admin 层会降级。
    List(ctx context.Context, tenantID string) ([]Skill, error)
}

var ErrSkillsNotEnumerable = errors.New("skill provider cannot enumerate the full catalog")
```

删除：`SkillCatalog` / `SkillCatalogInventory` / `SkillAssembler` / `SkillAssemblyRequest` / `SkillFile` / `SkillFileKind` / `SkillRuntimeEntry`。

### 5.3 声明 + 选择（追加语义）

```go
// 绑定时：可以直接塞值，也可以只塞 key
func WithDefaultSkills(refs ...SkillRef) AgentOption

// 运行时：追加到 binding defaults，不替换
func WithSkills(refs ...SkillRef) RunOption

// SkillRef 是 sum type
type SkillRef interface{ isSkillRef() }

type SkillKey string           // 引用名，走 Provider.List 查找
func Key(k string) SkillRef    // 语义化构造器
func (Skill) isSkillRef()      // 值对象本身也是 ref
```

常用构造器（可选糖，本质等价于手写 `Skill{}`）：

```go
func LocalSkill(path string) Skill            // Source=SkillFromPath, Key 默认取目录 basename
func FSSkill(f fs.FS, root string) Skill      // Source=SkillFromFS
func InlineSkill(key, skillmd string) Skill   // Source=SkillFromInline
func Require(s Skill, reason string) Skill    // Required=true, Reason=reason（在已有 Skill 上打标记）
```

混用示例（per-run 追加一次性 skill）：

```go
sdk.Run(ctx, prompt,
    agentadaptor.WithSkills(
        agentadaptor.Key("write-proof"),              // 走 Provider.List
        agentadaptor.InlineSkill("adhoc", skillmd),   // 不进 catalog
        agentadaptor.LocalSkill("./one-shot"),
    ),
)
// 本次 Selected = binding defaults ∪ {write-proof, adhoc, one-shot} ∪ {Provider 里 Required=true 的} 并集去重
```

设计取舍：

- per-run `WithSkills` **无法**减少 binding defaults 中的 skill。如果 caller 的确需要一个"skill 更少"的 agent，应该通过 `sdk.Agent(name)` 拿一个 skill 更少的命名 binding，而不是在 per-run 里表达。
- 这条约束换来了**合并规则只剩并集一条**、"必装"不再需要平行 Option。

### 5.4 SkillSet（静态表的 Provider sugar）

```go
// SkillSet 是 SkillProvider 的内置实现：直接是一张 key->Skill 的表。
type SkillSet map[string]Skill

func (s SkillSet) List(ctx, tenantID string) ([]Skill, error) {
    out := make([]Skill, 0, len(s))
    for _, v := range s { out = append(out, v) }
    sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
    return out, nil
}

func WithSkillSet(set SkillSet) Option  // 等价 WithSkillProvider(set)
```

画像 A 用法：

```go
agentadaptor.WithSkillSet(agentadaptor.SkillSet{
    "write-proof": agentadaptor.LocalSkill("./skills/write-proof"),
})
```

画像 B/C 标记必装：

```go
agentadaptor.WithSkillSet(agentadaptor.SkillSet{
    "compliance":  agentadaptor.Require(
        agentadaptor.InlineSkill("compliance", complianceMD), "SOC2 audit",
    ),
    "write-proof": agentadaptor.LocalSkill("./skills/write-proof"),
})
// 每次 run 都会自动把 compliance 加进 Selected，不管 caller 传什么
```

### 5.5 合并规则 + 同 Key 不变式

本节是整套设计的核心，规则只有一条 + 一条不变式。

#### 5.5.1 合并规则（一条）

解析一次 run 时，SDK 按以下管线得到最终 Selected 集合：

1. **候选池**：把所有来源的 Skill 值放进同一个候选池——
   - `WithDefaultSkills(...)` 展开后的 Skill 值（其中的 `SkillKey` 通过 Provider 解析成 Skill 值）
   - `WithSkills(...)` 展开后的 Skill 值（同上）
   - `SkillProvider.List(tenantID)` 返回的**所有 `Required=true` 的**条目
2. **按 Key 分组去重**：见 §5.5.2。
3. **最终 Selected** = 候选池去重后的集合。

无优先级、无覆盖、无偏序。

#### 5.5.2 同 Key 不变式

对候选池里按 Key 分组后，每组必须满足：**组内所有 Skill 值结构相等**（`Key / Source / Required / Reason / Metadata` 逐字段深等）。

判定结果：

- 组内全部相等（只有一个唯一值） → 合并为 1 条，正常入 Selected。
- 组内出现两个或以上不同值 → SDK 立即返回 `ErrSkillKeyConflict`，错误信息必须带上冲突各方的 Source 简述。

错误时机：

- 能在 `agentadaptor.New(...)` 构造时发现的冲突（`WithDefaultSkills` + `WithSkillSet` 内部），在 `New` 就 panic（与 `AGENTS.md` §4.4 "构造失败直接 panic" 一致）。
- 运行时才能发现的冲突（per-run Skill 值与 Provider tenant-aware 结果），在 `Run/Start` 返回 `ErrSkillKeyConflict`。

错误消息样例：

```
ErrSkillKeyConflict: skill key "compliance" is defined by multiple sources with different content:
  - provider:  source=SkillFromPath("/opt/catalog/compliance"), required=true
  - per-run:   source=SkillFromInline(len=842), required=false
hint: use Key("compliance") to reference the catalog skill, or rename the inline skill.
```

#### 5.5.3 为什么是"同 Key 同值必报错"

这条硬规则替代了所有"哪方赢"的偏序讨论。它的正面作用：

- caller 传 `Key("compliance")` 引用 Provider 已有的 Required skill → 解析后与 Required 自动注入拿到同一个 Skill 值，结构相等 → 幂等通过。**这是绝大多数真实场景**。
- caller 传 `InlineSkill("compliance", custom)` 试图换掉 Provider 的 Required skill → 结构不等 → SDK 报错。caller 得到明确指引："改名叫 `compliance-patch`"或者"你要引用的是已有那个就写 `Key("compliance")`"。
- 宿主自己 `WithDefaultSkills` 和 `WithSkillSet` 都定义了同 Key 且内容不同 → New 时 panic，宿主立刻发现这是 bug。

caller 的三种真实意图与写法：

| 意图 | 正确写法 | 结果 |
|---|---|---|
| 用 catalog 里那个 `compliance` | `WithSkills(Key("compliance"))` | 幂等，与 Required 自动注入同一 Skill |
| 在 compliance 之外再追加一个说明 | `WithSkills(InlineSkill("compliance-patch", md))` | 新 Key，无冲突 |
| 完全换掉 `compliance` 实现 | SDK 拒绝 | 要么换 binding，要么改 Provider tenant 逻辑；不走 per-run |

#### 5.5.4 为什么不再需要 `WithRequiredSkills` / `WithRequiredSkillsFunc`

所有"必装"信息只有一个入口：**Skill 值上的 `Required` 字段**。它可以在这些地方设置：

- `WithSkillSet` 的值里（静态全局必装）
- `WithSkillProvider.List(ctx, tenantID)` 的返回里（tenant-aware 动态必装——这正是画像 C 的标准写法）
- `WithDefaultSkills` 的值里（binding-level 必装，但由于 §5.3 追加语义，effectively 等于 "始终入选"）

两个旧 Option（`WithRequiredSkills/Func`）删除，因为：

- tenant-aware 场景：Provider 已经拿到 `tenantID`，直接在返回项里打标记，不需要第二个 Option。
- 第三方 Provider 不可改的边缘场景：用 `providers.MarkRequired` 之类的装饰器包一层 Provider 即可（§5.6）。

### 5.6 Provider 装饰器（可选）

对"第三方 Provider 不可改，但宿主想叠加 required 标记"的边缘情况，提供一个装饰器 sugar。非主 API：

```go
package providers

// MarkRequired 返回一个 SkillProvider，在 inner.List 返回后把指定 Key 的
// Skill 标记成 Required=true。不存在的 Key 静默忽略。
func MarkRequired(inner agentadaptor.SkillProvider, reqs ...Pin) agentadaptor.SkillProvider

type Pin struct{ Key, Reason string }
```

用法：

```go
sdk := agentadaptor.New(
    agentadaptor.WithSkillProvider(
        providers.MarkRequired(thirdPartyProvider,
            providers.Pin{Key: "compliance", Reason: "SOC2"},
        ),
    ),
)
```

这个装饰器不是 SDK 公共 API 的第一公民——它在 `providers` 子包里，是可组合的数据变换器，不新增 SDK 构造层 Option。

### 5.7 Materializer（可选，替代旧 SkillAssembler）

**"缓存根" 是默认 Materializer 的一个体现**：SDK 把 `SkillFromFS` / `SkillFromInline` 这类"没有现成磁盘路径"的 Skill 物化成 SKILL.md 目录时，所有物化目录的共同父目录就是缓存根，默认是 `os.UserCacheDir()/agent-adaptor/skill-cache/`。自定义 `SkillMaterializer` 的第一动机通常就是把这个根换掉（多租户隔离、容器 PVC、测试 tempdir、审计卷、桌面应用自管目录等），其次才是接管命名策略、原子化方式、跨 Run 去重规则等。`SkillFromPath` 源本身就是稳定路径，默认实现直接回传，不经过缓存根。

```go
type SkillMaterializer interface {
    // Materialize 把 Skill 落到一个稳定磁盘路径，返回 SourcePath（目录，含 SKILL.md）。
    // SkillFromPath：大多数实现直接回传 Path。
    // SkillFromFS / SkillFromInline：写到缓存根并返回物化目录。
    Materialize(ctx context.Context, s Skill) (sourcePath string, err error)
}

func WithSkillMaterializer(m SkillMaterializer) Option
```

默认实现：

- `SkillFromPath` → 原样返回（不拷贝）。
- `SkillFromFS` / `SkillFromInline` → 写到 `os.UserCacheDir()/agent-adaptor/skill-cache/<runtime-name>--<12 位内容 hash>/` 下，带 `.agent-adaptor-ready` marker 做原子化；hash 基于 `(Key, RuntimeName, Required, 文件内容, Metadata)`，内容一变目录就变，同内容跨 Run 复用。

常见的 "换一个 Materializer" 触发场景（非穷举）：

| 场景 | 默认根的问题 | 自定义要点 |
|---|---|---|
| 多租户 SaaS | 全租户共用 `~/.cache/...`，审计 / 清理粒度差 | 落到 `/var/lib/app/skill-cache/{tenant}/<hash>/` |
| 容器 / K8s | `$HOME` 可能只读或被清空 | 指向挂载的 PVC / emptyDir / tmpfs |
| 测试 | 污染真实 `~/.cache`，并行 race | 用 `t.TempDir()` 子目录 |
| 合规 | 需要审计挂载、SELinux label、加密卷 | 落到受审计的卷 |
| 桌面应用 | 用户看不到默认缓存目录，清理困难 | 落到 `~/.myapp/cache/skills/` |

SDK 永远是**唯一**调用 `Materialize` 的主体。adapter 只读 `ResolvedSkill.SourcePath`。

### 5.8 Adapter 合同（sink 到 adapter-only 包）

`SkillPayload` 从公共 API 删除，替换为 adapter 合同：

```go
// 位于 adapter 合同包（与 DriverAdapter 同层），宿主一般看不见。
type ResolvedSkills struct {
    Mode        SkillSyncMode
    Entries     []ResolvedSkill
    Warnings    []string
    Fingerprint string
}

type ResolvedSkill struct {
    Key         string
    RuntimeName string
    SourcePath  string
    Required    bool
    Reason      string
    Metadata    map[string]string
}

type SkillAwareDriver interface {
    InjectSkills(ctx context.Context, cfg any,
        skills ResolvedSkills, profile *ProfileSelection) error
    ListSkills(ctx context.Context, cfg any,
        skills ResolvedSkills, selected []string, resolved []Skill,
        profile *ProfileSelection) (SkillSnapshot, error)
    SyncSkills(ctx context.Context, cfg any,
        skills ResolvedSkills, selected []string, resolved []Skill,
        profile *ProfileSelection) (SkillSnapshot, error)
}
```

SDK ⇒ adapter 合同约束（强制不变量）：

- **`selected == payload.Keys()`**。两者永远同序同值；adapter 可以任选其一
  使用，SDK 保证一致性。`selected` 参数只是帮助阅读代码时把"选择面"
  和"物化面"分开，没有语义冗余。
- **`resolved` ⊇ `payload.Entries` (by Key)**。`resolved` 是 provider +
  binding-only + selected 的全量合并结果（包含未被选中的候选项）；adapter
  必须原样把它透传到 `SkillSnapshot.Resolved`，不得静默删减。
- **`payload.Warnings`** 由 SDK 填入物化阶段的非致命信息（例如某个 key
  物化失败、被剔除）。adapter 应把它原样追加到 `SkillSnapshot.Warnings`。

Resume 守卫约束（强制）：

- `payload.Fingerprint` 是这一次 skill 解析的稳定摘要（entries + warnings）。
- 支持 resume 的 adapter **应该**把 `Fingerprint`（或等价的 skill
  fingerprint）塞进 `SessionParams.Values` 里，使得 `SessionCodec.GuardFingerprint`
  对不同的 skill 选择产生不同的 guard。
- 当 `Run` 收到一个 resume id，而当前的 skill fingerprint 与该 session
  捕获时的 fingerprint 不一致时，adapter **应**拒绝继续，返回
  `ErrSessionSkillDrift` 或领域特定错误。宿主 UI 层再决定是提示用户
  开新 session 还是允许继续。

术语对齐（公共 API 层）：

```go
type SkillSnapshot struct {
    DriverType  string
    Supported   bool
    Mode        SkillSyncMode
    Selected    []string       // 旧 Desired / Requested 统一到 Selected
    Resolved    []Skill
    Entries     []SnapshotEntry // 旧 SkillEntry，字段照搬
    Warnings    []string
    Fingerprint string
}
```

### 5.9 Admin 面语义校正

```go
type AgentAdmin interface {
    // ... 其它保留 ...
    ListSkills(ctx context.Context) (SkillSnapshot, error)
    SetSelectedSkills(ctx context.Context, keys []string) (SkillSnapshot, error) // 原 SyncSkills
}
```

- `SetSelectedSkills` 明确是 **process-local**，从方法名消除"持久化"暗示。
- 持久化是宿主职责（比如画像 C 的数据库）；SDK 不假装做持久化。
- 如果 Provider 返回 `ErrSkillsNotEnumerable`，`ListSkills` 返回 `Supported=false, Mode=SkillSyncUnsupported, Warnings=["provider cannot enumerate"]`。
- `SetSelectedSkills` 的 keys 只能是 Provider 里已有的 Key；Required skills 不受这个 set 影响（它们的 Required 属性来自 Provider/SkillSet，永远自动入选）。

### 5.10 SDK 构造侧 Option 面总表

新 API 的全部 skill 相关 Option（对比旧 API 的表面积）：

| 旧 API | 新 API |
|---|---|
| `WithSkillCatalog(SkillCatalog)` | `WithSkillProvider(SkillProvider)` |
| — | `WithSkillSet(SkillSet)` |
| `WithSkillAssembler(SkillAssembler)` | ~~删除~~ |
| — | `WithSkillMaterializer(SkillMaterializer)`（可选） |
| `WithDefaultSkills(...string)` | `WithDefaultSkills(...SkillRef)`（接受 key 或 Skill 值） |
| `WithSkills(...string)` | `WithSkills(...SkillRef)`（追加语义） |

构造器 helpers：`LocalSkill`、`FSSkill`、`InlineSkill`、`Key`、`Require`。

表面积：**删 2 个 Option，加 2 个 Option + 5 个 helper**。画像 A/B 在常见调用路径上只用 `WithDefaultSkills` + `WithSkills` + helpers，完全不感知 Provider / Materializer / 冲突规则。

## 6. 按画像走一遍 before/after

### 6.1 画像 A（本地 CLI）

改前：

```go
catalog := mockkit.StaticSkillCatalog{
    Entries: map[string]agentadaptor.Skill{
        "write-proof": {Key: "write-proof", Runtime: "write-proof", PathHint: "./skills/write-proof"},
    },
}
sdk := agentadaptor.New(
    agentadaptor.WithDefaultAgent(codex.New(cfg,
        agentadaptor.WithDefaultSkills("write-proof"),
    )),
    agentadaptor.WithSkillCatalog(catalog),
)
```

改后：

```go
sdk := agentadaptor.New(
    agentadaptor.WithDefaultAgent(codex.New(cfg,
        agentadaptor.WithDefaultSkills(
            agentadaptor.LocalSkill("./skills/write-proof"),
        ),
    )),
)
```

关键差异：不需要 `SkillCatalog`、不需要理解 `Runtime/PathHint`、不需要字符串 + 表二次声明。

### 6.2 画像 B（桌面应用，混合来源 + 一次性 skill）

改前：

```go
catalog := buildCatalogFromAllSources(embedded, userHome, marketplaceCache)
sdk := agentadaptor.New(
    agentadaptor.WithDefaultAgent(claude.New(cfg,
        agentadaptor.WithDefaultSkills(selectedKeys...),
    )),
    agentadaptor.WithSkillCatalog(catalog),
)
// 一次性 skill：必须先塞回 catalog 再引用
catalog.Add("adhoc", agentadaptor.Skill{Key: "adhoc", Content: md})
sdk.Run(ctx, prompt, agentadaptor.WithSkills(append(selectedKeys, "adhoc")...))
```

改后：

```go
sdk := agentadaptor.New(
    agentadaptor.WithDefaultAgent(claude.New(cfg,
        agentadaptor.WithDefaultSkills(selectedRefs...),  // SkillRef 可以是 key 或 Skill
    )),
    agentadaptor.WithSkillProvider(marketplaceProvider),  // 动态来源
    agentadaptor.WithSkillMaterializer(myAppCache),       // 自定义物化策略（含缓存根）
)
// 一次性 skill：直接追加值对象，不污染 catalog
sdk.Run(ctx, prompt, agentadaptor.WithSkills(
    agentadaptor.InlineSkill("adhoc", md),
))
```

关键差异：一次性 skill 不污染 catalog；多源通过 `SkillProvider` 一把抽象；物化策略（缓存根、命名、原子化）可换；per-run 是追加，defaults 里的选中 skill 自然保留。

### 6.3 画像 C（多租户 SaaS）

改前：

```go
// 实现 SkillCatalog + SkillCatalogInventory（否则 ListSkills 不工作）
// 可能还要实现 SkillAssembler 以定制缓存根或 fingerprint
// 必装 skill 塞进 WithDefaultSkills 里，但会被 per-run WithSkills 覆盖丢掉
catalog := tenantCatalog{db: db}
assembler := tenantAssembler{cache: cache}
sdk := agentadaptor.New(
    agentadaptor.WithDefaultAgent(codex.New(cfg,
        agentadaptor.WithDefaultSkills(defaultRequired...),
    )),
    agentadaptor.WithSkillCatalog(catalog),
    agentadaptor.WithSkillAssembler(assembler),
)
```

改后：

```go
// Provider.List 里按 tenant 返回的 Skill 自己带 Required=true
type tenantProvider struct{ db Database }

func (p tenantProvider) List(ctx context.Context, tenantID string) ([]agentadaptor.Skill, error) {
    skills := p.db.ListForTenant(tenantID)
    for i := range skills {
        if p.db.IsTenantPinned(tenantID, skills[i].Key) {
            skills[i].Required = true
            skills[i].Reason   = "tenant policy"
        }
    }
    return skills, nil
}

sdk := agentadaptor.New(
    agentadaptor.WithDefaultAgent(codex.New(cfg)),
    agentadaptor.WithSkillProvider(tenantProvider{db: db}),
    agentadaptor.WithSkillMaterializer(tenantCache), // 自定义物化策略（按 tenant 隔离缓存根）
)
// caller 在 per-run 里怎么传都不会丢 tenant 必装
sdk.Run(ctx, prompt, agentadaptor.WithSkills(agentadaptor.InlineSkill("adhoc", md)))
```

关键差异：

- `List` 必须实现而不是 optional，SaaS 宿主不再踩陷阱。
- 必装从一个"第二组 Option"回归为"Skill 自带属性"，由 Provider 在 tenant 上下文里直接打标记。
- caller 无法通过 per-run 绕过必装（同 Key 同值时幂等，不同值时报错）。
- `Assembler` 被 `Materializer` 取代，接口从 "组装整个 payload" 收窄到 "给我一个路径"。

## 7. 与 §3 痛点一一对照

| 痛点 | 本设计的应对 |
|---|---|
| 3.1 双 hook 分工不直觉 | `SkillProvider` 唯一主 hook；物化点下沉到可选 `SkillMaterializer` |
| 3.2 `Skill` 三顶帽子 | `Source` sum type；`SkillFile/SkillFileKind` 删除；`Runtime` 字段降级为 `_runtime_name` metadata |
| 3.3 `SkillPayload` 半公半内 | `SkillPayload` 从公共 API 删除；adapter 合同用 `ResolvedSkills` |
| 3.4 术语漂移 | 统一 `Selected / Entries / Resolved`；`Desired / Requested` 废弃 |
| 3.5 字符串引用一等公民 | `SkillRef` 同时接受 key 和 Skill 值 |
| 3.6 物化黑盒 | `SkillMaterializer` 插拔点；默认实现保留 |
| 3.7 `SyncSkills` 语义模糊 | 改名 `SetSelectedSkills`；文档明确 process-local |
| 3.8 覆盖语义 → policy/selection 困惑 | `WithSkills` 改追加语义；`Required` 成为 Skill 的属性；合并规则收敛为并集一条 |

## 8. 非目标

本设计**不**做的事：

- 不做 skill 跨 agent 共享的策略层（留给宿主）。
- 不做 skill 版本管理 / 依赖解析 / 冲突检测（skill 市场自己的问题）。
- 不给 adapter 新增 skill 之外的 injection 协议（MCP/Runtime Services 不受影响）。
- 不改 `SkillSyncMode` 枚举（`ephemeral/persistent/unsupported` 维持）。
- 不改 Session 对 skill 变更的处理（按 `AGENTS.md` §6，skill 变更仍不自动 incompatible session）。
- 不提供"per-run 减少 binding defaults 中的 skill"的入口。如果需要，改用命名 binding（`sdk.Agent(name)`）。

## 9. 开放问题（评审点）

评审时请特别看这几条，不同取舍影响大：

1. **Q1：`SkillProvider.List` 必须实现吗？**
   提案：是。必须实现，无法枚举时显式返回 `ErrSkillsNotEnumerable`，让"静默半工作"变成"显式降级"。
   替代：保留 optional 分层，但强制 Provider 在构造时 advertise 能力（`SkillProvider.Capabilities()`）。

2. **Q2：`SkillRef` 是否用 sum type？**
   提案：是。`SkillRef interface{ isSkillRef() }` + `SkillKey string` + `Skill`。
   替代：用两个 option (`WithSkills / WithInlineSkills`)，API 更扁平但调用点分散。

3. **Q3：per-run `WithSkills` 是追加还是覆盖？**
   提案：追加（§5.3、§5.5.1）。代价：per-run 无法表达"这次少用一个 default skill"；必须通过切换 binding 解决。收益：合并规则收敛一条，`WithRequiredSkills` 不再需要。
   替代：保留覆盖语义，引入 `WithRequiredSkills`。被否决的原因见 §3.8 与前版本的设计讨论。
   **此问题是整套设计的支点，必须优先评审**。

4. **Q4：同 Key 不同值是硬错还是柔性合并？**
   提案：硬错（`ErrSkillKeyConflict`，见 §5.5.2）。Key 是身份，同名必同值。
   替代：柔性合并（引入偏序：per-run > default > provider）。但这正是我们要避免的"哪方赢"偏序，且和"必装不可绕过"冲突。
   二级子问题：错误消息是否需要带 unified diff 级别的 Source 细节？倾向带，便于调试。

5. **Q5：`SkillMaterializer` 是否 MVP 提供？**
   提案：MVP 就提供，因为没有它画像 B/C 的缓存路径需求退回到"替换整个 provider 并自己抄物化代码"。
   替代：MVP 先不做，等外部需求明确再加。

6. **Q6：`SkillFromFS` 是否保留 `Root` 字段？**
   提案：保留，以支持"一个 fs.FS 挂载多个 skill 子目录"的 embed 场景。
   替代：只允许一个 fs 代表一个 skill（子目录由宿主自己切）。

7. **Q7：`SkillSnapshot` 要不要同步改名?**
   提案：字段内改名（`Desired→Selected`，`SkillEntry→SnapshotEntry`），类型名保留。
   替代：类型名一起改（`SkillSnapshot→SkillReport`），clean slate 更彻底但连带面更大。

8. **Q8：`providers.MarkRequired` 放在哪个包？**
   提案：`github.com/agent-dance/agent-adaptor/providers`，与 core SDK 解耦，但和 `SkillProvider` 同仓。
   替代：放到 `examples/` 里不视为公共 API；让每个宿主按需自行实现。

## 10. 预期工作量（粗估）

- 公共类型定义（`Skill/SkillSource/SkillProvider/SkillRef/SkillSet/...`）：1 天。
- SDK 内部替换（`skill_helpers.go / skill_resolution.go / managers.go / options.go / sdk.go`），含 §5.5 合并规则与冲突检测：2 天。
- 三个 adapter（codex / claude / cursor）的 adapter 合同层适配（`ResolvedSkills`）：2 天。
- 内部 `skillruntime` helper 重构到新 `ResolvedSkills`：1 天。
- examples + mockkit 重写（`StaticSkillCatalog` 变 `SkillSet`，`StaticSkillAssembler` 删除）：1 天。
- 文档（usage-guide / AGENTS §4.3 / skill-api-design 定稿）：1 天。
- 测试（`skills_sdk_test.go` / 三个 adapter 的 skill 测试 / `adaptertest` conformance / 同 Key 冲突用例）：2 天。

合计 ~10 工作日。若评审完 §9 之后方向锁定，实施阶段可以和 streaming / HITL 工作并行（它们改的文件不冲突）。

## 11. 下一步

1. 评审本文档 §2 场景画像是否覆盖真实宿主、§5 API 是否在表达力上退化。
2. 回答 §9 Q1–Q8；锁定后我会把本文档 Rev 成 "workstream-skill-api.md" 并加实施计划与回归用例清单。
3. 实施不留兼容层，旧符号直接删除；commit 前由 `AGENTS.md` §9 "对未来修改者的硬要求" 再做一次清单核对。
