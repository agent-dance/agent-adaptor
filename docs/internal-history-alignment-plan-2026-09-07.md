# agent-adaptor 与 internal 历史改动对齐方案

日期：2026-09-07。状态：完成历史核对与方案编制，尚未实施下述代码改动。

本文以当前仓库 `AGENTS.md` 为架构裁决依据。internal 的提交用于提供问题证据和修复参考，不能覆盖当前 v1 合同。结论是：保留已完成的 API 重构与可靠性改进，分 13 个工作项吸收 internal 的应用反馈；不整体 merge，不机械 cherry-pick。

## 1. 拉取结果、范围与可复现基线

### 1.1 本次更新

| 仓库 | 分支 | 更新前 | 更新后 | 结果 |
|---|---|---|---|---|
| `agent-adaptor` | `main` / `origin/main` | `6b186bab1972d4ad41b5cdd7bf9ec22ff305751c` | `919f140f64f89c80933802840c8878681a85a4d9`，v1.1.2 | 快进成功；GitHub SSH 连接关闭后，用命令级 HTTPS URL 重写重试，未修改持久 remote 配置 |
| `agent-adaptor-internal` | `master` / `origin/master` | `8ffe22a2a29c0b72e6c993d7774ffb54ac15c010` | `e2f0620bdd6477e6fe16f6db5648093589342ca2`，v0.14.15 | `fetch --prune origin` 后快进成功 |

internal 原有工作区内容完整保留：`examples/profile-resources/main.go` 的未提交修改，以及 `docs/vision-augmentation.html`、`docs/vision.md`、`docs/workstream-vision-augmentation.md` 三个未跟踪文件。它们不属于已提交历史，不作为本次对齐依据。

“最新”指上述 fetch 时点远端主分支；本报告不声称覆盖随后新增提交、远端已经删除且本地不可达的历史或尚未提交的工作。

### 1.2 两边的内容基线

两个完整、非 shallow 仓库之间，`git merge-base` 找不到共同提交。这不否定共同来源：internal 初始提交和当前仓库保留的 `squash_master` 快照 tree 完全相同：

| 位置 | 提交 | Tree |
|---|---|---|
| internal 根提交 | `15b620a9a531d72f3791042aa240a990defcd82b` | `295cb2779779ea0a5f65bae7eeee0b5ec06bb06c` |
| 当前仓库 `squash_master` | `c9a0bb82cb03a7573da16b4d7782302c1e6c3dde` | `295cb2779779ea0a5f65bae7eeee0b5ec06bb06c` |

`squash_master` 是保留的独立快照，并非本次宣称的 `main` 祖先。对照方法是：以相同 tree 作为内容基线，逐提交阅读 internal 增量，再在当前 `main` 上查找对应实现、测试和合同，而不是用提交日期或 hash 不同来判断“缺失”。

核对范围：internal `origin/master` 可达 **30 个提交，含 1 个初始快照、29 个后续提交**；整个本地 `--all` 可达 **45 个提交**，另有 **15 个不在 master 祖先链上的分支提交**。初始快照到最新 master 的净变化涉及 **173 个文件**。附录 A、B 逐条列出全部 45 个提交。

分支提交不是额外的 15 份功能：按每个分支相对分叉点的变更文件，逐文件比较分支 tip 与对应主线集成提交，8 组变更文件内容全部一致。真正 merge 的父链另行核对，避免把 merge 父分支、squash 原提交重复计入工作量。

复现命令（从当前仓库执行）：

```sh
git rev-parse HEAD origin/main
git -C ../agent-adaptor-internal rev-parse HEAD origin/master
git show -s --format='%H %T' c9a0bb8
git -C ../agent-adaptor-internal show -s --format='%H %T' 15b620a
git -C ../agent-adaptor-internal log --reverse --topo-order --format='%H %P %ad %s' --date=iso-strict origin/master
git -C ../agent-adaptor-internal log --reverse --topo-order --format='%H %P %ad %s' --date=iso-strict --all --not origin/master
git -C ../agent-adaptor-internal diff --name-status 15b620a e2f0620
```

阅读单个变更使用 `git -C ../agent-adaptor-internal show <sha> -- <path>`；merge 使用 `git diff <sha>^1 <sha>` 并检查第二父链。本文的源码定位均指上表固定 HEAD；后续实施时先检查基线是否变化。

## 2. 对齐原则与总清单

### 2.1 必须保留的 v1 设计

1. `adaptor.New(driver, opts...)`、Agent/Thread 的 `Run` 和 `Stream`、一个 resolved invocation、一个 typed Event sink、一个 error 判定面。
2. Thread 默认采用已声明的常驻能力，`WithSpawn()` 显式关闭本轮复用；不带回 internal 的 `PersistentProcess` opt-in 配置。
3. Thread fingerprint 覆盖已解析配置、workspace、profile、资源、模型、identity 和服务环境；不采用 internal “资源改变只重启进程、仍无条件复用同一会话”的宽松边界。
4. structured output 由 core 自动协商；不引入 `OutputMode`、`WithoutStreaming` 等消费者或 SPI 模式开关。
5. Driver 自己解析协议；共享 helper 不识别 provider 工具名，不从任意 JSON 猜 checkpoint、todo 或 capability。
6. runtime/observer/store 的宿主组件留在公开扩展点及 hosttools；不恢复 `SDK.Admin()`、命名 Agent registry 或根包查询控制面。
7. 当前 v1.1.1 的 Tool 隔离、随机凭据载体、精确/兼容 fingerprint 分离，以及 v1.1.2 的 Windows 常驻进程支持和前端依赖修复，均作为回归基线保留。

### 2.2 排期总表

P0：已有流程可能挂起、真实会话丢失或续答提前终止，先修。P1：输出、事件或交互保真缺口。P2：需要新增公共语义的能力扩展，完成设计冻结后实施。这是建议的实施优先级，不表示本次已经执行 live 故障复现。

| 工作项 | 优先级 | 当前判定 | internal 来源 | 主要落点 | 前置依赖 |
|---|---|---|---|---|---|
| W01 Claude 终局结果关闭一次性 stdin | P0 | 缺失 | `eefcaef`、`a88eacd`、`d01a086` | `claude/parser.go` | 无 |
| W02 Dedicated hosted-tool profile 跨 Agent 重建保留 | P0 | 缺失；internal 方案需强化所有权 | `ca571fb` | `tools.go`、profile 物化、Close | 无；先定目录协调合同 |
| W03 A2A 续答区分历史 Task 与 live 状态 | P0 | 缺失 | `8e35b12` | `clients/a2a`、`hosttools/a2adelegation` | 无 |
| W04 中断错误保留部分结果，守住 checkpoint | P1 | 结果缺口；拒绝直接放宽 checkpoint | `126d610` | 三个常驻 Driver、`invocation.go` | 先定错误携带结果合同 |
| W05 Claude 嵌套工具事件与父关联 | P1 | 部分 Transcript 已有；typed 工具事件缺失 | `3ea225a` | Claude parser、Event、bridges | 无 |
| W06 Claude 原生 schema 与 Question/PlanReview 共存 | P1 | 基线 schema+Ask 预检拒绝；普通 Permission Ask 已支持 | `4261914` | Descriptor、参数构建、schema 协商 | W01 |
| W07 工具输入错误给模型可安全修正的提示 | P1 | 缺失 | `64163ab` | `tool/tool.go`、tool runtime | 无 |
| W08 A2A 实时制品保留 Parts | P1 | 完整最终制品已有 opt-in；实时精简投影缺失 | `3253009` | delegation DTO、映射、clone | 与 W03 协调 |
| W09 Capability typed 观测及可选记录 | P2 | 缺失 | `1921636`、`9b1ce27`、`dab6933` | Driver、Event、可选 hosttool | 公共事件/扩展点设计冻结 |
| W10 嵌套 A2A capability relay 与开始顺序 | P2 | 缺失 | `51d12bd`、`7438d26` | A2A wire、delegation Service | W09、W03 |
| W11 Provider-native append system prompt | P2 | 缺失 | `8ffe22a` | SharedOption、Request、各 Driver | 配置/fingerprint 合同冻结 |
| W12 统一 todo/plan 快照事件 | P2 | 缺失；internal 有需要修正的推断 | `eb82ed3` | 各 Driver、Event、bridges | W05 的嵌套域规则 |
| W13 可暂停主动执行预算 | P2 | 当前仅墙钟 timeout | `e2f0620` | Policy、唯一 sink、A2A | W03、W04 |

