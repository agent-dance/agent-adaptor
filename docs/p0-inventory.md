# P0.7 · 根包盘点：逐文件去向映射（v1 重构输入件）

> 状态：只读盘点产出（P0.7）。基线：`claude/sdk-api-redesign-64bddf` @ bbba7a0，根包 54 个 .go 文件（32 非测试 + 22 测试），全部实测核对。
> 配套：[api-v1-redesign.md](./api-v1-redesign.md) §2.1/§4、[api-v1-implementation-plan.md](./api-v1-implementation-plan.md) P0–P5。
> 本文所有去向判断基于真实 import / 符号引用（grep 全仓核实），不是按文件名猜测。§5 列出与既有计划不一致处，是 P0.2 engine 抽取 agent 的必读输入。

## 0. 关键结论速览

1. **根包不 import 任何 `internal/` 包**（grep 证实 0 处）；反向却有 **11/16 个 internal 包 import 根包类型**。P0.2 把管线搬进 `internal/engine` 时，engine 不能回头 import 根包（根包薄包装要 import engine，会成环），所以**管线引用的全部合同类型必须随 P0.2 一起进 engine（或 contract 子包）再以别名回指根包**——实际波及 ≈ 全部 32 个非测试文件，远超 P0.2 触及清单列出的 7 个文件。
2. `archive_source.go` / `archive_materializer.go` 是 **skill 归档源/物化器**（`SkillFromArchive`，zip/tar/tar.gz），不是 run 结果归档。设计文档 §4 把它映射到 `Result.Raw()/Transcript()` 属张冠李戴；v1 skill 词汇（Dir/FS/Inline/Key）**缺 Archive 构造器**，是能力保全缺口。
3. 四个存疑件裁决：`providers/` **删除**；`runtimeservice/` **删除**；`caller_identity.go` → **根包 adaptor**（Identity 类型 + WithIdentity + IdentityFromContext）；`run_policy.go` **四分**（Policy 词汇 → 新根包、merge/validate → engine、HITL 语义 → P1.3、RunPolicyCapabilities → driver 包），P5 删空、无留守剩余物。
4. 实施计划 §3 测试迁移映射表 **22/22 无遗漏**；仅 `archive_source_test.go` 的 P0 归类与其公共 API 性质有张力（建议公共行为断言在 P3.2 收编进 skill 包合同测试）。
5. `pkg/clients/a2a` 无被计划遗漏的依赖方，但 `pkg/bridges/a2a/import_boundary_test.go` 硬编码了 `pkg/clients/a2a` 路径（且 forbidden 列表漏了 codebuddy），P4.1 目录提升时必须同步修订。

---

## 1. 逐文件映射表 — 非测试文件（32）

去向图例：`engine` = internal/engine（P0.2 抽取）；`driver/` = SPI 包（P0.3）；`next→root` = 新根包 adaptor（P0.5 起生长、P5 落位）；`skill/` `mcp/` `profile/` = 词汇包（P3）；`threadstore` = P2；`删除` = v1 不保留（P5.2 删）。凡标 engine 的文件，其在根包的公共导出名都需要「别名回指」维持旧 API 编译（P5 删别名）。

