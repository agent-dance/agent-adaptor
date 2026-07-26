# 迁移到 v1：旧 API → 新 API 完整对照

> **本稿状态**：P5.4 提前稿，基于本 worktree commit `771590a` 起草。v1 各阶段仍在并行落地中：
>
> - 标注 **✅** 的新 API 已提交，形状即最终形状（P5 只做 import 路径搬迁，见 §5）；
> - 标注 **🚧 接线中** 的新 API 尚未提交或尚未接线，**形状以 [docs/api-v1-redesign.md](./api-v1-redesign.md) 对应小节为准**，本文引用处均注明小节号；
> - v1.0.0 发布前本文随 P5 收尾更新，移除全部 🚧 标注。待定稿章节清单见 §4。
>
> **import 路径注记**：新 API 目前暂存在 `next/`（包名已经是 `adaptor`），P5 整体搬到仓库根。本文所有新 API 示例按**最终形态**（根包 `adaptor`）书写；暂存期试用方法见 §5。

---

## 1. 五分钟概览：心智模型变了什么

v1 是干净切换（v0.x 打 tag 冻结，无兼容层）。API 围绕六个名词组织：**Agent、Thread、Stream、Event、Result、Driver**。旧世界的概念按下表折叠：

| 旧世界 | 新世界 | 决策 |
|---|---|---|
| 中央 `SDK` 对象 + 命名注册表（`sdk.Agent("review")` 运行时查找） | **agent 就是 Go 变量**：`agent := adaptor.New(driver, opts...)`；多 agent = 多变量 | 设计 §2.2 |
| 四层 ID（AgentName / Namespace+Key / SessionID / RunID） | **两层**：thread key（你起的业务字符串）+ run ID（SDK 给的执行号）；SessionID 降级为 `th.Checkpoint()` 内部细节 | 设计 §2.4 |
| `Run` / `Start` 两个入口 + `WithStreaming()` 开关 + `Events()`/`StreamEvents()` 双通道 | **动词即开关**：`Run` = 批处理，`Stream` = 流式；`Run` 内部就是 `Stream` + drain，单一执行路径 | D4、设计 §2.5 |
| `err` + `result.Failure` 两层判断（容易漏检） | **一个 `err`**：业务失败是类型化 `*RunError`（携带完整 `Result`），`errors.As` 一层判定 | D1、设计 §2.7 |
| 3 种选项类型（`Option`/`AgentOption`/`RunOption`）+ 16 对 `WithDefaultX`/`WithX` | **1 套词汇、2 个作用域**：同名选项用在 `New` 是默认值，用在 `Run/Stream` 是本次覆盖；作用域非法双向都是**编译错误**（三接口 `Option`/`CallOption`/`SharedOption`） | D7、设计 §2.3 |
| `DecisionRequests()` channel + `ResolveDecision(requestID, resp)` + 3×2 typed handler 选项 | **审批请求自己会应答**：`*ApprovalRequest` 自带 `Approve/Deny/Answer`，回调（`OnApproval`）与事件双形态 | D2、设计 §2.6 |
| 事件 = `Kind string` + 松散字段 | **密封接口 + 11 个类型化事件**，type switch 分发，不处理即忽略 | 设计 §2.5 |
| 包名 `agentadaptor` | 包名 **`adaptor`**（import 路径不变） | D3 |

合并语义只有一句话（与现状一致）：**「近处覆盖远处；skills 追加、其余替换」**；metadata 按键合并。

---

## 2. 逐 API 对照表

本节是 §4 能力映射表（api-v1-redesign.md）的逐 API 展开。**全部 66 个旧 `With*` 选项**（options.go 48 + profile.go 4 + structured_output.go 4 + skill_dirscan.go 3 + archive_materializer.go 4 + archive_source.go 2 + engine_wrappers.go 1）在 §2.2–§2.5 中逐一编号列出，一个不漏。

### 2.1 入口与顶层类型

| 旧 API | 新 API | 状态 |
|---|---|---|
| `agentadaptor.Build(opts...) (SDK, error)` | `adaptor.New(driver, opts...) *Agent`——构造不再返回 error（nil driver 直接 panic，属编程错误） | ✅ |
| `agentadaptor.New(opts...) SDK`（出错 panic） | `adaptor.New(driver, opts...) *Agent`。**注意同名不同义**：旧 `New` 收选项造 SDK，新 `New` 第一个参数是 driver | ✅ |
| `SDK` 接口 | 删除。没有中央对象；共享基础设施（store/manager）通过共享实例达成（设计 §6.1） | ✅ |
| `sdk.Run(ctx, prompt, opts...)` | `agent.Run(ctx, prompt, opts...) (*Result, error)` | ✅ |
| `sdk.Start(ctx, prompt, opts...) (RunHandle, error)` | `agent.Stream(ctx, prompt, opts...) Stream`——不返回 error，启动失败走「关闭的事件通道 + `Result()` 报错」 | ✅ |
| `sdk.Default()` / `sdk.Agent(name)` | 删除（命名注册表删除；多 agent = 多变量） | ✅ |
| `sdk.Admin()` / `AdminAPI` / `AgentAdmin` 全部探针 | `agent.Inspect()` 面板（`Environment/Models/DetectModel/Quota/ConfigSchema/Skills`）+ `agent.ProfileState(ctx)` / `agent.SyncProfile(ctx)`；`SetSelectedSkills` → `agent.SelectSkills(ctx, keys)` | 🚧 接线中（P3.6），形状以设计 §2.9 为准 |
| `Runner` 接口（`Run`/`Start`） | `adaptor.Runner` 接口（`Run`/`Stream`）——`Agent` 与 `Thread` 都实现它 | ✅ |
| `RunHandle.Events() <-chan RunEvent` | 删除双通道：操作事件（`RunStarted`/`RunFinished`/`ProcessInfo`/`Notice`）并入同一条 `stream.Events()`，不处理即忽略，**没有「必须 drain 第二条通道」义务** | ✅ |
| `RunHandle.StreamEvents() <-chan StreamPayload` | `stream.Events() <-chan Event`（类型化事件，见 §2.8 事件对照） | ✅ |
| `RunHandle.RunID()` | `stream.RunID()`（启动即可用） | ✅ |
| `RunHandle.Wait(ctx) (RunResult, error)` | `stream.Result() (*Result, error)`——含 D1 错误合同，可多次、跨 goroutine 调用 | ✅ |
| `RunHandle.Cancel()` | `stream.Cancel()`（幂等；或直接 cancel ctx） | ✅ |
| `RunHandle.DecisionRequests() <-chan DecisionRequest` | `*ApprovalRequest` 作为事件出现在 `stream.Events()`（形态 B），或走 `OnApproval` 回调（形态 A） | ✅（D2） |
| `RunHandle.ResolveDecision(requestID, resp)` | `req.Approve(ctx)` / `req.Deny(ctx, reason)` / `req.Answer(ctx, option)`——应答器就在请求上，requestID 簿记消失 | ✅（D2） |
| `Bind(adapter, cfg, opts...)` / `BindTyped[T](...)` / `AgentBinding` / `TypedAgentBinding[T]` | 删除。驱动构造收敛为 `adaptor.New(<驱动包>.Driver(cfg), opts...)`（设计 §2.10） | ✅ |
| `codex.New(cfg, opts...)`（及 claude / cursor / codebuddy 同形） | `codex.Driver(codex.Config{...})` 得到 `driver.Driver`，再 `adaptor.New(...)`。四个内置驱动包均已提交 `Config` + `Driver()` | ✅（P3.1） |
| `codex.NewAdapter()` / `DriverAdapter` SPI | `driver.Driver` SPI（独立 `driver/` 包；能力接口原样保留） | ✅（P0） |
| `RunResult` | `*adaptor.Result`：高频字段平铺（`Text`/`Summary`/`Usage`/`Model`/`Provider`/`Metadata`），审计收拢为 `Raw()`/`Transcript()`/`Services()`/`Decode()` | ✅ |
| `RunResult.Output` | `res.Text` | ✅ |
| `RunResult.Failure` | 删除字段。业务失败 = `*RunError`（`Reason`/`Message`/`Details` + 完整 `Result`），走 err 路径 | ✅（D1） |
| `RunResult.SessionID` | `th.Checkpoint(ctx)`（驱动 resume 句柄降级为审计入口） | ✅ |
| `AgentIdentity{ID, TenantID, ProfileID, Name}` | `adaptor.Identity{ID, Tenant, Profile, Name}`——四字段全保留，字段名去 `ID` 后缀 | ✅（D11） |
| `SessionStore` / `SessionRequest` / 4 种 `SessionMode` | `threadstore.Store`（能力等价：resolve / finalize / lease 防并发）+ Thread 的 4 个**有名字的动作**（见 §2.4 行 33–37） | ✅（P2） |
| `memory.NewSessionStore()` | `memory.NewStore()`（同包并存至 P5） | ✅ |

