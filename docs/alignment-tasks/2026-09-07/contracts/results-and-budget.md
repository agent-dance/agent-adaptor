# C02 — 部分 Result、错误归因与主动执行预算合同

合同版本：1。所有者 C02；冻结门禁 G00；实现 T05/T10/T18/T19，独立验收按 coverage.json。基线 `bc0d421f9c0b1e80e529d1e843fe5d8396eab02f`（代码祖先 `919f140f64f89c80933802840c8878681a85a4d9`）。本文件冻结实施合同，不声称功能已经实现或 W04/W13 已通过。

权威依次为 AGENTS.md、固定对齐方案 W04/W13、本合同。固定源证据仅通过 `git -C /Users/blurooo/project/agent-adaptor-internal show` 读取 `126d610dfb6afd2cca19661ab0a7c0b6f626b490`、`e2f0620bdd6477e6fe16f6db5648093589342ca2` 的 Git 对象；不依赖源工作区或同批未验收产物。

## 1. 边界与已确认缺口

- `Run` 仍为 `Stream` + drain + `Result()`；成功 `result, nil`，失败 **始终 `nil, error`**。不增加 Result.Failure、第二个结果入口、执行入口或通用 context 框架。
- 基线 `invocation.go:finalizeRun` 已构造 Result，却在未分类的 context/transport 错误上丢弃它；已分类 failure 又丢失同时返回的原 cause。续期失败、Persist/缺失 checkpoint 和 defer 中仅有 cleanup 错误同样可能绕过或丢失映射。
- 基线 `sink.go:RequestDecision` 持有 `decisionSerial` 覆盖整个 Ask，`setPendingFailure` 可覆盖之前 failure；基线按派生 context 的 `Err()==DeadlineExceeded` 判断审批超时，会混淆 parent deadline。T10 必须按下述状态机处理。
- 基线 A2A server 与 Local loopback 先判断 `errors.Is(context.Canceled)`；新 carrier 允许同时匹配具体失败与其派生取消后，必须先读权威 Reason，防止审批/预算失败变成 canceled。
- 重新打开 AGENTS §14 的“错误路径 Result 映射”审计子项。C02 仅关闭合同设计；T05、provider 实现、T10、独立验证和相关 gate 才提供功能关闭证据。

## 2. 冻结的公共 Go 增量

以下声明是实施签名，不是新增的第三方 SPI alias。所有既有字段、方法与 reason 保留。

```go
// package adaptor; T05 修改 errors.go
const (
    ReasonInfrastructure FailureReason = "infrastructure_error"
    ReasonDeadlineExceeded FailureReason = "deadline_exceeded"
)

// 在既有 RunError 中追加；不删除 Reason/Message/Details/Result。
// Cause 保存主原因及已观察到的次因，允许 errors.Join；可为 nil。
// Result 在 SDK 返回 RunError 时始终非 nil。
type RunError struct {
    Reason FailureReason
    Message string
    Details map[string]any
    Result *Result
    Cause error
}
func (e *RunError) Error() string
func (e *RunError) Unwrap() error

// package adaptor; T10 修改 policy.go / errors.go
// 在既有 Policy 中追加 ActiveExecutionTimeout time.Duration。
const ReasonActiveExecutionTimeout FailureReason = "active_execution_timeout"
var ErrActiveExecutionTimeout = errors.New("adaptor: active execution timeout")

type ActiveExecutionTimeoutError struct {
    Limit time.Duration
}
func (e *ActiveExecutionTimeoutError) Error() string
func (e *ActiveExecutionTimeoutError) Unwrap() error

// package driver; T10 在其拥有的 driver/run.go 追加，不改 driver/decision.go。
const FailureActiveExecutionTimeout FailureCode = "active_execution_timeout"
```

`RunError.Unwrap() error` 保持原签名，返回 `errors.Join(reasonSentinel, e.Cause)`（忽略 nil，均 nil 时返回 nil）。Reason 与 sentinel 映射：既有五种不变；ReasonDeadlineExceeded → `context.DeadlineExceeded`；ReasonActiveExecutionTimeout → ErrActiveExecutionTimeout；ReasonInfrastructure 无新 sentinel，匹配 Cause。零值和 nil receiver 的 Error/Unwrap 不 panic，nil Unwrap 返回 nil。ActiveExecutionTimeoutError.Unwrap 返回 ErrActiveExecutionTimeout；其 Error 使用静态前缀及 Limit 的 duration 文本，不能加入 prompt、路径或 provider payload。SDK 创建的本地 ActiveExecutionTimeoutError.Limit 是本轮有效正预算。

ReasonCancelled 扩展为已进入执行阶段的主动取消，不再限于 Driver 分类的取消；仍匹配 ErrRunCancelled。普通取消的 Cause 保留 `context.Canceled`；外部 deadline 保留 `context.DeadlineExceeded`。**不新增 IsActiveExecutionTimeout 便捷函数**，使用标准 `errors.Is/As`。预算仅由 core/hosttool 的私有计时器执行；新增 Driver FailureCode 供 typed 生命周期与 Response 映射保持一致，不授权 Driver 自行建立第二个预算或改变 checkpoint。

