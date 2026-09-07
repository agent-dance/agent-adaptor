// Package adaptor provides one API for running local coding agents.
//
// The API is organized around six nouns:
//
//   - Agent is a configured, ready-to-run driver plus host defaults.
//   - Thread adds durable resume and fork semantics to an Agent.
//   - Stream represents one execution in progress.
//   - Event is one typed observation from that execution.
//   - Result is its final output and audit record.
//   - Driver is the provider integration SPI implemented in package driver.
//
// Construct an Agent directly from a provider Driver. Multiple agents are
// ordinary Go variables; the package has no central SDK object or registry:
//
//	agent := adaptor.New(
//		codex.Driver(codex.Config{Model: "gpt-5.4"}),
//		adaptor.WithWorkspace("/repo"),
//	)
//	result, err := agent.Run(ctx, "Review this change")
//
// Run and Stream share one execution pipeline. Run is exactly Stream followed
// by draining Events and calling Result. Agent and Thread both implement
// Runner, so bridges and host integrations do not need separate stateful and
// stateless paths.
//
// On Windows, macOS, and Linux, built-in Drivers reuse one provider process
// across compatible Thread turns by default when the provider supports it.
// WithSpawn forces a fresh process for an Agent default or one call, and
// Agent.Close performs bounded, idempotent cleanup of processes owned by that
// Agent.
//
// Options have explicit scopes. Option applies at construction, CallOption
// applies to one invocation, and SharedOption may be used in either place.
// Per-call values override Agent defaults; skills append, while other settings
// follow their documented replacement or merge rules.
//
// WithAppendSystemPrompt adds exact native append text only when the configured
// Driver declares support; it never replaces Prompt or Instructions. Policy's
// ActiveExecutionTimeout pauses for this run's Ask waits and seals before atomic
// persistence; Finalize still determines success with the original context.
//
// Host-defined Tools use the provider-neutral tool package and the
// construction-only WithTools option. The Agent owns their runtime for its
// full lifetime; MCP delivery, authentication, and endpoint lifecycle remain
// internal implementation details. Existing external MCP servers continue to
// use WithMCP.
//
// An Agent is stateless unless constructed with a ThreadStore. Agent.Thread
// continues or creates the host key, while Thread.Fork creates an independent
// child. The SDK coordinates leases, compatibility fingerprints, and atomic
// checkpoint persistence; provider resume identifiers remain Driver details.
//
// Stream exposes one ordered typed Event channel. ApprovalRequest events carry
// their own exactly-once responder, allowing either callbacks or interactive
// hosts to approve, deny, or answer. Result separates assistant Text and
// Summary from Raw process streams, Transcript entries, runtime Services, and
// validated structured output. After Driver.Run is entered, every failure uses
// RunError with the available Result and the original error chain in Cause.
// Pre-invocation failures remain ordinary wrapped errors.
//
// CapabilityInvocation and TodoUpdated carry validated leaf vocabulary from
// capability and todo. Optional RunAttachment observers see those facts before
// user backpressure; optional hosttools/capabilityrecorder provides scoped live
// queries through an explicit host-owned Store. Attachment publishers join the
// same Event stream. Every
// admitted execution has one core lifecycle whose terminal reflects the final
// Result and resource cleanup. Static pre-admission refusals have no lifecycle.
package adaptor