### 2.2 SDK 级 `Option`（9 个）

| # | 旧选项 | 新归宿 | 状态 |
|---|---|---|---|
| 1 | `WithDefaultAgent(binding)` | 删除。每个 agent 由 `adaptor.New(driver, opts...)` 独立构造，「默认 agent」概念随注册表消失（设计 §2.2、§6.1） | ✅ |
| 2 | `WithAgent(name, binding)` | 删除。多 agent = 多变量；`name` 字符串查找不复存在 | ✅ |
| 3 | `WithSessionStore(store)` | `adaptor.WithThreadStore(store threadstore.Store)`（仅 New；用在 Run/Stream 是编译错误） | ✅ |
| 4 | `WithWorkspaceManager(m)` | `WithWorkspaceManager(m)`（仅 New） | 🚧 接线中，形状以设计 §2.3 为准 |
| 5 | `WithSkillProvider(p)` | `WithSkillProvider(p)`（仅 New；`skill.Provider` 别名 ✅ 已就位） | 🚧 接线中，形状以设计 §2.3 为准 |
| 6 | `WithSkillSet(set)` | 由 `WithSkillProvider` 承接——`SkillSet` 本就是静态 Catalog，`skill.Set`/`skill.Catalog` 别名 ✅ 已就位 | 🚧 接线中，形状以设计 §2.3 为准 |
| 7 | `WithSkillMaterializer(m)` | `WithSkillMaterializer(m)`（仅 New；`skill.Materializer` 别名 ✅ 已就位） | 🚧 接线中，形状以设计 §2.3 为准 |
| 8 | `WithRuntimeServiceManager(m)` | `WithServiceManager(m)`（仅 New）。与已删除的 `runtimeservice/` 包无代码关系（见 §2.9，D10） | 🚧 接线中，形状以设计 §2.3 为准 |
| 9 | `WithEventBuffer(runBuf, streamBuf, policy)` | `WithEventBuffer(n int)` + `WithBlockingEvents()`——事件流合一后只剩一个缓冲；背压两策略从枚举参数变成两个显式选项（默认丢弃 + `Dropped{Count}` 聚合标记） | ✅ |

### 2.3 `AgentOption`（20 个）

新世界没有 `AgentOption` 这一类：下列选项的语义由**同一词汇在 New 作用域**承接。

