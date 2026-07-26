# agent-adaptor v1 API 重设计提案 — 以 SDK 使用方为中心

> 状态：提案（未实施）。目标：抛弃历史包袱，从「易用、易理解、易扩展、可持续维护」出发重构公共 API。
> 本文所有「现状」代码均来自当前仓库的真实示例与文档；所有「新 API」代码是提案目标形态。

---

## 0. 一页结论

**设计哲学：API 的词汇表应该等于使用方脑子里那句话。**

一个要接入本地 coding agent 的产品团队，心里想的是：

> 「我要**让 codex 在这个仓库里干活**，**记住这段对话**，**把过程流式推给前端**，**危险操作要人审批**，**结果要能解析成结构体**。」

这句话里的每个名词/动词，都应该恰好对应一个 API 元素，而且只对应一个。新 API 一共 **6 个核心名词**：

| 名词 | 含义 | 取代现状中的 |
|---|---|---|
| `Agent` | 一个配置好的、可以直接对话的智能体 | `SDK` + `Runner` + `AgentBinding` + 命名注册表 |
| `Thread` | 一段有记忆的对话 | `SessionRequest{Namespace,Key,Mode}` + 4 种 mode option + SessionID 概念 |
| `Stream` | 一次流式执行的句柄 | `RunHandle` + `WithStreaming` 开关 |
| `Event` | 执行过程中发生的一件事 | `RunEvent` + `StreamPayload` + `DecisionRequest` 三套事件体系 |
| `Result` | 一次执行的结果 | `RunResult`（20+ 平铺字段） + `Failure` 双层错误模型 |
| `Driver` | 一种 agent CLI 的接入实现（扩展方专属） | `DriverAdapter` + 根包里的一堆 SPI 类型 |

**量化对比：**

| 维度 | 现状 | 新 API |
|---|---:|---:|
| 根包 `With*` 选项函数 | 66 个 | ~24 个 |
| 选项类型 | 3 种（`Option`/`AgentOption`/`RunOption`，调用点无法区分） | 1 套词汇、2 个作用域（同名选项在构造处=默认值，在调用处=本次覆盖） |
| 消费者必须理解的 ID 层级 | 4 层（ThreadID/SessionKey/SessionID/RunID） | 2 层（Thread key / Run ID） |
| 消费运行过程的通道 | 4 种（`Events()` / `StreamEvents()` / `DecisionRequests()`+`ResolveDecision` / 3×2 typed handler） | 1 条事件流 + 1 个可选审批回调 |
| 判定结果的步骤 | 3 步（先 `err`，再 `Result.Failure`，再成功） | 1 步（`if err != nil`，类型化错误携带细节） |
| 核心概念总数（消费者视角） | 40+ | ~13 |

---

## 1. 现状诊断：为什么必须重构

以下每一条都有仓库内证据，不是审美偏好。

### 1.1 三种 Option 类型共用同一命名风格，调用点无法区分

`options.go` 中并存 `Option`（SDK 级）、`AgentOption`（绑定级）、`RunOption`（单次调用级），全部叫 `agentadaptor.WithXxx(...)`。使用方写代码时**从函数名完全看不出**一个选项该放在 `New(...)`、`codex.New(...)` 还是 `Run(...)` 里，放错了是编译错误（还算好的）或语义偏差（更糟）。

由此派生了 16 对 `WithDefaultX` / `WithX` 重复选项（`WithDefaultSkills`/`WithSkills`、`WithDefaultMCP`/`WithMCP`……），66 个 `With*` 函数堆在同一个包的 godoc 里。

### 1.2 官方文档亲自记录了「真实生产宿主反复踩过的坑」

`docs/usage-guide.md` §7 需要一张四层 ID 对照表（ThreadID / SessionKey / SessionID / RunID）来解释身份体系，并特别注明「新人 grep `SessionKey` 找不到对应类型不是 bug」；§8 列出 **5 种真实生产宿主反复踩过的命名错误**（SessionStore 撞名、RunID 当 SessionID 用……）。

当官方文档需要为 API 写「防踩坑指南」时，说明抽象层级与使用方心智模型错位——这正是本次重构要消除的根因，而不是要继续用文档缓解的症状。

### 1.3 双层错误模型需要文档教学

`RunHandle.Wait` 的注释原文：「Hosts should check err first, then Failure, then success.」——判定一次执行成败需要三步，`RunResult.Failure`、`RunResult.Question`、`err` 三处分布。Go 使用方的肌肉记忆是 `if err != nil`，现状 API 与语言习惯相悖。

### 1.4 消费运行过程有 4 种并存机制

`Events()`（操作事件）、`StreamEvents()`（语义流，需 `WithStreaming()` 开启）、`DecisionRequests()` + `ResolveDecision(requestID, resp)`（异步 HITL）、以及 `WithPermissionHandler` / `WithPlanReviewHandler` / `WithQuestionHandler` ×（绑定级 + 调用级）6 个 typed handler 选项。使用方必须理解哪些通道必须 drain、哪些互斥、哪些组合。

### 1.5 结构性别扭

- 驱动配置在根包、构造器在子包：`codex.New(agentadaptor.CodexConfig{...})` 强制两个包一起 import。
- 命名注册表 `WithAgent("review", ...)` + `sdk.Agent("review") (Runner, error)` 用字符串查找 + 运行时错误，解决的是 Go 变量本来就能解决的问题（两个 agent 就是两个变量）。
- MCP 注入要写三层嵌套结构体；runtime service 的 MCP 注入靠 `agentadaptor.mcp.enabled=true` 这类 **stringly-typed metadata key**（见 `docs/api-reference.md` Run Options 一节）。
- 根包 godoc 混杂消费者 API、适配器作者 SPI（`DriverAdapter` + 10 个能力接口）、以及内部合同类型，初学者无从下手。

### 1.6 值得保留的优点（重构不是全盘否定）

以下现有设计决策在新 API 中**原样保留精神**：

- 单一执行路径不变式（合并默认值 → 组装 → 会话协调 → 执行 → checkpoint 持久化 → 归档）；
- 默认无状态、会话显式注入；
- 「真话能力汇报」：不支持的探针返回真实的 unsupported，而不是编造数据；
- `Output` / `RawStreams` / `Transcript` 分层，禁止互相污染；
- 严格的启动前失败（skill 物化失败即失败，不降级为 warning）；
- bridges（agui/sse/a2a）作为独立包、核心保持协议无关；
- `adaptertest` 一致性测试套件；
- A2A 边界的 ExposurePolicy 脱敏与最小暴露原则。

---

## 2. 新 API 全景

### 2.1 包布局：按「读者」分包，而不是按「功能」分包

```
github.com/agent-dance/agent-adaptor          → package adaptor   （应用开发者，~35 个导出名）
├── driver/                                    → SPI（适配器作者专属：Driver、RunRequest、EventSink、能力接口）
├── codex/  claude/  cursor/  codebuddy/       → 各驱动的 Config + Driver() 构造器（配置回归各自的包）
├── skill/                                     → skill.Dir / skill.FS / skill.Archive / skill.Inline / skill.Key / Provider 接口
├── mcp/                                       → mcp.HTTP / mcp.Stdio / mcp.Server
├── profile/                                   → profile.Native / Dedicated / Clone / 资源声明（子 agent、hooks、config patch）
├── threadstore/  memory/                      → Thread 存储接口与内置实现
├── bridges/{sse,agui,a2a,subagentstream}/     → 传输桥（现 pkg/bridges 提升一级）
├── hosttools/{a2adelegation,sessionrecorder}/ → 宿主可选组件（现 pkg/hosttools）
├── clients/a2a/                               → A2A 客户端（现 pkg/clients/a2a 提升一级）
└── adaptertest/                               → 驱动一致性测试套件
```

原则：**应用开发者只需要根包 + 一个驱动包就能完成 80% 的场景**；`skill` / `mcp` / `profile` 是需要时才 import 的「词汇扩展包」；`driver` 包把 SPI 从消费者视野里彻底移走。

根包 package name 由 `agentadaptor` 改为 `adaptor`（import path 不变，goimports 自动处理别名）。备选：保持 `agentadaptor`，此项为低成本可逆决策。

### 2.2 Agent：构造即可用，没有中央 SDK 对象

```go
agent := adaptor.New(codex.Driver(codex.Config{Model: "gpt-5.4"}),
    adaptor.WithWorkspace("/repo"),
)

res, err := agent.Run(ctx, "fix the failing tests")
```

- `adaptor.New(d driver.Driver, opts ...Option) *Agent` 是唯一构造入口——学一次，所有驱动通用。根包提供别名 `type Driver = driver.Driver`，宿主在结构体字段等处引用该类型时无需 import SPI 包。
- 驱动包只提供 `codex.Driver(codex.Config) driver.Driver`（以及给扩展作者的低层 `codex.NewAdapter`）。配置类型回归驱动自己的包。
- **删除命名注册表**。多 agent = 多个 Go 变量；需要注册表的宿主自己写 `map[string]*adaptor.Agent`，这本来就是一行 Go。
- 原 SDK 级注入（ThreadStore / WorkspaceManager / SkillProvider / ServiceManager / 事件缓冲）全部变为 Agent 构造选项；多个 Agent 共享同一个 store/manager 实例即可实现今天「一个 SDK 多个 binding」的共享效果。

