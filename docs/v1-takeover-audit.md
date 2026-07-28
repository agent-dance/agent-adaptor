# v1 重构接管审计与执行清单

> **接管结案（2026-07-27）**：本文已从“待执行清单”转为“已执行审计证据”，不是当前 API 使用说明。文中 `next/`、`pkg/`、旧 SDK/RunHandle/registry、临时 `V1` 名和“未开始/阻断”措辞均保留为接管时事实。
>
> PRE、G-01～G-12、P4 14 项、Driver SPI 9 项、bridge 保真、REHOME/DELETE/ROOT CUTOVER/RESIDUAL DELETE/RENAME/DOCS 均已关闭；ROOT CUTOVER 提交为 `cc6cd82`。当前仍是 **[Unreleased]**，未创建 `v1.0.0` tag，也不虚称尚未取得结果的远端 Linux CI 已成功。

## 0. 最终关闭矩阵

| 项目 | 状态 | 修复与测试证据 |
|---|---|---|
| G-01 唯一执行管线 | **已关闭** | `invocation.go` 成为 Agent/Thread 唯一 coordinator；`Run` 严格为 `Stream + drain + Result`；AST/architecture tests 锁定唯一 `driver.Run`、Thread persist 与 finalize 路径 |
| G-02 checkpoint 污染 | **已关闭** | Claude/Cursor/CodeBuddy/Codex CLI/app-server 各自正式 parser 判定 checkpoint；非零退出、取消、协议错误和缺失 checkpoint 不持久化；provider protocol tests 与 Thread 旧 active 保留测试覆盖 |
| G-03 key 碰撞 | **已关闭** | `internal/keycodec` 对复合维度做长度/结构编码；Thread 原始 key 逐字保存；store/bridge 表驱动覆盖分隔符、Unicode、同前缀和往返 |
| G-04 Fork 安全 | **已关闭** | fork 校验 Driver、identity、fingerprint、codec/checkpoint，协调父/目标 lease，目标存在时原子拒绝；`threadsession_fork_test` 覆盖并发、CAS、persist failure 与父状态不变 |
| G-05 fingerprint | **已关闭** | recipe 覆盖 Driver 类型/完整构造配置、codec、identity/model、resolved workspace、runtime、profile/skills/MCP/instructions；provider fingerprint 与 Thread 维度变化测试锁定跨进程稳定性 |
| G-06 Cancel/backpressure | **已关闭** | event broker 的 blocking send/approval/resource waits 同时监听取消；Cancel 幂等并有界解除阻塞；buffer 0/1、停止 drain、并发 producer、deadline/approval race tests 覆盖 |
| G-07 Codex app-server | **已关闭** | JSON-RPC 路径先 tee 完整 stdout，再由同一 accumulator 产出 Text/Transcript/Usage/failure/checkpoint/terminal；reader/process/stderr 全部结束后快照；fixture tests 覆盖成功/失败/取消 |
| G-08 terminal payload | **已关闭** | `driver.RawStreams.Terminal *TerminalPayload` 字节保真进入 `Result.Raw().Terminal`；Response→Result 深拷贝，Run/Stream 与失败路径合同测试覆盖 |
| G-09 Inspect config | **已关闭** | 内置 Driver 捕获构造期真 Config，Run/Inspect/probe 观察同一配置；四 provider configured/probe contract tests 与公共边界测试覆盖 |
| G-10 Approval 零值 | **已关闭** | 未绑定/零值 responder 立即返回稳定错误，settle exactly-once；零值、重复、kind mismatch、超时、并发和 run-ended tests 覆盖 |
| G-11 Config internal 泄漏 | **已关闭** | 四 provider 拥有真 `Config`；公开 skill/mcp/profile/structured error/DTO 归公开叶子包；AST/import boundary tests 禁止公共签名泄漏 `internal/*` |
| G-12 archive 合同 | **已关闭** | closure 不再用函数地址猜等价；显式 Fingerprint 表示声明 identity，缓存由内容寻址；相同代码不同捕获、同/异 fingerprint、缓存污染/命中测试及 archive fuzz 覆盖 |

### P4、SPI 与 bridge 关闭证据

| 清单 | 状态 | 最终证据 |
|---|---|---|
| P4.9 14 项 | **已关闭** | #1–6/#8/#11–13 完成实现和回归；#9 继承 PRE；#7/#10/#13 accessor/#14 以单 Event 流、展示边界、Result observation、transport 分层明确拒绝并由 `adaptertest/api_boundary_test.go` 防回归 |
| Driver SPI 9 项 | **已关闭** | driver godoc 与最终 `adaptertest` 的 EVT/SES/SO/RSP/CAP/RUN 条款一致；reference driver 正向、negative table 反向、四 provider conformance 接线完成 |
| SSE | **已关闭** | 审批请求/错误映射、EventMeta/Last-Event-ID、raw thread key、断连取消、Result-authoritative terminal 由 handler/approval correctness tests 覆盖 |
| AG-UI | **已关闭** | user-turn Event、最后用户消息 ID、完整 Args、approval、Dropped、failure/terminal、取消与 thread key 映射由 input/events/verifier tests 覆盖 |
| A2A | **已关闭** | typed adapter event decode、ExposurePolicy、approval/Dropped/failure/terminal、context key 无碰撞、严格生命周期/单终局由 mapping/server/status tests 覆盖 |
| subagentstream/hosttools | **已关闭** | 单流 merge、终局最后/重排/source meta/cancel；delegation ordered access、typed session recorder JSONL 与持久化失败语义均有测试 |

