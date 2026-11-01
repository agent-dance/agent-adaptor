# PR #30: real Codex SDLC and Multica scenario validation

This is the review ledger for the additional validation requested on 2026-11-01
(Asia/Shanghai). Changes by Codex carry the review marker **便便** in their commit
messages. Implementation, model observations, and host acceptance are recorded
separately. A model's success message is not acceptance evidence.

## Scope and sources

The preceding PR work passed real Codex schema/message tests, the fifteen-step
issue-daemon scenario (143 checks), and CI at commit `6b12b9a`. Those results do
not establish that a realistic SDLC team follows a prescribed workflow or that
the Multica application has adopted this SDK.

The additional workflow is a host-selected adaptation of these primary sources:

- [Anthropic: Harness design for long-running application development](https://www.anthropic.com/research/harness-design-long-running-apps): agree on testable work, exchange structured artifacts, and evaluate implementation in an independent context.
- [Anthropic: Building effective agents](https://www.anthropic.com/research/building-effective-agents): use programmatic stage gates, parallelize independent work, and bound evaluation/repair loops.
- [Claude Code best practices](https://code.claude.com/docs/en/best-practices): give agents executable verification and evidence; distinguish model instructions from enforceable checks.
- [Multica source](https://github.com/multica-ai/multica/tree/0d627e54e1bd4d2521359f293d04521246495ce2): the local read-only reference used for product routing. Actual server/daemon integration is a separate acceptance surface.

The intended DAG is:

```text
scope/plan -> implement -> [independent review || verification] -> host gate
                 ^                         |
                 +------- bounded repair --+
```

The leader coordinates. Distinct workers return schema-checked products. Review
and verification must use the same frozen content digest and actually overlap.
The host collects each task's result, raw protocol output, transcript, usage,
role/run identity and artifact hashes. Test commands run in the host executor;
model-supplied exit codes do not prove execution. A changed candidate invalidates
earlier acceptance. Only the host can accept the final candidate.

Live execution uses the local authenticated Codex CLI, model `gpt-6.1-sol`, and
reasoning effort `xhigh`. No credentials belong in committed reports. Prompts,
requirements, immutable candidate manifests, command receipts, and actual task
lifecycle events must make the run reviewable.

## Findings and fixes

| ID | Kind | Observation | Resolution | Evidence/status |
|---|---|---|---|---|
| SDLC-001 | Documentation defect | V2-M3 claimed process-kill recovery with no duplicate input, but issue-daemon step 10 reconstructs an Agent over the same in-memory store. A provider acceptance/receipt crash window can replay input. | Correct the proposal gate; explicitly retain process-kill/durable-store and actual Multica integration as unverified work. | Source review; documentation fix prepared. No crash-recovery pass is claimed. |
| SDLC-002 | Coverage gap | Existing team-agent-workflow has sequential plan/implementation/review and no independent parallel verifier bound to a frozen candidate. | Add a dedicated real-model SDLC showcase with host gates and per-member evidence. | Implementation in progress; no live pass claimed yet. |
| SDLC-003 | Product mapping constraint | Multica's “Start after this run” is a new run after Done. Chat Steer/“Stop and start over” stop the active run before starting another. Core AfterTurn and Interrupt have different contracts. | Test these as explicit host routing operations; do not infer coverage from the similarly named SDK timing options. | Coverage matrix and additional host scenarios in preparation. |

## Acceptance status

The additional SDLC team and expanded Multica routing cases have not yet been
accepted. The final update will record exact commands, model configuration,
candidate digests, observed concurrency, product-level limits, failures and
their regression/real-model retests. Failed attempts remain part of the ledger.

No production Multica migration, external publication, merge, or release tag is
implied by this validation.