| 文件 | 行数 | 现职责（一句话） | v1 去向 | 阶段 | 依据 / 风险备注 |
|---|---:|---|---|---|---|
| doc.go | 19 | 包级文档（default-agent-first 模型说明） | 重写（6 名词开篇） | P5.4 | 无代码 |
| api.go | 417 | 混装文件：消费者接口（SDK/Runner/RunHandle/AdminAPI/AgentAdmin）+ SPI（DriverAdapter + 10 能力接口 + EventSink + StreamCapability）+ AgentBinding/AgentDefaults + DriverDescriptor/各 Capability/ConfigSchema | SPI 半场 → `driver/`（Driver/Descriptor/EventSink/能力接口/StreamCapability/ConfigSchema 族）；消费者半场被 next 的 Agent/Runner/Stream/Inspect 取代；AgentBinding/AgentDefaults 沉入 engine 合同 | P0.3（SPI）/ P0.5+P1+P3.6（消费者面）/ P0.2（Binding 合同） | 计划 P0.3 只写「api.go（SPI 部分）」——正确，但要注意 DriverDescriptor 引用 RunPolicyCapabilities（run_policy.go）、SessionCapability 等，P0.3 拆分时需连带这些类型 |
| sdk.go | 161 | sdkImpl：Build/New 构造、命名注册表、事件缓冲配置、SetSelectedSkills 进程内覆盖 | engine（命名注册表能力在 v1 删除，设计 §2.2） | P0.2 | P0.2 明列 |
| runner.go | 1804 | 单一执行路径核心：resolveInvocation → 会话协调 → 执行 → checkpoint → 失败 overlay；dualSink/背压/HITL 分发 | engine | P0.2 | P0.2 明列；P1.2 sink→typed event 翻译层建立在其 dualSink 语义上，勿重写 |
| binding.go | 144 | Bind/BindTyped/staticAgentBinding + validateAgentBinding + defaults 深拷贝 | engine（组装合同）；公共 Bind/BindTyped 在 v1 删除（`adaptor.New(driver)` 取代，设计 §2.10） | P0.2 | P0.2 明列 |
| config.go | 33 | extractCommonConfig：对四驱动 Config 的类型开关 | engine（过渡）；P3.1 Config 回家后此机制应改为 driver 包能力声明，P5 删 | P0.2 → P3.1 | P0.2 明列；这是根包对四个具体驱动 Config 的唯一硬编码点，P3.1 的解耦锚点 |
| util.go | 410 | clone*/stableHash 等无依赖工具 | engine | P0.2 | P0.2 明列（§7 首刀件） |
| session.go | 453 | 会话协调：resolveSessionDefaults/lease 续租/finalize（resolvedSessionPlan） | engine；P2.2 的 Thread 方法化映射在 engine 之上做 | P0.2 → P2.2 | P0.2 明列 |
| session_types.go | 174 | SessionMode 枚举/SessionRequest/SessionCompatibility/SessionStore 五方法合同/SessionRecord/SessionLease | SessionStore 合同 → `threadstore.Store`（P2.1）；SessionMode/SessionRequest 被 Thread/NewThread/ResumeOnly/Fork 方法取代；类型本体随 P0.2 进 engine 别名回指 | P0.2（搬）→ P2.1（换词） | memory/ 包过渡期双实现（P2.1 已计划） |
| session_codec.go | 119 | SessionParams/SessionCodec 接口 + passthrough codec + SessionParam* 常量 + normalizeSessionState 管线 helper | SessionCodec 接口 → `driver.SessionCodec`（P2.3）；passthrough/normalize helper → engine | P0.2（helper）/ P2.3（接口） | **P0.2 触及清单未列此文件**，但 normalizeSessionState/sessionDisplayID 是管线依赖，必须随行 |
| options.go | 615 | 三种 Option 类型 + 66 个 With*（SDK 级 9 / Agent 级 17 / Run 级 40） | 类型与合并语义 → engine；新双作用域词汇 → next→root（~24 个，P0.1 spike 定机制） | P0.2（机制）/ P0.5 起逐阶段补齐词汇 | `Option func(*sdkImpl) error` 公共签名引用非导出类型：sdkImpl 移 engine 后需 `type sdkImpl = engine.XXX` 整体别名，属 P0.2 高危机械点 |
| run_types.go | 661 | 巨型合同文件：AgentIdentity/RawStreams/RunResult/Usage/RunEvent/TranscriptItem/DriverRunRequest/DriverRunResult/DriverSessionState/OutputSchema 枚举/RunFailure/WorkspaceManager/RuntimeServiceManager 接口/StreamPayload/resolvedInvocation | 按读者四分：Driver* + StreamKind/StreamPayload → `driver/`（P0.3）；RunResult/RunFailure 语义 → next Result/RunError（P0.5，D1）；RunEvent/StreamPayload 语义 → P1.1 事件族；Workspace/RuntimeServiceManager 接口 → 根包构造选项对应接口；resolvedInvocation → engine | P0.2（搬+别名）/ P0.3 / P0.5 / P1.1 | 这是「别名回指」范围最大的单文件；11 个 internal 包引用其中类型（见 §4.1） |
| errors.go | 408 | 公共错误目录（30+ 哨兵/typed error + 6 谓词 + HTTP/日志矩阵注释） | next→root 错误目录（RunError + 哨兵，D1）；engine 依赖同一目录 | P0.2（搬）/ P0.5（新面） | errors_test.go 已在 §3 P0 行 |
| decision_types.go | 344 | HITL 词汇：HumanDecisionKind/Mode/QuestionMode/FailureAction/HumanDecisionPolicy/DecisionRequest/DecisionResponse/3 个 typed handler | → ApprovalRequest 三 Kind 合一 + OnApproval + Policy.Approvals（D2） | P1.3 | P1.3 明列；DecisionCapableSink 若在此文件外（runner.go）随 P0.2 |
| run_policy.go | 240 | RunPolicy/Isolation/FeatureLevel/HITL 默认值常量/3 preset/merge+validate/EffectiveHumanDecisionPolicy/RunPolicyCapabilities | **四分**，详见 §3.4 裁决 | P0.2 / P0.3 / P0.5 / P1.3 | P5 删空，无留守物 |
| caller_identity.go | 46 | ctx 注入/读取 AgentIdentity（SkillProvider hook 的 caller identity 合同） | **根包 adaptor**：Identity + WithIdentity + IdentityFromContext，详见 §3.3 裁决 | P0.5（类型）/ P0.2（注入随管线）/ P3.2（provider 读取路径验证） | caller_identity_test.go 按 §3 归 P3 正确 |
| structured_output.go | 611 | OutputSchema 构造/三模式选项/JSONSchemaFor 泛型派生/DecodeStructuredOutput/本地校验 | → RunAs[T] + WithSchema[T] + 模式词汇（D8 定名） | P3.5 | P3.5 明列；invopop/santhosh 两个第三方依赖随迁 |
| mcp_types.go | 372 | MCPTransport/MCPServerSpec/MCPConfig/MCPPayload 验证+排序+fingerprint | 公共词汇 → `mcp/`（HTTP/Stdio/Server 一行式构造器）；MCPPayload 归一化 → engine | P0.2（payload 搬）/ P3.3（词汇） | P4.5 的 `RuntimeServiceRef.MCP *mcp.Server` 依赖 mcp 包先行（P3.3），计划依赖图已体现（P4 依赖 P3 mcp 包） |
| skill_types.go | 497 | Skill/SkillRef/SkillKey/SkillSource（Path/FS/Inline）/SkillProvider/SkillCatalog/SkillSet/SkillMaterializer/ResolvedSkills/SkillSnapshot | 消费者词汇 → `skill/`（Dir/FS/Inline/Key/Provider/Materializer）；ResolvedSkills/SkillSnapshot（SPI 面）→ `driver/` | P0.2（搬）/ P3.2（词汇）/ P0.3（SPI 面） | 引用 CallerIdentityFromContext（文档注释层） |
| skill_helpers.go | 640 | skill clone/合并器（skillMerger/冲突检测）+ 内置物化器 Materialize 实现（FS/Inline/Archive 写盘） | engine（合并器与物化器实现均为管线内部） | P0.2 | **计划把它算进 P3.2 的「skill_*.go 5 个文件」，但它 90% 是管线内部件**，P0.2 就必须随行；P3.2 只搬其中公共可见语义 |
| skill_resolution.go | 297 | `(*sdkImpl).resolveSkills`：合并默认/run/candidate 引用、批量 GetSkills、Required 注入、物化 | engine | P0.2 | 同上：是 *sdkImpl 方法，**P0.2 必须随行**，P3.2 认领的只是其公共语义（追加合并/Required/冲突检测行为不变） |
| skill_dirscan.go | 198 | LocalSkillsFromDir 目录扫描 + DirScanOption（前缀/排除/SKILL.md 校验） | `skill/`（skill.Dir 的实现基础） | P3.2 | 纯公共词汇，无管线耦合，可整体平移 |
| archive_source.go | 219 | **skill** 归档源：SkillFromArchive（zip/tar/tar.gz + Subpath + 幂等 Reader 合同） | `skill/`（需新增 skill.Archive 词汇——见 §5.1 缺口）；类型本体随 P0.2 进 engine 别名回指 | P0.2（搬）/ P3.2（词汇） | **设计 §4 将其映射到 Result.Raw()/Transcript() 是错配**（那是 run 结果分层，与此无关；runner.go 中 grep "archive" 为 0） |
| archive_materializer.go | 384 | 内置归档物化器：magic 嗅探、zip/tar/tgz 解压、安全上限（256MiB/64MiB/10000 entry）+ NewDefaultSkillMaterializer 公共选项 | 解压内核 → engine；NewDefaultSkillMaterializer + WithMaxArchiveSize 等公共选项 → `skill/` | P0.2（内核）/ P3.2（选项） | archive_fuzz_test.go 归 P0（fuzz 内核）合理；archive_source_test.go 见 §5.4 |
| workspace_skill_types.go | 357 | 又一杂烩：ModelInfo/DetectedModel/AgentProfile/EnvironmentReport/QuotaReport（管理面）+ Workspace 族（Spec/Lease/Strategy/三实现）+ RuntimeService 族（Spec/Request/Ref/Payload/Report） | 管理面报告类型 → Inspect 返回值（根包或 driver/）；Workspace 族 → 根包 WithWorkspace/WithWorkspaceSpec 词汇 + driver SPI（Lease/Request）；RuntimeService 族 → 根包 WithServices 词汇，Ref 在 P4.5 加类型化 MCP 字段 | P0.2（搬）/ P0.3 / P3.6 / P4.5 | RuntimeServiceRef 定义在此（:254），**不在** runtime.go——P4.5 触及清单写 runtime.go 时注意实际改的是这里搬进 engine 后的位置 |
| runtime.go | 190 | `(*sdkImpl).prepareRuntime`：runtime service 声明合并、Ensure 调用、SecretEnv 收集、Ref 归一化 | engine | P0.2 | **P0.2 触及清单遗漏此文件**；它是 *sdkImpl 方法，不随行则 runner.go 不编译。P4.5 在 engine 内做 MCP 类型化注入 |
| managers.go | 110 | 默认实现三件套（passthroughWorkspaceManager/emptySkillProvider/noopRuntimeManager）+ skill snapshot 路由 | engine | P0.2 | **实施计划全文未出现此文件**——P0.2 清单遗漏 |
| admin.go | 332 | adminImpl/agentAdminImpl：全部管理探针实现（CheckEnvironment/ListModels/Profile/Quota/Skills/SetSelectedSkills）+ profileResourceDriver 内部接口 | 实现体 → engine；公共面 → Inspect()/ProfileState/SyncProfile/SelectSkills | P0.2（实现随行）/ P3.6（换面） | agentAdminImpl 直接持有 *sdkImpl——engine 抽取时须同步处理，P0.2 清单未列 |
| admin_helpers.go | 34 | summarizeEnvironment 聚合 | engine | P0.2 → P3.6 | 同上 |
| config_types.go | 127 | CommonConfig/EnvBinding/InstructionsBundleRef/Instruction 枚举 + 四驱动 Config（Codex/Claude/Cursor/CodeBuddy）+ 各自枚举 | 四驱动 Config → 各驱动包 `codex.Config` 等（根包留别名至 P5）；CommonConfig 并入驱动包或 driver/ 共享；InstructionsBundleRef → 根包 WithInstructions 词汇 | P3.1 | P3.1 明列；EnvBinding 同时被 RuntimeServiceRef.SecretEnv 引用，归 driver/ 更合适 |
| profile.go | 140 | ProfileMode/CloneProfileOptions/CloneProfileAuthMode/ProfileSelection + 4 个 profile AgentOption + NormalizeProfileDir | → `profile/`：Native/Dedicated/CloneNative/CloneFrom + LinkAuth（CloneProfileAuthLink 的语义源头） | P3.4 | P3.4 明列 |
| profile_resources.go | 878 | ProfileKind/ProfileResourceKind/ProfileResources/ProfilePayload/ProfileSnapshot/AgentSpec/HookSpec/ProfileConfigPatch + fingerprint 计算 | 公共词汇 → `profile/`（profile.Resources）；Payload/fingerprint → engine | P0.2（payload）/ P3.4（词汇） | P3.4 明列；fingerprint 是 session resume guard 的输入，P2 也依赖其稳定性 |

