# T19 — A2A 安全预算失败投影

任务范围：T19-AC01、T19-AC02、T19-AC03、W13-R09。基线为已接受 G03 `926dbbf90a98d35416cdbbc6e376c7bbdc5da084`，实现仅在 bridges/a2a。T18 同批源码、输出与新 helper 均未读取或依赖；新 Server 与新 Delegator 的组合验收仍由 G04/T21/T22 执行，本交付不宣布 G04 或 W13 整体关闭。

## 公开语义和例子

以前 A2A failure 直接输出 RunError.Message/Error、任意 Reason，bare-error 分支还附带 layer。现在 failure 状态文本使用安全类别文案，仅已知的八类主因开放 `Text Part.Metadata["agentadaptor.failure"]`。该对象不是 DataPart、Message/Task 顶层 metadata，也不是 adapter.stream.v1 新 kind。

| Reason/code | 状态 | 静态文本 |
|---|---|---|
| active_execution_timeout | failed | active execution budget exhausted |
| approval_denied | failed | approval denied |
| approval_timeout | failed | approval timed out |
| cancelled | canceled | task cancelled |
| deadline_exceeded | failed | execution deadline exceeded |
| agent_error | failed | agent run failed |
| policy_violation | failed | execution policy violated |
| infrastructure_error | failed | execution infrastructure failed |

未知或空 carrier Reason、旧 SPI decision_rejected/decision_timeout，或没有合格 R016 提示且不带可识别类别的普通 error 保持 failed、安全通用文本且无提升的 failure 对象。ResultBuilder/制品构建错误使用 infrastructure_error 与静态文本，不透出任意错误正文或 layer 字段。启动前 bare active/cancel/deadline 有安全 code，但没有伪造 RunError 或部分 Result。

主归因先读 RunError.Reason；Cancel/Deadline/同型父预算只是次因。仅主 active 的 carrier.Cause 中第一个 typed `ActiveExecutionTimeoutError` 正 Limit 可输出 limit_ms；不读取外层 Join 中不属于 carrier 的预算，不跳过第一个非法限额去借父预算，不从 Details 猜限额。core 已把本轮选中的预算排在 Cause 前，此 bridge 不重新排序终止原因。

例如 Limit=100ms 的主动超时，即使 Cause 同时含 context.Canceled、父预算777s，控制对象仍为：

```json
{"code":"active_execution_timeout","limit_ms":100}
```

毫秒使用除法和余数向上取整；1ns→1ms，100000001ns→101ms，MaxInt64 ns→9223372036855ms，不执行可能溢出的加法。没有可信正 typed Limit 时省略 limit_ms；未知不是0。数值表示 wire 毫秒精度，接收端纳秒饱和另属 T18。

默认控制对象只含 code 和合法可选 limit_ms。IncludeMetadata 可为已知原因增加经过既有 sanitizer 处理的 metadata 子对象；其中同名 code/limit_ms 不能覆盖顶层控制。即使诊断全开，RunError.Message/Cause.Error 不进入状态文本。此过滤遵守既有敏感键及 inline-secret 规则，不承诺任意自然语言正文 DLP。

所有 RunError 部分制品仍先于终态发送：默认 summary 保留；Raw/Usage/Transcript/provider terminal 各自遵循原开关，已观察全零 Usage 和 JSON terminal 0 保留，nil Usage 仍省略；已转发文本、Append=false/LastChunk=true 不回退。失败时不增加 ResultBuilder/custom artifacts 调用保证。新预算控制不隐式开启 capability/todo/tool/HITL 或任何诊断。

## R016 同 Stream 的 bare error 分类提示

Attempt 1 的 `bbd357485ecb2ec8303530687d3fc882ce537ab6` 按 revision16 原有限验收完成，原报告/29 artifacts 与独立288项通过记录保留。R016 是 revision17 新增的跨层分类合同，不把旧未覆盖组合倒称原作者违背旧表；G03 基线未重开。

真实 pre-Driver RunService 中 parent CancelCause(ActiveExecutionTimeoutError{Limit:777s}) 先发生且本轮预算未耗尽，G03 的 Result 返回普通错误（保留父 Is/As，nil Result、无 RunError、Driver0），同一次流的最后 RunFinished 明确为 cancelled。旧 bare Is(active) 优先错误投影为 active_execution_timeout/777000。Attempt 2 仅在本桥内部保存同一 Stream 的最终分类证据，将该例恢复为 canceled/cancelled 且不输出限额。

失败与否仍只看同一次已关闭 Stream.Result() 的 error：nil error 忽略失败事件；非 nil RunError 始终按其 Reason，即使 carrier Reason 未知/为空，提示也不能改写。只有 bare error 可使用合格提示；不存在或不合格时完整保留原 bare fallback，包括第三方 typed active + Canceled 仍为 active。

