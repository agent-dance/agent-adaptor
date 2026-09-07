# C04：native append 与 Claude schema/HITL 冻结合同

本合同面向 T31、T07、T10、T14–T17，以及其独立 verifier。基线为 `bc0d421f9c0b1e80e529d1e843fe5d8396eab02f`（生产代码祖先 `919f140f64f89c80933802840c8878681a85a4d9`）。internal 仅按固定 Git 对象 `426191444582f9fbbe951dbd8b54c1004464262c`、`8ffe22a2a29c0b72e6c993d7774ffb54ac15c010`、`e2f0620bdd6477e6fe16f6db5648093589342ca2` 阅读。MUST 表示实施和验收的共同约束；示例是拟实施合同，不能视为当前已发布 API。

## 1. 已核对事实与协调裁决

1. 当前 `claude/driver.go` 的 `RunPolicyCaps.Permission.Ask` **已经为 true**；`claude/interactive_test.go` 和 `interactive_live_test.go` 有相应入口。W06 原文“Permission Ask 继续 unsupported”不能被解释为移除无 schema 的既有 Permission Ask。
2. 当前 `internal/engine/structured.go:resolveStructuredOutputSource` 对 native 和 prompt 两种机制都使用同一 `WorksWithHITL`。Claude 当前值为 false，因此 **schema + Ask 在当前基线全部拒绝**；`wiring.go` 的 batch 重试不能绕过这项限制。原方案“当前可自动 prompt fallback”是事实勘误，不是本批实测成功。
3. 协调者已接受：保留普通 Permission Ask；本项开放 native + Question/PlanReview；native + Permission Ask 暂不宣告，固定回退 prompt + 本地校验。通过逐机制、逐 Kind 的声明落实，绝不由粗粒度 bool 推导全部组合可用。
4. 为避免同批 Go 符号依赖，协调者指定 **B01 T31** 先实施 §2 的 SPI/core 合同，G01 合流后 B02 T07 才使用。G00 必须将新增任务、范围、要求映射、manifest 哈希和 W06 勘误纳入正式任务包；C04 自身不改任务包、不消费同批产物。
5. Codex 当前三份正式 `schema/v2/Thread{Start,Resume,Fork}Params.json` 均包含 `developerInstructions: [string,null]`，但手写 `union.go` 尚未接线。W11 不需要改 generated Go 或 schema JSON。

## 2. schema 协商的精确 Go 合同（T31 提供，T07 消费）

在 `driver/driver.go` 增加真实 SPI 值类型及两项可选字段；不向根包增加 alias：

```go
type StructuredOutputHITLCapability struct {
    Permission bool
    PlanReview bool
    Question   bool
}

// 以下两字段加入既有 StructuredOutputCapability，其余字段保留。
NativeHITL         *StructuredOutputHITLCapability
PromptValidateHITL *StructuredOutputHITLCapability
```

这两个字段是 Driver 的能力事实，不是消费者或 SPI 的机制选择器。每次返回 Descriptor 必须给独立快照；core 不修改声明。精确算法如下：

1. 先验证有效 `RunPolicy` 及每个 Kind 的 `RunPolicyCaps`。schema 能力不能授予普通 policy 本身不支持的 Ask。
2. schema 为 nil 时 source 为空，不检查本节矩阵。所有已有无 schema 行为保持。
3. 对 native / prompt 分别计算 eligibility：对应 `JSONSchema*` 为 true、`WorksWithRun` 为 true；resolved provider streaming 时还要求 `WorksWithStreaming`。
4. 对每种机制取对应 HITL 指针：nil 沿用 `WorksWithHITL` 仅检查显式 Ask 的原语义；非 nil 则**替代该机制**的粗粒度 HITL 判定，先用 `driver.EffectiveHumanDecisionPolicy` 解析默认 Permission/PlanReview Ask 和 Question 自动拒绝。该完整有效 Ask 集合中的所有 Kind 都必须为 true，普通 Ask 能力也必须支持。非 Ask 不读取对应字段。非 nil 全 false 是显式不支持；不能被 `WorksWithHITL=true` 翻转。
5. 优先 native；不可用时 prompt + 本地校验；两者不可用才返回现有 `*driver.StructuredOutputUnsupportedError`。其 `Driver` 为 descriptor Type，`Reason` 给受控组合诊断，`errors.Is(err, driver.ErrStructuredOutputUnsupported)` 保持。
6. 静态 schema/source/transport 在普通 policy 校验后、任何 profile/workspace/runtime/skills/lease 获取前只解析一次，缓存仅属于本次调用，clone 必须清空；wiring 复用决定。transport 只由一次 resolved invocation 决定，Run 与 Stream 完全一致。batch fallback 按机制分别检查适用 Ask：非 nil 使用完整有效默认值，nil 保留旧显式边界；只在没有必须保持 rich transport 的已解析需求时可用；不能为了 schema 丢掉 Ask、已请求的 capability/todo 观测。C03 的需求必须与本算法在唯一协商点求交集，不能派生第二管线。
7. Driver 只执行 `Request.StructuredOutputSource`，禁止重新决定机制或修改 `OutputSchema`。