## 2. 逐文件映射表 — 测试文件（22），对照实施计划 §3

§3 映射表**逐一核对：22 个测试文件全部在表，无遗漏、无多余**。下表补充每个文件实测内容与归类核实结论。

| 测试文件 | §3 归类 | 实测内容 | 核实结论 |
|---|---|---|---|
| run_contract_test.go | P0 | Run 合同（Output/RawStreams 分层等） | ✓ |
| run_policy_test.go | P0 | merge/validate/Effective 策略矩阵 | ✓（merge 部分随 engine；preset/HITL 断言 P1.3 还会引用） |
| run_model_override_test.go | P0 | WithModel 覆盖 | ✓ |
| errors_test.go | P0 | 哨兵/谓词 | ✓ |
| archive_fuzz_test.go | P0（随 engine） | 归档解压 fuzz（内核安全性） | ✓ 内核随 engine 合理 |
| archive_source_test.go | P0（随 engine） | SkillFromArchive 公共行为（格式嗅探/Subpath/上限） | **△ 张力**：断言的是 skill 公共 API 行为，建议 P0 先随 engine 保绿，P3.2 收编进 skill 包合同测试（见 §5.4） |
| stream_internal_test.go | P1 | dualSink 背压/Dropped 语义 | ✓（P1.2 行为快照基线） |
| runner_hitl_integration_test.go | P1 | HITL 三 Kind 集成 | ✓ |
| runner_decision_test.go | P1 | DecisionRequest/Resolve 往返 | ✓ |
| sdk_session_test.go | P2 | 四 SessionMode 矩阵 | ✓ |
| sdk_start_session_test.go | P2 | Start + session | ✓ |
| runner_session_internal_test.go | P2 | lease/finalize 内部合同 | ✓ |
| session_codec_internal_test.go | P2 | passthrough codec | ✓ |
| skill_contract_test.go | P3 | skill 合并/Required/冲突/物化合同（含 Archive 源） | ✓ |
| skills_sdk_test.go | P3 | WithSkills/provider 集成 | ✓ |
| skill_dirscan_test.go | P3 | LocalSkillsFromDir | ✓ |
| mcp_sdk_test.go | P3 | WithMCP 注入 | ✓ |
| structured_output_test.go | P3 | 三模式/派生/解码 | ✓ |
| admin_profile_test.go | P3 | ProfileSnapshot/Sync | ✓ |
| profile_resources_test.go | P3 | profile.Resources 语义 | ✓ |
| caller_identity_test.go | P3 | ctx 注入/读取 | ✓（与 §3.3 裁决一致） |
| runtime_admin_test.go | P4 | runtime service 声明→Ensure→Report + MCP capability 门 | ✓（P4.5 的行为基线） |

