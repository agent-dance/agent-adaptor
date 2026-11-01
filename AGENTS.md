# agent-adaptor v2 AGENTS

本文件定义 `agent-adaptor/v2` 的架构边界、公共语义、迁移纪律与发布门禁。后续设计、实现、重构、评审和文档均必须以本文件为准。

## 1. 项目定位

`agent-adaptor` 是一个可嵌入宿主的 Go SDK。

它负责：

- 以统一 API 调用不同本地 coding agent
- 统一构造、消息投递、流式执行与有状态对话语义
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

v2 的消费者心智只能由以下六个核心名词组成：

| 名词 | 含义 |
|---|---|
| `Agent` | 一个配置完整、构造后即可执行的智能体 |
| `Thread` | 一段由宿主 key 标识、可续接、分叉或待命的对话 |
| `Stream` | 一次正在进行的执行 |
| `Event` | 执行过程中发生的一件 typed 事件 |
| `Result` | 一次执行的最终结果与审计信息 |
| `Driver` | 一种 agent CLI/provider 的接入实现，属于扩展方 SPI |

硬约束：

- 不存在中央 SDK 对象。
- 消费者执行动词只有 `Send`；`Agent.Send`、`Thread.Send` 开始执行，`Stream.Send` 补充本次执行。不得保留旧执行入口或新增平行入口。
- `Standby` 是 Thread 的待命句柄，归入 Thread；不得成为第七个核心名词。同步取结果与消费 Events 必须使用同一次 Send 返回的 Stream。
- 不得重新引入 binding、默认 Agent、字符串查找 Runner 等平行抽象。
- 根包面向应用开发者；SPI 必须收敛在 `driver/`，不得再次把扩展方合同铺回根包 godoc。

## 3. 包布局与依赖方向

目标包布局：

