# C03：Event、父关联、Capability、Todo、观察与 A2A 合同

版本：C03/1，2026-09-07。本文是 B00 的实施合同；不是已实现或已通过 live 的声明。权威基线为 AGENTS.md 和对齐方案 W05/W09/W10/W12。目标代码 base 为 `bc0d421f9c0b1e80e529d1e843fe5d8396eab02f`；internal 仅从固定 `e2f0620bdd6477e6fe16f6db5648093589342ca2` 及 task.json 指定提交读取。

本文所有列出的名称、字段、值、零值、错误、顺序与默认暴露均为选定合同；没有供实施者自行选择的候选接口。代码块中的类型声明表示应增加的真实 Go 声明；“字段追加”块只列出对既存类型增加的字段，不重声明既存字段。

## 1. 归属与文件分配

| Owner / 批次 | 文件 | 固定职责 |
|---|---|---|
| T06 / B02 | `capability/types.go`, `todo/types.go` | 独立公开叶包值；只依赖标准库，不依赖 root/driver/internal |
| T06 / B02 | `events.go`, `result.go` | 两种 sealed Event、父关联、clone、drop、Transcript 映射 |
| T06 / B02 | `driver/events.go`, `driver/driver.go`, `driver/run.go`, `driver/runtime.go`, `driver/doc.go` | SPI 字段、需求/能力矩阵、godoc；没有 provider parser |
| T06 / B02 | `runservices.go`, `sink.go`, `event_broker.go`, `options.go`, `wiring.go`, `internal/engine/driver_types.go`, `internal/engine/clone_exports.go` | 绑定 publisher、observer、唯一序号与需求转换；映射无 internal 类型外泄 |
| T06 / B02 | `internal/capabilityobs/catalog.go`, `tracker.go`; `internal/todoobs/table.go` 及同目录 tests | 只接受规范化值的 catalog/tracker/table |
| T06 / B02 | `alignment_event_contract_test.go`, `alignment_observer_test.go`, 两份 root/SPI golden、既存事件 tests | 本文的公共/中立合同；逐声明审阅 golden |
| T11 / B03 | `bridges/a2a/**` | DTO、严格 codec、exposure、无 Store demand provider；不 import 同批 T10 新增类型 |
| T12 / B03 | `bridges/{agui,sse,subagentstream}/**`, `hosttools/sessionrecorder/**` | 翻译与事件持久化，保留坐标/空快照；不解释 provider |
| T13 / B03 | `hosttools/capabilityrecorder/**` | 公开 Store/Query、组件 Option 与安全写入 |
| T14–T17 / B04 | `claude/**`, `codebuddy/**`, `codex/**`, `cursor/**` | 正式协议识别、catalog 构建、成功结果确认、真实 transport 能力 |
| T18 / B04 | `hosttools/a2adelegation/**` | Local/Remote 相同投影、publisher-before-EventBus、命名域、外层生命周期 |
| G00 及各 gate | 中央文档/CHANGELOG/冻结清单 | 合并本文与 documentation.md；不由 worker 越界写 |

不新增 root 查询面、执行方法、With* 名、通用 observation 包或 runtime 依赖。C02 负责主动预算/部分 Result；C04 负责 schema/native prompt；三份合同共享同一个 resolved invocation 和 `Request.Streaming`。

## 2. 父工具关联、唯一坐标与生命周期

对 `adaptor.ToolCall`、`adaptor.ToolResult`、`driver.StreamPayload`、`driver.TranscriptItem` 追加完全同名字段；根 `TranscriptItem` 若继续映射/alias SPI，必须同步覆盖其 public golden：

```go
ScopeID          string
ParentScopeID    string
ParentToolCallID string
```

`ScopeID == ""` 表示本 run 的根作用域。工具身份是 `(Event.Meta().RunID, ScopeID, ID)`，Transcript 中 ID 为 ToolUseID，SPI 为 ToolCallID。父引用为同 run 的 `(ParentScopeID, ParentToolCallID)`；父 ID 为空时 ParentScopeID 必须为空，不能指向自己。ParentScopeID 解决不同嵌套域具有相同 provider ID 时的歧义，不能仅靠 ParentToolCallID 拼树。ScopeID 是不透明关联坐标，不是新的消费者 Thread 身份。

Driver 使用正式 parent wrapper 建立父图，内部 map key 用 Go 可比较结构体，持久/wire 命名域采用第 8 节的 tuple 编码。Claude 只读取 wrapper 顶层 `parent_tool_use_id`，绝不从 `Explore`、`Agent` 等自然语言名称推断层级。已知父工具的 full key 唯一时产生子域；未知父保留原始协议和一次 `parent_unresolved` notice，不把它猜作根工具。父 wrapper 缺失不新增父关系。正式结果仅给裸 ID 时，只有当前 run 唯一活跃匹配才配对；跨 scope 歧义不任选一个。

一条工具生命周期的参数表示在 start 时固定：

- 完整快照：`ToolCall{Phase: PhaseStart, Args: complete}` → `PhaseEnd`；**不再补一条相同 ArgsDelta**。
- 增量：start 的 Args=nil → 每个真实 ArgsDelta 一次 → end；完整 assistant wrapper 重放只用于核对/Transcript，不重新 start、不重复参数。
- end 表示调用描述的闭合，不证明工具执行成功。`ToolResult` 在正式结果到达时产生，可以在 ToolCall end 后；结果不得由参数完成推断。
- 同 key 同内容重放是 no-op；同 key 不同 Name、父或完整参数是协议冲突，发安全 notice 并保留 Raw，不能重新分配 ID 伪装为另一次调用。
- 缺 ID 的 wrapper 保留 Raw/合法 Transcript，不合成可与未知真实结果配对的 tool lifecycle。已开的工具在 parser 明确终局、EOF/异常终止或取消收尾时只 end 一次；结束顺序按 start 接收顺序，全部先于唯一 RunFinished。未观察结果不伪造 ToolResult。

公共 `EventMeta.Sequence` 是唯一权威序号，由统一 sink 串行接受事件时分配。Driver 的 StreamPayload Sequence/Seq/Timestamp 继续必须为零；provider 时间放以下值的 OccurredAt 或 Source。不同 producer 的 accepted 顺序、observer 顺序和可见事件顺序一致；被 drop 的增量也占用一个序号。`WithEventMeta` 仅供恢复 wire/日志坐标，活跃 sink 总是重新盖本 run 的 meta。

`EventSourceMeta` 追加以下来源坐标，A2A DTO 同步（见第 7 节）：

```go
ScopeID      string
ToolCallID   string
InvocationID string
DelegationID string
Upstream     *EventSourceMeta
```

Source 代表立即上游的 envelope，不参与本 run 排序。嵌套 relay 将收到的 Meta 复制为本地 Source，收到的 Meta.Source 放 Source.Upstream；深度最多 8，超出产生 `relay_depth_exceeded` 降级，不能截掉中间层冒充完整来源。Meta.Source、Upstream、map、slice、Args、Result、todo Items、Duration 指针都递归深复制；observer、用户、EventBus、recorder、解码返回值各持有独立副本。Approval responder 不得因 clone 产生新答复权或新 exactly-once 状态。

## 3. Capability 公开叶包与 Event / SPI

`capability/types.go` 的完整新增值合同：

