# Internal 对齐任务派发包

将[逐提交对齐方案](../../internal-history-alignment-plan-2026-09-07.md)拆为 **7 个批次、36 个并行工作任务、7 个串行验收任务，共 43 份 task.json**。覆盖原方案全部 **13 个 W 工作项、45 个历史提交和 96 条原子验收项**。单批最多并行 6 个任务。

本包定义计划与验收要求，task.json 的 planned 状态不是实时执行状态。实际派发、提交、返修与验收证据由协调者在 `docs/alignment-execution/2026-09-07/` 单独保存；不能将计划或合同冻结当成已通过的功能或发布证据。

## 派发入口与文件

| 文件 | 用途 |
|---|---|
| [manifest.json](manifest.json) | 唯一调度入口：批次顺序、任务路径与校验和、依赖门禁、集成顺序、基线。 |
| [coverage.json](coverage.json) | 45 个提交的处置，以及 96 条要求的唯一实施 owner、独立 verifier、最终关闭门禁。 |
| [task.schema.json](task.schema.json) | 每份 task.json 的严格 JSON Schema，拒绝未知字段。 |
| [result.schema.json](result.schema.json) | 交付格式：实际 SHA、测试、逐项证据、文档片段及剩余问题。 |
| [execution-state.template.json](execution-state.template.json) | 外部执行状态模板；全部 planned，没有预填成功结果。 |
| [validate.py](validate.py)、[test_validate.py](test_validate.py) | 标准库校验器与故障注入测试；不执行任务中的命令。 |
| [validation-report.json](validation-report.json) | 本次任务包静态校验结果，明确标记 implementation_executed=false。 |

每个任务位于 batches/批次/任务ID小写/task.json；批次验收任务位于该批次的 gate/task.json。任务 JSON 自包含原方案相关 W 节全文、固定源提交、必读文件、允许修改的路径、实施步骤、验收项、验证命令及交付格式。**必须传递完整 JSON，不能只传标题。**

contracts/ 保存已审阅合同及版本化修订，handoffs/ 的源码提交包含局部文档片段。实际 result 与测试日志在所引用源码提交之外收集，派发器必须向后续 worktree 提供前置 gate 已验收的产物。

## 批次与并行边界

| 批次 | 可并发任务 | 并发数 | 放行任务 | 放行后获得 |
|---|---|---:|---|---|
| B00 合同冻结 | C01 profile 生命周期；C02 部分结果与主动预算；C03 Event/观察/relay/todo；C04 native prompt/schema | 4 | G00 | 精确 Go/wire 合同、目录与锁策略、接口文件分配和冻结清单。 |
| B01 关键修复 | T01 Claude stdin；T02 Tool 错误；T03 A2A continuation；T04 持久 profile；T05 公共部分 Result；T31 schema/HITL 协商 | 6 | G01 | 六条独立修复通道合流后的可运行基线。 |
| B02 Event 基础 | T06 Event/observer/中立状态机；T07 Claude 部分结果与 schema；T08 CodeBuddy 部分结果；T09 实时 artifact Parts | 4 | G02 | 后续消费者可使用的真实接口及错误/制品保真基础。 |
| B03 Core 与协议 | T10 append prompt/主动预算 core；T11 A2A 新事件；T12 其余桥与 recorder；T13 capability recorder | 4 | G03 | Driver 与 delegation 接入所需的 core 和 wire 合同。 |
| B04 Provider 接入 | T14 Claude；T15 CodeBuddy；T16 Codex；T17 Cursor；T18 delegation；T19 A2A 预算错误映射；T32 原 bridge owner 的 AG-UI 快照修复 | 最多 6 | G04 | 13 个 W 项完整实现的集成候选。 |
| B05 跨层验收 | T20 生命周期；T21 协议与嵌套 A2A；T22 策略/错误组合；T23 conformance/CI；T24 文档/示例 | 5 | G05 | 已通过实现验收的冻结 SHA，供全部最终平台/live 检查共用。 |
| B06 平台与 live | T25 Linux/race/fuzz；T26 原生 Windows；T27 Claude live；T28 CodeBuddy live；T29 Codex live；T30 Cursor live | 6 | G06 | 同一 SHA 上的平台、真实协议与最终验收证据。 |

B00 → G00 → B01 → G01 → B02 → G02 → B03 → G03 → B04 → G04 → B05 → G05 → B06 → G06。

同批的并行任务没有相互依赖，也没有交叉写文件。max_parallelism 是同时执行上限；B04 七项任务按空闲槽派发，最多六项同时运行，G04 仍须等待全部七项验收。G00–G06 在对应 worker 完成后单独运行，不能作为“第 N 个并行任务”一起派发。批次屏障使下一批获得实际可用的接口，避免用尚未合入的 peer 分支补依赖。

### 拆分依据

