# T15 CodeBuddy provider adoption

本任务基于已接受 G03 `926dbbf90a98d35416cdbbc6e376c7bbdc5da084`，仅修改 `codebuddy/` 与本 handoff。承担 W09-R07、W11-R06、W12-R05；不宣称 W09/W11/W12、G04 或发布门禁整体完成。

## 使用语义与示例

```go
agent := adaptor.New(codebuddy.Driver(codebuddy.Config{Model: "glm-5.2-ioa"}),
    adaptor.WithAppendSystemPrompt("请保留默认规则，并始终使用中文。"))
result, err := agent.Run(ctx, "说明本仓库", adaptor.WithAppendSystemPrompt("本轮使用英文。"))
// 空串只清除此 SDK 通道的构造默认值。
result, err = agent.Run(ctx, "说明本仓库", adaptor.WithAppendSystemPrompt(""))
```

CodeBuddy 原生追加独立传递为 `--append-system-prompt <原文>`，不写入用户 Prompt、Instructions 或 profile。文本原 UTF-8 字节不 trim；32768 bytes 接受，超长、非法 UTF-8、NUL 明确拒绝。非空 append 校验 processx 实际 executable/argv；Windows native/PowerShell 限制为含结尾 NUL 的 32767 UTF-16 units，cmd shim 为 8191，且有 CR/LF、引号及 shell 元字符的 cmd argv 返回 `unsafe_shell_argument`。空 append 保持此前未追加时的 transport 行为；这不是对原有 cmd 参数组合新增无损担保。含 JSON/schema 的追加请求应使用原生 executable 或可无损 PowerShell 路径。

`--system-prompt`、`--system-prompt-file`、`--append-system-prompt`、`--append-system-prompt-file` 的 detached/`=` ExtraArgs 在配置校验、Inspect probe、直接 SPI Run 中一律拒绝，即使 append 为空；不会悄悄删除隐藏覆盖。SDK invocation 元数据中的 append 值脱敏，实际 argv 保持原值。OS 仍可能暴露 argv；provider 自行输出的 Raw、Transcript 和 terminal 仍完整保留，不能把脱敏扩大成审计输出丢失。

相同 append 可续接和复用；变化/清除在 ResumeOnly 下拒绝且不动健康记录，默认策略通过既有原子重绑替换。checkpoint Data 用原文 SHA-256 的 `append_system_prompt_fingerprint`，空省略、旧缺键视空；SessionCodec 原样映射并纳入 guard。私有进程签名增加同一 hash，保留原 command/env/profile/settings 等全部维度；逐轮 Streaming 不变成 durable identity。native schema 临时轮、prewarm、WithSpawn、Close 与单 writer 仍走既有生命周期。没有新文件载体或清理归属。

## 观测矩阵

| 实际 provider 协议 | Capability | Todo | 父关联 |
|---|---|---|---|
| batch JSON（原生 schema） | 未声明支持 | 未声明支持 | 未观察 |
| stream-json，包括 control/常驻与非 partial 完整 wrapper | Skill.command/skill；MCP 精确 catalog 前缀；Task/Agent.subagent_type | 正式成功结果确认后的全量快照 | 不宣告父图支持 |

consumer Run/Stream 均消费同一 resolved transport；不因为某次用了 Run 而禁止已正式观察到的事实。Catalog 来自最终 Request 的 Skills/MCP（含 hosted/runtime 已合入服务）/ProfilePayload.Agents；逐字精确、重复项去重、歧义永不选赢家。MCP 只接受 CodeBuddy 自身 `mcp__<server>__<operation>` 构造形式，枚举全部已知 server 前缀，保留 Unicode/下划线；不复制 Claude alias 规则。Skill init 列表、slash prompt 文本和审批允许均不证明调用完成。started/terminal 使用同一真实 tool ID，缺结果仅 Interrupted，明确取消为 Cancelled；Duration 未观察为 nil，OccurredAt 为识别时 UTC。

TodoWrite 的已核实字段是 `newTodos`，其正式成功结果才确认输入列表。TaskCreate/TaskUpdate/TaskList 首选 `tool_result._meta.rawResponse.todos` 完整快照；TaskCreate 以 `rawResponse.task.id` 或精确正式成功文案取得真实 ID。已确认 task 对象缺 ID 时才用 `synthetic:` 无碰撞 run/scope/call tuple，明确 SyntheticID；不把本地创建计数当 TaskUpdate.taskId。没有 ID 的列表项使用 run/scope/snapshot/position tuple，仅为全量快照稳定展示坐标，绝不能被真实 taskId 更新命中。TaskUpdate 无完整列表时只更新本轮已知真实 ID；无 `_meta` 时仍须精确正式成功文案。新 run 不恢复本地表；相同快照不增 revision，首次空表为 revision 1 清空。失败/未知/畸形结果、未知状态、重复 ID、非法 UTF-8、超限内容均不部分更新旧表，并发安全 notice，Raw 与原工具/Transcript 不丢。

CodeBuddy 2.137.1 的 user 结果 wrapper `parent_tool_use_id` 指向自身 call ID，不能把它当父关系。assistant/partial 的非空或畸形父字段缺少已核实图语义，观察时明确降级并隔离对应 ID，不合并入根 scope；不冒充完成了 Claude W05。没有观察事件不意味着没有调用，更不证明审计/计费完整。