R005：Question Ask 未显式设置 Permission 时，Permission 会继承 Ask；Claude native Permission=false 因而不能只凭 Question=true 选 native。采用新矩阵允许此前隐式策略的 schema 调用变为 prompt 或 unsupported，禁止偷偷改成自动批准。真实 fixture 必须发起 DecisionSink 请求并核对回复，不能仅验证假 Response。

`WorksWithHITL` 保留原字段及 nil 语义，便于既有第三方 Driver 不变。若新矩阵有 true，对应机制和 `WorksWithRun` MUST 为 true，且对应普通 `RunPolicyCaps` 支持 Ask；adaptertest/godoc 同步冻结这些蕴含关系。不把旧字段当成新矩阵的总开关。

Claude 的目标声明（T07，T31 已合入后）：

```go
StructuredOutput: driver.StructuredOutputCapability{
    JSONSchemaNative: true, JSONSchemaPromptValidate: true,
    WorksWithRun: true, WorksWithStreaming: true, WorksWithHITL: false,
    NativeHITL: &driver.StructuredOutputHITLCapability{
        Permission: false, PlanReview: true, Question: true,
    },
    PromptValidateHITL: &driver.StructuredOutputHITLCapability{
        Permission: true, PlanReview: true, Question: true,
    },
},
```

`WorksWithHITL=false` 是给旧读者的保守摘要；Notes MUST 解释精确矩阵。T07 不修改其它 Driver 能力；不宣称 Permission native 已被验证。所有组合均使用现有 `ApprovalRequest` / `DecisionCapableSink`、既有 policy timeout/retry/abort 规则；不新增审批 channel、Risk、fallback 审批策略。

## 3. Claude 参数、状态机及正式 fixture（T07）

`native := req.OutputSchema != nil && req.StructuredOutputSource == driver.StructuredOutputSourceNative`；`interactive := wantsInteractiveClaude(req.Policy.HumanDecision)`；`useStreamJSON := interactive || req.Streaming`。以下 argv 均是独立数组元素，例子不表示通过 shell 拼接：

| resolved invocation | 必须参数 |
|---|---|
| native + Question/PlanReview Ask | `--print --output-format stream-json --verbose --json-schema <prepared JSON> --input-format stream-json --include-partial-messages --replay-user-messages --permission-prompt-tool stdio` |
| native，无交互，Streaming=true | `--print --output-format stream-json --verbose --json-schema <prepared JSON> - --include-partial-messages` |
| native，无交互，Streaming=false | `--print --output-format json --json-schema <prepared JSON> -` |
| prompt fallback + 任意受支持 Ask | 双向 stream-json 参数同第一行，但没有 `--json-schema`；core 的 schema 提示沿既有 user prompt 协议 |

SDK 管理的 transport/schema/permission/session/model 参数仍按当前 `withoutManagedClaudeArgs` 规则移除 ExtraArgs 重复项，再由 resolved invocation 发出唯一值。不得照搬 internal 的 `OutputSchema.Mode` 或 `WorksWithStart`。加入 append 后，§7 的提示通道冲突单独 fail-closed。

