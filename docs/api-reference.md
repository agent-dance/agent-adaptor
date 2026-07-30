# API 参考

`agent-adaptor` 的根包名是 `adaptor`，导入路径保持不变：

```go
import adaptor "github.com/agent-dance/agent-adaptor"
```

公共 API 围绕六个名词组织：`Agent`、`Thread`、`Stream`、`Event`、`Result`、`Driver`。应用通常只需导入根包和一个 provider 包；只有扩展 Driver 时才直接导入 `driver`。

## 1. 构造 Agent

```go
func New(d driver.Driver, opts ...Option) *Agent
```

`New` 是应用侧的构造入口。它接收一个已经捕获配置的 Driver 和构造选项，返回可并发使用的 `*Agent`。

```go
agent := adaptor.New(
	codex.Driver(codex.Config{Model: "gpt-5.4"}),
	adaptor.WithWorkspace("/repo"),
)
```

传入 nil Driver 属于启动期编程错误，`New` 会 panic。CLI 可用性、登录态和动态能力在执行或检查时返回错误，不在构造期间做环境 I/O。

多个 Agent 直接使用多个 Go 变量：

```go
coder := adaptor.New(codex.Driver(codex.Config{Model: "gpt-5.4"}))
reviewer := adaptor.New(
	claude.Driver(claude.Config{Model: "claude-sonnet-4"}),
	adaptor.WithPolicy(adaptor.PolicyReadOnly),
)
```

## 2. Agent 与 Runner

```go
type Runner interface {
	Run(ctx context.Context, prompt string, opts ...CallOption) (*Result, error)
	Stream(ctx context.Context, prompt string, opts ...CallOption) Stream
}
```

`*Agent` 与 `*Thread` 都实现 `Runner`。bridge、结构化输出和宿主装饰器应面向 `Runner` 编程，以便无状态和有状态执行使用同一合同。

```go
func (a *Agent) Run(ctx context.Context, prompt string, opts ...CallOption) (*Result, error)
func (a *Agent) Stream(ctx context.Context, prompt string, opts ...CallOption) Stream
func (a *Agent) Close(ctx context.Context) error
```

`Run` 是 `Stream`、消费完整事件流、再读取 `Result()` 的便捷形式。两者共享同一执行管线和相同的结果合同。
`Close` 幂等停止该 Agent 的 Driver 所拥有的常驻进程并关闭 Agent 自有的 Tool runtime；Close 开始后的 Agent/Thread 新运行返回 `ErrAgentClosed`。

## 3. 选项作用域

选项接口用于在编译期限制使用位置：

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

- `Option`：仅用于 `New`。
- `CallOption`：仅用于 `Run` 或 `Stream`。
- `SharedOption`：构造时设置 Agent 默认值，调用时只覆盖本次执行。

总体合并规则是：调用处比构造处更近；skills 追加；其他选项按下表替换或合并。Agent 默认值不会被单次调用修改，因此并发调用不会互相污染。

### 3.1 双作用域选项

| 函数 | 语义 | 调用处合并规则 |
|---|---|---|
| `WithModel(string)` | provider 模型覆盖 | 非空值替换 |
| `WithTimeout(time.Duration)` | 单次执行总时限 | 替换；超时匹配 `context.DeadlineExceeded` |
| `WithSpawn()` | 强制使用新 provider 进程，不复用或留下常驻 writer | 替换默认进程模式 |
| `WithInstructions(string)` | 附加指令文本 | 替换 |
| `WithWorkspace(string)` | 基础工作目录 | 替换 |
| `WithMetadata(key, value)` | 审计元数据 | 按 key 合并，同名 key 覆盖 |
| `WithIdentity(Identity)` | 传给宿主 hook 与 Driver 的调用方身份 | 整体替换 |
| `WithPolicy(Policy)` | sandbox、可选能力和审批策略 | 整体替换，不做字段级合并 |
| `OnApproval(ApprovalHandler)` | HITL 回调 | 替换 |
| `WithSkills(...SkillRef)` | skill 引用 | 追加；冲突 key 必须结构相同 |
| `WithMCP(...mcp.Server)` | MCP server 集合 | 整组替换；空调用显式清空 |
| `WithProfileResources(profile.Resources)` | profile 资源期望状态 | 各资源族按其合同合并 |
| `WithWorkspaceSpec(WorkspaceSpec)` | workspace 供应策略 | 整体替换 |
| `WithServices(...ServiceSpec)` | 声明式 runtime service 集合 | 整组替换；空调用显式清空 |
| `WithRunServices(...RunServiceProvider)` | 每次执行附着的生态服务 | 追加，并按 provider 身份去重 |

