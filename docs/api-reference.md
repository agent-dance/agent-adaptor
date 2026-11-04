# API reference

The v2 root package is `adaptor`, imported as `github.com/agent-dance/agent-adaptor/v2`. Its six nouns are **Agent, Thread, Stream, Event, Result, and Driver**. This reference follows the public declarations frozen in [`testdata/root_api.golden`](../testdata/root_api.golden). Team and A2A package contracts are covered separately in [A2A integration](./a2a.md).

## 1. Constructing an Agent

```text
New(d driver.Driver, opts ...Option) *Agent
```

`New` is the single constructor. Built-in `codex.Driver(Config)`, `claude.Driver(Config)`, `cursor.Driver(Config)`, and `codebuddy.Driver(Config)` capture their configuration; third-party Drivers use the same constructor. `Driver` is a root alias of `driver.Driver` for host fields and signatures. Extension contracts belong to `driver/`.

```go
agent := adaptor.New(
	codex.Driver(codex.Config{Model: "gpt-5.4"}),
	adaptor.WithWorkspace("/repo"),
)
```

A nil Driver panics. Environment, policy, and configuration failures surface through execution or inspection. Without `WithThreadStore`, named Threads use a private in-memory store. Direct Agent sends remain independent.

## 2. Agent and Runner

```text
type Runner interface {
    Send(ctx context.Context, text string, opts ...CallOption) Stream
}

(*Agent).Send(ctx context.Context, prompt string, opts ...CallOption) Stream
(*Agent).Thread(key string, opts ...ThreadOption) *Thread
(*Agent).Close(ctx context.Context) error
```

`*Agent` and `*Thread` implement `Runner`. Send starts one execution and immediately returns its handle; startup errors use `Stream.Result()` and terminal `Done`, with no separate error return. `Agent.Send` has the isolation semantics of a fresh anonymous Thread: input on its returned Stream joins only that execution. It does not establish a reusable named conversation.

```go
result, err := agent.Send(ctx, "Review the diff").Result()
if err != nil {
	return err
}
fmt.Println(result.Text)
```

`Close` is Agent resource shutdown. It prevents new executions, cancels and drains admitted work, reclaims persistent provider processes, and releases the hosted Tool runtime and isolated profiles. It is idempotent and bounded by its context; incomplete cleanup remains retryable. New Agent/Thread executions after it begins fail with `ErrAgentClosed`. Stream and Standby handles have `Cancel`, not `Close`.

## 3. Option scopes

| Interface | Contract | Accepted by |
|---|---|---|
| `Option` | `ApplyNew(*AgentSettings)` | `New` |
| `CallOption` | `ApplyRun(*RunSettings)` | Agent/Thread/Standby `Send`, `RunAs` |
| `SharedOption` | embeds `Option` and `CallOption` | construction and execution |
| `MessageOption` | embeds `CallOption`, sealed message marker | all sends; the only options accepted by `Stream.Send` |
| `ThreadOption` | sealed Thread-handle option | `Agent.Thread` |

Nearer scope wins; skills append; other families follow their rules below. Per-call options never mutate Agent defaults. A Standby message with any execution option waits to start a separate execution, because active execution settings are fixed.

### 3.1 Dual-scope options

All functions in this table return `SharedOption`.

| Function | Semantics and merge rule |
|---|---|
| `WithModel(string)` | Non-empty model replaces the default. |
| `WithTimeout(time.Duration)` | Execution deadline; positive values bound the entire execution, including continuations. |
| `WithSpawn()` | Force a fresh provider process for this invocation; leave no persistent writer for later invocations. |
| `WithInstructions(string)` | Replace extra instruction text. |
| `WithWorkspace(string)` | Replace base working directory. |
| `WithWorkspaceSpec(WorkspaceSpec)` | Replace provisioning strategy. |
| `WithMetadata(k, v string)` | Merge by key, replacing the same key. |
| `WithIdentity(Identity)` | Replace caller identity as a whole. |
| `WithPolicy(Policy)` | Replace the entire policy, including approvals and messages. |
| `OnApproval(ApprovalHandler)` | Replace handler; nil restores event-form routing. |
| `WithSkills(...SkillRef)` | Append; conflicting duplicate keys fail. |
| `WithMCP(...mcp.Server)` | Replace the whole set; an empty call explicitly clears it. |
| `WithProfileResources(profile.Resources)` | Skills append; non-nil MCP, Agents, Hooks, Config, and Instructions replace their respective families. Non-nil empty slices explicitly clear a family. |
| `WithServices(...ServiceSpec)` | Replace declarative services; empty explicitly clears. |
| `WithRunServices(...RunServiceProvider)` | Append, deduplicating the same provider. |

### 3.2 Construction-only options

All functions in this table return `Option`.

| Function | Semantics |
|---|---|
| `WithThreadStore(threadstore.Store)` | Replace the private memory backend; durable stores enable recovery across processes. |
| `WithTools(...tool.Definition)` | Immutable Tool set; last declaration replaces the entire set, empty clears. |
| `WithEventBuffer(int)` | Ordinary event capacity, default 1024; critical events enqueue independently. |
| `WithBlockingEvents()` | Ordinary producers wait for capacity during event consumption; cancellation releases them. |
| `WithProfile(profile.Selection)` | Provider profile strategy. |
| `WithSkillProvider(skill.Provider)` | Bare-key resolution and optional catalogue. |
| `WithSkillMaterializer(skill.Materializer)` | Skill source materialization. |
| `WithWorkspaceManager(WorkspaceManager)` | Workspace lease backend. |
| `WithServiceManager(ServiceManager)` | Runtime service backend. |

Passing these to Send fails to compile.

### 3.3 Call-only and message options

`WithSchema[T any](...SchemaOption)` and `WithSchemaJSON([]byte, ...SchemaOption)` return `CallOption`. They configure one execution's output, and cannot be passed to `New` or `Stream.Send`.