### 迁移与冻结结论

| 波次 | 状态 | 证据 |
|---|---|---|
| PRE | **已关闭** | `46a726f` |
| CORRECTNESS | **已关闭** | `82d2f00` 及随后同波回归 |
| REHOME/REPOINT + LEGACY EDGE DELETE | **已关闭** | cutover 前生产 import/旧边缘清零并通过 build/test/vet 波次门禁 |
| ROOT CUTOVER | **已关闭** | `cc6cd82` |
| RESIDUAL DELETE | **已关闭** | Core/binding/admin/execute/decision/options、metadata fallback、临时 aliases/wrappers/死码清除；v1 Thread/Inspect/skill/profile/runtime 真路径保留 |
| RENAME | **已关闭** | Go 临时 `V1` 后缀清除、adaptertest 上提、examples 最终命名；线上 `adapter.stream.v1` wire 名保留 |
| DOCS | **已关闭** | 活跃 README/godoc/API/usage/streaming/A2A/structured/migration/CHANGELOG 同步，历史工作流移入 `docs/archive/` |
| FREEZE/发布 | **实现冻结已执行；发布待外部结果** | 本地 build/test/vet、定向重复/fuzz/examples/API 边界由最终冻结波执行；Linux race/CI 与 `v1.0.0` tag 不在没有真实结果时标成功 |

### 0.1 `~35` 导出名门禁的显式设计勘误

批准的 Claude 草稿原文写过“应用开发者，~35 个导出名”，实施计划 P5.5 也曾写“根包导出名 ≤~35”。最终文档不能通过改写句子假装这个字面门禁从未存在。

完整 AST 机械清点得到根包 229 个顶层导出标识符：64 个 const、35 个 var、41 个 func、89 个 type（53 个 defined type、36 个 alias），另有 54 个具体类型导出方法。它们的主体不是 229 套平行执行入口，而是同一设计同时要求的 typed Event/Result/approval/profile/service DTO、枚举、稳定 error sentinel、options、小型 schema/approval constructors，以及避免消费者强制 import `driver` SPI 的 alias。若把 raw declaration 数字压到 35，必须删除这些已批准的类型安全合同，或退回 string/`any`/巨型结构体；这与 v1 的生产级、强类型和可维护性目标自相矛盾。

因此接管实施作出并公开记录如下修订：消费者 API 的量化门禁是 **24 个 `With*` 名 + 约 13 个核心概念组**；全部 229 个 raw exports 的种类、字段、tag、alias/defined 区别、const 值、函数/方法/interface 签名则由 `testdata/root_api.golden` 的完整 AST freeze 守卫。授权依据是用户要求完整实现 Claude v1、接受正确性修复并以生产级质量收口；该授权不应被解释为可以删除同一 v1 设计要求的 typed 合同。任何未来新增仍必须显式评审并更新 golden。本段是设计勘误，不是对原验收线的静默移动。

### 0.2 最终本地冻结证据（推送前）

2026-07-27 在最终代码树上完成以下门禁；这些是接管后对中断项及其依赖项的最终验收，不重复 Claude 已明确签收的 PRE 子项：

- Go `1.26.5 windows/amd64`：`go build -p=4 ./...`、`go test -count=1 -p=4 ./...`、`go vet ./...`、`go mod verify` 全部通过。
- `govulncheck@v1.6.0 ./...` 报告 0 个可达漏洞；依赖图中的不可达公告不冒充可达风险，也不以危险的跨 major override 掩盖。
- `AGENT_ADAPTOR_LIVE_CONFORMANCE=0` 下，`claude_live`、`codebuddy_live`、`codex_live` 三套 build-tag 测试全部通过且没有调用真实/付费 provider。
- 最终修改影响到的六个协议 fuzz 目标各真实运行 30 秒并通过：Claude/Cursor/CodeBuddy/Codex batch parser、Codex app-server notification decoder、Codex `ThreadItem` union；三项 archive fuzz 已在其代码稳定后各运行 30 秒，本轮未再修改 archive 路径，按“不重复已完整验收项”原则不机械重跑。
- 根 API 与 Driver SPI 的 AST golden 已补齐 sealed interface 私有方法、私有嵌入提升出的公开 method set、常量精确值/静态类型、变量推断静态类型和 build-tag 文件选择；变异测试、vocabulary guard 与最终 golden 均通过。
- 资源/并发最终 hostile tests 覆盖满缓冲取消终局保留、late producer 封口、无视 context 的 run-service/session hook、有界且全部尝试的 unwind、lease renewal、128-bit fail-closed fencing entropy；核心关键测试最高重复 100 次通过。
- 四个 Driver 与 Codex app-server 的正式协议、Fork、resume reject、checkpoint、structured output、ExtraArgs/Policy 与 terminal staging 由 Driver reviewer 和独立 architecture reviewer 交叉签收；关键矩阵重复 10～20 次并通过相关 vet/fuzz。
- 两份前端 lock 仅使用 `registry.npmjs.org`，AG-UI 版本守卫对齐 `0.0.57`；AG-UI clean install/build/audit 通过，CopilotKit lock dry-run、lint/build 与 high audit 通过。Windows Node 24 的一次官方源 clean install 在展开依赖后 I/O 停滞，因此不虚称该环境成功；推送后的 Node 22/Linux `frontend` job 是最终 fresh-install 权威门禁。
- 活跃 Go 源码没有旧 `/next`、`/pkg`、`/providers` import；活动目录没有实际 TODO/FIXME/`🚧`、本机 Claude project 路径或临时 `V1` 名。剩余 `V1` 仅属于明确版本化的 `adapter.stream.v1` A2A wire contract。
- 四条独立终审（架构、API/文档、Driver、release）全部给出 PASS；Windows 无 CGO/GCC，`go test -race ./...` 必须由推送后的 Linux CI 完成。

