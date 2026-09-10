# agent-adaptor AGENTS

本文件定义 `agent-adaptor` v1 的架构边界、公共语义、迁移纪律与发布门禁。后续设计、实现、重构、评审和文档均必须以本文件为准。

## 1. 项目定位

`agent-adaptor` 是一个可嵌入宿主的 Go SDK。

它负责：

- 以统一 API 调用不同本地 coding agent
- 统一构造、批处理、流式执行与有状态对话语义
- 提供可选的 Thread、workspace、tool、skill、MCP、profile、runtime service 注入点
- 提供稳定的 Driver SPI 与一致性测试
- 允许宿主嵌入 CLI、桌面应用、HTTP/gRPC 服务、工作流和定时任务

它不负责：

- 在 core 内内置面向宿主的通用 HTTP/gRPC server、队列、调度器、租户、鉴权或数据库；`WithTools` 私有、loopback-only 的 MCP transport 属于 Agent capability 交付实现，不构成宿主服务框架
- 自动决定一次任务应使用哪个 Agent
- 定义宿主的团队角色、业务流程或路由策略
- 强制任何持久化、分布式锁或服务框架依赖

Bridges 可以提供协议适配和便捷 handler，hosttools 可以提供可选宿主组件，但这些能力不得反向污染 core 执行语义。

## 2. 六个核心名词

v1 的消费者心智只能由以下六个核心名词组成：

| 名词 | 含义 |
|---|---|
| `Agent` | 一个配置完整、构造后即可执行的智能体 |
| `Thread` | 一段由宿主 key 标识、可持续续接或分叉的对话 |
| `Stream` | 一次正在进行的执行 |
| `Event` | 执行过程中发生的一件 typed 事件 |
| `Result` | 一次执行的最终结果与审计信息 |
| `Driver` | 一种 agent CLI/provider 的接入实现，属于扩展方 SPI |

硬约束：

- 不存在中央 SDK 对象。
- 消费者执行动词只有 `Run` 与 `Stream`，不得增加 `Start` 或其他平行执行入口。
- 不得重新引入 binding、默认 Agent、字符串查找 Runner 等平行抽象。
- 根包面向应用开发者；SPI 必须收敛在 `driver/`，不得再次把扩展方合同铺回根包 godoc。

## 3. 包布局与依赖方向

目标包布局：

```text
github.com/agent-dance/agent-adaptor          package adaptor
├── driver/                                    Driver SPI
├── codex/ claude/ cursor/ codebuddy/          各驱动 Config 与 Driver(Config)
├── tool/                                      宿主定义 Tool 词汇
├── skill/                                     skill 词汇与来源
├── capability/ todo/                          安全观测与已确认快照叶词汇
├── mcp/                                       MCP 声明
├── profile/                                   profile 与资源声明
├── threadstore/ memory/                       Thread 持久化合同与内存实现
├── bridges/{sse,agui,a2a,subagentstream}/     协议桥
├── hosttools/{a2adelegation,sessionrecorder}/ 宿主可选组件
├── clients/a2a/                               A2A 客户端
└── adaptertest/                               Driver 一致性套件
```

依赖方向必须单向：

- 根包可以依赖 `driver`、词汇包和 `internal/engine`。
- `internal/engine` 不得 import 根包。
- `driver` 不得依赖根包或具体 provider 包。
- `tool`、`skill`、`mcp`、`profile`、`threadstore` 不得反向 import 根包。
- bridges 与 hosttools 只能消费公开 `Runner`、`Stream`、`Event`、`Result` 等执行合同，不得调用内部 engine 或直接派发 Driver。唯一精确实现依赖例外是 `hosttools/a2adelegation -> internal/activebudget` 的中立私有计时器复用；它不解析 provider、不派发执行，公共签名不得泄露该类型，其他 internal 依赖仍禁止。
- 公共类型不得通过 alias、字段或方法签名泄露 `internal/*` 类型。

## 4. 唯一构造语义

消费者唯一构造入口：

```go
agent := adaptor.New(
	codex.Driver(codex.Config{Model: "gpt-5.4"}),
	adaptor.WithWorkspace("/repo"),
)
```

合同：

