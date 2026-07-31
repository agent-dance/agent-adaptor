# API reference

The root package of `agent-adaptor` is named `adaptor`, and the import path is unchanged:

```go
import adaptor "github.com/agent-dance/agent-adaptor"
```

The public API is organized around six nouns: `Agent`, `Thread`, `Stream`, `Event`, `Result`, and `Driver`. An application normally imports only the root package and one provider package; `driver` is imported directly only when extending a Driver.

## 1. Constructing an Agent

```go
func New(d driver.Driver, opts ...Option) *Agent
```

`New` is the construction entry point on the application side. It takes a Driver that has already captured its configuration plus construction options, and returns an `*Agent` that is safe for concurrent use.

```go
agent := adaptor.New(
	codex.Driver(codex.Config{Model: "gpt-5.4"}),
	adaptor.WithWorkspace("/repo"),
)
```

Passing a nil Driver is a startup-time programming error and `New` panics. CLI availability, login state, and dynamic capabilities are reported as errors during execution or inspection; no environment I/O happens during construction.

Multiple Agents are simply multiple Go variables:

```go
coder := adaptor.New(codex.Driver(codex.Config{Model: "gpt-5.4"}))
reviewer := adaptor.New(
	claude.Driver(claude.Config{Model: "claude-sonnet-4"}),
	adaptor.WithPolicy(adaptor.PolicyReadOnly),
)
```

## 2. Agent and Runner

```go
type Runner interface {
	Run(ctx context.Context, prompt string, opts ...CallOption) (*Result, error)
	Stream(ctx context.Context, prompt string, opts ...CallOption) Stream
}
```

Both `*Agent` and `*Thread` implement `Runner`. Bridges, structured output, and host decorators should program against `Runner` so that stateless and stateful execution share one contract.

```go
func (a *Agent) Run(ctx context.Context, prompt string, opts ...CallOption) (*Result, error)
func (a *Agent) Stream(ctx context.Context, prompt string, opts ...CallOption) Stream
func (a *Agent) Close(ctx context.Context) error
```

`Run` is the convenience form of `Stream`, draining the full event stream, and then reading `Result()`. Both share the same execution pipeline and the same result contract.
`Close` idempotently stops the persistent processes owned by this Agent's Driver and shuts down the Agent's own Tool runtime; new Agent/Thread runs started after Close begins return `ErrAgentClosed`.

## 3. Option scopes

The option interfaces restrict where an option may be used at compile time:

```go
type Option interface {
	ApplyNew(*AgentSettings)
}

type CallOption interface {
	ApplyRun(*RunSettings)
}

type SharedOption interface {
	Option
	CallOption
}
```

- `Option`: only for `New`.
- `CallOption`: only for `Run` or `Stream`.
- `SharedOption`: sets the Agent default at construction, and overrides only the current execution at call time.

The overall merge rule is: the call site is nearer than the construction site; skills append; other options are replaced or merged as listed in the table below. Agent defaults are never modified by a single invocation, so concurrent invocations do not contaminate one another.

### 3.1 Dual-scope options

| Function | Semantics | Call-site merge rule |
|---|---|---|
| `WithModel(string)` | provider model override | non-empty value replaces |
| `WithTimeout(time.Duration)` | total deadline for one execution | replaces; a timeout matches `context.DeadlineExceeded` |
| `WithSpawn()` | force a new provider process, reusing nothing and leaving no persistent writer | replaces the default process mode |
| `WithInstructions(string)` | additional instruction text | replaces |
| `WithWorkspace(string)` | base working directory | replaces |
| `WithMetadata(key, value)` | audit metadata | merged by key; an identical key overwrites |
| `WithIdentity(Identity)` | caller identity passed to host hooks and the Driver | replaced as a whole |
| `WithPolicy(Policy)` | sandbox, optional features, and approval policy | replaced as a whole, with no field-level merge |
| `OnApproval(ApprovalHandler)` | HITL callback | replaces |
| `WithSkills(...SkillRef)` | skill references | appends; conflicting keys must be structurally identical |
| `WithMCP(...mcp.Server)` | MCP server set | replaces the whole set; an empty call clears it explicitly |
| `WithProfileResources(profile.Resources)` | desired state of profile resources | each resource family merges per its own contract |
| `WithWorkspaceSpec(WorkspaceSpec)` | workspace provisioning strategy | replaced as a whole |
| `WithServices(...ServiceSpec)` | declarative runtime service set | replaces the whole set; an empty call clears it explicitly |
| `WithRunServices(...RunServiceProvider)` | ecosystem services attached per execution | appends, deduplicated by provider identity |

