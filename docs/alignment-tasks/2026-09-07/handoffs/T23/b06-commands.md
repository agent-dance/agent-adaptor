# B06 exact execution plan

All B06 reports must use the same G05 `implementation_head`. This document and
`b06-inventory.json` are entrypoint inventories, not Linux/Windows/live results.
Inventory baseline: accepted replacement G04 `b2035bc793369fb8fefb9de229ff1dbd2b748853`,
canonical22: 43 tasks, 96 requirements, at most six concurrent workers (including
the R019 T32 supplement). R017 fixture coverage gaps are closed at this baseline;
`findings.md` preserves the original gaps and their exact replacement roots.
Required scenarios must execute successfully; a skip cannot replace them. Run each task.json command below unchanged in scope/count.
Adding `-json -timeout=30m` is allowed; retain the command process exit code.

## Native platform and environment evidence

Record `git rev-parse HEAD`, `go version`, `go env GOOS GOARCH GOROOT GOTOOLCHAIN`,
and `uname -a` on Linux or `$PSVersionTable` plus the Windows OS version on Windows.
T25 requires actual GOOS=linux execution. T26 requires GOOS=windows execution;
cross-compilation and the darwin developer checks are never native evidence.

Use a task-private HOME/USERPROFILE/XDG_CONFIG_HOME and remove unrelated provider
root overrides; do not print all environment variables. Preserve actual GOROOT
and use GOTOOLCHAIN=local. Ordinary runs close all three gates:
`AGENT_ADAPTOR_LIVE_CONFORMANCE=0`, `AGENT_ADAPTOR_E2E=0`,
`AGENT_ADAPTOR_UPDATE_API_GOLDEN=0`. No credentials are needed.

## Exact task commands

| Check | Command |
|---|---|
| T25-V01 | `go test -count=1 ./...` |
| T25-V02 | `go vet ./...` |
| T25-V03 | `go test -race -count=1 ./...` |
| T25-V04 | `go test -count=20 . ./clients/a2a ./bridges/a2a ./hosttools/a2adelegation` |
| T25-V05 | `go test -count=1 . ./bridges/a2a ./hosttools/a2adelegation -run 'TestScenarioS[1-9]|TestServiceTeamCollaborationS9'` |
| T25-V06 | `go test -count=1 ./claude ./codebuddy ./codex ./cursor -run 'Test(Claude|CodeBuddy|Codex|Cursor)DriverConformance'` |
| T25-V07 | `go test ./internal/engine -run '^$' -fuzz '^FuzzExtractZip$' -fuzztime=30s` |
| T25-V08 | `go test ./internal/engine -run '^$' -fuzz '^FuzzExtractTar$' -fuzztime=30s` |
| T25-V09 | `go test ./internal/engine -run '^$' -fuzz '^FuzzSniffArchiveFormat$' -fuzztime=30s` |
| T25-V10 | `go test ./claude -run '^$' -fuzz '^FuzzClaudeBatchParser$' -fuzztime=30s` |
| T25-V11 | `go test ./cursor -run '^$' -fuzz '^FuzzCursorBatchParser$' -fuzztime=30s` |
| T25-V12 | `go test ./codebuddy -run '^$' -fuzz '^FuzzCodeBuddyBatchParser$' -fuzztime=30s` |
| T25-V13 | `go test ./codex -run '^$' -fuzz '^FuzzCodexBatchParser$' -fuzztime=30s` |
| T25-V14 | `go test ./codex/appserver -run '^$' -fuzz '^FuzzCodexAppserverNotificationDecoder$' -fuzztime=30s` |
| T25-V15 | `go test ./codex/appserver -run '^$' -fuzz '^FuzzDecodeThreadItem$' -fuzztime=30s` |
| T26-V01 | `go test -count=1 ./...` |
| T27-V01 | `go test -count=1 -tags=claude_live ./claude -run TestAlignmentLive` |
| T27-V02 | `go test -count=1 -tags=claude_live ./claude -run TestClaudeDriverConformance` |
| T28-V01 | `go test -count=1 -tags=codebuddy_live ./codebuddy -run TestAlignmentLive` |
| T28-V02 | `go test -count=1 -tags=codebuddy_live ./codebuddy -run TestCodeBuddyDriverConformance` |
| T29-V01 | `go test -count=1 -tags=codex_live ./codex/... -run TestAlignmentLive` |
| T29-V02 | `go test -count=1 -tags=codex_live ./codex -run TestCodexDriverConformance` |
| T30-V01 | `go test -count=1 -tags=cursor_live ./cursor -run TestAlignmentLive` |
| T30-V02 | `go test -count=1 -tags=cursor_live ./cursor -run TestCursorDriverConformance` |

