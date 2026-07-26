# agent-adaptor v1 实施计划

> 配套设计文档：[api-v1-redesign.md](./api-v1-redesign.md)（"什么/为什么"）。本文是"怎么做/什么顺序/怎么验收"。
> 基线：main @ bbba7a0；根包 17,522 行 / 54 个 Go 文件；Go 1.26；CI：`.github/workflows/go.yml`。

---

## 0. 决策记录（Decision Log）

| # | 决策 | 状态 | 翻案窗口 |
|---|---|---|---|
| D1 | 业务失败并入 error（`*RunError` 携带完整 `Result`），删除 `RunResult.Failure` 双层判定 | **采纳**（方向已确认） | P0 结束前（`Result`/`RunError` 定型后翻案成本陡增） |
| D2 | 审批请求自带应答器（`ApprovalRequest.Approve/Deny/Answer`），删除 `ResolveDecision` requestID 往返与 3×2 typed handler | **采纳**（方向已确认） | P1 结束前 |
| D3 | 根包 package name `agentadaptor` → `adaptor`（import path 不变） | **采纳为默认** | P5 大挪移前零成本翻案（纯文本替换） |
| D4 | `Stream` 定义为小接口（`Events`/`Result`/`RunID`/`Cancel`），而非具体结构体 | 采纳（S9 分析反哺） | P1 |
| D5 | `delegation.Service` 一体化入口 + `delegation.Local/Remote` 双目标 + `SubagentUpdate` 事件入主流 | 采纳（S9/§9.8） | P4 |
| D6 | 实施策略：**绞杀者路线**（内核抽取 → staging 包并行生长 → 终局大挪移），不做同包新旧共存 | 采纳（本文 §1） | 立即生效 |
| D7 | Option 双作用域的编译期约束具体类型设计 | **已定案（P0.1 spike）**：案 A 三接口 `Option` / `CallOption`（不嵌入 Option）/ `SharedOption`，双向误用编译错；`AgentSettings` 内嵌 `RunSettings`，字段不导出、扩展面为精选导出方法。详见 [p0-option-scope-decision.md](./p0-option-scope-decision.md)。连带定稿：`WithModel`/`WithTimeout` 为双作用域（已回改方案 §2.3）；`a2a.ServerOptions.Options` 类型为 `[]adaptor.CallOption` | 已关闭 |
| D8 | 结构化输出模式词汇归属（根包常量 vs `schema` 子包） | 待定（默认根包常量 `adaptor.SchemaStrict` 等，少一个包） | P3 |
| D9 | `providers/` 包去留 | **删除**（P0.7 裁决：全仓唯一引用者是自身测试，自述 opt-in sugar；Required 能力在 skill.Provider 合同中保留，宿主 10 行 wrapper 等价）。迁移指南记一行 | P5 前若产品异议，归宿为 `skill.MarkRequired` |
| D10 | `runtimeservice/` 包去留 | **删除**（P0.7 裁决：v0.5 的宿主兼容 mixin，与 runtime.go / RuntimeServiceRef 零代码关系；v1 `WithServiceManager` 是全新契约，无存量宿主需要垫片） | P4.5 |
| D11 | `Identity` 归属与字段集 | 归**根包** `adaptor.Identity` + `IdentityFromContext`（消费方横跨 skill/workspace/services 三域，不进 skill 包）；现状四字段（ID/Tenant/Profile/Name）vs 设计稿两字段是能力缩水，**字段集 P0.5 定案**（默认保四字段） | P0.5 |

---

## 1. 总体策略：绞杀者路线

同包新旧共存不可行（`Option` 等核心名字直接冲突），一步到位全量重写不可评审。因此分三步走：

1. **内核抽取（P0 前半）**：把根包的执行管线（合并默认值 → 组装 → 会话协调 → 执行 → checkpoint → 归档）原样搬进 `internal/engine`，旧公共 API 降级为薄包装。**零行为变化，全量现有测试零修改通过**——这是整个计划最重要的减险动作：此后新旧两套 API 是同一个内核上的两张脸。
2. **staging 包并行生长（P0–P4）**：新 API 在 `next/` 目录（package name 即为 `adaptor`）生长，与旧 API 互不干扰；每个阶段以设计文档 §3 的场景测试（S1–S9）为验收锚点。旧 API 与旧测试在此期间**保持绿色不动**，持续兜底。
3. **大挪移（P5）**：`next/` 内容平移至根目录，删除旧 API 文件与旧测试，文档全量重写，打 `v1.0.0` tag。模块路径 `github.com/agent-dance/agent-adaptor` 不变（当前处于 v0，语义化版本允许直接切 v1，无需 `/v2` 后缀）。

