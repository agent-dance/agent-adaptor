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
//   - Root-owned infrastructure that internal tests reach into (dualSink,
//     runOptions, lease-timing vars, the default skill materializer backed
//     by archive_*.go) stays in the root package and is wired into engine
//     through small interfaces and injection points (DecisionSink,
//     PendingFailureSource, LeaseTTL/LeaseRenewInterval,
//     DefaultSkillMaterializerFactory).
package engine