T25 fuzz commands each run for 30 seconds and must show actual seed/sample
execution. Their `-run ^$` deliberately excludes ordinary tests and is not a
zero-execution success rule. Record target, duration and samples separately.

T25/T26 also execute (not merely compile) `go run ./examples/threads/codec` and
`go test -count=1 ./examples/...`; T24 may supply an additional runnable fake-driver
example at G05. Never execute the live quickstart or smoke runner as a fake example.

Native Windows mandatory named roots (all in the full T26 suite):

- `internal/hostedprofile`: TestWindowsPrivateDACLAndTamperedDACL, TestWindowsReparseProfileRejected, TestWindowsOwnershipLockForbidsRenameAndDelete.
- `internal/processx`: TestPrepareCommandLaunchesBatchShim, TestConfigureCancellationTerminatesWindowsProcessTree.
- `adaptertest`: TestAlignmentWindowsNativeArgvRoundTrip, TestAlignmentWindowsPowerShellArgvRoundTrip.
- Full `internal/hostedprofile`, root, provider and e2e packages supply cross-process ownership, Close/retry, persistent/WithSpawn and checkpoint tests. Do not limit T26-V01 to the named smoke roots.

## Paid suites: independent gates and required scenarios

Only the authorized B06 runner enables the provider build tag and
`AGENT_ADAPTOR_LIVE_CONFORMANCE=1` together, keeping E2E/golden gates at zero.
The existence of this plan is not live authorization. Record each CLI `--version`
and supported transport, with only safe version/help output. Missing CLI, auth,
quota or protocol evidence blocks/fails the required probe. Test listing is
read-only selection evidence and cannot replace the command above.

Provider profile setup (never use the operator profile implicitly):

- Claude: private HOME and CLAUDE_CONFIG_DIR generated by the fixture; explicit runner auth environment only.
- CodeBuddy: absolute CODEBUDDY_CONFIG_DIR_SOURCE supplied as an authorized isolated authentication fixture; helper copies only allowed login files into another private config directory.
- Codex: AGENT_ADAPTOR_CODEX_LIVE_PROFILE must name an explicitly authorized isolated auth seed; only auth.json is copied into private CODEX_HOME, with a private workspace. Optional AGENT_ADAPTOR_CODEX_LIVE_MODEL selects the model.
- Cursor: isolated provider config with explicit runner API key; print transport only, Skills/Todos/append/persistence remain unsupported.

Before execution, `go test -list TestAlignmentLive -tags=<provider>_live ./<provider>`
must contain every expected root below (with live environment gate closed).
After execution, validate `go test -json` using `check_go_json.py --require
github.com/agent-dance/agent-adaptor/<provider>:<TestName>` for each root.
All required live roots/subtests must pass. A normal non-live conformance skip
is allowed only as explicitly disabled evidence; it does not satisfy T27–T30.

### claude

- `TestAlignmentLiveAppendAndThread` (claude/alignment_live_test.go)
- `TestAlignmentLiveCancellationPreservesPartialResult` (claude/alignment_live_test.go)
- `TestAlignmentLiveDeclaredSkillAndSubagentCompleted` (claude/alignment_catalog_live_test.go)
- `TestAlignmentLiveDedicatedToolResumeAfterClose` (claude/alignment_live_test.go)
- `TestAlignmentLiveExistingStreamingAndApproval` (claude/alignment_live_test.go)
- `TestAlignmentLiveNativeSchemaHITL` (claude/alignment_live_test.go)
- `TestAlignmentLiveToolsTodosAndNestedParent` (claude/alignment_live_test.go)

