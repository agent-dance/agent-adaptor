# T12: non-A2A event fidelity and transparent Merge

Source base: `75332ea502b42ce40229840f3eae75c103b5a9f2` (G02 source base, revision 12 / R001–R011; attempt 2 validated against canonical revision 13).
Requirements: W05-R05, W09-R11, W12-R09. This is the T12 implementation handoff; G03 must merge the central documentation before batch acceptance. It does not close W05/W09/W12 or provider/live verification.

## Public behavior and examples

Raw SSE now emits `capability.invocation` and `todo.updated` frames. The existing top-level map and `meta` envelope remain; capability and todo payloads use C03 snake_case fields. Tool call/result frames retain `scope_id`, `parent_scope_id`, and `parent_tool_call_id`. All new Source coordinates and the complete `upstream` chain survive. An observed duration of zero remains `duration_ns: 0`; unobserved duration is omitted. A full empty todo array is emitted as a clear:

```json
{"meta":{"run_id":"r","sequence":9,"time":"2026-09-07T00:00:01Z"},"todo":{"items":[],"source":"plan_update","revision":2,"occurred_at":"2026-09-07T00:00:01Z"}}
```

AG-UI uses explicit CUSTOM events because its native event vocabulary has no capability/todo equivalents:

- `adapter.capability.invocation`: value has `kind`, `meta`, and `capability`.
- `adapter.todo.updated`: value has `kind`, `meta`, and `todo`, including empty `items`.
- `adapter.tool.parent` before each tool start: value has `meta`, original `tool_call_id`, `scope_id`, `parent_scope_id`, and `parent_tool_call_id`.

Source chains exceeding eight nodes or containing a cycle are projected as an explicit safe `relay_depth_exceeded` runtime notice, without pretending to preserve a truncated chain. These are private wire projections built from B02 public types; no dependency on same-batch A2A DTOs is introduced. The AG-UI tool card ID is now `aa1:` plus unpadded base64url of compact UTF-8 JSON `["tool",runID,scopeID,ID]`, using HTML escaping disabled. Tool results use the same card ID. This is a client-visible ID change that avoids collisions between equal provider IDs in different scopes. CUSTOM parent data is the explicit graph mapping; AG-UI clients must consume it when rendering the graph.

`RunError.Reason` takes precedence over a secondary cancellation in `Cause` when AG-UI emits its terminal error. CloseResult still obtains the final outcome from the parent Stream.Result; no additional execution/terminal authority is introduced.

The session recorder's existing private `{host_seq,recorded_at,kind,meta,event}` envelope gains kinds `capability.invocation` and `todo.updated`. `event` retains the typed public payload's existing Go JSON field names; `meta` continues to use its stable snake_case format. New parent and recursive Source fields round-trip. Input, history, backend and query records hold independent mutable values. Every recorded approval, including in-memory history, is descriptive and has no responder.

JSONL rejects unknown kinds/fields, duplicate keys, invalid UTF-8/unpaired surrogate escapes, required null/missing observation fields, invalid closed enums, invalid parent/source coordinates and invalid snapshot values. Load reports `ErrJSONLEventLogCorrupt`; append encoding/write/sync errors remain observable. Failed appends never advance the cached HostSeq or create a successful in-memory record. Existing rollback/poison/Flush/Close behavior is retained.

A2A's envelope and safe-number limits do **not** apply to SSE, AG-UI or JSONL. These consumers retain complete valid observations, including UTF-8 content at the 4096-byte item boundary, records larger than 64 KiB, full uint64 event/host cursors, and the native duration range. JSONL's typed value limits remain the leaf package contracts, not a transport truncation policy.

## Breaking Merge correction and migration

Previously `subagentstream.Merge` subscribed to a delegation bus, injected its mirrors, renumbered parent events and synthesized delegation terminals. This conflicted with the core sink's unique sequence and terminal authority. The published function signature and nil-bus passthrough remain. A non-nil bus now produces a transparent wrapper that never subscribes to or waits for the bus, injects mirrors, reassigns metadata, or creates terminal events.

When the parent Events channel closes, Merge obtains and caches its Result and checks a private structural `RunEventsBound(runID string) bool` proof. A true historical proof preserves the parent Result/error exactly. Missing/false proof returns `nil, *adaptor.RunError` containing the complete available parent Result and `ErrEventInjectionUnsupported`. An existing RunError's primary reason/message/details remain intact; the entire original parent error graph is joined with the sentinel without mutating the parent error. This preserves `errors.Join` siblings, custom wrapper `Is`/`As` behavior, multi-`%w` causes and the original error identity. A direct RunError points only to the original immutable parent, never to its copied wrapper. Bare or wrapped `context.DeadlineExceeded` maps to `ReasonDeadlineExceeded`; existing RunError primary reasons retain precedence over secondary context errors. Proof is checked after completion, so asynchronous attachment binding is allowed. Concurrent/repeated Result callers receive the same cached outcome.

Cancel immediately cancels the parent and unblocks wrapper sends, then drains the cancelled parent through closure to preserve its partial Result. The wrapper creates no extra RunFinished. A conforming parent's Cancel/Events/Result lifecycle remains necessary; the bridge cannot make an arbitrary non-closing Stream terminate.

