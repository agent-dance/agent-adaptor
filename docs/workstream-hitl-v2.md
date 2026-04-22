# Workstream: HITL v2 设计（破坏性重构 · host-intent policy）

本文件是 [`docs/workstream-hitl.md`](./workstream-hitl.md) 的配对设计文档。那份是一手调查资料（现状、证据、根因），本文件只做两件事：

1. 在 **SDK 职责边界内**，给"人在回路"定义一份 host-intent 的单一维度合同（去 vendor 化）
2. 对不同调用场景（脚本化、同进程 UI、远程 UI）**明确宿主怎么用**，代码级清晰

对 `claude` / `codex` / `cursor` 三个内置 adapter，本期只落地 `claude`；`codex` / `cursor` 给出方案与 vendor 翻译表，暂不实施。

**本期接受 break 现有公共 API**：`RunPolicy.Approvals` / `ApprovalLevel` / `RunPolicy.Trust` / `TrustLevel` / 全部相关预设都将被替换。迁移指南见 §7。

## 0. 命名约定

经 review 敲定的新符号按**三层**命名，每层关心一件事，前缀即表达其层次：

| 用途 | 名字 | 备注 |
|---|---|---|
| **策略/声明层（`HumanDecision*`）** | | 描述"该决策怎么被决定 / 失败了如何归因" |
| 策略字段 | `RunPolicy.HumanDecision` | `RunPolicy` 上唯一的 HITL 入口 |
| 策略容器 | `HumanDecisionPolicy` | 子结构，承载三类决策模式、超时、失败动作 |
| 三态类型（Permission / PlanReview） | `HumanDecisionMode` | 单维度枚举，表达宿主意图；**仅用于 Permission / PlanReview 两类**（二元批准） |
| 零值/继承 | `HumanDecisionUnset = ""` | 零值专用标签，宿主通常不显式写 |
| 询问人类 | `HumanDecisionAsk` | 走 HITL 通道（handler 或 channel）|
| Agent 自动通过 | `HumanDecisionAutoApprove` | 放权给 agent/CLI 自家 bypass/auto 路径 |
| Agent 自动拒绝 | `HumanDecisionAutoReject` | 显性拒绝 + emit Failure，不静默 |
| 两态类型（Question 专用） | `QuestionMode` | `HumanDecisionMode` 的真子集：**不含 AutoApprove**（Question 结果是 `DecisionAnswered`，没有"空答案合成"的合法语义；类型层堵死） |
| Question 零值 | `QuestionUnset = ""` | 零值：按 SDK 默认（= `QuestionAutoReject`） |
| Question 询问人类 | `QuestionAsk` | 走 HITL 通道 |
| Question 自动拒绝 | `QuestionAutoReject` | 显性拒绝，agent 拿不到答案走自家 fallback |
| 决策类别 | `HumanDecisionKind` | 三类 tag 的枚举类型 |
| 类别值 | `HumanDecisionPermission` / `HumanDecisionPlanReview` / `HumanDecisionQuestion` | A/B/C 三类 |
| 能力声明（Permission / PlanReview） | `HumanDecisionSupport{ Ask, AutoApprove, AutoReject, Retry bool }` | Descriptor 用 |
| 能力声明（Question） | `QuestionSupport{ Ask, AutoReject, Retry bool }` | 无 `AutoApprove` 字段——对应 `QuestionMode` 没有这个值 |
| 失败归因上下文 | `HumanDecisionFailure` | `RunResult.Failure.HumanDecision` 的结构化内容 |
| **决策层（`Decision*`）** | | 描述"一次决策的事件、处理、结果" |
| 决策请求（SDK → 宿主） | `DecisionRequest` | 携带 `Kind` / `Source` / `Payload` / `Choices` / `CreatedAt` / `Deadline` / `RetryAttempt` |
| 决策响应（宿主 → SDK） | `DecisionResponse` | 字段：`RequestID` / `Result` / `Choice` / `Answer` / `Text` |
| 选项条目 | `DecisionChoice` | 供 UI 渲染的候选 |
| adapter Sink 能力 | `DecisionCapableSink` | `EventSink` 扩展，允许 adapter 发起阻塞请求（adapter 只用统一 `DecisionRequest`） |
| 阻塞请求方法 | `sink.RequestDecision(ctx, req)` | adapter 侧 |
| 异步通道 | `handle.DecisionRequests() <-chan DecisionRequest` | 宿主侧；**只承接未挂 typed handler 的 Kind** |
| 回填方法（channel 模式） | `handle.ResolveDecision(id, resp DecisionResponse) error` | 宿主侧 |
| 跨类通用结果值类型 | `DecisionResult` | 用于 channel 回填、`HumanDecisionFailure.Decision` 归因 |
| 跨类通用结果值 | `DecisionApproved` / `DecisionRejected` / `DecisionAnswered` / `DecisionTimedOut` / `DecisionAborted` | — |
| **typed handler 层** | | per-Kind 强类型，Kind 间完全独立 |
| Permission 强类型 | `PermissionRequest` / `PermissionResponse` / `PermissionHandler` | Response.Result 类型是 `ApprovalResult` |
| PlanReview 强类型 | `PlanReviewRequest` / `PlanReviewResponse` / `PlanReviewHandler` | Response.Result 类型是 `ApprovalResult`（与 Permission 同构） |
| Question 强类型 | `QuestionRequest` / `QuestionResponse` / `QuestionHandler` | Response.Result 类型是 `QuestionResult`（值集合不同） |
| 批准型结果 | `ApprovalResult` + `ApprovalApproved` / `ApprovalRejected` | 供 Permission / PlanReview |
| 问答型结果 | `QuestionResult` + `QuestionAnswered` / `QuestionRejected` | 供 Question |
| 单次 Option | `WithPermissionHandler(h)` / `WithPlanReviewHandler(h)` / `WithQuestionHandler(h)` | 优先级最高，per-Kind |
| 绑定级 Option | `WithDefaultPermissionHandler(h)` / `WithDefaultPlanReviewHandler(h)` / `WithDefaultQuestionHandler(h)` | 绑定级默认，per-Kind |
| **失败/策略动作层（`Failure*`）** | | 描述"失败时 SDK 下一步做什么 / 失败码是什么" |
| 失败动作类型 | `FailureAction` | `OnTimeout` / `OnReject` 字段的值类型 |
| 失败动作值 | `FailureAbort` / `FailureContinue` / `FailureRetry` | 终止 / 让 agent 走自家 reject fallback / 重新触发决策 |
| 重试上限字段 | `HumanDecisionPolicy.MaxRetries` | `FailureRetry` 动作的上限 |
| 失败码类型 | `FailureCode` | `RunResult.Failure.Code` 的类型 |
| 失败码值 | `FailureReject` / `FailureTimeout` / `FailureAgentError` / …（见 §3.2） | — |
| **横切（沿用旧名，不 churn）** | | |
| Stream 事件 | `StreamHITLRequested` / `StreamHITLResolved` | 沿用现有常量，避免 churn |
| 预设 | `PolicyHostReview` / `PolicyReadOnlyReview` / `PolicyAutonomous` | 替代旧 `RunPolicyInteractive` 等 |
| 启动校验错误 | `ErrHumanDecisionModeUnsupported` | （原 `ErrDecisionHandlerAndChannelConflict` 删除——per-Kind 分派使得 handler 和 channel 不会对同一 Kind 冲突） |

**三层命名分层意图**：

- `HumanDecision*`（**策略/声明层**）：描述"这个决策该怎么被决定"，宿主用它来**声明意图**，失败时 SDK 也用这个家族的结构（`HumanDecisionFailure`）做归因。
- `Decision*`（**决策层**）：描述"一次决策的全过程"——跨类通用的事件对象（`DecisionRequest/Response`）、adapter sink（`DecisionCapableSink` / `RequestDecision` / `ResolveDecision`）、结果值（`DecisionResult`），以及 per-Kind 的 typed handler 家族（`PermissionHandler` / `PlanReviewHandler` / `QuestionHandler` 及其 `*Request` / `*Response`）。
- `Failure*`（**策略动作/失败码层**）：描述"失败时下一步动作"（`FailureAction`）和"失败归因的枚举码"（`FailureCode`）。

三个层次各司其职：宿主在 `RunPolicy` 上写 `HumanDecision`（**我打算怎么决策 / 超时或被拒时走哪个 `FailureAction`**），运行时 SDK 用 `Decision` 家族的通道推送事件并收回结果（**现在需要一个决策 / 我决定是什么**），失败落到 `RunResult.Failure` 上用 `FailureCode` + `HumanDecisionFailure` 归因。

> 为什么把事件和结果合并到 `Decision*` 一个前缀？因为这一层的所有成员（`Request`/`Response`/`Handler`/`Sink`/`Choice`/`Result`）都只服务"决策流"这一件事，用"交互"等更宽泛的词反而让读者多建一张认知地图。统一 `Decision*` 后，宿主读一眼就知道"这是决策域的事件对象"。
>
> 为什么有 `HumanDecisionMode` 和 `QuestionMode` 两个模式类型？因为 Permission / PlanReview 和 Question 的**正常结果值**本就不同——前两者是 `DecisionApproved`（二元批准），Question 是 `DecisionAnswered`（结构化答案）。"自动通过一个问题"缺少合法的 `DecisionResult` 可合成，所以 `QuestionMode` 的值集合是 `HumanDecisionMode` 的真子集（少一个 `AutoApprove`）。我们只在**真正不均质**的地方拆类型，Permission 和 PlanReview 因值集合完全相同继续共享。这样宿主 IDE 在写 `Question:` 时自动补全根本不会出现 `HumanDecisionAutoApprove`，错误在编译期被堵死。
>
> typed handler 层做**同样的拆分**：同步 handler 拆成 `PermissionHandler` / `PlanReviewHandler` / `QuestionHandler` 三个强类型签名，各带对应的 `*Request` / `*Response`（Permission/PlanReview 共享 `ApprovalResult`，Question 独立用 `QuestionResult`）。好处：宿主不再被迫 `switch on Kind`，可**按 Kind 选择性挂载**；Payload 强类型；Result 按类收窄（Question 写不出 Approved）。adapter 侧仍只看统一 `DecisionRequest/Response`，分派由 SDK 内部完成；异步 channel 模式（`DecisionRequests()`）也保留统一类型，因为服务化场景里宿主主要是"转发到 UI"而非直接决策，弱类型 + Kind switch 更实用——这个不对称是**被语义差异驱动**的，不是遗漏。
>
> 设计选择：`rejected` 和 `timedout` 在 `DecisionResult` 和 `FailureCode` 都**保持并列**，不引入统一的 "Denial" 抽象——两者在运维语义上不同（用户明确拒绝 vs 系统 + 用户协作失败），宿主分别配 `OnReject` / `OnTimeout` 即可。

## 1. 范围锚点

- 现有 HITL v1 合同：[`docs/streaming-adapter-contract.md`](./streaming-adapter-contract.md) §2.5（audit-only / auto-deny）
- 现有 policy：[`docs/run-policy.md`](./run-policy.md)（`Approvals ∈ {ask, auto, off}` + `Trust ∈ {ask, auto, deny}`）
- 现有执行面：`api.go` `RunHandle` / `EventSink`，`runner.go::Start / dualSink`
- 现有桥接：`pkg/bridges/agui/bridge.go`（`StreamHITLRequested → CustomEvent`）

## 2. 结论先行

1. **去 vendor 化**：`Approvals` / `Trust` 都是"CLI flag 形状反推出来的字段"，宿主不该看见 vendor 词汇。两者合并为 `HumanDecision`——单维度、`HumanDecisionPolicy` 承载三类人在回路。
2. **HITL 不新开执行入口**：继续复用 `Runner.Run / Start` + `RunHandle`，是第三条可选通道（已有 `Events()`、`StreamEvents()`）。符合 `AGENTS.md` §2.1 "只有一套执行语义"。
3. **宿主三种接入模式**，按场景自选，语义等价：
   - **模式 A：声明式 policy**（无 UI、脚本、CI）——所有字段写 `HumanDecisionAutoApprove` 或 `HumanDecisionAutoReject`
   - **模式 B：同步 handler 注入**（CLI/TUI、同进程应用、单测）——想同步参与的 Kind 写 `Ask` + 挂对应 typed handler（`WithPermissionHandler` / `WithPlanReviewHandler` / `WithQuestionHandler`，可按 Kind 独立挂）
   - **模式 C：异步 channel 回填**（远程 UI、SSE/WebSocket）——任一字段写 `HumanDecisionAsk` + 消费 `handle.DecisionRequests()`
4. **Decision 是三类均质事件的统一模型**：`permission` / `plan_review` / `question`。adapter 按白名单 + schema 识别；未识别的 tool_use 仍走普通 `tool_call.*`。
5. **SDK 只做通道，不做 UI/传输**。不内置 HTTP、不序列化 UI schema。bridge 层（`pkg/bridges/agui`）负责把请求上浮到 AG-UI / CopilotKit。
6. **break 既有 API**：`Approvals` / `Trust` / `ApprovalLevel` / `TrustLevel` 及全部预设（`RunPolicyInteractive` / `RunPolicyReadOnly` / `RunPolicyTrusted`）被删除。迁移表见 §7。
7. **Claude 先修"显性失败"，再修"双向回填"**：Phase 1 在 claude adapter 里识别 `ExitPlanMode` reject 并 emit `RunFailure{Code: FailureReject, HumanDecision: {Kind: HumanDecisionPlanReview, Source: "claude.exit_plan_mode", Decision: DecisionRejected}}` + `StreamHITLRequested/Resolved`，解决用户感知的"磁盘零变更但 UI 显示已完成"；双向回填留给 Phase 3（依赖 CLI 层方案确认）。

