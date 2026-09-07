# T24 消费者文档与可执行示例交付

T24 只拥有 task.json 明列的使用文档、五语言 README、examples 与本 handoff。
初始编写基线为已验收的 G04 `2421fe470cf67b22697fef796b038c0be6e395c8`、canonical17
C01–C04 与 R001–R016；R017 重新打开 G04 后继续独立编写。正式交付须迁移到协调者
重新接受的 G04 并在最终源码 HEAD 执行原 validation，准确 base/head 只记录在随后
生成的 result.json。历史基线的检查不会充当新基线验收。

## 本任务消费者增量

- `examples/offline` 实际通过公开 New/Run/Stream 执行脚本化内存 Driver，无 CLI、
  provider 配置、凭据或网络访问。main.go 只消费根与 hosttool 公共 API；driver.go
  单独展示 fixture SPI。它演示原生追加默认/覆盖/空清除/下轮默认不变，同流 Question
  应答、Capability 两阶段、Todo 全量/空表，以及精确 scope 的显式内存 Store 查询。
- 同一示例实际让 25ms 主动预算结束一个等待中的 fake Run，从唯一 RunError 读取
  Reason、部分 Text 和 Raw。构造与调用都用整值 Policy，没有新 timeout option。
  parent 是 10s 墙钟上限；这不是模拟时钟或声称真实 provider 的 25ms 性能。
- 可执行 Example 精确核对输出，另一个实际 SDK 管线测试核对 Run/Stream 文本/摘要、
  回调应答、Todo 清空/Revision、唯一运行序号、末尾终局与重复 Result。
  原 examples 全包测试保留，不以编译或零测试替代实际执行。
- T24-F01 补齐教学 Driver 的 SPI 生命周期：Streaming=true 的所有脚本由同一个
  Run 实现发布 provider 首尾；review 的 capability 与文本生命周期在正常、拒答、
  取消及发送失败时均尝试一次收尾，保留原错误与 cleanup 错误。失败的 sink 不保证
  交付，示例不会通过重发制造重复终局。core 仍独占最终消费者 RunFinished。
  独立录制器直接调用该 Driver，以 adaptertest.VerifyStreamSequence 和 capability
  配对断言验证上述路径；SDK 测试另断言消费者只有一对运行首尾。原输出保持。
- examples/README 区分 offline、纯 codec、只读 Inspect、资源物化和真实 CLI/main；
  quickstart/streaming chat/showcase 注释同步 Driver entry 后所有失败均有 RunError，
  启动前错误仍为原包装。没有修改这些 live 示例的业务流程或缩减其断言。
- 五语言 README 同步 27 With*（包含 WithEventMeta）、原生追加唯一入口、Todo 与
  ToolCall/Transcript 并存且不是 PlanReview、best-effort 观测不构成授权/完整审计/计费
  证据、四种不同证据层与 offline 命令。Dedicated+Tools 保留/宿主清理/临时选择/
  已删历史无法恢复一致。修正原“复制登录状态”概述为既有 AuthLink 链接共享合同。
- API/streaming/run-policy/A2A/errors/provider matrix/SPI 使用文档补齐可执行入口和
  证据语义。A2A 例子同时展示墙钟和独立主动预算，保留 team.Option 原流接入；
  既有 Source/Meta、ExposurePolicy、R016 分类、部分制品和 C02 终止原因规则保持。

所有 offline facts/raw 均显式标明演示数据，不认证 provider。CodeBuddy/Claude
正式工具结果、Codex native input acceptance/spawn 与实际下游执行仍严格分开。
profile matrix 的原研究版本、历史 PASS/auth/CLI 失败和时间边界没有改写。

## 13 个工作项的消费者文档映射

这些入口说明最终消费者行为；表格不替代各 owner 的实现、独立验证和平台/live 门禁。