```go
func New(d driver.Driver, opts ...Option) *Agent
```

- 内置驱动包拥有自己的真实 `Config` 类型，并以 `Driver(Config) driver.Driver` 返回已捕获配置的 Driver。
- 第三方扩展方直接实现 `driver.Driver`，与内置驱动走同一个 `adaptor.New`。
- `New` 对 nil Driver 等编程错误可以 panic；运行环境、能力或配置错误必须在执行或 Inspect 时结构化返回。
- Driver 配置不得以 `internal/engine` 类型别名对外暴露。
- Inspect、Run、Stream 和 capability probe 必须观察同一份构造期配置，不得向已配置 Driver 的 probe 静默传入丢失语义的 nil config。

## 5. 选项与唯一执行管线

选项采用一套词汇、两个作用域：

- `Option`：只允许用于 `New`
- `CallOption`：只允许用于 `Run` / `Stream`
- `SharedOption`：同时适用于构造默认值和单次调用覆盖

作用域错误必须尽可能成为编译错误。合并规则只有一条：

> 近处覆盖远处；skills 追加；其余能力按公开合同替换或显式合并。

所有执行必须收敛到同一内部管线：

1. 读取 Agent 默认值与本次 CallOption
2. 生成唯一的 resolved invocation
3. 校验 Driver 能力、policy、schema 与审批模式
4. 解析 workspace、profile、Tools、skills、MCP 与 runtime services
5. 若接收者是 Thread，协调 store、lease、resume/fork 与兼容性
6. 通过唯一 Event sink 调用一次 `driver.Run`
7. 形成 Event、Result、RunError 与 checkpoint
8. 原子持久化有效 Thread 状态
9. 关闭事件流并释放 workspace、runtime service、lease 等资源

硬约束：

- `Run` 必须等价于 `Stream` + drain Events + `Result()`，不能拥有第二份默认值合并、资源解析、Driver 派发或结果归档逻辑。
- SDK 的统一 Event 管线与 provider transport 的 streaming 选择不是同一个概念；transport 必须按 resolved invocation 和 Driver capability 明确协商，不能仅因调用 `Run` 或 `Stream` 就无条件切换 provider 协议。
- `Agent` 与 `Thread` 都实现同一个 `Runner`。
- Thread 只是在统一管线中增加状态协调阶段，不得复制执行管线。
- bridges、hosttools、structured output 和 Inspect 不得产生第二套执行入口或默认值语义。

进程生命周期合同：

- Claude、CodeBuddy 与 Codex 对显式 Thread 默认允许复用常驻进程；Cursor 与无状态 Agent 调用仍逐轮启动。
- `WithSpawn()` 是双作用域选项，显式强制使用本轮新进程；该进程不得注册为后续轮次的常驻 writer。
- 常驻只是 Driver 内部 transport 生命周期选择，不得形成平行执行入口、事件流或结果合同。
- `Agent.Close(ctx)` 必须幂等回收 Driver 管理的全部常驻进程；Close 开始后所有 Agent/Thread 新运行稳定返回 `ErrAgentClosed`。
- Thread record 重绑、配置漂移、临时单次形态和预热之间必须保持单 writer：旧进程完成有界退出后才可启动 replacement。
- prompt 交付前的常驻启动失败可以安全回退一次；prompt 可能已经交付后不得自动重放。

## 6. Thread 语义

默认无状态。只有显式注入 `WithThreadStore(store)` 后才启用有状态对话。

公共动作：

```go
th := agent.Thread(key)                 // 有则续、无则建
th := agent.Thread(key, ResumeOnly())   // 只续不建
branch := th.Fork(newKey)               // 从父对话分叉
checkpoint, err := th.Checkpoint(ctx)
```

合同：