```go
res, err := agent.Run(ctx, "work")
if err != nil {
    // res == nil 是唯一失败返回约定。
    var re *adaptor.RunError
    if errors.As(err, &re) {
        raw := re.Result.Raw()
        _ = raw // stdout/stderr/正式 terminal payload，包括中断前已观察到的部分
        switch re.Reason { // 主归因只读这里，不按 errors.Is 的排列顺序猜
        case adaptor.ReasonActiveExecutionTimeout:
            var budget *adaptor.ActiveExecutionTimeoutError
            if errors.As(err, &budget) { _ = budget.Limit }
        }
    }
    // errors.Is/As 仍能到达 context、*exec.ExitError、自定义 transport/store cause。
    return err
}
_ = res.Text
```

`Cause` 不是第二个判定面：它只保留可检查的证据。SDK 为同一次运行只构造一个最外层 RunError，不自行嵌套另一个携带不同 Result 的 carrier；外部返回的原 cause 按原类型保留。追加次因时更新/构造最终 carrier 的 Cause，不用任意 `errors.Join` 的遍历顺序决定 Reason。Result/Details/Cause 一旦 Events 关闭便不再修改，多次并发 Result() 返回一致对象与错误。宿主不应并发修改收到的结果。

## 3. 执行前后与部分结果

“Driver 已执行”以唯一 `driver.Run` 调用被进入为界，不以 prompt 是否交付为界；prompt 边界只控制安全 fallback。结果映射保留同一 Response 的 Text、Summary、Raw stdout/stderr/Terminal、Transcript、已观察 Usage、Model、Provider、Metadata、Services 与已校验结构化数据。Text 不回填原始输出，Summary 不回填完整 Text，取消不触发新 schema 校验来替换原 cause。只有已校验的结构化数据可由 Decode 使用。

| 发生阶段/错误 | 唯一用户 outcome | Result/cause | checkpoint/store |
|---|---|---|---|
| 准入关闭、ID 生成、静态 Config/Policy/schema/capability/Thread selector 校验 | `nil,` 原有可 Is/As 包装错误 | 无 RunError；保持原错误类型，ErrAgentClosed 不变 | 不获取资源、不调用 Driver、不持久化 |
| workspace/profile/skill/MCP/runtime/Thread 准备失败，Driver 未进入 | `nil,` 原有包装错误 | 无伪造业务失败或 provider Result；已经发生的资源错误保留 cause | 有界释放已获取资源；不创建孤儿记录 |
| 主动预算在资源准备中耗尽 | `nil,` 包装的 ActiveExecutionTimeoutError | `errors.Is(ErrActiveExecutionTimeout)`；尚无 Driver 则不伪造 RunError；已观察的 runtime 报告仍按既有事件/服务清理合同保留 | 不调用 Driver；不持久化 |
| Driver 已进入后 bare cancel / outer deadline / transport 或 handler 返回 error | `nil, *RunError` | ReasonCancelled / ReasonDeadlineExceeded / ReasonInfrastructure；Result 可空但非 nil；原 cause 可 Is/As | 不持久化 |
| provider 正式业务失败/非零退出/畸形协议/handler panic | `nil, *RunError` | ReasonAgentError（或正式 Driver reason）；保留 Result、原 cause/过程错误 | 不持久化 |
| 审批拒绝/审批自身超时且 fallback Abort、Retry 不支持或耗尽 | `nil, *RunError` | ReasonApprovalDenied / ReasonApprovalTimeout；原审批原因优先于由其引发的 generic cancel | 不持久化 |
| 审批拒绝/超时且 fallback Continue | 不是终止候选；按后续执行终局返回 | 保留审批 requested/resolved 事件；不制造 RunError | 仅后续健康成功可持久化 |
| Driver 已进入后的预算耗尽 | `nil, *RunError` | ReasonActiveExecutionTimeout；Cause 含 typed Limit 和已观察到的 Driver error | 不持久化 |
| Driver 返回后 lease renewal/Finalize/缺失 checkpoint 错误 | `nil, *RunError` | 无更早失败时 ReasonInfrastructure，Cause 可匹配公开 Thread/store 错误；Result 不丢 | 原健康记录不变；Finalize 原子语义不变 |
| Driver 返回成功且仅 release/teardown 失败 | `nil, *RunError` | ReasonInfrastructure；完整 Result 与 cleanup cause | 健康 checkpoint 若已原子提交，不回滚、不再次提交；这不是允许失败 checkpoint |
| 已有失败后再遇到 release/teardown/CancelTask/AfterDelegate 错误 | 原 Reason/Code 不变 | 次因作为 Cause 或 hosttool 安全诊断；不得覆盖主错误/部分输出 | 不增加持久化路径 |

T05 需让所有 Driver 已进入后的出口回到唯一映射/终局路径，包括 resume-reject 后 PrepareFresh 失败、续期错误、Persist/缺失 checkpoint、纯 cleanup 错误。安全 resume fallback 或 Driver 启动 fallback 是同一 invocation，最多既有合同允许的一次，保留已观察审计数据、同一 RunID 与同一主动预算；fallback 前若已取消不再启动。失败路径不因“存在 ResumeID”而写 store。

`invocationCanPersist` 的全部健康条件保留：context 未终止、runErr/pending/Failure 均空、exit 0、无 signal/timedOut、正式 parser 的有效 checkpoint 通过 SessionCodec。预算、取消、审批失败、协议失败都不能绕开它。第一次中断没有健康 checkpoint 时下轮新建；已有健康 record 逐字段不变。cleanup detached context 仅用于有界资源回收，绝不用来持久化中断 checkpoint。

