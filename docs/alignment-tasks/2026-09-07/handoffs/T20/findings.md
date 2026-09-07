# T20 findings and fixture development record

No production-contract violation has been confirmed during T20 prevalidation. Final required checks are recorded separately after the source commit and accepted G04 migration.

Original precheck logs are retained externally in `/private/tmp/agent-adaptor-alignment-20260907/t20-precheck/`. They are development evidence, not final SHA validation:

- Initial compiler errors were fixture API spelling/signature mistakes, corrected against public declarations.
- The first full run encountered sandbox-denied loopback listeners; tests were rerun with authorized loopback access. The denial is not counted as a pass.
- Claude emitted a fabricated extra semantic frame after its formal terminal; the fixture was corrected to emit a raw whitespace suffix after EOF, leaving official terminal ordering intact. Claude's documented missing-terminal Text is empty; the fixture now checks partial assistant text in Transcript and Raw instead of inventing final Text.
- CodeBuddy partial usage now uses its formal message-start counters, and its batch fixture reads the positional prompt. Codex token-usage JSON now includes the required formal counters. These were invalid/incomplete fixture protocol assumptions, not demonstrated product failures.
- Removing an SDK-created empty MCP config on Close is valid. A user-modified foreign entry must be preserved, so Close retry now uses a real directory-at-file-path IO error. Mode drift seeds an actual persistent external config before changing its real mode.
- Failed-handshake launch counts distinguish a delivered turn from empty prewarm. A Claude child dying before reading does not prove the parent's pipe write failed. The final fixture reads and records one actual prompt byte before exiting, deterministically proving partial delivery and requiring no replay.

Required assertions were not skipped or changed to make a confirmed product failure pass. Public contracts and official fixture shapes explain each development correction. R020 adds independent post-unlock fault evidence using existing private seams; the T04 owner test is not counted. No native Windows or paid provider evidence is claimed.

## Independent C04 review corrections

The external review of `4be1f09dbed3551387b71cf183aadd311bdcdeec` established QA oracle gaps, not production failures: intentionally wrong approval correlation and zero Transcript/Services elements still passed six selected tests. The original reviewer files/logs remain immutable under external `findings/T20-review/4be1f09dbed3551387b71cf183aadd311bdcdeec`; follow-up evidence reapplies the same mutations to the tightened tests and requires rejection.

QA-01 adds complete independent approval wire equality and public Kind checks. QA-02 adds complete literal Transcript/Services/Raw/metadata expectations in addition to Run/Stream equality. QA-03 adds real no-schema Permission Run/Stream plus nil legacy explicit/unset Ask matrix and actual DecisionCapableSink roundtrips. QA-04 authenticates with the original real private token before Close and tests that same token afterward; timeout cannot pass. QA-05 adds independent watchdogs and once-protected failure-path barrier release. QA-06/R020 adds independent lock-close/root-close injection with an active successor and byte/third-contender oracles.

The first added legacy ordinary-policy rejection precheck expected the broad policy-capability sentinel; the published `HumanDecisionModeUnsupportedError` has its own `ErrHumanDecisionModeUnsupported` identity. The fixture expectation was corrected to that declared identity; original precheck output is retained in `evidence/review-precheck/e2e.jsonl`. This did not change a production assertion or relax acceptance to any error. Final accepted-base validation is recorded separately in result.json and evidence; phase logs are retained only as historical development/sensitivity evidence.
