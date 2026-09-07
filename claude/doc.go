// Package claude provides the built-in Driver implementation for the Claude
// Code CLI.
//
// Construct a configured driver with [Driver] and pass it to adaptor.New:
//
//	agent := adaptor.New(claude.Driver(claude.Config{
//		Model: "claude-sonnet-4",
//	}))
//
// Driver snapshots its configuration without performing environment I/O.
// Configuration validation and CLI availability checks occur when the agent
// runs or is inspected.
//
// Native schema supports Question/PlanReview Ask over bidirectional stream-json.
// Effective Permission Ask (including an unset Permission) selects prompt
// validation through core's per-mechanism capability matrix. A zero raw policy
// retains observational transport; the driver never changes it to auto-approve
// Permission. Native schema Thread turns use a temporary process after stopping
// any resident writer; WithSpawn suppresses subsequent resident prewarming.
//
// Errors preserve the available formal protocol output and original cause for
// RunError.Result. Incomplete or failed runs cannot produce a healthy checkpoint.
//
// In bidirectional one-shot runs, a formal result or a terminal root assistant
// message closes host input exactly once, allowing the CLI to finish even if
// it omits message_stop. Tool-use and nested message stops keep input available.
// Resident Thread turns release only their per-turn input handle; the process
// remains available for the next turn. Output is drained and checkpoint health
// is checked against the process outcome before the final event is emitted.
package claude