- **热点文件按批次串行。** T05 → T06 → T10 依次拥有相关 root 文件和 golden；Claude 按 T01 → T07 → T14 前进；delegation 按 T03 → T09 → T18 前进。
- **每个 Provider 目录同批只有一个 owner。** T14–T17 各自完成该 Driver 的多个 W 项，避免多个任务同时改同一个 parser。
- **共享合同先交付，再接消费者。** T11 不依赖同批 T10 新增的 Go 符号；T18/T19 使用前批已存在的类型和 G00 冻结的 wire code/fixture，互不等待对方。
- **独立验证在实施之后。** 每条原子要求有唯一实施 owner 和后续批次的独立 verifier。T24 文档由下一批 T25 核对，避免同批 QA 依赖尚未写完的文档。
- **公共文档每批同步。** worker 写自己的 documentation.md；gate 在该批放行前合并集中文档、CHANGELOG 和必要的 AGENTS/godoc。T24 做最终一致性校对，不能成为前几批欠文档的理由。
- **外部环境检查集中并行。** Linux、Windows、四个 live 任务共用 G05 的冻结 SHA。B06 只生成外部报告，不改代码或追加文档提交。

## 实际派发流程

1. **准备 seed。** 代码基线为 919f140f64f89c80933802840c8878681a85a4d9，源仓库为 e2f0620bdd6477e6fe16f6db5648093589342ca2。开始实施时，在隔离的 codex/alignment-integration 分支把原方案、本任务包和 AGENTS 纳入 seed commit，记录真实 seed_head。本次已创建 seed `bc0d421f9c0b1e80e529d1e843fe5d8396eab02f`，G00 交付 `93ef44f24e63ce52ad29dce2b54ff28fa0503470`。不要从不含本包的默认 main 直接派发，也不要 stash/reset 丢弃用户改动。
2. **建立外部状态。** 复制 execution-state.template.json 到派发器数据目录，填入 seed SHA。task.json 始终描述计划，真实进展填外部 state/result，不能把计划的 status 改为 complete。
3. **派发当前批 worker。** 从 manifest 读取 parallel_tasks，每个任务使用独立 worktree 和自己的 codex/alignment-任务ID 分支。同批任务共用一个 base：B00 用 seed，其余用上一 gate 实际交付的 head_sha。同时提供必读文档、固定源仓库 Git 对象和已验收合同。源路径是 hint，允许映射到只读 clone；不能假设 worktree 的 ../agent-adaptor-internal 一定存在。
4. **收集交付。** worker 提交范围内的实施/测试/局部文档后，在实际 head 上运行检查，再生成 result.json。报告、日志和执行态由派发器收集，不塞进它们所引用的同一个源码 commit，避免 SHA 自引用。逐项验收 acceptance、任务对应的 requirements、validation 和实际 Git diff。
5. **运行本批 gate。** 全部 worker 交付可接受后才派发 gate。协调者按 integration_order 合入隔离集成分支，合并中央文档，并在最终合流 SHA 上重跑检查。文档提交后的 SHA 才是测试和下一批 base，不能复用文档提交前的测试结果。
6. **放行或返工。** gate 通过后更新外部 state，下一批使用其 SHA。失败则保留原任务/要求 ID，把复现与归属交回原 owner；修复并重新集成后重跑受影响验证。自动合并无冲突不等于语义验收通过。
7. **最终验收。** G05 记录 implementation_head。B06 全部任务和 G06 的 base_head/head_sha 均指向这个 SHA，commits/changed_files 为空，证据在外部收集。如需修改实现或文档，回到 owner 和 G05 冻结新 SHA，再按影响重验，不能混用多个版本的证据。

G00 须把四份合同落实为具体字段、函数、错误、wire fixture、目录/锁方案与文件分配，并输出 contracts/frozen.json 的逐文件 SHA256。关键接口未定不能放行实现。目标仓库未合并分支 6432e5e、12a1ad1 由 G00 记录协调结论，本包不自动合并它们。