---

## 3. 四个归属存疑件：裁决

### 3.1 `providers/`（providers.go，151 行）→ **删除（P5.2 不迁移）**

**谁在用**：全仓 grep `agent-adaptor/providers`，唯一引用者是它自己的 `providers_test.go`。examples、bridges、hosttools、docs 使用指南均无引用。

**它是什么**：`MarkRequired(inner SkillProvider, pins ...Pin)` —— 把上游 provider 返回的指定 Key 提升为 Required 的装饰器。包文档自述「intentionally live outside the main SDK surface so they remain opt-in sugar rather than first-class API」，且注明典型场景本应在宿主自己的 provider 里做（返回 Required=true）。

**裁决：删除**。依据：① 零消费者；② 自我定位就是非一等 sugar；③ 能力不丢——Required 语义完整保留在 `skill.Skill`/`skill.Provider` 合同里，宿主 10 行 wrapper 即可等价实现；④ v1 目标是词汇做减法。若产品侧坚持保留 sugar，唯一合理归宿是 P3.2 的 `skill.MarkRequired`（skill 包已经是 Provider 接口的家），但默认裁决是删。**行动项**：P5.4 迁移指南补一行「providers.MarkRequired → 自写 Provider 装饰器（附 10 行示例）」。

### 3.2 `runtimeservice/`（runtimeservice.go，42 行）→ **删除（P5.2）**