`WithProfileResources` 的细分规则：

- `Skills` 追加。
- 非 nil `MCP` 替换 MCP 集合；非 nil 空切片表示显式清空。
- 非 nil `Agents`、`Hooks`、`Config` 分别替换对应资源族；非 nil 空切片表示显式声明为空。
- 非 nil `Instructions` 替换指令资源。

### 3.2 仅构造选项

| 函数 | 语义 |
|---|---|
| `WithThreadStore(threadstore.Store)` | 启用 Thread 持久化、续接与租约协调 |
| `WithTools(...tool.Definition)` | 安装不可变的宿主定义 Tool 集合；整组替换，空调用显式清空 |
| `WithEventBuffer(int)` | 设置每次执行的事件缓冲；默认 1024 |
| `WithBlockingEvents()` | 事件发送改为阻塞、无丢弃模式 |
| `WithProfile(profile.Selection)` | 选择 provider profile 策略 |
| `WithSkillProvider(skill.Provider)` | 解析 `skill.Key` 并可选提供 catalogue |
| `WithSkillMaterializer(skill.Materializer)` | 覆盖非目录 skill 的物化方式 |
| `WithWorkspaceManager(WorkspaceManager)` | 将 `WorkspaceSpec` 解析为实际 lease |
| `WithServiceManager(ServiceManager)` | 确保和释放 `WithServices` 声明的服务 |

将这些选项传给 `Run` 或 `Stream` 会编译失败。

### 3.3 仅调用选项

| 函数 | 语义 |
|---|---|
| `WithSchema[T](...SchemaOption)` | 从 Go 类型派生本次执行的 JSON Schema |
| `WithSchemaJSON([]byte, ...SchemaOption)` | 使用调用方提供的 JSON Schema |

schema 属于一个具体问题，因此不能传给 `New`。

### 3.4 生态选项

`AgentSettings` 和 `RunSettings` 提供受控 setter，生态包可实现上述选项接口。setter 的 `Set*` 表示替换，`Add*` 表示追加。典型例子是 delegation service 发行自己的选项，并通过 `WithRunServices` 接入，而无需在根包增加业务词汇。

## 4. Policy 与 Identity

```go
type Identity struct {
	ID      string
	Tenant  string
	Profile string
	Name    string
}
```

Identity 用于宿主提供的 skill、workspace、service 等组件做作用域隔离；库不会用它自动路由 Agent。`IdentityFromContext(ctx)` 可从运行期间传给宿主 hook 的 context 读取生效身份。

```go
type Policy struct {
	Sandbox   SandboxLevel
	WebSearch FeatureLevel
	Browser   FeatureLevel
	Approvals ApprovalPolicy
}
```

Sandbox 值为 `SandboxInherit`、`ReadOnly`、`WorkspaceWrite`、`Unrestricted`。可选能力值为 `FeatureInherit`、`FeatureAllow`、`FeatureDeny`。

常用 Policy 预设：

- `PolicyReadOnly`
- `PolicyWorkspaceWrite`
- `PolicyUnrestricted`

`WithPolicy` 整体替换默认 Policy；需要单次只修改一个维度时，调用方应显式构造完整值。

零值 / Inherit 是跨 Driver 的可移植表达。任何显式 Sandbox、WebSearch 或 Browser 值都会在进程启动前严格检查 `Descriptor.RunPolicyCaps`；Driver 不支持时返回匹配 `ErrPolicyCapabilityUnsupported` 的 `*PolicyCapabilityUnsupportedError`，不会静默忽略。