Detailed rules for `WithProfileResources`:

- `Skills` appends.
- A non-nil `MCP` replaces the MCP set; a non-nil empty slice means an explicit clear.
- Non-nil `Agents`, `Hooks`, and `Config` each replace the corresponding resource family; a non-nil empty slice means an explicit declaration of emptiness.
- A non-nil `Instructions` replaces the instruction resource.

### 3.2 Construction-only options

| Function | Semantics |
|---|---|
| `WithThreadStore(threadstore.Store)` | enable Thread persistence, resumption, and lease coordination |
| `WithTools(...tool.Definition)` | install the immutable host-defined Tool set; the whole set is replaced, and an empty call clears it explicitly |
| `WithEventBuffer(int)` | set the per-execution event buffer; the default is 1024 |
| `WithBlockingEvents()` | switch event delivery to blocking, no-drop mode |
| `WithProfile(profile.Selection)` | select the provider profile strategy |
| `WithSkillProvider(skill.Provider)` | resolve `skill.Key` and optionally supply a catalogue |
| `WithSkillMaterializer(skill.Materializer)` | override how non-catalogue skills are materialized |
| `WithWorkspaceManager(WorkspaceManager)` | resolve a `WorkspaceSpec` into an actual lease |
| `WithServiceManager(ServiceManager)` | ensure and release the services declared by `WithServices` |

Passing these options to `Run` or `Stream` fails to compile.

### 3.3 Call-only options

| Function | Semantics |
|---|---|
| `WithSchema[T](...SchemaOption)` | derive the JSON Schema for this execution from a Go type |
| `WithSchemaJSON([]byte, ...SchemaOption)` | use a caller-supplied JSON Schema |

A schema belongs to one concrete question, so it must not be passed to `New`.

### 3.4 Ecosystem options

`AgentSettings` and `RunSettings` expose controlled setters, so ecosystem packages can implement the option interfaces above. On a setter, `Set*` means replace and `Add*` means append. The typical example is a delegation service that issues its own options and hooks in through `WithRunServices`, without adding business vocabulary to the root package.

## 4. Policy and Identity

```go
type Identity struct {
	ID      string
	Tenant  string
	Profile string
	Name    string
}
```

Identity is used by host-supplied components such as skill, workspace, and service for scope isolation; the library never uses it to route Agents automatically. `IdentityFromContext(ctx)` reads the effective identity from the context handed to host hooks during a run.

```go
type Policy struct {
	Sandbox   SandboxLevel
	WebSearch FeatureLevel
	Browser   FeatureLevel
	Approvals ApprovalPolicy
}
```

Sandbox values are `SandboxInherit`, `ReadOnly`, `WorkspaceWrite`, and `Unrestricted`. Optional-feature values are `FeatureInherit`, `FeatureAllow`, and `FeatureDeny`.

Common Policy presets:

- `PolicyReadOnly`
- `PolicyWorkspaceWrite`
- `PolicyUnrestricted`

`WithPolicy` replaces the default Policy as a whole; a caller that wants to change a single dimension for one invocation must construct the complete value explicitly.

The zero value / Inherit is the portable expression across Drivers. Any explicit Sandbox, WebSearch, or Browser value is checked strictly against `Descriptor.RunPolicyCaps` before the process starts; when the Driver does not support it, a `*PolicyCapabilityUnsupportedError` matching `ErrPolicyCapabilityUnsupported` is returned rather than being silently ignored.

