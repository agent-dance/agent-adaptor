// Package sessionrecorder is an opt-in host utility that records
// agent-adaptor StreamPayload events under a host-owned session key and
// serves them back by a cursor that stays monotonic across runs.
//
// # Placement
//
// This package lives under pkg/hosttools because:
//
//   - the core SDK (github.com/agent-dance/agent-adaptor) does not import
//     it — AGENTS.md §1 and §8 explicitly forbid the core from growing
//     server-side / persistence responsibilities;
//   - it is one concrete answer to the "UI session history recovery"
//     problem docs/workstream-hitl-v2.md §4.3.1 outlines but deliberately
//     leaves to the host;
//   - it keeps the SDK stateless by default (AGENTS.md §2.3) while still
//     saving every host from writing the same JSONL-plus-cursor plumbing.
//
// # HostSeq vs StreamPayload.Seq
//
// StreamPayload.Seq is per-run monotonic by contract
// (docs/workstream-streaming-chat.md §4, run_types.go `StreamPayload`).
// Two runs that share the same host-side session key — for example a
// browser's stable thread id that survives a page refresh — restart the
// Seq counter at zero, so Seq alone cannot serve as a cross-run recovery
// cursor: a naive `ev.Seq > afterSeq` filter over mixed runs will fold
// old-run events into the "new" window and reorder the stream.
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
//     - multi-pod hosts: routing-agnostic; any pod can append
//     - downside: cannot accumulate cross-run history of the same
//       logical conversation; the host correlates RunIDs externally
//
//  2. Conversation style: sessionKey = ThreadID (or any host-stable
//     "logical thread" identifier)
//     - history accumulates across multiple SDK SessionIDs (forks /
//       continues) under the same UI thread
//     - downside: multi-pod hosts MUST route stickily by sessionKey,
//       otherwise HostSeq across pods will collide and the cursor
//       protocol breaks
//
// Single-process / single-pod hosts can pick either. Multi-pod hosts
// should default to (1) and only graduate to (2) once they have a
// sticky-routing layer (or plug a coordinator-aware Backend such as
// one using Redis INCR).
//
// # Scope
//
// The package handles "append a payload, read payloads back by cursor",
// plus a small set of derivers that operate on records:
//
//   - PendingDecisions(records): one-shot snapshot of pending HITL
//     requests, suitable for admin dumps and REST handlers (O(n)).
//   - PendingTracker: incremental Apply/Snapshot type for long-running
//     session loops where re-deriving on every event would degrade to
//     O(n²); thread-safe, O(1) per Apply.
//
// The package does NOT own:
//
//   - persistent HITL pending state — derive it on demand from the
//     existing history. The recorder remains the single source of
//     truth; introducing a separate pending dimension creates
//     double-write inconsistency risk.
//   - HTTP / SSE transport — that's pkg/bridges/* and the host's HTTP
//     router's job.
//   - routing / sticky-by-thread dispatch across pods — the package is
//     single-process by design. Multi-pod hosts should plug a shared
//     Backend (e.g. Redis, Postgres) below the Recorder.
//   - fan-out (stream → SSE + Recorder + metrics) — that's a few lines
//     of host-owned for loop; see examples/streaming-chat-copilotkit
//     for the canonical pattern.
package sessionrecorder