### 2.3 选项系统：一套词汇、两个作用域

**同一个选项，用在 `New(...)` 是这个 Agent 的默认值，用在 `Run/Stream/Thread(...)` 是本次覆盖。**

```go
// 构造时：默认策略
agent := adaptor.New(claude.Driver(cfg),
    adaptor.WithPolicy(adaptor.Policy{Sandbox: adaptor.ReadOnly}),
    adaptor.WithSkills(skill.Dir("./skills/write-proof")),
)

// 调用时：同一个函数名，本次放开写权限、追加一个技能
res, err := agent.Run(ctx, prompt,
    adaptor.WithPolicy(adaptor.Policy{Sandbox: adaptor.WorkspaceWrite}),
    adaptor.WithSkills(skill.Key("deploy-checklist")),
)
```

实现上采用三接口定稿（P0.1 spike 已验证，详见 [p0-option-scope-decision.md](./p0-option-scope-decision.md)）：`Option`（New 接受的全集）、`CallOption`（Run/Stream 接受，**有意不嵌入** Option）、`SharedOption`（双作用域，同时满足两者）。作用域非法的组合双向都是编译错误（如 `WithThreadStore` 用在 Run 上、仅调用处选项用在 New 上），IDE 即时红线，godoc 的返回类型即作用域文档。16 对 `WithDefaultX/WithX` 就此消失，语义规则只有一句话：**「近处覆盖远处；skills 追加、其余替换」**（与现状合并语义一致）。

核心选项词汇（全集，约 24 个）：

| 类别 | 选项 | 作用域 |
|---|---|---|
| 环境 | `WithWorkspace(dir)` / `WithWorkspaceSpec(spec)` | 双 |
| 能力 | `WithSkills(refs...)` `WithMCP(servers...)` `WithInstructions(text)` | 双 |
| 策略 | `WithPolicy(Policy{Sandbox, WebSearch, Browser, Approvals})` | 双 |
| 审批 | `OnApproval(handler)` | 双 |
| 服务 | `WithServices(specs...)` | 双 |
| 标注 | `WithMetadata(k, v)` `WithIdentity(Identity{...})` | 双 |
| 资源 | `WithProfileResources(profile.Resources{...})` | 双 |
| 模型 | `WithModel(m)` `WithTimeout(d)` | 双 |
| 单次 | `WithSchema[T](...)` `WithoutTokenStream()` | 仅 Run/Stream |
| 构造 | `WithThreadStore(s)` `WithProfile(profile.Dedicated(dir))` `WithWorkspaceManager(m)` `WithSkillProvider(p)` `WithSkillMaterializer(m)` `WithServiceManager(m)` `WithEventBuffer(n)` `WithBlockingEvents()` | 仅 New |

`skill` / `mcp` 包提供一行式构造器，消灭嵌套结构体：

```go
adaptor.WithMCP(
    mcp.HTTP("docs", "https://example.com/mcp"),
    mcp.Stdio("repo-tools", "npx", "repo-mcp"),
)
adaptor.WithSkills(skill.Dir("./skills/write-proof"), skill.Key("code-review"))
```

### 2.4 Thread：对话就是对话，不是 (Namespace, Key, Mode) 三元组

```go
agent := adaptor.New(claude.Driver(cfg), adaptor.WithThreadStore(memory.NewStore()))

th := agent.Thread("tenant-1/issue-123")     // 有则续、无则建（continue_or_start）
res, err := th.Run(ctx, "continue the fix")

fresh := agent.NewThread("tenant-1/issue-123")        // 强制新开（start_new）
locked := agent.Thread("k", adaptor.ResumeOnly())     // 只续不建（continue_only）
branch := th.Fork("tenant-1/issue-123-alt")           // 分叉（fork）
```

- Thread key 是宿主自己的业务字符串（多租户自行拼 `"tenant/key"` ——现状 SSE bridge 的 wire 格式 `"ns/thread-id"` 已经证明这就是宿主的真实用法）。
- 4 种 SessionMode 变成 4 个**有名字的动作**，不再是传给结构体的枚举。
- `SessionID`（驱动 resume 句柄）降级为内部实现细节，需要审计的宿主用 `th.Checkpoint()` 获取。四层 ID 变两层：**你起的名字（thread key）和 SDK 给的执行号（run ID）**。
- `Thread` 与 `Agent` 都实现 `Runner` 接口（`Run` + `Stream`），所以 bridges、`RunAs[T]`、宿主工具对两者一视同仁。
- `threadstore.Store` 接口保留现状 SessionStore 的全部能力（resolve / finalize / lease 防并发），只是命名对齐；`memory` 包照旧。

### 2.5 Stream + Event：一条事件流，一次 for-range

`Run` = 批处理语义，`Stream` = 流式语义。**动词本身就是开关**，`WithStreaming()` / `WithoutStreaming()` / `WithDefaultStreaming()` 三个选项、以及 `Events()` 与 `StreamEvents()` 两条通道的辨析，全部删除。

```go
stream := th.Stream(ctx, userMsg)

for ev := range stream.Events() {
    switch e := ev.(type) {
    case adaptor.TextDelta:
        io.WriteString(w, e.Text)
    case adaptor.ToolCall:
        renderToolCard(e.Name, e.Args)
    case adaptor.Thinking:
        renderReasoning(e.Text)
    case *adaptor.ApprovalRequest:
        e.Approve(ctx)                        // 审批请求自带应答能力，见 §2.6
    }
}

res, err := stream.Result()                    // 收口：最终结果 + 错误一次拿到
```

- 事件是**带类型的 Go 值**（type switch 分发），不是 `Kind string + map[string]any`。
- 原 `RunEvent` 的操作事件（进程 spawn、原始 chunk、生命周期、runtime 服务）成为同一条流上的低频事件类型（`RunStarted` / `RunFinished` / `ProcessInfo` / `Notice` / `Dropped{Count}`），要不要处理由使用方的 switch 决定——不处理就自然忽略，**不再有「必须 drain 两条 channel」的隐性义务**。
- `stream.RunID()` 立即可用；取消 = cancel ctx 或 `stream.Cancel()`；背压策略仍由构造选项控制（默认丢弃+`Dropped` 标记，可选阻塞）。
- 驱动能力差异（token 级 / 工具参数流 / reasoning）保留 `StreamCapability` 真话降级机制，bridges 照旧兜底。

### 2.6 审批（HITL）：请求自己会应答

现状的 requestID 簿记 + `ResolveDecision` + kind 不匹配错误 + 3×2 handler 选项，收敛为两种自然形态：

**形态 A —— 回调（程序化策略 / 终端应用）：**

```go
res, err := agent.Run(ctx, "refactor the auth module",
    adaptor.OnApproval(func(ctx context.Context, req *adaptor.ApprovalRequest) error {
        if req.Kind == adaptor.ApprovalPermission && req.Risk() < adaptor.RiskHigh {
            return req.Approve(ctx)
        }
        fmt.Printf("[%s] %s\n(y/N): ", req.Kind, req.Title)
        if askUser() { return req.Approve(ctx) }
        return req.Deny(ctx, "operator rejected")
    }),
)
```

**形态 B —— 事件（Web UI 异步审批）：**

```go
case *adaptor.ApprovalRequest:
    pending.Store(e.ID, e)          // 存下请求本身
    pushCardToBrowser(e)            // 推审批卡片
// 浏览器回包后，在 HTTP handler 里：
req, _ := pending.Load(id)
req.(*adaptor.ApprovalRequest).Answer(ctx, chosenOption)
```

- `ApprovalRequest` 统一三种 Kind（Permission / PlanReview / Question），带 `Approve` / `Deny(reason)` / `Answer(option)` 方法，**应答器就在请求上**，没有跨对象的 requestID 往返，kind 不匹配在方法级即不可能发生。
- 超时 / 重试 / 兜底策略并入 `Policy.Approvals`（现 `HumanDecisionPolicy` 语义不变）。
- 预设策略：`adaptor.ApproveAll()`、`adaptor.DenyAll(reason)` 作为现成 handler。

### 2.7 Result 与错误模型：一个 err，一层判断

```go
res, err := agent.Run(ctx, prompt)
if err != nil {
    var runErr *adaptor.RunError
    if errors.As(err, &runErr) {
        // agent 完整跑完但业务失败：runErr.Reason ∈ {ApprovalDenied, ApprovalTimeout, PolicyViolation, ...}
        // 部分结果仍可访问：runErr.Result
        log.Warn("run failed", "reason", runErr.Reason, "summary", runErr.Result.Summary)
    }
    return err // 基础设施失败（ctx 取消、进程崩溃、协议破裂）同样走这里
}
fmt.Println(res.Text)
```

