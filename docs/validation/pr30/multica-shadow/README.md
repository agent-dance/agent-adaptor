# Actual local Multica shadow integration

Review marker: **便便**. This directory tracks the separate host experiment
requested for PR #30. It is not evidence of a production Multica rollout.

## Reviewable host changes

- Upstream base: [`multica-ai/multica@0d627e54e1bd4d2521359f293d04521246495ce2`](https://github.com/multica-ai/multica/tree/0d627e54e1bd4d2521359f293d04521246495ce2).
- Separate local branch: `codex/pr30-shadow`; the original Multica checkout is unchanged.
- Backend source commit: `dfdb8023ecfda28a30a2e3155ee09510c2f2180a`.
- E2E source commit: `1bab70fa1` (source and offline regressions; application acceptance is separate).
- [Backend patch](0001-feat-agent-add-opt-in-Codex-SDK-shadow-backend.patch), SHA256 `5c83623f9a05a105e33c58a518a386508675783743f75fd0567ef39c092b5ce2`.
- [E2E patch](0002-test-e2e-verify-real-Codex-SDK-shadow-routes-and-hos.patch), SHA256 `e27233495f9ce68ded014c4891dea1533ff390042865f1c4558962cf3e79addc`.

The patch contains real daemon integration and offline regressions. It keeps
the native backend as default and selects the SDK only with
`MULTICA_CODEX_BACKEND=agent-adaptor`. No host changes have been pushed upstream.
The real model acceptance is tracked separately from construction and fixture
tests; the patch alone does not establish a passed application run.

To review in another isolated Multica checkout, apply the patch with `git am`,
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

The first native task completed and passed the host's six fixed arithmetic
cases and three generated unittests. Its complete run remains **failed** because
the browser did not display the issue. Diagnosis reused the completed issue:
the managed localhost URLs loaded its actual data and title, while the
noncanonical 127.0.0.1 page stalled before API requests. The temporary launcher
has been corrected and a fresh full native run is pending (MC-005).

MC-001/004 passed twelve Windows and twelve WSL real-Python regressions and
independent review before execution. Actual UI/API results, independent host
check receipts, screenshots, and corresponding SDK receipt/cleanup log evidence
will be added here after execution. An online runtime or successful build is
not application acceptance.

This pilot does not prove durable daemon restart, native-session import, the
complete free-form mention parser, every plugin/provider, or a production
migration. Dynamic task configuration can require a disclosed fresh provider
conversation with bounded continuity; it must not be reported as full resume.
