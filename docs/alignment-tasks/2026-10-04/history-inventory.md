# internal 历史完整性与迁移判定（2026-10-04）

来源固定为 `origin/master=2e57139e31891e359c4fe74b9186d6c146eca143`；目标固定为 `969514bf34d267a20681419c197c1858cf20cef6`。本报告只读盘点，不宣称本轮代码已实现或测试已通过。

全 refs（含 tag 的可达 commit）共 **69** 个，master 祖先 **39** 个、其余 **30** 个。旧计划的 **45** 条全部仍可达、无历史漏项；新增 **24** 个 hash（master 祖先 9、其余分支 15），去重为 **8** 个语义组。其中 **6 组为本轮可迁移的主线能力/缺陷缺口**；图片 role 修复只在新增图片支持后适用；另有 **1 组未合主线的跨HTTP运行托管 feature，经评估本轮不纳入**。

## 精确排除与既有成果

`1921636510ced4c830c0287fe68d41218f1ed185`（feat: add capability invocation observability）就是本轮排除的 skill/MCP/agent 调用观测提交。`origin/flowx/1069990761136960355/flowx_agent_2_0` 直接指向它；全体单父提交的 stable patch-id 中没有另一个等价提交。master 中直接包含该 commit，后续 merge 只是把含它的祖先链汇入；不能因此整条排除 merge 或后代独立修复。

目标已有 typed Capability、Driver 解析与可选 recorder，本轮不删除、回退或重新迁移这些能力。`51d12bd/7438d26/9b1ce27/dab6933` 是后续独立 relay/顺序/名称修复，仍逐项核查，当前已覆盖。旧 W01–W13 的实现证据见下表；历史方案的“缺失/尚未实施”是 9 月 7 日时点，不能当作本轮状态。

## 新增语义组及迁移边界

| 组 | 来源（去重） | 基线判定 | 目标证据与处理 |
|---|---|---|---|
| N01 模型 context window / auto-compact 下发 | `674a9b3`, `e97b74f`, `dddce05` | new-gap | 目标无typed配置，generated ModelContextWindow只是用量反馈。按Claude/Codex真实Config迁移，0不注入，拒绝非法/冲突；不恢复CommonConfig/WithContextWindow，不搬Cursor/CodeBuddy静默warning，不照搬Codex落盘patch。 证据：`claude/configured.go:22`, `codex/configured.go:23`, `codex/appserver/generated.go:164` |
| N02 Claude 仅正式 result 终止 stdin | `70ebfbd`, `7a37cc5`, `b77b102`, `5eb3bc8`, `b7fe1fc`, `91963d3`, `abdaa49` | new-gap | 目标非空且非tool_use的message_stop仍关闭stdin，max_tokens/end_turn后正式控制请求可失败。旧nested/empty/tool_use测试不足覆盖该场景；按最终branch修复思想迁移并保留目标checkpoint/后台工具边界。 证据：`claude/parser.go:907`, `claude/alignment_stdin_test.go:240` |
| N03 recorder HostSeq 区间与尾部 | `67c2183`, `607a815` | new-gap | 目标只有Since，缺上下界与tail查询；在可选hosttool提供，不扩张core执行概念。 证据：`hosttools/sessionrecorder/recorder.go:127`, `hosttools/sessionrecorder/recorder.go:268` |
| N04 recorder 按完整 Run 对齐回放下界 | `70d0f78`, `aaf95bd`, `8b2b9ba`, `a0f75f9`, `0373d16` | new-gap | 目标无run-start对齐，须基于typed RunID/生命周期而非旧StreamPayload字符串猜测；包含首记录run.started短路与最终精简fixture。 证据：`hosttools/sessionrecorder/recorder.go:127`, `hosttools/sessionrecorder/recorder.go:268` |
| N05 图片内容记录/回放 | `09cd0d3` | new-gap | 目标只有TextDelta及通用HostEvent，没有显式图片内容合同；作为typed Event与bridge/recorder映射迁移，不扩张provider图像输入或读未提交vision文档。 证据：`events.go:131`, `bridges/agui/events.go:350`, `hosttools/sessionrecorder/recorder.go:456` |
| N06 新增图片分支的 role 字面量修复 | `cb25c0b`, `2c8b594` | not-applicable | 目标尚无image分支，所以当前不会触发这项bug；N05新增图片时必须复用既有textRoleOpt映射，空值RoleAssistant输出assistant，不能把数字枚举直接写入CUSTOM。 证据：`bridges/agui/events.go:679` |
| N07 记录可信提问者 UserID | `2e69bd9`, `2e57139` | new-gap | 目标TextDelta和UserTurnEvents无提问者字段。仅宿主显式注入可信身份并保留typed事件/录制/bridge；不从用户body/Name/Raw猜身份，不让SDK负责鉴权。 证据：`events.go:131`, `bridges/agui/input.go:179`, `hosttools/sessionrecorder/recorder.go:456` |
| N08 未合并 A2A input-required 跨HTTP托管 feature | `2f935a6`, `b0dba48` | not-applicable | 唯一真正独立未合并增量，但不是目标已合修复漏移植。宿主可经OnApproval和live ApprovalRequest应答，尚缺A2A内置跨HTTP托管/同task回应便利。该新feature存在价值，但上游park/cancel/expiry破坏目标生命周期，本轮不搬；不宣称便利功能已覆盖。重新启动须先定义持续drain/终局归档、宿主生命周期、容量、取消、审批fallback与并发应答合同，仍只使用同一Stream/responder。 证据：`bridges/a2a/server.go:93`, `options.go:501`, `approval.go:149`, `bridges/a2a/mapping.go:132` |