- 「业务失败」不再是成功返回值里的 `Failure` 字段，而是**类型化的 error**（携带完整 `Result`），与 Go 的 `*exec.ExitError` 惯例一致。使用方只有一条判定路径。
- 哨兵错误保留匹配能力：`errors.Is(err, adaptor.ErrApprovalDenied)` 等。
- `Result` 瘦身分组：高频字段平铺（`Text` / `Summary` / `Usage` / `Model`），审计字段收拢（`res.Raw()` 返回 stdout/stderr/终端 payload；`res.Transcript()`；`res.Services()`），`res.Decode(&v)` 解结构化输出。字段能力一个不丢，godoc 首屏只有使用方每天要看的东西。

### 2.8 结构化输出：一个泛型动词

```go
type Review struct {
    Verdict string   `json:"verdict"`
    Issues  []string `json:"issues"`
}

review, res, err := adaptor.RunAs[Review](ctx, reviewer, "review the diff")
```

- `RunAs[T]` 接受任何 `Runner`（Agent 或 Thread），内部完成 schema 派生、模式协商、校验、解码。
- 流式/手动场景用选项：`agent.Stream(ctx, p, adaptor.WithSchema[Review]())` + `res.Decode(&review)`。
- 三种执行模式收敛为 `WithSchema` 的模式参数：`schema.Strict()`（默认，仅 provider 原生约束）/ `schema.Flexible()`（原生优先，允许提示词+本地校验回退）/ `schema.PromptOnly()`。能力矩阵仍由 `driver.Descriptor` 真话声明，不支持即启动前报错。

### 2.9 Inspect / Profile：管理面按用途命名

```go
env,    _ := agent.Inspect().Environment(ctx)   // onboarding 体检
models, _ := agent.Inspect().Models(ctx)        // 设置界面下拉框
quota,  _ := agent.Inspect().Quota(ctx)         // 余量展示
schema, _ := agent.Inspect().ConfigSchema(ctx)  // 动态设置表单
skills, _ := agent.Inspect().Skills(ctx)        // 技能清单（含未选中）

snap, _ := agent.ProfileState(ctx)              // desired vs observed 资源状态
snap, _ = agent.SyncProfile(ctx)                // 不跑 prompt 的资源物化
```

- `Admin()` 这个名字消失（它从来不是「管理员」，是只读探针 + profile 同步）。
- Profile 选择在构造期声明：`adaptor.WithProfile(profile.Native())` / `profile.Dedicated(dir)` / `profile.CloneNative(dir, profile.LinkAuth())` / `profile.CloneFrom(src, dst, ...)`——语义与现状 4 个 profile option 一一对应，`LinkAuth` 保留 OAuth 登录态共享。
- `SetSelectedSkills` 的进程内覆盖语义保留为 `agent.SelectSkills(ctx, keys)`，文档同样强调「持久化偏好是宿主的事」。

### 2.10 Driver SPI（扩展作者）：搬家、不减能力

`driver` 包收纳全部 SPI：

```go
package driver

type Driver interface {
    Descriptor() Descriptor
    Validate(cfg any) error
    Run(ctx context.Context, req Request, sink EventSink) (Response, error)
}
// 能力接口原样保留：EnvironmentProbe / ModelLister / ModelDetector / ProfileReporter /
// QuotaProbe / ConfigSchemaProvider / SkillSupport / StreamSupport / SessionCodec
```

- 消费者的 godoc 里再也看不到 `ResolvedSkills` / `EventSink` / `DriverRunRequest`。
- `adaptor.Bind` / `BindTyped` 不再需要——`adaptor.New(myDriver, opts...)` 对第三方驱动与内置驱动完全同构。
- `adaptertest` 升级为对 `driver.Driver` 的一致性套件，第三方驱动的验收路径不变。
- RuntimeServiceRef 发布 MCP 的通道由 stringly metadata 升级为类型化字段 `RuntimeServiceRef.MCP *mcp.Server`（旧 metadata key 在迁移期兼容解析）。

### 2.11 Bridges 与宿主工具：接口换新词，分层不变

```go
http.Handle("/v1/chat", sse.Handler(agent))          // Runner 接口，Agent/Thread 皆可
srv := a2a.NewServer(agent, a2a.ServerOptions{...})  // A2A 发布不变
for ev := range agui.Events(stream) { ... }          // AG-UI 翻译层不变
```

subagentstream / a2adelegation / sessionrecorder 保持宿主可选层定位，仅适配新事件类型。A2A 客户端包不变（它本来就是协议形状的，没有历史包袱）。

一项升级：`hosttools/a2adelegation` 在保留 Registry / EventBus / Delegator / MCPServer 各组件的同时，新增开箱即用的 `delegation.Service` 一体化入口（组件组装 + per-run MCP sidecar 生命周期 + 结果记录），其过程事件以 `SubagentUpdate` 事件汇入宿主的主事件流——详见 S9，这是 team-agent-workflow showcase 里 323 行手写样板的直接来源。委托目标既可以是远程 A2A 卡片（`delegation.Remote`），也可以是进程内的任何 `Runner`（`delegation.Local`，见 §9.8）——现状 Registry 仅支持 `ProtocolA2A`，进程内角色也被迫先发布成 HTTP 服务再被自己人调用。

---

## 3. 真实业务场景 Before / After

### S1 · CLI 工具：一次性任务

**现状（examples/codex-basic 形态）：**

```go
sdk := agentadaptor.New(
    agentadaptor.WithDefaultAgent(codex.New(agentadaptor.CodexConfig{Model: "gpt-5.4"})),
)
result, err := sdk.Run(ctx, "fix the failing tests")
if err != nil { ... }
if result.Failure != nil { ... }        // 第二层判断，容易漏
fmt.Println(result.Output)
```

**新 API：**

```go
agent := adaptor.New(codex.Driver(codex.Config{Model: "gpt-5.4"}))

res, err := agent.Run(ctx, "fix the failing tests")
if err != nil { ... }                    // 唯一判断点
fmt.Println(res.Text)
```

概念数 6 → 3；不可能再漏检业务失败。

### S2 · 多 agent 流水线：Codex 实现、Claude 评审

**现状：** 注册表 + 字符串查找 + 两处错误处理（见 README「Multiple Agents」）。

**新 API：agent 就是变量。**

```go
coder    := adaptor.New(codex.Driver(codex.Config{Model: "gpt-5.4"}))
reviewer := adaptor.New(claude.Driver(claude.Config{Model: "claude-sonnet-4"}),
    adaptor.WithPolicy(adaptor.Policy{Sandbox: adaptor.ReadOnly}),   // 评审者天然只读
)

patch, err := coder.Run(ctx, "implement the fix")
if err != nil { return err }

review, _, err := adaptor.RunAs[Review](ctx, reviewer,
    "review this patch:\n"+patch.Text)
if err != nil { return err }
if review.Verdict != "approve" { ... }
```

注册表、`sdk.Agent("review")` 的运行时错误、reviewer 的类型断言全部消失；评审结果直接是结构体。

### S3 · Web 聊天服务：多轮对话 + SSE 流式 + 前端审批卡片

**现状：** `WithSessionStore` + `WithSessionKey(ns,key)` + `Start` + `WithStreaming()` + 区分 `Events`/`StreamEvents` + `DecisionRequests` channel + `ResolveDecision(requestID, ...)`，四层 ID 对照表就是为这个场景写的。

**新 API：**

```go
agent := adaptor.New(claude.Driver(claude.Config{Model: "claude-sonnet-4"}),
    adaptor.WithThreadStore(memory.NewStore()),
)

// 快速路线：一行起服务（现有 sse bridge 能力保留）
http.Handle("/v1/chat", sse.Handler(agent))

// 自定义路线：完全掌控
func chat(w http.ResponseWriter, r *http.Request) {
    th := agent.Thread(threadKeyFrom(r))              // 有则续、无则建
    stream := th.Stream(r.Context(), promptFrom(r))

    for ev := range stream.Events() {
        switch e := ev.(type) {
        case adaptor.TextDelta:
            sseWrite(w, "delta", e.Text)
        case adaptor.ToolCall:
            sseWrite(w, "tool", e)
        case *adaptor.ApprovalRequest:
            pending.Store(e.ID, e)                    // 请求自带应答器
            sseWrite(w, "approval", e)
        }
    }
    if _, err := stream.Result(); err != nil {
        sseWrite(w, "error", err.Error())
    }
}

// 审批回包端点
func resolve(w http.ResponseWriter, r *http.Request) {
    if req, ok := pending.LoadAndDelete(idFrom(r)); ok {
        req.(*adaptor.ApprovalRequest).Answer(r.Context(), optionFrom(r))
    }
}
```

「重新生成」按钮 = `th.Fork(newKey)`，一个方法调用，不需要理解 SessionMode 枚举与 fingerprint。

### S4 · 后台批量 worker：每个任务覆盖模型 / 超时 / 审计标签

