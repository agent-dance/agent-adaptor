# 完整修复并推进到可合入 main 的 Plan

状态：**待实施的修复计划，不代表修复完成或门禁通过。**

目标是在质量不降低、功能不损失的前提下，关闭本轮全部 live 问题，使分支达到可审阅、可合入 main 的状态。本计划终点是通过验收的候选及合入材料；实际 merge、tag 和 release 不包含在本计划的执行终点中。

## 1. 基线与完成定义

| 项目 | 本次规划基线 |
|---|---|
| 当前分支 | `codex/review-fixes-20260910` |
| 已测试候选 | `1c49823fafd2a057be4b0fc6dd96a563e957e2e9` |
| 本地记录的 `origin/main` | `919f140f64f89c80933802840c8878681a85a4d9`；实施开始和最终验收前分别 fetch 复核 |
| 原任务包 | revision 28，47 个任务、96 条要求、45 个历史提交处置项 |
| 当前结果 | T27/T28/T30 `needs_rework`；T29 `blocked`；G06 未通过 |
| 已具备授权 | 本机 Claude/CodeBuddy/Codex/Cursor 的真实验证，沿用既有授权 |
| 历史证据 | 同 SHA 的 G05、Linux、Windows、全量 CI 已通过；本轮新发现要求重新修复、冻结及验收 |

依据：[本轮验收](alignment-execution/2026-09-16/provider-live/acceptance.md)、[返修队列](alignment-execution/2026-09-16/provider-live/rework-queue.json)、[执行状态](alignment-execution/2026-09-16/provider-live/execution-state.json)、[原任务入口](alignment-tasks/2026-09-07/README.md)、[项目合同](../AGENTS.md)。历史证据目录已封存，本计划放在目录之外，不改写原失败、原哈希或旧结论。

只有同时满足以下条件才可宣布“可合入”：

- 本文 9 个条目全部关闭，并有独立复验；所有新发现一并登记、关闭，没有把缺少 requirement ID 的问题遗漏。
- 原 47 个任务、96 条要求与 45 个提交处置全部可追溯。已交付实现保留，下游 blocked 表示等待重新验收，不要求重做不相关实现。
- 修复、回归测试、godoc、使用文档、CHANGELOG、任务包修订全部进入最终候选提交 **S**，其后不再改源码或受检文档。
- G05、T25、T26、T27–T30 均验收 S；G06 在全部依赖接受后通过。没有必需场景 skip、零测试、缺失终局或未解决 finding。
- Go、Review contracts、Windows alignment verification 三组现有 CI 全部通过，证据对应实际 checkout 的 S。
- 最终 review 无未处理的合入阻断；分支已包含最新目标 main，工作树及提交范围可审阅。

## 2. 全部问题与归属

下表保留原 finding ID。没有原 requirement ID 的条目绑定原 validation/acceptance 和公开合同，不伪造 W 编号，也不因数组为空而跳过。

| ID | 已知事实与未知边界 | 唯一实施负责人 | 关闭证据 |
|---|---|---|---|
| T27-F01 | Claude 普通 Permission 场景没有回调；尚不能判断 CLI 自动允许、未调用工具或协议投影问题 | T14；T07/T31 提供合同审阅 | W06-R02/R03；T27-V01 普通 Permission 正式请求/响应及原结果断言；T27-V02 |
| T27-F02 | 同构造的第二个 Agent 在 Close 后被 `run fingerprint changed` 拒绝；变化分量未知 | T06；T04/T14 协作 | W02-R06/R08；指纹差异反例、兼容/不兼容双向回归、真实 cold resume |
| T28-F01 | 六项 CodeBuddy live 测试要求 `Unrestricted`，而 Driver 不支持隔离控制，正确地在启动前拒绝 | T15 | T28-V01 六项真正进入 Driver，原 append、常驻、工具、Todo、冷续接断言全部保留 |
| T28-F02 | 原生登录后 Question 回调仍为 0，只观察到 ToolSearch；原因未知 | T15 | T28-V01 QuestionAnswered、T28-AC01；Question 正式调用、审批 exactly-once 与答案回传 |
| T28-F03 | 原生登录后 native schema 仍 `Valid=false`、RawJSON 为空；缺少该轮正式终局诊断 | T15；T23 提供共享诊断 | T28-V02 live_structured_output、T28-AC01；原生 schema 真正生效且本地验证正确 |
| T29-E01 | Codex auth-only seed 丢失自定义 provider 路由，`OPENAI_BASE_URL` 对本机 CLI 无效 | T16 | 全部 12 条原 T29 要求；无凭据路由反例及原 T29-V01/V02 真实执行 |
| T30-F01 | SDK 使用 CURSOR_HOME，当前官方 CLI 使用不同 config/data 根，Dedicated 资源和会话目录不一致 | T17；共享层变更交 T06/T04 | W02-R01/R03/R06/R08、W09-R09；正式目录契约、实际 MCP/Subagent 与 cold resume |
| T30-F02 | conformance live_run 接受非零退出/空输出的合法失败 Response，无法证明成功模型执行 | T23 | adaptertest 独立失败响应反例；四 Driver 的健康 live success 检查；T30-V02 |
| T30-U01 | Cursor cancel 场景没有观察到预期取消，得到 RunError；原因未保留，workspace trust 只是线索 | T17；T23 审阅成功前提 | T30-V01 CancelPartial、T30-AC01、Result/取消合同；真实 cause、部分审计与旧健康 checkpoint |