推送前仍不得写成“远端 CI 已绿”。下一项且唯一开放的外部门禁是：创建新的 `codex/**` 分支、推送，等待 validate/race/fuzz/frontend 四个 job 全绿；本轮没有 `v1.0.0` tag 授权。

> 历史状态：**接管时执行事实源（现已结案）**
> 接管日期：2026-07-27  
> 设计断点：`4a66cc3`  
> 历史适用范围：Claude 设计的 v1 API 从当时 staging 状态到实现冻结的全部收口工作

本文件保存本次接管调研的可验证结论、未关闭问题、执行顺序和验收证据，避免会话中断后再次依赖聊天记录还原状态。它不是新的产品设计；产品合同由根 [`AGENTS.md`](../AGENTS.md) 和 [`api-v1-redesign.md`](./api-v1-redesign.md) 定义。

权威顺序：

1. 根 [`AGENTS.md`](../AGENTS.md)
2. [`api-v1-redesign.md`](./api-v1-redesign.md)
3. 本文件
4. [`api-v1-implementation-plan.md`](./api-v1-implementation-plan.md) 与 [`p5.2-recon.md`](./p5.2-recon.md)

机械迁移清单不能覆盖本文件的正确性门禁。状态变化必须在本文件留下代码、测试和提交证据，不能只写“基本完成”。

## 1. 最终产品裁决

用户已于 2026-07-27 最终确认：**Claude 设计的 v1 API 完全取代旧 `AGENTS.md` 合同。**

因此：

- `Agent · Thread · Stream · Event · Result · Driver` 是唯一的六个核心名词。
- 不再以中央 `SDK`、默认/命名 Agent registry、`Runner` 查找、`Start`、`RunHandle` 或多条事件/决策 channel 作为产品方向。
- 唯一消费者构造入口是 `adaptor.New(driver, opts...)`；多 Agent 由宿主持有多个 Go 值。
- 唯一执行动词是 `Run` / `Stream`；`Agent` 与 `Thread` 实现同一 `Runner` 合同。
- 旧 API 只作为 P5 的删除对象存在。不得再为它新增能力，也不得用兼容 shim 把它带进 v1。

这项裁决已经写入根 `AGENTS.md`，此前“旧 AGENTS 与 v1 redesign 谁优先”的架构冲突已关闭。

## 2. 调研依据

本次审计交叉核对了：

- Claude 主会话记录：仓库外的本地 Claude project 会话 `0bb133c6-9bdd-498c-9a90-9d4d73f8edc7.jsonl`
- Git 提交历史、分支/远端引用和未提交工作树
- `docs/api-v1-redesign.md`、实施计划、P0 inventory、P5.2 recon、迁移指南
- `next/` staging API、`internal/engine`、四个内置 Driver、Codex app-server、memory/thread store、bridges/hosttools
- P4.9 的 14 项可用性反馈与 `adaptertest` 记录的 9 项 SPI godoc 缺口
- 全仓非缓存测试、vet、定向测试和已有 fuzz 结果

Claude 主会话并非因代码或测试失败停止。最后一个 PRE Config 子任务正常报告完成后，主协调器在生成下一轮响应时收到 `oauth_org_not_allowed` 403；会话记录停在主 JSONL 第 1715 行。其影响是“未完成接管验收、汇总和后续实施”，不是“子任务改动未落盘”。

## 3. 接管快照

以下是开始修改治理文档之前的可复现快照；本次新增/修改的治理文档不计入 Claude 遗留 PRE 数量。

| 维度 | 接管事实 |
|---|---|
| 分支 | `claude/sdk-api-redesign-64bddf` |
| HEAD | `4a66cc3533d8b74f362059524f2ac05195b8c678` |
| main 基点 | `bbba7a0` |
| 相对 main | ahead 41 / behind 0；41 个提交线性、无 merge commit |
| v0 冻结基线 | 当前最新已推进到 `v0.12.0`，旧计划中的 `v0.9.x` 假设作废 |
| 远端 | 有 `origin`；当前分支无 upstream，远端无同名 head，远端 head 也没有等于当前 HEAD 的引用 |
| PRE 工作树 | 55 个既有脏路径：41 modified、4 deleted、10 untracked、0 staged |
| tracked PRE diff | 约 `+574/-2105`，不含 10 个未跟踪文件 |
| 基础验证 | `go test -count=1 ./...` 通过；`go vet ./...` 通过 |
| race | 本机 `CGO_ENABLED=0`，需在 Linux CI 执行 `go test -race ./...` |