### codebuddy

- `TestAlignmentLiveCodeBuddyCapabilityAndTodoResults` (codebuddy/alignment_live_test.go)
- `TestAlignmentLiveCodeBuddyDedicatedToolsResumeAfterClose` (codebuddy/alignment_live_resume_test.go)
- `TestAlignmentLiveCodeBuddyHeadlessStreaming` (codebuddy/run_live_test.go)
- `TestAlignmentLiveCodeBuddyNativeAppendAndResume` (codebuddy/alignment_live_test.go)
- `TestAlignmentLiveCodeBuddyPermissionApprove` (codebuddy/run_live_test.go)
- `TestAlignmentLiveCodeBuddyPermissionReject` (codebuddy/run_live_test.go)
- `TestAlignmentLiveCodeBuddyPersistentReuse` (codebuddy/run_live_test.go)
- `TestAlignmentLiveCodeBuddyPlanApprove` (codebuddy/run_live_test.go)
- `TestAlignmentLiveCodeBuddyPlanReject` (codebuddy/run_live_test.go)
- `TestAlignmentLiveCodeBuddyQuestionAnswered` (codebuddy/run_live_test.go)
- `TestAlignmentLiveCodeBuddyThreadResume` (codebuddy/run_live_test.go)

### codex

- `TestAlignmentLiveCancellationAndAppendRebind` (codex/alignment_live_test.go)
- `TestAlignmentLiveDedicatedToolResumeAfterClose` (codex/alignment_cold_live_test.go)
- `TestAlignmentLiveNativeAppend` (codex/alignment_live_test.go)
- `TestAlignmentLiveObservationAndPersistent` (codex/alignment_live_test.go)
- `TestAlignmentLiveSubagentCatalog` (codex/alignment_live_test.go)

### cursor

- `TestAlignmentLiveCursorCancelPartial` (cursor/alignment_live_test.go)
- `TestAlignmentLiveCursorCapabilities` (cursor/alignment_live_test.go)
- `TestAlignmentLiveCursorDedicatedToolsColdResume` (cursor/alignment_live_cold_resume_test.go)
- `TestAlignmentLiveCursorPrintResume` (cursor/alignment_live_test.go)
- `TestAlignmentLiveCursorUnsupportedAppend` (cursor/alignment_live_test.go)

Required scenario coverage is source-reviewed separately from names: native append
nonce/control and resume/WithSpawn (Codex exec plus app-server start/resume/fork),
formal provider observations and full/clear Todo where declared, partial cancellation
with unchanged healthy checkpoint, explicit Dedicated hosted-tool cross-Agent nonce
resume, and Claude native schema plus actual Question/PlanReview approval roundtrip.
Cursor uses print batch/stream resume and explicit unsupported checks. The accepted
R017 additions above supply CodeBuddy/Codex/Cursor cold fixtures and Claude/CodeBuddy
formal catalog completion facts. This is source/entry coverage; all 28 real-provider
roots (Claude 7, CodeBuddy 11, Codex 5, Cursor 5) still require authorized B06 execution.
Codex also selects `TestAlignmentLiveGateCanary` and
`TestAlignmentLiveGateDisabledWithoutBuildTag` in `codex/live_profile_test.go`;
these two hermetic gate tests never count as real-provider scenario successes.

S1–S9 are selected by T25-V05, all four conformance roots by T25-V06; each root
must have an actual pass record. BDD syntax remains a non-live execution:
`go test -count=1 -tags=e2e ./e2e -run TestPersistentProcessBDDFeaturesParse`.
Its successful parsing of all 15 features is not actual provider BDD execution.
Real BDD requires its own `e2e` build tag and `AGENT_ADAPTOR_E2E=1`; T23 does not
authorize or execute that suite.