审批 mode 遵循相同规则。显式 `ApprovalAsk`、`ApprovalAutoApprove`、`ApprovalAutoDeny` 或 `QuestionAutoDeny` 必须由对应 Kind 的 capability 声明支持，否则返回匹配 `ErrHumanDecisionModeUnsupported` 的 `*HumanDecisionModeUnsupportedError`。`ApprovalsAutoDeny` 要求 Permission、PlanReview、Question 三类都支持 auto-reject，因此不是跨 provider 的通用预设；Question 保持 `QuestionInherit` 时会使用库的保守默认 auto-deny，而不形成显式 capability 要求。

## 5. Thread

Thread 是带持久化续接能力的 `Runner`。Agent 默认无状态，使用 Thread 前必须通过 `WithThreadStore` 注入 store。Claude、CodeBuddy 和 Codex 对显式 Thread 默认允许常驻复用；Cursor 和直接 Agent 调用逐轮启动。

```go
func (a *Agent) Thread(key string, opts ...ThreadOption) *Thread
func (t *Thread) Fork(newKey string) *Thread
func (t *Thread) Key() string
func (t *Thread) Checkpoint(ctx context.Context) (*Checkpoint, error)
func ResumeOnly() ThreadOption
```

动作语义：

| 动作 | 语义 |
|---|---|
| `agent.Thread(key)` | 有 active checkpoint 就续接，否则新建 |
| `agent.Thread(key, ResumeOnly())` | 只允许续接；缺失或不兼容时返回错误 |
| `parent.Fork(newKey)` | 首次执行从父 checkpoint 分叉；父 Thread 保持不变，目标 key 必须未占用 |

key 是宿主自己的非空、不透明字符串；空 key 会 panic。新的无关对话必须由宿主分配新的 key，SDK 不提供同 key 主动重绑入口。`Checkpoint` 只用于审计和诊断，正常续接由 Thread 自动完成。

主要错误均可用 `errors.Is` 判断：

- `ErrThreadStoreRequired`
- `ErrThreadNotFound`
- `ErrThreadBusy`
- `ErrThreadIncompatible`
- `ErrThreadLeaseLost`
- `ErrThreadCheckpointMissing`
- `ErrThreadAlreadyExists`
- `ErrResumeRejected`

Thread store 只保存 provider resume checkpoint、兼容 fingerprint 和租约，不保存 UI 聊天记录。

### 5.1 provider 进程生命周期

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

`WithSpawn()` 是双作用域选项：传给 `New` 时所有轮次默认为单次进程；传给 `Run`/`Stream` 时只覆盖本轮。配置/指纹漂移、native schema 等必须暂时切换进程形态时，Driver 在启动 replacement 前等待旧 writer 完整退出，并只在得到有效新 checkpoint 后预热。prompt 可能已经送达后的断线不会自动重放。

Driver 通过 `Descriptor.Process.Persistent` 声明能力。声明为 true 的 Driver 必须实现 `driver.ProcessLifecycleDriver`，供 `Agent.Close` 有界回收全部进程组。

## 6. Stream 与 Event

```go
type Stream interface {
	Events() <-chan Event
	Result() (*Result, error)
	RunID() string
	Cancel()
}
```

`Stream` 创建后立即返回，启动前错误也通过 `Result()` 返回；不会增加第二个 error 返回值。`Cancel` 幂等。调用者通常先消费 `Events()` 到关闭，再读取 `Result()`。

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

`Event` 是 sealed typed interface。每个事件的 `Meta()` 返回根包拥有的权威 envelope：

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

`Sequence` 在一个 run 内严格递增。provider 自带的坐标保存在 `Source`，不会覆盖根包的顺序。

事件族：

| 类型 | 含义 |
|---|---|
| `TextDelta` | assistant 文本；`PhaseStart` / `PhaseContent` / `PhaseEnd` |
| `Thinking` | reasoning 文本生命周期 |
| `ToolCall` | tool start、参数增量、end 生命周期 |
| `ToolResult` | 完整 tool result |
| `RunStarted` | 执行开始 |
| `RunFinished` | 执行终局提示；权威结果仍以 `Stream.Result()` 为准 |
| `ProcessInfo` | spawn、原始 stdout/stderr chunk |
| `Notice` | invocation、lifecycle、runtime、step、transcript、approval 通知 |
| `Dropped` | 背压丢弃聚合，含数量、种类、序号范围和原因 |
| `SubagentUpdate` | delegation 子 Agent 的开始、增量和结束 |
| `*ApprovalRequest` | 等待宿主应答的 HITL 请求 |

