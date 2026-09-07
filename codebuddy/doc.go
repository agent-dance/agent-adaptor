// Package codebuddy provides the built-in Driver implementation for the
// CodeBuddy CLI.
//
// Construct a configured driver with [Driver] and pass it to adaptor.New:
//
//	agent := adaptor.New(codebuddy.Driver(codebuddy.Config{
//		Model: "claude-sonnet-5",
//	}))
//
// Driver snapshots its configuration without performing environment I/O.
// Configuration validation and CLI availability checks occur when the agent
// runs or is inspected.
//
// Interrupted persistent turns preserve the official protocol's available
// text, transcript, usage, and raw streams in the failed run's RunError.Result.
// The original transport or context error remains reachable with errors.Is/As.
// An observed process ExitError is preserved alongside that cause; an observed
// nonzero exit is classified as an agent failure unless a more specific
// context, approval, or provider failure takes precedence. Partial usage sums
// distinct formal message IDs and deduplicates their cumulative counters;
// explicit terminal usage, including zero, remains authoritative.
// Partial output does not certify a resumable checkpoint: cancellation,
// disconnects, and malformed or missing terminal results leave the previous
// healthy Thread record unchanged. A delivered prompt is never replayed
// automatically after a persistent transport failure.
//
// Native append uses WithAppendSystemPrompt through a separate
// --append-system-prompt argv element, preserving exact UTF-8 bytes and the
// provider default instructions. The inline limit is 32768 UTF-8 bytes. When
// nonempty, final Windows executable/argv quoting and shim limits are checked
// before spawn: 32767 UTF-16 units including NUL, or 8191 for cmd shims. Unsafe
// cmd shell characters are rejected. SDK invocation diagnostics redact the
// value; the OS can still expose inline arguments. Provider Raw is complete.
// All four system/append ExtraArgs flags are reserved and rejected even when
// the SDK append value is empty. Append content participates in Thread and
// process compatibility, including checkpoint guards for direct SPI calls.
//
// Formal stream-json, control, and persistent turns observe Skill.command or
// Skill.skill, Task/Agent.subagent_type, and exact mcp__server__tool catalog
// names. Ambiguous or unknown names are not attributed. Tool results confirm
// completion; unconfirmed calls close as interrupted or explicitly cancelled.
// Batch JSON has no capability or todo observation support. No event proves
// audit completeness or the absence of an unobserved call.
//
// CodeBuddy 2.137.1 TodoWrite confirms newTodos only after its official success
// result. TaskCreate/TaskUpdate/TaskList prefer tool_result._meta.rawResponse's
// full todos list and real task IDs. Missing IDs in confirmed creations are
// explicitly synthetic and cannot match TaskUpdate.taskId. Valid empty lists
// clear the run-local snapshot; invalid data leaves it unchanged with a safe
// notice. Raw tool arguments/results and Transcript remain available. A new
// run does not inherit a local task cache.
//
// CodeBuddy's user result wrapper parent_tool_use_id can equal its own call
// ID, so this driver does not claim parent graph observation. Foreign or
// malformed result parent fields cannot complete a root call. Unproved nested
// assistant/partial wrappers are not merged into the root observation scope.
package codebuddy