**谁在用**：唯一代码引用是自己的测试；另有 docs/v0.5.0-* 两处历史文档提及。

**与根包 runtime.go / RuntimeServiceRef 的关系**：*没有代码关系*。三者职责完全不同——

- `runtimeservice.NoopReleaseByLabels` 是给**宿主**用的接口演进 mixin：v0.5 给 `RuntimeServiceManager`（定义在 run_types.go:462，不在 runtime.go）加了第三个方法 `ReleaseByLabels`，旧宿主 embed 这个空实现即可继续编译；
- 根包 `runtime.go` 是 **SDK 管线侧**：`(*sdkImpl).prepareRuntime` 合并声明、调用 Ensure、归一化 Ref —— P0.2 随 engine；
- `RuntimeServiceRef` 定义在 workspace_skill_types.go:254，P4.5 要给它加 `MCP *mcp.Server` 类型化字段 —— 改的是类型定义与 engine 注入路径 + internal/mcpruntime 的解析，**与 runtimeservice/ 包零交集**。

**裁决：删除**。v1 的 `WithServiceManager` 对应接口是全新契约、没有任何「已实现两个方法的存量宿主」需要垫片；接口演进 mixin 的存在前提在 v1 元年不成立。P4.5 触及清单里列的 `runtimeservice/` 一项，实际动作应为「确认删除 + 迁移指南记一行」，不是改造。若 v1 以后再演进 ServiceManager 接口，届时再发 mixin 不迟。