Approval modes follow the same rule. An explicit `ApprovalAsk`, `ApprovalAutoApprove`, `ApprovalAutoDeny`, or `QuestionAutoDeny` must be supported by the capability declaration for the corresponding Kind, otherwise a `*HumanDecisionModeUnsupportedError` matching `ErrHumanDecisionModeUnsupported` is returned. `ApprovalsAutoDeny` requires auto-reject support for all three of Permission, PlanReview, and Question, so it is not a portable cross-provider preset; leaving Question at `QuestionInherit` uses the library's conservative auto-deny default instead of forming an explicit capability requirement.

## 5. Thread

A Thread is a `Runner` with persistent resumption. Agents are stateless by default, and a store must be injected through `WithThreadStore` before a Thread is used. Claude, CodeBuddy, and Codex allow persistent reuse for an explicit Thread by default; Cursor and direct Agent invocations start a process per turn.

```go
func (a *Agent) Thread(key string, opts ...ThreadOption) *Thread
func (t *Thread) Fork(newKey string) *Thread
func (t *Thread) Key() string
func (t *Thread) Checkpoint(ctx context.Context) (*Checkpoint, error)
func ResumeOnly() ThreadOption
```

Action semantics:

| Action | Semantics |
|---|---|
| `agent.Thread(key)` | resume if an active checkpoint exists, otherwise create |
| `agent.Thread(key, ResumeOnly())` | resumption only; returns an error when missing or incompatible |
| `parent.Fork(newKey)` | the first execution forks from the parent checkpoint; the parent Thread stays unchanged and the target key must be unoccupied |

The key is the host's own non-empty, opaque string; an empty key panics. A new unrelated conversation must be assigned a new key by the host; the SDK offers no entry point for deliberately rebinding the same key. `Checkpoint` is only for auditing and diagnostics; normal resumption is handled by the Thread automatically.

The main errors can all be matched with `errors.Is`:

- `ErrThreadStoreRequired`
- `ErrThreadNotFound`
- `ErrThreadBusy`
- `ErrThreadIncompatible`
- `ErrThreadLeaseLost`
- `ErrThreadCheckpointMissing`
- `ErrThreadAlreadyExists`
- `ErrResumeRejected`

A Thread store holds only the provider resume checkpoint, the compatibility fingerprint, and leases; it does not hold UI chat history.

### 5.1 provider process lifecycle

```go
agent := adaptor.New(
	claude.Driver(claude.Config{}),
	adaptor.WithThreadStore(memory.NewStore()),
)
defer agent.Close(context.Background())

thread := agent.Thread("ticket-42")
_, _ = thread.Run(ctx, "first")
_, _ = thread.Run(ctx, "second")
_, _ = thread.Run(ctx, "isolated", adaptor.WithSpawn())
```

`WithSpawn()` is a dual-scope option: passed to `New` it makes every turn default to a single-use process; passed to `Run`/`Stream` it overrides only that turn. When configuration/fingerprint drift, native schema, or similar reasons force a temporary change of process shape, the Driver waits for the old writer to exit completely before starting the replacement, and only warms up after obtaining a valid new checkpoint. A disconnection after the prompt may already have been delivered is never replayed automatically.

A Driver declares the capability through `Descriptor.Process.Persistent`. A Driver that declares true must implement `driver.ProcessLifecycleDriver` so that `Agent.Close` can reclaim all process groups within a bounded time.

## 6. Stream and Event

```go
type Stream interface {
	Events() <-chan Event
	Result() (*Result, error)
	RunID() string
	Cancel()
}
```

`Stream` returns immediately after creation, and pre-start errors are also returned through `Result()`; there is no second error return value. `Cancel` is idempotent. Callers normally drain `Events()` until it closes and then read `Result()`.