默认背压在缓冲满时丢弃合同允许丢弃的事件，并产生 `Dropped`。`WithBlockingEvents` 保证不丢弃，但慢消费者会反压 Driver；取消仍会解除阻塞。

`WithEventMeta` 仅供 bridge 和持久化 recorder 回放 typed Event。实时 sink 总会重写权威顺序。

## 7. Approval

HITL 有两种消费方式，但共用同一个 `ApprovalRequest`。

回调方式：

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

事件方式是在 `Stream.Events()` 中接收 `*ApprovalRequest`，保存请求对象并在其他 goroutine 调用它的应答方法。

```go
func (r *ApprovalRequest) Approve(ctx context.Context) error
func (r *ApprovalRequest) Deny(ctx context.Context, reason string) error
func (r *ApprovalRequest) Answer(ctx context.Context, option string) error
```

Kinds：`ApprovalPermission`、`ApprovalPlanReview`、`ApprovalQuestion`。`Approve` 不适用于 Question，`Answer` 只适用于 Question，`Deny` 适用于所有 Kind。

应答 exactly-once。主要错误：

- `ErrApprovalResolved`
- `ErrApprovalExpired`
- `ErrApprovalKindMismatch`
- `ErrApprovalUnavailable`

策略通过 `Policy.Approvals` 配置：Permission 和 PlanReview 使用 `ApprovalMode`；Question 使用 `QuestionMode`；超时或拒绝后的动作使用 `FallbackAction`。零值采用保守默认值。所有显式 mode 都会按 Driver capability 做启动前严格校验。`ApproveAll()` 和 `DenyAll(reason)` 只处理已经通过 capability 校验并路由到 handler 的 Ask 请求。

## 8. Result 与错误

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

高频字段直接暴露；审计和大对象通过方法访问：

```go
func (r *Result) Raw() RawStreams
func (r *Result) Transcript() []TranscriptItem
func (r *Result) Services() []ServiceReport
func (r *Result) Decode(v any) error
```

分层语义：

- `Text` 只包含最终 assistant-facing 文本。
- `Summary` 是可选的短摘要，不保证由每个 Driver 生成。
- `Usage == nil` 表示 provider 未报告用量；非 nil 的零值表示已观察到用量且所有归一化计数明确为零。
- `Raw().Stdout` 与 `Raw().Stderr` 是完整原始进程输出。
- `Raw().Terminal` 保存 Driver 从官方协议识别出的终局事件名与精确 JSON；未识别到时为 nil。
- `Transcript()` 是 Driver 从官方协议解析出的标准化条目，并返回深拷贝。
- `Services()` 是实际确保或 Driver 观察到的 runtime service 报告，不回显秘密环境变量或 MCP 声明。
- `Decode()` 优先解码已经校验的结构化输出；未请求 schema 时会尝试把 `Text` 当 JSON。

业务失败返回 `*RunError`，其中保留部分或完整 Result：

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

`errors.Is` 可匹配 `ErrApprovalDenied`、`ErrApprovalTimeout`、`ErrAgentFailed`、`ErrRunCancelled`、`ErrPolicyViolation`。启动前配置和策略错误可匹配 `ErrInvalidDriverConfig`、`ErrInvalidPolicy`、`ErrPolicyCapabilityUnsupported` 或 `ErrHumanDecisionModeUnsupported`。配置、资源解析、context 取消和其他基础设施错误也走同一个 `error` 判定面；相应 typed error 可通过 `errors.As` 读取 Driver、字段和值等诊断信息。

## 9. 结构化输出

```go
func RunAs[T any](ctx context.Context, r Runner, prompt string, opts ...CallOption) (T, *Result, error)
func WithSchema[T any](opts ...SchemaOption) CallOption
func WithSchemaJSON(schemaJSON []byte, opts ...SchemaOption) CallOption
```