只有协调者可以修订范围或 DAG：记录理由、受影响 ID、旧/新 scope 和需失效的验证；同步任务内容、manifest 的 task SHA256、coverage 及相关上下文，再运行校验。已映射要求不能通过删除、改名或将 blocked 视作 passed 来关闭。白名单只允许**精确路径或目录 /** 后缀**，包含未来新增文件；校验不依赖文件当前是否存在。

## 验收、环境与完成语义

普通命令由派发器注入 task.validation.environment，显式关闭 AGENT_ADAPTOR_LIVE_CONFORMANCE、AGENT_ADAPTOR_E2E 和 golden 自动更新。全量测试中明确关闭的 live 探针可以 skip；支持矩阵中的不适用 probe 须列明理由。**当前任务要求验证的场景不能 skip，零测试不算通过。**

允许给既定 Go 测试命令添加 -json 或 -v 收集证据，不能缩窄 -run、删除包或降低次数。报告记录真实执行数量；fuzz 的 executed_test_count 记录实际种子/样本数，并附目标、时长和日志。T25 已列出 9 个 fuzz 目标各 30 秒，以及 S1–S9、四 Driver conformance、全量、race 和重复检查。T24/T23 还须记录 fake-driver 示例的实际运行命令，编译不能替代运行。

T14–T17 先建立各自 TestAlignmentLive* 入口，为既有 conformance live 路径落实 build tag、环境变量门与隔离 profile；T23/G05 检查命令确实选择到测试后，B06 才能使用。真实调用沿用会话已给出的明确授权，本次生成计划不等于授权执行付费测试。缺 runner、CLI、认证、配额或授权时记录对应任务 blocked，其他独立验证仍可继续；不能因此把 G06 标为完成。

每条 W 要求须同时具备实施与独立验证证据。worker complete 仅表示该任务交付，G05 表示实现验收通过，G06 才表示本方案全部验收证据闭环。Git tag/release 始终属于独立发布动作。

result.schema.json 的关键要求：

- base_head/head_sha/commits/changed_files 是真实 Git 信息；每个 check 的 head_sha 对应交付 head。
- acceptance/requirements/checks 逐 ID 回填；complete 不能遗漏必需项、保留 open finding 或 remaining item。
- checks 记录命令、环境、OS/Go/CLI 版本、退出码、实际测试数量、允许的 skip 和日志路径，不能只写“测试通过”。
- documentation 指向任务文档片段和唯一中央 gate；无公开变化时也记录“无变化”及原因。
- 环境不足用 blocked，实现/合同缺陷用 needs_rework；两者均须附具体剩余事项或 findings。
- gate_details 仅 gate 使用，包含已接受任务、测试 SHA、下一批 base 或最终 readiness，不携带发布授权。

## 校验命令与边界

在仓库根运行，Python 3.9+ 即可，不增加 runtime 或 Go 依赖：

~~~sh
python3 docs/alignment-tasks/2026-09-07/validate.py --verify-git
python3 -m unittest discover -s docs/alignment-tasks/2026-09-07 -p 'test_validate.py' -v
~~~

派发后验证真实报告，将尖括号占位替换为实际路径：

~~~sh
python3 docs/alignment-tasks/2026-09-07/validate.py \
  --result <result.json> --state <execution-state.json> --verify-git --json
~~~

--verify-git 核对固定基线的 AGENTS 内容哈希；有 result 时还核对祖先关系、实际净 diff 与所报 commit。complete 报告必须带外部 state，才能检查前置任务和动态 base。blocked 报告允许缺少尚未获得的 SHA/检查结果，但必须说明缺项。

校验器检查 JSON 结构、DAG、屏障、同批路径/资源冲突、前置产物归属、方案嵌入内容、45 提交覆盖、96 条要求映射、任务哈希和报告元数据。它不能判断全部 Go 符号依赖、断言是否充分、外部日志是否真实或 fixture 是否代表正式协议；这些由 G00、独立 QA、平台/live 证据和各批 gate 负责。gate 还须核对 artifact SHA256 与实际文件，不能将格式正确视为内容可信。

附带 schema 是 Draft 2020-12；标准库校验器只实现本包使用的关键字。扩展 schema 关键字时须同时扩展校验器，或使用完整 JSON Schema 实现验证。

## 执行期修订

[R001](amendments/R001.md)：B00依据代码更正W06，新增B01 T31作为Claude的SPI/core前置；补齐T05取消断言的测试范围。95条原要求全部保留。

[R002](amendments/R002.md)：T03提前修复A2A的RunError优先级与部分输出保留，使用基线符号保持同批独立。

[R003](amendments/R003.md)：schema预检提前到资源前；动态resolved profile指纹由B02管线owner接线，95要求全部保留。

[R004](amendments/R004.md)：同步W06总表/附录与已核实的schema/HITL基线事实。

[R005](amendments/R005.md)：按完整有效Ask校验nonnull schema矩阵，保留nil旧语义；T31首轮独立审阅发现P1并返修。

[R006](amendments/R006.md)：新增W09-R14关闭已实证的Driver-only终局权威矛盾；原95要求全部保留，现96条。

[R007](amendments/R007.md)：精确区分profile解锁前后的Close重试，Windows长期锁禁止delete sharing。


[R017](amendments/R017.md)：补齐真实 cold-resume 和 Skill/Subagent 用例入口，禁用门只证明 fixture，不充当 live 通过。

[R018](amendments/R018.md)：修复 CodeBuddy 公开 Agents 物化路径，原 owner 以官方 loader 与独立公开反例验收精确 runtime name 和安全 .md 文件名。

[R019](amendments/R019.md)：原 T12 bridge 负责人以补充 T32 修复实际 AG-UI 已发布快照共享状态/race；W05-R05、W09-R11 最终责任显式转交，历史验收保留，G04 增加该独立任务并保持最多六并发。

[R020](amendments/R020.md)：T20 利用既有 private post-unlock seam 增加独立 close 故障验收，精确新增一测试文件，不改生产与公共API。