```go
stream := agent.Stream(ctx, prompt)
for ev := range stream.Events() {
	switch e := ev.(type) {
	case adaptor.TextDelta:
		if e.Phase == adaptor.PhaseContent {
			fmt.Print(e.Text)
		}
	case adaptor.ToolCall:
		log.Printf("tool %s", e.Name)
	case *adaptor.ApprovalRequest:
		_ = e.Approve(ctx)
	}
}
result, err := stream.Result()
```

`Event` is a sealed typed interface. Each event's `Meta()` returns the authoritative envelope owned by the root package:

```go
type EventMeta struct {
	RunID     string
	ThreadKey string
	Sequence  uint64
	Time      time.Time
	TurnID    string
	Source    *EventSourceMeta
}
```

`Sequence` increases strictly within one run. Coordinates supplied by the provider itself are kept in `Source` and never override the root package's ordering.

Event families:

| Type | Meaning |
|---|---|
| `TextDelta` | assistant text; `PhaseStart` / `PhaseContent` / `PhaseEnd` |
| `Thinking` | reasoning text lifecycle |
| `ToolCall` | tool start, argument deltas, and end lifecycle |
| `ToolResult` | complete tool result |
| `RunStarted` | execution started |
| `RunFinished` | terminal hint for the execution; the authoritative result is still `Stream.Result()` |
| `ProcessInfo` | spawn plus raw stdout/stderr chunks |
| `Notice` | invocation, lifecycle, runtime, step, transcript, and approval notices |
| `Dropped` | aggregated backpressure drops, with count, kinds, sequence range, and reason |
| `SubagentUpdate` | start, deltas, and end of a delegation subagent |
| `*ApprovalRequest` | a HITL request awaiting a host response |

Default backpressure drops the events the contract allows to be dropped when the buffer is full, and produces a `Dropped`. `WithBlockingEvents` guarantees no drops, but a slow consumer then applies backpressure to the Driver; cancellation still unblocks it.

`WithEventMeta` is only for bridges and persistent recorders replaying typed Events. A live sink always rewrites the authoritative ordering.

## 7. Approval

HITL has two consumption forms, but both share one `ApprovalRequest`.

Callback form:

```go
adaptor.OnApproval(func(ctx context.Context, req *adaptor.ApprovalRequest) error {
	switch req.Kind {
	case adaptor.ApprovalQuestion:
		return req.Answer(ctx, "yes")
	default:
		return req.Approve(ctx)
	}
})
```

The event form receives `*ApprovalRequest` from `Stream.Events()`, keeps the request object, and calls its response methods from another goroutine.

```go
func (r *ApprovalRequest) Approve(ctx context.Context) error
func (r *ApprovalRequest) Deny(ctx context.Context, reason string) error
func (r *ApprovalRequest) Answer(ctx context.Context, option string) error
```

Kinds: `ApprovalPermission`, `ApprovalPlanReview`, `ApprovalQuestion`. `Approve` does not apply to a Question, `Answer` applies only to a Question, and `Deny` applies to every Kind.

A response is exactly-once. The main errors:

- `ErrApprovalResolved`
- `ErrApprovalExpired`
- `ErrApprovalKindMismatch`
- `ErrApprovalUnavailable`

Policy is configured through `Policy.Approvals`: Permission and PlanReview use `ApprovalMode`; Question uses `QuestionMode`; the action after a timeout or rejection uses `FallbackAction`. The zero value takes conservative defaults. Every explicit mode is validated strictly against Driver capability before startup. `ApproveAll()` and `DenyAll(reason)` only handle Ask requests that have already passed capability validation and been routed to a handler.

## 8. Result and errors

```go
type Result struct {
	RunID    string
	Model    string
	Provider string
	Text     string
	Summary  string
	Usage    *Usage
	Metadata map[string]string
}
```

Frequently used fields are exposed directly; audit data and large objects are reached through methods:

```go
func (r *Result) Raw() RawStreams
func (r *Result) Transcript() []TranscriptItem
func (r *Result) Services() []ServiceReport
func (r *Result) Decode(v any) error
```

