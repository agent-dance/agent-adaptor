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
// App-server invocations deliver messages with turn/steer (Codex >=0.100.0),
// interrupt and restart in the same thread, and consume turn-end continuations
// inside one driver Run. Exec invocations expose continuation through resume
// only. The driver declares the minimum version but does not probe CLI versions.
// Native output schemas are sent on every turn/start, including continuation
// and interrupt restarts, and the final turn is validated before completion.
// Responses retain all turns' raw streams and transcript, sum their observed
// usage, and expose the last turn's assistant text and terminal payload.
package codex
