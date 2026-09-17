// Package appserver is a typed client for the codex app-server JSON-RPC
// protocol over stdio. It is the backing transport used by codex adapter
// runs whose resolved driver.Request selects provider-native streaming.
//
// The main protocol implementation files are:
//
//   - generate.go / generated.go — schema-derived flat notification types.
//     Generated from schema/{v1,v2}/*.json via go:generate; do not edit by
//     hand.
//   - union.go — hand-written discriminated unions that go-jsonschema cannot
//     express (ThreadItem, UserInput, SandboxPolicy, CommandAction,
//     WebSearchAction, and the envelope Params/Response types we call
//     directly).
//   - codec.go — stdioStream, a minimal jsonrpc2.ObjectStream adapter that
//     bridges a subprocess's stdin (io.WriteCloser) and stdout (io.Reader)
//     into the format sourcegraph/jsonrpc2 expects. It does not mutate
//     frames; sourcegraph/jsonrpc2 already tolerates missing
//     "jsonrpc":"2.0" markers.
//   - client.go — sourcegraph/jsonrpc2.Conn wrapper with typed method
//     helpers for Initialize, ThreadStart, ThreadResume, TurnStart,
//     TurnInterrupt, and notification subscription. Chosen over
//     creachadair/jrpc2 because its Handler is dispatched synchronously
//     and preserves wire order of notifications.
//   - run.go — driver-facing entry point that owns the codex app-server
//     subprocess lifecycle for a single Run.
//   - translate.go — notification → StreamPayload mapping.
//   - child_metadata.go — bounded optional role lookup for a formal spawn receiver.
//
// Client.Close stops new RPC calls, releases active waiters, and closes its
// owned transport once. The original synchronous reader alone settles pending
// responses on EOF/read error, so an in-flight response cannot race an external
// pending-channel close. Notification FIFO and server-request rejection remain
// on that reader. Close can be called from a notification handler without
// waiting for itself. Caller cancellation and RPC errors retain their causes;
// a client-only shutdown remains recognizable through IsDisconnected.
//
// Logical client shutdown, reader disconnect, raw-stream drain, and OS process
// exit are separate boundaries. Process owners retain their bounded shutdown,
// full Raw capture and actual Wait joins; failed/cancelled turns gain no healthy
// checkpoint. Healthy resident turns do not wait for process exit.
//
// A successful turn/start response records the current thread's turn identity
// at the synchronous reader boundary, even when cancellation has already
// released the RPC waiter. After the existing shutdown drain and process join,
// the owner retains the confirmed turn's partial text, usage and transcript.
// The normal successful callback order is unchanged. Unmatched, failed or
// malformed responses never bind identity; notifications still pass the same
// thread/turn fence. Cancellation keeps its original cause and cannot create
// a healthy checkpoint or trigger prompt replay. No additional wait is added.
//
// Codex automatically attaches this connection to multiple threads. Known
// scoped notifications first validate their envelope and required scope. Valid
// foreign turn/item/usage/error/status notifications remain Raw audit data;
// their inner item variants are opaque, they do not bind identity or publish
// parent semantics, and a foreign terminal is not a duplicate parent terminal.
// Malformed scope/status, same-parent wrong turns and duplicate parent terminals
// remain protocol errors. Foreign thread/started metadata is considered in
// notification order only before the parent's terminal; it never completes a
// turn. Conflicting previously accepted direct-child identity fails immediately.
//
// Subagent capability facts require both a current parent spawn receiver and
// official direct-parent role metadata resolved to a unique catalog key. If
// that receiver lacks metadata, one private worker requests thread/read with
// includeTurns=false. Per turn it admits at most 128 distinct receivers, with a
// one-second call deadline and a two-second total terminal settlement budget.
// Missing/unsupported metadata produces an unresolved notice; first invalid
// evidence rejects that receiver for the turn. A missing parent is unavailable,
// not a contradictory parent; incomplete frames are not combined into role proof.
// The bounded identity table keeps
// rejections sticky, including at saturation. No arbitrary foreign ID is queried.
// Admitted reads settle before the public terminal and Result freeze. A single
// notification barrier joins any in-flight handler before the owner snapshot;
// all later notifications remain Raw only. Child work completion is not inferred.
//
// An abandoned metadata response permanently pauses further optional reads on
// that resident process. The original request ID still owns a late wire reply;
// it cannot apply metadata to a later turn. Raw captures only bytes actually
// received in each run's capture interval and returned results remain frozen.
// Cancellation cannot unblock a synchronous pipe write by itself. If the worker
// misses settlement, the process owner closes its transport and performs bounded
// cleanup before joining. Actual write/EOF/close causes remain in the error chain,
// with partial audit retained and no healthy checkpoint or writer reuse.
//
// MCP tool-result transcripts treat an absent or JSON-null error as no error.
// A non-null formal error or failed tool status remains a tool failure, matching
// the capability observation without rewriting its result, Raw or turn terminal.
//
// Protocol upgrades follow the generation procedure in generate.go.
package appserver