Layered semantics:

- `Text` contains only the final assistant-facing text.
- `Summary` is an optional short summary and is not guaranteed to be produced by every Driver.
- `Usage == nil` means the provider reported no usage; a non-nil zero value means usage was observed and every normalized count is explicitly zero.
- `Raw().Stdout` and `Raw().Stderr` are the complete raw process output.
- `Raw().Terminal` holds the terminal event name and exact JSON that the Driver recognized from the official protocol; it is nil when nothing was recognized.
- `Transcript()` holds the normalized items the Driver parsed from the official protocol, and returns a deep copy.
- `Services()` holds reports of runtime services actually ensured or observed by the Driver; it does not echo secret environment variables or MCP declarations.
- `Decode()` decodes already-validated structured output when available; when no schema was requested it attempts to treat `Text` as JSON.

A business failure returns a `*RunError`, which retains a partial or complete Result:

```go
type RunError struct {
	Reason  FailureReason
	Message string
	Details map[string]any
	Result  *Result
}
```

```go
res, err := agent.Run(ctx, prompt)
if err != nil {
	var runErr *adaptor.RunError
	if errors.As(err, &runErr) {
		log.Printf("reason=%s partial=%q", runErr.Reason, runErr.Result.Text)
	}
	return err
}
_ = res
```

`errors.Is` matches `ErrApprovalDenied`, `ErrApprovalTimeout`, `ErrAgentFailed`, `ErrRunCancelled`, and `ErrPolicyViolation`. Pre-start configuration and policy errors match `ErrInvalidDriverConfig`, `ErrInvalidPolicy`, `ErrPolicyCapabilityUnsupported`, or `ErrHumanDecisionModeUnsupported`. Configuration, resource resolution, context cancellation, and other infrastructure errors travel the same single `error` path; the corresponding typed errors expose diagnostics such as Driver, field, and value through `errors.As`.

## 9. Structured output

```go
func RunAs[T any](ctx context.Context, r Runner, prompt string, opts ...CallOption) (T, *Result, error)
func WithSchema[T any](opts ...SchemaOption) CallOption
func WithSchemaJSON(schemaJSON []byte, opts ...SchemaOption) CallOption
```

`RunAs` accepts any Runner, adds `WithSchema[T]` automatically, executes, and decodes.

```go
type Review struct {
	Verdict string   `json:"verdict"`
	Issues  []string `json:"issues"`
}

review, result, err := adaptor.RunAs[Review](ctx, reviewer, "review the diff")
```

Schema options:

| Option | Semantics |
|---|---|
| `SchemaName(string)` | provider-facing schema name |
| `SchemaDescription(string)` | schema description |
| `SchemaReturnInvalid()` | keep an invalid payload on validation failure instead of failing the execution |
| `SchemaInlineReferences()` | inline `$ref`; a recursive type is an error |
| `SchemaAllowAdditionalProperties()` | allow additional object properties |
| `SchemaRequireExplicitTags()` | only `jsonschema:"required"` fields are required |
| `SchemaUseGoComments(base, path)` | generate descriptions from Go comments |

Consumers do not choose a structured-output mode. The framework always prefers the
provider's native JSON Schema; when the current transport or policy does not support
it, it falls back automatically to Prompt plus local validation; only when neither
mechanism is available does it return `ErrStructuredOutputUnsupported` before the
process starts. An invalid schema matches `ErrInvalidOutputSchema`, and the Driver's
`Descriptor.StructuredOutput` is the source of truth for the capability.

## 10. Inspect and profile state

```go
func (a *Agent) Inspect() Inspector
```

Inspector has only five read-only methods:

```go
func (in Inspector) Environment(ctx context.Context) (EnvironmentReport, error)
func (in Inspector) Models(ctx context.Context) ([]ModelInfo, error)
func (in Inspector) Quota(ctx context.Context) (QuotaReport, error)
func (in Inspector) ConfigSchema(ctx context.Context) (*ConfigSchema, error)
func (in Inspector) Skills(ctx context.Context) (SkillSnapshot, error)
```

