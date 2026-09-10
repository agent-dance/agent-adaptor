// Package codex provides the built-in Driver implementation for Codex.
//
// Construct a configured driver with [Driver] and pass it to adaptor.New:
//
//	agent := adaptor.New(codex.Driver(codex.Config{
//		Model: "gpt-5.4",
//	}))
//
// Driver snapshots its configuration without performing environment I/O.
// Configuration validation and transport availability checks occur when the
// agent runs or is inspected. The appserver subpackage contains the typed
// Codex app-server transport used when that transport is selected.
//
// adaptor.WithAppendSystemPrompt uses the native developer instruction channel:
// exec/exec resume receive a TOML developer_instructions override; app-server
// start/resume/fork receive developerInstructions. Empty clears the SDK default.
// Exec accepts at most 32768 UTF-8 bytes and validates the prepared command line;
// app-server has no SDK inline limit. The text is visible in exec OS argv but is
// redacted from SDK invocation diagnostics. Raw provider output stays complete.
// ExtraArgs cannot override any native system/developer instruction source.
// Unrelated config overrides retain Codex's literal-string fallback when the
// value is not TOML, including unquoted values such as model_reasoning_effort=high.
// Append content participates in Thread compatibility and resident signatures.
//
// Observation support is transport-specific: exec reports no capability or todo
// facts. App-server reports exact catalog-matched MCP lifecycles, accepted typed
// skill inputs selected by explicit $name references, and spawn operations only
// when an official child role and current-turn collab receiver match. Skill
// NativeInputAccepted completion proves acceptance of the input, not execution
// or reading of its contents. Declarations and ordinary text are not evidence.
// Subagent spawn completion proves the spawn operation, not the child task's
// eventual success. Unknown/ambiguous identities stay unobserved. No parent tool
// association is fabricated when the official protocol has none.
//
// Official turn/plan/updated produces complete ordered todo snapshots, including
// clears. IDs identify synthetic turn/position slots, never provider task IDs.
// Malformed plans leave the prior snapshot unchanged with a safe notice. Current
// thread/turn and terminal fences apply; experimental plan deltas remain opaque.
// Valid historical token-usage replay for the same thread is audit-only Raw.
//
// A resident turn snapshots the stdout and stderr received for that turn.
// Stdout terminal delivery does not acknowledge receipt on the independent
// stderr pipe. Bytes already received are preserved exactly; future unframed
// stderr is not inferred from the terminal and cannot amend a returned Result.
// One-shot and failed-process cleanup also drain the process streams within
// their shutdown bounds. A healthy connected resident remains available for
// the next turn without waiting for process exit.
// If stdout has ended, bounded drain/Wait precedes the health decision. A formal
// successful terminal followed by a clean exit preserves its checkpoint; EOF
// without a successful terminal, malformed protocol, cancellation or a nonzero
// exit cannot produce a healthy checkpoint.
package codex