## 4. 唯一终局归因与竞争规则

### 4.1 候选与锁定

T10 在本轮现有管线内维护一个私有终止记录；不新增公共接口或语义 channel。锁定只覆盖候选比较/写入，禁止在锁内调用 Driver、observer、handler、发送 Event 或做 I/O。

1. 一个终止候选在 SDK **确认它要求结束本轮**时登记：审批 fallback 决定 Abort/耗尽后（不是仅收到拒绝时）、计时器确认耗尽时、父 context/Cancel 观察到时、lease 明确失效时、Driver 返回正式失败或原 error 时。Driver 的中间失败事件不单独作为整个 run 终局。
2. 已登记的具体主因不被后到候选改写；由主因引起的 context.Canceled、process killed、取消后的续期失败、cleanup 仅追加为次因。并发候选在同一短临界区线性化，先完成登记者获胜；测试用 barrier 确定顺序，不用壁钟时间戳推测实际先后。
3. 一个观察点已经同时持有多个候选、尚无主因时，固定次序为：已作出 Abort 的 approval → 已锁定 context.Cause（预算/外部取消/deadline）→ Driver 正式 failure → lease/transport 基础设施错误 → provider-agnostic 非零退出归类。具体 cause 缺失才使用 generic ctx.Err；不得把包裹 active cause 的 Canceled 当成用户取消。
4. 父 context 在 budget/Ask 回调提交前已经 Done，则先登记 parent 的真实 Cause；自定义 parent cause 仍可 Is/As，分类由 parent.Err 区分 Cancel/Deadline。一个已经成功响应的审批只结束该 Ask，不阻止后续预算耗尽。
5. 所有终止路径都向最终错误保留已观察到的原始 cause；主 Reason 只存在一处。`errors.Is` 可同时匹配次因，所以 bridge/hosttool 必须先检查 carrier 的 Reason/Code，再使用 bare context fallback。
6. 正常 Driver 返回后仍检查 renewal、结构化输出与 Thread commit；未完成这些步骤不能锁定成功。按 R012 在健康候选进入原子 Persist 前完成主动预算封账；Finalize 仍决定最终成功。失败路径 Stop 主动计时，再做有界 cleanup；cleanup 自身不能把已结束运行改成 active timeout。仅 cleanup 失败时将成功替换成 ReasonInfrastructure 并携带原 Result。

T05 在尚无主动预算的 B01 使用既有 pending approval → Response.Failure → 原 error 的具体原因优先级，修复 Cause/Result 映射。T10 加入上述 first-terminal 记录并同步唯一 sink；不能只在 finalizeRun 最后根据 ctx.Err 覆盖已有原因。RunFinished/终局 Result/error 读取同一最终记录。资源准备中预算耗尽尚无RunError时，T10在其拥有的sink.go先匹配typed active cause再匹配generic context；T05的新Reason已由既有errors.As分支直接复制，无需提前修改sink.go。事件关闭发生在所有终局信息交付之后。

### 4.2 必须区分的 race fixture

| 已确认先后 | Reason / Is 断言 |
|---|---|
| approval Abort → Driver 返回 Canceled | approval reason；匹配审批 sentinel，原 Canceled 仍可匹配 |
| approval Continue → budget expiry | active_execution_timeout |
| budget expiry → parent Cancel / killed / lease error / cleanup | active_execution_timeout，所有实际返回的次因仍可到达 |
| parent deadline → Ask 派生 context Done | deadline_exceeded，不是 approval_timeout |
| Ask 自身 deadline → Abort → parent Cancel | approval_timeout |
| parent Cancel → budget stale callback | cancelled；stale callback 不产生 active sentinel |
| provider 正式失败登记 → parent Cancel | agent_error；取消只是次因 |
| parent Cancel 登记 → Driver 返回因终止产生的失败 | cancelled；保留正式 payload 和返回 error |
| lease 失效登记 → Driver 被取消 | infrastructure_error；匹配原 lease error |
| 健康成功停止计时 → cleanup 耗时大于原 remaining | infrastructure_error 或成功取决 cleanup 结果，绝非预算超时 |

## 5. Policy 与计时生命周期

`Policy.ActiveExecutionTimeout time.Duration`：0 无主动预算；正值为本轮限额；负值在任何资源获取/Driver 调用前返回既有 `*InvalidPolicyError{Field:"Policy.ActiveExecutionTimeout", Value: duration.String()}`，匹配 ErrInvalidPolicy。全部正 Duration 包括不足 1ms 的值均有效。

call `WithPolicy` 整值替换 Agent Policy。构造 `Policy{ActiveExecutionTimeout:100*time.Millisecond}` 后 call `WithPolicy(Policy{})` 清除主动预算，其他维度也按既有整体替换；绝不逐字段“0 继承、负值关闭”。预设 PolicyReadOnly/WorkspaceWrite/Unrestricted 的新字段为 0。没有额外 WithActiveExecutionTimeout。