| # | 旧选项 | 新归宿 | 状态 |
|---|---|---|---|
| 10 | `WithDefaultPermissionHandler(h)` | `OnApproval(h)` 用于 `New(...)`；handler 内按 `req.Kind == adaptor.ApprovalPermission` 分流，`req.Approve/Deny` 应答 | ✅（D2） |
| 11 | `WithDefaultPlanReviewHandler(h)` | 同上（`adaptor.ApprovalPlanReview`） | ✅（D2） |
| 12 | `WithDefaultQuestionHandler(h)` | 同上（`adaptor.ApprovalQuestion`；用 `req.Answer(ctx, option)` 从 `req.Choices` 选择） | ✅（D2） |
| 13 | `WithDefaultIdentity(id)` | `WithIdentity(adaptor.Identity{...})` 用于 `New(...)` | ✅（D11） |
| 14 | `WithDefaultWorkspace(dir)` | `WithWorkspace(dir)` 用于 `New(...)` | ✅ |
| 15 | `WithDefaultSkills(refs...)` | `WithSkills(skill.Dir/FS/Inline/Key/Archive...)` 用于 `New(...)`；skills 是唯一**追加合并**的选项族（skill 构造器 ✅ 已提交） | 🚧 接线中，形状以设计 §2.3 为准 |
| 16 | `WithDefaultMCP(specs...)` | `WithMCP(mcp.HTTP/SSE/Stdio(...))` 用于 `New(...)`（mcp 构造器 ✅ 已提交；替换语义不变） | 🚧 接线中，形状以设计 §2.3 为准 |
| 17 | `WithDefaultProfileResources(res)` | `WithProfileResources(profile.Resources{...})` 用于 `New(...)`（`profile.Resources` ✅ 已提交） | 🚧 接线中，形状以设计 §2.3 为准 |
| 18 | `WithDefaultAgents(specs...)` | 并入 `profile.Resources` 的子代理字段 + `WithProfileResources`。字段暂名 `Agents`（P3.4 提交形态），设计 §3 S8 写作 `SubAgents`，P5 统一定名 | 🚧 接线中，形状以设计 §2.9/§3 S8 为准 |
| 19 | `WithDefaultHooks(specs...)` | `profile.Resources.Hooks` + `WithProfileResources`（`profile.Hook` 别名与 20 个 HookEvent 常量 ✅ 已提交） | 🚧 接线中，形状以设计 §2.9 为准 |
| 20 | `WithDefaultProfileConfig(patches...)` | `profile.Resources` 的 ConfigPatch 族 + `WithProfileResources`（ConfigPatch 家族 ✅ 已提交） | 🚧 接线中，形状以设计 §2.9 为准 |
| 21 | `WithDefaultRuntimeServices(services...)` | `WithServices(specs...)` 用于 `New(...)`；服务规格随 P4.5 类型化（`RuntimeServiceRef.MCP`） | 🚧 接线中，形状以设计 §2.3、§4 表为准 |
| 22 | `WithDefaultRunPolicy(p)` | `WithPolicy(adaptor.Policy{Sandbox, WebSearch, Browser, Approvals})` 用于 `New(...)`；HITL 策略并入 `Policy.Approvals` | ✅ |
| 23 | `WithDefaultInstructions(ref *InstructionsBundleRef)` | `WithInstructions(text string)`——**签名变化**：直接传文本。作为 profile 资源下发的形态改用 `profile.Resources.Instructions` + `profile.Text(...)`（后者 🚧 接线中） | ✅（文本形态） |
| 24 | `WithDefaultMetadata(key, value)` | `WithMetadata(k, v)` 用于 `New(...)`（按键合并语义不变） | ✅ |
| 25 | `WithDefaultStreaming(...)` | 删除。动词即开关：`Run` = 批处理、`Stream` = 流式（设计 §2.5） | ✅ |
| 26 | `WithNativeProfile()` | `WithProfile(profile.Native())`（构造器 ✅ 已提交，选项接线在 P3） | 🚧 接线中，形状以设计 §2.9 为准 |
| 27 | `WithDedicatedProfile(dir)` | `WithProfile(profile.Dedicated(dir))` | 🚧 接线中，形状以设计 §2.9 为准 |
| 28 | `WithCloneProfile(dir, opts)` | `WithProfile(profile.CloneNative(dir, opts...))`；`CloneProfileOptions` 结构体变一行式选项：`IncludeSettings/IncludeMCP/IncludeSkills` → `profile.CopySettings()/CopyMCP()/CopySkills()`，`AuthMode: CloneProfileAuthCopy/Link` → `profile.CopyAuth()/LinkAuth()`（`LinkAuth` 保留 OAuth 登录态共享），另有 `profile.WithOptions(...)` 逃生舱 | 🚧 接线中，形状以设计 §2.9 为准 |
| 29 | `WithCloneProfileFrom(src, dst, opts)` | `WithProfile(profile.CloneFrom(src, dst, opts...))` | 🚧 接线中，形状以设计 §2.9 为准 |

### 2.4 `RunOption`（27 个）

| # | 旧选项 | 新归宿 | 状态 |
|---|---|---|---|
| 30 | `WithPermissionHandler(h)` | `OnApproval(h)` 用于 `Run/Stream(...)`（近处覆盖 New 处默认） | ✅（D2） |
| 31 | `WithPlanReviewHandler(h)` | 同上 | ✅（D2） |
| 32 | `WithQuestionHandler(h)` | 同上 | ✅（D2） |
| 33 | `WithSession(req SessionRequest)` | 删除结构体入口。4 种 SessionMode 变 4 个有名字的动作（行 34–37），会话本身升格为 `Thread` 对象 | ✅（P2） |
| 34 | `WithSessionKey(namespace, key)`（continue_or_start） | `agent.Thread(key)`——有则续、无则建；namespace 并入 key，多租户自拼 `"tenant/key"` | ✅ |
| 35 | `WithContinueSession(id)`（continue_only） | `agent.Thread(key, adaptor.ResumeOnly())`——只续不建，缺档报 `ErrThreadNotFound`。按 SessionID 定位的审计场景改用 `th.Checkpoint(ctx)` | ✅ |
| 36 | `WithNewSession(namespace, key)`（start_new） | `agent.NewThread(key)`——强制新开 | ✅ |
| 37 | `WithForkSession(fromID, namespace, key)`（fork） | `th.Fork(newKey)`——从现有 Thread 分叉，不需要拿着 fromID 对三元组 | ✅ |
| 38 | `WithWorkspace(dir)` | `WithWorkspace(dir)`（同名保留，双作用域） | ✅ |
| 39 | `WithRuntimeServices(services...)` | `WithServices(specs...)`（双作用域） | 🚧 接线中，形状以设计 §2.3 为准 |
| 40 | `WithSkills(refs...)` | `WithSkills(refs ...)`（同名保留；参数改收 `skill` 包构造器产物；追加合并语义不变） | 🚧 接线中，形状以设计 §2.3 为准 |
| 41 | `WithMCP(specs...)` | `WithMCP(mcp.HTTP/SSE/Stdio(...))`（替换语义不变） | 🚧 接线中，形状以设计 §2.3 为准 |
| 42 | `WithProfileResources(res)` | `WithProfileResources(profile.Resources{...})` | 🚧 接线中，形状以设计 §2.3 为准 |
| 43 | `WithAgents(specs...)` | 并入 `profile.Resources` 子代理字段（同行 18） | 🚧 接线中，形状以设计 §2.9/§3 S8 为准 |
| 44 | `WithHooks(specs...)` | `profile.Resources.Hooks`（同行 19） | 🚧 接线中，形状以设计 §2.9 为准 |
| 45 | `WithProfileConfig(patches...)` | `profile.Resources` ConfigPatch 族（同行 20） | 🚧 接线中，形状以设计 §2.9 为准 |
| 46 | `WithModel(m)` | `WithModel(m)`（同名保留，双作用域） | ✅ |
| 47 | `WithRunPolicy(p)` | `WithPolicy(p)`（整体替换，不做字段级合并） | ✅ |
| 48 | `WithInstructions(ref *InstructionsBundleRef)` | `WithInstructions(text string)`——**签名变化**，同行 23 | ✅（文本形态） |
| 49 | `WithMetadata(key, value)` | `WithMetadata(k, v)`（同名保留；按键合并） | ✅ |
| 50 | `WithAgentIdentity(id)` | `WithIdentity(adaptor.Identity{...})` | ✅（D11） |
| 51 | `WithStreaming()` | 删除。要流式就调 `agent.Stream(...)`（设计 §2.5） | ✅ |
| 52 | `WithoutStreaming()` | 删除。要批处理就调 `agent.Run(...)`；「流式跑但不要 token 级增量」→ `WithoutTokenStream()`（仅 Run/Stream） | 🚧 后者接线中，形状以设计 §2.3 为准 |
| 53 | `WithOutputSchema(schema OutputSchema)` | `WithSchema[T](...)` 的模式参数（`schema.Strict/Flexible/PromptOnly`；模式词汇 D8 待定，默认方案为根包常量如 `adaptor.SchemaStrict`） | 🚧 P3.5，形状以设计 §2.8 + D8 为准 |
| 54 | `WithJSONSchemaOutput(schemaJSON, opts...)` | 裸 JSON schema 形态并入 `WithSchema` 定稿（宿主已有 schema 字节串的场景） | 🚧 P3.5，形状以设计 §2.8 为准 |
| 55 | `WithJSONSchemaOutputFile(path, opts...)` | 同上（宿主读文件后传入；SDK 不再代读文件） | 🚧 P3.5，形状以设计 §2.8 为准 |
| 56 | `WithJSONSchemaOutputFor[T](opts...)` | 一步到位用 `adaptor.RunAs[T](ctx, runner, prompt)`；流式/手动用 `WithSchema[T]()` + `res.Decode(&v)` | 🚧 P3.5，形状以设计 §2.8 为准 |

