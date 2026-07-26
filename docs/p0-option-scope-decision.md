# P0.1 决策记录：Option 双作用域机制（决策 D7）

> 状态：spike 完成，**推荐案 A（双接口，编译期约束）**。
> 日期：2026-07-26。配套：[api-v1-redesign.md](./api-v1-redesign.md) §2.3、[api-v1-implementation-plan.md](./api-v1-implementation-plan.md) §0 D7 / P0.1 / 风险 R1。
> spike 代码：scratchpad `option-scope-spike/`（独立 go module，`go test ./...` 10 用例全绿，go1.26 实测；`dualiface/` = 案 A，`scoped/` = 案 B，各含 `team/` 子包模拟生态扩展）。

## 1. 要解决的问题

v1 约 24 个 `With*` 选项一套词汇、两个作用域：出现在 `adaptor.New(driver, opts...)` 是 agent 级默认值，出现在 `agent.Run/Stream(ctx, prompt, opts...)` 是单次覆盖；`WithThreadStore`、`WithProfile` 等只在构造处有意义；§2.3 表格还有一类只在调用处有意义（`WithTimeout`、`WithSchema[T]`、`WithoutTokenStream`）。同时，生态包必须能发行自己的选项（如 `team.Option()` 传入 `New`），因此选项类型必须是**外部包可实现的接口**，不能是仅根包可构造的函数闭包集合。

两案原型均完整覆盖：双作用域生效与调用处覆盖构造处（含「skills 追加、其余替换」合并语义、跨 run 无污染）、构造处专属选项误用的失败形态、生态包自定义选项（构造处专属 + 双作用域各一个）、泛型选项 `WithSchema[T]`。

## 2. 案 A：双接口，编译期约束（`dualiface/`）

三种作用域对应三个导出接口，**构造函数的返回类型即作用域文档**：

```go
// Option 是 New 接受的全集接口。
type Option interface {
    ApplyNew(*AgentSettings)
}

// CallOption 是 Run/Stream 接受的调用处接口。注意不嵌入 Option。
type CallOption interface {
    ApplyRun(*RunSettings)
}

// SharedOption 双作用域：既可传 New，也可传 Run/Stream。
type SharedOption interface {
    Option
    CallOption
}

func New(d driver.Driver, opts ...Option) *Agent
func (a *Agent) Run(ctx context.Context, prompt string, opts ...CallOption) (*Result, error)

func WithModel(m string) SharedOption            // 双作用域
func WithThreadStore(s Store) Option             // 仅构造处
func WithTimeout(d time.Duration) CallOption     // 仅调用处
func WithSchema[T any]() CallOption              // 泛型与接口返回值直接组合，无障碍
```

关键设计取舍：

- **`CallOption` 不嵌入 `Option`**。若嵌入（子集关系），则「仅调用处」选项无法在编译期禁止出现在 `New` 中；两接口独立、`SharedOption` 汇合，三种作用域全部可表达，且两个方向的误用都是编译错误（对称）。
- **写入目标用结构体嵌入表达子集关系**：`AgentSettings{ RunSettings; ThreadStore; Profile }`。`CallOption.ApplyRun` 拿到的 `*RunSettings` 上**根本没有** `ThreadStore` 字段可写——即使生态包想越权也过不了类型系统，作用域约束下沉到了字段级。
- 双作用域选项的实现成本 = 一个 func 类型挂两个转发方法：

```go
type sharedOptionFunc func(*RunSettings)

func (f sharedOptionFunc) ApplyNew(s *AgentSettings) { f(&s.RunSettings) }
func (f sharedOptionFunc) ApplyRun(s *RunSettings)   { f(s) }
```

- 生态包直接实现导出方法即可发行选项（spike 的 `team` 包分别实现了构造处专属的 `svc.Option() adaptor.Option` 与双作用域的 `team.WithTrace(label) adaptor.SharedOption`，不依赖根包任何非导出符号）。

**误用 = 编译错误（go1.26 实测报错原文）：**

```text
agent.Run(ctx, "p", adaptor.WithThreadStore(store))
  → cannot use adaptor.WithThreadStore(store) (value of interface type adaptor.Option)
    as adaptor.CallOption value in argument to agent.Run:
    adaptor.Option does not implement adaptor.CallOption (missing method ApplyRun)

adaptor.New("codex", adaptor.WithTimeout(time.Second))
  → cannot use adaptor.WithTimeout(time.Second) (value of interface type adaptor.CallOption)
    as adaptor.Option value in argument to adaptor.New:
    adaptor.CallOption does not implement adaptor.Option (missing method ApplyNew)
```

