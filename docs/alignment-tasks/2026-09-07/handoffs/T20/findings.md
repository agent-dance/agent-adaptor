# T20 findings and fixture development record

No production-contract violation has been confirmed during T20 prevalidation. Final required checks are recorded separately after the source commit and accepted G04 migration.

Original precheck logs are retained externally in `/private/tmp/agent-adaptor-alignment-20260907/t20-precheck/`. They are development evidence, not final SHA validation:

- Initial compiler errors were fixture API spelling/signature mistakes, corrected against public declarations.
- The first full run encountered sandbox-denied loopback listeners; tests were rerun with authorized loopback access. The denial is not counted as a pass.
- Claude emitted a fabricated extra semantic frame after its formal terminal; the fixture was corrected to emit a raw whitespace suffix after EOF, leaving official terminal ordering intact. Claude's documented missing-terminal Text is empty; the fixture now checks partial assistant text in Transcript and Raw instead of inventing final Text.
- CodeBuddy partial usage now uses its formal message-start counters, and its batch fixture reads the positional prompt. Codex token-usage JSON now includes the required formal counters. These were invalid/incomplete fixture protocol assumptions, not demonstrated product failures.
- Removing an SDK-created empty MCP config on Close is valid. A user-modified foreign entry must be preserved, so Close retry now uses a real directory-at-file-path IO error. Mode drift seeds an actual persistent external config before changing its real mode.
- Failed-handshake launch counts distinguish a delivered turn from empty prewarm. A Claude child dying before reading does not prove the parent's pipe write failed. The final fixture reads and records one actual prompt byte before exiting, deterministically proving partial delivery and requiring no replay.

Required assertions were not skipped or changed to make a confirmed product failure pass. Public contracts and official fixture shapes explain each development correction. T20 references the accepted T04 post-unlock fault test only as prior owner evidence; no native Windows or paid provider evidence is claimed.