Codex native schema requests use app-server `turn/start.outputSchema`, including when only `Result()` is read. Named Threads retain normal process reuse; `WithSpawn()` forces a fresh process.

`From(Author)`, `MessageID(string)`, and `When(Timing)` return `MessageOption`. They can describe a starting prompt as well as supplemental input. Starting prompts always start immediately, irrespective of `When`. See §6.1 for delivery semantics.

### 3.4 Ecosystem options

`AgentSettings` embeds `RunSettings`. Both are controlled setter surfaces for packages implementing options; execution state is private. Setters use the same merge semantics as the corresponding options.

| Receiver | Exported setters |
|---|---|
| `*RunSettings` (also promoted on `*AgentSettings`) | `SetModel(string)`, `SetTimeout(time.Duration)`, `SetSpawn()`, `SetInstructions(string)`, `SetWorkspace(string)`, `SetWorkspaceSpec(WorkspaceSpec)`, `SetMetadata(k, v string)`, `SetIdentity(Identity)`, `SetPolicy(Policy)`, `SetApprovalHandler(ApprovalHandler)`, `AddSkills(...skill.Ref)`, `SetMCPServers([]mcp.Server)`, `SetServices([]ServiceSpec)`, `AddRunServiceProvider(RunServiceProvider)` |
| `*RunSettings`, resource/schema extensions | `SetAgents([]driver.AgentSpec)`, `SetHooks([]driver.HookSpec)`, `SetConfigPatches([]driver.ProfileConfigPatch)`, `SetInstructionsBundle(*driver.InstructionsBundleRef)`, `SetOutputSchema(driver.OutputSchema)`, `SetOutputSchemaError(error)` |
| `*AgentSettings` only | `SetThreadStore(threadstore.Store)`, `SetEventBuffer(int)`, `SetBlockingEvents()`, `SetProfile(profile.Selection)`, `SetSkillProvider(SkillProvider)`, `SetSkillMaterializer(SkillMaterializer)`, `SetWorkspaceManager(WorkspaceManager)`, `SetServiceManager(ServiceManager)` |

Schema derivation errors are sticky: setting a later schema does not clear an earlier derivation error. Consumers normally use the public option functions instead of these setters.

## 4. Policy and Identity

`Identity` has `ID`, `Tenant`, `Profile`, and `Name` string fields. `IdentityFromContext(context.Context) (Identity, bool)` reads the effective identity passed to host hooks. Identity scopes host resource lookups; it does not select a Driver or route a task.

`Policy` contains `Messages MessagePolicy`, `Sandbox SandboxLevel`, `WebSearch FeatureLevel`, `Browser FeatureLevel`, and `Approvals ApprovalPolicy`.

| Type | Values |
|---|---|
| `SandboxLevel` | `SandboxInherit` (`""`), `ReadOnly`, `WorkspaceWrite`, `Unrestricted` |
| `FeatureLevel` | `FeatureInherit` (`""`), `FeatureAllow`, `FeatureDeny` |
| `ApprovalMode` | `ApprovalInherit`, `ApprovalAsk`, `ApprovalAutoApprove`, `ApprovalAutoDeny` |
| `QuestionMode` | `QuestionInherit`, `QuestionAsk`, `QuestionAutoDeny` |
| `FallbackAction` | `FallbackInherit`, `FallbackAbort`, `FallbackContinue`, `FallbackRetry` |

`PolicyReadOnly`, `PolicyWorkspaceWrite`, and `PolicyUnrestricted` set only Sandbox. Explicit sandbox/search/browser or approval modes require corresponding Driver support; inherit values remain portable. A valid unsupported dimension produces `ErrPolicyCapabilityUnsupported`, an unsupported approval mode produces `ErrHumanDecisionModeUnsupported`, and malformed fields produce `ErrInvalidPolicy`.

`ApprovalPolicy` aliases `driver.HumanDecisionPolicy`: Permission and PlanReview default to Ask; Question defaults to auto-deny; Timeout defaults to 30 seconds; OnTimeout and OnReject default to abort; MaxRetries defaults to 3. A negative Timeout removes the practical deadline, and negative MaxRetries is invalid. `ApprovalsAutoDeny` explicitly requires auto-deny support for all three kinds. [Run policy](./run-policy.md) has the capability matrix.

### 4.1 Message policy

| `MessagePolicy` field | Contract |
|---|---|
| `Disabled bool` | Omit the Driver message port and reject supplemental sends with `ErrMessageUnsupported`. Starting prompts remain usable. |
| `Format func(MessageContext) string` | Replace built-in supplemental framing; nil uses author-aware templates. |
| `MaxPending int` | Unclaimed-message limit; zero means 32. A full queue blocks Send until space or context cancellation. |
| `MaxTurns int` | Provider turns per execution; zero means 20. |

Negative limits are invalid. `MessageContext` has `ID string`, `Text string` (original), `From Author`, and `Timing Timing`. Built-in framing preserves the original objective, distinguishes human/peer/system authority, and asks for one final answer without a separate acknowledgement. The starting prompt is unframed.

With MaxTurns at least 2 and continuation available, core emits `Notice{Kind: "limit.approaching"}` after `min(ceil(0.8 * MaxTurns), MaxTurns - 1)` completed turns. System wrap-up guidance only accompanies an existing continuation batch when it fits the resolved framed size limit. It never starts a turn alone or displaces host input. At the limit, no new turn starts; pending messages fail with `MessageLimitExceeded` and appear in `Undelivered()`. The single-agent result can still succeed. This is a turn limit, independent of execution timeouts or provider-specific internal-step limits.

## 5. Thread

```text
(*Agent).Thread(key string, opts ...ThreadOption) *Thread
ResumeOnly() ThreadOption
(*Thread).Key() string
(*Thread).Send(ctx context.Context, prompt string, opts ...CallOption) Stream
(*Thread).Standby(ctx context.Context) (Standby, error)
(*Thread).Fork(newKey string) *Thread
(*Thread).Checkpoint(ctx context.Context) (*Checkpoint, error)
```