错误在 IDE 输入时即出现（gopls 红线），且信息点名了缺失方法与两个类型名，指向性足够。

## 3. 案 B：单接口 + 运行时校验（`scoped/`）

全部构造函数返回同一 `Option`；选项自带作用域位掩码与名字，`New`/`Run` 应用前校验：

```go
type Scope uint8

const (
    ScopeNew Scope = 1 << iota // 仅构造处
    ScopeCall                  // 仅调用处
    ScopeBoth = ScopeNew | ScopeCall
)

var ErrOptionScope = errors.New("option used outside its allowed scope")

type Option interface {
    OptionName() string   // 用于错误信息
    OptionScope() Scope
    Apply(*Settings)      // 单一 Settings：构造处写默认值，调用处写克隆
}

func WithModel(m string) Option       // 签名看不出作用域
func WithThreadStore(s Store) Option  // 同上
```

- `Run` 收到构造处专属选项立即返回错误（可 `errors.Is(err, ErrOptionScope)`），信息含正确用法提示，实测输出：

```text
adaptor: option used outside its allowed scope: WithThreadStore is a
construction-time option; pass it to adaptor.New(driver, WithThreadStore(...))
instead of Agent.Run/Stream
```

- 反方向（调用处专属选项传给 `New`）因 v1 的 `New` 不返回 error，只能**暂存误用、在首次 Run/Stream 返回**（spike 采用此形态；备选是 `New` panic，属程序员错误可辩护，但对「构造一次、常驻服务」的宿主不友好）。误用反馈点从「写代码时」退到「跑到这行时」，再退到「第一次执行时」。
- 生态包实现三个方法（或用 `adaptor.NewOption(name, scope, apply)` 便捷构造）即可。但 **scope 申报靠自觉**：外部选项可以声称 `ScopeBoth` 却在 `Apply(*Settings)` 里写 `ThreadStore` 字段——单一 Settings 对所有字段一视同仁，越权只能靠 code review 拦。

## 4. 评分表

| 维度 | 案 A（双接口） | 案 B（单接口 + 运行时校验） |
|---|---|---|
| 误用是否编译期报错 | **5/5** ✅ 双向（构造处专属→Run、调用处专属→New）都是编译错误，IDE 即时红线；报错点名缺失方法与接口名 | **2/5** ❌ 全部推迟到运行时；Run 处误用报错及时且信息友好，但 New 处误用要等首次 Run 才暴露；测试没覆盖到的调用点会带病上线 |
| godoc 呈现 | **4/5** ✅ 返回类型即作用域文档，24 个选项在 godoc 里天然按 `Option` / `CallOption` / `SharedOption` 三组签名自解释；代价：3 接口 + 2 Settings 共 5 个类型进入首屏（计入 ~35 导出名预算） |	**3/5** ➖ 首屏类型更少（Option/Scope/Settings），但 24 个函数签名清一色 `func WithX(...) Option`，作用域只能靠注释第一句约定，正是现状「66 个 With* 无法区分」痛点的残留形态 |
| 生态包扩展性 | **5/5** ✅ 实现 1–2 个导出方法即可发行任意作用域选项；`RunSettings ⊂ AgentSettings` 结构嵌入把作用域约束下沉到字段级，生态包类型上就摸不到越权字段 | **4/5** ✅ 可实现且实现面略小（3 方法或一行 `NewOption`）；但 scope 申报与字段访问都不设防，谎报作用域/越权写字段只能靠 review |

两案共同点（不构成区分项）：双作用域生效与「近处覆盖远处；skills 追加、其余替换」语义实现完全一致；泛型选项 `WithSchema[T]` 均无障碍；生态包均无需根包适配器即可自行实现接口。

## 5. 推荐：案 A（双接口，编译期约束）

理由：

