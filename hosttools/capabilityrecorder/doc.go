// Package capabilityrecorder provides optional, scoped capability observation
// storage through adaptor's run-service observer. Add Recorder.Option to New or
// a Run/Stream call, then Query the exact IdentityID, Tenant, Profile and RunID.
// Records become visible when Append succeeds, before user event backpressure,
// so querying an active run does not require a UI or a user Events consumer.
// A blocking Stream can still stall further execution until drained or cancelled.
//
// Only accepted CapabilityInvocation values are stored. The package does not
// parse provider protocols, infer calls from configured resources, or record
// prompts, arguments, results, raw streams, credentials or free-form errors.
// No observations means only that no facts were observed; it proves neither
// non-use nor complete auditing or billing coverage. NextSequence=0 describes
// the current page, not completion of a run or its observation history.
//
// Core invokes observers sequentially per run with a 100ms wall-clock bound,
// shortened by remaining cleanup time. First Append error, panic or timeout
// disables only that run's observer and emits one safe observation_disabled
// notice. Result, approvals and checkpoints retain their original semantics.
// Late callbacks cannot re-enable the observer or create a second notice.
// Custom stores must honor context before commit: arbitrary late host side
// effects cannot be rolled back. Callbacks must not wait synchronously on the
// current run; concurrent Query is supported.
//
// Store lifetime and any persistence or authorization belong to the host.
// Exact scope filtering is not caller authentication: a host exposing queries
// must authorize the requested scope. Agent.Close never closes a shared Store.
// New requires an explicit Store; NewMemoryStore provides only process-local
// memory, and never silently substitutes for requested durable storage.
package capabilityrecorder
