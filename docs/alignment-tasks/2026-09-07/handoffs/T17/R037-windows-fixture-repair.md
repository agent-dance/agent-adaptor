# T17 — R037 T26-F01 Windows fixture outcome evidence

Prepared base: `39162b38d0bd5160a5ac9388f868b68b4fb0df14`.
Only the existing Cursor projection test and this handoff change. Production
process handling, protocol parsing, profile resolution and checkpoint rules
remain unchanged.

## Observed failure and diagnostic boundary

On candidate `29e569c6d7610ba28610735b71591f97172ab4d8`, both Windows jobs
failed `TestCursorRuntimeRootsStaySelectedAndCleanupFailureRetainsResponse` /
`Native_runtime_override`. The old message, `Native execution failed <nil>`,
proves the checkpoint was nil; it does not reveal process exit, parser failure,
raw output or a root cause. `Dedicated_cleanup_failure` passed. These historical
records remain intact and are not relabeled with a later diagnosis.

The old Windows fixture used `set /p` with NUL input to write HOME and then
printed its success terminal without an explicit exit code. A retained nonzero
cmd.exe errorlevel is a hypothesis requiring native execution. The test now
runs that exact old script inside the existing Native Windows subtest before
its healthy control. It must observe exit 1, no signal/timeout/Driver error or
protocol Failure, complete terminal/Raw/Transcript, and no checkpoint. An
unexpected outcome fails with all observed fields; it cannot silently validate
the hypothesis. This new probe is not a reconstruction of the old job's missing
observations.

## Fixture change and retained contracts

The healthy script explicitly ends with `exit /b 0` on Windows and `exit 0` on
POSIX. Both Native and Dedicated now require process exit 0, no signal, no
timeout and no protocol Failure. Native additionally requires a valid checkpoint
with the expected resume ID and retains actual HOME/config-root protection
against runtime environment overrides. Dedicated still requires an observable
projection-cleanup error, no checkpoint, and complete Response layers. Its
explicit successful process outcome proves the rejection is due to cleanup,
not an unrelated process exit.

All runs are bounded by a ten-second context. The test logs each fixture's Go
error, exit, signal, timeout, protocol Failure, checkpoint, Raw and Transcript.
No test names, mandatory inventory, skip allowances or original assertions are
removed. The native probe stays in the existing mandatory parent/subtest, so
its failure fails the required Windows item.

## Validation boundary

External evidence: `docs/alignment-execution/2026-09-16/live-repair/handoffs/T17/windows-fixture-repair`.
It records the original V01, targeted controls, full race, closed-live-tag,
vet and Windows test-binary compilation with private HOME/XDG roots, offline
Go 1.27.1 and all paid/golden gates zero. Source snapshots, exact commands,
exit codes, named test counts, raw logs and SHA-256 digests are preserved.

The owner workstation is macOS. Windows cross-compilation does not execute
the old-script probe and cannot establish its exit-code hypothesis or close
T26-F01. The coordinator's complete native Windows Actions, including all 32
mandatory items, must pass on the integrated new source. G05, other native/live
checks and G06 still require that same new source. No real provider, credentials
or user profile are accessed; no workflow, collector or centralized docs change.