```text
github.com/agent-dance/agent-adaptor/v2       package adaptor
├── driver/                                    Driver SPI
├── codex/ claude/ cursor/ codebuddy/          各驱动 Config 与 Driver(Config)
├── tool/                                      宿主定义 Tool 词汇
├── skill/                                     skill 词汇与来源
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
- bridges 与 hosttools 只能消费公开 `Runner`、`Stream`、`Event`、`Result` 等合同，不得调用内部 engine 或直接派发 Driver。
- 公共类型不得通过 alias、字段或方法签名泄露 `internal/*` 类型。

## 4. 唯一构造语义

消费者唯一构造合同为 `adaptor.New(d driver.Driver, opts ...Option) *Agent`；模块路径为 `github.com/agent-dance/agent-adaptor/v2`。

- 内置驱动包拥有真实 `Config` 结构体，以 `Driver(Config) driver.Driver` 返回捕获构造配置的 Driver；第三方实现走同一个 `New`。
- `New` 对 nil Driver 等编程错误可以 panic；运行环境、能力或配置错误必须在 Send 的 Result/Done 或 Inspect 中结构化返回。
- Driver 配置不得以 `internal/engine` 类型别名对外暴露。
- Inspect、Send、配置校验及 capability resolution 必须观察同一份构造配置。对已配置 Driver，nil config 表示使用已捕获配置，不能丢失配置语义。
- 未指定 `WithThreadStore` 时，每个 Agent 必须创建独立的内存 store；跨 Agent 或跨进程续接由宿主显式共享 store。

## 5. 选项与唯一执行管线

选项使用一套词汇与编译期作用域：

- `Option`：只用于 New。
- `CallOption`：用于 Agent.Send、Thread.Send 和 Standby.Send。
- `SharedOption`：用于构造默认值与单次执行覆盖。
- `MessageOption`：同时实现 CallOption；只有 `From(Author)`、`MessageID(string)`、`When(Timing)` 这类消息选项可以传给 Stream.Send，执行级选项必须编译失败。

合并规则：近处覆盖远处；skills 追加；其余能力按公开合同替换或显式合并。`WithPolicy` 整体替换 Policy，不得单独合并 Messages 或 Approvals。

`Policy.Messages` 是消息策略的唯一入口：

- `Disabled` 禁止补充并省略 Request.Messages，不得禁用初始 prompt。
- `Format` 为 nil 时使用内置作者与权威模板；初始 prompt 不包装，补充交付必须使用已包装文本。
- `MaxPending` 的零值为 32，限制未领取消息；满队列阻塞 Send，必须可被 ctx/Cancel 解除。
- `MaxTurns` 的零值为 20；负上限必须在启动前返回 ErrInvalidPolicy。达到约 80% 时发布 `Notice{Kind: "limit.approaching"}` 并注入 AuthorSystem 收尾提示；达到上限不得再开一轮，剩余消息以 limit_exceeded 失败并交还原文。
- 默认 AuthorHuman；AuthorAgent 不具备人的指令权威，AuthorSystem 表示宿主策略。模板必须保留原目标、并入最终回答、不要求单独确认。

所有执行必须收敛到同一内部管线：

1. 合并构造默认值与 CallOption，分配立即可用的 RunID 与唯一消息 hub/Event sink。
2. 校验配置、policy、schema、审批及 Thread Driver 合同；接纳执行并允许启动前消息排队。
3. 对具名 Thread 协调模式与 ownership lease；解析 workspace、profile、Tools、skills、MCP、runtime services。
4. 生成 resolved invocation；协调 checkpoint、resume/fork、fingerprint 与已投递 ID；按实际 transport 调用 MessageCapabilityResolver 并校正排队消息。
5. 通过唯一派发位置调用 `driver.Run`；每轮发布 TurnEnd。Driver 可在一次调用内续跑，core 也可用本轮健康 checkpoint 在同一执行内再次派发，不能创建第二条消费者执行管线。
6. 每个健康返回点在有效 lease 下原子保存 checkpoint、消息回执与有界连续性信息；处理剩余消息或原子封闭接纳。
7. 完成结构化校验、消息结算、结果汇总、有界资源清理与 lease 处理后，发布唯一权威终局 Done，关闭 Events，使 Result 可立即返回。

硬约束：

- Agent 与 Thread 都实现只有 Send 的 Runner；ThreadRunner 只增加 Key、Standby、Checkpoint。Agent.Send 对宿主等价于新的匿名 Thread：不跨调用共享上下文或去重状态，允许并发；不得强迫无状态调用通过具名 Thread 的 store/lease 合同。
- Result 与 Events 只是同一执行的两种读取方式，不得再次合并默认值、解析资源或派发 Driver。`RunAs[T]` 必须委托 Runner.Send。
- SDK Event 流与 provider streaming transport 分开协商，不得因为调用 Result 或 Events 改变协议。
- 一次执行可以跨多轮及多次 driver.Run；只有 core 负责最终 Done，不能把 provider 终局或 TurnEnd 当作整个执行的最终裁决。
- bridges、hosttools、structured output 与 Inspect 不得引入第二套执行入口或默认值语义。

进程生命周期合同：

- Claude、CodeBuddy、Codex 对显式 Thread 默认允许复用常驻进程；Cursor 与匿名调用仍按各自 transport 启动进程。进程复用与 Thread 待命是不同维度。
- `WithSpawn()` 为双作用域选项，强制本轮新进程，不得注册为后续常驻 writer。
- Stream 与 Standby 只有 Cancel，没有 Close；Stream.Cancel 结束本次执行，Standby.Cancel 中止当前处理并结算排队输入，随后继续待命。结束待命必须取消创建它的 ctx。
- `Agent.Close(ctx)` 仍是 Agent 生命周期清理入口，必须幂等取消并排空执行、回收常驻进程及 Tools runtime。开始 Close 后新执行稳定返回 ErrAgentClosed。
- Thread 重绑、配置漂移、临时单次形态与预热必须保持单 writer：旧进程有界退出后才启动 replacement。
- prompt 交付前的常驻启动失败可以安全回退一次；prompt 可能已经交付后不得自动重放。显式 Standby 的分类失败重试须遵守 §6 的可观察合同。

## 6. Thread 语义

Agent.Send 默认无跨调用记忆；具名 `agent.Thread(key)` 默认使用 Agent 内存 store，`WithThreadStore(store)` 替换为宿主存储。公共动作仅为 Thread(key, ResumeOnly())、Send、Fork(newKey)、Checkpoint(ctx)、Standby(ctx) 与 Key。

身份、兼容性与持久化合同：

- Thread key 是宿主提供的不透明字符串，必须逐字保存和比较。新会话必须使用新 key；不得提供同 key 的 NewThread、start_new 或主动重绑入口。
- 内部与 bridge 的复合 key 必须使用无碰撞编码；provider resume ID 只是内部 checkpoint，不得成为第二种消费者身份。
- Thread Driver 必须支持 resume，提供稳定的 SessionCodec 与 SessionConfigFingerprinter；不完整的合同必须在获取资源或 store lease 前以 ErrThreadIncompatible 拒绝。
- 同一 Thread 同时只有一个有效 ownership lease 的写者。任务模式的 Thread.Send 遇到执行或 live Standby 必须立即通过 Stream.Result/Done 返回 ErrThreadBusy，不得并入；补充只能发给该 Stream。
- Standby 在空闲时仍持有并续期 ownership lease；共享 store 的其他 Agent/进程必须得到 ErrThreadBusy。本 Agent 对同 key 重复 Standby 返回已有 live 句柄，生命周期仍属于第一次调用的 ctx。
- lease acquire、renew、finalize、release 必须校验 owner/token；失去 ownership 必须停止派发与写入，旧运行不得覆盖新状态。释放有界且错误可观察。
- 正常任务结束释放 ownership；存储恢复尚未完成时允许保留 ownership 并重试。Standby 退出必须释放 ownership，即使仍有待写 checkpoint；后台恢复必须重新获取 ownership 并核对 durable version，禁止覆盖后来写者。
- Fork 必须在首轮执行时持必要父协调租约，验证 checkpoint、Driver、配置、identity 与 fingerprint；不得修改或归档父 Thread。目标已有 active record 时返回 ErrThreadAlreadyExists，Finalize 必须原子检查 RequireKeyAbsent。
- continue-or-start 对配置不兼容或明确的 resume 拒绝最多安全回退一次；ResumeOnly 不得静默新建。旧状态只能在新的健康 checkpoint 成功 Finalize 后归档。
- Store.Finalize 必须原子执行 save/archive/rebind、回执和连续性数据写入及 lease 校验。非法 selector 必须在获取资源前拒绝。
- 可恢复的存储失败必须保留进程内最新健康 checkpoint、发布 thread.persistence Notice 并后台退避补写；下一执行先尝试补写，仍不可用时可在有效 ownership 下继续。该保证不提供进程崩溃后的未落盘恢复。
- DeliveredMessages 回执与 checkpoint 在同一 lease 下原子持久化，保留最近最多 1000 个且不超过七天的 ID。重复已交付初始 MessageID 以零 Turns、空 Text 完成，不重复推理；Fork 的 ID 空间独立。
- provider 不持久化的已投递补充写入 EarlierGuidance；RecentTurns 保存最近三轮 assistant 文本，与指导合计不超过 8 KiB。新会话回退发布 thread.continuity Notice 并注入有界前缀，不得宣称恢复完整历史。
- provider 接受消息与 core 持久化回执之间的崩溃窗口允许恢复后重投；不得承诺跨该窗口的 exactly-once。宿主恢复使用原 MessageID 重发未确认输入。

兼容 fingerprint 必须确定性、跨进程稳定，覆盖 Driver 构造配置与 codec/version、模型、identity、实际解析 workspace、profile/materialized resources、Tools、skills、instructions、MCP、runtime service 环境及 SessionCodec 其他兼容维度。不得因漏项错误复用；无法稳定表达的配置必须通过稳定扩展点 fingerprint，或在 Thread 启动前拒绝。

Standby 合同：

- `Thread.Standby(ctx) (Standby, error)` 无需初始消息；Standby 不提供整体 Result。每个 provider 轮次产生 TurnEnd，每次执行以 Done 结束，所有事件在一个顺序 channel 上交付。
- Standby.Send 的 ctx 只约束接纳，接纳后输入与执行属于 Standby ctx；HTTP 请求结束不得撤回已接纳输入。纯消息选项在忙碌时并入当前执行，空闲时开启新执行；带执行级 CallOption 的消息排到当前执行之后。
- Stream.Send 的 ctx 约束接纳及尚未 Claim 的输入生命周期；Claim 后只由 Driver Ack 决定，不得因调用方 ctx 结束抢先失败。
- Standby.Cancel 取消当前处理，将尚未交付的排队/携带输入以 cancelled 交还；Standby 本身继续接受输入。Standby ctx 结束则取消当前执行、结算输入、交付终局事件并关闭 channel。
- 一次执行失败不结束待命。仅 run_failed 的未交付输入携带到下次执行；同一 ID 先报告 failed/run_failed，恢复交付后再报告 delivered，不得伪造首轮成功。
- 仅已分类 rate-limit、network、provider-server 失败可自动重试一次；使用结构化 retry_after，无有效 hint 时默认等待 250ms。普通错误文本不得触发重试；取消与截止不重试。无重试或一次重试仍失败时，携带输入等待下一条宿主输入唤醒，不得无限自行续跑。
- 从未调用 Standby.Events 时事件不缓冲；订阅后应用正常背压合同。Standby 不是历史回放或广播器。

## 7. Stream、Event 与 HITL

Stream 合同固定为 Send(ctx, text, ...MessageOption) error、Events() <-chan Event、Result() (*Result, error)、RunID() string、Cancel()；不得增设语义流、操作流、决策 channel 或 Close。

- 一次执行只有一条 typed Event 流，承载文本、thinking、tool、生命周期、进程、notice、drop、宿主事件、审批与消息交付。
- RunID 在 Send 返回时可用；EventMeta 的 RunID、Sequence、Time、ThreadKey 由 core 唯一赋值，并发 producer 的接收顺序必须与序号一致。provider 坐标只进入 Source。
- MessageUpdate 是不可丢弃的接纳、重排队、delivered 或 failed 回执。失败 Text 保留原文；未接纳的错误同步返回 MessageError，不得伪造 delivered。Soon 取最早可用边界，AfterTurn 仅轮次末尾，Interrupt 仅 Driver 明确支持时接纳。
- TurnEnd 表示 provider 一轮的文本与已观察 Usage，Turn 从 1 开始，MessageIDs 关联本轮已交付消息；不得据此推断 checkpoint、校验及清理成功。
- Done 是每次执行最后一个事件，必须在持久化处理、校验、清理与消息结算之后发布，即使启动失败或取消也必须交付；其 Result 指针与 Err 必须与 Stream.Result 完全一致。RunFinished 仅供参考。
- Result 可并发多次调用，内容一致，Events 关闭后应立即返回；允许先读取 Result 再读取保留的关键事件。需要完整普通事件时必须先调用 Events。
- 未订阅或仅 Result 的执行不得被普通事件背压永久阻塞：保留生命周期、消息、轮次、审批、Notice 与 Done，将普通事件汇总为 Dropped。默认背压仅允许丢弃合同允许的增量；正常订阅时 tool result 与生命周期等关键语义必须可靠交付。
- Dropped 必须包含完整丢失计数、分类与序号范围，且只能在 Done 之前发布。WithBlockingEvents 与取消必须避免 sink/执行 goroutine 闭环等待。
- Cancel 幂等，解除阻塞发布、未领取消息、审批与资源等待；已 Claim 的输入继续由 Ack 结算，Driver 返回前必须完成所有 Ack。

HITL 只通过 ApprovalRequest 表达：

- Kind 仅 Permission、PlanReview、Question；请求自带 Approve、Deny、Answer。回调使用 OnApproval，UI 消费 Event 中的请求。
- 应答 exactly-once；重复、Kind 不匹配、已过期返回稳定错误。零值、未绑定 responder 或脱离运行环境的方法立即返回明确错误，不得阻塞 nil channel。
- `Origin() Origin` 报告 Member/Task；单 Agent 为零值。`Forward(Origin)` 返回归属副本，必须共享原 exactly-once responder，原件与副本不能各应答一次。
- 请求出现时从未调用 Stream.Events 且无 OnApproval，必须立即按 Policy.Approvals 的 OnTimeout 兜底并发布说明 Notice，不等待完整超时。需要事件应答的宿主必须及时订阅；调用 Result 不能代替订阅。
- 超时、重试与兜底只属于 Policy.Approvals；bridge/Driver 不得复制策略，也不得在没有真实风险信号时伪造 Risk 能力。

## 8. Result、错误与输出合同

成功从 Stream.Result 返回 `*Result, nil`；业务失败返回 `nil, *RunError`，其 Result 携带可获得的完整或部分结果；基础设施失败走可 errors.Is/As 的包装错误。Done 必须保持相同返回语义，不得在成功 Result 中新增 Failure 判定面。

- Text 是最后一轮最终 assistant-facing 文本，允许为空，不得承载 stdout dump、Summary 或 provider 终局 JSON。
- Summary 为有界短摘要，没有可用摘要时允许为空，不得退化成完整 Text。
- Turns 为本次执行实际 provider 轮数；重复已交付初始 ID 可以为零。Usage 汇总已观察轮次与实际附加子任务用量，不能重复计数；nil 与观察到的零值不同。
- Raw() 提供各次派发完整 stdout/stderr 的合并以及最后识别的 provider 终局原始 payload；Transcript() 包含各轮正式解析的语义条目。
- core 可以追加 Kind 为 message、Message 为 TranscriptMessage 的实际补充交付记录；保留 ID、作者、机制、边界与原文，不得伪装成 provider user 角色或猜测解析条目。
- Messages() 按接纳顺序返回最终 MessageOutcome；Undelivered() 返回失败项及原文。MessageUpdate 是过程回执，结果方法是执行结束时快照；均不得静默遗漏已接纳输入。
- MessageError 为接纳拒绝载体，保留 ID、Text、Reason、Err 并 Unwrap；ErrRunFinished、ErrMessageUnsupported、ctx 等必须可 errors.Is 匹配。接纳返回 nil 不等于已投递。
- Subagents() 只提供已收集的子任务摘要及观察到的用量，不能把声明回显成执行证据；不得把所有子任务 Raw/Transcript 填入根 Result。
- Services() 只报告实际观察的 runtime services；Decode() 解码已经校验的结构化输出，无 schema 时才按明确文档从 Text 便利解码。
- Model、Provider、Metadata 保持独立含义；Raw、Transcript、Services、Messages、Undelivered、Subagents 的返回值按公开合同复制，Done 与 Result 本体共享指针。

结构化输出仅有一种协商语义：优先 provider 原生 schema 约束，不支持时自动回退 Prompt 加本地校验，两者都不可用则启动前失败。消费者与 Driver SPI 不得暴露机制选择；Driver 只执行 core 已解析机制。所有读取方式都必须保留相同的输出与审计层。

## 9. Driver、消息交付与 checkpoint 边界

`driver/` 是规范 SPI 所在层；根包 Driver 别名只是构造依赖的便利用法，不得铺开扩展方合同。Driver 必须准确声明 Descriptor、校验配置、解析正式协议，并从同次解析生成 Text、Summary、Raw、Transcript、terminal payload、Usage、checkpoint；不得猜测或伪造成功。

消息 SPI 以 `driver/messages.go` 为冻结合同：

- core 拥有队列、顺序、ID 去重、framing、取消与记账；Request.Messages 为本轮 MessagePort，禁用时为 nil。Driver 不得持有独立语义队列，不得在 Run 返回后使用 port 或 sink。
- MessageCapability 只声明已验证的 immediate/tool/stop 边界、Interrupt、ContinueMode、framed MaxUnits/MessageUnit、ConfirmedDelivery、PersistsDelivered、MinVersion。turn_end 对可续跑 Driver 隐含，不要求列入 Boundaries；零值表示无中途边界、无打断、支持 resume 时由 core 续接。
- MessageCapabilityResolver 每 invocation 按最终解析 Request 协商，与实际 transport 一致，不能超出 Descriptor。core 在派发前原子修正能力与预排队消息；不支持 Interrupt 必须拒绝，不得降成 Soon。MinVersion 只是最低版本要求，未做本机探测不得声称已验证。
- Window(true) 只能来自 provider 确认的活跃窗口，结束时必须 Window(false)；不得把本地进程启动等同于可投递。push Driver 监听 Signal，hook Driver 在真实回调中 Claim。
- Claim 原子领取边界可交付项，保持接纳顺序；Soon 可在任意允许边界、AfterTurn 只在 turn_end、Interrupt 只在 immediate。Text 已包装，必须逐字交付。
- Claim 后每项必须恰好一次 Ack，且发生在 Run 返回前。调用方 ctx/Cancel 不得抢先结算已 Claim 项；Driver 仍必须响应运行取消并停止 I/O、结算所有 claim。
- MessageAck.Delivered 仅表示 provider 在该边界接受；Mechanism 必须真实。ConfirmedDelivery=true 必须等待正式 provider echo，HTTP/pipe 写入成功不能替代。
- 未交付 Ack 必须带 Err。边界故障可 Requeue=true，core 使其仅在 turn_end 重试；可能已经交付时同时 MaybeDelivered=true，重试项标记 MaybeDuplicate 并附防重复说明。取消不得启动重投；不得在相同失败边界忙循环。
- ContinueResume 每轮返回，由 core 使用本轮健康 checkpoint 续接；ContinueInRun 每轮结束 Claim(turn_end)，有消息就在同次 Run 续跑，空则返回。最终空 Claim 不是原子封闭，晚到已接纳消息由 core resume；若只有 InRun 且无 resume，则必须明确 failed，不能丢失。
- Driver 每个 provider 轮次（含最后一轮）恰好发一个 RunEventTurnCompleted，携带从 0 开始的调用内 Index、最终 Text、已观察 Usage、Interrupted、CompletedAt。core 转为执行内从 1 开始的 TurnEnd 并合计用量。Driver 不发布消费者 Done。
- PersistsDelivered 只能在 resume 后确实保留输入时为真；为假时 core 在 resume/Thread 中重放有界已交付指导，不能以写入成功推断历史持久化。回执由 core 与健康 checkpoint 原子落盘。

内置 Driver 的最低诚实声明：Codex app-server 为 immediate/Interrupt/InRun 且持久化补充，exec 为 Resume；Claude 为 tool/stop/InRun，Interrupt 与 PersistsDelivered 为 false，hook 注册失败或 10 秒未答复时发布 Notice 并由 core Resume；Cursor 为 tool/Resume，以 echo 确认，完整 hook context 上限 10000 UTF-16（可接纳 framed 上限扣除 nonce 标签），Windows 与不合资格 workspace/relay 使用 Resume；CodeBuddy 无已验证中途边界或 Interrupt，只使用 Resume。Pi/OpenCode 不属于 v2 已支持 Driver。

shared process helper 只负责启动/停止、stdin、完整 raw 捕获、chunk 发布与 tee 给 Driver parser；不得解释 provider 语义、递归扫描 JSON 猜 checkpoint、构造语义 Transcript、判健康或把 stdout 放进 Text。

checkpoint 安全合同：

- 只能来自对应 Driver 正式 parser 的明确、codec 可归一化且可续接状态；init、嵌套猜测 ID、部分输出不充分。不同 provider 对正式 checkpoint ID 的位置以各自协议合同为准。
- 非零退出、signal、timeout、取消、协议错误、业务失败或 Driver error 不得产生有效新 checkpoint；不允许失败路径例外覆盖健康状态。
- 无健康新 checkpoint 时必须保持之前健康 resume state。已交付的临时指导与回执可在仍持 ownership 的既有健康 state 上保存，但不得推进失败 provider 状态。
- Claude、Cursor、CodeBuddy、Codex CLI/app-server 分别覆盖成功、非零退出、畸形协议、缺失 terminal/checkpoint 与取消；app-server 不得降低 Raw、Transcript、terminal payload 合同。

SPI 的 MUST/SHOULD、事件生命周期、Sequence/Seq 权威性、SessionCodec nil/零值映射与能力蕴含关系，必须与 godoc、API golden、adaptertest 及 Driver 专项合同测试一致；通用一致性套件尚未覆盖的消息条款必须保留为发布缺口，不得声称已通过。

## 10. Inspect、tool、skill、MCP、profile 与 runtime

- `Agent.Inspect()` 是只读探针入口，不是控制面或执行面。
- Environment、Models、Quota、ConfigSchema、Skills 等探针不支持时必须返回明确 unavailable/unsupported 语义，不得伪造。
- Inspect 必须使用 Agent 构造时真实 Driver 配置。
- `ProfileState` 只报告 desired 与 observed；`SyncProfile` 才执行资源物化。
- skill 物化、schema 协商、profile 同步等启动前错误必须显式失败，不得降级成 warning 后继续运行。
- `tool`、`skill`、`mcp`、`profile` 必须各自拥有完整公共词汇；调用方不得为了构造这些值 import `driver` 或 `internal`。
- Runtime service 发布 MCP 必须使用类型化字段，v2 不保留 stringly metadata 兼容解析。
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

## 14. 审计与关闭记录

v1 已硬化的单管线、Fork、lease/token、fingerprint、key 编码、checkpoint 污染、Raw 分层、Inspect 配置与审批 exactly-once 合同继续有效。v2 的回归证据包括：

- messages_contract_test、messages_hub_test、message_review_test：Done/Result 一致、resume/InRun、最后 Claim 竞态、取消所有权、重排队、UTF-16、ID 去重、上限、审批立即兜底与转发 responder。
- standby_contract_test、standby_internal_test、thread_recovery_test：模式冲突、重复待命、空闲 ownership、取消后继续、一次分类重试、失败输入携带、store 恢复与有界连续性。
- 各 Driver messages/handshake/relay 测试：真实能力声明、每轮完成、确认与降级路径；Codex 另有双门控消息 live 测试。
- testdata/root_api.golden 与 adaptertest/testdata/driver_api.golden：最终公共 AST 导出面。

不得把上述测试存在等同于发布门禁全部通过。当前 adaptertest 未提供 MessagePort/MessageCapability/TurnCompleted 的通用消息一致性条款；最低 CLI 版本没有自动探测，未授权 live 与 Linux race/fuzz 结果也不得伪称已完成。

代码、godoc、测试或权威文档出现合同矛盾时必须重新打开审计项；单纯测试通过不能代替修复。

## 15. 发布门禁

v2 发布前必须全部满足：

- S1–S9 场景、`go test -count=1 ./...`、`go vet ./...`、Linux CI `go test -race ./...` 全绿；archive 与关键 parser fuzz 通过约定时长。
- 四个内置 Driver 均通过最终 adaptertest；补齐并通过通用消息交付一致性条款，不得仅靠 Driver 专项测试替代该门禁。
- 消息合同覆盖接纳前后能力解析、启动前排队、边界 Claim/Ack 恰好一次、取消所有权、Requeue/MaybeDelivered、大小限制、队列/轮次上限、最终空 Claim 竞态、无 resume 的明确失败与原文返回。
- Thread/Standby 模式、ownership acquire/renew/loss/release、fork、resume reject、持久化失败恢复、已投递回执去重与崩溃重投窗口测试重复运行稳定；关键取消/交付竞态至少重复 1000 次，并在 Linux race 下验证。
- 每轮 TurnCompleted/TurnEnd、Usage 不重计、Done 最后且与 Result 指针/错误一致；取消、Result-only、无人订阅、blocking/drop 背压、Approval exactly-once/Origin/Forward/立即兜底全部验证。
- 每个内置 Driver 的实际 transport 均覆盖消息声明、交付/确认、turn_end 续接、原始输出及 checkpoint 清洁性；Claude hooks 降级、Cursor relay/nonce/echo/UTF-16/Windows unavailable 必须有合同测试。
- live conformance 维持显式门控，普通 CI 不产生付费调用。Codex 消息 live 必须同时使用 `-tags=codex_live` 与 `AGENT_ADAPTOR_LIVE_CONFORMANCE=1`，并检查 CLI 可用；编译 live suite 不等于通过 live。
- 所有 examples 编译，fake-driver 示例可执行；文档 Go 示例必须编译，临时校验包必须删除。
- 根包 godoc 仍以六名词开篇；Send 是唯一消费者执行入口，Standby 属于 Thread；消息选项不新增 With*。根包最终 AST 由 testdata/root_api.golden 冻结，SPI 由 adaptertest/testdata/driver_api.golden 冻结；不得用兼容入口规避 golden。
- README、文档地图、API reference、streaming、policy、structured output、协议桥及 Driver 专题文档与代码一致；CHANGELOG 的 breaking changes 与迁移要求准确。
- 无未决占位、死代码、临时 V1 后缀或过期兼容入口；迁移改动逐项验收并形成可追溯提交。
- `v2.0.0` tag 是独立发布动作，只有全部门禁通过且获得用户/发布负责人明确授权后才能创建。

## 16. 对未来修改者的硬要求

- 保持六名词模型；Thread 待命、消息选项、交付 SPI 不得形成平行抽象。
- 保持一个 New、一套选项合并、一条 Send 执行管线、一条 Event 流、一个 Go error 判定面与最后唯一 Done。
- 不得复活 v1 Run/Stream 执行方法、Steer、NotReady、Close 型执行句柄或兼容 shim；Agent.Close 的资源清理合同继续保留。
- 保持 core 的队列/Claim 所有权与 Driver 的协议/确认职责；helper、bridge、hosttool 不得吞并 Driver parser 或复制审批策略。
- 不得让 core 变成宿主服务框架、持久队列、团队编排器或自动 Agent router。
- 不得静默丢失消息原文、输出、事件、checkpoint、回执或安全状态；不能宣称跨 provider 接受与 receipt 落盘崩溃窗口的 exactly-once。
- 未验证的边界、Interrupt、echo、跨 resume 保留或最低版本必须如实报告，不得编造成功或隐蔽降级。
- 不得留下占位、死代码、临时 alias 或发布缺口；公共语义变化必须同步合同测试、golden、godoc、相关使用文档与 CHANGELOG。