```go
agent := adaptor.New(codex.Driver(codex.Config{Model: "gpt-5.4"}),
    adaptor.WithPolicy(adaptor.Policy{Sandbox: adaptor.WorkspaceWrite, Approvals: adaptor.ApprovalsAutoDeny}),
)

for job := range jobs {
    res, err := agent.Run(ctx, job.Prompt,
        adaptor.WithWorkspace(job.RepoDir),
        adaptor.WithModel(job.Model),              // 便宜任务用小模型
        adaptor.WithTimeout(10*time.Minute),
        adaptor.WithMetadata("job", job.ID),
    )
    record(job, res, err)                          // err 一元判定，含类型化失败原因
}
```

同一套 `With*` 词汇，构造处是舰队默认值，调用处是单任务覆盖——不需要查「这个选项是 AgentOption 还是 RunOption」。

### S5 · issue 分类器：结构化输出直出业务结构体

**现状：** `WithJSONSchemaOutputFor[T](NativeStrictOutput(), StructuredOutputName(...))` + `DecodeStructuredOutput[T](res)` 两段式。

**新 API：**

```go
type Triage struct {
    Severity  string   `json:"severity"`
    Component string   `json:"component"`
    Duplicate *string  `json:"duplicate_of"`
}

triage, _, err := adaptor.RunAs[Triage](ctx, agent, "triage this issue:\n"+issueBody)
```

### S6 · 把本地 agent 发布为 A2A 服务（对外能力不变）

```go
agent := adaptor.New(codex.Driver(cfg), adaptor.WithThreadStore(store))

srv := a2a.NewServer(agent, a2a.ServerOptions{
    AgentCard: a2a.AgentCard{Name: "Local Codex", ...},
    Session:   a2a.ThreadByContextID(),
})
http.Handle("/a2a", srv.Handler())
```

ExposurePolicy 脱敏、任务保留策略、可视化委托（a2adelegation + subagentstream）全部保留，仅内部适配新事件模型。

### S7 · 设置界面 / Onboarding 向导

```go
env, _ := agent.Inspect().Environment(ctx)
if !env.Ready {
    wizard.Show(env.Problems)                      // CLI 没装 / 没登录 / profile 缺失
}
models, _ := agent.Inspect().Models(ctx)           // 下拉框
fields, _ := agent.Inspect().ConfigSchema(ctx)     // 动态表单
quota,  _ := agent.Inspect().Quota(ctx)            // 余量条
```

与现状能力一一对应，但入口名（`Inspect`）说明了它是什么：只读探针，不是「Admin」。

### S8 · 租户隔离的专用 profile（桌面产品常见）

```go
agent := adaptor.New(claude.Driver(cfg),
    adaptor.WithProfile(profile.CloneNative(
        filepath.Join(appData, "profiles", tenantID),
        profile.LinkAuth(),                        // 共享本机 OAuth 登录态，不复制 token 文件
    )),
    adaptor.WithProfileResources(profile.Resources{
        Instructions: profile.Text("Follow ACME coding standards."),
        SubAgents:    []profile.SubAgent{{Key: "tester", Instructions: "..."}},
    }),
)
```

### S9 · 团队协作 showcase：Claude 领队 + plan/impl/review 三个 A2A 角色（team-agent-workflow 全量重构）

> 现状代码：`examples/showcases/team-agent-workflow/`（分支 cl/opt_examples）——main.go 463 行 + roles.go 326 行 + delegation_runtime.go 323 行，共 1112 行核心编排（另有 fixture / trace / console 辅助）。这是仓库最复杂的展示案例，现状 API 的每一处摩擦在这里集中出现，故单列一章做全量对照。

**场景**：Claude 领队通过宿主注入的 `delegate_to_agent` MCP 工具，按 plan(Codex, 只读) → impl(Claude, 可写) → review(Codex, 只读) 顺序调度三个以 A2A 发布的角色；MCP sidecar 按 run 创建、带 per-run bearer token；远端执行过程实时回流渲染但**不进入领队的模型上下文**；工作区阶段边界与 review 结论由宿主审计。

**现状仪式清单：**

| # | 环节 | 现状形态 | 成本 |
|---|---|---|---|
| 1 | 领队构造 | `ClaudeConfig`（根包）→ `claude.New(cfg, 7 个 WithDefault*)` → 单独的 `newLeaderSDK`（`New` + `WithDefaultAgent` + `WithRuntimeServiceManager` + 条件 `WithSessionStore`） | 4 层对象、3 类选项混用 |
| 2 | 角色发布 | 每个角色 binding + **整个 SDK 实例**（`agentadaptor.New(agentadaptor.WithDefaultAgent(binding))`）只为 `.Default()` 取 Runner | 4 个 SDK 实例 |
| 3 | MCP sidecar | 手写 323 行 RuntimeServiceManager：`net.Listen` + `http.Server` + serveErr channel + 8 个 `agentadaptor.mcp.*` 魔法字符串 + 手拼 `RuntimeServiceRef` / `SecretEnv` + Release/Close 生命周期 | 最重的一块样板 |
| 4 | 委托结果回收 | `auditMCPCalls` 拦截 MCP HTTP body、手工 JSON 解析 `tools/call` 响应提取 `DelegationResult`（~90 行） | 协议层打洞 |
| 5 | 过程消费 | `Start(WithStreaming)` 后手工管理 3 个 goroutine（trace 订阅 bus + 空转 drain `Events()` + 渲染 `StreamEvents()`）+ `sync.WaitGroup` + `bus.ClearRun` | 双通道 drain 义务 + 手工并发 |
| 6 | 结果判定 | `waitErr` 与 `result.Failure` 两处检查（领队与角色日志各一套） | 双层错误模型 |
| 7 | 角色观测 | `observedRoleRunner` 实现 Run/Start + `observedRoleHandle` 包 Wait，3 个拦截点、~45 行 | 接口面太宽 |
| 8 | 工具超时 | `withMCPToolTimeout` 手工克隆 Env 追加 `MCP_TOOL_TIMEOUT` | 环境变量侧信道 |

#### 9.1 委托服务：323 行手写运行时 → 一次配置

**现状（delegation_runtime.go 节选）：**

```go
// 每个想暴露 delegate_to_agent 的宿主都要重写一遍的样板
metadata := map[string]string{
    "agentadaptor.mcp.enabled":              "true",
    "agentadaptor.mcp.key":                  "team-delegation",
    "agentadaptor.mcp.transport":            string(agentadaptor.MCPTransportHTTP),
    "agentadaptor.mcp.url":                  "http://" + listener.Addr().String() + "/mcp",
    "agentadaptor.mcp.bearer_token_env_var": delegationTokenEnv,
    "agentadaptor.mcp.required":             "true",
    // ...
}
return []agentadaptor.RuntimeServiceRef{{
    ID: req.RunID + ":team-delegation", /* ... */ Metadata: cloneLabels(metadata),
    SecretEnv: []agentadaptor.EnvBinding{{Name: delegationTokenEnv, Value: token}},
}}, nil
```

外加手工 `net.Listen` / `http.Server` / serveErr channel / `ReleaseByRun` / `ReleaseByLabels` / `Close` 生命周期，以及为了拿回每次委托的 `DelegationResult` 而对 MCP HTTP body 做的 JSON 拦截解析。

**新 API：**

```go
team, err := delegation.NewService(delegation.Config{
    Agents:      remoteSpecs,                       // plan/impl/review 的 RemoteAgentSpec（协议形状不变）
    ToolTimeout: opts.roleTimeout + 30*time.Second, // 取代 withMCPToolTimeout 的 env 侧信道
    Observe:     func(ev delegation.Event) { term.Live(ev) },
})
defer team.Close()
```

- `delegation.Service` 是 `hosttools/a2adelegation` 的新一体化入口：Registry + EventBus + Delegator + per-run MCP sidecar（随机 bearer token、监听器、http.Server 生命周期）+ 结果记录收进一个类型。**这段每个宿主都需要、示例被迫手写的 323 行成为 SDK 能力**；分立组件仍导出，深度定制不受限。
- sidecar 对 run 的注入走类型化 `RuntimeServiceRef.MCP`（§2.10），8 个 stringly key 消失。
- 每次委托的最终结果由服务记录：`team.Result(runID, "review")` 直接取，取代 90 行 HTTP body 拦截。

#### 9.2 领队构造：四层对象 → 一个变量

**现状（main.go 节选）：**

```go
leaderBinding := claude.New(agentadaptor.ClaudeConfig{
    CommonConfig: agentadaptor.CommonConfig{Command: leaderCfg.Command, CWD: leaderCfg.CWD, /*...*/},
    Model: leaderCfg.Model, PersistentProcess: true,
},
    fixture.CloneProfileOption("leader-claude"),
    agentadaptor.WithDefaultWorkspace(agentadaptor.SharedWorkspace{}),
    agentadaptor.WithDefaultIdentity(agentadaptor.AgentIdentity{ID: "team-leader", TenantID: "example", /*...*/}),
    agentadaptor.WithDefaultRuntimeServices(agentadaptor.RuntimeServiceSpec{
        ID: "team-delegation-mcp", Name: "team-delegation",
        Lifecycle: agentadaptor.RuntimeLifecycleEphemeral, /*...*/
    }),
    agentadaptor.WithDefaultMetadata("example", "team-agent-workflow"),
    agentadaptor.WithDefaultMetadata("workflow_role", "leader"),
)
leaderSDK := newLeaderSDK(leaderBinding, runtimeManager, opts.webMode) // 又一层封装
```