When the Driver does not implement the corresponding probe, Inspector returns a descriptor fallback or an explicitly unavailable report; it never fakes success.

The stateful actions for profile and skill hang directly off the Agent:

```go
func (a *Agent) ProfileState(ctx context.Context) (ProfileSnapshot, error)
func (a *Agent) SyncProfile(ctx context.Context) (ProfileSnapshot, error)
func (a *Agent) SelectSkills(ctx context.Context, keys []string) (SkillSnapshot, error)
```

- `ProfileState` only reads the desired and observed state.
- `SyncProfile` materializes the resources the Driver supports, and reports unsupported resources truthfully.
- `SelectSkills` installs an in-process selection override; it does not persist user preferences on the host's behalf.

## 11. The tool, skill, MCP, and profile vocabulary packages

### 11.1 tool

Host-defined Tools use typed Go handlers and expose no MCP server, transport, or credential:

```go
lookup := tool.Define(
	"lookup_issue",
	"Look up one issue by number.",
	func(ctx context.Context, in LookupInput) (LookupOutput, error) {
		return lookupIssue(ctx, in.Number)
	},
	tool.ReadOnly(),
	tool.Idempotent(),
	tool.Revision("lookup_issue/v1"),
)

agent := adaptor.New(driver, adaptor.WithTools(lookup))
```

Input and output schemas are inferred from the Go types and their tags by default; `tool.InputSchemaJSON` and `tool.OutputSchemaJSON` are the standard JSON Schema escape hatch. `tool.Reject(code, message)` reports an expected failure that is safe to show the model; `tool.AsRejection` recognizes only those package-minted errors, while ordinary or lookalike errors and panics are sanitized. `WithTools` is construction-only, and the last option replaces the previous set as a whole. Every Tool used with a Thread must provide a stable `Revision` that is updated whenever its behavioral semantics change. The Agent delivers these Tools internally through an authenticated loopback MCP runtime with a per-Agent token and unpredictable credential env name; aliasing that env name from another MCP declaration fails before Driver launch. Built-in Drivers use an SDK-owned isolated execution profile so that concurrent host processes never rewrite the same native profile. None of these mechanisms enter the caller-facing API. For the complete contract see [`tools.md`](./tools.md).

### 11.2 skill

Common constructors:

```go
skill.Dir(path)
skill.FS(fsys, root)
skill.Inline(key, skillMD)
skill.Archive(key, opener, opts...)
skill.Key(key)
skill.Require(value, reason)
```

`WithSkills` accepts a `skill.Ref`. A complete Skill value can be used directly; a `skill.Key` is resolved by the `skill.Provider` injected through `WithSkillProvider`. Implementing `skill.Catalog` lets `Inspect().Skills()` enumerate the catalogue. `skill.Set` is the static map-backed implementation.

Archives support zip, tar, and tgz; `NewDefaultSkillMaterializer` can configure the cache root and the limits on archive size, file size, and entry count. A skill resolution or materialization failure returns an error before the Driver starts.

### 11.3 MCP

```go
adaptor.WithMCP(
	mcp.HTTP("docs", "https://example.com/mcp"),
	mcp.SSE("events", "https://example.com/sse"),
	mcp.Stdio("repo-tools", "npx", mcp.Args("repo-mcp")),
)
```

Options: `mcp.Args`, `mcp.Env`, `mcp.WithHeader`, `mcp.WithHeaders`, `mcp.WithBearerTokenEnv`, `mcp.Required`. A mismatch between transport and option is not silently ignored; an MCP configuration error is returned before startup.

### 11.4 profile

Profile selection:

```go
profile.Default()
profile.Native()
profile.Dedicated(dir)
profile.CloneNative(dir, profile.LinkAuth())
profile.CloneFrom(src, dst, profile.CopySettings(), profile.CopyMCP())
```

