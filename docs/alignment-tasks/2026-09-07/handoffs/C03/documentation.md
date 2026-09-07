# C03 文档集成片段

合同路径：`docs/alignment-tasks/2026-09-07/contracts/events-observation.md`。本片段交 G00 合入中央文档；C03 只交合同，没有生产代码实现或 live 结果。

## 公开语义变化

一次运行仍只有 Run/Stream 和一条 typed Event 流。新增 CapabilityInvocation 与 TodoUpdated；前者报告正式识别的 capability 调用事实，后者报告某 scope 的全量有序计划快照。没有事件表示未观察到，不表示从未调用。TodoUpdated 的空 Items 表示清空，Todo 不等于审批 PlanReview。

ToolCall、ToolResult 与 TranscriptItem 新增 ScopeID、ParentScopeID、ParentToolCallID；父引用要结合 run 和 scope，不能仅用裸工具 ID。完整 Args 快照不再重复映成相同 ArgsDelta，ToolCall end 不证明工具执行成功。

RunAttachment 增加 Observation、Observer、BindEvents，配套受限 RunEventInfo/RunEventObserver/RunEventPublisher 和真实 ObservationDemand。Observer 仅看 CapabilityInvocation/TodoUpdated 两种规范化事实，在同一 sink 排序后、用户/EventBus背压前接收。每调用100ms上限，首次错误/panic/timeout关闭本run观察并发一次安全notice，不改运行错误、输出、checkpoint或审批。Store由宿主关闭。

A2A增加两个显式默认关闭的ExposurePolicy字段：IncludeCapabilityInvocations、IncludeTodos。IncludeToolCalls/Diagnostics不隐式开启。新kind使用现有adapter.stream.v1，整个envelope最大65536bytes，闭集字段与安全整数范围双侧验证；不可映射或超限显式dropped。新client读旧wire，旧client对新kind明确unsupported。默认过滤不回显内部key/source/count。

## 可复现使用例（由对应实施任务加入可运行测试）

```go
store := capabilityrecorder.NewMemoryStore()
rec, err := capabilityrecorder.New(capabilityrecorder.Config{Store: store})
if err != nil { return err }
agent := adaptor.New(configuredDriver, rec.Option())
stream := agent.Stream(ctx, "完成任务")
for event := range stream.Events() {
    switch e := event.(type) {
    case adaptor.CapabilityInvocation:
        renderCapability(e.Invocation)
    case adaptor.TodoUpdated:
        replaceScope(e.Meta().RunID, e.Snapshot.ScopeID, e.Snapshot.Items)
    }
}
_, err = stream.Result()
```

查询以实际identity ID/Tenant/Profile和RunID四维精确选择，AfterSequence为独占分页游标。Args、Raw、tool result、URL/header/env不进入capability记录。若需要完整typed事件审计，仍显式使用sessionrecorder及其持久backend，不能假称capabilityrecorder完整。

## 必须单列的 breaking 行为与迁移

既存 `subagentstream.Merge` 对父Event重新编号与唯一Sequence合同冲突。保留签名后只透明转发：EventBus有成功绑定本run的单调RunEventsBound证明则返回父结果；否则通过既存RunError/Cause返回ErrEventInjectionUnsupported，完整或partial Result放RunError.Result。父已失败时保持其Reason与cause，不伪造另一个provider终局。

迁移方式是在执行前装 `team.Option()`（delegation.Service.Option），随后直接消费原始Stream。不要在执行后用bus给同run追加第二套序号。T12以窄结构接口和fake验证；T18再实现EventBus.RunEventsBound，避免跨批提前依赖Go符号。G00已确认此最小裁决，生产修复归T12/T18。

## 合并目标

| 文件/段落 | 应并入内容 |
|---|---|
| `docs/api-reference.md`：Event、runtime扩展 | 两Event、父/scope、ObservationDemand与闭集observer；没有新增执行名/查询面 |
| `docs/streaming.md`：工具生命周期、背压、序号 | snapshot/delta互斥；parent full key；关键Todo/capability；取消最终Dropped+RunFinished两个保留槽 |
| `docs/streaming-adapter-contract.md` 与 `driver/doc.go` | 两StreamKind/payload字段、真实transport支持矩阵、正式解析职责、started/terminal收尾 |
| `docs/a2a.md`：exposure、adapter.stream.v1、delegation | 新DTO/64KiB/安全整数边界；默认关闭；无Store relay；before成功→started→I/O；tuple域与Source链 |
| `docs/run-policy.md`：人工等待与观测 | Observer不参与审批、不拥有budget；C02 token保持唯一 |
| `docs/structured-output.md`：协商 | 单一resolver先保留可交付观测transport，再在同transport按原生→prompt校验；不按Run/Stream分流 |
| `README.md`、`docs/README.md`：可选宿主组件与文档地图 | capabilityrecorder是可选hosttool，查询不进入Agent/Inspect；真实支持矩阵链接 |
| `CHANGELOG.md`：本批contract和后续实施批次 | Event与扩展点按公共API变更记录；Merge行为修复单列breaking；不得宣称四Driver所有transport均支持todo |

## 新公共声明与 golden 理由

Root新增两个sealed Event及其字段、Tool/Result父scope字段、EventSourceMeta来源链、ObservationDemand、RunEventInfo、RunEventObserver、RunEventPublisher和RunAttachment三个字段。根With*数量不变，不新增第三执行动词。Driver对应payload/Transcript字段、两个StreamKind、ObservationSupport/Capabilities/Demand、Descriptor.Observation与Request.Observation同步SPI golden。

叶包capability/todo拥有全部值词汇；hosttools/capabilityrecorder拥有Store/Query/Option。A2A公开DTO是版本化wire唯一允许的V1名称。AGUI/SSE/sessionrecorder用自己的私有序列化映射，不import同批T11新增符号。T06只精确更新受影响golden，禁止全局自动更新掩盖漂移。

## 明确拒绝的 internal 行为

不复制root Admin/旧SDK/多个输出channel；不在共享tracker/table解析Claude JSON或MCP名字；不把TaskCreate局部序号当TaskUpdate真实ID；不在参数完成时把失败任务说成成功；不丢合法空快照；不凭prompt/skill catalog回显声称调用；不将所有开放能力在run成功时统一Completed；不默认公开远端capability；不把Args快照再补为同样ArgsDelta。

## 验证边界

C03只运行task.validation的Python任务包校验和本合同静态review，未运行生产Go测试、provider CLI、付费live、Linux/race或Windows。四Driver矩阵是后续fixture/live待证明的允许接入范围，不是C03实测能力宣称。各W项的实现与独立验收继续归T06/T11–T18/B05/B06。
