# C02 公共语义文档片段

中央所有者 G00。此片段须在 B00 放行前作为设计合同合并；实现用户文档中的“已支持”表述由相应实现 gate 在代码验收后同步，不能把合同冻结当成实现交付。合同与可复制 fixture 分别为 contracts/results-and-budget.md 与 handoffs/C02/fixtures/budget-wire.json。

## 前后变化与复现

基线 `finalizeRun` 会在 bare context/transport 错误上丢弃已经解析的 Result；审批/provider failure 又会丢失同时返回的 cause。冻结方案复用 RunError，加入 Cause，保留 Unwrap() error，并把执行后的取消、deadline、基础设施、lease/Finalize/cleanup 错误也映射为携带完整或部分 Result 的 RunError。仍为成功 Result,nil 和失败 nil,error；启动前 Config/Policy/schema/capability/resource 错误不伪装成业务 RunError。详情见合同 §2–4。

建议替换 docs/api-reference.md §8“Result and errors”中的仅业务失败表述，以及 errors.go 的文件级注释、FailureReason/RunError/ReasonCancelled/Result/Unwrap godoc、agent.go Run godoc、docs/streaming.md 与 README 的错误分类说明：

> When execution has reached Driver.Run, any failed outcome retains the available Result in RunError.Result. Run and Stream.Result still return nil, error on failure. RunError.Reason is the primary classification; Cause preserves original errors for errors.Is/As, including secondary errors caused by cancellation or cleanup. Pre-execution configuration and resource errors keep their existing wrapped identities. A carried Result may be partial and is not evidence of a healthy checkpoint.

复现：fake Driver 构造带 Text、Raw stdout/stderr/Terminal、Transcript、Usage、Services 与校验过结构化数据的 Response，再返回自定义 transport error 或 ctx.Err。修复后 `errors.As(err,&re)` 得到这些原字段，`errors.Is(err,cause)` 成立，普通返回的 Result 仍 nil；Run 与 Stream.Result逐字段相同。现有健康 Thread record不变。审批 Abort 后再收到 Canceled 时 Reason 仍为 approval_denied/approval_timeout。

在 docs/run-policy.md“Policy value and replacement rule”及 Policy godoc追加字段 `ActiveExecutionTimeout time.Duration`：0 无主动预算，正限额，负值执行前 ErrInvalidPolicy；call WithPolicy 仍整值替换。新增小节“Active execution budget and wall-clock deadlines”：准备资源起计时，只有当前 run 的 Ask attempt 在入队/handler前暂停，重叠等待最后一个token释放后恢复；自动批准/拒绝、重试间处理不暂停；父 deadline和WithTimeout都是绝对墙钟。详情见合同 §5–7。

```go
agent := adaptor.New(d, adaptor.WithPolicy(adaptor.Policy{
    ActiveExecutionTimeout: 100 * time.Millisecond,
}))
// 40ms执行 + 300ms Ask + 50ms执行还剩10ms；再执行10ms才耗尽。
// 下面的call清除主动预算，同时整体替换其他Policy维度。
_, err := agent.Run(ctx, prompt, adaptor.WithPolicy(adaptor.Policy{}))
_ = err
```

Approval godoc同时说明：审批 Timeout独立墙钟；父 deadline不会冒充审批timeout；Cancel/Close/lease续期不受pause影响；不公开通用 PausableContext。预算不是provider会话环境，不进入Thread fingerprint。WithTimeout、Thread默认常驻、WithSpawn语义不变。

在 docs/a2a.md 的失败映射、ExposurePolicy和delegation策略段增加：保留旧 Timeout/MaxTimeout墙钟含义；新增 DelegationRequest.ActiveExecutionTimeout 和 DelegationPolicy.MaxActiveExecutionTimeout。每次Delegate/continuation新预算，同次retry/recovery不重置。Member的Ask不自动暂停Leader或外层delegation；input-required结束本次调用，宿主答复时间在调用间。已知入参TaskID时初始化超时也尽力CancelTask，5秒detached上限，失败仅诊断。详情见合同 §8。