```go
package capability

type Kind string
const (
    Skill Kind = "skill"
    MCP Kind = "mcp"
    Subagent Kind = "subagent"
)
type Phase string
const (
    Started Phase = "started"
    Completed Phase = "completed"
    Failed Phase = "failed"
    Cancelled Phase = "cancelled"
    Interrupted Phase = "interrupted"
)
type Evidence string
const (
    ProviderProtocol Evidence = "provider_protocol"
    NativeInputAccepted Evidence = "native_input_accepted"
    HostLifecycle Evidence = "host_lifecycle"
    Relayed Evidence = "relayed"
)
type Source string
const (
    Provider Source = "provider"
    Host Source = "host"
    Relay Source = "relay"
)
type ErrorCode string
const (
    ToolFailed ErrorCode = "tool_failed"
    RunCancelled ErrorCode = "run_cancelled"
    RunInterrupted ErrorCode = "run_interrupted"
    ProtocolError ErrorCode = "protocol_error"
    DelegationFailed ErrorCode = "delegation_failed"
)
type Ref struct {
    Kind Kind
    Key string
    Operation string
}
type Invocation struct {
    InvocationID string
    Ref Ref
    Phase Phase
    Evidence Evidence
    Source Source
    ScopeID string
    ParentScopeID string
    ParentToolCallID string
    OccurredAt time.Time
    Duration *time.Duration
    ErrorCode ErrorCode
}
```

以上文件import `time`。Source/Evidence合法配对仅Provider+ProviderProtocol、Provider+NativeInputAccepted、Host+HostLifecycle、Relay+Relayed；不接受任意交叉组合。枚举是封闭值集；零 Kind/Phase/Evidence/Source 无效，空 ErrorCode 表示无已分类错误。Key 必须是实际 resolved catalog 的 canonical Key，逐字保留，不 trim/大小写归一化。Skill Operation 固定 `activate`，Subagent 为 `spawn`，MCP 为正式操作标识；Operation 不是工具参数。InvocationID 在本 run + ScopeID 内稳定，同一 started/terminal 不改 ID、Ref、父、Source 或 Evidence。

OccurredAt 必须非零 UTC：有正式 provider 时间用该时间，否则由 Driver 在识别时采集墙钟时间；它不决定接收顺序。不凭墙钟差猜单调耗时；Duration=nil 是未观察/不可可靠计算，非 nil 包括真实 0，非负。started 的 Duration 必须 nil，ErrorCode 必须空；Completed 的 ErrorCode 必须空。Cancelled/Interrupted 分别对应明确取消与无工具终局的异常收尾；成功 run 上尚未见工具结果也只能 Interrupted，不能因 run 成功就把所有调用标成 Completed。

```go
// events.go, package adaptor
 type CapabilityInvocation struct {
    eventMetaCarrier
    Invocation capability.Invocation
 }
// driver/events.go：在 StreamKind 常量组追加
 const StreamCapabilityInvocation StreamKind = "capability.invocation"
// driver.StreamPayload 字段追加
 Capability *capability.Invocation
```

仅该 Kind 可携带非 nil Capability，不能同时带 Todo/Args/Result/Raw/HITL 或 Role。sink 映射为 `CapabilityInvocation{Invocation: deepCopy(*p.Capability)}`。Root eventKind=`capability.invocation`；它是关键事件，不属于 eventMayDrop。没有第二条 RunEventCapability 路由；所有事实只调用既有 `EmitStream` 进入同一 sink，不能发 RunEvent + StreamPayload 两份副本。命名以 `CapabilityInvocation` 为 Event，叶包 `Invocation` 是其值；不新增消费者“Capability runner”。

未观察到只意味着没有相应事实，不能声称未调用、覆盖完整、计费正确或安全审计完整。能力已配置不等于使用；init 的 catalog 列表、prompt 提到 skill、工具名称含自然语言关键词都不能产生调用事实。正式 typed input 得到 provider 接受确认仅可产生 NativeInputAccepted 事实；它证明输入交付，不能证明加载了每一行 skill 内容。

## 4. Todo 公开值、快照及状态表

```go
// todo/types.go（import time）
package todo

type Status string
const (
    Pending Status = "pending"
    InProgress Status = "in_progress"
    Completed Status = "completed"
    Cancelled Status = "cancelled"
)
type Source string
const (
    ToolResult Source = "tool_result"
    PlanUpdate Source = "plan_update"
)
type Item struct {
    ID string
    Content string
    Status Status
    SyntheticID bool
}
type Snapshot struct {
    Items []Item
    Source Source
    ScopeID string
    ParentScopeID string
    ParentToolCallID string
    Revision uint64
    OccurredAt time.Time
}
// events.go, package adaptor
 type TodoUpdated struct {
    eventMetaCarrier
    Snapshot todo.Snapshot
 }
// driver/events.go
 const StreamTodoUpdated StreamKind = "todo.updated"
// driver.StreamPayload 字段追加
 Todo *todo.Snapshot
```

Items 是该 `(RunID,ScopeID)` 的**全量有序**快照，`[]` 合法且清空；nil 输入在规范化时变成非 nil 空 slice，wire 的 items 必须是数组。Revision 从该 scope 本 run 第一次已确认快照的 1 开始递增，不是 Event Sequence；新 run 不继承本地缓存。相同内容/source/父的重复确认不增 revision、不重发；首次空快照仍是已观察的清空，必须发 Revision=1。状态未知/格式错误不降成 Pending；整次操作原子拒绝，旧表不变、Raw 保留、一次对应原因 notice。

Snapshot Source 必须是正式成功工具结果确认或正式 plan notification。TaskCreate 收到成功结果中真实 ID 才 Create；未给正式 ID 的成功创建可用 `synthetic:` + tuple(runID, scope, toolCallID)，对应 Item.SyntheticID=true。绝不拿本地创建序号当 TaskUpdate.taskId。未知真实 task ID 的 update 返回 ErrUnknownID；不得匹配 synthetic 项。Resume 后没有完整 task list 就没有已知表，不能从前一轮运行缓存推断。正式结果中有全量表时优先 Replace；父/子作用域独立。

TodoWrite/TaskUpdate 的 input 完成时只缓存请求；对应 tool_result `is_error=true`、失败/取消/缺结果均不更新成功状态。正式成功结果确认的输入可以作为更新值；未有明确成功语义，保持未观察。Codex 只接 `turn/plan/updated` 的 plan 数组，实验性 plan 文本 delta 不识别为 Todo。Todo 不是 Approval PlanReview，也不暂停预算。

所有 TodoUpdated 都设为关键事件：正常执行不得 drop/coalesce，简化为无需猜测“最后一份”的完整恢复合同。broker须预留两个独立终局槽：一份最终Dropped摘要和唯一RunFinished。Cancel可解除可靠事件的阻塞发送，但每个已分配序号而未交付的事件都进入该摘要（Count/ByKind/FirstSequence/LastSequence完整），先Dropped后RunFinished，不等待消费者腾位；不能沿用基线abort直接清空dropAggregate的做法。正常队列不得占用这两个槽。取消时遵循既存 cancellation-safe abort：已被 sink 接受且未能送达的 todo 在取消 drop 摘要中按 `todo.updated` 明确计数，不能撤回已经完成的 observer Append；终局保留。任务不得宣称取消后不 drain 消费者仍能获得每份快照。UI 在收到取消/dropped 后将状态标为不完整，不能当作已同步终态。