已覆盖而不重复实施的基线：Claude/CodeBuddy/Codex 常驻、Agent.Close、Codex 每轮原生 schema、CodeBuddy partial-message 重建、真实 CLI BDD 基础、WithTools 主能力。见附录 A 的逐提交判定及第 4 节。

## 3. 各工作项的具体方案

### W01：Claude 收到 type:result 后结束一次性 stdin

**证据与差距。** internal `eefcaef` 为 `handleResult` 增加关闭 stdin，`a88eacd` 去掉提取后重复的 guard，`d01a086` 合入。当前 `claude/parser.go` 的 `handleResult` 没有该动作；`onAssistantMessageStop` 仅在非空且非 `tool_use` 的 stop reason 后关闭。只收到终局 `result`、没有此前 `message_stop(end_turn)` 时，双向 print 进程可能继续等待 stdin。

**实施。** 提取 parser 私有的终局 stdin 关闭函数，让 `message_stop` 与正式 `type:result` 共用；关闭 exactly-once。一次性 `WithSpawn` 和无状态 interactive 路径真正关闭输入；常驻 turn 的 stdin 适配器仍保持“不关闭底层进程输入”的合同。不可把 `tool_use`、中间消息或嵌套工具终局当作整个 run 终局。

**验收。** 只发 result 的成功/失败/native schema fixture 都有界退出；`message_stop + result` 只关闭一次；`tool_use` 后仍能接收应答；常驻第二轮可执行；末尾 stdout、Terminal、Transcript、RunFinished 均保留。取消与终局并发不阻塞。更新 `claude/README-streaming.md`、`docs/streaming.md`、CHANGELOG。

### W02：保留真实 hosted-tool provider 会话文件

**证据与差距。** 当前 `tools.go:prepareHostedToolProfile` 对所有选择都 `MkdirTemp`；`releaseHostedToolProfiles` 删除全部 owned clone。`e2e/tools_test.go` 验证了 fake provider 的重建和 endpoint 变化，但 fake checkpoint 可续不证明真实 CLI 的会话文件存在。internal `ca571fb` 指出 Dedicated + Tools 下 Claude 的 transcript 随临时 profile 删除，冷重建 resume 失败。

**不能照搬的部分。** internal 直接使用 `<profile>.hosted-tools` sibling；它不能充分证明当前 v1 要求的 Agent/identity 隔离、跨进程单 writer 和目录所有权。当前删除函数只允许临时目录直接子目录，也不能简单扩宽为“任何路径都可删”。

**实施方案。**

1. 仅对显式 Dedicated 选择启用持久 hosted-tool clone，Native/临时选择保持现有生命周期并明确文档化。稳定位置采用 Dedicated sibling 下按 Driver、canonical source、完整 identity 无碰撞编码后求 hash 的命名空间；不把 host Thread key 拼成目录名。
2. clone 带 SDK 所有权标记、版本、源目录身份。已有目录要验证标记、权限和非符号链接边界；不能仅因目录存在就采用，也不能重新复制源 profile 覆盖已有 transcript。
3. 对共享执行 profile 引入有界、跨进程可验证的独占所有权。Agent 持有期间另一 Agent 使用同一执行目录必须明确冲突；不能只靠进程内 mutex 或不同 Thread 的 store lease。原 Agent 进程退出并释放所有权后才能接力。锁只服务于可选 profile 功能，不新增宿主数据库或通用锁服务。
4. 重建时更新本 Agent 的 MCP endpoint/env carrier；先结束旧 writer，再写新投影、启动进程。保留现有 source 不写入、保留 MCP key/凭据冲突 fail-closed、随机 token 与私有权限。
5. Close 顺序仍为准入关闭、取消/drain、Driver 回收、移除 owned MCP 投影、关闭 gateway、释放目录所有权。持久 clone 的 provider 会话文件保留；临时 clone 删除。不得留下可用的旧 gateway/token 投影。
6. Thread compatibility 使用稳定的语义身份；process signature 使用实际 endpoint 和物化内容。持久目录不等于忽略 Tools Revision、模型、skills/MCP/instructions 或配置漂移。

**迁移。** 已被旧版本删除的 transcript 无法凭 store 中的 resume ID 恢复。原 Thread 遇到 provider resume reject 时继续使用现有一次性安全 fallback；不能宣布“自动修复历史会话”，不能修改宿主 key。持久 profile 的清理归属要写入 `docs/tools.md`，不能让 `Agent.Close` 同时承诺“保留”和“删除”。

**验收。** Agent A 真实写入 session 文件 → Close → Agent B 同配置/identity/Thread key 续接；endpoint/token 轮换而会话保留。覆盖两进程抢同目录、不同 identity、篡改标记、符号链接、旧 MCP 载体、Close 超时重试和 native 临时清理。先用严格依赖 session 文件的 provider fixture，再做显式授权 live 验证。重新打开 AGENTS §14 的 Thread/profile 兼容审计子项。

### W03：A2A 历史 Task 快照不结束续答

**证据与差距。** 当前 `clients/a2a/execution_state.go:executionFinalEvent` 会把 `EventTask` 的 `input-required` 视为执行结束；`hosttools/a2adelegation/delegator.go:delegateStreaming` 同样可由 `event.Task` 或 EOF 后的 `lastTask` 结束，mapper 会重放其旧 status/question。internal `8e35b12` 修改了这三个位置。

**实施。** 流式接收中明确区分完整 Task 快照与 live Status/Message。快照只恢复 TaskID、ContextID 与已存在 artifacts，不生成本轮新问卷或终态；本轮终态以 live Status/Message 为准。同步 Send、GetTask、polling 的完整 Task 仍可作为权威结果，不能把流语义套到所有查询。

同时修正 client 停流条件、delegator 收尾条件、mapper 状态转换，避免只修其中一层。EOF 的历史 `input-required` 不能冒充新问卷；无新 live 终态应显式 `stream_interrupted`。真正断流后的 GetTask 恢复要区别“主动补查结果”与“原流回放”，并避免重新采用已回答的同一问题。

**需超越 internal 的边界。** internal `canFinishFromLastTask` 仍允许某些已结束 Task 在 EOF 收尾，其提交说明“只认 live 终态”比实现更严格。实施时把首次请求、continuation、显式 recovery 分开写合同：continuation 不凭旧 completed/failed 快照误结束；兼容仅返回完整 Task 的服务端时，采用有明确模式的同步/轮询路径，不静默猜测。