## 3. 统一抽象：`HumanDecision`

### 3.1 三类事件的共性

对照 [`docs/workstream-hitl.md`](./workstream-hitl.md) §8 的分类，A/B/C 三类都有相同的形状：

- 携带**提示文本**（给人看）+ **结构化 payload**（给程序看）
- 期望一个**决策**：同意 / 拒绝 / 给出结构化答案
- 允许**超时兜底**
- 需要一个**稳定 ID**关联请求与响应

这决定了它们可以塞进同一个事件模型，不必分三条 `StreamKind`。分类信息放在 `Kind` 字段里。

### 3.2 类型定义（新文件 `decision_types.go`）

```go
package agentadaptor

import "time"

// HumanDecisionKind 标明人在回路事件的语义类别。
type HumanDecisionKind string

const (
    HumanDecisionPermission HumanDecisionKind = "permission"  // A: 前置权限（file write / shell exec / MCP）
    HumanDecisionPlanReview HumanDecisionKind = "plan_review" // B: plan 审批（claude ExitPlanMode）
    HumanDecisionQuestion   HumanDecisionKind = "question"    // C: 结构化澄清（claude AskUserQuestion）
)

// HumanDecisionMode 表示宿主对 Permission / PlanReview 两类"二元批准型"决策的意图。
//
// 语义分层：
//   - Ask         → 走 HITL 通道问人
//   - AutoApprove → agent/CLI 自家 bypass/auto 路径自动通过（不经宿主）
//   - AutoReject  → 显性拒绝，emit Failure，不静默
//   - Unset ("")  → 继承 SDK 保守默认（见 §3.7）
//
// 为什么 Question 不共用这个类型？
// Question 的正常结果是 DecisionAnswered（结构化 payload），不是 DecisionApproved；
// "自动通过一个问题"在物理上没有合法的结果值可合成。因此 Question 使用独立的
// QuestionMode 类型，从**类型层**堵死 "Question=AutoApprove" 的反模式。
type HumanDecisionMode string

const (
    HumanDecisionUnset       HumanDecisionMode = ""             // 零值：按 SDK 保守默认。宿主通常不显式写。
    HumanDecisionAsk         HumanDecisionMode = "ask"          // 问人类（走 handler 或 channel）
    HumanDecisionAutoApprove HumanDecisionMode = "auto_approve" // agent 自动通过
    HumanDecisionAutoReject  HumanDecisionMode = "auto_reject"  // agent 自动拒绝 + Failure
)

// QuestionMode 表示宿主对 Question 类决策的意图。值集合是 HumanDecisionMode 的真子集
// ——不含 AutoApprove（见上面 HumanDecisionMode 的 godoc）。
type QuestionMode string

const (
    QuestionUnset      QuestionMode = ""            // 零值：按 SDK 保守默认（= QuestionAutoReject）
    QuestionAsk        QuestionMode = "ask"         // 问人类（走 handler 或 channel）
    QuestionAutoReject QuestionMode = "auto_reject" // 没有 handler / 不想被问时显性拒，emit Failure
)

// HumanDecisionPolicy 是 RunPolicy.HumanDecision 的子结构。零值表示全继承 SDK 默认。
type HumanDecisionPolicy struct {
    Permission HumanDecisionMode // 二元批准（tool 允许/拒绝），允许全部三个 HumanDecisionMode 值
    PlanReview HumanDecisionMode // 二元批准（plan 允许/拒绝），允许全部三个 HumanDecisionMode 值
    Question   QuestionMode      // 结构化问答，类型层只接受 Ask / AutoReject

    // Timeout 是 SDK 等待宿主回填的最大时间（仅在字段值为 Ask 时生效：
    // HumanDecisionAsk 或 QuestionAsk）。
    //   0   → 采用 SDK 默认（30s）
    //   < 0 → 永不超时（只有 ctx 取消能中断）
    Timeout time.Duration

    // OnTimeout 是决策 Deadline 到期后 SDK 采取的动作。
    // 合法值：FailureAbort（默认）/ FailureContinue / FailureRetry。
    // 注意：这里**不**允许"超时自动放行"——超时是系统信号，不是宿主决定；要放行
    // 请把 Permission / PlanReview 直接设成 HumanDecisionAutoApprove（Question 没有
    // AutoApprove，按设计如此）。
    OnTimeout FailureAction

    // OnReject 是决策结果为 DecisionRejected（包括 HumanDecisionAutoReject 合成
    // 的 rejected、handler 返回的 rejected、异步回填的 rejected）后的动作。
    // 合法值：FailureAbort（默认）/ FailureContinue / FailureRetry。
    OnReject FailureAction

    // MaxRetries 是 FailureRetry 动作的上限。
    //   0   → 采用 SDK 默认（3）
    //   < 0 → 视为非法，mergeRunPolicy 报错
    // 耗尽后自动降级为 FailureAbort。
    MaxRetries int
}

// DecisionRequest 由 adapter 构造，由 SDK 统一打 ID / deadline，上浮到宿主。
type DecisionRequest struct {
    RequestID string    // SDK 分配：runID + "-itx-" + seq
    RunID     string
    ThreadID  string
    Kind      HumanDecisionKind

    // Source 是 adapter-specific 起因标签，用于观测与路由，例如：
    //   "claude.exit_plan_mode" / "claude.ask_user_question"
    //   "claude.permission_request.bash" / "claude.permission_request.write"
    //   "codex.requestApproval.sandbox_escape"
    Source string

    ToolCallID string // 关联的 tool_use_id（若有），便于与 stream tool_call.* 对齐

    Prompt  string            // 人类可读摘要（renderable）
    Payload map[string]any    // 原始 payload（plan 文本、命令、文件路径、schema）
    Choices []DecisionChoice

    DefaultDecision DecisionResult // adapter 建议的兜底（供 handler 参考）
    CreatedAt       time.Time      // SDK 分配请求 ID 时的墙钟时间；宿主做通知去重/节流时用这个
    Deadline        time.Time      // SDK 基于 Policy.Timeout 计算后填充（= CreatedAt + Policy.Timeout）

    // RetryAttempt 是本次决策的重试序号，从 0 开始。0 表示首次；>0 表示由
    // HumanDecisionPolicy.OnTimeout / OnReject = FailureRetry 触发的重试。
    // handler / 异步 UI 可据此调整文案（如提示"第 2 次询问"）。
    // RequestID 在每次重试时会重新分配，但同一决策的多次尝试共享 Source +
    // ToolCallID，供宿主做跨重试关联。
    RetryAttempt int
}

type DecisionChoice struct {
    Key         string
    Label       string
    Description string
}

// DecisionResult 是宿主回填的决策值。
type DecisionResult string

const (
    DecisionApproved DecisionResult = "approved"
    DecisionRejected DecisionResult = "rejected"
    DecisionAnswered DecisionResult = "answered"  // Question 的结构化回答
    DecisionTimedOut DecisionResult = "timed_out" // 超过 Deadline
    DecisionAborted  DecisionResult = "aborted"   // 宿主主动放弃（context.Cancel）
)

// DecisionResponse 由宿主构造，回填给 adapter 推进任务。
//
// 用途：**仅用于 channel 模式**（`RunHandle.DecisionRequests()` + `ResolveDecision`）。
// 同步 handler 模式请使用 typed Response（`PermissionResponse` / `PlanReviewResponse`
// / `QuestionResponse`）——那里有更收窄的 Result 值集合，编译期堵死非法组合。
type DecisionResponse struct {
    RequestID string
    Result    DecisionResult // 跨类通用：DecisionApproved / Rejected / Answered / ...（非法组合由 runtime 校验）
    Choice    string         // 命中的 DecisionChoice.Key
    Answer    map[string]any // 结构化回答（question 用，遵循 DecisionRequest.Payload 里的 schema）
    Text      string         // 给 provider 的自由文本（可选）
}

// -----------------------------------------------------------------------------
// Typed handler 层（per-Kind，强类型）
// -----------------------------------------------------------------------------
//
// 设计目的：同步 handler 场景下宿主直接做决策，强类型能在编译期堵死：
//   1. 写错 Payload 键名（`req.Plan` vs `req.Payload["plan"].(string)`）
//   2. 写错 Result 值（Question 返回 Approved 在类型层即不合法）
//   3. 漏处理 Kind（没挂的 Kind 走 policy 兜底或 channel；不再强制宿主 switch）
//
// adapter 侧仍用统一的 `DecisionRequest` / `DecisionResponse`（见 §3.4）——SDK
// runner 负责按 `DecisionRequest.Kind` 分发到对应的 typed handler 或 channel。

// decisionRequestBase 是三类 typed request 共用的基础字段。嵌入到每类请求。
type decisionRequestBase struct {
    RequestID    string
    RunID        string
    ThreadID     string
    Source       string    // adapter-specific 起因标签（同 DecisionRequest.Source）
    ToolCallID   string    // 关联 tool_use_id（若有）
    CreatedAt    time.Time
    Deadline     time.Time
    RetryAttempt int       // 第 N 次重试（0 = 首次）
}

// PermissionRequest 是 Permission 类（工具/命令前置授权）的强类型请求。
type PermissionRequest struct {
    decisionRequestBase
    Tool   string         // 工具名，如 "Bash" / "Write" / "mcp:slack.post"
    Prompt string         // 人类可读摘要
    Args   map[string]any // 原始参数（command / path / cwd 等 vendor-specific 字段）
}

// PlanReviewRequest 是 PlanReview 类的强类型请求。
type PlanReviewRequest struct {
    decisionRequestBase
    Prompt string         // 人类可读摘要（常用作卡片 title）
    Plan   string         // markdown 格式的 plan 文本
    Extra  map[string]any // vendor 专有扩展
}

// QuestionRequest 是 Question 类的强类型请求。
type QuestionRequest struct {
    decisionRequestBase
    Prompt  string
    Schema  map[string]any   // JSON Schema 或等价结构（描述 Answer 形状）
    Choices []DecisionChoice // 预设选项（若有）
}

// ApprovalResult 是 Permission / PlanReview 两类"二元批准型"决策的合法结果值。
// 这两类共享同一值集合（Approved/Rejected），所以合并到一个类型。
type ApprovalResult string

const (
    ApprovalApproved ApprovalResult = "approved"
    ApprovalRejected ApprovalResult = "rejected"
)

// QuestionResult 是 Question 类的合法结果值。值集合和 ApprovalResult 不同
// （Answered 替代 Approved），所以独立成类型。
type QuestionResult string

const (
    QuestionAnswered QuestionResult = "answered"
    QuestionRejected QuestionResult = "rejected"
)

// 注意：typed Response 里**不**暴露 DecisionTimedOut / DecisionAborted——
//   - 超时由 SDK 内部合成（handler 拿到 ctx cancel，返回值被丢弃）
//   - Aborted = handler 返回 error，SDK 按取消路径走
// 这两种系统态不是宿主能作为"决策值"主动回填的。

type PermissionResponse struct {
    RequestID string
    Result    ApprovalResult
    Text      string // 可选自由文本（audit / provider hint）
}

type PlanReviewResponse struct {
    RequestID string
    Result    ApprovalResult
    Text      string
}

type QuestionResponse struct {
    RequestID string
    Result    QuestionResult
    Choice    string         // 命中的 DecisionChoice.Key（若用了 Choices）
    Answer    map[string]any // 结构化回答（遵循 QuestionRequest.Schema）
    Text      string
}

// Handler 函数类型：每类一个签名，互不兼容。
type PermissionHandler func(ctx context.Context, req PermissionRequest) (PermissionResponse, error)
type PlanReviewHandler func(ctx context.Context, req PlanReviewRequest) (PlanReviewResponse, error)
type QuestionHandler   func(ctx context.Context, req QuestionRequest)   (QuestionResponse, error)

// -----------------------------------------------------------------------------
// 失败/策略动作层（Failure*）
// -----------------------------------------------------------------------------

// FailureAction 是 HumanDecisionPolicy.OnTimeout / OnReject 的值类型，描述 SDK
// 遇到对应失败信号时的下一步动作。
type FailureAction string

const (
    FailureActionUnset FailureAction = ""         // 零值 = 继承 SDK 默认（= FailureAbort）
    FailureAbort       FailureAction = "abort"    // 终止 run + emit RunResult.Failure（默认）
    FailureContinue    FailureAction = "continue" // 让 adapter 把 reject/timeout 作为 tool_result 回给 agent，run 继续
    FailureRetry       FailureAction = "retry"    // 重新发起同一次决策（受 MaxRetries 限制），耗尽后降级为 FailureAbort
)

// FailureCode 是 RunResult.Failure.Code 的枚举类型。集中声明避免宿主处理
// 失败时到处 match 字符串字面量。
type FailureCode string

const (
    FailureReject      FailureCode = "decision_rejected" // 决策 Rejected（含 AutoReject 合成）+ OnReject=FailureAbort
    FailureTimeout     FailureCode = "decision_timeout"  // 决策 Deadline 到期 + OnTimeout=FailureAbort
    FailureAgentError  FailureCode = "agent_error"          // adapter 自身错误（exit 非 0、协议破损等）
    FailureCancelled   FailureCode = "cancelled"            // ctx.Cancel / 宿主 Abort
    FailurePolicyError FailureCode = "policy_error"         // Policy 合并/校验失败（§3.7/§3.8 报错）
)

// HumanDecisionFailure 是当 run 因一次人类决策失败而终止时，对那次决策的
// 结构化归因。挂在 RunResult.Failure.HumanDecision 上。
type HumanDecisionFailure struct {
    Kind     HumanDecisionKind // permission / plan_review / question
    Source   string            // 同 DecisionRequest.Source
    Decision DecisionResult    // DecisionRejected / DecisionTimedOut
    Request  *DecisionRequest // 触发失败的那一次请求（含 Payload，便于渲染 / 溯源）
    Attempts int                 // 总共尝试了几次（含重试）
}

// RunFailure 是 RunResult.Failure 的当前形状（本文件只声明 HITL 相关新增字段，
// 其余字段沿用 run_types.go 既有定义）。
type RunFailure struct {
    Code     FailureCode
    Message  string
    Metadata map[string]any // 通用补充上下文（vendor stderr 片段等）

    // HumanDecision 在 Code ∈ {FailureReject, FailureTimeout} 时非 nil，
    // 其余失败码为 nil。宿主可据此做结构化 switch。
    HumanDecision *HumanDecisionFailure
}
```