- Thread key 是宿主提供的单一、不透明业务字符串，SDK 必须逐字保存和比较。
- 宿主主动开启无上下文的新对话时必须分配新的 Thread key；core 不提供同 key 的 `NewThread`、`start_new` 或重绑入口。continue-or-start 仅可在配置不兼容或 provider 拒绝 resume 的安全回退中原子替换内部 checkpoint。
- 内部存储或 bridge 不得通过未转义分隔符拼接多个维度；任何复合 key 必须使用无碰撞编码。
- Driver 的 resume ID 是内部 checkpoint 细节，不得升级成第二个消费者身份体系。
- 同一 Thread 同时只能有一个持有有效 lease 的运行。
- lease 的 acquire、renew、finalize、release 必须以 owner 和 token 防止过期运行覆盖新状态；释放必须有界且错误可观察。
- `Fork(newKey)` 必须验证父 checkpoint、Driver、配置、identity 与 fingerprint 兼容性，并持有必要的父记录协调租约。
- Fork 不得归档或修改父 Thread；目标 key 已存在时必须执行明确、有测试的冲突策略，不能静默留下多条 active record。
- continue-or-start 在 provider 拒绝 resume 时最多按合同回退一次到新会话；旧状态只能在新 checkpoint 成功持久化后归档。
- Store 的 Finalize 必须对 save、archive、rebind 与 lease 校验提供原子语义。
- 不完整、冲突或同时指定多个 selector 的请求必须在获取资源前稳定拒绝；不得创建无法按 key 找回的孤儿记录。

兼容 fingerprint 必须确定性、跨进程稳定，并覆盖所有影响续接正确性的已解析状态，包括：

- Driver 类型及构造配置
- 模型与 identity
- 实际解析后的 workspace，而非仅调用方原始字符串
- profile 与 materialized resources
- Tools、skills、instructions、MCP
- 会影响会话环境的 runtime service fingerprint
- Driver SessionCodec 所要求的其他兼容维度

不得因为遗漏配置或运行环境而错误复用 Thread；无法稳定 canonicalize 的自定义 Driver 配置必须由稳定扩展点提供 fingerprint，或在启用 Thread 前被拒绝。

## 7. Stream、Event 与 HITL

`Stream` 是小接口：

```go
type Stream interface {
	Events() <-chan Event
	Result() (*Result, error)
	RunID() string
	Cancel()
}
```

合同：

- 一次运行只有一条 typed Event 流。
- Event 流承载文本、thinking、tool、生命周期、进程、notice、drop、宿主事件与审批请求。
- 不得重新拆出第二条语义流、操作流或决策 channel。
- `RunID()` 在返回 Stream 时立即可用。
- Events 必须按定义顺序发布，并在所有终局事件交付后关闭。
- `Result()` 可并发、多次调用，结果必须一致；Events 关闭后应立即返回。
- `Cancel()` 必须幂等，并能解除所有阻塞发送、等待审批和资源获取。
- 默认背压可以丢弃合同允许丢弃的增量事件，但必须聚合成信息完整的 `Dropped`；审批请求、终局状态和合同规定的关键语义事件不得丢失。
- blocking 模式必须是 cancellation-safe；禁止出现消费者取消后 sink 与执行 goroutine 互相等待的闭环死锁。
- Event 的 RunID、序号、时间与生命周期字段必须有唯一权威定义，并在并发 producer 下保持接收顺序一致。

HITL 只通过 `ApprovalRequest` 表达：

- 三种 Kind 为 Permission、PlanReview、Question。
- 请求自带 `Approve`、`Deny`、`Answer` 应答能力。
- 回调模式使用 `OnApproval`；Web/UI 模式直接消费 Event 中的请求。
- 应答必须 exactly-once；重复、Kind 不匹配、已过期请求必须返回稳定错误。
- 零值、脱离运行环境或未绑定 responder 的 `ApprovalRequest` 方法必须立即返回明确错误，绝不能向 nil channel 发送或永久阻塞。
- 超时、重试与兜底策略只属于 `Policy.Approvals`，不得在 bridge 或 Driver 中形成第二套策略。
- 未有真实 Driver 风险信号前，不得伪造 `Risk()` 能力。

## 8. Result、错误与输出合同

成功运行返回 `*Result, nil`。失败只走 Go 的 `error` 路径：

- 以唯一 `driver.Run` 已进入为界，后续业务、取消、deadline、基础设施或清理失败均返回 `nil, *RunError`，其中 Result 非 nil 并保留可获得的审计数据；启动前错误仍为普通包装错误。
- `RunError.Reason` 是主原因，`Cause` 保留原始及次要错误并可 `errors.Is/As`。审批或 provider 主因不得被其取消/清理次因覆盖。
- 健康 checkpoint 已原子提交后才发生的清理失败保留已提交状态，报告基础设施主因及完整 Result；不得回滚或重复提交。
- 不得在成功 Result 中再设置第二个 Failure 判定面。