| 工作项 | 消费者路径与章节 | 示例与必须保留的边界 |
|---|---|---|
| W01 result-only stdin | docs/streaming.md §4 Result/errors/cancellation；docs/streaming-adapter-contract.md §8 Raw/provider terminal | examples/streaming；一次性关闭、常驻不关底层 writer，保留尾部 Raw/终局 |
| W02 Dedicated session files | docs/tools.md Threads/semantic revisions、Persistent Dedicated profiles；docs/api-reference.md §5/§9；docs/public-errors.md Hosted profile errors；五 README Agent isolation | source/clone 分离、已有 source、Agent.Close 后保留、所有权/冲突/离线恢复、失去历史无法重建；真实冷续接验收属于 provider live |
| W03 A2A continuation | docs/a2a.md Streaming continuation and recovery | examples/a2a-server；历史 Task 不变成本轮问卷/终局、EOF 明确中断、显式 GetTask 恢复另有语义 |
| W04 partial Result | docs/api-reference.md §8；docs/public-errors.md One execution error path；docs/streaming.md §4；五 README Results/errors | examples/offline budget、quickstart/streaming chat/showcase 注释；Driver entry 边界、原 Cause、无健康 checkpoint 不持久化 |
| W05 nested tool parent | docs/streaming.md §3/Provider observation support；docs/streaming-adapter-contract.md §5；docs/a2a.md Capability/Todo/parent projection | 原 ToolCall/ToolResult/Transcript 保留 scope/parent；完整 Args 不再重复为 ArgsDelta，未知父不猜 |
| W06 schema + HITL | docs/structured-output.md Automatic capability negotiation、Claude schema and approvals；docs/run-policy.md Run errors/矩阵；docs/api-reference.md §9 | examples/structured-output 是需真实 CLI 的示例；Claude native Question/PlanReview、Permission prompt fallback、完整 effective Ask 与 raw 交互激活分开 |
| W07 safe tool correction | docs/tools.md Schemas、Errors and cancellation；docs/public-errors.md Host-defined Tool errors；docs/api-reference.md §11.1 | examples/tools；invalid_input 保留 ErrInvalidInput 与安全 rejection，handler 未执行，不回显字段值/原错误 |
| W08 artifact Parts | docs/a2a.md Delegation artifact updates | 一次更新 Parts 与最终累计值分开；Append/LastChunk、空终块、IncludeRemoteArtifacts/Raw 不暗开 |
| W09 capability observation | docs/streaming.md §3、Provider observation support；docs/api-reference.md §12.1；docs/profile-resource-provider-matrix.md Materialization and invocation evidence | examples/offline 同流与显式 recorder；best effort、未观察不等于未调用，非授权/完整审计/计费证明；Store 由宿主管理 |
| W10 relay | docs/a2a.md Capability/Todo/parent projection、Curated Local/Remote delegation；docs/streaming.md Session recording and subagent streams | examples/showcases/team-agent-workflow 原 team.Option；Before 成功后 started、observer 在有损 bus 前、唯一 envelope/无碰撞来源、独立 opt-in |
| W11 native append | docs/api-reference.md §3.1/§5；docs/structured-output.md Native append with structured output；五 README Options/resources | examples/offline 四次真实 SDK 调用；唯一 SharedOption、原字节覆盖/清除、Instructions 独立、Cursor 拒绝、inline/Windows 限制、完整指纹/单 writer |
| W12 confirmed Todo | docs/streaming.md §3、Provider observation support；docs/a2a.md Capability/Todo/parent projection；docs/streaming-adapter-contract.md Formal observations | examples/offline 非空→空、Revision/Sequence；Todo/ToolCall/Transcript 并存、非 PlanReview；真实/synthetic ID，Cursor print 无支持 |
| W13 active budget | docs/run-policy.md Active execution budget、Delegation active execution budget；docs/public-errors.md；docs/a2a.md Stable failure classification；docs/api-reference.md §4/§8 | examples/offline 预算部分结果，docs/a2a.md request/maximum 示例；整值 Policy、Ask token、墙钟独立、R012 Persist 前封账、R014 原因与 R016 同流提示 |