A non-empty key is the host's opaque identity, stored and compared verbatim. Empty Thread or Fork keys panic. Allocate a new key to deliberately start an unrelated conversation.

| Action | Contract |
|---|---|
| `agent.Thread(key)` | Continue an active compatible record, otherwise start; safe resume rejection can fall back once to a fresh session. |
| `agent.Thread(key, ResumeOnly())` | Fail if missing, incompatible, or resume is rejected. |
| `parent.Fork(newKey)` | Resolve the parent at the child's first execution; validate checkpoint and compatibility, and reject an occupied target. Parent remains active and unchanged. |
| `Checkpoint(ctx)` | Audit the latest healthy resume state; pending in-process storage recovery takes precedence over stale durable state. |

The lease protects one writer. Task Send during another task or a live Standby fails with `ErrThreadBusy` through Result/Done. A separate process with the same store is excluded by the ownership lease. Compatibility covers Driver/config, model, identity, resolved workspace, materialized resources, skills, instructions, MCP, and runtime-service environment. Failed/cancelled/protocol-invalid runs cannot replace a healthy provider checkpoint.

Storage failures retain the latest checkpoint in process, retry persistence under validated ownership, and report `thread.persistence` notices. Resume rejection or incompatible continue-or-start emits `thread.continuity`; a fresh session receives bounded earlier supplemental guidance and up to three recent assistant turns (under an 8 KiB total prefix budget). This is limited recovery context, not full UI chat history. Receipts and bounded guidance are stored alongside checkpoints, fingerprints, and leases.

### 5.1 Provider process lifecycle

Explicit Threads allow persistent processes for Codex, Claude, and CodeBuddy; Cursor and direct Agent tasks start fresh processes. `WithSpawn()` forces a fresh process for an invocation and does not register it for later reuse. The Driver maintains a single writer across configuration changes and temporary transport changes. A prompt that may already have been delivered is not automatically replayed as a process-start fallback.

`Agent.Close` reclaims Agent-owned processes and Tools. Providers declare persistent support in `Descriptor.Process.Persistent` and implement `driver.ProcessLifecycleDriver` when supported.

### 5.2 Standby and ThreadRunner

```text
type Standby interface {
    Send(ctx context.Context, text string, opts ...CallOption) error
    Events() <-chan Event
    Cancel()
}

type ThreadRunner interface {
    Runner
    Key() string
    Standby(ctx context.Context) (Standby, error)
    Checkpoint(ctx context.Context) (*Checkpoint, error)
}
```

`*Thread` implements `ThreadRunner`. Standby is a conversation handle with no overall Result and no RunID method. Each execution has its own `Event.Meta().RunID` and final Done; each provider turn has TurnEnd.

Open it with a server-lifetime context. `Standby.Send` contexts bound admission only: an HTTP request ending after acceptance does not withdraw the input or cancel processing. While active, message-only sends join current processing; while idle they start an execution. Any execution option queues a separate execution. Repeat `Standby(ctx)` on the same Agent/key returns the existing live handle; the first context still owns its lifetime.

`Cancel()` cancels current processing, fails queued input with `MessageCancelled`, and keeps Standby accepting input. End the lifetime context to shut it down and release ownership. Events are discarded before the first `Events()` call; after subscription, normal backpressure applies. The channel is single-consumer; broadcasting belongs to the host.

A failed execution does not end Standby. Classified rate-limit/network/provider-server failures with undelivered work retry at most once, using structured retry-after hints or a bounded fallback delay. Other failures do not automatically retry. Undelivered `run_failed` messages carry under their original IDs into the next execution, so one ID can report failed and later delivered; carried input waits for the next host message or an eligible retry. Cancellation clears pending work instead of carrying it.

## 6. Stream and Event

```text
type Stream interface {
    Send(ctx context.Context, text string, opts ...MessageOption) error
    Events() <-chan Event
    Result() (*Result, error)
    RunID() string
    Cancel()
}
```

`RunID()` is available immediately. `Result()` is concurrency-safe and repeatable. `Done` is always last, after finalization, and contains exactly the result pointer/error returned by Result. Its Result is nil on a business failure; inspect `Done.Err` as `*RunError` for partial output. `TurnEnd` is a per-turn observation before execution finalization and does not prove checkpoint persistence or cleanup succeeded. Events close after terminal delivery; Result is then available.

Call Events before progress consumption. Calling Result without ever calling Events releases ordinary blocking producers and retains critical events plus a Dropped summary for a later Events call. A late subscriber after completion receives that critical log, not a full ordinary-event replay. `Cancel` is idempotent, unblocking producers, approval waits, and pending input; drain remaining critical events after cancelling.

### 6.1 Sending messages

| Option/type | Contract |
|---|---|
| `When(Soon)` | Default; earliest declared boundary, with turn-end continuation as fallback. |
| `When(AfterTurn)` | Only at turn end, within this execution. |
| `When(Interrupt)` | Abort the current provider turn and continue with the input; requires Interrupt capability. |
| `MessageID(string)` | Opaque Thread-scoped idempotency key; empty generates an ID. |
| `From(Author)` | AuthorKind plus optional ID and Name; defaults to AuthorHuman. |

`Timing` is uint8 (`Soon=0`, `Interrupt=1`, `AfterTurn=2`). `AuthorKind` is uint8 (`AuthorHuman=1`, `AuthorAgent=2`, `AuthorSystem=3`). `Author` contains `Kind AuthorKind`, `ID string`, and `Name string`.

```go
stream := agent.Send(ctx, "Implement the fix")
if err := stream.Send(ctx, "Keep compatibility with the existing config",
	adaptor.MessageID("comment-17"),
	adaptor.From(adaptor.Author{Kind: adaptor.AuthorHuman, ID: "reviewer"}),
	adaptor.When(adaptor.Soon),
); err != nil {
	return err
}
result, err := stream.Result()
if err != nil {
	return err
}
fmt.Println(result.Messages())
```