当前 Claude native schema 是本轮临时进程形态，`persistentEligible` 对 native 保持 false。Thread 调用先有界停止旧 writer，再完成本轮新进程；本轮进程不进入常驻池。之后允许的 prewarm 使用同一 append 内容和同一健康 checkpoint，`WithSpawn` 不把本轮进程注册为 writer。测试必须覆盖“有 Thread”与“实际复用常驻”的区别，不能仅凭 Thread 测试名宣称 native 多轮使用同一 PID。

T07 fixture 必须包含以下可逐字段比较的 NDJSON（JSON 编碼和字段顺序可变，语义不变），并使用已有正式 parser：

```json
{"type":"system","subtype":"init","session_id":"session-c04"}
{"type":"control_request","request_id":"question-c04","request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion","tool_use_id":"tool-question-c04","input":{"questions":[{"question":"选择目录","header":"目录","multiSelect":false,"options":[{"label":"docs","description":"文档"},{"label":"src","description":"源码"}]}]}}}
```

调用对应 Question 的 `Answer` 后，期望同一 stdin 发出：

```json
{"type":"control_response","response":{"subtype":"success","request_id":"question-c04","response":{"behavior":"allow","toolUseID":"tool-question-c04","updatedInput":{"questions":[{"question":"选择目录","header":"目录","multiSelect":false,"options":[{"label":"docs","description":"文档"},{"label":"src","description":"源码"}]}],"answers":{"选择目录":"docs"}}}}}
```

PlanReview fixture 使用 `tool_name:"ExitPlanMode"`、`input:{"plan":"检查文档"}`，Approve 回 `behavior:"allow"` 和原 `updatedInput`；Deny 回 `behavior:"deny"` 及受控 message，是否 `interrupt:true` 严格由既有 `Policy.Approvals` 决定。Question 的 synthetic `question_type` 不得回送给 CLI。

终局单独测试，无须先有 `message_stop`：

```json
{"type":"result","subtype":"success","is_error":false,"session_id":"session-c04","result":"完成","structured_output":{"directory":"docs"},"usage":{"input_tokens":0,"output_tokens":0}}
```

schema 是 `{"type":"object","properties":{"directory":{"type":"string"}},"required":["directory"],"additionalProperties":false}`。预期：Text=`完成`、完整 stdout 和 terminal JSON 保留、正式 Transcript 保留；结构化结果取 `structured_output`，不把其 JSON 拼进 Text。真实零 usage 与未观察 usage 区分。缺少 assistant 文本时不能拿 stdout 兜底 Text。

状态顺序固定：prompt 交付 → 正式 control_request → 唯一 sink Ask 等待/应答 → provider terminal → 解析余下 stdout/stderr → 本地结构化校验 → Result/checkpoint → 终局 Events 关闭。T01 的 result 关闭 stdin 必须生效且 exactly-once；常驻 nonClosingStdin 不关闭底层 writer。错误/取消/timeout 必须解除等待；重复、过期、Kind 不匹配响应沿既有错误身份。C02 的部分 Result/cause 合同覆盖这些失败路径，C02 预算暂停仅发生在唯一 sink，不由 Claude parser 自己计时。

将上述终局改为错误类型值 `directory:1`、缺 `structured_output`、`is_error:true`、非零退出、截断 NDJSON，以及超时/取消，均不能得到有效 checkpoint。保留旧健康 Thread record；不把“拿到 session_id”当健康证明。终局和 cancel 并发时遵守已确定主因，Driver 不自动重发可能已交付的 prompt。

## 4. append 的精确公共合同（T10）

新增且仅新增以下 root 操作和设置方法：

```go
func WithAppendSystemPrompt(text string) SharedOption
func (s *RunSettings) SetAppendSystemPrompt(text string)
```

`RunSettings` 以私有 `appendSystemPrompt string` 保存；clone 复制字符串即可。Set 方法替换目标值，不拼接；构造默认值被每次 call 克隆后就地覆盖，最后一个同作用域选项胜出。**只以 `text == ""` 判断清除**；空格、前后换行和 Unicode 原字节保留，不 TrimSpace，不标准化换行。空串只清除 SDK 此通道的默认值，不擦除宿主其它原生配置。

新增 SPI：