### 2.5 其余选项族（10 个）

| # | 旧选项 | 新归宿 | 状态 |
|---|---|---|---|
| 57 | `WithDirSkillKeyPrefix(prefix)`（DirScanOption） | 随 `LocalSkillsFromDir` 迁入 `skill` 包，语义不变 | 🚧 P5 搬迁，形状以 p0-inventory §3 为准 |
| 58 | `WithDirIgnore(patterns...)`（DirScanOption） | 同上 | 🚧 P5 搬迁 |
| 59 | `WithDirSkillFile(name)`（DirScanOption） | 同上 | 🚧 P5 搬迁 |
| 60 | `WithSkillCacheRoot(dir)`（DefaultMaterializerOption） | 随 `NewDefaultSkillMaterializer` 迁入 `skill` 包物化管线（`skill.Materializer` 别名 ✅ 已就位），语义不变 | 🚧 P5 搬迁 |
| 61 | `WithMaxArchiveSize(n)`（DefaultMaterializerOption） | 同上 | 🚧 P5 搬迁 |
| 62 | `WithMaxFileSize(n)`（DefaultMaterializerOption） | 同上 | 🚧 P5 搬迁 |
| 63 | `WithMaxArchiveEntries(n)`（DefaultMaterializerOption） | 同上 | 🚧 P5 搬迁 |
| 64 | `WithArchiveHeader(key, value)`（ArchiveHTTPOption） | `skill.WithArchiveHeader(key, value)`——挂在 `skill.ArchiveURL(url, opts...)` opener 上 | ✅ |
| 65 | `WithArchiveHTTPClient(c)`（ArchiveHTTPOption） | `skill.WithArchiveHTTPClient(c)` | ✅ |
| 66 | `WithCallerIdentity(ctx, id)`（context 助手） | 传播端：`WithIdentity(...)` 选项，SDK 自动注入运行 ctx；读取端：`CallerIdentityFromContext(ctx)` → `adaptor.IdentityFromContext(ctx) (Identity, bool)` | ✅ |

### 2.6 技能 / 归档 / 结构化输出辅助 API

| 旧 API | 新 API | 状态 |
|---|---|---|
| `LocalSkill(dir)` | `skill.Dir(path)` | ✅ |
| `FSSkill(fsys, root)` | `skill.FS(fsys, root)` | ✅ |
| `InlineSkill(key, skillMD)` | `skill.Inline(key, skillMD)` | ✅ |
| `Key(k)` | `skill.Key(k)` | ✅ |
| `Require(s, reason)` | `skill.Require(s, reason)` | ✅ |
| `ArchiveFromBytes(data)` / `ArchiveFromPath(path)` / `ArchiveFromURL(url, opts...)` | `skill.Archive(key, opener, opts...)` + `skill.ArchiveBytes(data)` / `skill.ArchiveFile(path)` / `skill.ArchiveURL(url, opts...)`；归档细节选项 `skill.WithFormat(FormatAuto/Zip/Tar/TarGz)` / `WithSubpath` / `WithFingerprint` | ✅ |
| `LocalSkillsFromDir(root, opts...)` / `SkillsAsRefs(skills)` | 目录批量扫描随 skill 包保留；`SkillsAsRefs` 转换助手删除（skill 构造器产物可直接交给 `WithSkills`） | 🚧 P5 搬迁，语义不变 |
| `NewDefaultSkillMaterializer(opts...)` | skill 包物化管线（同 §2.5 行 60–63） | 🚧 P5 搬迁 |
| `NativeStrictOutput()` | `schema.Strict()`（默认模式；仅 provider 原生约束） | 🚧 P3.5 + D8，形状以设计 §2.8 为准 |
| `PreferNativeOutput()` | `schema.Flexible()`（原生优先，允许提示词 + 本地校验回退） | 🚧 同上 |
| `PromptValidateOutput()` | `schema.PromptOnly()` | 🚧 同上 |
| `StructuredOutputName(...)` / `StructuredOutputDescription(...)` | `WithSchema` 定稿携带的命名/描述参数 | 🚧 P3.5，形状以设计 §2.8 为准 |
| `ReturnInvalidStructuredOutput()` | 校验失败时原文已随 `*RunError.Result` 保留（`res.Decode` 自行处理）；是否保留独立开关随 P3.5 定稿 | 🚧 P3.5 |
| `SchemaInlineReferences()` / `SchemaAllowAdditionalProperties()` / `SchemaRequireExplicitTags()` / `SchemaUseGoComments()` | schema 派生细节选项随 `WithSchema[T]` 定稿 | 🚧 P3.5 |
| `JSONSchemaFor[T](opts...)` | 内部化：`RunAs[T]` / `WithSchema[T]` 自动派生 schema；需要裸 schema 的宿主场景以 §2.8 定稿为准 | 🚧 P3.5 |
| `DecodeStructuredOutput[T](res)` | `res.Decode(&v)` | ✅ |
| `RunStructured[T](ctx, r, prompt, opts...)` | `adaptor.RunAs[T](ctx, runner, prompt)`——接受任何 `Runner`（Agent 或 Thread） | 🚧 P3.5，形状以设计 §2.8 为准 |

