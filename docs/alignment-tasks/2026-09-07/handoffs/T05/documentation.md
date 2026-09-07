# T05 — 执行失败的部分 Result 与 cause

本片段由 G01 在本批放行前合并到集中使用文档、CHANGELOG 和必要的权威说明。源码基线为 G00 `93ef44f24e63ce52ad29dce2b54ff28fa0503470`；Source-Internal-Commits: `126d610dfb6afd2cca19661ab0a7c0b6f626b490`；工作项 W04-R01–R04。本任务不代表 provider 常驻路径、整个 W04 或最终发布已验收。

## 公开行为与例子

以前，已分类业务失败用 `RunError.Result` 保留结果，但执行后的普通取消、transport、lease、Finalize、缺失 checkpoint 和纯 cleanup 错误可能丢失已有结果；已分类失败同时返回的原 error 也不可再用 `errors.Is/As` 检查。

现在，以唯一 `Driver.Run` 被进入为界，后续失败均返回 `nil, *RunError`。其 `Result` 非 nil，包含已观察到的 Text、Summary、Raw stdout/stderr/正式 terminal payload、Transcript、Usage、Model、Provider、Metadata、实际 Services 和已校验的结构化数据。`Reason` 是主原因，`Cause` 保留原始与次要错误，支持标准 `errors.Is/As`；没有新增结果获取入口或 Result.Failure。

```go
result, err := agent.Run(ctx, "work")
if err != nil {
    // result == nil。错误是唯一失败判定面。
    var runErr *adaptor.RunError
    if errors.As(err, &runErr) {
        partial := runErr.Result
        raw := partial.Raw()
        _ = raw
        switch runErr.Reason {
        case adaptor.ReasonCancelled:
            // errors.Is(err, context.Canceled) 保留实际观察到的取消。
        case adaptor.ReasonDeadlineExceeded:
            // 匹配 context.DeadlineExceeded。
        case adaptor.ReasonInfrastructure:
            // errors.Is/As 可到达原 transport/store/cleanup 类型。
        }
    }
    return err
}
_ = result.Text
```

审批拒绝或超时、正式 provider failure 的主原因优先于同时观察到的 generic cancellation。`Cause` 可以同时匹配取消或清理错误；bridge、hosttool 应先读 `RunError.Reason`，不能用 `errors.Is` 的检查顺序猜主原因。T05 保持 B01 的 pending approval → Response.Failure → 已知协调错误或原 error 规则；主动预算和 first-terminal 状态机由后续任务实现。

静态配置、能力、资源准备、Thread acquire 等发生在 Driver 进入前的失败，保留原包装错误，不伪造 RunError/Result。已经准入的初次 Driver 调用仍遵循原取消交付语义；它实际被进入后返回取消即携带 Result。Agent.Close 后的新调用继续返回 ErrAgentClosed。

结构化输出在中断时不重新校验以覆盖原 cause。Driver 已校验数据仍可 Decode；已请求 schema 但尚无校验数据时 Decode 明确失败，不能落入无 schema 的 Text JSON 便利解码。未观察 Usage 和观察到零值的区别不变。

## Thread 与资源回收

所有 Driver 进入后的出口，包括 PrepareFresh、lease renewal、Finalize、缺失 checkpoint，在统一收尾中映射同一 Response。Run 仍是 Stream + drain + Result。Events 关闭后 Result/error 不再被修改，多次并发 Result 调用返回同一对象。

安全 resume rejection 至多触发原合同允许的一次 fresh fallback，保持同一 RunID。每次重入前确认 context 未取消；取消后不再启动。Raw stdout/stderr 和 Transcript 按尝试顺序保留，Usage 合计已观察值，Services 按 ID 合并，Metadata 后近覆盖。Raw.Terminal 保留最近一个正式 terminal；此前终局原始字节仍在完整 stdout 和正式 Transcript 中。Text/Summary/Model/Provider/结构化数据和 checkpoint 来自当前尝试，不把此前拒绝的执行健康状态带到 replacement。

`invocationCanPersist` 的全部健康条件与唯一原子 Persist 位置保留。取消、审批失败、非零退出、协议失败、缺失健康 checkpoint、失去 lease 都不会替换旧健康 record。首次取消不会创建可续 checkpoint，下一轮按原合同新建。没有将仅存在 resume ID 当作健康证据，也没有用 detached context 保存中断 checkpoint。