1. **达标性**：设计文档 §2.3 的原文要求是「编译期拒绝作用域非法的组合」，两案中只有案 A 达标；案 B 正是风险登记册 R1 预留的兜底方案，spike 证明不需要动用兜底。
2. **误用反馈时机**：案 A 把反馈从「运行到这行时/首次执行时」提前到「敲代码时」。对「构造一次、常驻多年」的宿主（Web 服务、桌面产品），New 处的配置错误在案 B 下最晚可能到上线后才炸。
3. **godoc 即文档**：`WithThreadStore(...) Option` 与 `WithModel(...) SharedOption` 并排出现时，作用域一目了然——这直接偿还了现状 1.1 节「三种 Option 同名风格、调用点无法区分」的设计债，而不是把它换个形式留下。
4. **生态安全边界**：`team.Option()` 这类生态选项两案都能做，但案 A 额外获得字段级权限边界（调用处选项类型上拿不到构造处专属字段），对「选项接口对全生态开放」的 v1 是实打实的防御纵深。
5. **成本可控**：双作用域选项的全部额外成本是一个 func 类型的两个单行转发方法；仅构造处/仅调用处选项与普通 functional option 无异。

案 A 的代价与缓解：

| 代价 | 缓解 |
|---|---|
| 根包多 5 个导出类型（`Option`/`CallOption`/`SharedOption`/`AgentSettings`/`RunSettings`） | 仍在 ~35 导出名预算内；`AgentSettings`/`RunSettings` 字段不导出、只暴露精选方法（见 §6），godoc 噪音低 |
| 宿主把选项收进 `[]adaptor.Option` 变量后不能再传 `Run`（反之亦然） | 属于作用域约束的本意而非缺陷；文档示例统一用字面量列表；确需复用的双作用域集合可声明为 `[]adaptor.SharedOption`（两处都可展开传入，Go 不允许 `[]SharedOption` 直接赋给 `...Option`，需逐个 append，文档给出惯用法） |
| 编译错误文案是编译器生成的（missing method），不如案 B 的定制文案口语化 | 报错已含两个接口名与缺失方法名；在 `Option`/`CallOption` 的 doc 注释里写明「看到 missing method ApplyRun = 该选项只能用于 New」即可 |
| `bridges/a2a` 的 `ServerOptions.Options []adaptor.Option`（P4.4，调用作用域转发）需改为 `[]adaptor.CallOption` | 属于连带的正确性修正，P4.4 实施时同步调整 |

## 6. v1 根包落地骨架

与 spike 的差别只有一处：真实落地时 `RunSettings`/`AgentSettings` 字段**不导出**，生态扩展面收敛为精选导出方法（根包自身选项也走这些方法，保证扩展面完备性被自我验证）。