## 5. 中立 catalog / tracker / table API

这些是 internal 实施合同，不能出现在 public 字段或 alias 中；只 import 标准库、capability/todo 叶包。无 sink、Driver Request、root import 或 provider 工具名称/JSON 解析。

```go
// internal/capabilityobs/catalog.go
 type Entry struct { Kind capability.Kind; RuntimeName string; Key string }
 type Catalog struct { /* private immutable maps */ }
 var ErrInvalid = errors.New("capabilityobs: invalid value")
 var ErrAmbiguous = errors.New("capabilityobs: ambiguous name")
 var ErrUnknown = errors.New("capabilityobs: unknown name")
 func NewCatalog(entries []Entry) (*Catalog, error)
 func (c *Catalog) Lookup(kind capability.Kind, runtimeName string) (string, error)

// internal/capabilityobs/tracker.go
 type Key struct { ScopeID string; InvocationID string }
 type Tracker struct { /* private ordered state */ }
 var ErrConflict = errors.New("capabilityobs: conflicting lifecycle")
 var ErrClosed = errors.New("capabilityobs: closed")
 func NewTracker() *Tracker
 func (t *Tracker) Start(value capability.Invocation) (*capability.Invocation, error)
 func (t *Tracker) Terminal(key Key, phase capability.Phase,
     code capability.ErrorCode, at time.Time, duration *time.Duration) (*capability.Invocation, error)
 func (t *Tracker) Close(phase capability.Phase, code capability.ErrorCode,
     at time.Time) ([]capability.Invocation, error)

// internal/todoobs/table.go
 type Scope struct { ID string; ParentScopeID string; ParentToolCallID string }
 type Patch struct { Content *string; Status *todo.Status }
 type Table struct { /* private ordered state */ }
 var ErrInvalid = errors.New("todoobs: invalid value")
 var ErrUnknownID = errors.New("todoobs: unknown id")
 var ErrConflict = errors.New("todoobs: conflicting id")
 func NewTable(scope Scope) (*Table, error)
 func (t *Table) Create(item todo.Item, source todo.Source, at time.Time) (*todo.Snapshot, error)
 func (t *Table) Update(id string, patch Patch, source todo.Source, at time.Time) (*todo.Snapshot, error)
 func (t *Table) Replace(items []todo.Item, source todo.Source, at time.Time) (*todo.Snapshot, error)
 func (t *Table) Snapshot() (todo.Snapshot, bool)
```

以上签名里的 private state 注释是结构布局自由度，不是可交付空实现。所有函数必须有完整实现和合同测试；本文不规定私有 struct 字段。

Catalog 对完全相同重复 Entry 去重；同 `(Kind,RuntimeName)` 对应不同 Key 时保留歧义 tombstone，构造仍成功，Lookup 返回 ErrAmbiguous；任何加入顺序都不能让其中一个赢。非法 Kind/空值/超限拒绝构造。Lookup 逐字比较，不 trim，不生成 alias。各 Driver 从最终 Skills、MCP（含服务/工具发布）、ProfilePayload.Agents 构造 Entries；Claude 特有的 Unicode 与 `_` 变换留在 `claude/capability_observation.go`。MCP 工具名拆分必须枚举已知 server alias 和所有符合正式分隔规则的候选 `(Key,Operation)`；恰好一个候选才接受。下划线属于合法操作名时不无条件 TrimLeft。多个 server/operation 候选即拒绝；无精确证据不能“最长前缀猜中”。CodeBuddy 要用自己的 fixture 验证规则，不能隐式共享 Claude 解释器。

Tracker 不直接发事件，避免持有状态锁调用外部代码。返回 non-nil 表示一份应发布事实，nil,nil 是相同重放。Start 参数必须 Phase=Started；再次不同身份 Start 返回 ErrConflict；terminal 无 start 返回 ErrUnknown；相同终局重放 nil,nil、冲突终局 ErrConflict；状态转换在 mutex 内原子完成。Close 只接受 Cancelled/Interrupted/Failed，依原 start 接收顺序返回未闭合项，随后拒绝 Start；重复 Close 返回空,nil。失败 tools 的最终分类由 Driver 决定，tracker 不读 JSON。

各 parser 持有自己的串行 dispatch 顺序锁，涵盖 tracker/table 调用和把返回值按返回顺序 EmitStream；不能 A goroutine Start 返回后延迟发布、B goroutine 先发 terminal。helper 内 mutex 只保障状态原子性，不承诺跨调用者的外部发布排序。

Table 原子验证整次操作：无效 UTF-8、未知 status、重复不同值 ID、空 Content、超限都不部分更新。Create 相同项重放 nil,nil，不同项同 ID ErrConflict。Update 至少一个非 nil字段；不存在 ID ErrUnknownID；只真实 ID 可用于 provider ID 更新，禁止 synthetic 匹配。Replace 保留传入顺序；空数组可清空；同值重复 nil,nil；Snapshot bool=false 是从未观察，true+空 Items 是已清空。返回值均深复制；SyntheticID 由 parser 根据正式信息明确设置，table 不生成 ID、不识别工具。

## 6. 观察入口、需求与宿主生命周期

在 `runservices.go` 固定追加以下 root 声明（均非 internal alias）：

```go
 type ObservationDemand struct {
    CapabilityInvocations bool
    Todos bool
 }
 type RunEventInfo struct {
    RunID string
    ThreadKey string
    Identity Identity
    DriverType string
 }
 type RunEventObserver func(context.Context, RunEventInfo, Event) error
 type RunEventPublisher func(context.Context, Event) error
 // RunAttachment 字段追加：
 Observation ObservationDemand
 Observer RunEventObserver
 BindEvents func(RunEventPublisher) error
```

Observer 和 publisher 是运行扩展点上的 hook，不是新执行入口/事件 channel。existing Events 可继续提供输入，但**一个事实只能经 Events 或绑定 publisher 之一交付**。BindEvents 不接受 publisher 返回的 Event 再喂回 Events。nil Observer 是不观察；nil BindEvents 是无需直送。publisher 只在成功 AttachRun 后交给该 attachment，捕获唯一 run 的 sink/授权/撤销状态，不从 context.Value 查找，不导出取 sink 的全局函数。BindEvents 在所有 AttachRun 成功、observers 安装后按 attachment 注册顺序调用，再启动 Events pump/Driver；绑定失败是启动前配置错误，正常逆序 DetachRun。

RunEventInfo 是 resolved immutable envelope 的深复制，允许 recorder 从实际 Identity 分区，无需解析 Notice 的 string metadata。Observer 仅收到已被 sink 接受的 CapabilityInvocation / TodoUpdated 两种 Event 的深复制；这是闭集，扩大集合需要公共合同变更。它不接收 ApprovalRequest、RunStarted/RunFinished、ProcessInfo、普通工具事件或 Notice，不取得答复能力，不接触 Raw。它不能改变 Event/Result/checkpoint。Capability recorder 只提取第 9 节闭集，不持久化 TodoUpdated 或 RunEventInfo.ThreadKey。