**新 API：**

```go
leader := adaptor.New(
    claude.Driver(claude.Config{Model: cfg.Model, PersistentProcess: true}),
    adaptor.WithProfile(profile.CloneNative(fixture.ProfileDir("leader"), profile.LinkAuth())),
    adaptor.WithWorkspace(fixture.WorkspaceDir),
    adaptor.WithPolicy(adaptor.Policy{Sandbox: adaptor.ReadOnly, Approvals: adaptor.ApprovalsAutoDeny}),
    adaptor.WithIdentity(adaptor.Identity{Tenant: "example", User: "team-leader"}),
    adaptor.WithMetadata("example", "team-agent-workflow"),
    adaptor.WithMetadata("workflow_role", "leader"),
    team.Option(),      // 委托服务：service 声明 + manager + 过程事件汇入，一个选项接完
)
```

- `team.Option()` 展示 Option 作为接口的扩展性红利：**生态包可以发行自己的选项**。它一次接入三件事——runtime service 声明（现状要 `WithDefaultRuntimeServices` + `WithRuntimeServiceManager` 两处）、sidecar 生命周期、以及把委托过程事件汇入领队事件流（见 9.4）。
- 隔离策略从每次调用的 `exampleutil.NonInteractiveRunOption(IsolationReadOnly)` 上移为构造期 `WithPolicy`——领队本来就永远只读。
- web 模式的差异收敛为两个追加选项：`adaptor.WithThreadStore(memory.NewStore())` + `adaptor.WithInstructions(leaderProtocol)`。

#### 9.3 角色发布：每角色一个 SDK → 每角色一个变量

**现状（roles.go 节选）：**

```go
binding := exampleutil.NewLiveAgentBinding(role.Config,
    cfg.Fixture.CloneProfileOption(role.Key+"-"+role.Provider),
    agentadaptor.WithDefaultWorkspace(agentadaptor.SharedWorkspace{}),
    agentadaptor.WithDefaultIdentity(agentadaptor.AgentIdentity{/*...*/}),
    agentadaptor.WithDefaultMetadata("example", "team-agent-workflow"),
    agentadaptor.WithDefaultMetadata("workflow_role", role.Key),
)
roleSDK := agentadaptor.New(agentadaptor.WithDefaultAgent(binding))   // 整个 SDK 只为 .Default() 取 Runner
server := bridgea2a.NewServer(observeRoleRunner(role.Key, roleSDK.Default(), hub.Audit.Record),
    bridgea2a.ServerOptions{
        AgentCard: /*...*/, Session: bridgea2a.Stateless(), Prompt: rolePromptBuilder(role, cfg.Fixture),
        Exposure:  bridgea2a.ExposurePolicy{IncludeToolCalls: true, IncludeReasoning: true},
        RunOptions: []agentadaptor.RunOption{exampleutil.NonInteractiveRunOption(role.Isolation)},
        TaskLifecycle: /*...*/,
    })
```

**新 API：**

```go
role := adaptor.New(def.Driver,     // codex.Driver(...) 或 claude.Driver(...)
    adaptor.WithProfile(profile.CloneNative(fixture.ProfileDir(def.Key), profile.LinkAuth())),
    adaptor.WithWorkspace(fixture.WorkspaceDir),
    adaptor.WithPolicy(adaptor.Policy{Sandbox: def.Sandbox, Approvals: adaptor.ApprovalsAutoDeny}),
    adaptor.WithInstructions(def.Instructions),
    adaptor.WithMetadata("workflow_role", def.Key),
)
srv := a2a.NewServer(observe(def.Key, role, hub.Audit.Record), a2a.ServerOptions{
    AgentCard: def.Card(baseURL),
    Session:   a2a.Stateless(),
    Prompt:    rolePrompt(def, fixture),
    Exposure:  a2a.ExposurePolicy{IncludeToolCalls: true, IncludeReasoning: true},
})
mux.Handle(def.CardPath(), srv.AgentCardHandler())
mux.Handle(def.RPCPath(), srv.Handler())
```

- per-role SDK 消失；`a2a.NewServer` 直接吃 `Runner`——Agent、Thread、装饰器同构。
- 角色的只读/可写沙箱是角色的**固有属性**，落在构造期 `WithPolicy`；`ServerOptions.RunOptions` 转发层不再需要。
- `RemoteAgentSpec` / `DelegationPolicy`（MaxTimeout / RequireStreaming / MaxArtifactBytes）保持原样——它们是协议形状，没有历史包袱。

#### 9.4 过程消费：3 个手工 goroutine → 1 个 for-range

**现状（main.go CLI 模式节选）：**

```go
handle, err := leaderSDK.Start(ctx, leaderPrompt(opts.roleTimeout),
    exampleutil.NonInteractiveRunOption(agentadaptor.IsolationReadOnly),
    agentadaptor.WithStreaming(),
)
// ...
traceDone := make(chan struct{})
go func() { trace.Collect(bus.SubscribeRun(ctx, handle.RunID())); close(traceDone) }()

var drains sync.WaitGroup
drains.Add(2)
go drainRunEvents(handle.Events(), &drains)           // 空转丢弃，纯粹为了不阻塞
go renderLeaderStream(handle.StreamEvents(), &drains) // 按 ev.Kind switch 渲染

result, waitErr := handle.Wait(ctx)
drains.Wait()
bus.ClearRun(handle.RunID())
<-traceDone
if waitErr != nil { return fmt.Errorf("wait for Claude leader: %w", waitErr) }
if result.Failure != nil { return fmt.Errorf("Claude leader failed: %s", result.Failure.Message) }
```

**新 API：**

```go
stream := leader.Stream(ctx, leaderPrompt(opts.roleTimeout))

trace := newWorkflowTrace()
for ev := range stream.Events() {
    switch e := ev.(type) {
    case adaptor.TextDelta:      term.Print(e.Text)
    case adaptor.Thinking:       term.Reasoning(e.Text)
    case adaptor.ToolCall:       term.Tool(e.Name, e.Args)
    case adaptor.SubagentUpdate: term.Live(e); trace.Add(e) // 委托过程与主流同一条流、同序到达
    case adaptor.Dropped:        term.Warnf("dropped %d events", e.Count)
    }
}

res, err := stream.Result()
if err != nil {
    return fmt.Errorf("Claude leader: %w", err) // 业务失败 = *RunError（含 Result 细节），基础设施失败同路
}

if err := trace.ValidateOrderedRoles("plan", "impl", "review"); err != nil { return err }
review, ok := team.Result(stream.RunID(), "review")
if !ok || !review.HasLine(reviewApprovalSentinel) {
    return fmt.Errorf("review did not approve; leader_output=%q", preview(res.Text, 1200))
}
```

- 空转 drain `Events()` 的义务、`sync.WaitGroup`、`bus.SubscribeRun` / `ClearRun` 簿记全部消失。
- 委托过程事件（started / text.delta / artifact / finished…）由 `team.Option()` 汇入同一条流，成为 `adaptor.SubagentUpdate` 事件——**CLI 模式与 web 模式从此消费同一形状**。现状是两套写法：CLI 手工订阅 bus，web 靠 `sse.Options.SubagentBus` 叠加；新 API 里 web 模式就是 `sse.Handler(leader)`，SubagentUpdate 已在流上，无需额外选项。
- 委托事件仍然只进宿主事件流、**不进领队模型上下文**（保持现有隔离原则）。实现要点：runtime service 在 Ensure 时已绑定 RunID，核心为宿主组件提供 per-run 事件注入口（内部 API），bridges 不再各自实现 overlay。

#### 9.5 角色观测：3 个拦截点 → 2 个方法

**现状：** `observedRoleRunner` 手工实现 `Run` / `Start`，再用 `observedRoleHandle` 包 `Wait`，共 3 个拦截点、~45 行；`logRoleResult` 里 `err` 与 `result.Failure` 双路径打印。

**新 API：** `Runner` 只有 `Run` + `Stream` 两个方法，且 `Stream` 定义为小接口（`Events` / `Result` / `RunID` / `Cancel`），装饰与测试替身就是普通 Go 代码：

```go
type observed struct {
    adaptor.Runner
    role   string
    record func(string)
}

func (o observed) Run(ctx context.Context, prompt string, opts ...adaptor.Option) (*adaptor.Result, error) {
    started := time.Now()
    res, err := o.Runner.Run(ctx, prompt, opts...)
    logRole(o.role, started, res, err) // err 一元判定；业务失败用 errors.As 取 *RunError.Result
    o.record(o.role)
    return res, err
}

func (o observed) Stream(ctx context.Context, prompt string, opts ...adaptor.Option) adaptor.Stream {
    return observedStream{Stream: o.Runner.Stream(ctx, prompt, opts...), o: o, started: time.Now()}
}

type observedStream struct {
    adaptor.Stream
    o       observed
    started time.Time
}

func (s observedStream) Result() (*adaptor.Result, error) {
    res, err := s.Stream.Result()
    logRole(s.o.role, s.started, res, err)
    s.o.record(s.o.role)
    return res, err
}
```

