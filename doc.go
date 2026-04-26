// Package agentadaptor is a pure Go SDK for running local coding agents
// through one execution contract.
//
// The public model is default-agent-first: construct an SDK with
// WithDefaultAgent, call SDK.Run or SDK.Start for that binding, and use
// SDK.Agent for any additional named bindings. Every path resolves defaults
// and per-run overrides into one DriverRunRequest before it reaches the
// adapter.
//
// Sessions are optional. Without a SessionStore each run is stateless. With a
// SessionStore, SessionRequest.Namespace and SessionRequest.Key form the
// stable host-facing session key, while SessionRef.ID is the SDK/adapter
// resume handle returned by a successful checkpoint.
//
// RunResult deliberately separates final assistant text (Output), raw process
// streams (RawStreams), structured transcript entries (Transcript), short
// summaries (Summary), and provider terminal payloads (Result). Adapters own
// provider protocol parsing; shared helpers only move bytes and events.
package agentadaptor