```go
// driver/driver.go
type SystemPromptCapability struct { Append bool }
// 加入 Descriptor：
SystemPrompt SystemPromptCapability

// driver/run.go，加入 Request：
AppendSystemPrompt string

// driver/config.go（T10 当前已拥有该文件；不新增未分配公共文件）
var ErrSystemPromptUnsupported = errors.New("agentadaptor: system prompt unsupported by driver")
type SystemPromptUnsupportedError struct {
    Driver string
    Reason string
}
func (e *SystemPromptUnsupportedError) Error() string
func (e *SystemPromptUnsupportedError) Unwrap() error

// errors.go，根包只复用 driver 身份，遵循已有 structured-error 形式：
var ErrSystemPromptUnsupported = driver.ErrSystemPromptUnsupported
type SystemPromptUnsupportedError = driver.SystemPromptUnsupportedError
```

Error 必须 nil-safe，不包含 prompt 内容；Unwrap（含 nil receiver）返回 sentinel。`Driver` 是静态 driver Type，内置实现的 Reason 是封闭原因字符串：`unsupported_driver`、`invalid_utf8`、`nul_byte`、`inline_limit`、`command_line_limit`、`unsafe_shell_argument`、`conflicting_extra_args`。不增加公开 Reason enum 或 Limit 字段；数字限制写在 godoc/使用文档和测试。materialization 的 I/O/权限/完整性失败走普通 `%w` 包装，保留底层可 `errors.Is/As` 的 error，不假装能力缺失。

不增加 `WithDefaultAppendSystemPrompt`、Config/CommonConfig 镜像、模式选择器或新的执行入口。非空请求遇到 `SystemPrompt.Append=false`，core 在资源获取、Thread lease、Driver.Run **之前**返回 typed unsupported；直接 SPI 调用也由 provider 在物化/启动前防御性拒绝。零值 Descriptor 明确不支持，空值无需任何能力。

全链路：New defaults / call override → 唯一有效 RunSettings → preflight 字符/能力校验 → `buildRequest` → `Request.AppendSystemPrompt` → Thread fingerprint → Driver native transport。原 prompt、Instructions、ProfilePayload、MCP/service 声明不得被此内容改写。schema prompt-validation 的既有前缀与 append 是两个独立通道。

Inspect 不增控制面或查询 façade：`Inspect().Environment(ctx)` 先只读校验构造默认 append 的有效性/能力，再调用同一 configured Driver probe；不创建文件，不付费，不执行 Run。ConfigSchema 不新增 append 配置字段，ProfileState 不列入 append 资源。调用方若保留 `d := claude.Driver(cfg)` 可读 `d.Descriptor().SystemPrompt.Append`，无需根包再暴露 SPI。配置仍通过当前 bound Driver 保存的真实快照传入 probe，禁止丢配置。

## 5. Thread、checkpoint 与单 writer（T10 + T14–T16）

`Fingerprint(text)` 对非空有效 UTF-8 文本返回原字节 SHA-256 的小写 64 位 hex；空串返回 `""`。不带路径、进程地址、随机值、时间、trim 或平台换行变换。

T10 保留 `threadInvocationFingerprint` 的**所有原维度**。先按现有实现取得 base fingerprint；非空 append 时返回 `engine.StableHash("adaptor/thread-append-system-prompt/v1", base, systemprompt.Fingerprint(text))`，空串仍返回 base。这既区分内容变化/清除，又保持从未设置 append 的旧健康记录兼容，不让所有旧 Thread 无谓失效。不得把原模型、identity、resolved workspace、profile/Tools/skills/Instructions/MCP/service 指纹替换为一个提示哈希。

行为表：

| 情况 | 唯一允许行为 |
|---|---|
| 相同文本/配置/identity/资源 | 正常续接；常驻可复用 |
| 变化或从非空清除，ResumeOnly | `ErrThreadIncompatible`，保留旧记录；不启动 replacement |
| 变化或清除，默认 continue-or-start | 在既有 lease/原子 Finalize 路径安全新建；仅新健康 checkpoint 保存成功后归档旧状态 |
| Fork 的 append 或其它兼容维度不匹配 | 拒绝，不改父记录、不创建孤儿子记录 |
| provider 拒绝 resume | 按既有合同至多一次安全 fallback；绝不重放可能已交付的 prompt |