远端结论的严格表述是：该重构分支尚未以同名远端分支发布；仅凭当前远端 refs 不能证明其中个别提交从未作为其他分支祖先出现。

## 4. 阶段真实进度

| 阶段 | 当前结论 | 证据/剩余边界 |
|---|---|---|
| P0 | 已有提交实现，不等于最终合同冻结 | engine/SPI/staging 骨架已落地；单一执行管线不变式尚未兑现 |
| P1 | 已有提交实现 | `Stream`、统一 Event、ApprovalRequest 已落地，但取消背压与零值 responder 有阻断缺口 |
| P2 | 已有提交实现 | Thread/store/lease/resume/fork 已落地，但 fingerprint、fork、key、checkpoint 安全仍有阻断缺口 |
| P3 | 各 wave 已提交 | skills/MCP/profile/structured output 等已接线；仍有 P5 收编和 archive 合同问题 |
| P4 | 各工作项已有实现提交 | bridges、hosttools、examples、S6/S9 已落地；14 项可用性和协议保真缺口未收口 |
| P5.1 | 未正式验收 | 应以 `v0.12.0` 核对 tag/维护线 |
| P5.2 PRE | **有未提交实现，待独立验收** | 当前 dirty 主要是 D-P5.2-2/5/6/7；不能因测试绿直接标完成 |
| P5.2 MOVE | 未开始 | 被 §6 的正确性门禁阻断 |
| P5.2 REHOME/DELETE | 未开始 | D-P5.2-3/4 先归位，旧 API/aliases/转发包再分批删除 |
| P5.2 RENAME | 未开始 | V1 后缀、adaptertest 上提、examples/docs 最终命名均未做 |
| P5.3 | 核心 suite 已提交并上提 | `adaptertest` 14 子测试/51 条款已落地；SPI godoc 含糊点已按冻结门禁硬化 |
| P5.4 | 仅迁移指南初稿 | 大量 `🚧` 尚在；README/API/usage/streaming/A2A 等最终文档未重写 |
| P5.5 | 未开始 | race/fuzz/examples/godoc/export surface/CHANGELOG/tag 等发布门禁未完成 |

结论：P0–P4 是“已有大量实现提交”，不是“v1 已完成约 80% 就可以直接 MOVE”。当前最关键的剩余工作不是机械搬文件，而是先修复已经审出的合同错误并收敛为一条执行管线。

## 5. PRE 工作集与验收记录

### 5.1 实际范围

当前 dirty 工作集主体对应：

- D-P5.2-2：四个 Driver 的 Config 真结构体化与转换接缝
- D-P5.2-5：`EffectiveHumanDecisionPolicy`、Session 参数键、`NormalizeProfileDir` 下沉
- D-P5.2-6：移除 `engine_wiring.go` init 注入；archive/materializer 真身和 lease 默认值下沉
- D-P5.2-7：archive 自冲突修复、skill/archive/dirscan 迁移和相关测试搬迁

D-P5.2-3（Profile 资源族）与 D-P5.2-4（HITL typed 族）**没有在当前 PRE 完成**，Claude 当时主动延期；机械复核后已明确移入 REHOME/REPOINT 波。`p5.2-recon.md` 已按实际依赖图校正。

### 5.2 Claude 验收证据的继承边界

用户明确要求不要重复验收已经由 Claude 主协调器确认的工作。会话证据恢复后的边界如下：

| 项目 | Claude 主验收证据 | 接管处理 |
|---|---|---|
| P5.2 侦察与 12 项裁定 | 主会话 L1640–1650 明确认可并提交为 `4a66cc3` | 完全继承，不重做文件分类或依赖盘点 |
| D-P5.2-5 policy/SessionParam/Normalize 下沉 | L1684–1703 主协调器亲自 build/vet/test、grep 调用点并写明“任务 2 确认完整” | 完全继承，不做专项重验 |
| D-P5.2-6 skill/archive/init/lease 迁移 | L1684–1706 主协调器亲自跑全仓门禁并写明已完成；续做任务明确“不要碰” | 功能验收继承；只修后来确认的公共类型归属和 archive 合同缺口 |
| D-P5.2-7 archive 自冲突原 bug | 首个 PRE agent L277–289 做过 mutation 验证，主协调器明确列为已修 | 原测试有效性继承；只补 opener/Fingerprint 新反例 |
| D-P5.2-2 Driver Config 真结构体 | 续做 agent L86–207 实现并跑绿；主会话 L1708–1710 收到报告后在 L1715 立即 403 | 不从头重验；补主审、公共 internal 泄漏、Inspect/config fingerprint 等依赖修复 |
| PRE 整体签收与提交 | L1706 明确承诺在 D2 后做，但 403 前没有执行 | 这是实际中断点，必须继续 |
| D-P5.2-3/4 Profile/HITL | PRE 派单主动延期 | 不是中断遗漏，REHOME/REPOINT 波实施 |

会话证据文件是仓库外的本地 Claude project 会话 `0bb133c6-9bdd-498c-9a90-9d4d73f8edc7.jsonl`；首个 PRE agent 为 `subagents/agent-a734921096b1f59fa.jsonl`，D2 续做 agent 为 `subagents/agent-a30507ca107bb8502.jsonl`。

