package agentadaptor

import "github.com/agent-dance/agent-adaptor/internal/engine"

// Bind creates a generic AgentBinding for a custom adapter/config pair.
//
// Built-in packages expose typed constructors such as codex.New; Bind is the
// lower-level helper for hosts implementing their own DriverAdapter. The SDK
// validates the binding during WithDefaultAgent/WithAgent, not here.
func Bind(adapter DriverAdapter, cfg any, opts ...AgentOption) AgentBinding {
	return engine.Bind(adapter, cfg, opts...)
}

// BindTyped preserves the concrete config type for built-in or custom adapters
// while still returning a standard AgentBinding-compatible value.
func BindTyped[T any](adapter DriverAdapter, cfg T, opts ...AgentOption) TypedAgentBinding[T] {
	return engine.BindTyped[T](adapter, cfg, opts...)
}