Nil means admission, not delivery. Messages can be admitted before provider launch. Prelaunch checks use the static descriptor; resolved transport can later fail queued input, preserving its text. Unsupported timing (including Interrupt), disabled messaging, oversized framed text, cancellation, and a finished execution return `*MessageError`. A Stream send context withdraws input only while unclaimed; a claimed item is settled by the Driver acknowledgement. Input is ordered within each delivery boundary; timing can defer an earlier AfterTurn behind later Soon input.

Delivered IDs are retained for up to seven days, at most 1000 receipts. A durable store makes that deduplication survive restarts. A duplicate starting prompt completes with zero Turns and empty Text. A crash between provider acceptance and receipt persistence can replay input. An in-run-only third-party Driver with no resume cannot recover input admitted at the final boundary race; it reports failure rather than silently losing it.

### 6.2 Event envelope and payloads

`Event` is sealed and exposes `Meta() EventMeta`. `EventMeta` fields are `RunID`, `ThreadKey`, `Sequence uint64`, `Time time.Time`, `TurnID`, and `Source *EventSourceMeta`. Source preserves provider `RunID`, `ThreadID`, `TurnID`, `Sequence`, and `Timestamp`; SDK Sequence is the receive-order authority and increases within one execution. ThreadKey is empty for direct Agent tasks. `WithEventMeta(Event, EventMeta) Event` restores coordinates on replay; live sinks overwrite them.

| Event | Exported payload fields |
|---|---|
| `TextDelta` | `MessageID`, `Text`, `Role Role`, `Phase Phase` |
| `Thinking` | `MessageID`, `Text`, `Phase` |
| `ToolCall` | `ID`, `Name`, `Args map[string]any`, `ArgsDelta`, `Result map[string]any`, `Phase` |
| `ToolResult` | `ID`, `Result map[string]any` |
| `RunStarted` | `RunID`, `ThreadID` (lifecycle identifiers, with an SDK fallback; use Meta for SDK identity) |
| `RunFinished` | `RunID`, `ThreadID`, `Usage *Usage`, `Failed bool`, `Reason FailureReason`, `Message`; informational |
| `MessageUpdate` | `ID`, `State MessageState`, `Turn int`, `Boundary`, `Mechanism`, `Reason MessageFailure`, `Text` (original on failure) |
| `TurnEnd` | `Turn int`, `Result *Result`, `Err error`, `MessageIDs []string` |
| `Done` | `Result *Result`, `Err error`; authoritative final event |
| `ProcessInfo` | `Kind`, `Text`, `Bytes []byte`, `Metadata map[string]string`, `Data map[string]any` |
| `Notice` | `Kind`, `Text`, `Item *TranscriptItem`, `Metadata map[string]string`, `Data map[string]any` |
| `Dropped` | `Count int`, `ByKind map[string]int`, `FirstSequence`, `LastSequence uint64`, `Reason`, `Source`, `Details map[string]any` |
| `*ApprovalRequest` | Approval payload and live responder, §7 |
| `SubagentUpdate` | `Task`, `Member`, `State`, `Summary`, `Kind SubagentEventKind`, `Delta`, `Data map[string]any`; package-level orchestration is documented separately |

Unannotated payload names above are strings. Turn numbers are one-based; message Turn zero means unassigned. `TurnEnd.MessageIDs` lists delivered messages associated with that turn in admission order.

`Role` values: `RoleAssistant` (zero/empty) and `RoleUser` (`"user"`, synthesized by hosts/bridges). `Phase` values: `PhaseContent` (zero/empty), `PhaseStart`, `PhaseEnd`; content fields are empty on text/thinking boundaries. `ProcessSpawn`, `ProcessStdout`, and `ProcessStderr` identify process kinds. Subagent kinds are `SubagentStarted`, `SubagentDelta`, `SubagentMessage`, `SubagentState`, `SubagentFinished`.

`MessageState` values are `MessageAccepted=1`, `MessageDelivered=2`, and `MessageFailed=3`. Delivery says the provider accepted input at the reported boundary, not that it followed the instruction. A requeue can report Accepted again with the attempted boundary/mechanism. `MessageFailure` values: `MessageCancelled` (`cancelled`), `MessageProviderRejected` (`provider_rejected`), `MessageTimeout` (`timeout`), `MessageLimitExceeded` (`limit_exceeded`), `MessageRunFailed` (`run_failed`).

### 6.3 Notices and backpressure

| Notice selector | Meaning |
|---|---|
| `NoticeInvocation` (`invocation`) | Resolved invocation information. |
| `NoticeLifecycle` (`lifecycle`) | Lifecycle/diagnostics; inspect optional metadata/data fields below. |
| `NoticeRuntime` (`runtime`) | Runtime service preparation/cleanup. |
| `NoticeStep` (`step`) | Provider work step; `Data["phase"]` is started/finished. |
| `NoticeTranscriptItem` (`transcript.item`) | Progressive normalized item in Item. |
| `NoticeApprovalRequested` / `NoticeApprovalResolved` | Approval audit; Data includes request_id, kind, attempt, and outcome. |
| Kind `limit.approaching` | Turn-limit warning and wrap-up request. |
| Kind `thread.continuity` | Configuration drift or provider resume rejection caused a fresh session. |
| Kind `thread.persistence` | Store failure; newest checkpoint retained in process for retry. |
| Kind `thread.ownership` | Standby ownership-release failure. |
| `NoticeLifecycle`, `Metadata["code"] == "cursor_hook_unavailable"` | Cursor hooks unavailable; delivery uses turn-end resume. |
| `NoticeLifecycle`, `Metadata["code"] == "claude_hooks_unavailable"` | Claude hook initialization failed; pending input falls back to resume. |
| `NoticeLifecycle`, `Data["warning"] == "human_decision_retry_unsupported"` | Requested approval retry degrades to abort. |