**验收。** 旧 question → working → completed；旧 question → 新 question；旧 question → EOF；快照后错误并恢复；首次同步 Task；polling；同一 TaskID 的多轮 answer。旧问卷不再发给 ApprovalRequest，不误取消远端，不丢后续 artifact。重复运行 client/bridge/delegation 用例并进行 race 检查。更新 `docs/a2a.md`。

### W04：保留中断后的输出，但不降低 checkpoint 健康要求

**证据与差距。** internal `126d610` 同时做了“保留部分结果”和“错误后落 checkpoint”。当前 Claude、CodeBuddy 常驻分支遇到非 fallback 错误返回空 `driver.Response{}`；Codex 的 `persistentWriter.run` 和 Driver 上层已经保留 `result, persistentErr`，无需重复修复这两层。剩余公共缺口在 `invocation.go:finalizeRun`：它在无已分类 failure 的 context/基础设施 error 上返回普通包装，丢弃刚映射的 Result。

**本期采用。** Driver 已观察到的 stdout/stderr、Transcript、Terminal、Usage 和文本不得因取消而丢弃：先构建部分 Response，再返回原始 error；在唯一 `finalizeRun` 中建立可通过 Go error 获取部分 Result 的合同。建议复用 `RunError.Result` 的携带能力并保留原始 cause 的 `errors.Is/As` 链；实施前同步明确“执行后基础设施包装错误携带部分 Result”的 godoc，避免把启动前配置错误包装成业务失败。仍返回 `nil, error`，不新增 Result.Failure 或第二个结果入口。具体公共错误声明随合同测试一起冻结。

**本期不采用。** internal Codex 以 `sent && ctx.Err()!=nil && ThreadID!=""` 直接设置 `Checkpoint.Valid=true`，Claude/CodeBuddy 的 session ID 保留也不能替代健康终局证明。当前 `invocationCanPersist` 和 parser 的成功终局检查继续保留：取消、非零退出、畸形协议、缺失终局都不持久化。首次中断没有 checkpoint 时，下轮新建是本期明确边界；已有健康 Thread record 保持原样。

未来若 provider 官方协议能证明中断后的健康可续状态，单独提出 Driver/SessionCodec 能力和证据合同，补齐 lease/token/atomic finalize 与有界脱离取消的持久化测试，再申请改变当前合同；不能以“internal 已用过”代替证明。

**验收。** 三个常驻 Driver 的交付前失败仅允许一次安全 fallback，交付后不重放；中断前输出逐字段可取；`errors.Is` 保留 context cancelled/deadline 与原 cause；Run 与 Stream.Result 等价；审批错误不被通用取消覆盖；无效 checkpoint 不修改 store，原健康记录保持一致。W13 必须复用此结果保留路径。重新打开 AGENTS §14 的错误路径 Result 映射审计子项。

### W05：Claude 嵌套工具的 typed 生命周期与父关联

**证据与差距。** internal `3ea225a` 从 assistant wrapper 取 `parent_tool_use_id`，为没有普通 `stream_event` 的子代理 tool_use 补 start/args/end。当前 `claude/parser.go` 只把 `message` 交给 `handleAssistantMessage`；有 TranscriptToolCall，但不保留 wrapper 父关系，也不为这一路径补 typed ToolCall。

**实施。** parser 只认正式 wrapper 字段；以本轮、父调用、tool-use ID 形成内部关联和去重状态。为嵌套完整 tool_use 补一组生命周期，普通父工具仍走既有增量；结果按真实 tool-use ID 配对。typed `ToolCall`、必要时 `ToolResult`/Transcript 与 SPI 增加可选父关联字段，并在 A2A/SSE/AG-UI/subagentstream 与 recorder 逐层保留或声明降级。

internal 代码只用 parent ID 判定“是否嵌套”，所示新增 emitter 并未把 parent ID 放进输出字段，因此直接复制仍不能让宿主可靠构造树；本项要补齐。完整 Args 与 ArgsDelta 同时出现时按当前合同保证消费者不会重复拼接。

**验收。** 主工具与嵌套工具混合、嵌套兄弟、相同 ID 的快照重放、缺 ID、并发、截断与异常收尾；每个工具 start/end exactly-once，parent 关系保留，终局之前全部关闭。不从名称“Explore”推断层级。更新 `driver/` godoc、adaptertest、root golden 和 streaming 文档。

### W06：Claude 原生结构化输出与交互审批共存

**证据与差距。** B00 执行期复核确认：Claude 的 WorksWithHITL=false 在 core 同时否决 native 与 prompt 两种机制，schema+Ask 实际预启动拒绝；无 schema 的 Permission Ask 已有正式支持。原记录声称当前自动 fallback 是错误的。internal `4261914` 开放 Question/PlanReview 与 `stream-json + --json-schema + --permission-prompt-tool stdio` 的同次执行组合。

**实施。** 在 W01 后按当前 Request 的 resolved `StructuredOutputSource` 选择 transport：interactive native 必须保持双向 stream-json；更新 Descriptor 和冲突参数校验。保留无 schema 的 Permission Ask。按审批 Kind 分别声明 native/prompt 能力，非 nil 矩阵替代本机制的 WorksWithHITL，nil保留旧语义；native schema+Permission Ask 不宣告支持时自动选择已支持的 Prompt+本地校验。先修复 SPI/core 协商，再接 Claude transport。无 native 能力的组合仍按 core 固定 fallback 规则处理，不引入模式选择器。

**验收。** Question Ask、PlanReview Ask 与 schema 成功；回答/拒绝/超时；只有 result 的结束；非法结构化结果；调用 Run/Stream 与 Thread/WithSpawn；stdout、terminal payload、结构化数据和 checkpoint 同次解析一致。验证当前支持的 CLI 版本后再宣告能力；internal 的测试结果不视为本仓库 live 证据。更新 structured-output、run-policy 和 Claude 文档。

### W07：模型可修正的 Tool 输入错误

**证据与差距。** 当前 `tool/tool.go` 的输入 schema/Go 解码失败只包装 `ErrInvalidInput`；toolruntime 仅把 SDK 私有 `tool.Reject` 作为模型可见错误，输入错误缺少可修正原因。internal `64163ab` 增加 `invalidInputRejection`，以 `errors.Join(ErrInvalidInput, Reject("invalid_input", safeMessage))` 同时保留 Go sentinel 和安全反馈。

**实施。** 采用这个错误分层。无效 JSON 只给语法提示；schema 失败给必填/类型/值域提示；通过既有 jsonschema 库的结构化 `AdditionalProperties` 类型识别额外字段。不得返回底层 error、完整 schema、字段值或 handler 错误。字段名也是不可信输入，增加长度、控制字符、引号/换行处理及确定性顺序，不能照搬无界回显第一个 key。Go 解码失败不应一律谎报“JSON 语法错误”，需单独给安全类型提示。

**验收。** extra key、required、类型、enum、畸形 JSON、嵌套对象、超长/恶意 key；handler 在失败时未执行；`errors.Is(ErrInvalidInput)` 和 `tool.AsRejection` 同时成立；任意实现 `As` 的外部 error 不能伪造 rejection；输出校验/业务 panic 的细节仍不泄露。更新 `docs/tools.md` 与 tool godoc。无需新增依赖。

### W08：A2A 每次 artifact 更新保留完整 Parts

