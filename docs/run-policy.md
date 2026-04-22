# Run policy（`RunPolicy`）合同与实施说明

本文档是 **agent-adaptor** 对「一次执行要遵守哪些安全/能力边界」的**唯一**公开合同；实现与 `AGENTS.md` §2.1 单一路径执行模型一致。

**HITL v2（2026-04-22）**：`RunPolicy.Approvals` / `RunPolicy.Trust` 已被替换为单维度 `RunPolicy.HumanDecision`（类型 `HumanDecisionPolicy`）。详细设计见 [`docs/workstream-hitl-v2.md`](./workstream-hitl-v2.md)，本文档只做合同总览。

## 1. 类型与语义

### 1.1 `RunPolicy` 字段

| 字段 | 类型 | 说明 | 空值 |
|------|------|------|------|
| `Isolation` | `IsolationLevel` | 工作区/系统隔离强度 | `""` 表示继承 |
| `WebSearch` | `FeatureLevel` | 是否允许联网搜索类能力 | `""` 表示继承 |
| `Browser` | `FeatureLevel` | 是否允许浏览器/受控页工具 | `""` 表示继承 |
| `HumanDecision` | `HumanDecisionPolicy` | 人在回路（HITL）三类决策的策略 | 零值表示全部继承 SDK 默认 |

### 1.2 枚举

- **`IsolationLevel`**：`read_only` / `workspace_write` / `unrestricted`（`unrestricted` 映射各 CLI 的"全访问/危险沙箱 off"）
- **`FeatureLevel`**（WebSearch / Browser）：`allow` / `deny`

### 1.3 `HumanDecisionPolicy`

```go
type HumanDecisionPolicy struct {
    Permission HumanDecisionMode   // Ask / AutoApprove / AutoReject
    PlanReview HumanDecisionMode   // Ask / AutoApprove / AutoReject
    Question   QuestionMode        // Ask / AutoReject（值集合是 HumanDecisionMode 的真子集）
    Timeout    time.Duration       // 0 = 30s 默认；<0 = 永不超时
    OnTimeout  FailureAction       // FailureAbort / FailureContinue / FailureRetry
    OnReject   FailureAction       // 同上
    MaxRetries int                 // FailureRetry 动作的上限，0 = SDK 默认 3
}
```

SDK 默认值（字段为零时生效）：

| 字段 | 默认 |
|------|------|
| `Permission` | `HumanDecisionAsk` |
| `PlanReview` | `HumanDecisionAsk` |
| `Question` | `QuestionAutoReject` |
| `Timeout` | `30s` |
| `OnTimeout` | `FailureAbort` |
| `OnReject` | `FailureAbort` |
| `MaxRetries` | `3` |

### 1.4 具名预设

包级变量（可直接用于 `WithRunPolicy` / `WithDefaultRunPolicy`）：

- **`PolicyHostReview`**：`Isolation=workspace_write`，三类 HITL 全部 `Ask`——有 UI、宿主参与全部决策的场景。
- **`PolicyReadOnlyReview`**：`Isolation=read_only` + 三类 HITL 全部 `Ask`——审阅模式。
- **`PolicyAutonomous`**：`Isolation=unrestricted`，`Permission`/`PlanReview` = `AutoApprove`，`Question` = `AutoReject`（Question 类没有 AutoApprove，见 §1.3）。等价于旧 `RunPolicyTrusted`。

## 2. 合并规则

1. 从 `AgentBinding.Defaults().RunPolicy` 得到绑定默认。
2. 与本次 `RunOption` 中的 `WithRunPolicy` 按字段合并，零值字段继承绑定默认。
3. `HumanDecision` 也按字段合并（每个子字段独立判零）。
4. 合并时调用 `validateHumanDecisionPolicy`，非法取值（`MaxRetries<0` 等）在合并阶段即报错。
5. 合并结果写入 `resolvedInvocation` / `DriverRunRequest.Policy`，**唯一**进入 adapter。

## 3. 宿主使用三种模式

| 模式 | 何时选 | 做法 |
|------|--------|------|
| **A. 声明式 policy** | CI / 脚本 / 无 UI | 三类字段写 `AutoApprove`/`AutoReject`；不挂任何 handler |
| **B. 同步 handler** | CLI / TUI / 单元测试 | 相关 Kind 写 `Ask` + `WithPermissionHandler` / `WithPlanReviewHandler` / `WithQuestionHandler` |
| **C. 异步 channel** | 服务化 / 远程 UI | 相关 Kind 写 `Ask`，消费 `handle.DecisionRequests()`，用 `handle.ResolveDecision` 回填 |

