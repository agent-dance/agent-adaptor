// Package sessionrecorder is an opt-in host utility that records typed
// agent-adaptor events under a host-owned session key and serves them back by
// a cursor that stays monotonic across runs. EventRecorder,
// NewMemoryEventBackend, and NewJSONLEventBackend are the v1 API. The
// StreamPayload-based Recorder and its backends exist only for migration and
// are removed by the v1 root cutover.
//
// # Placement
//
// This package lives under hosttools because:
//
//   - the core SDK (github.com/agent-dance/agent-adaptor) does not import
//     it — AGENTS.md §1 and §11 explicitly forbid the core from growing
//     server-side / persistence responsibilities;
//   - it is one concrete answer to the "UI session history recovery"
//     problem docs/workstream-hitl-v2.md §4.3.1 outlines but deliberately
//     leaves to the host;
//   - it keeps the SDK stateless by default while still
//     saving every host from writing the same JSONL-plus-cursor plumbing.
//
// # HostSeq vs provider/run sequence numbers
//
// Event ordering is scoped to one run. Two runs that share the same host-side
// session key — for example a browser's stable thread id that survives a page
// refresh — restart their run-local sequence, so that sequence alone cannot
// serve as a cross-run recovery cursor. A naive filter over a provider or
// run-local sequence can fold old-run events into the new window and reorder
// the stream.
//
// HostSeq is the cursor this package assigns. It is strictly monotonic
// within one session key regardless of how many runs contribute events
// to that session, so the standard increment-and-resume protocol
//
//	afterHostSeq := lastKnownHostSeq
//	records, _ := recorder.Since(ctx, sessionKey, afterHostSeq)
//	lastKnownHostSeq = records[len(records)-1].HostSeq
//
// works across arbitrary run boundaries.
//
// # Choosing a sessionKey
//
// sessionKey is intentionally a neutral aggregation key. There are two
// canonical patterns; pick one and keep it stable per logical thread
// you want to read back:
//
//  1. Audit style (recommended starting point): sessionKey = RunID
//     - one record stream per run, immutable after the run ends
//     - downside: cannot accumulate cross-run history of the same
//     logical conversation; the host correlates RunIDs externally
//
//  2. Conversation style: sessionKey = ThreadID (or any host-stable
//     "logical thread" identifier)
//     - history accumulates across resumed/forked runs under the same UI
//     conversation
//
// EventRecorder assigns HostSeq in process. All access for one sessionKey must
// therefore route to one process, regardless of which key style is chosen.
// Multi-process hosts need sticky routing or a coordinator-aware EventBackend
// that owns sequence allocation transactionally.
//
// # Scope
//
// The v1 API handles "append an Event, read Events back by cursor". The
// StreamPayload pending-decision derivers remain migration-only alongside the
// legacy Recorder.
//
// The package does NOT own:
//
//   - persistent HITL pending state — derive it on demand from the
//     existing history. The recorder remains the single source of
//     truth; introducing a separate pending dimension creates
//     double-write inconsistency risk.
//   - HTTP / SSE transport — that's bridges/* and the host's HTTP
//     router's job.
//   - routing / sticky-by-thread dispatch across pods — the package is
//     single-process by design. Multi-pod hosts should plug a shared
//     EventBackend (e.g. Redis, Postgres) below the EventRecorder.
//   - fan-out (Stream → SSE + EventRecorder + metrics) — that's a few lines
//     of host-owned for loop; see examples/streaming-chat-copilotkit
//     for the canonical pattern.
package sessionrecorder
