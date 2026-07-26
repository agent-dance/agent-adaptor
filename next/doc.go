// Package adaptor is the v1 consumer API of agent-adaptor.
//
// This package currently lives in the next/ staging directory while the v1
// rewrite is in progress (strangler route, see docs/api-v1-implementation-plan.md
// §1). It moves to the repository root in P5; nothing outside next/ should
// import it before then.
//
// The API is organized around six nouns (docs/api-v1-redesign.md §0):
// Agent, Thread, Stream, Event, Result, and Driver. P0 delivers the first
// four building blocks of that vocabulary:
//
//   - Agent: adaptor.New(driver, opts...) + Agent.Run — construct and go.
//   - Options: one vocabulary, two scopes. The same WithX used in New is the
//     agent default; used in Run it is a per-call override. Scope misuse is a
//     compile error (see Option / CallOption / SharedOption).
//   - Result: flat high-frequency fields plus Raw() / Transcript() /
//     Services() / Decode() audit accessors.
//   - RunError: business failures are typed errors carrying the full Result;
//     "if err != nil" is the single verdict point.
//
// Stream/Event (P1), Thread (P2), and the skill/mcp/profile vocabulary
// packages (P3) join in later phases.
package adaptor