#### 9.6 量化对比

| 维度 | 现状 | 新 API |
|---|---:|---:|
| 中央对象 | 4 个 SDK 实例（领队 + 3 角色） | 0（4 个 `*adaptor.Agent` 变量） |
| 手写委托运行时 | 323 行（delegation_runtime.go） | ~6 行 `delegation.NewService` 配置 |
| stringly MCP metadata key | 8 个 | 0（类型化 `RuntimeServiceRef.MCP`） |
| CLI 模式手工 goroutine | 3 个 + WaitGroup + ClearRun 簿记 | 0 |
| 委托过程消费形状 | CLI / web 两套（手工订阅 bus vs `SubagentBus` 选项） | 一套（`SubagentUpdate` 事件） |
| 结果判定 | `err` + `Failure` 双检 ×2 处 | `if err != nil` |
| 观测装饰器 | 3 拦截点 / ~45 行 | 2 方法 / ~25 行 |
| 编排代码总量（main + roles + delegation_runtime） | 1112 行 | 粗估 ~450 行（约 -60%） |

这个案例正是 §0 那句话的团队版：「让 Claude 当领队，把 plan/impl/review 三个角色以 A2A 发布出去，领队通过一个 MCP 工具调度他们，全程可看、可审计。」——新 API 里每个子句恰好对应一个变量或一个选项。

#### 9.7 重构后完整示例（单文件全景）

> 现状的 main.go(463) + roles.go(326) + delegation_runtime.go(323) 收敛为下面这一个文件（~250 行）。
> fixture / console / audit / rolePrompt / leaderProtocol 是纯宿主逻辑（临时 git 仓库、终端渲染、工作区阶段快照、给 review 附加 go test 证据、领队编排协议文本），与 SDK API 无关，原样保留，此处只引用。