### 3.3 `RunPolicy` 新形状

```go
type RunPolicy struct {
    Isolation     IsolationLevel
    WebSearch     FeatureLevel
    Browser       FeatureLevel
    HumanDecision HumanDecisionPolicy
    // 删除：Approvals ApprovalLevel
    // 删除：Trust TrustLevel
}
```

- `ApprovalLevel` / `TrustLevel` 及其所有常量一并删除
- `RunPolicyCapabilities` 形状也变（§3.8）

### 3.4 `EventSink` 扩展（adapter 侧）

```go
// 原有（保留）：
type EventSink interface {
    Emit(event RunEvent) error
    EmitStream(payload StreamPayload) error
}

// 新增能力：内置 dualSink 都实现它；自定义 sink 可不实现，adapter 按 policy 兜底。
type DecisionCapableSink interface {
    EventSink
    // RequestDecision 阻塞直到宿主回填、超时、或 ctx 取消。
    // 当 SDK 没有任何 decision 通道（字段=Ask 但既无 handler 又无 channel 消费者）时，
    // 立即按 Policy.OnTimeout 返回兜底，不阻塞。
    RequestDecision(ctx context.Context, req DecisionRequest) (DecisionResponse, error)
}
```

adapter 代码范式：

```go
if ic, ok := sink.(agentadaptor.DecisionCapableSink); ok {
    resp, err := ic.RequestDecision(ctx, req)
    // 根据 resp.Result 决定：回填 tool_result / 继续 / 放弃
} else {
    // 按 policy 兜底（通常 deny），emit StreamHITLRequested 做 audit
}
```

#### 3.4.1 设计意图

这里**不**往 `EventSink` 里直接加方法，而是拆成可选的扩展接口，有三条理由：

1. **向后兼容**：`EventSink` 已是 v1 公共合同，bridge / 自测 mock / 宿主自定义 sink 都实现它。直接加方法会破坏所有既有实现的编译。
2. **能力协商**：不是所有宿主都需要 HITL 能力（observer-only sink 天然没有"回填"概念）。用类型断言 `sink.(DecisionCapableSink)` 让 adapter 运行时探测，**有就走真实决策，没有就走降级路径**，两类宿主都跑得通。
3. **语义纯度**：`EventSink.Emit/EmitStream` 是非阻塞 + 单向发射；`RequestDecision` 是阻塞 + 双向请求响应。混在一个接口里 godoc 说不清。

Go 标准库同款套路：`io.Writer` → `io.WriterTo`、`http.ResponseWriter` → `http.Flusher/http.Hijacker`、`net.Conn` → `syscall.Conn`。

**为什么 `RequestDecision` 是阻塞语义**：

adapter 的调用语境是流中断——看到 `ExitPlanMode` tool_use 后必须拿到决策才能继续读下一个 stream-json 帧，否则和 CLI 的后续 `tool_result` / 新 event 就串位了。阻塞把"谁推动决策到达"**收敛到 SDK 一侧**：adapter 只管阻塞等 resp，SDK 内部负责把请求按 `req.Kind` 路由到对应的 typed handler（模式 B，见 §3.6）或 `DecisionRequests()` channel（模式 C），adapter 完全不感知这些分派细节——它只用统一的 `DecisionRequest` / `DecisionResponse`。

非阻塞的代价是每个 adapter 自己维护 `tool_use_id → 续读 callback` 映射，违反"SDK 收敛"原则。

`ctx` 参数承担三重职责：(1) 响应 `handle.Cancel()` 的取消链；(2) SDK 内部包 `WithTimeout(ctx, Policy.Timeout)` 的超时链；(3) 模式 B 的 handler 内部还可以自己叠 `WithTimeout`。

**为什么保留 `else` 降级路径 + audit 广播**：

`DecisionCapableSink` 不可用时，adapter 必须让宿主至少"知道发生过什么"，不能静默推进或静默死循环。`StreamHITLRequested` 作为广播通道兜底：

| 通道 | 能力 | 失败时的表现 |
|---|---|---|
| `RequestDecision`（阻塞，可选） | 真正控制 adapter 推进 | 降级：按 Policy 兜底（通常 `AutoReject`） |
| `StreamHITLRequested`（广播，必发） | 观测 / 日志 / UI 显示 | 不会失败（`EventSink` 是必选接口） |

两条腿的详细分工见 §3.10。

#### 3.4.2 `StreamPayload` 扩展：`Seq` 字段

为支撑 §4.3.1 的"UI 会话历史恢复"协议，给既有公共类型 `StreamPayload` 增加一个字段：

```go
type StreamPayload struct {
    Seq  uint64      // 新增：run 内部单调递增游标（从 0 开始）
    Kind StreamKind
    // ... 其他既有字段不变
}
```

**语义**：

- 每次 `EventSink.EmitStream` 时 SDK runner 自动分配 `Seq`，保证**同一 run 内严格单调递增**（无跳号、无重复）
- 跨 run 不保证全局单调——`(RunID, Seq)` 共同作为全局唯一事件坐标
- 跨 `FailureRetry` 合成的重试事件：同一个 run 的序列持续，不重置；run 结束后下一个 run 从 0 重新开始
- adapter 侧**不自行填写** `Seq`——填了也会被 runner 覆盖，保持分配权单点在 runner

**宿主用途**（规范见 §4.3.1）：

1. **持久化游标**：持久化层以 `(RunID, Seq)` 为主键，天然去重
2. **增量拉取**：`GET /session/events?run_id=...&after=<last_seq>` 从持久化层拉未同步事件
3. **SSE 断线重连**：SSE 响应每条 message 写 `id: <Seq>`，浏览器断线后 `Last-Event-ID` 头自动带回来，bridge 据此从持久化或实时流接续
4. **不依赖墙钟**：`CreatedAt` 在多 pod 写入时有时钟漂移，`Seq` 在单进程 runner 内无歧义

**兼容性**：零值 `Seq=0` 是合法值（run 的第一条事件）；旧消费者忽略 `Seq` 字段也能跑（只是丢失断线重连能力）。

### 3.5 `RunHandle` 扩展（宿主侧）

```go
type RunHandle interface {
    Events() <-chan RunEvent
    StreamEvents() <-chan StreamPayload
    RunID() string
    Wait(ctx context.Context) (RunResult, error)
    Cancel(ctx context.Context) error

    // 新增：
    // DecisionRequests 返回"需要宿主异步回包"的请求通道。只包含**没有挂 typed
    // handler**（见 §3.6）那些 Kind 的请求——挂了 PermissionHandler 的 run，
    // Permission 类请求不会出现在这里，只有 PlanReview / Question 可能出现。
    // channel 在 run 结束后 close，消费者可安全 `for range`。
    // 未消费的 channel 不会死锁：SDK 会在 Deadline 到期后按 Policy.OnTimeout 兜底。
    DecisionRequests() <-chan DecisionRequest

    // ResolveDecision 把宿主决策回填给 adapter（仅供 channel 模式使用；handler 模式
    // 直接返回 typed Response 即可）。返回错误当且仅当：
    //   - requestID 不存在（已超时 / 已被其他调用消费）
    //   - run 已结束
    //   - resp.Result 与 requestID 对应的 Kind 不兼容（如 Question 收到 Approved）
    ResolveDecision(requestID string, resp DecisionResponse) error
}
```

### 3.6 Option（绑定级 / 单次）

每类决策一对 Option（单次级优先级高于绑定级；类型定义见 §3.2）：

```go
// Permission
func WithPermissionHandler(h PermissionHandler) RunOption
func WithDefaultPermissionHandler(h PermissionHandler) AgentOption

// PlanReview
func WithPlanReviewHandler(h PlanReviewHandler) RunOption
func WithDefaultPlanReviewHandler(h PlanReviewHandler) AgentOption

// Question
func WithQuestionHandler(h QuestionHandler) RunOption
func WithDefaultQuestionHandler(h QuestionHandler) AgentOption
```

**为什么不提供一个统一 `WithDecisionHandler`？** 见 §4.2 的设计讨论——统一 handler 会强制宿主 switch on Kind，类型安全失真（Question 能写 Approved、Payload 弱类型）。拆成三个 typed handler 后：

- 宿主只挂自己关心的 Kind（比如只写 PlanReview 审批，permission/question 走 policy 默认）
- 每类 Payload 强类型（IDE 自动补全、编译期类型检查）
- 每类 Result 按类收窄（Question 不会出现 Approved）

**per-Kind 分发规则**（runner 启动时按每类 Kind 独立判定）：

| Kind 的 Mode | 是否挂了对应 typed handler | 是否有 channel 消费者 | 行为 |
|---|---|---|---|
| `Ask` | ✅ | — | 该 Kind 走 handler；**该 Kind 的 request 不会出现在 `DecisionRequests()` channel** |
| `Ask` | ❌ | ✅（`DecisionRequests()` 被读） | 该 Kind 走 channel，用 `DecisionResponse` 回填 |
| `Ask` | ❌ | ❌ | Deadline 到期按 `OnTimeout` 兜底 |
| `AutoApprove` / `AutoReject` / `QuestionAutoReject` | — | — | 不走人类通道；SDK 本地合成结果 |

**关键性质**：handler 和 channel 的互斥是 **per-Kind 的**，不是 per-run。宿主完全可以"PlanReview 挂 typed handler + Permission 走 channel 异步推 UI"——两条路径不冲突。因此之前设计的 `ErrDecisionHandlerAndChannelConflict` 不再需要（删除，见 §7.1）。

**启动前校验**（runner 层）：

- 若某 Kind `Mode=Ask` 但 adapter 的 support 矩阵 `Ask:false` → `ErrHumanDecisionModeUnsupported`（不变）
- 若某 Kind `Mode=Ask` 且未挂 handler → warn "该 Kind 会走 `DecisionRequests()` channel，请确认已消费"（降级：没消费也只会超时，不会死锁）

### 3.7 SDK 默认语义（`HumanDecisionUnset` 的翻译）

`HumanDecisionPolicy` 任何字段为零值（`HumanDecisionUnset` / `QuestionUnset` / 数值 0）时，SDK 按下表决议：

| 字段 | 默认 | 理由 |
|---|---|---|
| `Permission` | `HumanDecisionAsk` | 文件/shell 有副作用，保守默认"问人" |
| `PlanReview` | `HumanDecisionAsk` | plan 被静默 reject 是当前用户感知的核心痛点 |
| `Question` | `QuestionAutoReject` | 没 handler 时 agent 拿不到人类答案就应显性失败，而不是塞空答案让 agent 乱走；有 handler 时宿主自觉改 `QuestionAsk` |
| `Timeout` | `30 * time.Second` | 与人在终端回答的实际耐心接近 |
| `OnTimeout` | `FailureAbort` | 超时 = 显性失败，run 终止并 emit `RunResult.Failure{Code: FailureTimeout}` |
| `OnReject` | `FailureAbort` | 用户明确拒绝 = 显性失败，emit `RunResult.Failure{Code: FailureReject}` |
| `MaxRetries` | `3` | `FailureRetry` 动作的默认上限，耗尽后自动降级为 `FailureAbort` |

**关键**：`HumanDecisionPolicy{}`（全零值）直接跑的行为 = 宿主必须挂 handler 或 channel，否则 Permission + PlanReview 会在第一次触发时超时 → `OnTimeout=FailureAbort` → emit `Failure{Code: FailureTimeout, HumanDecision: …}`。这个"默认不危险"是有意的。

> **为什么 `OnTimeout` 的值类型不是 `HumanDecisionMode`？** 早期设计允许 `OnTimeout=HumanDecisionAutoApprove`（超时即自动放行），但在 `Permission` 这种副作用操作上等价于"无人值守默认通过"——这是 HITL 设计里最危险的反模式。新 API 通过类型选择从源头堵死了这条路径：要放行不经 HITL，请直接把该类别的 `Mode` 写成 `HumanDecisionAutoApprove`。

### 3.8 Descriptor 能力矩阵

