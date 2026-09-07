# T22 独立 policy 组合验证片段

T22 仅增加测试、离线协议装置和复验脚本，没有公共或生产语义变化，没有新增公共声明、依赖或 golden 更新。预期来自 AGENTS、冻结 C01–C04 和 R001–R016 的公开合同；测试不调用生产错误分类函数生成预期，不复制 owner 测试。canonical22 replacement G04 `b2035bc793369fb8fefb9de229ff1dbd2b748853` 已验收；T22 的 27 个 requirement 与公开合同保持不变，5 个阶段提交已无冲突迁移。

## 应合并的文档段落

G05 可将以下证据说明合入 `docs/run-policy.md` 的主动预算/HITL、`docs/structured-output.md` 的固定协商、`docs/a2a.md` 的失败与 Local/Remote 边界、`docs/tools.md` 的可修正输入错误。CHANGELOG 如需记录，使用“增加跨层 policy 独立回归验证”，不得宣称新增能力或真实 provider 已通过。根/SPI godoc 与 golden 无变更。

- 100ms 主动预算：40ms 执行、300ms Ask、50ms 执行仍健康；额外 10ms 才耗尽。重叠 Ask 在最后一个回答后恢复；自动应答不暂停。审批自身超时、外层 deadline、Close、lease 续期仍按各自合同工作，成员 Ask 不暂停父层预算。
- R012：Driver 返回后的 schema、checkpoint/lease 健康检查属于执行预算；原子 Finalize 之前结算封账。已经耗尽但尚未调度的 timer 必须阻止 Finalize；封账后迟到 timer、已提交后延迟返回不产生 active timeout。原 parent context、Finalize 错误和 cleanup 仍可影响最终结果，已提交状态没有通用回滚。
- R013：三种 Approval 的 Choices/嵌套 Details 为独立描述快照；live 副本共享一个 exactly-once responder。记录器 JSON replay 无应答权。R014 的 Timer.Stop 屏障实际阻塞于已选本轮原因到锁外通知之间，后到父 cause 不得抢主因，反向也独立核验。
- R016：失败唯一来自同次 Stream.Result 的 error，非 nil RunError carrier 优先。只接受归属匹配、Failed、闭集 Reason、唯一且最后、完整 drain 关闭的终局提示。测试的 typed 777s 父 cause 与本轮 100ms 来源由构造/时序确定，不按 limit 大小或 errors.Is 顺序猜测。
- R016 的公开 CancelTask ack 仍可先给无安全 code 的 canceled；它与 executor drain 的分类是不同可观察边界。真实 HTTP 用屏障核验实际第二次 Events；Result 内同时要求 producer 已关闭且缓冲尾 channel 已清空，并写入测试直接检查的 earlyResult 标志。HTTP 在有界等待独立 resultDone 屏障后才检查一次 Result、一次 Stream 和无伪造 limit，不能将 ACK/EOF 或 producer close 当作消费完成。翻译错误保持协议失败，不被尾 hint 改为成功。内部 collector 分类引用固定 G04/T19 已验收证据，不计 T22 新独立执行数；Local 取消 drain 独立保留 partial/cause，并遵守本次 Delegate 已选主因。
- schema 在资源前校验静态候选，AttachRun 晚需求只选择最终 transport，资源只 Attach 一次。显式和继承 Ask 都送入真实 sink，逐机制非 nil 矩阵、nil 旧语义与零 raw policy 分别验证。Claude 离线正式 stdin/stdout 协议装置执行 Question/PlanReview/schema、Permission fallback、回答/拒绝/超时/无效结果及 result-only；结果各层来自同一次正式 parser。
- native append 保持独立通道，近处替换与空串清除，不进入用户 prompt/语义 transcript。真实离线 CodeBuddy 子进程用同一 loopback 独占监听证明 rich→batch→rich prewarm、WithSpawn 和 append 签名变化的单 writer/每 prompt 一次；不以 PID 日志声明代替实际排他资源。
- Tool 输入错误同时保留 ErrInvalidInput 与私有 safe rejection，分清语法/schema/Go 解码；恶意字段名有界且确定，handler 不运行，外部 As 无法伪造 rejection，panic/输出错误不泄露私有内容。

## 27 项独立覆盖索引

测试名省略根前缀 `TestAlignmentPolicy`；`PublicComposition` 每次都运行完整内层 `TestT22*` 测试 binary。以下为新独立装置的观察范围，不以 owner 的测试通过替代。

