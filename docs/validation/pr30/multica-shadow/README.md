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
- Actual UI/receipt fixture corrections: `0250bea3da997fd47fb71549231ae67812229871`.
- Cancelled-task artifact collection: `25c0ff1b27702f0b5f25b62d67bb0f56b8452699`.
- [Backend patch](0001-feat-agent-add-opt-in-Codex-SDK-shadow-backend.patch), SHA256 `5c83623f9a05a105e33c58a518a386508675783743f75fd0567ef39c092b5ce2`.
- [E2E patch](0002-test-e2e-verify-real-Codex-SDK-shadow-routes-and-hos.patch), SHA256 `e27233495f9ce68ded014c4891dea1533ff390042865f1c4558962cf3e79addc`.
- [Live-discovered fixes](0003-fix-agent-bind-shadow-readiness-and-audits-to-real-p.patch), SHA256 `11417ff24939b7d26856030978be4f3e62d9badac5ddfc50da3d3de24224442b`.
- [Queued-product assertions](0004-test-e2e-require-original-queued-guidance-and-actual.patch), SHA256 `5bd9f8ab3e71432203cebd673e41706d37ed48204bc0568663c8bbe95a298351`.
- [UI/receipt fixture corrections](0005-fix-e2e-bind-shadow-receipts-and-restart-controls-to.patch), SHA256 `c106f1ae6d7ff409e5499354d203eb5bde4b4ece4be0d1c9921043c34e917504`.
- [Cancelled-task collection](0006-fix-e2e-collect-cancelled-task-artifacts-from-observ.patch), SHA256 `13c50396b579b56be5640aee6ec0698750738c6a2d9636c8f8771e9460c21664`.

The patches contain real daemon integration and offline regressions. They keep
the native backend as default and selects the SDK only with
`MULTICA_CODEX_BACKEND=agent-adaptor`. No host changes have been pushed upstream.
The real model acceptance is tracked separately from construction and fixture
tests; the patch alone does not establish a passed application run.

To review in another isolated Multica checkout, apply all six patches in order with `git am`,
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

The [acceptance manifest](acceptance.json) accepts four actual application
scenarios by explicitly composing passed individual cases from two executions.
The [independent physical review](independent-review.md) checked real files,
receipts, raw daemon lines, task identities, screenshots and binary/source hashes.

| Scenario | Accepted evidence |
|---|---|
| Development, explicit @ mention and actual result UI | [run03 case 1](shadow-run03-failed/report.json) |
| Current comment, original author, delivered/Read receipt, strict retry dedup, cross-issue/unknown-target rejection, AfterDone queue and exact files | [run03 case 2](shadow-run03-failed/report.json) |
| Stop and start over, cancelled partial output, exact old/new files, SDK cleanup before the fresh run | [targeted run04 case 3](shadow-run04-targeted/report.json) |
| Chat Steer, priority before ordinary, withdrawal without execution and independent-session isolation | [run03 case 4](shadow-run03-failed/report.json) |

This is **not a single full-suite green run**. Original run03 remains
`complete=true/passed=false`: its restart behavior succeeded, but the cancelled
task API omitted the artifact workspace and the collector correctly failed.
MC-013 added a cancelled-only fallback to the unique formal daemon startup
record for that exact task, preserving its original line/path/task association
and all exact file checks. Targeted run04 then passed that whole case with exit
zero. Its report retains `expectedCases=4` and `complete=false/passed=false`
because only one case was selected. The manifest names both sources explicitly;
the other three already passed cases did not require another model execution.

Both use actual daemon binary SHA256
`65f27dc7094ac36491453a6fac2c7ce02eca203eeabf63871c01953b6b184c34`, SDK build
`3950473` and host production `4f4a0b285`. E2E source is `0250bea3d` for run03
and `25c0ff1b2` for targeted run04. All ten frozen source hashes match their
respective commits. Forty-one offline regressions, TypeScript and independent
review passed, including actual Chromium fixture checks and negative evidence
tests. All six review patches reproduce the final local Multica source tree.

The [native comparison](native-run02-passed/report.json) passed its complete
development/UI case, six fixed arithmetic cases and three actual generated tests.
The other three cases were explicitly skipped as shadow-only. The [first native
failure](native-run01-failed/report.json), [first shadow failure](shadow-run01-failed/report.json),
[second shadow failure/interruption](shadow-run02-failed/report.json) and
[third full-run failure](shadow-run03-failed/report.json) remain unchanged.
Their read-only diagnostics never rewrite the original verdicts.

The [final environment snapshot](environment/run04-final-summary.json) passed
eleven checks: API/Web healthy; actual daemon source/hash/backend matched;
no active tasks, unfinished database tasks, pending supplements, terminal
reports or daemon child processes. The dedicated services remain running for
local inspection. API's self-reported build commit remains unknown.

This functional pilot does not establish statistical noninferiority, durable
daemon restart, native-session import, the complete mention grammar, every
plugin/provider or a production migration. Actual configuration drift used
an explicitly observed fresh provider conversation with bounded continuity;
it is not reported as full resume.