计时从完成静态准入/负值校验后、**首次资源准备之前**开始；包含 hosted-tool profile、workspace/runtime、skill/append 文件、Thread lease acquire、resume/recovery、安全 fallback、Driver 启动/运行、本轮 schema 终局处理及 lease/健康条件复核；按 R012 在进入唯一原子 Persist 前以 FinishExecution 封账，Finalize 及其返回延迟不扣主动预算。无状态成功在对应最终健康判定点封账；封账不等于运行成功，Finalize 失败仍保留部分 Result 并返回原错误。一个 invocation 不在任何重试点重置预算。WithTimeout 与 parent deadline 始终按整体墙钟计时；主动预算的 Deadline() 不伪装成固定到期时间，child context 只继承 parent 的真实 deadline。

暂停只作用于明确持有的当前 run controller；不把 controller 放进 context.Value 再向上查找，不让 Member Ask 自动暂停 Leader、别的 Thread 或另一次 Delegate。预算不进入 Thread compatibility fingerprint、profile manifest 或常驻启动签名，因为它不改变 provider 会话环境；实际 prompt/Instructions/资源变化仍由原合同完整覆盖。

R012：Finalize 使用原可取消 run context，禁止 detached 持久化；父取消/墙钟和 store 原子 lease 检查仍有效。没有提交确认接口时不能以调用返回时间推断提交时刻，已经原子提交的健康状态不回滚。必须测试提交后延迟返回跨过原预算、提交错误、封账前余额已尽但 timer 未执行、封账后迟到 callback，以及 parent 在提交前取消。

R012 Store补充：Finalize必须在提交前检查context；内置memory在取得mutex前及锁内第一写前检查，等待锁期间取消不能写入。有效检查后进入原子提交临界阶段，完成的健康提交不因返回延迟中后到取消而回滚；这不是允许取消后新写失败checkpoint。T10新增memory/store.go、memory/store_test.go和threadstore/threadstore.go（godoc）范围，无公开接口变更。

## 6. 私有计时器 API、fake clock 与状态机

T10 创建 `internal/activebudget/budget.go` 和同包 `budget_test.go`。该包只依赖标准库，不 import 根包、Driver 或 provider。T18 只依赖已由 B03 合入的此包；它不是内部 engine，也不替代执行管线。

```go
// package activebudget（internal，可供 root 与 hosttool 使用）
type Timer interface { Stop() bool }
type Clock interface {
    Now() time.Time
    AfterFunc(time.Duration, func()) Timer
}
type Controller struct { /* 私有状态 */ }
func New(parent context.Context, limit time.Duration, expired error, clock Clock) (context.Context, *Controller)
func (b *Controller) Pause() (release func())
func (b *Controller) Stop()
func (b *Controller) FinishExecution() error
func (b *Controller) Cancel(cause error)
func (b *Controller) SelectedCause() error
```

签名中的 struct 仅表示实现拥有私有字段，不是要提交空实现。`clock==nil` 使用 time.Now/time.AfterFunc；父 context 必须非 nil，limit 必须非负，正 limit 的 expired 必须非 nil，违反这些私有编程前置条件可以 panic；公共入口先完成结构化校验。New(0) 返回可取消 child 与无 timer controller。expired 由调用方传入 `&adaptor.ActiveExecutionTimeoutError{Limit:effectiveLimit}`，所以私有包无反向依赖。

状态为 running / paused / stopped，保护字段至少包括 remaining、lastStart、generation、未释放 token 集合、timer 和 cancelCause。不暴露通用 PausableContext 或单一 Pause/Resume bool API。

- running → Pause：锁内以单调 `Now().Sub(lastStart)` 扣 remaining，停止旧 timer 并递增 generation；若 elapsed >= remaining，提交 expired，不能用暂停救回已耗尽预算；否则登记唯一 token，进入 paused。
- paused → Pause：新增另一个唯一 token，不重复扣时、不重置预算；返回仅释放自己 token 的闭包。
- release：并发/重复调用幂等；非最后 token 只移除自己；最后 token 才以 remaining 重新 arm，递增 generation。旧 token 不能解除新 token。所有停机后的 release/Pause 均为 no-op，Pause 仍返回可安全调用的非 nil release。
- timer 回调携带 arm 时 generation。仅 running、token 数 0、generation 相等且 parent 未终止时生效；先按单调 now 重新核对余额，提前触发则重新 arm 剩余时间，已到限额才提交 expired。已停止但排队的旧回调一律忽略。
- R012 FinishExecution 在同一短锁检查 parent/child cause，running 段按 Now 扣余额（paused 不扣）；已耗尽则取消为 expired 并返回 cause，否则永久封账、Stop timer、推进 generation、清 token。首次结果缓存，重复调用不重算。nil 只代表预算封账成功；parent 随后仍可取消。Core 必须将已有主因检查和封账在归因短临界区排序，方法不得回调 core；已有失败或返回 error 则不能 Persist。该方法用于最终健康候选，必须在 cleanup Stop 前调用；Stop先行后的晚到Finish/Fire不得补扣出新active cause，core不能借清理Stop跳过健康封账。
- Stop 幂等，冻结 timer/token，不取消 child，不阻断仍在进行的父取消传播；仅用于已有失败或已封账后的 cleanup，不重新结算/生成后到预算错误。Cancel 幂等，先停止再 cancelCause(cause)；cause==nil 遵循 context.WithCancelCause 的 Canceled 语义，不能覆盖已发生 cause。
- parent cancellation 始终通过标准 context 传播，无论 paused/Stop；watcher 负责停 timer 与释放引用，退出后无泄漏。锁内不执行外部 callback；Clock.AfterFunc 不同步 inline 调用回调，Timer.Stop 不能被当作“回调绝不再来”的保证。
- 单调时间不能负向增加预算。fake clock 的 Advance 必须拒绝负数，时刻相同的 timer 按创建序处理；测试能保存已停止 callback 并手工 Fire，以验证 generation。fake API 为测试文件内 `newFakeClock()`、`Advance(d)`、`Fire(timerID)`、`Pending()`，不形成 runtime 公共 API。