**证据与差距。** 当前 `RemoteArtifact.Parts` 和 `IncludeRemoteArtifacts` 已支持最终完整投影；`DelegationArtifact` 只有 ID/URI/MediaType 等精简字段，`artifactCreatedEvent` 丢掉 Text/Data/多 Part。internal `3253009` 为实时 DelegationArtifact 增加 `Parts` 并调用 `cloneRemoteParts`。

**实施。** additive 增加可选 `Parts []RemotePart`，映射每次实际更新的 parts，沿既有 Append/LastChunk 语义透传。不是另建 artifact 流，也不是把每轮更新误当完整最终累计值。同步检查 resultFromTask、cloneDelegationResult、event clone/EventBus/Service recorder 的深复制；internal 的小 patch 不能代替这些链路检查。

保持既有 `IncludeRemoteArtifacts` 与 ExposurePolicy 最小暴露：明确区分可展示的 parts 和原始 Raw/metadata，不能因新字段让默认精简结果突然暴露鉴权或隐藏正文。更新来源和最终结果要有一致的过滤决定；被过滤/超限要有可见降级语义。

**验收。** 同一 ArtifactID 多次更新、Append、LastChunk、多 Text/Data/File Parts、URI 与 inline bytes、上限、源对象后续修改不影响已发事件；最终汇总无重复。W03 回放只恢复 artifacts，不重放问卷。更新 `docs/a2a.md`、hosttool DTO godoc。

### W09：Capability 调用观测，放在正确公共层

**来源。** `1921636` 提供 Skill/MCP/Subagent 调用观测；`9b1ce27` 修复标准化 MCP server name；`dab6933` 处理 Unicode 和下划线分隔歧义。当前没有对应 typed 事件、Driver 字段或 recorder 查询合同。

**实施方案。**

1. 采用封闭 typed 事件：kind、canonical resource key、operation、invocation ID、phase、evidence、source、发生时间/耗时、安全 error code。typed Event 在根层由 Event 家族表达；Driver 事实合同留在 `driver/`，可共用的值词汇放独立公开叶包，任何公共字段不得指向 internal。
2. Driver 从正式协议识别，基于本次 resolved Skill/MCP/profile Agent catalog 做精确映射。共用 tracker 只管理 catalog 和生命周期，provider-specific MCP name 规则移入 Claude 边界，不把 internal tracker 里的 Claude 特例复制成通用 helper 职责。
3. Claude MCP server aliases 以已知 catalog 驱动，处理空格、标点、Unicode、开头/末尾下划线；冲突时拒绝归属。不能盲目按 `__` 拆分，更不能根据 tool name 的自然语言含义猜 Skill/Subagent。
4. 提供可选 `hosttools/capabilityrecorder`，Store/Query 由该包和宿主拥有，不增加 Agent.Admin 或 Inspect 历史查询。通过既有 RunService 扩展机制发行组件 `Option()`；为实现接收事件，设计最小可选 attachment observer 字段或可选观察接口，在唯一 sink 接收后、用户背压丢弃之前投影。现有 RunAttachment 仅能提供输入 Events，**不能假装它已经支持观察输出**。
5. observer 调用必须有明确并发/顺序、超时、panic、关闭和迟到事件合同；首次存储失败只关闭当前 run 的写入并产生一次安全 notice，不改变 Result/checkpoint/审批。Store 由宿主关闭，不让 Agent.Close 关闭共享 Store。
6. 流选择与调用 Run/Stream 解耦；按 resolved observation 需求和 Driver 能力选择协议。无正式证据表示未观察到，不能宣告“从未调用”，也不宣告审计/计费完整性。

**验收。** 四 Driver 正式 fixture、未知/歧义 catalog 不入库、Unicode MCP、重复 started/terminal、取消、观察中途实时 Query、store 超时/panic/失败、无 store 运行、并发 runs/identity 隔离、无 args/result/Raw/URL/header/env 泄露。Run 与 Stream 所观察事实一致，原 Result 不受观察失败影响。注意 internal workstream 曾有“立即 started”的旧文字，与后续 `7438d26` 冲突；以 W10 的最终顺序为准。

### W10：远端嵌套 Capability 沿现有 A2A 事件流透传

**来源与差距。** `51d12bd` 增加安全 capability relay，`7438d26` 把 started 移到 BeforeDelegate 成功之后。当前 A2A `adapter.stream.v1` 不接受 capability kind，delegation 也没有对应安全观察路径。

**实施。** 扩充当前版本化 DTO 的可选 capability 字段，编解码都验证封闭字段、长度、UTF-8、时间和 64 KiB 上限；不在 bridge 重新识别工具。保留当前 `EventMeta` 权威序号，remote source 坐标只作来源。无远端 Store 也可 relay；用既有执行/扩展点传递需求，不恢复 `WithCapabilityInvocationRelay + WithoutStreaming + StreamEvents` 三套旧入口。

顺序固定为 registry 解析 → 有界预算/BeforeDelegate 成功 → 观察外层 started 并安装唯一 terminal defer → 远端 I/O。before hook 拒绝不伪造调用事实。远端事实在本地可丢弃 EventBus 之前安全投影；不同 DelegationID 对相同远端 invocation ID 做无碰撞域隔离，started/terminal ID 保持一致。Local Runner delegation 也走同一公开 Event 合同。

**暴露裁决。** internal 默认不受 IncludeToolCalls 约束，不可直接作为 v1 默认。建议 capability 采用专用显式 exposure 控制，默认关闭，因为资源 key/source 也可能暴露内部能力目录；开启后只传安全闭集字段。旧 client 遇到新 kind 有明确 unsupported/dropped 事件，新 client 可读旧 wire。

**验收。** 无远端 store、有/无本地 recorder、EventBus 满、observer 失败、before hook 拒绝、双层远端相同 ID、超限/畸形 payload、默认 exposure 无外泄、Local/Remote 一致。更新 A2A 文档和 Driver/hosttool 观测合同。

### W11：追加 provider-native system prompt

**来源与差距。** internal `8ffe22a` 最终使用带 Append 语义的名字；当前只有 `WithInstructions`，二者不是同一通道。其实现采用 Claude `--append-system-prompt-file`、CodeBuddy `--append-system-prompt`、Codex exec `developer_instructions` 与 app-server `developerInstructions`，Cursor 非空请求 fail-closed。这些是 internal 固定提交的实现选择，实施时仍需核验目标 CLI 版本。

**公共方案。** 仅增加一个 `WithAppendSystemPrompt(string) SharedOption`，构造与调用共用；近处替换、空串清除。增加 Driver Request 字段和 append 能力声明、结构化 unsupported error。仅 append，保留 provider 默认提示词；不再加 `WithDefaultAppendSystemPrompt`，不增加 Config 镜像字段，不拼入用户 Prompt，也不降级为 Instructions。

**工程细节。** 内容进入 Thread compatibility 和 persistent startup signature，两者都要覆盖；Codex start/resume/fork 对应正式字段都核对，不能恢复时漏传。它不是 profile 资源，不写入 profile manifest。Claude 文件采用 owned、0600、内容校验和安全原子物化；internal 缓存仅比较同名文件 size 的判断需强化，避免同长度篡改和 symlink。CodeBuddy inline 长度、Windows argv 限制、Codex TOML 编码与 ExtraArgs 冲突启动前校验，缓存/文件清理有明确归属。

