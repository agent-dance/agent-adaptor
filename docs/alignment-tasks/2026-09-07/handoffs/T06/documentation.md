# T06：事件、观察入口与最终资源指纹

以 G01 `0adb8355378b0d1c0457119f96da470f4c33366a`、C03、R003/R006/R009/R010/R011 为边界。此片段由 G02 在本批验收前合入集中使用文档和 CHANGELOG。仅 T06 范围交付；provider 正式 parser、各 bridge wire、recorder、预算与 profile 后续验证分别由其 owner 完成。

## 合入 docs/streaming.md：typed 事实及收尾

同一 Events channel 增加 `CapabilityInvocation{Invocation capability.Invocation}` 与 `TodoUpdated{Snapshot todo.Snapshot}`。Capability 仅含 canonical key、operation、稳定调用及父 scope 坐标、闭集 phase/evidence/source、非零 UTC 发生时间、可选可靠耗时与安全错误码。`Duration=nil` 是未观察到，指向 0 是真实零耗时。Skill operation 固定 activate，Subagent 固定 spawn；catalog 名称逐字匹配，不猜名称含义。没有事实只表示未观察到，不能当作“没有调用”或完整审计证明。

Todo 是正式确认后的全量有序快照，不是审批 PlanReview。空数组清空当前 scope；`SyntheticID` 明确区分合成与真实 ID，未知 ID 不猜更新。ToolCall、ToolResult、TranscriptItem 及对应 SPI payload 保留 ScopeID/ParentScopeID/ParentToolCallID。Source 支持最多八层无环 Upstream；投递、观察、Result/Transcript 返回及 WithEventMeta 对可变数据分别复制。

准入执行由 core 发布唯一 RunStarted、RunFinished，与有无 run services 和 provider transport 无关。provider terminal 仍保留在 Raw.Terminal，公共 RunFinished 的 Failed/Reason 则在最终 Result/error、lease 释放、source 排空及 Detach 后确定。例如 provider 成功后 Detach 失败，Result 返回包含完整 Raw/Transcript 的 RunError，唯一公共终局也为 infrastructure；不得先向 UI 宣告最终成功。Run 仍严格等于 Stream + drain + Result。openStream 在静态 config/schema/policy/closed Agent/熵失败时没有准入或资源，保持既存空的已关闭 Events 与稳定 Result error，不伪造执行生命周期。

Capability/Todo 属于关键事件。已有 Dropped 摘要的序号在下一关键事实之前，用户也先接收摘要；但该事实的 observer 必须在上述任何用户发送等待之前运行。正常背压不丢关键事实。Cancel 或 SDK 发布权撤销可以解除阻塞，所有已编号而未交付事件进入最终 Dropped，之后独立保留槽发布 RunFinished，不要求消费者先腾位。Count 是未交付 typed 事件数，包含已编号但未送达的 Dropped 摘要本身，不是业务调用次数。

精确取消例：buffer=1，RunStarted seq1 占满；delta seq2 丢失；旧摘要 seq3、新 Todo seq4 已编号且 observer 完成；取消而暂不 drain 后，最终摘要 seq5 的 Count=3，ByKind={text.content:1,dropped:1,todo.updated:1}，FirstSequence=2、LastSequence=4，终局 seq6。正常送达的旧摘要不再计入最终损失。UI 收到取消/drop 后应将投影标记为不完整。

## 合入 docs/api-reference.md：RunAttachment

`RunAttachment.Observation` OR 合并 `CapabilityInvocations` 和 `Todos`；单独安装 Observer 不隐含 transport 需求。`RunEventInfo` 逐 run 提供 RunID、opaque ThreadKey、完整 Identity、DriverType。

`Observer` 只接收已验证、已盖接收坐标的 Capability/Todo，按 run 内接收顺序及 attachment 注册顺序执行，在用户背压前可实时写入查询投影。每次最多 100ms，取消收尾还受剩余总 cleanup 预算限制。首个 error/panic/timeout 永久关闭当前 observer/run，并发布一次仅含 code=observation_disabled、reason、observer_index 的 NoticeRuntime；错误或 panic 正文不泄露，Result/HITL/checkpoint 不改变，其他 observer/run 继续。回调即使在 deadline 后返回 nil 也不能重新启用。

Observer 非重入：不能同步等待本轮 Result/Events/Close 或调用本轮 publisher；传 callback ctx 调用 publisher 会立即失败。宿主 Store 必须在 commit 前检查 context。SDK 无法强杀任意 Go 回调或撤销恶意 Store 迟到写入；超时后不再给该 observer 启动新回调。共享 Store 由宿主关闭。