## 3. 批次与并行方式

所有 worker 使用独立 worktree；同批从同一已准备 base 开始。每个任务交付自己的提交、回归和局部文档片段。协调者是 task manifest、公共文档、执行状态和集成分支的唯一 writer。

| 批次 | 并发方式 | 工作 | 放行条件 |
|---|---|---|---|
| P0：准备与追踪 | 协调者串行 | 复核远端、保留脏工作树、修订范围、确认认证/模型/诊断和文件所有权 | 9 项均映射到 owner/check；任务包校验通过；没有文件交叉写入 |
| P1：六条修复通道 | T06、T14、T15、T16、T17、T23，最多 6 并发 | 每条先定位/反例，再最小修复、测试、文档 | 每条有明确根因或被证实的测试前提错误，以及修复前失败/修复后成功证据 |
| P2：合流与独立审查 | 协调者合流；非作者并行审查独立子系统 | 合并公共文档，检查跨层影响、需求覆盖和完整 diff | 9 项的实现交付被独立接受；不存在未知根因却标 resolved 的条目 |
| P3：G05 重新冻结 | 串行 gate | 最终文档/任务包入提交，完整测试与 vet，确定候选 S | G05 完成，S 可供所有平台/live 使用 |
| P4：最终验收 | T25/T26 GitHub Actions + T27–T30 本机，最多 6 验收通道 | 同 S 的 Linux/Windows/race/fuzz 与四个真实 provider | 六个 worker 全部接受；原检查及新增回归均通过 |
| P5：G06 与合入交付 | 协调者串行 | 全量证据审计、最终 review、目标分支复核、PR/合入说明 | G06 完成、merge_ready=true、最终 S 无漂移 |

P1 可以并行开发；provider 的最终 live 验收必须使用已经包含 T23 修复的合流版本，不能用旧的宽松 conformance 给 worker 放行。诊断性 live 调用仅用于获得缺失事实，不能替代 P4 的完整原矩阵。

### P0：实施前具体动作