三个支持 Driver 的 `persistentSpec` 增加私有 `appendSystemPrompt string`；`sig()` 包含内容 Fingerprint，不能仅包含文件名或长度。`spawnArgs()` / Codex `openOptions()` 使用同一已解析文本。单 writer 顺序仍为旧 writer 有界退出 → 释放旧启动载体 → 准备/校验 replacement → 启动，适用于常驻、临时 native schema、WithSpawn、prewarm、record 重绑、Close。先改变 sig 再启动不能代替 core 的 Thread 兼容判断。

各 provider 的健康 checkpoint 在既有 Data/SessionCodec 原样传递的扩展 key `append_system_prompt_fingerprint` 中记录相同内容 hash，空串可省略；provider 私有常量定义该 key，不新增根或 SPI 名字。各 `validate*SessionGuard` 比较该 key（缺失按空），防止第三方直接 Driver.Run 绕过 core 时用旧 checkpoint 更换追加文本。此 key 不写进 profile manifest；只在正式成功且结构化校验成功后产生有效 checkpoint。Codex 必须覆盖 exec 和 app-server 两条路径及 start/resume/fork。

## 6. 原生参数及空值（T14–T17）

| Driver/transport | `AppendSystemPrompt == "甲\n\"乙\""` 的目标映射 | 空值 |
|---|---|---|
| Claude batch/双向/常驻 spawn/prewarm | 独立 argv `--append-system-prompt-file`, `<owned file>`；文件为文本原字节 | 不物化、不发 flag |
| CodeBuddy batch/control/常驻 spawn | 独立 argv `--append-system-prompt`, `甲\n"乙"`（真实换行，不是字面反斜线 n） | 不发 flag |
| Codex exec/start 或 exec resume | 独立 argv `-c`, `developer_instructions="甲\n\"乙\""`（TOML 编码）；resume selector 保持现有位置 | 不发 SDK override |
| Codex app-server start/resume/fork | JSON 属性 `developerInstructions`，值为原字符串 | 不发送该可选属性 |
| Cursor print | 非空以 `unsupported_driver` 拒绝，零子进程/零物化 | 原有行为 |

T16 在手写 `appserver.Options` 加 `AppendSystemPrompt string`；在 `ThreadStartParams`、`ThreadResumeParams`、`ThreadForkParams` 三者各加 `DeveloperInstructions string`，JSON tag 精确为 `json:"developerInstructions,omitempty"`。每条 thread handshake 均赋 `opts.AppendSystemPrompt`，保持原有 CWD/model/serviceTier/sandbox/approval/fork 字段。`TurnStartParams.Input` 仍只包含原 prompt；不改 `baseInstructions`、`instructions`，不把内容塞进 `Extras`。

对应 wire fixture（现有其它已解析字段按原合同保留）：

```json
{"id":2,"method":"thread/start","params":{"developerInstructions":"甲\n\"乙\""}}
{"id":3,"method":"thread/resume","params":{"threadId":"parent-c04","developerInstructions":"甲\n\"乙\""}}
{"id":4,"method":"thread/fork","params":{"threadId":"parent-c04","developerInstructions":"甲\n\"乙\""}}
```

fresh/WithSpawn/persistent reconnect 均验证上述赋值；fork 返回新 ID，父 ID 不得写入子 checkpoint。显式清除不能以“omit 字段但仍 resume 带旧追加内容的 session”冒充清除，必须先通过 §5 的兼容校验或新会话路径。

## 7. 编码、文件所有权和长度（T10 helper，provider 调用）

`internal/systemprompt` 是私有纯机制包，禁止解释 provider stdout、工具名、checkpoint 或 Event。冻结供 provider 使用的内部接口：

```go
const MaxInlineBytes = 32 << 10
func Validate(driverType, text string) error
func Fingerprint(text string) string
func ValidateInline(driverType, text string) error
func ValidateCommandLine(driverType, command string, args []string, goos string) error
func TOMLString(text string) (string, error)
func Materialize(ctx context.Context, text string) (*File, error)
type File struct { /* 私有字段 */ }
func (f *File) Path() string
func (f *File) Fingerprint() string
func (f *File) Verify(ctx context.Context) error
func (f *File) Close() error
```

