# T16 Codex 文档交付片段

本片段由 G04 合并到集中使用文档与 CHANGELOG。T16 仅交付 Codex 范围，不关闭整个 W04/W09/W11/W12、G04 或发布门禁。

## 公开语义与使用例

此前 Codex 未声明 append、capability 或 todo 支持。本次消费前批冻结的公共合同，不增加根包执行入口或 Config 镜像字段：

```go
agent := adaptor.New(
    codex.Driver(codex.Config{Model: "gpt-5.4"}),
    adaptor.WithThreadStore(memory.NewStore()),
    adaptor.WithAppendSystemPrompt("请先验证结论，再简洁回答。"),
)
defer agent.Close(context.Background())
thread := agent.Thread("host-owned-key")
result, err := thread.Run(ctx, "检查这里的错误处理。")
// 单次调用空串清除构造默认值；continue-or-start 可安全新建兼容会话。
result, err = thread.Run(ctx, "继续检查。", adaptor.WithAppendSystemPrompt(""))
```

已有的近处替换、空串清除、WithInstructions 独立通道和唯一 Run/Stream resolved 管线保持。非空 append 使用 provider-native developer instruction 通道，绝不拼入用户 Prompt、profile instructions 或 baseInstructions。

| 实际 transport | Append 载体 | Skill 事实 | MCP / Subagent 事实 | Plan |
| --- | --- | --- | --- | --- |
| exec JSONL / exec resume | 独立 `-c developer_instructions=<TOML string>` argv | unsupported | unsupported | unsupported |
| app-server one-shot / resident | thread/start、resume、fork 的 `developerInstructions`，两套握手共六处 | 实际提交并经 turn/start 正式接受的 typed skill input | 正式 MCP server/tool；正式 spawn collab receiver 与 child role 唯一关联 | 正式 `turn/plan/updated` 全量 |

能力描述按 provider transport；消费者选择 Run 或 Stream 不自行决定另一套协议。无正式证据只表示未观察到，不表示资源从未被调用，不能作为计费完整性证明。

exec inline 上限为原文 32768 UTF-8 bytes，并校验 processx.PrepareCommand 后的最终 command/argv；32769 启动前拒绝。app-server JSON-RPC 无此 SDK inline 上限。中文、换行、CR、引号、反斜线、emoji 和前后空白保持原字节。非 UTF-8/NUL 按冻结合同拒绝。exec 的正文仍可见于操作系统 argv；SDK invocation diagnostics 对托管 developer_instructions 值脱敏。provider stdout/stderr/Raw 不删改，也不承诺清洗 provider 主动回显的正文。

ExtraArgs 中 `developer_instructions`、`instructions`、`base_instructions`、`model_instructions_file`、`experimental_instructions_file` 五类顶层 TOML key 始终拒绝，包括空 append、引号 key、attached/detached `-c` / `--config`。冲突使用 SystemPromptUnsupportedError（conflicting_extra_args），畸形 override 使用 InvalidDriverConfigError；错误不包含正文。无关合法覆盖仍支持。

append 原字节 fingerprint 同时进入实际常驻 startup signature 与私有 checkpoint Data 的 `append_system_prompt_fingerprint`。SessionCodec 保全此值，direct SPI resume/fork 同样校验。历史缺 key 按空比较，空 append 保持历史 guard fingerprint；同值复用、同长度不同值和非空到空均正确区分。ResumeOnly 遇变化拒绝；允许新建的 Thread 路径先有界停止旧 writer，再 replacement。WithSpawn 本轮进程不注册成后续 writer；prewarm 带同一 append，不发送用户 prompt。

## 观测与 plan 的精确含义

Skill 显式 `$name` 引用从本次 resolved catalog 选择唯一 runtime name 和绝对 SKILL.md 路径作为独立 native skill input。只有 turn/start 正式成功才产生 Started/Completed，Evidence=NativeInputAccepted；Completed 表示输入接受操作完成，不表示 skill 文件已被读取或任务已执行。仅声明 catalog、普通文本或失败的 turn/start 不构成事实。