Result 分层合同：

- `Text`：最终 assistant-facing 文本；没有 assistant 文本时允许为空。
- `Summary`：适合列表、日志、issue comment 的简短摘要；无可用摘要时允许为空，不得退化成无界完整 Text。
- `Raw()`：必须提供本次运行完整 stdout、stderr，并保留 Driver 识别到的 provider 终局原始 payload。
- `Transcript()`：Driver 从正式协议解析出的标准化语义条目。
- `Services()`：本次运行已观察到的 runtime service 报告，不得把声明伪装成执行成功证据。
- `Decode()`：解码已经校验的结构化输出；无 schema 时只可按明确文档从 Text 便利解码。
- `Usage`、`Model`、`Provider`、`Metadata` 必须保持各自独立语义。

硬约束：

- Text 不得承载原始 stdout dump。
- Text 不得自动拼接 Summary 或 provider 终局 payload。
- Run 与 Stream.Result 必须提供同样完整的 Raw、Transcript、Services 与结构化输出。
- 结构化输出只有一种固定协商语义：优先使用 provider 原生 schema 约束；当前 transport 或 policy 不支持时自动回退到 Prompt 加本地校验；两者都不可用才在启动前失败。消费者和 Driver SPI 均不得暴露模式选择，Driver 只能执行 core 已解析的机制。
- Transcript 只能来自 Driver 对官方协议的解析，不能来自 shared helper 的 JSON 猜测。
- provider 终局 payload 不得在 Driver Response → Result 转换中丢失。
- Usage 未观察到与真实零值的区别若协议需要表达，必须在冻结前以明确类型合同解决，不得靠猜测。

## 9. Driver、协议解析与 checkpoint 边界

`driver/` 是扩展作者专属 SPI。Driver 必须：

- 准确声明 Descriptor 与能力矩阵
- 校验自己的配置
- 解析自己的正式 stdout/stderr/app-server 协议
- 从同一次解析产出 Text、Summary、Raw、Transcript、终局 payload 与 checkpoint
- 只识别 provider 正式协议中的明确字段
- 对 unsupported 能力报告真话，不得编造成功或静默降级

shared process helper 只负责：

- 启动与停止进程
- 传递 stdin
- 捕获完整 stdout/stderr
- 发布原始 chunk
- tee 原始数据给 Driver parser

shared helper 不得：

- 解释 provider 语义
- 递归扫描 JSON 猜 session/checkpoint
- 构造 assistant/tool/result Transcript
- 判定 provider checkpoint 是否健康
- 把 stdout 填进 Result.Text

checkpoint 安全合同：

- checkpoint 只能由对应 Driver 的正式 parser 产生。
- 顶层、明确、通过 SessionCodec 归一化且可续接的状态才可标记 Valid。
- 非零退出、取消、协议错误、业务失败默认不得产生有效 checkpoint。
- 只有 Driver 能证明 provider 明确交付了健康可续状态，且合同明确允许时，失败路径才可例外持久化。
- 没有有效 checkpoint 时，Thread Store 必须保持之前的健康 active record 不变。
- Claude、Cursor、CodeBuddy、Codex CLI 与 Codex app-server 都必须分别有成功、非零退出、畸形协议、缺失 checkpoint 的合同测试。
- Codex app-server 必须完整保留 stdout、Transcript 与 provider 终局 payload，不得因 JSON-RPC 路径而降低输出合同。

Driver SPI 的所有 MUST/SHOULD、事件生命周期、Sequence/Seq 权威性、SessionCodec nil/零值映射及 capability 蕴含关系，必须在 `driver/` godoc 与 `adaptertest` 条款中一致表达。

## 10. Inspect、tool、skill、MCP、profile 与 runtime