完整示例与失败处理合同见 `docs/workstream-hitl-v2.md` §4。

## 4. 内置 adapter 映射

| 维度 | Codex | Claude | Cursor |
|------|-------|--------|--------|
| Permission=AutoApprove | `app-server`：`mapApprovalPolicy`=`never`；`exec`：`--dangerously-bypass-...` 当 `Isolation=unrestricted` | `--dangerously-skip-permissions` | `--yolo` |
| Permission=Ask | `app-server`：`on-request`（Phase 2 双向回填） | Phase 1 仅观测层识别（`ExitPlanMode` / `AskUserQuestion`），Phase 3 才真正拦截 | 不支持（`Ask:false`，Start 前报错） |
| PlanReview | — | Phase 1 观测层 + 显性失败（识别 `ExitPlanMode` tool_result） | 不支持 |
| Question | — | Phase 1 观测层；Phase 3 答复回填 | 不支持 |

`DriverDescriptor.RunPolicyCaps` 里 `Permission` / `PlanReview` 使用 `HumanDecisionSupport{Ask, AutoApprove, AutoReject, Retry}`；`Question` 使用 `QuestionSupport{Ask, AutoReject, Retry}`（无 AutoApprove）。宿主写一个 adapter 不支持的 `Ask` 值时，`Start` 前返回 `ErrHumanDecisionModeUnsupported`。

## 5. 失败模型

`RunResult.Failure` 采用结构化类型 `RunFailure`：

```go
type RunFailure struct {
    Message       string
    Code          FailureCode
    Metadata      map[string]any
    HumanDecision *HumanDecisionFailure // 非 nil 时 Code ∈ {FailureReject, FailureTimeout}
}
```

`FailureCode` 枚举：`FailureReject` / `FailureTimeout` / `FailureAgentError` / `FailureCancelled` / `FailurePolicyError`。

提供三个 nil-safe 便利方法用于粗粒度分类：

- `f.IsHumanDecision()` — 是否源自 HITL 决策
- `f.IsRejected()` — `Code == FailureReject`
- `f.IsTimedOut()` — `Code == FailureTimeout`

细粒度处理请 `switch f.Code`。宿主处理 `handle.Wait(ctx)` 返回值时按"**err → Failure → success**"三段式处理：

```go
result, err := handle.Wait(ctx)
if err != nil { /* 基础设施错误：agent 没跑完 */ }
if result.Failure != nil { /* 业务失败：agent 跑完但结果是失败 */ }
// success
```

## 6. Stream 事件

HITL v2 在 stream 通道上发 `StreamHITLRequested` + `StreamHITLResolved`：

```go
type HITLRequestedPayload struct {
    RequestID, Source string
    Kind HumanDecisionKind
    ToolCallID string
    Prompt string
    Payload map[string]any
    Choices []DecisionChoice
    CreatedAt, Deadline time.Time
    RetryAttempt int
}

type HITLResolvedPayload struct {
    RequestID, Source string
    Kind HumanDecisionKind
    RetryAttempt int
    Result DecisionResult
    Choice string
    Answer map[string]any
    ResolvedAt time.Time
    Latency time.Duration
}
```

`StreamPayload.Seq` 是 run 内部零基单调计数器（见 `docs/workstream-hitl-v2.md` §3.4.2），供持久化 / SSE 断线重连使用。

## 7. 迁移表（旧 → 新）

| 旧写法 | 新写法 |
|--------|--------|
| `RunPolicy{Approvals: ApprovalAsk}` | `RunPolicy{HumanDecision: HumanDecisionPolicy{Permission: HumanDecisionAsk}}` |
| `RunPolicy{Approvals: ApprovalAuto/Off}` | `RunPolicy{HumanDecision: HumanDecisionPolicy{Permission: HumanDecisionAutoApprove}}` |
| `RunPolicy{Trust: TrustAuto}` | `RunPolicy{HumanDecision: HumanDecisionPolicy{Permission: HumanDecisionAutoApprove}}` |
| `RunPolicyInteractive` | `PolicyHostReview` |
| `RunPolicyReadOnly` | `PolicyReadOnlyReview` |
| `RunPolicyTrusted` | `PolicyAutonomous` |

## 8. 与 profile 文档的衔接

外置 profile/持久化层若在结构中存储策略，应使用 `*RunPolicy`（与 `AGENTS` 中绑定默认字段对齐），见 `docs/profile-resolver-api.md`。
