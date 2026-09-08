# T35: stdin completion is safe with two finishers

G05 owns the central documentation changes below. This fragment accompanies the
shared-helper repair for the native Linux T25-F03 panic; it does not introduce a
new public execution or error contract.

## Behavior and reproduction

The subprocess writer and the process-wait path can both finish the same stdin
controller. Previously `markDone` released its mutex before checking and closing
the completion channel. Both callers could choose the open-channel branch and
one would panic. The original Linux arm64 / Go 1.26.5 full-race run at
`d94fd17a24f3710a079c79b6932b99a85af6e911` observed this during
`TestAlignmentClaudeApprovalFailures/question/deny`. The original log and its
SHA-256 provenance are retained in this handoff's evidence directory.

The helper now sets its closed state and checks/closes the completion channel
under the same existing short mutex. Either finisher may arrive first, and both
legal call sites remain. Reading the buffered writer-error message is not treated
as joining the writer's deferred completion.

`Close` still means no more input: a healthy writer drains already accepted
frames after the initial prompt, in FIFO order, before closing stdin. `markDone`
means the writer or process has ended and releases blocked writes. A previous
`Close` does not suppress this later completion notification. Late writes keep
returning `ErrStdinClosed`; before-ready and full-queue waits are released. The
mutex does not cover waiting for readiness, enqueuing, OS writes, or joining a
goroutine. The producer queue is not closed.

This keeps the existing Claude and CodeBuddy Driver responsibilities: their
formal parsers and the common invocation pipeline still preserve available
Response/Result layers and original causes, prioritize established approval or
host-cancellation causes, reject unhealthy checkpoints, and prohibit replay after
possible prompt delivery. The helper continues to transport stdin, manage the
process, capture raw streams, and tee chunks; it interprets no provider payload,
approval kind, failure reason, or checkpoint.

## Regression coverage and evidence limits

`TestStdinLifecycleBlockedWrite` uses Go's `testing/synctest` to establish a
durably blocked before-ready or full-queue write before termination. The finisher
order tests release explicit channels for writer-first, process-first, and
simultaneous starts, including `Close` before completion and completion before
`Close`. They wait for both finishers and verify notification and rejected writes.
Concurrent starts add race pressure; they do not claim to force both old callers
inside the unsafe default branch. The native Linux panic is the pre-fix red
evidence, without a production hook or a recover wrapper.

The real child-process fixture reexecutes only its own test in a private HOME and
workspace. Writer-first waits for stdin EOF and requires the writer's completion
notification before allowing output drain to finish; process-first exits with
the input controller still open. Healthy FIFO prompt/frame output and complete
stdout/stderr are asserted. Separate cancellation and observer-error cases wait
for observed output before aborting, retain raw bytes, and preserve the observer
sentinel. No installed provider, external network, or sleep-based ordering is
required. Existing Claude approval denial/timeout/cancel/Continue and resident
partial/cause/checkpoint tests are retained unchanged.

The child fixture's input phase also waits on a bounded context and notices Run
returning before readiness. Failure cleanup releases the controller and joins its
input goroutine. The cancellation oracle requires a nil helper error alongside
the canceled caller context and recorded killed-process outcome; an unrelated
helper error cannot satisfy that case.

The final report records the four required commands, their exact source SHA,
Go/OS/environment, exit codes, counts, allowed skips, and hashed logs. Local
execution uses native macOS arm64 and the installed Go 1.26.5 binary with private
HOME, offline module/toolchain settings, and live/E2E/golden-update gates set to
zero. T35 does not establish a new Linux/Windows result or real-provider success.
T25 must independently repeat its original 15 Linux commands and 3 supplements
at the replacement G05 SHA; T27 real Claude verification remains separately gated.

## Central integration targets

- `docs/streaming.md`: in input and cancellation lifecycle guidance, state the
  distinction between stopping new input with FIFO drain and terminal completion
  that releases writes, with duplicate completion safe under competing finishers.
- `docs/streaming-adapter-contract.md`: in shared-helper/Driver boundaries, retain
  both writer and process completion exits and the Driver's exclusive authority
  for formal parsing, failure cause, and checkpoint health.
- `docs/public-errors.md`: in interrupted-result preservation, clarify this repair
  prevents a lifecycle panic from interrupting the existing partial-result/cause
  pipeline; it adds no success-side failure field or parallel error route.
- `CHANGELOG.md`: record the shared stdin duplicate-close race fix affecting
  Claude approval/cancellation and the shared CodeBuddy transport.
- `AGENTS.md` section 14.1: close the T25-F03 implementation subitem only with its
  concrete evidence, while keeping replacement G05 and native Linux/live gates
  separate from this worker delivery.

## API, dependencies, and internal-history boundary

No public declaration, API/golden file, generated file, dependency, Driver, or
parser changes are needed. The only production delta is the helper's existing
mutex boundary plus a local explanation of the two finishers.

The adopted lesson from internal `126d610dfb6afd2cca19661ab0a7c0b6f626b490`
remains preserving available interrupted output and original causes. Its relaxed
error-path checkpoint behavior is not adopted: an observed session identifier
does not prove a healthy resumable terminal, and old healthy records must remain
unchanged after failure. This patch changes neither checkpoint validation nor
pre-delivery fallback and post-delivery replay rules.