Codes are in the fields listed, not a `Notice.Code` field. Unknown notice kinds/extensions pass through. The immediate unobserved-approval fallback is a lifecycle notice with explanatory Text; it has no dedicated machine code.

During normal event consumption only text/thinking/tool-argument content deltas, stdout/stderr chunks, and subagent deltas may drop. Lifecycle boundaries, tool results, notices, approvals, MessageUpdate, TurnEnd, Done, and drop reports are reliable. WithBlockingEvents blocks ordinary producers but never critical publications; Result-only and cancellation paths can discard ordinary traffic with a loss summary. See [Streaming](./streaming.md) for the full contract.

## 7. Approval

`ApprovalHandler` is `func(context.Context, *ApprovalRequest) error`. Install it through `OnApproval`, or subscribe to Events for live `*ApprovalRequest` values. Without a handler, a request that arrives before Events waits for Events or Result; when Result comes first, the timeout fallback runs immediately with a notice.

`ApprovalRequest` fields: `ID`, `RunID`, `Kind ApprovalKind`, `Title`, `Source`, `ToolCallID`, `Choices []Choice`, `Details map[string]any`, `CreatedAt`, `Deadline time.Time`, and `Attempt int` (zero-based). `Choice` aliases `driver.DecisionChoice`.

```text
(*ApprovalRequest).Approve(ctx context.Context) error
(*ApprovalRequest).Deny(ctx context.Context, reason string) error
(*ApprovalRequest).Answer(ctx context.Context, option string) error
(*ApprovalRequest).Origin() Origin
(*ApprovalRequest).Forward(o Origin) *ApprovalRequest
```

Kinds are `ApprovalPermission`, `ApprovalPlanReview`, and `ApprovalQuestion`. Approve handles binary kinds, Answer handles Question (choice key or free text), and Deny handles all kinds. `ApproveAll()` approves binary requests and denies questions; `DenyAll(reason)` denies all requests. They do not change capability admission.

Responses are exactly-once, including copies. Duplicate, expired, mismatched, or unbound requests return `ErrApprovalResolved`, `ErrApprovalExpired` (also matches Resolved), `ErrApprovalKindMismatch`, or `ErrApprovalUnavailable` promptly. Timeout/reject/retry policy belongs only to Policy.Approvals.

`Origin{Member string, Task string}` is zero for a single Agent. `Forward` returns an attributed copy sharing the same responder, with copied Choices and Details; responding through either copy resolves both. A nil receiver returns nil from Forward and zero Origin. Origin supports attribution, not a second approval policy.

## 8. Result and errors

| `Result` field | Contract |
|---|---|
| `Turns int` | Provider turns in this execution; zero for a deduplicated starting prompt. |
| `RunID string` | SDK execution ID. |
| `Model`, `Provider string` | Effective provider-reported identities. |
| `Text string` | Final provider turn's assistant-facing answer. |
| `Summary string` | Optional short host-facing label. |
| `Usage *Usage` | Sum of observed turn usage; nil means unobserved, non-nil zero means observed zero. |
| `Metadata map[string]string` | Driver-reported metadata. |

```text
(*Result).Raw() RawStreams
(*Result).Transcript() []TranscriptItem
(*Result).Services() []ServiceReport
(*Result).Decode(v any) error
(*Result).Messages() []MessageOutcome
(*Result).Undelivered() []MessageOutcome
(*Result).Subagents() []SubagentResult
```

Raw has complete stdout/stderr across provider invocations and the last recognized terminal payload in `Terminal`; there is no public truncation or spill-to-disk policy. `TerminalPayload` exposes `Event string` (provider-native event name) and `JSON json.RawMessage`. `Usage` exposes `InputTokens`, `OutputTokens`, `CachedInputTokens int`, and `EstimatedCostMilli int64`. Raw never becomes Text, and Summary is not appended to Text. Audit methods return copies; retain a returned copy when reusing it.

`TranscriptItem` exposes `Kind driver.TranscriptKind`, `Text`, `Delta bool`, `ToolUseID`, `ToolName`, `Input any`, `IsError bool`, `Model`, `SessionID`, `Usage *Usage`, `CostUSD *float64`, `Subtype`, `Errors []string`, `Metadata map[string]string`, `Data map[string]any`, and `Message *TranscriptMessage`. Provider entries come from official parsers. Core appends actual supplemental deliveries with Kind `"message"` and `TranscriptMessage{ID, From Author, Mechanism, Boundary, Text}`; these do not claim a provider user role.

`MessageOutcome` exposes `ID`, original `Text`, `From Author`, `Timing Timing`, `State MessageState`, `Turn int`, `Boundary`, `Mechanism`, and `Reason MessageFailure`. Messages returns final outcomes in admission order; Undelivered filters failed outcomes, preserving original text. Services reports ensured/observed services, without echoing secret env values or MCP input declarations.

`SubagentResult` contains `Member`, `Task`, `State`, `Summary`, `Usage *Usage`, and `Unread []string`. Subagents returns copied summaries; a single Agent normally has none. Package orchestration details remain outside this reference.

### 8.1 One error verdict

Success is `(*Result, nil)`. Business failure is `(nil, *RunError)` with a non-nil `RunError.Result`. Infrastructure failure is `(nil, error)`. Done uses the identical pair. `RunError` exposes `Reason FailureReason`, `Message string`, `Details map[string]any`, and `Result *Result`, with Error and Unwrap methods.

| FailureReason | Sentinel |
|---|---|
| `ReasonApprovalDenied` | `ErrApprovalDenied` |
| `ReasonApprovalTimeout` | `ErrApprovalTimeout` |
| `ReasonAgentError` | `ErrAgentFailed` |
| `ReasonCancelled` | `ErrRunCancelled` |
| `ReasonPolicyViolation` | `ErrPolicyViolation` |