```go
package adaptor // v1 根包

// ============ 作用域接口三件套 ============

// Option 可出现在 adaptor.New 构造处，写入 agent 级默认配置。
// 传给 Run/Stream 无法编译（缺 ApplyRun 方法）。
type Option interface {
	// ApplyNew 把选项写入 agent 级默认配置。
	ApplyNew(*AgentSettings)
}

// CallOption 可出现在 Run/Stream 调用处，写入本次执行的有效配置。
// 有意不嵌入 Option：仅调用处选项（WithTimeout、WithSchema 等）
// 传给 New 同样无法编译，两个方向对称。
type CallOption interface {
	// ApplyRun 把选项写入本次调用的有效配置（agent 默认值的克隆）。
	ApplyRun(*RunSettings)
}

// SharedOption 是双作用域选项的返回类型：
// 用于 New 是该 Agent 的默认值，用于 Run/Stream 是本次覆盖。
// 约 24 个核心选项中的大多数返回它。
type SharedOption interface {
	Option
	CallOption
}

// ============ 选项写入目标（生态包的受控扩展面） ============

// RunSettings 汇集调用处可覆盖的配置。字段不导出，
// 生态包通过导出方法写入受支持的扩展点；合并语义由方法本身表达
// （AddSkills 追加、SetModel 替换、SetMetadata 按 key 覆盖）。
type RunSettings struct {
	model    string
	skills   []skill.Ref
	metadata map[string]string
	services []ServiceSpec
	// ... 其余双作用域/调用处字段
}

func (s *RunSettings) SetModel(m string)                { s.model = m }
func (s *RunSettings) AddSkills(refs ...skill.Ref)      { s.skills = append(s.skills, refs...) }
func (s *RunSettings) SetMetadata(k, v string)          { /* lazily init map; set */ }
func (s *RunSettings) AddServices(specs ...ServiceSpec) { s.services = append(s.services, specs...) }

// AgentSettings = RunSettings（双作用域字段）+ 构造处专属字段。
// 子集关系由结构体嵌入直接表达：CallOption 拿到的 *RunSettings
// 类型上就不存在 threadStore/profile，可写字段即作用域边界。
type AgentSettings struct {
	RunSettings
	threadStore threadstore.Store
	profile     profile.Spec
	// ... 其余仅构造处字段（WorkspaceManager / SkillProvider / EventBuffer ...）
}

func (s *AgentSettings) SetThreadStore(st threadstore.Store) { s.threadStore = st }
func (s *AgentSettings) SetProfile(p profile.Spec)           { s.profile = p }

// ============ 根包内部的函数适配器（三种作用域各一个） ============

type sharedOptionFunc func(*RunSettings)

func (f sharedOptionFunc) ApplyNew(s *AgentSettings) { f(&s.RunSettings) }
func (f sharedOptionFunc) ApplyRun(s *RunSettings)   { f(s) }

type newOptionFunc func(*AgentSettings)

func (f newOptionFunc) ApplyNew(s *AgentSettings) { f(s) }

type callOptionFunc func(*RunSettings)

func (f callOptionFunc) ApplyRun(s *RunSettings) { f(s) }

// ============ 一个双作用域选项的完整写法 ============

// WithModel 设定模型。
// 用于 New 时是该 Agent 的默认模型；用于 Run/Stream 时仅覆盖本次执行。
func WithModel(m string) SharedOption {
	return sharedOptionFunc(func(s *RunSettings) { s.SetModel(m) })
}

// ============ 一个构造处专属选项的完整写法 ============

// WithThreadStore 注入 Thread 存储，启用有状态对话。
// 仅构造处有效；传给 Run/Stream 是编译错误（missing method ApplyRun）。
func WithThreadStore(st threadstore.Store) Option {
	return newOptionFunc(func(s *AgentSettings) { s.SetThreadStore(st) })
}

// ============ New / Run 侧的消费 ============

func New(d driver.Driver, opts ...Option) *Agent {
	s := defaultAgentSettings()
	for _, o := range opts {
		o.ApplyNew(&s)
	}
	return &Agent{driver: d, defaults: s}
}

func (a *Agent) Run(ctx context.Context, prompt string, opts ...CallOption) (*Result, error) {
	eff := a.defaults.RunSettings.clone() // 深拷贝 slice/map，跨 run 无污染
	for _, o := range opts {
		o.ApplyRun(&eff)
	}
	return a.execute(ctx, prompt, a.defaults.withRunOverrides(eff))
}

// Stream / Thread 的调用处签名同为 ...CallOption。
```

生态包（以 delegation 为例）的对应写法：

```go
package delegation // hosttools/a2adelegation

// Option 返回构造处专属选项：委托服务接入只在 New 有意义。
func (s *Service) Option() adaptor.Option { return serviceOption{svc: s} }

type serviceOption struct{ svc *Service }

func (o serviceOption) ApplyNew(set *adaptor.AgentSettings) {
	set.AddServices(o.svc.runtimeServiceSpec())
}
```

## 7. 遗留问题（不阻塞 D7 定案）

1. **`SharedOption` 命名**：备选 `DualOption`。`SharedOption` 语感为「两个作用域共享」，与 gRPC `CallOption` 先例搭配自然，暂定之；P0.5 落地前可低成本改名。
2. **单个选项的作用域归类需逐项复核**：本任务描述以 `WithModel` 为双作用域示例，而设计文档 §2.3 表格把它列为「仅 Run/Stream」。机制上两案都能表达任意归类，但 P0.5 实现前需把 ~24 个选项的 scope 清单逐个定稿（建议：`WithModel` 归双作用域——「这个 agent 默认用什么模型」是自然需求，归类调整后同步更新 §2.3 表格）。
3. **Thread 作用域**：`agent.Thread(key, adaptor.ResumeOnly())` 的选项属于第三个调用面。倾向复用 `CallOption`（Thread 也是调用处）+ Thread 专属选项单列 `ThreadOption` 小接口，P2 时按同一模式展开，不新增机制。
4. **`AgentSettings`/`RunSettings` 导出方法清单**即 v1 的官方扩展面契约，P0.5 定稿时需与 §2.3 的 24 个选项一一对应，并在 `driver`/生态包评审中冻结。