全部 attachments 成功且 observer 安装后，按注册顺序调用 BindEvents，再启动旧 Events sources 与 Driver。BindEvents 失败按原错误身份返回并逆序释放已获取资源。RunEventPublisher 只接受 Host/HostLifecycle、Relay/Relayed capability、typed Todo 及原 SubagentUpdate/Notice/Dropped；provider 证据、审批、公共 run 生命周期、typed nil 和非法字段返回固定 `adaptor: invalid run event`。它与旧 Events source 二选一发布同一事实，不能回灌重复。发布 ctx 限定等待，SDK 撤销同样解除已阻塞的直接 publisher 和 source pump。撤销发生于 Detach 之前，后续调用稳定 context.Canceled。

## 合入 docs/structured-output.md / run-policy.md：一次 resolved 协商

资源前 Normalize schema 一次，按构造配置捕获后的 `StreamSupport/StreamCapability` 与 `providerRichTransport` 选择初始 transport，校验该候选的静态 schema/policy。仅既有 rich 可用时构建 batch 备选，每候选 source 由同一 T31 协商器冻结。精确 NativeHITL/PromptValidateHITL 分别处理继承 Ask；nil 矩阵保留 legacy 显式 Ask 语义。不存在额外 transport-lock SPI，也不从零 capability 臆造 rich。

AttachRun 的晚到 observation demand 只对已验证候选评分，CapabilityInvocations 的 Skill/MCP/Subagent 各一分，Todos 一分，平分保留初选。没有 demand 保留原选择；无 schema 的显式 Ask 也禁止由观察评分把初始 rich 切成消音 Ask 的 batch，但原本初选 batch 且按其合同合法支持 Ask 的第三方 Driver 不退化。构造配置能证明的 transport 约束保持不变。无可行 schema 机制在资源前失败；晚阶段不 Normalize、不重做 resolver/物化、不复制 Driver dispatch。只有全部可行候选均不支持的所需事实产生安全 observation_unavailable Notice。

## 合入 docs/tools.md：最终 resolved profile 视图

早期 claim 只验证所有权、安全树及已有 managed symlink 的精确 manifest/source-path 证明；允许合法 skill 缓存稍后由唯一 resolver 重建。唯一 ResolveSkills/InjectSkills 完成后、Thread fingerprint/store/Driver 之前，在本轮持有不可变最终 snapshot：实际 resource 文件内容和模式、已证明 symlink 指向的 resolved source 内容、ordinary MCP、未知附加配置。最终 IO、安全或锁释放错误显式返回，绝不 warning 后继续。

内置 Driver 的 InjectSkills 可把实际 profile reconciliation 推迟到 Run；最终 snapshot 因此读取本轮已物化的 resolved source，构造带所有权证明的预期目标视图，不再运行 resolver 或自行写资源。投影视图复用正式 reconciler 的只读 prune eligibility：Claude/CodeBuddy 的 hosted profile 与 Cursor 使用 PruneManaged，Codex 保留健康未选项并仅剪除 broken managed（空 payload 保持不操作）。同 key 改 runtime name 也投影正式旧目标删除。只有精确 manifest/source 证明成立的待删除 symlink 从实际树与 source 读取中一起排除；复制树 marker 不能证明当前内容/权限，准备剪除这种树时显式 ErrUnsafe，保留的复制树仍按实际内容读取。未知及用户资源保守纳入；逐目标 Lstat 区分 NotExist 与 IO 失败，不能把宽松 discovery 的缺项当删除证明。MCP JSON/TOML writer 保留已存在 regular 配置文件的实际权限（包括 0400/0600），新文件继续默认 0644；非 regular 或 IO 错误显式失败，不通过权限归一化消除漂移。普通 MCP 同 key 覆盖/剪除只有在现值与 manifest 的 provider/path/rendered fingerprint 一致时允许；被改写、未知字段或缺少证明不得被 desired 投影抹平。