`RunAs` 接收任意 Runner，自动增加 `WithSchema[T]`、执行并解码。

```go
type Review struct {
	Verdict string   `json:"verdict"`
	Issues  []string `json:"issues"`
}

review, result, err := adaptor.RunAs[Review](ctx, reviewer, "review the diff")
```

Schema options：

| 选项 | 语义 |
|---|---|
| `SchemaName(string)` | provider-facing schema 名称 |
| `SchemaDescription(string)` | schema 描述 |
| `SchemaReturnInvalid()` | 校验失败时保留 invalid payload，而不是让执行失败 |
| `SchemaInlineReferences()` | 内联 `$ref`；递归类型会报错 |
| `SchemaAllowAdditionalProperties()` | 允许对象附加属性 |
| `SchemaRequireExplicitTags()` | 仅 `jsonschema:"required"` 字段为 required |
| `SchemaUseGoComments(base, path)` | 用 Go 注释生成描述 |

消费者不选择结构化输出模式。框架固定优先使用 provider 原生 JSON Schema；
当前 transport 或 policy 不支持时，自动回退到 Prompt 加本地校验；两种机制都
不可用才在进程启动前返回 `ErrStructuredOutputUnsupported`。无效 schema 匹配
`ErrInvalidOutputSchema`，Driver 的 `Descriptor.StructuredOutput` 是能力真相源。

## 10. Inspect 与 profile 状态

```go
func (a *Agent) Inspect() Inspector
```

Inspector 只有五个只读方法：

```go
func (in Inspector) Environment(ctx context.Context) (EnvironmentReport, error)
func (in Inspector) Models(ctx context.Context) ([]ModelInfo, error)
func (in Inspector) Quota(ctx context.Context) (QuotaReport, error)
func (in Inspector) ConfigSchema(ctx context.Context) (*ConfigSchema, error)
func (in Inspector) Skills(ctx context.Context) (SkillSnapshot, error)
```

Driver 未实现相应 probe 时，Inspector 返回 descriptor fallback 或明确不可用的报告，不伪造成功。

Profile 和 skill 的有状态动作直接挂在 Agent：

```go
func (a *Agent) ProfileState(ctx context.Context) (ProfileSnapshot, error)
func (a *Agent) SyncProfile(ctx context.Context) (ProfileSnapshot, error)
func (a *Agent) SelectSkills(ctx context.Context, keys []string) (SkillSnapshot, error)
```

- `ProfileState` 只读取 desired 与 observed 状态。
- `SyncProfile` 物化 Driver 支持的资源，并对不支持的资源如实报告。
- `SelectSkills` 安装进程内 selection override；它不替宿主持久化用户偏好。

## 11. tool、skill、MCP 与 profile 词汇包

### 11.1 tool

宿主定义 Tool 使用 typed Go handler，不暴露 MCP server、transport 或 credential：

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

输入和输出 schema 默认从 Go 类型及 tag 推导；`tool.InputSchemaJSON` 与 `tool.OutputSchemaJSON` 是标准 JSON Schema escape hatch。`tool.Reject(code, message)` 表示可安全展示给模型的预期失败；普通 error 与 panic 会被清洗。`WithTools` 仅用于构造期，最后一个选项整体替换此前集合。使用 Thread 时每个 Tool 必须提供稳定 `Revision`，并在行为语义变化时更新。Agent 在内部通过经过鉴权的 loopback MCP runtime 交付这些 Tool；内置 Driver 使用 SDK 自有的隔离执行 profile，避免并发宿主进程改写同一个原生 profile。这些实现机制都不进入调用方 API。完整合同见 [`tools.md`](./tools.md)。

### 11.2 skill

常用构造器：

```go
skill.Dir(path)
skill.FS(fsys, root)
skill.Inline(key, skillMD)
skill.Archive(key, opener, opts...)
skill.Key(key)
skill.Require(value, reason)
```

`WithSkills` 接受 `skill.Ref`。完整 Skill 值可直接使用；`skill.Key` 由 `WithSkillProvider` 注入的 `skill.Provider` 解析。实现 `skill.Catalog` 可让 `Inspect().Skills()` 枚举 catalogue。`skill.Set` 是静态 map-backed 实现。

