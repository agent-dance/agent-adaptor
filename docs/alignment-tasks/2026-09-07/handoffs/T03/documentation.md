# T03 — A2A continuation 与主错误归因

实施归属：B01/T03，W03-R01–R06、T03-AC04；集中合并归属 G01。固定来源 `8e35b123af693b1a922d9d53b879692d04e00795`，当前基线 `93ef44f24e63ce52ad29dce2b54ff28fa0503470`。R002 和 results-and-budget 合同优先于来源实现。

## 公开语义与复现

发送同一 TaskID 的下一轮 answer 后，服务器可以先发送旧完整 Task（例如 `input-required` 和已回答的问题），再发送 working、制品更新、新问题或 completed。以前客户端会在旧 Task 停流，delegator 和 mapper 又将旧状态及问卷作为本轮结果。现在完整 Task 流帧只恢复身份与已有制品；本轮仅由 live Status/Message 或经过历史校验的显式 GetTask recovery 结束。旧 completed/failed 快照同样不能在 EOF 冒充本轮完成。

同步 Send、主动 GetTask 与 polling 的完整 Task 仍是权威查询结果。传输错误后的 recovery 与纯 EOF 分开：GetTask 返回发生变化的终局时可恢复；如果状态相同且两个非空 status message ID 相同，则视为旧结果；不同非空 ID 表示新消息，即使问卷文案相同。只有缺少完整 ID 时才比较 Parts，当前 message 缺失不能证明产生了新问题。continuation 尚未观察到历史快照时，不把无法证明为新问题的 recovery input-required 当成新问卷。真正新 live input-required 始终可交付，旧问题的存盘补查不能覆盖它。

纯流 EOF 没有新 live 终局时，hosttool 返回 `DelegationError.Code == "stream_interrupted"`，保留已收到的制品，并对已知远端任务执行现有的有界取消。客户端仍按其协议接口返回 EOF；应用不能把最后一个历史 Task 的状态自行提升为成功。仅发送完整 Task 的服务端应采用明确 Send/GetTask 轮询模式，不静默猜测流终局。

流中 snapshot、后续 artifact 与 append 内容汇总为最终制品；一旦某 ArtifactID 接收到 live 更新，历史回放和可能滞后的 GetTask 只补其他 ID，不能覆写或重新发布该 ID 的旧内容。新的 live 问题、Message 和 terminal Raw 保留。Local loopback 在终局 Task 旁携带 live Status，使用同一 delegator 规则。

桥与 Local 均先 `errors.As` 提取既有 `*adaptor.RunError`，按其主 `Reason` 判状态并保留允许投影的 partial Result。`errors.Join(&RunError{Reason: ReasonApprovalDenied/ReasonApprovalTimeout, Result: partial}, context.Canceled/DeadlineExceeded)` 仍为 failed；显式 `ReasonCancelled` 为 canceled，并保留 partial artifacts/text。无 RunError 才使用既有 bare context 分支。T03 保留原有 bare deadline 区别：bridge failed，Local canceled；完整 deadline/approval/budget failure code 的闭集往返由 T18/T19 负责，本交付不声称 W13 已完成。

## G01 需要合并的集中段落

### docs/a2a.md — Result and exposure

用以下内容替换该节第一段：

> Successful `Stream.Result()` values produce the completed status and terminal artifacts. When an error contains `*adaptor.RunError`, the bridge reads its authoritative `Reason` before inspecting secondary context causes: explicit `ReasonCancelled` maps to canceled, while approval denial/timeout and other failure reasons map to failed. Allowed partial Result artifacts are emitted before either terminal status. Local delegation applies the same primary-reason rule and retains partial text. Bare errors retain their existing fallback classification. The bridge never treats a non-nil execution error as success.

### docs/a2a.md — Calling a remote A2A agent

用以下内容替换以 `SendStream and Subscribe return ordered protocol events` 开头的段落：

> `SendStream` and `Subscribe` return ordered protocol events. A full Task frame is a historical snapshot: it restores task/context identity and existing artifacts, but its status and question do not end the current execution. Live Status updates (including input-required) and live Message results determine the current outcome. Late events after the first live final event are ignored. After a transport error, one explicit GetTask recovery may supply a terminal event marked `RecoveredState`; an unchanged historical terminal or previously answered question is rejected. A continuation without an observed historical snapshot cannot recover an input-required result whose freshness is unknown. Synchronous Send and GetTask polling continue to treat full Tasks as authoritative query results.

紧接追加：

> For example, `old input-required Task → working → artifact → completed Status` completes with the later artifact; `old question Task → new input-required Status` presents only the new question. A historical input-required/completed/failed Task followed by EOF has no current terminal outcome. Delegation reports `stream_interrupted` and performs its bounded cancellation of the known task; it does not replay the old question. Servers that return only full Tasks should be consumed through explicit Send/GetTask polling. Continuations keep the same remote TaskID while each call consumes only its current live outcomes.

### CHANGELOG.md — 当前未发布修复条目

- Fix A2A continuations ending on historical Task snapshots: retain identity and artifacts, await current live status/message, and reject stale questionnaire recovery or snapshot-only EOF.
- Preserve synchronous/polling Task results and Local/Remote parity by giving Local terminal outcomes an explicit live Status.
- Prefer RunError's primary Reason over joined context causes in the A2A bridge and Local delegation; preserve allowed partial artifacts/text for failed and canceled runs.

## 局部 godoc、公共声明与依赖

已更新 `Client.SendStream`、`Event.Task`/`RecoveredState`、`A2AStream.Recv`、Local finalTask/finalEvent 和私有状态机注释。没有新增公共字段、函数、root/SPI 声明或模式选项；无 golden 变更、无新增顶层 require。只使用既有 A2A 依赖和标准库比较消息身份/Parts；没有新 provider 解析器、调度器或第二条执行管线。

## 不采用的 internal 行为

不采用 `canFinishFromLastTask` 在 EOF 接受旧 completed/failed 的分支，因其会再次结束 continuation。也不无条件接受错误补查返回的旧 input-required，不把“已恢复存盘状态”伪装为本轮新审批。不同传输模式的区别写入现有接口文档，不新增消费者模式选择 API。

## 验证与边界

首先在未修改生产代码的基线上运行 `TestAlignment*` fake/loopback fixtures，取得 47 个失败测试/子测试的可复现证据；随后实施并验证全部三个 A2A 包。协调者预审另补两类负例，并在首次实施提交上复现失败：相同文案但不同正式 MessageID 的新问卷恢复，以及旧 snapshot → live append → 旧 snapshot 重播 / 滞后 GetTask。修复后新增覆盖旧三种终局快照、live 新问卷与真实 HITL payload、EOF、显式恢复、stale GetTask、同 TaskID 三轮 answer、append 制品、同步/polling，以及 bridge/Local 的 joined errors。

交付源码提交后，重新运行 task.json 的完整 `go test -count=1` 与 `go test -count=20` 三包命令，并补同范围 `go test -race -count=1`。实际 SHA、计数、退出码与日志在 result.json 中；日志不加入被测试源码提交，避免 SHA 自引用。执行环境为 macOS，三项 live/E2E/API golden 环境门均为 0。没有执行真实 provider CLI、付费/live 测试、Linux/Windows 发布验证、push 或 tag。
