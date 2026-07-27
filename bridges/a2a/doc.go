// Package a2a exposes an adaptor Runner as an A2A-compatible agent.
//
// Scope:
//   - converts a host-supplied Agent Card to the official A2A model
//   - exposes HTTP handlers that hosts can mount wherever they serve A2A
//   - maps SendMessage and SendStreamingMessage onto Runner.Stream
//   - maps Event, Result, and Cancel onto Task status/artifact updates
//
// The bridge is intentionally adapter-agnostic: it depends only on the core
// Runner / Stream contracts and never imports concrete adapters. It uses
// github.com/a2aproject/a2a-go/v2/a2asrv for protocol handling; hosts remain
// responsible for HTTP routing, auth middleware, TLS, tenancy, and durability.
//
// Inbound prompt extraction defaults to the last non-empty text part. Hosts can
// provide a PromptBuilderV1 to support domain-specific message, file, or data-part
// projection without changing the SDK core.
package a2a