**验收。** 默认、call override、空串清除、中文/换行/引号/TOML 字符、超长文本、Cursor unsupported、构造 Inspect 一致、Run/Stream 一致、Thread 配置变化拒绝/安全重建、常驻签名变化先停后起、恢复无漏传。更新 root/SPI golden、API reference、各 Driver godoc、examples、README 翻译与 CHANGELOG；With* 数量变更显式记录，不能绕过冻结守卫。

### W12：Todo/Plan 作为同一 Event 流中的快照

**来源与差距。** internal `eb82ed3` 增加 `todo.updated`；当前没有此 typed Event。internal 自述仅实现 Claude/CodeBuddy streaming 和 Codex app-server，未实现 batch Transcript todo 或 Cursor print todo；不能把它描述成四 Driver 全覆盖。

**公共方案。** 增加 `TodoUpdated` typed Event，携带全量有序快照、状态枚举、source/scope 和 `SyntheticID`；它属于 Event 家族，不是新执行概念，也不是 Approval PlanReview。原 ToolCall 和 Transcript 保留。对 Run 与 Stream 采用同一 resolved transport 协商，Driver 能力按实际 transport 如实声明。

**必须修改 internal 的三个假设。**

1. provider 识别留在各 parser。共享 table 只接收规范化的 create/update/replace 操作；不复制 `internal/todoobs.ApplyClaudeTaskTool` 对 provider JSON 和工具名的解释，也不反向 import 根包。
2. 不能把每 run 的 `TaskCreate` 递增序号假装成 provider `TaskUpdate.taskId`：resume、嵌套、重放或已有任务都可错配。优先从正式结果取得真实 ID，合成 ID 明确标记，未知 ID 不猜；父工具/子代理拥有独立 scope。工具参数完成仅代表请求，不能在工具实际失败时报告成功状态。
3. 合法空快照代表清空，不能静默丢弃导致 UI 永久保留旧计划；畸形/未知格式以明确可观察降级处理，保留 Raw。最终快照应为关键状态或通过等价的合并/补发保证终态可恢复，并完整报告 drop。

Codex 结构化源限正式 `turn/plan/updated`；实验性文本 plan delta 不猜成 todo。Cursor 当前 print transport 保持 unsupported，不借本项切换整个 Driver 到 ACP。

**验收。** create/update/replace/clear、错误工具调用、真实 ID、synthetic ID、同 Thread 多轮、嵌套 scope、重复快照、UTF-8 长度、取消/drop、A2A round-trip、AG-UI/SSE 映射或明确降级、session recorder 编解码、batch/stream 输出合同不变。A2A 文本内容继续受 v1 ExposurePolicy 过滤，不直接采用 internal“只截断，redact 全交宿主”的默认。更新 API/godoc/golden/streaming 文档。

### W13：暂停人工等待期间的主动执行预算

**来源与差距。** internal `e2f0620` 增加 PausableContext、RunPolicy.ActiveExecutionTimeout、Ask 暂停与 A2A 错误映射。当前 `stream.go:openStream` 的 WithTimeout 是整体墙钟；`Policy` 是整值替换；delegator 的 Timeout 也是墙钟。

**公共方案。** 在现有 `Policy` 增加 `ActiveExecutionTimeout time.Duration`，不新增平行执行动词。最终 Policy 的零值表示无主动预算、正值表示限额、负值在启动前拒绝；call `WithPolicy` 仍整值替换构造 Policy。不能带回 internal 的“0 继承字段、负数关闭”的逐字段合并。`WithTimeout` 和 parent deadline 继续是绝对墙钟上限。

计时器实现放私有包，默认不把通用 `PausableContext/WithPausableTimeout` 铺到根包。预算从准备资源前开始；只有唯一 approval sink 进入 Ask 等待时暂停自己的 run，自动批准/拒绝不暂停，审批自身 Timeout 仍按墙钟。等待开始前的背压边界要明确，不能让已经发出的审批卡仍消耗整个主动预算。多个并发 Ask 使用 token/refcount 配对，最后一个等待结束才恢复，避免 internal 单一 paused bool 过早恢复。

R012 明确计时结束：包含 Driver 返回后的 schema 和健康/lease 复核，在唯一原子持久化前结算并封账；无状态成功在同等健康判定点封账。Finalize 与返回延迟不扣主动预算，但仍决定最终成功且使用原可取消 context；封账前已耗尽即拒绝持久化，已封账的迟到 timer 不得重写 outcome。Store 已提交而延迟返回没有通用回滚或提交确认，因此不将返回时间冒充原子提交点。

用单调时间和 timer generation 处理迟到回调；所有成功/拒绝/超时/panic/取消出口恢复或终止计时。pause 不阻断 parent Cancel、Agent.Close、lease 续期和清理。嵌套 Member 不自动暂停 Leader，也不能自行改变 parent context。

**错误与 A2A。** 增加稳定 active-execution-timeout sentinel、failure reason 和安全 Limit 详情，保留原错误可匹配性，复用 W04 部分结果。审批拒绝/超时、用户取消、外部 deadline、预算耗尽明确区分；并发终止以已确定的原因优先，不能由 cleanup 覆盖。预算耗尽不让 checkpoint 有效。

delegation 保留已发布 `Timeout` 的墙钟含义；另在该可选 hosttool 的请求/策略中增加主动预算字段，不能把同一个字段悄悄改义。每次 Delegate/continuation 分配新预算，传输重试和 recovery 不重置本次预算。已知 TaskID（包括入参携带）时用有界 detached context 尽力 CancelTask；取消失败为诊断，不覆盖主因。bridge 仅翻译安全 code/limit，不自行决定重试，也不把其他 metadata 绕过 ExposurePolicy。

**验收。** fake-clock 40ms 执行 + 300ms Ask + 50ms 执行仍未耗尽 100ms 预算；再执行 10ms 才超时。覆盖重叠 Ask、retry、父 deadline、外层取消、初始化耗时、timer callback race、Close、lease renewal、旧 Task 回放、远端错误 code 往返和 partial Result。更新 Policy、Approval、errors godoc、root golden、run-policy、A2A 文档。

## 4. 已覆盖的历史改动及保留要求

| internal 改动组 | 当前证据 | 处理 |
|---|---|---|
| `e36c59b`、`456e03e`、`0985324` 常驻进程 | 各 Driver `persistent.go`；Claude/CodeBuddy `TestPersistent*IsDefaultAndWithSpawnOptsOut`，Codex `TestPersistentCodexReusesAppServerAcrossTurns` | 不重写；保留默认 Thread 常驻与单 writer，资源 fingerprint 的语义差异按 §2.1 处理 |
| `bcaeec1` Close | `agent.go`、`agent_close_test.go`、各 Driver Close/idle/cancel 测试、v1.1.1 cleanup 修复 | 已覆盖且当前准入/清理更完整，不恢复 SDK 对象 |
| `1b2de5b` Codex 每轮 schema | `codex/persistent_test.go:TestPersistentCodexNativeSchemaUsesPerTurnFieldWithoutHandoff`、app-server turn options | 已覆盖，不加入 OutputMode |
| `75ce05d` CodeBuddy partial-message 输出 | `codebuddy/parser.go` 的 `enableOutputReconstruction`、`deltaOrder`、`emitReconstructedAssistant`；`TestNonStreamingControlParserReconstructsTranscriptWithoutStreamEvents` | 已覆盖；当前 terminal Result 权威、空终局不覆盖、Summary 不回显长文本的合同更严格，不能倒退到拼接全部 assistant 文本 |
| `6a1e515`、`6bfa5ed` BDD | `e2e/bdd_test.go`、15 个 feature 与 steps/world，Godog 依赖 | 复用基础设施，只补新场景；旧五种 Session 模式/显式 PersistentProcess 场景必须译成最终 Thread/WithSpawn 语义 |
| `803abc1` WithTools | `tools.go`、`tool/`、`internal/toolruntime`、`internal/mcpruntime`、`tools_contract_test.go`、`e2e/tools_test.go` | 主能力已覆盖；同一提交的 Go 1.26.5、MCP SDK、profile selection 优先于 env 也已有。只提取 W02/W07 等后续缺口 |