归档支持 zip、tar、tgz；`NewDefaultSkillMaterializer` 可配置 cache root、archive 大小、文件大小与条目数上限。skill 解析或物化失败会在 Driver 启动前返回错误。

### 11.3 MCP

```go
adaptor.WithMCP(
	mcp.HTTP("docs", "https://example.com/mcp"),
	mcp.SSE("events", "https://example.com/sse"),
	mcp.Stdio("repo-tools", "npx", mcp.Args("repo-mcp")),
)
```

可选项：`mcp.Args`、`mcp.Env`、`mcp.WithHeader`、`mcp.WithHeaders`、`mcp.WithBearerTokenEnv`、`mcp.Required`。transport 与 option 不匹配时不会静默忽略，而是在启动前返回 MCP 配置错误。

### 11.4 profile

Profile selection：

```go
profile.Default()
profile.Native()
profile.Dedicated(dir)
profile.CloneNative(dir, profile.LinkAuth())
profile.CloneFrom(src, dst, profile.CopySettings(), profile.CopyMCP())
```

clone options 包括 `CopySettings`、`CopyMCP`、`CopySkills`、`CopyAuth`、`LinkAuth`、`WithOptions`。OAuth CLI 通常应使用 `LinkAuth`，避免复制会轮转的 refresh token 状态。

`profile.Resources` 可声明 `Skills`、`MCP`、`Agents`、`Hooks`、`Instructions`、`Config`，并通过 `WithProfileResources` 进入统一解析管线。

## 12. Workspace 与 runtime services

直接目录使用 `WithWorkspace(dir)`。需要策略化供应时使用：

```go
adaptor.WithWorkspaceSpec(adaptor.SharedWorkspace{})
adaptor.WithWorkspaceSpec(adaptor.GitWorktreeWorkspace{
	BaseRef:           "main",
	BranchTemplate:    "agent/{run_id}",
	WorktreeParentDir: "/tmp/worktrees",
})
adaptor.WithWorkspaceSpec(adaptor.DriverManagedWorkspace{})
```

`WorkspaceManager`：

```go
type WorkspaceManager interface {
	Resolve(ctx context.Context, req WorkspaceRequest) (WorkspaceLease, error)
	Release(ctx context.Context, lease WorkspaceLease, mode WorkspaceReleaseMode) error
}
```

未注入 manager 时，WorkspaceSpec 通过 passthrough manager 解析为基础目录；需要真正创建 worktree 或 sandbox 时必须提供 manager。

声明式服务：

```go
type ServiceManager interface {
	Ensure(ctx context.Context, req ServiceRequest) ([]ServiceRef, error)
	ReleaseByRun(ctx context.Context, runID string) error
	ReleaseByLabels(ctx context.Context, labels map[string]string) error
}
```

`WithServices` 只声明期望服务；没有 `WithServiceManager` 时声明不会虚构 endpoint。`ServiceRef.MCP` 是服务向本次执行发布 MCP 的类型化入口，`SecretEnv` 只进入 Driver 子进程，不进入 Result report。

`RunServiceProvider` 用于 delegation、浏览器池等每次执行附着的动态服务：

```go
type RunServiceProvider interface {
	AttachRun(ctx context.Context, runID string) (RunAttachment, error)
	DetachRun(ctx context.Context, runID string) error
}
```

`RunAttachment` 可贡献 Services 和一条已经投影成根 Event 的 `RunEventSource`；这些事件直接进入同一 Stream。`RunStarted` / `RunFinished` 由 core 独占，event source 若提供这两种事件会被过滤。SDK 会先发布唯一的 `RunStarted`，在事件源完成 flush 且 run-scoped 资源释放后再发布唯一的 `RunFinished` 并关闭 Events。`DetachRun`、`ReleaseByRun` 和 workspace release 共享一个全局有界预算，但每一步都有公平子预算；任一 source/hook 超时不会阻止后续释放动作被调用。超时或释放错误会进入 `Result()` 的 error，不会被静默吞掉。

## 13. threadstore 与 memory