### 5.3 最小补验与修复清单（已完成）

完成以下事项后，PRE 才能形成独立、可追溯提交：

- [x] 定向修复 D2：四个公开 Driver `Config`/`CommonConfig` 不再 alias `internal/engine`；字段、零值、逐字段转换及 Run/Inspect/probe 捕获语义一致；构造、转换和每次注入均深拷贝可变配置
- [x] 定向修复 D6 公共边界：`skill` 的公开 Format/Archive/Provider/Catalog/Set/Materializer 等词汇不 alias `internal/*`；解压与物化机器继续局部化在 engine
- [x] 保留 D7 原回归，新增“相同 closure code、不同捕获内容”反例；opener 等价性不再通过函数指针猜内容，Fingerprint/cache/source identity 合同一致
- [x] 只运行一次 PRE 综合门禁：`go build -p 4 ./...`、`go vet ./...`、`go test -count=1 -p 4 ./...`、关键包 `-count=5`、skill-only 依赖图和 `git diff --check` 全绿（2026-07-27）
- [x] 独立 reviewer 两轮复核；首轮发现 Config 浅拷贝和三包 mutation 测试缺失，修复后最终签收无阻断

明确不重复：D5 的 7 个 policy 调用点、4 个 SessionParam 和 NormalizeProfileDir；D6 的 init/factory/lease 基础功能；D7 已有自冲突 mutation 验证；P5.2 侦察清单。

Thread compatibility 的 Driver config fingerprint 属于 G-05/唯一 invocation 的 resolved-state 配方，不在 PRE 另造一套实现。

## 6. MOVE 前阻断级合同缺口

下面每一项都必须以“实现 + 回归测试 + 文档/合同同步 + 可追溯提交”关闭。单纯全仓测试绿色不算关闭。

### G-01 唯一执行管线尚未兑现

**确认事实**：`next/stream.go` 自行 apply options、acquire/resolve resources、创建 sink 并直接调用 `driver.Run`；`next/thread.go` 又自行执行 Thread prepare/lease/resume/fork/persist。它们复用了 engine helper，但没有共同收敛到一份 resolved invocation orchestration，也没有走旧 `Core.Execute`。

**要求**：提取一份 v1 唯一执行编排；Agent 与 Thread 只提供 receiver-specific thread stage，`Run` 必须是 `Stream + drain + Result`。provider transport streaming 协商是 resolved invocation 的能力选择，不能和 SDK 的 `Run`/`Stream` API 入口机械绑定。

**验收**：同一 fake Driver 下，Agent Run/Stream、Thread Run/Stream 对 options、资源生命周期、事件、Result、RunError、checkpoint、取消产生逐字段同构结果；用测试证明只存在一个 Driver dispatch 点和一个 finalize 路径。

### G-02 非零退出会污染健康 Thread

**确认事实**：Claude、Cursor、CodeBuddy parser 在已有正式 session id 时仍可对非零 exit 返回 `Valid: true`。当前 Thread persist 又以 checkpoint Valid 为主要门禁，失败运行因此可能覆盖健康状态。

**要求**：四个内置 Driver 与 Codex app-server 的 checkpoint 合同统一为：非零退出、取消、协议错误、业务失败默认 invalid；只有 provider 明确保证健康可续且合同记录了例外时才能持久化。

**验收**：每个 Driver 至少覆盖成功、非零退出、畸形协议、缺失 checkpoint；Thread 回归测试证明失败后旧 active record 原样保留。

### G-03 Thread key 编码存在碰撞

**确认事实**：legacy memory store 使用 `namespace + ":" + key`；engine lease target 同样用冒号拼接；v1 SSE bridge 使用 `ns + "/" + key`。诸如 `("a:b","c")`/`("a","b:c")` 或含 `/` 的输入会碰撞或失去维度。v1 公共 Thread 虽是单一 key，过渡 store/bridge 仍会引入复合编码风险。

**要求**：core 将 v1 Thread key 作为不透明字符串逐字保存；需要复合维度的 bridge 必须使用明确无碰撞编码或结构化键，不能靠未转义分隔符。

**验收**：覆盖空值、分隔符、Unicode、同前缀与跨 bridge 映射的表驱动测试；不同业务 key 永不解析到同一 active record/lease。

### G-04 Fork 兼容性、租约与目标冲突不安全

**确认事实**：当前 fork 先按 parent ID 构造计划，未完整验证 Driver/config/identity/fingerprint 兼容；父记录协调租约不足；目标 key 已存在时旧 active 可能没有归档或拒绝，留下多个 active/索引错配。

**要求**：Fork 校验父 checkpoint、Driver、构造配置、identity、resolved fingerprint；协调父状态且不修改/归档父 Thread；目标 key 冲突必须按明确策略原子拒绝或替换，默认不得静默覆盖。

**验收**：同 key、已有 target、不同 Driver/config/identity/workspace/runtime、并发 fork、lease lost、persist failure 全覆盖；任何失败后父/目标记录与索引保持原状。

### G-05 Thread fingerprint 缺少关键维度