Clone options include `CopySettings`, `CopyMCP`, `CopySkills`, `CopyAuth`, `LinkAuth`, and `WithOptions`. An OAuth CLI should normally use `LinkAuth` to avoid copying refresh-token state that rotates.

`profile.Resources` can declare `Skills`, `MCP`, `Agents`, `Hooks`, `Instructions`, and `Config`, and enters the unified resolution pipeline through `WithProfileResources`.

## 12. Workspace and runtime services

Use `WithWorkspace(dir)` for a direct directory. Use the following when policy-driven provisioning is needed:

```go
adaptor.WithWorkspaceSpec(adaptor.SharedWorkspace{})
adaptor.WithWorkspaceSpec(adaptor.GitWorktreeWorkspace{
	BaseRef:           "main",
	BranchTemplate:    "agent/{run_id}",
	WorktreeParentDir: "/tmp/worktrees",
})
adaptor.WithWorkspaceSpec(adaptor.DriverManagedWorkspace{})
```

`WorkspaceManager`:

```go
type WorkspaceManager interface {
	Resolve(ctx context.Context, req WorkspaceRequest) (WorkspaceLease, error)
	Release(ctx context.Context, lease WorkspaceLease, mode WorkspaceReleaseMode) error
}
```

Without an injected manager, a WorkspaceSpec resolves to the base directory through a passthrough manager; a manager must be supplied when a worktree or sandbox has to be created for real.

Declarative services:

```go
type ServiceManager interface {
	Ensure(ctx context.Context, req ServiceRequest) ([]ServiceRef, error)
	ReleaseByRun(ctx context.Context, runID string) error
	ReleaseByLabels(ctx context.Context, labels map[string]string) error
}
```

`WithServices` only declares the desired services; without `WithServiceManager` a declaration does not invent an endpoint. `ServiceRef.MCP` is the typed entry point through which a service publishes MCP to this execution, and `SecretEnv` reaches only the Driver subprocess, never the Result report.

`RunServiceProvider` covers dynamic services attached per execution, such as delegation or a browser pool:

```go
type RunServiceProvider interface {
	AttachRun(ctx context.Context, runID string) (RunAttachment, error)
	DetachRun(ctx context.Context, runID string) error
}
```

A `RunAttachment` may contribute Services plus one `RunEventSource` already projected into root Events; those events enter the same Stream directly. `RunStarted` / `RunFinished` are owned exclusively by core, and an event source that emits either of them is filtered. The SDK publishes the single `RunStarted` first, and publishes the single `RunFinished` and closes Events only after the event sources have finished flushing and run-scoped resources have been released. `DetachRun`, `ReleaseByRun`, and workspace release share one global bounded budget, but each step has a fair sub-budget; a timeout in any one source/hook does not prevent later release actions from being invoked. A timeout or release error surfaces as the error from `Result()` and is never swallowed silently.

## 13. threadstore and memory

```go
type Store interface {
	Resolve(ctx context.Context, q Query) (*Record, error)
	Finalize(ctx context.Context, req FinalizeRequest) error
	AcquireLease(ctx context.Context, target, owner string, ttl time.Duration) (Lease, error)
	RenewLease(ctx context.Context, lease Lease, ttl time.Duration) error
	ReleaseLease(ctx context.Context, lease Lease) error
}
```

`Finalize` must atomically complete lease validation, record saving, archiving of the old record, and key rebinding; Fork uses `RequireKeyAbsent` to prevent a target conflict.

`memory.NewStore()` returns a concurrency-safe single-process implementation suitable for tests, local tools, and demos. A service that needs cross-process resumption and coordination must implement a persistent Store.

## 14. Built-in provider Drivers

All four providers use the same shape:

```go
func Driver(cfg Config) driver.Driver
```