需求、观察与直送互不暗示：只有Observer不默认要求某transport；组件必须声明Observation。需求不是事件过滤开关：Driver无论有无Store/Observer，都应发布实际所选协议已经正式识别的capability/todo事实；zero demand只不要求切换成更丰富的transport。只有需求没有 observer/store 合法，A2A relay 即用此方式。publisher 必须拒绝宿主伪造 RunStarted/RunFinished、ApprovalRequest 或 Evidence=ProviderProtocol/NativeInputAccepted 的 Capability；允许 Host+HostLifecycle 外层调用、Relay+Relayed 经正式 wire 解码的事实，以及现有宿主 SubagentUpdate/Notice/Dropped。授权附件有责任验证 remote bytes，core 不再解析 provider。禁止匿名全局注入。撤销后调用立即返回 `context.Canceled`；其他无效输入返回 `fmt.Errorf("adaptor: invalid run event")`（稳定错误文本，不新增 sentinel）。publication 的 ctx 只限制该次等待，不覆盖绑定 run 的 cancel。

### 6.1 回调顺序与失败隔离

每个 accepted event 的确定顺序如下（第 3 步仅对两种观察事实执行）：

1. 应用既有生命周期过滤/typed 值校验；拒绝的事件不分配 Sequence、不触发 observer。
2. 串行化接收，深复制并分配一次 RunID/ThreadKey/Sequence/Time；先前 drop aggregate 若需交付，先分配它的序号。
3. 按 attachment 顺序调用仍启用的 Observer，使用该事件私有副本；同一 run 任意两个观察调用不并发，不持有 Approval 等待锁。
4. 观察完成后向唯一 user broker 入队或按合同 drop；对于 delegation，直送 publisher 完成这一接受步骤后，才调用 EventBus.Publish。EventBus 不再成为 core 输入源。
5. 下一 accepted event 继续。Observer 创建的安全 notice 排在触发事件之后、下一个外部事件之前，不递归回调刚失败 observer。

每次 observer 调用有固定 100ms 墙钟上限；不增公开 timeout knob。正常运行使用同时受 run cancellation 控制的子 ctx。取消收尾只允许仍启用 observer 用剩余总 cleanup budget 的不超过 100ms 窗口观察已接受的 capability 终态或 todo 快照。每 attachment/run 最多一个在途回调；必须 recover panic，绝不把 panic正文/error正文输出。首次 error/panic/timeout 使该 observer 在本 run 永久失效；继续其他 observer，发一次 `NoticeRuntime`，Data 只含 `code="observation_disabled"`、`reason="error"|"panic"|"timeout"`、`observer_index`。后续 callback 不再启动；该 hook 错误不加入 Result error，不改 checkpoint/审批结果。

100ms 到点 SDK 取消 ctx 并放弃等待；无法强杀任意宿主 Go 回调，该回调迟到返回的值被 generation fence 丢弃，不能触发第二 notice 或重新启用写入。SDK 不在超时之后为同 observer 启动更多 goroutine。Store 实现必须在 commit 前检查 context；违反约定而在 cancel 后仍提交的自定义 Store 无法由 SDK 回滚，合同不能承诺任意错误宿主的副作用可撤销。nil/panic/timeout observer 不是配置错误，不阻止运行。

observer 是非重入回调：不得调用当前 run 的 publisher、Stream.Result/Events drain、Agent.Close 或同步等待当前 run；Query/不依赖当前 run 的其他操作允许。publisher 能通过 callback ctx 识别同回调重入并立即返回 invalid run event；换成 Background 绕过 ctx 的错误宿主最终受 100ms fence 隔离，不能形成永久循环等待。Cancel/Close 的信号必须无须取得 publication mutex 即生效，以解开 blocking user send 与 observer 等待。

收尾顺序是 Driver parser 闭合其工具/capability → 停止并有界 drain 宿主输入 → 撤销 publisher（已接受者完成观察或有界失败，未送用户者计 drop）→ 停用 observer、flush 其安全 notice → 逆序 DetachRun/资源释放 → 形成最终 Result/error → 接受唯一 RunFinished → 关闭 Events。RunFinished 不调用 observer，因此不会产生终局之后才发现的观察notice；任何事件不重新盖第二次序号。DetachRun 不关闭共享 Store。

C02 Ask token 在观察/用户入队或 OnApproval 调用之前建立，最后 response/abort 释放；observer 本身不创建、释放或扫描预算 token。非 Ask observer 处理属于主动执行；Ask 段恰好随既存 token 暂停。发布排序 mutex 不跨 Ask 等待，去除既存 decisionSerial 对重叠 Ask 的阻挡由 C02/T10 落实。

### 6.2 Driver demand 与 resolved transport

```go
// driver/driver.go
 type ObservationSupport struct {
    Skills bool
    MCP bool
    Subagents bool
    Todos bool
 }
 type ObservationCapabilities struct {
    Batch ObservationSupport
    Streaming ObservationSupport
 }
 // Descriptor 字段追加：
 Observation ObservationCapabilities
// driver/run.go：与 root 真实结构体逐字段转换，非 root alias
 type ObservationDemand struct {
    CapabilityInvocations bool
    Todos bool
 }
 // Request 字段追加：
 Observation ObservationDemand
```

需求按全部 attachments OR 合并，不改变既有 Option 近处覆盖/skills追加/Policy整值替换。最终 schema/policy/native prompt/observation 共同解析一次 transport；仍只有 `Request.Streaming`。先在符合硬 policy/native prompt且至少支持一种schema机制的transport中保留观测：CapabilityInvocations计Skills/MCP/Subagents三个位，Todos一位，选择满足需求位数最多者，平分则保留既存选择。随后在选定transport按C04原生schema→prompt加本地校验协商；不能仅为batch原生schema放弃可交付的rich事实。如果构造配置明确锁定transport，则不覆盖它。各项在所有可行transport都不支持时，仍运行并在Driver启动前发一次 `NoticeRuntime`，Data=`{"code":"observation_unavailable","capabilities":["skill","mcp","subagent","todo"]}`（只列未支持且被请求的项）。不假报空快照或零次调用。第三方零值 Descriptor 明确未声明，不推测支持。

Observer/demand/publisher 的对象地址不参与会话 fingerprint；被选 transport、真实 catalog、resource/env/session 兼容维度参与既存 canonical fingerprint 和常驻启动签名。不能为观测绕过单 writer 或交付后自动重放；能力探针与执行观察同一构造配置。T06在wiring/runservices交付并接入真实negotiation，B02 fake Driver端到端测试立即可执行；T10只扩展同一个resolver的native prompt/budget输入，不能另建pipeline或在Driver再协商。

## 7. A2A `adapter.stream.v1` 的精确增量

版本名/URI 不变，不改变既存 text/tool/HITL DTO 的解码合同。新增公开 Go DTO 位于 `bridges/a2a/stream_status.go`；所有 JSON tag 如下，外层 Envelope 的 64 KiB 指序列化后的**整个 envelope UTF-8 字节数**（包含 schema/meta/父/来源）。