`N06 not-applicable` 只表示目标当前没有这一图片分支的原始 bug，并非不处理 role。N05 的新实现必须一并采用正确字面量映射。N08 是唯一未合并但真正独立的增量，已检查后明确不纳入，不是遗漏。它是额外协议便利能力，并非宿主无法完成审批，也不是已合主线修复；不得把“本轮不搬”写成“已覆盖”。以上是固定基线差距，实施后的状态应由各 owner 的代码/验收记录更新，不覆盖本清单。

## 已有语义逐组核查

| 组 | 来源 | 判定 | 目标代码/测试证据及裁决 |
|---|---|---|---|
| O01 Claude 默认 Thread 常驻 | `e36c59b` | already-covered | 已有默认 Thread 复用及 WithSpawn；不迁移旧 opt-in 配置和宽松兼容逻辑。 证据：`claude/persistent.go`, `claude/persistent_test.go:38` |
| O02 Close 与准入关闭 | `bcaeec1` | already-covered | Agent.Close 幂等有界回收并拒绝新运行，旧 SDK.Close 不适用。 证据：`agent.go:125`, `agent_close_test.go:229` |
| O03 CodeBuddy 常驻 | `456e03e` | already-covered | 已具备默认复用、进程签名、启动前单次回退和 WithSpawn。 证据：`codebuddy/persistent.go`, `codebuddy/persistent_test.go:42` |
| O04 Codex app-server 常驻 | `0985324` | already-covered | 已复用 app-server；保留当前 Raw/terminal、单 writer 和 Close 合同。 证据：`codex/persistent.go`, `codex/persistent_test.go:33` |
| O05 Codex 每轮 output schema | `1b2de5b` | already-covered | 原生 schema 已为每轮字段；不引入新的 OutputMode。 证据：`codex/persistent_test.go:308`, `codex/appserver/run.go` |
| O06 真实 CLI BDD 结构及执行器 | `6a1e515`, `6bfa5ed` | already-covered | 15个feature与runner/steps已存在；本次只读盘点不等于重新执行真实CLI。 证据：`e2e/bdd_test.go`, `e2e/features` |
| O07 CodeBuddy partial-message 重建 | `75ce05d` | already-covered | 重建已有，当前终局 Text/Summary 权威规则更严格。 证据：`codebuddy/parser.go`, `codebuddy/streaming_parser_test.go:113` |
| O08 宿主 Tools | `803abc1` | already-covered | WithTools 与唯一runtime管线已有；R051 MCP对象兼容后续修复保留。 证据：`tools.go`, `tools_contract_test.go:47`, `internal/toolruntime/output.go` |
| O09 skill/MCP/agent 调用观测 | `1921636` | excluded | 按用户本轮范围精确排除增量迁移；既有 typed Capability/recorder/保护测试原样保留，不删除。 证据：`events.go`, `capability`, `hosttools/capabilityrecorder` |
| O10 嵌套 capability relay | `51d12bd` | already-covered | 后续独立提交不因祖先1921636而整条排除；无碰撞域和typed relay已覆盖。 证据：`hosttools/a2adelegation/relay.go:85`, `hosttools/a2adelegation/alignment_relay_test.go:211` |
| O11 成功生命周期后才观察开始 | `7438d26` | already-covered | BeforeDelegate成功后发事实，独立开始/终局规则已有。 证据：`hosttools/a2adelegation/delegator.go:123`, `hosttools/a2adelegation/alignment_relay_test.go` |
| O12 Claude MCP normalized/Unicode 名称 | `9b1ce27`, `dab6933` | already-covered | 原名、逐rune归一化和歧义拒绝已有；这两个后续修复仍核查，不被祖先排除误伤。 证据：`claude/capability_observation.go:39`, `claude/capability_observation.go:54`, `claude/alignment_adoption_test.go:208` |
| O13 中断部分结果 | `126d610`, `f972c10` | already-covered | 已有完整cause/部分Result；internal取消即Valid checkpoint违反目标健康合同，不迁移。 证据：`alignment_partial_result_test.go`, `claude/alignment_claude_test.go:417`, `codebuddy/alignment_partial_test.go` |
| O14 Claude schema/HITL | `4261914` | already-covered | 原生Question/PlanReview与Permission回退按唯一协商已有。 证据：`claude/alignment_claude_test.go:270`, `alignment_structured_hitl_test.go` |
| O15 Claude nested tool 事件 | `3ea225a`, `5d2b5de`, `98f41c7` | already-covered | typed工具事件、父域、重复和歧义防护已覆盖。 证据：`claude/tool_observation.go:115`, `claude/alignment_adoption_test.go:35` |
| O16 Dedicated hosted profile | `ca571fb`, `0c0f32f` | already-covered | 持久profile、跨进程owner与冷续接已有；不复制较弱旧实现。 证据：`tools.go`, `alignment_profile_test.go:88`, `alignment_profile_test.go:845` |
| O17 Claude root result 关闭 stdin | `eefcaef`, `a88eacd`, `d01a086` | already-covered | 旧W01 result-only/一次关闭已交付；较新的message_stop过早关闭缺陷另列N02。 证据：`claude/parser.go`, `claude/alignment_stdin_test.go:70`, `claude/alignment_stdin_test.go:200` |
| O18 A2A 多次 artifact Parts | `3253009`, `b11e052`, `f16fb20` | already-covered | Parts、Append、LastChunk、深复制及大小界限已有。 证据：`hosttools/a2adelegation/alignment_artifact_parts_test.go:77`, `hosttools/a2adelegation/alignment_artifact_parts_test.go:32` |
| O19 append system prompt | `8ffe22a`, `954aa68`, `6943d2a`, `38e0e8c` | already-covered | 单一SharedOption及原字节hash已有，Cursor明确unsupported保持。 证据：`options.go:456`, `driver/run.go:80`, `claude/alignment_append_test.go`, `codex/alignment_prompt_test.go` |
| O20 工具非法输入安全纠错 | `64163ab`, `8923108`, `f5fbb14` | already-covered | schema/解码错误有安全提示，系统/callback错误不泄漏。 证据：`internal/toolruntime/input_errors_test.go:18`, `tool/tool.go` |
| O21 Todo/plan 快照 | `eb82ed3`, `a2f2b0b`, `2e5f411`, `8ea34b4` | already-covered | 正式协议生成typed快照，空清单与作用域已覆盖。 证据：`todo`, `claude/todo_observation.go`, `codebuddy/todo_observation.go`, `alignment_protocol_integration_test.go:2809` |
| O22 A2A 历史问卷不截断续答 | `8e35b12`, `ac62c19` | already-covered | 历史Task快照和本轮live状态分离，含恢复与制品收敛。 证据：`clients/a2a/execution_state.go:28`, `hosttools/a2adelegation/delegator.go:507`, `alignment_protocol_integration_test.go:2316` |
| O23 可暂停主动预算 | `e2f0620` | already-covered | 唯一主动预算、重叠Ask暂停、原子提交前封账已有。 证据：`policy.go:48`, `alignment_budget_error_test.go:166`, `alignment_budget_error_test.go:418` |

