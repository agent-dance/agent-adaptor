# T21 45 项独立覆盖映射

所有测试名均加 `TestAlignmentProtocol` 前缀。此表描述断言归属，不预填通过；实际结果只取同一 HEAD 完整 V01/V02 JSON。旧基线 W09-R07 的 F01/F02 与 W05-R05 的 F03 已由原 owner 修复并经 root 在 replacement G04 不变复验，详见 findings.md；T21 最终完整检查仍保留全部原断言。已有 owner/G04 证据不计入新测试数。

| Requirement | 新增独立入口 | 核验内容 |
|---|---|---|
| W03-R01 | ContinuationArtifacts | 3 种历史快照 × completion/question/EOF/recovery/stale recovery；不发旧问卷，EOF 为 stream_interrupted。 |
| W03-R02 | ContinuationArtifacts | 3 种历史快照 × completion/question/EOF/recovery/stale recovery；不发旧问卷，EOF 为 stream_interrupted。 |
| W03-R03 | ContinuationArtifacts | 3 种历史快照 × completion/question/EOF/recovery/stale recovery；不发旧问卷，EOF 为 stream_interrupted。 |
| W03-R04 | ContinuationArtifacts | 3 种历史快照 × completion/question/EOF/recovery/stale recovery；不发旧问卷，EOF 为 stream_interrupted。 |
| W03-R05 | ContinuationArtifacts / ArtifactRecoveryReconciliation | 真实 Send+poll/GetTask 与流历史分开，recovery 旧状态不能回滚本轮事实。 |
| W03-R06 | ContinuationRoundsAndClones | 同 TaskID 三轮，新的问卷与制品持续到达，正常路径不 CancelTask。 |
| W05-R01 | FormalClaude / PartialWrappers(claude) | 兄弟 scope 相同工具 ID、完整 wrapper 重放、裸结果歧义、缺 ID/未知 parent、partial+wrapper、异常关闭、Transcript 父关联。 |
| W05-R02 | FormalClaude / PartialWrappers(claude) | 兄弟 scope 相同工具 ID、完整 wrapper 重放、裸结果歧义、缺 ID/未知 parent、partial+wrapper、异常关闭、Transcript 父关联。 |
| W05-R03 | FormalClaude / PartialWrappers(claude) | 兄弟 scope 相同工具 ID、完整 wrapper 重放、裸结果歧义、缺 ID/未知 parent、partial+wrapper、异常关闭、Transcript 父关联。 |
| W05-R04 | FormalClaude / InboundNegativeRelay | 正式父关系经过真实 A2A DTO，unknown kind 通过真实 remote mapper 发 dropped。 |
| W05-R05 | FormalClaude / ServiceRelay | 同一真实事件回放 SSE、AG-UI，实际 Merge 与重开 JSONL 比较父关联；AG-UI 同步保留输出快照与异步序列化均核验，保留 F03 原不变反例。 |
| W05-R06 | ServiceRelay | 真实 Local/Remote/nested-Remote Service 保留 caller 与实际父工具 scope。 |
| W08-R01 | ContinuationArtifacts | 实时多 Part 类型、替换/Append/LastChunk 与最终有序聚合独立断言。 |
| W08-R02 | ContinuationArtifacts | 实时多 Part 类型、替换/Append/LastChunk 与最终有序聚合独立断言。 |
| W08-R03 | ContinuationRoundsAndClones | 修改结果、实时 event、Service Result/Results/Delegations 后，EventBus replay 与各副本不受污染。 |
| W08-R04 | ContinuationArtifacts / ArtifactRecoveryReconciliation | 最终不重复累计；完整 query 的 extension/lagging/conflict 与历史回放分别处理。 |
| W08-R05 | ContinuationArtifacts | opt-out 不带完整 Parts；opt-in 保留；MaxArtifactBytes 安全拒绝与 dropped。 |
| W09-R01 | NeutralVocabulary / WireValidation | public 叶包与 SPI typed 值可构造；静态依赖边界和 closed wire 枚举/字段反向验证。 |
| W09-R02 | ObserverIsolationAndDrop / ServiceRelay | 盖章后观察、用户 drop 前观察、error/panic/100ms timeout/reentry/晚返回、满 bus 不倒流丢记录。 |
| W09-R03 | FormalClaude / OtherProviders / ServiceRelay | Run/Stream audit 等价，无远端 Store 的真实 relay demand；未知 catalog 不从 JSON 名字猜事实。 |
| W09-R04 | ServiceRelay / RecorderOwnership | 运行中立即查询、完整 identity/RunID 精确隔离、分页与独立副本。 |
| W09-R05 | RecorderOwnership / ObserverIsolationAndDrop | 共享 Store 每失败 run 首错停写，健康 run 不受影响，安全 notice、成功 Result 保持，Agent.Close 不关闭 Store。 |
| W09-R06 | FormalClaude / PartialWrappers(claude) | Unicode/下划线 catalog、ambiguous/unknown 不猜、正式 scoped wrapper 与 partial 去重。 |
| W09-R07 | OtherProviders(codebuddy) / CodeBuddyControl / PartialWrappers(codebuddy) | 正式 headless/control/partial 路径；保留 F01/F02 与精确重复 tool_result 的原输入和断言，完整检查复验修复。 |
| W09-R08 | OtherProviders(codex) | 真实 app-server initialize/thread/turn、MCP/plan 明确字段、重放去重、foreign scope 拒绝且保留 Raw。 |
| W09-R09 | OtherProviders(cursor) | 真实 print 明确 MCP 字段；unknown 无事实；Todo unavailable 不伪造空快照。 |
| W09-R10 | FormalClaude / WireValidation / InboundNegativeRelay / OutgoingLoss | 默认最小暴露、独立 opt-in、closed wire 大小/字段/UTF-8/整数、双侧安全 dropped。 |
| W09-R11 | FormalClaude / ServiceRelay | SSE/AG-UI/Merge 与实际 JSONL 重开保留 Capability。 |
| W09-R12 | ServiceRelay / HookAndDuplicateDomains | Host+HostLifecycle started/terminal 经绑定 publisher 与 core 唯一 Event 管线。 |
| W09-R14 | CoreTerminal / LeaseTerminal / RealPreparationCauses | 实际 parser success 后 cleanup/lease 失败，sources 有/无；唯一末尾 RunFinished 与最终 error 同因，原 terminal/transcript 完整。 |
| W10-R01 | ServiceRelay / WireValidation / InboundNegativeRelay | 无远端 Store demand，真实 HTTP+mapper，64KiB 及 UTF-8/枚举边界。 |
| W10-R02 | FormalClaude / InboundNegativeRelay | cap/todo 默认关闭，Tool 开关不代开；未知 kind 明确 dropped，旧工具 wire 仍读取。 |
| W10-R03 | HookAndDuplicateDomains | Before 成功且 Host Started 已到 core 后才远端请求；拒绝不产生 invocation/IO。 |
| W10-R04 | ServiceRelay | 同一 live core observer 在 EventBus 当前事实发布前查询，满队列后 recorder 仍保留事实。 |
| W10-R05 | HookAndDuplicateDomains / ServiceRelay | 相同 remote RunID+invocation 在两 delegation 域按手写 tuple 无碰撞，Source.Sequence/OccurredAt 保留。 |
| W10-R06 | ServiceRelay / HookAndDuplicateDomains | Local/Remote/nested 等价；hook 拒绝无伪造，观察失败不改 parent 成功。 |
| W12-R01 | TodoWholeSnapshots / NeutralVocabulary | 真实 ID、有序完整 Create/Update、synthetic 标记/tuple、未知本地序号不匹配；中立表无反向/provider/JSON 依赖。 |
| W12-R02 | TodoWholeSnapshots / NeutralVocabulary | 真实 ID、有序完整 Create/Update、synthetic 标记/tuple、未知本地序号不匹配；中立表无反向/provider/JSON 依赖。 |
| W12-R03 | FormalClaude / TodoWholeSnapshots / ObserverIsolationAndDrop | 合法空清空、无效状态/失败工具不更新、取消时 accepted Todo 对应完整 Dropped/末尾终局。 |
| W12-R08 | TodoWholeSnapshots / WireValidation / InboundNegativeRelay / OutgoingLoss | 完整与空 Todo 真实 A2A 往返；items null、invalid status、内容/数量上限安全拒绝。 |
| W12-R09 | FormalClaude / ServiceRelay | SSE/AG-UI/Merge/重开 JSONL 中空数组及快照字段保留。 |
| W12-R10 | ServiceRelay | Local/Remote/nested scoped Todo 与最终 clear 保留。 |
| W13-R07 | DelegationBudgets | Timeout 保持墙钟；独立 active 字段；同 Task 三次新预算；慢 recovery 不重置本次预算。 |
| W13-R08 | DelegationBudgets | 入参 TaskID 在首响应前可用；真实 HTTP CancelTask 采用未取消且 <=5s deadline；失败只为 remote_cancel 诊断。 |
| W13-R09 | TerminalQualification / EveryDrain / LocalCancelDrain / RealPreparationCauses | carrier 优先/资格反向/唯一 error 判定/真实 pre-Driver 原因来源/安全 code-limit/部分结果/取消和翻译 drain。 |

AC01：两种新事件的正式 parser→Agent→A2A→Service/非 A2A bridge/JSONL 闭环与结构负向。AC02：repetition/cancel/drop/EOF/bad payload/Local-Remote 明确断言。AC03：R016 公开正常与取消/翻译边界；私有 Cancel collector 只引用 documentation.md 中固定 archive，不计新增独立数。