fake fixture：limit=100ms；Advance(40ms)；p=Pause()；Advance(300ms)；p()；Advance(50ms) 时 Cause=nil；Advance(10ms) 时 Cause 匹配 ErrActiveExecutionTimeout，typed Limit=100ms。`budget.Err()` 可为 Canceled，分类必须读 Cause。

还必须测试 A/B 重叠 token、重复 release、旧 callback、Pause 恰好在100ms、zero limit、Cancel/Stop 与 callback 并发、parent 在 paused 时 Cancel/deadline、终局后 release。race 测试包含真实调度压力，精确预算断言使用 fake clock；不能把 sleep 的运气作为通过依据。

## 7. Ask、背压与 observer 的精确边界

只在唯一 approval sink 已解析模式为 Ask、准备开始**一个 attempt**时申请 Pause token，紧接注册 defer release。token 先于审批 notice、ApprovalRequest 入队、OnApproval callback 和等待响应；因此已经对用户发出的审批卡不消耗主动预算，队列已满时也由墙钟审批 deadline 约束。自动批准/拒绝从不申请 token。

一次 Ask 生命周期：规范化/选择模式（计时）→ 申请 token → 创建审批独立墙钟 deadline → 入队或 handler → 等待且 exactly-once 取响应/expire → release token → resolved notice、fallback/Retry 策略处理（计时）。每个 retry 重新生成既有 RequestID/deadline，重新申请独立 token；attempt 间处理不暂停，不重置总预算。请求已经过期也必须有界退出并释放 token。

父 run context Done 与审批本地 timer 必须分别检查；只有本地审批 deadline 实际先到且 parent 尚未结束，才是 DecisionTimedOut。外部 deadline 保持 ReasonDeadlineExceeded，不能流入 OnTimeout fallback。panic、handler error、返回未应答、拒绝、超时、Cancel、broker abort、运行结束各出口必须 release 或 Stop；迟到应答只按既有 ErrApprovalResolved/错误 kind 合同失败。

短锁仅保护归因/注册/序号。现有 `decisionSerial` 不得覆盖入队或 handler/人工等待；T10 调整为支持多个并发 Ask，outstanding 以唯一 RequestID 隔离。第二个请求即使排队进入同一 broker，也拥有自己的 token；一个请求结束不能恢复另一个仍在等待的请求。

G00统一裁决：C03的observer只接收CapabilityInvocation/TodoUpdated，不接收ApprovalRequest或审批notice，因此不存在“审批observer”。Ask入队与人工handler属于暂停段；Capability/Todo observer在唯一sink分配Meta后、用户drop前执行，若当时没有未结束Ask则计入主动预算，若另一个并发Ask持有token则随整轮暂停。observer独立100ms墙钟限制不暂停；错误/panic不改变run主结果。Pause不暂停lease renewal、父Cancel、Agent.Close或清理。

Close 开始后新 Agent/Thread 运行稳定 ErrAgentClosed；已准入运行被 Close 取消时按已锁定原因结束，没有更早原因则 ReasonCancelled + context.Canceled。无需为已运行 Close 添加新执行错误类别。T10 可在既有 stream/cancel 接线内完成；不要求修改 agent.go 的公共 Close API。

## 8. Delegation 独立预算合同（T18）

仅在 hosttools/a2adelegation 追加：

```go
// 加入既有 DelegationRequest：
ActiveExecutionTimeout time.Duration
// 加入既有 DelegationPolicy：
MaxActiveExecutionTimeout time.Duration
// 加入既有 DelegationError；其他字段及返回 Result+error 的 hosttool 合同保留：
Cause error `json:"-"`
func (e *DelegationError) Unwrap() error
```

DelegationError.Unwrap 返回 Cause，nil receiver 返回 nil。Code 是该 hosttool 原有主归因；Cause 仅保存原 error，不把 root Result 另建一个入口。Local Runner 的 root error 可作为 Cause 保留，部分公开文本/artifacts 照既有 exposure 正常投影；不改 hosttool 已发布的 DelegationResult.Error 字段或其 Result+error 返回合同。

新请求预算与政策上限均非负，负值在 BeforeDelegate/远端 I/O 前返回 `DelegationError{Code:"invalid_policy"}`；Registry.Register 拒绝负 MaxActiveExecutionTimeout，运行入口防御性再检查。有效主动预算为：请求>0、上限>0 时取 min；仅一个>0 时取该值；都0则无预算。新字段不使用旧 MaxTimeout 默认值，原 Timeout/MaxTimeout 的墙钟语义及默认合并保持。

