# T23 conformance, architecture and platform-entry changes

Consumer execution semantics and exported root/Driver declarations are unchanged.
No golden, generated schema, runtime implementation, go.mod or dependency changes.
The approved root `With*` count remains 27: the original 26 plus the already
accepted `WithAppendSystemPrompt`. No root option is introduced by T23.

## Merge into Driver conformance documentation

`adaptertest` now checks the previously accepted observation contract rather than
treating `capability.invocation` and `todo.updated` as unknown vendor events.
Clause DRV-03 requires independent mutable Descriptor/HITL snapshots; EVT-14
checks scoped tool IDs, stable explicit parent coordinates, unique results and
no ArgsDelta after a complete Args snapshot. OBS-01–06 validate actual resolved
transport negatives, exclusive semantic payloads, closed safe provider values,
per-scope invocation lifecycles and confirmed full Todo snapshots/revisions.
A non-nil empty Todo array is a clear; no observed event never proves no call.
Provider source provenance and catalog membership still require provider fixtures
and real protocol evidence; the generic verifier does not parse provider JSON.

Example: two nested scopes may both use tool ID `x`; each has its own start/end
and result. A capability start without its terminal is rejected before a provider
terminal. A Todo `[]` at revision 2 after a populated revision 1 is accepted;
nil items, revision gaps, repeated snapshots and unknown states are rejected.
Fault-injected fixtures make these checks distinguish the rejected behaviors.
Core's final public outcome remains independent of the preserved provider terminal.

Targets: `docs/streaming-adapter-contract.md` clause catalogue and validation
section; `docs/streaming.md` observation/parent-field and dropped-state guidance.
Local `adaptertest/doc.go` is synchronized in this commit. Existing MUST/SHOULD,
SessionCodec nil/zero laws, typed output and four-provider conformance stay intact.

## Merge into CI / contributor instructions and CHANGELOG

The automatic workflow closes live/E2E/golden gates globally, keeps vulnerability
scanning and all frontend install/build/lint/audit steps, retains the nine 30-second
fuzz targets, and makes uncached full/race/repeated/scenario checks explicit.
It adds BDD feature parsing, Cursor's disabled-live-tag check, a native Windows
job, and native executable/PowerShell Unicode, newline and quote argv fixtures.
Windows ownership/ACL/reparse/no-delete-sharing and process-tree tests execute
on the Windows runner. Darwin development and Windows cross-compilation do not
claim native Windows success. Full logs/process exits and nonzero named-test
passes, with permitted opt-in skips listed separately, remain necessary.

Targets: CHANGELOG validation/infrastructure notes and contributor CI guidance.
`b06-commands.md` and `b06-inventory.json` enumerate exact T25–T30 commands,
platform/profile prerequisites, existing named live scenarios and the R017
coverage corrections accepted at replacement G04 `b2035bc793369fb8fefb9de229ff1dbd2b748853`.
Canonical22 has 43 tasks, 96 requirements and a six-worker ceiling, including
the original bridge owner's T32 supplement. The refreshed live inventory has
28 real-provider roots (7 Claude, 11 CodeBuddy, 5 Codex, 5 Cursor); two separately
listed Codex gate canaries do not count as provider execution.
`check_go_json.py` refuses empty execution, missing named
passes, failed tests/packages and any skip not explicitly listed by exact ID.
Test selection/listing, disabled paid probes and compilation are evidence of
availability only, never real Linux/Windows/provider acceptance.

The architecture test inspects all platforms' production sources. It prevents
reverse dependency paths and transitive local aliases exposing internal types;
allows only the precise a2adelegation -> internal/activebudget implementation
exception; and bars dispatch-bearing Driver SPI from bridge/hosttool execution.
Neutral public driver vocabulary imports already used for wire enum/env values
are legal; they do not authorize direct Driver execution. Existing root AST
golden and single invocation architecture guards are preserved unchanged.

## Evidence limits / rejected historical behavior

No paid provider or user credential is accessed by T23. No live capability or
platform result is inferred from internal historical commits, fake fixtures,
static test names or a successful compile. `findings.md` preserves G04 pre-R017
coverage gaps and their exact owner/requirement links, with each accepted
replacement fixture recorded as coverage closed. T23 source commits were
migrated to the accepted replacement G04 before fresh final validation.
G05 still owns implementation freeze and B06 owns native/live execution.


T23-F06 follow-up: provider dependency guards uniformly cover Codex, Claude,
CodeBuddy and Cursor roots and package subtrees. The negative oracle checks
Driver/bridge/hosttool importers, while similar prefixes remain legal. This
corrects a T23 guard blind spot; it is not a new runtime or public API change.

C03-QA01/02 follow-up: capability parents cannot reference their own scoped
invocation identity; equal IDs in different scopes remain legal. The shared
private envelope check enforces zero Sequence/Seq/Timestamp and the existing
Role rule for rich, observation-only and native batch payloads. Observation-only
Drivers still need no rich run.* frames. Independent suite-entry oracles use
an in-memory Driver, preserving the distinction between verifier coverage and
real provider evidence. Merge these clarifications with OBS-03 / EVT-10 in the
conformance guide; exported declarations and runtime behavior are unchanged.