```go
type RunPolicyCapabilities struct {
    Isolation bool
    WebSearch bool
    Browser   bool

    Permission HumanDecisionSupport // 值集合 = HumanDecisionMode（Ask/AutoApprove/AutoReject）
    PlanReview HumanDecisionSupport // 值集合 = HumanDecisionMode
    Question   QuestionSupport      // 值集合 = QuestionMode（Ask/AutoReject）
    // 删除：Approvals bool / Trust bool
}

// HumanDecisionSupport 描述 Permission / PlanReview 在该 adapter 上能支持哪些模式 / 动作。
// 宿主设置了某模式但 adapter 不支持时，SDK 启动前报 ErrHumanDecisionModeUnsupported。
type HumanDecisionSupport struct {
    Ask         bool // 能否把请求上浮到宿主通道
    AutoApprove bool // 能否放权给 agent / CLI
    AutoReject  bool // 能否显性拒绝（不让 agent 进入）
    Retry       bool // 能否真正再次触发同一次决策（支撑 FailureRetry）
}

// QuestionSupport 描述 Question 在该 adapter 上能支持哪些模式 / 动作。
// 故意不含 AutoApprove 字段——QuestionMode 值集合里就没有 AutoApprove，
// 类型系统已经堵死，能力层不需要再冗余声明。
type QuestionSupport struct {
    Ask        bool // 能否把请求上浮到宿主通道
    AutoReject bool // 能否显性拒绝（不让 agent 进入）
    Retry      bool // 能否真正再次触发同一次决策（支撑 FailureRetry）
}
```

本期三家 adapter 自填：

| adapter | `Permission` (`HumanDecisionSupport`) | `PlanReview` (`HumanDecisionSupport`) | `Question` (`QuestionSupport`) |
|---|---|---|---|
| claude | `{Ask:true, AutoApprove:true, AutoReject:true, Retry:false}` | `{Ask:true, AutoApprove:true, AutoReject:true, Retry:false}` | `{Ask:true, AutoReject:true, Retry:false}` |
| codex  | `{Ask:true, AutoApprove:true, AutoReject:true, Retry:true}` | `{Ask:false, AutoApprove:true, AutoReject:false, Retry:false}` | `{Ask:false, AutoReject:false, Retry:false}` |
| cursor | `{Ask:false, AutoApprove:true, AutoReject:false, Retry:false}` | `{Ask:false, AutoApprove:true, AutoReject:false, Retry:false}` | `{Ask:false, AutoReject:false, Retry:false}` |

规则：
- `Ask:true` 的字段才允许宿主写对应的 `Ask`（`HumanDecisionAsk` / `QuestionAsk`）；`AutoReject:true` 的字段才允许对应的 `AutoReject`；`AutoApprove` 几乎总是 true（所有 adapter 都有 bypass / auto 路径），Question 类没有这个值。
- `Retry:true` 的字段才允许 `OnTimeout=FailureRetry` / `OnReject=FailureRetry`。配了 Retry 但 adapter 不支持时，SDK 启动前 warn 并自动降级为 `FailureAbort`（不报致命错误——宿主的语义表达仍被尊重，只是 adapter 没能力兑现）。
- Claude Phase 1 所有类别 `Retry:false`：CLI 一旦对 `ExitPlanMode` / tool_use 做出决策就无法重新询问（`stream-json` 协议下没有重发机制）。Phase 3 双向通道若能做到"保持同一次 tool_use_id 等待新 permission_result"可改为 `true`。
- Codex / Cursor 的 `Question` 三个字段全部 `false`：两个 adapter 都不产生 `AskUserQuestion` 类事件，值集合为空即表达了"这个 adapter 不接受任何 Question 模式"。宿主写非 `QuestionUnset` 值时 SDK 启动前 warn 并 treat as Unset。

### 3.9 预设

替代旧的 `RunPolicyInteractive / RunPolicyReadOnly / RunPolicyTrusted`：

```go
var (
    // PolicyHostReview：文件和 plan 都问宿主；question 也交给宿主。
    // 适合"有 UI，宿主愿意参与全部决策"的场景。
    PolicyHostReview = RunPolicy{
        Isolation: IsolationWorkspaceWrite,
        HumanDecision: HumanDecisionPolicy{
            Permission: HumanDecisionAsk,
            PlanReview: HumanDecisionAsk,
            Question:   QuestionAsk,
        },
    }

    // PolicyReadOnlyReview：只读工作区 + 全部交给宿主审批。
    PolicyReadOnlyReview = RunPolicy{
        Isolation: IsolationReadOnly,
        HumanDecision: HumanDecisionPolicy{
            Permission: HumanDecisionAsk,
            PlanReview: HumanDecisionAsk,
            Question:   QuestionAsk,
        },
    }

    // PolicyAutonomous：放权给 agent，等价于旧 RunPolicyTrusted。仅限可信环境。
    // 注意 Question 类只能是 QuestionAutoReject——"放权给 agent" 在 Question 上的
    // 唯一合理翻译是"SDK 不提供人类答案，agent 需自行处理被拒的情形"；Question 类
    // 没有 AutoApprove（无法合成一个空 Answered），见 §3.2 QuestionMode godoc。
    PolicyAutonomous = RunPolicy{
        Isolation: IsolationUnrestricted,
        HumanDecision: HumanDecisionPolicy{
            Permission: HumanDecisionAutoApprove,
            PlanReview: HumanDecisionAutoApprove,
            Question:   QuestionAutoReject,
        },
    }
)
```

### 3.10 通道分层：知情权 vs 决策权

HITL 在 SDK 里走**两条独立的通道**，各自的职责、消费者数量、失败模式都不一样。这是整个设计里最容易被误解的一点，所以单开一节讲清。

| 通道 | 职责 | 消费者数量 | 失败时 |
|---|---|---|---|
| `StreamHITLRequested` / `Resolved`（经 `StreamEvents()`） | **知情权**：广播"发生了一次决策请求 / 决策刚落下" | N 个（审计、通知、metrics、UI push、IM 机器人、...） | 不失败（`EventSink` 必选） |
| Typed handler（`WithPermissionHandler` / `WithPlanReviewHandler` / `WithQuestionHandler`） | **决策权，per-Kind**：强类型地回填**挂载的那类** Kind | 每类 Kind **至多一个** | 未挂载的 Kind 落到 `DecisionRequests()` 兜底 |
| `DecisionRequests()` + `ResolveDecision()` | **决策权，Kind 残余**：承接所有**未挂 typed handler** 的 Kind | **有且仅有一个**消费 goroutine | 无消费者时按 `OnTimeout` 兜底 |

**adapter 侧**每次 HITL 发生时，两条路径**成对触发**（见 §3.4 代码范式）：

1. 先 `EmitStream(StreamHITLRequested{...})` 让所有旁观者都能看到
2. 再 `RequestDecision(ctx, DecisionRequest{...})` 阻塞等决策
3. SDK runner 根据 `DecisionRequest.Kind` 分派到 typed handler（若挂）或 `DecisionRequests()` channel
4. 决策到手后 `EmitStream(StreamHITLResolved{Decision: ...})` 让旁观者同步状态

**关键不对称**：决策权必须**排他**（per-Kind），知情权应当**广播**。理由：

- **决策权排他**：如果多个消费方都能对同一 Kind 回填，会出现双重回填、竞态、反悔不掉。所以"每类 Kind 最多一个 typed handler + 最多一个 channel 消费者且二者不重叠"是正确约束。
- **知情权广播**：审计、通知、metrics 各自独立演化，**不能让其中一个失败/慢拖累另一个**；也不能让"开启决策 UI 的那个 goroutine"垄断知情权——用户还可能在另一个房间收推送。

**对应的宿主部署模式**：

| 宿主形态 | 决策权消费者 | 知情权消费者 |
|---|---|---|
| CLI / TUI（模式 B） | 对想参与的 Kind 挂 typed handler；其余走 policy 默认 | 通常不用 |
| 脚本 / CI（模式 A） | 不挂 handler，所有字段 `AutoApprove` / `AutoReject` / `QuestionAutoReject` | 日志 / metrics |
| 服务化 + 远端 UI（模式 C） | 所有字段 `Ask`，**UI 推送 goroutine** 消费 `DecisionRequests()`（推给用户 → 收到 resolve POST → 回填） | **审计 + 通知 + metrics + IM** 等任意多个 |
| 混合（C + 局部 typed） | 对能同步处理的 Kind（例如 CI 侧固定的 PlanReview 审批机器人）挂 typed handler，其余走 channel | 同服务化场景 |

模式 C 典型 fan-out 范式见 §4.3。

**实现约束**：

- `StreamEvents()` / `DecisionRequests()` 本身都是 Go channel。channel 的 `for range` 天然是竞争式消费，多订阅者场景需要宿主在**一个** reader goroutine 里自行 fan-out（不能起 N 个 goroutine 各自 `for range`，那会导致事件被随机分走）。
- 本期不内置 broadcast helper；如果反馈强烈，可在 `pkg/bridges/` 下加一个通用 `Fanout[T]`，不列入 §8 Phase 1。
- `StreamHITLRequested/Resolved` 常量名**不改**（避免 churn）；它们与 `HumanDecisionKind` 的对应关系在 godoc 里点明即可。

## 4. 宿主使用手册（三种模式）

三种模式**互斥**，宿主按场景选一种。每种都可独立通过 `Run` 或 `Start` 使用。

### 4.0 公共契约：失败处理（`OnTimeout` / `OnReject`）

三个模式共享同一套失败处理合同。先说清这部分，后面示例不再重复。

#### 4.0.1 决策结果与两条失败路径

每次 HITL 决策最终落到一个 `DecisionResult`，SDK 根据结果走不同的策略字段：

| DecisionResult | 含义 | SDK 走哪条策略 |
|---|---|---|
| `DecisionApproved` / `DecisionAnswered` | 正常放行或结构化回答 | 不走失败路径，run 继续 |
| `DecisionRejected` | 显性拒绝（handler 返回 / 用户点 reject / `HumanDecisionAutoReject` 合成） | `HumanDecisionPolicy.OnReject` |
| `DecisionTimedOut` | Deadline 到期，决策值由 SDK 合成 | `HumanDecisionPolicy.OnTimeout` |
| `DecisionAborted` | 宿主主动放弃（ctx.Cancel、handler 抛 error） | 不经 `OnReject`/`OnTimeout`，直接按取消终结 |

> **设计选择**：`rejected` 和 `timedout` **不**合并为一个"Denial"抽象。两者在运维上语义不同——`rejected` 是"用户明确说不"（不需要告警），`timeout` 是"系统+用户协作失败"（可能需要升级通知、换通道重推）。用**两个独立旋钮** `OnReject` / `OnTimeout` 分别配置，零认知负担。

#### 4.0.2 `FailureAction`：失败发生时 SDK 下一步做什么

两个旋钮共用同一个类型 `FailureAction`：

```go
type FailureAction string

const (
    FailureActionUnset FailureAction = ""         // 零值 = 继承 SDK 默认（= FailureAbort）
    FailureAbort       FailureAction = "abort"    // 终止 run + emit RunResult.Failure（默认，向后兼容）
    FailureContinue    FailureAction = "continue" // adapter 把 reject/timeout 作为 tool_result 回给 agent，run 继续
    FailureRetry       FailureAction = "retry"    // 重新触发同一次决策，受 MaxRetries 限制；耗尽后降级为 FailureAbort
)
```

三种 action 的**语义取舍**：

| Action | run 结局 | 典型场景 | 风险提示 |
|---|---|---|---|
| `FailureAbort`（默认） | 立刻结束，`RunResult.Failure` 非空 | 严格 CI、金融/生产环境 | 宿主要读 `Failure.HumanDecision` 并妥善处理 |
| `FailureContinue` | run 继续，adapter 把 reject/timeout 作为 `tool_result` 回给 agent | 研究型任务、宽松 CI（"跳过被拒的工具也能完成部分目标"） | agent 可能反复尝试被拒的操作，注意 cost |
| `FailureRetry` | SDK 重新触发同一次决策（≤ `MaxRetries`），仍失败则 Abort | 交互式 UI / CLI（用户反悔）、IM 机器人换通道推送 | **adapter 能力相关**——不是所有 adapter 都能真正重试；`HumanDecisionSupport.Retry=false` 时 SDK 启动前 warn + 降级为 Abort |

**`FailureRetry` 的 adapter 能力**：见 §3.8 能力矩阵。Claude Phase 1 `Retry:false`（CLI 已本地合成 reject，无法原地重新发起）；Codex `requestApproval` 可支持；Cursor 不支持。

**Retry 场景下的 `DecisionRequest.RetryAttempt`**：SDK 在 Retry 触发的每次 request 上标注当前尝试次数：

- `RetryAttempt == 0` — 首次（默认值）
- `RetryAttempt == 1..MaxRetries` — 第 N 次重试

Handler / 宿主 UI 拿到 `RetryAttempt > 0` 时可以据此调整提示文案（"这是第 2 次征询"）、变更默认选项、收集用户理由；也用于测试断言。

#### 4.0.3 `OnTimeout` 为什么不接受 `HumanDecisionAutoApprove`

早期设计允许 `OnTimeout=HumanDecisionAutoApprove`（超时自动放行），在 `Permission` / `PlanReview` 上等价于"无人值守默认通过"——这是 HITL 里最危险的反模式：系统等不到人就默认授予 file write / shell exec 权限。