```go
type Store interface {
	Resolve(ctx context.Context, q Query) (*Record, error)
	Finalize(ctx context.Context, req FinalizeRequest) error
	AcquireLease(ctx context.Context, target, owner string, ttl time.Duration) (Lease, error)
	RenewLease(ctx context.Context, lease Lease, ttl time.Duration) error
	ReleaseLease(ctx context.Context, lease Lease) error
}
```

`Finalize` 必须原子完成 lease 校验、record 保存、旧 record 归档和 key 重绑；Fork 使用 `RequireKeyAbsent` 防止目标冲突。

`memory.NewStore()` 返回并发安全的单进程实现，适用于测试、本地工具和 demo。需要跨进程续接与协调的服务应实现持久化 Store。

## 14. 内置 provider Driver

四个 provider 都采用同一形状：

```go
func Driver(cfg Config) driver.Driver
```

| 包 | Config 主要字段 |
|---|---|
| `codex` | `CommonConfig`、`Model`、`ReasoningEffort`、`FastMode` |
| `claude` | `CommonConfig`、`Model`、`Effort`、`MaxTurnsPerRun` |
| `cursor` | `CommonConfig`、`Model`、`Mode` |
| `codebuddy` | `CommonConfig`、`Model`、`Effort`、`PermissionMode`、`MaxTurnsPerRun` |

各包的 `CommonConfig` 都是 `driver.CommonConfig` 的别名，包含 `Command`、`CWD`、`Env`、instructions、prompt templates、workspace defaults、timeouts、grace period 与 extra args。Driver 构造器对配置做深拷贝快照，后续修改原切片或 map 不影响 Agent。

provider-specific Config 表达 CLI 与 transport 配置；调用级 `WithModel` 等覆盖由根包统一解析。

## 15. Driver SPI 与 adaptertest

第三方 Driver 的最小合同：

```go
type Driver interface {
	Descriptor() Descriptor
	ValidateConfig(cfg any) error
	Run(ctx context.Context, req Request, sink EventSink) (Response, error)
}
```

根包负责选项合并、Thread 协调、workspace/runtime/skill/MCP 解析和结果收口；Driver 负责 provider 配置校验、进程或协议执行、Transcript 解析和 checkpoint 提取。

可选 capability interfaces 包括：

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

`Descriptor` 的声明必须与实现接口和真实行为一致。声明 `Descriptor.Sessions.SupportsResume=true` 的 Driver 必须同时稳定提供 non-nil `SessionCodec` 与 non-empty、跨进程确定的 `SessionConfigFingerprint`；fingerprint 覆盖所有 provider 可见构造配置及 codec/version 合同，不能稳定 canonicalize 时必须报错。公共 Thread 在获取 workspace/runtime/store lease 或派发 Driver 前校验这两项，缺失或不稳定时返回可 `errors.Is(err, adaptor.ErrThreadIncompatible)` 判断的错误。

Driver 发出的 stream sequence、时间戳和根 Event 顺序由 core 分配；Driver 的 `Response.Transcript` 必须与它发出的 transcript item 镜像一致。`Checkpoint.Valid` 只允许在干净成功、官方终局和可 round-trip resume ID 同时成立时设置。

一致性测试：

```go
func TestMyDriver(t *testing.T) {
	adaptertest.TestDriver(t, func() driver.Driver {
		return mydriver.Driver(mydriver.Config{Model: "m-1"})
	}, adaptertest.WithConfig(mydriver.Config{Model: "m-1"}))
}
```

`adaptertest.TestDriver` 检查 Driver/config、capability 真话性、structured output、session codec、session config fingerprint、event 生命周期、Transcript 镜像和 Response 不变量。真实 CLI 探针必须显式使用 `WithLiveRun`；默认测试不应产生外部或付费调用。

`adaptertest.NewReferenceDriver` 是全内存合规参考实现；`VerifyOutcome`、`VerifyStreamSequence`、`VerifyRunEvents`、`VerifyTranscript`、`VerifyTranscriptMirror` 等函数可用于更细粒度测试。

## 16. 相关文档

- [文档地图](./README.md)
- [结构化输出](./structured-output.md)
- [Streaming](./streaming.md)
- [A2A bridge](./a2a.md)
- [架构与发布合同](../AGENTS.md)