```go
// AdapterStreamEventV1 字段追加
ScopeID          string `json:"scope_id,omitempty"`
ParentScopeID    string `json:"parent_scope_id,omitempty"`
ParentToolCallID string `json:"parent_tool_call_id,omitempty"`
Capability *AdapterCapabilityInvocationV1 `json:"capability,omitempty"`
Todo       *AdapterTodoSnapshotV1 `json:"todo,omitempty"`

 type AdapterCapabilityInvocationV1 struct {
    InvocationID string `json:"invocation_id"`
    Kind string `json:"kind"`
    Key string `json:"key"`
    Operation string `json:"operation"`
    Phase string `json:"phase"`
    Evidence string `json:"evidence"`
    Source string `json:"source"`
    ScopeID string `json:"scope_id,omitempty"`
    ParentScopeID string `json:"parent_scope_id,omitempty"`
    ParentToolCallID string `json:"parent_tool_call_id,omitempty"`
    OccurredAt string `json:"occurred_at"`
    DurationNS *int64 `json:"duration_ns,omitempty"`
    ErrorCode string `json:"error_code,omitempty"`
 }
 type AdapterTodoItemV1 struct {
    ID string `json:"id"`
    Content string `json:"content"`
    Status string `json:"status"`
    SyntheticID bool `json:"synthetic_id"`
 }
 type AdapterTodoSnapshotV1 struct {
    Items []AdapterTodoItemV1 `json:"items"`
    Source string `json:"source"`
    ScopeID string `json:"scope_id,omitempty"`
    ParentScopeID string `json:"parent_scope_id,omitempty"`
    ParentToolCallID string `json:"parent_tool_call_id,omitempty"`
    Revision uint64 `json:"revision"`
    OccurredAt string `json:"occurred_at"`
 }
// AdapterEventSourceMetaV1 字段追加
ScopeID      string `json:"scope_id,omitempty"`
ToolCallID   string `json:"tool_call_id,omitempty"`
InvocationID string `json:"invocation_id,omitempty"`
DelegationID string `json:"delegation_id,omitempty"`
Upstream *AdapterEventSourceMetaV1 `json:"upstream,omitempty"`
// ExposurePolicy 字段追加
IncludeCapabilityInvocations bool
IncludeTodos bool
```

新 kind=`capability.invocation`（必须有 capability，不能有 todo 或其他语义字段）与 `todo.updated`（必须有 todo，不能有 capability 或其他语义字段）。tool_call.* 使用外层 scope/parent；capability/todo 的 scope/parent 只在其内层值中，外层必须空，防止双权威。Meta 是权威；新 kind 要求非 nil meta、非空 run_id、Sequence>0、非零有效 time，flat RunID/Sequence/Timestamp/TurnID 若出现必须与 meta 一致，ThreadID 若出现必须与非空 Meta.ThreadKey 一致。Source 永远不是替代 meta 的序号。

严格校验在 encode **及** decode 双侧执行：匹配新kind后使用封闭字段decoder，拒绝未知字段、wrong-type、null代替required值；data为json.RawMessage时直接读取原bytes并拒绝重复JSON key、额外顶层JSON值和非法UTF-8/不成对surrogate。data为已解码map时先递归检查所有string/key的UTF-8及值类型，再Marshal，不能让Go json静默修复非法字节。对既存kind维持已发布兼容，新增安全字段若存在也须校验。以map输入时已丢失重复key/原编码证据，DecodeAdapterEventV1不能追溯恢复，合同只证明其接收到的结构化值合法；这里不新增clients/a2a越界改造或宣称原bytes审计完整。bridge自身encode/decode保持既存客户端的安全整数范围，不能把stringify后的文本冒充原始bytes。解码入口 `DecodeAdapterEventV1(data any)` 对非本 schema 仍 matched=false,nil；对本 schema 的未知 kind/非法 payload 返回 matched=true,error，不返回半个有效 Event。

| 值 | 精确限额/校验 |
|---|---|
| Kind/Phase/Evidence/Source/Status/ErrorCode | 仅第 3、4 节的封闭枚举，无自由 error 文本 |
| canonical Key | 1–512 UTF-8 bytes，无 Unicode control；原文不 trim/截断 |
| Operation | 1–256 UTF-8 bytes，无 Unicode control；Skill/Subagent 固定值 |
| InvocationID、tool ID、ScopeID、ParentScopeID、DelegationID | 非空时 ≤2048 UTF-8 bytes，无 Unicode control；required ID 不得空；超限不能 hash 掩盖，按降级处理 |
| Todo Content | 1–4096 UTF-8 bytes；保留 LF/tab，拒绝其余控制字符；不切字节或静默改内容 |
| Todo Items | 0–128；ID 唯一；Status合法；空数组清空，null或缺 items 非法 |
| Meta/flat Sequence、Todo Revision | 1..9007199254740991；Source.Sequence可0；root内部仍uint64，wire超限明确降级，不转float64丢精度 |
| OccurredAt、Meta.Time、Source.Timestamp | RFC3339Nano，可解析，年份 1..9999；零 time 无效（optional Source.Timestamp 可省）；encode UTC；decode 保持等价时刻 |
| DurationNS | 可省/0..9007199254740991整数（含真实0），禁止小数、溢出；root内部仍time.Duration，started禁止出现 |
| Source.Upstream | 总节点数 ≤8，无环（encode 检查）；所有节点校验 |
| 整个 Envelope | ≤65536 bytes；65537 必须拒绝；encode先过滤再计大小 |

总量超限或无法映射不截断 Todo 列表/内容，避免伪造完整快照。发送一个仍通过既存 schema 的 `stream.dropped` 降级，Raw 闭集为 `dropped_count:1, reason:"invalid_payload"|"payload_too_large"|"unsupported_event_kind"|"relay_depth_exceeded", source:"a2a", event_kind:<已验证kind或"unknown">`，不回显非法字段/secret。出站以该事件既有Meta替换成一份Dropped投影，不额外编号；入站非法bytes由T18绑定publisher送入本地唯一sink后分配本地Meta。它只解释wire信息损失，不改变remote task或本地Result的业务成败；若整个网络/编码 transport 失败，按既存基础设施 error 路径返回。

暴露固定为：

- 两个新 Include* 默认 false；开启 IncludeToolCalls、Diagnostics 或 transcript 不会隐式开启它们。
- Capability opt-in 仅传闭集值；不会带 args/result/Raw/URL/header/env/prompt/error 正文。Source.* 元坐标及 Upstream 仍额外受 `Diagnostics.IncludeMetadata` 控制；不通过 Capability.Source 绕过该开关。
- Todo opt-in 允许经验证的 Items 内容，属于显式正文暴露；仍经过既存 ExposurePolicy 和敏感字段过滤，不允许任意 metadata/Raw。工具/Transcript 里的原内容继续遵循自身既有开关，不能为了 Todo 把它们打开。
- opt-out 是明确政策过滤，静默省略相应事实而不发送包含 kind/key/count 的 notice，以免默认暴露“有哪些内部能力”；文档告知接收方无事件不代表未调用。opt-in 后非法/超限是信息损失，必须发上述安全 dropped。
- 新 client 读取旧 wire 无需新增字段。未升级 client 已有 unknown kind 的 matched=true,error 行为；sender 不可能让旧二进制支持新 kind。T11 保持此明确 unsupported，T18 将 decoder 错误投影为 DelegationStreamDropped，不能吞掉或宣称保真；新 sender 的默认关闭避免未经 opt-in 给旧端发送新数据。