分支与发布节奏：

- 长驻分支 `v1`，每阶段 1–3 个 PR 合入 `v1`；`main` 冻结为 v0.x 维护线，切换前打 `v0.9.0`（或顺延号）冻结 tag。
- CI 扩展：`go.yml` 增加 `next/...` 与 `driver/...` 的 build+test+vet；保持现有全仓测试矩阵；P1 起对事件流跑 `-race`。
- 每阶段合入门禁（统一）：① 该阶段场景测试绿；② 旧 API 全量测试仍绿（P5 前）；③ `go vet` / race 干净；④ 设计文档 §4 能力映射表中该阶段负责的行逐条勾验。

---

## 2. 阶段分解

### P0 · 内核抽取 + SPI 分离 + 新根骨架

**目标**：地基。engine 独立、SPI 出根包、新 API 的 `Agent/Option/Result/RunError` 四件套可用，S1/S2/S4 场景跑通。

> **✅ P0 已完成（2026-07-26）**。提交序列：93cabb0（driver SPI，四驱动零修改、导出面 381 符号零变化）→ 7f4cf9a（next/ 四件套 + S1/S2/S4 + RunError 三路径表驱动）→ a129602/0d2d293/eac458f/a364d46（engine 四批抽取，engine 仅 import driver 无环，根包降薄壳 19 文件）。全部门禁绿：现有 _test.go 零修改、导出符号 AST 逐符号比对零增删改、全仓 build/vet/test 通过。偏差记录见各批提交与本文档 git 历史。

| 任务 | 内容 | 触及现状 |
|---|---|---|
| P0.1 | **Option 双作用域 spike**（D7）：原型两案——(a) 双接口（`Option` 含构造+双作用域、call 参数收窄接口）；(b) 单接口 + 构造/调用点运行时校验（报错信息含正确用法）。以「误用是否编译期报错、godoc 呈现、可否由生态包扩展」三条评分定案，结论写入本表 | — |
| P0.2 | `internal/engine` 抽取：run 管线、选项合并、会话协调、checkpoint。**波及面修正（P0.7 盘点）**：engine 不能回 import 根包，管线引用的全部合同类型必须随迁并别名回指，实际波及 ≈ 全部 32 个非测试文件（逐文件路线图见 [p0-inventory.md](./p0-inventory.md)）；`archive_*.go` 是 skill 归档源，**不在本任务**（归 P3.2） | 根包全量（见 p0-inventory.md 映射表） |
| P0.3 | `driver/` SPI 包：`Driver`/`Descriptor`/`Request`/`Response`/`EventSink` + 10 能力接口；旧根包以类型别名回指（`type DriverAdapter = driver.Driver`），旧 API 编译不变 | `api.go`（SPI 部分） |
| P0.4 | 四个驱动包（codex/claude/cursor/codebuddy）经 shim 实现 `driver.Driver`；`adaptertest` 内部改走 driver 接口（对外签名暂不动） | `codex/` `claude/` `cursor/` `codebuddy/` `adaptertest/` |
| P0.5 | `next/`：`New(d driver.Driver, opts ...Option) *Agent`、`Agent.Run`、`Result`（高频平铺 + `Raw()`/`Transcript()`/`Services()`/`Decode()`）、`*RunError` + 哨兵错误、`Runner` 接口、`type Driver = driver.Driver` 别名；**Policy{Sandbox,...} + 预设**（S4 门禁需要，从 run_policy.go 前移，不等 P1）；**`Identity` 四字段定案（D11）**；选项三接口按 D7 骨架落地 | 新增；语义来自 `run_types.go` `errors.go` `run_policy.go` `caller_identity.go` |
| P0.6 | 场景测试 S1（一次性任务）/S2（多 agent 流水线）/S4（批量 worker 双作用域覆盖），基于 fake driver（`internal/testutil` 扩展） | `internal/testutil` |
| P0.7 | ✅ 已完成：[p0-inventory.md](./p0-inventory.md)（54 文件逐一映射 + 四裁决 D9/D10/D11 + `run_policy.go` 四分方案）。注意：`EffectiveHumanDecisionPolicy` 被 claude/codebuddy 4 处调用，须经 driver 包保留等价物 | 清单已产出 |

