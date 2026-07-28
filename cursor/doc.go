// Package cursor provides the built-in Driver implementation for the Cursor
// Agent CLI.
//
// Construct a configured driver with [Driver] and pass it to adaptor.New:
//
//	agent := adaptor.New(cursor.Driver(cursor.Config{
//		Model: "gpt-5",
//	}))
//
// Driver snapshots its configuration without performing environment I/O.
// Configuration validation and CLI availability checks occur when the agent
// runs or is inspected.
package cursor