一次执行的 materialized digest 同时进入 Thread fingerprint、具体 ProfilePayload.Fingerprint 和稳定 SessionCompatibilityFingerprint。只规范化已证明属于本 Agent 的 hosted MCP 端口/凭据载体；真实 endpoint/env 仍传给 Driver 并影响具体进程配置。模型、identity、Driver 配置、workspace、skills/instructions/MCP、runtime 与 SessionCodec 原有维度均保留。R011 明确 Request.Streaming 只表示本轮交付选择，不作为额外持久 Thread 身份；真实 transport 若改变 checkpoint 编码或会话环境，仍由既有 SessionConfigFingerprinter、SessionCodec 参数及 Driver 启动前 guard 表达。Driver eligibility、真实 argv/env 和私有进程 signature 不削弱。按真实 execution.Dir 的 hosted profile 协调锁覆盖 claim/唯一解析至 Driver 返回，防止同 Agent 不同 Thread 的共享 profile 被并发改写；全局 map 锁只保护登记/复制，不同 identity 的不同隔离目录可独立并发运行；每轮 snapshot 不写回共享兼容缓存。T04 的跨进程所有权、随机 nonce 会话验证、Close 次序与目录删除边界保留。

可复现：运行 `TestAlignmentProfileDynamicResolvedSnapshot` 与 `TestAlignmentProfileDeferredMaterializerColdSnapshot`。A 的 Driver 写 session 文件与随机 nonce → Close → B 使用同声明/实际内容及旧 Thread key 真正读取原文件续接；同路径内容变化拒绝 ResumeOnly；缓存删除后本轮 resolver 重建可继续。TestAlignmentProfileDeferredPruneSnapshot 覆盖同 Agent、同目录 alpha→beta→beta ResumeOnly 及反复切换，用户 skill 保留且内容漂移仍拒绝续接；内部对照直接比较投影视图与正式 reconciler 的 link/rename/broken/none/用户树行为，并拒绝合法 marker 下复制树内容/权限漂移。另有 mode/unknown-config/IO/managed-link/ordinary-MCP 同key篡改/不同Thread并发测试；都走真实统一 Run/Stream 管线。

兼容 rich→临时 schema batch→rich 或 observation demand 引起的 transport 切换保持同一 active record 和健康 resume state，ResumeOnly 不因布尔值变化拒绝，每个 prompt 只派发一次。临时轮仍按原 provider 合同停止旧 writer、执行一次并预热下一轮；WithSpawn 不注册常驻 writer。真实构造配置、资源内容或权限变化继续拒绝。G02 原 Codex WithTools 一次 spawn 和 CodeBuddy persistent + one-shot + prewarm 共三次 spawn 断言原样保留，V04 在最终 SHA 跑完整两个 provider 包。

## 新增公共声明与 internal 取舍

root/Driver AST golden 增量逐项审阅：仅 C03 冻结的 parent/source 字段、两类 typed Event、ObservationDemand、RunEventInfo/Observer/Publisher、RunAttachment 扩展、driver ObservationCapabilities/Support/Request 及两个 StreamKind。capability 与 todo 是公开叶词汇包，内部 catalog/tracker/table 无 root/provider 依赖；无新增 module dependency。

不照搬历史 internal helper 对 Claude Unicode/下划线 alias、tool JSON、Todo 本地自增 ID 的解释，也不根据墙钟猜耗时、忽略空快照、回显参数/结果/自由错误。Catalog 冲突 tombstone 与 tracker exactly-once、table 原子 create/update/replace/clear 只接受规范化事实；provider parser 必须将状态变更与发布顺序串行化。

Usage 的非 nil 零值表示至少观察到一项有效 usage、归一化值均为零；当前类型不表达每个字段是否出现，不能据此声称 provider 明确报告每项为零。

## 合入 CHANGELOG

新增安全 capability/todo 事件、父 scope、可选每轮 observer/publisher 与独立 transport observation demand；所有准入执行的公共终局改由 core 最终 Result/error 决定。修复取消 drop 摘要完整性、观察回调失效隔离与发布清理死锁。修复 hosted Tools 下动态 resolved 资源/权限/外部配置漏指纹、已管理 skill symlink 冷续接及缓存重建边界。已有空流/事件计数假设需包含 core start/terminal；审批与错误身份保持原合同。

## 验证范围

本机 macOS arm64，fake Driver、正式资源 reconciler、loopback MCP、临时 profile/store。源码 SHA 固定后的完整必需命令与实际子测试计数见 result.json/evidence：V01 全指定包；V02 observer race×5；V03 profile race×5；V04 完整 codex/codebuddy 包。所有命令显式关闭 live/E2E/API golden 自动更新。未执行真实 provider、付费 live、独立 Linux/Windows 平台验收、整个批次/发布 gate；不据本交付宣称其通过。