### 3.3 `caller_identity.go` → **根包 adaptor（不是 skill 包）**

**引用关系实测**：`WithCallerIdentity` 由管线在调用 SkillProvider hook 前注入（skill_resolution.go 文档合同 + options.go/skill_types.go 注释引用）；`CallerIdentityFromContext` 是 provider/manager **实现方**的读取器。关键证据：`AgentIdentity` 的合同（run_types.go:9-14）明说它服务于「SkillProvider、**WorkspaceManager、RuntimeServiceManager**」三类宿主组件的租户/用户 scoping —— 消费方横跨三个词汇域，**不属于 skill 包专有**。

**裁决**：Identity 概念归**新根包 adaptor**：
- `adaptor.Identity` 类型 + `WithIdentity`（双作用域选项，设计 §2.3 已列）——P0.5 立类型（S9 场景用到）；
- ctx 注入机制随管线进 engine，原样保留（§4 承诺「ctx 传播机制不变」）——P0.2；
- 读取器 `adaptor.IdentityFromContext(ctx)` 留根包（provider 作者本来就要 import 根包拿不到？不——skill.Provider 在 skill 包，但其实现方读 identity 时 import 根包是可接受的：Identity 是跨域概念，根包是它唯一不产生循环的家）——P3.2 验证 provider 读取路径；
- `caller_identity_test.go` 按 §3 随 P3 迁移，归类正确。

**风险提示（需 P0.5 定案）**：现状 `AgentIdentity{ID, TenantID, ProfileID, Name}` 四字段，设计稿 `Identity{Tenant, User}` 只有两字段。ProfileID（租户内用户私有技能分区）与 Name（逻辑 agent 名）都有真实 scoping 用途（见 CallerIdentityFromContext godoc 示例），直接缩水与 §4「能力不丢」冲突。建议 v1 Identity 保留四维语义（命名可改：Tenant/User/Profile/Agent），否则在能力映射表上标记为有意裁剪。

### 3.4 `run_policy.go` → 四分，P5 删空

拆给 P1 的 `Policy.Approvals`（HumanDecisionPolicy 语义）之后，剩余部分**全部有主，没有含糊地带**：