Migration: install the delegation Service's existing `team.Option()` before `New` or `Run`/`Stream`, and consume the original Stream. Do not feed `SubagentEvent` UI mirrors back into the core. The bridge-only test fake proves the structural interface independently; the concrete EventBus historical proof and Service publisher wiring belong to T18/B04 and are not claimed as delivered in T12.

```go
// team is the host-created delegation Service; configuredDriver is its Agent's Driver.
leader := adaptor.New(configuredDriver, team.Option())
stream := leader.Stream(ctx, prompt)
for event := range stream.Events() {
    consume(event)
}
result, err := stream.Result()
```

## Central merge targets

G03 must merge the following before accepting B03:

- `docs/streaming.md`: event table, scoped tool identity, nested Source, empty Todo clear; SSE/AG-UI CUSTOM mapping and Merge migration section.
- `docs/api-reference.md`: record/replay semantics and the `subagentstream.ErrEventInjectionUnsupported` error; no new root entry point.
- `docs/public-errors.md`: Merge configuration failure wrapping and JSONL corruption/append failure behavior.
- `CHANGELOG.md`: added non-A2A capability/todo/parent fidelity and recorder deep copies; breaking Merge behavior and AG-UI tool card ID changes; primary RunError classification correction.
- `AGENTS.md` §14: T12 bridge/recorder implementation is delivered subject to independent validation; concrete T18 delegation wiring and B05/B06 evidence remain open.

Local godoc is already updated in each owned package. The only new public declaration is the C03-frozen `subagentstream.ErrEventInjectionUnsupported`. Root and Driver APIs/goldens are unchanged. `RunEventsBound` is consumed through a private interface; no same-batch symbol is required.

## Source choices and dependencies

Fixed internal references: `3ea225acea572830100381a5c180dd86f295a70d` (nested tools), `1921636510ced4c830c0287fe68d41218f1ed185`, `9b1ce27faa4e46ba6a213210eaffc8748b327bdb`, `dab6933b9159894c36fa78b8367c6bc572227621` (observations), `eb82ed36eaf6339b00860b5506a683e6dbd74765` (Todo); background records read only with `git show` at fixed `e2f0620bdd6477e6fe16f6db5648093589342ca2`.

No internal provider recognition, guessed task IDs, empty-snapshot dropping, text truncation, direct Driver execution, observation Admin facade, or multiple channel model is copied. The bridge consumes already typed facts; provider confirmation belongs to Drivers.

Dependency selection: no new top-level require. Existing AG-UI protocol dependency handles its native events; small private DTO conversion and strict JSON checks use the standard library and remain inside the owning bridge/recorder. A general framework would not improve these bounded translations enough to justify another runtime dependency.

## Verification scope

A fixture-only first run at the G02 production base failed in all four packages: missing new events/parents/Source, recorder aliases, and Merge sequence replacement. The saved baseline failure log records the actual assertions. Final full T12 package tests, local race tests and vet evidence are generated after the source commit and identify that exact SHA, commands, counts, skips and exit statuses in result.json.

This task runs on macOS arm64 with Go 1.26.5. Ordinary validation explicitly disables both live gates and golden updates. Local httptest loopback listeners require the authorized sandbox escalation; no remote provider is contacted. Linux/Windows, paid live, whole-repository release gates, T18 concrete integration and T21 independent verification are not claimed.

## Attempt 2 independent-review repair

Independent review of `2778effb89f952400bfd271f899ee1a4f74a8163` reproduced T12-F01 (lost outer wrapper/join error causes) and T12-F02 (bare deadline misclassified as infrastructure). The unchanged reviewer fixture was rerun before this repair and failed on all three outer-wrapper cases plus the deadline case. `merge_review_test.go` preserves those fixtures and strengthens original-error identity, secondary-deadline and primary-reason assertions. The repair affects only Merge error wrapping/classification, its contract tests and this fragment; no Event sequence, binding proof, Result field or execution entry changes. Required full package, race and vet commands are rerun on the final repair SHA, with attempt-1 evidence retained separately.

## Attempt 2 approval-description repair

Independent T12-F03 fixtures exposed a distinction in the public replay-copy behavior: preserving an ApprovalRequest's response authority did not copy its mutable Choices/Details. Each owned consumer now explicitly snapshots the description. Recorder and Raw SSE allocate their own Choice slice; recorder, SSE and AG-UI copy nested Details through the public map-carrying Event copy contract without publishing that intermediate value. AG-UI's existing choice projection already constructs fresh wire values. No root-package changes or responder changes are introduced. Real DecisionSink Permission/PlanReview/Question fixtures prove recorded descriptions remain unavailable for all response methods while the original request still completes its live round trip. Memory/JSONL input, returned record, query and backend snapshots are tested independently; SSE and AG-UI CUSTOM output cannot be altered by later source mutation. F03 is reproduced before the fix on `02e8b4e791ba0f87911363cef275f1a5edcc0360`; that SHA and all prior logs remain preserved. The final four-package/race/vet evidence supersedes its earlier attempt-2 check logs.