**门禁**：现有全量测试零修改通过（P0.2–P0.4 的硬指标）；S1/S2/S4 绿；`errors.Is/As` 全路径（业务失败/取消/进程崩溃）有表驱动测试。
**回退**：engine 抽取是纯机械移动 + 薄包装，任何阶段可整体 revert。

### P1 · 事件流合一：Stream / Event / ApprovalRequest

**目标**：`Agent.Stream` 返回接口型 `Stream`（D4）；一条事件流承载语义流 + 操作事件 + 审批；HITL 双形态可用。S3 跑通。

| 任务 | 内容 | 触及现状 |
|---|---|---|
| P1.1 | Event 类型族：`TextDelta` `Thinking` `ToolCall` `ToolResult` `RunStarted` `RunFinished` `ProcessInfo` `Notice` `Dropped` `SubagentUpdate`（类型先立，注入口 P4 接）| `run_types.go`（StreamPayload/RunEvent 语义） |
| P1.2 | engine sink → typed event 翻译层；背压语义原样保留（默认丢弃 + `Dropped{Count}`，可选阻塞），`WithEventBuffer`/`WithBlockingEvents` 构造选项 | `stream_internal_test.go` 迁移 |
| P1.3 | `ApprovalRequest`（三 Kind 合一，自带 `Approve/Deny/Answer`，kind 专属数据字段组）+ `OnApproval` 回调形态 + 事件形态 + `Policy.Approvals`（超时/重试/兜底沿用 `HumanDecisionPolicy` 语义）+ 预设 `ApproveAll`/`DenyAll` | `decision_types.go` `run_policy.go` |
| P1.4 | `Run` 内部复用 Stream 管线（`Run` = drain + `Result()`），确保单一执行路径不变式 | — |
| P1.5 | 合同测试迁移：streaming 合同（`stream_internal_test.go`）、HITL（`runner_hitl_integration_test.go` `runner_decision_test.go`）到 next 版本；全事件族 `-race` 测试 | 同名测试 |
| P1.6 | 场景测试 S3（Web 聊天：Thread 占位用无状态 Agent，SSE 部分推后到 P4） | — |

**门禁**：S3 绿；HITL 三 Kind × 双形态 × 超时兜底矩阵测试；`Dropped` 语义与现状 `BackpressureDropStream` 行为逐项对齐；race 干净。
**风险**：事件顺序/关闭时序回归 → 以现状合同测试为基线先写「行为快照」再动手。

### P2 · Thread + threadstore

**目标**：四层 ID 归两层。`Thread`/`NewThread`/`ResumeOnly`/`Fork`/`Checkpoint`；`threadstore.Store` 承接 SessionStore 全部能力。

| 任务 | 内容 | 触及现状 |
|---|---|---|
| P2.1 | `threadstore/` 包：`Store` 接口（resolve/finalize/lease 三能力，语义 = 现状 `SessionStore` 五方法）；`memory/` 适配为双实现（过渡期同时满足新旧接口） | `session_types.go` `memory/` |
| P2.2 | `Agent.Thread(key, ...)`/`NewThread`/`Fork` → 4 种 SessionMode 的方法化映射；`Thread` 实现 `Runner` | `session.go` `sdk_session_test.go` |
| P2.3 | `Thread.Checkpoint()` 暴露驱动 resume 句柄（审计用）；session codec 保留为 `driver.SessionCodec` 能力接口 | `session_codec.go` |
| P2.4 | 合同测试迁移：`sdk_session_test.go` `sdk_start_session_test.go` `runner_session_internal_test.go` `session_codec_internal_test.go`；lease 并发防护测试原样搬 | 同名测试 |

**门禁**：mode→方法映射矩阵测试（含 fingerprint/fork 边界）；S3 升级为带 Thread 的完整版；lease 竞争测试 race 干净。

### P3 · 词汇包 + 驱动配置回家 + 结构化输出 + Inspect

**目标**：消费者视野里的名词全部就位。