- `Agent.Inspect()` 是只读探针入口，不是控制面或执行面。
- Environment、Models、Quota、ConfigSchema、Skills 等探针不支持时必须返回明确 unavailable/unsupported 语义，不得伪造。
- Inspect 必须使用 Agent 构造时真实 Driver 配置。
- `ProfileState` 只报告 desired 与 observed；`SyncProfile` 才执行资源物化。
- skill 物化、schema 协商、profile 同步等启动前错误必须显式失败，不得降级成 warning 后继续运行。
- `tool`、`skill`、`mcp`、`profile` 必须各自拥有完整公共词汇；调用方不得为了构造这些值 import `driver` 或 `internal`。
- Runtime service 发布 MCP 必须使用类型化字段，最终 v1 不保留 stringly metadata 兼容解析。
- Service report 只能陈述实际观察，不能把宿主输入声明回显成“已执行”。

## 11. Bridges 与 hosttools 的定位

Bridges：

- `bridges/sse`、`agui`、`a2a`、`subagentstream` 只做 Runner/Event/Result 与外部协议之间的翻译。
- 不得直接调用 Driver、复制 Thread 协调或实现第二套错误/HITL 策略。
- 外部 ThreadID/contextID 到 Thread key 的映射必须稳定且无碰撞。
- A2A 必须保持 ExposurePolicy 的最小暴露、敏感字段过滤与错误状态映射。
- 协议保真缺口必须通过字段补齐、明确降级事件或文档化决策解决，不得静默丢失。

Hosttools：

- `a2adelegation` 与 `sessionrecorder` 是可选宿主组件，不进入 core 词汇。
- delegation 可以消费任意 Runner，并支持 Local/Remote 目标。
- `delegation.Service` 负责 Registry、EventBus、Delegator、per-run MCP sidecar、结果记录与生命周期。
- `team.Option()` 由 delegation 包发行；根包不得新增 `WithTeam`。
- SDK 只内建协议与生命周期，不内建角色配置表、团队业务形状或自动路由。
- session recorder 必须记录稳定 typed Event 信封；需要持久化时不得静默退回内存。

## 12. 依赖选型原则

可靠性与可持续维护高于“零依赖”。新增依赖必须评估：

1. 是否显著提高协议解析、IPC、schema、session 或安全边界的可靠性
2. 是否由官方或主流社区持续维护，具备版本、文档、问题追踪与 CVE 响应
3. 是否可局部化在 Driver、bridge、hosttool 或构建工具边界内

要求：

- 三项显著占优时优先采用成熟依赖。
- 与手写方案接近时倾向手写，减少审计面。
- provider SDK/协议客户端只允许对应 Driver import。
- AG-UI/SSE/A2A 协议依赖只允许对应 bridge import。
- 构建工具使用 `go:generate`，不得污染 runtime 公共 API。
- 每个新增顶层 `require` 必须在相关 workstream 的“依赖选型”记录上述评估。
- 不得手工编辑 Codex app-server generated Go 或 schema JSON；同步必须走官方 schema 生成命令与 `go generate`。

最终树的硬约束：

- 根目录只保留最终 `package adaptor/adaptor_test`；不得复活 `next/`、`pkg/` 转发包或旧根 API。
- 不允许用兼容 shim 带回中央 SDK、registry、binding、RunHandle 或平行执行入口。
- 公共 Config 必须是真结构体且不泄露 `internal` 类型；Profile、HITL、tool、skill、MCP 与 runtime 词汇必须由正确公共层拥有。
- archive source 等价性不得依赖函数地址判断 closure 内容；Fingerprint 的文档语义必须与 materializer/cache 实际用途一致。
- examples 必须使用最终名与最终 API；不得保留只为迁移期编译的示例。
- Git tag 属于独立发布动作，只能在明确发布授权和发布门禁满足后创建。

## 14. 阻断项关闭记录

接管审计曾列出的发布阻断项均已关闭，并由回归测试或 API 冻结守卫覆盖：