### 2.7 策略预设与 HITL 类型

| 旧 API | 新 API | 状态 |
|---|---|---|
| `RunPolicy` | `adaptor.Policy{Sandbox, WebSearch, Browser, Approvals}` | ✅ |
| `IsolationLevel`（ReadOnly / WorkspaceWrite / Unrestricted / Inherit） | `adaptor.SandboxLevel`（= `driver.IsolationLevel`）：`adaptor.SandboxInherit/ReadOnly/WorkspaceWrite/Unrestricted` | ✅ |
| `PolicyHostReview`（WorkspaceWrite + 全 Ask） | 预设名不保留：`adaptor.PolicyWorkspaceWrite` + `OnApproval(宿主 handler)`（Ask 即审批默认模式） | ✅ |
| `PolicyReadOnlyReview`（ReadOnly + 全 Ask） | `adaptor.PolicyReadOnly` + `OnApproval(...)` | ✅ |
| `PolicyAutonomous`（Unrestricted + 自动批准 + Question 自动拒答） | `adaptor.PolicyUnrestricted` + `OnApproval(adaptor.ApproveAll())`——`ApproveAll` 批准 Permission/PlanReview、拒答 Question，与旧预设语义一致 | ✅ |
| `HumanDecisionPolicy`（超时 / 重试 / 兜底） | `adaptor.ApprovalPolicy`（= `driver.HumanDecisionPolicy` 别名，字段语义不变），并入 `Policy.Approvals`；默认超时 30s / 重试 3 次的缺省值不变 | ✅ |
| `EffectiveHumanDecisionPolicy(p)` | 驱动内部继续使用（claude / codebuddy 调用点），由 `driver` 包承接等价入口 | 🚧 P5 盘点定稿，依据 p0-inventory §3.4 |
| `HumanDecisionKind` | `adaptor.ApprovalKind`：`ApprovalPermission` / `ApprovalPlanReview` / `ApprovalQuestion` | ✅ |
| `DecisionRequest` / `DecisionResponse` | `*adaptor.ApprovalRequest`（`ID/RunID/Kind/Title/Source/ToolCallID/Choices/Details/CreatedAt/Deadline/Attempt`）+ `Approve/Deny/Answer` 方法 | ✅（D2） |
| `DecisionChoice` | `adaptor.Choice`（= `driver.DecisionChoice`） | ✅ |
| HITL 各 mode 枚举 | `ApprovalMode`（Inherit/Ask/AutoApprove/AutoDeny）、`QuestionMode`、`FallbackAction`（Abort/Continue/Retry）+ 预设 `adaptor.ApprovalsAutoDeny` | ✅ |
| 审批预设 handler | `adaptor.ApproveAll()`、`adaptor.DenyAll(reason)` | ✅ |
| `DecisionRequest` 的风险分级 | **v1.0 不提供 `req.Risk()`**：驱动 SPI 没有真实风险信号源，不造假数据；待上游有信号后再加 | 延期（D12） |

### 2.8 事件模型对照

旧世界：`Events()` 通道（`RunEvent`，6 种 `RunEventType`）+ `StreamEvents()` 通道（`StreamPayload`，18 种 `StreamKind` 字符串）。新世界：**一条 `Events()` 流、11 个密封类型**（P1 已提交，18 + 6 种旧 kind 全部有归宿）：

| 旧事件 | 新事件类型 | 状态 |
|---|---|---|
| `text.start` / `text.content` / `text.end`（`Delta` 字段） | `TextDelta{MessageID, Text, Role, Phase}`（`PhaseStart`/`PhaseContent`/`PhaseEnd`） | ✅ |
| `reasoning.start` / `reasoning.content` / `reasoning.end` | `Thinking{...}`（同 Phase 机制） | ✅ |
| `tool_call.start` / `tool_call.args` / `tool_call.end` | `ToolCall{ID, Name, Args, ArgsDelta, Phase}` | ✅ |
| `tool_call.result` | `ToolResult{...}` | ✅ |
| `run.started` | `RunStarted{RunID, ThreadID}` | ✅ |
| `run.finished` / `run.error` | `RunFinished{RunID, ThreadID, Usage, Failed, Reason, Message}` | ✅ |
| RunEvent：进程 spawn / stdout / stderr | `ProcessInfo{Kind, Text, Bytes, ...}` | ✅ |
| RunEvent：invocation / lifecycle / runtime 服务 / `step.started` / `step.finished` / transcript item | `Notice{Kind, Text, Item, ...}`（kind：`invocation`/`lifecycle`/`runtime`/`step`/`transcript.item`） | ✅ |
| `hitl.requested` / `hitl.resolved` | `*ApprovalRequest` 事件本体 + `Notice`（`approval.requested`/`approval.resolved` 公告） | ✅ |
| `stream.dropped` | `Dropped{Count}`（聚合标记） | ✅ |
| subagent 委托事件（现走独立 sidecar 流） | `SubagentUpdate{Agent, Kind, Delta, Data}`（started/delta/finished）——类型 ✅ 已提交，委托服务注入主流在 P4 | 🚧 注入接线中，形状以设计 §2.11（D5）为准 |

### 2.9 错误哨兵对照

