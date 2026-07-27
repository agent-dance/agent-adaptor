// Package engine hosts the SDK execution pipeline extracted from the root
// agentadaptor package (v1 refactor P0.2, see docs/api-v1-implementation-plan.md).
//
// Layering rules:
//
//   - engine MUST NOT import the root package (the root package is a thin
//     wrapper over engine, so an engine → root import would be a cycle).
//   - Contract types the pipeline depends on live here (or in the driver
//     package) and are re-exported from the root package via type aliases,
//     so the historical root API surface is unchanged.
//   - Runtime machinery is self-sufficient here: the default skill
//     materializer and its archive extraction (archive_*.go,
//     skill_materializer.go) and the lease-timing defaults need no wiring
//     from the root package (v1 refactor P5.2 removed the init() injection).
//   - Root-owned infrastructure that internal tests reach into (dualSink,
//     runOptions) stays in the root package and is wired into engine through
//     small interfaces (DecisionSink, PendingFailureSource).
package engine
