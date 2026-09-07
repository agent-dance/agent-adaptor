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
// Partial output does not certify a resumable checkpoint: cancellation,
// disconnects, and malformed or missing terminal results leave the previous
// healthy Thread record unchanged. A delivered prompt is never replayed
// automatically after a persistent transport failure.
package codebuddy