**确认事实**：`next/thread.go` 当前 fingerprint 只覆盖 driver type、identity、model、调用设置中的 workspace 和 profile payload fingerprint；没有完整 Driver 构造配置、实际 resolved workspace fingerprint、runtime service fingerprint，且自定义 config 跨进程稳定性无可靠扩展点。

**要求**：定义确定性 canonical fingerprint recipe，覆盖根 `AGENTS.md` §6 列出的所有续接维度；无法稳定 canonicalize 的自定义 Driver 必须提供稳定 fingerprint 或在 Thread 模式启动前被拒绝。

**验收**：每个维度逐一变化都会拒绝不安全 resume；无关 metadata 变化不会误拒绝；map 顺序与进程重启不改变 fingerprint。

### G-06 `BackpressureBlock + Cancel` 可能闭环死锁

**确认事实**：blocking sink 在 event channel 满时可阻塞 Driver；`Cancel()` 只取消 context，而 stream close/finalize 又等待 Driver 返回。如果消费者取消后停止 drain，双方可能永久互等。

**要求**：所有阻塞发布必须同时监听 run cancellation；Cancel 必须幂等并解除 event send、approval wait、resource acquire/renew；终局关键事件的交付策略必须明确且无泄漏。

**验收**：buffer=0/1、消费者停止读取、Driver 并发 producer、审批等待、重复 Cancel、deadline 下均在有界时间结束；goleak/race 测试无残留 goroutine。

### G-07 Codex app-server 输出合同不完整

**确认事实**：当前 app-server 路径主要聚合 assistant 文本和 stderr buffer；没有与 CLI 路径等价的完整 raw stdout、Transcript 和 provider terminal Result。JSON-RPC 路径因此降低了 Result 审计能力。

**要求**：用正式 app-server notification/response 同一次解析产出 Text、Summary、完整 Raw、Transcript、Usage、terminal payload 和 checkpoint；不得手工编辑 generated schema。

**验收**：fixture 覆盖 assistant/thinking/tool/result、stderr、失败和取消；同一语义的 CLI/app-server Result 分层合同等价。

### G-08 Driver terminal payload 在 v1 Result 中丢失

**确认事实**：`next/result.go:resultFromResponse` 复制 Output/Summary/Usage/RawStreams/Transcript/StructuredOutput/RuntimeServices，但没有保存 `driver.Response` 的 provider terminal Result。

**要求**：`Result.Raw()` 或同一审计分组必须保留 Driver 已识别的终局原始 payload，不得混入 Text/Summary。

**验收**：Run 与 Stream.Result 在成功、业务失败和部分结果路径逐字段一致，terminal payload 字节保真。

### G-09 Inspect 丢失构造期 Driver config

**确认事实**：`next/inspect.go` 明确写着 config always nil，并向 Environment/Models/Quota/ConfigSchema/Skills/Profile probes 传 nil；但 PRE 的 `Driver(Config)` 已捕获真实配置，probe 仍可能观察不同世界。

**要求**：Driver SPI 明确“捕获配置后 probe 无 cfg 参数”或让 Agent 保存并传递同一份公共 config；不允许运行使用配置 A、Inspect 静默探测默认配置 B。

**验收**：四 Driver 用非默认 executable/profile/model/config 跑 Inspect 合同测试，断言 probe 与 Run 观察一致。

### G-10 零值 ApprovalRequest 会永久阻塞

**确认事实**：`ApprovalRequest.settle` 最终向 `r.reply` 发送；正常构造会创建 buffered channel，但 `&ApprovalRequest{}` 的 reply 为 nil，Approve/Deny/Answer 可永久阻塞。

**要求**：零值、过期、脱离运行或未绑定 responder 立即返回稳定错误；exactly-once 和 Kind mismatch 保持结构化可判别。

**验收**：零值/拷贝/重复/并发/超时/运行结束后应答均有界返回，并通过 race。

### G-11 公共 Config 仍泄漏 internal 类型

**确认事实**：PRE 虽将四 Driver `Config` 改为真结构体方向，但 `CommonConfig = engine.CommonConfig` 等 alias 仍使消费者公共面依赖 internal 形状。

**要求**：内置 Driver 包拥有全部真实公共配置词汇；转换只在包内发生，公共签名、字段、alias 不出现 `internal/*`。

**验收**：AST/import 边界测试扫描所有导出类型；四 Driver config round-trip、零值与 Validate/Run/Inspect 一致。

### G-12 archive opener/Fingerprint 合同不可靠

**确认事实**：函数值只能可靠比较是否为 nil；用函数指针判断两个 closure 是否打开同一内容会误判。与此同时，`SkillFromArchive.Fingerprint` 的 godoc 把它描述为缓存键，materializer 又主要按内容寻址，合同冲突。

**要求**：source identity 由显式稳定 fingerprint/内容 hash 表达，不猜 closure 捕获值；明确 Fingerprint 是兼容 identity、缓存 hint 还是内容摘要，并让等价性、cache、Thread fingerprint 使用同一语义。

**验收**：不同 closure/同内容、同 closure/内容变化、显式 fingerprint、无 fingerprint、缓存命中/污染均覆盖。

## 7. ROOT CUTOVER 前必须关闭的 API/SPI/bridge 工作

Claude 已把这些事项列入 P4/P5 收口，但尚未实施。它们会改变 staging Event/Result/bridge 公共合同和迁移文件集合，因此必须与 G-01～G-12 一样在 ROOT CUTOVER 前关闭，不能搬到根包后再返工。

