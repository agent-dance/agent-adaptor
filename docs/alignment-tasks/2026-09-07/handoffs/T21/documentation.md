# T21 跨层协议独立 QA 交付片段

本任务只增加外部 `adaptor_test` 集成测试和凭据隔离的子进程 fixture。没有公共声明、生产实现、依赖、golden 或执行入口变化。测试前缀全部为 `TestAlignmentProtocol`；所有权只限本任务 allow。最终通过状态以事后 result/evidence 的实际 SHA 为准，此片段不能替代门禁报告。

独立 oracle 起始于 C01–C04/canonical17 及 R001–R016 冻结合同中的字段与手写正式 NDJSON/JSON-RPC bytes，最终按 canonical22/R001–R020 和 replacement G04 复核。正式 provider、AG-UI 快照和生命周期 fixture/断言保持；不可编码 Meta 的 HTTP 形状按 C02 只读复核校准，详见 findings.md。Claude、CodeBuddy、Cursor 与 Codex app-server 全部通过真实 Driver parser → 公共 Agent；A2A 采用真实 Handler/AgentCardHandler/HTTP client，再与 Service Local/Remote、嵌套 Service、subagentstream、SSE、AG-UI、JSONL sessionrecorder 和 capabilityrecorder 组合。普通 fixture 的 CLI Command 是当前测试 binary 的私有 init，不启动真实 provider。

`apRunner` 明确表示第三方公开 Runner，用于协议资格、取消、错误和出站恶意值边界；其人工终局不作为 core 生命周期证据。core 的开始/唯一末尾终局另由真实 Agent 的静态拒绝、准入准备失败、正式 parser、cleanup 屏障、Thread lease release 失败证明。原 error/Result/Cause 不因 bridge hint 重写。手写独立 A2A peer 只用于第三方 continuation/recovery 输入，不冒称我们的 Handler 实现。

第三方 fixture 的 drain audit 同时要求 producer 在事件 channel 关闭后完成，以及缓冲尾事件已经消费；只见 producer done 不足以证明 drain。独立 Result 完成屏障在次数/earlyResult 核验前有界等待；DrainOracle 保留未消费尾事件负控和完整消费正控。此 QA 校准不修改公共 Stream 合同。

可复现行为示例：同一个 TaskID 连续三轮，旧 input-required/completed/failed Task 快照只能恢复身份和制品；新 Status 才能产生新问卷/结束。制品逐更新保留 Text/Data/inline bytes/URL、Append/LastChunk；显式 recovery 合并已证明的扩展、保留较旧前缀，对不兼容快照发安全 conflict 降级。Local/Remote 在真实父工具 scope、重复 invocation ID、Source/Upstream 与本地 Sequence 上保留相同合同。

观察器在唯一 sink 盖章之后、用户队列和 EventBus 之前接收事实。回调 error/panic/timeout/reentry 只停用本轮观察并给安全 notice；不改变成功结果。Recorder 不自动创建 Store、不替宿主关闭共享 Store，查询按完整 identity/RunID 分区且可在运行中读取。取消带背压时仍可有界完成 Result，已接受而未交付的 Capability/Todo 计入完整 Dropped，终局最后交付。正常 Todo 是每次完整有序快照，空数组是已观察清空。

A2A capability/todo 分别显式 opt-in，Tool/diagnostics 开关不能隐式开启。无远端 Store 也必须按 observation demand 使用正式事实。closed wire 的未知字段/枚举/非法内容/大小上限在边界可见降级，不能原样泄漏非法字段。raw decoder 可检验原 bytes 的 duplicate key/UTF-8/surrogate；已解码 map 只能证明结构值合法，不能声称恢复原字节审计。旧 wire 兼容与 unknown kind 明确 dropped 分开验证。

R016 公开观察边界：真实 HTTP Send/SendStream 和 Local/Remote 正常路径校验同一次 Stream、非 nil carrier 优先、末尾唯一合格 hint、nil error 成功、所有非法 hint 保持原 bare fallback。真实准备期四组父 cause/本轮预算通过公开 Agent 触发，不能用手造 RunError 代替。公开 CancelTask 可先给 legacy canceled ack；本测试对其证明一次 Stream、幂等 Cancel、完整 drain 后一次 Result、无伪造 limit。HTTP handler 请求取消与独立执行上下文不同，不能从 EOF/ack 推断 executor 最后私有分类。翻译失败保持可观察协议失败：可以是实际 failed Task/status 后的 EOF，也可以是携带同源安全翻译 cause 的 StreamRecoveryError，不能被尾 hint 改成 cancelled/active。超长 ThreadKey 仅接受 payload_too_large，超安全整数的 Sequence 仅接受 invalid_payload；两者完整消息前缀都是 `encode adapter stream status: `，并核验直接非 nil 的公开 `*client.StreamRecoveryError`、非 nil Cause 的完整 `Error()` 精确文案、最后观察 TaskID（未观察时为空）、所有 Task/status/Part/Raw 中无假 control 或 limit。normal、CancelTask ACK、可编码 dropped 的成功路径不采用此二选一。Local cancel-drain 另验证部分文本与原 cause。