1. `git fetch origin`，记录目标 main SHA、当前分支与用户已有脏文件。使用干净 worktree 实施和测试，不 stash/reset 用户文件，不把 `docs/README.md` 等用户改动混入修复提交。
2. 保持集成分支落在现有 Windows workflow 的 `codex/review-fixes-*` 触发范围；若另开集成分支，应显式 dispatch 现有 workflow 或由 T23 提交精确触发调整。不能因为换了分支名漏跑 Windows 验收。
3. 以当前下一修订号 **R030 / revision 29** 记录本轮范围；实施时若该编号已被占用，则顺延。保留原 47 个任务、96 条要求、owner/verifier 索引、原 G05 依赖和历史 G04 base。协调者只追加实际需要的精确文件路径、验收条款和可追溯修订，不重写旧 disposition。
4. 将本计划条目并入原 task.json 的执行要求/验收及外部 state，保留原检查 ID、包、selector、次数和 fuzz 时长。对尚无 W 编号的四项，增加 finding→原 check/公开合同映射，不暗中删除或挪用已有 W 要求。
5. 冻结测试诊断格式：exit/signal/timeout、RunError Reason 和 Cause 类型、正式终局、协议片段、工具/审批事件顺序、checkpoint 健康性、实际 config/data/profile 根，以及非秘密指纹分量差异。原始完整性断言在进程内完成，归档输出脱敏；凭据、账户、完整用户 profile 不进入仓库。
6. 先以关闭付费门的构建检查验证 Go 模块缓存、四组 live build tags、命令选择范围和 collector。只记录不含凭据的环境信息，不直接归档完整 `go env` 或进程环境。
7. 明确版本与模型：以本轮 CLI 版本作为初始复现基线，Go 验收用 `.github/go-version` 的 1.27.1，最低版本仍为 1.26.8；Cursor 通过已有测试入口使用已经验证可用的 `gpt-5.2`。模型/CLI 更换必须有前提证据、明确记录，不能成为反复刷绿手段。
8. 正式处理包级超时：根据既有逐测试 context、版本探针和 Close 上限计算完整包预算。需要增加 `-timeout` 时，先修订 task.validation.command，再校验 manifest/命令一致性；collector 只增加获准的日志参数和更大的外层回收上限。不得静默放宽单个场景时限，也不得让默认 10 分钟截断原本必须执行的整个矩阵。

### 文件冲突控制

| Writer | 本轮主要写域 | 必须串行协调的边界 |
|---|---|---|
| T06 | `invocation_fingerprint.go`、`tools_profile_snapshot.go` 等根包兼容计算及对应 root 回归；实际所需共享 profile 文件 | 新拆出的文件未全部在原 T06 allow 中，须先精确扩权。`internal/hostedprofile`、`internal/skillruntime`、`internal/mcpruntime` 同一时段仅一个 writer |
| T14 | `claude/**` 及自己的 handoff | T07 作为合同协作者，不同时改 Claude 文件；T06 不直接改 Claude live fixture |
| T15 | `codebuddy/**`；确有需要时原允许的 CodeBuddy agent 物化文件 | `internal/profileagents/agents.go` 若被其他 provider 需要，协调者安排独立串行变更，不能双方各自修改 |
| T16 | `codex/**` 及自己的 handoff | 不手工编辑 generated schema/client；不为 live 路由新增根包消费者 API |
| T17 | `cursor/**` 及自己的 handoff | 共享 profile 的实现需求交 T06/T04；不能私自改共享 helper 与另一通道抢文件 |
| T23 | `adaptertest/**`；必要的 `.github` 验收脚本/CI 范围 | 其他 owner 不直接改共享 suite；脚本新增路径须纳入 manifest；不得顺手降低 CI 阈值 |
| 协调者 | manifest、AGENTS、CHANGELOG、集中 docs、外部 state | 按实际公共语义整合各通道文档；用户原有脏文档单独保留 |

共享问题需要跨写域修改时，由协调者先分配唯一 writer、调整任务范围并重新验证。不能让同批任务等待另一个尚未交付的未冻结 API；有这种依赖就拆为前后两段合流。

## 4. 各修复通道的实施与验收

### T23：先使“live 成功”判定可信

1. 将本轮 `Response{ExitCode:1}, nil error` 的无模型反例变成独立回归，覆盖非零退出、signal、timeout、Response.Failure 和 Go error。
2. 保持通用 SPI `VerifyOutcome` 对合法失败响应的支持；在专门的 live success 检查中要求真实成功，不能通过改变 Driver 错误合同掩盖测试问题。
3. 成功探针必须验证其明确请求的结果。不能对所有 Result 强制非空 Text；只有该探针明确要求文本/nonce 时断言对应内容。checkpoint 要求按 resume 能力和 SessionCodec 合同适用，不能强加给不支持会话的第三方 Driver。
4. native schema 检查保留 `Valid`、结构和值约束，并在失败时保留脱敏正式终局及 Raw 诊断。已声明支持的能力不能用空输出、无 checkpoint 或静默失败凑成功。
5. 以合成失败/成功 Driver 分别验证断言，并证明错误判定能捕获现有反例；现有失败响应、生命周期和公共转换测试继续通过。
6. collector 使用完整 `package:test` 标识；对必须执行、允许 unsupported skip、普通 CI 禁用 live 三类清楚区分，检查每个 run 都有终局。只列明确不支持的 quota/rich-stream/native-schema skip，不能把本次必需场景加进白名单。