Validate 先拒绝非 UTF-8，再拒绝 NUL；随后 core 才校验非空请求的 capability，保证混合无效输入的错误次序稳定。不改变合法字符串。TOMLString 使用已有 `github.com/pelletier/go-toml/v2` 的 encoder 得到单个合法 TOML string value；通过该库 decode round-trip 校验换行、中文、引号、反斜线、tab、CR、控制字符、真实 U+FFFD。不得用遍历 rune 把非法 UTF-8 偷换为 U+FFFD；不增加顶层 require。

**Claude 文件：** 每次实际 spawn 才 Materialize；创建 `os.TempDir()` 下随机 SDK 私有直属目录 `agent-adaptor-append-*`，0700；文件以内容 hash 命名，0600。目录和文件都由本次 File handle 唯一拥有，既不在 provider profile，也不在用户工作目录。不采用全局持久缓存，无跨 Agent/进程共享文件，因此不需要另一个跨进程锁。拥有证明是当前 handle 保存的目录/文件身份，不能凭可伪造文件名就接管或删除其它目录。

创建临时文件 → 写入完整原字节 → Sync/Close 成功 → 同目录原子发布 → Verify。权限、类型、链接及内容校验失败都在 prompt 交付前显式失败。Verify 比较完整 SHA-256、实际字节、regular file 身份、当前权限；对目录/文件的 symlink 或 Windows reparse point、同长度篡改、文件替换及不受控路径拒绝，不使用 `Stat.Size == len(text)` 缓存命中。使用目录约束打开/路径身份检查保证校验及删除不越出 owned root；安全边界不声称能防止同 UID 在校验之后任意写进程内存或拥有目录。

空文本 Materialize 返回 `(nil,nil)`，nil handle 的 Path/Fingerprint 返回空串、Verify/Close 成功。File.Close 幂等；仅删除该 handle 的文件及其私有目录，失败可观察并可重试，不递归删除任何外部 profile。创建失败/取消清理已创建载体；Close/cleanup 的错误按既有主因合并而非覆盖。

一次性进程从 spawn 前至进程退出持有 File；常驻 liveProcess 从实际 spawn 至有界退出持有 File，不能在本轮 Run 返回时删掉仍被 writer 持有的载体。复用既有进程时无需重新物化；sig 由文本 hash 决定，与随机路径无关。replacement、idle、取消、Agent.Close 的退出清理均释放；退出未完成不能提前释放。崩溃残留仅是 OS 临时目录残留，本能力不自动遍历删除其它进程遗留目录，也不许把它变成持久 profile 清理。

**长度与 Windows：** CodeBuddy 与 Codex exec 的原文本限制为 32768 UTF-8 bytes，超过返回 `inline_limit`，不可截断/切片或改用 user prompt。编码后的完整 argv 仍须校验，不能只检查原文。Claude file 和 Codex app-server 不套用 inline 上限；超出真实 provider/schema transport 上限时明确错误，不伪造交付成功。

`ValidateCommandLine` 接收与 `processx.PrepareCommand` 等价的最终 executable/argv，按目标 OS 校验：Windows native executable/PowerShell 的最终引用命令行包含结尾 NUL 不超过 32767 UTF-16 code units；cmd.exe 的最终展开命令行含 wrapper 不超过 8191 字符。必须计入 executable、空格、引号和反斜线转义放大；raw 32 KiB 小于阈值并不保证 Windows 可用。`goos` 是内部测试 seam，生产传 `runtime.GOOS`。

当前 Windows `.cmd/.bat` 经 `cmd.exe /d /s /c call` 运行，有 shell 再解释；本项不修改共享 processx 的既有启动语义。对内联文本含 CR/LF、`"`、`%`、`!`、`^`、`&`、`|`、`<`、`>`、`(`、`)` 的 cmd shim 以 `unsafe_shell_argument` 在启动前拒绝；不得尝试用普通 Windows argv quoting 声称能够无损越过二次 cmd 解析。原生 executable/可证明无损的 PowerShell 路径仍必须通过中文/换行/引号 round-trip；原生 Windows 验证由 T26 执行，跨编译不算。