- 所有执行收敛到单一 invocation/Thread 管线；Fork、lease、fingerprint、key 编码与 checkpoint 污染边界已硬化。
- Claude、Cursor、CodeBuddy 的非零退出不再产生有效 checkpoint。
- blocking/drop 背压、Cancel、Event 顺序、关闭时序与 Approval exactly-once 已形成可执行合同。
- Codex app-server 与 batch 路径均保留 Raw streams、Transcript、Output、Summary、provider terminal Result 与 Usage 语义。
- Driver Response 到公共 Result 的逐字段映射、Run 与 Stream.Result 等价性已覆盖。
- Inspect 使用构造时真实 Driver 配置；公共 Config 不再泄露 internal 类型。
- ApprovalRequest 零值、超时、拒绝与 responder 生命周期不会永久阻塞。
- archive/Fingerprint、P4 可用性缺口、Driver SPI 含糊点与 bridge 保真缺口均已有最终裁决和测试。
- 根包导出面由完整 AST golden 冻结；临时 V1 名只在明确版本化的 A2A wire schema 中保留。

若代码、godoc、测试或权威文档对上述任一合同出现矛盾，必须重新打开对应审计项；单纯“测试通过”不能替代合同修复。

### 14.1 2026-09-07 internal 对齐重新打开项

B01 已交付 Claude 一次性 stdin 终止、Tool 安全输入纠错、A2A continuation/完整制品恢复、Dedicated+WithTools 持久 profile 所有权、执行后部分 Result/cause 和 schema/HITL SPI 协商。T04 的 unlock 后清理不得再改接任者状态；Windows owner.lock 必须持有禁止 delete-sharing 的长期句柄。新非 nil schema 矩阵检查含默认 Permission/PlanReview Ask 的有效策略，nil 保留旧显式 Ask 语义。

B02 已交付最终 resolved profile 快照（R003 / W02-R06）、所有准入执行的 core 最终生命周期（R006 / W09-R14）、typed Capability/Todo、observer/publisher 及可行 transport 候选评分。静态 openStream 拒绝保留无资源/Driver 的空 closed Events；准入后公共终局必须在最终 Result/error 与 cleanup 后决定。最终 profile 视图与正式 managed prune 一致；未能证明的 copied tree prune 和 IO 错误稳定拒绝，不得把用户内容从摘要中抹去。Usage 的非 nil 零值只表示观察到有效用量且归一化数值均零，当前类型不证明每个字段都显式出现。

R011 要求 MCP writer 保留既有 regular 文件的实际权限；缺失文件快照采用平台可观察的默认值，实际 writer 默认仍为 0644。兼容 transport 的逐轮 Streaming 选择不单独进入持久 Thread 身份；真实配置、codec、资源环境与 Driver 进程形态检查继续有效。

Claude schema/HITL 与 Claude/CodeBuddy 常驻部分结果、真实 Wait cause、消息累计 Usage 已交付；Claude native 支持 PlanReview/Question Ask，Permission Ask 回退 Prompt。零 raw policy 的交互激活保持原语义，不伪造 Permission 往返。Delegation artifact Parts/Append/LastChunk、opt-in Raw、累计字节边界与深复制已交付。

B03 已交付单一 WithAppendSystemPrompt/Request/Descriptor/Inspect 合同和既有 fingerprint 上的原字节 hash，以及 Policy.ActiveExecutionTimeout。预算从资源准备前开始，Ask 每次请求在入队/回调前持有独立暂停 token，重叠等待最后释放才恢复；墙钟、Close、lease 继续。R012 在唯一原子 Persist 前结算封账，Finalize/返回延迟不扣主动时间但仍使用原可取消 context 决定成功；已健康提交不因之后取消回滚。R014 将计时器锁内已选原因与锁外取消通知分开，后到父取消不能覆盖已选本轮原因，继承同型 cause 不能冒充本轮超时；本轮先耗尽时 errors.As 首先得到本轮限额，同时保留父 cause 身份。原 Cause 和部分 Result 保留，根私有计时器不成为公共 API。

B03 的 A2A capability/todo/parent wire 采用独立 opt-in、闭集/大小/编码校验、原 Meta 的安全 drop 或可观察基础设施错误，并保持旧正文/零时间兼容和 opaque key。SSE/AG-UI/session recorder 保留新 typed 事实与来源；AG-UI tool card 使用无碰撞 tuple。Merge 非 nil bus 只透明转发且要求完成后的绑定证明，保留全 parent error graph。可选 capabilityrecorder 用显式共享 Store、精确 scope/Sequence 查询与既有 observer 首错/迟到合同。R013 分离审批描述 JSON 容器副本和唯一 live responder；所有历史录制移除 responder。

