// Package a2a exposes an adaptor Runner as an A2A-compatible agent.
//
// Scope:
//   - converts a host-supplied Agent Card to the official A2A model
//   - exposes HTTP handlers that hosts can mount wherever they serve A2A
//   - maps SendMessage and SendStreamingMessage onto Runner.Stream
//   - maps Event, Result, and Cancel onto Task status/artifact updates
//
// The bridge is intentionally Driver-agnostic: it depends only on the core
// Runner and Stream contracts and never imports concrete Driver packages. It uses
// github.com/a2aproject/a2a-go/v2/a2asrv for protocol handling; hosts remain
// responsible for HTTP routing, auth middleware, TLS, tenancy, and durability.
//
// Inbound prompt extraction defaults to the last non-empty text part. Hosts can
// provide a PromptBuilder to support domain-specific message, file, or data-part
// projection without changing the SDK core.
//
// Capability invocations and confirmed todo snapshots are separate explicit
// ExposurePolicy opt-ins, both disabled by default. Tool or diagnostic exposure
// does not enable them. Todo content uses the existing inline-secret filter;
// capability facts contain only their closed observation vocabulary. Upstream
// source coordinates additionally require Diagnostics.IncludeMetadata. An absent
// event does not establish that a capability was unused or a plan was absent.
//
// Both SendMessage and SendStreamingMessage request enabled observation facts
// through a private RunServiceProvider attachment on the same Runner.Stream.
// The attachment needs no Store, observer or second event source. Core remains
// responsible for choosing the resolved provider transport.
//
// adapter.stream.v1 preserves tool scope/parent coordinates, ordered todo clears
// and current EventMeta. Its new shapes reject malformed or oversized payloads;
// outgoing loss becomes a safe stream.dropped with the original coordinates.
// If those coordinates cannot themselves be encoded safely, execution is drained,
// allowed partial artifacts are retained, and the existing infrastructure error
// path reports the failure. Keys and source chains are never truncated to fit.
// Old readers explicitly reject unknown kinds; default-disabled exposure avoids
// sending these kinds without opt-in. RawMessage decoding preserves byte-level
// checks, while already decoded maps cannot recover duplicate-key evidence.
// Legacy tool/diagnostic values and optional zero timestamps keep their existing
// decoding rules. New safety fields are checked without reinterpreting those
// legacy payloads. Thread keys, including JSON-escaped controls, remain opaque;
// only the whole envelope limits their encoded length. OccurredAt values from
// valid offset timestamps normalize to UTC without changing the instant.
package a2a