| 任务 | 内容 | 触及现状 |
|---|---|---|
| P3.1 | ✅ 已完成（提前并行，提交 275e8a1）：四驱动包各得 `Config`（别名指向根包公开别名）+ `Driver(cfg) driver.Driver`（configuredDriver 嵌入现有 adapter，能力接口经方法提升自动保留；req.Config==nil 时注入构造期 cfg，显式 Config 不覆盖）；现有入口与既有测试零修改；P5 翻转别名真身方向 | `config_types.go` 四驱动包 |
| P3.2 | `skill/` 包：`Dir`/`FS`/**`Archive`**/`Inline`/`Key` + `Provider`/`Materializer` 接口；`WithSkills`/`WithSkillProvider`/`WithSkillMaterializer`；追加合并语义、Required、冲突检测、严格物化全部保留。**收编 `archive_*.go`**（P0.7 勘误：它们是 skill 归档源 zip/tar/tgz，非 run 归档；`skill.Archive` 构造器补齐能力保全缺口） | `skill_*.go` 5 个文件 + `archive_*.go` + `internal/skillruntime` |
| P3.3 | `mcp/` 包：`HTTP`/`Stdio`/`Server`；`WithMCP` 替换语义 + profile 物化 + fingerprint 不变 | `mcp_types.go` `internal/mcpruntime` |
| P3.4 | `profile/` 包：`Native`/`Dedicated`/`CloneNative`/`CloneFrom` + `LinkAuth`；`profile.Resources`（agents/hooks/instructions/config patch）；真话物化汇报 | `profile.go` `profile_resources.go` `internal/profile*` 8 个包 |
| P3.5 | 结构化输出：`RunAs[T]`（接受任意 `Runner`）+ `WithSchema[T]` + 三模式（D8 定名）；能力矩阵校验、启动前失败语义不变 | `structured_output.go` |
| P3.6 | `Inspect()` 面板（Environment/Models/Quota/ConfigSchema/Skills）+ `ProfileState`/`SyncProfile`/`SelectSkills` | `admin.go` `admin_helpers.go` |
| P3.7 | 合同测试迁移：`skill_contract_test.go` `skills_sdk_test.go` `mcp_sdk_test.go` `structured_output_test.go` `admin_profile_test.go` `profile_resources_test.go` | 同名测试 |
| P3.8 | 场景测试 S5（issue 分类器）/S7（onboarding）/S8（租户 profile） | — |

**门禁**：S5/S7/S8 绿；skills/MCP/profile 合同测试全量迁移且与旧版行为逐项对照；`WithSkills` 追加 vs 其余替换的合并语义表驱动测试。
**说明**：P3.2–P3.4 三个词汇包相互独立，可并行三条支线。

### P4 · bridges / hosttools 适配 + delegation.Service

**目标**：传输层与宿主组件换新词；S6/S9 跑通；examples 全量重写。

| 任务 | 内容 | 触及现状 |
|---|---|---|
| P4.1 | ✅ 已完成（提前并行，提交 4deabe7）：7 包提升至顶层，测试随迁新路径全绿；旧路径转发包全集镜像（Err 哨兵用 var 转发保 errors.Is 同一性，Deprecated 标记，P5 删）；三个 import 守门测试路径重锚定 + forbidden 列表全部补齐 codebuddy；CI repeat/race A2A 步骤改指新路径 | 目录移动 + 守门测试 |
| P4.2 | `agui`：`Events(stream)` 基于新事件族重写状态机；capability 降级逻辑保留；`internal/aguiversion` 守门测试随迁 | `pkg/bridges/agui` |
| P4.3 | `sse.Handler(runner, Options)`：接受 `Runner`（Agent/Thread 同构）；断连取消语义不变 | `pkg/bridges/sse` |
| P4.4 | `a2a.NewServer(runner, ServerOptions)`：`Session: Stateless()/ThreadByContextID()`；ExposurePolicy 脱敏、TaskLifecycle 不变；`ServerOptions.Options []adaptor.Option`（调用作用域） | `pkg/bridges/a2a` |
| P4.5 | **`RuntimeServiceRef.MCP` 类型化**（`*mcp.Server`）；迁移期兼容解析旧 `agentadaptor.mcp.*` metadata key（P5 删）；`runtimeservice/` 按 D10 **确认删除**（非改造——它与 RuntimeServiceRef 零代码关系）| `runtime.go` `workspace_skill_types.go` `internal/mcpruntime` |
| P4.6 | **`delegation.Service`**（D5）：Registry+EventBus+Delegator+per-run MCP sidecar+结果记录一体；`delegation.Local(key, runner, policy)` / `delegation.Remote(key, cardURL, policy)`；`team.Option()`；`team.Result(runID, key)` | `pkg/hosttools/a2adelegation` |
| P4.7 | **宿主事件注入口**：engine 提供 per-run 的 host-event 注入内部 API，`SubagentUpdate` 汇入主流；`subagentstream` 降级为兼容层。**兜底**：若注入侵入面超预期，退回 `subagentstream.Merge(stream, bus)` 桥接方案，S9 文档同步改写 | `pkg/bridges/subagentstream` + engine |
| P4.8 | `sessionrecorder` 适配新事件族 | `pkg/hosttools/sessionrecorder` |
| P4.9 | examples 全量重写（见 §4 清单）；`examples/showcases/team-agent-workflow` 按设计文档 §9.7 形态落地（**依赖协调**：`cl/opt_examples` 分支先合入 main 或直接在 v1 分支重写，二选一，倾向后者） | `examples/` 14 个 |
| P4.10 | 场景测试 S6（A2A 发布）/S9（团队协作，fake driver 版做进 CI，live 版留 example） | — |