A2A failure仍位于Text Part.Metadata的agentadaptor.failure。控制字段为code和可选limit_ms；新code active_execution_timeout，其他root原因保持approval_denied、approval_timeout、cancelled，并新增deadline_exceeded/infrastructure_error的明确分类。limit_ms从typed Limit向上取整到毫秒，安全整数上限9223372036855；纳秒本地Limit不变，wire仅保证毫秒投影。普通Details/Raw仍受ExposurePolicy，不能以“限额可公开”为理由放行全部metadata。T18/T19先读取主Reason再匹配context次因。详情见合同 §9及机器fixture。

## 公共声明与 golden 理由

T05 root golden：RunError.Cause error、ReasonInfrastructure、ReasonDeadlineExceeded；RunError.Unwrap签名不变，已执行bare cancellation改为ReasonCancelled且携带Result。T10 root golden：Policy.ActiveExecutionTimeout、ReasonActiveExecutionTimeout、ErrActiveExecutionTimeout、ActiveExecutionTimeoutError及Error/Unwrap。T10 Driver golden：FailureActiveExecutionTimeout（在driver/run.go追加，避免越过ownership）。没有新With*名、执行动词、Result.Failure或公共clock框架。

T18 hosttool godoc：两种独立主动预算字段、DelegationError.Cause和Unwrap（Cause不序列化），不改已有DelegationResult/Error返回约定。公共声明变更需实现owner精确审查AST后更新golden；本合同不修改生产golden。

G00已获协调者确认需扩展T05 scope：approval_test.go的TestApprovalCancelDuringPending（第693行旧“非RunError”断言），stream_contract_test.go的TestStreamCancel（第282/310行及TestStreamInfraError说明）。更新为nil返回Result、RunError携带非nil部分Result与原cause，保留Events关闭、Cancel幂等和已取消Approval responder失效断言。process_outcome_test.go与runerror_test.go同类断言已在T05范围。

## CHANGELOG 与审计落点

G00在Unreleased设计记录明确W04/W13合同冻结与重新打开的AGENTS §14错误路径Result审计。G01验收后记录“执行后的取消/基础设施失败现在可通过RunError.Result访问部分结果，原cause仍可Is/As；启动前错误不变”。G03记录Policy主动预算和error新增公共字段/声明，以minor公共语义变化审查；G04记录A2A/delegation独立预算与安全failure wire。不得在B00即写“实现已完成”，也不得等到T24才补前批语义变化。

## 不采用的 internal 行为与原因

不采用126d610以sent/session ID标记取消checkpoint有效及detached持久化；它们没有正式健康终局证明。继续保留invocationCanPersist、单writer、lease owner/token与atomic Finalize；仅成功提交后cleanup失败可能携带已持久化健康结果。

不采用e2f0620公开PausableContext、单paused bool、Policy逐字段0继承/负值关闭、将旧delegation Timeout改义、任意metadata limit透出、按ctx.Err覆盖具体审批原因。当前合同行为更严格。source工作区未提交vision内容未读取。无新增顶层Go依赖：标准库计时与错误链可局部实现，fake clock/generation测试覆盖竞态。

## 验证边界

C02只提交合同、fixture与本片段；在实际提交head上执行task规定的 `python3 docs/alignment-tasks/2026-09-07/validate.py`，附具体签名/状态机/fixture一致性审阅记录。执行时显式关闭AGENT_ADAPTOR_LIVE_CONFORMANCE、AGENT_ADAPTOR_E2E、AGENT_ADAPTOR_UPDATE_API_GOLDEN。

本地为macOS arm64，Python3.14.4，Go1.26.5。C02没有运行未来Go实现测试、Linux/Windows/race/fuzz或真实provider live；这些由后续任务持有，不能用合同fixture或internal历史测试冒充功能/发布证据。报告记录实际SHA与命令日志，result/evidence在提交后生成并由派发器收集，不提交到其引用的代码SHA。