顺序固定为 registry 查找/验证 → 建立墙钟与主动预算 → BeforeDelegate 成功 → 外层 started 事实与唯一 terminal defer → discovery/card/Send/流/poll/recovery。BeforeDelegate 拒绝不产生“已调用”capability 事实；主动预算涵盖 BeforeDelegate、discovery、网络重试、读取、poll间隔和 recovery。Delegate 结束候选按 R012 完成私有预算封账后，AfterDelegate/CancelTask 是独立有界 cleanup；不在主预算中重新计时，既有主 Code 仍先于后到原因。

每次 Delegate（包括同 TaskID continuation）创建新预算；一个 Delegate 内传输重试、GetTask recovery、历史 Task 回放不重新 New。预算不跨 Task、Thread 或进程持久化，不通过任意请求 metadata 让远端改 core Policy。已知 TaskID 从 `req.Message.TaskID` 在调用开始播种，随后只由已确认同次远端响应更新。

Delegator 不拥有 root approval sink，因此不会根据远端 status/工具名擅自暂停 parent/Leader 的 budget，也不向 Local Runner 注入一个会整体覆盖其已配置 Policy 的 WithPolicy。它自身的请求预算约束本次调用；Member 自己的 Policy 只在自己的 Ask 暂停。live input-required 是本次 Delegate 的明确返回边界（按 AllowInputRequired），宿主人工答复时间发生在两次 Delegate 之间，自然不收费；旧 Task 的 input-required snapshot 不是暂停、成功或新问卷。EOF 且无新 live 终态保持 stream_interrupted；只有计时器确实赢得终局才映射 active timeout。

本地预算终止、父取消/deadline 等需要中止远端时，已知 TaskID 必须尽力 CancelTask 一次，使用 `context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)`；不知道 TaskID 不猜测、不补发 prompt。诊断只有 `Metadata["remote_cancel"]="failed"`/`"timeout"`，不暴露 URL/token/底层错误；不改原 Code/Retryable/部分结果。AfterDelegate 失败若已有错误只作次因；若本次执行成功则延续已发布 workflow_after_failed。预算错误统一 Retryable=false；其他既有策略不借本项改义或自动重试。

## 9. A2A 安全 wire（T18/T19 独立可用）

沿用已有 status message 的 **Text Part.Metadata["agentadaptor.failure"]**，不另建 stream schema，不使用 Driver SPI 的 decision_rejected/decision_timeout 代替 root 已发布的 approval_denied/approval_timeout。

### 9.1 封闭语义与编码

安全控制字段仅 `code` 与可选 `limit_ms`；现有显式 metadata opt-in 仍可增加经 ExposurePolicy 过滤的 `metadata` 对象，但不得把它提升为控制字段。code 以 RunError.Reason 或 bare-error 的具体原因产生。必须先选择主 Reason，再检查次因；审批原因 + Cause(Canceled) 仍为 failed 审批，不为 canceled。

| root Reason / bare cause | status.state | code | limit_ms | T18 Code / Retryable |
|---|---|---|---|---|
| ReasonActiveExecutionTimeout / typed active cause | failed | active_execution_timeout | 有可信 typed Limit 时有 | active_execution_timeout / false |
| ReasonApprovalDenied | failed | approval_denied | 无 | approval_denied / false |
| ReasonApprovalTimeout | failed | approval_timeout | 无 | approval_timeout / false |
| ReasonCancelled / bare Canceled | canceled | cancelled | 无 | cancelled / 保持既有取消策略 |
| ReasonDeadlineExceeded / bare DeadlineExceeded | failed | deadline_exceeded | 无 | deadline_exceeded / false |
| ReasonAgentError | failed | agent_error | 无 | agent_error / false |
| ReasonPolicyViolation | failed | policy_violation | 无 | policy_violation / false |
| ReasonInfrastructure | failed | infrastructure_error | 无 | infrastructure_error / false |
| 无有效封闭 code 的远端失败/未知扩展 reason | 保留实际失败状态 | 不提升任意文本 | 无 | 既有 remote_failed/remote_cancelled fallback |

`limit_ms` 是 JSON 安全整数，范围 **1..9223372036855**。编码为 `ceil(Limit / time.Millisecond)`，用除法+余数避免 duration 加法溢出；只从主预算错误的 typed Limit>0 产生，不信任 RunError.Details 中同名值。亚毫秒向上取整；上界来自最大 int64 nanoseconds 的向上取整；这是声明预算的毫秒投影，不声称跨 wire 纳秒精确。

接收端仅当主 code 为 active_execution_timeout 才接纳 limit_ms：有限、整数、上述范围，禁止字符串、布尔、fraction、NaN/Inf、负值/0。Go 解码后的 json.Number/int64/确实安全的 float64 按相同数学域验证。未知/额外控制字段、多个 failure Part 冲突或 code 与终态不匹配时不提升整个 failure 控制对象，保持远端失败并记录安全 `failure_payload_invalid` 诊断；不能产生成功或猜测 retry。多个完全相同控制对象可去重。`metadata` 不能覆盖顶层 code/limit_ms，不参与 promotion。

T18 在 DelegationError.Metadata 中只保留安全数值 limit_ms；有值时 Cause 可构造 ActiveExecutionTimeoutError，Limit=`min(limit_ms*1ms, math.MaxInt64)`，乘法前作溢出检查。缺值仍保留 Code 并匹配 ErrActiveExecutionTimeout，但不伪造 typed Limit；合法缺值兼容不携带限额的对端。接受限额不改变本地预算。此毫秒往返的舍入/饱和须测试，实际本地 Error.Limit 保持原纳秒值。