## 合并位置与公共变化

- `codebuddy/doc.go`：本提交已交付局部 godoc，包含上述支持和限制。
- `docs/api-reference.md` 的 WithAppendSystemPrompt provider 表：CodeBuddy 标为 native inline，补 32768 bytes、Windows 限制、ExtraArgs 和 SDK argv 脱敏说明。
- `docs/streaming.md` / `docs/streaming-adapter-contract.md` 的 provider observation 表：stream-json/control/常驻支持 Skill/MCP/Subagent/Todo，batch JSON 为 false；TodoWrite 使用 newTodos，任务成功结果与真实 ID，CodeBuddy 父图未观察。
- `docs/run-policy.md` / `docs/structured-output.md`：不修改 CodeBuddy 既有 native JSON schema 与 control HITL 不共存的矩阵；schema/append 组合在 Windows cmd shim 上的限制明确列出。
- README provider-native prompt 示例和 CHANGELOG：新增 CodeBuddy 原生 append、正式 capability/todo，以及 ExtraArgs 保留 flag 的明确拒绝；完整 Raw/partial/checkpoint、三进程接力与单 writer 保持。
- 未新增 root/SPI/Config 公共声明，没有 golden 更新或新 With*；使用前批已冻结的一个 WithAppendSystemPrompt 和 Event/SPI。全树 27 个 With* 计数由中央文档维护。

## 不采用的 internal 行为与依赖

固定读取 internal `1921636510ced4c830c0287fe68d41218f1ed185`、`8ffe22a2a29c0b72e6c993d7774ffb54ac15c010`、`eb82ed36eaf6339b00860b5506a683e6dbd74765` 的 CodeBuddy parser/argv/fixture；`9b1ce27faa4e46ba6a213210eaffc8748b327bdb`、`dab6933b9159894c36fa78b8367c6bc572227621` 属 Claude alias 修订，不移入 CodeBuddy。源 head 固定 `e2f0620bdd6477e6fe16f6db5648093589342ca2`，未读其脏工作区。

拒绝 slash+init 推断 skill 调用、源库根 RunEventCapability/旧 API、输入完成即 todo 成功、本地自增序号猜真实 taskId、空表丢弃、共享 helper 解析 provider JSON、append 只入进程 hash 却不入 Thread/checkpoint。使用前批中立 catalog/tracker/table 与 systemprompt 机制，没有新增依赖；纯状态机/JSON 仍用标准库，无新增顶层 require。

## 验证与 live 交接

本地 macOS arm64 的正式帧重放与真实假子进程（不是 live）覆盖：控制许可、partial/wrapper 去重、catalog 冲突/Unicode、成功/失败/缺失/取消生命周期、真实/合成 ID、完整/空/非法快照、新轮无缓存、parent 不误归属、并发 dispatch；公开 Agent/Thread × Run/Stream 到 observer 的同次事实，以及终局 Text/Raw/Transcript/Usage 保真。追加覆盖真实 control/batch argv、默认/覆盖/清除、SDK 诊断与 provider Raw 区分、checkpoint guard、ResumeOnly/default、native schema 三进程接力、WithSpawn 与 single writer。原 T08 partialRaw/terminal/Usage/Waitcause 测试未删减。最后 SHA 的必需整包和五轮 race 日志见 result.json/evidence；该报告在源码 commit 后产生，避免 SHA 自引用。

已有两项 fake process outcome 测试曾缺 HOME 配置，sandbox 拒绝其用户 profile lock 路径；现已加私有 HOME/USERPROFILE/CODEBUDDY_CONFIG_DIR，原断言完整。最终完整测试使用任务私有 HOME，loopback 仅为已有 WithTools fixture。本任务没有读登录凭据或运行付费模型。

所有真实测试统一 `TestAlignmentLiveCodeBuddy*`，位于 `codebuddy_live` build tag；既有 DriverConformance live 分支也由独立带 tag helper + `AGENT_ADAPTOR_LIVE_CONFORMANCE=1` 门控。启用后缺 CLI 不 skip，版本/help 必须采集；仅从显式绝对 `CODEBUDDY_CONFIG_DIR_SOURCE` 认证 fixture 复制两个 credentials 文件到私有目录，不复制 settings/MCP，不默认采用用户 profile。运行目录和 HOME 私有。B06/T28 使用 `go test -tags codebuddy_live -count=1 -timeout 30m ./codebuddy -run 'TestAlignmentLive|TestCodeBuddyDriverConformance'`，要求授权、认证与同一 G05 SHA；本批只在 env=0 验证入口编译/禁用，没有 native Linux/Windows 或 live 通过声明。

live 入口含既有 streaming/Thread/persistent/Permission/Question/PlanReview，新增随机 append 标记有/无对照、同内容复用/WithSpawn/清除 guard、hosted MCP 工具完成、真实 task ID 与 todo 清空。未知 capability/父图、batch observation 和 native schema+control HITL 的未支持边界按文档明确，不用 skip 冒充已支持通过。Native Windows argv/进程树与 Linux race/fuzz 留既定 B06 门禁。