新 API 把值类型换成 `FailureAction` 从**源头堵死**这条路径。真正的诉求（让特定类别不经 HITL）请直接把对应类别的 `Mode` 写成 `HumanDecisionAutoApprove`，那是一个显性的、在类型层就"安全可审"的选择。

#### 4.0.4 结构化失败对象

`RunResult.Failure` 是类型化的，不是裸字符串 map：

```go
type FailureCode string

const (
    FailureReject      FailureCode = "decision_rejected" // OnReject=FailureAbort + Decision=DecisionRejected
    FailureTimeout     FailureCode = "decision_timeout"  // OnTimeout=FailureAbort + Decision=DecisionTimedOut
    FailureAgentError  FailureCode = "agent_error"
    FailureCancelled   FailureCode = "cancelled"
    FailurePolicyError FailureCode = "policy_error"
)

type RunFailure struct {
    Code     FailureCode
    Message  string
    Metadata map[string]any

    // HumanDecision 在 Code ∈ {FailureReject, FailureTimeout} 时非 nil。
    HumanDecision *HumanDecisionFailure
}

type HumanDecisionFailure struct {
    Kind     HumanDecisionKind   // 哪一类决策失败（permission/plan_review/question）
    Source   string              // adapter 起因标签（"claude.exit_plan_mode" 等）
    Decision DecisionResult      // DecisionRejected 或 DecisionTimedOut
    Request  *DecisionRequest // 触发失败的请求快照（含 Payload）
    Attempts int                 // 总共尝试了几次（含 FailureRetry 消耗的重试）
}
```

宿主 switch 语义（`FailureReject` 和 `FailureTimeout` 平行分支，不再有 `Denied`）：

```go
if f := result.Failure; f != nil {
    switch f.Code {
    case agentadaptor.FailureReject:
        log.Warn("HITL 被用户拒绝",
            "kind", f.HumanDecision.Kind,
            "source", f.HumanDecision.Source,
            "attempts", f.HumanDecision.Attempts)
    case agentadaptor.FailureTimeout:
        alert.Page("HITL 超时",
            "kind", f.HumanDecision.Kind,
            "source", f.HumanDecision.Source,
            "attempts", f.HumanDecision.Attempts)
    case agentadaptor.FailureAgentError:
        log.Error("agent 执行失败", "msg", f.Message)
    case agentadaptor.FailureCancelled:
        log.Info("run 被取消")
    default:
        log.Error("未分类失败", "code", f.Code, "msg", f.Message)
    }
}
```

> 说明：§4.0 引入的新公共符号（`FailureAction`、`FailureCode`、`HumanDecisionFailure`、`RunFailure.HumanDecision` 字段、`HumanDecisionPolicy.OnTimeout` / `OnReject` / `MaxRetries`）的规范定义见 §3.2。本节从宿主视角敲定契约。

---

### 4.1 模式 A：声明式 policy（无 UI / CI / 脚本）

**适用**：CI、脚本化、定时任务、后台 agent worker。宿主不参与人类审批，所有字段写 `AutoApprove` 或 `AutoReject`。

**特征**：不挂任何 typed handler，不消费 `DecisionRequests()`。

**该场景下的 `OnReject` 选型**（脚本模式下 `OnTimeout` 几乎不会触发——所有字段都是 `AutoApprove/AutoReject`，不产生等待）：

| 业务语义 | 推荐 `OnReject` | 理由 |
|---|---|---|
| 严格 CI / 金融 / 生产部署 | `FailureAbort`（默认） | 任何 reject 都应让 build fail，避免被静默跳过 |
| 研究型任务 / 宽松 CI | `FailureContinue` | agent 跳过被拒的工具也能完成部分目标，节省一次完整重跑 |
| — | `FailureRetry` | **不推荐**：`AutoReject` 是确定性合成，重试结果不会变；配了 SDK 会自动降级为 Abort |

```go
package main

import (
    "context"
    "log"

    agentadaptor "github.com/agent-dance/agent-adaptor"
    "github.com/agent-dance/agent-adaptor/claude"
)

func main() {
    sdk := agentadaptor.New(
        agentadaptor.WithDefaultAgent(claude.New(agentadaptor.ClaudeConfig{})),
    )

    policy := agentadaptor.RunPolicy{
        Isolation: agentadaptor.IsolationWorkspaceWrite,
        HumanDecision: agentadaptor.HumanDecisionPolicy{
            Permission: agentadaptor.HumanDecisionAutoApprove, // 放权给 agent
            PlanReview: agentadaptor.HumanDecisionAutoApprove, // plan 自动批
            Question:   agentadaptor.QuestionAutoReject,  // 没人能答，显性拒（Question 类没有 AutoApprove，见 §3.2）
            OnReject:   agentadaptor.FailureAbort,             // 严格模式：拒了就让 run 失败
        },
    }

    ctx := context.Background()
    result, _ := sdk.Run(ctx, "把 AGENTS.md 里 X 节挪到 docs",
        agentadaptor.WithRunPolicy(policy))

    if f := result.Failure; f != nil {
        switch f.Code {
        case agentadaptor.FailureReject:
            // Question=AutoReject 命中时走到这里。
            // f.HumanDecision.Kind == HumanDecisionQuestion
            // f.HumanDecision.Source 是 adapter 具体起因（如 "claude.ask_user_question"）
            log.Printf("HITL 被拒：kind=%s source=%s",
                f.HumanDecision.Kind, f.HumanDecision.Source)

        case agentadaptor.FailureAgentError:
            log.Printf("agent 执行失败：%s", f.Message)

        default:
            log.Printf("未分类失败：code=%s msg=%s", f.Code, f.Message)
        }
        return
    }

    log.Printf("完成：%s", result.Summary)
}
```

**关键点**：

- 把每一类想要的态度**显式**写清楚，不写 = 用 SDK 保守默认（§3.7）
- `HumanDecisionAutoApprove` 由各 adapter 翻译成自家 bypass/auto flag（claude `--dangerously-skip-permissions`、codex `bypass_approvals_and_sandbox`、cursor `--yolo`）。宿主不关心具体 flag。
- `HumanDecisionAutoReject` 命中 + `OnReject=FailureAbort` 时，SDK emit `RunFailure{Code: FailureReject, HumanDecision: {Kind, Source, Decision}}`，不让 agent 静默继续。
- 想"宽松 CI"改 `OnReject: FailureContinue`：agent 会把 reject 作为 tool_result 看到，继续尝试其他路径；不会 emit Failure（除非别的原因）。
- `MaxRetries` 在模式 A 下不生效（因为不推荐 Retry）。

### 4.2 模式 B：同步 handler（同进程 UI）

**适用**：CLI / TUI、桌面应用、单元测试。宿主能在一个 callback 里同步阻塞等到人类输入。

**特征**：对**想同步参与**的 Kind 分别注入 typed handler（`WithPermissionHandler` / `WithPlanReviewHandler` / `WithQuestionHandler`，或它们的 `WithDefault*` 绑定级等价物），并把对应 Mode 设为 `Ask`。没挂 handler 的 Kind 走 policy（`AutoApprove` / `AutoReject` / `QuestionAutoReject`）或 channel 兜底。

> **为什么不是一个胖 handler？** 一个统一 handler 意味着宿主必须 `switch req.Kind`，且：
> 1. 漏写 default 会吃掉决策；
> 2. `req.Payload["plan"].(string)` 是字符串键名约定，IDE 不提示、拼错运行时才炸；
> 3. `DecisionResponse.Result = DecisionApproved` 对 Question 非法但编译器不拦。
>
> 拆成 typed handler 后，三个问题在**类型层**一次解决（详见 §3.2 "Typed handler 层"）。

**该场景下的 `OnReject` / `OnTimeout` 选型**：

| 业务语义 | `OnReject` | `OnTimeout` | 理由 |
|---|---|---|---|
| 用户反悔场景（"这 plan 我不太满意，让 agent 改改再来"） | `FailureRetry` | `FailureAbort` | 用户点"改"让 agent 重新规划再问（`MaxRetries` 限次）；超时按严格路径退出 |
| 严格 CLI（拒就退） | `FailureAbort`（默认） | `FailureAbort`（默认） | 明确语义，退出前展示 `Failure.HumanDecision` 告诉用户拒在哪一步 |
| 批量脚本（"拒了就跳过这个任务"） | `FailureContinue` | `FailureAbort` | 用户只是不想参与这个子决策，但希望 run 继续；较少见 |

**Retry 的 adapter 支持矩阵**（`HumanDecisionSupport.Retry` / `QuestionSupport.Retry`，详见 §3.8）：

| adapter | `Retry` 能力 | 不支持时的行为 |
|---|---|---|
| claude Phase 1 | ❌ | SDK 启动时 warn，`OnReject/OnTimeout=FailureRetry` 自动降级为 `FailureAbort` |
| claude Phase 3 | ✅（stream-json 双向通道） | — |
| codex | ✅（JSON-RPC `requestApproval` 可重发） | — |
| cursor | ❌ | 同 claude Phase 1 |

**示例：只挂 PlanReviewHandler，其余走默认**（最常见的 CLI 场景——宿主只关心 plan 审批，permission 选择 AutoApprove，question 选择 AutoReject）：

```go
planReview := func(ctx context.Context, req agentadaptor.PlanReviewRequest) (agentadaptor.PlanReviewResponse, error) {
    // req.Plan 直接可用，无须 Payload["plan"].(string)
    if req.RetryAttempt > 0 {
        fmt.Printf("\n--- Plan (retry #%d) ---\n%s\n", req.RetryAttempt, req.Plan)
    } else {
        fmt.Printf("\n--- Plan ---\n%s\n", req.Plan)
    }
    fmt.Printf("-------------\n[y/N/r(改)]? ")
    var in string
    fmt.Scanln(&in)
    switch strings.ToLower(in) {
    case "y":
        return agentadaptor.PlanReviewResponse{
            RequestID: req.RequestID,
            Result:    agentadaptor.ApprovalApproved, // 类型收窄：Permission/PlanReview 用 ApprovalResult
        }, nil
    case "r":
        // 让 OnReject=FailureRetry 触发，agent 会重新规划
        return agentadaptor.PlanReviewResponse{
            RequestID: req.RequestID,
            Result:    agentadaptor.ApprovalRejected,
            Text:      "请换一个更保守的 plan，不要动 AGENTS.md 根节。",
        }, nil
    default:
        return agentadaptor.PlanReviewResponse{
            RequestID: req.RequestID,
            Result:    agentadaptor.ApprovalRejected,
        }, nil
    }
    // 注意：这里**没有** DecisionApproved / DecisionAnswered 等可写的值——
    // PlanReviewResponse.Result 的类型是 ApprovalResult，只接受 Approved/Rejected。
}

result, err := sdk.Run(ctx, prompt,
    agentadaptor.WithRunPolicy(agentadaptor.RunPolicy{
        HumanDecision: agentadaptor.HumanDecisionPolicy{
            Permission: agentadaptor.HumanDecisionAutoApprove, // 工具调用默认放行
            PlanReview: agentadaptor.HumanDecisionAsk,         // plan 才问人
            Question:   agentadaptor.QuestionAutoReject,       // 没人能答，显性拒
            Timeout:    0, // 默认 30s；想让 handler 无限等用 -1

            // 用户点 "r(改)" 时让 agent 重规划，最多 2 次；
            // 2 次仍被拒 → 按 FailureAbort 路径终止，emit Failure{Code: FailureReject}。
            OnReject:   agentadaptor.FailureRetry,
            MaxRetries: 2,
        },
    }),
    agentadaptor.WithPlanReviewHandler(planReview),
    // 不写 WithPermissionHandler / WithQuestionHandler——那两类走 policy 默认
)

if f := result.Failure; f != nil && f.Code == agentadaptor.FailureReject {
    fmt.Printf("用户拒绝了 %s（共尝试 %d 次后放弃）\n",
        f.HumanDecision.Kind, f.HumanDecision.Attempts)
}
```

**示例：挂三类都接**（桌面 / TUI，全程人工审）：

```go
sdk.Run(ctx, prompt,
    agentadaptor.WithRunPolicy(agentadaptor.PolicyHostReview), // 三类都是 Ask
    agentadaptor.WithPermissionHandler(func(ctx context.Context, req agentadaptor.PermissionRequest) (agentadaptor.PermissionResponse, error) {
        // req.Tool / req.Command / req.Args 都是强类型
        return agentadaptor.PermissionResponse{
            RequestID: req.RequestID,
            Result:    askYesNoForTool(req.Tool, req.Prompt), // 返回 ApprovalApproved / ApprovalRejected
        }, nil
    }),
    agentadaptor.WithPlanReviewHandler(planReview),
    agentadaptor.WithQuestionHandler(func(ctx context.Context, req agentadaptor.QuestionRequest) (agentadaptor.QuestionResponse, error) {
        // req.Schema / req.Choices 强类型可用
        answer, ok := promptUserStructured(req.Prompt, req.Schema, req.Choices)
        if !ok {
            return agentadaptor.QuestionResponse{RequestID: req.RequestID, Result: agentadaptor.QuestionRejected}, nil
        }
        return agentadaptor.QuestionResponse{
            RequestID: req.RequestID,
            Result:    agentadaptor.QuestionAnswered,
            Answer:    answer,
        }, nil
    }),
)
```

**关键点**：