| ID | 独立可执行证据 |
|---|---|
| W04-R01 | PartialAndHealthyState、StaticAndOptionResolution、SelectedCauseNotificationGap、内层 R016RealCoreSources |
| W04-R02 | PartialAndHealthyState 的 Run/Stream 逐字段 audit；ClaudeFormalSchema 的 Run/Stream 正式 parser |
| W04-R03 | PartialAndHealthyState 六类失败、BudgetSealAndAtomicFinalize、ClaudeFormalSchema 无效/拒绝/超时零 Finalize |
| W04-R04 | RetryExpiryAndBackpressure、LeaseContinuesDuringAsk、IndependentParentAndClose、BudgetSealAndAtomicFinalize cleanup |
| W06-R01 | SchemaLateDemand 真 sink；ClaudeFormalSchema Question/PlanReview × Run/Stream × Thread/stateless |
| W06-R02 | ClaudeFormalSchema 原始 argv/control response 与 Permission fallback；SchemaLateDemand 无 schema Ask |
| W06-R03 | SchemaLateDemand native/prompt/non-nil false/nil/初始batch/晚需求 × Run/Stream |
| W06-R04 | ClaudeFormalSchema result-only/回答/拒绝/timeout/invalid × Thread/WithSpawn × Run/Stream |
| W06-R05 | ClaudeFormalSchema Raw/Terminal/zero Usage/Decode/健康 checkpoint 同次解析 |
| W07-R01 | SafeToolCorrection 的 sentinel + 私有 rejection |
| W07-R02 | SafeToolCorrection 语法/schema/Go number→int 解码、handler 零调用 |
| W07-R03 | SafeToolCorrection 控制字符/超长 Unicode key、重复稳定文本 |
| W07-R04 | SafeToolCorrection 外部 As/敏感值/schema；ToolPrivateFailures 的 panic/输出/handler |
| W07-R05 | SafeToolCorrection required/type/enum/nested/extra/malformed/malicious 全表 |
| W11-R01 | StaticAndOptionResolution 编译作用域、默认/多重覆盖/empty clear |
| W11-R02 | StaticAndOptionResolution unsupported 零资源；MCPModeAndInspectSnapshot 构造期 Command |
| W11-R03 | ThreadTransportAndPromptIdentity；ResidentSchemaPrewarm append 改变后新记录、新 writer；ClaudeFormalSchema 原 prompt/transcript 不混入 append |
| W11-R04 | NativeFileIntegrity owned carrier、0600、相同长度篡改、symlink、取消优先、幂等清理 |
| W13-R01 | StaticAndOptionResolution、TinyBudgetAndStoppedController、IndependentParentAndClose |
| W13-R02 | ExactActiveLedger、RetryExpiryAndBackpressure auto/满队列、SelectedCauseNotificationGap pre-Driver、BudgetSealAndAtomicFinalize |
| W13-R03 | OverlappingAsk、RetryExpiryAndBackpressure、SnapshotAndResponder 三 Kind 12 路竞答 |
| W13-R04 | TinyBudgetAndStoppedController、BudgetSealAndAtomicFinalize stale、SelectedCauseNotificationGap Stop barrier |
| W13-R05 | IndependentParentAndClose、LeaseContinuesDuringAsk、LocalCancellationDrainsPartialCarrier |
| W13-R06 | BudgetSealAndAtomicFinalize 九类场景、SelectedCauseNotificationGap 四种时序、PartialAndHealthyState |
| W13-R07 | 内层 DelegationOwnBudgetAndKnownCancellation（初始化/Before/墙钟/负数/continuation），DelegationRecoveryKeepsBudgetAndRejectsStaleReplay（同一预算 context、旧 Task 不作本轮完成） |
| W13-R08 | 上述内层 known 入参 TaskID、一次 detached ≤5s CancelTask、失败仅 safe diagnostic |
| W13-R09 | 内层 R016HintQualification 16×HTTP/Local×Send/Stream、R016RealCoreSources 真实 pre-Driver 2×HTTP/Local×Send/Stream、CancellationAndTranslationDrain、DrainOracleConsumption 早读反向控制 |

## 执行与证据边界

`run_checks.py` 只修改被启动子进程的环境；工具链、缓存与私有 HOME 路径由执行台架显式传入，拒绝非 Go 1.26.5。Go 测试使用 runtime.GOROOT 查找平台工具链，保持 GOTOOLCHAIN=local；每个外层进程仅构建一次外层调用的公共组合 binary，每次 count 仍重新运行该 binary，传播 race、递归守卫、40s 内层 timeout，保留完整 test2json 并拒绝零测试/skip/非零退出。唯一 TestMain 透明保留 m.Run exit code，等待同步子进程结束后只清理自身 MkdirTemp 编译目录。

最终要求仍是原命令 `go test -count=1 . -run TestAlignmentPolicy` 与 `go test -race -count=20 . -run TestAlignmentPolicy`。可添加 `-json` 收集；外层超时由脚本进程边界施加，不改变原 Go 命令。正式 result 在上述 replacement G04 基线、全部源已提交的同一个 final HEAD 执行后生成，最终日志为 `evidence/T22-V01.jsonl` 与 `evidence/T22-V02.jsonl`，内层 JSON 与二进制实际执行次数单独计数；结果/evidence 不提交到它引用的 HEAD。旧阶段日志仅保留历史反例与迁移前信息，不用于当前 acceptance。

本次任务执行台架为 macOS arm64，使用真实 Go 1.26.5、私有测试 HOME，provider override 已移除，三个门变量为 0。未执行 Linux/Windows 原生运行、paid/live、真实 CLI/凭据；装置报告的离线 Claude 版本字符串只是协议输入，绝不是 CLI live 版本证明。Windows 的模式断言仅覆盖该平台可观察权限，symlink 要求 runner 提供相应能力，不静默 skip。T25/T26/T27 的原生/真实验证保持各自职责。

## 不采用的旧行为

不引入 exported PausableContext、逐字段零值继承 Policy、无健康证明的取消 checkpoint、按 size 信任 append 缓存、将 native append 拼入 user prompt、从 JSON 猜 provider 语义、桥层第二错误策略、依据父/本轮 limit 数值猜来源，也不把公开 HTTP CancelTask ack 冒充内部 drain 分类证据。