| 旧哨兵 | 新归宿 | 状态 |
|---|---|---|
| `ErrAgentBindingRequired` / `ErrAgentNameRequired` / `ErrAgentNotFound` / `ErrDefaultAgentAlreadyConfigured` / `ErrDefaultAgentMissing` / `ErrReservedAgentName` | 删除——注册表消失后无此失败模式；nil driver 为 `New` panic | ✅ |
| `ErrSessionStoreRequired` | `adaptor.ErrThreadStoreRequired` | ✅ |
| `ErrSessionNotFound` | `adaptor.ErrThreadNotFound` | ✅ |
| `ErrSessionBusy` | `adaptor.ErrThreadBusy` | ✅ |
| `ErrSessionIncompatible` | `adaptor.ErrThreadIncompatible` | ✅ |
| `ErrSessionLeaseLost` | `adaptor.ErrThreadLeaseLost` | ✅ |
| `ErrSessionCheckpointMissing` | `adaptor.ErrThreadCheckpointMissing` | ✅ |
| `ErrResumeRejected` | `adaptor.ErrResumeRejected`（同名保留） | ✅ |
| `ErrDecisionRequestExpired` | 超时语义并入 `Policy.Approvals` 兜底：`adaptor.ErrApprovalTimeout` / `RunError.Reason == ReasonApprovalTimeout` | ✅ |
| `ErrDecisionResultKindMismatch` | 结构性消灭（D2：应答器在请求上，kind 错配不可能发生）；方法级误用（如对 Permission 调 `Answer`）返回 `adaptor.ErrApprovalKindMismatch` | ✅ |
| `ErrRunEnded`（运行结束后应答） | `adaptor.ErrApprovalResolved`（请求已被应答或已失效） | ✅ |
| 运行失败分类 | `RunError.Reason` + 哨兵：`ErrApprovalDenied` / `ErrApprovalTimeout` / `ErrAgentFailed` / `ErrRunCancelled` / `ErrPolicyViolation`（`errors.Is` 可匹配） | ✅（D1） |
| `ErrInvalidDriverConfig` | 驱动构造/校验期错误，随 `driver` 包保留 | 🚧 P5 哨兵终表定稿 |
| `ErrInvalidMCPConfig` / `ErrMCPUnsupported` / `ErrMCPTransportUnsupported` | 随 `WithMCP` 接线保留（能力校验语义不变） | 🚧 P3 接线 |
| `ErrHumanDecisionModeUnsupported` | 随审批能力校验保留（`driver.RunPolicyCaps` 真话降级） | 🚧 P5 哨兵终表定稿 |
| `ErrStructuredOutputUnsupported` / `ErrInvalidOutputSchema` | 随 `RunAs`/`WithSchema` 定稿（能力矩阵仍在启动前报错） | 🚧 P3.5 |
| `ErrSkillKeyConflict` / `ErrSkillMaterializationFailed` / `ErrSkillSourceMissing` / `ErrSkillKeyMissing` / `ErrSkillNotFound` | 随 `WithSkills` 接线保留（语义不变） | 🚧 P3 接线 |

### 2.10 删除的包与延期项（必读）

**`providers/` 包：删除，不迁移（D9）。**
全仓唯一消费者是它自己的测试；`Required` 能力本体保留在 skill Provider 合同与 `skill.Require(s, reason)` 中。需要「整个 Provider 打 Required 标记」的宿主，用约 10 行装饰器达到 `providers.MarkRequired` 等价效果：

```go
// 等价于旧 providers.MarkRequired 的宿主侧装饰器。
type requireAll struct {
    skill.Provider
    reason string
}

func (p requireAll) GetSkills(ctx context.Context, keys []string) (map[string]skill.Skill, error) {
    out, err := p.Provider.GetSkills(ctx, keys)
    if err != nil {
        return nil, err
    }
    for k, s := range out {
        out[k] = skill.Require(s, p.reason)
    }
    return out, nil
}
```

若后续社区需求集中，回迁位置定为 `skill.MarkRequired`（p0-inventory §2 裁定）。

**`runtimeservice/` 包：删除，不迁移（D10）。**
它是 v0.5 宿主兼容 mixin，与 `RuntimeServiceRef` 没有任何代码关系；v1 的 `WithServiceManager` 是新合同（🚧 接线中，形状以设计 §2.3 为准），不承诺兼容该包的接口形状。

**`ApprovalRequest.Risk()`：延期出 v1.0（D12）。**
现有驱动 SPI 拿不到真实的风险分级信号，v1.0 不提供会说谎的字段；设计 §2.6 示例中的注释即此裁定。

---

## 3. 典型流程 Before / After

### 3.1 一次性任务（Run）

**旧：**

```go
sdk := agentadaptor.New(
    agentadaptor.WithDefaultAgent(codex.New(agentadaptor.CodexConfig{Model: "gpt-5.4"})),
)
result, err := sdk.Run(ctx, "fix the failing tests")
if err != nil { ... }
if result.Failure != nil { ... }        // 第二层判断，容易漏
fmt.Println(result.Output)
```

**新：**

```go
agent := adaptor.New(codex.Driver(codex.Config{Model: "gpt-5.4"}))

res, err := agent.Run(ctx, "fix the failing tests")
if err != nil { ... }                    // 唯一判断点
fmt.Println(res.Text)
```

错误判定的完整形态（D1）：

```go
res, err := agent.Run(ctx, prompt)
if err != nil {
    var runErr *adaptor.RunError
    if errors.As(err, &runErr) {
        // agent 完整跑完但业务失败：runErr.Reason ∈ {ApprovalDenied, ApprovalTimeout, PolicyViolation, ...}
        // 部分结果仍可访问：runErr.Result
        log.Warn("run failed", "reason", runErr.Reason, "summary", runErr.Result.Summary)
    }
    return err // 基础设施失败（ctx 取消、进程崩溃、协议破裂）同样走这里
}
fmt.Println(res.Text)
```

### 3.2 流式消费

**旧**（双通道 + 手动 drain + Wait 后仍要查 Failure）：

```go
handle, err := sdk.Start(ctx, prompt, agentadaptor.WithStreaming())
if err != nil { return err }

go func() {
    for range handle.Events() { /* 操作事件通道必须 drain，否则可能阻塞 */ }
}()
for p := range handle.StreamEvents() {
    switch p.Kind {
    case agentadaptor.StreamTextContent:
        io.WriteString(w, p.Delta)
    }
}
res, err := handle.Wait(ctx)
if err != nil { return err }
if res.Failure != nil { ... }
```

**新**（一条流、一次 for-range、一个收口）：

```go
stream := agent.Stream(ctx, prompt)

for ev := range stream.Events() {
    switch e := ev.(type) {
    case adaptor.TextDelta:
        io.WriteString(w, e.Text)
    case adaptor.ToolCall:
        renderToolCard(e.Name, e.Args)
    case adaptor.Thinking:
        renderReasoning(e.Text)
    case *adaptor.ApprovalRequest:
        e.Approve(ctx)                        // 审批请求自带应答能力，见 §3.4
    }
}

res, err := stream.Result()                    // 收口：最终结果 + 错误一次拿到
```