关闭：T30-F02 反例使旧 oracle 失败、新 oracle 正确拒绝；四 Driver 的最终 live conformance 在新 oracle 下通过。允许的 skip 不被当成真实模型成功。

### T06：修复 Claude 冷续接兼容性

1. 在同一 cfg/store/identity/workspace/Tool Revision 下记录首次资源解析、成功归档、Close 后、第二次资源解析的非秘密兼容分量及文件清单。先确定是哪一项变化，不能先删字段再测试。
2. 将实际变化归类：真实配置漂移、SDK endpoint/token 轮换、provider 运行时元数据、资源物化不稳定或序列化差异。由 provider owner 提供正式字段语义，core/helper 不猜协议或任意忽略 JSON 字段。
3. 只规范化已经证明不改变续接语义的变化；保留真正影响模型、workspace、profile、Tools/Revision、skills、instructions、MCP、runtime services 和 append 原字节的维度。
4. 加入修复前可复现的确定性 fixture：首次成功、Close、同 key 的第二 Agent `ResumeOnly` 成功。增加不同进程重建的兼容证据，防止仅靠驻留对象或内存路径通过。
5. 增加反向用例：真正修改任一相关配置仍拒绝复用；失败/不完整 checkpoint 不覆盖健康状态；旧 writer 先有界退出；旧 gateway 已关闭；token/URL 的合法轮换不造成虚假不兼容。
6. 检查既有持久记录兼容性。没有实际原因不能让所有旧 checkpoint 都失效；需要格式/语义修订时明确范围，遵守既有安全 continue-or-start 及原子 Finalize，不增加新消费者身份或兼容 shim。

关闭：T27-F02、W02-R06/R08 的确定性回归与实际 Claude cold resume 均通过；相关 root/profile race 和 Windows 所有权检查仍通过。

### T14：补齐 Claude 普通 Permission

1. 针对原失败场景记录真实 argv、正式工具调用、`control_request/can_use_tool`、审批响应和 provider 终局，分辨“根本没有请求”与“有请求但 SDK 丢失”。
2. 若 CLI 合法自动允许该无害命令，则修复测试的审批前提：在私有 workspace/profile 中使用官方明确会请求批准的配置或无害操作。保留真实 Bash/工具执行、exactly-once、Approve 后继续及最终内容断言，记录旧 fixture 为什么没有建立审批条件。
3. 若正式请求已发出，则在 Claude owner 内修复解析/派发/控制响应。不得伪造 ApprovalRequest 来满足测试，不用回调次数替代正式协议事实。
4. 保留 nil schema 的既有 Permission Ask；Question/PlanReview native schema、Permission Ask 的既定 schema fallback 规则均不得退化。
5. 增加拒绝、超时、取消、重复应答及 provider 提前终止的确定性边界，复用现有覆盖而非复制第二套审批策略。对应 race 检查沿用任务原次数。

关闭：原 T27 全选择器通过；成功 schema/HITL、常驻、多轮、WithSpawn、Raw/Transcript/Terminal 与部分 Result 继续保留。

### T15：完整修复 CodeBuddy 三个条目

**Sandbox 前提**：将 live headless fixture 中与测试目标无关、且明确不受支持的 `Unrestricted` 改为继承/未指定隔离语义，保留原 AutoApprove 与所有业务断言。增加负向断言证明用户显式请求不支持的隔离仍被拒绝；不把 Descriptor.Isolation 改成 true 来迁就测试。原六项必须实际进入执行并验收。

**Question**：先保留该轮正式工具目录、ToolSearch 结果、AskUserQuestion 调用/控制帧及最终结果。若是正式工具未被提供，修复已证实的 CLI/config/fixture 前提；若已发出而投影丢失，修复 Driver。保持恰好一次 Question 回调、Answer 回传、结果体现答案及错误路径。不能把成功结束但未问问题当成功，也不能用宿主伪造 Question。

**Native schema**：记录实际 print/json/schema 参数、exit、provider Failure、正式 terminal 的 native 输出字段，以及解析后的结构化对象。分别构造 CLI 拒绝、畸形终局、缺失值和有效值的回归，定位正式输出缺失还是映射丢失。只有官方 native 约束确实执行时才报告 native 成功；Prompt fallback 不能冒充 native conformance。