`MessageError{ID, Text string, Reason MessageFailure, Err error}` is a send-admission error with Error/Unwrap. `errors.Is` finds `ErrRunFinished`, `ErrMessageUnsupported`, or the context cause; `errors.As` recovers the rejected original text. An accepted message's later delivery failure is reported through MessageUpdate/outcomes and need not make the execution fail.

Other root sentinels:

- Lifecycle: `ErrAgentClosed`.
- Thread: `ErrThreadNotFound`, `ErrThreadBusy`, `ErrThreadIncompatible`, `ErrThreadLeaseLost`, `ErrThreadCheckpointMissing`, `ErrThreadAlreadyExists`, `ErrResumeRejected`.
- Approvals: the four responder errors in §7.
- Skills: `ErrSkillNotFound`, `ErrSkillKeyConflict`, `ErrSkillMaterializationFailed`, `ErrSkillSourceMissing`, `ErrSkillKeyMissing`.
- MCP: `ErrInvalidMCPConfig`, `ErrMCPUnsupported`, `ErrMCPTransportUnsupported`.
- Configuration/schema/policy: `ErrInvalidDriverConfig`, `ErrInvalidOutputSchema`, `ErrStructuredOutputUnsupported`, `ErrInvalidPolicy`, `ErrPolicyCapabilityUnsupported`, `ErrHumanDecisionModeUnsupported`.

Root typed aliases for diagnostics are `SkillKeyConflictError`, `SkillMaterializationError`, `InvalidDriverConfigError`, `InvalidOutputSchemaError`, `StructuredOutputUnsupportedError`, `InvalidPolicyError`, `PolicyCapabilityUnsupportedError`, and `HumanDecisionModeUnsupportedError`. Match with errors.Is/As rather than strings. Bare context errors are separate from Driver-classified cancellation. See [Public errors](./public-errors.md).

## 9. Structured output

```text
RunAs[T any](ctx context.Context, r Runner, prompt string, opts ...CallOption) (T, *Result, error)
WithSchema[T any](opts ...SchemaOption) CallOption
WithSchemaJSON(schemaJSON []byte, opts ...SchemaOption) CallOption
```

RunAs adds WithSchema, calls Send(...).Result(), and decodes. Core prefers native JSON Schema, otherwise exact-JSON prompting with local validation; unavailable combinations fail before launch. Callers do not choose a mode. Decode uses validated structured bytes; without a schema it treats Text as JSON.

`SchemaOption` functions: `SchemaName(string)`, `SchemaDescription(string)`, `SchemaReturnInvalid()`, `SchemaInlineReferences()`, `SchemaAllowAdditionalProperties()`, `SchemaRequireExplicitTags()`, and `SchemaUseGoComments(base, path string)`. All return SchemaOption. Default invalid output fails with ErrPolicyViolation; SchemaReturnInvalid preserves a successful result whose Decode still fails. Generation settings apply only to Go-derived schemas. See [Structured output](./structured-output.md).

## 10. Inspect and profile state

```text
(*Agent).Inspect() Inspector
(Inspector).Environment(context.Context) (EnvironmentReport, error)
(Inspector).Models(context.Context) ([]ModelInfo, error)
(Inspector).Quota(context.Context) (QuotaReport, error)
(Inspector).ConfigSchema(context.Context) (*ConfigSchema, error)
(Inspector).Skills(context.Context) (SkillSnapshot, error)
(*Agent).ProfileState(context.Context) (ProfileSnapshot, error)
(*Agent).SyncProfile(context.Context) (ProfileSnapshot, error)
(*Agent).SelectSkills(context.Context, []string) (SkillSnapshot, error)
```

Inspection uses captured Driver configuration. Missing probes return descriptor-derived or explicitly unsupported/unavailable data. EnvironmentReport contains health Status/Checks, the detected CLI Version, and effective Messages capability. Built-in Codex, Claude, and Cursor probes and resolvers trim in-run message delivery below a known MinVersion, with one execution Notice and checkpoint-resume fallback. Unknown versions keep declared support with a warning; Inspect never starts a model task.

ProfileState observes desired versus materialized state; SyncProfile materializes resources. SelectSkills installs a process-local key selection after successful resolution/sync; unknown keys leave selection unchanged. Inline skills must first be introduced through WithSkills, and can then be selected by key.

`ProfileSnapshot` fields: `DriverType`, `Profile AgentProfile`, `Kind ProfileKind`, `Fingerprint`, `Resources []ResourceSnapshot`, `Warnings []string`. ResourceSnapshot fields: `Kind ProfileResourceKind`, `Fingerprint`, `Managed`, `External []string`, `Support ProfileResourceSupport`, `Materialization ProfileResourceMaterialization`, `Warnings []string`, `Error string`.

| Type | Constants |
|---|---|
| `ProfileKind` | `ProfileKindShared`, `ProfileKindHostManaged` |
| `ProfileResourceKind` | `ProfileResourceSkills`, `ProfileResourceMCP`, `ProfileResourceAgents`, `ProfileResourceHooks`, `ProfileResourceInstructions`, `ProfileResourceConfig` |
| `ProfileResourceSupport` | `ProfileResourceSupportPortableCore`, `ProfileResourceSupportPortableExtended`, `ProfileResourceSupportNativeEscape`, `ProfileResourceSupportFallback`, `ProfileResourceSupportUnsupported` |
| `ProfileResourceMaterialization` | `ProfileResourceMaterializationNativeManaged`, `ProfileResourceMaterializationFileManaged`, `ProfileResourceMaterializationPromptInjected`, `ProfileResourceMaterializationFallback`, `ProfileResourceMaterializationNotMaterialized` |

Root aliases of Driver-owned reports are `EnvironmentReport`, `EnvironmentCheck`, `ModelInfo`, `QuotaReport`, `ConfigSchema`, `SkillSnapshot`, and `AgentProfile`. See [`driver/environment.go`](../driver/environment.go) and Driver godoc for their report fields.