| 剩余物 | 去向 | 阶段 | 依据 |
|---|---|---|---|
| `RunPolicy` 结构 + `IsolationLevel`（ReadOnly/WorkspaceWrite/Unrestricted）+ `FeatureLevel`（WebSearch/Browser） | 新根包 `Policy{Sandbox, WebSearch, Browser, Approvals}` + `adaptor.ReadOnly` 等常量 | **P0.5**（S4 场景测试用 `WithPolicy`，P0.6 门禁需要，不能等 P1） | 设计 §2.3 选项表 |
| `mergeRunPolicy` / `mergeHumanDecisionPolicy` / `validateHumanDecisionPolicy` / `cloneRunPolicy`（非导出管线） | internal/engine | P0.2 | *sdkImpl 管线依赖（runner.go 调用） |
| `DefaultHumanDecisionTimeout` / `DefaultHumanDecisionMaxRetries` 常量 + `EffectiveHumanDecisionPolicy` | 随 HITL 语义进 engine + 经 `driver/` 暴露给驱动 | P0.2（搬）/ P1.3（换词） | **不可简单删除**：实测 claude/driver.go、claude/parser.go、codebuddy/control_parser.go、codebuddy/engine.go 四处调用 EffectiveHumanDecisionPolicy 计算 Deadline/失败文案——驱动侧需要物化后的策略值，v1 应作为 driver 包的一部分（如 `driver.EffectiveApprovalPolicy`）保留 |
| 3 个 preset（PolicyHostReview/PolicyReadOnlyReview/PolicyAutonomous） | 由 `Policy` 字面量 + `ApproveAll()`/`DenyAll()` 预设 handler 取代，不保留同名预设 | P1.3 | 设计 §2.6；迁移指南逐一映射（PolicyAutonomous ≈ Policy{Sandbox: Unrestricted, Approvals: 自动放行} + Question 自动拒绝语义并入 Approvals 兜底） |
| `RunPolicyCapabilities`（Isolation/WebSearch/Browser + 三 HITL 支持声明） | `driver/` Descriptor（能力真话声明） | P0.3 | api.go 的 DriverDescriptor.RunPolicyCaps 引用；四驱动 + runner + 两个 HITL 测试在用 |

---

## 4. 计划盲点核查

### 4.1 internal/ 16 包 × P0.2 engine 抽取

**依赖方向实测**（全仓 grep import）：