- **per-Kind 挂载**：只写 `WithPlanReviewHandler` 也合法——没挂的 Kind 走 policy 默认或 `DecisionRequests()` channel（不会死锁）
- handler 返回 `error` 会被 SDK 当成 `DecisionAborted`（不经 `OnReject`，直接按取消路径走 `FailureCancelled`），adapter 按 reject 路径结束
- handler 返回 `ApprovalRejected` / `QuestionRejected` 才会触发 `OnReject`；这是"用户主动拒绝"的典型路径
- `OnReject=FailureRetry` 时 SDK 递增 `req.RetryAttempt` 后重新调同一个 typed handler
- **Retry 不是 adapter-transparent 的**：如果 support 矩阵 `Retry=false`（见 §3.8），SDK 在 `Start` 时 warn 并自动降级为 `FailureAbort`
- handler 被调用时 `ctx` 是 run ctx；宿主可以自己叠 `WithTimeout`，也可以依赖 `Policy.Timeout`
- `With*Handler`（RunOption）只对注入这一轮的 run 生效；要绑定级默认用 `WithDefault*Handler`（AgentOption）

### 4.3 模式 C：异步 channel（远程 UI / SSE · 服务化场景）

**适用**：`examples/streaming-chat-copilotkit` / `examples/streaming-sse-server` 这类"SDK 在服务端、UI 在浏览器、可能有通知服务/审计/IM 机器人等多个旁路"的部署。宿主需要：

- 通过 SSE/WebSocket 把决策请求推到前端
- 用户可能不在前端（关了 tab、在另一个设备）→ **同一事件还要触发推送通知 / IM 提醒 / 审计日志**
- 用户重新打开 UI 时要看到当前 pending 的决策请求

**特征**：需要异步回填的 Kind 设为 `Ask` 且**不挂**对应的 typed handler，**两个 goroutine 分别消费** `handle.DecisionRequests()`（决策路径）和 `handle.StreamEvents()`（知情路径，见 §3.10），通过 `handle.ResolveDecision(id, resp)` 回填。

> **混合模式也合法**：如果某一类你能在服务端同步处理（例如 `PlanReview` 交给固定的审批服务），挂对应的 typed handler；其余 Kind 的 request 会落到 `DecisionRequests()` channel 由 UI 线程接走。`per-Kind` 分派规则见 §3.6。

**该场景下的 `Timeout` / `OnTimeout` / `OnReject` 组合**（服务化场景里这三者**必须一起拍板**）：

| 组合 | 语义 | 适用场景 |
|---|---|---|
| `Timeout=10m` + `OnTimeout=FailureAbort` + `OnReject=FailureAbort`（推荐） | 用户 10 分钟没回应 → run 终止 → `Failure{Code: FailureTimeout}`；显性拒 → `Failure{Code: FailureReject}` | 有用户交互的生产部署，安全为先 |
| `Timeout=10m` + `OnTimeout=FailureAbort` + `OnReject=FailureContinue` | 拒后 agent 跳过被拒操作继续；超时仍然终止 | 异步工单型任务，允许部分失败 |
| `OnTimeout=FailureRetry`（adapter 支持时） | 超时后再问一次？ | **慎用**：用户不在时重推通常无效；限"换通道推送"的场景（如 IM 机器人升级到手机） |
| `OnReject=FailureRetry` | 用户反悔：拒了让 agent 改改再问 | `MaxRetries` 限次；需要 `HumanDecisionSupport.Retry=true` |
| （已堵死）`OnTimeout=HumanDecisionAutoApprove` | — | 类型层即不合法（`OnTimeout` 是 `FailureAction`）——详见 §4.0.3 |

```go
// 启动 run，保留 handle
handle, _ := sdk.Start(ctx, prompt,
    agentadaptor.WithStreaming(),
    agentadaptor.WithRunPolicy(agentadaptor.RunPolicy{
        HumanDecision: agentadaptor.HumanDecisionPolicy{
            PlanReview: agentadaptor.HumanDecisionAsk,
            Timeout:    10 * time.Minute,        // 服务化场景通常比 30s 默认更长
            OnTimeout:  agentadaptor.FailureAbort, // 超时 → run 终止 + Failure{Code: FailureTimeout}
            OnReject:   agentadaptor.FailureAbort, // 显性拒 → run 终止 + Failure{Code: FailureReject}
        },
    }),
)

// pending 是宿主自己维护的可寻址表，供两类访问：
//   1. /decision/resolve HTTP 路由按 RequestID 查 handle
//   2. 用户重连 UI 时按 userID/runID 拉当前未决请求（read-then-subscribe 模式）
// 这里用 sync.Map；生产环境可换 Redis / 内存 LRU + 过期清理。
type pendingEntry struct {
    Req    agentadaptor.DecisionRequest
    Handle agentadaptor.RunHandle
}
pending := sync.Map{}

// ===== 决策路径（排他消费者，仅一个）：驱动 UI 交互 =====
go func() {
    for req := range handle.DecisionRequests() {
        pending.Store(req.RequestID, pendingEntry{Req: req, Handle: handle})
        uiPusher.PushToBrowser(userIDOf(handle), req) // SSE/WS 推给前端
    }
    // channel 被 SDK close → run 结束，可做 pending 清理
}()

// ===== 知情路径（fan-out 给 N 个旁路）：审计 + 通知 + metrics =====
// 注意：StreamEvents() 也是 Go channel，多订阅者必须在这一个 goroutine 里分发，
// 不能起 N 个 goroutine 各自 for range（那会导致事件被随机分走）。
go func() {
    for ev := range handle.StreamEvents() {
        auditLogger.Append(ev) // 所有事件都归档
        metrics.Observe(ev)

        if ev.Kind == agentadaptor.StreamHITLRequested {
            // 异步发通知，不阻塞主分发循环
            // notifier 内部按 CreatedAt 做去重/节流/跨设备折叠
            go notifier.MaybeNotify(userIDOf(handle), ev)
        }
    }
}()

// ===== 回填路由：前端点了 approve/reject 后 POST 进来 =====
http.HandleFunc("/decision/resolve", func(w http.ResponseWriter, r *http.Request) {
    var body struct {
        RequestID string         `json:"request_id"`
        Decision  string         `json:"decision"`
        Choice    string         `json:"choice"`
        Answer    map[string]any `json:"answer,omitempty"`
    }
    _ = json.NewDecoder(r.Body).Decode(&body)

    v, ok := pending.LoadAndDelete(body.RequestID)
    if !ok {
        http.Error(w, "expired", http.StatusGone)
        return
    }
    entry := v.(pendingEntry)
    _ = entry.Handle.ResolveDecision(body.RequestID, agentadaptor.DecisionResponse{
        RequestID: body.RequestID,
        Result:    agentadaptor.DecisionResult(body.Decision),
        Choice:    body.Choice,
        Answer:    body.Answer,
    })
    w.WriteHeader(http.StatusNoContent)
})

// ===== 用户重连 UI 时的 read-then-subscribe 路由 =====
// 前端在订阅 SSE 之前先调一次这个接口，拿到当前 pending 的所有请求渲染出来，
// 再订阅 SSE 接收后续增量事件，避免"重连后丢失已有决策请求"。
http.HandleFunc("/decision/pending", func(w http.ResponseWriter, r *http.Request) {
    userID := authUserID(r)
    var out []agentadaptor.DecisionRequest
    pending.Range(func(_, v any) bool {
        entry := v.(pendingEntry)
        if userIDOf(entry.Handle) == userID {
            out = append(out, entry.Req)
        }
        return true
    })
    _ = json.NewEncoder(w).Encode(out)
})

result, _ := handle.Wait(ctx)

// ===== 结构化失败处理：服务化场景里按 FailureCode 决定 escalation =====
if f := result.Failure; f != nil {
    switch f.Code {
    case agentadaptor.FailureTimeout:
        // 用户没在规定时间内回应 → 通知服务升级告警（email / IM / SMS）
        // f.HumanDecision.Kind 告知是 plan/permission/question 哪一类
        alerting.Escalate(userIDOf(handle), f.HumanDecision)

    case agentadaptor.FailureReject:
        // 用户显性拒了 → 只做审计 / 状态回写，不 escalation
        audit.Append("user rejected",
            "kind", f.HumanDecision.Kind,
            "source", f.HumanDecision.Source,
            "attempts", f.HumanDecision.Attempts)

    case agentadaptor.FailureAgentError:
        alerting.PageOncall("agent crashed", f.Message)

    default:
        alerting.PageOncall("unknown failure", f.Code, f.Message)
    }
}
```

**关键点**：

- **双通道分工**：`DecisionRequests()` 是排他的决策权，`StreamEvents()` 是广播知情权，详见 §3.10。宿主自己在知情路径上做 fan-out（审计 / 通知 / metrics 各写各的）。
- **pending state 由宿主维护**：SDK 不持久化 HITL 请求；宿主必须维护一张 `RequestID → (Req, Handle)` 的表，用于 HTTP 路由查询和用户重连。内存 `sync.Map` 足够单 pod 场景；跨 pod 要走 sticky routing + 本地 pending（见下）。
- **用户重连 UI**：前端必须先 `GET /decision/pending` 拿快照，再订阅 SSE 增量事件。SDK 不重放 `StreamEvents()`——重放要从 pending 表拉。
- **通知去重**：`DecisionRequest.CreatedAt` 提供时间锚点，通知服务基于它做"静默期内不重复推"、"用户已在 UI 则抑制推送"等策略；这些策略属于宿主职责。
- **`ResolveDecision` 幂等性**：不存在的 ID 返回 error（已超时 / 已被其他调用消费），HTTP 可据此返回 410，让前端刷新 pending 列表。
- **`FailureTimeout` vs `FailureReject`**：服务化场景里两者**运维意义不同**——`FailureTimeout` 是"系统 + 用户协作失败"（需要 escalation），`FailureReject` 是"用户明确拒绝"（不需要告警）。SDK 只给结构化信号，具体怎么 escalation 是宿主职责。
- **`OnTimeout=FailureRetry` 在本模式下少用**：超时重推给同一个用户通常无效；真要做（例如 IM 机器人逐级升级到手机），建议在宿主层捕获 `FailureTimeout` 后启动新 run，而不是让 SDK 内循环。
- **`handle` 不能跨进程**：多 pod 部署时必须把 `/decision/resolve` 的 ingress 按 `runID` 路由到启动该 run 的 pod（sticky-by-runID）。跨进程路由是**宿主职责**，不是 SDK 职责。Run 持久化 / 跨 pod 恢复是未来的另一个 workstream。
- **bridge 层**：`pkg/bridges/agui` / `pkg/bridges/sse` 把上面的手写 goroutine 模板化（§6）。生产部署直接用 bridge，这里的示例只是说清底层契约。

#### 4.3.1 UI 会话历史恢复

**问题**：用户关了浏览器 tab、换了设备、网络闪断——重新打开 UI 时，除了 pending 决策（已有 `/decision/pending`），还要看到**此前 run 中发生过的全部事件**：agent 消息、tool_call、thinking、已 resolve 的决策历史 …… 否则上下文丢失。

**SDK 的职责边界**：SDK **不**内置 history replay（不提供 `handle.StreamHistory()` / `handle.Replay()` 这类 API）。四条理由：

1. 违反"SDK 只做通道不做传输"原则——加内存 buffer 让 SDK 有状态，需要 eviction 策略
2. 跨进程不 work——`handle` 不跨 pod，SDK 的内存 buffer 帮不到另一个 pod 上的重连 UI
3. 存多久、存什么、要不要脱敏，都是宿主/业务的 ops 决策，SDK 不该替任何人拍板
4. 宿主已经有接入点——`StreamEvents()` 就是现成的持久化插桩位，没必要在 SDK 再包一层

因此**历史恢复是宿主职责**。SDK 只提供一块基础设施（`StreamPayload.Seq`，见 §3.4.2）作为重放游标。

**Canonical 三步协议**（`history → pending → subscribe`）：

**写入端**——§4.3 示例里的 audit goroutine 顺手做持久化：

```go
go func() {
    for ev := range handle.StreamEvents() {
        // history 是宿主选的持久化后端：Redis Stream / Postgres JSONB / S3 / 内存环形缓冲。
        // 主键 (RunID, Seq) 天然去重幂等。
        history.Append(handle.RunID(), ev.Seq, ev)

        auditLogger.Append(ev)
        metrics.Observe(ev)
        if ev.Kind == agentadaptor.StreamHITLRequested {
            go notifier.MaybeNotify(userIDOf(handle), ev)
        }
    }
}()
```

**恢复端**——宿主注册两条 HTTP 路由：

```go
// 1. 历史事件增量拉取：从持久化层按 Seq 游标读
http.HandleFunc("/session/events", func(w http.ResponseWriter, r *http.Request) {
    runID     := r.URL.Query().Get("run_id")
    afterSeq  := parseUint(r.URL.Query().Get("after")) // 上次收到的 Seq，0 表示全量
    events    := history.Load(runID, afterSeq)        // 宿主持久化层实现
    _ = json.NewEncoder(w).Encode(events)
})

// 2. /decision/pending —— 已在 §4.3 定义
```

**前端重连协议**：

```
1. GET  /session/events?run_id=R&after=0         // 拿全部历史（首次）
                                                  //   or after=<lastSeenSeq> 拿增量
2. GET  /decision/pending?run_id=R               // 拿当前未决决策
3. SSE  /session/stream?run_id=R&since=<lastSeq> // 订阅实时增量
```