这些判断来自源码、测试名称与本次基线测试，**不是**依赖 CHANGELOG 单方面声称完成。无法由 fixture 证明的真实持久会话恢复已在 W02 单独打开。

## 5. 交付顺序、API 冻结与依赖选型

### 5.1 建议按独立 PR 推进

| 批次 | 内容 | 合并前要求 |
|---|---|---|
| A：既有流程修复 | W01、W03、W07 各一 PR；W02 在目录所有权方案冻结后单独 PR | 故障 fixture 能区分修复前后；不改变执行入口；相应文档/CHANGELOG 同步 |
| B：输出与协议保真 | W04、W05、W06、W08 分别提交；W06 后于 W01 | error cause/部分结果合同、父关联 wire、schema/HITL 组合、artifact 深复制冻结 |
| C：可选观测能力 | W09，再 W10 | 叶包/Driver/root/hosttool 依赖检查，单 sink，observer-before-drop，默认 exposure 明确 |
| D：新提示与进度能力 | W11、W12 | 新公共声明、With* 数量、fingerprint、实际 provider 能力矩阵和 UI 协议降级一起冻结 |
| E：主动预算 | W13，依赖 W03/W04 | Policy 整值替换不变；并发 Ask 与 A2A timeout 分类验证 |

每个 PR 描述写明 `Source-Internal-Commits`、本方案 W 编号、触发条件、当前 v1 改法、保留/拒绝的 internal 行为和实际执行的验证命令。不要用“迁移内部实现”代替用户可见行为说明。修复与新增公共语义分开版本评估；带公共字段/选项的批次按 minor 变更审查，不能把所有功能塞入无说明的 patch。

工作在新的 `codex/` 前缀分支上实施；本次仅更新主分支并写方案，没有创建发布 tag 或推送代码。

### 5.2 公共面变更预算

| 工作项 | 预期公共变化 | 冻结检查 |
|---|---|---|
| W01/W03/W07 | 原则上无新增 root 名字，修正既有行为/错误反馈 | 合同测试、文档、CHANGELOG |
| W02 | Dedicated + Tools 的文件生命周期与所有权合同 | profile/tools/Close 文档；新增错误若需要必须明确归属 |
| W04 | 错误携带部分 Result 及 cause 的合同 | `errors.go`、root golden、错误矩阵 |
| W05 | Tool/Transcript 可选父关联 | root 与 Driver golden，所有 bridges/recorder |
| W06 | Driver 能力矩阵变化 | adaptertest、structured output 文档 |
| W08 | hosttool artifact DTO Parts | clone/wire/暴露策略，不增加 core 执行概念 |
| W09/W10 | Event 变体、叶包值、可选 observer attachment、hosttool 查询、A2A 可选字段 | root/SPI golden、import 边界、无 Admin facade |
| W11 | 1 个 SharedOption、Driver Request/Descriptor 字段与错误 | `With*` 数量明确变更、option 作用域编译检查 |
| W12 | Todo Event/值、能力声明、wire 字段 | root/SPI golden、全部 serializer |
| W13 | Policy 字段及错误，hosttool 主动预算字段 | 整值替换、根包不增加通用 context 框架 |

更新 golden 之前先审阅 AST 差异；不能用重新生成 golden 把架构错误变成“测试通过”。源库的 `pkg/`、`SDK`、`Start`、`RunHandle`、双/三 channel、公开 structured mode、profile identity 宽松 resume 不得出现于最终实现。

### 5.3 依赖选型

当前与 internal 最新 Go 依赖集合的主要新增项已对齐：Godog v0.15.1、MCP Go SDK v1.7.0、jsonschema/v6 v6.0.2 等无需为移植再次新增。W07 可直接使用现有 jsonschema `kind`；W09/W12/W13 的有界状态机可用标准库；A2A 修改局限于既有 bridge/client/hosttool。

W02 若需跨平台文件锁，应在该 workstream 明确比较：是否显著提高跨进程所有权可靠性，是否有持续维护/版本/问题与 CVE 响应，是否能局部化在私有 profile 边界。证据成立时使用成熟实现；不能为了“零依赖”手写未经验证的锁，也不能在本规划阶段凭空增加顶层 require。

Codex 只有确需新 schema 时才执行官方 `codex app-server generate-json-schema` 与仓库 `go generate` 流程，记录 CLI/schema 版本；不得手工编辑 generated Go/schema JSON。追加文本的编码优先使用已有 TOML 能力并做 round-trip，避免不必要的重复手写解析。

## 6. 验证与发布门禁

### 6.1 本次已经执行

固定目标 HEAD `919f140`，本地 macOS 环境：

| 检查 | 结果 |
|---|---|
| `AGENT_ADAPTOR_LIVE_CONFORMANCE=0 AGENT_ADAPTOR_E2E=0 AGENT_ADAPTOR_UPDATE_API_GOLDEN=0 go test -count=1 ./...` | 通过；examples 随包树编译；不触发 live CLI |
| `go vet ./...` | 通过 |
| `AGENT_ADAPTOR_E2E=0 AGENT_ADAPTOR_LIVE_CONFORMANCE=0 go test -count=1 -tags=e2e ./e2e -run '^TestPersistentProcessBDDFeaturesParse$'` | 通过，15 个 feature 可解析；未执行真实 CLI 场景 |

这些是**修改前基线**，不证明 W01–W13 已修复。本次未执行四 provider 的付费 live 调用，未把 internal 提交说明中的测试结果冒充本项目验证，也未执行 Linux/Windows CI、完整 race 或 fuzz 发布门禁。

### 6.2 实施中的最小验证矩阵

| 维度 | 必须覆盖 |
|---|---|
| 执行入口 | Agent.Run、Agent.Stream、Thread.Run、Thread.Stream；Run = drain + Result |
| 进程 | 默认 Thread 常驻、WithSpawn、无状态、单 writer 接力、Close/取消/启动前 fallback |
| 输出 | Text、Summary、Raw stdout/stderr/terminal、Transcript、Usage、Services、Decode 和错误携带部分 Result 逐字段等价 |
| Thread | 健康/缺失/错误 checkpoint、resume reject 仅一次、Fork 冲突、opaque key、fingerprint 漂移、lease token、旧状态不污染 |
| 审批 | Permission/PlanReview/Question，OnApproval 与 Event 应答，重复/过期/Kind 错误，cancel/block/drop |
| A2A | 初始请求、continuation、旧 Task snapshot、live status、polling/recovery、artifact 多次更新、嵌套 ID 与 exposure |
| 平台 | Linux race/进程组；Windows 常驻/进程树/文件权限与 argv；macOS 本地开发 |

按修改涉及的包先跑 targeted tests，再跑 `go test -count=1 ./...`、`go vet ./...`；反复执行 Thread/事件/A2A 时带 `-count=20`。发布前执行 AGENTS §15 的全部门禁，包括 S1–S9、四 Driver adaptertest、Linux `go test -race ./...`、examples/fake driver、文档/API golden。