合格提示须同时满足：Stream 返回即取得的 RunID 非空；终局 Meta.RunID 非空且逐字相等；typed RunFinished（值/非nil指针均可）Failed=true 且 Reason 在八项闭集中；整流只有一个 RunFinished，并且它是最后一个 Event，随后完整 drain 至关闭。正文 provider RunID 可不同/为空；ThreadID、Source、Message、时间与 Usage 均不参与分类或转发。相同重复终局、nil终局指针、归属错误、Failed=false、空/未知Reason及尾随任何 Event 都使提示无效；尚未关闭不得采用提示。

私有收集器 O(1)，计数饱和为2，只保存固定流ID、闭集Reason和资格位。同一消费goroutine在正常/Cancel drain/翻译错误 drain 中统一收集，投影前记录，关闭后查询；不增加等待channel、外部IO、重试或第二terminal发射。翻译错误与ResultBuilder错误仍保有原有独立桥错误路径。

提示为 active 时才可从原 bare error 的第一个可信正 typed 主预算取限额；已接受本轮100ms先于父777s的形状仍输出100。无typed省略，cancel/deadline/approval提示即使保留父active原因也无limit。原error/cause/Result均不替换或构造，正常结果不因hintFailed变失败。

R016-01至09有限矩阵均使用 TestAlignment 前缀纳入原V01全包和V02 count20。公共HTTP测试使用实际G03 RunService及正式core输出审计，再验证Send/SendStream/GetTask与唯一Stream；Cancel-drain测试在Stream返回前先触发runCtx.Done，并将终局扣在Cancel屏障之后，强制覆盖原空for-range路径。提示尚未关闭、翻译错误drain及ResultBuilder独立错误也有定点对照。新增测试不改写原C02安全wirefixture，不读取T18同批实现。

## 中央合并目标（G04）

- `docs/a2a.md` 的错误映射/ExposurePolicy 段：纳入上述八类 code、carrier优先、R016唯一末尾已关闭的Meta.RunID分类提示及无效fallback、typed 限额来源和 Text Part 位置、默认部分制品与文案迁移。
- `docs/public-errors.md` 的跨协议错误段：说明本地 errors.Is/As Cause 保留与远端闭集安全投影不同；无 Limit 不伪造值。
- `docs/run-policy.md` 主动预算段：补100ms wire示例、毫秒ceil和父/本轮预算隔离。
- `CHANGELOG.md` 未发布变更：A2A 增加稳定主动预算 code/limit，安全静态失败正文替代 provider error 明文；保留部分制品和显式诊断开关。
- 局部 `bridges/a2a/doc.go` 和 DiagnosticsPolicy godoc 已同步。没有新公共 Go 声明、root/SPI 变化或 golden 修改。

## 固定来源与依赖选型

源证据只使用 `git -C /Users/blurooo/project/agent-adaptor-internal show e2f0620bdd6477e6fe16f6db5648093589342ca2 -- pkg/bridges/a2a/mapping.go pkg/bridges/a2a/server.go`。不移植 internal 的任意 metadata limit 直通、layer、错误正文回显、context-first 或 SDK/Start/RunResult.Failure 旧架构，也不改变 checkpoint/Policy/计时/重试。

无新增顶层 require：标准库 errors/time 完成已冻结的类型选择与整数投影，现有官方 A2A binding 提供 JSON-RPC/HTTP；没有新增 IPC/parser 需求，另加依赖不会显著改善可靠性。改动局限于 bridge 私有 mapper。

## 验证边界

外部包测试使用真实 NewServer.Handler、httptest loopback、既有 clients/a2a.Send/SendStream/GetTask，fake Runner 的 Run 被计数且必须零次、Stream 每请求恰一次。安全 Result 由公开 adaptor.New(fake Driver) 的 Response→Result 管线构造。预期值字面独立书写，既有四份 T11 canonical fixture 追加预算终态仍保真。实际 HTTP binding 响应正文由不缓冲流的读取 tee 留在 `TestAlignmentBudgetWireCapturedHTTP` 日志。

最终 SHA 必需运行 T19-V01 全 bridge、T19-V02 原 TestAlignment 20次；另跑客户端 W03 全回归及原预算wire和R016的 bridge race。实际命令、SHA、计数、退出码、skip和日志只由提交后的 evidence/result 提供。运行平台为本地 macOS；沙箱禁止 loopback bind 的环境失败单独保留，使用允许的本机测试权限复跑。未运行 Linux/Windows、provider CLI、付费/live、profile探测、发布或推送。
