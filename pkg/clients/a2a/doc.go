// Package a2a provides thin, host-oriented client primitives for remote A2A
// agents.
//
// The package deliberately stays protocol-shaped:
//   - it fetches, validates, and caches Agent Cards
//   - it exposes Send, SendStream, Subscribe, GetTask, and CancelTask
//   - it preserves task IDs, context IDs, status, artifacts, raw protocol
//     payloads, and protocol errors in stable agent-adaptor-owned DTOs
//   - it surfaces typed protocol errors without introducing SDK RunResult semantics
//
// The implementation delegates discovery and wire transports to the official
// github.com/a2aproject/a2a-go/v2 SDK.
package a2a