已具备的认证使用原生 access token 的最小内存投影和已确认的服务路由，不再次使用已证无效的环境 API key。版本、模型选择及 required 能力需要记录。若当前 CLI/模型确实无法提供已承诺能力，应保持阻断并找到受支持的正式执行路径，不能以削减能力声明关闭问题。

关闭：T28-F01/F02/F03 全部有确定原因和对策；T28 原 11 项 live 与 conformance 通过，四个已通过的 Permission/Plan 场景不回归；T33 stderr 交接、常驻单 writer、部分 Result 和 Wait cause 保护保留。

### T16：使 Codex 可以安全执行完整矩阵

1. 为 live helper 增加**显式、最小、受限**的路由输入。优先复用现有 `Config.ExtraArgs` 的官方 `-c` 投影；保留当前选中 provider 的必要 name/base_url/wire_api/requires_openai_auth 等配置，不把自定义 provider 偷换成默认 OpenAI。
2. 输入使用测试专属 fixture/runner 通道，不增加公共构造选项。不复制完整用户 config、MCP、features、history、prompts 或任意命令参数；认证仍只进入私有 profile/受控内存。未知或无法安全表达的关键路由字段必须明确失败。
3. 确保 exec 和 app-server 两条路径接收同一份路由配置，并进入现有构造配置/会话兼容语义。保持 reserved prompt/policy 参数保护，不能靠 PATH wrapper、流量代理改写或全局配置把错误掩盖掉。
4. 先用人工 dummy key、只响应的 loopback endpoint 和只拒绝的代理验证：两条 transport 都命中预期路径，未尝试默认服务，认证不发往其它目标；路由缺失/畸形/不兼容在实际请求前被拒绝。保留旧反例。
5. 两个正式 live helper/conformance 复用该输入，模型与 expected-model 保持一致。之后执行原完整 T29-V01/V02，包括 exec/app-server append、常驻观测、子代理、取消/配置漂移、Dedicated 冷续接与原生 schema。

关闭：T29-E01 消除，原两个 check 实际执行，12 条原要求逐项有真实证据；gate canary 和无匹配测试的子包不算模型证据。

### T17：修复 Cursor 目录、资源与取消前提

1. 冻结并测试配置优先级：显式 SDK Profile 选择对其实际配置/数据目录具有权威性；没有显式选择时遵循已核实的官方 config/data/XDG/HOME 规则。已有 CURSOR_HOME 的 SDK 选择语义要明确映射到官方入口，不能让历史调用方静默失效。
2. Dedicated 模式将实际 config 和 data 根约束在受管理目录内，资源物化、CLI Env、Inspect、fingerprint、会话文件与 Close 所有权观察同一组实际路径。冲突的环境变量不能把 provider 偷带回日常 HOME。
3. Native 合法分离 config/data 路径时，必须如实建模相关资源与会话状态；不支持的选择冲突执行前明确拒绝。完成优先级矩阵和旧记录兼容审阅后再实现，不仅给 live runner 临时补变量。
4. 用安装版官方 CLI 复验目录选择，并用 fake CLI/正式目录函数反例验证两组根、冲突 env、Native/Dedicated、不同 identity、残留会话、source profile 不污染。验证 MCP、skills/subagent 文件真正被 provider 读取，而不只是“SDK 已写入”。
5. 保留官方 print 协议，不为解决目录问题迁移 ACP 或改变公开执行合同；append 仍明确 unsupported，不伪造 Todo/能力观测。
6. 取消场景先保留 RunError Reason/Cause、stderr、正式终局和首个 TextDelta 的时间顺序。验证新 workspace 是否被 CLI 信任；若缺合法前提，用既有受支持的 policy/官方配置建立测试前提，不能无条件对所有 SDK 调用加 force。
7. 在真实开始执行后触发 Cancel，验证 cancellation cause、部分 Raw/Transcript、无有效失败 checkpoint、旧健康状态未被覆盖，且 Result/Events/资源回收有界。不能接受任意 RunError 作为取消成功。

关闭：T30-F01/U01 以及原 T30 全矩阵通过；实际工具调用、正式 MCP/Subagent 事实、跨 Agent 冷续接、取消审计均成立。conformance 必须使用修正后的 T23 oracle。