## 11. The tool, skill, MCP, and profile vocabulary packages

### 11.1 tool

`tool.Define` takes typed handlers. Input/output schemas are inferred, with `tool.InputSchemaJSON` and `tool.OutputSchemaJSON` overrides. `tool.Reject` creates a model-visible expected failure; `tool.AsRejection` recognizes only package-minted rejections. Other errors/panics are sanitized. WithTools is construction-only; each Tool used by a Thread needs a semantic Revision. See [Tools](./tools.md).

### 11.2 skill

`skill.Dir`, `skill.FS`, `skill.Inline`, `skill.Archive`, `skill.Key`, and `skill.Require` produce WithSkills inputs. Root `SkillRef`, `SkillProvider`, and `SkillMaterializer` alias leaf contracts. `skill.Set` is a static Provider; optional `skill.Catalog` enables catalogue inspection. Archive materialization supports zip/tar/tgz with configurable safety limits through `skill.NewDefaultSkillMaterializer`. Resolution/materialization failures stop launch.

### 11.3 MCP

`mcp.Stdio`, `mcp.HTTP`, and `mcp.SSE` declare servers. Options include Args, Env, WithHeader, WithHeaders, WithBearerTokenEnv, and Required. Invalid transport/field combinations fail before launch. WithMCP replaces the explicit set; typed runtime-published servers append. Hosted Tools have their own Agent-owned declaration and survive a per-call WithMCP clear.

### 11.4 profile

Selections are `profile.Default`, `profile.Native`, `profile.Dedicated`, `profile.CloneNative`, and `profile.CloneFrom`. Clone options include CopySettings, CopyMCP, CopySkills, CopyAuth, LinkAuth, and WithOptions. Profile identity is construction-only. `profile.Resources` supplies Skills, MCP, Agents, Hooks, Instructions, and Config; WithProfileResources uses the family-specific merge rules in §3.1.

## 12. Workspace and runtime services

`WorkspaceSpec` is sealed: `SharedWorkspace{}`, `GitWorktreeWorkspace{BaseRef, BranchTemplate, WorktreeParentDir string}`, or `DriverManagedWorkspace{}`. WithWorkspace sets the base directory; a spec describes provisioning. Without a WorkspaceManager, the passthrough backend leases the base directory unchanged; it does not create a worktree.

```text
type WorkspaceManager interface {
    Resolve(ctx context.Context, req WorkspaceRequest) (WorkspaceLease, error)
    Release(ctx context.Context, lease WorkspaceLease, mode WorkspaceReleaseMode) error
}

type ServiceManager interface {
    Ensure(ctx context.Context, req ServiceRequest) ([]ServiceRef, error)
    ReleaseByRun(ctx context.Context, runID string) error
    ReleaseByLabels(ctx context.Context, labels map[string]string) error
}

type RunServiceProvider interface {
    AttachRun(ctx context.Context, runID string) (RunAttachment, error)
    DetachRun(ctx context.Context, runID string) error
}
```

`WorkspaceRequest` has `BaseCWD string`, `Spec WorkspaceSpec`, `Metadata map[string]string`. Release modes are `WorkspaceReleaseKeep` and `WorkspaceReleaseStop`. `WorkspaceLease` aliases the Driver DTO.

`ServiceRequest` has `RunID`, `DriverType`, `Agent Identity`, `Config any`, `Workspace WorkspaceLease`, `Desired []ServiceSpec`, and `Metadata map[string]string`. `ServiceSpec`, `ServiceRef`, and `ServiceReport` alias Driver runtime DTOs. WithServices without a manager does not invent endpoints. ServiceRef.MCP publishes typed MCP; SecretEnv reaches subprocesses only.

`RunAttachment` has `Preamble string` (excluded from Thread compatibility), `Results func() []SubagentResult` (collected before detach), `Services []ServiceRef`, and `Events RunEventSource`. RunEventSource is `func(context.Context, string) <-chan Event`, folded into the execution's one event stream. Sources flush and close when cancelled; core filters source RunStarted/RunFinished. Cleanup is bounded and errors join the final outcome before Done; RunFinished remains informational.

## 13. threadstore and memory

`memory.NewStore()` is the concurrency-safe backend installed by default per Agent. Provide a shared durable `threadstore.Store` for cross-process continuation, leases, receipt deduplication, and bounded continuity history.

Store methods are Resolve, Finalize, AcquireLease, RenewLease, and ReleaseLease. Finalize must atomically validate owner/token/lease, save, archive/rebind, and enforce absent-target conditions for Fork. Implement the current [`threadstore` contract](../threadstore/threadstore.go), including delivered-message receipts, instead of persisting only a provider session ID.

## 14. Built-in provider Drivers

Each provider exports `Driver(cfg Config) driver.Driver`, taking a copied configuration snapshot.

| Package | Main Config fields |
|---|---|
| `codex` | CommonConfig, Model, ReasoningEffort, FastMode |
| `claude` | CommonConfig, Model, Effort, MaxTurnsPerRun |
| `cursor` | CommonConfig, Model, Mode, HookRelay (`[]string` override) |
| `codebuddy` | CommonConfig, Model, Effort, PermissionMode, MaxTurnsPerRun |