```go
// Command team-agent-workflow —— v1 API 重构版。
//
// Claude 领队通过宿主注入的 delegate_to_agent MCP 工具，按
// plan(Codex, 只读) → impl(Claude, 可写) → review(Codex, 只读)
// 的顺序调度三个以 A2A 发布的角色。全程在临时仓库与克隆 profile 中进行。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"slices"
	"strings"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/bridges/a2a"
	"github.com/agent-dance/agent-adaptor/bridges/sse"
	"github.com/agent-dance/agent-adaptor/claude"
	"github.com/agent-dance/agent-adaptor/codex"
	delegation "github.com/agent-dance/agent-adaptor/hosttools/a2adelegation"
	"github.com/agent-dance/agent-adaptor/memory"
	"github.com/agent-dance/agent-adaptor/profile"
	"github.com/agent-dance/agent-adaptor/skill"
)

const (
	workflowSentinel       = "TEAM_AGENT_WORKFLOW_OK"
	reviewApprovalSentinel = "TEAM_REVIEW_APPROVED"
)

type options struct {
	claudeModel, codexModel string
	timeout, roleTimeout    time.Duration
	keepWorkspace, webMode  bool
	webAddr, webCORS        string
}

func main() {
	var opts options
	flag.StringVar(&opts.claudeModel, "claude-model", "", "Claude model override")
	flag.StringVar(&opts.codexModel, "codex-model", "", "Codex model override")
	flag.DurationVar(&opts.timeout, "timeout", 15*time.Minute, "workflow deadline")
	flag.DurationVar(&opts.roleTimeout, "role-timeout", 4*time.Minute, "per-role deadline")
	flag.BoolVar(&opts.keepWorkspace, "keep-workspace", false, "keep temp repo & profiles")
	flag.BoolVar(&opts.webMode, "web-mode", false, "serve over AG-UI instead of one-shot CLI")
	flag.StringVar(&opts.webAddr, "web-addr", ":8080", "AG-UI listen address")
	flag.StringVar(&opts.webCORS, "web-cors", "*", "Access-Control-Allow-Origin")
	flag.Parse()
	if err := run(opts); err != nil {
		fmt.Fprintln(os.Stderr, "team-agent-workflow:", err)
		os.Exit(1)
	}
}

func run(opts options) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, opts.timeout)
	defer cancel()

	fixture, err := newWorkflowFixture(opts.keepWorkspace) // 临时 git 仓库 + TASK.md（宿主逻辑）
	if err != nil {
		return err
	}
	defer fixture.Cleanup()

	// 1. 三个角色：每个角色就是一个 Agent 变量，以 A2A 发布。
	hub, remoteSpecs, err := startRoleHub(fixture, buildRoles(opts), opts.roleTimeout)
	if err != nil {
		return err
	}
	defer hub.Close()

	// 2. 委托服务：Registry + EventBus + per-run MCP sidecar + 结果记录，一次配置。
	//    取代现状 323 行手写 delegation_runtime.go；bearer token、监听器、
	//    MCP 注入（类型化 RuntimeServiceRef.MCP）、工具超时全部内置。
	team, err := delegation.NewService(delegation.Config{
		Agents:      remoteSpecs,
		ToolTimeout: opts.roleTimeout + 30*time.Second,
	})
	if err != nil {
		return err
	}
	defer team.Close()

	// 3. 领队：一个 Agent 变量；委托能力用 team.Option() 一个选项接入。
	leaderOpts := []adaptor.Option{
		adaptor.WithProfile(profile.CloneNative(fixture.ProfileDir("leader-claude"), profile.LinkAuth())),
		adaptor.WithWorkspace(fixture.WorkspaceDir),
		adaptor.WithPolicy(adaptor.Policy{Sandbox: adaptor.ReadOnly, Approvals: adaptor.ApprovalsAutoDeny}),
		adaptor.WithIdentity(adaptor.Identity{Tenant: "example", User: "team-leader"}),
		adaptor.WithMetadata("example", "team-agent-workflow"),
		adaptor.WithMetadata("workflow_role", "leader"),
		team.Option(), // service 声明 + sidecar 生命周期 + SubagentUpdate 事件汇入
	}
	if opts.webMode {
		leaderOpts = append(leaderOpts,
			adaptor.WithThreadStore(memory.NewStore()),                 // AG-UI thread ↔ Thread
			adaptor.WithInstructions(leaderProtocol(opts.roleTimeout)), // 前端供 per-turn 输入，编排协议进指令
		)
	}
	leader := adaptor.New(claude.Driver(claude.Config{
		Model:             opts.claudeModel,
		PersistentProcess: true, // 领队多轮复用一个长驻 claude 进程
	}), leaderOpts...)

	if opts.webMode {
		mux := http.NewServeMux()
		mux.Handle("/agent", sse.Handler(leader, sse.Options{
			Protocol:          sse.AGUI,
			CORSAllowedOrigin: opts.webCORS,
			// 无需 SubagentBus：SubagentUpdate 已在 leader 的事件流上。
		}))
		term.Logf("[web] AG-UI server on %s (POST /agent)", opts.webAddr)
		return http.ListenAndServe(opts.webAddr, mux)
	}

	// 4. CLI 模式：一条事件流，一个 for-range。没有 drain 义务、没有手工 goroutine。
	stream := leader.Stream(ctx, leaderProtocol(opts.roleTimeout))

	trace := &workflowTrace{}
	for ev := range stream.Events() {
		switch e := ev.(type) {
		case adaptor.TextDelta:
			term.Print(e.Text)
		case adaptor.Thinking:
			term.Reasoning(e.Text)
		case adaptor.ToolCall:
			term.Tool(e.Name, e.Args)
		case adaptor.SubagentUpdate: // plan/impl/review 的远端过程，与主流同序到达
			term.Live(e.Agent, e.Kind, e.Delta)
			trace.Add(e)
		case adaptor.Dropped:
			term.Warnf("dropped %d events", e.Count)
		}
	}

	res, err := stream.Result()
	if err != nil {
		var runErr *adaptor.RunError
		if errors.As(err, &runErr) { // 领队完整跑完但业务失败
			return fmt.Errorf("leader failed (%s): %s", runErr.Reason, runErr.Result.Summary)
		}
		return fmt.Errorf("leader: %w", err) // 基础设施失败同一条路
	}

	// 5. 编排校验：委托顺序、工作区阶段边界、review 结论。
	if err := trace.RequireOrder("plan", "impl", "review"); err != nil {
		return err
	}
	hub.Audit.Record("final")
	if err := hub.Audit.ValidateStageBoundaries(); err != nil {
		return err
	}
	review, ok := team.Result(stream.RunID(), "review")
	if !ok || !review.HasLine(reviewApprovalSentinel) {
		return fmt.Errorf("review did not approve; leader_output=%q", preview(res.Text, 1200))
	}
	if _, err := fixture.Validate(ctx); err != nil {
		return err
	}
	term.Logf("[done] sentinel=%v run_id=%s order=%v",
		strings.Contains(res.Text, workflowSentinel), stream.RunID(), trace.Order())
	return nil
}

// ---- 角色定义与 A2A 发布 ----

type roleDef struct {
	Key, DisplayName string
	Driver           adaptor.Driver   // 根包别名 = driver.Driver，宿主无需 import SPI 包
	Sandbox          adaptor.SandboxLevel
	Instructions     string
	Options          []adaptor.Option // 逃生舱：该角色专属的任意追加配置，全量选项词汇可用
}

func buildRoles(opts options) []roleDef {
	return []roleDef{{
		Key: "plan", DisplayName: "Codex planner",
		Driver:  codex.Driver(codex.Config{Model: opts.codexModel}),
		Sandbox: adaptor.ReadOnly,
		Instructions: "Act only as the planning stage. Inspect TASK.md, code, and tests. " +
			"Do not modify files. Return a concise ordered plan with acceptance checks.",
	}, {
		Key: "impl", DisplayName: "Claude Code implementer",
		Driver:  claude.Driver(claude.Config{Model: opts.claudeModel}),
		Sandbox: adaptor.WorkspaceWrite,
		Instructions: "Act only as the implementation stage. Use the supplied plan context, " +
			"modify only slug.go, run go test ./... and git diff --check, and do not commit.",
		Options: []adaptor.Option{ // 角色差异化不受表结构限制：技能、MCP、profile 资源……
			adaptor.WithSkills(skill.Dir("./skills/go-implementer")),
			adaptor.WithProfileResources(profile.Resources{
				SubAgents: []profile.SubAgent{{Key: "tester", Instructions: "Run and summarize go test."}},
			}),
		},
	}, {
		Key: "review", DisplayName: "Codex reviewer",
		Driver:  codex.Driver(codex.Config{Model: opts.codexModel}),
		Sandbox: adaptor.ReadOnly,
		Instructions: "Act only as the review stage. Do not modify files. Evaluate the attached " +
			"go test / git diff evidence; do not rerun the Go toolchain. End with a line containing " +
			"exactly TEAM_REVIEW_APPROVED only if every requirement is satisfied; otherwise end with " +
			"TEAM_REVIEW_REJECTED.",
	}}
}

func startRoleHub(fixture *workflowFixture, roles []roleDef, roleTimeout time.Duration) (*roleHub, []delegation.RemoteAgentSpec, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, nil, fmt.Errorf("listen for A2A role hub: %w", err)
	}
	baseURL := (&url.URL{Scheme: "http", Host: listener.Addr().String()}).String()
	mux := http.NewServeMux()
	hub := newRoleHub(baseURL, listener, fixture)

	remote := make([]delegation.RemoteAgentSpec, 0, len(roles))
	for _, def := range roles {
		cardPath := "/agents/" + def.Key + "/.well-known/agent-card.json"
		rpcPath := "/agents/" + def.Key + "/a2a"

		role := adaptor.New(def.Driver, append([]adaptor.Option{ // 角色 = Agent 变量，不再有 per-role SDK 实例
			adaptor.WithProfile(profile.CloneNative(fixture.ProfileDir(def.Key), profile.LinkAuth())),
			adaptor.WithWorkspace(fixture.WorkspaceDir),
			adaptor.WithPolicy(adaptor.Policy{Sandbox: def.Sandbox, Approvals: adaptor.ApprovalsAutoDeny}),
			adaptor.WithInstructions(def.Instructions),
			adaptor.WithIdentity(adaptor.Identity{Tenant: "example", User: "team-" + def.Key}),
			adaptor.WithMetadata("example", "team-agent-workflow"),
			adaptor.WithMetadata("workflow_role", def.Key),
		}, def.Options...)...) // 角色专属选项排在公共默认之后：按「近处覆盖远处」规则可覆盖任何公共默认

		srv := a2a.NewServer(observe(def.Key, role, hub.Audit.Record), a2a.ServerOptions{
			AgentCard: a2a.AgentCard{
				Name:        def.DisplayName,
				Description: def.Key + " role in the plan -> impl -> review team workflow.",
				Version:     "1.0.0",
				URL:         baseURL + rpcPath,
				Skills:      []a2a.Skill{{ID: def.Key, Name: def.DisplayName, Description: def.Instructions}},
			},
			Session:  a2a.Stateless(),
			Prompt:   rolePrompt(def, fixture), // review 角色附加 go test / git diff 证据（宿主逻辑）
			Exposure: a2a.ExposurePolicy{IncludeToolCalls: true, IncludeReasoning: true},
			TaskLifecycle: a2a.TaskLifecycleOptions{
				Ephemeral: &a2a.EphemeralTaskStoreOptions{MaxTasks: 32, TTL: 30 * time.Minute},
			},
		})
		mux.Handle(cardPath, srv.AgentCardHandler())
		mux.Handle(rpcPath, srv.Handler())

		remote = append(remote, delegation.RemoteAgentSpec{
			Key:          def.Key,
			DisplayName:  def.DisplayName,
			AgentCardURL: baseURL + cardPath, // Protocol / Transport 默认 A2A + JSON-RPC
			Policy: delegation.Policy{
				MaxTimeout: roleTimeout, RequireStreaming: true, MaxArtifactBytes: 1 << 20,
			},
		})
	}
	hub.Serve(mux)
	return hub, remote, nil
}

// ---- 角色观测：装饰 Runner 的两个方法（现状是 Run/Start/Wait 三个拦截点） ----

type observed struct {
	adaptor.Runner
	role   string
	record func(string)
}

func observe(role string, next adaptor.Runner, record func(string)) adaptor.Runner {
	return observed{Runner: next, role: role, record: record}
}

func (o observed) Run(ctx context.Context, prompt string, opts ...adaptor.Option) (*adaptor.Result, error) {
	started := time.Now()
	res, err := o.Runner.Run(ctx, prompt, opts...)
	o.done(started, res, err)
	return res, err
}

func (o observed) Stream(ctx context.Context, prompt string, opts ...adaptor.Option) adaptor.Stream {
	return observedStream{Stream: o.Runner.Stream(ctx, prompt, opts...), o: o, started: time.Now()}
}

func (o observed) done(started time.Time, res *adaptor.Result, err error) {
	elapsed := time.Since(started).Round(time.Millisecond)
	if err != nil { // err 一元判定：业务失败细节在 *RunError.Result，无需 Failure 双路径
		term.Logf("[role] %s failed in %s: %v", o.role, elapsed, err)
	} else {
		term.Logf("[role] %s done in %s: %s", o.role, elapsed, res.Summary)
	}
	o.record(o.role) // 工作区阶段快照（宿主审计逻辑）
}

type observedStream struct {
	adaptor.Stream
	o       observed
	started time.Time
}

func (s observedStream) Result() (*adaptor.Result, error) {
	res, err := s.Stream.Result()
	s.o.done(s.started, res, err)
	return res, err
}

// ---- 委托顺序校验 ----

type workflowTrace struct{ order []string }

func (t *workflowTrace) Add(e adaptor.SubagentUpdate) {
	if e.Kind == adaptor.SubagentStarted {
		t.order = append(t.order, e.Agent)
	}
}

func (t *workflowTrace) Order() []string { return t.order }

func (t *workflowTrace) RequireOrder(want ...string) error {
	if !slices.Equal(t.order, want) {
		return fmt.Errorf("delegation order = %v, want %v", t.order, want)
	}
	return nil
}
```

对照读法（现状 → 本文件）：

| 现状 | 行数 | 新 API 归宿 | 位置 |
|---|---:|---|---|
| `newLeaderSDK` + `claude.New(ClaudeConfig, 7×WithDefault*)` | ~60 | `adaptor.New(claude.Driver(...), leaderOpts...)` | run() 第 3 步 |
| `delegation_runtime.go` 整个文件 | 323 | `delegation.NewService` + `team.Option()` + `team.Result` | run() 第 2 步 |
| trace/drain/render 3 goroutine + WaitGroup + ClearRun | ~70 | 一个 for-range | run() 第 4 步 |
| `waitErr` + `result.Failure` 双检 | 分散 | `stream.Result()` 单判定 | run() 第 4 步末 |
| per-role SDK + `observedRoleRunner`（3 拦截点） | ~120 | Agent 变量 + 2 方法装饰器 | startRoleHub / observed |
| `withMCPToolTimeout` env 侧信道 | ~15 | `delegation.Config.ToolTimeout` | run() 第 2 步 |

关于 `roleDef` 的定位：它是**宿主自己的数据表，不是 SDK 概念**。三个角色恰好只差四个维度所以压成了表；`Options []adaptor.Option` 字段是全量逃生舱——任何角色可携带任意构造选项（技能、MCP、profile 资源、独立 ThreadStore、审批回调……），且排在公共默认之后即可按「近处覆盖远处」规则覆盖公共默认。这种表驱动写法在现状 API 里做不干净：同样的角色差异必须拆进两种类型的切片（`AgentOption` 给 binding、`RunOption` 给 `ServerOptions.RunOptions`），而新 API 单一选项词汇让宿主数据表天然可组合。若某个角色的差异大到表放不下，直接不用表——每个 Agent 本来就是变量，逐个手写也只有十来行。另外 `a2a.ServerOptions` 仍保留调用作用域的 `Options []adaptor.CallOption`（本例不需要：沙箱已上移为角色构造期属性），供需要按请求维度注入的宿主使用。