**冲突：** Claude/CodeBuddy 的 ExtraArgs 一律拒绝 `--system-prompt`、`--system-prompt-file`、`--append-system-prompt`、`--append-system-prompt-file` 的 detached/`=` 形式（即使此次 append 为空，防止 hidden default 绕过清除）。Codex 拒绝 `-c`/`--config`（含 attached/`=`）中 TOML key 归一化后等于 `developer_instructions`、`instructions`、`base_instructions`、`model_instructions_file`、`experimental_instructions_file` 的 override；解析 quoted key/空白，不能只 HasPrefix。解析无效的 config override 以现有 InvalidDriverConfig 明确失败，不能吞掉。保留无关 ExtraArgs，不把任何替换提示参数设为新公共模式。所有拒绝先于物化、writer 启动和 prompt 交付；配置捕获/Inspect 保持同一决定。

追加文本不可新增进入 metadata、notice、tool/capability/todo、A2A wire、recorder 索引。CodeBuddy/Codex exec 的 OS argv 可见性是选定原生 transport 的限制，要在使用文档说明；SDK 不额外复制到事件诊断。provider 实际写出的 Raw 仍按完整 Raw 合同保留，不能为此偷偷截断审计输出。

## 8. 文件分配、golden 与文档门禁

| Owner | 必须交付的精确范围 |
|---|---|
| T31（G00 修订任务包，B01） | `driver/driver.go` 的 HITL 类型/字段及 godoc；`internal/engine/structured.go` 的算法；其精确协商合同测试；必要 `wiring.go` 协商；`adaptertest/doc.go`、`adaptertest/verify.go`、`adaptertest/testdata/driver_api.golden` 的 SPI 蕴含与 AST。G00 必须给这些路径明确 ownership |
| T07（B02） | 仅 `claude/` 下 descriptor/native 参数/parser/fixture/test 与局部 godoc；消费 G01 已合入矩阵及 T01 stdin；不改 SPI、root 或 engine |
| T10（B03） | `options.go`、`wiring.go`、`invocation.go`、`inspect.go`、`errors.go`；`driver/driver.go`、`driver/run.go`、`driver/config.go`；`internal/systemprompt/`；`alignment_prompt_contract_test.go`、scope/inspect 测试；root/SPI golden。helper、root、SPI 一次交付，provider 不依赖同批 peer |
| T14（B04） | `claude/` native 文件、进程持有/清理、signature/checkpoint guard、append fixtures、TestAlignmentLive* |
| T15（B04） | `codebuddy/` batch/control argv、长度/平台/冲突、signature/checkpoint guard、TestAlignmentLive* |
| T16（B04） | `codex/driver.go`、`run_streaming.go`、`persistent.go`、`appserver/run.go`、`appserver/union.go` 及目录内 tests/godoc；两 transport、三 handshake、signature/checkpoint guard、TestAlignmentLive* |
| T17（B04） | `cursor/` false capability、直接 Request fail-closed、空串兼容、无付费拒绝证明及 TestAlignmentLive* |
| 每批 gate / T24 | 合并本批 documentation.md 到使用文档、README 翻译、CHANGELOG；T24 最终一致性，不承担前批欠账 |

本基线 root `func With*` 实际 26 个（含 `WithEventMeta`）；W11 **+1 → 27**，不是继续声称 26。新增 root AST 为 WithAppendSystemPrompt、RunSettings.SetAppendSystemPrompt、error var/type alias；新增 SPI AST 为 SystemPromptCapability、Descriptor.SystemPrompt、Request.AppendSystemPrompt、error var/type/methods，以及 T31 的 HITL 类型/字段。没有其它 root 能力类型、控制面或 config 字段。先审查精确 AST 差异，再更新两份 golden；禁用自动全量更新掩盖其它漂移。

`codex/appserver/union.go` 和 `run.go` 是手写映射，可在 owner 目录修改。`generated.go` 和 `schema/**/*.json` 本项预期零 diff；需要升级时仅通过官方 `codex app-server generate-json-schema --out codex/appserver/schema/` 然后 `go generate ./codex/appserver/...`，记录 CLI 版本及 schema hash，另经 gate 审核，不手改。

## 9. 必需测试与证据分层

