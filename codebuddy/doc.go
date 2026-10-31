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
// In-run messages continue through core-managed resume after each provider turn.
// Native hook boundaries and interrupt support remain unverified and are not
// advertised. Both one-shot and resident transports emit one turn-completed
// event per provider turn through the same EventSink.
package codebuddy