#### 9.8 团队抽象内建到哪一层：`delegation.Local`，而不是 roleDef

评审中的自然追问：领队侧已经有 `team.Option()` 了，为什么不把 roleDef 也内建进 SDK？拆开看，roleDef 承担两件事，内建价值截然不同。

**该内建的一半——「可委托目标」的注册与联通。** 现状 Registry 只认 `ProtocolA2A`（`pkg/hosttools/a2adelegation/types.go` 强校验），进程内的角色也被迫先发布成 HTTP A2A 服务、再被同进程的领队绕一圈 localhost 调用。v1 的 `delegation.Service` 补上本地目标：

```go
// 进程内团队：多数宿主的真实形态——零 HTTP、零 AgentCard、零端口管理
team, _ := delegation.NewService(delegation.Config{
    Agents: []delegation.AgentRef{
        delegation.Local("plan", planner, delegation.Policy{MaxTimeout: t}),
        delegation.Local("impl", implementer, delegation.Policy{MaxTimeout: t}),
        delegation.Local("review", reviewer, delegation.Policy{MaxTimeout: t}),
    },
})
leader := adaptor.New(claude.Driver(cfg), team.Option())
```

- `delegation.Local(key, runner, policy)` 接受任何 `Runner`——Agent、Thread、装饰器（`observe(...)` 照样能包）。
- 本地目标直接消费 Runner 的事件流，`SubagentUpdate` 保真度反而高于经 A2A 序列化的远端流。
- `Local` 与 `Remote` 可混编：三个本地角色 + 一个跨组织的远程 A2A 角色是同一张表。
- showcase 保留 Remote 形态是因为它的教学目的就是跨进程 A2A；换成 Local 后，startRoleHub 里的 HTTP hub 整段消失。

**不该内建的一半——roleDef 的配置表（Driver / Sandbox / Instructions 字段）。** 三条理由：

1. SDK 已有「配置好的 agent」的唯一通用表示：`Runner`。团队抽象消费 Runner 即可组合一切；若 SDK 再定义一个描述角色配置的结构体，字段集要么太窄（§9.7 的 Options 逃生舱问题会在 SDK 层重演，且用户无法逃生），要么膨胀成镜像全部 ~24 个选项的平行词汇——正是本次重构消灭的 `WithDefaultX`/`WithX` 双词汇病的转世。
2. 现状 SDK 犯过一次同样的错：命名注册表 `WithAgent("review", binding)` + `sdk.Agent("review")` 就是内建的 roleDef-lite，§2.2 删除它的全部理由在此同样成立。
3. 判据一句话：**进 SDK 的是协议与生命周期（目标注册、sidecar、事件桥、A2A 发布），留在宿主的是业务形状（有哪些角色、每个角色怎么配、何时委托）。**

顺带澄清命名：`team.Option()` 不是根包的 `adaptor.WithTeam()`——根包不知道「团队」这个词。该选项由 `delegation` 包发行（`Option` 是接口，生态包可自行扩展），根包词汇表维持 ~24 个不动。

---

## 4. 能力保全映射（重构不丢任何一项现有能力）

| 现有能力 | 新 API 归宿 |
|---|---|
| Run / Start 单一执行路径 | `Run` / `Stream`（同一内部管线） |
| 4 种 SessionMode + SessionStore + lease | `Thread` / `NewThread` / `ResumeOnly` / `Fork` + `threadstore.Store`（接口能力等价） |
| Session codec / 参数检视 | `driver.SessionCodec` 能力接口 + `th.Checkpoint()` |
| skill 归档源（archive source / materializer，zip/tar/tgz 分发技能包） | `skill.Archive(...)` 构造器 + 物化管线随 skill/ 包保留（P0.7 盘点勘误：archive_*.go 是 skill 归档源而非 run 结果归档） |
| Skills：key/dir/FS/inline、Provider、Catalog、Materializer、Required、冲突检测、严格物化 | `skill` 包 + `WithSkills` / `WithSkillProvider` / `WithSkillMaterializer`（语义不变） |
| MCP 声明式注入 + profile 物化 + fingerprint | `mcp` 包 + `WithMCP`（替换语义不变） |
| Runtime services 生命周期 + MCP sidecar 注入 | `WithServices` / `WithServiceManager` + 类型化 `RuntimeServiceRef.MCP` |
| Profile：native/dedicated/clone/cloneFrom + AuthLink | `profile` 包 4 个构造器 + `LinkAuth` |
| Profile resources：agents/hooks/instructions/config patch + 真话物化汇报 | `profile.Resources` + `ProfileState` / `SyncProfile` |
| RunPolicy（isolation / websearch / browser / HITL 策略） | `Policy` 结构体选项 |
| HITL 三种 Kind、超时重试兜底、channel 模式、typed handler 模式 | `ApprovalRequest`（事件 + 回调双形态）+ `Policy.Approvals` |
| 流式能力声明与降级、背压两策略、Dropped 标记 | `StreamCapability`（driver 包）+ `WithEventBuffer` / `WithBlockingEvents` + `Dropped` 事件 |
| 结构化输出三模式 + 泛型派生 + 能力矩阵校验 | `RunAs[T]` / `WithSchema[T]` + `schema.Strict/Flexible/PromptOnly` |
| Admin 全部探针 + SetSelectedSkills | `Inspect()` 面板 + `SelectSkills` |
| AG-UI / SSE / A2A bridge、subagent 委托、session recorder | 包原样保留，适配新事件模型 |
| delegate_to_agent 委托（Registry / EventBus / Delegator / MCP server 分立组件 + 宿主手写 sidecar） | `delegation.Service` 一体化入口（组件仍可单独使用）+ 类型化 `RuntimeServiceRef.MCP` + `SubagentUpdate` 事件入主流 |
| 每次运行的 Metadata / CallerIdentity 传播 | `WithMetadata` / `WithIdentity`（ctx 传播机制不变） |
| adaptertest 一致性套件 | 升级为 `driver.Driver` 版本 |

---

## 5. 落地计划（建议 6 个阶段，每阶段可独立评审）

| 阶段 | 内容 | 产出 |
|---|---|---|
| P0 | 根包骨架：`Agent` / `Option`（双作用域机制）/ `Result` / `RunError`；`driver` 包拆分 | 新旧并存的编译单元 + S1/S2/S4 场景测试 |
| P1 | 事件流合一：`Stream` / `Event` 类型族 / `ApprovalRequest` 双形态；删除双通道 | S3 场景测试 + streaming 合同测试迁移 |
| P2 | `Thread` + `threadstore`：mode → 方法映射、Fork、Checkpoint、lease | 会话合同测试迁移 |
| P3 | 驱动包迁移（codex/claude/cursor/codebuddy 的 Config 回家）、`skill`/`mcp`/`profile` 词汇包、`Inspect` | S5/S7/S8 场景测试 |
| P4 | bridges / hosttools 适配新事件模型；`RuntimeServiceRef.MCP` 类型化 | S6 场景 + examples 全量重写 |
| P5 | 删除旧 API、`adaptertest` v1、文档重写（README 以 6 名词开篇）、迁移指南 | v1.0.0 tag |

兼容策略：按「抛弃历史包袱」的前提，v1 是干净切换；v0.x 打 tag 冻结。若后续需要，可提供一次性 `go fix` 风格迁移工具（旧 API → 新 API 多数是机械映射，见 §4 表）。

---

## 6. 关键取舍与风险

1. **删除命名注册表**：失去「SDK 对象统一管理所有 binding」的形式，换来零概念成本。共享基础设施通过共享 store/manager 实例达成。风险低。
2. **业务失败并入 error**：从「返回值字段」变「类型化 error」。反对观点是「完成了的 run 不是 error」；但现状三步判定的实际漏检成本更高，且 `RunError.Result` 保留全部信息。与 Go 生态惯例（`exec.ExitError`）一致。
3. **事件合一后的低频操作事件**：spawn/lifecycle 混入语义流可能让纯 UI 消费者多写两个 case——代价是 default 分支一行；换来的是删除整条 `Events()` 通道与「必须 drain」义务。
4. **`ApprovalRequest` 合并三种 Kind**：損失一点编译期 kind 精度（现状 3 个 typed handler），换来审批表面从 10 个 API 元素减到 2 个。Kind 专属数据以字段组暴露，误用在方法级被拦截。
5. **root 包名改 `adaptor`**：与目录名不一致（Go 工具链完全支持，goimports 自动别名）。若团队反感，保持 `agentadaptor` 不影响其余设计。
6. **实施规模**：这是全量 breaking change，P0–P5 需要同步重写测试与文档。缓解：§4 映射表保证能力不丢；每阶段以场景测试为验收锚点；`adaptertest` 保住第三方驱动的迁移路径。