### 7.1 P4.9 的 14 项 API 可用性缺口

权威原清单在实施计划 P4.9。逐项“实现或明确拒绝并写合同”，不能留口头结论。当前裁决为：

1. `mcp.Stdio` 的 Option 组合能力
2. `profile.Resources` 能否只 import `profile` 填满
3. ApprovalRequest 零值安全（同时属于 G-10）
4. sessionrecorder 的 v1 typed Event JSONL backend 与持久化失败语义
5. SSE v1 审批错误映射助手
6. AG-UI user turn 的 v1 Event 访问面与最后用户消息 ID
7. **最终拒绝新增** SSE `SubagentBus`/`WrapAGUI` v1 平行入口；typed Event + `team.Option()` 是唯一主路径
8. A2A adapter stream status 的 v1 Event 解码
9. Driver 会话参数键导出（PRE D-P5.2-5 已完成，本波只引用）
10. **最终拒绝进入 core** 的 `SubagentEventKind.String()`、ToolCall args 预览和 `Summary`→`Text` 展示兜底（UI、安全展示与 fallback policy 属于宿主）
11. 本次 run 的有序 delegation 访问器
12. `Result.Summary` 与 `Result.Text` 的稳定语义
13. **最终拒绝新增 attachment accessor**；`Result.Services()` 是唯一 typed runtime observation 面，按稳定 ID 合并时 driver observed 覆盖同 ID，SDK ensured 只补缺失
14. **最终拒绝新增**供 A2A policy 使用的 provider streaming 查询；委托 transport 与 provider rich transport 是不同语义

实施项为 1～6、8、11～13；第 9 项已由 PRE 完成，不重复验收；7、10、14 以 godoc/测试明确拒绝。

上述四项拒绝裁决已经关闭，不再作为“以后可以补的便利 API”：

- **#7 单流边界**：`team.Option()` 把 `SubagentUpdate` 注入 Runner 的唯一 Event 流，SSE/AG-UI 只翻译该流。再次接收 bus 或包装第二条 AG-UI 流会引入重复事件、双重 drain 与不同取消顺序。`adaptertest/api_boundary_test.go::TestNoParallelSubagentBusAPI` 锁定 SSE 不依赖 delegation overlay。
- **#10 展示边界**：`SubagentEventKind` 是语义枚举，`ToolCall.Args` 是未删改的原始结构；统一字符串化或预览必然替 SDK 选择截断、脱敏和本地化策略。`Summary` 允许为空且不从 `Text` 自动兜底，宿主可按自己的展示场景选择 fallback。`TestPresentationPolicyStaysOutsideCore` 锁定 core 不增加这些展示方法或 fallback accessor。
- **#13 观察边界**：run attachment 含输入声明、事件源和 secret 生命周期，不能伪装成执行观察或被 Result 旁路暴露。宿主只从 `Result.Services()` 读取实际观察/ensure 报告；公开 Result 方法集不增加 attachment accessor，由同一架构测试固化。稳定 ID 合并的行为正确性仍必须由 Result 合同测试覆盖，不能靠新增 accessor 绕过。
- **#14 transport 边界**：所有 v1 Runner 都有 `Stream`；A2A `RequireStreaming` 询问的是远端 A2A transport，Remote 以 AgentCard 协商，Local 直接消费 Runner.Stream。`driver.StreamSupport` 仅陈述 provider-native 事件保真，不能成为 A2A policy 查询面。`TestNoProviderStreamingCapabilityQuery` 锁定 Agent/Thread/Inspector 不暴露这种混合语义的查询。

### 7.2 Driver SPI 的 9 项 godoc 硬化

Claude 原报告的九项是：生命周期终局；SessionCodec 零值；Session 参数键；Sequence/Seq 权威；structured-output capability 矩阵；HumanDecision failure invariant；checkpoint validity；`SupportsResume` 与 SessionCodecProvider 双向真话；Transcript mirror。

合同硬化已关闭，映射如下：

| 原缺口 | `driver/` 权威合同 | `adaptertest` 硬条款 |
|---|---|---|
| 生命周期终局 | `StreamKind` / `StreamPayload` | EVT-01、02、11（成功和错误终局都不得留开放 lifecycle） |
| SessionCodec 零值 | `SessionCodec` | SES-01、02、07 |
| Session 参数键 | PRE 导出的 `SessionParam*` 常量；本波只引用、不重复验收 | SES-08 |
| Sequence/Seq 权威 | `EventSink`、`RunEvent`、`StreamPayload` | EVT-10、RUN-03 |
| structured-output 矩阵 | `StructuredOutputCapability` 的 mode × mechanism × provider transport × HITL Ask 完整矩阵 | SO-01～03 |
| HumanDecision failure invariant | `RunFailure` | RSP-02 |
| checkpoint validity | `Checkpoint`、`Driver.Run` | RSP-01、RSP-05；`VerifyOutcome` 同时检查 Go error |
| resume 与 codec 双向真话 | `SessionCodecProvider`、`SessionCapability` | CAP-01 |
| Transcript mirror | `EventSink`、`RunEvent` | RUN-04 |

