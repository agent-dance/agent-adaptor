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
// # Scope
//
// The package only handles "append a payload, read payloads back by
// cursor". It does not own:
//
//   - pending DecisionRequest tracking — that's per-run runtime state
//     that evaporates when the RunHandle goes away; see the example
//     under examples/streaming-chat-copilotkit for the pattern.
//   - HTTP / SSE transport — that's pkg/bridges/* and the host's HTTP
//     router's job.
//   - routing / sticky-by-thread dispatch across pods — the package is
//     single-process by design. Multi-pod hosts should plug a shared
//     Backend (e.g. Redis, Postgres) below the Recorder.
package sessionrecorder