## 5. 合流、回归与 G05 冻结

协调者检查每个提交的实际 diff、范围和证据，再逐个合流。建议先合并 T23，再合并 T06 和四个 provider；每次涉及共享层的合并执行对应 root/provider 组合检查，最后执行全量。发现跨通道冲突回交原 owner，协调者不顺手重写未经审阅的 provider 逻辑。

独立审查由非作者完成，重点包括：实际 profile 与 fingerprint、错误主因/次因、审批 responder、单 writer/关闭、失败 checkpoint、Run/Stream 等价、共享 conformance 的正负断言和认证隔离。源码 review 与测试结论分别记录，不能相互替代。

原 owner 的验证命令全部保留，并补充本轮反例对应检查。新测试要能捕获已知旧行为；对跨 pipe、取消及 timeout 的验证继续使用实际接收/同步证明，不靠 sleep 或放宽 deadline。

集中整合 godoc、Profile/Tools/HITL/structured output 使用文档、provider 文档、CLI 兼容边界、五语言 README 的适用说明和 CHANGELOG。若没有公共 API 变化，AST golden 应保持不变；发现需要公共变化时先完成合同审阅及精确修订，不自动更新 golden。

最终提交 **S** 形成后执行 G05 原命令：

```sh
python3 docs/alignment-tasks/2026-09-07/validate.py
go test -count=1 ./...
go vet ./...
```

另执行任务包 `--verify-git`、校验器自身回归、root/Driver AST golden 及必要的合流回归。普通命令显式设置 live/E2E/golden-update 三门为 `0`。从最后一轮中央文档提交后开始计算 S，不能先测代码再追加文档仍沿用旧 SHA。

## 6. 同一 S 的平台及真实验证

### CI / 平台

| 通道 | 必须保留的检查 |
|---|---|
| Go workflow（6 jobs） | minimum-go 1.26.8 全量 test/vet；1.27.1 validate；Linux race；9 个各 30 秒 fuzz；windows-latest；frontend build/lint 与生产/全依赖 high 审计阈值 |
| Review contracts（3 平台） | Ubuntu、windows-latest、windows-2022；原 ACL/append、原字节资源、审批 cause、Codex 参数及 EOF、100 次主动预算边界、20 次冷续接/旧网关负例 |
| Windows alignment verification（2 jobs） | ubuntu-24.04 的完整 T25 + windows-2022 的完整 T26；collector 前后源码快照、失败时仍上传制品 |
| T25 | 原 15 个 V 检查：全量/vet/race、关键包 20 次、S1–S9、四 Driver conformance、9 个 fuzz；另保留 X01–X03 两个可执行示例和全部 examples 编译 |
| T26 | 原全量测试；X01–X05 的示例/编译、20 次取消审计、20 次 Cursor 合同；原生 ACL、禁止 delete-sharing、进程树、argv 与 profile 生命周期证据 |

上述 11 个 jobs 是当前工作流基线；实施若增加 job，最终所有应运行的 job 也必须通过。不能只检查 workflow 总体图标。下载并审计日志、测试身份、次数、skip 和 artifact hash。

GitHub Actions 的实际 checkout SHA 必须核对为 S；PR 的临时 merge SHA 若不同，应另行标明，不能仅凭 API 显示的 head 字段就认作 S 的原生证据。普通 CI 只编译/检验 live 门，不消费 provider 凭据。

### 本机四 provider

四个 worker 使用 S 的独立干净 worktree、私有 HOME/profile/workspace、独立 Thread key 和端口。沿用本次已确认的最小认证接入；原登录和用户工作区保持原状。CLI 路径、版本/二进制标识、Go 版本、模型、非秘密路由标识随证据记录。认证刷新或 CLI 变化需重新确认前提，不能把旧失败覆盖掉。

原八个检查仍是完整矩阵：

```sh
go test -count=1 -tags=claude_live ./claude -run TestAlignmentLive
go test -count=1 -tags=claude_live ./claude -run TestClaudeDriverConformance
go test -count=1 -tags=codebuddy_live ./codebuddy -run TestAlignmentLive
go test -count=1 -tags=codebuddy_live ./codebuddy -run TestCodeBuddyDriverConformance
go test -count=1 -tags=codex_live ./codex/... -run TestAlignmentLive
go test -count=1 -tags=codex_live ./codex -run TestCodexDriverConformance
go test -count=1 -tags=cursor_live ./cursor -run TestAlignmentLive
go test -count=1 -tags=cursor_live ./cursor -run TestCursorDriverConformance
```