这些条款均为 MUST，不存在 Lenient/SHOULD 逃生舱。`adaptertest` 的 hermetic reference driver 证明套件正向路径，negative table 证明 SPI 的结构与声明违规会真实被拒绝；structured-output 的三种 mode × provider transport × HITL 运行时选择/拒绝由 core structured contract tests 负责。四内置 Driver 的正式协议/fixture 证据由 §7.4 的 correctness workstream 继续承担，不能以 live probe 默认跳过替代。

### 7.3 bridge/协议保真

逐项验证并决定显式映射或显式降级：

- ApprovalRequest 的 kind/payload/choices/deadline 和 resolved 结果
- Dropped 的 count 及可恢复明细
- Event RunID/ThreadID/Sequence/Time 与生命周期顺序
- `RunError` 的业务失败 code/message/result
- provider terminal result、Raw、Transcript、Usage
- AG-UI/A2A/SSE 的取消、断连、重连和 thread key 映射

### 7.4 Driver 正式协议复核

- Cursor/Claude/CodeBuddy 必须只按各自官方事件识别 output/checkpoint，不以通用 JSON 猜测补洞。
- Codex exec 与 app-server 分别以官方协议/schema 为准；generated Go/schema 只走官方生成命令。
- Summary 不得无界复制完整 Text；没有可靠摘要时允许为空。
- Usage “未观察到”与真实零值若需要区分，必须在类型层定案。

### 7.5 清除迁移痕迹

- 删除 `next/`、旧根 API、`pkg/` forward、provider sugar、legacy metadata parser、无调用死码和临时 alias
- D-P5.2-3/4 在 REHOME/REPOINT 完成类型归位后再删 `engine_aliases.go`
- 删除临时 `V1` 后缀并上提 `adaptertest`
- 清除代码中的真实 `TODO` 和迁移指南全部 `🚧`
- examples 按 recon 映射改名/合并并全部编译；新增 structured-output 最小示例

## 8. 固定执行顺序

除非出现新的 P0 证据，后续实施按以下顺序推进：

1. **保护断点**：保留 `4a66cc3` 与接管快照；不 reset、不覆盖用户 dirty 工作
2. **PRE 独立验收**：按 §5 审计 D-P5.2-2/5/6/7 并形成单独提交
3. **CORRECTNESS**：关闭 G-01～G-12；先收敛唯一管线，再修 Thread/checkpoint/cancel/output/Inspect 等合同
4. **API/SPI/bridge CORRECTNESS**：关闭 §7 的可用性、godoc、协议保真事项
5. **REHOME/REPOINT**：完成 D-P5.2-3/4、公共 internal aliases 归位，并把生产消费者从旧根 API 接缝迁出
6. **LEGACY EDGE DELETE**：旧根尚可编译时逐包移除旧 bridge/hosttool/memory/provider/adaptertest/forward 边缘
7. **ROOT CUTOVER（最终 MOVE）**：删除所有剩余旧根 Go 文件、搬入完整 staging、清除 `/next` import；同一提交根目录仅有 `package adaptor/adaptor_test`
8. **RESIDUAL DELETE**：删除剩余 aliases、兼容解析和死代码
9. **RENAME**：最终命名、adaptertest 上提、examples 整理
10. **DOCS**：README、根 doc、API reference、usage、streaming、A2A、structured output、migration guide 全量同步
11. **FREEZE**：完整发布门禁、CHANGELOG、`v1.0.0` tag

ROOT CUTOVER 是目录/package 切换，不是正确性或类型归位工具。原约 107 文件估算遗漏了 10 个根残留文件、171 个旧根 import 消费者和 correctness 新文件，已作废；每次 cutover 前必须用 `rg`/`go list` 动态重建清单。

## 9. 每波统一验收模板

每个变更波结束都要记录：

```text
变更波：
范围/提交：
关闭的审计项：
公共合同变化：
新增回归测试：
go test -count=1 ./...：
go vet ./...：
go test -race ./...（Linux）：
定向重复/fuzz/live conformance：
独立 reviewer 结论：
仍未关闭项：
```

状态词只允许：

- **未开始**：没有实现
- **实现中**：存在未验收改动
- **待独立验收**：作者报告完成，但 reviewer/门禁未完成
- **已关闭**：实现、回归、文档和提交证据齐全
- **明确拒绝**：架构裁决说明为什么不做，并有防回归合同

## 10. 最终发布门禁

根 `AGENTS.md` §15 是最终门禁。接管阶段尤其要确认：

- G-01～G-12 全部“已关闭”
- P4.9 14 项、SPI godoc 9 项和 bridge 保真清单全部有最终裁决
- `go test -count=1 ./...`、`go vet ./...`、Linux race、关键 fuzz/重复测试全绿
- 四个内置 Driver 通过最终 adaptertest；live conformance 继续使用 CLI 存在 + 显式环境变量双门
- Run 与 Stream.Result 各层逐字段等价，Thread 失败不污染旧状态
- examples、README、godoc、全部活跃 docs 与最终导出面一致
- 无旧 API、双栈、临时后缀、兼容转发、死代码、TODO、`🚧`
- 当前重构分支有可恢复远端引用后，才进入最终 tag 操作

在这些条件全部满足前，不创建 `v1.0.0` tag，也不把“测试当前绿色”描述成“重构完成”。