最小 wire fixtures（对应 JSON 文件置于 C03 handoff，供 T11/T18 复制测试）：

```json
{"schema":"adapter.stream.v1","event":{"kind":"capability.invocation","meta":{"run_id":"remote-r1","sequence":7,"time":"2026-09-07T00:00:00Z"},"capability":{"invocation_id":"call-1","kind":"mcp","key":"knowledge","operation":"search","phase":"started","evidence":"provider_protocol","source":"provider","occurred_at":"2026-09-07T00:00:00Z"}}}
```

```json
{"schema":"adapter.stream.v1","event":{"kind":"todo.updated","meta":{"run_id":"remote-r1","sequence":9,"time":"2026-09-07T00:00:01Z"},"todo":{"items":[],"source":"plan_update","revision":2,"occurred_at":"2026-09-07T00:00:01Z"}}}
```

A2A 无 Store demand 用桥内私有 RunServiceProvider，在现有 `Runner.Stream` 的 CallOption 中追加 `adaptor.WithRunServices(provider)`；AttachRun 只返回 Observation={CapabilityInvocations: exposure.IncludeCapabilityInvocations, Todos: exposure.IncludeTodos}，没有 Observer、Services 或 Events。该方式对任意公开 Runner 可用，DetachRun 为真实无资源 no-op，不能直接调用 Driver 或暴露第二流模式开关。未启用 exposure 的请求不增加需求。

## 8. Delegation / relay / 其余 bridge

### 8.1 注入顺序与域隔离

T18 的 Service.AttachRun 使用 BindEvents 保存该 run 的 publisher，Events=nil；所有需要进入 core 的 SubagentUpdate、CapabilityInvocation、TodoUpdated 都在 `Delegator.publish` 串行边界安全 clone→绑定 publisher→EventBus.Publish。既有 `Config.Observe` 是 bus 的可丢弃 UI 观察器，不能用作 capability recorder 的安全入口，也不能越过它操作 Store。没有本地 recorder 时 publisher 仍将事件送同一个 public Stream；没有远端 Store 时第 7 节需求仍得到 provider 事实。EventBus replay 不能重喂 core。

实际调用順序固定：

1. 分配 DelegationID；解析 Registry 得到明确 Local/Remote target。
2. 建立本次预算/墙钟 bound；在有界 ctx 中运行 BeforeDelegate，检查其返回后 ctx 仍可执行。拒绝/超时只产生既有 delegation failure，不产生 capability Started/terminal。
3. 发布一次外层 capability Started（Ref=Subagent/canonical AgentSpec.Key/spawn；Source=Host，Evidence=HostLifecycle），安装唯一 terminal defer。
4. Local.Runner.Stream 或远端 AgentCard/请求/续答/recovery I/O。外层 Started 早于所有远端 I/O。
5. 收到并校验子事实，映射父/命名域，绑定 publisher 接受（observer 同步投影）后才发布 bus event。
6. 主调用结束→AfterDelegate（既有错误保留政策）→记录最终 delegation Result→capability terminal 一次→flush delegation terminal→返回。after hook 影响最终外层状态，不能先发 Completed 再改 Failed。

外层 invocation ID 是 `tuple("delegation", DelegationID)`；对每条子事实的 InvocationID、ScopeID、ToolCallID 使用不同 kind tag 编码。tuple 定义唯一：UTF-8 JSON string array，Go `json.Encoder` SetEscapeHTML(false) 的紧凑编码，去掉末尾 LF，再用 `base64.RawURLEncoding`，前缀 `aa1:`。元素逐字保留，不 trim、不用分隔符拼接、不截短/hash。相同 tuple 恒同 ID，不同 tuple 的编码不同。

- 子 invocation：tuple("invocation", DelegationID, remote.Meta.RunID, remote.ScopeID, remote.InvocationID)。
- 子 scope：tuple("scope", DelegationID, remote.Meta.RunID, remote.ScopeID)。即使远端根 scope 为空，本地 relay scope 也非空。
- 子 tool：tuple("tool", DelegationID, remote.Meta.RunID, remote.ScopeID, remote.ToolCallID)。
- 子父：远端有 parent 时按对应 remote.ParentScopeID/ParentToolCallID 映射；远端根子项无 parent 时采用本地 DelegationRequest.ParentToolCallID 及调用方实际 ParentScopeID，未知时保持空，不伪造外层工具 ID。

T18 对 `DelegationRequest`、`DelegationEvent` 追加 ScopeID/ParentScopeID（ParentToolCallID 已有）；`DelegationEvent` 追加 `Capability *capability.Invocation`、`Todo *todo.Snapshot`、`Source *adaptor.EventSourceMeta`。新增 `DelegationCapabilityInvocation = "capability.invocation"`、`DelegationTodoUpdated = "todo.updated"` 常量属于其既有 DelegationEventKind。它们不是新的 bus/channel；原 Event.Sequence 仅为上游来源，送入 core 后 `EventMeta.Sequence` 重新统一分配。outer scope 为实际调用方 scope，inner scope/parent 采用上述 tuple。所有 field clone、backpressure 清理、Replay/terminal 缓冲均同步。

任意两次 delegation 即使同 remote RunID/InvocationID 也不冲突；双层 relay 每层新 DomainID，Source.Upstream 保留上一层坐标。收到重复 remote `(RunID,Sequence)` 且 payload 相同不重发，冲突重复是 invalid_payload；不能跨不同 DelegationID 去重。跨 retry/recovery 不重置本次去重和预算；新 continuation 分配新的 DelegationID/预算，与 C02 合同一致。

对 Local Runner 使用同一 typed Event 判断/clone/映射，不能从 local tool args 重识别 capability/todo。Local 原始 provider Evidence 进入父 run 时同样变为 Source=Relay、Evidence=Relayed，Source chain 保留其根来源坐标；“local”不意味着能绕过父域和 publisher 授权。

### 8.2 各桥/recorder 固定映射

| 面 | 新 Event / 父字段投影 | 不支持/丢失规则 |
|---|---|---|
| Raw SSE | event name=`capability.invocation` / `todo.updated`；保持既有顶层map+meta：body=`{"meta":meta,"capability":value}`或`{"meta":meta,"todo":value}`，value字段按第7节snake_case；tool body追加scope_id/parent_scope_id/parent_tool_call_id | 保留 EventMeta；SSE 不读取 Raw 猜值；网络失败沿既存取消/error |
| AG-UI | 两种新 Event 映为 `CUSTOM`，name=`adapter.capability.invocation` / `adapter.todo.updated`，value为第7节Event的wire等价私有结构（无schema外壳，保留meta；T12从B02 typed值构造，不import或等待同批T11 Go DTO）；tool start 前发 `CUSTOM adapter.tool.parent`，value含meta+tool_call_id+scope_id+parent_scope_id+parent_tool_call_id | tool卡ID用 tuple("tool",runID,scopeID,ID)避免scope撞名；AG-UI无父字段用 CUSTOM 明示；todo空数组必须发；仍是同一AG-UI流 |
| sessionrecorder | 既存私有eventRecordWire.Kind支持capability.invocation/todo.updated，Event字段保留typed值，Meta单独保存；不新增EventRecord.Type/Payload公开字段；Decode 恢复两个 sealed Event及新 Source/父字段 | 严格拒绝未知/畸形持久事件，沿既存 ErrJSONLEventLogCorrupt；不偷偷降级内存；记全 Event 与 capabilityrecorder 最小事实表是两个不同宿主选择 |
| subagentstream 标准接入 | Service.Option 绑定 publisher，在执行期将新 typed Event 送入原 Stream；`ToSubagentUpdate` 的现有镜像仅用于 bus/UI展示，不能再给同事实增加 core副本 | 不通过桥重解释 provider，不通过另一个 bus 作为事实源 |