MCP 使用正式 mcpToolCall 的 server/tool/id/status 与本次 resolved MCP catalog，重复 start/terminal 去重，失败与缺终态分别产生 Failed/Interrupted；取消关闭待定事实为 Cancelled。Subagent 只观察 spawnAgent 操作：当前 thread/turn 的正式 collab item 提供 receiver，thread/started 必须通过 source.subAgent.thread_spawn.parent_thread_id 指回当前 thread，并由 agentRole/agent_role 唯一匹配已解析 Agent catalog。缺失、冲突、未知或歧义归属不猜；spawn Completed 不代表子任务最终完成。没有正式 parent tool 字段时 ParentToolCallID 留空。

Capability typed 数据只含冻结的安全字段，不带 arguments、result、Raw、URL、header、env 或正文；原 Transcript/Raw 保全。安全降级使用 `capability_unresolved`、`capability_invalid`、`capability_limit` notice。事实状态仅使用前批中立 tracker；共享 helper 不解析 provider JSON。

Plan 使用原有 current thread/turn、响应前排队和 terminal fence。pending/inProgress/completed 精确映射，快照保持原顺序和原文。provider 无 step ID，因此使用确定性、无碰撞的 turn/position tuple synthetic ID，SyntheticID=true；不能当成 provider task identity。首次合法空表产生 revision 1，重复表不递增，合法 [] 清空。整表验证失败保留上一快照，发 `todo_invalid` notice；无效 UTF-8 或破损 surrogate 不修补成事实。实验性 plan delta 仍作为现有 opaque 事件/Raw，不产生 Todo，也不构成 PlanReview 审批。

历史 tokenUsage 只有“同 thread、非当前 turn、通过正式 generated DTO 校验”的 replay 可作为 Raw 审计数据忽略。不会更新当前 Usage、Transcript、Todo、Capability 或 checkpoint；异 thread、其他旧 turn 通知和终局守卫没有放宽。child thread 元数据也不重绑定当前 run。

## 输出、生命周期与回归

保留已有 persistentWriter 的部分 `result, persistentErr`，补齐同一 finishAppServerResult 的元数据/已观察 runtime 报告映射。exec helper 错误也先解析其已捕获完整 stdout/stderr 再返回原 cause；helper broken-pipe 后已到达的正式终局不丢。常驻错误先有界回收再 snapshot，保全已观察 stdout/stderr、Text、Transcript、Usage 和正式终局。

失败、取消、畸形协议、缺终局、非零退出不产生健康 checkpoint；不能从 thread ID 推断健康，也不把输出保留改为错误后持久化。原健康 Thread record 仍由既有统一管线维持。成功 Run/Stream.Result 的 Text/Summary/Raw/Transcript/Usage 等价；schema/native structured output、MCP 权限/凭据/隔离、审批与单 writer 既有合同由完整 codex 包回归覆盖。

## 来源、公共声明与依赖

实施 base 是唯一已接受 G03 `926dbbf90a98d35416cdbbc6e376c7bbdc5da084`。internal 仅通过固定 `git show e2f0620bdd6477e6fe16f6db5648093589342ca2:<path>` 读取 capability_observation_test.go、run_streaming.go、appserver/translate.go 及固定 tree；不读 internal 工作区，也不合并其旧公共 API/调度框架。

正式协议依据为本 base 已提交的 `codex/appserver/schema/v2/{ThreadStartParams,ThreadResumeParams,ThreadForkParams,TurnStartParams,TurnPlanUpdatedNotification,ItemStartedNotification,ItemCompletedNotification,ThreadStartedNotification,ThreadTokenUsageUpdatedNotification}.json`。字段 developerInstructions、skill 的 type/name/path、mcpToolCall 的 server/tool/status、collabAgentToolCall 的 senderThreadId/receiverThreadIds/tool/status、child agentRole/source.subAgent.thread_spawn 和 plan step/status 均已存在。ThreadStartParams checked-in schema 的引入 commit 为 `1e635dc6e3295e00b7db08f49d47d76d923064ad`；现有 union/run 注释记录的历史 CLI 是 0.120.0，provider matrix 记录 0.125.0。这些是历史来源说明，不能替代 B06 实际 CLI 版本证明；本任务没有运行真实 codex --version。