## 未合并 A2A feature 的不采纳裁决

root 与盘点独立复核 `2f935a6`：`ServerOptions.Options`/`PromptBuilder` 能配置 `OnApproval`，宿主通过 live `ApprovalRequest` 的 `Approve/Deny/Answer` 完成业务应答；HITL 观测已有。尚缺的是先结束 INPUT_REQUIRED HTTP 响应、后台继续托管同一 Stream，再接后续 message 的内置便利。上游使用 `context.WithoutCancel`，park 后停止读取事件与终局投影，另起 deadline timer 强制取消，且缺少 live-run 容量/Close 与并发resume/cancel所有权合同。直接搬迁将降低取消、审批fallback、背压和资源回收质量，违反目标 AGENTS 第5/7/11节。

本轮保留现有能力，不引入新的运行托管器；这不表示该便利已经实现。以后要重启，需先定义宿主可关闭/有界容量、持续唯一drain、原Policy审批超时、唯一终局持久化、并发应答/取消和断线归属，再基于同一公开Stream/responder实现。该评估是用户“是否存在同问题、是否有价值修正”的处置，不是把未合feature算成已合修复漏项。

## 分支与 merge 去重证明

比较 branch 相对分叉点真正改过的路径，并逐字节比较 branch tip 与集成 commit；不能要求包含其他主线增量的整棵 tree 相等。以下 **14 组全部一致**：

