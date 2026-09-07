# T23 fixture coverage findings

Reviewed base: `2421fe470cf67b22697fef796b038c0be6e395c8` (G04 before R017).
These are missing validation evidence/fixtures, not observed live failures.
No paid/live command or credential read was performed. R017 is coordinated in
external `decisions/R017-live-fixture-coverage.md`; only root updates that state.

| ID | Requirement | Existing evidence and missing assertion | Minimal owner correction |
|---|---|---|---|
| T23-F01 | W02-R08, related W02-R04/R05; T28 | `codebuddy/alignment_live_test.go:TestAlignmentLiveCodeBuddyNativeAppendAndResume` keeps one Agent; `run_live_test.go` resumes in one Agent. Neither proves Dedicated + WithTools files survive Close. | T15: one isolated Dedicated source/store/workspace/identity; A calls tool and stores unpredictable nonce in conversation, successful Close, B same configuration resumes only same key and recalls nonce without reinjecting it; verify healthy checkpoint and owned credential rotation. |
| T23-F02 | W02-R08, related W02-R04/R05; T29 | `codex/alignment_live_test.go:TestAlignmentLiveObservationAndPersistent` uses WithTools but not a second Agent with Dedicated profile. Append SPI resume is not this lifecycle. | T16: same A.Close → B ResumeOnly hosted Dedicated nonce fixture; both use the same Thread store, verify provider files retained and old gateway credentials revoked. |
| T23-F03 | W02-R08, related W02-R04/R05; T30 | `cursor/alignment_live_test.go:TestAlignmentLiveCursorPrintResume` calls one configured Driver directly; `TestAlignmentLiveCursorCapabilities` has no cold Thread resume. | T17: public Agent/Thread Dedicated + WithTools cold nonce fixture, matching the same ownership/Close/ResumeOnly conditions. Cursor remains spawn-per-turn. |
| T23-F04 | W09-R06; T27 | `claude/alignment_live_test.go:TestAlignmentLiveToolsTodosAndNestedParent` checks MCP completion, actual Todo ID and parent ToolCall/ToolResult; it does not materialize/require Skill or canonical Subagent Capability. Parent tool evidence is not a catalog-resolved Subagent invocation. | T14: isolated Skill and SubAgent catalog, require formal provider Skill activation and Subagent started/completed canonical keys in the same observed Event stream; absence fails. |
| T23-F05 | W09-R07; T28 | `codebuddy/alignment_live_test.go:TestAlignmentLiveCodeBuddyCapabilityAndTodoResults` checks MCP/Todo only although Streaming declares Skill/Subagents. | T15: isolated Skill and SubAgent resources and required formal Skill/Subagent lifecycle with canonical catalog identity; absence fails. |

The existing Claude cold fixture is `TestAlignmentLiveDedicatedToolResumeAfterClose`.
All corrections keep build-tag/environment gates and isolate credentials. Missing CLI,
authentication, quota or required provider evidence cannot become a passing skip.
T23 will inventory the accepted replacement G04 fixtures before final handoff.

Native Windows already has ownership DACL/reparse/long-held no-delete-sharing,
process-tree cancellation and `.cmd` launch tests. The previous PowerShell check in
`internal/clihelper/clihelper_test.go` only inspected prepared arguments; T23 adds
native executable and PowerShell actual Unicode/newline/quote argv round trips in
`adaptertest/argv_windows_test.go`. They are Windows-only and remain unexecuted on
this Darwin worker. Native T26 must run them; cross-compilation is compile evidence only.

## T23-F06: provider subtree guard coverage

Root review identified a guard blind spot at
`45111c97b4087647b6cdaddfdc27d4a70defd158`: only Codex child packages were
recognized as provider implementation imports. The independent oracle overlays
that exact tracked source and adds 60 cases without changing the old predicate.
Eighteen child/deep-child cases fail across Driver, bridge and hosttool importers;
the run records 42 passing cases, 18 failing cases plus the failing parent, and
zero skips. Evidence: `evidence/F06/{old.log,old.json,overlay.json,old-guard-with-oracle.go.txt}`.

The scoped repair recognizes all four exact provider roots and each root plus
`/` descendants. The committed oracle covers every root, child and nested child
for all three importer classes, and allows each `providerish` root/child to
prove prefix matching does not overreach. No runtime import violation was
observed and no production/public API or golden changes are needed. This is
an AGENTS dependency-boundary guard fix; final new-G04 acceptance remains pending.