## 四项 ownership requirement

- W09-R13：API/streaming/provider matrix 和五语言均明确 best effort、无观察的有限
  含义、授权/完整审计/计费边界；offline 数据不能作为 provider 证据。
- W11-R09：使用当前代码/冻结声明；仅 WithAppendSystemPrompt 新 option，27 名
  含 WithEventMeta；五语言与可执行覆盖/清除示例一致。provider 局部 godoc 已由
  已验收 owner 交付；T24 未越界重写。根 doc.go 的集中确认由 G05 完成。
- W12-R11：streaming 及 SPI 使用文档明确 ToolCall/Transcript 并存、Todo 非
  PlanReview、空表清除/新 run 独立、实际协议矩阵；没有四 Driver 全覆盖声明。
- W13-R10：Policy、Approval、errors、root 与 delegation 两层预算及 A2A 安全
  分类均有使用路径和实际离线示例，R012 提交前封账/Finalize 仍决定成功/已提交不
  回滚、R014 已选原因/R016 单错误判定保持。未新设执行入口或公开计时器。

## G05 集中收口请求

T24 未编辑 AGENTS.md、CHANGELOG.md、doc.go、中央执行态或其它 owner 的 handoff。
G05 合流时请：

1. CHANGELOG Unreleased 增加：“Add an executable offline consumer example covering
   native append override/clear, same-stream approval and confirmed Todo snapshots,
   scoped capability recording and active-timeout partial Results. Align all five
   READMEs and usage references with the evidence and profile ownership contracts.”
   原 B01–B04 breaking changes 保留，不重复声称新增已有 API。
2. doc.go 的 Examples/Documentation 入口加入 examples/offline；保持六名词开篇、
   27 个 With*、唯一 New/Run/Stream 与 driver/ 专属 SPI。
3. AGENTS §14.1 的 T24 状态只能在 T24 最终新基线/HEAD 报告被独立接受后更新；
   T25–T30/G06 与发布授权继续独立。R017 测试入口补全也不能替代实际 live。

无新公共声明、无 root/SPI golden 变更、无新 require、无 generated/schema 编辑。
不采用 internal 的默认 Agent/SDK/Admin、额外 Start、WithTeam、string Runner 查找、
模式开关、逐字段 Policy 继承、取消有效 checkpoint、参数完成就成功 Todo、按文本
猜资源调用、隐式 capability exposure 或历史 live 结果复用。

## 验证纪律

原必需检查保持完整：`go test -count=1 ./examples/...`（仅可添加 -json/-v，
由进程外 timeout 保证有界；带 Go -timeout 的预检查单列，不替代原 validation）。
另执行 `go run ./examples/offline`，输出应与 Example 的具名行为一致。实际非零数量、
允许 skip、退出码、OS/Go、base/head、日志 SHA256 在最终 commit 后的 result/evidence
记录，不写入本源码 commit 制造 SHA 自引用。

全部测试子进程使用专用 HOME/USERPROFILE/XDG_CONFIG_HOME，移除 provider root
环境变量；固定 Go 1.26.5/GOROOT/GOTOOLCHAIN=local 与既定共享缓存，三项 paid/live/
golden 门为 0。不执行真实 CLI/version/auth 探针；本机 loopback 仅用于原 examples
HTTP 假夹具。Linux/Windows、付费/provider live 均未在 T24 执行，历史研究记录保持。

T24-F01 的反例在 `0bf137f` 加入独立测试、尚未修改 fixture 时实际执行失败，
记录为 `evidence/f01-red.{json,log}`；它是该旧 HEAD 加测试的开发证据，不是新 HEAD
验证。`c225a9b`/`0bf137f` 阶段与原预验日志保留。修复 commit 后重新预验，仍须等
重新接受的 G04 迁移后运行原完整 validation 才可生成正式 complete 报告。