Bridge 的 failure 文本使用该类别安全静态文案；不默认输出 Cause.Error、provider 原错误或 Details。普通 Raw/Transcript/Usage/artifacts 的暴露继续走原 ExposurePolicy；为跨进程稳定判断开放 code/limit_ms，不开放任意 metadata。取消/预算失败也要在 terminal status 前投影 error.Result 中允许暴露的部分输出，不能因优先 cancel 分支丢失。

### 9.2 可复制 JSON fixture

以下是规范化 A2A status 对象（嵌入现有 Task/status-update 外层；SDK ID 在测试中固定）。现有 protobuf/JSON-RPC binding 负责外层 wire 拼写，不能把历史 Task snapshot 当作这次 live status。

```json
{
  "state": "failed",
  "message": {
    "messageId": "failure-1",
    "role": "agent",
    "parts": [{
      "kind": "text",
      "text": "active execution budget exhausted",
      "metadata": {"agentadaptor.failure": {"code": "active_execution_timeout", "limit_ms": 100}}
    }]
  }
}
```

下面的每一行是独立 failure 控制对象 fixture，T18 可直接用 clienta2a.TaskStatus/Part 构造，T19 可从 fake Runner 错误生成后反向提取同一对象；不需要引用同批新 Go helper。

```json
{"code":"active_execution_timeout","limit_ms":100}
{"code":"active_execution_timeout"}
{"code":"approval_denied"}
{"code":"approval_timeout"}
{"code":"cancelled"}
{"code":"deadline_exceeded"}
{"code":"agent_error"}
{"code":"policy_violation"}
{"code":"infrastructure_error"}
```

非法/不提升 fixture：

```json
{"code":"active_execution_timeout","limit_ms":0}
{"code":"active_execution_timeout","limit_ms":-1}
{"code":"active_execution_timeout","limit_ms":1.5}
{"code":"active_execution_timeout","limit_ms":"100"}
{"code":"active_execution_timeout","limit_ms":9223372036856}
{"code":"active_execution_timeout","limit_ms":100,"token":"secret"}
{"code":"approval_denied","limit_ms":100}
{"code":"decision_timeout"}
```

最后一项是不同产品历史 SPI 拼写，不新增兼容 alias。另测 `metadata:{"limit_ms":1,"code":"cancelled","token":"secret"}` 不能替换有效顶层控制字段；默认 exposure 不生成 metadata。T19 编码、T18解码双边 fixture 在后续集成中同表核对。机器可读相同用例见 [budget-wire.json](../handoffs/C02/fixtures/budget-wire.json)；状态外壳是规范化示意，failure object 才是双边逐字段匹配目标。

## 10. 文件分配与实施独立性

| owner / batch | 最小改动与交付 |
|---|---|
| T05 / B01 | errors.go、invocation.go、Result 映射/测试和 root golden；新增 Cause、两种 reason；Driver 后全部错误携带部分 Result；不改 provider 或预算 |
| T06 / B02（C03 合同） | observer/事件排序基础；不用跨 callback/发送的 run-wide 等待锁；不需要预算符号 |
| T10 / B03 | policy.go、errors.go、stream.go、sink.go、invocation.go、approval.go、internal/activebudget/ 与 tests；driver/run.go 的新 FailureCode；两 golden。复用 T05 carrier；资源准备/append时间统一计时 |
| T18 / B04 | 仅 hosttools/a2adelegation/；本合同中的新字段、Cause、私有 timer（消费 B03）、Local/Remote reason 投影、输入 TaskID cancel、wire 安全解码 |
| T19 / B04 | 仅 bridges/a2a/；消费 B03 RunError/typed Limit，把本合同固定 code/limit fixture 编码；不依赖 T18 helper |
| G00 及各 gate | 冻结与文件 scope 协调、中央文档/CHANGELOG、实际合同 hash；各实现 gate 必须同步公共语义后才能放行 |

已由协调者确认的范围修订：T05 需加入 **approval_test.go**（基线 TestApprovalCancelDuringPending，第693行，旧断言“bare cancellation must stay a plain error”）。修订后断言应要求 Result()==nil、errors.As 到 RunError、ReasonCancelled、Result 非 nil且原 Canceled 可 Is；不得删取消后 responder ErrApprovalResolved 断言。另将 **stream_contract_test.go** 加入 T05 scope，更新 TestStreamCancel 第282/310行及 TestStreamInfraError 的“plain error”说明/相关断言，保留 Cancel 幂等、nil Result、Events 关闭与原 cause 匹配。G00 在实施派发前正式更新 T05 scope/manifest/task 校验和。C02 不修改 task 或该测试。其他提及的代码路径仅是后续 owner 落点，本合同不授权越界修改。

跨 C04：只需明确追加提示词的资源物化计入预算、预算本身不入会话 fingerprint；不引入 Driver.Config 的 ActiveExecutionTimeout 镜像。跨 C03：采用 §7 的 token/observer/短锁顺序；root 已确认该协调决定，同批代码不是本合同依赖。

## 11. 必须转成合同测试的验收表