| 层/owner | 非零必需场景 |
|---|---|
| T31 | nil 旧矩阵、显式全 false、逐 Kind、重叠 Ask、native 优先/prompt fallback/均无支持；policy gate；Run/Stream 相同 resolved 值；rich 需求不可被 batch 消音 |
| T07 | Question/PlanReview + native：Approve/Answer/Deny/timeout/cancel；Permission + schema 明确 prompt fallback；普通 Permission 无 schema 保持；result-only 收尾；非法结构化输出；Run/Stream/Thread/WithSpawn 输出逐字段相同 |
| T10 | defaults、多个 call 覆盖、空串清除、纯空白、原 prompt 不变；Inspect 同配置且零物化；nil capability、无效文本零 Driver/资源/lease；Thread 无提示历史兼容、变化拒绝/安全重建/Fork；File 安全和编码 |
| T14–T16 | 正式 argv/RPC 逐字段；start/resume/fork；中文/引号/TOML/控制字符/超长/Windows shim；同内容复用与变化先停后起；prewarm 与 WithSpawn；篡改/cleanup；失败不污染 checkpoint |
| T17 | Cursor 非空零启动；空清除可执行旧路径；不切 ACP、不映射 Instructions |
| T20–T23 | 跨层严格 Thread/单 writer、HITL/error/partial Result、root/SPI conformance 与普通 CI 双门 |
| T26–T30 | 同 G05 SHA 的原生 Windows、四 provider live；四个 TestAlignmentLive* 入口须实际选中且明确版本/认证/profile/transport |

本 C04 仅交付合同，validation 是任务包静态校验；没有 Go 行为改动、没有执行 provider live，也不把上述 fixture 当已执行的行为测试。后续 live 证据必须记录目标 SHA、OS/Go、CLI 完整版本、profile 隔离、argv/RPC/schema 哈希、实际测试数量/退出码及已脱敏日志。用本轮随机不可自然猜中的响应标记做有 append/无 append 对照；仅捕获参数不证明模型收到。Claude native/HITL 还必须观察真实 control_request、应答、terminal structured_output 来自同一执行；CodeBuddy 未认证/401 记录 blocked，不记 passed。目标不支持的 probe 说明理由，不能把必需支持场景 skip 掉。

## 10. 可追溯依据与不采用的历史行为

- 固定目标代码：`wiring.go`、`invocation.go`、`driver/{driver,run,config,structured_errors}.go`、`claude/{driver,parser,persistent,structured_output}.go`、`codebuddy/{exec_args,persistent}.go`、`codex/{driver,persistent,run_streaming}.go`、`codex/appserver/{run,union,generate}.go`、三份 Thread params schema、`internal/processx/process.go`。
- 固定 internal：`4261914` 的双向 schema argv；`8ffe22a` 的 native 通道及实现差异；`e2f0620:docs/workstream-system-prompt.md` 仅作历史证据。历史记录版本 Claude 2.1.159、CodeBuddy 2.137.1、Codex 0.146.0、Cursor 2026.07.23-e383d2b **不是 C04 当前验证版本**，其 live PASS/SKIP 不可复制为本仓库证据。
- 2026-09-07 阅读的 [Claude CLI 正式参考](https://code.claude.com/docs/en/cli-reference) 确认 append-file 和 JSON schema/stream-json 参数；组合可运行性仍由 T27 live 证明。
- [CodeBuddy CLI 正式参考](https://www.codebuddy.ai/docs/cli/cli-reference) 检索内容列出 inline append；完整页面抓取失败，故只作为辅助定位，正式版本 help/实际协议由 T15/T28 补齐，不由搜索摘要判定实时能力。
- [OpenAI 配置参考](https://learn.chatgpt.com/docs/config-file/config-reference) 确认 `developer_instructions`；[OpenAI app-server 文档](https://developers.openai.com/codex/app-server/) 与本地官方生成 schema 支撑 thread 方法映射。正式 schema 已有字段，不需要推测或手工改生成物。

明确不采用：旧 SDK/binding/Admin、双默认选项、公开 schema mode、TrimSpace 提示内容、append 只进 process sig 不进 Thread compatibility、size-only 全局缓存、永久缓存无清理归属、漏传 Codex resume/fork、Cursor 降级 user prompt、native Permission 推断支持、Windows raw-byte 长度等同命令行长度、旧 live 声明当本次证据。没有新增依赖；TOML 复用已维护且已存在的 go-toml/v2，私有机制与 provider 协议边界分离。
