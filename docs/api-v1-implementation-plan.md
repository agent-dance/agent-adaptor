# agent-adaptor v1 实施计划

> 配套设计文档：[api-v1-redesign.md](./api-v1-redesign.md)（“什么/为什么”）。本文是阶段计划；当前接管状态、阻断项与下一步以 [v1-takeover-audit.md](./v1-takeover-audit.md) 为唯一执行入口。
> 原始设计基线：main @ `bbba7a0`。接管断点：HEAD `4a66cc3`，v0 冻结基线已推进至 `v0.12.0`；未提交 PRE 工作集的实时状态见 takeover audit。

---

## 0. 决策记录（Decision Log）

| # | 决策 | 状态 | 翻案窗口 |
|---|---|---|---|
| D1 | 业务失败并入 error（`*RunError` 携带完整 `Result`），删除 `RunResult.Failure` 双层判定 | **采纳**（方向已确认） | P0 结束前（`Result`/`RunError` 定型后翻案成本陡增） |
| D2 | 审批请求自带应答器（`ApprovalRequest.Approve/Deny/Answer`），删除 `ResolveDecision` requestID 往返与 3×2 typed handler | **采纳**（方向已确认） | P1 结束前 |
| D3 | 根包 package name `agentadaptor` → `adaptor`（import path 不变） | **最终确认** | 已关闭 |
| D4 | `Stream` 定义为小接口（`Events`/`Result`/`RunID`/`Cancel`），而非具体结构体 | 采纳（S9 分析反哺） | P1 |
| D5 | `delegation.Service` 一体化入口 + `delegation.Local/Remote` 双目标 + `SubagentUpdate` 事件入主流 | 采纳（S9/§9.8） | P4 |
| D6 | 实施策略：**绞杀者路线**（内核抽取 → staging 包并行生长 → 终局大挪移），不做同包新旧共存 | 采纳（本文 §1） | 立即生效 |
| D7 | Option 双作用域的编译期约束具体类型设计 | **已定案（P0.1 spike）**：案 A 三接口 `Option` / `CallOption`（不嵌入 Option）/ `SharedOption`，双向误用编译错；`AgentSettings` 内嵌 `RunSettings`，字段不导出、扩展面为精选导出方法。详见 [p0-option-scope-decision.md](./p0-option-scope-decision.md)。连带定稿：`WithModel`/`WithTimeout` 为双作用域（已回改方案 §2.3）；`a2a.ServerOptions.Options` 类型为 `[]adaptor.CallOption` | 已关闭 |
| D8 | 结构化输出模式词汇归属（根包常量 vs `schema` 子包） | **已定案**：根包常量 `SchemaStrict` / `SchemaFlexible` / `SchemaPromptOnly` | 已关闭 |
| D9 | `providers/` 包去留 | **删除**（P0.7 裁决：全仓唯一引用者是自身测试，自述 opt-in sugar；Required 能力在 skill.Provider 合同中保留，宿主 10 行 wrapper 等价）。迁移指南记一行 | P5 前若产品异议，归宿为 `skill.MarkRequired` |
| D10 | `runtimeservice/` 包去留 | **删除**（P0.7 裁决：v0.5 的宿主兼容 mixin，与 runtime.go / RuntimeServiceRef 零代码关系；v1 `WithServiceManager` 是全新契约，无存量宿主需要垫片） | P4.5 |
| D11 | `Identity` 归属与字段集 | 归**根包** `adaptor.Identity` + `IdentityFromContext`（消费方横跨 skill/workspace/services 三域，不进 skill 包）；现状四字段（ID/Tenant/Profile/Name）vs 设计稿两字段是能力缩水，**字段集 P0.5 定案**（默认保四字段） | P0.5 |
| D12 | `ApprovalRequest.Risk()` 风险分级 | **推迟出 v1.0**（P1 裁决）：现状驱动 SPI 无风险信号来源，拍脑袋 API 违背真话原则；设计文档 §2.6 示例已改注。待任一驱动提供真实风险信号后作为 additive API 补入 | v1.x |
| D13 | Claude 设计的 v1 API 完全取代旧 `AGENTS.md` 的 SDK/Runner/RunHandle/registry 合同；`Agent · Thread · Stream · Event · Result · Driver` 是唯一北极星，旧 API 仅作为 P5 删除对象存在 | **用户最终裁决（2026-07-27）** | 已关闭 |

---

## 1. 总体策略：绞杀者路线

同包新旧共存不可行（`Option` 等核心名字直接冲突），一步到位全量重写不可评审。因此分三步走：