| fixture ID | 场景与关键断言 | 实施/独立证据 |
|---|---|---|
| C02-R01 | fake Driver 返回含 Text/Summary/Raw stdout/stderr/Terminal/Transcript/Usage/Metadata/Services/结构化数据的 Response + 自定义错误，Run/Stream逐字段一致且 errors.As 原类型 | T05，后续W04 verifier |
| C02-R02 | cancel/deadline/approval error/panic/非零退出逐类；已批准后续取消、审批拒绝与 Canceled 并存；原 chain不丢 | T05/T10 |
| C02-R03 | lease renew/Finalize/缺 checkpoint/cleanup失败不丢部分 Result；已有健康 record unchanged；仅健康成功已commit后的 cleanup失败不回滚 | T05/T22 |
| C02-B01 | fake 40ms + Ask300ms + 50ms + 10ms=100ms active；重叠 token A/B、重复释放、旧callback、边界Pause | T10/T22 |
| C02-B02 | zero/positive/sub-ms/negative Policy、call整值清除、WithTimeout/parent deadline、准备耗时、安全fallback与retry不reset | T10/T22 |
| C02-B03 | broker满时 Ask已暂停且审批墙钟仍到期；auto不暂停；retry间隙计时；handlerpanic/取消/超时全部release；同run并发Ask | T10/T22 |
| C02-B04 | Close/parentCancel/leaseRenew在paused时继续；Member不暂停Leader；observer非Ask计时；收尾Stop不超预算 | T10/T22 |
| C02-D01 | Delegate旧Timeout仍墙钟、新请求/上限clamp、BeforeDelegate计时且拒绝无started、每次continuation新预算，recovery不reset | T18/T22 |
| C02-D02 | 已有TaskID在初始化耗尽时也CancelTask；5秒有界且失败仅诊断；旧问卷snapshot+EOF=stream_interrupted | T18/T21/T22 |
| C02-W01 | §9正常/非法闭集fixture，code主因优先、typed Limit来源、ceil/max值、防metadata冒充、缺limit兼容 | T18/T19/T21/T22 |
| C02-W02 | Local/Remote active/approval/cancel/deadline round-trip与部分输出；默认ExposurePolicy无Raw/secret外泄 | T18/T19/T21/T22 |

C02 验收为签名、状态机、fixtures一致性审阅及任务包静态校验，不执行未来实现用例，不把本表当成已通过。平台/race/live gate 依任务图后续执行。

## 12. 依赖选型与明确拒绝的源行为

无需新增顶层 require。标准库 context/sync/time/errors 足够表达有界计时与 cause 链，fake clock 可局部注入并验证 timer race；没有跨协议解析职责或生命周期外包需求，因此本次新增依赖不会显著提高可靠性或降低维护面。私有实现局限 internal/activebudget，协议解析仍分别在 bridge/hosttool。

拒绝 internal 的公开 PausableContext/WithPausableTimeout、单 paused bool、0继承/负数关闭的逐字段 Policy 合并、将 DelegationRequest.Timeout 偷换为主动预算、取消只凭 sent/session ID标记健康checkpoint、用 detached context写中断checkpoint、metadata任意limit直通、依据ctx.Err覆盖审批与已定主因。只采纳部分输出保留、单调timer/generation、Ask暂停及安全code投影的思想，按当前 v1 合同实现。

## R014：已选主因与取消通知的线性化

固定d623b718独立Stop屏障实证：onTimer在controller锁内确认余额耗尽、检查parent仍活动并登记本轮expired后，锁外cancel之前发生的parent取消可抢先锁定标准context.Cause，造成最终本轮active主因及cause丢失。此前child.Done先关闭的fixture不足以覆盖此窗口。

Controller.SelectedCause() error只在同一锁中读取已选cause；nil receiver返回nil，未选择返回nil。它不根据当前parent重算，不修改状态，也不把未触发/尚未结算的余额视为已经选择。已有expireLocked/Cancel选择遵循first-selection，后续通知/Stop/Finish不得覆盖；parent在选择前已经Done仍先选parent真实cause。core/hosttool各自用本次expired对象身份区分本地选择与继承的同型原因，保留原cause而不伪造parent.Err。

本轮controller选择是已有first-terminal合同的确认点，不能由稍后context传播调度改写。core唯一terminal短锁读取SelectedCause与已有主因并保存原cause；锁顺序只允许terminal→controller，controller锁内不得调用core回调、事件或宿主IO，cancel仍在锁外。标准child.Err/Cause仍服从Go context自身规则，SDK不会尝试改写它；公开Reason/终局与保存的Cause则必须保留已确认本轮预算。T18的每次Delegate使用同一规则，不让Leader传入的active cause冒充Member自身超时。

Stop屏障反例、parent先Done、真实本轮先选、pre-Driver与多次Result/终局一致性由T10现有V02/V03范围回归，T22独立验收；没有新公开API、Timer或持久化入口。原d623所有通过和失败证据保留，新源码必须重跑最终必需检查。

C02提交前校核补充：SelectedCause等于本轮expired时，即使child.Err仍nil，也已构成active候选；core不得把它隐藏在contextErr非nil分支中，最终Cause直接保留selected实例。FinishExecution首次结算以已有b.cause先于child.Err/context.Cause，防止后到parent占据标准child cause；已缓存finishErr仍最先返回。SelectedCause不扣时、不读parent、不从Stop合成新原因；parent选择前已Done仍按其Err归因，并非本地预算总是优先。
