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
package codex
