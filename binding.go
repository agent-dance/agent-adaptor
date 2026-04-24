package agentadaptor

type staticAgentBinding struct {
	adapter  DriverAdapter
	config   any
	defaults AgentDefaults
}

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

func (b *typedAgentBinding[T]) TypedConfig() T {
	var zero T
	if b == nil {
		return zero
	}
	return b.typedConfig
}

func (b *staticAgentBinding) Adapter() DriverAdapter {
	if b == nil {
		return nil
	}
	return b.adapter
}

func (b *staticAgentBinding) Config() any {
	if b == nil {
		return nil
	}
	return b.config
}

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