`subagentstream.Merge` 当前自行重编号父 Event 并混入 bus 的做法与单一权威 Sequence 冲突，作为本合同打开的既存矛盾处理。冻结兼容裁决：保留已发布函数签名，nil bus 仍原样返回；非 nil bus 只透明转发父 Stream，不再注入或重编号。T12 在 bridge 内声明私有可选接口 `interface { RunEventsBound(runID string) bool }`；T18 在 EventBus 提供公开方法 `func (b *EventBus) RunEventsBound(runID string) bool`，T12测试使用实现该方法的fake，不依赖B04符号。

RunEventsBound 是“本 bus 的该 run 曾通过 Service.BindEvents 成功绑定唯一 publisher”的单调证明。只由 Service 成功绑定后置真，普通 EventBus.Publish 不能置真。ClearRun/DetachRun 不撤回历史证明，保证缓冲父事件稍晚读取时仍可验证；该记录与已有 Service 结果同寿命，在 bus 对象释放时一起回收。未知 run 返回false；方法并发安全，不阻塞、不执行运行。绑定所服务的runID逐字比较。

Merge 在后台透明发送父事件，保持Meta所有字段和terminal-last；父Events关闭后取得完整父Result，最后查询该证明。true 时原样返回父Result/error；false或不实现接口时返回nil与携带完整/partial Result的既存RunError：父成功时Reason=ReasonInfrastructure、Cause=ErrEventInjectionUnsupported、Result=父结果；父已为RunError时复制该错误并保留Reason/Message/Details/Result，Cause=errors.Join(原Cause,ErrEventInjectionUnsupported)，不修改父错误；其他父错误作为Cause与sentinel合并，不覆盖已确定原因。errors.Is必须仍匹配父cause及ErrEventInjectionUnsupported，不另增非nil Result+error返回约定。新增 sentinel 位于 `bridges/subagentstream/merge.go`，文本 `subagentstream: event injection requires a run service attachment`。只在父流完成后检查证明，避免立即Merge早于异步Attach时误拒。Merge从不订阅或等待bus，因此无双流等待闭环。Result并发/多次调用共享已缓存结果；用户仍须按Stream合同drain Events。

Cancel直接调用parent.Cancel并解除自己的阻塞send；停止对外发送后仍drain已取消parent至关闭并通过既存RunError提取可用Result，不丢弃Raw/Transcript。普通终局事件只来自父流；取消时不制造额外RunFinished。桥配置错误不冒充provider业务失败，不创建HITL策略。用户迁移为在New/Run前装 `team.Option()`（Service.Option），随后直接消费原Stream；这是明确breaking行为修复，G00/T12须在批次文档记录，不能静默声称事后Merge仍保真。

## 9. 可选 capabilityrecorder 的完整公共入口

T13 新包 `hosttools/capabilityrecorder` 只消费 root 公共扩展点和 capability 值；文件 `types.go`, `recorder.go`, `memory.go`, `doc.go`。不提供 Agent.Admin/Inspect 历史查询，不创建数据库，不在 Agent.Close 关闭 Store。

```go
 type Scope struct { IdentityID string; Tenant string; Profile string; RunID string }
 type Record struct {
    Scope Scope
    Sequence uint64
    Time time.Time
    Invocation capability.Invocation
 }
 type Query struct {
    Scope Scope
    AfterSequence uint64
    Limit int
 }
 type Page struct { Records []Record; NextSequence uint64 }
 type Store interface {
    Append(context.Context, Record) error
    Query(context.Context, Query) (Page, error)
 }
 type Config struct { Store Store }
 type Recorder struct { /* private provider implementation */ }
 func New(cfg Config) (*Recorder, error)
 func (r *Recorder) Option() adaptor.SharedOption
 func (r *Recorder) Query(ctx context.Context, q Query) (Page, error)
 func NewMemoryStore() Store
 var ErrInvalidQuery = errors.New("capabilityrecorder: invalid query")
 var ErrStoreRequired = errors.New("capabilityrecorder: store required")
```

package imports `context`, `time`, root、capability。IdentityID/Tenant/Profile分别精确取RunEventInfo.Identity的ID/Tenant/Profile，允许空表示该维度未配置，绝不填显示名/路径。完整三维identity都参与分区，避免相同ID跨tenant/profile串库。RunID必须非空；Query只支持精确scope，不支持无条件跨identity全库扫描。Limit=0 为100，1..1000有效，其他 ErrInvalidQuery。AfterSequence 独占下界；Store按 Sequence升序返回最多Limit条，NextSequence在还有后续项时为该页最后Sequence，否则0。Scope四字段使用结构体map key/无碰撞编码；不trim，包括空identity维度。

Append 是原子幂等写入，主键 `(IdentityID,Tenant,Profile,RunID,Sequence)`：完全相同值 no-op，冲突值错误；严格深复制输入，读返回独立副本。成功 Append 返回后 Query 立即可见；查询中的 run 允许正在进行。Store.Query 不得因未见 terminal 把页说成完整历史。Store 未实现持久化时 NewMemoryStore 明确仅内存；New 的 nil Store 返回 ErrStoreRequired，绝不默认回退。

Recorder.Option 用真实 RunServiceProvider：AttachRun 返回 Observation.CapabilityInvocations=true 与 Observer，仅 CapabilityInvocation 经白名单 validation 投影为 Record，其他 Event不入库。Descriptor或来源的未声明能力、非法值或不可信 Evidence 不伪造修正。Info.Identity的ID/Tenant/Profile来自core，record不存ThreadKey、identity.Name、任意 map、args/result/Raw/URL/header/env/prompt/error正文。首次 Append 失败/超时/panic 由第6节关闭本 run 观察；其他 run/identity继续写。DetachRun 清当前 run 辅助状态但不给共享 Store.Close；Recorder 无 Close方法；宿主自有 Store 若另实现 io.Closer，由宿主负责。

## 10. 实际协议交付矩阵与 fail-closed 边界

下面“可接入”指固定源对象提供了正式字段/fixture阅读依据，T14–T17 必须在目标代码 fixture和后续 live 实证后才置 Descriptor 对应位；**C03 没有执行 Driver，也没有宣称支持已落地**。Batch指所选 provider非增量终局协议，Streaming指现有原生协议，不是消费者 Run/Stream方法。

