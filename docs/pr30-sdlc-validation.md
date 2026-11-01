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
- [Multica source](https://github.com/multica-ai/multica/tree/0d627e54e1bd4d2521359f293d04521246495ce2): the reference used for product routing. On 2026-11-02 the user also requested actual local shadow integration. That work uses a separate checkout and managed isolated environment; it remains a separate acceptance surface from the SDK scenarios.

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
| SDLC-001 | Documentation defect | V2-M3 claimed process-kill recovery with no duplicate input, but issue-daemon step 10 reconstructs an Agent over the same in-memory store. A provider acceptance/receipt crash window can replay input. | Correct the proposal gate; explicitly retain process-kill/durable-store and actual Multica integration as unverified work. | Fixed in `67eb022`. No crash-recovery pass is claimed. |
| SDLC-002 | Coverage gap | Existing team-agent-workflow has sequential plan/implementation/review and no independent parallel verifier bound to a frozen candidate. | Add a dedicated real-model SDLC showcase with host gates and per-member evidence. | Implemented; Windows tests/vet and Linux race passed. Live run 02 is in progress; no live pass claimed yet. |
| SDLC-003 | Product mapping constraint | Multica's “Start after this run” is a new run after Done. Chat Steer/“Stop and start over” stop the active run before starting another. Core AfterTurn and Interrupt have different contracts. | Test these as explicit host routing operations; do not infer coverage from the similarly named SDK timing options. | Routing run 02 passed all 97 checks across six real Team runs. Independent review recomputed artifact hashes and checked receipts, task identities, cancellation, queue order, Raw/Transcript and usage. Actual Multica integration remains separate. |
| SDLC-004 | Acceptance harness defect found before paid execution | Checking only `go test`/`go vet` exit codes allows generated package initialization to call `os.Exit(0)` before the trusted tests run. Prompt instructions forbidding initialization are not enforcement. | Restrict source syntax/imports for this narrowly defined fixture and require the exact acceptance tests' run/pass events from `go test -json`; retain an adversarial early-exit regression. This is not a general sandbox for arbitrary code. | Fixed with adversarial regressions; Windows tests/vet and Linux race passed. No affected live pass was claimed. |
| SDLC-005 | Evidence omission defect found before paid execution | The gate initially checked only receipt IDs selected by the verifier. A failed check followed by a successful retry could be omitted from that list. | Permit each named check once per task/candidate and compare the supplied IDs with the host's complete receipt set. A failed attempt remains failed and requires a new candidate/review cycle. | Fixed with hidden-receipt/repeat-check regressions; component checks passed. |
| SDLC-006 | Host check cancellation defect found before paid execution | A deadline on `exec.CommandContext(go test ...)` alone can kill only the Go parent while a hung test child keeps the output pipe open. The check can outlive its deadline. | Bound the test binary itself and use process-tree cancellation plus bounded pipe waiting; preserve cancellation and command diagnostics. | Fixed using the shared process-tree helper, test timeout and bounded pipe wait; regression passed. |
| SDLC-007 | Failure artifact collection defect found before paid execution | A failed `Stream.Result()` returns a nil result and a `RunError` carrying the partial result. Saving only nonnil successful returns omits the leader's partial Raw/Transcript. JSON serialization of Done does not include those private audit fields. | Extract `RunError.Result` for artifact collection while preserving the original failure. Keep raw output and transcript in separate evidence files for cancellation and failure too. | Fixed with collector regression. Run 01 retained its failed leader partial result. |
| SDLC-008 | First real-run configuration rejection | Run 01 was rejected before any model dispatch: the host-defined `workflow_inspect` tool lacked a semantic revision required for named Thread compatibility. Compilation and Agent construction did not exercise that check. | Give every host tool an explicit stable semantic revision and add a contract regression covering the complete tool set. Keep run 01 as a failed attempt. | Fixed; all eight host tool types have a revision and a public Descriptor/schema regression. Run 01 remains failed; run 02 reached actual planner/engineer inference. |
| SDLC-009 | Routing acceptance harness defect | The restart checks accepted a file written by a worker without requiring that worker to succeed, and did not reject an extra task. A Team leader can succeed after a worker fails, so the outer Result alone does not prove the intended workflow. Cancellation also retained but did not assert the already observed worker Raw/Transcript. | Require the exact successful beta task and unchanged task set across both restart runs; reject unexpected failed tasks, and inspect cancelled worker partial audit. Add negative regressions for a written artifact followed by failure and for extra delegation. | Fixed with adversarial regressions. Run 01's green exit is retained as diagnostic evidence and is not accepted. Fresh run 02 passed 97 checks and independent artifact/receipt review. |
| SDLC-010 | Windows host-check environment defect observed live | SDLC run 02's real `go test` and `go vet` commands both failed before checking source: the restricted environment omitted `LOCALAPPDATA`, so Windows Go could not locate its build cache when `GOCACHE` was unset. | Retain this non-secret build setting in the host command environment and add a real host-vet regression with no explicit cache. Preserve both failed command receipts. | Run 02 logs retained. The new regression passed on Windows; the original live attempt is not accepted. |
| SDLC-011 | Real model protocol deviation and schema gap | Run 02's verifier used `R1-R8` as a finding ID. The native schema permitted that string while host validation correctly rejected it. After the gate error, the leader attempted replacement review/verifier tasks against the same round; the host rejected both. | Put the exact finding IDs in the native schema, including `HOST` for infrastructure findings, and explicitly instruct the leader to preserve current task IDs and stop after a host protocol rejection. Keep the strict host stage gate and zero-violation acceptance condition. | Schema/host regression passed. Run 02 remains failed with all three violations and interrupted task records. Its candidate independently passed test/vet afterward, which does not convert the failed workflow into success. Fresh run 03 uses the corrected environment/schema/prompt. |
| ENV-001 | Local prerequisite failure, outside the SDK | Docker Desktop could not start because a stale Windows AF_UNIX `dockerInference` runtime endpoint was inaccessible. The Multica checkout initially had no managed environment. | After Docker exited, preserve the transient runtime parent directories by renaming them and allow Docker to create fresh ones, following the [upstream reproduction/workaround](https://github.com/docker/desktop-feedback/issues/554). No reset, prune, VHDX changes or data deletion. Create Multica's separate environment with its managed scripts. | Docker Engine subsequently became available; Multica setup continues. This is not an SDK code defect. |

Leader context retains the SDK's documented bounded member notifications,
including schema-checked structured result text. It does not contain only a
custom summary. Full Raw/Transcript evidence remains separate. The validation
does not truncate TaskStore writes or change the SDK's notification semantics.

## Acceptance status

The expanded SDK host-routing cases passed: see the [curated run 02 evidence](validation/pr30/routing-run02.json).
It preserves all 97 check results, exact host inputs, task identities, file
contents/hashes, structured results, audit file hashes and Raw/Transcript metrics.
Full local audit files remain separate; a curated summary is not their replacement.
The model was configured as `gpt-6.1-sol` / `xhigh`. Incoming-only Raw cannot
independently attest each worker turn's effort; request construction and the
Codex configuration contract tests support that configuration statement.

SDLC run 03 and the first broad offline test command were externally interrupted
before completion; neither is counted as passed. Its existing report directory
and log are retained without inventing a final Result. Fresh SDLC run 04 is in
progress. After resuming, Windows `go test -count=1 -timeout=5m ./...` completed
successfully; both new showcase packages also passed Windows vet and Linux race.

The SDLC workflow and actual Multica shadow integration remain unaccepted until
their separate complete evidence is available. Failed attempts remain in this
ledger, and no SDK scenario establishes production Multica adoption.

No production Multica migration, external publication, merge, or release tag is
implied by this validation.
