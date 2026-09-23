# R049: Codex cold first-turn failure diagnostics

The original T29-V01 at `8deec330aeb1be94e78b7b48a034ee7968d7f64f`
failed the combined first-turn callback/text guard. Its log does not reveal
which operand failed. Reaching this guard proves that the preceding unchanged
`alignmentColdTurn` guard returned; it does not prove the later checkpoint,
Agent Close, or second-Agent resume assertions ran. No historical cause is
inferred and no production fix is claimed.

Only that first-turn failure branch now logs a closed projection: the callback
count, exact/trimmed text equality, nonce containment, UTF-8 byte counts, and
formal Transcript tool-call/result/error item counts. Probe result attribution
requires a matching nonempty ToolUseID and the same formal scope coordinates,
with one unambiguous tool name. Missing and conflicting associations have
separate counts. These are item counts, not deduplicated executions, and do not
replace the callback or formal capability oracle. The callback value is sampled
when the failure diagnostic runs, after Result has returned.

The projected type contains only booleans and integers. No text, nonce, tool
arguments/results, IDs, names, Raw, credentials, paths, or arbitrary metadata
are logged. There is no Raw parser, new public API, or production behavior.

The original first guard and fatal message remain verbatim. Both prompts, the
model selection, five-minute context, all assertions, second-turn behavior,
and dual live gates remain unchanged. Removing the one new import, one logging
statement, and new private projection block reproduces the base fixture bytes;
the external evidence records that exact comparison.

Offline cases distinguish no/duplicate callback, exact/whitespace/extra/missing
marker text, and both failed operands. They also cover formal scope matching,
missing/ambiguous IDs, identical-call replay, error item counts, and sensitive
sentinels excluded from bounded output. These counterexamples establish the
diagnostic's discrimination, not the cause of the historical live failure.

Independent review of `03a7fbb` found that a trailing empty tool name incorrectly
classified an already ambiguous ID as unmatched. The follow-up preserves the
sticky conflict before checking an empty last name. Regression cases retain
both name orders, conflict followed by empty/probe, and repeated empty names;
none attributes an ambiguous result to the probe. The original review and
author reproduction failures remain archived alongside the new checks.

Validation uses Go 1.27.1, private HOME, offline dependencies, all three live/E2E/
golden gates zero, and an external watchdog: original T16 package count1 and
Alignment race5, tagged diagnostic race1, tagged vet, and the original cold
test's disabled live gate. Exact commands, source hashes, results, and logs are
kept outside the source commit in `live-repair/repairs/R049-T16-cold-diagnostic`.
No real CLI, native authentication, provider call, push, or Actions run is part
of this task. A subsequent complete T29 on the coordinator's new integrated
source remains the live acceptance boundary.