1. **内核抽取（P0 前半）**：把根包执行职责抽进 `internal/engine`，旧公共 API 降级为薄包装。目标不变式是所有路径最终收敛为“选项解析 → 单一 invocation → thread/session 协调 → driver 执行 → checkpoint/result 归档”。接管审计确认 staging 的 `next/stream.go`、`next/thread.go` 仍存在旁路编排；该不变式尚未兑现，必须在 MOVE 前收敛，详见 takeover audit。
2. **staging 包并行生长（P0–P4）**：新 API 在 `next/` 目录（package name 即为 `adaptor`）生长，与旧 API 互不干扰；每个阶段以设计文档 §3 的场景测试（S1–S9）为验收锚点。旧 API 与旧测试在此期间**保持绿色不动**，持续兜底。
3. **大挪移（P5）**：`next/` 内容平移至根目录，删除旧 API 文件与旧测试，文档全量重写，打 `v1.0.0` tag。模块路径 `github.com/agent-dance/agent-adaptor` 不变（当前处于 v0，语义化版本允许直接切 v1，无需 `/v2` 后缀）。

分支与发布节奏：

- v0 冻结基线已推进到 `v0.12.0`；P5.1 以该版本核对 tag 与维护线，不再创建或引用计划中的 `v0.9.x`。
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