**门禁**：S6/S9 绿；AG-UI 前后端版本守门测试通过；`delegation.Local` 与 `Remote` 的行为对照测试（同一角色两种注册方式，事件序列与结果等价）；每个 example 在 CI 至少编译、fake-driver 类 example 可执行。

### P5 · 大挪移 + 删旧 + 文档 + 发布

**目标**：`v1.0.0`。

| 任务 | 内容 |
|---|---|
| P5.1 | 冻结 v0：main 打 `v0.9.x` tag；`v1` 分支 rebase 收口 |
| P5.2 | 大挪移：`next/` → 根目录（package `adaptor`，D3 最后确认点）；删除旧 API 54 个根文件中被取代者与旧测试；`pkg/` 转发包删除；旧 metadata key 兼容解析删除；`providers/`（D9）与 `runtimeservice/`（D10，若 P4.5 未删）移除。**检查项**：11 个 import 根包类型的 internal 包 + 四驱动包的引用 repoint 到新家（别名删除前逐包核对，清单见 p0-inventory.md 盲点核查节） |
| P5.3 | `adaptertest` v1：面向 `driver.Driver` 的一致性套件（能力声明真话性、事件时序、会话 codec、结构化输出矩阵）；四内置驱动 + fake driver 全过 |
| P5.4 | 文档重写：`README`（6 名词开篇）、`doc.go`、`docs/api-reference.md`、`usage-guide.md`（删除四层 ID 对照表与防踩坑指南——它们的存在理由已被消除）、`streaming.md`、`a2a.md`、`structured-output.md`；新增 `docs/migrating-to-v1.md`（§4 能力映射表展开成旧→新逐 API 对照）；`workstream-*.md` 移入 `docs/archive/` |
| P5.5 | 发布检查单：godoc 首屏审查（根包导出名 ≤ ~35）、`go vet`/race/fuzz（archive fuzz 随迁）、examples 全绿、CHANGELOG、`v1.0.0` tag |

**门禁**：S1–S9 全绿；`docs/api-v1-redesign.md` §4 能力映射表 100% 勾验；根包导出符号数与选项数达标（选项 ~24、概念 ~13 的量化承诺逐项核对）；migration guide 覆盖全部 66 个旧选项的去向。

---

## 3. 测试策略总览

1. **行为快照先行**：每个阶段动某块语义前，先把现状合同测试的断言固化为「基线快照」，新实现对照快照逐项打勾——尤其事件时序、合并语义、lease、HITL 超时。
2. **场景测试 = 验收语言**：S1–S9 直接做成 `next/` 的可执行测试（fake driver），每个场景就是设计文档里的代码逐字可跑。文档与实现漂移时 CI 报警。
3. **合同测试迁移映射**：

| 现状测试 | 迁移阶段 | 新归宿 |
|---|---|---|
| `run_contract_test.go` `run_policy_test.go` `run_model_override_test.go` `errors_test.go` | P0 | engine + next 核心 |
| `stream_internal_test.go` `runner_hitl_integration_test.go` `runner_decision_test.go` | P1 | 事件流/审批 |
| `sdk_session_test.go` `sdk_start_session_test.go` `runner_session_internal_test.go` `session_codec_internal_test.go` | P2 | Thread/threadstore |
| `skill_contract_test.go` `skills_sdk_test.go` `skill_dirscan_test.go` `mcp_sdk_test.go` `structured_output_test.go` `admin_profile_test.go` `profile_resources_test.go` `caller_identity_test.go` | P3 | 词汇包/Inspect |
| `runtime_admin_test.go` + bridges/hosttools 各包测试 + `internal/aguiversion` | P4 | 服务/桥 |
| `archive_fuzz_test.go` `archive_source_test.go` | P3 随 skill 归档源（P0.7 勘误：非 engine） | `skill/` |

4. **live 冒烟**：examples 中依赖真实 CLI 的（codex-basic、streaming-chat、team-agent-workflow）保持手动/定期跑，不进 PR 门禁；CI 只保证编译。