| branch tip | 集成 | 改动路径数 |
|---|---|---|
| `f972c10` | `126d610` | 10 |
| `98f41c7` | `3ea225a` | 5 |
| `0c0f32f` | `ca571fb` | 3 |
| `f16fb20` | `3253009` | 4 |
| `38e0e8c` | `8ffe22a` | 30 |
| `f5fbb14` | `64163ab` | 2 |
| `8ea34b4` | `eb82ed3` | 23 |
| `ac62c19` | `8e35b12` | 7 |
| `e97b74f` | `dddce05` | 11 |
| `91963d3` | `abdaa49` | 8 |
| `67c2183` | `607a815` | 3 |
| `a0f75f9` | `0373d16` | 4 |
| `cb25c0b` | `2c8b594` | 2 |
| `2e69bd9` | `2e57139` | 9 |

七组完整 patch 等价对：`f972c10`/`126d610`；`5d2b5de`/`3ea225a`；`0c0f32f`/`ca571fb`；`ac62c19`/`8e35b12`；`67c2183`/`607a815`；`cb25c0b`/`2c8b594`；`2e69bd9`/`2e57139`。其余 refine/test 提交按最终 branch-tip 内容收敛，不能按标题判为仅测试或重复 cherry-pick。

`b0dba48` 相对最新 master 只余 A2A `convert.go/server.go/server_test.go/types.go` 四个路径，均与原 `2f935a6` 的这些路径完全一致。它合并 master 的动作不创造第二份新功能。`dddce05` 的 context 分支、旧 `d01a086/97b2c85/a68d907` merge 均按父链去重。

## 全部 69 个提交（无漏项）

状态描述其语义组相对固定目标基线的状态；同组 hash 共享状态，**不能相加当作独立工作量**。是否 canonical/重复、完整 parents、旧计划行号及原处置、refs 与逐路径比较结果保存在 JSON。