| Package | Main Config fields |
|---|---|
| `codex` | `CommonConfig`, `Model`, `ReasoningEffort`, `FastMode` |
| `claude` | `CommonConfig`, `Model`, `Effort`, `MaxTurnsPerRun` |
| `cursor` | `CommonConfig`, `Model`, `Mode` |
| `codebuddy` | `CommonConfig`, `Model`, `Effort`, `PermissionMode`, `MaxTurnsPerRun` |

The `CommonConfig` in each package is an alias of `driver.CommonConfig` and contains `Command`, `CWD`, `Env`, instructions, prompt templates, workspace defaults, timeouts, grace period, and extra args. A Driver constructor takes a deep-copied snapshot of the configuration, so later modification of the original slices or maps does not affect the Agent.

A provider-specific Config expresses CLI and transport configuration; call-level overrides such as `WithModel` are resolved uniformly by the root package.

## 15. Driver SPI and adaptertest

The minimal contract for a third-party Driver:

```go
type Driver interface {
	Descriptor() Descriptor
	ValidateConfig(cfg any) error
	Run(ctx context.Context, req Request, sink EventSink) (Response, error)
}
```

The root package is responsible for option merging, Thread coordination, workspace/runtime/skill/MCP resolution, and result consolidation; the Driver is responsible for provider configuration validation, process or protocol execution, Transcript parsing, and checkpoint extraction.

The optional capability interfaces include:

- `EnvironmentProbe`
- `ModelLister`
- `ModelDetector`
- `ProfileReporter`
- `SessionCodecProvider`
- `SessionConfigFingerprinter`
- `ConfigSchemaProvider`
- `QuotaProbe`
- `SkillSupport`
- `StreamSupport`

A `Descriptor` declaration must match the implemented interfaces and the real behavior. A Driver that declares `Descriptor.Sessions.SupportsResume=true` must also stably supply both a non-nil `SessionCodec` and a non-empty, cross-process deterministic `SessionConfigFingerprint`; the fingerprint covers every provider-visible construction configuration plus the codec/version contract, and must return an error when it cannot be canonicalized stably. The public Thread validates both before acquiring a workspace/runtime/store lease or dispatching the Driver, and returns an error matching `errors.Is(err, adaptor.ErrThreadIncompatible)` when either is missing or unstable.

For each resolved invocation, `ProfilePayload.Fingerprint` identifies the exact provider-visible profile desired state and remains authoritative for materialization. `ProfilePayload.SessionFingerprint()` returns the separate resume and persistent-process compatibility guard; core may normalize only Agent-owned ephemeral transport allocations in that value. A Driver must apply the complete current Request on every resumed invocation, store `SessionFingerprint()` in its session params, and must never substitute the compatibility value for concrete MCP/profile materialization.

The stream sequence, timestamps, and root Event ordering for the events a Driver emits are assigned by core; a Driver's `Response.Transcript` must mirror the transcript items it emitted. `Checkpoint.Valid` may be set only when a clean success, an official terminal event, and a round-trippable resume ID all hold.

Conformance testing:

```go
func TestMyDriver(t *testing.T) {
	adaptertest.TestDriver(t, func() driver.Driver {
		return mydriver.Driver(mydriver.Config{Model: "m-1"})
	}, adaptertest.WithConfig(mydriver.Config{Model: "m-1"}))
}
```

`adaptertest.TestDriver` checks Driver/config, capability truthfulness, structured output, session codec, session config fingerprint, event lifecycle, Transcript mirroring, and Response invariants. A real CLI probe must use `WithLiveRun` explicitly; the default tests must not produce external or paid calls.

`adaptertest.NewReferenceDriver` is a fully in-memory conformant reference implementation; functions such as `VerifyOutcome`, `VerifyStreamSequence`, `VerifyRunEvents`, `VerifyTranscript`, and `VerifyTranscriptMirror` are available for finer-grained testing.

## 16. Related documents

- [Documentation map](./README.md)
- [Structured output](./structured-output.md)
- [Streaming](./streaming.md)
- [A2A bridge](./a2a.md)
- [Architecture and release contract](../AGENTS.md)
