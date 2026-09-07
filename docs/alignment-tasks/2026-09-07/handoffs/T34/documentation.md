# T34：Codex 常驻 stderr 的接收与映射验收

本次仅修正 QA 的跨 pipe 前提和补充 codex godoc；生产实现、公开声明、依赖、golden、generated Go/schema 均不变。原 W04-R07 owner T16、verifiers T20/T29 不变，T34 requirements_owned/verified 均为空。本交付不是新 G05 或 T25 的平台验收。

健康常驻 turn 的 stdout terminal 无法证明独立 stderr pipe 的接收已经完成。已经接收的本轮字节必须完整保留；未来未接收、无轮次标记的 stderr 不可猜测，也不可修改已返回的 Result。健康连通进程仍立即完成该轮，不等待退出。one-shot 和失败的原有有界 drain 完整捕获要求不变。

## 双层覆盖矩阵

| 精确测试 | 接收或转换证明 | 严格断言 |
|---|---|---|
| `appserver.TestAlignmentCodexStderrAdmission/received-and-future-idle-across-turns` | 原真实 child 先写原始 `fixture-stderr` 14 字节；同包测试从 Open 创建、生产 io.Copy 使用的同一个 stderr buffer 的本轮 start offset 读取；确认 exact14 后，才经测试控制连接放行原 stdout Usage/text/terminal | 完整本轮 stdout 等于同生产 buffer 区间；stderr exact14；Usage 非 nil 0/2；正式当前 thread/turn completed payload 和原字节；健康 checkpoint；进程仍连通 |
| 同一用例的 future-idle gate 与第二轮 | child 明确停在未来 idle write 之前，首轮结果先发布；放行后确认真实 idle 字节进入同 buffer；下一轮再次以新 offset 确认 exact14 | 未来字节不能被推断到已发布轮次；已接收 idle 不回填旧结果；下一轮不携带旧 idle 字节；首轮全部 Response 层在 idle 和下一轮后不变 |
| `appserver.TestAlignmentCodexStderrAdmission/cancel-held-terminal` | 收到本轮 exact14 后保持 child terminal gate 关闭，再取消真实 RunTurn context | 有界返回原 context cancellation；已接收 stderr exact14；没有 terminal 或健康 checkpoint；清理可以解除等待 |
| `codex.TestAlignmentPartialResultAndPublicEquivalence` 全部 8 个原场景 | 真实 configuredDriver 的 test-local wrapper 保留原 optional interfaces，仅在唯一 Run 返回前保存同一 Response；公开 Agent/Thread × healthy/nonzero/malformed/cancel 的 Stream 和 Run 各自核对捕获值 | Raw stdout/stderr 与 terminal JSON 逐字、Transcript/Usage/Services 全值、Text/Summary/Provider/Model 一致；真实 Driver cause 可 Is；每次只派发一次；Usage 非 nil 0/2；正式健康 terminal；失败 carrier/checkpoint/cancel 原断言保留 |
| 同一公开矩阵的重复 Result 与下一次 Run | 以独立字符串快照保留首次公开全部审计层 | 多次 Result 和后续健康/失败 Run 均不改变首次成功或部分 Result；one-shot 及失败 Run/Stream 继续 exact `fixture-stderr`，仅健康 resident 的跨 pipe exact 前提由第一行承担 |

Services 比较保留全部报告值与顺序；Driver nil 与 core 空列表均表示零报告，因此统一零报告序列表示。Usage 的未观察 nil 与正式零值仍严格区分，Raw/Terminal 原始字节不规范化。

测试控制连接只存在于 testdata child 和测试文件，使用 loopback 与有界 deadline。成功条件来自生产 buffer 实际字节值，不来自 child write ACK、sleep、quiet window、EOF 或杀健康进程。receipt 循环仅检查精确条件并让出调度，context 到期是失败边界；所有控制连接在失败/父 context 取消时关闭，随后有界回收真实 child。取消子例真实停止被取消的进程，不把其退出当健康接收证据。

原 root `TestRunAndStreamResultAllLayersAreEquivalent`、`TestResultFromResponseDeepCopiesAuditLayers`、`TestAlignmentPartialResultOutcomeMatrix` 保持不变，继续为独立全仓门禁提供输出转换/错误矩阵覆盖；T34 不用未执行的 root 测试冒充其四项检查。

## 证据与文档集成

保留原 frozen45cada Linux fullcount1 红（原 OR 未打印具体值）、隔离 fatal-only count20 非复现、全仓 count5 新同型 stderr-only 红、before-first-Read 红与同 buffer 接收绿的原日志/patch/metadata，以及 C02 最终报告。它们证明旧前提不足；不倒填历史原红字段，不将诊断当正式 T25 验收，也不合入诊断 process.go hook。

G05 应把上述接收边界合并至 `docs/streaming.md` 的 Raw/常驻结果说明及 CHANGELOG 的测试可靠性条目；`codex/doc.go` 已提供局部 godoc。没有新增公共声明或 golden 更新理由。未采用通过中断 session ID 标健康 checkpoint、等健康常驻退出或推断未来 stderr 的行为。

源码提交后在同一最终 SHA 执行完整 Codex count1、全部 Alignment race5、新 admission race20 和 vet。普通环境显式关闭 live/E2E/golden 更新，使用隔离 HOME/provider 默认路径及固定 Go 1.26.5。这里的最终检查是 macOS 本地 fake 验证；native Linux 的正式完整 18 条 T25 命令、Windows 和 paid/live 由后续对应门禁执行，不能由旧诊断或本地结果替代。