| Commit | 旧编号 / 新增 | master祖先 | 原始标题 | 组 / 判定 |
|---|---|---|---|---|
| `15b620a9a531` | A01 | 是 | feat: agent-adaptor Go SDK | 共同基线/集成 / not-applicable |
| `e36c59b2b980` | A02 | 是 | feat: add opt-in Claude persistent processes | O01 / already-covered |
| `bcaeec18f3e6` | A03 | 是 | feat: close SDK persistent process pools | O02 / already-covered |
| `456e03eb23cc` | A04 | 是 | feat: add opt-in CodeBuddy persistent processes | O03 / already-covered |
| `0985324984c1` | A05 | 是 | feat: reuse Codex app-server across streaming turns | O04 / already-covered |
| `1b2de5bb015c` | A06 | 是 | feat: pass Codex output schemas through app-server turns | O05 / already-covered |
| `6a1e51577a86` | A07 | 是 | test: define real CLI persistent-process BDD features | O06 / already-covered |
| `75ce05da0bbb` | A08 | 是 | fix: reconstruct CodeBuddy control output from partial events | O07 / already-covered |
| `6bfa5ed8e603` | A09 | 是 | test: execute persistent-process BDD against real CLIs | O06 / already-covered |
| `803abc127ffe` | A10 | 是 | feat: support host-defined tools | O08 / already-covered |
| `1921636510ce` | A11 | 是 | feat: add capability invocation observability | O09 / excluded |
| `51d12bd143ae` | A12 | 是 | feat(a2a): relay nested capability invocations | O10 / already-covered |
| `7438d2633ef7` | A13 | 是 | fix(a2a): observe delegation after lifecycle begin | O11 / already-covered |
| `9b1ce27faa4e` | A14 | 是 | fix(claude): resolve normalized MCP server names | O12 / already-covered |
| `dab6933b9159` | A15 | 是 | fix(claude): normalize Unicode MCP server names | O12 / already-covered |
| `f972c102dce2` | B01 | 否 | fix: 持久进程取消后保留可恢复会话 | O13 / already-covered |
| `126d610dfb6a` | A16 | 是 | fix: 持久进程取消后保留可恢复会话 (merge request !1) | O13 / already-covered |
| `5d2b5de8af1a` | B02 | 否 | refine | O15 / already-covered |
| `426191444582` | A17 | 是 | feat(claude): allow AskUserQuestion with native structured output | O14 / already-covered |
| `97b2c85c36fc` | A18 | 是 | Merge tag 'v0.14.4' into release/v0.14.6 | 共同基线/集成 / not-applicable |
| `a68d907d1d12` | A19 | 是 | Merge branch 'release/v0.14.6' into 'master' (merge request !2) | 共同基线/集成 / not-applicable |
| `98f41c7949cc` | B03 | 否 | Merge branch 'master' into fix/claude-nested-subagent-tool-stream | O15 / already-covered |
| `0c0f32fe6eb6` | B04 | 否 | fix: 持久化 dedicated hosted tool profile | O16 / already-covered |
| `3ea225acea57` | A20 | 是 | 补发 claude 嵌套子代理（如 Explore）中 tool_use 的 Stream 事件 (merge request !3) | O15 / already-covered |
| `ca571fbed845` | A21 | 是 | fix: 持久化 dedicated hosted tool profile (merge request !4) | O16 / already-covered |
| `eefcaef35621` | A22 | 是 | refine | O17 / already-covered |
| `a88eacdb4818` | A23 | 是 | refine | O17 / already-covered |
| `d01a086e5145` | A24 | 是 | Merge branch 'fix/claude-stdin-on-type-result' into 'master' (merge request !5) | O17 / already-covered |
| `b11e05235ca9` | B05 | 否 | test | O18 / already-covered |
| `f16fb20aaf8d` | B06 | 否 | test | O18 / already-covered |
| `954aa68cd911` | B07 | 否 | feat: 支持宿主定义系统提示词 | O19 / already-covered |
| `6943d2adddfe` | B08 | 否 | refactor(system-prompt): 把 append 语义写进公共 API 的名字 | O19 / already-covered |
| `38e0e8c2d055` | B09 | 否 | fix(claude): buildClaudeExecArgs 拆分以满足函数长度规范 | O19 / already-covered |
| `325300905468` | A25 | 是 | 一次 a2a 调用流程中支持多次上报制品 (merge request !6) | O18 / already-covered |
| `8ffe22a2a29c` | A26 | 是 | feat: 支持宿主定义系统提示词 (merge request !7) | O19 / already-covered |
| `89231087ddb3` | B10 | 否 | test | O20 / already-covered |
| `f5fbb1478473` | B11 | 否 | test | O20 / already-covered |
| `64163ab67ecf` | A27 | 是 | 当工具调用 json 传入某个不支持的 key 时，可返回具体原因 (merge request !8) | O20 / already-covered |
| `a2f2b0b7e5c8` | B12 | 否 | refine | O21 / already-covered |
| `2e5f411673be` | B13 | 否 | refine | O21 / already-covered |
| `8ea34b47089d` | B14 | 否 | refine | O21 / already-covered |
| `eb82ed36eaf6` | A28 | 是 | 归一化 plan/todo 为 todo.updated 事件 (merge request !9) | O21 / already-covered |
| `ac62c1921a36` | B15 | 否 | fix: 续轮流忽略存盘 Task 快照，只认 live Status 终态 | O22 / already-covered |
| `8e35b123af69` | A29 | 是 | 修复 A2A 续答时将历史问卷回放误判为本轮结束 (merge request !10) | O22 / already-covered |
| `e2f0620bdd64` | A30 | 是 | feat: 新增 Run 级可暂停主动执行超时预算 (merge request !11) | O23 / already-covered |
| `70ebfbde6725` | 新增 | 否 | refine | N02 / new-gap |
| `7a37cc540a9c` | 新增 | 否 | refine | N02 / new-gap |
| `b77b102ec8f7` | 新增 | 否 | refine | N02 / new-gap |
| `5eb3bc8be208` | 新增 | 否 | refine | N02 / new-gap |
| `b7fe1fc3090b` | 新增 | 否 | refine | N02 / new-gap |
| `91963d3c8f5f` | 新增 | 否 | refine | N02 / new-gap |
| `674a9b3eac85` | 新增 | 是 | feat: 支持模型级上下文窗口与自动压缩阈值翻译下发 | N01 / new-gap |
| `e97b74fd69b0` | 新增 | 是 | fix: 拆分超长行修复 CI 代码规范中风险问题 | N01 / new-gap |
| `dddce056ff74` | 新增 | 是 | Merge branch 'feature/flowx/137864965_ovryqy3o' into 'master' (merge request !12) | N01 / new-gap |
| `abdaa497214e` | 新增 | 是 | fix(claude):  interactive 仅在 type:result 关闭 stdin，修复 max_tokens 等其他 message stop 场景误关 stdin 导致 Stream closed (merge request !13) | N02 / new-gap |
| `67c21832de59` | 新增 | 否 | feat: sessionrecorder 支持按 HostSeq 的区间与尾部查询 | N03 / new-gap |
| `607a8154d85b` | 新增 | 是 | feat: sessionrecorder 支持按 HostSeq 的会话历史区间与尾部查询 (merge request !14) | N03 / new-gap |
| `70d0f78e5f11` | 新增 | 否 | feat: sessionrecorder 支持按完整 Run 对齐历史回放下界的查询 | N04 / new-gap |
| `aaf95bdfddda` | 新增 | 否 | fix: runStartIndex 对首条原始记录即 run.started 时短路返回自身 | N04 / new-gap |
| `8b2b9baabc6f` | 新增 | 否 | refine | N04 / new-gap |
| `a0f75f909e67` | 新增 | 否 | refine | N04 / new-gap |
| `0373d1656120` | 新增 | 是 | feat: sessionrecorder 支持按完整 Run 对齐历史回放下界的查询 (merge request !15) | N04 / new-gap |
| `09cd0d3fcb03` | 新增 | 是 | Merge branch 'feature/flowx/138424162_sux4qrcy' into 'master' (merge request !16) | N05 / new-gap |
| `cb25c0b7b86a` | 新增 | 否 | fix(agui): image.content 的 CUSTOM role 对齐 text.* 字面量映射 | N06 / not-applicable |
| `2c8b594802a2` | 新增 | 是 | fix(agui): image.content 的 CUSTOM role 对齐 text.* 字面量映射 (merge request !17) | N06 / not-applicable |
| `2e69bd9ea2d8` | 新增 | 否 | feat: StreamPayload 支持记录提问用户（UserID） | N07 / new-gap |
| `2f935a6244e0` | 新增 | 否 | feat(a2a): resume input-required decisions in place | N08 / not-applicable |
| `2e57139e3189` | 新增 | 是 | feat: StreamPayload 支持记录提问用户（UserID） (merge request !18) | N07 / new-gap |
| `b0dba4809995` | 新增 | 否 | Merge branch 'master' into feature/a2a-input-required-decision-bridge | N08 / not-applicable |

本次未读取/复制凭据，未调用 provider，未执行 live 或测试。目标代码和测试引用仅证明当前设计/实现位置；新改动需对应验证，已有 G06/Cursor 阻塞不会被本报告关闭。源库未提交的 vision 文档不属于提交范围；源本地 master 仍在 2c8b594，本报告以已 fetch 的 origin/master 2e57139 为准，不修改源工作区。

机器可读清单：[history-inventory.json](/Users/blurooo/project/agent-adaptor/docs/alignment-execution/2026-10-04/history-inventory.json)。