TranslationOutcomeOracle 的正控是人工公共类型输入，仅证明 oracle 接受两种合法形状并拒绝错误类型、cause、TaskID、终态、早先/后部 Part control 与 deadline；不能描述为实际 HTTP 必然跑到了 StreamRecoveryError 分支。两个真实 HTTP translation 用例在日志中记录各次实际观察形状，完整验证数量与分支覆盖据此报告。C02 此次复核只静态推导了 T21 的同型风险；T21 旧 `2139534` 全绿属于保留的阶段证据，不证明唯一 HTTP 表示。

根 QA 仅依赖公开 client 错误合同：SRE 的 Cause 类型是 `error`，普通 `errors.New(精确文案)` 也是合法 cause。顶层同文案普通 error、包装/join SRE、超时、错误 TaskID、错误 cause 文案与所有已观察帧中的假 control 仍被拒绝。上游 A2A SDK 的 concrete error/code/Details/TypedDetails 不属于公开 oracle；相关检查已移除，以遵守现有 import localization guard，不以 reflection、helper 或 allowlist 例外绕过边界。该差异是协调要求的纠正，不是公开 API 变化，也不声明上游内部字段的新增覆盖。

历史 failed Task 快照后仍可能得到同源 SRE；完整 Task 自身不能冒充 live 终局。live failed Status 或明确 RecoveredState 后则要求正常结束，不接受第二个 SRE。正/负控明确区分这三个来源。

私有 executor Cancel-drain 分类补充只引用已接受的 T19/C02/G04 archive：G04 `2421fe470cf67b22697fef796b038c0be6e395c8`，T19 `075887499a1966ed4d08681bd837a4fcbfa1c650`，archive SHA256 `399a38c3673ef0ea6b430797f2f1f3f26c7f8f49f891303537b9330adefb728f`，外部 `findings/G04-composition/review.md` 记录已提交 owner barrier `TestAlignmentR016EveryDrainPath/cancel` race10 20 个 parent/child pass。此引用不是本任务新增独立证明，不计入 T21 pass 数；root 已确认无需新增不存在的 executor 公共入口或 unsafe。

集中合并目标由 G05 唯一写入：`docs/a2a.md` 的 continuation、制品、最小暴露、取消 ack 与预算小节；`docs/streaming.md` 的父 scope、完整 Todo、observer/Dropped 小节；`CHANGELOG.md` 的验证记录。现有公共语义不变，无新增 godoc/AST golden 理由。CodeBuddy 两条正式 partial-wrapper 生产发现和 AG-UI 已发布快照被修改的 race 保留在 `findings.md`，由原 owner T15/T32 修复；不变反例随完整检查重放，修复来源与历史红证据分别记录。

未采用 internal 的旧 SDK/binding、跨 scope 裸 ID 配对、Args 与 ArgsDelta 重复拼接、从 stdout/声明猜 capability/todo、历史 Task 充当本轮终态、成功文本与失败双判定面、失败 checkpoint 宽松保存。独立预期不调用生产 encoder/namespace helper，不计 owner 已有测试为新证据。

平台与费用边界：只运行当前 macOS arm64、真实 Go 1.26.5、本地 loopback、私有 HOME、三门 0。没有 Linux/Windows 原生执行、付费/live、真实 CLI 版本/认证、push/tag/release 证据。最终基线是 root 已重新验收的 G04 `b2035bc793369fb8fefb9de229ff1dbd2b748853`；三个原 QA 提交 rebase 到该基线，最后文档提交后按原 V01/V02 完整选择范围运行，另补充 `go test -count=1 ./clients/a2a -run TestA2AImportsStayLocalized`，再生成报告。测试 argv 仅可增加 -json/-v；超时由进程外 watchdog 实现。T21 交付不表示 G05 或 B06 已验收。
