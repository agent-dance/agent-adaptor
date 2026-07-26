package engine

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

type staticAgentBinding struct {
	adapter  DriverAdapter
	config   any
	defaults AgentDefaults
}

// Bind creates a generic AgentBinding for a custom adapter/config pair.
//
// Built-in packages expose typed constructors such as codex.New; Bind is the
// lower-level helper for hosts implementing their own DriverAdapter. The SDK
// validates the binding during WithDefaultAgent/WithAgent, not here.
func Bind(adapter DriverAdapter, cfg any, opts ...AgentOption) AgentBinding {
	return bindWithDefaults(adapter, cfg, opts...)
}

type typedAgentBinding[T any] struct {
	*staticAgentBinding
	typedConfig T
}

// BindTyped preserves the concrete config type for built-in or custom adapters
// while still returning a standard AgentBinding-compatible value.
func BindTyped[T any](adapter DriverAdapter, cfg T, opts ...AgentOption) TypedAgentBinding[T] {
	return &typedAgentBinding[T]{
		staticAgentBinding: bindWithDefaults(adapter, cfg, opts...),
		typedConfig:        cfg,
	}
}

func bindWithDefaults(adapter DriverAdapter, cfg any, opts ...AgentOption) *staticAgentBinding {
	defaults := AgentDefaults{}
	for _, opt := range opts {
		if opt != nil {
			opt(&defaults)
		}
	}
	return &staticAgentBinding{
		adapter: adapter,
		config:  cfg,
		defaults: AgentDefaults{
			Agent:             defaults.Agent,
			Workspace:         defaults.Workspace,
			Runtime:           cloneWorkspaceRuntimeConfig(defaults.Runtime),
			RunPolicy:         cloneRunPolicy(defaults.RunPolicy),
			Skills:            cloneSkillRefs(defaults.Skills),
			MCP:               cloneMCPConfig(defaults.MCP),
			Agents:            cloneAgentSpecs(defaults.Agents),
			Hooks:             cloneHookSpecs(defaults.Hooks),
			ProfileConfig:     cloneProfileConfigPatches(defaults.ProfileConfig),
			Profile:           cloneProfileSelection(defaults.Profile),
			Instructions:      cloneInstructions(defaults.Instructions),
			Metadata:          cloneStringMap(defaults.Metadata),
			profileDeclared:   defaults.profileDeclared,
			Streaming:         cloneBool(defaults.Streaming),
			PermissionHandler: defaults.PermissionHandler,
			PlanReviewHandler: defaults.PlanReviewHandler,
			QuestionHandler:   defaults.QuestionHandler,
		},
	}
}

// TypedConfig returns the concrete config value captured by BindTyped.
func (b *typedAgentBinding[T]) TypedConfig() T {
	var zero T
	if b == nil {
		return zero
	}
	return b.typedConfig
}

// Adapter returns the bound adapter implementation.
func (b *staticAgentBinding) Adapter() DriverAdapter {
	if b == nil {
		return nil
	}
	return b.adapter
}

// Config returns the adapter config captured at binding time.
func (b *staticAgentBinding) Config() any {
	if b == nil {
		return nil
	}
	return b.config
}

// Defaults returns a defensive copy of the binding-level defaults.
func (b *staticAgentBinding) Defaults() AgentDefaults {
	if b == nil {
		return AgentDefaults{}
	}
	return AgentDefaults{
		Agent:             b.defaults.Agent,
		Workspace:         b.defaults.Workspace,
		Runtime:           cloneWorkspaceRuntimeConfig(b.defaults.Runtime),
		RunPolicy:         cloneRunPolicy(b.defaults.RunPolicy),
		Skills:            cloneSkillRefs(b.defaults.Skills),
		MCP:               cloneMCPConfig(b.defaults.MCP),
		Agents:            cloneAgentSpecs(b.defaults.Agents),
		Hooks:             cloneHookSpecs(b.defaults.Hooks),
		ProfileConfig:     cloneProfileConfigPatches(b.defaults.ProfileConfig),
		Profile:           cloneProfileSelection(b.defaults.Profile),
		Instructions:      cloneInstructions(b.defaults.Instructions),
		Metadata:          cloneStringMap(b.defaults.Metadata),
		profileDeclared:   b.defaults.profileDeclared,
		Streaming:         cloneBool(b.defaults.Streaming),
		PermissionHandler: b.defaults.PermissionHandler,
		PlanReviewHandler: b.defaults.PlanReviewHandler,
		QuestionHandler:   b.defaults.QuestionHandler,
	}
}

func validateAgentBinding(binding AgentBinding) error {
	if binding == nil {
		return ErrAgentBindingRequired
	}
	adapter := binding.Adapter()
	if adapter == nil {
		return ErrAgentBindingRequired
	}
	if err := validateAdapterConfig(adapter, binding.Config()); err != nil {
		return err
	}
	return nil
}

// profileDeclarationsFromDefaults folds implicit declarations (non-empty
// resource slices) into the explicit profileDeclared flags captured by the
// WithDefault* options.
func profileDeclarationsFromDefaults(defaults AgentDefaults) ProfileResourceDeclarations {
	declared := defaults.profileDeclared
	if len(defaults.Agents) > 0 {
		declared.Agents = true
	}
	if len(defaults.Hooks) > 0 {
		declared.Hooks = true
	}
	if len(defaults.ProfileConfig) > 0 {
		declared.Config = true
	}
	if defaults.Instructions != nil {
		declared.Instructions = true
	}
	return declared
}