当前 `.github/workflows/go.yml` 的 fuzz 基线时长是每 target 30s：archive 的 Zip/Tar/Sniff、四个 batch parser、Codex notification 和 ThreadItem decoder。新增 parser 状态转换补 seed 后沿用此时长，若调整另行记录，不写“已 fuzz”而没有 target/时长/平台证据。

live conformance 继续沿用专用 build tag + `AGENT_ADAPTOR_LIVE_CONFORMANCE=1` 双门；真实 BDD 使用 `-tags=e2e` + `AGENT_ADAPTOR_E2E=1`。普通 CI 保持关闭。W02/W06/W11/W12 的 provider 版本验证进入专门 live 记录，不能用文档中的历史模型/CLI 版本推断现在支持。

### 6.3 完成与回退标准

每个 W 项在来源提交、实施 commit、合同测试、文档、CHANGELOG、实际验证记录均可追溯后关闭。新能力若需临时停用，由其 option/hosttool 配置关闭，已发布公共 API 不反复删改；P0 修复不能回退到已知会话丢失或挂起路径而不显式说明。

W02 回滚不得删除已保留的真实 transcript；W04 回滚不能把中断记录变为健康 checkpoint；W10/W12 旧端读新字段必须有明确兼容行为；W13 禁用主动预算后墙钟 timeout 仍生效。最终发布 tag 是独立动作，仅在门禁满足并获得明确发布授权后创建。

## 7. 当前仓库自己的未合并分支协调

本次 fetch 还取得当前仓库的两个相关远端分支，它们**不属于已拉取的 main 代码，也不计入 internal 的 45 个提交**：

| 分支与提交 | 实际变更 | 对本方案的影响 |
|---|---|---|
| `origin/codex/fix-resume-stale-token-usage`：`6432e5e` | Codex resume 时忽略同 thread、旧 turn 的合法 token usage 回放，其他跨 turn 通知仍严格拒绝 | 与 W04/W11 及“历史回放不污染本轮”相邻；实施前确认该修复是否已合入，避免重复修改 app-server scope fence |
| `origin/codex/run-scoped-mcp-tool-approval`：`12a1ad1`，包含 `6432e5e` | 精确 server/tool 的 MCP approval policy、materialization 与 SPI golden | 与 W09 MCP catalog、W11 configuration fingerprint 相邻；不能因 internal 没有而移除这些新字段，若先合入应纳入指纹和观测目录验证 |

这两条只做变更协调，不自动合并，也不扩张为本次对所有当前远端分支的重新审计。

## 附录 A：internal master 全部提交逐项核对

下表按 `git log --reverse origin/master` 列出每条提交，包含 merge 的父分支提交；merge 的第一父增量可能重新包含已列出的修复，因此不能重复移植。编号是清单顺序，不代表严格日期先后。

| # | Commit | 日期 | 原始标题 | 当前结论与处理 |
|---|---|---|---|---|
| A01 | `15b620a9a531` | 2026-07-23 | feat: agent-adaptor Go SDK | 共同内容快照；与 c9a0bb8 tree 相等，不作为增量移植。 |
| A02 | `e36c59b2b980` | 2026-07-28 | feat: add opt-in Claude persistent processes | 常驻主能力已覆盖；保留当前默认 Thread 常驻和完整兼容 fingerprint，不搬入 opt-in/宽松资源兼容。 |
| A03 | `bcaeec18f3e6` | 2026-07-28 | feat: close SDK persistent process pools | 已覆盖于 Agent.Close 及准入、取消/drain、重试清理；不恢复 SDK.Close。 |
| A04 | `456e03eb23cc` | 2026-07-28 | feat: add opt-in CodeBuddy persistent processes | CodeBuddy 常驻/control 已覆盖；当前 persistent/control 测试验证默认复用和 WithSpawn。 |
| A05 | `0985324984c1` | 2026-07-28 | feat: reuse Codex app-server across streaming turns | Codex app-server 常驻、接力与 idle/Close 已覆盖；保持当前 Raw/terminal 协议合同。 |
| A06 | `1b2de5bb015c` | 2026-07-28 | feat: pass Codex output schemas through app-server turns | 已覆盖：每轮 OutputSchema 与 TestPersistentCodexNativeSchemaUsesPerTurnFieldWithoutHandoff。 |
| A07 | `6a1e51577a86` | 2026-07-28 | test: define real CLI persistent-process BDD features | 15 个真实 CLI feature 基础已在当前 e2e；保留最终 Thread 语义，新增用例按各 W 项补齐。 |
| A08 | `75ce05da0bbb` | 2026-07-28 | fix: reconstruct CodeBuddy control output from partial events | 已覆盖：enableOutputReconstruction 与对应测试；当前终局 Text/Summary 规则优先。 |
| A09 | `6bfa5ed8e603` | 2026-07-28 | test: execute persistent-process BDD against real CLIs | BDD runner/steps/world/Godog 已覆盖，双门保留；不是仅有 feature 文本。 |
| A10 | `803abc127ffe` | 2026-08-04 | feat: support host-defined tools | WithTools、schema、profile 隔离与依赖已覆盖；后续实际缺口分别 W02/W07，不整包替换。 |
| A11 | `1921636510ce` | 2026-08-04 | feat: add capability invocation observability | 新增 capability 观测，当前缺失 → W09；其 Admin/store 根 API 不移植。 |
| A12 | `51d12bd143ae` | 2026-08-05 | feat(a2a): relay nested capability invocations | 新增嵌套 A2A capability relay，当前缺失 → W10，依赖 W09。 |
| A13 | `7438d2633ef7` | 2026-08-14 | fix(a2a): observe delegation after lifecycle begin | before lifecycle hook 成功后再观察 started → W10 的必需顺序测试。 |
| A14 | `9b1ce27faa4e` | 2026-08-17 | fix(claude): resolve normalized MCP server names | Claude 标准化 MCP server 映射修复 → W09；不是通用 MCP transport 连接修复。 |
| A15 | `dab6933b9159` | 2026-08-17 | fix(claude): normalize Unicode MCP server names | Unicode/下划线 MCP alias 与分隔歧义修复 → W09，必须与上一提交成组。 |
| A16 | `126d610dfb6a` | 2026-08-17 | fix: 持久进程取消后保留可恢复会话 (merge request !1) | 中断部分结果应移植，首次取消直接 Valid checkpoint 不移植 → W04。 |
| A17 | `426191444582` | 2026-08-17 | feat(claude): allow AskUserQuestion with native structured output | Claude 原生 schema + Question/PlanReview Ask → W06；native schema+Permission Ask 不随之开放，普通 Permission Ask 保持。 |
| A18 | `97b2c85c36fc` | 2026-08-17 | Merge tag 'v0.14.4' into release/v0.14.6 | 双父 merge：把 126d610 分支（含 relay/MCP 修复）并入 4261914；按父链去重，无独立新工作项。 |
| A19 | `a68d907d1d12` | 2026-08-17 | Merge branch 'release/v0.14.6' into 'master' (merge request !2) | release/v0.14.6 集成 merge；承接上行集成，不把第一父增量再当一份独立修复。 |
| A20 | `3ea225acea57` | 2026-08-18 | 补发 claude 嵌套子代理（如 Explore）中 tool_use 的 Stream 事件 (merge request !3) | 嵌套 assistant wrapper 的工具事件补发 → W05；另补真实父关联与去重。 |
| A21 | `ca571fbed845` | 2026-08-18 | fix: 持久化 dedicated hosted tool profile (merge request !4) | Dedicated hosted-tool clone 持久化 → W02；强化 identity、跨进程所有权和清理边界。 |
| A22 | `eefcaef35621` | 2026-08-21 | refine | Claude result 关闭 interactive stdin 的主实现与 Phase3 测试 → W01。 |
| A23 | `a88eacdb4818` | 2026-08-21 | refine | 移除提取函数后的重复 guard；W01 采用最终差异。 |
| A24 | `d01a086e5145` | 2026-08-24 | Merge branch 'fix/claude-stdin-on-type-result' into 'master' (merge request !5) | 合入 eefcaef/a88eacd 的 merge；已逐父核对，W01 不重复实施。 |
| A25 | `325300905468` | 2026-08-25 | 一次 a2a 调用流程中支持多次上报制品 (merge request !6) | 实时 DelegationArtifact.Parts → W08；最终 full remote artifact 已存在，不误报全部缺失。 |
| A26 | `8ffe22a2a29c` | 2026-08-25 | feat: 支持宿主定义系统提示词 (merge request !7) | append system prompt 多 Driver 支持 → W11；采用单一 SharedOption 和当前 fingerprint 规则。 |
| A27 | `64163ab67ecf` | 2026-08-28 | 当工具调用 json 传入某个不支持的 key 时，可返回具体原因 (merge request !8) | 工具输入错误的 safe rejection → W07；补字段名边界与正确 Go 解码提示。 |
| A28 | `eb82ed36eaf6` | 2026-08-31 | 归一化 plan/todo 为 todo.updated 事件 (merge request !9) | todo/plan 标准化与 A2A DTO → W12；修正 ID 猜测、空快照和 helper 协议职责。 |
| A29 | `8e35b123af69` | 2026-09-03 | 修复 A2A 续答时将历史问卷回放误判为本轮结束 (merge request !10) | A2A 续答不受历史问卷 Task 快照截断 → W03。 |
| A30 | `e2f0620bdd64` | 2026-09-04 | feat: 新增 Run 级可暂停主动执行超时预算 (merge request !11) | 主动执行预算、Ask 暂停、A2A 错误映射 → W13；不搬逐字段 policy 合并或改义旧 Timeout。 |