- 根包 → internal/*：**0 处**。根包管线只依赖 stdlib + 两个 jsonschema 第三方库。engine 抽取不需要动任何 internal 包的代码本体。
- internal/* → 根包：**11/16 个包 import 根包类型**：adapterutil、clihelper、mcpruntime、profileagents、profileconfig、profilehooks、profileinstructions、profilekind、profilesnapshot、skillruntime、testutil（全部是给四个驱动包服务的共享件）。不依赖根包的 5 个：aguiversion、configprobe、processx、profilereconcile、profilestate。

**结论与牵连**：

1. **直接改动：无**。只要 P0.2 严格执行「移动 + 类型别名回指」，11 个包引用的 `agentadaptor.MCPPayload`、`agentadaptor.Skill`、`agentadaptor.ProfileSelection` 等名字仍从根包解析，零修改编译通过——这正是 P0.2 门禁「现有全量测试零修改通过」的成立前提。
2. **真正的坑（P0.2 范围低估）**：根包薄包装要 import engine ⇒ engine **绝不能** import 根包 ⇒ 管线引用的**所有**合同类型（run_types/session_types/decision_types/mcp_types/skill_types/profile.go/profile_resources.go/workspace_skill_types/api.go 的 Binding 与 SPI 类型/errors.go）都必须与 7 文件清单一起进 engine（或独立 contract 子包），根包全部变别名。P0.2 实际是「搬全部类型 + 搬管线 + 根包变别名壳」，触及 ≈ 32 个非测试文件而非 7 个。§7 启动动作「先移 util.go/config.go 验证机械流程」的方向正确，但后续刀数要按此重估。
3. `internal/testutil`（P0.6 fake driver 扩展点）需要同时满足旧 `DriverAdapter` 与新 `driver.Driver` 双形态——计划已预期，确认无额外风险。
4. **P5.2 清单需补一项**：删除根包别名时，这 11 个 internal 包（若彼时仍存活）+ 四驱动包的 import 需批量改指 `driver/` 与词汇包；当前 P5.2 只写了「删除旧 API 文件与旧测试」，未列 internal 包 repoint。

### 4.2 pkg/clients/a2a × P4.1 目录提升

**全部依赖方实测**（grep `agent-adaptor/pkg/clients/a2a`）：

| 依赖方 | 性质 | 计划覆盖情况 |
|---|---|---|
| pkg/hosttools/a2adelegation（delegator.go / mapping.go / types.go + 2 测试） | 生产依赖（RemoteAgentSpec/DTO 复用） | ✓ P4.6 delegation.Service 重写时自然更新 |
| pkg/bridges/a2a/import_boundary_test.go | 守门测试：**硬编码 forbidden 路径字符串** `"github.com/agent-dance/agent-adaptor/pkg/clients/a2a"` | **✗ 未覆盖**：P4.1 提升为 `clients/` 后旧字符串永远匹配不到 ⇒ 守门变假绿。P4.1 验收项需加「同步改写 boundary 测试路径」。顺带发现该 forbidden 列表只有 claude/codex/cursor，**漏了 codebuddy**（bbba7a0 新增驱动时未更新），建议一并修 |
| examples/a2a-local/main.go | 示例 | ✓ P4.9 全量重写覆盖 |

**结论**：无被计划遗漏的依赖方；但有两处文档/守门修订需补——① 设计文档 §2.1 目标布局图漏画 `clients/` 目录（实施计划 P4.1 有 `pkg/clients` → `clients/`，两文不一致，以计划为准补图）；② 上表 boundary 测试问题列入 P4.1。

---

## 5. 与实施计划既有假设不一致的发现（汇总，按严重度）

1. **archive_*.go 的身份错认（影响 P0.2 / P3.2 / §4 映射表）**：`archive_source.go`/`archive_materializer.go` 是 **skill 归档源与物化器**（`SkillFromArchive`，含公共选项 WithMaxArchiveSize 等），不是 run 结果归档——runner.go 中不存在任何 archive 逻辑。设计 §4「归档（archive source / materializer）→ 保留内部管线，Result.Raw()/Transcript() 暴露」一行张冠李戴；且 v1 skill 词汇（Dir/FS/Inline/Key，§2.1 与 P3.2）**缺 Archive 构造器**，按「能力不丢」原则需在 P3.2 补 `skill.Archive`（或显式决定裁剪并写入迁移指南）。
2. **P0.2 触及清单低估（影响 engine 抽取排刀）**：清单遗漏了同为管线组成的 `runtime.go`（prepareRuntime 是 *sdkImpl 方法）、`managers.go`（计划全文未出现）、`skill_resolution.go`（*sdkImpl.resolveSkills，被 P3.2 认领但 P0.2 必须随行）、`skill_helpers.go`（合并器/物化器实现）、`session_codec.go`（管线 helper）、`admin.go`/`admin_helpers.go`（agentAdminImpl 持有 *sdkImpl）；更根本地，因 engine 不能回 import 根包，**全部合同类型文件都要随行 + 别名回指**（详见 §4.1.2）。
3. **`EffectiveHumanDecisionPolicy` 不能作为 run_policy.go 的一部分静默消亡**：claude/codebuddy 两驱动 4 个文件在用，v1 需经 driver 包保留等价物（§3.4）。
4. **archive_source_test.go 的 §3 归类张力**：P0「随 engine」可保绿，但其断言对象是公共 skill API 行为；P3.2 应把这些断言收编进 skill 包合同测试，避免 P5 删旧测试时丢失公共行为覆盖。archive_fuzz_test.go 留 engine 无异议。
5. **Identity 字段缩水风险**：AgentIdentity 四字段 → 设计稿 Identity 两字段，与 §4「ctx 传播机制不变」承诺冲突，P0.5 定案（§3.3）。
6. **import_boundary_test 路径硬编码 + 漏 codebuddy**：P4.1 需同步修订（§4.2）。
7. **设计 §2.1 布局图缺 `clients/`**：与 P4.1 不一致，补图即可（§4.2）。
8. **P4.5 触及清单中的 `runtimeservice/`**：实际动作是删除确认而非改造；`RuntimeServiceRef` 真身在 workspace_skill_types.go:254（P0.2 后在 engine），不在 runtime.go（§3.2）。