| Driver / transport | Capability 的准许证据 | Todo | 父工具 |
|---|---|---|---|
| Claude batch JSON | 全部false；单个终局 result 不证明工具/skill调用，init catalog亦不够 | false；没有本项 batch Todo transcript parser | 未观察；不从终局猜父 |
| Claude stream-json，包括control/常驻 | Skill工具的正式skill字段；MCP正式tool_use+catalog唯一alias；Agent/Task显式subagent_type且catalog唯一；terminal以正式tool_result | true：TodoWrite/Task*正式成功结果确认后；TaskCreate真实ID优先 | true：顶层wrapper parent_tool_use_id及真实ID |
| CodeBuddy batch JSON | 全部false；不因与Claude类似就宣称覆盖 | false | 未观察 |
| CodeBuddy stream-json/control/常驻 | 自己的Skill.command/skill字段、MCP字段、Task.subagent_type与tool_result；只接受其fixture支持的版本分支 | true：官方同类工具与结果配对；partial/wrapper不重复 | 仅协议明确父字段才写；本源未证明wrapper父覆盖则保留空并声明未观察 |
| Codex exec JSONL | 全部false于本轮目标；exec仅已有输出合同，不借观测从stdout文字猜调用 | false | 未观察 |
| Codex app-server one-shot/常驻 | MCP item/started与item/completed、server/tool字段；Skill仅typed UserInputSkill 的 turn/start成功确认；Subagent仅正式collab/thread字段能与resolved Agent catalog唯一匹配 | true：仅正式turn/plan/updated，按当前turn fence拒绝旧/异域通知 | 有正式 item parent时才填写；不能猜collab工具名就是父 |
| Cursor print stream-json | MCP正式mcpToolCall.args.serverIdentifier（正式备用providerIdentifier须fixture明确）、toolName/name；Subagent taskToolCall.args.subagentType.custom.name；成功/失败按正式result oneof | **false**；保持 print，不切ACP | 当前无正式父证据则空 |
| Cursor print非流终局 | 全部false | false | 未观察 |

Cursor 的 user消息只回显 `/review` 不能证明skill接受，因此本合同把 Cursor Skills=false；Claude init列出skills同样不够，不能采用源fixture `TestClaudeExplicitSlashSkillRequiresInitCatalogAck` 的宽松推断。Codex“输入文本含 $name 就认已调用”的实现不得复制；typed Skill输入必须真实传入并获得正式成功确认。未知模型、transport配置限制、版本缺字段都发 observation_unavailable 或对应安全 notice，正常输出仍由既存 parser产生；不能开虚假的能力位。

Capability start见证的是一次调用/输入接受，不是成功完成。正式错误结果=>Failed；用户cancel=>Cancelled；缺terminal/EOF/协议截断=>Interrupted（或有明确协议错误时Failed+ProtocolError）。Todo的失败工具请求=>没有新快照，不能沿用源代码在args结束处直接改表。取消/非零退出也不能因任何新事件把 checkpoint标为Valid。

固定源证据入口：`3ea225a` 的 Claude parser/streaming_parser补发（拒绝其Args+ArgsDelta重复）；`1921636` 的四Driver capability fixtures；`9b1ce27`/`dab6933` 的catalog驱动MCP归属（特例留Claude）；`51d12bd` 的safe relay；`7438d26` 的before成功后started；`eb82ed3` 的todo fixtures/table（拒绝其本地序号冒充ID、空表drop与provider helper解析）。这些均通过 `git -C /Users/blurooo/project/agent-adaptor-internal show <固定SHA>:<path>` 阅读。

## 11. 可直接复现的合同验收 fixtures

各 owner 的测试必须先证明基线缺口或误判，再验证实际实现；下面是合同期的预期输入/输出，不冒充已跑 Go tests。

| Fixture ID / 实施者 | 输入或安排 | 必须断言 |
|---|---|---|
| C03-F01 / T06,T14 | 父p，子scope A/B均tool ID x；A同wrapper重放；普通增量与完整wrapper混合 | 每key start/end各一次；ParentScopeID准确；快照不再发同ArgsDelta；ToolResult不跨scope |
| C03-F02 / T06 | 并发100producer，observer记录seq，buffer1/drop/block/cancel | observer与accepted序号一致；只有增量可正常drop；关键事件保留；Cancel/Close有界；Result在Events关闭时可读 |
| C03-F03 / T06,T13 | recorder在第一次Append后 Query；第二次分别error/panic/挂起；另一run继续 | 第一次实时可见；100ms停本run写一次notice；共享Store不Close；迟到不触发第二notice；原Result/checkpoint/审批相同 |
| C03-F04 / T06,T14,T15 | 成功TaskCreate真实ID=91，更新91；更新1；失败Create；缺ID成功Create再真实Update1 | 91正确更新；1未知不改表；失败不改表；synthetic明确且不匹配1；多轮缓存不复用 |
| C03-F05 / T06,T11,T12 | replace一条→重复→replace []；未知status、invalidUTF8、重复ID | revision1→无事件→revision2空；拒绝整次畸形快照；wire/AGUI/SSE/JSONL保留[] |
| C03-F06 / T14,T15,T16,T17 | 四driver第10节正式fixture+unknown catalog | 仅明确字段归属；Unicode/下划线alias冲突不归属；无文字猜测；source正式错误按闭集分类 |
| C03-F07 / T11,T18 | 65536/65537 bytes、未知字段、重复key、null items、uint64>2^53安全拒绝、duration0/negative | 双侧边界一致；不把decode错误当有效Event；safe dropped无payload回显 |
| C03-F08 / T11,T18 | 默认exposure与仅IncludeToolCalls、专用opt-in、metadata off/on | default新能力/todo零泄漏；opt-in只闭集；Source链需metadata开关；不存在任意Raw穿透 |
| C03-F09 / T18 | 无远端Store，有/无本地Store，EventBus满，两DelegationID共享remote IDs；双层relay | 事实先observer；本地ID不同且生命周期一致；单一core事件；Source顺序保留；无EventBus重播副本 |
| C03-F10 / T18 | BeforeDelegate拒绝/超时，成功后AgentCard失败，AfterDelegate失败；Local同组 | before失败没有cap事实；成功后先started再I/O；一份准确终局；Local/Remote相同 |
| C03-F11 / T06,T10,T14–T17 | Agent/Thread × Run/Stream × WithSpawn；schema+Ask+observation | transport来自同resolver；字段与Result各层相等；无observer失败污染；C02重叠Ask/主动预算不变 |
| C03-F12 / T12 | subagentstream.Merge非nilbus与parent partial error；标准Service.Option | 已绑定证明下Merge透明；未绑定保留原meta/partial Result并明确unsupported；Service入口一流一序号 |

## 12. 依赖选型、发布与审阅结论

本合同选标准库实现有界catalog/tracker/table/strict DTO校验与现有A2A/AGUI依赖，不新增顶层 require。理由：状态机规模小且只处理规范化值；新增通用框架不能明显提升可靠性；成熟协议依赖已在对应bridge边界。Codex若缺生成schema必须用官方生成命令和go generate，不手工改generated/schema。

公共变化按minor/API审阅处理，Merge行为修复必须单列breaking说明。Root golden只增加两Event、三关联字段、来源链和第6节的受限运行扩展声明；不增加With*计数。Driver golden增加两个StreamKind/payload字段、父字段及矩阵/需求；叶包与recorder由各自godoc/test冻结。所有Result输出层、HITL error唯一面、checkpoint与source解析边界保留。

合同期验证限文档/任务图/签名状态机fixture静态审阅，不需要付费CLI、Windows或Linux runner。后续实现tests/live由表列owner和B05/B06验收；C03完成不表示W05/W09/W10/W12关闭，也不授权tag/push/release。