B04 已交付四 Driver 的原生 append 支持/明确 unsupported、正式 capability/Todo 观测与实际 transport 声明。追加原文的字节 hash 同时进入进程/codec/checkpoint guard；未知 alias/父域/任务结果不得变成执行事实。Codex typed skill 输入接受、资源物化与实际执行须区别；缺失 Usage 不得变成观察到的零，已知 stdout EOF 必须有界收尾后判断健康。

Delegation 通过 BindEvents 让 typed 事实先进入 core observer 再进入有损 bus，真实历史 binding proof 在撤销后保留。relay 使用无碰撞可逆域，超长/超深显式保留丢弃原因与数量。每次 Delegate 独立主动预算，不改 Member Policy，Member Ask 不暂停 delegator；终局封账后才有界清理。A2A failure 只提升八项闭集与可信主预算 limit_ms；carrier Reason 第一，R016 只为 bare error 采用同一已排空 Stream 的唯一最后、Meta.RunID 匹配的失败分类，nil error 绝不因事件变失败。

R017 补齐真实冷续接与 Skill/Subagent 验证入口，禁用门不构成实际调用证据。R018 保证 CodeBuddy 物化的 native name 与 resolved catalog 逐字一致，安全 .md 文件名独立编码，SourcePath 保持原生内容责任；正式 error 前闭合未完成观察，完全相同的结果 wrapper 只投影一次 typed ToolResult，Raw/Transcript 仍完整。R019 要求已发布 AG-UI Subagent snapshot/delta 独占工具结构与嵌套 JSON 容器，后续翻译与消费者修改不能彼此改写；原 bridge owner 的 T32 和不变的独立 T21 反例共同验收。

G04 已在精确合流提交验收上述生产修复。B05 的 T20–T24 已交付独立生命周期/协议/policy、Driver 一致性与架构守卫、CI 和五语言文档/离线示例；R020 的独立 post-unlock 故障注入不能代替原生 Windows 证据。HTTP 翻译错误允许明确 failed Task 或可观察基础设施错误，测试不得将单一调度形状写成更强合同，也不得接受任意错误。G05 仍须在合流后的精确 SHA 通过完整测试、vet 与任务包校验才可冻结 implementation_head。T25–T30 平台/live 证据和 G06 发布就绪判定独立保持，不能以 fixture、交叉编译或 CI 配置关闭。

R021：原生 Linux 验收曾发现 CodeBuddy 健康常驻轮次返回 Transcript 时，已登记 stderr callback 仍在追加 parser 数据。原 T08 负责人通过 B05 补充任务 T33 修复：原始字节与 callback 归属在同一交接锁下确定，卸下本轮 observer 后在锁外等待已登记 callback 完成，才 finalize 和构造 Response。不得等待健康常驻进程整体退出，不得将闲时或下一轮字节补写进已返回 Result；取消/非零退出仍保留原始 cause、部分审计、单 writer 和健康 checkpoint。W04-R06 最终实现归 T33，独立复验归 T25 与 T28；旧失败和所有 96 项保留，G05 与 B06 均须重新验收新 SHA。

R022：Codex 健康常驻测试不得把子进程 stderr 写入和独立 stdout 终局的顺序，误认为宿主已接收 stderr 的证明。已观察字节必须完整保留；真实子进程与同一捕获缓冲区的接收 gate 验证 exact Raw，公开路径独立验证同一 Response 的逐字段映射及 Run/Stream/结果冻结。one-shot 与失败 drain 的 exact stderr、正式 Usage/Terminal、cause/checkpoint 继续严格。补充 T34 只改测试与局部文档；原生产代码和 W04-R07 的 T16/T20/T29 责任不变。原未打印字段的 Linux 失败不倒填原因，同型实值复现与可达读前反例分开归档；G05 与全部 B06 用新 SHA 重验。