提前弃读是安全的：默认背压丢弃多余事件并聚合成 `Dropped{Count}`，运行自行结束；`stream.Cancel()` 提前终止。

### 3.3 会话 → Thread

**旧**（四层 ID + SessionMode 枚举）：

```go
sdk := agentadaptor.New(
    agentadaptor.WithDefaultAgent(claude.New(cfg)),
    agentadaptor.WithSessionStore(memory.NewSessionStore()),
)

res, err := sdk.Run(ctx, prompt, agentadaptor.WithSessionKey("tenant-1", "issue-123")) // continue_or_start
res, err  = sdk.Run(ctx, prompt, agentadaptor.WithContinueSession(sessionID))          // continue_only
res, err  = sdk.Run(ctx, prompt, agentadaptor.WithNewSession("tenant-1", "issue-123")) // start_new
res, err  = sdk.Run(ctx, prompt, agentadaptor.WithForkSession(fromID, "tenant-1", "issue-123-alt")) // fork
```

**新**（4 种模式 = 4 个有名字的动作；P2 已提交）：

```go
agent := adaptor.New(claude.Driver(cfg), adaptor.WithThreadStore(memory.NewStore()))

th := agent.Thread("tenant-1/issue-123")     // 有则续、无则建（continue_or_start）
res, err := th.Run(ctx, "continue the fix")

fresh := agent.NewThread("tenant-1/issue-123")        // 强制新开（start_new）
locked := agent.Thread("k", adaptor.ResumeOnly())     // 只续不建（continue_only）
branch := th.Fork("tenant-1/issue-123-alt")           // 分叉（fork）
```

`Thread` 与 `Agent` 都实现 `Runner`（`Run` + `Stream`），bridges、`RunAs[T]`、宿主工具对两者一视同仁。需要驱动 resume 句柄做审计时：`cp, err := th.Checkpoint(ctx)`。

### 3.4 HITL 审批（两种形态）

**旧**（requestID 簿记 + 跨对象往返 + 3×2 typed handler）：

```go
handle, _ := sdk.Start(ctx, prompt)
go func() {
    for req := range handle.DecisionRequests() {
        // 自己对 req.Kind 选 handler、自己保证 resp 的 kind 匹配
        _ = handle.ResolveDecision(req.ID, agentadaptor.DecisionResponse{ /* ... */ })
    }
}()
```

**新·形态 A —— 回调（程序化策略 / 终端应用）：**

```go
res, err := agent.Run(ctx, "refactor the auth module",
    adaptor.OnApproval(func(ctx context.Context, req *adaptor.ApprovalRequest) error {
        if req.Kind == adaptor.ApprovalPermission {
            // 注：风险分级 req.Risk() 推迟到驱动 SPI 有真实风险信号源后再加（实施计划 D12）
            return req.Approve(ctx)
        }
        fmt.Printf("[%s] %s\n(y/N): ", req.Kind, req.Title)
        if askUser() { return req.Approve(ctx) }
        return req.Deny(ctx, "operator rejected")
    }),
)
```

**新·形态 B —— 事件（Web UI 异步审批）：**

```go
case *adaptor.ApprovalRequest:
    pending.Store(e.ID, e)          // 存下请求本身
    pushCardToBrowser(e)            // 推审批卡片
// 浏览器回包后，在 HTTP handler 里：
req, _ := pending.Load(id)
req.(*adaptor.ApprovalRequest).Answer(ctx, chosenOption)
```

超时 / 重试 / 兜底并入 `Policy.Approvals`（原 `HumanDecisionPolicy` 语义不变）；现成 handler：`adaptor.ApproveAll()`、`adaptor.DenyAll(reason)`。

### 3.5 技能（含归档技能包）

**旧：**

```go
sdk := agentadaptor.New(
    agentadaptor.WithDefaultAgent(claude.New(cfg,
        agentadaptor.WithDefaultSkills(
            agentadaptor.LocalSkill("./skills/write-proof"),
            agentadaptor.Key("code-review"),
        ),
    )),
    agentadaptor.WithSkillMaterializer(agentadaptor.NewDefaultSkillMaterializer(
        agentadaptor.WithSkillCacheRoot(cacheDir),
    )),
)
```

**新**（skill 构造器 ✅ 已提交；`WithSkills` 选项本身 🚧 接线中，形状以设计 §2.3 为准）：

```go
agent := adaptor.New(claude.Driver(cfg),
    adaptor.WithSkills(                       // 🚧 接线中
        skill.Dir("./skills/write-proof"),
        skill.Key("code-review"),
        skill.Archive("deploy-kit",
            skill.ArchiveURL("https://example.com/skills/deploy-kit.tgz",
                skill.WithArchiveHeader("Authorization", "Bearer "+token),
            ),
        ),
    ),
)

// 调用处追加（skills 是唯一追加合并的选项族）：
res, err := agent.Run(ctx, prompt, adaptor.WithSkills(skill.Key("deploy-checklist")))
```

### 3.6 MCP

**旧**（嵌套结构体 `MCPServerSpec{...}` 交给 `WithDefaultMCP`/`WithMCP`）。**新**（一行式构造器 ✅ 已提交；`WithMCP` 选项 🚧 接线中）：

```go
adaptor.WithMCP(
    mcp.HTTP("docs", "https://example.com/mcp"),
    mcp.Stdio("repo-tools", "npx", "repo-mcp"),
)
```

需要鉴权头 / 必需标记时：`mcp.HTTP(name, url, mcp.WithBearerTokenEnv("DOCS_TOKEN"), mcp.Required("docs are mandatory"))`。

### 3.7 Profile（租户隔离的专用 profile）

**旧：**

```go
binding := claude.New(cfg,
    agentadaptor.WithCloneProfile(
        filepath.Join(appData, "profiles", tenantID),
        agentadaptor.CloneProfileOptions{
            IncludeSettings: true,
            IncludeMCP:      true,
            AuthMode:        agentadaptor.CloneProfileAuthLink,
        },
    ),
)
```

**新**（profile 构造器 ✅ 已提交；`WithProfile`/`WithProfileResources` 选项 🚧 接线中，形状以设计 §2.9、§3 S8 为准）：

