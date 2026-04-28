package agentadaptor

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
		Streaming:         cloneBool(b.defaults.Streaming),
		PermissionHandler: b.defaults.PermissionHandler,
		PlanReviewHandler: b.defaults.PlanReviewHandler,
		QuestionHandler:   b.defaults.QuestionHandler,
	}
}

func cloneBool(b *bool) *bool {
	if b == nil {
		return nil
	}
	v := *b
	return &v
}

func cloneInstructions(ref *InstructionsBundleRef) *InstructionsBundleRef {
	if ref == nil {
		return nil
	}
	copyRef := *ref
	return &copyRef
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