顺序很重要：**先历史后 pending 再订阅**，且步骤 3 的 `since=` 必须带步骤 1 最后一条的 Seq——否则 1→3 之间可能丢事件。SSE bridge 响应每条 message 写 `id: <Seq>`，浏览器 `EventSource` 自带的 `Last-Event-ID` 机制在断线后自动续传。

**关键约束**：

- **持久化格式 = `StreamPayload`**——宿主存什么就是 SDK 给什么，避免格式转换带来的语义丢失；要暴露给前端时由 bridge 层统一转 AG-UI / 其他协议
- **主键 `(RunID, Seq)`**——请不要用 `CreatedAt` 做主键（多 pod 写入时钟漂移）
- **`runID` 的生命周期**由宿主掌握——持久化保留多久是 retention policy；SDK 不内置
- **SDK 不重放**——`StreamEvents()` channel 消费后事件即走即散，重放必须从持久化层拉
- **跨 pod**：持久化层（Redis / DB）是跨 pod 共享的唯一权威源；`handle` 仍不能跨进程（新请求 SSE 订阅 / resolve 仍需 sticky routing 到 run owner pod）

**未来方向（预告）**：本期把持久化下放到宿主，长期我们会考虑让 SDK 提供一个**可选的** "vendor session replay" capability——直接从 vendor CLI 自家的 session 源（如 claude 的 `~/.claude/projects/*.jsonl`、codex 的 session db）重建 `StreamPayload` 流。这将作为**独立 workstream**，详见 §8.3。

### 4.4 模式选择决策树

```
                          宿主能在 callback 里同步等人?
                              /             \
                            yes              no
                             │                │
                         模式 B           宿主完全自动化?
                                             /     \
                                           yes      no
                                            │        │
                                         模式 A    模式 C
```

## 5. Adapter 接入矩阵（vendor 翻译）

所有 adapter 的 CLI flag 翻译**单点完成**在各自 driver 的 `buildExecArgs`。宿主 API 对三家完全同构。

### 5.1 Claude（本期实施 Phase 1，Phase 3 补双向回填）

**`HumanDecision.Permission` → CLI flag**：

| Mode | 行为 |
|---|---|
| `HumanDecisionAutoApprove` | 传 `--dangerously-skip-permissions`（vendor bypass）|
| `HumanDecisionAsk` | 不传；通过 `permission_request` 上浮到 `DecisionRequest`，Phase 3 双向回填 |
| `HumanDecisionAutoReject` | 不传；识别到 permission_request 后 SDK 预制 reject，adapter 把 reject 作为 tool_result 回给 CLI（Phase 3） |
| `HumanDecisionUnset` | 等价 `HumanDecisionAsk`（§3.7 默认） |

**`HumanDecision.PlanReview` → 工具识别**：

在 `claude/streaming_parser.go::handleContentBlockStart` 的 `tool_use` 分支上，按白名单识别：

```go
var claudeInteractiveTools = map[string]HumanDecisionKind{
    "ExitPlanMode":    HumanDecisionPlanReview,
    "AskUserQuestion": HumanDecisionQuestion,
    // EnterPlanMode 不上浮：只是进入 plan 模式的标记，真正的审批在 ExitPlanMode
}
```

**Phase 1 行为（本期落地）**：
- 看到 `ExitPlanMode` tool_use 帧 → 暂存 `input.plan`
- 看到对应 `tool_result`：
  - `is_error: false` 且 content ≈ "User approved the plan." → emit `StreamHITLResolved{Decision: approved}`，继续
  - `is_error: true`（当前 CLI 在 `--dangerously-skip-permissions` 下默认走这条）→ emit:
    - `StreamHITLRequested{Kind: plan_review, Source: "claude.exit_plan_mode", Raw: {plan}}`
    - `StreamHITLResolved{Decision: rejected}`
    - 若 `OnReject=FailureAbort`（默认）：`RunResult.Failure = &RunFailure{Code: FailureReject, Message: "Claude Plan Mode was not approved; no file changes applied.", HumanDecision: &HumanDecisionFailure{Kind: HumanDecisionPlanReview, Source: "claude.exit_plan_mode", Decision: DecisionRejected, Request: …, Attempts: 1}}`
    - 若 `OnReject=FailureContinue`：不 emit Failure，run 继续（agent 拿到 "plan rejected" 作为 tool_result）
- `DriverCheckpoint.Valid` 仍然有效（session 可 resume）；`RunResult.Summary = "Plan rejected"`

**Phase 1 不做**：
- 不做"调用 sink.RequestDecision 真实阻塞" —— CLI 层已经合成 reject 了，阻塞没意义
- Phase 1 对 claude 是**观测层显性化**，Phase 3 才会通过 stdin stream-json 或 MCP permission prompt 真正拦截 + 回填

**Descriptor**（见 §3.8）：Permission / PlanReview 各填 `HumanDecisionSupport{Ask:true, AutoApprove:true, AutoReject:true, Retry:false}`，Question 填 `QuestionSupport{Ask:true, AutoReject:true, Retry:false}`；本期 Phase 1 的 `Ask`/`AutoReject` 只在观测意义下成立，Phase 3 才有真正的执行效力；`Retry:false` 源于 CLI 已本地合成 reject 无法原地重新询问，Phase 3 双向通道打通后可改为 `true`。

### 5.2 Codex（只设计，不实施）

**现状**：codex app-server 有原生的 server-initiated `requestApproval` 事件，且 JSON-RPC 允许 `response` 回包。

**`HumanDecision.Permission` → CLI + 协议**：

| Mode | 行为 |
|---|---|
| `HumanDecisionAutoApprove` | `approvalPolicy = auto_always`（等价现 `bypass_approvals_and_sandbox`） |
| `HumanDecisionAsk` | `approvalPolicy = never_auto`；收到 `requestApproval` 通知 → `sink.RequestDecision(ctx, DecisionRequest{Kind: permission})` → 用 JSON-RPC client 回复对应 `user/approveCommand` |
| `HumanDecisionAutoReject` | `approvalPolicy = never_auto`；所有 `requestApproval` 自动回 reject |
| `HumanDecisionUnset` | `HumanDecisionAsk` |

**覆盖面**：Permission 类完整。PlanReview / Question 类在 codex 无对应（它没有 plan mode 也不产生 Question 事件），Descriptor 照实填：`PlanReview HumanDecisionSupport{Ask:false, AutoApprove:true, AutoReject:false, Retry:false}`；`Question QuestionSupport{Ask:false, AutoReject:false, Retry:false}`——Question 支持矩阵全 `false` 即表达"Question 类不被 adapter 认可"。

**工作量估计**：~150 行。

### 5.3 Cursor（只设计，不实施）

**现状**：`cursor/driver.go` 只有 `--yolo`，没有流式 approval 通道。

**`HumanDecision.Permission` → CLI flag**：

| Mode | 行为 |
|---|---|
| `HumanDecisionAutoApprove` | 传 `--yolo` |
| `HumanDecisionAsk` / `HumanDecisionAutoReject` | 启动前 SDK 检查 `Descriptor.RunPolicyCaps.Permission.Ask=false`，返回 `ErrHumanDecisionModeUnsupported`；宿主必须改 policy |
| `HumanDecisionUnset` | 等价 `HumanDecisionAutoApprove`（降级，并在 log warn） |

PlanReview 同样填 `HumanDecisionSupport{Ask:false, AutoApprove:true, AutoReject:false, Retry:false}`；Question 填 `QuestionSupport{Ask:false, AutoReject:false, Retry:false}`（cursor 不产生 Question 事件）。若将来 cursor CLI 暴露流式 approval，再参考 codex 路径接入。

## 6. Bridge 层改造

### 6.1 AG-UI bridge

文件 `pkg/bridges/agui/bridge.go`：

**现状**（`StreamHITLRequested → CustomEvent`，CopilotKit 默认忽略）：保留作为 legacy audit，但**不是**默认。

**升级**（Phase 1 一并做）：
- `StreamHITLRequested` 映射成 AG-UI `ToolCallStart` + `name = "dec." + kind + "." + source`（例如 `"dec.plan_review.claude.exit_plan_mode"`）
- `ToolCallArgs` 承载 `DecisionRequest.Payload` + `Choices`
- 宿主前端用 CopilotKit 的 `useCopilotAction({name: "dec.plan_review.claude.exit_plan_mode", handler: ...})` 直接接住；handler 返回值作为 `ResolveDecision` 的输入
- bridge 侧暴露 `bridge.ResolveDecision(runID, requestID, resp)` 便捷函数，内部持有 `runID → handle` 映射

**向后兼容**：新增 `agui.WithDecisionMode(agui.DecisionAsToolCall)`（新默认）/ `agui.DecisionAsCustom`（旧行为）。

### 6.2 SSE bridge（`pkg/bridges/sse`）

SSE 是单向流，回填必须走一条额外的 HTTP 路由。bridge 推送时把 `DecisionRequest` 序列化为 SSE 事件 `event: decision.request`；宿主自己注册 `POST /decision/resolve` 并调 `handle.ResolveDecision`。不替宿主做路由。

### 6.3 没升级到 v2 的 bridge

所有 `DecisionRequest` 由 SDK 超时后按 `OnTimeout` 兜底（默认 `FailureAbort` → emit `Failure{Code: FailureTimeout}`）；`StreamHITLRequested` 仍正常 emit；`RunResult.Failure` 仍可被宿主读到。用户感受从"完全静默"升级成"显性失败"。

## 7. 迁移指南（break 的具体范围）

### 7.1 删除的公共符号

```
agentadaptor.ApprovalLevel                         // 类型
agentadaptor.ApprovalInherit / ApprovalAsk / ApprovalAuto / ApprovalOff
agentadaptor.TrustLevel                            // 类型
agentadaptor.TrustInherit / TrustAsk / TrustAuto / TrustDeny
agentadaptor.RunPolicy.Approvals                   // 字段
agentadaptor.RunPolicy.Trust                       // 字段
agentadaptor.RunPolicyInteractive                  // 预设
agentadaptor.RunPolicyReadOnly                     // 预设
agentadaptor.RunPolicyTrusted                      // 预设
agentadaptor.RunPolicyCapabilities.Approvals       // 字段
agentadaptor.RunPolicyCapabilities.Trust           // 字段
```

### 7.2 新增的公共符号

见 §0 命名约定。完整列表：

- **策略/声明层（`HumanDecision*` + `Question*`）**：
  - `HumanDecisionMode`（适用 Permission / PlanReview）+ `HumanDecisionUnset` / `HumanDecisionAsk` / `HumanDecisionAutoApprove` / `HumanDecisionAutoReject`
  - `QuestionMode`（Question 专用，值集合是 `HumanDecisionMode` 的真子集）+ `QuestionUnset` / `QuestionAsk` / `QuestionAutoReject`
  - `HumanDecisionPolicy`（含字段 `Permission HumanDecisionMode` / `PlanReview HumanDecisionMode` / `Question QuestionMode` / `Timeout` / `OnTimeout` / `OnReject` / `MaxRetries`）
  - `HumanDecisionKind` + `HumanDecisionPermission` / `HumanDecisionPlanReview` / `HumanDecisionQuestion`
  - `HumanDecisionSupport`（含 `Ask` / `AutoApprove` / `AutoReject` / `Retry`——Permission / PlanReview 用）
  - `QuestionSupport`（含 `Ask` / `AutoReject` / `Retry`——Question 用，无 `AutoApprove` 字段）
  - `HumanDecisionFailure`（挂在 `RunResult.Failure.HumanDecision`）
- **`RunPolicy` 字段**：`RunPolicy.HumanDecision`
- **决策层（`Decision*`）——跨类通用**（channel 模式、adapter sink）：
  - 事件对象：`DecisionRequest`（含 `CreatedAt` / `RetryAttempt` 一等字段）/ `DecisionResponse`（字段 `RequestID` / `Result` / `Choice` / `Answer` / `Text`）/ `DecisionChoice`
  - 处理入口：`DecisionCapableSink`（adapter 侧扩展接口）
  - 结果值：`DecisionResult` + `DecisionApproved` / `DecisionRejected` / `DecisionAnswered` / `DecisionTimedOut` / `DecisionAborted`
- **决策层——typed handler（per-Kind）**：
  - Request 类型：`PermissionRequest` / `PlanReviewRequest` / `QuestionRequest`（公共字段嵌入未导出的 `decisionRequestBase`）
  - Response 类型：`PermissionResponse` / `PlanReviewResponse` / `QuestionResponse`
  - Result 子类型：`ApprovalResult` + `ApprovalApproved` / `ApprovalRejected`（供 Permission / PlanReview）；`QuestionResult` + `QuestionAnswered` / `QuestionRejected`（供 Question）
  - Handler 函数类型：`PermissionHandler` / `PlanReviewHandler` / `QuestionHandler`
- **失败/策略动作层（`Failure*`）**：
  - `FailureAction` + `FailureActionUnset` / `FailureAbort` / `FailureContinue` / `FailureRetry`
  - `FailureCode` + `FailureReject` / `FailureTimeout` / `FailureAgentError` / `FailureCancelled` / `FailurePolicyError`
  - `RunFailure.HumanDecision *HumanDecisionFailure` 字段（在 `run_types.go` 既有 `RunFailure` 上扩展）