---

## 4. examples 重写清单（P4.9）

| 现状 example | v1 去向 |
|---|---|
| codex-basic | `quickstart`（S1 形态） |
| codex-admin-named | 并入 `inspect`（S7 形态；命名注册表已删，展示多变量） |
| codex-sessions / session-codec-inspect | `threads`（S3 会话半场 + Checkpoint） |
| codex-skills-live | `skills` |
| codex-profile-full / profile-resources | `profiles`（S8 形态） |
| codex-stream / streaming-chat | `streaming`（单事件流 for-range） |
| streaming-sse-server / streaming-chat-aguiclient / streaming-chat-copilotkit | `web-chat`（S3 完整形态，3 合 1 分子目录） |
| a2a-local | `a2a-server`（S6 形态） |
| （cl/opt_examples）showcases/team-agent-workflow | `showcases/team-agent-workflow`（§9.7 形态） |
| 新增 | `structured-output`（S5，RunAs 一屏示例） |

---

## 5. 风险登记册

| # | 风险 | 概率/影响 | 缓解 |
|---|---|---|---|
| R1 | Option 双作用域达不到理想编译期约束 | 中/中 | P0.1 spike 先行；兜底为运行时校验 + 清晰报错；不阻塞后续阶段 |
| R2 | 事件合一引入时序/背压回归 | 中/高 | P1 行为快照 + race + 保留 engine sink 语义不重写 |
| R3 | `SubagentUpdate` 注入口对 engine 侵入超预期 | 中/中 | P4.7 自带兜底方案（bridge 层 Merge），S9 文档随之微调 |
| R4 | 驱动特殊路径回归（claude PersistentProcess、codex app-server、codebuddy 新驱动） | 中/高 | P0.4 shim 期不动驱动内部；adaptertest v1 + live 冒烟清单 |
| R5 | `cl/opt_examples` 分支与 `v1` 的合并冲突 | 高/低 | P4.9 直接在 v1 重写该 example，不做机械合并 |
| R6 | 大挪移 PR 过大不可评审 | 高/中 | P5.2 拆三步：移动（无 diff 语义）→ 删除 → 重命名；分 PR，每步 CI 绿 |
| R7 | v0 用户升级断崖 | —/中 | migration guide 覆盖 66 选项逐一映射；v0.x tag 冻结可长期 pin |
| R8 | P0.2 波及面 ≈ 全根包 32 个非测试文件（合同类型必须随 engine 迁移并别名回指，否则依赖成环） | 高/中 | ✅ 已兑现：p0-inventory.md 为路线图分四批推进，两次中断均无损恢复 |
| R9 | S9/设计文档使用的 `claude.Config{PersistentProcess: true}` 字段（及背后的常驻进程驱动能力）只存在于 `cl/opt_examples` 分支，main 的 ClaudeConfig 上没有（P3.1 实测） | 高/中 | P4.9 重写 team-agent-workflow 前，先把 claude 驱动的 PersistentProcess 能力从 cl/opt_examples 移植/合入 v1 线（或示例改用现有进程模型并回改 §9 文档）；列为 P4 前置检查项 |

---

## 6. 依赖与并行度

```
P0 ──► P1 ──► P2 ──► P5
 │      │           ▲
 │      └──► P3 ────┤   （P3.2/P3.3/P3.4 三词汇包内部可并行）
 │             │
 └────────────►P4 ──┘   （P4 依赖 P1 事件族 + P3 mcp 包；P4.1 目录提升可提前）
```

- 关键路径：P0 → P1 → P4（事件族定型是 bridges 适配的前置）。
- P2（Thread）与 P3（词汇包）在 P1 后可双线并行。
- 规模预估（PR 数）：P0 ×3、P1 ×2、P2 ×1、P3 ×3、P4 ×3、P5 ×3，共约 15 个可独立评审的 PR。

---

## 7. 启动动作（P0 第一个 PR 的内容）

1. `docs/api-v1-implementation-plan.md`（本文）+ `docs/api-v1-redesign.md` 合入 `v1` 分支；
2. P0.1 spike：`next/options_scope_test.go` 里两案原型 + 决策注记（半天量级）；
3. P0.2 首刀：`internal/engine` 建包，先移 `util.go`/`config.go` 等无依赖件，验证「移动 + 别名回指」的机械流程与 CI 表现；
4. CI：`go.yml` 补 `next/...`、`driver/...`、`internal/engine/...` 的 job。