generated.go、schema/** 和 generate.go 的 Git blob 全部保持 base 原值；本次不需要生成变更。handwritten envelope 新增三个 DeveloperInstructions 字段，Options 新增 append/typed skill input/本次 resolved catalog 字段，常量新增正式 NotifyTurnPlanUpdated。属于现有 provider appserver 边界，不泄露 internal 类型。根包/driver SPI 与 AST golden 由前批提供，本任务不改它们；无需新增公共 With* 名或依赖。

复用已在 go.mod 中的 pelletier/go-toml/v2 解析实际 TOML key，并复用已冻结 systemprompt 编码/guard helpers：可避免手工 quote/key 解析歧义，维护与版本已由项目管理，使用局限于 Codex；没有新增顶层 require。不采用 internal 递归/文本猜测 capability、按工具名猜 parent、trim 原 plan 文本、忽略空 plan、错误后凭 ID 造 Valid checkpoint、旧 transport/双执行管线。

## G04 集中合并目标

- `docs/api-reference.md`：WithAppendSystemPrompt/Thread compatibility 段加入上述 Codex native 载体、空清除、typed unsupported、exec 长度和诊断边界。
- `docs/streaming.md`：能力矩阵中将 Codex app-server Skill/MCP/Subagent/Todo 标为正式有限支持、exec 四项 unsupported；写入 NativeInputAccepted、spawn-only、plan synthetic/clear/fence 与旧 usage audit-only。
- `docs/profile-resource-provider-matrix.md`：Codex 行区分资源物化、input acceptance 与实际调用观测；列出准确正式字段及版本证据边界。
- `README.md`、`README.zh-CN.md`、`README.de.md`、`README.ja.md`、`README.ko.md`：在 append/能力摘要处同步 Codex 支持及空清除例，保留统一 API。
- `CHANGELOG.md`：Added Codex native append 与正式 app-server capability/plan；Fixed exec helper 错误和常驻错误部分结果保全、historical tokenUsage replay 污染；注明无 checkpoint 健康降级。
- 局部 godoc 已写入 `codex/doc.go`，handwritten Options/DTO 的字段注释随代码交付。

## 验证入口与实跑边界

新增回归均为 TestAlignment 前缀；使用 `codex/testdata/alignment-provider/main.go` 的本地协议进程，记录真实 argv/stdin/JSON-RPC、PID 和前一进程仍存活数；六处握手并非单独 marshal DTO。完整必需命令和最终提交的实际测试/子测试计数、skip、OS/Go、日志 SHA 见 result.json/evidence。普通环境强制三项 gate=0，任务私有 HOME/USERPROFILE，native Go PATH 与只读 GOMODCACHE；fixture 显式自有 CODEX_HOME。开发中最初 sandbox profile/loopback 失败日志保留，不计功能通过；没有越权重试用户 profile。

B06 已交付四个 `TestAlignmentLive*`：NativeAppend（exec/app-server fresh/resume、app-server fork、无 append nonce 对照）、ObservationAndPersistent（typed skill/MCP/plan 与两轮复用）、SubagentCatalog（正式 child role + canonical key）、CancellationAndAppendRebind（取消部分输出、原健康 checkpoint、ResumeOnly 拒绝与空清除新建）。既有 TestCodexDriverConformance live 分支与 appserver live 测试亦补齐 build tag + env 双门和独立 profile/workspace。两门开启后 CLI/证据缺失失败，不能 skip 必需场景。

B06 只有另获授权才可执行：`AGENT_ADAPTOR_LIVE_CONFORMANCE=1 AGENT_ADAPTOR_CODEX_LIVE_PROFILE=<explicit-isolated-auth-seed> go test -tags=codex_live -count=1 ./codex/... -run 'TestAlignmentLive|TestCodexDriverConformance|TestAppServer'`。helper 只从明确授权的隔离 seed 复制 auth.json 到新的 0600 临时 profile，记录 CLI --version，并使用新 workspace；绝不推断或复用用户原目录。本批只以 env=0 编译这些入口，不产生 provider 请求；普通无 tag 即使 env=1 仍不会开启 live。Windows native、Linux native/race 全局门和真实 live 仍由 B06 在指定 head 执行，不把本地 fixture 或禁用 probe 当作通过证据。
