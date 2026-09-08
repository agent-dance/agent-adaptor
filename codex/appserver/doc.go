// Package appserver is a typed client for the codex app-server JSON-RPC
// protocol over stdio. It is the backing transport used by codex adapter
// runs whose resolved driver.Request selects provider-native streaming.
//
// The package is organised into five files:
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
// Protocol upgrades follow the generation procedure in generate.go.
package appserver
