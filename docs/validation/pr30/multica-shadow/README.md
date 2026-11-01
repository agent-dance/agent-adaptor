# Actual local Multica shadow integration

Review marker: **便便**. This directory tracks the separate host experiment
requested for PR #30. It is not evidence of a production Multica rollout.

## Reviewable host changes

- Upstream base: [`multica-ai/multica@0d627e54e1bd4d2521359f293d04521246495ce2`](https://github.com/multica-ai/multica/tree/0d627e54e1bd4d2521359f293d04521246495ce2).
- Separate local branch: `codex/pr30-shadow`; the original Multica checkout is unchanged.
- Backend source commit: `dfdb8023ecfda28a30a2e3155ee09510c2f2180a`.
- E2E source commit: `1bab70fa1` (source and offline regressions; application acceptance is separate).
- Live-discovered integration/log fix: `4f4a0b285fe93782a594e92d4dd612bb7ae1807a`.
- Final queued-product assertions: `61daadbae4cdefb03890dbe69f311b741e83e71c`.
- [Backend patch](0001-feat-agent-add-opt-in-Codex-SDK-shadow-backend.patch), SHA256 `5c83623f9a05a105e33c58a518a386508675783743f75fd0567ef39c092b5ce2`.
- [E2E patch](0002-test-e2e-verify-real-Codex-SDK-shadow-routes-and-hos.patch), SHA256 `e27233495f9ce68ded014c4891dea1533ff390042865f1c4558962cf3e79addc`.
- [Live-discovered fixes](0003-fix-agent-bind-shadow-readiness-and-audits-to-real-p.patch), SHA256 `11417ff24939b7d26856030978be4f3e62d9badac5ddfc50da3d3de24224442b`.
- [Queued-product assertions](0004-test-e2e-require-original-queued-guidance-and-actual.patch), SHA256 `5bd9f8ab3e71432203cebd673e41706d37ed48204bc0568663c8bbe95a298351`.

The patch contains real daemon integration and offline regressions. It keeps
the native backend as default and selects the SDK only with
`MULTICA_CODEX_BACKEND=agent-adaptor`. No host changes have been pushed upstream.
The real model acceptance is tracked separately from construction and fixture
tests; the patch alone does not establish a passed application run.

To review in another isolated Multica checkout, apply all four patches in order with `git am`,
then use an external Go workspace that replaces
`github.com/agent-dance/agent-adaptor/v2` with this PR's SDK checkout. See the
included `docs/engineering/agent-adaptor-shadow.md`. Its `v2.0.0` requirement is
an unreleased local workspace identity, not a published dependency claim.
Never copy the machine-specific workspace or an authenticated Codex profile
into the repositories.

## Actual environment

The dedicated managed environment is `pr30-sdk-shadow`: API port 19014, Web
port 13934, isolated database `multica_multica_934`, workspace `pr30-shadow`.
The official managed up/status commands passed. Its account and agent profiles
are dedicated to this run; credential contents stay outside Git and reports.

The daemon uses Codex CLI 0.166.0, configured model `gpt-6.1-sol` and effort
`xhigh`, with an eight-minute total limit, three-minute idle budget and one
active task. Native and SDK phases use separate profile homes and runtime IDs.
Record the actual binary hash and SDK source revision per phase: an upstream
build label alone cannot identify the local patch or workspace dependency.

Local prerequisite failures and their repairs are recorded as ENV-001/002 in
the [validation ledger](../../../pr30-sdlc-validation.md). Docker data was not
reset or pruned, and the managed process identity checks remain intact.

## Acceptance status

The [fresh native comparison](native-run02-passed/report.json) passed the full
development/UI case and independent audit: six fixed arithmetic cases and three
generated tests actually ran successfully, all source/log hashes matched, and
the screenshot shows the actual task result. The other three cases are
shadow-only and explicitly skipped in this native phase.

The [first native run](native-run01-failed/report.json) remains failed for
MC-005. Diagnosis reused its completed issue without another model call; the
managed localhost URLs loaded actual data and title, while the noncanonical
127.0.0.1 page stalled before API requests. The temporary launcher was corrected.

The [first shadow run](shadow-run01-failed/report.json) remains incomplete and
failed. MC-006/007 exposed the formal tint-log parsing gap and the incorrect
assumption about public RunStarted coordinates. Its completed development task,
read-only diagnostic UI/host recheck, and actual cancellation/cleanup of the
interrupted second task are preserved separately. Neither diagnostic converts
the original verdict to success.

The fixes passed targeted Windows/WSL tests, independent log-parser review and
twenty Linux race repetitions through the real public SDK app-server pipeline
with an offline protocol peer. A rebuilt daemon runs the four-case
acceptance, using SDK `3950473`, production source `4f4a0b285`, E2E
`61daadbae` and actual binary SHA256
`65f27dc7094ac36491453a6fac2c7ce02eca203eeabf63871c01953b6b184c34`.
Run 02's development/UI case passed. Its current-comment case then exposed
MC-009: the helper polled comments instead of the UI timeline for hydrated
receipts. A separate read-only timeline query confirmed the actual delivered
receipt, matching the SDK acknowledgement. Run 02 remains failed while the
remaining cases complete; corrected full acceptance is still required.

MC-001/004 passed twelve Windows and twelve WSL real-Python regressions and
independent review before execution. Actual UI/API results, independent host
check receipts, screenshots, and corresponding SDK receipt/cleanup log evidence
will be added here after execution. An online runtime or successful build is
not application acceptance.

This pilot does not prove durable daemon restart, native-session import, the
complete free-form mention parser, every plugin/provider, or a production
migration. Dynamic task configuration can require a disclosed fresh provider
conversation with bounded continuity; it must not be reported as full resume.
