# T01 — Claude one-shot terminal stdin lifecycle

## Behavior and reproduction

A bidirectional one-shot Claude run can receive the formal top-level frame
`{"type":"result","subtype":"success","is_error":false,"session_id":"alignment-stdin","result":"done"}`
without a preceding `message_stop(end_turn)`. Previously stdin stayed open, so a
CLI waiting for EOF remained alive after its result. A fake CLI which emits that
frame, waits for stdin EOF, then writes trailing stdout/stderr reproduces the
failure within a 3-second deadline. The same fixture covers failure results and
native `structured_output` payloads.

The parser now releases its input handle exactly once after either a terminal
root assistant message or a formal result, including failure results. The same
helper handles decision abort/write failure/interrupt, so a later result cannot
close twice. Empty or `tool_use` stop reasons preserve the control channel.
Nested `parent_tool_use_id` message start/delta/stop frames cannot reset or
replace the root stop reason, or close root stdin; tool-result content is not
promoted into a run result. Tests send another control request after nested
completion and verify its response.

Resident Thread turns keep the existing `nonClosingStdin` adapter. Releasing the
turn handle never closes the underlying process stdin; two interactive turns
reuse one process. Stateless Agent calls and Thread `WithSpawn()` calls finish
through stdin EOF. No lifecycle, execution-entry, or checkpoint API was added.

EOF only releases process input. The Driver still drains output, retains the
exact terminal JSON and Transcript, checks the actual process result, and emits
one final outcome. A provider failure, cancellation, malformed protocol, or
post-terminal frame cannot become a healthy checkpoint. Run and Stream.Result
preserve equal Text, Summary, Raw (including trailing stdout/stderr and Terminal),
Transcript, Usage, and Services for the success/failure one-shot fixtures.

## Documentation integration for G01

- `claude/doc.go`: implemented package godoc explaining root terminal closure,
  intermediate/nested input availability, resident handles, and final output
  draining/checkpoint validation.
- `claude/README-streaming.md`, “Provider transport”: implemented the one-shot
  input lifecycle paragraph. The existing native-schema/HITL capability text is
  intentionally unchanged; T31/T07 own that negotiation and its correction.
- `docs/streaming.md`, provider process/Claude transport discussion: merge:

  > Claude's one-shot bidirectional transport closes stdin exactly once after a
  > formal `type:result`, including when no terminal `message_stop` preceded it.
  > Terminal root assistant messages may also release input; `tool_use` and
  > nested subagent messages keep it available for control responses. Resident
  > Thread turns release only their per-turn handle and keep process stdin open.
  > The Driver drains stdout/stderr and validates the process outcome before
  > publishing the final event or persisting a healthy checkpoint.

- `CHANGELOG.md`, next unreleased “Fixed” section: merge:

  > Fixed Claude bidirectional one-shot runs hanging after a result-only
  > terminal response by closing stdin once. Nested subagent completion no
  > longer closes the root control channel. Resident Thread reuse and complete
  > terminal output/checkpoint validation are preserved.

These central fragments must be merged by G01 before B01 acceptance.

## Public declarations and dependency decision

No new public Go declarations, root options, Driver capability claims, modes,
golden changes, or dependencies. This is a private Claude parser lifecycle fix
using the existing stdin controller and resident adapter. Existing Go test
subprocess fixtures are sufficient; no process/protocol dependency is warranted.

## Source evidence and rejected transfers

Read only committed Git objects from the internal repository:
`eefcaef35621e6bbfb3e2e859edf4aadc4e38a5d`,
`a88eacdb4818c75df44caeec39bbb74062e58c53`, and merge
`d01a086e5145566b7313b83ca88bb525f1d3675b`.
The shared close concept is retained. Legacy root Driver types, Phase 3 names,
capability tracker callbacks, stdout-derived Text, and permissive checkpoint
behavior are not imported. Current strict protocol/post-terminal/checkpoint
contracts remain authoritative. The root/nested stop-reason fence additionally
protects the current stream parser; T14 still owns nested typed-event association.

## Verification and limits

Before implementation, all three result-only subprocess cases reached their
3-second deadline and lost EOF tail bytes. The root/nested lifecycle negative
fixture also failed. These red-run logs are retained as evidence, not success
checks.

Required final validation is `go test -count=1 ./claude` and
`go test -race -count=5 ./claude -run TestAlignmentStdin`, with `-json` added only
for exact test/subtest counts. Actual post-commit SHA, exit codes, counts and logs
are in the separately collected `result.json` and evidence directory. Live
conformance, E2E, and API golden update environment gates are all `0`.

Validation runs on macOS Darwin/arm64 with Go 1.26.5 and a local Go test executable
as the fake CLI. The full package includes existing nonzero-exit, malformed,
missing-terminal/checkpoint, native output, fork, HITL, resident/single-writer,
and output contracts. Its two explicitly disabled live conformance probes are
permitted skips. Sandbox denial of the existing user-profile lock and loopback
fake MCP was resolved by rerunning the unchanged command with authorized local
permissions.

Native structured-output termination is exercised through the production parser,
real stdin controller, and structured-response validation. This task does not
claim public interactive+native schema support, which is still rejected on this
base and belongs to T31/T07. Cancellation/terminal races assert bounded Stream
closure and stable cancellation; broader interrupted partial Result improvements
belong to T05/T07. No real provider, Linux, or Windows live/platform checks were
run. T20/T27 and later gates supply independent integration/live evidence.