**清理失败可能发生在健康 checkpoint 已经原子提交之后。** 此时返回 ReasonInfrastructure、完整 Result 和 cleanup cause，已成功提交的状态保留，不回滚、不重复提交。已有失败再遇清理错误时只追加 Cause，不覆盖主 Reason。detach/release 继续使用既有有界、脱离取消的清理 context。

## 合并目标

- `docs/public-errors.md`：执行前/后的错误矩阵、新 reason、Cause 与主归因优先级、cleanup 已提交例外。
- `docs/api-reference.md`：Result / RunError / Run 和 Stream.Result 的统一部分结果获取示例与新字段。
- `docs/streaming.md`：Cancel 后 RunError.Result、原 cause、Events 关闭后的不可变结果；保持既有关闭、背压与 responder 合同。
- `docs/run-policy.md`：approval primary reason 可以同时带 context.Canceled 次因；不提前宣称 active budget 已实现。
- `docs/structured-output.md`：中断不重新校验，只有已校验数据可 Decode；缺失数据不能便利解码未验证 Text。
- `docs/a2a.md`：与 T03 同批记录主 Reason 优先和允许暴露的部分输出。
- `CHANGELOG.md`：新增 Cause、两种 reason；修复执行后部分结果/原 cause 丢失、fallback 审计、cleanup 已提交语义。公共字段/常量按 additive API 变化记录。
- `AGENTS.md` §8 与 §14.1：按本批 gate 的实际边界说明公共错误 carrier 已实现，provider 路径与独立跨层验收仍由后续任务交付。

局部 godoc 已更新 `errors.go`、`result.go`。`Stream.Result` 的 godoc 由协调者分配给同批 T31，不在 T05 修改。

## 公共声明与 golden 审阅

只新增三个冻结增量：`RunError.Cause error`、`ReasonInfrastructure FailureReason = "infrastructure_error"`、`ReasonDeadlineExceeded FailureReason = "deadline_exceeded"`。保留所有既有字段、方法签名和 reason；`Unwrap() error` 内部 join sentinel 与 Cause，nil/零值不 panic。root AST 失败输出精确只列以上三个增量，人工逐条更新 golden；所有验证中 `AGENT_ADAPTOR_UPDATE_API_GOLDEN=0`，未全局重生成。

无需新增 require；标准库 errors/context 已能完整保留类型与有界 cleanup，变更局限统一 invocation 和公共错误层。

## 验证与明确边界

`TestAlignmentPartialResult*` 覆盖 Agent/Thread × Run/Stream 的逐字段错误结果、typed cause、cancel/deadline、审批拒绝/超时/handler error/panic、已批准后取消、协议/非零、旧健康 record、首次取消、lease/Finalize/缺失 checkpoint、cleanup 已提交、safe fallback 与取消、实际 Services、schema 中断和并发 Result。保留了原审批取消后的 ErrApprovalResolved、Cancel 幂等与 Events 关闭断言。

必需命令为 `go test -count=1 .` 与 `go test -race -count=5 . -run TestAlignmentPartialResult`；提交后实际 SHA、测试/子测试数量、日志与环境见同目录 result.json/evidence。环境为 macOS arm64、Go 1.26.5，live/E2E/API golden 更新门均为 0；未运行真实 provider、Linux/Windows、全仓发布门禁或付费调用。原 sandbox 禁止 loopback，根包的 WithTools fixture 使用会话已授权的本地权限复跑。

发现并交协调者跟踪的**基线已有**边界：Driver-only envelope 会立即发布 provider RunFinished；之后 cleanup 失败的最终 Go error 无法改写该事件。`evidence/lifecycle_repro.go` 及基线 overlay/当前日志可复现：两边都是 success terminal 后返回 cleanup error；T05 当前额外保留部分 Result。根包 merged lifecycle 已用本次 carrier 主归因并有测试，不能据此声称全局 terminal 唯一权威已实现。协调者通过 **R006 / W09-R14** 明确由 B02 T06 统一 Driver-only envelope，T20/T21 独立验证，再由 T10 接入预算终局归因；T05 不修改 sink.go 或 Event 管线，不将该缺口记作由 T05 关闭。