命令在同时具备 build tag 和 `AGENT_ADAPTOR_LIVE_CONFORMANCE=1` 的授权子进程中执行；E2E/golden-update 保持 `0`。可以增加 `-json` 收证；包级超时只能使用 P0 正式修订后的值。不得缩窄 selector、删包、降低次数、移除原测试或把新测试移出选择范围。

验收逐项检查实际模型/工具/审批/终局，而不是只看 exit 0。旧 conformance oracle 的通过不沿用。所有正式支持的能力必须有证据；unknown、disabled、fixture、模型未调用、provider 已拒绝、空矩阵均不能替代真实成功。

## 7. 失败处理与证据纪律

- 每次执行记录 attempt、完整命令、模型/CLI、S、开始结束时间、退出码、每个测试终局、允许的 skip、源码前后哈希和脱敏日志。原始失败不覆盖、不改名成成功。
- 凭据、路由、模型或模块缓存前提有**明确新证据**时可以修正环境后重新执行完整矩阵；保留修正前后两次。相同条件反复运行直到偶然通过不构成修复。
- 原因未知的正式失败先增强诊断，归属明确后交原 owner；不随意扩大生产修复范围。新增 finding 必须有唯一 owner、关闭 check 和依赖，不成为“后续再说”的遗漏项。
- P4 中发生任何源码、测试、受检文档或任务包改动，都回到 owner/P2/P3，形成新的 S'，最终重新收齐同 S' 的 G05、T25–T30。旧 S 的证据仅保留为历史。
- 环境暂不可用则该 worker blocked，其他独立通道继续；门禁保持未通过。已有授权不重复询问，不把缺配额或能力真实失败降级为 skip。
- 外部报告和状态不提交到其自称已验收的源码提交，避免 SHA 自引用。原 delivery、旧失败和完整 96 条要求保留；只在相应 gate 重新接受依赖后恢复当前状态。

## 8. G06、最终 review 与交付

1. 独立核对六个 B06 报告、全部必需 checks、原 96 条要求的实施/验证、9 项修复、新 findings、45 个提交 disposition、文档/API/golden 和所有 artifact hash。
2. 只有所有 worker 已接受，才执行 G06-V01 并形成 G06 result；报告结构校验不能冒充实际平台/live 验收。
3. 再次 fetch `origin/main`。若目标分支变化且未被 S 包含，先处理集成，再形成新候选并按上节重新验收；不以“合并无冲突”代替行为验证。
4. 以最新 `origin/main` 为基线复查整个合入 diff，同时针对本轮修复做独立 review。确认无未处理 P0/P1、无违反冻结合同的 P2、无公开功能削减、无隐藏降级和不相关文件混入。
5. 准备或更新 PR/合入材料：问题与最终行为、所有 breaking changes（如有）、S、CI/平台/live 证据、受支持 CLI/模型与明确 unsupported 边界、旧失败到修复的映射。标题和正文按最终实现重写。
6. 输出 `merge_ready=true` 所依据的检查清单、最终 S、目标 main SHA 和未完成项为空的报告。实际 merge/tag/release 仍是之后的独立动作。

最终应交付：修复提交、每项独立回归、更新后的任务包/合同与使用文档、同 S 的 G05/T25–T30/G06 报告、最终 review、合入说明。完成顺序以证据与批次门禁为准，不预设未知根因的修复时长。

## 9. 首轮执行清单

- [ ] P0：创建干净实施 worktree、复核远端、精确修订任务范围和验证命令。
- [ ] P1：派发 T06/T14/T15/T16/T17/T23 六通道，分别覆盖上表全部 9 项。
- [ ] P2：合流、独立 review、集中 docs/CHANGELOG/合同同步。
- [ ] P3：G05 原命令通过，冻结最终 S。
- [ ] P4：T25/T26 原生 Actions 与 T27–T30 本机真实矩阵全部接受 S。
- [ ] P5：G06 通过、目标 main 无漂移、最终 review 关闭，交付可合入结论。

本清单全部未勾选；本次只输出计划，不把计划文件的存在计作修复或测试完成。
