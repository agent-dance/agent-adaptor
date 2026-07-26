package agentadaptor

import "context"

// SDK is the host-facing entry point for executing bound local agents.
//
// The SDK is intentionally default-agent-first: Run and Start always target
// the binding supplied by WithDefaultAgent, while Agent(name) returns a Runner
// for an explicitly registered named binding. Hosts should keep routing,
// tenancy, HTTP/RPC concerns, and "which agent should handle this request"
// decisions above this interface.
type SDK interface {
	Run(ctx context.Context, prompt string, opts ...RunOption) (RunResult, error)
	Start(ctx context.Context, prompt string, opts ...RunOption) (RunHandle, error)

	Default() Runner
	Agent(name string) (Runner, error)

	Admin() AdminAPI
}

// Runner is the execution contract shared by the default binding and every
// named binding. It lets workflow code accept "the chosen agent" without
// needing access to SDK construction, Admin APIs, or the named-agent registry.
type Runner interface {
	Run(ctx context.Context, prompt string, opts ...RunOption) (RunResult, error)
	Start(ctx context.Context, prompt string, opts ...RunOption) (RunHandle, error)
}

// RunHandle represents one asynchronous execution returned by Start.
//
// Hosts use Events for operational status, StreamEvents for token/tool/HITL
// streaming when enabled, DecisionRequests/ResolveDecision for async HITL
// channel mode, and Wait for the final RunResult. All channels are closed by
// the SDK when the run ends, so consumers may range over them safely.
type RunHandle interface {
	// Events streams operational RunEvents (chunks, lifecycle, spawn, etc.).
	Events() <-chan RunEvent
	// StreamEvents streams normalized StreamPayloads produced by
	// stream-aware adapters. When streaming was not enabled for the run (or
	// the adapter does not emit them) the returned channel is closed and
	// empty; consumers can `for range` it safely.
	StreamEvents() <-chan StreamPayload
	// RunID returns the stable run identifier assigned by the SDK. It is
	// available as soon as Start() returns, before Wait() completes.
	RunID() string

	// Wait blocks until the run terminates or ctx is cancelled.
	//
	// The error model is two-layered (see docs/run-policy.md §5):
	//
	//   - The returned error is an infrastructure-layer error — the agent
	//     did not finish (ctx cancel, adapter crash, protocol break, SDK
	//     panic recovery). RunResult fields may be incomplete.
	//   - RunResult.Failure is the business-layer failure — the agent ran
	//     to completion but the result is a failure (HITL reject / timeout,
	//     adapter-declared error, policy validation). RunResult fields are
	//     complete.
	//
	// Hosts should check err first, then Failure, then success.
	Wait(ctx context.Context) (RunResult, error)
	Cancel(ctx context.Context) error

	// DecisionRequests returns the channel of HITL DecisionRequest envelopes
	// destined for host-side resolution. It only carries requests whose Kind
	// does not have a typed handler mounted (see WithPermissionHandler /
	// WithPlanReviewHandler / WithQuestionHandler). Consumers can `for range`
	// it safely; the channel is closed when the run ends.
	DecisionRequests() <-chan DecisionRequest

	// ResolveDecision resolves a DecisionRequest delivered through
	// DecisionRequests(). Returns ErrDecisionRequestExpired if the request is
	// unknown or already consumed; ErrDecisionResultKindMismatch if Result is
	// incompatible with the request Kind; ErrRunEnded when the run has
	// already terminated.
	ResolveDecision(requestID string, resp DecisionResponse) error
}

// AdminAPI is the control-plane view over the same default/named agent model
// used for execution. It never executes prompts; it is for diagnostics,
// settings UIs, skill inventory, profile introspection, and capability probes.
type AdminAPI interface {
	Default() AgentAdmin
	Agent(name string) (AgentAdmin, error)
	Agents() []AgentInfo
}

// AgentAdmin exposes management probes for one bound agent. Implementations
// are backed by adapter capability interfaces, so unsupported probes return
// truthful fallback reports instead of inventing data.
type AgentAdmin interface {
	Info() AgentInfo
	CheckEnvironment(ctx context.Context) (EnvironmentReport, error)
	ListModels(ctx context.Context) ([]ModelInfo, error)
	DetectModel(ctx context.Context) (*DetectedModel, error)
	GetProfile(ctx context.Context) (AgentProfile, error)
	ProfileSnapshot(ctx context.Context) (ProfileSnapshot, error)
	SyncProfile(ctx context.Context) (ProfileSnapshot, error)
	ConfigSchema(ctx context.Context) (*ConfigSchema, error)
	GetQuota(ctx context.Context) (QuotaReport, error)
	ListSkills(ctx context.Context) (SkillSnapshot, error)
	// SetSelectedSkills overrides the process-local default selection for
	// this agent. The override replaces the binding's WithDefaultSkills
	// values for subsequent Run / Start calls on this agent; Required
	// skills from the SkillProvider continue to appear regardless. The
	// override is not persisted across process restarts.
	SetSelectedSkills(ctx context.Context, keys []string) (SkillSnapshot, error)
}

// AgentBinding couples one DriverAdapter with its validated config and
// binding-level defaults. Built-in packages return AgentBinding values from
// New(cfg, opts...), and custom adapters can use Bind or BindTyped.
type AgentBinding interface {
	Adapter() DriverAdapter
	Config() any
	Defaults() AgentDefaults
}

// TypedAgentBinding is an AgentBinding that also exposes the concrete config
// type. It is useful in tests, admin tooling, or custom adapter plumbing that
// wants to inspect strongly-typed configuration after binding.
type TypedAgentBinding[T any] interface {
	AgentBinding
	TypedConfig() T
}

// AgentDefaults are binding-level defaults merged into every Run/Start call
// before per-call RunOptions are applied. They are copied on binding
// construction and when returned from AgentBinding.Defaults, so callers may
// inspect the value without mutating live SDK state.
type AgentDefaults struct {
	Agent           AgentIdentity
	Workspace       WorkspaceSpec
	Runtime         *WorkspaceRuntimeConfig
	RunPolicy       *RunPolicy
	Skills          []SkillRef
	MCP             *MCPConfig
	Agents          []AgentSpec
	Hooks           []HookSpec
	ProfileConfig   []ProfileConfigPatch
	Profile         *ProfileSelection
	Instructions    *InstructionsBundleRef
	Metadata        map[string]string
	profileDeclared ProfileResourceDeclarations
	// Streaming marks the binding as streaming-by-default when non-nil and
	// true. Per-call WithStreaming / WithoutStreaming still wins. Using a
	// pointer keeps the three states (nil / true / false) distinct so that
	// clones do not accidentally downgrade an opt-out to a default.
	Streaming *bool

	// per-Kind typed HITL handlers bound at agent level. Per-call
	// WithPermissionHandler / WithPlanReviewHandler / WithQuestionHandler
	// override these.
	PermissionHandler PermissionHandler
	PlanReviewHandler PlanReviewHandler
	QuestionHandler   QuestionHandler
}

// AgentInfo is the admin/listing view of a bound agent. Hosts commonly use it
// to render settings screens, capability badges, and named-agent pickers.
type AgentInfo struct {
	Name        string
	Default     bool
	DriverType  string
	DisplayName string
	Descriptor  DriverDescriptor
}