R023：原生 Linux 又在 Claude 审批拒绝和 Codex 取消时发现生产关闭 panic。共享 stdin 的 writer 与进程退出均可合法终结，done 的检查和关闭必须在同一同步边界内幂等执行；Close 停止接收并保留健康 FIFO drain，不能因此跳过终结通知。Codex 外部 Close 不得与唯一 RPC reader 竞争关闭 pending；关闭自有 transport 并解除 active RPC 等待，保持 caller/RPC 主因、正式 FIFO、完整 Raw drain、失败无 checkpoint，禁止 handler 自等待或 recover 掩盖 panic。补充 T35/T36 由原 T07/T16 负责人实施，W04-R05/R07 最终 owner 改为 T35/T36、独立 verifier 为 T25 加原 T27/T29 live，T20 仍保留全部原测试但当前独立职责为22项。两处真实panic与中断测试均保留，替换 G05 后全部 B06 同一新 SHA 重验。

R024：2026-09-10 main 审查补充约束：skill 附件及非配置 manifest 资源按原字节计算指纹，不能按扩展名强制解析为配置对象；Codex 无关 config override 保留官方 literal-string fallback，reserved 提示键仍拒绝；成功终局后的 stdout EOF 先有界 drain/Wait，再根据真实退出与协议状态判断健康。已接收审批 error/panic 在取消竞态下保留 cause，既有主因、fallback 和迟到 handler 边界不变。Windows append 私有目录必须在创建时建立受保护 DACL，文件在写入提示前保护，并验证 owner/ACL；Chmod 模式位和交叉编译不能代替原生权限证据。修复后的精确 SHA 仍须独立验收，不沿用旧 G05/G06 结论。

具体冻结设计、未支持边界、文件所有权和fixture见 `docs/alignment-tasks/2026-09-07/contracts/frozen.json`。合同冻结不代表代码已实现；后续同批godoc、合同测试、使用文档和CHANGELOG完成后才能关闭对应项。其他第14节既有保护继续有效。

## 15. 发布门禁

发布前必须全部满足：

- S1–S9 场景测试全绿
- `go test -count=1 ./...`
- `go vet ./...`
- Linux CI 下 `go test -race ./...`
- archive fuzz 与关键 parser fuzz 通过约定时长
- 四个内置 Driver 全部通过最终 `adaptertest`
- live conformance 保持显式环境变量双门，普通 CI 不产生付费调用
- Thread mode、lease、resume reject、fork、checkpoint 污染测试重复运行稳定
- Event 顺序、关闭、Cancel、blocking/drop 背压与 Approval exactly-once race 测试通过
- Result 各层在 Run 与 Stream.Result 上逐字段等价
- 所有 examples 编译，fake-driver 示例可执行
- 根包 godoc 以六个核心名词开篇，27 个 `With*` 名与约 13 个核心概念组的心智负担目标达成；`WithAppendSystemPrompt` 是原生追加提示的唯一根包入口；新增的 `WithTools` 是宿主定义能力的唯一根包入口，`WithSpawn` 是常驻进程默认语义的唯一显式反向开关；根包全部公共声明由 `testdata/root_api.golden` 的完整 AST golden 冻结
- README、文档地图、API reference、streaming、run policy、A2A、structured output 与代码一致
- 无 TODO、无死代码、无临时 V1 后缀、无过期兼容入口
- CHANGELOG 与实际 breaking changes 一致
- 当前脏工作树中的 PRE 改动已逐项验收并形成可追溯提交
- `v1.0.0` tag 只在上述条件全部满足且获得用户/发布负责人明确授权后创建

## 16. 对未来修改者的硬要求

- 始终保持六名词模型，不为局部便利增加平行抽象。
- 始终保持一个构造入口、一套选项合并、一条执行管线、一条 Event 流和一个 error 判定面。
- 不得让 helper、bridge 或 hosttool 吞并 Driver 的协议职责。
- 不得让 core SDK 长成服务框架、团队编排器或自动 Agent router。
- 不得以兼容为理由保留未经设计批准的旧 API。
- 不得静默丢弃输出、事件、checkpoint 或安全相关状态。
- 不得留下 TODO、死代码、临时 alias 或“后续再补”的发布缺口。
- 任何公共语义变化必须同时更新合同测试、godoc、相关使用文档与 CHANGELOG。