> **✅ P1 已完成（2026-07-26，提交 ac94313）**。11 个事件类型（密封接口），18 StreamKind + 6 RunEventType 全映射零丢失；Stream 小接口 + Run=Stream+drain 单一路径；审批双形态（OnApproval 回调 / 事件自应答，exactly-once，重试/超时/兜底对齐 EffectiveHumanDecisionPolicy，ApprovalRequest 豁免丢弃策略）；背压 Dropped 聚合语义对齐现状；40 用例含 3Kind×2形态×兜底矩阵，-count=5 稳定。主要偏差已记录于提交与本表 D12（Risk() 推迟）；Run 恒走流式路径。这里的“单一路径”仅指 `Agent.Run = Agent.Stream + drain` 的 P1 表面合同，不代表 [takeover audit G-01](./v1-takeover-audit.md#g-01-唯一执行管线尚未兑现) 所要求的 Agent/Thread 全局编排已经收敛。

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

### P2 · Thread + threadstore ✅ 已完成（提交 b0aea73，15 文件 +2263 行）

**目标**：四层 ID 归两层。`Thread`/`NewThread`/`ResumeOnly`/`Fork`/`Checkpoint`；`threadstore.Store` 承接 SessionStore 全部能力。

| 任务 | 内容 | 触及现状 |
|---|---|---|
| P2.1 | ✅ `threadstore/` 包：`Store` 五方法（Resolve/Finalize/AcquireLease/RenewLease/ReleaseLease）+ Record/Query/Lease/FinalizeRequest + ErrBusy/ErrLeaseLost；namespace+key 收敛为单一 thread key；只 import driver。`memory/` 双实现（新增 `memory.Store`/`NewStore()`，旧 `SessionStore` 零 diff） | `session_types.go` `memory/` |
| P2.2 | ✅ `Thread(key)` → continue_or_start；`Thread(key, ResumeOnly())` → continue_only；`NewThread(key)` → start_new（仅首轮，成功持久化后转 continue，旧会话归档+key 重绑）；`th.Fork(newKey)` → fork（仅首轮，父会话运行时按 key 解析）；`*Thread` 实现 `Runner`；`WithThreadStore` 从 any 收紧为 `threadstore.Store` 并真消费；8 个哨兵错误（`ErrThreadStoreRequired`、`ErrThreadNotFound`、`ErrThreadBusy`、`ErrThreadIncompatible`、`ErrThreadLeaseLost`、`ErrThreadCheckpointMissing`、`ErrThreadAlreadyExists`、`ErrResumeRejected`） | `session.go` `sdk_session_test.go` |
| P2.3 | ✅ `Thread.Checkpoint()` 返回 `Checkpoint = driver.Checkpoint`，经 driver.SessionCodecProvider 归一化 | `session_codec.go` |
| P2.4 | ✅ 13 组语义断言全部迁移（mode 矩阵/fingerprint 匹配失配/fork 边界/busy 并发/resume-rejected 回退与保留/无 checkpoint 人审容忍/lease 续期与丢失/codec 快照往返），旧测试零修改仍为基线；lease 测试全 channel 同步零 sleep；S3 升级为带 Thread+Fork 的完整版（原帧/审批断言逐字保留） | 同名测试 |

**门禁**：✅ 全仓 35 包 build/vet/test 绿；next/threadstore/memory -count=5 稳定；根包 -count=2 绿（引擎 session.go 重构后旧行为不变）。
**接缝裁决**：选了首选方案——engine 内 `prepareSession`/`persistSession` 机械抽为 store 参数化自由函数（一处真身），新增 additive 入口 `threadsession.go`（ThreadSessionPlan/PrepareThreadSession）；next/ 经 engineStore 适配器注入固定 namespace "thread"。
**偏差记录**：NewThread 不收 ThreadOption（无有意义组合）；Fork 父会话延迟到首轮运行时按原始 key、在协调租约下解析（缺失 → ErrThreadNotFound，目标冲突 → ErrThreadAlreadyExists）；租约丢失时 ErrThreadLeaseLost 优先于被连带取消的 run 错误（与引擎语义一致）；memory/session_store.go 在 HEAD 本就 gofmt 不净，按零改动纪律未动。

### P3 · 词汇包 + 驱动配置回家 + 结构化输出 + Inspect

**目标**：消费者视野里的名词全部就位。

| 任务 | 内容 | 触及现状 |
|---|---|---|
| P3.1 | ✅ 已完成（提前并行，提交 275e8a1）：四驱动包各得 `Config`（别名指向根包公开别名）+ `Driver(cfg) driver.Driver`（configuredDriver 嵌入现有 adapter，能力接口经方法提升自动保留；req.Config==nil 时注入构造期 cfg，显式 Config 不覆盖）；现有入口与既有测试零修改；P5 翻转别名真身方向 | `config_types.go` 四驱动包 |
| P3.2 | 🟡 词汇包已完成（提交 61b76ba）：`skill/` 包 `Dir`/`FS`/`Inline`/`Key`/`Require` + **`Archive(key, open, opts...)`**（P0.7 缺口补齐：Opener= 根包归档 helper 别名，`ArchiveBytes/File/URL`，`WithFormat`/`WithSubpath`/`WithFingerprint` 走不透明 config，P5 搬真身签名零变）+ `Ref`/`Skill`/`Source`/`Provider`/`Catalog`/`Set`/`Materializer` 别名族；15 用例含三格式×auto/显式共 6 路真实物化验证。**接线已完成（7e4f6d4）**：`WithSkills`（SharedOption，唯一追加合并，defaultSkillBoundary 分界）+ `WithSkillProvider`/`WithSkillMaterializer`（仅 New）经 engine `ResolveSkills`（resolveSkillsWith 抽参的 additive 导出）复用真身算法。**P5 待办**：① 收编 `archive_*.go` 真身 + `LocalSkillsFromDir`/`SkillsAsRefs`/`NewDefaultSkillMaterializer` 族；② SkillFromArchive.Fingerprint godoc 称缓存键但默认物化器忽略（纯内容寻址）——收编时二选一收敛；③ skill→根包依赖翻转；④ **接线波新发现（预存在 bug，未修）**：`skillSourcesEquivalent`（engine/skill_helpers.go:151）无 SkillFromArchive 分支，归档源永不相等（含与自身比较）→ **default 作用域归档技能必然自冲突**（"same key, different content"）；run 作用域不受影响。修复会改旧语义，留 P5 与 ② 一并收敛 | `skill_*.go` 5 个文件 + `archive_*.go` + `internal/skillruntime` |
| P3.3 | 🟡 词汇包已完成（提交 66443b4）：`mcp/` 包 `Server = driver.MCPServerSpec`（单服务器形状，兼容 P4.5 `RuntimeServiceRef.MCP *mcp.Server`；别名指向叶子包 driver 避免 P5 后根包 import 环）+ `HTTP`/`SSE`/`Stdio` 构造器（SSE 为现状能力保全补入）+ `WithHeader(s)`/`WithBearerTokenEnv`/`Required` 选项，10 字段零缩水，8 用例全绿。**接线已完成（7e4f6d4）**：`WithMCP(servers ...mcp.Server)` SharedOption，整体替换、零参=显式清空，经 engine `ResolveMCPPayloadWithRuntime` 复用传输/键校验（runtime 槽位暂 nil，P4.7 接 delegation 时补） | `mcp_types.go` `internal/mcpruntime` |
| P3.4 | 🟡 词汇包已完成（提交 0b670ed）：`profile/` 包 `Selection = driver.ProfileSelection` + `Default`/`Native`/`Dedicated`/`CloneNative`/`CloneFrom` 构造器（`Default()` 为现状 Unset 形态的显式 v1 名）+ CloneOption 族（`CopySettings`/`CopyMCP`/`CopySkills`/`CopyAuth`/`LinkAuth`/`WithOptions` 逃生舱，遗留 IncludeAuth 经 WithOptions 无损迁移）+ `Resources = ProfileResources` 及 SubAgent/Hook(20 事件全量)/Instructions(`Text()` 构造)/ConfigPatch 别名族，22 用例对照旧 root 选项 DeepEqual 全绿。**接线已完成（7e4f6d4）**：`WithProfile`（仅 New）+ `WithProfileResources`（SharedOption：Skills 追加、MCP 替换、Agents/Hooks/Config/Instructions 替换且**声明**——非 nil 空切片=声明为空），指纹与旧别名归一（如 Event:"PreToolUse"）全走 engine 真身。S8 的 `SubAgents` 字段名仍留 P5 定夺（现名 `Agents`） | `profile.go` `profile_resources.go` `internal/profile*` 8 个包 |
| P3.5 | ✅ 已交（7e4f6d4）：`RunAs[T]`（接受任意 Runner，Agent/Thread 双验证；显式选项胜隐式 `WithSchema[T]()`）+ `WithSchema[T]`/`WithSchemaJSON`（裸 schema 逃生舱，超纲但对齐 root 能力）均 CallOption；**D8 已定**：根包常量 `SchemaStrict`（默认）/`SchemaFlexible`/`SchemaPromptOnly` + 失效策略 `SchemaFailRun`（默认，`*RunError` Reason=PolicyViolation）/`SchemaReturnInvalid`；schema 派生在选项构造期，失败=启动前错误；能力矩阵校验走 engine 真身（next 恒流式→streaming=true） | `structured_output.go` |
| P3.6 | ✅ 已交（7e4f6d4）：`Inspect()` 面板（Environment/Models/Quota/ConfigSchema/Skills）+ Agent 上 `ProfileState`/`SyncProfile`/`SelectSkills`；探针驱动原样透传，无探针驱动诚实降级（Available:false 等），绝不伪造成功 | `admin.go` `admin_helpers.go` |
| P3.7 | ✅ 已交（7e4f6d4）：合同测试迁移 6 组 → next/（merge_semantics 含门禁要求的 9 行「WithSkills 追加 vs 其余替换」表驱动、skills 13 测试、mcp、structured 全 17 语义、profile、inspect）；旧测试零修改。**已知不可复制面**：`admin_profile_test.go`、`skill_dirscan_test.go`（对应表面 next 无，P5 收编时补） | 同名测试 |
| P3.8 | ✅ 已交（7e4f6d4）：S5/S7/S8 场景测试按设计文档代码块近逐字落地（S7 的 env.Ready/Problems→实名 Healthy/Checks、S8 的 SubAgents→Agents，差异记在测试头注释） | — |

**门禁**：S5/S7/S8 绿；skills/MCP/profile 合同测试全量迁移且与旧版行为逐项对照；`WithSkills` 追加 vs 其余替换的合并语义表驱动测试。
**说明**：P3.2–P3.4 三个词汇包相互独立，可并行三条支线。

### P4 · bridges / hosttools 适配 + delegation.Service

**目标**：传输层与宿主组件换新词；S6/S9 跑通；examples 全量重写。

| 任务 | 内容 | 触及现状 |
|---|---|---|
| P4.1 | ✅ 已完成（提前并行，提交 4deabe7）：7 包提升至顶层，测试随迁新路径全绿；旧路径转发包全集镜像（Err 哨兵用 var 转发保 errors.Is 同一性，Deprecated 标记，P5 删）；三个 import 守门测试路径重锚定 + forbidden 列表全部补齐 codebuddy；CI repeat/race A2A 步骤改指新路径 | 目录移动 + 守门测试 |
| P4.2 | ✅ 已交（80119ef）`agui.Events(stream)`/`NewEventTranslator` 基于新事件族重写状态机；**12 行新旧同语义输入对照表证明 AG-UI 帧逐字节等价**（仅剥时间戳）；capability 降级保留（`human_decision_retry_unsupported` 以 CUSTOM 生命周期帧可见 + DecisionAsCustom 模式） | `pkg/bridges/agui` |
| P4.3 | ✅ 已交（80119ef）`sse.HandlerV1(runner, OptionsV1)`（V1 后缀，P5 删旧后改回设计名）：Agent/Thread 同构（带 store 的 \*Agent 按请求绑 `agent.Thread(ns/key)`）；断连取消 channel 同步零 sleep 锚定 | `pkg/bridges/sse` |
| P4.4 | ✅ 已交（80119ef）`a2a.NewServerV1(runner, ServerOptionsV1)` + `StatelessV1()/ThreadByContextID()`；ExposurePolicy 默认脱敏与旧版 forbidden-key 清单逐项等价；业务失败 `*RunError`→TASK_STATE_FAILED 保留消息。**发现**：a2a-go 会替客户端补发 contextID，故 ThreadByContextID 的空 contextID 无状态分支经 SendMessage 实际不可达（有测试记档）；且 engine thread 跑道要求驱动交出可续 checkpoint，ThreadByContextID 仅适配可 resume 驱动 | `pkg/bridges/a2a` |
| P4.5 | ✅ 已交并合入主线（隔离 worktree 628509e → cherry-pick 456551e）：`RuntimeServiceRef.MCP *MCPServerSpec` 真身在 driver 包（≡`*mcp.Server` 别名同型，避免 driver→mcp 成环）；typed 优先、metadata 完全不解析，nil 时旧 `agentadaptor.mcp.*` 兜底（P5 删）；`runtimeservice/` 按 D10 删除（全仓唯一 import 者是其自身测试）。**遗留**：next/result.go 的 TODO(P4.5)——ServiceReport 是否透出 typed MCP，P4.7 波定夺 | `runtime.go` `internal/engine/mcp.go` `internal/engine/util.go` |
| P4.6 | **`delegation.Service`**（D5）：Registry+EventBus+Delegator+per-run MCP sidecar+结果记录一体；`delegation.Local(key, runner, policy)` / `delegation.Remote(key, cardURL, policy)`；`team.Option()`；`team.Result(runID, key)`。**✅ 全部完成（970ec3b + 7772b6d）**：`team.Option()` 已交——`Service.Option()` 实现 next 通用 `RunServiceProvider` 接口（AttachRun→EnsureSidecar、DetachRun→ReleaseRun、事件源→Bus().SubscribeRun），e2e 测试用只看 driver.Request 的 leader（从 req.MCP+SecretEnv 重建端点做真 HTTP tools/call）证明发布链路完整，含 sidecar 先于 Result() 返回被释放、Closed Service=启动前错误等 8 断言点。首波交付细节：新文件落在现有 `hosttools/a2adelegation`（设计稿的 `delegation.` 是 import 别名；team 对象=\*Service 值），吸收示例 delegation_runtime.go 全部 409 行职责，仅剩「把 Sidecar{URL,BearerToken,ToolTimeout} 指给 leader 驱动的 MCP 面」= team.Option() 接缝；Local/Remote 对照测试（门禁项）以归一化事件序列+结果双等价锚定，成功/失败双路径。**team.Option() 接缝清单（P4.7 波实现）**：① next 侧 run-scoped 服务挂载点（AttachRun(runID)→RunAttachment{Sidecar+事件源}，engine 在 RunID 分配后、驱动派发前调用）② engine 请求组装期把 Sidecar 映射进驱动 MCP 载荷（与宿主配置合并非替换）③ sink 注入：SubscribeRun→SubagentEvent 直灌事件流（engine 版 Merge，省一跳 goroutine）④ teardown：sink 关闭后 DetachRun→ReleaseRun（不剪终止事件） | `pkg/hosttools/a2adelegation` |
| P4.7 | **宿主事件注入口**：engine 提供 per-run 的 host-event 注入内部 API，`SubagentUpdate` 汇入主流；`subagentstream` 降级为兼容层。**兜底**：若注入侵入面超预期，退回 `subagentstream.Merge(stream, bus)` 桥接方案，S9 文档同步改写。**✅ 主方案落地（7772b6d）**：next `RunServiceProvider`/`RunAttachment`/`RunEventSource` 挂载点 + `WithRunServices`（追加+身份去重）；engine 时序=RunID 分配后 AttachRun（失败=启动前错误）→ MCP 载荷经 runtime 槽位合并（宿主 WithMCP 共存）→ 事件泵先于驱动派发启动 → 驱动/宿主事件同 sink 交织 → finish 时 stopPumps→sink 关闭→逆序 DetachRun（防剪终止事件：源侧负责"先冲已发布事件再关"，泵侧纯 for-range 无死锁）。剩余选项同波接线：`WithServices(ServiceSpec)`（按设计 §581 用 Spec 非 Ref）/`WithWorkspaceSpec`（SharedOption 替换）+ `WithServiceManager`/`WithWorkspaceManager`（仅 New）。**波内捉获真 bug**：SecretEnv 曾在 normalize 后采集（clone 剥密）致 token 永不达驱动 env，已改为 raw refs 先采集。TODO(P4.5) 裁定：ServiceReport **不透出** typed MCP（方向/诚实填充/避免密钥旁置三理由，记录在 Result.Services godoc）。`subagentstream.Merge` 降级为兜底桥（synthetic-terminal flush 仅兜底桥保留），`SubagentEvent` 真身移 a2adelegation、桥侧转发 | `pkg/bridges/subagentstream` + engine |
| P4.8 | ✅ 已交（80119ef）`sessionrecorder.NewEventRecorder(backend, opts...)` 收 `adaptor.Event`，`{host_seq, recorded_at, kind, event}` 稳定 JSON 信封；JSONL v1 后端推迟（内存后端已证序列化） | `pkg/hosttools/sessionrecorder` |
| P4.9 | ✅ 全部完成（51adc22 + 0228dbd）。**team-agent-workflow showcase ✅（0228dbd，main.go 450 行 + host.go 311 行）**：设计 §9.7 逐要素落地——leader + plan/impl/review 三 Local 角色、`team.Option()` 一行接入、单 for-range 事件流 + SubagentUpdate、trace.RequireOrder、宿主侧 stage-boundary 审计（仅 impl 可写工作区且不得动 TASK.md）、`team.Result(runID,"review")`+HasLine 哨兵裁决、web 分支 sse.HandlerV1+memory.NewStore；零付费防护（`-leader` 必填无兜底、无测试文件、角色用 NonInteractivePolicy 而非 ApprovalsAutoDeny——后者会锁死要写文件的 impl 角色）；R9 注释开关在 leader adaptor.New 上方并交叉引用 exampleutil，设计文档 §9.7 加 2 行 R9 注记；10 处对设计草稿的偏差已记录（Local 非 Remote 角色按 §9.8/D5、sse.HandlerV1 落地名、CallOption 签名、Agents 非 SubAgents 字段、agent 选择改 flag 驱动等）。**examples 全量重写 ✅（51adc22，24 文件 +1647/−1165）**：13 个示例全部改写至 v1 API（零 deferred、零造假 API），exampleutil 改造（`NewLiveDriver(cfg)` 走四驱动 v1 构造器、`NonInteractive` 变 SharedOption、R9 PersistentProcess 以注释开关留在 live_agent.go claude 分支）、run_examples.ps1（codebuddy 接入、session-codec-inspect 提首位免 CLI、profile-full 以 `-run=false -probe=false` 免付费入列）、双 README 重写。亮点：codex-admin-named 改主题为 Inspect 面板（文件头记录命名注册表删除理由，目录名保留保索引稳定）；copilotkit 全测试套件对 fake `adaptor.Stream` 过（approval form B=事件自带应答器）；codex-profile-full 实跑两遍验证 native_managed/file_managed 归属；session-codec-inspect 四驱动实跑。**注**：目录未按 §4 改名（quickstart/threads 等）且 3 合 1 未做——改名/合并挪至 P5.2 大挪移一并处理，避免与删旧双重 churn；`showcases/team-agent-workflow` 单独在飞。**P5 待办（agent 报的 9 处新 API 可用性缺口）**：① `mcp.Stdio` 不收 `mcp.Option`（Env/Required 只能结构体赋值，破坏单行构造承诺）；② `profile.Resources` 无法纯用 profile 包填满（MCP 是 `*driver.MCPConfig`、Skills 要 SkillRef，均未 re-export）；③ 零值 `&ApprovalRequest{}` 在 Approve/Deny/Answer 上死锁（nil reply 通道、无导出构造器，宿主测试无法安全构造）；④ sessionrecorder 无 `adaptor.Event` 的 JSONL EventBackend（THREAD_STORE_DIR 静默降级内存）；⑤ bridges/sse 无 v1 审批错误映射助手（示例自带 `ErrApprovalResolved`→410/`ErrApprovalKindMismatch`→400，宜上提进桥）；⑥ `agui.RunAgentInput.UserTurnPayloads` 仍返回旧 StreamPayload、无 `[]adaptor.Event` 孪生、无"最后用户消息 id"访问器；⑦ `sse.OptionsV1` 无 SubagentBus 字段 + `subagentstream.WrapAGUI` 仍收旧 RunHandle——可视化 A2A 委托无 v1 通路；⑧ `a2a.DecodeAdapterStreamStatus` 仍返回旧 StreamPayload；⑨ driver 未导出会话参数键常量（cwd/workspace_id/profile_fingerprint，示例用字符串字面量+注释）；⑩ `SubagentEventKind` 无 String()、`ToolCall.Args` 无规范预览助手（SDK-free 渲染器处处手写转换）；⑪ SDK 侧无"本 run 发生过哪些委托"的有序访问器（`team.Results` 返回无序 map，示例只能数流上的 SubagentStarted 重建顺序）；⑫ `Result.Summary` vs `Result.Text` 语义含糊（Summary 按驱动可选，日志代码要写 pick 兜底）；⑬ 消费侧无 run 已挂载运行时服务的类型化访问器（只能经 DelegateToolName 元数据间接断言）；⑭ `delegation.Policy.RequireStreaming` 在 SDK 侧无对应能力查询（宿主无法预先问 Agent 的驱动是否支持流式，只能延迟到委托时失败） | `examples/` 14 个 |
| P4.10 | ✅ 场景测试双双落地：**S9 CI 版**（970ec3b，service_s9_test.go）leader + 3 Local 角色过真 HTTP MCP sidecar，断言终止事件观察瞬间的 Result 实时可用性、每角色事件文法、HasLine 哨兵门控；**S6**（80119ef，scenario_s6_test.go）fake driver + NewServerV1 + ThreadByContextID，两次 SendMessage 共享 contextId 走 continue_or_start，turn-1 checkpoint 成为 turn-2 ResumeID；live 版均留 example（P4.9） | — |

**门禁**：S6/S9 绿；AG-UI 前后端版本守门测试通过；`delegation.Local` 与 `Remote` 的行为对照测试（同一角色两种注册方式，事件序列与结果等价）；每个 example 在 CI 至少编译、fake-driver 类 example 可执行。

**桥适配波交出的 next/ 保真缺口清单**（80119ef 报告，P3.7/P5 复审时定夺加字段还是记入 migration guide 的行为变化）：① 审批 resolved `Notice` 无 answer/source/latency 结构保证、requested `Notice` 无 payload/choices/deadline/tool_call_id（全保真只能走 `*ApprovalRequest` 本体）② `Dropped{Count}` 丢弃 dropped_kind/reason 明细 ③ v1 事件无 Seq/Timestamp/TurnID（桥各自本地计数）④ `Result` 无 provider-result 字段、值类型 `Usage` 失去 nil/零值之分 ⑤ `RunError` 无 HumanDecisionOutcome（a2a 的 human_decision 失败块消失）⑥ 透传 `Notice` 丢 `p.Name`（agui 回落 "codex.event"）⑦ 线上审批失败码 `decision_rejected/decision_timeout` → `approval_denied/approval_timeout`（属 v1 词汇有意变更，migration guide 已可记）。

### P5 · 大挪移 + 删旧 + 文档 + 发布

> **接管门禁**：P5.2 MOVE 不得开始，直到 [v1-takeover-audit.md](./v1-takeover-audit.md) 中全部阻断级合同缺口关闭、单一执行管线收敛、当前 PRE dirty 工作集完成独立验收并形成可追溯提交。

**目标**：`v1.0.0`。

| 任务 | 内容 |
|---|---|
| P5.1 | 冻结 v0：以现有 `v0.12.0` 为冻结基线核对 tag/维护分支；`v1` 分支收口。旧 `v0.9.x` 计划作废 |
| P5.2 | 大挪移：`next/` → 根目录（package `adaptor`）；删除旧 API、旧测试、`pkg/` 转发包、legacy metadata 兼容解析和 `providers/`。迁移清单见 [p5.2-recon.md](./p5.2-recon.md)。**修订波次**：PRE → CORRECTNESS（含 P4/SPI/bridge）→ REHOME/REPOINT（含 D-P5.2-3/4 与全部公共 internal aliases）→ LEGACY EDGE DELETE → ROOT CUTOVER（最终 MOVE，原子）→ RESIDUAL DELETE → RENAME。原“MOVE 约107后 DELETE约200”经机械复核不可编译且漏算大量旧根消费者，已作废；每波按实时依赖图重建清单。 |
| P5.3 | `adaptertest` v1：面向 `driver.Driver` 的一致性套件（能力声明真话性、事件时序、会话 codec、结构化输出矩阵）；四内置驱动 + fake driver 全过。**✅ 已交（e3d5673）**：`adaptertest/v1/`（编译图仅 driver+stdlib），`TestDriver` 14 子测试 × 51 编号条款（doc.go 全文），自证参考驱动实现全部 10 能力接口零跳过，verify_test 以 30+ 故意违例证明每个校验器按条款号报错；四驱动各一个新增测试接入，live 探针需 CLI 在 PATH **且** `AGENT_ADAPTOR_LIVE_CONFORMANCE=1` 双门（裸 `go test` 永不付费实跑）。**P5 待办**：doc.go 记录了 9 处 `driver/` godoc 合同含糊点（run 生命周期框定无 MUST、SessionCodec nil/零值映射未文档化、Sequence vs Seq 权威归属、SupportsResume⇒SessionCodecProvider 未成文等），v1 冻结前硬化 SPI godoc |
| P5.4 | 文档重写：`README`（6 名词开篇）、`doc.go`、`docs/api-reference.md`、`usage-guide.md`（删除四层 ID 对照表与防踩坑指南——它们的存在理由已被消除）、`streaming.md`、`a2a.md`、`structured-output.md`；新增 `docs/migrating-to-v1.md`（§4 能力映射表展开成旧→新逐 API 对照）；`workstream-*.md` 移入 `docs/archive/`。**🟡 migrating-to-v1.md 初稿已交（9ac6144，基于 771590a）**：66 旧选项逐一编号映射 + ~90 行非选项对照，在飞面标 🚧 并注明定稿依据，P5 收尾时按落地结果摘 🚧；其余文档待 P5。初稿发现两处设计稿勘误待 P5 处理：① 设计 §3 S8/S9 示例用 `Identity{User: ...}`，与 D11 定稿四字段（ID/Tenant/Profile/Name）不符；② p0-inventory「66 个 With* 全在 options.go」不准（实为横跨 7 文件 48+4+4+3+4+2+1，总数 66 无误） |
| P5.5 | 发布检查单：godoc 首屏审查（根包导出名 ≤ ~35）、`go vet`/race/fuzz（archive fuzz 随迁）、examples 全绿、CHANGELOG、`v1.0.0` tag |

**门禁**：S1–S9 全绿；`docs/api-v1-redesign.md` §4 能力映射表 100% 勾验；根包导出符号数与选项数达标（选项 ~24、概念 ~13 的量化承诺逐项核对）；migration guide 覆盖全部 66 个旧选项的去向；takeover audit 的阻断项为零，且每项均有对应回归测试。

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
| R6 | 大挪移 PR 过大不可评审 | 高/中 | PRE 独立验收提交；阻断级合同修复与单管线收敛作为 MOVE 前置门禁；之后 MOVE → DELETE → RENAME 每波独立 CI 绿 |
| R7 | v0 用户升级断崖 | —/中 | migration guide 覆盖 66 选项逐一映射；v0.x tag 冻结可长期 pin |
| R8 | P0.2 波及面 ≈ 全根包 32 个非测试文件（合同类型必须随 engine 迁移并别名回指，否则依赖成环） | 高/中 | ✅ 已兑现：p0-inventory.md 为路线图分四批推进，两次中断均无损恢复 |
| R9 | ✅ 已裁决（P4.9 前置检查完成）：`PersistentProcess` 确认只在 `cl/opt_examples`（claude/ 相对 main 分歧 17 文件 +2407 行，是完整的常驻进程复用 feature——batch/streaming/HITL 三路，4ac64f3——非单个开关字段）。**裁决**：不移植，v1.0.0 范围外。P4.9 的 team-agent-workflow 示例按本分支现有进程模型落地，`PersistentProcess: true` 行以注释形态保留（注明"该能力随 cl/opt_examples 合入 main 后可开启"）；§9.7 文档同步加注。理由：该 feature 是 claude 驱动内部优化，日后合入是 Config 结构体 additive 字段，与 v1 API 形状零冲突，不构成发布阻塞 | — |
| R10 | staging 已形成第二套执行编排，MOVE 会把旁路固化为 v1 根合同 | 高/高 | takeover audit 逐项关闭；以跨 `Run`/`Stream`/`Thread` 的同构结果、checkpoint、取消和背压合同测试证明单管线 |

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
- P5.2 MOVE 的直接前置不是“P0–P4 已交”，而是 takeover audit 阻断项关闭 + PRE 验收提交 + 单管线收敛。
- 规模预估（PR 数）：P0 ×3、P1 ×2、P2 ×1、P3 ×3、P4 ×3、P5 ×3，共约 15 个可独立评审的 PR。

---

## 7. 历史启动动作（P0 已完成，仅作记录）

1. `docs/api-v1-implementation-plan.md`（本文）+ `docs/api-v1-redesign.md` 合入 `v1` 分支；
2. P0.1 spike：`next/options_scope_test.go` 里两案原型 + 决策注记（半天量级）；
3. P0.2 首刀：`internal/engine` 建包，先移 `util.go`/`config.go` 等无依赖件，验证「移动 + 别名回指」的机械流程与 CI 表现；
4. CI：`go.yml` 补 `next/...`、`driver/...`、`internal/engine/...` 的 job。
