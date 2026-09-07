# T19 — A2A 安全预算失败投影

任务范围：T19-AC01、T19-AC02、W13-R09。基线为已接受 G03 `926dbbf90a98d35416cdbbc6e376c7bbdc5da084`，实现仅在 bridges/a2a。T18 同批源码、输出与新 helper 均未读取或依赖；新 Server 与新 Delegator 的组合验收仍由 G04/T21/T22 执行，本交付不宣布 G04 或 W13 整体关闭。

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

未知扩展 Reason、旧 SPI decision_rejected/decision_timeout 或不带可识别类别的普通 error 保持 failed、安全通用文本且无提升的 failure 对象。ResultBuilder/制品构建错误使用 infrastructure_error 与静态文本，不透出任意错误正文或 layer 字段。启动前 bare active/cancel/deadline 有安全 code，但没有伪造 RunError 或部分 Result。

主归因先读 RunError.Reason；Cancel/Deadline/同型父预算只是次因。仅主 active 的 carrier.Cause 中第一个 typed `ActiveExecutionTimeoutError` 正 Limit 可输出 limit_ms；不读取外层 Join 中不属于 carrier 的预算，不跳过第一个非法限额去借父预算，不从 Details 猜限额。core 已把本轮选中的预算排在 Cause 前，此 bridge 不重新排序终止原因。

例如 Limit=100ms 的主动超时，即使 Cause 同时含 context.Canceled、父预算777s，控制对象仍为：

```json
{"code":"active_execution_timeout","limit_ms":100}
```

毫秒使用除法和余数向上取整；1ns→1ms，100000001ns→101ms，MaxInt64 ns→9223372036855ms，不执行可能溢出的加法。没有可信正 typed Limit 时省略 limit_ms；未知不是0。数值表示 wire 毫秒精度，接收端纳秒饱和另属 T18。

默认控制对象只含 code 和合法可选 limit_ms。IncludeMetadata 可为已知原因增加经过既有 sanitizer 处理的 metadata 子对象；其中同名 code/limit_ms 不能覆盖顶层控制。即使诊断全开，RunError.Message/Cause.Error 不进入状态文本。此过滤遵守既有敏感键及 inline-secret 规则，不承诺任意自然语言正文 DLP。

所有 RunError 部分制品仍先于终态发送：默认 summary 保留；Raw/Usage/Transcript/provider terminal 各自遵循原开关，已观察全零 Usage 和 JSON terminal 0 保留，nil Usage 仍省略；已转发文本、Append=false/LastChunk=true 不回退。失败时不增加 ResultBuilder/custom artifacts 调用保证。新预算控制不隐式开启 capability/todo/tool/HITL 或任何诊断。

## 中央合并目标（G04）

- `docs/a2a.md` 的错误映射/ExposurePolicy 段：纳入上述八类 code、优先 Reason、typed 限额来源和 Text Part 位置、默认部分制品与文案迁移。
- `docs/public-errors.md` 的跨协议错误段：说明本地 errors.Is/As Cause 保留与远端闭集安全投影不同；无 Limit 不伪造值。
- `docs/run-policy.md` 主动预算段：补100ms wire示例、毫秒ceil和父/本轮预算隔离。
- `CHANGELOG.md` 未发布变更：A2A 增加稳定主动预算 code/limit，安全静态失败正文替代 provider error 明文；保留部分制品和显式诊断开关。
- 局部 `bridges/a2a/doc.go` 和 DiagnosticsPolicy godoc 已同步。没有新公共 Go 声明、root/SPI 变化或 golden 修改。

## 固定来源与依赖选型

源证据只使用 `git -C /Users/blurooo/project/agent-adaptor-internal show e2f0620bdd6477e6fe16f6db5648093589342ca2 -- pkg/bridges/a2a/mapping.go pkg/bridges/a2a/server.go`。不移植 internal 的任意 metadata limit 直通、layer、错误正文回显、context-first 或 SDK/Start/RunResult.Failure 旧架构，也不改变 checkpoint/Policy/计时/重试。

无新增顶层 require：标准库 errors/time 完成已冻结的类型选择与整数投影，现有官方 A2A binding 提供 JSON-RPC/HTTP；没有新增 IPC/parser 需求，另加依赖不会显著改善可靠性。改动局限于 bridge 私有 mapper。

## 验证边界

外部包测试使用真实 NewServer.Handler、httptest loopback、既有 clients/a2a.Send/SendStream/GetTask，fake Runner 的 Run 被计数且必须零次、Stream 每请求恰一次。安全 Result 由公开 adaptor.New(fake Driver) 的 Response→Result 管线构造。预期值字面独立书写，既有四份 T11 canonical fixture 追加预算终态仍保真。实际 HTTP binding 响应正文由不缓冲流的读取 tee 留在 `TestAlignmentBudgetWireCapturedHTTP` 日志。

最终 SHA 必需运行 T19-V01 全 bridge、T19-V02 原 TestAlignment 20次；另跑客户端 W03 全回归及 bridge race。实际命令、SHA、计数、退出码、skip和日志只由提交后的 evidence/result 提供。运行平台为本地 macOS；沙箱禁止 loopback bind 的环境失败单独保留，使用允许的本机测试权限复跑。未运行 Linux/Windows、provider CLI、付费/live、profile探测、发布或推送。