```go
agent := adaptor.New(claude.Driver(cfg),
    adaptor.WithProfile(profile.CloneNative(
        filepath.Join(appData, "profiles", tenantID),
        profile.LinkAuth(),                        // 共享本机 OAuth 登录态，不复制 token 文件
    )),
    adaptor.WithProfileResources(profile.Resources{
        Instructions: profile.Text("Follow ACME coding standards."),
        SubAgents:    []profile.SubAgent{{Key: "tester", Instructions: "..."}},
    }),
)
```

注：`profile.Resources` 的子代理字段在 P3.4 提交形态中暂名 `Agents`，设计稿写作 `SubAgents`，P5 统一定名（见 §4）。

### 3.8 结构化输出

**旧**（两段式）：

```go
res, err := sdk.Run(ctx, "triage this issue:\n"+issueBody,
    agentadaptor.WithJSONSchemaOutputFor[Triage](
        agentadaptor.NativeStrictOutput(),
        agentadaptor.StructuredOutputName("triage"),
    ),
)
if err != nil { ... }
triage, err := agentadaptor.DecodeStructuredOutput[Triage](res)
```

**新**（🚧 P3.5，形状以设计 §2.8 为准）：

```go
type Triage struct {
    Severity  string   `json:"severity"`
    Component string   `json:"component"`
    Duplicate *string  `json:"duplicate_of"`
}

triage, _, err := adaptor.RunAs[Triage](ctx, agent, "triage this issue:\n"+issueBody)
```

流式 / 手动场景：`agent.Stream(ctx, p, adaptor.WithSchema[Review]())` + `res.Decode(&review)`。三种执行模式收敛为 `WithSchema` 的模式参数：`schema.Strict()`（默认）/ `schema.Flexible()` / `schema.PromptOnly()`——模式词汇归属（独立 schema 子包 vs 根包常量 `adaptor.SchemaStrict` 等）由 D8 定稿，当前默认方案是根包常量。

### 3.9 驱动构造（内置与第三方）

**旧：**

```go
binding := codex.New(agentadaptor.CodexConfig{Model: "gpt-5.4"}, /* AgentOption... */)
sdk := agentadaptor.New(agentadaptor.WithDefaultAgent(binding))
// 或底层形态：
binding = agentadaptor.Bind(codex.NewAdapter(), agentadaptor.CodexConfig{...})
```

**新**（四个内置驱动包 ✅ 已提交 `Config` + `Driver()`）：

```go
agent := adaptor.New(codex.Driver(codex.Config{Model: "gpt-5.4"}))
```

第三方驱动实现 `driver.Driver`（独立 `driver/` 包，能力接口原样保留），使用方同样 `adaptor.New(myDriver, opts...)`；`Bind`/`BindTyped` 删除。一致性测试套件 `adaptertest` 升级为 `driver.Driver` 版本（🚧 P5.3 进行中）。

---

## 4. 未定稿清单（状态标注纪律）

本文基于 commit `771590a`。下列表面**尚未落地或尚未接线**，正文相应行已标 🚧；它们的最终形状以 `docs/api-v1-redesign.md` 对应小节为准，落地后本文随即更新：

| 主题 | 等待阶段 | 定稿依据 |
|---|---|---|
| `WithSkills` / `WithMCP` / `WithProfile` / `WithProfileResources` / `WithServices` / `WithWorkspaceSpec` / `WithWorkspaceManager` / `WithSkillProvider` / `WithSkillMaterializer` / `WithServiceManager` 选项接线 | P3 接线波 | 设计 §2.3 |
| `RunAs[T]` / `WithSchema[T]` / `WithoutTokenStream` / schema 模式词汇（D8 待定） | P3.5 | 设计 §2.8 + 实施计划 D8 |
| `Inspect()` 面板 / `ProfileState` / `SyncProfile` / `SelectSkills` | P3.6 | 设计 §2.9 |
| `profile.Resources` 子代理字段定名（`Agents` vs `SubAgents`） | P5 | 设计 §3 S8 + 实施计划 P3.4 备注 |
| bridges 新签名：`sse.Handler(agent)` / agui / `a2a.NewServer(agent, ...)`；`delegation.Service`（D5）+ `SubagentUpdate` 注入主流；`RuntimeServiceRef.MCP` 类型化 | P4 | 设计 §2.11、§3 S3/S6、§4 表 |
| `LocalSkillsFromDir` / 目录扫描选项 / 物化器及其 4 个选项迁入 `skill` 包 | P5 | p0-inventory §3 |
| 错误哨兵终表（`ErrInvalidDriverConfig`、MCP/技能/结构化输出族） | P5 | — |
| 根包搬迁（`next/` → 仓库根）、旧 API 删除、`adaptertest` v1、v1.0.0 tag | P5 | 设计 §5 |

另注：`PersistentProcess` 常驻进程开关**不进 v1.0.0 范围**（实施计划 R9 裁定），examples 中以注释形式保留旋钮位置。

---

## 5. import 路径迁移注记

**最终形态（v1.0.0，P5 之后）**——本文全部示例按此书写：

```go
import (
    adaptor "github.com/agent-dance/agent-adaptor"          // 根包，包名 adaptor（D3）
    "github.com/agent-dance/agent-adaptor/codex"            // 驱动包：codex / claude / cursor / codebuddy
    "github.com/agent-dance/agent-adaptor/driver"            // 扩展作者 SPI
    "github.com/agent-dance/agent-adaptor/skill"             // 词汇包
    "github.com/agent-dance/agent-adaptor/mcp"
    "github.com/agent-dance/agent-adaptor/profile"
    "github.com/agent-dance/agent-adaptor/threadstore"
    "github.com/agent-dance/agent-adaptor/memory"
)
```

**暂存期（现在，P5 之前）**：新消费者 API 位于 `next/`，包名已经是 `adaptor`，因此只需把根包一行换成：

```go
import adaptor "github.com/agent-dance/agent-adaptor/next"   // 暂存路径；P5 整体搬到根
```

其余包（`driver/`、`skill/`、`mcp/`、`profile/`、`threadstore/`、`memory/` 与四个驱动包）**已经在最终路径上**（P4.1 顶层化完成），暂存期与最终形态的 import 完全一致。P5 搬迁时只有 `next` → 根包一处路径变化，示例代码本体不动。

旧 API（根包 `agentadaptor`）在 P5 删除前保持原样可用；v0.x 以 tag 冻结。旧→新多数是机械映射（见 §2 表），若后续需要可提供一次性 `go fix` 风格迁移工具（设计 §5 兼容策略）。