CommonConfig aliases driver.CommonConfig and includes command, directory, environment, instructions/templates, workspace defaults, timeouts, grace period, and extra args. Use WithModel for uniform per-call model overrides. Cursor hosts call `cursor.RunHookRelayIfRequested()` at the beginning of main for tool-boundary delivery; see the [message matrix](./streaming.md#12-message-delivery-capabilities). Pi and OpenCode are suspended and are not v2-capable integrations.

## 15. Driver SPI and adaptertest

Drivers implement Descriptor, ValidateConfig, and the Driver-level Run method. Core owns resolution, message queue/accounting, Thread coordination, and final results. Drivers own official protocol parsing, output layers, healthy checkpoints, and actual message-boundary delivery.

Optional interfaces include EnvironmentProbe, ModelLister, ModelDetector, ProfileReporter, SessionCodecProvider, SessionConfigFingerprinter, ConfigSchemaProvider, QuotaProbe, SkillSupport, StreamSupport, ProcessLifecycleDriver, and MessageCapabilityResolver. Resume support requires a non-nil SessionCodec and stable non-empty SessionConfigFingerprint before Thread resources are acquired.

[`driver/messages.go`](../driver/messages.go) is the frozen message SPI. Descriptor.Messages declares boundaries, Interrupt, Continue mode, MaxUnits/Unit, ConfirmedDelivery, PersistsDelivered, and MinVersion. An invocation resolver narrows that declaration. MessagePort claims only declared boundaries, and every claimed item must be acknowledged exactly once. A boundary-specific failure can requeue for turn-end continuation; ambiguous attempts are marked as possible duplicates. ContinueInRun drives provider turns internally; ContinueResume uses a healthy checkpoint through core. These are transport choices within one execution.

ProfilePayload.Fingerprint identifies concrete desired materialization; SessionFingerprint separately guards resume/persistent-process compatibility, normalizing only supported ephemeral allocations. Resumed invocations must refresh the complete current request.

Use `adaptertest.TestDriver` and its verification helpers for conformance, including message lifecycle and capability truthfulness. `adaptertest.NewReferenceDriver` is in-memory. Live calls require explicit opt-in; ordinary conformance does not call paid providers.

## 16. Composite execution hooks

These hooks let composite Runners such as [teams](./team.md) reuse core semantics instead of reimplementing them.

```go
func ComposeResults(runID string, parts ...*Result) *Result
func (r *Result) WithSubagents(subagents []SubagentResult) *Result
func MessageAttributes(opts ...CallOption) MessageContext
func (s *RunSettings) CapMessageTurns(remaining int)
```

ComposeResults uses the same aggregation as core continuation executions:
last text/summary/model/provider/metadata/structured output, summed usage and
turns, ordered audit/message/subagent slices, concatenated raw streams and the
last observed terminal payload. Returned data is detached from children.
Message turn numbers are rebased onto the composite sequence.

Composite Runners use WithSubagents after ComposeResults when their final task
snapshot includes tasks that completed or were cancelled after the last child
result was collected. It returns a detached copy with the supplied subagent
snapshot replacing the collected summaries, preserving all other result fields.
Usage stays unchanged: the composite Runner accounts for observed child usage
once, and replacing summaries does not add that usage again. A nil receiver is
treated as an empty result. Teams apply the final snapshot on success,
cancellation, failure and limit termination.

MessageAttributes reads only message options, with later values overriding
earlier ones, AuthorHuman/Soon defaults and no generated ID. Execution options
are ignored without applying them. A composite call option can use
CapMessageTurns after caller options to lower the inherited message turn
budget while preserving every other policy field. Nonpositive budgets panic.

ReasonLimitExceeded classifies work aborted by a team limit. The single-Agent
turn-cap path completes normally and returns pending messages with
MessageLimitExceeded. Named task-mode Team.Thread.Send rejects an active
execution or live standby with adaptor.ErrThreadBusy; Standby.Send performs
ongoing routing. Stream.Result and Done carry the same authoritative result.

## 17. Bridge routing and event recording

The optional packages below consume the public Runner, Stream, Event and Result contracts; they do not add a second execution pipeline.

`bridges/agui` exports `ThreadKey(threadID string) string` for collision-free frontend identity mapping, plus a concurrency-safe `RunRegistry` whose zero value is usable:

```text
(*RunRegistry).Active(threadKey string) adaptor.Stream
(*RunRegistry).Send(ctx context.Context, thread adaptor.ThreadRunner, input *RunAgentInput, opts ...adaptor.CallOption) (adaptor.Stream, bool, error)
(*RunRegistry).SendActive(ctx context.Context, threadKey string, input *RunAgentInput) (adaptor.Stream, error)
(*RunRegistry).Forget(threadKey string, stream adaptor.Stream)
```

Send begins Thread.Send when idle and uses Stream.Send when active; its bool reports supplementation. SendActive never starts a run and returns a MessageError matching ErrRunFinished when no active execution exists. Supplements use the latest user ID/human author/Soon timing and keep receipts on the original event stream. RunRegistry passes ctx unchanged to Stream.Send, so cancellation can withdraw unclaimed input even after Send returns nil; claimed input is settled by the Driver. Hosts must explicitly provide a detached context when choosing to let protocol input outlive an HTTP request. Only the creator drains that Stream and calls Forget after completion; execution options apply only to newly started work.

`hosttools/sessionrecorder` exports `ExposurePolicy` and `WithExposure(ExposurePolicy) EventOption`. Done/TurnEnd retain only RunID, Turns, Text, Summary and error identity by default. Opt-in flags are IncludeMetadata, IncludeUsage, IncludeRawStreams, IncludeProviderResult, IncludeTranscript and IncludeServices. Explicit private audit data replays on EventRecord.Diagnostics; replay does not reconstruct Result's private fields or live approval responders. Hosts must redact other event types themselves. See [Streaming](./streaming.md#8-ag-ui-bridge) for routing and [the CopilotKit example](../examples/web-chat/copilotkit) for its HTTP companion endpoint.

## 18. Related documents

- [Documentation map](./README.md)
- [Streaming and messages](./streaming.md)
- [Run policy and approvals](./run-policy.md)
- [Structured output](./structured-output.md)
- [Public errors](./public-errors.md)
- [v1 → v2 migration](../README.md#migrating-from-v1-to-v2)
- [Teams](./team.md)
- [A2A bridge](./a2a.md)
- [Architecture and release contract](../AGENTS.md)