## 附录 B：其余 15 个分支提交与主线集成对应

以下提交在 `--all --not origin/master` 中仍可达，多数为 squash 的原分支。比较使用分支实际变更路径，而非要求包含其他主线改动的整个 tree 相同。

| # | Commit | 日期 | 原始标题 | 主线 / 工作项 | 单条核对说明 |
|---|---|---|---|---|---|
| B01 | `f972c102dce2` | 2026-08-17 | fix: 持久进程取消后保留可恢复会话 | 126d610 / W04 | 取消 checkpoint/部分结果原始实现，最终改动文件内容相同。 |
| B02 | `5d2b5de8af1a` | 2026-08-17 | refine | 3ea225a / W05 | 嵌套工具补发原实现，含 fixture/streaming 测试。 |
| B03 | `98f41c7949cc` | 2026-08-17 | Merge branch 'master' into fix/claude-nested-subagent-tool-stream | 3ea225a / W05 | 把 master 合入嵌套工具分支；该 tip 的 5 个变更文件与集成提交一致。 |
| B04 | `0c0f32fe6eb6` | 2026-08-17 | fix: 持久化 dedicated hosted tool profile | ca571fb / W02 | 持久 hosted-tool profile 原实现和跨重建回归。 |
| B05 | `b11e05235ca9` | 2026-08-24 | test | 3253009 / W08 | 虽题为 test，实际增加 Parts 字段、映射、测试与设计文档。 |
| B06 | `f16fb20aaf8d` | 2026-08-24 | test | 3253009 / W08 | 修订 artifact 文档；该分支最终 4 文件与集成提交一致。 |
| B07 | `954aa68cd911` | 2026-08-25 | feat: 支持宿主定义系统提示词 | 8ffe22a / W11 | 系统提示词初版，多 Driver 参数/协议/测试/文档。 |
| B08 | `6943d2adddfe` | 2026-08-25 | refactor(system-prompt): 把 append 语义写进公共 API 的名字 | 8ffe22a / W11 | 重命名为 Append；不能移植初版覆盖含义不清的名字。 |
| B09 | `38e0e8c2d055` | 2026-08-25 | fix(claude): buildClaudeExecArgs 拆分以满足函数长度规范 | 8ffe22a / W11 | 拆分 Claude 输出参数构建并调整测试；最终 30 文件一致。 |
| B10 | `89231087ddb3` | 2026-08-28 | test | 64163ab / W07 | 虽题为 test，实际实现 invalidInputRejection 与 runtime 测试。 |
| B11 | `f5fbb1478473` | 2026-08-28 | test | 64163ab / W07 | 补 runtime 边界测试并微调错误文本；最终 2 文件一致。 |
| B12 | `a2f2b0b7e5c8` | 2026-08-31 | refine | eb82ed3 / W12 | todo 初始实现：三个 Driver、快照表、A2A DTO/decoder 和文档。 |
| B13 | `2e5f411673be` | 2026-08-31 | refine | eb82ed3 / W12 | 补 CodeBuddy 非 partial wrapper/去重路径、A2A 往返测试及合同说明。 |
| B14 | `8ea34b47089d` | 2026-08-31 | refine | eb82ed3 / W12 | Codex plan snapshot 初始化微调；最终 23 文件一致。 |
| B15 | `ac62c1921a36` | 2026-09-03 | fix: 续轮流忽略存盘 Task 快照，只认 live Status 终态 | 8e35b12 / W03 | 续答 Task snapshot 修复原提交；最终 7 文件一致。 |

分组内容核对结果：`f972c10 → 126d610`（10 文件）、`98f41c7 → 3ea225a`（5 文件）、`0c0f32f → ca571fb`（3 文件）、`f16fb20 → 3253009`（4 文件）、`38e0e8c → 8ffe22a`（30 文件）、`f5fbb14 → 64163ab`（2 文件）、`8ea34b4 → eb82ed3`（23 文件）、`ac62c19 → 8e35b12`（7 文件），各组分支改动文件在集成提交均与分支 tip 完全一致。中间 refine/test 提交的最终效果由对应 tip 承接，不另建工作项。

## 附录 C：实施阅读入口

当前权威合同：[AGENTS](../AGENTS.md)、[API reference](api-reference.md)、[streaming](streaming.md)、[Driver contract](streaming-adapter-contract.md)、[tools](tools.md)、[A2A](a2a.md)、[run policy](run-policy.md)、[structured output](structured-output.md)、[CHANGELOG](../CHANGELOG.md)。

internal 资料须在固定 `e2f0620` 上读取，避免混入其未提交 vision 文档：

- `docs/workstream-persistent-process-e2e.md`
- `docs/workstream-capability-invocation-observability.md`
- `docs/workstream-a2a-nested-capability-relay.md`
- `docs/workstream-a2a-streaming-artifact-parts.md`
- `docs/workstream-system-prompt.md`
- `docs/workstream-plan-todo-normalization.md`
- `docs/workstream-active-execution-timeout.md`

这些资料是背景和实现记录。遇到它们与代码、后续修复或当前 AGENTS 不一致时，以本方案列出的差异裁决和当前合同为准，并在实施 PR 中补齐合同证据。