- **RunHandle 方法**：`DecisionRequests()` / `ResolveDecision()`（仅 channel 模式使用）
- **EventSink 方法**：`RequestDecision()`
- **Options（per-Kind）**：
  - 单次：`WithPermissionHandler` / `WithPlanReviewHandler` / `WithQuestionHandler`
  - 绑定级：`WithDefaultPermissionHandler` / `WithDefaultPlanReviewHandler` / `WithDefaultQuestionHandler`
- **预设**：`PolicyHostReview` / `PolicyReadOnlyReview` / `PolicyAutonomous`
- **错误**：`ErrHumanDecisionModeUnsupported`（`ErrDecisionHandlerAndChannelConflict` 不再需要——per-Kind 分派使冲突不可能发生）
- **保留**：`StreamHITLRequested` / `StreamHITLResolved`（stream 常量名不改）
- **既有类型扩展**：`StreamPayload` 新增字段 `Seq uint64`（run 内部单调递增，见 §3.4.2；支撑 §4.3.1 UI 会话历史恢复）

### 7.3 调用方迁移表

| 旧写法 | 新写法 | 备注 |
|---|---|---|
| `RunPolicy{Approvals: ApprovalAsk}` | `RunPolicy{HumanDecision: HumanDecisionPolicy{Permission: HumanDecisionAsk}}` | 只管 A 类；B/C 走默认 |
| `RunPolicy{Approvals: ApprovalAuto}` | `RunPolicy{HumanDecision: HumanDecisionPolicy{Permission: HumanDecisionAutoApprove}}` | `auto` 和 `off` 的宿主意图其实一致，这里合并了 |
| `RunPolicy{Approvals: ApprovalOff}` | 同上 | 旧 `off` 的"一刀切 bypass"语义由 `PolicyAutonomous` 预设承担 |
| `RunPolicy{Trust: TrustAuto}` | `RunPolicy{HumanDecision: HumanDecisionPolicy{Permission: HumanDecisionAutoApprove}}` | cursor 特有字段消失 |
| `RunPolicyInteractive` | `PolicyHostReview` | 语义等价 |
| `RunPolicyReadOnly` | `PolicyReadOnlyReview` | 语义等价 |
| `RunPolicyTrusted` | `PolicyAutonomous` | 语义等价 |

### 7.4 受影响的仓内文件

**代码**：
- `run_policy.go` — 全面重写
- `api.go` — `RunHandle` / `RunPolicyCapabilities`
- `options.go` — 增六个 per-Kind option（`WithPermissionHandler` / `WithPlanReviewHandler` / `WithQuestionHandler` + 三个 `WithDefault*`）；`runOptions.permissionHandler` / `planReviewHandler` / `questionHandler`；`AgentDefaults` 三个对应字段
- `runner.go` — `dualSink` 实现 `DecisionCapableSink`，`RequestDecision` 按 `req.Kind` 分派到 typed handler 或 `asyncRunHandle.decisionRequests` channel；handler 返回 typed response 由 runner 转成统一 `DecisionResponse` 回给 adapter；`EmitStream` 入口处单点分配 `StreamPayload.Seq`（run 内部单调计数器）
- `claude/driver.go` — `buildClaudeExecArgs` 读 `HumanDecision.Permission`；`Descriptor.RunPolicyCaps` 换新形状
- `claude/parser.go` + `claude/streaming_parser.go` — `ExitPlanMode` 识别 + emit Failure
- `codex/driver.go` + `codex/run_streaming.go` — `mapApprovalPolicy` 改读 `HumanDecision.Permission`（仅重命名，语义映射等价）
- `cursor/driver.go` — `--yolo` 改读 `HumanDecision.Permission=HumanDecisionAutoApprove`
- `pkg/bridges/agui/bridge.go` — 新映射

**文档**：
- `docs/run-policy.md` — 全面重写
- `docs/streaming-adapter-contract.md` §2.5 — 替换为 v2 合同
- `docs/streaming.md` / `docs/usage-guide.md` — 补三种模式示例

**示例**：
- `examples/internal/exampleutil/agui_sdk.go` `AGUIExampleRunPolicy` — 改用 `PolicyHostReview` 或等价写法
- `examples/streaming-chat-copilotkit` — 接 `useCopilotAction("dec.*")`
- `examples/mock-adapter-playground` — HITL 分支验证三模式
- `examples/streaming-sse-server` — README 加 `POST /decision/resolve`

**测试**：
- `run_policy_test.go` — 合并用例全改
- `claude/streaming_parser_test.go` — 增 `ExitPlanMode reject → Failure` fixture
- agui bridge 增 Decision round-trip 测试

### 7.5 外部兼容性评估

本仓库目前没有外部 import（检查 `go.sum` / 无公开 module registry 记录）。`Approvals` / `Trust` 只在内部 examples 和内置 adapter 间使用。break 的传播半径 = 本仓库。

## 8. 实施路线

| Phase | 内容 | 工作量（估） | 触发条件 |
|---|---|---|---|
| **Phase 1**（本期） | SDK 合同全改（`HumanDecision*` + `Decision*` + `Failure*` 类型 + sink + handle + policy + handler）；claude Phase 1 识别 + 显性失败；agui bridge 升级；迁移 examples / tests | ~1100 行 + 全面文档 | 本 workstream |
| Phase 2 | codex `requestApproval` 双向回填 | ~150 行 | codex 下一轮迭代 |
| Phase 3 | claude 双向回填（stdin stream-json 或 MCP permission prompt） | 取决于 CLI 方案 | CLI 方案验证后 |
| Phase 4 | cursor 视 CLI 演进决定 | — | cursor CLI 支持流式 approval 后 |

Phase 1 比上一稿增加约 500 行，主要来自：
- 删除 `ApprovalLevel` / `TrustLevel` 全链路（负 ~100 行）
- 新增 `HumanDecision*` + `Decision*` + `Failure*` 类型系列（~200 行）
- 示例和测试迁移（~400 行）

### 8.3 独立 workstream 预告：Vendor Session Replay

与本 workstream **正交但相关**的后续能力：让 SDK 提供一个可选 `SessionReplayer` capability，**直接从 vendor CLI 自家的 session 源重建 `StreamPayload` 流**（claude 的 `~/.claude/projects/*.jsonl` / codex app-server 的 session/turn db / cursor 待定）。把 vendor 自家持久化当"权威真相源"，SDK 只做格式翻译，和"SDK 只做通道"原则一致。

**为什么不塞进本 workstream**：

- HITL v2 已经很大，再加 replay 会显著拖慢落地
- Replay 价值显著但**不依赖** HITL v2——两者正交
- 每期独立 review / 独立 merge，风险可控

**与本期的衔接**：本期加 `StreamPayload.Seq`（§3.4.2）是 replay workstream 的基础设施——持久化和重放都以 `(RunID, Seq)` 为主键。所以本期的工作不白做，下期 replay 可以直接复用。

**预计范围**（独立 workstream 文件 `docs/workstream-session-replay.md` 待立项）：

- 新 capability 接口：`SessionReplayer.ReplaySession(ctx, checkpoint, opts) (<-chan StreamPayload, error)`
- Phase 1 实现：claude adapter（`.jsonl` → `StreamPayload` 翻译，复用 `claude/streaming_parser.go`）
- Phase 1 约束：**snapshot 语义**（要求 run 已结束，不支持 tail-follow）
- Phase 2：tail-follow 模式 + 和 `DriverCheckpoint` / `--resume` 协同实现"replay + 继续 streaming"
- `ReplaySession` 模式下 `DecisionCapableSink` 的阻塞语义跳过——replay 是**历史快照只读重放**，不触发新决策

**风险/约束**：

- 依赖 vendor 内部 session 格式（非官方 API）——需要 adapter 版本探测 + fallback
- 仅限本地文件场景（继承"`handle` 不跨进程"约束）；跨 pod 仍需宿主自持久化方案
- cursor 的 session 格式待调研——首版可能只覆盖 claude

**触发条件**：宿主有明确需求（单 pod 部署 + 不愿自己搭持久化 + 愿意接受 vendor 格式 coupling）时立项。在此之前，§4.3.1 的"宿主自持久化"规范是唯一受支持的历史恢复路径。

## 9. 未决问题（review 期间定）

1. `HumanDecisionPolicy.Timeout = 0` 的含义。**倾向**：= 30s 默认；想"禁用超时"用 `-1`。
2. `RunHandle.DecisionRequests()` 在 `HumanDecision` 全部字段都不是 `Ask`（`HumanDecisionAsk` / `QuestionAsk`）时是否返回 nil / 立即关闭的 channel？**倾向**：立即关闭的 channel，`for range` 零迭代，调用方无需分支判断。
3. `ResolveDecision` 是否允许对同一 ID 调用多次？**倾向**：不允许，第二次返回错误。状态中间态由宿主自管。
4. `FailureReject` 是否按 `Kind` 再分（`FailurePermissionReject` / `FailurePlanReject` / `FailureQuestionReject`）？**倾向**：不分——`Failure.HumanDecision.Kind` 已能区分，拆出 N 个常量只增加宿主 switch 分支数，对观测不加信息。

## 10. 与 `workstream-hitl.md` 的问答映射

本节给 `workstream-hitl.md` §9 的每个问题一个**显式**答案。

| workstream-hitl.md §9 问题 | 本设计答案 |
|---|---|
| 9.1.1 统一抽象 vs 三种 | **统一**：`HumanDecisionKind` 字段区分；Payload 承载三类 |
| 9.1.2 如何识别 | 白名单 + descriptor；claude 用工具名白名单；codex 用协议自带 marker |
| 9.1.3 Stream 事件加子类型 | **加**：`DecisionRequest.Kind` 承载；`StreamKind` 不拆 |
| 9.1.4 响应通道 | `EventSink.RequestDecision` + `RunHandle.ResolveDecision`；与 `StreamEvents` 独立 |
| 9.1.5 阻塞 vs 非阻塞 | **阻塞**（对 adapter 而言）；宿主侧可同步或异步；超时由 `Policy.Timeout` 决定，`OnTimeout=FailureAbort` 默认 |
| 9.2.1 `Approvals ∈ {ask,auto,off}` 够用吗 | **不够且方向错**：`Approvals` 是 vendor-facing，直接删掉换成 host-facing `HumanDecisionMode` |
| 9.2.2 细粒度子开关 | `HumanDecisionPolicy.Permission/PlanReview/Question`（与 A/B/C 三类对应） |
| 9.2.3 CLI 旋钮对齐 | CLI flag 翻译移入各 adapter 的 `buildExecArgs` 单点，API 层完全去 vendor 化 |
| 9.3.1 AG-UI 新 event type | **不急**，Phase 1 映射成 `ToolCallStart(name=dec.*)`，宿主用 `useCopilotAction` 接；等 AG-UI 协议扩展再升 |
| 9.3.2 requestID 关联 | SDK 分配 `runID-itx-seq`；bridge / 宿主各自通道透传；`ResolveDecision` 按 ID 路由 |
| 9.3.3 宿主没接入的默认 | **显性 Failure + audit**（不是静默 reject） |
| 9.4.* Claude adapter | Phase 1 仅"知情"；Phase 3 探索双向回填 |

## 11. 决策清单（给下一个 reviewer）

- [ ] 接受"HITL 作为 RunHandle 第三条通道"的定位，不新开 API 入口
- [ ] 接受"三模式互斥 / 语义等价"的设计选型
- [ ] 接受 **break 现有 `Approvals` / `Trust` / 所有预设**，换成单维度 `HumanDecision`
- [ ] 接受三层命名：`HumanDecision*`（策略/失败归因）/ `Decision*`（决策事件 + 处理入口 + 结果值）/ `Failure*`（失败动作 + 失败码）
- [ ] 接受 `DecisionResponse.Result`（字段名）避免 `Decision.Decision` 的字面重复
- [ ] 接受 Question 类使用独立的 `QuestionMode` / `QuestionSupport`——类型层堵死 `Question: HumanDecisionAutoApprove` 这种无合法结果值可合成的写法；Permission / PlanReview 因值集合同构继续共享 `HumanDecisionMode`
- [ ] 接受 typed handler 分拆：`PermissionHandler` / `PlanReviewHandler` / `QuestionHandler` 三个 per-Kind 签名（取代旧的胖 `DecisionHandler`）；异步 `DecisionRequests()` channel 保留统一形状；`ErrDecisionHandlerAndChannelConflict` 删除
- [ ] 接受 `StreamPayload.Seq` 作为基础设施字段（§3.4.2），以及 §4.3.1 规定的"宿主自持久化 → history → pending → subscribe"三步重连协议
- [ ] 认可 vendor session replay 独立立项（§8.3），不在本期实施——本期只铺 `Seq` 基础设施
- [ ] 接受 `OnTimeout` / `OnReject` 拆成**两个独立** `FailureAction` 旋钮（不合并为 `OnDenial`）
- [ ] 接受 `OnTimeout` 的值类型是 `FailureAction` 而非 `HumanDecisionMode`——从源头堵死"超时自动放行"反模式
- [ ] 接受预设重命名（`RunPolicyInteractive → PolicyHostReview` 等）
- [ ] 接受 claude Phase 1 只到"观测层显性化"，Phase 3 再追双向回填
- [ ] 认可 codex / cursor 方案已足够清晰，不在本期实施
- [ ] 认可仓库无外部 import，break 的传播半径 = 本仓库，不走 deprecation 周期
