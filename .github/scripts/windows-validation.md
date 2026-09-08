# T26 native Windows verification

The `Windows alignment verification` workflow runs on pushes to
`codex/windows-validation-*`. It uses Windows Server 2022 and the exact Go
version from the SDK's `go.mod`. The controller branch contains the workflow and
collector; a second checkout pins the SDK to the exact triggering commit.
Both revisions are recorded. The initial frozen G05 source failed native Windows,
so each repaired candidate now runs both Windows and all 18 original T25 Linux
checks. Earlier source acceptance is never inherited by a changed candidate.

The original `go test -count=1 ./...` runs with only `-json` added. The codec and
offline examples run as programs, followed by `go test -count=1 ./examples/...`.
An additional Windows regression repeats the received-turn cancellation and
public partial-result scenarios 20 times; both exact test roots must pass every
iteration. This supplements the original commands after a native cancellation
failure exposed an unbound, already received turn/start response.
A separate Cursor check repeats the public Run/Stream comparison, Driver output
comparison and deterministic equivalence controls 20 times. All three exact roots
must pass without skips. It checks each pipe's complete ordered content across
independent runs while retaining exact event-to-Result order within each run.
Process-external deadlines do not change test counts or Go test timeout flags.
Private HOME, USERPROFILE, APPDATA, caches and temporary directories prevent
implicit use of provider profiles. Paid live, E2E and golden-update gates stay off.

The collector requires every mandatory Windows root to pass, rejects unknown
skips, package failures, missing test terminals and zero-test results, and verifies
tracked bytes against their committed Git blobs before and after every command.
The explicit skip policy documents only existing Windows-inapplicable fixtures,
disabled live probes and declared unsupported probes; it cannot satisfy a required
test. DACL, reparse-point, lock-sharing, process-tree and native argv tests stay
mandatory. Windows runner privileges must allow the existing symlink/DACL tests;
an unavailable prerequisite or failed assertion fails verification.

Actions artifacts retain raw logs, command exits/counts, OS/Go/PowerShell versions,
source hashes, collector revision and per-file SHA-256 hashes, including on failure.
The coordinator must download and independently audit them before changing T26's
external handoff result. A green workflow supports Windows verification only;
provider live tasks and G06 release readiness remain separate.
