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
package claude
