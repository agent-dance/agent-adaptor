# T33 — CodeBuddy 常驻 stderr 回调与 Result 交接

Canonical 23 / R021，W04-R06 补充实施，基线为已验收 G04 `b2035bc793369fb8fefb9de229ff1dbd2b748853`。由原 T08 owner 实施，保留 T08/T20 历史证据；G05 合入本片段到集中使用文档、CHANGELOG 和 AGENTS。T25 在 replacement G05 上提供独立原生 Linux 证明。

旧 G05 `43e6ba3d90016b1a4b267408ccd200a950041369` 的真实 Linux race 显示：先行健康 Thread.Run 返回时，stderr copy goroutine 已取出本轮 observer，却仍未调用 parser.onChunk。清空 active 指针不能撤销该回调，随后它与 Response.Transcript 读取并发；无换行 stderr 也可能错过 finalize。测试变绿不能抹去旧 T25 红日志。

现在同一 activeMu 边界处理 Raw append、observer 归属、callback 登记、轮次起点和封账时的 Raw 快照。登记之后释放锁再调用 sink/parser；关闭入口后释放锁，再等待该轮已登记回调结束，最后由原 parser.finalize 刷入无换行尾段、构建既有 Response。没有在 activeMu、parser.mu 或进程池锁下等待宿主回调。健康轮只等待本轮回调，不等待常驻进程退出。每轮都有独立 observer、计数和 parser，返回后没有旧 producer 再修改已交付审计。

每轮包括在入口关闭前线性化接收的 stderr chunk，Raw 与正式 parser 恰好对应；关闭后无 active observer 的诊断不回填已完成结果，也不冒充下一轮 Transcript。跨 stdout/stderr 管道无法判断未来尚未接收的 provider 字节属于哪轮，SDK 不作语义猜测。已接收 callback 不能通过超时弃置来换取表面有界；原 context/pipe/sink 取消路径解除阻塞后仍完成 join。

失败轮保留原先终止进程、drain stdout、等待 stderr copy/cmd.Wait、收集真实原 cause 的顺序，然后才关闭本轮入口。取消、非零退出、短写、缺失/畸形终局仍保留可用 Text、Raw/Terminal、Transcript、Usage 与 RunError 原因；失败不产生健康 checkpoint，不改旧 healthy record，交付前仅一次安全 fallback，交付后不重放，单 writer 不变。T15 append、正式 capability/todo、父域/重复 wrapper 与观察收尾不变。

## 可复现与验证

`TestAlignmentCodeBuddyPersistentHandoffHealthy` 使用真实测试二进制常驻子进程；stderr callback 在正式 parser 消费前被明确屏障挂起，stdout terminal 只能在 callback 已登记后被解析。旧代码四个 Run/Stream × newline/无 newline 组合提前返回且缺 Transcript；新代码直到屏障释放才返回完整审计。有限负向等待用于确认已到达屏障后的 Result 尚未完成，不靠 sleep 或概率调度制造触发。下一轮与闲时 stderr 后，对首轮全部审计层做独立 JSON 内容快照比较；真实 helper ledger 验证每个 prompt 一次和同一常驻 writer，无重叠。

屏障先累计已经接收的完整预期诊断，再阻塞其最后一个 callback，因此独立的正文/换行管道写入被拆成多个 chunk 时，Raw 断言也不包含尚未接收的字节。首次 race20 揭示的 fixture 前提错误及修正后固定 G04 生产代码的反例日志均保留。

`TestAlignmentCodeBuddyPersistentHandoffFailure` 覆盖 Run/Stream 下待处理 stderr 的取消和真实 exit23，验证完整部分审计、errors.Is/As、健康 store record 不变、失败后 replacement 以及不重放。比较前先确认健康记录与 State 非空、记录 ID 和正式 session 正确，序列化错误显式失败，避免 missing record 的空值比较虚过。已有全部 CodeBuddy 和 Partial race 测试继续验证 W04-R06 历史边界。

源码提交固定后执行完整包、Partial race×5、新 Handoff race×20 和 vet；每条命令使用 Go 1.26.5 的实际路径、隔离进程 HOME、三个 live/E2E/golden 门为 0，以及进程外 watchdog。实跑计数、环境、历史失败和哈希在外部 result/evidence 中。本地 macOS arm64 fake-process 证据不替代 T25 原生 Linux、Windows、live、G05 或发布门禁。

## 集中文档目标

- `docs/streaming.md`：常驻 stderr 交接、已接收回调与正式终局的完成顺序。
- `docs/public-errors.md`：失败 drain/Wait 及可用部分 Result/cause 的原合同保持。
- `docs/api-reference.md`：Result 审计在轮次交接后稳定，Raw/Transcript 的共同接收边界。
- `CHANGELOG.md`：修复 CodeBuddy 健康常驻轮 stderr callback 与返回 Transcript 的竞争及无换行尾诊断丢失。
- `AGENTS.md` §14.1：R021 原生 Linux 红发现及 T33 补充实施；只有 replacement G05/T25 证据才能关闭独立验收。

无公共 API、依赖、golden 或正式协议字段变化；仅修改常驻 transport 的同步交接。局部 godoc 位于 codebuddy/doc.go。无新增 require，继续使用标准库 sync.WaitGroup；不采用 internal 以中断 session ID 放宽 checkpoint 的旧行为。
