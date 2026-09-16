# T26 native Windows verification

The `Windows alignment verification` workflow runs on pushes to
`codex/windows-validation-*` and `codex/review-fixes-*`. It uses Windows Server 2022 and the exact Go
version from the SDK's `.github/go-version`. This CI toolchain includes Go's
official fix for fuzz deadline shutdown (issue 75804); `go.mod` still declares
the minimum consumer toolchain, tested separately by the `minimum-go` CI job.
The original 30-second fuzz budgets and failure checks are unchanged.
The controller branch contains the workflow and
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
mandatory. The six `TestWindowsAppend*` roots require private creation, DACL
drift rejection, the complete lifetime pin and retryable cleanup, trusted owner
checks, hard-link rejection, and same-directory atomic publication without
releasing the directory pin or overwriting a conflicting target; their subtests may not skip. Windows runner privileges must allow the existing symlink/DACL tests;
an unavailable prerequisite or failed assertion fails verification.

R033 and its approved T17 follow-up require eleven exact Cursor roots to the full T26 mandatory-pass inventory:

- `TestCursorWindowsProjectionCreatesProtectedObjectsAndRejectsChangedACL`
- `TestCursorWindowsPrivateDescriptorRejectsWrongOwnerAndUnprotectedACL`
- `TestCursorPrivateHomePublicationNeverReplaces`
- `TestCursorProjectionConcurrentFirstUse`
- `TestCursorProjectionCleanupRejectsReplacementAndLinks`
- `TestCursorRuntimeRootsStaySelectedAndCleanupFailureRetainsResponse`
- `TestCursorProjectionIsPerRunBoundedAndPreservesSources`
- `TestCursorProjectionRejectsForeignOwnershipLinksAndCancellation`
- `TestCursorProjectionSizeLimitAndSourceSymlinks`
- `TestCursorWindowsOfficialEnvironmentNamesAreCaseInsensitive`
- `TestCursorProjectionSnapshotTracksDeliveredBytes`

Each identity is qualified by `github.com/agent-dance/agent-adaptor/cursor:`.
Missing roots, an unmatched selector, a skip, or only a passing child cannot
satisfy the required top-level pass. Their subtests have no skip exemptions.
These checks cover private descriptor ownership/protected ACLs, publication
without replacement, concurrent first use, projection bounds/source preservation,
selected runtime roots, cleanup failures, case-insensitive Windows environment
selection and fingerprints of the bytes actually delivered. The existing full T26 command and
all supplemental checks remain unchanged; these eleven roots do not change the
three-root 20-iteration Cursor equivalence selector.

Synthetic collector regressions reject each missing, skipped or child-only root
through `execute_check("T26-V01", ...)`, while a complete set of exact passes is
accepted. These regressions establish collector behavior only. Actual execution
on the final frozen SDK commit in native Windows Actions is still required;
Darwin checks or cross-compilation cannot establish ACL, publication or cleanup
success. The skip allowlist is unchanged.

Actions artifacts retain raw logs, command exits/counts, OS/Go/PowerShell versions,
source hashes, collector revision and per-file SHA-256 hashes, including on failure.
The coordinator must download and independently audit them before changing T26's
external handoff result. A green workflow supports Windows verification only;
provider live tasks and G06 release readiness remain separate.
